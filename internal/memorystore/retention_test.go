package memorystore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/project"
)

var retentionNow = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func TestRetentionSortsCancelMidSortWithoutMutatingInputs(t *testing.T) {
	const entries = 100_000
	candidates := make([]retentionFile, entries)
	directoryEntries := make([]fs.DirEntry, entries)
	for index := 0; index < entries; index++ {
		name := fmt.Sprintf("entry-%06d", entries-index)
		candidates[index] = retentionFile{relative: "cache/" + name}
		directoryEntries[index] = retentionSortDirEntry{name: name}
	}

	tests := []struct {
		name  string
		phase string
		run   func(context.Context) error
	}{
		{
			name: "candidate sort", phase: "candidate_sort",
			run: func(ctx context.Context) error {
				before := append([]retentionFile(nil), candidates...)
				sorted, err := sortRetentionCandidatesContext(ctx, candidates)
				if sorted != nil {
					t.Fatal("cancelled candidate sort returned a result")
				}
				for index := range candidates {
					if candidates[index].relative != before[index].relative {
						t.Fatalf("cancelled candidate sort mutated input at %d", index)
					}
				}
				return err
			},
		},
		{
			name: "directory entry sort", phase: "directory_entry_sort",
			run: func(ctx context.Context) error {
				before := append([]fs.DirEntry(nil), directoryEntries...)
				sorted, err := sortRetentionDirectoryEntriesContext(ctx, directoryEntries)
				if sorted != nil {
					t.Fatal("cancelled directory sort returned a result")
				}
				for index := range directoryEntries {
					if directoryEntries[index].Name() != before[index].Name() {
						t.Fatalf("cancelled directory sort mutated input at %d", index)
					}
				}
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New("cancel " + test.name)
			ctx, cancel := context.WithCancelCause(context.Background())
			calls := 0
			retentionSortCheckpoint = func(phase string) {
				if phase == test.phase {
					calls++
					if calls == 1024 {
						cancel(cause)
					}
				}
			}
			t.Cleanup(func() { retentionSortCheckpoint = nil })
			if err := test.run(ctx); !errors.Is(err, cause) {
				t.Fatalf("sort error=%v want cause", err)
			}
			if calls != 1024 {
				t.Fatalf("sort checkpoints=%d want 1024", calls)
			}
			retentionSortCheckpoint = nil
		})
	}
}

func TestRetentionContextSortsMatchLegacyOrder(t *testing.T) {
	random := rand.New(rand.NewSource(20260901))
	for iteration := 0; iteration < 64; iteration++ {
		count := 1 + random.Intn(4096)
		permutation := random.Perm(count)
		candidates := make([]retentionFile, count)
		entries := make([]fs.DirEntry, count)
		for index, value := range permutation {
			name := fmt.Sprintf("entry-%08d", value)
			candidates[index] = retentionFile{relative: "cache/" + name}
			entries[index] = retentionSortDirEntry{name: name}
		}

		wantCandidates := append([]retentionFile(nil), candidates...)
		sort.Slice(wantCandidates, func(i, j int) bool { return wantCandidates[i].relative < wantCandidates[j].relative })
		gotCandidates, err := sortRetentionCandidatesContext(context.Background(), candidates)
		if err != nil {
			t.Fatal(err)
		}
		wantEntries := append([]fs.DirEntry(nil), entries...)
		sort.Slice(wantEntries, func(i, j int) bool { return wantEntries[i].Name() < wantEntries[j].Name() })
		gotEntries, err := sortRetentionDirectoryEntriesContext(context.Background(), entries)
		if err != nil {
			t.Fatal(err)
		}
		for index := 0; index < count; index++ {
			if gotCandidates[index].relative != wantCandidates[index].relative {
				t.Fatalf("iteration %d candidate %d=%q want %q", iteration, index, gotCandidates[index].relative, wantCandidates[index].relative)
			}
			if gotEntries[index].Name() != wantEntries[index].Name() {
				t.Fatalf("iteration %d entry %d=%q want %q", iteration, index, gotEntries[index].Name(), wantEntries[index].Name())
			}
		}
	}
}

func TestRetentionSnapshotReadsSharedObjectsAndReconcilesGenerationsOnce(t *testing.T) {
	dataRoot, store, first := newRetentionStore(t, "generation-shared-000")
	large := buildStoredFixture(t, store, "generation-shared-001")
	large.session.Diagnostics = make([]memory.Diagnostic, 1024)
	for index := range large.session.Diagnostics {
		large.session.Diagnostics[index] = memory.Diagnostic{
			Code: fmt.Sprintf("shared-%04d", index),
			Path: strings.Repeat("large-shared-object/", 40) + fmt.Sprintf("%04d", index),
		}
	}
	var err error
	large.session.Digest, err = memory.SessionViewDigest(large.session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSessionView(large.session); err != nil {
		t.Fatal(err)
	}
	largeSessionPath := filepath.Join(retentionMemoryRoot(dataRoot), "sessions", digestLeaf(large.session.Digest, ".json"))
	if size := fileSize(t, largeSessionPath); size < 512<<10 {
		t.Fatalf("shared SessionView fixture size=%d want at least 512 KiB", size)
	}
	large.project.SessionViewDependencies[0].Digest = large.session.Digest
	large.project.Digest, err = memory.ProjectViewDigest(large.project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProjectView(large.project); err != nil {
		t.Fatal(err)
	}
	large.manifest.SessionViews[0].Digest = large.session.Digest
	large.manifest.ProjectViewDigest = large.project.Digest
	if err := memory.ValidateGenerationManifest(large.manifest); err != nil {
		t.Fatal(err)
	}
	expected, _, err := store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvancePrepared(expected, large.manifest); err != nil {
		t.Fatal(err)
	}

	const sharedGenerations = 32
	memoryRoot := retentionMemoryRoot(dataRoot)
	for index := 2; index < sharedGenerations; index++ {
		manifest := large.manifest
		manifest.GenerationID = fmt.Sprintf("generation-shared-%03d", index)
		writeCanonicalJSONForTest(t, filepath.Join(memoryRoot, "generations", manifest.GenerationID+".json"), manifest, 0o600)
	}

	objectReads := make(map[string]int)
	objectReconciles := make(map[string]int)
	reconciles := make(map[string]int)
	retentionObjectReadCheckpoint = func(kind ObjectKind, digest string) {
		objectReads[string(kind)+"/"+digest]++
	}
	retentionObjectReconcileCheckpoint = func(kind ObjectKind, digest string) {
		objectReconciles[string(kind)+"/"+digest]++
	}
	retentionGenerationReconcileCheckpoint = func(generationID string) {
		reconciles[generationID]++
	}
	t.Cleanup(func() {
		retentionObjectReadCheckpoint = nil
		retentionObjectReconcileCheckpoint = nil
		retentionGenerationReconcileCheckpoint = nil
	})
	if _, err := store.ReportRetention(retentionNow, first.manifest.GenerationID, large.manifest.GenerationID); err != nil {
		t.Fatal(err)
	}
	for object, reads := range objectReads {
		if reads != 1 {
			t.Fatalf("shared object %s read/decode/hash count=%d want 1", object, reads)
		}
	}
	if len(objectReads) != 7 {
		t.Fatalf("unique stored object reads=%d want 7", len(objectReads))
	}
	if len(objectReconciles) != len(objectReads) {
		t.Fatalf("unique object reconciles=%d want %d", len(objectReconciles), len(objectReads))
	}
	for object, count := range objectReconciles {
		if count != 1 {
			t.Fatalf("shared object %s validated/reconciled %d times want 1", object, count)
		}
	}
	if len(reconciles) != sharedGenerations {
		t.Fatalf("unique generation reconciles=%d want %d", len(reconciles), sharedGenerations)
	}
	for generationID, count := range reconciles {
		if count != 1 {
			t.Fatalf("generation %s reconciled %d times want 1", generationID, count)
		}
	}
}

func TestRetentionCandidatePlanningCancelsInsideLargeLoopsWithoutResult(t *testing.T) {
	const count = 100_000
	candidates := make([]retentionFile, count)
	for index := range candidates {
		candidates[index] = retentionFile{
			relative: fmt.Sprintf("cache/%06d.cache", index),
			identity: pathguard.IdentityToken{Kind: "posix-dev-inode", Volume: "1", File: fmt.Sprintf("%d", index+1)},
		}
	}
	before := append([]retentionFile(nil), candidates...)
	for _, phase := range []string{"candidate_copy", "candidate_identity_map"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			cause := errors.New("cancel " + phase)
			calls := 0
			retentionLargeLoopCheckpoint = func(current string) {
				if current == phase {
					calls++
					if calls == 128 {
						cancel(cause)
					}
				}
			}
			t.Cleanup(func() { retentionLargeLoopCheckpoint = nil })
			planned, identities, err := planRetentionCandidatesContext(ctx, candidates)
			if !errors.Is(err, cause) {
				t.Fatalf("planning error=%v want cause", err)
			}
			if planned != nil || identities != nil {
				t.Fatal("cancelled candidate planning returned partial mutable state")
			}
			if calls != 128 {
				t.Fatalf("%s checkpoints=%d want 128", phase, calls)
			}
			for index := range candidates {
				if candidates[index] != before[index] {
					t.Fatalf("candidate input mutated at %d", index)
				}
			}
			retentionLargeLoopCheckpoint = nil
		})
	}
}

func TestManifestObjectReferenceConstructionCancelsMidLoop(t *testing.T) {
	const count = 65_536
	manifest := memory.GenerationManifest{
		SessionViews:    make([]memory.SessionViewDependency, count),
		SessionLineages: make([]memory.SessionLineageDependency, count),
	}
	for index := 0; index < count; index++ {
		digest := prefixedDigest(fmt.Sprintf("manifest-reference-%06d", index))
		manifest.SessionViews[index] = memory.SessionViewDependency{Digest: digest}
		manifest.SessionLineages[index] = memory.SessionLineageDependency{Digest: digest}
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("cancel manifest references")
	calls := 0
	retentionLargeLoopCheckpoint = func(phase string) {
		if phase == "manifest_reference_construction" {
			calls++
			if calls == 512 {
				cancel(cause)
			}
		}
	}
	t.Cleanup(func() { retentionLargeLoopCheckpoint = nil })
	references, err := manifestObjectReferencesContext(ctx, manifest)
	if !errors.Is(err, cause) {
		t.Fatalf("reference construction error=%v want cause", err)
	}
	if references != nil {
		t.Fatal("cancelled reference construction returned partial references")
	}
	if calls != 512 {
		t.Fatalf("reference construction checkpoints=%d want 512", calls)
	}
}

type retentionSortDirEntry struct{ name string }

func (entry retentionSortDirEntry) Name() string         { return entry.name }
func (retentionSortDirEntry) IsDir() bool                { return false }
func (retentionSortDirEntry) Type() fs.FileMode          { return 0 }
func (retentionSortDirEntry) Info() (fs.FileInfo, error) { return nil, nil }

func TestRetentionReportsExactReachableGraphAndDryRunDoesNotMutate(t *testing.T) {
	dataRoot, store, fixture := newRetentionStore(t, "generation-1")
	memoryRoot := retentionMemoryRoot(dataRoot)
	before := retentionInventory(t, memoryRoot)

	report, err := store.ReportRetention(retentionNow)
	if err != nil {
		t.Fatalf("report retention: %v", err)
	}
	wantObjects, wantBytes := reachableFixtureTotals(t, memoryRoot, fixture.manifest)
	if report != (RetentionReport{ReachableObjects: wantObjects, ReachableBytes: wantBytes}) {
		t.Fatalf("report=%+v want reachable_objects=%d reachable_bytes=%d", report, wantObjects, wantBytes)
	}
	after := retentionInventory(t, memoryRoot)
	if !equalRetentionInventory(before, after) {
		t.Fatalf("dry report changed private store metadata:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestRetentionKeepsNativeLineageAndValidatesOpaquePins(t *testing.T) {
	dataRoot, store, first := newRetentionStore(t, "generation-1")
	second := buildStoredFixture(t, store, "generation-2")
	expected, _, err := store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvancePrepared(expected, second.manifest); err != nil {
		t.Fatalf("advance prepared: %v", err)
	}

	report, err := store.ReportRetention(retentionNow)
	if err != nil {
		t.Fatalf("report native lineage: %v", err)
	}
	memoryRoot := retentionMemoryRoot(dataRoot)
	wantObjects, wantBytes := reachableFixtureTotals(t, memoryRoot, second.manifest)
	firstInfo, statErr := os.Stat(filepath.Join(memoryRoot, "generations", first.manifest.GenerationID+".json"))
	if statErr != nil {
		t.Fatal(statErr)
	}
	if report.ReachableObjects != wantObjects || report.ReachableBytes != wantBytes || report.RetainedUnreachableObjects != 1 || report.RetainedUnreachableBytes != firstInfo.Size() {
		t.Fatalf("orphan lineage accounting is wrong: report=%+v want reachable=%d/%d retained=1/%d", report, wantObjects, wantBytes, firstInfo.Size())
	}

	pinned, err := store.ReportRetention(retentionNow, first.manifest.GenerationID)
	if err != nil {
		t.Fatalf("report pinned lineage: %v", err)
	}
	if pinned.ReachableObjects != wantObjects+1 || pinned.ReachableBytes != wantBytes+firstInfo.Size() || pinned.RetainedUnreachableObjects != 0 || pinned.RetainedUnreachableBytes != 0 {
		t.Fatalf("validated pin did not promote only its graph: %+v", pinned)
	}

	for _, pins := range [][]string{{"missing-generation"}, {"../escape"}, {first.manifest.GenerationID, first.manifest.GenerationID}} {
		if _, err := store.ReportRetention(retentionNow, pins...); err == nil {
			t.Fatalf("invalid external pins accepted: %q", pins)
		}
	}
}

func TestRetentionTraversesManifestWithIndependentlyPermutedLineages(t *testing.T) {
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := buildStoredFixture(t, store, "generation-retention-permuted-lineages")
	manifest := buildPermutedLineageManifest(t, store, fixture)
	if _, err := store.PrepareGeneration(manifest); err != nil {
		t.Fatalf("prepare independently permuted lineage manifest: %v", err)
	}
	report, err := store.ReportRetention(retentionNow)
	if err != nil {
		t.Fatalf("retention rejected independently permuted lineage manifest: %v", err)
	}
	// One generation, one ProjectView, one ProbeState, and two each of
	// SessionView, SessionLineage, and observation chunk are reachable.
	if report.ReachableObjects != 9 || report.ReachableBytes <= 0 || report.CleanupCandidates != 0 {
		t.Fatalf("retention report=%+v want nine reachable immutable objects and no cleanup candidate", report)
	}
}

func TestRetentionRejectsDuplicateAndMismatchedLineageIdentitySets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*memory.GenerationManifest)
	}{
		{name: "duplicate", mutate: func(manifest *memory.GenerationManifest) {
			manifest.SessionLineages[0] = memory.SessionLineageDependency{Provider: "codex", SessionID: "session-1", Digest: manifest.SessionLineages[0].Digest}
		}},
		{name: "mismatch", mutate: func(manifest *memory.GenerationManifest) {
			manifest.SessionLineages[0] = memory.SessionLineageDependency{Provider: "codex", SessionID: "session-3", Digest: manifest.SessionLineages[0].Digest}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataRoot := t.TempDir()
			store, err := Open(dataRoot, testProjectID)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			fixture := buildStoredFixture(t, store, "generation-retention-invalid-"+test.name)
			manifest := buildPermutedLineageManifest(t, store, fixture)
			test.mutate(&manifest)
			writeCanonicalJSONForTest(t, filepath.Join(retentionMemoryRoot(dataRoot), "generations", manifest.GenerationID+".json"), manifest, 0o600)
			if _, err := store.ReportRetention(retentionNow, manifest.GenerationID); err == nil {
				t.Fatal("retention accepted an inexact SessionLineage identity set")
			}
		})
	}
}

func TestRetentionRejectsMoreThan64ExternalPinsBeforeStoreIO(t *testing.T) {
	_, store, _ := newRetentionStore(t, "generation-pin-limit")
	pins := make([]string, 65)
	for index := range pins {
		pins[index] = fmt.Sprintf("generation-pin-%02d", index)
	}
	var snapshots atomic.Int32
	retentionFullSnapshotCheckpoint = func() { snapshots.Add(1) }
	t.Cleanup(func() { retentionFullSnapshotCheckpoint = nil })
	if _, err := store.ReportRetention(retentionNow, pins...); err == nil || !strings.Contains(err.Error(), "at most 64") {
		t.Fatalf("over-limit pin error=%v", err)
	}
	if snapshots.Load() != 0 {
		t.Fatalf("over-limit pins reached store I/O: snapshots=%d", snapshots.Load())
	}
}

func TestRetentionContextCancelsWhileStoreLockIsHeld(t *testing.T) {
	_, store, _ := newRetentionStore(t, "held-lock-cancellation")
	held, err := project.AcquireProjectLock(store.memory.Root, "locks/scan.lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			_ = held.Release()
		}
	})

	cancelCause := errors.New("stop waiting for retention lock")
	ctx, cancel := context.WithCancelCause(context.Background())
	result := make(chan error, 1)
	go func() {
		_, reportErr := store.ReportRetentionContext(ctx, retentionNow)
		result <- reportErr
	}()
	// Establish that the report reached lock contention. This delay is much
	// larger than the lock poll interval and much smaller than the legacy
	// five-second timeout.
	select {
	case err := <-result:
		t.Fatalf("report returned before cancellation: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	cancel(cancelCause)

	select {
	case err := <-result:
		if !errors.Is(err, cancelCause) {
			t.Fatalf("report error=%v want cancellation cause", err)
		}
	case <-time.After(2 * time.Second):
		// Release before failing so the old blocking implementation can unwind
		// without leaking a goroutine from this test.
		released = true
		if releaseErr := held.Release(); releaseErr != nil {
			t.Fatalf("release held lock after timeout: %v", releaseErr)
		}
		err := <-result
		t.Fatalf("context cancellation did not interrupt lock acquisition: %v", err)
	}
	released = true
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionLargeInputsCancelInsideChunkedDecode(t *testing.T) {
	payload := bytes.Repeat([]byte{'x'}, 16<<20)
	jsonBody := make([]byte, 0, len(payload)+32)
	jsonBody = append(jsonBody, "{\"padding\":\""...)
	jsonBody = append(jsonBody, payload...)
	jsonBody = append(jsonBody, "\"}"...)
	jsonBody = append(jsonBody, '\n')
	observationBody := bytes.Clone(jsonBody)

	tests := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "observation chunk", run: func(ctx context.Context) error {
			_, err := decodeObservationChunkContext(ctx, observationBody)
			return err
		}},
		{name: "session object", run: func(ctx context.Context) error {
			return validateObjectBytesContext(ctx, ObjectSessionView, prefixedDigest("large-session"), jsonBody, testProjectID)
		}},
		{name: "project object", run: func(ctx context.Context) error {
			return validateObjectBytesContext(ctx, ObjectProjectView, prefixedDigest("large-project"), jsonBody, testProjectID)
		}},
		{name: "generation manifest", run: func(ctx context.Context) error {
			_, err := decodeGenerationContext(ctx, jsonBody, testProjectID, "large-generation", "")
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New("cancel large " + test.name)
			ctx, cancel := context.WithCancelCause(context.Background())
			calls := 0
			storeContextCheckpoint = func(phase string) {
				if phase == "decode" && calls < 32 {
					calls++
					if calls == 32 {
						cancel(cause)
					}
				}
			}
			t.Cleanup(func() { storeContextCheckpoint = nil })
			if err := test.run(ctx); !errors.Is(err, cause) {
				t.Fatalf("large decode error=%v want cancellation cause", err)
			}
			if calls != 32 {
				t.Fatalf("decode checkpoints=%d want 32", calls)
			}
			storeContextCheckpoint = nil
		})
	}
}

func TestRetentionLargeValidationAndCanonicalHashCancelInsideLoops(t *testing.T) {
	const entries = 65_536
	values := make([]string, entries)
	for index := range values {
		values[index] = prefixedDigest(fmt.Sprintf("large-context-%d", index))
	}

	t.Run("validation", func(t *testing.T) {
		cause := errors.New("cancel large validation")
		ctx := &checkpointCancelContext{Context: context.Background(), cancelAt: 128, cause: cause}
		value := memory.ProjectView{
			SchemaVersion:          memory.MemorySchemaVersion,
			Digest:                 prefixedDigest("large-validation-view"),
			ProjectID:              testProjectID,
			Generation:             1,
			StartedAt:              testStartedAt,
			EndedAt:                testEndedAt,
			ObservationRevisionIDs: values,
			ProbeStateDigest:       prefixedDigest("large-validation-probe"),
			DependencyDigest:       prefixedDigest("large-validation-dependency"),
			ReducerVersion:         "v1",
		}
		if err := memory.ValidateProjectViewContext(ctx, value); !errors.Is(err, cause) {
			t.Fatalf("validation error=%v want cancellation cause", err)
		}
		if ctx.calls != 128 {
			t.Fatalf("validation checkpoints=%d want 128", ctx.calls)
		}
	})

	t.Run("canonical hash", func(t *testing.T) {
		cause := errors.New("cancel canonical hash")
		ctx := &checkpointCancelContext{Context: context.Background(), cancelAt: entries + 128, cause: cause}
		if _, err := memory.DigestContext(ctx, values); !errors.Is(err, cause) {
			t.Fatalf("digest error=%v want cancellation cause (calls=%d)", err, ctx.calls)
		}
		if ctx.calls != ctx.cancelAt {
			t.Fatalf("digest checkpoints=%d want %d", ctx.calls, ctx.cancelAt)
		}
	})
}

type checkpointCancelContext struct {
	context.Context
	calls    int
	cancelAt int
	cause    error
}

func (ctx *checkpointCancelContext) Err() error {
	ctx.calls++
	if ctx.calls >= ctx.cancelAt {
		return ctx.cause
	}
	return nil
}

func TestRetentionReadsEveryAllowedPinManifestOncePerCandidate(t *testing.T) {
	dataRoot, store, first := newRetentionStore(t, "generation-pin-read-00")
	pins := []string{first.manifest.GenerationID}
	for index := 1; index < 64; index++ {
		fixture := buildStoredFixture(t, store, fmt.Sprintf("generation-pin-read-%02d", index))
		artifacts, err := store.prepareArtifacts(fixture.manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.withStoreLock(func() error { return store.writeGenerationUnlocked(fixture.manifest, artifacts) }); err != nil {
			t.Fatal(err)
		}
		pins = append(pins, fixture.manifest.GenerationID)
	}
	writeRetentionCandidate(t, retentionMemoryRoot(dataRoot), "cache", ".cache", []byte("pin-read-candidate"), retentionNow.Add(-8*24*time.Hour))
	var reads atomic.Int32
	retentionPinnedManifestReadCheckpoint = func(string) { reads.Add(1) }
	t.Cleanup(func() { retentionPinnedManifestReadCheckpoint = nil })
	if _, err := store.CleanupUnreachable(retentionNow, pins...); err != nil {
		t.Fatalf("cleanup at pin limit: %v", err)
	}
	if reads.Load() != 64 {
		t.Fatalf("pin manifest reads=%d want 64", reads.Load())
	}
}

func TestRetentionCleanupBytesCountHardlinkedStorageOnceButEveryEntryIsUnlinked(t *testing.T) {
	dataRoot, store, _ := newRetentionStore(t, "generation-hardlinks")
	memoryRoot := retentionMemoryRoot(dataRoot)
	body := []byte("one-physical-candidate")
	cache := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", body, retentionNow.Add(-8*24*time.Hour))
	sum := sha256.Sum256(body)
	stage := filepath.Join(memoryRoot, "staging", hex.EncodeToString(sum[:])+".stage")
	if err := os.Link(cache, stage); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	report, err := store.ReportRetention(retentionNow)
	if err != nil {
		t.Fatal(err)
	}
	if report.CleanupCandidates != 2 || report.CleanupBytes != int64(len(body)) {
		t.Fatalf("hardlink accounting=%+v want entries=2 unique_bytes=%d", report, len(body))
	}
	if _, err := store.CleanupUnreachable(retentionNow); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{cache, stage} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("authorized hardlink entry remains %q: %v", filepath.Base(path), err)
		}
	}
}

func TestRetentionGraceBoundaryAndCleanupUseCanonicalRootedCandidates(t *testing.T) {
	dataRoot, store, _ := newRetentionStore(t, "generation-1")
	memoryRoot := retentionMemoryRoot(dataRoot)
	oldCache := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte("old-cache"), retentionNow.Add(-7*24*time.Hour))
	oldStage := writeRetentionCandidate(t, memoryRoot, "staging", ".stage", []byte("old-stage"), retentionNow.Add(-8*24*time.Hour))
	youngCache := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte("young-cache"), retentionNow.Add(-7*24*time.Hour+2*time.Second))

	report, err := store.ReportRetention(retentionNow)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := fileSize(t, oldCache) + fileSize(t, oldStage)
	if report.CleanupCandidates != 2 || report.CleanupBytes != wantBytes {
		t.Fatalf("grace report=%+v want candidates=2 bytes=%d", report, wantBytes)
	}
	if _, err := os.Stat(oldCache); err != nil {
		t.Fatalf("report mutated exact-boundary candidate: %v", err)
	}

	cleaned, err := store.CleanupUnreachable(retentionNow)
	if err != nil {
		t.Fatalf("cleanup unreachable: %v", err)
	}
	if cleaned.CleanupCandidates != 0 || cleaned.CleanupBytes != 0 {
		t.Fatalf("cleanup returned stale candidate totals: %+v", cleaned)
	}
	for _, removed := range []string{oldCache, oldStage} {
		if _, err := os.Lstat(removed); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old candidate remains %q: %v", filepath.Base(removed), err)
		}
	}
	if _, err := os.Stat(youngCache); err != nil {
		t.Fatalf("younger-than-seven-days cache was removed: %v", err)
	}
}

func TestRetentionFailsClosedOnCorruptGraphNamespaceRedirectAndPermissions(t *testing.T) {
	t.Run("corrupt generation", func(t *testing.T) {
		dataRoot, store, fixture := newRetentionStore(t, "generation-corrupt")
		candidate := writeRetentionCandidate(t, retentionMemoryRoot(dataRoot), "cache", ".cache", []byte("candidate"), retentionNow.Add(-8*24*time.Hour))
		generationPath := filepath.Join(retentionMemoryRoot(dataRoot), "generations", fixture.manifest.GenerationID+".json")
		if err := os.WriteFile(generationPath, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CleanupUnreachable(retentionNow); err == nil {
			t.Fatal("corrupt generation graph did not fail closed")
		}
		assertExists(t, candidate)
	})

	t.Run("unknown namespace", func(t *testing.T) {
		dataRoot, store, _ := newRetentionStore(t, "generation-namespace")
		candidate := writeRetentionCandidate(t, retentionMemoryRoot(dataRoot), "staging", ".stage", []byte("candidate"), retentionNow.Add(-8*24*time.Hour))
		if err := os.Mkdir(filepath.Join(retentionMemoryRoot(dataRoot), "unknown-state"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CleanupUnreachable(retentionNow); err == nil {
			t.Fatal("unknown memory namespace was accepted")
		}
		assertExists(t, candidate)
	})

	t.Run("noncanonical entry", func(t *testing.T) {
		dataRoot, store, _ := newRetentionStore(t, "generation-entry")
		candidate := writeRetentionCandidate(t, retentionMemoryRoot(dataRoot), "cache", ".cache", []byte("candidate"), retentionNow.Add(-8*24*time.Hour))
		if err := os.WriteFile(filepath.Join(retentionMemoryRoot(dataRoot), "staging", "not-canonical.tmp"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CleanupUnreachable(retentionNow); err == nil {
			t.Fatal("noncanonical staging entry was accepted")
		}
		assertExists(t, candidate)
	})

	t.Run("redirect", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("unprivileged symlink setup is unavailable; Windows reparse behavior is cross-compiled and covered by pathguard")
		}
		dataRoot, store, _ := newRetentionStore(t, "generation-redirect")
		memoryRoot := retentionMemoryRoot(dataRoot)
		outside := filepath.Join(t.TempDir(), "outside.cache")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte("outside"))
		link := filepath.Join(memoryRoot, "cache")
		if err := os.Mkdir(link, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(link, hex.EncodeToString(sum[:])+".cache")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := store.ReportRetention(retentionNow); err == nil {
			t.Fatal("redirected cache entry was accepted")
		}
		assertExists(t, outside)
	})

	t.Run("permission", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX mode contract")
		}
		dataRoot, store, _ := newRetentionStore(t, "generation-mode")
		candidate := writeRetentionCandidate(t, retentionMemoryRoot(dataRoot), "cache", ".cache", []byte("mode"), retentionNow.Add(-8*24*time.Hour))
		if err := os.Chmod(candidate, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CleanupUnreachable(retentionNow); err == nil {
			t.Fatal("public cache candidate was accepted")
		}
		assertExists(t, candidate)
	})
}

func TestRetentionRechecksIdentityMetadataAndNamespaceBeforeDelete(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, candidate, memoryRoot string)
	}{
		{
			name: "entry replacement",
			mutate: func(t *testing.T, candidate, _ string) {
				moved := candidate + ".moved"
				if err := os.Rename(candidate, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(candidate, []byte("replacement"), 0o600); err != nil {
					t.Fatal(err)
				}
				_ = os.Chtimes(candidate, retentionNow.Add(-9*24*time.Hour), retentionNow.Add(-9*24*time.Hour))
			},
		},
		{
			name: "metadata change",
			mutate: func(t *testing.T, candidate, _ string) {
				if err := os.Chtimes(candidate, retentionNow.Add(-9*24*time.Hour), retentionNow.Add(-9*24*time.Hour)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "namespace addition",
			mutate: func(t *testing.T, _, memoryRoot string) {
				writeRetentionCandidate(t, memoryRoot, "staging", ".stage", []byte("concurrent"), retentionNow.Add(-8*24*time.Hour))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataRoot, store, _ := newRetentionStore(t, "generation-toctou")
			memoryRoot := retentionMemoryRoot(dataRoot)
			candidate := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte("original"), retentionNow.Add(-8*24*time.Hour))
			var invoked atomic.Bool
			retentionDeleteCheckpoint = func() error {
				if invoked.CompareAndSwap(false, true) {
					test.mutate(t, candidate, memoryRoot)
				}
				return nil
			}
			t.Cleanup(func() { retentionDeleteCheckpoint = nil })
			if _, err := store.CleanupUnreachable(retentionNow); err == nil {
				t.Fatal("cleanup accepted a candidate changed after planning")
			}
			if !invoked.Load() {
				t.Fatal("delete revalidation checkpoint was not reached")
			}
			assertExists(t, candidate)
		})
	}
}

func TestRetentionRevalidatesReachabilityAnchorsBeforeEveryUnlink(t *testing.T) {
	t.Run("prepared-pointer-content", func(t *testing.T) {
		dataRoot, store, _ := newRetentionStore(t, "generation-anchor-pointer")
		memoryRoot := retentionMemoryRoot(dataRoot)
		candidate := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte("pointer-candidate"), retentionNow.Add(-8*24*time.Hour))
		assertAnchorMutationStopsCleanup(t, store, candidate, nil, func() {
			mutateRetentionFileInPlace(t, filepath.Join(memoryRoot, "manifest.json"))
		})
	})

	t.Run("prepared-generation-manifest", func(t *testing.T) {
		dataRoot, store, fixture := newRetentionStore(t, "generation-anchor-prepared")
		memoryRoot := retentionMemoryRoot(dataRoot)
		candidate := writeRetentionCandidate(t, memoryRoot, "staging", ".stage", []byte("prepared-candidate"), retentionNow.Add(-8*24*time.Hour))
		assertAnchorMutationStopsCleanup(t, store, candidate, nil, func() {
			mutateRetentionFileInPlace(t, filepath.Join(memoryRoot, "generations", fixture.manifest.GenerationID+".json"))
		})
	})

	t.Run("external-pinned-generation-manifest", func(t *testing.T) {
		dataRoot, store, pinned := newRetentionStore(t, "generation-anchor-pinned")
		successor := buildStoredFixture(t, store, "generation-anchor-current")
		expected, _, err := store.LoadPrepared()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AdvancePrepared(expected, successor.manifest); err != nil {
			t.Fatal(err)
		}
		memoryRoot := retentionMemoryRoot(dataRoot)
		candidate := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte("pinned-candidate"), retentionNow.Add(-8*24*time.Hour))
		assertAnchorMutationStopsCleanup(t, store, candidate, []string{pinned.manifest.GenerationID}, func() {
			mutateRetentionFileInPlace(t, filepath.Join(memoryRoot, "generations", pinned.manifest.GenerationID+".json"))
		})
	})

	t.Run("memory-root-identity", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows prevents renaming a directory while its authenticated lock handles are open")
		}
		dataRoot, store, _ := newRetentionStore(t, "generation-anchor-root")
		memoryRoot := retentionMemoryRoot(dataRoot)
		candidate := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte("root-candidate"), retentionNow.Add(-8*24*time.Hour))
		movedRoot := memoryRoot + ".moved"
		retained := filepath.Join(movedRoot, "cache", filepath.Base(candidate))
		assertAnchorMutationStopsCleanup(t, store, retained, nil, func() {
			if err := os.Rename(memoryRoot, movedRoot); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(memoryRoot, 0o700); err != nil {
				t.Fatal(err)
			}
		})
	})

	t.Run("namespace-identity", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows prevents renaming a directory while its authenticated lock handles are open")
		}
		dataRoot, store, _ := newRetentionStore(t, "generation-anchor-namespace")
		memoryRoot := retentionMemoryRoot(dataRoot)
		candidate := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte("namespace-candidate"), retentionNow.Add(-8*24*time.Hour))
		rootInfo, err := os.Stat(memoryRoot)
		if err != nil {
			t.Fatal(err)
		}
		movedCache := filepath.Join(t.TempDir(), "cache.moved")
		retained := filepath.Join(movedCache, filepath.Base(candidate))
		assertAnchorMutationStopsCleanup(t, store, retained, nil, func() {
			if err := os.Rename(filepath.Join(memoryRoot, "cache"), movedCache); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(memoryRoot, "cache"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(memoryRoot, rootInfo.ModTime(), rootInfo.ModTime()); err != nil {
				t.Fatal(err)
			}
		})
	})
}

func assertAnchorMutationStopsCleanup(t *testing.T, store *Store, retainedPath string, pins []string, mutate func()) {
	t.Helper()
	var invoked atomic.Bool
	retentionDeleteCheckpoint = func() error {
		if invoked.CompareAndSwap(false, true) {
			mutate()
		}
		return nil
	}
	t.Cleanup(func() { retentionDeleteCheckpoint = nil })
	if _, err := store.CleanupUnreachable(retentionNow, pins...); err == nil {
		t.Fatal("cleanup accepted a changed reachability anchor")
	}
	if !invoked.Load() {
		t.Fatal("anchor mutation checkpoint was not reached")
	}
	assertExists(t, retainedPath)
}

func mutateRetentionFileInPlace(t *testing.T, path string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 {
		t.Fatal("anchor fixture is empty")
	}
	body[len(body)/2] ^= 1
	if err := os.WriteFile(path, body, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionSerializesConcurrentAdvanceAndCopiesExplicitPins(t *testing.T) {
	dataRoot, store, first := newRetentionStore(t, "generation-concurrent-a")
	second := buildStoredFixture(t, store, "generation-concurrent-b")
	expected, _, err := store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	candidate := writeRetentionCandidate(t, retentionMemoryRoot(dataRoot), "cache", ".cache", []byte("concurrent-cleanup"), retentionNow.Add(-8*24*time.Hour))
	pins := []string{first.manifest.GenerationID}
	started := make(chan struct{})
	advanced := make(chan error, 1)
	var invoked atomic.Bool
	retentionDeleteCheckpoint = func() error {
		if invoked.CompareAndSwap(false, true) {
			pins[0] = "caller-mutated-pin"
			go func() {
				close(started)
				_, err := store.AdvancePrepared(expected, second.manifest)
				advanced <- err
			}()
			<-started
		}
		return nil
	}
	t.Cleanup(func() { retentionDeleteCheckpoint = nil })
	if _, err := store.CleanupUnreachable(retentionNow, pins...); err != nil {
		t.Fatalf("cleanup with copied external pin: %v", err)
	}
	if err := <-advanced; err != nil {
		t.Fatalf("concurrent prepared advance after cleanup: %v", err)
	}
	if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("eligible cache survived serialized cleanup: %v", err)
	}
	prepared, _, err := store.LoadPrepared()
	if err != nil || prepared.GenerationID != second.manifest.GenerationID {
		t.Fatalf("concurrent advance did not converge: prepared=%+v err=%v", prepared, err)
	}
	if _, err := store.ReportRetention(retentionNow, first.manifest.GenerationID, second.manifest.GenerationID); err != nil {
		t.Fatalf("native and explicit successor pins did not reconcile: %v", err)
	}
}

func TestRetentionSerializesConcurrentInitialPrepare(t *testing.T) {
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := buildStoredFixture(t, store, "generation-concurrent-initial")
	candidate := writeRetentionCandidate(t, retentionMemoryRoot(dataRoot), "staging", ".stage", []byte("initial-prepare"), retentionNow.Add(-8*24*time.Hour))
	prepared := make(chan error, 1)
	started := make(chan struct{})
	var invoked atomic.Bool
	retentionDeleteCheckpoint = func() error {
		if invoked.CompareAndSwap(false, true) {
			go func() {
				close(started)
				_, err := store.PrepareGeneration(fixture.manifest)
				prepared <- err
			}()
			<-started
		}
		return nil
	}
	t.Cleanup(func() { retentionDeleteCheckpoint = nil })
	if _, err := store.CleanupUnreachable(retentionNow); err != nil {
		t.Fatalf("cleanup before initial prepare: %v", err)
	}
	if err := <-prepared; err != nil {
		t.Fatalf("concurrent initial prepare after cleanup: %v", err)
	}
	if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("eligible staging survived serialized cleanup: %v", err)
	}
	current, manifest, err := store.LoadPrepared()
	if err != nil || current.GenerationID != fixture.manifest.GenerationID || manifest.GenerationID != fixture.manifest.GenerationID {
		t.Fatalf("initial prepare did not converge: current=%+v manifest=%+v err=%v", current, manifest, err)
	}
}

func TestRetentionRejectsInvalidPinBeforeAnyDeletion(t *testing.T) {
	dataRoot, store, _ := newRetentionStore(t, "generation-pin-fail")
	candidate := writeRetentionCandidate(t, retentionMemoryRoot(dataRoot), "staging", ".stage", []byte("pin-fail"), retentionNow.Add(-8*24*time.Hour))
	if _, err := store.CleanupUnreachable(retentionNow, "missing-generation"); err == nil {
		t.Fatal("cleanup accepted a missing external generation pin")
	}
	assertExists(t, candidate)
}

func TestRetentionCleanupIsRestartableAfterPartialProcessExit(t *testing.T) {
	if os.Getenv("SESSION_REVIEWER_RETENTION_CRASH_HELPER") == "1" {
		retentionCleanupCrashHelper(t)
		return
	}
	dataRoot, store, _ := newRetentionStore(t, "generation-crash")
	memoryRoot := retentionMemoryRoot(dataRoot)
	first := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte("a-first"), retentionNow.Add(-8*24*time.Hour))
	second := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte("z-second"), retentionNow.Add(-8*24*time.Hour))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestRetentionCleanupIsRestartableAfterPartialProcessExit$")
	command.Env = append(os.Environ(), "SESSION_REVIEWER_RETENTION_CRASH_HELPER=1", "SESSION_REVIEWER_RETENTION_DATA_ROOT="+dataRoot)
	if err := command.Run(); err == nil {
		t.Fatal("retention crash helper did not exit")
	}
	remaining := 0
	for _, candidate := range []string{first, second} {
		if _, err := os.Lstat(candidate); err == nil {
			remaining++
		}
	}
	if remaining != 1 {
		t.Fatalf("partial cleanup removed %d candidates, want exactly one", 2-remaining)
	}

	restarted, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatalf("reopen after cleanup crash: %v", err)
	}
	defer restarted.Close()
	if _, err := restarted.CleanupUnreachable(retentionNow); err != nil {
		t.Fatalf("resume cleanup: %v", err)
	}
	for _, candidate := range []string{first, second} {
		if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("candidate remains after restart %q: %v", filepath.Base(candidate), err)
		}
	}
}

func TestRetentionCleanupUsesOneFullSnapshotAndLinearCandidateRevalidation(t *testing.T) {
	dataRoot, store, _ := newRetentionStore(t, "generation-linear")
	memoryRoot := retentionMemoryRoot(dataRoot)
	const candidates = 64
	for index := 0; index < candidates; index++ {
		writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte(fmt.Sprintf("linear-%03d", index)), retentionNow.Add(-8*24*time.Hour))
	}
	var snapshots, revalidations atomic.Int32
	retentionFullSnapshotCheckpoint = func() { snapshots.Add(1) }
	retentionCandidateRevalidationCheckpoint = func() { revalidations.Add(1) }
	t.Cleanup(func() {
		retentionFullSnapshotCheckpoint = nil
		retentionCandidateRevalidationCheckpoint = nil
	})
	report, err := store.CleanupUnreachableContext(context.Background(), retentionNow)
	if err != nil {
		t.Fatal(err)
	}
	if snapshots.Load() != 1 || revalidations.Load() != candidates {
		t.Fatalf("cleanup complexity snapshots=%d revalidations=%d want 1/%d", snapshots.Load(), revalidations.Load(), candidates)
	}
	if report.CleanupCandidates != 0 || report.CleanupBytes != 0 {
		t.Fatalf("completed cleanup report=%+v", report)
	}
}

func TestRetentionCleanupCancellationReturnsValidPartialReportAndCanResume(t *testing.T) {
	dataRoot, store, _ := newRetentionStore(t, "generation-cancel")
	memoryRoot := retentionMemoryRoot(dataRoot)
	paths := make([]string, 5)
	for index := range paths {
		paths[index] = writeRetentionCandidate(t, memoryRoot, "staging", ".stage", []byte(fmt.Sprintf("cancel-%d", index)), retentionNow.Add(-8*24*time.Hour))
	}
	ctx, cancel := context.WithCancel(context.Background())
	var checkpoints atomic.Int32
	retentionDeleteCheckpoint = func() error {
		if checkpoints.Add(1) == 2 {
			cancel()
		}
		return nil
	}
	t.Cleanup(func() { retentionDeleteCheckpoint = nil })
	report, err := store.CleanupUnreachableContext(ctx, retentionNow)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cleanup cancellation error=%v", err)
	}
	if report.CleanupCandidates != 4 {
		t.Fatalf("partial cleanup report=%+v want 4 remaining candidates", report)
	}
	remaining := 0
	for _, path := range paths {
		if _, statErr := os.Lstat(path); statErr == nil {
			remaining++
		} else if !errors.Is(statErr, os.ErrNotExist) {
			t.Fatal(statErr)
		}
	}
	if remaining != 4 {
		t.Fatalf("cancelled cleanup left %d candidates, want 4", remaining)
	}
	if current, reportErr := store.ReportRetention(retentionNow); reportErr != nil || current.CleanupCandidates != 4 {
		t.Fatalf("partial store is not reportable: report=%+v err=%v", current, reportErr)
	}
	retentionDeleteCheckpoint = nil
	if _, err := store.CleanupUnreachable(retentionNow); err != nil {
		t.Fatalf("resume partial cleanup: %v", err)
	}
}

func TestRetentionSnapshotTraversalReturnsContextCauseWithoutMutation(t *testing.T) {
	for _, operation := range []struct {
		name string
		run  func(*Store, context.Context) error
	}{
		{name: "report", run: func(store *Store, ctx context.Context) error {
			_, err := store.ReportRetentionContext(ctx, retentionNow)
			return err
		}},
		{name: "cleanup-capture", run: func(store *Store, ctx context.Context) error {
			_, err := store.CleanupUnreachableContext(ctx, retentionNow)
			return err
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			dataRoot, store, _ := newRetentionStore(t, "generation-snapshot-cancel-"+operation.name)
			memoryRoot := retentionMemoryRoot(dataRoot)
			for index := 0; index < 256; index++ {
				writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte(fmt.Sprintf("snapshot-cancel-%03d", index)), retentionNow.Add(-8*24*time.Hour))
			}
			before := retentionInventory(t, memoryRoot)
			ctx, cancel := context.WithCancelCause(context.Background())
			cause := errors.New("retention-snapshot-cause")
			var traversed atomic.Int32
			retentionTraversalCheckpoint = func() {
				if traversed.Add(1) == 12 {
					cancel(cause)
				}
			}
			t.Cleanup(func() { retentionTraversalCheckpoint = nil })
			if err := operation.run(store, ctx); !errors.Is(err, cause) {
				t.Fatalf("snapshot cancellation error=%v want cause", err)
			}
			if after := retentionInventory(t, memoryRoot); !equalRetentionInventory(before, after) {
				t.Fatal("cancelled retention snapshot mutated the store")
			}
		})
	}
}

func retentionCleanupCrashHelper(t *testing.T) {
	dataRoot := os.Getenv("SESSION_REVIEWER_RETENTION_DATA_ROOT")
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoints atomic.Int32
	retentionDeleteCheckpoint = func() error {
		if checkpoints.Add(1) == 2 {
			os.Exit(93)
		}
		return nil
	}
	_, _ = store.CleanupUnreachable(retentionNow)
	os.Exit(94)
}

func newRetentionStore(t *testing.T, generationID string) (string, *Store, storedFixture) {
	t.Helper()
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixture := buildStoredFixture(t, store, generationID)
	if _, err := store.PrepareGeneration(fixture.manifest); err != nil {
		t.Fatalf("prepare generation: %v", err)
	}
	return dataRoot, store, fixture
}

func retentionMemoryRoot(dataRoot string) string {
	return filepath.Join(dataRoot, "projects", testProjectID, "memory-v1")
}

func writeRetentionCandidate(t *testing.T, memoryRoot, namespace, suffix string, body []byte, modified time.Time) string {
	t.Helper()
	directory := filepath.Join(memoryRoot, namespace)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	path := filepath.Join(directory, hex.EncodeToString(sum[:])+suffix)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	return path
}

func reachableFixtureTotals(t *testing.T, memoryRoot string, manifest memory.GenerationManifest) (int, int64) {
	t.Helper()
	sessionPath := filepath.Join(memoryRoot, "sessions", digestLeaf(manifest.SessionViews[0].Digest, ".json"))
	body, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	var session memory.SessionView
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(memoryRoot, "generations", manifest.GenerationID+".json"),
		filepath.Join(memoryRoot, "observations", digestLeaf(session.ObservationChunkDigests[0], ".jsonl")),
		sessionPath,
		filepath.Join(memoryRoot, "session-lineages", digestLeaf(manifest.SessionLineages[0].Digest, ".json")),
		filepath.Join(memoryRoot, "project-probes", digestLeaf(manifest.ProbeStateDigest, ".json")),
		filepath.Join(memoryRoot, "project-views", digestLeaf(manifest.ProjectViewDigest, ".json")),
	}
	var total int64
	for _, path := range paths {
		total += fileSize(t, path)
	}
	return len(paths), total
}

type retentionInventoryEntry struct {
	path       string
	size       int64
	mode       os.FileMode
	modified   time.Time
	contentSum string
}

func retentionInventory(t *testing.T, root string) []retentionInventoryEntry {
	t.Helper()
	var entries []retentionInventoryEntry
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		digest := ""
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(body)
			digest = hex.EncodeToString(sum[:])
		}
		relative, _ := filepath.Rel(root, path)
		modified := time.Time{}
		if info.Mode().IsRegular() {
			modified = info.ModTime()
		}
		entries = append(entries, retentionInventoryEntry{path: filepath.ToSlash(relative), size: info.Size(), mode: info.Mode(), modified: modified, contentSum: digest})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return entries
}

func equalRetentionInventory(first, second []retentionInventoryEntry) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index].path != second[index].path || first[index].size != second[index].size || first[index].mode != second[index].mode || !first[index].modified.Equal(second[index].modified) || first[index].contentSum != second[index].contentSum {
			return false
		}
	}
	return true
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("expected path to remain %q: %v", path, err)
	}
}

func TestRetentionCandidateNamesArePortableAndContentAddressed(t *testing.T) {
	for _, body := range [][]byte{[]byte("cache"), []byte(strings.Repeat("x", 1024))} {
		sum := sha256.Sum256(body)
		name := hex.EncodeToString(sum[:]) + ".cache"
		if strings.ContainsAny(name, `/\\:`) || len(name) != 70 {
			t.Fatalf("nonportable canonical candidate name %q", name)
		}
		decoded, err := hex.DecodeString(strings.TrimSuffix(name, ".cache"))
		if err != nil || !bytes.Equal(decoded, sum[:]) {
			t.Fatalf("candidate name does not authenticate content: %q", name)
		}
	}
}
