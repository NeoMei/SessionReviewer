// Package publicationstate defines the small persisted boundary shared by the
// publication writer and read-only sync authentication. It does not own locks,
// recovery, or publication orchestration.
package publicationstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

const (
	IntentLeaf          = "intent-v1.json"
	AcceptedReceiptLeaf = "accepted-markdown-v1.json"
	maxStateBytes       = 4 << 20
)

type Stage string

const (
	StagePrepared         Stage = "prepared"
	StageProjectWritten   Stage = "project_written"
	StageVaultSynced      Stage = "vault_synced"
	StageVerified         Stage = "verified"
	StageBaseCommitted    Stage = "base_committed"
	StageCommitted        Stage = "committed"
	StageRollbackRequired Stage = "rollback_required"
)

type Kind string

const (
	KindGeneration Kind = "generation"
	KindMarkdown   Kind = "markdown"
)

type Outcome string

const (
	OutcomeAccepted   Outcome = "accepted"
	OutcomeRolledBack Outcome = "rolled_back"
)

type Destination struct {
	Side           string `json:"side"`
	Relative       string `json:"relative"`
	PreimageSHA256 string `json:"preimage_sha256,omitempty"`
	DesiredSHA256  string `json:"desired_sha256"`
	PreimageExists bool   `json:"preimage_exists"`
}

type IndexGuard struct {
	Relative      string `json:"relative"`
	VaultRelative string `json:"vault_relative"`
	ProjectSHA256 string `json:"project_sha256"`
	VaultSHA256   string `json:"vault_sha256"`
	Digest        string `json:"digest"`
	GenerationID  string `json:"generation_id"`
}

type MigrationSourceProof struct {
	GenerationID      string        `json:"generation_id"`
	ManifestDigest    string        `json:"manifest_digest"`
	ProjectViewDigest string        `json:"project_view_digest"`
	IndexDigest       string        `json:"index_digest"`
	JournalDigest     string        `json:"journal_digest"`
	Destinations      []Destination `json:"destinations"`
}

type Intent struct {
	Version             int                   `json:"version"`
	Kind                Kind                  `json:"kind,omitempty"`
	ProjectID           string                `json:"project_id"`
	GenerationID        string                `json:"generation_id"`
	ManifestDigest      string                `json:"manifest_digest"`
	ProjectViewDigest   string                `json:"project_view_digest"`
	RevisionID          string                `json:"revision_id,omitempty"`
	Stage               Stage                 `json:"stage"`
	Outcome             Outcome               `json:"outcome,omitempty"`
	CreatedAt           time.Time             `json:"created_at"`
	Destinations        []Destination         `json:"destinations"`
	IndexGuard          *IndexGuard           `json:"index_guard,omitempty"`
	BasePreimageDigest  string                `json:"base_preimage_digest,omitempty"`
	BaseReviewPreimage  string                `json:"base_review_preimage_sha256,omitempty"`
	BaseHistoryPreimage string                `json:"base_history_preimage_sha256,omitempty"`
	BaseDesiredDigest   string                `json:"base_desired_digest,omitempty"`
	RequiresPointer     bool                  `json:"requires_pointer,omitempty"`
	PointerPreimage     *string               `json:"pointer_preimage_generation_id,omitempty"`
	MigrationSource     *MigrationSourceProof `json:"migration_source,omitempty"`
}

type AcceptedReceipt struct {
	Version           int                   `json:"version"`
	ProjectID         string                `json:"project_id"`
	GenerationID      string                `json:"generation_id"`
	ManifestDigest    string                `json:"manifest_digest"`
	ProjectViewDigest string                `json:"project_view_digest"`
	RevisionID        string                `json:"revision_id"`
	Destinations      []Destination         `json:"destinations"`
	IndexGuard        *IndexGuard           `json:"index_guard,omitempty"`
	BaseDigest        string                `json:"base_digest"`
	RequiresPointer   bool                  `json:"requires_pointer,omitempty"`
	PointerPreimage   *string               `json:"pointer_preimage_generation_id,omitempty"`
	MigrationSource   *MigrationSourceProof `json:"migration_source,omitempty"`
}

var (
	idPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	bareHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func MarkdownRevisionID(intent Intent) string {
	type revision struct {
		ProjectID, GenerationID, ManifestDigest, ProjectViewDigest string
		Destinations                                               []Destination
		IndexGuard                                                 *IndexGuard
		BaseDesiredDigest                                          string
		RequiresPointer                                            bool
		PointerPreimage                                            *string               `json:"PointerPreimage,omitempty"`
		MigrationSource                                            *MigrationSourceProof `json:"MigrationSource,omitempty"`
	}
	body, err := canonical(revision{
		ProjectID: intent.ProjectID, GenerationID: intent.GenerationID,
		ManifestDigest: intent.ManifestDigest, ProjectViewDigest: intent.ProjectViewDigest,
		Destinations: intent.Destinations, IndexGuard: intent.IndexGuard,
		BaseDesiredDigest: intent.BaseDesiredDigest, RequiresPointer: intent.RequiresPointer,
		PointerPreimage: cloneString(intent.PointerPreimage),
		MigrationSource: cloneMigrationSource(intent.MigrationSource),
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func ReceiptFromIntent(intent Intent) (AcceptedReceipt, error) {
	if err := ValidateIntent(intent, intent.ProjectID); err != nil {
		return AcceptedReceipt{}, err
	}
	if intent.Version != 2 || intent.Kind != KindMarkdown || intent.Stage != StageBaseCommitted || intent.Outcome != "" {
		return AcceptedReceipt{}, errors.New("Markdown intent is not ready for acceptance")
	}
	return AcceptedReceipt{
		Version: 1, ProjectID: intent.ProjectID, GenerationID: intent.GenerationID,
		ManifestDigest: intent.ManifestDigest, ProjectViewDigest: intent.ProjectViewDigest,
		RevisionID: intent.RevisionID, Destinations: cloneDestinations(intent.Destinations),
		IndexGuard: cloneGuard(intent.IndexGuard), BaseDigest: intent.BaseDesiredDigest, RequiresPointer: intent.RequiresPointer,
		PointerPreimage: cloneString(intent.PointerPreimage),
		MigrationSource: cloneMigrationSource(intent.MigrationSource),
	}, nil
}

func WriteAccepted(root *os.Root, intent Intent) (AcceptedReceipt, error) {
	receipt, err := ReceiptFromIntent(intent)
	if err != nil {
		return AcceptedReceipt{}, err
	}
	body, err := canonical(receipt)
	if err != nil {
		return AcceptedReceipt{}, err
	}
	if err := atomicfile.WriteRootFileChecked(root, AcceptedReceiptLeaf, body, 0o600, nil); err != nil {
		return AcceptedReceipt{}, fmt.Errorf("commit accepted Markdown receipt: %w", err)
	}
	loaded, err := ReadAccepted(root, receipt.ProjectID)
	if err != nil || loaded.RevisionID != receipt.RevisionID {
		return AcceptedReceipt{}, errors.Join(errors.New("accepted Markdown receipt re-read failed"), err)
	}
	if err := writeAcceptedResult(root, loaded); err != nil {
		return AcceptedReceipt{}, fmt.Errorf("archive accepted Markdown result: %w", err)
	}
	return loaded, nil
}

func ReadAccepted(root *os.Root, projectID string) (AcceptedReceipt, error) {
	body, found, err := readPrivate(root, AcceptedReceiptLeaf)
	if err != nil {
		return AcceptedReceipt{}, err
	}
	if !found {
		return AcceptedReceipt{}, os.ErrNotExist
	}
	var receipt AcceptedReceipt
	if err := decode(body, &receipt); err != nil {
		return AcceptedReceipt{}, fmt.Errorf("decode accepted Markdown receipt: %w", err)
	}
	if err := ValidateReceipt(receipt, projectID); err != nil {
		return AcceptedReceipt{}, err
	}
	return receipt, nil
}

func ReadIntent(root *os.Root, projectID string) (Intent, error) {
	body, found, err := readPrivate(root, IntentLeaf)
	if err != nil {
		return Intent{}, err
	}
	if !found {
		return Intent{}, os.ErrNotExist
	}
	var intent Intent
	if err := decode(body, &intent); err != nil {
		return Intent{}, err
	}
	if err := ValidateIntent(intent, projectID); err != nil {
		return Intent{}, err
	}
	return intent, nil
}

type Reader struct {
	data, journal *pathguard.Directory
	projectID     string
}

func OpenReadOnly(dataRoot, projectID string) (*Reader, error) {
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot || !idPattern.MatchString(projectID) {
		return nil, errors.New("valid publication state identity is required")
	}
	data, err := pathguard.Open(dataRoot)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && data.Info().Mode().Perm() != 0o700 {
		_ = data.Close()
		return nil, errors.New("publication data root is not private")
	}
	journal, err := pathguard.Open(filepath.Join(dataRoot, "publication-journal", projectID))
	if err != nil {
		_ = data.Close()
		return nil, err
	}
	if len(journal.Ancestors) < 3 || !os.SameFile(data.Info(), journal.Ancestors[len(journal.Ancestors)-3]) || (runtime.GOOS != "windows" && journal.Info().Mode().Perm() != 0o700) {
		_ = journal.Close()
		_ = data.Close()
		return nil, errors.New("publication journal escaped or is not private")
	}
	return &Reader{data: data, journal: journal, projectID: projectID}, nil
}

func (r *Reader) Close() error {
	if r == nil {
		return nil
	}
	return errors.Join(r.journal.Close(), r.data.Close())
}

// Intent returns the validated durable publication intent without creating or
// mutating journal state.
func (r *Reader) Intent() (Intent, error) {
	if r == nil {
		return Intent{}, errors.New("publication state reader is required")
	}
	return ReadIntent(r.journal.Root, r.projectID)
}

func (r *Reader) Accepted() (AcceptedReceipt, error) {
	intent, err := ReadIntent(r.journal.Root, r.projectID)
	if err != nil {
		return AcceptedReceipt{}, err
	}
	if intent.Stage != StageCommitted {
		return AcceptedReceipt{}, errors.New("publication has unresolved recovery state")
	}
	receipt, err := ReadAccepted(r.journal.Root, r.projectID)
	if err != nil {
		return AcceptedReceipt{}, err
	}
	if intent.Version == 2 && intent.Outcome == OutcomeAccepted && intent.RevisionID != receipt.RevisionID {
		return AcceptedReceipt{}, errors.New("committed Markdown intent does not match accepted receipt")
	}
	return receipt, nil
}

func ValidateIntent(intent Intent, projectID string) error {
	if intent.Version != 1 && intent.Version != 2 {
		return errors.New("unsupported journal intent version")
	}
	if intent.ProjectID != projectID || !idPattern.MatchString(intent.ProjectID) || !idPattern.MatchString(intent.GenerationID) || !digestPattern.MatchString(intent.ManifestDigest) || !digestPattern.MatchString(intent.ProjectViewDigest) || intent.CreatedAt.IsZero() || len(intent.Destinations) == 0 {
		return errors.New("invalid publication intent identity")
	}
	if intent.Version == 1 {
		if intent.Kind != "" || intent.RevisionID != "" || intent.Outcome != "" || intent.IndexGuard != nil || intent.BasePreimageDigest != "" || intent.BaseReviewPreimage != "" || intent.BaseHistoryPreimage != "" || intent.BaseDesiredDigest != "" || intent.RequiresPointer || intent.PointerPreimage != nil || intent.MigrationSource != nil {
			return errors.New("legacy journal contains v2 fields")
		}
		if intent.Stage != StagePrepared && intent.Stage != StageProjectWritten && intent.Stage != StageVaultSynced && intent.Stage != StageVerified && intent.Stage != StageCommitted && intent.Stage != StageRollbackRequired {
			return errors.New("legacy journal contains invalid stage")
		}
		return validateDestinations(intent.Destinations)
	}
	if intent.Kind != KindMarkdown || !digestPattern.MatchString(intent.RevisionID) || intent.RevisionID != MarkdownRevisionID(intent) || !bareHashPattern.MatchString(intent.BaseDesiredDigest) || (intent.BasePreimageDigest != "" && !bareHashPattern.MatchString(intent.BasePreimageDigest)) || intent.IndexGuard == nil {
		return errors.New("invalid Markdown publication intent")
	}
	if intent.RequiresPointer {
		if intent.PointerPreimage == nil || (*intent.PointerPreimage != "" && !idPattern.MatchString(*intent.PointerPreimage)) {
			return errors.New("invalid Markdown pointer preimage")
		}
	} else if intent.PointerPreimage != nil {
		return errors.New("unexpected Markdown pointer preimage")
	}
	if err := validateMigrationSourceProof(intent.MigrationSource, intent.RequiresPointer, intent.GenerationID, intent.ManifestDigest, intent.ProjectViewDigest, intent.IndexGuard.Digest); err != nil {
		return err
	}
	if (intent.BasePreimageDigest == "" && (intent.BaseReviewPreimage != "" || intent.BaseHistoryPreimage != "")) ||
		(intent.BasePreimageDigest != "" && (!bareHashPattern.MatchString(intent.BaseReviewPreimage) || !bareHashPattern.MatchString(intent.BaseHistoryPreimage))) {
		return errors.New("invalid Markdown Base rollback preimage")
	}
	if intent.Stage == StageCommitted && intent.Outcome != OutcomeAccepted && intent.Outcome != OutcomeRolledBack {
		return errors.New("committed Markdown intent has no outcome")
	}
	if intent.Stage != StageCommitted && intent.Outcome != "" {
		return errors.New("nonterminal Markdown intent has an outcome")
	}
	if intent.Stage != StagePrepared && intent.Stage != StageProjectWritten && intent.Stage != StageVaultSynced && intent.Stage != StageVerified && intent.Stage != StageBaseCommitted && intent.Stage != StageCommitted && intent.Stage != StageRollbackRequired {
		return errors.New("Markdown journal contains invalid stage")
	}
	if err := validateGuard(intent.IndexGuard, intent.GenerationID); err != nil {
		return err
	}
	return validateDestinations(intent.Destinations)
}

func ValidateReceipt(receipt AcceptedReceipt, projectID string) error {
	if receipt.Version != 1 || receipt.ProjectID != projectID || !idPattern.MatchString(receipt.ProjectID) || !idPattern.MatchString(receipt.GenerationID) || !digestPattern.MatchString(receipt.ManifestDigest) || !digestPattern.MatchString(receipt.ProjectViewDigest) || !digestPattern.MatchString(receipt.RevisionID) || !bareHashPattern.MatchString(receipt.BaseDigest) || receipt.IndexGuard == nil {
		return errors.New("invalid accepted Markdown receipt")
	}
	if err := validateGuard(receipt.IndexGuard, receipt.GenerationID); err != nil {
		return err
	}
	if err := validateDestinations(receipt.Destinations); err != nil {
		return err
	}
	if receipt.RequiresPointer {
		if receipt.PointerPreimage == nil || (*receipt.PointerPreimage != "" && !idPattern.MatchString(*receipt.PointerPreimage)) {
			return errors.New("invalid accepted Markdown pointer preimage")
		}
	} else if receipt.PointerPreimage != nil {
		return errors.New("unexpected accepted Markdown pointer preimage")
	}
	if err := validateMigrationSourceProof(receipt.MigrationSource, receipt.RequiresPointer, receipt.GenerationID, receipt.ManifestDigest, receipt.ProjectViewDigest, receipt.IndexGuard.Digest); err != nil {
		return err
	}
	probe := Intent{ProjectID: receipt.ProjectID, GenerationID: receipt.GenerationID, ManifestDigest: receipt.ManifestDigest, ProjectViewDigest: receipt.ProjectViewDigest, Destinations: receipt.Destinations, IndexGuard: receipt.IndexGuard, BaseDesiredDigest: receipt.BaseDigest, RequiresPointer: receipt.RequiresPointer, PointerPreimage: cloneString(receipt.PointerPreimage), MigrationSource: cloneMigrationSource(receipt.MigrationSource)}
	if MarkdownRevisionID(probe) != receipt.RevisionID {
		return errors.New("accepted Markdown receipt digest mismatch")
	}
	return nil
}

func validateMigrationSourceProof(proof *MigrationSourceProof, requiresPointer bool, generationID, manifestDigest, projectViewDigest, indexDigest string) error {
	if proof == nil {
		return nil
	}
	if !requiresPointer || !idPattern.MatchString(proof.GenerationID) || !digestPattern.MatchString(proof.ManifestDigest) || !digestPattern.MatchString(proof.ProjectViewDigest) || !digestPattern.MatchString(proof.IndexDigest) || !digestPattern.MatchString(proof.JournalDigest) || len(proof.Destinations) == 0 || validateDestinations(proof.Destinations) != nil {
		return errors.New("invalid Markdown migration source proof")
	}
	if proof.GenerationID == generationID && (proof.ManifestDigest != manifestDigest || proof.ProjectViewDigest != projectViewDigest || proof.IndexDigest != indexDigest) {
		return errors.New("invalid Markdown migration source proof")
	}
	return nil
}

func cloneMigrationSource(source *MigrationSourceProof) *MigrationSourceProof {
	if source == nil {
		return nil
	}
	clone := *source
	clone.Destinations = cloneDestinations(source.Destinations)
	return &clone
}

func validateGuard(guard *IndexGuard, generation string) error {
	if guard == nil || guard.Relative == "" || guard.VaultRelative == "" || guard.GenerationID != generation || !bareHashPattern.MatchString(guard.ProjectSHA256) || !bareHashPattern.MatchString(guard.VaultSHA256) || !digestPattern.MatchString(guard.Digest) {
		return errors.New("invalid session index guard")
	}
	return nil
}

func validateDestinations(destinations []Destination) error {
	for index, destination := range destinations {
		if (destination.Side != "project" && destination.Side != "vault") || destination.Relative == "" || filepath.IsAbs(destination.Relative) || strings.Contains(destination.Relative, "..") || !bareHashPattern.MatchString(destination.DesiredSHA256) || (destination.PreimageExists && !bareHashPattern.MatchString(destination.PreimageSHA256)) {
			return errors.New("invalid publication destination")
		}
		if index > 0 {
			previous := destinations[index-1]
			if previous.Side > destination.Side || (previous.Side == destination.Side && previous.Relative >= destination.Relative) {
				return errors.New("publication destinations are not strictly sorted")
			}
		}
	}
	return nil
}

func readPrivate(root *os.Root, leaf string) ([]byte, bool, error) {
	rootBefore, err := root.Stat(".")
	if err != nil || rootBefore == nil || !rootBefore.IsDir() {
		return nil, false, errors.New("publication state root is unavailable")
	}
	info, err := root.Lstat(leaf)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		return nil, false, errors.New("publication state file is missing or unsafe")
	}
	file, err := root.Open(leaf)
	if err != nil {
		return nil, false, errors.New("publication state file cannot be opened")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameFile(info, opened) {
		return nil, false, errors.New("publication state changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil || len(body) > maxStateBytes {
		return nil, false, errors.Join(errors.New("publication state exceeds size limit"), err)
	}
	afterOpen, openErr := file.Stat()
	afterName, nameErr := root.Lstat(leaf)
	rootAfter, rootErr := root.Stat(".")
	if openErr != nil || nameErr != nil || rootErr != nil || !sameFile(opened, afterOpen) || !sameFile(opened, afterName) || !sameFile(rootBefore, rootAfter) {
		return nil, false, errors.New("publication state changed while reading")
	}
	return body, true, nil
}

func sameFile(first, second os.FileInfo) bool {
	return first != nil && second != nil && os.SameFile(first, second) && first.Size() == second.Size() && first.Mode() == second.Mode() && first.ModTime().Equal(second.ModTime())
}

func canonical(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	if out.Len() > maxStateBytes {
		return nil, errors.New("publication state exceeds size limit")
	}
	return out.Bytes(), nil
}

func decode(body []byte, target any) error {
	if len(body) > maxStateBytes {
		return errors.New("publication state exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("publication state has trailing bytes")
	}
	canonicalBody, err := canonical(target)
	if err != nil || !bytes.Equal(body, canonicalBody) {
		return errors.New("publication state is not canonical")
	}
	return nil
}

func cloneDestinations(source []Destination) []Destination {
	return append([]Destination(nil), source...)
}
func cloneGuard(source *IndexGuard) *IndexGuard {
	if source == nil {
		return nil
	}
	copy := *source
	return &copy
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	copy := *source
	return &copy
}
