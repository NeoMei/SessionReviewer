package scan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/projectprobe"
	"github.com/neomei/SessionReviewer/internal/projectview"
	"github.com/neomei/SessionReviewer/internal/sessionview"
	"github.com/neomei/SessionReviewer/internal/source"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

const (
	scanTestProject = "project-a"
	scanTestForeign = "project-b"
	scanStartedAt   = "2026-08-31T10:00:00Z"
	scanEndedAt     = "2026-08-31T10:01:00Z"
)

type fakeSourceSpec struct {
	candidate      source.Candidate
	boundary       source.Boundary
	record         memory.SourceRecord
	observations   []memory.ObservationRevision
	report         source.DecodeReport
	freezeErr      error
	decodeErr      error
	freezePanic    bool
	decodePanic    bool
	skipCatalog    bool
	wait           <-chan struct{}
	frozenExpected string
}

type fakeAdapter struct {
	catalog     *sourcecatalog.Catalog
	sources     map[string]*fakeSourceSpec
	issues      []source.Issue
	discoverErr error

	mu              sync.Mutex
	active          int
	maxActive       int
	decoded         []string
	decodeErrors    map[string]error
	decodeStarted   chan string
	afterDiscover   func()
	afterFreeze     func(source.Boundary)
	leaseSequence   int
	candidateLeases map[string]string
	boundaryLeases  map[string]string
}

func (adapter *fakeAdapter) Discover(ctx context.Context) (source.Discovery, error) {
	if err := ctx.Err(); err != nil {
		return source.Discovery{}, err
	}
	candidates := make([]source.Candidate, 0, len(adapter.sources))
	for _, spec := range adapter.sources {
		candidate := spec.candidate
		adapter.mu.Lock()
		adapter.leaseSequence++
		candidate.Lease = fmt.Sprintf("candidate-lease-%d", adapter.leaseSequence)
		adapter.candidateLeases[candidate.Lease] = candidate.Handle
		adapter.mu.Unlock()
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].SessionID < candidates[j].SessionID
	})
	result := source.Discovery{Candidates: candidates, Issues: append([]source.Issue(nil), adapter.issues...)}
	if adapter.afterDiscover != nil {
		adapter.afterDiscover()
	}
	return result, adapter.discoverErr
}

func (adapter *fakeAdapter) Freeze(ctx context.Context, candidate source.Candidate) (source.Boundary, error) {
	if err := ctx.Err(); err != nil {
		return source.Boundary{}, err
	}
	spec := adapter.sources[candidate.SessionID]
	stable := candidate
	stable.Lease = ""
	adapter.mu.Lock()
	owned := adapter.candidateLeases[candidate.Lease] == candidate.Handle
	adapter.mu.Unlock()
	if spec == nil || !owned || !reflect.DeepEqual(spec.candidate, stable) {
		return source.Boundary{}, errors.New("unknown fake candidate")
	}
	if spec.freezePanic {
		panic("fake freeze panic")
	}
	if spec.freezeErr != nil {
		return source.Boundary{}, spec.freezeErr
	}
	existing, found, err := adapter.catalog.GetSource(spec.record.Provider, spec.record.SessionID)
	if err != nil {
		return source.Boundary{}, err
	}
	spec.frozenExpected = ""
	if found {
		spec.frozenExpected, err = memory.Digest(existing)
		if err != nil {
			return source.Boundary{}, err
		}
	}
	boundary := spec.boundary
	boundary.Candidate = candidate
	adapter.mu.Lock()
	delete(adapter.candidateLeases, candidate.Lease)
	adapter.leaseSequence++
	boundary.Lease = fmt.Sprintf("boundary-lease-%d", adapter.leaseSequence)
	adapter.boundaryLeases[boundary.Lease] = boundary.Handle
	adapter.mu.Unlock()
	if adapter.afterFreeze != nil {
		adapter.afterFreeze(boundary)
	}
	return boundary, nil
}

func (adapter *fakeAdapter) Decode(ctx context.Context, boundary source.Boundary, visit func(memory.ObservationRevision) error) (returned source.DecodeReport, returnedErr error) {
	spec := adapter.sources[boundary.Candidate.SessionID]
	if spec == nil {
		return source.DecodeReport{}, errors.New("unknown fake boundary")
	}
	adapter.mu.Lock()
	owned := adapter.boundaryLeases[boundary.Lease] == boundary.Handle
	adapter.mu.Unlock()
	if !owned {
		return source.DecodeReport{}, errors.New("unknown fake boundary lease")
	}
	if spec.decodePanic {
		panic("fake decode panic")
	}
	adapter.mu.Lock()
	adapter.active++
	if adapter.active > adapter.maxActive {
		adapter.maxActive = adapter.active
	}
	adapter.mu.Unlock()
	defer func() {
		adapter.mu.Lock()
		if returnedErr == nil {
			delete(adapter.boundaryLeases, boundary.Lease)
		}
		adapter.active--
		adapter.decoded = append(adapter.decoded, boundary.Candidate.SessionID)
		if returnedErr != nil {
			adapter.decodeErrors[boundary.Candidate.SessionID] = returnedErr
		}
		adapter.mu.Unlock()
	}()
	if adapter.decodeStarted != nil {
		select {
		case adapter.decodeStarted <- boundary.Candidate.SessionID:
		case <-ctx.Done():
			return source.DecodeReport{}, ctx.Err()
		}
	}
	if spec.wait != nil {
		select {
		case <-spec.wait:
		case <-ctx.Done():
			return source.DecodeReport{}, ctx.Err()
		}
	}
	if spec.decodeErr != nil {
		return spec.report, spec.decodeErr
	}
	report := spec.report
	report.EmittedRevisions = 0
	for _, observation := range spec.observations {
		if err := visit(observation); err != nil {
			return report, err
		}
		report.EmittedRevisions++
	}
	if !spec.skipCatalog {
		report.ProposedSource = spec.record
		report.ExpectedCatalogDigest = spec.frozenExpected
	}
	return report, nil
}

func (adapter *fakeAdapter) AbandonCandidate(candidate source.Candidate) {
	adapter.mu.Lock()
	if adapter.candidateLeases[candidate.Lease] == candidate.Handle {
		delete(adapter.candidateLeases, candidate.Lease)
	}
	adapter.mu.Unlock()
}

func (adapter *fakeAdapter) AbandonBoundary(boundary source.Boundary) {
	adapter.mu.Lock()
	if adapter.boundaryLeases[boundary.Lease] == boundary.Handle {
		delete(adapter.boundaryLeases, boundary.Lease)
	}
	adapter.mu.Unlock()
}

func (adapter *fakeAdapter) leaseCounts() (int, int) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return len(adapter.candidateLeases), len(adapter.boundaryLeases)
}

func (*fakeAdapter) Read(context.Context, memory.SourceRef, int64) ([]byte, error) {
	return nil, errors.New("read is not used by zero-token scan")
}

type scanHarness struct {
	options Options
	adapter *fakeAdapter
	catalog *sourcecatalog.Catalog
	store   *memorystore.Store
}

type failingMemoryStore struct {
	MemoryStore
	failObservation bool
}

func (store *failingMemoryStore) PutObservationChunk(records []memory.ObservationRevision) (string, error) {
	if store.failObservation {
		store.failObservation = false
		return "", errors.New("object-store-canary")
	}
	return store.MemoryStore.PutObservationChunk(records)
}

func newScanHarness(t *testing.T) scanHarness {
	t.Helper()
	projectRoot := t.TempDir()
	dataRoot := t.TempDir()
	sessionsRoot := t.TempDir()
	binding, err := projectidentity.Resolve(config.ProjectMapping{ID: scanTestProject, Root: projectRoot}, projectRoot, runtime.GOOS)
	if err != nil {
		t.Fatalf("resolve project identity: %v", err)
	}
	catalog, err := sourcecatalog.Open(dataRoot)
	if err != nil {
		t.Fatalf("open source catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	store, err := memorystore.Open(dataRoot, scanTestProject)
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	adapter := &fakeAdapter{
		catalog: catalog, sources: make(map[string]*fakeSourceSpec), decodeErrors: make(map[string]error),
		candidateLeases: make(map[string]string), boundaryLeases: make(map[string]string),
	}
	now := time.Date(2026, 8, 31, 10, 2, 0, 0, time.UTC)
	options := Options{
		ProjectID: scanTestProject, Binding: binding, SessionsRoot: sessionsRoot,
		DataRoot: dataRoot, Adapter: adapter, Catalog: catalog, Store: store,
		Workers: 8, Now: func() time.Time { return now },
		Materialize: sessionview.Materialize,
		Probe: func(ctx context.Context, options projectprobe.Options) (memory.ProjectProbeState, memory.ProbeCheck, error) {
			if err := ctx.Err(); err != nil {
				return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
			}
			state := memory.ProjectProbeState{
				SchemaVersion: memory.MemorySchemaVersion, ProjectID: options.Binding.ProjectID,
				CanonicalRoot: options.Binding.CanonicalRoot, RemoteIdentityHashes: []string{},
				VersionFiles: []memory.ProbeFile{}, RequiredProjectionFiles: []memory.ProbeFile{},
				ProbeVersion: projectprobe.ProbeVersion, Diagnostics: []memory.Diagnostic{},
			}
			state.Digest, err = memory.ProjectProbeStateDigest(state)
			if err != nil {
				return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
			}
			check := memory.ProbeCheck{SchemaVersion: memory.MemorySchemaVersion, CheckedAt: now.Format(time.RFC3339Nano), StateDigest: state.Digest, Available: true, Diagnostics: []memory.Diagnostic{}}
			return state, check, nil
		},
		Reduce: projectview.Reduce,
	}
	return scanHarness{options: options, adapter: adapter, catalog: catalog, store: store}
}

func (h scanHarness) addSource(index int, state memory.TerminalState, projects ...string) *fakeSourceSpec {
	sessionID := "session-" + strconv.Itoa(index)
	sourceIdentity := "source-" + strconv.Itoa(index)
	candidate := source.Candidate{Provider: "codex", SessionID: sessionID, StartedAt: scanStartedAt, InitialCWD: h.options.Binding.CanonicalRoot, Handle: "candidate-" + strconv.Itoa(index)}
	boundary := source.Boundary{
		Candidate: candidate, SourceIdentity: sourceIdentity,
		Frozen:        memory.FrozenBoundary{Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: 2, ByteOffset: int64(100 + index)}}, SourceHash: scanHex("boundary-" + sessionID)},
		Segments:      []source.SegmentBoundary{{Ordinal: 1, Size: int64(100 + index), SourceHash: scanHex("segment-" + sessionID)}},
		TerminalState: memory.Indexed, Handle: "boundary-" + strconv.Itoa(index),
	}
	record := memory.SourceRecord{
		SchemaVersion: memory.MemorySchemaVersion, Provider: "codex", SessionID: sessionID,
		SourceIdentity: sourceIdentity, StartedAt: scanStartedAt, EndedAt: scanEndedAt,
		FrozenBoundary: boundary.Frozen, Availability: memory.SourceAvailable,
		Usage: accounting.SessionUsage{
			StartedAt: scanStartedAt, EndedAt: scanEndedAt, DurationMS: 60_000,
			Models: []accounting.ModelUsage{{Model: "fixture-model", TokenUsage: accounting.TokenUsage{}}}, TotalTokens: 0,
		},
		ProjectIDs: append([]string(nil), projects...),
	}
	observation := memory.ObservationRevision{
		SchemaVersion: memory.MemorySchemaVersion,
		Key:           memory.ObservationKey{Provider: "codex", SessionID: sessionID, SourceIdentity: sourceIdentity, Sequence: 2, ProjectID: scanTestProject, Kind: "file", Subject: "file-" + strconv.Itoa(index)},
		Ref:           memory.SourceRef{Provider: "codex", SessionID: sessionID, SourceIdentity: sourceIdentity, Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: 2, ByteOffset: 40}}, SourceHash: scanHex("record-" + sessionID)},
		Timestamp:     scanEndedAt, Operation: "file_change", Object: "file-" + strconv.Itoa(index), Outcome: "success",
		Fields: map[string]string{"path": "file-" + strconv.Itoa(index)}, AdapterID: "fake-codex", AdapterVersion: "v1",
	}
	observation.RevisionID = memory.ObservationRevisionID(observation)
	spec := &fakeSourceSpec{candidate: candidate, boundary: boundary, record: record, observations: []memory.ObservationRevision{observation}, report: source.DecodeReport{BoundaryRelation: source.BoundaryInitial, TerminalState: state}}
	h.adapter.sources[sessionID] = spec
	return spec
}

func TestRunCompletes154SessionsWithoutAgentOrForeignFacts(t *testing.T) {
	harness := newScanHarness(t)
	for index := 1; index <= 151; index++ {
		harness.addSource(index, memory.Indexed, scanTestProject)
	}
	unsupported := harness.addSource(152, memory.Unsupported, scanTestProject)
	unsupported.observations = nil
	unsupported.report.UnsupportedRecords = 1
	malformed := harness.addSource(153, memory.Unreadable, scanTestProject)
	malformed.observations = nil
	malformed.report.MalformedLines = 1
	ambiguous := harness.addSource(154, memory.Ambiguous, scanTestProject, scanTestForeign)
	foreign := ambiguous.observations[0]
	foreign.Key.ProjectID = scanTestForeign
	foreign.Key.Subject = "foreign-only"
	foreign.RevisionID = memory.ObservationRevisionID(foreign)
	ambiguous.observations = append(ambiguous.observations, foreign)
	for sessionID, spec := range harness.adapter.sources {
		if err := memory.ValidateSourceRecord(spec.record); err != nil {
			t.Fatalf("invalid fake source %s: %v", sessionID, err)
		}
		for _, observation := range spec.observations {
			if err := memory.ValidateObservationRevision(observation); err != nil {
				t.Fatalf("invalid fake observation %s: %v", sessionID, err)
			}
		}
	}

	result, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatalf("run complete project scan: %v; decode errors: %v", err, harness.adapter.decodeErrors)
	}
	if result.SchemaVersion != 1 || result.ProjectID != scanTestProject || result.GenerationID == "" {
		t.Fatalf("invalid result identity: %+v", result)
	}
	if result.State != CompletedWithIssues || result.SourceSessions != 154 || result.TerminalSessions != 154 || result.IndexedSessions != 151 || result.IssueSessions != 3 {
		t.Fatalf("unexpected terminal reconciliation: %+v", result)
	}
	if result.ReviewRunTokens != 0 || !result.Prepared || result.ProjectViewDigest == "" {
		t.Fatalf("scan was not a zero-token prepared result: %+v", result)
	}
	prepared, manifest, err := harness.store.LoadPrepared()
	if err != nil || prepared.GenerationID != result.GenerationID || prepared.ProjectViewDigest != result.ProjectViewDigest || len(manifest.SessionViews) != 154 {
		t.Fatalf("prepared generation mismatch: prepared=%#v sessions=%d err=%v", prepared, len(manifest.SessionViews), err)
	}
	projectBody, err := harness.store.LoadObject(memorystore.ObjectProjectView, manifest.ProjectViewDigest)
	if err != nil {
		t.Fatalf("load ProjectView: %v", err)
	}
	var projectView memory.ProjectView
	if err := decodeExactJSON(projectBody, &projectView); err != nil {
		t.Fatalf("decode ProjectView: %v", err)
	}
	if len(projectView.AssociatedUsage) != 154 {
		t.Fatalf("associated usage count=%d want 154", len(projectView.AssociatedUsage))
	}
	sharedFound := false
	for _, usage := range projectView.AssociatedUsage {
		if usage.SessionID == ambiguous.candidate.SessionID {
			sharedFound = usage.Shared
		}
	}
	if !sharedFound {
		t.Fatal("shared Session usage was not marked shared")
	}
	for _, digest := range manifest.ObservationChunkDigests {
		body, err := harness.store.LoadObject(memorystore.ObjectObservationChunk, digest)
		if err != nil {
			t.Fatalf("load observation chunk: %v", err)
		}
		if string(body) == "" || containsBytes(body, []byte(scanTestForeign)) || containsBytes(body, []byte("foreign-only")) {
			t.Fatalf("foreign project fact entered target store: %s", body)
		}
	}
	maximum := minScanInt(4, runtime.GOMAXPROCS(0))
	if harness.adapter.maxActive > maximum {
		t.Fatalf("decode concurrency=%d exceeds %d", harness.adapter.maxActive, maximum)
	}
	projectEntries, err := os.ReadDir(harness.options.Binding.CanonicalRoot)
	if err != nil || len(projectEntries) != 0 {
		t.Fatalf("Gate A wrote into Project root: entries=%v err=%v", projectEntries, err)
	}
}

func TestRunScopesTerminalIssuesThroughPriorCatalogAssociation(t *testing.T) {
	harness := newScanHarness(t)
	for index := 1; index <= 3; index++ {
		harness.addSource(index, memory.Indexed, scanTestProject)
	}
	nonTarget := harness.addSource(90, memory.Indexed, scanTestForeign)
	if _, err := harness.catalog.UpsertSource(nonTarget.record); err != nil {
		t.Fatalf("seed non-target catalog record: %v", err)
	}
	delete(harness.adapter.sources, nonTarget.candidate.SessionID)
	first, err := Run(context.Background(), harness.options)
	if err != nil || first.SourceSessions != 3 || !first.Prepared {
		t.Fatalf("prepare issue baseline: result=%+v err=%v", first, err)
	}
	_, baselineManifest, err := harness.store.LoadPrepared()
	if err != nil {
		t.Fatalf("load issue baseline: %v", err)
	}

	harness.adapter.sources = map[string]*fakeSourceSpec{}
	harness.adapter.issues = []source.Issue{
		{Provider: "codex", SessionID: "session-1", Code: "missing_segment", TerminalState: memory.Missing, Path: "/private/canary-must-not-persist"},
		{Provider: "codex", SessionID: "session-2", Code: "unreadable_segment", TerminalState: memory.Unreadable, Path: "/private/unreadable-canary"},
		{Provider: "codex", SessionID: "session-3", Code: "duplicate_segment", TerminalState: memory.Ambiguous, Path: "/private/ambiguous-canary"},
		{Provider: "codex", SessionID: "first-seen", Code: "missing_segment", TerminalState: memory.Missing, Path: "/private/first-seen-canary"},
		{Provider: "codex", SessionID: nonTarget.candidate.SessionID, Code: "unreadable_segment", TerminalState: memory.Unreadable, Path: "/private/non-target-canary"},
	}
	second, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatalf("run catalog-scoped issue scan: %v", err)
	}
	if second.SourceSessions != 3 || second.TerminalSessions != 3 || second.IndexedSessions != 0 || second.IssueSessions != 3 || second.State != CompletedWithIssues {
		t.Fatalf("issue scope/count mismatch: %+v", second)
	}
	if second.GenerationID == baselineManifest.GenerationID {
		t.Fatal("missing-source availability did not advance prepared generation")
	}
	_, manifest, err := harness.store.LoadPrepared()
	if err != nil || len(manifest.SessionViews) != 3 {
		t.Fatalf("load issue successor: sessions=%d err=%v", len(manifest.SessionViews), err)
	}
	for _, dependency := range manifest.SessionViews {
		body, err := harness.store.LoadObject(memorystore.ObjectSessionView, dependency.Digest)
		if err != nil {
			t.Fatalf("load issue SessionView: %v", err)
		}
		var view memory.SessionView
		if err := decodeExactJSON(body, &view); err != nil {
			t.Fatalf("decode issue SessionView: %v", err)
		}
		wantState := map[string]memory.TerminalState{"session-1": memory.Missing, "session-2": memory.Unreadable, "session-3": memory.Ambiguous}[view.SessionID]
		if view.TerminalState != wantState || view.SourceAvailability != memory.SourceUnavailable || len(view.ActiveRevisionIDs) != 1 {
			t.Fatalf("issue did not preserve prior facts honestly: %+v", view)
		}
		if containsBytes(body, []byte("/private/")) || containsBytes(body, []byte("canary")) {
			t.Fatalf("issue path/text entered SessionView: %s", body)
		}
	}
	for _, excluded := range []string{"first-seen", nonTarget.candidate.SessionID} {
		for _, dependency := range manifest.SessionViews {
			if dependency.SessionID == excluded {
				t.Fatalf("unassociated issue %q entered target generation", excluded)
			}
		}
	}
}

func TestRunAdvancesSupersededAndWithdrawnRevisionsThenReusesUnchangedGeneration(t *testing.T) {
	harness := newScanHarness(t)
	spec := harness.addSource(1, memory.Indexed, scanTestProject)
	secondOld := spec.observations[0]
	secondOld.Key.Sequence = 3
	secondOld.Key.Subject = "removed-file"
	secondOld.Ref.Location.JSONL = &memory.JSONLSourceLocation{Line: 3, ByteOffset: 80}
	secondOld.Ref.SourceHash = scanHex("removed-old")
	secondOld.Object = "removed-file"
	secondOld.Fields = map[string]string{"path": "removed-file"}
	secondOld.RevisionID = memory.ObservationRevisionID(secondOld)
	spec.observations = append(spec.observations, secondOld)
	spec.boundary.Frozen.Location.JSONL = &memory.JSONLSourceLocation{Line: 4, ByteOffset: 200}
	spec.boundary.Frozen.SourceHash = scanHex("boundary-initial")
	spec.record.FrozenBoundary = spec.boundary.Frozen

	first, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatalf("prepare initial revision set: %v", err)
	}
	_, firstManifest, err := harness.store.LoadPrepared()
	if err != nil {
		t.Fatalf("load initial generation: %v", err)
	}
	firstKey := observationKeyDigestForScan(t, spec.observations[0].Key)
	removedKey := observationKeyDigestForScan(t, secondOld.Key)
	oldFirstRevision := firstManifest.ActiveRevisions[firstKey]
	oldRemovedRevision := firstManifest.ActiveRevisions[removedKey]

	successor := spec.observations[0]
	successor.AdapterVersion = "v2"
	successor.Outcome = "updated"
	successor.RevisionID = memory.ObservationRevisionID(successor)
	added := successor
	added.Key.Sequence = 4
	added.Key.Subject = "added-file"
	added.Ref.Location.JSONL = &memory.JSONLSourceLocation{Line: 4, ByteOffset: 120}
	added.Ref.SourceHash = scanHex("added-new")
	added.Object = "added-file"
	added.Fields = map[string]string{"path": "added-file"}
	added.RevisionID = memory.ObservationRevisionID(added)
	spec.observations = []memory.ObservationRevision{successor, added}
	spec.report.BoundaryRelation = source.BoundaryAppend
	spec.boundary.Frozen.Location.JSONL = &memory.JSONLSourceLocation{Line: 5, ByteOffset: 300}
	spec.boundary.Frozen.SourceHash = scanHex("boundary-successor")
	spec.record.FrozenBoundary = spec.boundary.Frozen
	spec.record.EndedAt = "2026-08-31T10:02:00Z"
	spec.record.Usage.EndedAt = spec.record.EndedAt
	spec.record.Usage.DurationMS = 120_000

	second, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatalf("advance revision set: %v", err)
	}
	if second.GenerationID == first.GenerationID {
		t.Fatal("changed revision set reused initial generation")
	}
	_, secondManifest, err := harness.store.LoadPrepared()
	if err != nil {
		t.Fatalf("load revision successor: %v", err)
	}
	if secondManifest.ActiveRevisions[firstKey] != successor.RevisionID || secondManifest.SupersededRevisions[oldFirstRevision] != successor.RevisionID {
		t.Fatalf("supersession lineage mismatch: active=%v superseded=%v", secondManifest.ActiveRevisions, secondManifest.SupersededRevisions)
	}
	if secondManifest.WithdrawnRevisions[removedKey] != oldRemovedRevision {
		t.Fatalf("withdrawal lineage mismatch: %v", secondManifest.WithdrawnRevisions)
	}
	if len(secondManifest.ObservationChunkDigests) != 2 {
		t.Fatalf("append/successor scan did not retain old plus new chunks: %v", secondManifest.ObservationChunkDigests)
	}

	third, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatalf("run unchanged replay: %v", err)
	}
	if third.GenerationID != second.GenerationID || third.ProjectViewDigest != second.ProjectViewDigest {
		t.Fatalf("unchanged replay churned generation: second=%+v third=%+v", second, third)
	}
	generationEntries, err := os.ReadDir(filepath.Join(harness.options.DataRoot, "projects", scanTestProject, "memory-v1", "generations"))
	if err != nil || len(generationEntries) != 2 {
		t.Fatalf("unchanged replay wrote another generation: entries=%d err=%v", len(generationEntries), err)
	}
}

func TestRunAppendReusesOldChunksAndPersistsOnlyNewSuffixRevision(t *testing.T) {
	harness := newScanHarness(t)
	spec := harness.addSource(1, memory.Indexed, scanTestProject)
	oldSecond := spec.observations[0]
	oldSecond.Key.Sequence = 3
	oldSecond.Key.Subject = "old-second"
	oldSecond.Ref.Location.JSONL = &memory.JSONLSourceLocation{Line: 3, ByteOffset: 80}
	oldSecond.Ref.SourceHash = scanHex("old-second")
	oldSecond.Object = "old-second"
	oldSecond.Fields = map[string]string{"path": "old-second"}
	oldSecond.RevisionID = memory.ObservationRevisionID(oldSecond)
	spec.observations = append(spec.observations, oldSecond)
	first, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatalf("prepare append baseline: %v", err)
	}
	_, firstManifest, err := harness.store.LoadPrepared()
	if err != nil || len(firstManifest.ObservationChunkDigests) != 1 {
		t.Fatalf("append baseline chunks=%v err=%v", firstManifest.ObservationChunkDigests, err)
	}
	oldChunk := firstManifest.ObservationChunkDigests[0]

	newSuffix := oldSecond
	newSuffix.Key.Sequence = 4
	newSuffix.Key.Subject = "new-suffix"
	newSuffix.Ref.Location.JSONL = &memory.JSONLSourceLocation{Line: 4, ByteOffset: 120}
	newSuffix.Ref.SourceHash = scanHex("new-suffix")
	newSuffix.Object = "new-suffix"
	newSuffix.Fields = map[string]string{"path": "new-suffix"}
	newSuffix.RevisionID = memory.ObservationRevisionID(newSuffix)
	spec.observations = []memory.ObservationRevision{spec.observations[0], oldSecond, newSuffix}
	spec.report.BoundaryRelation = source.BoundaryAppend
	spec.boundary.Frozen.Location.JSONL = &memory.JSONLSourceLocation{Line: 5, ByteOffset: 300}
	spec.boundary.Frozen.SourceHash = scanHex("append-successor-boundary")
	spec.record.FrozenBoundary = spec.boundary.Frozen
	spec.record.EndedAt = "2026-08-31T10:02:00Z"
	spec.record.Usage.EndedAt = spec.record.EndedAt
	spec.record.Usage.DurationMS = 120_000

	second, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatalf("run real append: %v", err)
	}
	if second.GenerationID == first.GenerationID {
		t.Fatal("append reused baseline generation")
	}
	_, manifest, err := harness.store.LoadPrepared()
	if err != nil || len(manifest.ObservationChunkDigests) != 2 {
		t.Fatalf("append successor chunks=%v err=%v", manifest.ObservationChunkDigests, err)
	}
	viewBody, err := harness.store.LoadObject(memorystore.ObjectSessionView, manifest.SessionViews[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	var view memory.SessionView
	if err := decodeExactJSON(viewBody, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.ActiveRevisionIDs) != 3 || len(view.ObservationChunkDigests) != 2 || view.ObservationChunkDigests[0] != oldChunk {
		t.Fatalf("append view did not retain full active set and ordered old chunk: %+v", view)
	}
	seen := map[string]string{}
	for _, digest := range manifest.ObservationChunkDigests {
		body, err := harness.store.LoadObject(memorystore.ObjectObservationChunk, digest)
		if err != nil {
			t.Fatal(err)
		}
		revisions, err := decodeObservationChunk(body)
		if err != nil {
			t.Fatal(err)
		}
		for _, revision := range revisions {
			if prior, duplicate := seen[revision.RevisionID]; duplicate {
				t.Fatalf("revision %s duplicated in chunks %s and %s", revision.RevisionID, prior, digest)
			}
			seen[revision.RevisionID] = digest
		}
	}
	if len(seen) != 3 || seen[newSuffix.RevisionID] == oldChunk {
		t.Fatalf("append persisted wrong revision set: %v", seen)
	}
}

func TestRunReplacementRebuildsActiveViewForInteriorMutation(t *testing.T) {
	harness := newScanHarness(t)
	spec := harness.addSource(1, memory.Indexed, scanTestProject)
	removed := spec.observations[0]
	removed.Key.Sequence = 3
	removed.Key.Subject = "removed-by-mutation"
	removed.Ref.Location.JSONL = &memory.JSONLSourceLocation{Line: 3, ByteOffset: 80}
	removed.Ref.SourceHash = scanHex("removed-by-mutation")
	removed.Object = "removed-by-mutation"
	removed.Fields = map[string]string{"path": "removed-by-mutation"}
	removed.RevisionID = memory.ObservationRevisionID(removed)
	spec.observations = append(spec.observations, removed)
	if _, err := Run(context.Background(), harness.options); err != nil {
		t.Fatal(err)
	}
	_, baseline, err := harness.store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	stableKey := observationKeyDigestForScan(t, spec.observations[0].Key)
	removedKey := observationKeyDigestForScan(t, removed.Key)
	oldActive := baseline.ActiveRevisions[stableKey]
	oldRemoved := baseline.ActiveRevisions[removedKey]

	replacement := spec.observations[0]
	replacement.Outcome = "corrected-after-interior-mutation"
	replacement.RevisionID = memory.ObservationRevisionID(replacement)
	spec.observations = []memory.ObservationRevision{replacement}
	spec.report.BoundaryRelation = source.BoundaryReplacement
	spec.boundary.Frozen.SourceHash = scanHex("same-coordinate-interior-mutation")
	spec.record.FrozenBoundary = spec.boundary.Frozen

	if _, err := Run(context.Background(), harness.options); err != nil {
		t.Fatalf("run interior replacement: %v", err)
	}
	_, manifest, err := harness.store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ActiveRevisions[stableKey] != replacement.RevisionID || manifest.SupersededRevisions[oldActive] != replacement.RevisionID || manifest.WithdrawnRevisions[removedKey] != oldRemoved {
		t.Fatalf("replacement lineage active=%v superseded=%v withdrawn=%v", manifest.ActiveRevisions, manifest.SupersededRevisions, manifest.WithdrawnRevisions)
	}
	viewBody, err := harness.store.LoadObject(memorystore.ObjectSessionView, manifest.SessionViews[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	var view memory.SessionView
	if err := decodeExactJSON(viewBody, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.ActiveRevisionIDs) != 1 || view.ActiveRevisionIDs[0] != replacement.RevisionID {
		t.Fatalf("replacement retained stale active facts: %+v", view.ActiveRevisionIDs)
	}
}

func TestRunFailsClosedOnEveryDecodeErrorWithoutCatalogMutation(t *testing.T) {
	harness := newScanHarness(t)
	failed := harness.addSource(1, memory.Indexed, scanTestProject)
	failed.decodeErr = errors.New("decode-canary")
	harness.addSource(2, memory.Indexed, scanTestProject)

	result, err := Run(context.Background(), harness.options)
	if err == nil || result.Prepared || result.State != Failed {
		t.Fatalf("decode failure result=%+v err=%v", result, err)
	}
	if records, listErr := harness.catalog.ListCandidates(); listErr != nil || len(records) != 0 {
		t.Fatalf("decode failure mutated catalog records=%+v err=%v", records, listErr)
	}
}

func TestRunAbandonsEveryLeaseAcrossCancellationAndAdapterFailures(t *testing.T) {
	tests := []struct {
		name    string
		workers int
		prepare func(*scanHarness, context.CancelFunc)
	}{
		{
			name: "discover-error-with-candidates", workers: 1,
			prepare: func(harness *scanHarness, _ context.CancelFunc) {
				harness.adapter.discoverErr = errors.New("discover-lease-canary")
			},
		},
		{
			name: "cancel-after-discover", workers: 1,
			prepare: func(harness *scanHarness, cancel context.CancelFunc) { harness.adapter.afterDiscover = cancel },
		},
		{
			name: "cancel-after-first-freeze", workers: 1,
			prepare: func(harness *scanHarness, cancel context.CancelFunc) {
				harness.adapter.afterFreeze = func(source.Boundary) { cancel() }
			},
		},
		{
			name: "freeze-error", workers: 1,
			prepare: func(harness *scanHarness, _ context.CancelFunc) {
				harness.adapter.sources["session-2"].freezeErr = errors.New("freeze-lease-canary")
			},
		},
		{
			name: "one-worker-decode-error", workers: 1,
			prepare: func(harness *scanHarness, _ context.CancelFunc) {
				harness.adapter.sources["session-1"].decodeErr = errors.New("decode-lease-canary")
			},
		},
		{
			name: "multi-worker-decode-error", workers: 4,
			prepare: func(harness *scanHarness, _ context.CancelFunc) {
				harness.adapter.sources["session-1"].decodeErr = errors.New("decode-lease-canary")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newScanHarness(t)
			for index := 1; index <= 8; index++ {
				harness.addSource(index, memory.Indexed, scanTestProject)
			}
			harness.options.Workers = test.workers
			for attempt := 0; attempt < 10; attempt++ {
				ctx, cancel := context.WithCancel(context.Background())
				test.prepare(&harness, cancel)
				_, _ = Run(ctx, harness.options)
				cancel()
				candidateLeases, boundaryLeases := harness.adapter.leaseCounts()
				if candidateLeases != 0 || boundaryLeases != 0 {
					t.Fatalf("attempt %d leaked leases: candidates=%d boundaries=%d", attempt, candidateLeases, boundaryLeases)
				}
			}
		})
	}
}

func TestRunLeaseCleanupIsPanicSafeAndSuccessfulCyclesStayBounded(t *testing.T) {
	t.Run("freeze-panic-unwinds-owned-candidates", func(t *testing.T) {
		harness := newScanHarness(t)
		harness.addSource(1, memory.Indexed, scanTestProject).freezePanic = true
		harness.addSource(2, memory.Indexed, scanTestProject)
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("freeze panic was not propagated")
				}
			}()
			_, _ = Run(context.Background(), harness.options)
		}()
		candidates, boundaries := harness.adapter.leaseCounts()
		if candidates != 0 || boundaries != 0 {
			t.Fatalf("freeze panic leaked leases: candidates=%d boundaries=%d", candidates, boundaries)
		}
	})

	t.Run("decode-panic-fails-closed", func(t *testing.T) {
		harness := newScanHarness(t)
		harness.addSource(1, memory.Indexed, scanTestProject).decodePanic = true
		harness.addSource(2, memory.Indexed, scanTestProject)
		if result, err := Run(context.Background(), harness.options); err == nil || result.Prepared {
			t.Fatalf("decode panic result=%+v err=%v", result, err)
		}
		candidates, boundaries := harness.adapter.leaseCounts()
		if candidates != 0 || boundaries != 0 {
			t.Fatalf("decode panic leaked leases: candidates=%d boundaries=%d", candidates, boundaries)
		}
	})

	t.Run("successful-repeated-runs", func(t *testing.T) {
		harness := newScanHarness(t)
		harness.addSource(1, memory.Indexed, scanTestProject)
		for attempt := 0; attempt < 100; attempt++ {
			if _, err := Run(context.Background(), harness.options); err != nil {
				t.Fatalf("attempt %d: %v", attempt, err)
			}
			candidates, boundaries := harness.adapter.leaseCounts()
			if candidates != 0 || boundaries != 0 {
				t.Fatalf("attempt %d retained leases: candidates=%d boundaries=%d", attempt, candidates, boundaries)
			}
		}
	})
}

func TestRunFailsClosedOnUnknownDecodeErrorWithoutAdvancingPrepared(t *testing.T) {
	harness := newScanHarness(t)
	spec := harness.addSource(1, memory.Indexed, scanTestProject)
	baseline, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatalf("prepare unknown-error baseline: %v", err)
	}
	spec.decodeErr = errors.New("adapter-contract-canary")
	spec.boundary.Frozen.SourceHash = scanHex("unknown-error-successor")
	spec.record.FrozenBoundary = spec.boundary.Frozen

	result, runErr := Run(context.Background(), harness.options)
	if runErr == nil || result.Prepared || result.State != Failed {
		t.Fatalf("unknown Decode error was downgraded: result=%+v err=%v", result, runErr)
	}
	prepared, manifest, loadErr := harness.store.LoadPrepared()
	if loadErr != nil || prepared.GenerationID != baseline.GenerationID || manifest.GenerationID != baseline.GenerationID {
		t.Fatalf("unknown Decode error changed prepared generation: prepared=%#v manifest=%#v err=%v", prepared, manifest, loadErr)
	}
}

func TestRunFailsClosedOnUnknownFreezeAndCatalogAssociationErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*fakeSourceSpec)
	}{
		{name: "unknown-freeze", mutate: func(spec *fakeSourceSpec) {
			spec.freezeErr = errors.New("freeze-integrity-canary")
		}},
		{name: "missing-catalog-record", mutate: func(spec *fakeSourceSpec) {
			spec.skipCatalog = true
		}},
		{name: "target-observation-without-target-association", mutate: func(spec *fakeSourceSpec) {
			spec.record.ProjectIDs = []string{scanTestForeign}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newScanHarness(t)
			bad := harness.addSource(1, memory.Indexed, scanTestProject)
			test.mutate(bad)
			harness.addSource(2, memory.Indexed, scanTestProject)
			result, err := Run(context.Background(), harness.options)
			if err == nil || result.Prepared || result.State != Failed {
				t.Fatalf("integrity error was downgraded: result=%+v err=%v", result, err)
			}
			if _, _, loadErr := harness.store.LoadPrepared(); !errors.Is(loadErr, memorystore.ErrNoPreparedGeneration) {
				t.Fatalf("integrity error prepared partial generation: %v", loadErr)
			}
		})
	}
}

func TestRunCancellationLeavesPriorPreparedGenerationReadable(t *testing.T) {
	harness := newScanHarness(t)
	spec := harness.addSource(1, memory.Indexed, scanTestProject)
	baseline, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatalf("prepare cancellation baseline: %v", err)
	}
	wait := make(chan struct{})
	spec.wait = wait
	harness.adapter.decodeStarted = make(chan string, 1)
	spec.boundary.Frozen.Location.JSONL = &memory.JSONLSourceLocation{Line: 3, ByteOffset: 200}
	spec.boundary.Frozen.SourceHash = scanHex("cancel-successor-boundary")
	spec.record.FrozenBoundary = spec.boundary.Frozen

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var result Result
	var runErr error
	go func() {
		defer close(done)
		result, runErr = Run(ctx, harness.options)
	}()
	select {
	case <-harness.adapter.decodeStarted:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled scan did not start decoding")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled scan leaked a worker")
	}
	if !errors.Is(runErr, context.Canceled) || result.State != Failed || result.Prepared || result.ReviewRunTokens != 0 {
		t.Fatalf("cancel result is dishonest: result=%+v err=%v", result, runErr)
	}
	prepared, manifest, err := harness.store.LoadPrepared()
	if err != nil || prepared.GenerationID != baseline.GenerationID || manifest.GenerationID != baseline.GenerationID {
		t.Fatalf("cancellation changed prior prepared generation: prepared=%#v manifest=%#v err=%v", prepared, manifest, err)
	}
}

func TestRunSerializesConcurrentProjectScansForFullLifecycle(t *testing.T) {
	harness := newScanHarness(t)
	spec := harness.addSource(1, memory.Indexed, scanTestProject)
	wait := make(chan struct{})
	spec.wait = wait
	harness.adapter.decodeStarted = make(chan string, 2)

	type outcome struct {
		result Result
		err    error
	}
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			result, err := Run(context.Background(), harness.options)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	select {
	case <-harness.adapter.decodeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first concurrent scan did not start")
	}
	select {
	case sessionID := <-harness.adapter.decodeStarted:
		t.Fatalf("second scan entered decode before lifecycle lock release: %s", sessionID)
	case <-time.After(150 * time.Millisecond):
	}
	close(wait)
	first := <-outcomes
	second := <-outcomes
	if first.err != nil || second.err != nil || !first.result.Prepared || !second.result.Prepared || first.result.GenerationID != second.result.GenerationID {
		t.Fatalf("concurrent scans did not converge: first=%+v/%v second=%+v/%v", first.result, first.err, second.result, second.err)
	}
}

func TestConcurrentProjectScansNeverPrepareOlderFrozenFactsAfterNewerAppendWins(t *testing.T) {
	older := newScanHarness(t)
	oldSpec := older.addSource(1, memory.Indexed, scanTestProject, scanTestForeign)
	if _, err := older.catalog.UpsertSource(oldSpec.record); err != nil {
		t.Fatal(err)
	}
	oldSpec.report.BoundaryRelation = source.BoundaryUnchanged
	blocked := make(chan struct{})
	oldSpec.wait = blocked
	older.adapter.decodeStarted = make(chan string, 1)

	projectRootB := t.TempDir()
	bindingB, err := projectidentity.Resolve(config.ProjectMapping{ID: scanTestForeign, Root: projectRootB}, projectRootB, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	catalogB, err := sourcecatalog.Open(older.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalogB.Close() })
	storeB, err := memorystore.Open(older.options.DataRoot, scanTestForeign)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeB.Close() })
	adapterB := &fakeAdapter{
		catalog: catalogB, sources: make(map[string]*fakeSourceSpec), decodeErrors: make(map[string]error),
		candidateLeases: make(map[string]string), boundaryLeases: make(map[string]string),
	}
	newer := *oldSpec
	newer.candidate.InitialCWD = bindingB.CanonicalRoot
	newer.boundary.Candidate = newer.candidate
	newer.boundary.Frozen.Location.JSONL = &memory.JSONLSourceLocation{Line: 3, ByteOffset: 220}
	newer.boundary.Frozen.SourceHash = scanHex("newer-shared-boundary")
	newer.record.FrozenBoundary = newer.boundary.Frozen
	newer.report.BoundaryRelation = source.BoundaryAppend
	newer.wait = nil
	newer.observations = append([]memory.ObservationRevision(nil), oldSpec.observations...)
	for index := range newer.observations {
		newer.observations[index].Key.ProjectID = scanTestForeign
		newer.observations[index].RevisionID = memory.ObservationRevisionID(newer.observations[index])
	}
	adapterB.sources[newer.record.SessionID] = &newer
	optionsB := older.options
	optionsB.ProjectID, optionsB.Binding, optionsB.Adapter, optionsB.Catalog, optionsB.Store = scanTestForeign, bindingB, adapterB, catalogB, storeB

	oldResult := make(chan error, 1)
	go func() { _, err := Run(context.Background(), older.options); oldResult <- err }()
	select {
	case <-older.adapter.decodeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for older decode")
	}
	newResult, err := Run(context.Background(), optionsB)
	if err != nil || !newResult.Prepared {
		t.Fatalf("newer scan result=%+v err=%v", newResult, err)
	}
	close(blocked)
	select {
	case err := <-oldResult:
		if !errors.Is(err, sourcecatalog.ErrCASConflict) {
			t.Fatalf("older frozen scan error=%v want catalog CAS conflict", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for older stale scan")
	}
	if _, _, err := older.store.LoadPrepared(); !errors.Is(err, memorystore.ErrNoPreparedGeneration) {
		t.Fatalf("older scan prepared stale observations: %v", err)
	}
	record, found, err := older.catalog.GetSource("codex", oldSpec.record.SessionID)
	if err != nil || !found || !reflect.DeepEqual(record.FrozenBoundary, newer.record.FrozenBoundary) {
		t.Fatalf("catalog lost newer boundary record=%+v found=%v err=%v", record, found, err)
	}
}

func TestRunFreezesOneReferenceTimeForProbeReductionAndManifest(t *testing.T) {
	harness := newScanHarness(t)
	harness.addSource(1, memory.Indexed, scanTestProject)
	calls := 0
	harness.options.Now = func() time.Time {
		calls++
		return time.Date(2026, 8, 31, 10, 2, calls, 0, time.UTC)
	}
	harness.options.Probe = func(ctx context.Context, options projectprobe.Options) (memory.ProjectProbeState, memory.ProbeCheck, error) {
		if err := ctx.Err(); err != nil {
			return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
		}
		state := memory.ProjectProbeState{
			SchemaVersion: memory.MemorySchemaVersion, ProjectID: options.Binding.ProjectID,
			CanonicalRoot: options.Binding.CanonicalRoot, RemoteIdentityHashes: []string{},
			VersionFiles: []memory.ProbeFile{}, RequiredProjectionFiles: []memory.ProbeFile{},
			ProbeVersion: projectprobe.ProbeVersion, Diagnostics: []memory.Diagnostic{},
		}
		var err error
		state.Digest, err = memory.ProjectProbeStateDigest(state)
		if err != nil {
			return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
		}
		checkedAt := options.Now().UTC().Format(time.RFC3339Nano)
		return state, memory.ProbeCheck{SchemaVersion: memory.MemorySchemaVersion, CheckedAt: checkedAt, StateDigest: state.Digest, Available: true, Diagnostics: []memory.Diagnostic{}}, nil
	}
	if _, err := Run(context.Background(), harness.options); err != nil {
		t.Fatalf("run time-frozen scan: %v", err)
	}
	if calls != 1 {
		t.Fatalf("scan sampled wall clock %d times, want one frozen reference", calls)
	}
}

func TestRunGlobalObservationBudgetFailsWithoutPreparedGeneration(t *testing.T) {
	harness := newScanHarness(t)
	for sourceIndex := 1; sourceIndex <= 2; sourceIndex++ {
		spec := harness.addSource(sourceIndex, memory.Indexed, scanTestProject)
		base := spec.observations[0]
		spec.observations = make([]memory.ObservationRevision, 32769)
		for index := range spec.observations {
			observation := base
			observation.Key.Sequence = sourceIndex*100000 + index
			observation.Key.Subject = fmt.Sprintf("budget-%d-%d", sourceIndex, index)
			observation.Ref.Location.JSONL = &memory.JSONLSourceLocation{Line: index + 2, ByteOffset: int64(index * 10)}
			observation.Ref.SourceHash = scanHex(observation.Key.Subject)
			observation.Object = observation.Key.Subject
			observation.Fields = map[string]string{"path": observation.Key.Subject}
			observation.RevisionID = memory.ObservationRevisionID(observation)
			spec.observations[index] = observation
		}
	}

	result, err := Run(context.Background(), harness.options)
	if err == nil || !errors.Is(err, ErrObservationBudget) || result.Prepared || result.State != Failed {
		t.Fatalf("global observation budget result=%+v err=%v", result, err)
	}
	if _, _, loadErr := harness.store.LoadPrepared(); !errors.Is(loadErr, memorystore.ErrNoPreparedGeneration) {
		t.Fatalf("observation budget prepared a partial generation: %v", loadErr)
	}
	if records, listErr := harness.catalog.ListCandidates(); listErr != nil || len(records) != 0 {
		t.Fatalf("observation budget mutated catalog records=%+v err=%v", records, listErr)
	}
}

func TestRunProbeFailureDoesNotApplyCatalogBatch(t *testing.T) {
	harness := newScanHarness(t)
	harness.addSource(1, memory.Indexed, scanTestProject)
	harness.options.Probe = func(context.Context, projectprobe.Options) (memory.ProjectProbeState, memory.ProbeCheck, error) {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, errors.New("probe-canary")
	}
	if _, err := Run(context.Background(), harness.options); err == nil {
		t.Fatal("probe failure was ignored")
	}
	if records, err := harness.catalog.ListCandidates(); err != nil || len(records) != 0 {
		t.Fatalf("probe failure mutated catalog records=%+v err=%v", records, err)
	}
}

func TestRunReduceFailureDoesNotApplyCatalogBatch(t *testing.T) {
	harness := newScanHarness(t)
	harness.addSource(1, memory.Indexed, scanTestProject)
	harness.options.Reduce = func(projectview.Input) (memory.ProjectView, bool, error) {
		return memory.ProjectView{}, false, errors.New("reduce-canary")
	}
	if _, err := Run(context.Background(), harness.options); err == nil {
		t.Fatal("reduce failure was ignored")
	}
	if records, err := harness.catalog.ListCandidates(); err != nil || len(records) != 0 {
		t.Fatalf("reduce failure mutated catalog records=%+v err=%v", records, err)
	}
}

func TestRunRepairsIdempotentlyAfterCatalogBatchPrecedesStoreFailure(t *testing.T) {
	harness := newScanHarness(t)
	harness.addSource(1, memory.Indexed, scanTestProject)
	failing := &failingMemoryStore{MemoryStore: harness.store, failObservation: true}
	harness.options.Store = failing
	result, err := Run(context.Background(), harness.options)
	if err == nil || result.Prepared {
		t.Fatalf("store failure result=%+v err=%v", result, err)
	}
	records, listErr := harness.catalog.ListCandidates(scanTestProject)
	if listErr != nil || len(records) != 1 {
		t.Fatalf("catalog did not retain complete desired batch records=%+v err=%v", records, listErr)
	}
	if _, _, loadErr := harness.store.LoadPrepared(); !errors.Is(loadErr, memorystore.ErrNoPreparedGeneration) {
		t.Fatalf("store failure prepared generation: %v", loadErr)
	}

	harness.options.Store = harness.store
	repaired, err := Run(context.Background(), harness.options)
	if err != nil || !repaired.Prepared || repaired.SourceSessions != 1 {
		t.Fatalf("idempotent repair result=%+v err=%v", repaired, err)
	}
}

func TestRunCancellationAfterReduceCannotPersistOrAdvanceSuccessor(t *testing.T) {
	harness := newScanHarness(t)
	spec := harness.addSource(1, memory.Indexed, scanTestProject)
	baseline, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatal(err)
	}
	spec.report.BoundaryRelation = source.BoundaryAppend
	appended := spec.observations[0]
	appended.Key.Sequence = 3
	appended.Key.Subject = "cancel-after-reduce"
	appended.Ref.Location.JSONL = &memory.JSONLSourceLocation{Line: 3, ByteOffset: 80}
	appended.Ref.SourceHash = scanHex("cancel-after-reduce")
	appended.Object = "cancel-after-reduce"
	appended.Fields = map[string]string{"path": "cancel-after-reduce"}
	appended.RevisionID = memory.ObservationRevisionID(appended)
	spec.observations = append(spec.observations, appended)
	spec.boundary.Frozen.Location.JSONL = &memory.JSONLSourceLocation{Line: 4, ByteOffset: 160}
	spec.boundary.Frozen.SourceHash = scanHex("cancel-after-reduce-boundary")
	spec.record.FrozenBoundary = spec.boundary.Frozen
	spec.record.EndedAt = "2026-08-31T10:02:00Z"
	spec.record.Usage.EndedAt = spec.record.EndedAt
	spec.record.Usage.DurationMS = 120_000

	ctx, cancel := context.WithCancel(context.Background())
	harness.options.Reduce = func(input projectview.Input) (memory.ProjectView, bool, error) {
		view, changed, err := projectview.Reduce(input)
		cancel()
		return view, changed, err
	}
	result, runErr := Run(ctx, harness.options)
	if !errors.Is(runErr, context.Canceled) || result.Prepared || result.State != Failed {
		t.Fatalf("late cancellation result=%+v err=%v", result, runErr)
	}
	prepared, manifest, loadErr := harness.store.LoadPrepared()
	if loadErr != nil || prepared.GenerationID != baseline.GenerationID || manifest.GenerationID != baseline.GenerationID {
		t.Fatalf("late cancellation advanced prepared: prepared=%#v manifest=%#v err=%v", prepared, manifest, loadErr)
	}
}

func observationKeyDigestForScan(t *testing.T, key memory.ObservationKey) string {
	t.Helper()
	digest, err := memory.Digest(key)
	if err != nil {
		t.Fatalf("digest observation key: %v", err)
	}
	return digest
}

func scanHex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func containsBytes(body, needle []byte) bool {
	return len(needle) > 0 && stringIndex(string(body), string(needle)) >= 0
}

func stringIndex(value, needle string) int {
	for index := 0; index+len(needle) <= len(value); index++ {
		if value[index:index+len(needle)] == needle {
			return index
		}
	}
	return -1
}

func minScanInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

var _ source.Adapter = (*fakeAdapter)(nil)
