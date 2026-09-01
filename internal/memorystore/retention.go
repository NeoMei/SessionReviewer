package memorystore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

const (
	retentionGracePeriod      = 7 * 24 * time.Hour
	maxRetentionEntries       = 100_000
	maxRetentionCandidateSize = 4 << 20
)

// RetentionReport accounts for the unique immutable graph retained by native
// generations and validated external generation pins, plus old unreachable
// cache/staging entries that are eligible for rooted cleanup.
type RetentionReport struct {
	ReachableObjects  int   `json:"reachable_objects"`
	ReachableBytes    int64 `json:"reachable_bytes"`
	CleanupCandidates int   `json:"cleanup_candidates"`
	CleanupBytes      int64 `json:"cleanup_bytes"`
}

type retentionFile struct {
	relative string
	digest   string
	size     int64
	mode     fs.FileMode
	modified time.Time
	identity pathguard.IdentityToken
	bodyHash string
}

type retentionSnapshot struct {
	report      RetentionReport
	candidates  []retentionFile
	fingerprint string
}

// retentionDeleteCheckpoint is a deterministic test seam at the final CAS
// boundary. Production leaves it nil.
var retentionDeleteCheckpoint func() error

// ReportRetention is read-only. External generation pins are opaque generation
// IDs supplied by later gates; each is validated against a complete immutable
// generation graph before it contributes reachability. The variadic form keeps
// the Gate A no-pin call concise without inventing a Gate B dependency.
func (s *Store) ReportRetention(now time.Time, externalGenerationPins ...string) (RetentionReport, error) {
	pins, err := normalizeGenerationPins(externalGenerationPins)
	if err != nil {
		return RetentionReport{}, err
	}
	var report RetentionReport
	err = s.withStoreLock(func() error {
		snapshot, snapshotErr := s.captureRetentionSnapshot(now, pins)
		if snapshotErr != nil {
			return snapshotErr
		}
		report = snapshot.report
		return nil
	})
	return report, err
}

// CleanupUnreachable removes only canonical, content-addressed cache/staging
// entries at least seven days old. It recomputes the complete graph and exact
// namespace immediately before each rooted content/identity-checked unlink.
func (s *Store) CleanupUnreachable(now time.Time, externalGenerationPins ...string) (RetentionReport, error) {
	pins, err := normalizeGenerationPins(externalGenerationPins)
	if err != nil {
		return RetentionReport{}, err
	}
	var report RetentionReport
	err = s.withStoreLock(func() error {
		expected, captureErr := s.captureRetentionSnapshot(now, pins)
		if captureErr != nil {
			return captureErr
		}
		planned := append([]retentionFile(nil), expected.candidates...)
		for _, candidate := range planned {
			current, err := s.captureRetentionSnapshot(now, pins)
			if err != nil {
				return err
			}
			if current.fingerprint != expected.fingerprint || !snapshotContainsExactCandidate(current, candidate) {
				return errors.New("retention namespace changed before cleanup")
			}
			checkpoint := func() error {
				if retentionDeleteCheckpoint != nil {
					if err := retentionDeleteCheckpoint(); err != nil {
						return err
					}
				}
				revalidated, err := s.captureRetentionSnapshot(now, pins)
				if err != nil {
					return err
				}
				if revalidated.fingerprint != expected.fingerprint || !snapshotContainsExactCandidate(revalidated, candidate) {
					return errors.New("retention candidate changed before rooted cleanup")
				}
				return nil
			}
			if err := s.memory.RemoveRegularIfHashMatchesChecked(candidate.relative, candidate.bodyHash, checkpoint); err != nil {
				return fmt.Errorf("remove retention candidate: %w", err)
			}
			expected, err = s.captureRetentionSnapshot(now, pins)
			if err != nil {
				return err
			}
		}
		report = expected.report
		return nil
	})
	return report, err
}

func normalizeGenerationPins(values []string) ([]string, error) {
	pins := append([]string(nil), values...)
	sort.Strings(pins)
	for index, pin := range pins {
		if err := validateStoreID(pin); err != nil {
			return nil, errors.New("external generation pin is invalid")
		}
		if index > 0 && pins[index-1] == pin {
			return nil, errors.New("external generation pins contain a duplicate")
		}
	}
	return pins, nil
}

func (s *Store) captureRetentionSnapshot(now time.Time, pins []string) (retentionSnapshot, error) {
	if now.IsZero() {
		return retentionSnapshot{}, errors.New("retention reference time is required")
	}
	root, err := s.reopenMemory()
	if err != nil {
		return retentionSnapshot{}, err
	}
	defer root.Close()

	rootEntries, err := readRetentionEntries(root.Root, maxRetentionEntries)
	if err != nil {
		return retentionSnapshot{}, fmt.Errorf("enumerate memory root: %w", err)
	}
	allowedDirectories := map[string]bool{
		"observations": true, "sessions": true, "project-probes": true,
		"project-views": true, "generations": true, "diagnostics": true,
		"staging": true, "locks": true, "cache": true,
	}
	rootFacts := make([]retentionFile, 0, len(rootEntries)+16)
	for _, entry := range rootEntries {
		name := entry.Name()
		info, statErr := root.Root.Lstat(name)
		if statErr != nil {
			return retentionSnapshot{}, fmt.Errorf("inspect memory root entry: %w", statErr)
		}
		if entry.IsDir() {
			if !allowedDirectories[name] {
				return retentionSnapshot{}, fmt.Errorf("unknown memory namespace %q", name)
			}
			fact, err := retentionDirectoryFact(root, name, info)
			if err != nil {
				return retentionSnapshot{}, err
			}
			rootFacts = append(rootFacts, fact)
			continue
		}
		if atomicfile.IsRootDirectoryLockName(name) {
			if err := atomicfile.ValidateRootDirectoryLock(root.Root, name); err != nil {
				return retentionSnapshot{}, err
			}
			file, err := readRetentionFile(root, name, 0)
			if err != nil {
				return retentionSnapshot{}, err
			}
			rootFacts = append(rootFacts, file)
			continue
		}
		if name != "manifest.json" {
			return retentionSnapshot{}, fmt.Errorf("unknown memory root state %q", name)
		}
		file, err := readRetentionFile(root, name, maxManifestBytes)
		if err != nil {
			return retentionSnapshot{}, err
		}
		rootFacts = append(rootFacts, file)
	}
	for _, required := range []string{"observations", "sessions", "project-probes", "project-views", "generations", "diagnostics", "staging", "locks"} {
		if !hasRetentionEntry(rootEntries, required) {
			return retentionSnapshot{}, fmt.Errorf("required memory namespace %q is missing", required)
		}
	}

	generationDirectory, err := openRetentionDirectory(s, "generations", false)
	if err != nil {
		return retentionSnapshot{}, err
	}
	defer generationDirectory.Close()
	generationEntries, err := readRetentionEntries(generationDirectory.Root, maxRetentionEntries)
	if err != nil {
		return retentionSnapshot{}, fmt.Errorf("enumerate generations: %w", err)
	}
	generations := make(map[string]memory.GenerationManifest, len(generationEntries))
	generationDigests := make(map[string]string, len(generationEntries))
	generationFiles := make(map[string]retentionFile, len(generationEntries))
	allFacts := append([]retentionFile(nil), rootFacts...)
	for _, entry := range generationEntries {
		generationID, ok := canonicalGenerationLeaf(entry.Name())
		if !ok {
			return retentionSnapshot{}, fmt.Errorf("noncanonical generation entry %q", entry.Name())
		}
		file, body, err := readRetentionFileWithBody(generationDirectory, entry.Name(), maxManifestBytes)
		if err != nil {
			return retentionSnapshot{}, err
		}
		file.relative = filepath.ToSlash(filepath.Join("generations", entry.Name()))
		manifest, err := decodeGeneration(body, s.projectID, generationID, "")
		if err != nil {
			return retentionSnapshot{}, fmt.Errorf("validate generation %q: %w", generationID, err)
		}
		digest, err := memory.Digest(manifest)
		if err != nil {
			return retentionSnapshot{}, err
		}
		file.digest = digest
		generations[generationID], generationDigests[generationID], generationFiles[generationID] = manifest, digest, file
		allFacts = append(allFacts, file)
	}

	prepared, preparedManifest, preparedErr := s.loadPreparedRawUnlocked()
	if preparedErr != nil && !errors.Is(preparedErr, ErrNoPreparedGeneration) {
		return retentionSnapshot{}, fmt.Errorf("load prepared generation for retention: %w", preparedErr)
	}
	if preparedErr == nil {
		manifest, exists := generations[prepared.GenerationID]
		if !exists || generationDigests[prepared.GenerationID] != prepared.ManifestDigest || !equalManifest(manifest, preparedManifest) {
			return retentionSnapshot{}, errors.New("prepared generation is absent from native lineage")
		}
	}
	for _, pin := range pins {
		manifest, exists := generations[pin]
		if !exists {
			return retentionSnapshot{}, fmt.Errorf("external generation pin %q is missing", pin)
		}
		if err := s.reconcileGenerationGraph(manifest); err != nil {
			return retentionSnapshot{}, fmt.Errorf("reconcile external generation pin %q: %w", pin, err)
		}
	}

	objectFiles, objectFacts, err := s.enumerateRetentionObjects()
	if err != nil {
		return retentionSnapshot{}, err
	}
	allFacts = append(allFacts, objectFacts...)
	reachablePaths := make(map[string]retentionFile)
	reachableDigests := make(map[string]struct{})
	for generationID, manifest := range generations {
		if err := s.reconcileGenerationGraph(manifest); err != nil {
			return retentionSnapshot{}, fmt.Errorf("reconcile native generation %q: %w", generationID, err)
		}
		generationFile := generationFiles[generationID]
		reachablePaths[generationFile.relative] = generationFile
		reachableDigests[generationDigests[generationID]] = struct{}{}
		for _, reference := range manifestObjectReferences(manifest) {
			file, exists := objectFiles[reference.relative]
			if !exists || file.digest != reference.digest {
				return retentionSnapshot{}, fmt.Errorf("generation %q references a missing immutable object", generationID)
			}
			reachablePaths[file.relative] = file
			reachableDigests[file.digest] = struct{}{}
		}
	}

	for _, namespace := range []struct {
		name   string
		suffix string
	}{
		{name: "staging", suffix: ".stage"},
		{name: "cache", suffix: ".cache"},
	} {
		directory, err := openRetentionDirectory(s, namespace.name, namespace.name == "cache")
		if errors.Is(err, os.ErrNotExist) && namespace.name == "cache" {
			continue
		}
		if err != nil {
			return retentionSnapshot{}, err
		}
		entries, err := readRetentionEntries(directory.Root, maxRetentionEntries)
		if err != nil {
			_ = directory.Close()
			return retentionSnapshot{}, fmt.Errorf("enumerate %s: %w", namespace.name, err)
		}
		for _, entry := range entries {
			digest, ok := canonicalRetentionLeaf(entry.Name(), namespace.suffix)
			if !ok {
				_ = directory.Close()
				return retentionSnapshot{}, fmt.Errorf("noncanonical %s entry %q", namespace.name, entry.Name())
			}
			file, err := readRetentionFile(directory, entry.Name(), maxRetentionCandidateSize)
			if err != nil {
				_ = directory.Close()
				return retentionSnapshot{}, err
			}
			file.relative = filepath.ToSlash(filepath.Join(namespace.name, entry.Name()))
			file.digest = digest
			if file.bodyHash != strings.TrimPrefix(digest, "sha256:") {
				_ = directory.Close()
				return retentionSnapshot{}, fmt.Errorf("%s entry content digest mismatch", namespace.name)
			}
			allFacts = append(allFacts, file)
		}
		if err := directory.Close(); err != nil {
			return retentionSnapshot{}, err
		}
	}

	report := RetentionReport{ReachableObjects: len(reachablePaths)}
	for _, file := range reachablePaths {
		if file.size > 0 && report.ReachableBytes > int64(^uint64(0)>>1)-file.size {
			return retentionSnapshot{}, errors.New("reachable byte count overflow")
		}
		report.ReachableBytes += file.size
	}
	candidates := make([]retentionFile, 0)
	for _, file := range allFacts {
		if !strings.HasPrefix(file.relative, "staging/") && !strings.HasPrefix(file.relative, "cache/") {
			continue
		}
		if _, retained := reachableDigests[file.digest]; retained {
			continue
		}
		if now.Sub(file.modified) < retentionGracePeriod {
			continue
		}
		candidates = append(candidates, file)
		report.CleanupCandidates++
		report.CleanupBytes += file.size
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].relative < candidates[j].relative })
	fingerprint := retentionFingerprint(allFacts)
	return retentionSnapshot{report: report, candidates: candidates, fingerprint: fingerprint}, nil
}

type manifestObjectReference struct {
	relative string
	digest   string
}

func manifestObjectReferences(manifest memory.GenerationManifest) []manifestObjectReference {
	references := make([]manifestObjectReference, 0, len(manifest.ObservationChunkDigests)+len(manifest.SessionViews)+2)
	for _, digest := range manifest.ObservationChunkDigests {
		references = append(references, manifestObjectReference{relative: filepath.ToSlash(filepath.Join("observations", digestLeafName(digest, ".jsonl"))), digest: digest})
	}
	for _, dependency := range manifest.SessionViews {
		references = append(references, manifestObjectReference{relative: filepath.ToSlash(filepath.Join("sessions", digestLeafName(dependency.Digest, ".json"))), digest: dependency.Digest})
	}
	references = append(references,
		manifestObjectReference{relative: filepath.ToSlash(filepath.Join("project-probes", digestLeafName(manifest.ProbeStateDigest, ".json"))), digest: manifest.ProbeStateDigest},
		manifestObjectReference{relative: filepath.ToSlash(filepath.Join("project-views", digestLeafName(manifest.ProjectViewDigest, ".json"))), digest: manifest.ProjectViewDigest},
	)
	return references
}

func (s *Store) enumerateRetentionObjects() (map[string]retentionFile, []retentionFile, error) {
	objects := make(map[string]retentionFile)
	facts := make([]retentionFile, 0)
	collections := []struct {
		kind   ObjectKind
		name   string
		suffix string
	}{
		{ObjectObservationChunk, "observations", ".jsonl"},
		{ObjectSessionView, "sessions", ".json"},
		{ObjectProbeState, "project-probes", ".json"},
		{ObjectProjectView, "project-views", ".json"},
	}
	for _, collection := range collections {
		directory, err := openRetentionDirectory(s, collection.name, false)
		if err != nil {
			return nil, nil, err
		}
		entries, err := readRetentionEntries(directory.Root, maxRetentionEntries)
		if err != nil {
			_ = directory.Close()
			return nil, nil, err
		}
		for _, entry := range entries {
			digest, ok := canonicalRetentionLeaf(entry.Name(), collection.suffix)
			if !ok {
				_ = directory.Close()
				return nil, nil, fmt.Errorf("noncanonical %s object %q", collection.name, entry.Name())
			}
			file, body, err := readRetentionFileWithBody(directory, entry.Name(), maxObjectBytes)
			if err != nil {
				_ = directory.Close()
				return nil, nil, err
			}
			if err := validateObjectBytes(collection.kind, digest, body, s.projectID); err != nil {
				_ = directory.Close()
				return nil, nil, fmt.Errorf("validate %s object: %w", collection.name, err)
			}
			file.relative = filepath.ToSlash(filepath.Join(collection.name, entry.Name()))
			file.digest = digest
			objects[file.relative] = file
			facts = append(facts, file)
		}
		if err := directory.Close(); err != nil {
			return nil, nil, err
		}
	}
	return objects, facts, nil
}

func retentionDirectoryFact(root *pathguard.Directory, name string, expected os.FileInfo) (retentionFile, error) {
	directory, info, err := root.OpenDirectory(name)
	if err != nil {
		return retentionFile{}, err
	}
	defer directory.Close()
	if !os.SameFile(expected, info) || (runtime.GOOS != "windows" && info.Mode().Perm() != privateDirectoryMode) {
		return retentionFile{}, errors.New("retention directory identity or mode is invalid")
	}
	file, err := directory.Open(".")
	if err != nil {
		return retentionFile{}, err
	}
	identity, identityErr := pathguard.PhysicalFileIdentity(file)
	closeErr := file.Close()
	if identityErr != nil || closeErr != nil {
		return retentionFile{}, errors.Join(identityErr, closeErr)
	}
	return retentionFile{relative: name + "/", mode: info.Mode(), modified: info.ModTime(), identity: identity}, nil
}

func openRetentionDirectory(s *Store, name string, optional bool) (*pathguard.Directory, error) {
	info, err := s.memory.Root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) && optional {
		return nil, os.ErrNotExist
	}
	if err != nil || info == nil || !info.IsDir() {
		return nil, errors.Join(fmt.Errorf("retention namespace %q is unavailable", name), err)
	}
	directory, err := pathguard.Open(filepath.Join(s.memory.Path, name))
	if err != nil {
		return nil, fmt.Errorf("open retention namespace %q: %w", name, err)
	}
	if len(directory.Ancestors) < 2 || !os.SameFile(s.memory.Info(), directory.Ancestors[len(directory.Ancestors)-2]) || !os.SameFile(info, directory.Info()) {
		_ = directory.Close()
		return nil, errors.New("retention namespace escaped or changed")
	}
	if runtime.GOOS != "windows" && directory.Info().Mode().Perm() != privateDirectoryMode {
		_ = directory.Close()
		return nil, errors.New("retention namespace has incorrect private mode")
	}
	return directory, nil
}

func readRetentionEntries(root *os.Root, limit int) ([]fs.DirEntry, error) {
	if root == nil || limit < 0 {
		return nil, errors.New("retention directory is invalid")
	}
	before, err := root.Stat(".")
	if err != nil || !before.IsDir() {
		return nil, errors.Join(errors.New("inspect retention directory"), err)
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(limit + 1)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	afterOpen, statErr := directory.Stat()
	closeErr := directory.Close()
	afterName, nameErr := root.Stat(".")
	if err := errors.Join(readErr, statErr, closeErr, nameErr); err != nil {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errors.New("retention directory exceeds entry budget")
	}
	if !sameRetentionMetadata(before, afterOpen) || !sameRetentionMetadata(before, afterName) {
		return nil, errors.New("retention directory changed while enumerating")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func readRetentionFile(directory *pathguard.Directory, leaf string, maximum int64) (retentionFile, error) {
	file, _, err := readRetentionFileWithBody(directory, leaf, maximum)
	return file, err
}

func readRetentionFileWithBody(directory *pathguard.Directory, leaf string, maximum int64) (retentionFile, []byte, error) {
	before, err := directory.Root.Lstat(leaf)
	if err != nil || before == nil || !before.Mode().IsRegular() {
		return retentionFile{}, nil, errors.Join(errors.New("retention entry is not a regular file"), err)
	}
	if runtime.GOOS != "windows" && before.Mode().Perm() != privateFileMode {
		return retentionFile{}, nil, errors.New("retention entry has incorrect private mode")
	}
	body, found, err := directory.ReadRegular(leaf, maximum)
	if err != nil || !found {
		return retentionFile{}, nil, errors.Join(errors.New("read stable retention entry"), err)
	}
	file, opened, err := directory.OpenRegular(leaf)
	if err != nil {
		return retentionFile{}, nil, err
	}
	identity, identityErr := pathguard.PhysicalFileIdentity(file)
	closeErr := file.Close()
	after, statErr := directory.Root.Lstat(leaf)
	if err := errors.Join(identityErr, closeErr, statErr); err != nil {
		return retentionFile{}, nil, err
	}
	if !sameRetentionMetadata(before, opened) || !sameRetentionMetadata(before, after) {
		return retentionFile{}, nil, errors.New("retention entry changed while reading")
	}
	sum := sha256.Sum256(body)
	return retentionFile{relative: leaf, size: before.Size(), mode: before.Mode(), modified: before.ModTime(), identity: identity, bodyHash: hex.EncodeToString(sum[:])}, body, nil
}

func canonicalGenerationLeaf(leaf string) (string, bool) {
	if !strings.HasSuffix(leaf, ".json") {
		return "", false
	}
	id := strings.TrimSuffix(leaf, ".json")
	return id, validateStoreID(id) == nil && id+".json" == leaf
}

func canonicalRetentionLeaf(leaf, suffix string) (string, bool) {
	if !strings.HasSuffix(leaf, suffix) {
		return "", false
	}
	hexDigest := strings.TrimSuffix(leaf, suffix)
	if len(hexDigest) != 64 || strings.ToLower(hexDigest) != hexDigest {
		return "", false
	}
	decoded, err := hex.DecodeString(hexDigest)
	if err != nil || len(decoded) != sha256.Size || hexDigest+suffix != leaf {
		return "", false
	}
	return "sha256:" + hexDigest, true
}

func hasRetentionEntry(entries []fs.DirEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name() == name && entry.IsDir() {
			return true
		}
	}
	return false
}

func sameRetentionMetadata(first, second os.FileInfo) bool {
	return first != nil && second != nil && os.SameFile(first, second) &&
		first.Size() == second.Size() && first.Mode() == second.Mode() && first.ModTime().Equal(second.ModTime())
}

func snapshotContainsExactCandidate(snapshot retentionSnapshot, candidate retentionFile) bool {
	for _, current := range snapshot.candidates {
		if current.relative == candidate.relative {
			return current.digest == candidate.digest && current.size == candidate.size && current.mode == candidate.mode &&
				current.modified.Equal(candidate.modified) && current.identity == candidate.identity && current.bodyHash == candidate.bodyHash
		}
	}
	return false
}

func retentionFingerprint(files []retentionFile) string {
	copyFiles := append([]retentionFile(nil), files...)
	sort.Slice(copyFiles, func(i, j int) bool { return copyFiles[i].relative < copyFiles[j].relative })
	hash := sha256.New()
	for _, file := range copyFiles {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%d\x00%d\x00%s\x00%s\x00%s\n",
			file.relative, file.digest, file.size, uint32(file.mode), file.modified.UnixNano(),
			file.identity.Kind, file.identity.Volume, file.identity.File+file.bodyHash)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
