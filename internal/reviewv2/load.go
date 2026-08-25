package reviewv2

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"reflect"
	"sort"

	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

type Version string

const (
	VersionLegacy Version = "legacy"
	VersionV2     Version = "v2"
	VersionMixed  Version = "mixed"
	VersionEmpty  Version = "empty"
)

// ErrMigrationRequired is returned only after the legacy ledger parses
// successfully. Callers can distinguish migration from malformed state.
type ErrMigrationRequired struct {
	ProjectRoot string
}

func (err *ErrMigrationRequired) Error() string {
	if err == nil {
		return "review ledger migration required"
	}
	return fmt.Sprintf("review ledger migration required for %q; run sync --dry-run followed by sync", err.ProjectRoot)
}

type Accepted struct {
	State    State
	Legacy   ledger.State
	Snapshot ledger.SnapshotUsage

	projectRoot string
	projectInfo os.FileInfo
	files       map[string]acceptedFile
	reviewDoc   ReviewDocument
	historyDoc  HistoryDocument
	v2          bool
}

type acceptedFile struct {
	body []byte
	perm fs.FileMode
}

type loadHooks struct {
	afterFilesRead  func() error
	afterLegacyLoad func() error
}

func DetectVersion(projectRoot string) (Version, error) {
	directory, err := openReviewRoot(projectRoot, nil)
	if err != nil {
		return "", err
	}
	defer directory.Close()
	return detectVersionFromDirectory(directory)
}

func DetectVersionExpected(projectRoot string, expectedRoot os.FileInfo) (Version, error) {
	directory, err := openReviewRoot(projectRoot, expectedRoot)
	if err != nil {
		return "", err
	}
	defer directory.Close()
	return detectVersionFromDirectory(directory)
}

func Load(projectRoot string) (Accepted, error) {
	return loadAccepted(projectRoot, nil, false)
}

func LoadExpected(projectRoot string, expectedRoot os.FileInfo) (Accepted, error) {
	return loadAccepted(projectRoot, expectedRoot, false)
}

func LoadAnyReadOnly(projectRoot string) (Accepted, error) {
	return loadAccepted(projectRoot, nil, true)
}

func LoadAnyReadOnlyExpected(projectRoot string, expectedRoot os.FileInfo) (Accepted, error) {
	return loadAccepted(projectRoot, expectedRoot, true)
}

func SnapshotUsageExpected(projectRoot string, expectedRoot os.FileInfo) (ledger.SnapshotUsage, error) {
	accepted, err := LoadExpected(projectRoot, expectedRoot)
	if err != nil {
		return ledger.SnapshotUsage{}, err
	}
	return cloneSnapshotUsage(accepted.Snapshot), nil
}

func loadAccepted(projectRoot string, expectedRoot os.FileInfo, allowLegacy bool) (Accepted, error) {
	return loadAcceptedWithHooks(projectRoot, expectedRoot, allowLegacy, loadHooks{})
}

func loadAcceptedWithHooks(projectRoot string, expectedRoot os.FileInfo, allowLegacy bool, hooks loadHooks) (Accepted, error) {
	directory, err := openReviewRoot(projectRoot, expectedRoot)
	if err != nil {
		return Accepted{}, err
	}
	defer directory.Close()
	version, err := detectVersionFromDirectory(directory)
	if err != nil {
		return Accepted{}, err
	}
	switch version {
	case VersionEmpty:
		return Accepted{}, errors.New("review ledger is empty")
	case VersionMixed:
		return Accepted{}, errors.New("mixed legacy and v2 review ledger state")
	case VersionLegacy:
		legacy, loadErr := ledger.LoadExpected(projectRoot, directory.Info())
		if loadErr != nil {
			return Accepted{}, loadErr
		}
		if !allowLegacy {
			return Accepted{}, &ErrMigrationRequired{ProjectRoot: projectRoot}
		}
		if hooks.afterLegacyLoad != nil {
			if err := hooks.afterLegacyLoad(); err != nil {
				return Accepted{}, err
			}
		}
		snapshot, snapshotErr := ledger.SnapshotUsageExpected(projectRoot, directory.Info())
		if snapshotErr != nil {
			return Accepted{}, snapshotErr
		}
		if !reflect.DeepEqual(ledger.SnapshotFiles(legacy), snapshot.Files) {
			return Accepted{}, errors.New("legacy ledger changed while loading")
		}
		reloaded, reloadErr := ledger.LoadExpected(projectRoot, directory.Info())
		if reloadErr != nil {
			return Accepted{}, errors.New("legacy ledger changed while loading")
		}
		finalSnapshot, finalSnapshotErr := ledger.SnapshotUsageExpected(projectRoot, directory.Info())
		if finalSnapshotErr != nil || !reflect.DeepEqual(snapshot, finalSnapshot) ||
			!reflect.DeepEqual(ledger.SnapshotFiles(reloaded), finalSnapshot.Files) {
			return Accepted{}, errors.New("legacy ledger changed while loading")
		}
		projected, _ := ProjectLegacy(legacy)
		return Accepted{
			State:       projected,
			Legacy:      legacy,
			Snapshot:    snapshot,
			projectRoot: projectRoot,
			projectInfo: directory.Info(),
		}, nil
	case VersionV2:
		return loadV2FromDirectory(projectRoot, directory, hooks)
	default:
		return Accepted{}, fmt.Errorf("unsupported review ledger version %q", version)
	}
}

func loadV2FromDirectory(projectRoot string, directory *pathguard.Directory, hooks loadHooks) (Accepted, error) {
	files := make(map[string]acceptedFile, 3)
	read := func(relative string, maximum int64) ([]byte, error) {
		body, perm, err := readStableReviewFile(directory, relative, maximum)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", relative, err)
		}
		files[relative] = acceptedFile{body: append([]byte(nil), body...), perm: perm}
		return body, nil
	}
	reviewBody, err := read(ReviewRelativePath, MaxDocumentBytes)
	if err != nil {
		return Accepted{}, err
	}
	historyBody, err := read(HistoryRelativePath, MaxDocumentBytes)
	if err != nil {
		return Accepted{}, err
	}
	machineBody, err := read(MachineLedgerRelativePath, MaxMachineLedgerBytes)
	if err != nil {
		return Accepted{}, err
	}
	if hooks.afterFilesRead != nil {
		if err := hooks.afterFilesRead(); err != nil {
			return Accepted{}, err
		}
	}
	reviewDocument, err := ParseReview(reviewBody)
	if err != nil {
		return Accepted{}, err
	}
	historyDocument, err := ParseHistory(historyBody)
	if err != nil {
		return Accepted{}, err
	}
	machine, err := ParseMachineLedger(machineBody)
	if err != nil {
		return Accepted{}, err
	}
	state := State{Review: reviewDocument.Model, Events: historyDocument.Events, Machine: machine}
	if historyDocument.ProjectID != state.Review.ProjectID || historyDocument.Revision != state.Review.Revision {
		return Accepted{}, errors.New("review and history document identities do not match")
	}
	if state.Machine.ReviewSHA256 != sha256Hex(reviewBody) || state.Machine.HistorySHA256 != sha256Hex(historyBody) {
		return Accepted{}, errors.New("accepted Markdown hash does not match machine ledger")
	}
	if err := validateStateProjectAccounting(state); err != nil {
		return Accepted{}, err
	}
	if err := Validate(state); err != nil {
		return Accepted{}, err
	}
	legacy, err := LegacyState(state)
	if err != nil {
		return Accepted{}, err
	}
	snapshot := ledger.SnapshotUsage{Files: make([]ledger.SnapshotFile, 0, 3)}
	for _, relative := range []string{HistoryRelativePath, MachineLedgerRelativePath, ReviewRelativePath} {
		file := files[relative]
		digest := sha256.Sum256(file.body)
		snapshot.Files = append(snapshot.Files, ledger.SnapshotFile{
			RelativePath: relative,
			SHA256:       fmt.Sprintf("sha256:%x", digest),
			Perm:         file.perm.Perm(),
			Size:         int64(len(file.body)),
		})
	}
	if err := revalidateLoadedFiles(directory, files); err != nil {
		return Accepted{}, err
	}
	return Accepted{
		State:       state,
		Legacy:      legacy,
		Snapshot:    snapshot,
		projectRoot: projectRoot,
		projectInfo: directory.Info(),
		files:       files,
		reviewDoc:   reviewDocument,
		historyDoc:  historyDocument,
		v2:          true,
	}, nil
}

func revalidateLoadedFiles(directory *pathguard.Directory, files map[string]acceptedFile) error {
	for _, relative := range []string{HistoryRelativePath, MachineLedgerRelativePath, ReviewRelativePath} {
		previous, exists := files[relative]
		if !exists {
			return fmt.Errorf("accepted file %s is missing from the loaded snapshot", relative)
		}
		maximum := int64(MaxDocumentBytes)
		if relative == MachineLedgerRelativePath {
			maximum = MaxMachineLedgerBytes
		}
		body, perm, err := readStableReviewFile(directory, relative, maximum)
		if err != nil || !bytes.Equal(body, previous.body) || perm.Perm() != previous.perm.Perm() {
			return fmt.Errorf("accepted file %s changed while loading", relative)
		}
	}
	return nil
}

func detectVersionFromDirectory(directory *pathguard.Directory) (Version, error) {
	v2 := false
	for _, relative := range []string{ReviewRelativePath, HistoryRelativePath, MachineLedgerRelativePath} {
		exists, err := reviewRegularExists(directory, relative)
		if err != nil {
			return "", err
		}
		v2 = v2 || exists
	}
	legacy := false
	for _, relative := range []string{
		"docs/session-review/project-overview.md",
		"docs/session-review/current-state.md",
		"docs/session-review/evolution-timeline.md",
	} {
		exists, err := reviewRegularExists(directory, relative)
		if err != nil {
			return "", err
		}
		legacy = legacy || exists
	}
	for _, relative := range []string{
		"docs/session-review/decisions",
		"docs/session-review/open-loops",
		"docs/session-review/sessions",
	} {
		exists, err := reviewDirectoryExists(directory, relative)
		if err != nil {
			return "", err
		}
		legacy = legacy || exists
	}
	switch {
	case legacy && v2:
		return VersionMixed, nil
	case legacy:
		return VersionLegacy, nil
	case v2:
		return VersionV2, nil
	default:
		return VersionEmpty, nil
	}
}

func reviewDirectoryExists(directory *pathguard.Directory, relative string) (bool, error) {
	opened, _, err := directory.OpenDirectory(relative)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect review ledger directory %s: %w", relative, err)
	}
	if err := opened.Close(); err != nil {
		return false, err
	}
	return true, nil
}

func openReviewRoot(projectRoot string, expectedRoot os.FileInfo) (*pathguard.Directory, error) {
	directory, err := pathguard.Open(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("open project root: %w", err)
	}
	if expectedRoot != nil && !os.SameFile(expectedRoot, directory.Info()) {
		_ = directory.Close()
		return nil, errors.New("opened project root does not match expected project root identity")
	}
	return directory, nil
}

func reviewRegularExists(directory *pathguard.Directory, relative string) (bool, error) {
	parent, _, err := directory.OpenDirectory(path.Dir(relative))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect review ledger path %s: %w", relative, err)
	}
	_, err = parent.Lstat(path.Base(relative))
	closeErr := parent.Close()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect review ledger path %s: %w", relative, err)
	}
	if closeErr != nil {
		return false, closeErr
	}
	file, _, err := directory.OpenRegular(relative)
	if err != nil {
		return false, fmt.Errorf("inspect review ledger path %s: %w", relative, err)
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	return true, nil
}

func readStableReviewFile(directory *pathguard.Directory, relative string, maximum int64) ([]byte, fs.FileMode, error) {
	file, info, err := directory.OpenRegular(relative)
	if err != nil {
		return nil, 0, err
	}
	if err := file.Close(); err != nil {
		return nil, 0, err
	}
	if info.Size() < 0 || info.Size() > maximum {
		return nil, 0, fmt.Errorf("file exceeds %d bytes", maximum)
	}
	body, err := pathguard.ReadStableRegularRootFile(directory.Root, relative, info, maximum)
	if err != nil {
		return nil, 0, errors.New("review ledger file changed while reading")
	}
	return body, info.Mode().Perm(), nil
}

func cloneSnapshotUsage(value ledger.SnapshotUsage) ledger.SnapshotUsage {
	value.Files = append([]ledger.SnapshotFile(nil), value.Files...)
	return value
}

func sortedEvidenceSessionIDs(values []ledger.EvidenceRef) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.SessionID)
	}
	return uniqueNonemptySorted(result)
}

func sortedAllEvidence(values map[string][]ledger.EvidenceRef) []ledger.EvidenceRef {
	byID := make(map[string]ledger.EvidenceRef)
	for _, refs := range values {
		for _, ref := range refs {
			byID[ref.EvidenceID] = ref
		}
	}
	ids := sortedMapKeys(byID)
	result := make([]ledger.EvidenceRef, 0, len(ids))
	for _, id := range ids {
		result = append(result, byID[id])
	}
	return result
}

func sortLegacyTimeline(values []ledger.TimelineEvent) {
	sort.Slice(values, func(left, right int) bool {
		if values[left].OccurredAt != values[right].OccurredAt {
			return values[left].OccurredAt < values[right].OccurredAt
		}
		return values[left].ID < values[right].ID
	})
}
