package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/neomei/SessionReviewer/internal/pathguard"
)

// SnapshotFile is the immutable identity of one source Markdown document in
// the accepted ledger namespace.
type SnapshotFile struct {
	RelativePath string
	SHA256       string
	Perm         fs.FileMode
	Size         int64
}

// SnapshotUsage includes the direct directory entries consumed by the three
// entity collections. Non-Markdown entries count toward traversal cost even
// though they are not source documents.
type SnapshotUsage struct {
	Files            []SnapshotFile
	DirectoryEntries int
}

// SnapshotFiles returns the exact source documents that produced state.
func SnapshotFiles(state State) []SnapshotFile {
	documents := make([]loadedDocument, 0, 3+len(state.documents.decisions)+len(state.documents.openLoops)+len(state.documents.sessions))
	for _, document := range []*loadedDocument{state.documents.overview, state.documents.current, state.documents.timeline} {
		if document != nil {
			documents = append(documents, *document)
		}
	}
	for _, group := range []map[string]loadedDocument{state.documents.decisions, state.documents.openLoops, state.documents.sessions} {
		for _, document := range group {
			documents = append(documents, document)
		}
	}
	result := make([]SnapshotFile, 0, len(documents))
	for _, document := range documents {
		result = append(result, newSnapshotFile(document.RelativePath, document.Original, document.Perm))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RelativePath < result[j].RelativePath })
	return result
}

// SnapshotExpected reads the ledger namespace without parsing it. It is used
// by the apply transaction to detect additions, removals, and edits while a
// prepared receipt is being applied or recovered.
func SnapshotExpected(projectRoot string, expectedRoot os.FileInfo) ([]SnapshotFile, error) {
	usage, err := SnapshotUsageExpected(projectRoot, expectedRoot)
	return usage.Files, err
}

// SnapshotUsageExpected returns both source identities and the resource usage
// needed to reject an apply target that would make its own receipt unrecoverable.
func SnapshotUsageExpected(projectRoot string, expectedRoot os.FileInfo) (SnapshotUsage, error) {
	directory, err := openLedgerProjectRoot(projectRoot, rootOpenOptions{expectedRoot: expectedRoot})
	if err != nil {
		return SnapshotUsage{}, err
	}
	defer directory.Close()
	return snapshotUsageFromDirectory(directory)
}

func snapshotUsageFromDirectory(directory *pathguard.Directory) (SnapshotUsage, error) {
	budget := &ledgerReadBudget{remainingFiles: maxLedgerLoadFiles, remainingBytes: maxLedgerLoadBytes}
	result := make([]SnapshotFile, 0)
	for _, relative := range []string{
		ledgerRootRelative + "/project-overview.md",
		ledgerRootRelative + "/current-state.md",
		ledgerRootRelative + "/evolution-timeline.md",
	} {
		body, perm, err := readLedgerRegularBudget(directory, relative, relative == ledgerRootRelative+"/project-overview.md", budget)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return SnapshotUsage{}, err
		}
		result = append(result, newSnapshotFile(relative, body, perm))
	}
	remainingEntries := maxLedgerLoadEntries
	directoryEntries := 0
	for _, class := range []string{"decisions", "open-loops", "sessions"} {
		relativeDir := ledgerRootRelative + "/" + class
		entries, err := readLedgerDirectory(directory, relativeDir, &remainingEntries)
		if err != nil {
			return SnapshotUsage{}, err
		}
		directoryEntries += len(entries)
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			relative := relativeDir + "/" + entry.Name()
			body, perm, err := readLedgerRegularBudget(directory, relative, true, budget)
			if err != nil {
				return SnapshotUsage{}, err
			}
			result = append(result, newSnapshotFile(relative, body, perm))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RelativePath < result[j].RelativePath })
	usage := SnapshotUsage{Files: result, DirectoryEntries: directoryEntries}
	if err := ValidateSnapshotUsage(usage); err != nil {
		return SnapshotUsage{}, err
	}
	return usage, nil
}

// ValidateSnapshotUsage applies the same aggregate limits as Load.
func ValidateSnapshotUsage(usage SnapshotUsage) error {
	if usage.DirectoryEntries < 0 || usage.DirectoryEntries > maxLedgerLoadEntries || len(usage.Files) > maxLedgerLoadFiles {
		return errors.New("ledger target exceeds aggregate entry budget")
	}
	remaining := int64(maxLedgerLoadBytes)
	for _, file := range usage.Files {
		if file.Size < 0 || file.Size > MaxDocumentBytes || file.Size > remaining {
			return errors.New("ledger target exceeds aggregate byte budget")
		}
		remaining -= file.Size
	}
	return nil
}

// IsSnapshotPath reports whether a canonical slash path belongs to the source
// ledger namespace. Derived diagrams intentionally do not participate.
func IsSnapshotPath(relative string) bool {
	switch relative {
	case ledgerRootRelative + "/project-overview.md", ledgerRootRelative + "/current-state.md", ledgerRootRelative + "/evolution-timeline.md":
		return true
	}
	for _, prefix := range []string{
		ledgerRootRelative + "/decisions/",
		ledgerRootRelative + "/open-loops/",
		ledgerRootRelative + "/sessions/",
	} {
		if strings.HasPrefix(relative, prefix) {
			name := strings.TrimPrefix(relative, prefix)
			return name != "" && !strings.Contains(name, "/") && strings.HasSuffix(name, ".md")
		}
	}
	return false
}

// IsCollectionSnapshotPath reports whether a source document consumes one
// entry in decisions, open-loops, or sessions.
func IsCollectionSnapshotPath(relative string) bool {
	if !IsSnapshotPath(relative) {
		return false
	}
	return strings.HasPrefix(relative, ledgerRootRelative+"/decisions/") ||
		strings.HasPrefix(relative, ledgerRootRelative+"/open-loops/") ||
		strings.HasPrefix(relative, ledgerRootRelative+"/sessions/")
}

func newSnapshotFile(relative string, body []byte, perm fs.FileMode) SnapshotFile {
	sum := sha256.Sum256(body)
	return SnapshotFile{RelativePath: relative, SHA256: "sha256:" + hex.EncodeToString(sum[:]), Perm: perm.Perm(), Size: int64(len(body))}
}
