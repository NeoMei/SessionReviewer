package publication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

const (
	journalDirectoryMode fs.FileMode = 0o700
	journalFileMode      fs.FileMode = 0o600
	maxIntentBytes                   = 4 << 20
	maxPreimageBytes                 = 64 << 20
	intentFileLeaf                   = "intent-v1.json"
	journalLockTimeout               = 5 * time.Second
)

var (
	journalIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	sha256HexPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	manifestDigestPat = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Journal manages durable cross-root publication intent and preimage payloads.
type Journal struct {
	mu        sync.Mutex
	closed    bool
	data      *pathguard.Directory
	dir       *pathguard.Directory
	projectID string
}

// OpenJournal creates and pins the journal directory for a project below dataRoot.
func OpenJournal(dataRoot, projectID string) (*Journal, error) {
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return nil, errors.New("SessionReviewer data root must be an absolute clean path")
	}
	if !journalIDPattern.MatchString(projectID) {
		return nil, errors.New("invalid project ID for publication journal")
	}
	data, err := pathguard.Open(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("open SessionReviewer data root: %w", err)
	}
	closeData := true
	defer func() {
		if closeData {
			_ = data.Close()
		}
	}()
	if err := protectDirectory(data, journalDirectoryMode); err != nil {
		return nil, err
	}

	journalRel := filepath.ToSlash(filepath.Join("publication-journal", projectID))
	dirs := []string{"publication-journal", journalRel, journalRel + "/payloads", journalRel + "/locks"}
	for _, d := range dirs {
		if err := data.EnsureDirectory(d, journalDirectoryMode); err != nil {
			return nil, fmt.Errorf("create journal directory %q: %w", d, err)
		}
	}

	projectJournalPath := filepath.Join(data.Path, "publication-journal", projectID)
	journalDir, err := pathguard.Open(projectJournalPath)
	if err != nil {
		return nil, fmt.Errorf("open project journal directory: %w", err)
	}
	closeJournal := true
	defer func() {
		if closeJournal {
			_ = journalDir.Close()
		}
	}()

	// Validate ownership / ancestry
	if len(journalDir.Ancestors) < 3 || !os.SameFile(data.Info(), journalDir.Ancestors[len(journalDir.Ancestors)-3]) {
		return nil, errors.New("journal directory escaped SessionReviewer data root")
	}

	closeData = false
	closeJournal = false
	return &Journal{data: data, dir: journalDir, projectID: projectID}, nil
}

// Close releases pinned directories.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	var errs []error
	if j.dir != nil {
		errs = append(errs, j.dir.Close())
	}
	if j.data != nil {
		errs = append(errs, j.data.Close())
	}
	return errors.Join(errs...)
}

// Create records a new publication intent in StagePrepared.
func (j *Journal) Create(intent Intent) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return errors.New("journal is closed")
	}
	if err := validateIntent(intent, j.projectID); err != nil {
		return err
	}
	if intent.Stage != StagePrepared {
		return fmt.Errorf("new publication intent must be in stage %q, got %q", StagePrepared, intent.Stage)
	}
	existing, err := j.loadIntentUnlocked()
	if err == nil {
		if existing.Stage != StageCommitted {
			return ErrActiveIntentExists
		}
	} else if !errors.Is(err, ErrNoActiveIntent) {
		return err
	}
	body, err := encodeCanonicalJSON(intent)
	if err != nil {
		return err
	}
	return atomicfile.WriteRootFileChecked(j.dir.Root, intentFileLeaf, body, journalFileMode, nil)
}

// Load returns the active intent or ErrNoActiveIntent.
func (j *Journal) Load() (Intent, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return Intent{}, errors.New("journal is closed")
	}
	return j.loadIntentUnlocked()
}

// LoadPrevious returns an authenticated legacy intent backup. Releases affected
// by the partial merge-base rollback defect may have retained this evidence as
// intent-v1.json.bak for a later exact compensation.
func (j *Journal) LoadPrevious() (Intent, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return Intent{}, errors.New("journal is closed")
	}
	return j.loadNamedIntentUnlocked(intentFileLeaf + ".bak")
}

func (j *Journal) loadIntentUnlocked() (Intent, error) {
	return j.loadNamedIntentUnlocked(intentFileLeaf)
}

func (j *Journal) loadNamedIntentUnlocked(leaf string) (Intent, error) {
	body, found, err := j.dir.ReadRegular(leaf, maxIntentBytes)
	if err != nil {
		return Intent{}, fmt.Errorf("read intent: %w", err)
	}
	if !found {
		return Intent{}, ErrNoActiveIntent
	}
	if err := requirePrivateRegular(j.dir.Root, leaf); err != nil {
		return Intent{}, err
	}
	var intent Intent
	if err := decodeStrictJSON(body, &intent); err != nil {
		return Intent{}, fmt.Errorf("decode intent: %w", err)
	}
	if err := validateIntent(intent, j.projectID); err != nil {
		return Intent{}, err
	}
	return intent, nil
}

// Advance moves the active intent from expected stage to next stage.
func (j *Journal) Advance(expected, next Stage) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return errors.New("journal is closed")
	}
	intent, err := j.loadIntentUnlocked()
	if err != nil {
		return err
	}
	if intent.Stage == next {
		return nil // Idempotent success
	}
	if intent.Stage != expected {
		return fmt.Errorf("%w: expected stage %q, current is %q", ErrStageMismatch, expected, intent.Stage)
	}
	if !validTransition(expected, next) {
		return fmt.Errorf("%w: cannot transition from %q to %q", ErrInvalidStageTransition, expected, next)
	}
	intent.Stage = next
	body, err := encodeCanonicalJSON(intent)
	if err != nil {
		return err
	}
	return atomicfile.WriteRootFileChecked(j.dir.Root, intentFileLeaf, body, journalFileMode, nil)
}

// PutPreimage stores an immutable preimage payload keyed by its sha256 hex string.
func (j *Journal) PutPreimage(hash string, data []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return errors.New("journal is closed")
	}
	hash = strings.ToLower(strings.TrimPrefix(hash, "sha256:"))
	if !sha256HexPattern.MatchString(hash) {
		return fmt.Errorf("invalid preimage hash %q", hash)
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if actual != hash {
		return fmt.Errorf("%w: expected %s, got %s", ErrPreimageMismatch, hash, actual)
	}
	payloadsDir, err := pathguard.Open(filepath.Join(j.dir.Path, "payloads"))
	if err != nil {
		return fmt.Errorf("open payloads directory: %w", err)
	}
	defer payloadsDir.Close()
	leaf := hash + ".blob"
	body, found, err := payloadsDir.ReadRegular(leaf, maxPreimageBytes)
	if err == nil && found {
		if bytes.Equal(body, data) {
			return nil
		}
		return errors.New("immutable preimage payload conflict")
	}
	return atomicfile.WriteRootFileChecked(payloadsDir.Root, leaf, data, journalFileMode, nil)
}

// LoadPreimage retrieves a preimage payload by hash.
func (j *Journal) LoadPreimage(hash string) ([]byte, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil, errors.New("journal is closed")
	}
	hash = strings.ToLower(strings.TrimPrefix(hash, "sha256:"))
	if !sha256HexPattern.MatchString(hash) {
		return nil, fmt.Errorf("invalid preimage hash %q", hash)
	}
	payloadsDir, err := pathguard.Open(filepath.Join(j.dir.Path, "payloads"))
	if err != nil {
		return nil, fmt.Errorf("open payloads directory: %w", err)
	}
	defer payloadsDir.Close()
	leaf := hash + ".blob"
	body, found, err := payloadsDir.ReadRegular(leaf, maxPreimageBytes)
	if err != nil {
		return nil, fmt.Errorf("read preimage: %w", err)
	}
	if !found {
		return nil, os.ErrNotExist
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, errors.New("stored preimage payload hash corrupted")
	}
	return body, nil
}

// Recover inspects the active intent and invokes the recovery handler if non-terminal.
func (j *Journal) Recover(ctx context.Context, h RecoveryHandler) error {
	if h == nil {
		return errors.New("recovery handler is required")
	}
	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		return errors.New("journal is closed")
	}
	intent, err := j.loadIntentUnlocked()
	j.mu.Unlock()
	if err != nil {
		if errors.Is(err, ErrNoActiveIntent) {
			return nil
		}
		return err
	}
	if intent.Stage == StageCommitted {
		return nil
	}
	return h.RecoverStage(ctx, intent, j)
}

func validTransition(from, to Stage) bool {
	switch from {
	case StagePrepared:
		return to == StageProjectWritten || to == StageRollbackRequired
	case StageProjectWritten:
		return to == StageVaultSynced || to == StageRollbackRequired
	case StageVaultSynced:
		return to == StageVerified || to == StageRollbackRequired
	case StageVerified:
		return to == StageCommitted || to == StageRollbackRequired
	case StageRollbackRequired:
		return to == StageCommitted // After rollback is completed
	default:
		return false
	}
}

func validateIntent(intent Intent, expectedProjectID string) error {
	if intent.Version != 1 {
		return fmt.Errorf("unsupported journal intent version %d", intent.Version)
	}
	if intent.ProjectID == "" || intent.ProjectID != expectedProjectID {
		return fmt.Errorf("journal intent project ID %q does not match %q", intent.ProjectID, expectedProjectID)
	}
	if !journalIDPattern.MatchString(intent.GenerationID) {
		return errors.New("invalid generation ID in journal intent")
	}
	if !manifestDigestPat.MatchString(intent.ManifestDigest) {
		return errors.New("invalid manifest digest in journal intent")
	}
	if !manifestDigestPat.MatchString(intent.ProjectViewDigest) {
		return errors.New("invalid project view digest in journal intent")
	}
	if intent.CreatedAt.IsZero() {
		return errors.New("journal intent created_at cannot be zero")
	}
	if len(intent.Destinations) == 0 {
		return errors.New("journal intent destinations cannot be empty")
	}
	for i, dest := range intent.Destinations {
		if dest.Side != "project" && dest.Side != "vault" {
			return fmt.Errorf("destination side %q is invalid", dest.Side)
		}
		if dest.Relative == "" || filepath.IsAbs(dest.Relative) || strings.Contains(dest.Relative, "..") {
			return fmt.Errorf("destination relative path %q is invalid", dest.Relative)
		}
		if !sha256HexPattern.MatchString(strings.ToLower(dest.DesiredSHA256)) {
			return fmt.Errorf("destination desired SHA256 %q is invalid", dest.DesiredSHA256)
		}
		if dest.PreimageExists && !sha256HexPattern.MatchString(strings.ToLower(dest.PreimageSHA256)) {
			return fmt.Errorf("destination preimage SHA256 %q is invalid", dest.PreimageSHA256)
		}
		if i > 0 {
			prev := intent.Destinations[i-1]
			if prev.Side > dest.Side || (prev.Side == dest.Side && prev.Relative >= dest.Relative) {
				return errors.New("journal intent destinations must be sorted strictly by side and relative path without duplicates")
			}
		}
	}
	return nil
}

func encodeCanonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encode json: %w", err)
	}
	return buf.Bytes(), nil
}

func decodeStrictJSON(data []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing bytes after JSON object")
	}
	return nil
}

func protectDirectory(dir *pathguard.Directory, mode fs.FileMode) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(dir.Path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != mode {
		if err := os.Chmod(dir.Path, mode); err != nil {
			return fmt.Errorf("chmod %q: %w", dir.Path, err)
		}
	}
	return nil
}

func requirePrivateRegular(root *os.Root, leaf string) error {
	info, err := root.Lstat(leaf)
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return errors.Join(errors.New("private journal file is not regular"), err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != journalFileMode {
		return fmt.Errorf("%q has invalid permissions %o, expected %o", leaf, info.Mode().Perm(), journalFileMode)
	}
	return nil
}
