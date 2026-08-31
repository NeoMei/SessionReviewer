// Package memorystore persists validated zero-token memory records in a
// project-scoped private store.
package memorystore

import (
	"bufio"
	"bytes"
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
	privateDirectoryMode fs.FileMode = 0o700
	privateFileMode      fs.FileMode = 0o600
	maxObjectBytes                   = 64 << 20
	maxManifestBytes                 = 16 << 20
	storeLockTimeout                 = 5 * time.Second

	// WriteRootFileChecked has three checkpoints when publishing a previously
	// absent destination: before temporary creation, before publication, and
	// after durable publication.
	manifestCheckpointCount = 3
)

var (
	ErrNoPreparedGeneration = errors.New("no prepared generation")
	ErrPreparedGeneration   = errors.New("a different prepared generation already exists")
	ErrImmutableConflict    = errors.New("immutable object digest already contains different bytes")

	storeIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// ObjectKind identifies one immutable content-addressed object collection.
type ObjectKind string

const (
	ObjectObservationChunk ObjectKind = "observations"
	ObjectSessionView      ObjectKind = "sessions"
	ObjectProbeState       ObjectKind = "project-probes"
	ObjectProjectView      ObjectKind = "project-views"
)

// Prepared is the durable pointer to one fully verified private generation.
type Prepared struct {
	GenerationID      string `json:"generation_id"`
	ManifestDigest    string `json:"manifest_digest"`
	ProjectViewDigest string `json:"project_view_digest"`
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
	for _, child := range []string{"observations", "sessions", "project-probes", "project-views", "generations", "diagnostics", "staging", "locks"} {
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

	closeData = false
	closeMemory = false
	return &Store{data: data, memory: memoryDirectory, projectID: projectID}, nil
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
		if err := atomicfile.WriteRootFileChecked(parent.Root, leaf, body, privateFileMode, s.objectCheckpoint); err != nil {
			return fmt.Errorf("write immutable object: %w", err)
		}
		stored, found, err := parent.ReadRegular(leaf, maxObjectBytes)
		if err != nil || !found || !bytes.Equal(stored, body) {
			return errors.Join(errors.New("immutable object canonical re-read failed"), err)
		}
		if err := requirePrivateRegular(parent.Root, leaf); err != nil {
			return err
		}
		return validateObjectBytes(kind, digest, stored, s.projectID)
	})
}

// PrepareGeneration verifies every project object before atomically publishing
// manifest.json as the sole prepared-generation commit point. A different
// already-prepared generation is never replaced by Gate A.
func (s *Store) PrepareGeneration(value memory.GenerationManifest) (Prepared, error) {
	if err := memory.ValidateGenerationManifest(value); err != nil {
		return Prepared{}, fmt.Errorf("invalid generation manifest: %w", err)
	}
	if value.ProjectID != s.projectID {
		return Prepared{}, errors.New("generation manifest belongs to a different project")
	}
	manifestDigest, err := memory.Digest(value)
	if err != nil {
		return Prepared{}, fmt.Errorf("digest generation manifest: %w", err)
	}
	prepared := Prepared{GenerationID: value.GenerationID, ManifestDigest: manifestDigest, ProjectViewDigest: value.ProjectViewDigest}
	manifestBody, err := marshalCanonical(value)
	if err != nil {
		return Prepared{}, err
	}
	pointerBody, err := marshalCanonical(prepared)
	if err != nil {
		return Prepared{}, err
	}

	err = s.withStoreLock(func() error {
		existing, _, loadErr := s.loadPreparedUnlocked()
		if loadErr == nil {
			if existing != prepared {
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
		if err := s.verifyManifestObjects(value); err != nil {
			return err
		}

		generations, err := s.openCollection("generations")
		if err != nil {
			return err
		}
		defer generations.Close()
		generationLeaf := value.GenerationID + ".json"
		existingGeneration, found, err := generations.ReadRegular(generationLeaf, maxManifestBytes)
		if err != nil {
			return fmt.Errorf("inspect generation object: %w", err)
		}
		if found {
			if !bytes.Equal(existingGeneration, manifestBody) {
				return ErrImmutableConflict
			}
			if _, err := decodeGeneration(existingGeneration, s.projectID, value.GenerationID, manifestDigest); err != nil {
				return err
			}
		} else {
			if err := atomicfile.WriteRootFileChecked(generations.Root, generationLeaf, manifestBody, privateFileMode, s.objectCheckpoint); err != nil {
				return fmt.Errorf("write immutable generation: %w", err)
			}
			stored, found, err := generations.ReadRegular(generationLeaf, maxManifestBytes)
			if err != nil || !found || !bytes.Equal(stored, manifestBody) {
				return errors.Join(errors.New("generation canonical re-read failed"), err)
			}
			if _, err := decodeGeneration(stored, s.projectID, value.GenerationID, manifestDigest); err != nil {
				return err
			}
		}

		root, err := s.reopenMemory()
		if err != nil {
			return err
		}
		defer root.Close()
		if err := rejectManifestBackup(root); err != nil {
			return err
		}
		if err := atomicfile.WriteRootFileChecked(root.Root, "manifest.json", pointerBody, privateFileMode, s.manifestCheckpoint); err != nil {
			return fmt.Errorf("commit prepared generation: %w", err)
		}
		loaded, loadedManifest, err := s.loadPreparedUnlocked()
		if err != nil || loaded != prepared || !equalManifest(loadedManifest, value) {
			return errors.Join(errors.New("prepared generation canonical re-read failed"), err)
		}
		return nil
	})
	if err != nil {
		return Prepared{}, err
	}
	return prepared, nil
}

// LoadPrepared returns both the durable prepared pointer and its fully
// validated immutable generation manifest.
func (s *Store) LoadPrepared() (Prepared, memory.GenerationManifest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.requireOpenLocked(); err != nil {
		return Prepared{}, memory.GenerationManifest{}, err
	}
	return s.loadPreparedUnlocked()
}

func (s *Store) loadPreparedUnlocked() (Prepared, memory.GenerationManifest, error) {
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
	if err := s.verifyManifestObjects(manifest); err != nil {
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

func (s *Store) withStoreLock(run func() error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.requireOpenLocked(); err != nil {
		return err
	}
	lock, err := project.AcquireProjectLock(s.memory.Root, "locks/scan.lock", storeLockTimeout)
	if err != nil {
		return fmt.Errorf("acquire memory store lock: %w", err)
	}
	return errors.Join(run(), lock.Release())
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

func (s *Store) verifyManifestObjects(value memory.GenerationManifest) error {
	for _, digest := range value.ObservationChunkDigests {
		if _, err := s.loadObjectUnlocked(ObjectObservationChunk, digest); err != nil {
			return fmt.Errorf("verify observation chunk %s: %w", digest, err)
		}
	}
	for _, dependency := range value.SessionViews {
		body, err := s.loadObjectUnlocked(ObjectSessionView, dependency.Digest)
		if err != nil {
			return fmt.Errorf("verify SessionView %s: %w", dependency.Digest, err)
		}
		var view memory.SessionView
		if err := decodeCanonicalJSON(body, &view); err != nil || view.Provider != dependency.Provider || view.SessionID != dependency.SessionID {
			return errors.Join(errors.New("SessionView dependency identity mismatch"), err)
		}
	}
	if _, err := s.loadObjectUnlocked(ObjectProbeState, value.ProbeStateDigest); err != nil {
		return fmt.Errorf("verify ProjectProbeState: %w", err)
	}
	if _, err := s.loadObjectUnlocked(ObjectProjectView, value.ProjectViewDigest); err != nil {
		return fmt.Errorf("verify ProjectView: %w", err)
	}
	return nil
}

func (s *Store) loadObjectUnlocked(kind ObjectKind, digest string) ([]byte, error) {
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
		return nil, err
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
	if len(body) == 0 || body[len(body)-1] != '\n' {
		return nil, errors.New("observation chunk is not canonical JSONL")
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), maxObjectBytes)
	var records []memory.ObservationRevision
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			return nil, errors.New("observation chunk contains an empty record")
		}
		var value memory.ObservationRevision
		if err := decodeCanonicalJSON(append(bytes.Clone(line), '\n'), &value); err != nil {
			return nil, err
		}
		if err := memory.ValidateObservationRevision(value); err != nil {
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
	return records, nil
}

func decodeGeneration(body []byte, projectID, generationID, expectedDigest string) (memory.GenerationManifest, error) {
	var value memory.GenerationManifest
	if err := decodeCanonicalJSON(body, &value); err != nil {
		return memory.GenerationManifest{}, fmt.Errorf("decode immutable generation: %w", err)
	}
	if err := memory.ValidateGenerationManifest(value); err != nil {
		return memory.GenerationManifest{}, fmt.Errorf("validate immutable generation: %w", err)
	}
	if value.ProjectID != projectID || value.GenerationID != generationID {
		return memory.GenerationManifest{}, errors.New("immutable generation identity mismatch")
	}
	if expectedDigest != "" {
		digest, err := memory.Digest(value)
		if err != nil || digest != expectedDigest {
			return memory.GenerationManifest{}, errors.Join(errors.New("immutable generation digest mismatch"), err)
		}
	}
	return value, nil
}

func decodeCanonicalJSON(body []byte, destination any) error {
	if len(body) == 0 || len(body) > maxObjectBytes {
		return errors.New("JSON object is empty or oversized")
	}
	if err := rejectDuplicateJSONFields(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	canonical, err := marshalCanonical(destination)
	if err != nil {
		return err
	}
	if !bytes.Equal(body, canonical) {
		return errors.New("JSON object is not in canonical stored form")
	}
	return nil
}

func rejectDuplicateJSONFields(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
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
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
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
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON object contains trailing data")
		}
		return err
	}
	return nil
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
