package memorystore

import (
	"context"
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
	// Gate B may pin a bounded publication window. Sixty-four generations is
	// deliberately generous for that window while keeping every cleanup
	// candidate's rooted-manifest revalidation a small constant factor.
	maxExternalGenerationPins = 64
)

// RetentionReport separates the current prepared/external-pin graph from
// fully validated but unreachable immutable lineage, plus old cache/staging
// entries that are eligible for rooted cleanup.
type RetentionReport struct {
	ReachableObjects           int   `json:"reachable_objects"`
	ReachableBytes             int64 `json:"reachable_bytes"`
	RetainedUnreachableObjects int   `json:"retained_unreachable_objects"`
	RetainedUnreachableBytes   int64 `json:"retained_unreachable_bytes"`
	CleanupCandidates          int   `json:"cleanup_candidates"`
	CleanupBytes               int64 `json:"cleanup_bytes"`
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
	report     RetentionReport
	candidates []retentionFile
	namespaces map[string]retentionFile
	anchors    retentionAnchors
}

type retentionAnchors struct {
	root            retentionFile
	preparedPointer *retentionFile
	generationRoots map[string]retentionFile
	externalPins    map[string]struct{}
	namespaces      map[string]retentionFile
}

// retentionDeleteCheckpoint is a deterministic test seam at the final CAS
// boundary. Production leaves it nil.
var retentionDeleteCheckpoint func() error

var retentionFullSnapshotCheckpoint func()
var retentionCandidateRevalidationCheckpoint func()
var retentionTraversalCheckpoint func()
var retentionPinnedManifestReadCheckpoint func(string)
var retentionSortCheckpoint func(string)

// ReportRetention is read-only. External generation pins are opaque generation
// IDs supplied by later gates; each is validated against a complete immutable
// generation graph before it contributes reachability. The variadic form keeps
// the Gate A no-pin call concise without inventing a Gate B dependency.
func (s *Store) ReportRetention(now time.Time, externalGenerationPins ...string) (RetentionReport, error) {
	return s.ReportRetentionContext(context.Background(), now, externalGenerationPins...)
}

// ReportRetentionContext is the cancellable read-only retention report. It
// returns context.Cause(ctx) when traversal is cancelled.
func (s *Store) ReportRetentionContext(ctx context.Context, now time.Time, externalGenerationPins ...string) (RetentionReport, error) {
	if ctx == nil {
		return RetentionReport{}, errors.New("retention report context is required")
	}
	if err := retentionContextCause(ctx); err != nil {
		return RetentionReport{}, err
	}
	pins, err := normalizeGenerationPins(externalGenerationPins)
	if err != nil {
		return RetentionReport{}, err
	}
	var report RetentionReport
	err = s.withStoreLock(func() error {
		snapshot, snapshotErr := s.captureRetentionSnapshot(ctx, now, pins)
		if snapshotErr != nil {
			return snapshotErr
		}
		report = snapshot.report
		return nil
	}, ctx)
	return report, err
}

// CleanupUnreachable is the compatibility entry point for callers that do not
// need cooperative cancellation.
func (s *Store) CleanupUnreachable(now time.Time, externalGenerationPins ...string) (RetentionReport, error) {
	return s.CleanupUnreachableContext(context.Background(), now, externalGenerationPins...)
}

// CleanupUnreachableContext removes only canonical, content-addressed
// cache/staging entries at least seven days old. Under the cross-process store
// lock it authenticates the complete graph once, then revalidates only the
// exact namespace and candidate immediately before each rooted unlink.
func (s *Store) CleanupUnreachableContext(ctx context.Context, now time.Time, externalGenerationPins ...string) (RetentionReport, error) {
	if ctx == nil {
		return RetentionReport{}, errors.New("retention cleanup context is required")
	}
	if err := retentionContextCause(ctx); err != nil {
		return RetentionReport{}, err
	}
	pins, err := normalizeGenerationPins(externalGenerationPins)
	if err != nil {
		return RetentionReport{}, err
	}
	var report RetentionReport
	err = s.withStoreLock(func() error {
		if err := retentionContextCause(ctx); err != nil {
			return err
		}
		expected, captureErr := s.captureRetentionSnapshot(ctx, now, pins)
		if captureErr != nil {
			return captureErr
		}
		report = expected.report
		planned := append([]retentionFile(nil), expected.candidates...)
		physicalCandidates := make(map[pathguard.IdentityToken]int, len(planned))
		for _, candidate := range planned {
			physicalCandidates[candidate.identity]++
		}
		for _, candidate := range planned {
			if err := retentionContextCause(ctx); err != nil {
				return err
			}
			namespace := strings.SplitN(candidate.relative, "/", 2)[0]
			if _, found := expected.namespaces[namespace]; !found {
				return errors.New("retention candidate namespace was not authenticated")
			}
			checkpoint := func() error {
				if retentionDeleteCheckpoint != nil {
					if err := retentionDeleteCheckpoint(); err != nil {
						return err
					}
				}
				if err := retentionContextCause(ctx); err != nil {
					return err
				}
				if retentionCandidateRevalidationCheckpoint != nil {
					retentionCandidateRevalidationCheckpoint()
				}
				return s.revalidateRetentionCandidate(ctx, candidate, &expected.anchors)
			}
			if err := s.memory.RemoveRegularIfHashMatchesChecked(candidate.relative, candidate.bodyHash, checkpoint); err != nil {
				return fmt.Errorf("remove retention candidate: %w", err)
			}
			report.CleanupCandidates--
			physicalCandidates[candidate.identity]--
			if physicalCandidates[candidate.identity] == 0 {
				report.CleanupBytes -= candidate.size
			}
			namespaceFact, err := s.captureRetentionNamespace(ctx, namespace)
			if err != nil {
				return err
			}
			expected.namespaces[namespace] = namespaceFact
			expected.anchors.namespaces[namespace] = namespaceFact
		}
		return nil
	}, ctx)
	return report, err
}

func normalizeGenerationPins(values []string) ([]string, error) {
	if len(values) > maxExternalGenerationPins {
		return nil, fmt.Errorf("external generation pins must contain at most %d entries", maxExternalGenerationPins)
	}
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

func (s *Store) captureRetentionSnapshot(ctx context.Context, now time.Time, pins []string) (retentionSnapshot, error) {
	if err := retentionCheckpoint(ctx); err != nil {
		return retentionSnapshot{}, err
	}
	if now.IsZero() {
		return retentionSnapshot{}, errors.New("retention reference time is required")
	}
	if retentionFullSnapshotCheckpoint != nil {
		retentionFullSnapshotCheckpoint()
	}
	root, err := s.reopenMemory()
	if err != nil {
		return retentionSnapshot{}, err
	}
	defer root.Close()
	rootAnchor, err := retentionRootFact(root)
	if err != nil {
		return retentionSnapshot{}, err
	}

	rootEntries, err := readRetentionEntries(ctx, root.Root, maxRetentionEntries)
	if err != nil {
		return retentionSnapshot{}, fmt.Errorf("enumerate memory root: %w", err)
	}
	allowedDirectories := map[string]bool{
		"observations": true, "sessions": true, "project-probes": true,
		"project-views": true, "generations": true, "diagnostics": true,
		"staging": true, "locks": true, "cache": true,
	}
	rootFacts := make([]retentionFile, 0, len(rootEntries)+16)
	namespaceFacts := make(map[string]retentionFile)
	var preparedPointerBody []byte
	preparedPointerFound := false
	for _, entry := range rootEntries {
		if err := retentionCheckpoint(ctx); err != nil {
			return retentionSnapshot{}, err
		}
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
			namespaceFacts[name] = fact
			continue
		}
		if atomicfile.IsRootDirectoryLockName(name) {
			if err := atomicfile.ValidateRootDirectoryLock(root.Root, name); err != nil {
				return retentionSnapshot{}, err
			}
			file, err := readRetentionFile(ctx, root, name, 0)
			if err != nil {
				return retentionSnapshot{}, err
			}
			rootFacts = append(rootFacts, file)
			continue
		}
		if name != "manifest.json" {
			return retentionSnapshot{}, fmt.Errorf("unknown memory root state %q", name)
		}
		file, body, err := readRetentionFileWithBody(ctx, root, name, maxManifestBytes)
		if err != nil {
			return retentionSnapshot{}, err
		}
		preparedPointerBody = body
		preparedPointerFound = true
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
	generationEntries, err := readRetentionEntries(ctx, generationDirectory.Root, maxRetentionEntries)
	if err != nil {
		return retentionSnapshot{}, fmt.Errorf("enumerate generations: %w", err)
	}
	generations := make(map[string]memory.GenerationManifest, len(generationEntries))
	generationDigests := make(map[string]string, len(generationEntries))
	generationFiles := make(map[string]retentionFile, len(generationEntries))
	allFacts := append([]retentionFile(nil), rootFacts...)
	for _, entry := range generationEntries {
		if err := retentionCheckpoint(ctx); err != nil {
			return retentionSnapshot{}, err
		}
		generationID, ok := canonicalGenerationLeaf(entry.Name())
		if !ok {
			return retentionSnapshot{}, fmt.Errorf("noncanonical generation entry %q", entry.Name())
		}
		file, body, err := readRetentionFileWithBody(ctx, generationDirectory, entry.Name(), maxManifestBytes)
		if err != nil {
			return retentionSnapshot{}, err
		}
		file.relative = filepath.ToSlash(filepath.Join("generations", entry.Name()))
		manifest, err := decodeGenerationContext(ctx, body, s.projectID, generationID, "")
		if err != nil {
			return retentionSnapshot{}, fmt.Errorf("validate generation %q: %w", generationID, err)
		}
		if err := retentionCheckpoint(ctx); err != nil {
			return retentionSnapshot{}, err
		}
		digest, err := memory.DigestContext(ctx, manifest)
		if cause := context.Cause(ctx); cause != nil {
			return retentionSnapshot{}, cause
		}
		if err != nil {
			return retentionSnapshot{}, err
		}
		if err := retentionCheckpoint(ctx); err != nil {
			return retentionSnapshot{}, err
		}
		file.digest = digest
		generations[generationID], generationDigests[generationID], generationFiles[generationID] = manifest, digest, file
		allFacts = append(allFacts, file)
	}

	prepared, preparedErr := Prepared{}, ErrNoPreparedGeneration
	if preparedPointerFound {
		preparedErr = decodeCanonicalJSONContext(ctx, preparedPointerBody, &prepared)
		if preparedErr == nil {
			preparedErr = validatePrepared(prepared)
		}
		if preparedErr != nil {
			return retentionSnapshot{}, fmt.Errorf("load prepared generation for retention: %w", preparedErr)
		}
		manifest, exists := generations[prepared.GenerationID]
		if !exists || generationDigests[prepared.GenerationID] != prepared.ManifestDigest || manifest.ProjectViewDigest != prepared.ProjectViewDigest {
			return retentionSnapshot{}, errors.New("prepared generation is absent from native lineage")
		}
		if err := s.reconcileGenerationGraphContext(ctx, manifest); err != nil {
			return retentionSnapshot{}, fmt.Errorf("reconcile prepared generation: %w", err)
		}
		preparedErr = nil
	}
	rootGenerations := make(map[string]struct{}, len(pins)+1)
	if preparedErr == nil {
		rootGenerations[prepared.GenerationID] = struct{}{}
	}
	for _, pin := range pins {
		if err := retentionCheckpoint(ctx); err != nil {
			return retentionSnapshot{}, err
		}
		manifest, exists := generations[pin]
		if !exists {
			return retentionSnapshot{}, fmt.Errorf("external generation pin %q is missing", pin)
		}
		if err := s.reconcileGenerationGraphContext(ctx, manifest); err != nil {
			return retentionSnapshot{}, fmt.Errorf("reconcile external generation pin %q: %w", pin, err)
		}
		rootGenerations[pin] = struct{}{}
	}

	objectFiles, objectFacts, err := s.enumerateRetentionObjects(ctx)
	if err != nil {
		return retentionSnapshot{}, err
	}
	allFacts = append(allFacts, objectFacts...)
	reachablePaths := make(map[string]retentionFile)
	reachableDigests := make(map[string]struct{})
	for generationID, manifest := range generations {
		if err := retentionCheckpoint(ctx); err != nil {
			return retentionSnapshot{}, err
		}
		if err := s.reconcileGenerationGraphContext(ctx, manifest); err != nil {
			return retentionSnapshot{}, fmt.Errorf("reconcile native generation %q: %w", generationID, err)
		}
		if _, rooted := rootGenerations[generationID]; !rooted {
			continue
		}
		generationFile := generationFiles[generationID]
		reachablePaths[generationFile.relative] = generationFile
		reachableDigests[generationDigests[generationID]] = struct{}{}
		for _, reference := range manifestObjectReferences(manifest) {
			if err := retentionCheckpoint(ctx); err != nil {
				return retentionSnapshot{}, err
			}
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
		entries, err := readRetentionEntries(ctx, directory.Root, maxRetentionEntries)
		if err != nil {
			_ = directory.Close()
			return retentionSnapshot{}, fmt.Errorf("enumerate %s: %w", namespace.name, err)
		}
		for _, entry := range entries {
			if err := retentionCheckpoint(ctx); err != nil {
				_ = directory.Close()
				return retentionSnapshot{}, err
			}
			digest, ok := canonicalRetentionLeaf(entry.Name(), namespace.suffix)
			if !ok {
				_ = directory.Close()
				return retentionSnapshot{}, fmt.Errorf("noncanonical %s entry %q", namespace.name, entry.Name())
			}
			file, err := readRetentionFile(ctx, directory, entry.Name(), maxRetentionCandidateSize)
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
	reachablePhysical := make(map[pathguard.IdentityToken]struct{}, len(reachablePaths))
	for _, file := range reachablePaths {
		if err := retentionCheckpoint(ctx); err != nil {
			return retentionSnapshot{}, err
		}
		if err := addUniqueRetentionBytes(&report.ReachableBytes, reachablePhysical, file); err != nil {
			return retentionSnapshot{}, fmt.Errorf("reachable accounting: %w", err)
		}
	}
	retainedPhysical := make(map[pathguard.IdentityToken]struct{}, len(generationFiles)+len(objectFiles))
	for _, collection := range []map[string]retentionFile{generationFiles, objectFiles} {
		for _, file := range collection {
			if err := retentionCheckpoint(ctx); err != nil {
				return retentionSnapshot{}, err
			}
			if _, reachable := reachablePaths[file.relative]; reachable {
				continue
			}
			report.RetainedUnreachableObjects++
			if err := addUniqueRetentionBytes(&report.RetainedUnreachableBytes, retainedPhysical, file); err != nil {
				return retentionSnapshot{}, fmt.Errorf("retained unreachable accounting: %w", err)
			}
		}
	}
	candidates := make([]retentionFile, 0)
	candidatePhysical := make(map[pathguard.IdentityToken]struct{})
	for _, file := range allFacts {
		if err := retentionCheckpoint(ctx); err != nil {
			return retentionSnapshot{}, err
		}
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
		if err := addUniqueRetentionBytes(&report.CleanupBytes, candidatePhysical, file); err != nil {
			return retentionSnapshot{}, fmt.Errorf("cleanup accounting: %w", err)
		}
	}
	candidates, err = sortRetentionCandidatesContext(ctx, candidates)
	if err != nil {
		return retentionSnapshot{}, err
	}
	anchors := retentionAnchors{
		root: rootAnchor, generationRoots: make(map[string]retentionFile, len(pins)+1),
		externalPins: make(map[string]struct{}, len(pins)), namespaces: cloneRetentionFacts(namespaceFacts),
	}
	for _, fact := range rootFacts {
		if fact.relative == "manifest.json" {
			copyFact := fact
			anchors.preparedPointer = &copyFact
			break
		}
	}
	if preparedErr == nil {
		anchors.generationRoots[prepared.GenerationID] = generationFiles[prepared.GenerationID]
	}
	for _, pin := range pins {
		anchors.generationRoots[pin] = generationFiles[pin]
		anchors.externalPins[pin] = struct{}{}
	}
	return retentionSnapshot{report: report, candidates: candidates, namespaces: namespaceFacts, anchors: anchors}, nil
}

// revalidateRetentionCandidate deliberately rechecks only reachability anchors
// and the exact candidate. Immutable object bodies were authenticated by the
// one full snapshot; unrelated object mutation cannot add a graph root or make
// this cache/staging candidate reachable, so rescanning all objects here would
// add quadratic work without strengthening the unlink decision.
func (s *Store) revalidateRetentionCandidate(ctx context.Context, candidate retentionFile, anchors *retentionAnchors) error {
	if anchors == nil {
		return errors.New("retention anchors are required")
	}
	if err := s.revalidateRetentionAnchors(ctx, anchors); err != nil {
		return err
	}
	parts := strings.SplitN(candidate.relative, "/", 2)
	if len(parts) != 2 || (parts[0] != "cache" && parts[0] != "staging") {
		return errors.New("retention candidate path is invalid")
	}
	namespace, leaf := parts[0], parts[1]
	suffix := ".cache"
	if namespace == "staging" {
		suffix = ".stage"
	}
	digest, ok := canonicalRetentionLeaf(leaf, suffix)
	if !ok || digest != candidate.digest {
		return errors.New("retention candidate digest path changed")
	}
	directory, err := openRetentionDirectory(s, namespace, false)
	if err != nil {
		return err
	}
	defer directory.Close()
	current, err := readRetentionFile(ctx, directory, leaf, maxRetentionCandidateSize)
	if err != nil {
		return err
	}
	current.relative, current.digest = candidate.relative, digest
	if !sameRetentionFact(current, candidate) {
		return errors.New("retention candidate changed before rooted cleanup")
	}
	return nil
}

func (s *Store) revalidateRetentionAnchors(ctx context.Context, anchors *retentionAnchors) error {
	if err := retentionCheckpoint(ctx); err != nil {
		return err
	}
	currentRoot, err := s.reopenMemory()
	if err != nil {
		return err
	}
	currentRootFact, factErr := retentionRootFact(currentRoot)
	if factErr == nil && !sameRetentionFact(currentRootFact, anchors.root) {
		factErr = errors.New("retention memory root changed before cleanup")
	}
	if factErr == nil {
		info, pointerErr := currentRoot.Root.Lstat("manifest.json")
		switch {
		case anchors.preparedPointer == nil && errors.Is(pointerErr, os.ErrNotExist):
		case anchors.preparedPointer == nil:
			factErr = errors.Join(errors.New("retention prepared pointer appeared before cleanup"), pointerErr)
		case pointerErr != nil || info == nil:
			factErr = errors.Join(errors.New("retention prepared pointer disappeared before cleanup"), pointerErr)
		default:
			current, readErr := readRetentionFile(ctx, currentRoot, "manifest.json", maxManifestBytes)
			if readErr != nil || !sameRetentionFact(current, *anchors.preparedPointer) {
				factErr = errors.Join(errors.New("retention prepared pointer changed before cleanup"), readErr)
			}
		}
	}
	closeErr := currentRoot.Close()
	if err := errors.Join(factErr, closeErr); err != nil {
		return err
	}

	generations, err := openRetentionDirectory(s, "generations", false)
	if err != nil {
		return err
	}
	for generationID, expected := range anchors.generationRoots {
		if err := retentionCheckpoint(ctx); err != nil {
			_ = generations.Close()
			return err
		}
		current, readErr := readRetentionFile(ctx, generations, generationID+".json", maxManifestBytes)
		if readErr == nil {
			if _, pinned := anchors.externalPins[generationID]; pinned && retentionPinnedManifestReadCheckpoint != nil {
				retentionPinnedManifestReadCheckpoint(generationID)
			}
		}
		current.relative = filepath.ToSlash(filepath.Join("generations", generationID+".json"))
		current.digest = expected.digest
		if readErr != nil || !sameRetentionFact(current, expected) {
			_ = generations.Close()
			return errors.Join(errors.New("retention rooted generation changed before cleanup"), readErr)
		}
	}
	if err := generations.Close(); err != nil {
		return err
	}
	for name, expectedNamespace := range anchors.namespaces {
		if err := retentionCheckpoint(ctx); err != nil {
			return err
		}
		currentNamespace, err := s.captureRetentionNamespace(ctx, name)
		if err != nil {
			return err
		}
		if !sameRetentionFact(currentNamespace, expectedNamespace) {
			return errors.New("retention namespace changed before cleanup")
		}
	}
	return nil
}

func (s *Store) captureRetentionNamespace(ctx context.Context, name string) (retentionFile, error) {
	if err := retentionCheckpoint(ctx); err != nil {
		return retentionFile{}, err
	}
	info, err := s.memory.Root.Lstat(name)
	if err != nil || info == nil || !info.IsDir() {
		return retentionFile{}, errors.Join(errors.New("retention namespace is unavailable"), err)
	}
	return retentionDirectoryFact(s.memory, name, info)
}

func retentionRootFact(root *pathguard.Directory) (retentionFile, error) {
	if root == nil || root.Info() == nil {
		return retentionFile{}, errors.New("retention memory root is unavailable")
	}
	file, err := root.Root.Open(".")
	if err != nil {
		return retentionFile{}, err
	}
	identity, identityErr := pathguard.PhysicalFileIdentity(file)
	closeErr := file.Close()
	if identityErr != nil || closeErr != nil {
		return retentionFile{}, errors.Join(identityErr, closeErr)
	}
	info := root.Info()
	return retentionFile{relative: "./", size: info.Size(), mode: info.Mode(), modified: info.ModTime(), identity: identity}, nil
}

func cloneRetentionFacts(values map[string]retentionFile) map[string]retentionFile {
	copyValues := make(map[string]retentionFile, len(values))
	for key, value := range values {
		copyValues[key] = value
	}
	return copyValues
}

func sameRetentionFact(first, second retentionFile) bool {
	return first.relative == second.relative && first.digest == second.digest && first.size == second.size && first.mode == second.mode &&
		first.modified.Equal(second.modified) && first.identity == second.identity && first.bodyHash == second.bodyHash
}

func addUniqueRetentionBytes(total *int64, physical map[pathguard.IdentityToken]struct{}, file retentionFile) error {
	if _, counted := physical[file.identity]; counted {
		return nil
	}
	if file.size < 0 || file.size > int64(^uint64(0)>>1)-*total {
		return errors.New("byte count overflow")
	}
	physical[file.identity] = struct{}{}
	*total += file.size
	return nil
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

func (s *Store) enumerateRetentionObjects(ctx context.Context) (map[string]retentionFile, []retentionFile, error) {
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
		if err := retentionCheckpoint(ctx); err != nil {
			return nil, nil, err
		}
		directory, err := openRetentionDirectory(s, collection.name, false)
		if err != nil {
			return nil, nil, err
		}
		entries, err := readRetentionEntries(ctx, directory.Root, maxRetentionEntries)
		if err != nil {
			_ = directory.Close()
			return nil, nil, err
		}
		for _, entry := range entries {
			if err := retentionCheckpoint(ctx); err != nil {
				_ = directory.Close()
				return nil, nil, err
			}
			digest, ok := canonicalRetentionLeaf(entry.Name(), collection.suffix)
			if !ok {
				_ = directory.Close()
				return nil, nil, fmt.Errorf("noncanonical %s object %q", collection.name, entry.Name())
			}
			file, body, err := readRetentionFileWithBody(ctx, directory, entry.Name(), maxObjectBytes)
			if err != nil {
				_ = directory.Close()
				return nil, nil, err
			}
			if err := validateObjectBytesContext(ctx, collection.kind, digest, body, s.projectID); err != nil {
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
	return retentionFile{relative: name + "/", size: info.Size(), mode: info.Mode(), modified: info.ModTime(), identity: identity}, nil
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

func readRetentionEntries(ctx context.Context, root *os.Root, limit int) ([]fs.DirEntry, error) {
	if err := retentionCheckpoint(ctx); err != nil {
		return nil, err
	}
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
	entries := make([]fs.DirEntry, 0, min(limit, 256))
	var readErr error
	for len(entries) <= limit {
		if err := retentionCheckpoint(ctx); err != nil {
			readErr = err
			break
		}
		batch, err := directory.ReadDir(min(256, limit+1-len(entries)))
		entries = append(entries, batch...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			readErr = err
			break
		}
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
	return sortRetentionDirectoryEntriesContext(ctx, entries)
}

func sortRetentionCandidatesContext(ctx context.Context, values []retentionFile) ([]retentionFile, error) {
	return mergeSortRetentionContext(ctx, values, "candidate_sort", func(left, right retentionFile) bool {
		return left.relative < right.relative
	})
}

func sortRetentionDirectoryEntriesContext(ctx context.Context, values []fs.DirEntry) ([]fs.DirEntry, error) {
	return mergeSortRetentionContext(ctx, values, "directory_entry_sort", func(left, right fs.DirEntry) bool {
		return left.Name() < right.Name()
	})
}

func mergeSortRetentionContext[T any](ctx context.Context, values []T, phase string, less func(T, T) bool) ([]T, error) {
	if err := retentionSortContextCause(ctx, phase); err != nil {
		return nil, err
	}
	current := make([]T, len(values))
	for offset := 0; offset < len(values); offset += 256 {
		if err := retentionSortContextCause(ctx, phase); err != nil {
			return nil, err
		}
		end := min(len(values), offset+256)
		copy(current[offset:end], values[offset:end])
	}
	if len(current) < 2 {
		return current, nil
	}
	next := make([]T, len(current))
	for width := 1; width < len(current); width *= 2 {
		for start := 0; start < len(current); start += 2 * width {
			middle := min(start+width, len(current))
			end := min(start+2*width, len(current))
			left, right, output := start, middle, start
			for left < middle && right < end {
				if err := retentionSortContextCause(ctx, phase); err != nil {
					return nil, err
				}
				if less(current[right], current[left]) {
					next[output], right = current[right], right+1
				} else {
					next[output], left = current[left], left+1
				}
				output++
			}
			for left < middle {
				if err := retentionSortContextCause(ctx, phase); err != nil {
					return nil, err
				}
				next[output], left, output = current[left], left+1, output+1
			}
			for right < end {
				if err := retentionSortContextCause(ctx, phase); err != nil {
					return nil, err
				}
				next[output], right, output = current[right], right+1, output+1
			}
		}
		current, next = next, current
	}
	if err := retentionSortContextCause(ctx, phase); err != nil {
		return nil, err
	}
	return current, nil
}

func retentionSortContextCause(ctx context.Context, phase string) error {
	if retentionSortCheckpoint != nil {
		retentionSortCheckpoint(phase)
	}
	return retentionContextCause(ctx)
}

func readRetentionFile(ctx context.Context, directory *pathguard.Directory, leaf string, maximum int64) (retentionFile, error) {
	file, _, err := readRetentionFileWithBody(ctx, directory, leaf, maximum)
	return file, err
}

func readRetentionFileWithBody(ctx context.Context, directory *pathguard.Directory, leaf string, maximum int64) (retentionFile, []byte, error) {
	if err := retentionCheckpoint(ctx); err != nil {
		return retentionFile{}, nil, err
	}
	before, err := directory.Root.Lstat(leaf)
	if err != nil || before == nil || !before.Mode().IsRegular() {
		return retentionFile{}, nil, errors.Join(errors.New("retention entry is not a regular file"), err)
	}
	if runtime.GOOS != "windows" && before.Mode().Perm() != privateFileMode {
		return retentionFile{}, nil, errors.New("retention entry has incorrect private mode")
	}
	body, found, err := directory.ReadRegularContext(ctx, leaf, maximum)
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
	bodyHash, err := retentionBodyHash(ctx, body)
	if err != nil {
		return retentionFile{}, nil, err
	}
	return retentionFile{relative: leaf, size: before.Size(), mode: before.Mode(), modified: before.ModTime(), identity: identity, bodyHash: bodyHash}, body, nil
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

func retentionBodyHash(ctx context.Context, body []byte) (string, error) {
	hash := sha256.New()
	for offset := 0; offset < len(body); offset += 64 << 10 {
		if err := retentionCheckpoint(ctx); err != nil {
			return "", err
		}
		end := min(offset+(64<<10), len(body))
		_, _ = hash.Write(body[offset:end])
	}
	if len(body) == 0 {
		if err := retentionCheckpoint(ctx); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func retentionCheckpoint(ctx context.Context) error {
	if retentionTraversalCheckpoint != nil {
		retentionTraversalCheckpoint()
	}
	return retentionContextCause(ctx)
}

func retentionContextCause(ctx context.Context) error {
	if ctx == nil {
		return errors.New("retention context is required")
	}
	return context.Cause(ctx)
}
