package scan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
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
	candidate    source.Candidate
	boundary     source.Boundary
	record       memory.SourceRecord
	observations []memory.ObservationRevision
	report       source.DecodeReport
	freezeErr    error
	decodeErr    error
	wait         <-chan struct{}
}

type fakeAdapter struct {
	catalog *sourcecatalog.Catalog
	sources map[string]*fakeSourceSpec
	issues  []source.Issue

	mu            sync.Mutex
	active        int
	maxActive     int
	decoded       []string
	decodeErrors  map[string]error
	decodeStarted chan string
}

func (adapter *fakeAdapter) Discover(ctx context.Context) (source.Discovery, error) {
	if err := ctx.Err(); err != nil {
		return source.Discovery{}, err
	}
	candidates := make([]source.Candidate, 0, len(adapter.sources))
	for _, spec := range adapter.sources {
		candidates = append(candidates, spec.candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].SessionID < candidates[j].SessionID
	})
	return source.Discovery{Candidates: candidates, Issues: append([]source.Issue(nil), adapter.issues...)}, nil
}

func (adapter *fakeAdapter) Freeze(ctx context.Context, candidate source.Candidate) (source.Boundary, error) {
	if err := ctx.Err(); err != nil {
		return source.Boundary{}, err
	}
	spec := adapter.sources[candidate.SessionID]
	if spec == nil || spec.candidate != candidate {
		return source.Boundary{}, errors.New("unknown fake candidate")
	}
	return spec.boundary, spec.freezeErr
}

func (adapter *fakeAdapter) Decode(ctx context.Context, boundary source.Boundary, visit func(memory.ObservationRevision) error) (returned source.DecodeReport, returnedErr error) {
	spec := adapter.sources[boundary.Candidate.SessionID]
	if spec == nil {
		return source.DecodeReport{}, errors.New("unknown fake boundary")
	}
	adapter.mu.Lock()
	adapter.active++
	if adapter.active > adapter.maxActive {
		adapter.maxActive = adapter.active
	}
	adapter.mu.Unlock()
	defer func() {
		adapter.mu.Lock()
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
	digest, err := adapter.catalog.UpsertSource(spec.record)
	if err != nil {
		return source.DecodeReport{}, err
	}
	report := spec.report
	report.CatalogRecordDigest = digest
	report.ProjectIDs = append([]string(nil), spec.record.ProjectIDs...)
	report.EmittedRevisions = 0
	for _, observation := range spec.observations {
		if err := visit(observation); err != nil {
			return report, err
		}
		report.EmittedRevisions++
	}
	return report, nil
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
	adapter := &fakeAdapter{catalog: catalog, sources: make(map[string]*fakeSourceSpec), decodeErrors: make(map[string]error)}
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
	spec := &fakeSourceSpec{candidate: candidate, boundary: boundary, record: record, observations: []memory.ObservationRevision{observation}, report: source.DecodeReport{TerminalState: state}}
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
		if view.TerminalState != memory.Missing || view.SourceAvailability != memory.SourceUnavailable || len(view.ActiveRevisionIDs) != 1 {
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

func TestRunIsolatesOneDecodeFailureAndContinuesLaterSources(t *testing.T) {
	harness := newScanHarness(t)
	failed := harness.addSource(1, memory.Indexed, scanTestProject)
	failed.decodeErr = errors.New("source-local-decode-canary")
	if _, err := harness.catalog.UpsertSource(failed.record); err != nil {
		t.Fatalf("pre-associate failing source: %v", err)
	}
	harness.addSource(2, memory.Indexed, scanTestProject)

	result, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatalf("source-local failure stopped project scan: %v", err)
	}
	if result.SourceSessions != 2 || result.TerminalSessions != 2 || result.IndexedSessions != 1 || result.IssueSessions != 1 || result.State != CompletedWithIssues {
		t.Fatalf("failure isolation counts mismatch: %+v", result)
	}
	harness.adapter.mu.Lock()
	decoded := append([]string(nil), harness.adapter.decoded...)
	harness.adapter.mu.Unlock()
	sort.Strings(decoded)
	if len(decoded) != 2 || decoded[0] != "session-1" || decoded[1] != "session-2" {
		t.Fatalf("later source did not run after failure: %v", decoded)
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
