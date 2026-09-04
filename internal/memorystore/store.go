// Package memorystore persists validated zero-token memory records in a
// project-scoped private store.
package memorystore

import (
	"bufio"
	"bytes"
	"context"
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
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/project"
)

const (
	privateDirectoryMode         fs.FileMode = 0o700
	privateFileMode              fs.FileMode = 0o600
	maxObjectBytes                           = 64 << 20
	maxManifestBytes                         = 16 << 20
	storeLockTimeout                         = 5 * time.Second
	storeLockContextPollInterval             = 20 * time.Millisecond
	storeContextReadChunkSize                = 64 * 1024
	preparedAdvanceJournalLeaf               = "prepared-advance-v1.json"

	// WriteRootFileChecked has three checkpoints when publishing a previously
	// absent destination: before temporary creation, before publication, and
	// after durable publication.
	manifestCheckpointCount = 3
)

var (
	ErrNoPreparedGeneration    = errors.New("no prepared generation")
	ErrPreparedGeneration      = errors.New("a different prepared generation already exists")
	ErrNoPublishedGeneration   = errors.New("no published generation")
	ErrPublicationProofInvalid = errors.New("publication proof is invalid")
	ErrImmutableConflict       = errors.New("immutable object digest already contains different bytes")

	storeIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	digestPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

	// storeContextCheckpoint is a deterministic test seam for proving that
	// cancellation is observed inside large decode, validation, and structural
	// graph-comparison work.
	storeContextCheckpoint func(string)
)

// ObjectKind identifies one immutable content-addressed object collection.
type ObjectKind string

const (
	ObjectObservationChunk ObjectKind = "observations"
	ObjectSessionView      ObjectKind = "sessions"
	ObjectSessionLineage   ObjectKind = "session-lineages"
	ObjectProbeState       ObjectKind = "project-probes"
	ObjectProjectView      ObjectKind = "project-views"
)

// Prepared is the durable pointer to one fully verified private generation.
type Prepared struct {
	GenerationID      string `json:"generation_id"`
	ManifestDigest    string `json:"manifest_digest"`
	ProjectViewDigest string `json:"project_view_digest"`
}

type preparedAdvanceJournal struct {
	Version   int      `json:"version"`
	ProjectID string   `json:"project_id"`
	Expected  Prepared `json:"expected"`
	Successor Prepared `json:"successor"`
}

// Store owns pinned handles to an absolute SessionReviewer data root and one
// project memory root. Test-only checkpoint seams remain unexported.
type Store struct {
	mu                 sync.RWMutex
	data               *pathguard.Directory
	memory             *pathguard.Directory
	projectID          string
	closed             bool
	objectCheckpoint   func() error
	manifestCheckpoint func() error
}

// Open creates and pins the private project layout below an existing absolute
// SessionReviewer data root. Project IDs are data identities, never paths.
func Open(dataRoot, projectID string) (*Store, error) {
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return nil, errors.New("SessionReviewer data root must be an absolute clean path")
	}
	if err := validateStoreID(projectID); err != nil {
		return nil, fmt.Errorf("invalid project ID: %w", err)
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
	if err := protectPinnedDirectory(data, privateDirectoryMode); err != nil {
		return nil, fmt.Errorf("protect SessionReviewer data root: %w", err)
	}

	projectBase := filepath.ToSlash(filepath.Join("projects", projectID, "memory-v1"))
	directories := []string{
		"source-catalog",
		"projects",
		filepath.ToSlash(filepath.Join("projects", projectID)),
		projectBase,
	}
	for _, child := range []string{"observations", "sessions", "session-lineages", "project-probes", "project-views", "generations", "diagnostics", "staging", "locks"} {
		directories = append(directories, projectBase+"/"+child)
	}
	for _, relative := range directories {
		if err := data.EnsureDirectory(relative, privateDirectoryMode); err != nil {
			return nil, fmt.Errorf("create private store directory %q: %w", relative, err)
		}
	}

	memoryRoot := filepath.Join(data.Path, "projects", projectID, "memory-v1")
	memoryDirectory, err := pathguard.Open(memoryRoot)
	if err != nil {
		return nil, fmt.Errorf("pin project memory root: %w", err)
	}
	closeMemory := true
	defer func() {
		if closeMemory {
			_ = memoryDirectory.Close()
		}
	}()
	if len(memoryDirectory.Ancestors) < 4 || !os.SameFile(data.Info(), memoryDirectory.Ancestors[len(memoryDirectory.Ancestors)-4]) {
		return nil, errors.New("project memory root escaped SessionReviewer data root")
	}

	store := &Store{data: data, memory: memoryDirectory, projectID: projectID}
	if err := store.withStoreLock(store.reconcilePreparedAdvanceUnlocked); err != nil {
		return nil, fmt.Errorf("recover prepared generation: %w", err)
	}
	closeData = false
	closeMemory = false
	return store, nil
}

func (s *Store) PutObservationChunk(records []memory.ObservationRevision) (string, error) {
	if len(records) == 0 {
		return "", errors.New("observation chunk must not be empty")
	}
	for index := range records {
		if err := memory.ValidateObservationRevision(records[index]); err != nil {
			return "", fmt.Errorf("invalid observation %d: %w", index, err)
		}
		if records[index].Key.ProjectID != s.projectID {
			return "", errors.New("observation belongs to a different project")
		}
	}
	digest, err := memory.Digest(records)
	if err != nil {
		return "", fmt.Errorf("digest observation chunk: %w", err)
	}
	var body bytes.Buffer
	for index := range records {
		line, err := json.Marshal(records[index])
		if err != nil {
			return "", fmt.Errorf("encode observation %d: %w", index, err)
		}
		body.Write(line)
		body.WriteByte('\n')
	}
	if err := s.putImmutable(ObjectObservationChunk, digest, body.Bytes()); err != nil {
		return "", err
	}
	return digest, nil
}

func (s *Store) PutSessionView(value memory.SessionView) (string, error) {
	if err := memory.ValidateSessionView(value); err != nil {
		return "", fmt.Errorf("invalid SessionView: %w", err)
	}
	if value.ProjectID != s.projectID {
		return "", errors.New("SessionView belongs to a different project")
	}
	return s.putJSON(ObjectSessionView, value.Digest, value)
}

func (s *Store) PutSessionLineage(value memory.SessionLineage) (string, error) {
	if err := memory.ValidateSessionLineage(value); err != nil {
		return "", fmt.Errorf("invalid SessionLineage: %w", err)
	}
	if value.ProjectID != s.projectID {
		return "", errors.New("SessionLineage belongs to a different project")
	}
	return s.putJSON(ObjectSessionLineage, value.Digest, value)
}

func (s *Store) PutProbeState(value memory.ProjectProbeState) (string, error) {
	if err := memory.ValidateProjectProbeState(value); err != nil {
		return "", fmt.Errorf("invalid ProjectProbeState: %w", err)
	}
	if value.ProjectID != s.projectID {
		return "", errors.New("ProjectProbeState belongs to a different project")
	}
	return s.putJSON(ObjectProbeState, value.Digest, value)
}

func (s *Store) PutProjectView(value memory.ProjectView) (string, error) {
	if err := memory.ValidateProjectView(value); err != nil {
		return "", fmt.Errorf("invalid ProjectView: %w", err)
	}
	if value.ProjectID != s.projectID {
		return "", errors.New("ProjectView belongs to a different project")
	}
	return s.putJSON(ObjectProjectView, value.Digest, value)
}

func (s *Store) putJSON(kind ObjectKind, digest string, value any) (string, error) {
	body, err := marshalCanonical(value)
	if err != nil {
		return "", err
	}
	if err := s.putImmutable(kind, digest, body); err != nil {
		return "", err
	}
	return digest, nil
}

func (s *Store) putImmutable(kind ObjectKind, digest string, body []byte) error {
	collection, suffix, err := objectLocation(kind, digest)
	if err != nil {
		return err
	}
	if len(body) > maxObjectBytes {
		return errors.New("immutable object exceeds size limit")
	}
	return s.withStoreLock(func() error {
		parent, err := s.openCollection(collection)
		if err != nil {
			return err
		}
		defer parent.Close()
		leaf := digestLeafName(digest, suffix)
		existing, found, err := parent.ReadRegular(leaf, maxObjectBytes)
		if err != nil {
			return fmt.Errorf("inspect immutable object: %w", err)
		}
		if found {
			if !bytes.Equal(existing, body) {
				return ErrImmutableConflict
			}
			if err := requirePrivateRegular(parent.Root, leaf); err != nil {
				return err
			}
			return validateObjectBytes(kind, digest, existing, s.projectID)
		}
		if err := atomicfile.WriteRootFileCreateIfAbsent(parent.Root, leaf, body, privateFileMode, s.objectCheckpoint); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("publish immutable object: %w", err)
		}
		stored, found, err := parent.ReadRegular(leaf, maxObjectBytes)
		if err != nil || !found {
			return errors.Join(errors.New("immutable object canonical re-read failed"), err)
		}
		if err := requirePrivateRegular(parent.Root, leaf); err != nil {
			return err
		}
		if !bytes.Equal(stored, body) {
			return ErrImmutableConflict
		}
		return validateObjectBytes(kind, digest, stored, s.projectID)
	})
}

// PrepareGeneration verifies every project object before atomically publishing
// manifest.json as the sole prepared-generation commit point. A different
// already-prepared generation is never replaced by Gate A.
func (s *Store) PrepareGeneration(value memory.GenerationManifest) (Prepared, error) {
	artifacts, err := s.prepareArtifacts(value)
	if err != nil {
		return Prepared{}, err
	}

	err = s.withStoreLock(func() error {
		existing, _, loadErr := s.loadPreparedUnlocked()
		if loadErr == nil {
			if existing != artifacts.prepared {
				return ErrPreparedGeneration
			}
			generation, err := s.loadGeneration(value.GenerationID)
			if err != nil || !equalManifest(generation, value) {
				return errors.Join(ErrImmutableConflict, err)
			}
			return nil
		}
		if !errors.Is(loadErr, ErrNoPreparedGeneration) {
			return loadErr
		}
		if err := s.reconcileGenerationGraph(value); err != nil {
			return err
		}
		if err := s.writeGenerationUnlocked(value, artifacts); err != nil {
			return err
		}
		if err := s.writePreparedPointerUnlocked(artifacts.pointerBody, s.manifestCheckpoint); err != nil {
			return err
		}
		loaded, loadedManifest, err := s.loadPreparedUnlocked()
		if err != nil || loaded != artifacts.prepared || !equalManifest(loadedManifest, value) {
			return errors.Join(errors.New("prepared generation canonical re-read failed"), err)
		}
		return nil
	})
	if err != nil {
		return Prepared{}, err
	}
	return artifacts.prepared, nil
}

// AdvancePrepared atomically advances only the exact validated prepared
// pointer supplied by the caller. It retains both immutable generation objects
// and never creates or changes a published-generation pointer.
func (s *Store) AdvancePrepared(expected Prepared, successor memory.GenerationManifest) (Prepared, error) {
	artifacts, err := s.prepareArtifacts(successor)
	if err != nil {
		return Prepared{}, err
	}
	expectedBody, err := marshalCanonical(expected)
	if err != nil {
		return Prepared{}, err
	}
	err = s.withStoreLock(func() error {
		current, _, err := s.loadPreparedUnlocked()
		if err != nil || current != expected {
			return errors.Join(ErrPreparedGeneration, err)
		}
		if artifacts.prepared == expected {
			return errors.New("successor generation must differ from expected prepared generation")
		}
		if err := s.reconcileGenerationGraph(successor); err != nil {
			return err
		}
		if err := s.writeGenerationUnlocked(successor, artifacts); err != nil {
			return err
		}
		journal := preparedAdvanceJournal{Version: 1, ProjectID: s.projectID, Expected: expected, Successor: artifacts.prepared}
		journalBody, err := marshalCanonical(journal)
		if err != nil {
			return err
		}

		root, err := s.reopenMemory()
		if err != nil {
			return err
		}
		defer root.Close()
		if err := rejectManifestBackup(root); err != nil {
			return err
		}
		if err := requirePreparedPointerBytes(root, expectedBody); err != nil {
			return err
		}
		if err := atomicfile.WriteRootFileCreateIfAbsent(root.Root, preparedAdvanceJournalLeaf, journalBody, privateFileMode, nil); err != nil {
			return fmt.Errorf("write prepared advance journal: %w", err)
		}
		if err := s.runManifestCheckpoint(); err != nil {
			return err
		}
		checkpoint := func() error {
			body, found, readErr := root.ReadRegular("manifest.json", maxManifestBytes)
			if readErr != nil || !found || (!bytes.Equal(body, expectedBody) && !bytes.Equal(body, artifacts.pointerBody)) {
				return errors.Join(ErrPreparedGeneration, readErr)
			}
			return s.runManifestCheckpoint()
		}
		if err := atomicfile.WriteRootFileChecked(root.Root, "manifest.json", artifacts.pointerBody, privateFileMode, checkpoint); err != nil {
			return fmt.Errorf("advance prepared generation: %w", err)
		}
		if err := s.runManifestCheckpoint(); err != nil {
			return err
		}
		loaded, loadedManifest, err := s.loadPreparedRawUnlocked()
		if err != nil || loaded != artifacts.prepared || !equalManifest(loadedManifest, successor) {
			return errors.Join(errors.New("advanced generation canonical re-read failed"), err)
		}
		if err := atomicfile.RemoveRoot(root.Root, preparedAdvanceJournalLeaf); err != nil {
			return fmt.Errorf("remove prepared advance journal: %w", err)
		}
		if err := s.runManifestCheckpoint(); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return Prepared{}, err
	}
	return artifacts.prepared, nil
}

func (s *Store) runManifestCheckpoint() error {
	if s.manifestCheckpoint != nil {
		return s.manifestCheckpoint()
	}
	return nil
}

type preparationArtifacts struct {
	prepared     Prepared
	manifestBody []byte
	pointerBody  []byte
}

func (s *Store) prepareArtifacts(value memory.GenerationManifest) (preparationArtifacts, error) {
	if err := memory.ValidateGenerationManifest(value); err != nil {
		return preparationArtifacts{}, fmt.Errorf("invalid generation manifest: %w", err)
	}
	if value.ProjectID != s.projectID {
		return preparationArtifacts{}, errors.New("generation manifest belongs to a different project")
	}
	manifestDigest, err := memory.Digest(value)
	if err != nil {
		return preparationArtifacts{}, fmt.Errorf("digest generation manifest: %w", err)
	}
	prepared := Prepared{GenerationID: value.GenerationID, ManifestDigest: manifestDigest, ProjectViewDigest: value.ProjectViewDigest}
	manifestBody, err := marshalCanonical(value)
	if err != nil {
		return preparationArtifacts{}, err
	}
	pointerBody, err := marshalCanonical(prepared)
	if err != nil {
		return preparationArtifacts{}, err
	}
	return preparationArtifacts{prepared: prepared, manifestBody: manifestBody, pointerBody: pointerBody}, nil
}

func (s *Store) writeGenerationUnlocked(value memory.GenerationManifest, artifacts preparationArtifacts) error {
	generations, err := s.openCollection("generations")
	if err != nil {
		return err
	}
	defer generations.Close()
	generationLeaf := value.GenerationID + ".json"
	existing, found, err := generations.ReadRegular(generationLeaf, maxManifestBytes)
	if err != nil {
		return fmt.Errorf("inspect generation object: %w", err)
	}
	if found {
		if err := requirePrivateRegular(generations.Root, generationLeaf); err != nil {
			return err
		}
		if !bytes.Equal(existing, artifacts.manifestBody) {
			return ErrImmutableConflict
		}
		_, err := decodeGeneration(existing, s.projectID, value.GenerationID, artifacts.prepared.ManifestDigest)
		return err
	}
	if err := atomicfile.WriteRootFileCreateIfAbsent(generations.Root, generationLeaf, artifacts.manifestBody, privateFileMode, s.objectCheckpoint); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("publish immutable generation: %w", err)
	}
	stored, found, err := generations.ReadRegular(generationLeaf, maxManifestBytes)
	if err != nil || !found {
		return errors.Join(errors.New("generation canonical re-read failed"), err)
	}
	if err := requirePrivateRegular(generations.Root, generationLeaf); err != nil {
		return err
	}
	if !bytes.Equal(stored, artifacts.manifestBody) {
		return ErrImmutableConflict
	}
	_, err = decodeGeneration(stored, s.projectID, value.GenerationID, artifacts.prepared.ManifestDigest)
	return err
}

func (s *Store) writePreparedPointerUnlocked(pointerBody []byte, checkpoint func() error) error {
	root, err := s.reopenMemory()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := rejectManifestBackup(root); err != nil {
		return err
	}
	if err := atomicfile.WriteRootFileChecked(root.Root, "manifest.json", pointerBody, privateFileMode, checkpoint); err != nil {
		return fmt.Errorf("commit prepared generation: %w", err)
	}
	return nil
}

func requirePreparedPointerBytes(root *pathguard.Directory, expected []byte) error {
	body, found, err := root.ReadRegular("manifest.json", maxManifestBytes)
	if err != nil || !found || !bytes.Equal(body, expected) {
		return errors.Join(ErrPreparedGeneration, err)
	}
	if err := requirePrivateRegular(root.Root, "manifest.json"); err != nil {
		return err
	}
	return nil
}

// LoadPrepared returns both the durable prepared pointer and its fully
// validated immutable generation manifest.
func (s *Store) LoadPrepared() (Prepared, memory.GenerationManifest, error) {
	var prepared Prepared
	var manifest memory.GenerationManifest
	err := s.withStoreLock(func() error {
		var err error
		prepared, manifest, err = s.loadPreparedUnlocked()
		return err
	})
	return prepared, manifest, err
}

// CommitPublished atomically records the published generation pointer after verifying
// the immutable generation manifest, project view digest, three projection file hashes,
// and the journal verification proof.
func (s *Store) CommitPublished(generationID string, proof memory.PublicationProof) error {
	if err := validateStoreID(generationID); err != nil {
		return fmt.Errorf("%w: invalid generation ID", ErrPublicationProofInvalid)
	}
	if proof.ProjectID != s.projectID || proof.GenerationID != generationID {
		return fmt.Errorf("%w: proof project or generation ID mismatch", ErrPublicationProofInvalid)
	}
	if !proof.JournalVerified {
		return fmt.Errorf("%w: journal verified proof is required", ErrPublicationProofInvalid)
	}
	if proof.Version != 0 && proof.Version != 4 {
		return fmt.Errorf("%w: unsupported publication proof version", ErrPublicationProofInvalid)
	}
	if !sha256HexPattern.MatchString(strings.ToLower(proof.ReviewSHA256)) ||
		!sha256HexPattern.MatchString(strings.ToLower(proof.HistorySHA256)) ||
		!sha256HexPattern.MatchString(strings.ToLower(proof.LedgerSHA256)) {
		return fmt.Errorf("%w: public projection file hashes are invalid", ErrPublicationProofInvalid)
	}
	if proof.Version == 4 && !sha256HexPattern.MatchString(strings.ToLower(proof.SessionIndexSHA256)) {
		return fmt.Errorf("%w: v4 session index hash is required", ErrPublicationProofInvalid)
	}
	if proof.Version == 0 && proof.SessionIndexSHA256 != "" {
		return fmt.Errorf("%w: legacy publication proof cannot include a session index hash", ErrPublicationProofInvalid)
	}

	return s.withStoreLock(func() error {
		manifest, err := s.loadGeneration(generationID)
		if err != nil {
			return fmt.Errorf("%w: load generation %q: %v", ErrPublicationProofInvalid, generationID, err)
		}
		digest, err := memory.Digest(manifest)
		if err != nil || digest != proof.ManifestDigest || manifest.ProjectViewDigest != proof.ProjectViewDigest {
			return fmt.Errorf("%w: manifest or project view digest mismatch", ErrPublicationProofInvalid)
		}

		root, err := s.reopenMemory()
		if err != nil {
			return err
		}
		defer root.Close()

		publishedBody := []byte(generationID + "\n")
		if err := atomicfile.WriteRootFile(root.Root, "published_generation", publishedBody, privateFileMode); err != nil {
			return fmt.Errorf("commit published generation: %w", err)
		}
		return nil
	})
}

// LoadPublished returns the currently published generation ID and its manifest.
func (s *Store) LoadPublished() (string, memory.GenerationManifest, error) {
	var genID string
	var manifest memory.GenerationManifest
	err := s.withStoreLock(func() error {
		root, err := s.reopenMemory()
		if err != nil {
			return err
		}
		defer root.Close()

		body, found, err := root.ReadRegular("published_generation", maxManifestBytes)
		if err != nil {
			return fmt.Errorf("read published generation: %w", err)
		}
		if !found {
			return ErrNoPublishedGeneration
		}
		if err := requirePrivateRegular(root.Root, "published_generation"); err != nil {
			return err
		}
		genID = strings.TrimSpace(string(body))
		if err := validateStoreID(genID); err != nil {
			return fmt.Errorf("corrupt published generation pointer: %w", err)
		}
		manifest, err = s.loadGeneration(genID)
		return err
	})
	return genID, manifest, err
}

func (s *Store) loadPreparedUnlocked() (Prepared, memory.GenerationManifest, error) {
	if err := s.reconcilePreparedAdvanceUnlocked(); err != nil {
		return Prepared{}, memory.GenerationManifest{}, err
	}
	return s.loadPreparedRawUnlocked()
}

func (s *Store) loadPreparedRawUnlocked() (Prepared, memory.GenerationManifest, error) {
	root, err := s.reopenMemory()
	if err != nil {
		return Prepared{}, memory.GenerationManifest{}, err
	}
	defer root.Close()
	if err := rejectManifestBackup(root); err != nil {
		return Prepared{}, memory.GenerationManifest{}, err
	}
	body, found, err := root.ReadRegular("manifest.json", maxManifestBytes)
	if err != nil {
		return Prepared{}, memory.GenerationManifest{}, fmt.Errorf("read prepared pointer: %w", err)
	}
	if !found {
		return Prepared{}, memory.GenerationManifest{}, ErrNoPreparedGeneration
	}
	if err := requirePrivateRegular(root.Root, "manifest.json"); err != nil {
		return Prepared{}, memory.GenerationManifest{}, err
	}
	var prepared Prepared
	if err := decodeCanonicalJSON(body, &prepared); err != nil {
		return Prepared{}, memory.GenerationManifest{}, fmt.Errorf("decode prepared pointer: %w", err)
	}
	if err := validatePrepared(prepared); err != nil {
		return Prepared{}, memory.GenerationManifest{}, err
	}
	manifest, err := s.loadGeneration(prepared.GenerationID)
	if err != nil {
		return Prepared{}, memory.GenerationManifest{}, err
	}
	digest, err := memory.Digest(manifest)
	if err != nil || digest != prepared.ManifestDigest || manifest.ProjectViewDigest != prepared.ProjectViewDigest {
		return Prepared{}, memory.GenerationManifest{}, errors.New("prepared pointer does not match immutable generation")
	}
	if err := s.reconcileGenerationGraph(manifest); err != nil {
		return Prepared{}, memory.GenerationManifest{}, err
	}
	return prepared, manifest, nil
}

func (s *Store) loadGeneration(generationID string) (memory.GenerationManifest, error) {
	if err := validateStoreID(generationID); err != nil {
		return memory.GenerationManifest{}, errors.New("prepared generation ID is invalid")
	}
	generations, err := s.openCollection("generations")
	if err != nil {
		return memory.GenerationManifest{}, err
	}
	defer generations.Close()
	body, found, err := generations.ReadRegular(generationID+".json", maxManifestBytes)
	if err != nil {
		return memory.GenerationManifest{}, fmt.Errorf("read immutable generation: %w", err)
	}
	if !found {
		return memory.GenerationManifest{}, errors.New("prepared generation object is missing")
	}
	if err := requirePrivateRegular(generations.Root, generationID+".json"); err != nil {
		return memory.GenerationManifest{}, err
	}
	return decodeGeneration(body, s.projectID, generationID, "")
}

// LoadObject returns a defensive copy of one validated canonical object.
func (s *Store) LoadObject(kind ObjectKind, digest string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.requireOpenLocked(); err != nil {
		return nil, err
	}
	collection, suffix, err := objectLocation(kind, digest)
	if err != nil {
		return nil, err
	}
	parent, err := s.openCollection(collection)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	leaf := digestLeafName(digest, suffix)
	body, found, err := parent.ReadRegular(leaf, maxObjectBytes)
	if err != nil {
		return nil, fmt.Errorf("read immutable object: %w", err)
	}
	if !found {
		return nil, os.ErrNotExist
	}
	if err := requirePrivateRegular(parent.Root, leaf); err != nil {
		return nil, err
	}
	if err := validateObjectBytes(kind, digest, body, s.projectID); err != nil {
		return nil, err
	}
	return bytes.Clone(body), nil
}

func validateObjectBytesContext(ctx context.Context, kind ObjectKind, digest string, body []byte, projectID string) error {
	_, err := decodeValidatedObjectBytesContext(ctx, kind, digest, body, projectID)
	return err
}

type decodedStoredObject struct {
	observations []memory.ObservationRevision
	session      memory.SessionView
	lineage      memory.SessionLineage
	probe        memory.ProjectProbeState
	project      memory.ProjectView
}

func decodeValidatedObjectBytesContext(ctx context.Context, kind ObjectKind, digest string, body []byte, projectID string) (decodedStoredObject, error) {
	if err := context.Cause(ctx); err != nil {
		return decodedStoredObject{}, err
	}
	switch kind {
	case ObjectObservationChunk:
		records, err := decodeObservationChunkContext(ctx, body)
		if err != nil {
			return decodedStoredObject{}, err
		}
		for index := range records {
			if err := storeCheckpoint(ctx, "validation"); err != nil {
				return decodedStoredObject{}, err
			}
			if records[index].Key.ProjectID != projectID {
				return decodedStoredObject{}, errors.New("observation chunk contains a different project")
			}
		}
		actual, err := memory.DigestContext(ctx, records)
		if cause := context.Cause(ctx); cause != nil {
			return decodedStoredObject{}, cause
		}
		if err != nil || actual != digest {
			return decodedStoredObject{}, errors.Join(errors.New("observation chunk digest mismatch"), err)
		}
		return decodedStoredObject{observations: records}, nil
	case ObjectSessionView:
		var value memory.SessionView
		if err := decodeCanonicalJSONContext(ctx, body, &value); err != nil {
			return decodedStoredObject{}, err
		}
		if err := storeCheckpoint(ctx, "validation"); err != nil {
			return decodedStoredObject{}, err
		}
		validationErr := memory.ValidateSessionViewContext(ctx, value)
		if cause := context.Cause(ctx); cause != nil {
			return decodedStoredObject{}, cause
		}
		if validationErr != nil || value.Digest != digest || value.ProjectID != projectID {
			return decodedStoredObject{}, errors.Join(errors.New("invalid stored SessionView"), validationErr)
		}
		return decodedStoredObject{session: value}, nil
	case ObjectSessionLineage:
		var value memory.SessionLineage
		if err := decodeCanonicalJSONContext(ctx, body, &value); err != nil {
			return decodedStoredObject{}, err
		}
		if err := storeCheckpoint(ctx, "validation"); err != nil {
			return decodedStoredObject{}, err
		}
		validationErr := memory.ValidateSessionLineageContext(ctx, value)
		if cause := context.Cause(ctx); cause != nil {
			return decodedStoredObject{}, cause
		}
		if validationErr != nil || value.Digest != digest || value.ProjectID != projectID {
			return decodedStoredObject{}, errors.Join(errors.New("invalid stored SessionLineage"), validationErr)
		}
		return decodedStoredObject{lineage: value}, nil
	case ObjectProbeState:
		var value memory.ProjectProbeState
		if err := decodeCanonicalJSONContext(ctx, body, &value); err != nil {
			return decodedStoredObject{}, err
		}
		if err := storeCheckpoint(ctx, "validation"); err != nil {
			return decodedStoredObject{}, err
		}
		validationErr := memory.ValidateProjectProbeStateContext(ctx, value)
		if cause := context.Cause(ctx); cause != nil {
			return decodedStoredObject{}, cause
		}
		if validationErr != nil || value.Digest != digest || value.ProjectID != projectID {
			return decodedStoredObject{}, errors.Join(errors.New("invalid stored ProjectProbeState"), validationErr)
		}
		return decodedStoredObject{probe: value}, nil
	case ObjectProjectView:
		var value memory.ProjectView
		if err := decodeCanonicalJSONContext(ctx, body, &value); err != nil {
			return decodedStoredObject{}, err
		}
		if err := storeCheckpoint(ctx, "validation"); err != nil {
			return decodedStoredObject{}, err
		}
		validationErr := memory.ValidateProjectViewContext(ctx, value)
		if cause := context.Cause(ctx); cause != nil {
			return decodedStoredObject{}, cause
		}
		if validationErr != nil || value.Digest != digest || value.ProjectID != projectID {
			return decodedStoredObject{}, errors.Join(errors.New("invalid stored ProjectView"), validationErr)
		}
		return decodedStoredObject{project: value}, nil
	default:
		return decodedStoredObject{}, errors.New("unknown immutable object kind")
	}
}

// Close releases pinned roots. It is safe to call more than once.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(s.memory.Close(), s.data.Close())
}

// withStoreLock keeps the legacy five-second wait when no cancellable context
// is supplied. Retention may pass one context; acquisition then uses repeated
// authenticated non-blocking attempts and observes cancellation between them.
func (s *Store) withStoreLock(run func() error, contexts ...context.Context) error {
	ctx := context.Background()
	if len(contexts) > 1 || (len(contexts) == 1 && contexts[0] == nil) {
		return errors.New("memory store lock accepts at most one non-nil context")
	}
	if len(contexts) == 1 {
		ctx = contexts[0]
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.requireOpenLocked(); err != nil {
		return err
	}
	var lock *project.ProjectLock
	for {
		timeout := storeLockTimeout
		if ctx.Done() != nil {
			timeout = 0
		}
		var err error
		lock, err = project.AcquireProjectLock(s.memory.Root, "locks/scan.lock", timeout)
		if err == nil {
			break
		}
		if ctx.Done() == nil || !errors.Is(err, project.ErrProjectLocked) {
			return fmt.Errorf("acquire memory store lock: %w", err)
		}
		timer := time.NewTimer(storeLockContextPollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return context.Cause(ctx)
		case <-timer.C:
		}
	}
	runErr := context.Cause(ctx)
	if runErr == nil {
		runErr = run()
	}
	return errors.Join(runErr, lock.Release())
}

func (s *Store) requireOpenLocked() error {
	if s == nil || s.closed || s.data == nil || s.memory == nil {
		return errors.New("memory store is closed")
	}
	return nil
}

func (s *Store) reopenMemory() (*pathguard.Directory, error) {
	current, err := pathguard.Open(s.memory.Path)
	if err != nil {
		return nil, fmt.Errorf("reopen project memory root: %w", err)
	}
	if !os.SameFile(s.memory.Info(), current.Info()) {
		_ = current.Close()
		return nil, errors.New("project memory root identity changed")
	}
	return current, nil
}

func (s *Store) openCollection(name string) (*pathguard.Directory, error) {
	if _, _, err := objectLocation(ObjectKind(name), prefixedZeroDigest()); err != nil && name != "generations" {
		return nil, errors.New("invalid private collection")
	}
	current, err := pathguard.Open(filepath.Join(s.memory.Path, name))
	if err != nil {
		return nil, fmt.Errorf("open private collection: %w", err)
	}
	if len(current.Ancestors) < 2 || !os.SameFile(s.memory.Info(), current.Ancestors[len(current.Ancestors)-2]) {
		_ = current.Close()
		return nil, errors.New("private collection escaped project memory root")
	}
	return current, nil
}

func (s *Store) reconcileGenerationGraph(value memory.GenerationManifest) error {
	return s.reconcileGenerationGraphContext(context.Background(), value)
}

func (s *Store) reconcileGenerationGraphContext(ctx context.Context, value memory.GenerationManifest) error {
	return reconcileGenerationGraphObjectsContext(ctx, value, storedGenerationGraphObjects{store: s})
}

type generationGraphObjects interface {
	observationChunk(context.Context, string) ([]memory.ObservationRevision, error)
	sessionView(context.Context, memory.SessionViewDependency) (memory.SessionView, error)
	sessionLineage(context.Context, memory.SessionLineageDependency) (memory.SessionLineage, error)
	probeState(context.Context, string) (memory.ProjectProbeState, error)
	projectView(context.Context, string) (memory.ProjectView, error)
}

type storedGenerationGraphObjects struct{ store *Store }

func (objects storedGenerationGraphObjects) observationChunk(ctx context.Context, digest string) ([]memory.ObservationRevision, error) {
	body, err := objects.store.loadObjectUnlockedContext(ctx, ObjectObservationChunk, digest)
	if err != nil {
		return nil, fmt.Errorf("verify observation chunk %s: %w", digest, err)
	}
	records, err := decodeObservationChunkContext(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("decode observation chunk %s: %w", digest, err)
	}
	return records, nil
}

func (objects storedGenerationGraphObjects) sessionView(ctx context.Context, dependency memory.SessionViewDependency) (memory.SessionView, error) {
	body, err := objects.store.loadObjectUnlockedContext(ctx, ObjectSessionView, dependency.Digest)
	if err != nil {
		return memory.SessionView{}, fmt.Errorf("verify SessionView %s: %w", dependency.Digest, err)
	}
	var view memory.SessionView
	if err := decodeCanonicalJSONContext(ctx, body, &view); err != nil || view.Provider != dependency.Provider || view.SessionID != dependency.SessionID {
		if cause := context.Cause(ctx); cause != nil {
			return memory.SessionView{}, cause
		}
		return memory.SessionView{}, errors.Join(errors.New("SessionView dependency identity mismatch"), err)
	}
	return view, nil
}

func (objects storedGenerationGraphObjects) sessionLineage(ctx context.Context, dependency memory.SessionLineageDependency) (memory.SessionLineage, error) {
	body, err := objects.store.loadObjectUnlockedContext(ctx, ObjectSessionLineage, dependency.Digest)
	if err != nil {
		return memory.SessionLineage{}, fmt.Errorf("verify SessionLineage %s: %w", dependency.Digest, err)
	}
	var lineage memory.SessionLineage
	if err := decodeCanonicalJSONContext(ctx, body, &lineage); err != nil || lineage.Provider != dependency.Provider || lineage.SessionID != dependency.SessionID {
		if cause := context.Cause(ctx); cause != nil {
			return memory.SessionLineage{}, cause
		}
		return memory.SessionLineage{}, errors.Join(errors.New("SessionLineage dependency identity mismatch"), err)
	}
	return lineage, nil
}

func (objects storedGenerationGraphObjects) probeState(ctx context.Context, digest string) (memory.ProjectProbeState, error) {
	body, err := objects.store.loadObjectUnlockedContext(ctx, ObjectProbeState, digest)
	if err != nil {
		return memory.ProjectProbeState{}, fmt.Errorf("verify ProjectProbeState: %w", err)
	}
	var probe memory.ProjectProbeState
	if err := decodeCanonicalJSONContext(ctx, body, &probe); err != nil {
		return memory.ProjectProbeState{}, fmt.Errorf("decode ProjectProbeState: %w", err)
	}
	return probe, nil
}

func (objects storedGenerationGraphObjects) projectView(ctx context.Context, digest string) (memory.ProjectView, error) {
	body, err := objects.store.loadObjectUnlockedContext(ctx, ObjectProjectView, digest)
	if err != nil {
		return memory.ProjectView{}, fmt.Errorf("verify ProjectView: %w", err)
	}
	var view memory.ProjectView
	if err := decodeCanonicalJSONContext(ctx, body, &view); err != nil {
		return memory.ProjectView{}, fmt.Errorf("decode ProjectView: %w", err)
	}
	return view, nil
}

func reconcileGenerationGraphObjectsContext(ctx context.Context, value memory.GenerationManifest, objects generationGraphObjects) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	sourceRecords := make(map[string]bool, len(value.SourceRecordDigests))
	for _, digest := range value.SourceRecordDigests {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		sourceRecords[digest] = false
	}
	projectView, err := objects.projectView(ctx, value.ProjectViewDigest)
	if err != nil {
		return err
	}
	dependenciesMatch, err := equalSessionViewDependenciesContext(ctx, projectView.SessionViewDependencies, value.SessionViews)
	if err != nil {
		return err
	}
	if !dependenciesMatch {
		return errors.New("ProjectView ordered SessionView dependencies do not match manifest")
	}
	evidenceRemaining := make(map[string]struct{}, len(projectView.ObservationRevisionIDs))
	for _, revisionID := range projectView.ObservationRevisionIDs {
		evidenceRemaining[revisionID] = struct{}{}
	}
	lineageBySession := make(map[string]memory.SessionLineageDependency, len(value.SessionLineages))
	for _, dependency := range value.SessionLineages {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		key := dependency.Provider + "\x00" + dependency.SessionID
		if _, duplicate := lineageBySession[key]; duplicate {
			return errors.New("duplicate SessionLineage dependency identity")
		}
		lineageBySession[key] = dependency
	}
	for _, dependency := range value.SessionViews {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		view, err := objects.sessionView(ctx, dependency)
		if err != nil {
			return err
		}
		if used, exists := sourceRecords[view.SourceRecordDigest]; !exists || used {
			return errors.New("SessionView source record does not resolve uniquely through manifest")
		}
		sourceRecords[view.SourceRecordDigest] = true
		lineageKey := view.Provider + "\x00" + view.SessionID
		lineageDependency, exists := lineageBySession[lineageKey]
		if !exists {
			return errors.New("SessionView has no matching SessionLineage dependency")
		}
		delete(lineageBySession, lineageKey)
		lineage, err := objects.sessionLineage(ctx, lineageDependency)
		if err != nil {
			return err
		}
		if lineage.ProjectID != view.ProjectID || lineage.Provider != view.Provider || lineage.SessionID != view.SessionID || lineage.SourceIdentity != view.SourceIdentity {
			return errors.New("SessionLineage identity does not match SessionView")
		}
		activeByRevision := make(map[string]string, len(lineage.ActiveRevisions))
		for keyDigest, revisionID := range lineage.ActiveRevisions {
			activeByRevision[revisionID] = keyDigest
		}
		viewActive := make(map[string]struct{}, len(view.ActiveRevisionIDs))
		for _, revisionID := range view.ActiveRevisionIDs {
			viewActive[revisionID] = struct{}{}
			if _, exists := activeByRevision[revisionID]; !exists {
				return errors.New("SessionView active revision is absent from SessionLineage")
			}
		}
		if len(viewActive) != len(activeByRevision) {
			return errors.New("SessionView and SessionLineage active revision sets disagree")
		}
		unresolved := make(map[string]string, len(activeByRevision))
		for revisionID, keyDigest := range activeByRevision {
			unresolved[revisionID] = keyDigest
		}
		seenChunks := make(map[string]struct{}, len(view.ObservationChunkDigests))
		for _, chunkDigest := range view.ObservationChunkDigests {
			if err := context.Cause(ctx); err != nil {
				return err
			}
			if _, duplicate := seenChunks[chunkDigest]; duplicate {
				return errors.New("SessionView repeats an observation chunk")
			}
			seenChunks[chunkDigest] = struct{}{}
			records, err := objects.observationChunk(ctx, chunkDigest)
			if err != nil {
				return err
			}
			for _, record := range records {
				if err := context.Cause(ctx); err != nil {
					return err
				}
				if record.Key.Provider != view.Provider || record.Key.SessionID != view.SessionID {
					return errors.New("SessionView observation chunk belongs to a different session")
				}
				keyDigest, active := unresolved[record.RevisionID]
				if !active {
					continue
				}
				actualKey, digestErr := memory.DigestContext(ctx, record.Key)
				if digestErr != nil || actualKey != keyDigest {
					return errors.Join(errors.New("SessionLineage active revision does not resolve to its observation key"), digestErr)
				}
				delete(unresolved, record.RevisionID)
				delete(evidenceRemaining, record.RevisionID)
			}
		}
		if len(unresolved) != 0 {
			return errors.New("SessionLineage active revision is absent from SessionView chunks")
		}
		if lineage.PreviousLineageDigest != "" {
			previous, err := objects.sessionLineage(ctx, memory.SessionLineageDependency{Provider: lineage.Provider, SessionID: lineage.SessionID, Digest: lineage.PreviousLineageDigest})
			if err != nil {
				return err
			}
			if previous.ProjectID != lineage.ProjectID || previous.SourceIdentity != lineage.SourceIdentity {
				return errors.New("SessionLineage predecessor identity mismatch")
			}
			expectedSuperseded, expectedWithdrawn := 0, 0
			for keyDigest, oldRevision := range previous.ActiveRevisions {
				if currentRevision, exists := lineage.ActiveRevisions[keyDigest]; exists {
					if currentRevision != oldRevision {
						expectedSuperseded++
						if lineage.SupersededRevisions[oldRevision] != currentRevision {
							return errors.New("SessionLineage supersession delta does not match predecessor")
						}
					}
				} else {
					expectedWithdrawn++
					if lineage.WithdrawnRevisions[keyDigest] != oldRevision {
						return errors.New("SessionLineage withdrawal delta does not match predecessor")
					}
				}
			}
			if expectedSuperseded != len(lineage.SupersededRevisions) || expectedWithdrawn != len(lineage.WithdrawnRevisions) {
				return errors.New("SessionLineage transition contains an extraneous classification")
			}
		}
	}
	if len(lineageBySession) != 0 {
		return errors.New("GenerationManifest has an unmatched SessionLineage dependency")
	}
	for digest, used := range sourceRecords {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if !used {
			return fmt.Errorf("manifest source record %s is not referenced by a SessionView", digest)
		}
	}
	if len(evidenceRemaining) != 0 {
		return errors.New("ProjectView selected observation evidence does not resolve through active Session lineages")
	}

	probe, err := objects.probeState(ctx, value.ProbeStateDigest)
	if err != nil {
		return err
	}
	if projectView.ProbeStateDigest != value.ProbeStateDigest || probe.Digest != value.ProbeStateDigest {
		return errors.New("ProjectView probe dependency does not match manifest")
	}
	return nil
}

const graphComparisonCheckpointInterval = 256

func equalSessionViewDependenciesContext(ctx context.Context, first, second []memory.SessionViewDependency) (bool, error) {
	if err := graphComparisonCheckpoint(ctx, "dependency_structural_compare", 0); err != nil {
		return false, err
	}
	if len(first) != len(second) {
		return false, nil
	}
	for index := range first {
		if err := graphComparisonCheckpoint(ctx, "dependency_structural_compare", index); err != nil {
			return false, err
		}
		if first[index].Provider != second[index].Provider || first[index].SessionID != second[index].SessionID || first[index].Digest != second[index].Digest {
			return false, nil
		}
	}
	if err := storeCheckpoint(ctx, "dependency_structural_compare"); err != nil {
		return false, err
	}
	return true, nil
}

func equalDigestSetContext(ctx context.Context, first, second map[string]struct{}) (bool, error) {
	if err := graphComparisonCheckpoint(ctx, "revision_set_compare", 0); err != nil {
		return false, err
	}
	if len(first) != len(second) {
		return false, nil
	}
	index := 0
	for digest := range first {
		if err := graphComparisonCheckpoint(ctx, "revision_set_compare", index); err != nil {
			return false, err
		}
		if _, exists := second[digest]; !exists {
			return false, nil
		}
		index++
	}
	if err := storeCheckpoint(ctx, "revision_set_compare"); err != nil {
		return false, err
	}
	return true, nil
}

func equalDigestSliceSetContext(ctx context.Context, values []string, expected map[string]struct{}) (bool, error) {
	if err := graphComparisonCheckpoint(ctx, "digest_slice_set_compare", 0); err != nil {
		return false, err
	}
	if len(values) != len(expected) {
		return false, nil
	}
	for index, digest := range values {
		if err := graphComparisonCheckpoint(ctx, "digest_slice_set_compare", index); err != nil {
			return false, err
		}
		if _, exists := expected[digest]; !exists {
			return false, nil
		}
	}
	if err := storeCheckpoint(ctx, "digest_slice_set_compare"); err != nil {
		return false, err
	}
	return true, nil
}

func graphComparisonCheckpoint(ctx context.Context, phase string, index int) error {
	if index%graphComparisonCheckpointInterval != 0 {
		return nil
	}
	return storeCheckpoint(ctx, phase)
}

func (s *Store) loadObjectUnlocked(kind ObjectKind, digest string) ([]byte, error) {
	return s.loadObjectUnlockedContext(context.Background(), kind, digest)
}

func (s *Store) loadObjectUnlockedContext(ctx context.Context, kind ObjectKind, digest string) ([]byte, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	collection, suffix, err := objectLocation(kind, digest)
	if err != nil {
		return nil, err
	}
	parent, err := s.openCollection(collection)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	leaf := digestLeafName(digest, suffix)
	if retentionObjectReadCheckpoint != nil {
		retentionObjectReadCheckpoint(kind, digest)
	}
	body, found, err := parent.ReadRegularContext(ctx, leaf, maxObjectBytes)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, os.ErrNotExist
	}
	if err := requirePrivateRegular(parent.Root, leaf); err != nil {
		return nil, err
	}
	if err := validateObjectBytesContext(ctx, kind, digest, body, s.projectID); err != nil {
		return nil, err
	}
	if retentionObjectReconcileCheckpoint != nil {
		retentionObjectReconcileCheckpoint(kind, digest)
	}
	return body, nil
}

func validateObjectBytes(kind ObjectKind, digest string, body []byte, projectID string) error {
	switch kind {
	case ObjectObservationChunk:
		records, err := decodeObservationChunk(body)
		if err != nil {
			return err
		}
		for index := range records {
			if records[index].Key.ProjectID != projectID {
				return errors.New("observation chunk contains a different project")
			}
		}
		actual, err := memory.Digest(records)
		if err != nil || actual != digest {
			return errors.Join(errors.New("observation chunk digest mismatch"), err)
		}
	case ObjectSessionView:
		var value memory.SessionView
		if err := decodeCanonicalJSON(body, &value); err != nil {
			return err
		}
		if err := memory.ValidateSessionView(value); err != nil || value.Digest != digest || value.ProjectID != projectID {
			return errors.Join(errors.New("invalid stored SessionView"), err)
		}
	case ObjectSessionLineage:
		var value memory.SessionLineage
		if err := decodeCanonicalJSON(body, &value); err != nil {
			return err
		}
		if err := memory.ValidateSessionLineage(value); err != nil || value.Digest != digest || value.ProjectID != projectID {
			return errors.Join(errors.New("invalid stored SessionLineage"), err)
		}
	case ObjectProbeState:
		var value memory.ProjectProbeState
		if err := decodeCanonicalJSON(body, &value); err != nil {
			return err
		}
		if err := memory.ValidateProjectProbeState(value); err != nil || value.Digest != digest || value.ProjectID != projectID {
			return errors.Join(errors.New("invalid stored ProjectProbeState"), err)
		}
	case ObjectProjectView:
		var value memory.ProjectView
		if err := decodeCanonicalJSON(body, &value); err != nil {
			return err
		}
		if err := memory.ValidateProjectView(value); err != nil || value.Digest != digest || value.ProjectID != projectID {
			return errors.Join(errors.New("invalid stored ProjectView"), err)
		}
	default:
		return errors.New("unknown immutable object kind")
	}
	return nil
}

func decodeObservationChunk(body []byte) ([]memory.ObservationRevision, error) {
	return decodeObservationChunkContext(context.Background(), body)
}

func decodeObservationChunkContext(ctx context.Context, body []byte) ([]memory.ObservationRevision, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if len(body) == 0 || body[len(body)-1] != '\n' {
		return nil, errors.New("observation chunk is not canonical JSONL")
	}
	scanner := bufio.NewScanner(&storeContextReader{ctx: ctx, reader: bytes.NewReader(body), phase: "decode"})
	scanner.Buffer(make([]byte, 64*1024), maxObjectBytes)
	var records []memory.ObservationRevision
	for scanner.Scan() {
		if err := storeCheckpoint(ctx, "decode"); err != nil {
			return nil, err
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			return nil, errors.New("observation chunk contains an empty record")
		}
		var value memory.ObservationRevision
		if err := decodeCanonicalJSONContext(ctx, append(bytes.Clone(line), '\n'), &value); err != nil {
			return nil, err
		}
		if err := storeCheckpoint(ctx, "validation"); err != nil {
			return nil, err
		}
		if err := memory.ValidateObservationRevisionContext(ctx, value); err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return nil, cause
			}
			return nil, fmt.Errorf("invalid stored observation: %w", err)
		}
		records = append(records, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan observation chunk: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("observation chunk is empty")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return records, nil
}

func decodeGeneration(body []byte, projectID, generationID, expectedDigest string) (memory.GenerationManifest, error) {
	return decodeGenerationContext(context.Background(), body, projectID, generationID, expectedDigest)
}

func decodeGenerationContext(ctx context.Context, body []byte, projectID, generationID, expectedDigest string) (memory.GenerationManifest, error) {
	var value memory.GenerationManifest
	if err := decodeCanonicalJSONContext(ctx, body, &value); err != nil {
		return memory.GenerationManifest{}, fmt.Errorf("decode immutable generation: %w", err)
	}
	if err := storeCheckpoint(ctx, "validation"); err != nil {
		return memory.GenerationManifest{}, err
	}
	if err := memory.ValidateGenerationManifestContext(ctx, value); err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return memory.GenerationManifest{}, cause
		}
		return memory.GenerationManifest{}, fmt.Errorf("validate immutable generation: %w", err)
	}
	if value.ProjectID != projectID || value.GenerationID != generationID {
		return memory.GenerationManifest{}, errors.New("immutable generation identity mismatch")
	}
	if expectedDigest != "" {
		digest, err := memory.DigestContext(ctx, value)
		if cause := context.Cause(ctx); cause != nil {
			return memory.GenerationManifest{}, cause
		}
		if err != nil || digest != expectedDigest {
			return memory.GenerationManifest{}, errors.Join(errors.New("immutable generation digest mismatch"), err)
		}
	}
	return value, nil
}

func decodeCanonicalJSON(body []byte, destination any) error {
	return decodeCanonicalJSONContext(context.Background(), body, destination)
}

func decodeCanonicalJSONContext(ctx context.Context, body []byte, destination any) error {
	if err := storeCheckpoint(ctx, "decode"); err != nil {
		return err
	}
	if len(body) == 0 || len(body) > maxObjectBytes {
		return errors.New("JSON object is empty or oversized")
	}
	if err := rejectDuplicateJSONFieldsContext(ctx, body); err != nil {
		return err
	}
	decoder := json.NewDecoder(&storeContextReader{ctx: ctx, reader: bytes.NewReader(body), phase: "decode"})
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := requireJSONEOFContext(ctx, decoder); err != nil {
		return err
	}
	if err := storeCheckpoint(ctx, "decode"); err != nil {
		return err
	}
	comparison := canonicalJSONComparison{expected: body}
	if err := memory.WriteCanonicalJSONContext(ctx, &comparison, destination); err != nil {
		return err
	}
	if err := storeCheckpoint(ctx, "decode"); err != nil {
		return err
	}
	if _, err := comparison.Write([]byte{'\n'}); err != nil {
		return err
	}
	if !comparison.matches() {
		return errors.New("JSON object is not in canonical stored form")
	}
	return nil
}

type canonicalJSONComparison struct {
	expected []byte
	offset   int
	mismatch bool
}

func (comparison *canonicalJSONComparison) Write(body []byte) (int, error) {
	start := comparison.offset
	comparison.offset += len(body)
	if start > len(comparison.expected) || len(body) > len(comparison.expected)-start {
		comparison.mismatch = true
		return len(body), nil
	}
	if !bytes.Equal(body, comparison.expected[start:comparison.offset]) {
		comparison.mismatch = true
	}
	return len(body), nil
}

func (comparison *canonicalJSONComparison) matches() bool {
	return !comparison.mismatch && comparison.offset == len(comparison.expected)
}

func rejectDuplicateJSONFields(body []byte) error {
	return rejectDuplicateJSONFieldsContext(context.Background(), body)
}

func rejectDuplicateJSONFieldsContext(ctx context.Context, body []byte) error {
	decoder := json.NewDecoder(&storeContextReader{ctx: ctx, reader: bytes.NewReader(body), phase: "decode"})
	decoder.UseNumber()
	if err := scanJSONValueContext(ctx, decoder); err != nil {
		return err
	}
	return requireJSONEOFContext(ctx, decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	return scanJSONValueContext(context.Background(), decoder)
}

func scanJSONValueContext(ctx context.Context, decoder *json.Decoder) error {
	if err := storeCheckpoint(ctx, "decode"); err != nil {
		return err
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			if err := storeCheckpoint(ctx, "decode"); err != nil {
				return err
			}
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("JSON object member name is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("duplicate JSON field %q", name)
			}
			seen[name] = struct{}{}
			if err := scanJSONValueContext(ctx, decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValueContext(ctx, decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	return requireJSONEOFContext(context.Background(), decoder)
}

func requireJSONEOFContext(ctx context.Context, decoder *json.Decoder) error {
	if err := storeCheckpoint(ctx, "decode"); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON object contains trailing data")
		}
		return err
	}
	return nil
}

type storeContextReader struct {
	ctx    context.Context
	reader io.Reader
	phase  string
}

func (reader *storeContextReader) Read(buffer []byte) (int, error) {
	if err := storeCheckpoint(reader.ctx, reader.phase); err != nil {
		return 0, err
	}
	if len(buffer) > storeContextReadChunkSize {
		buffer = buffer[:storeContextReadChunkSize]
	}
	return reader.reader.Read(buffer)
}

func storeCheckpoint(ctx context.Context, phase string) error {
	if ctx == nil {
		return errors.New("memory store context is required")
	}
	if storeContextCheckpoint != nil {
		storeContextCheckpoint(phase)
	}
	return context.Cause(ctx)
}

func marshalCanonical(value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode canonical JSON: %w", err)
	}
	body = append(body, '\n')
	return body, nil
}

func validatePrepared(value Prepared) error {
	if err := validateStoreID(value.GenerationID); err != nil || !digestPattern.MatchString(value.ManifestDigest) || !digestPattern.MatchString(value.ProjectViewDigest) {
		return errors.New("prepared pointer is invalid")
	}
	return nil
}

func (s *Store) reconcilePreparedAdvanceUnlocked() error {
	root, err := s.reopenMemory()
	if err != nil {
		return err
	}
	defer root.Close()
	journalBody, journalFound, err := root.ReadRegular(preparedAdvanceJournalLeaf, maxManifestBytes)
	if err != nil {
		return fmt.Errorf("read prepared advance journal: %w", err)
	}
	backupLeaf := atomicfile.BackupPath("manifest.json")
	backupBody, backupFound, err := root.ReadRegular(backupLeaf, maxManifestBytes)
	if err != nil {
		return fmt.Errorf("read prepared rollback backup: %w", err)
	}
	manifestBody, manifestFound, err := root.ReadRegular("manifest.json", maxManifestBytes)
	if err != nil {
		return fmt.Errorf("read prepared pointer during recovery: %w", err)
	}
	if !journalFound {
		if !backupFound {
			return nil
		}
		if !manifestFound || !bytes.Equal(manifestBody, backupBody) {
			return errors.New("prepared manifest rollback backup requires journal recovery")
		}
		if err := requirePrivateRegular(root.Root, "manifest.json"); err != nil {
			return err
		}
		if err := requirePrivateRegular(root.Root, backupLeaf); err != nil {
			return err
		}
		if err := atomicfile.RemoveRoot(root.Root, backupLeaf); err != nil {
			return fmt.Errorf("remove stale prepared rollback alias: %w", err)
		}
		return nil
	}
	if err := requirePrivateRegular(root.Root, preparedAdvanceJournalLeaf); err != nil {
		return err
	}
	var journal preparedAdvanceJournal
	if err := decodeCanonicalJSON(journalBody, &journal); err != nil {
		return fmt.Errorf("decode prepared advance journal: %w", err)
	}
	if journal.Version != 1 || journal.ProjectID != s.projectID || journal.Expected == journal.Successor {
		return errors.New("prepared advance journal identity is invalid")
	}
	if _, err := s.validatePreparedTarget(journal.Expected); err != nil {
		return fmt.Errorf("validate expected prepared generation: %w", err)
	}
	if _, err := s.validatePreparedTarget(journal.Successor); err != nil {
		return fmt.Errorf("validate successor prepared generation: %w", err)
	}
	expectedBody, err := marshalCanonical(journal.Expected)
	if err != nil {
		return err
	}
	successorBody, err := marshalCanonical(journal.Successor)
	if err != nil {
		return err
	}
	if manifestFound {
		if err := requirePrivateRegular(root.Root, "manifest.json"); err != nil {
			return err
		}
	}
	selectedBody := manifestBody
	if !manifestFound {
		if !backupFound || !bytes.Equal(backupBody, expectedBody) {
			return errors.New("prepared advance lost both pointer and expected rollback state")
		}
		selectedBody = expectedBody
		if err := atomicfile.WriteRootFile(root.Root, "manifest.json", selectedBody, privateFileMode); err != nil {
			return fmt.Errorf("restore expected prepared pointer: %w", err)
		}
	} else if !bytes.Equal(manifestBody, expectedBody) && !bytes.Equal(manifestBody, successorBody) {
		return errors.New("prepared pointer matches neither journal generation")
	}
	if backupFound {
		if !bytes.Equal(backupBody, expectedBody) {
			return errors.New("prepared rollback backup does not match journal expected pointer")
		}
		if err := requirePrivateRegular(root.Root, backupLeaf); err != nil {
			return err
		}
		if err := atomicfile.RemoveRoot(root.Root, backupLeaf); err != nil {
			return fmt.Errorf("remove recovered prepared rollback backup: %w", err)
		}
	}
	verified, found, err := root.ReadRegular("manifest.json", maxManifestBytes)
	if err != nil || !found || !bytes.Equal(verified, selectedBody) {
		return errors.Join(errors.New("recovered prepared pointer verification failed"), err)
	}
	if err := atomicfile.RemoveRoot(root.Root, preparedAdvanceJournalLeaf); err != nil {
		return fmt.Errorf("remove recovered prepared advance journal: %w", err)
	}
	return nil
}

func (s *Store) validatePreparedTarget(prepared Prepared) (memory.GenerationManifest, error) {
	if err := validatePrepared(prepared); err != nil {
		return memory.GenerationManifest{}, err
	}
	manifest, err := s.loadGeneration(prepared.GenerationID)
	if err != nil {
		return memory.GenerationManifest{}, err
	}
	digest, err := memory.Digest(manifest)
	if err != nil || digest != prepared.ManifestDigest || manifest.ProjectViewDigest != prepared.ProjectViewDigest {
		return memory.GenerationManifest{}, errors.Join(errors.New("prepared target does not match immutable generation"), err)
	}
	if err := s.reconcileGenerationGraph(manifest); err != nil {
		return memory.GenerationManifest{}, err
	}
	return manifest, nil
}

func rejectManifestBackup(root *pathguard.Directory) error {
	_, found, err := root.ReadRegular(atomicfile.BackupPath("manifest.json"), maxManifestBytes)
	if err != nil {
		return fmt.Errorf("inspect prepared rollback backup: %w", err)
	}
	if found {
		return errors.New("prepared manifest rollback backup requires recovery")
	}
	return nil
}

func objectLocation(kind ObjectKind, digest string) (string, string, error) {
	if !digestPattern.MatchString(digest) {
		return "", "", errors.New("invalid immutable object digest")
	}
	switch kind {
	case ObjectObservationChunk:
		return "observations", ".jsonl", nil
	case ObjectSessionView:
		return "sessions", ".json", nil
	case ObjectSessionLineage:
		return "session-lineages", ".json", nil
	case ObjectProbeState:
		return "project-probes", ".json", nil
	case ObjectProjectView:
		return "project-views", ".json", nil
	default:
		return "", "", errors.New("unknown immutable object kind")
	}
}

func digestLeafName(digest, suffix string) string {
	return strings.TrimPrefix(digest, "sha256:") + suffix
}

func validateStoreID(value string) error {
	if !storeIDPattern.MatchString(value) || value == "." || value == ".." || strings.ContainsAny(value, `/\\:`) {
		return errors.New("identity is not a safe path-independent token")
	}
	if _, err := platform.PathKey("windows", platform.CaseSensitive, value); err != nil {
		return errors.New("identity is not portable")
	}
	return nil
}

func protectPinnedDirectory(directory *pathguard.Directory, mode fs.FileMode) error {
	file, err := directory.Root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !os.SameFile(directory.Info(), before) || !before.IsDir() {
		return errors.New("pinned directory identity changed")
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.IsDir() {
		return errors.New("pinned directory identity changed while protecting")
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != mode {
		return errors.New("pinned directory has incorrect private mode")
	}
	return nil
}

func requirePrivateRegular(root *os.Root, leaf string) error {
	info, err := root.Lstat(leaf)
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return errors.Join(errors.New("private store file is not regular"), err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != privateFileMode {
		return errors.New("private store file has incorrect mode")
	}
	return nil
}

func prefixedZeroDigest() string {
	return "sha256:" + strings.Repeat("0", 64)
}

func equalManifest(first, second memory.GenerationManifest) bool {
	firstBody, firstErr := marshalCanonical(first)
	secondBody, secondErr := marshalCanonical(second)
	return firstErr == nil && secondErr == nil && bytes.Equal(firstBody, secondBody)
}
