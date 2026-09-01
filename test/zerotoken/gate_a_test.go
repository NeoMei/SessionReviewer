package zerotoken

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/projectprobe"
	"github.com/neomei/SessionReviewer/internal/projectview"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/scan"
	"github.com/neomei/SessionReviewer/internal/sessionview"
	"github.com/neomei/SessionReviewer/internal/source"
	"github.com/neomei/SessionReviewer/internal/source/codex"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

const (
	gateProjectID      = "gate-a-project"
	gateForeignProject = "gate-a-foreign"
	gateUnsupportedID  = "session-unsupported"
	gateMalformedID    = "session-malformed"
	gateSharedID       = "session-shared"
	gateForeignID      = "session-foreign"
	gateFirstSeenID    = "first-seen-unassociated"
	gateToolCanary     = "COMPLETE-TOOL-OUTPUT-MUST-NOT-PERSIST"
	gateTranscriptTail = "COMPLETE-USER-TRANSCRIPT-MUST-NOT-PERSIST"
)

var (
	gateSessionStart = time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	gateNow          = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
)

type gateManifest struct {
	SchemaVersion   int `json:"schema_version"`
	LogicalSessions int `json:"logical_sessions"`
	Terminal        struct {
		Indexed     int `json:"indexed"`
		Unsupported int `json:"unsupported"`
		Unreadable  int `json:"unreadable"`
		Ambiguous   int `json:"ambiguous"`
	} `json:"terminal"`
	NonCountedCases []string `json:"non_counted_cases"`
	ReplayCases     []string `json:"replay_cases"`
}

type gateAdapter struct {
	source.Adapter
}

func (adapter gateAdapter) Discover(ctx context.Context) (source.Discovery, error) {
	discovery, err := adapter.Adapter.Discover(ctx)
	if err != nil {
		return source.Discovery{}, err
	}
	discovery.Issues = append(discovery.Issues, source.Issue{
		Code: "unreadable_segment", Provider: "codex", SessionID: gateFirstSeenID,
		Path: "/private/non-counted-canary", TerminalState: memory.Unreadable,
	})
	return discovery, nil
}

func (adapter gateAdapter) Decode(ctx context.Context, boundary source.Boundary, visit func(memory.ObservationRevision) error) (source.DecodeReport, error) {
	report, err := adapter.Adapter.Decode(ctx, boundary, visit)
	if err != nil {
		return report, err
	}
	switch boundary.Candidate.SessionID {
	case gateUnsupportedID:
		report.TerminalState = memory.Unsupported
	case gateMalformedID:
		report.TerminalState = memory.Unreadable
	case gateSharedID:
		report.TerminalState = memory.Ambiguous
	}
	return report, nil
}

type gateHarness struct {
	repositoryRoot string
	dataRoot       string
	sessionsRoot   string
	projectRoot    string
	vaultRoot      string
	foreignRoot    string
	binding        projectidentity.Binding
	foreignBinding projectidentity.Binding
	catalog        *sourcecatalog.Catalog
	store          *memorystore.Store
	options        scan.Options
	baseBodies     map[string][]byte
	probeCalls     atomic.Int32
}

func TestGateAZeroTokenCore(t *testing.T) {
	repositoryRoot := gateRepositoryRoot(t)
	fixture := readGateManifest(t, filepath.Join(repositoryRoot, "testdata", "zero-token", "manifest.json"))
	if fixture.SchemaVersion != 1 || fixture.LogicalSessions != 154 || fixture.Terminal.Indexed != 151 || fixture.Terminal.Unsupported != 1 || fixture.Terminal.Unreadable != 1 || fixture.Terminal.Ambiguous != 1 {
		t.Fatalf("invalid Gate A fixture contract: %+v", fixture)
	}
	harness := newGateHarness(t, repositoryRoot)
	projectBefore := snapshotGateTree(t, harness.projectRoot)
	vaultBefore := snapshotGateTree(t, harness.vaultRoot)
	assertNoForbiddenGateDependencies(t, repositoryRoot)

	initial := harness.run(t)
	if initial.SourceSessions != fixture.LogicalSessions || initial.TerminalSessions != fixture.LogicalSessions || initial.IndexedSessions != fixture.Terminal.Indexed || initial.IssueSessions != 3 || initial.ReviewRunTokens != 0 || !initial.Prepared || initial.State != scan.CompletedWithIssues {
		t.Fatalf("initial Gate A reconciliation failed: %+v", initial)
	}
	_, initialManifest := harness.prepared(t)
	initialProject := loadGateProjectView(t, harness.store, initialManifest.ProjectViewDigest)
	if initialProject.TerminalCounts != (memory.TerminalCounts{Indexed: 151, Unsupported: 1, Unreadable: 1, Ambiguous: 1}) {
		t.Fatalf("terminal matrix=%+v", initialProject.TerminalCounts)
	}
	assertGateSharedUsage(t, initialProject)
	assertGateExcludedCases(t, initialManifest)
	assertGatePrivateStore(t, harness.dataRoot)

	beforeAppend := snapshotGateImmutable(t, harness.dataRoot)
	appendGateRecord(t, filepath.Join(harness.sessionsRoot, "session-001.jsonl"), harness.projectRoot)
	appended := harness.run(t)
	if appended.ReviewRunTokens != 0 || appended.GenerationID == initial.GenerationID {
		t.Fatalf("append did not create a zero-token successor: before=%+v after=%+v", initial, appended)
	}
	_, appendManifest := harness.prepared(t)
	assertSuccessorOnlyAppend(t, beforeAppend, snapshotGateImmutable(t, harness.dataRoot), initialManifest, appendManifest)

	beforeUnchanged := snapshotGateImmutable(t, harness.dataRoot)
	unchanged := harness.run(t)
	if unchanged.GenerationID != appended.GenerationID || unchanged.ProjectViewDigest != appended.ProjectViewDigest {
		t.Fatalf("unchanged replay churned generation: before=%+v after=%+v", appended, unchanged)
	}
	if after := snapshotGateImmutable(t, harness.dataRoot); !equalGateSnapshots(beforeUnchanged, after) {
		t.Fatal("unchanged replay wrote immutable objects")
	}

	oldActive := cloneStringMap(appendManifest.ActiveRevisions)
	harness.options.Adapter = harness.codexAdapter(t, "v2", "v1")
	superseded := harness.run(t)
	if superseded.ReviewRunTokens != 0 || superseded.GenerationID == unchanged.GenerationID {
		t.Fatalf("adapter successor did not advance: %+v", superseded)
	}
	_, supersededManifest := harness.prepared(t)
	assertAdapterSupersession(t, oldActive, supersededManifest)

	appendPath := filepath.Join(harness.sessionsRoot, "session-001.jsonl")
	if err := os.WriteFile(appendPath, harness.baseBodies["session-001"], 0o600); err != nil {
		t.Fatal(err)
	}
	withdrawn := harness.run(t)
	if withdrawn.GenerationID == superseded.GenerationID || withdrawn.ReviewRunTokens != 0 {
		t.Fatalf("source replacement/truncation did not advance: %+v", withdrawn)
	}
	_, withdrawnManifest := harness.prepared(t)
	assertWithdrawal(t, supersededManifest, withdrawnManifest)

	missingPath := filepath.Join(harness.sessionsRoot, "session-002.jsonl")
	if err := os.Remove(missingPath); err != nil {
		t.Fatal(err)
	}
	missing := harness.run(t)
	if missing.SourceSessions != 154 || missing.TerminalSessions != 154 || missing.IndexedSessions != 150 || missing.ReviewRunTokens != 0 {
		t.Fatalf("missing replay did not preserve complete coverage: %+v", missing)
	}
	_, missingManifest := harness.prepared(t)
	missingView := loadGateSessionView(t, harness.store, missingManifest, "session-002")
	if missingView.TerminalState != memory.Missing || missingView.SourceAvailability != memory.SourceUnavailable || len(missingView.ActiveRevisionIDs) == 0 {
		t.Fatalf("missing replay lost retained facts or availability: %+v", missingView)
	}
	missingAgain := harness.run(t)
	if missingAgain.GenerationID != missing.GenerationID || missingAgain.ProjectViewDigest != missing.ProjectViewDigest {
		t.Fatalf("unchanged unavailable replay churned: before=%+v after=%+v", missing, missingAgain)
	}

	assertPlatformEquivalentGatePaths(t)
	assertGatePrivateStore(t, harness.dataRoot)
	if harness.probeCalls.Load() != 7 {
		t.Fatalf("unexpected read-only probe count %d", harness.probeCalls.Load())
	}
	if _, err := os.Lstat(filepath.Join(harness.projectRoot, "PROJECT-SCRIPT-MUST-NOT-RUN")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a captured project command executed: %v", err)
	}
	if projectAfter := snapshotGateTree(t, harness.projectRoot); !equalGateSnapshots(projectBefore, projectAfter) {
		t.Fatal("Gate A changed Project fixture bytes or metadata")
	}
	if vaultAfter := snapshotGateTree(t, harness.vaultRoot); !equalGateSnapshots(vaultBefore, vaultAfter) {
		t.Fatal("Gate A changed Vault fixture bytes or metadata")
	}

	t.Log("Gate A: 154/154 terminal, 151 indexed, zero model tokens")
}

func newGateHarness(t *testing.T, repositoryRoot string) *gateHarness {
	t.Helper()
	root := t.TempDir()
	harness := &gateHarness{
		repositoryRoot: repositoryRoot,
		dataRoot:       filepath.Join(root, "data"),
		sessionsRoot:   filepath.Join(root, "sessions"),
		projectRoot:    filepath.Join(root, "Project"),
		vaultRoot:      filepath.Join(root, "Vault"),
		foreignRoot:    filepath.Join(root, "Foreign"),
		baseBodies:     make(map[string][]byte),
	}
	for _, directory := range []string{harness.dataRoot, harness.sessionsRoot, harness.projectRoot, harness.vaultRoot, harness.foreignRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeGateFixtureFile(t, filepath.Join(harness.projectRoot, "project-fixture.md"), []byte("# project fixture\n"))
	writeGateFixtureFile(t, filepath.Join(harness.vaultRoot, "vault-fixture.md"), []byte("# vault fixture\n"))
	harness.binding = resolveGateBinding(t, gateProjectID, harness.projectRoot)
	harness.foreignBinding = resolveGateBinding(t, gateForeignProject, harness.foreignRoot)
	harness.projectRoot = harness.binding.CanonicalRoot
	harness.foreignRoot = harness.foreignBinding.CanonicalRoot
	harness.installSessions(t)

	catalog, err := sourcecatalog.Open(harness.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	harness.catalog = catalog
	t.Cleanup(func() { _ = catalog.Close() })
	store, err := memorystore.Open(harness.dataRoot, gateProjectID)
	if err != nil {
		t.Fatal(err)
	}
	harness.store = store
	t.Cleanup(func() { _ = store.Close() })
	harness.options = scan.Options{
		ProjectID: gateProjectID, Binding: harness.binding, SessionsRoot: harness.sessionsRoot,
		DataRoot: harness.dataRoot, Adapter: harness.codexAdapter(t, "v1"), Catalog: harness.catalog, Store: harness.store,
		Workers: 8, Now: func() time.Time { return gateNow }, Materialize: sessionview.Materialize,
		Probe: harness.readOnlyProbe, Reduce: projectview.Reduce,
	}
	return harness
}

func (harness *gateHarness) codexAdapter(t *testing.T, version string, supersedes ...string) source.Adapter {
	t.Helper()
	redactor := redact.Default()
	adapter, err := codex.New(codex.AdapterOptions{
		SessionsRoot: harness.sessionsRoot, Bindings: []projectidentity.Binding{harness.binding, harness.foreignBinding},
		Catalog: harness.catalog, Redactor: &redactor, AdapterVersion: version,
		SupersedesAdapterVersions: append([]string(nil), supersedes...),
	})
	if err != nil {
		t.Fatal(err)
	}
	return gateAdapter{Adapter: adapter}
}

func (harness *gateHarness) readOnlyProbe(ctx context.Context, options projectprobe.Options) (memory.ProjectProbeState, memory.ProbeCheck, error) {
	if err := ctx.Err(); err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
	}
	harness.probeCalls.Add(1)
	state := memory.ProjectProbeState{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: options.Binding.ProjectID,
		CanonicalRoot: options.Binding.CanonicalRoot, Branch: "main", Head: strings.Repeat("a", 40),
		RemoteIdentityHashes: []string{}, VersionFiles: []memory.ProbeFile{}, RequiredProjectionFiles: []memory.ProbeFile{},
		ProbeVersion: projectprobe.ProbeVersion, Diagnostics: []memory.Diagnostic{},
	}
	var err error
	state.Digest, err = memory.ProjectProbeStateDigest(state)
	if err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
	}
	check := memory.ProbeCheck{SchemaVersion: memory.MemorySchemaVersion, CheckedAt: gateNow.Format(time.RFC3339Nano), StateDigest: state.Digest, Available: true, Diagnostics: []memory.Diagnostic{}}
	return state, check, nil
}

func (harness *gateHarness) run(t *testing.T) scan.Result {
	t.Helper()
	result, err := scan.Run(context.Background(), harness.options)
	if err != nil {
		t.Fatalf("run Gate A composition: %v", err)
	}
	return result
}

func (harness *gateHarness) prepared(t *testing.T) (memorystore.Prepared, memory.GenerationManifest) {
	t.Helper()
	prepared, manifest, err := harness.store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	return prepared, manifest
}

func (harness *gateHarness) installSessions(t *testing.T) {
	t.Helper()
	for index := 1; index <= 151; index++ {
		id := fmt.Sprintf("session-%03d", index)
		body := gateSessionBody(t, id, harness.projectRoot, harness.projectRoot, false)
		harness.baseBodies[id] = body
		writeGateFixtureFile(t, filepath.Join(harness.sessionsRoot, id+".jsonl"), body)
	}
	unsupported := gateSessionBody(t, gateUnsupportedID, harness.projectRoot, harness.projectRoot, false)
	harness.baseBodies[gateUnsupportedID] = unsupported
	writeGateFixtureFile(t, filepath.Join(harness.sessionsRoot, gateUnsupportedID+".jsonl"), unsupported)
	malformed := gateSessionBody(t, gateMalformedID, harness.projectRoot, harness.projectRoot, true)
	harness.baseBodies[gateMalformedID] = malformed
	writeGateFixtureFile(t, filepath.Join(harness.sessionsRoot, gateMalformedID+".jsonl"), malformed)
	shared := gateSharedSessionBody(t, harness.projectRoot, harness.foreignRoot)
	harness.baseBodies[gateSharedID] = shared
	writeGateFixtureFile(t, filepath.Join(harness.sessionsRoot, gateSharedID+".jsonl"), shared)
	foreign := gateSessionBody(t, gateForeignID, harness.foreignRoot, harness.foreignRoot, false)
	writeGateFixtureFile(t, filepath.Join(harness.sessionsRoot, gateForeignID+".jsonl"), foreign)
}

func gateSessionBody(t *testing.T, sessionID, metadataRoot, workRoot string, malformed bool) []byte {
	t.Helper()
	base := gateSessionStart.Add(time.Duration(len(sessionID)) * time.Minute)
	input, _ := json.Marshal(map[string]string{"cmd": "go test ./internal/example && touch PROJECT-SCRIPT-MUST-NOT-RUN", "workdir": workRoot})
	lines := []any{
		map[string]any{"timestamp": base.Format(time.RFC3339), "type": "session_meta", "payload": map[string]any{"id": sessionID, "cwd": metadataRoot, "source": "gate-a"}},
		map[string]any{"timestamp": base.Add(time.Second).Format(time.RFC3339), "type": "turn_context", "payload": map[string]any{"cwd": workRoot, "model": "fixture-model"}},
		map[string]any{"timestamp": base.Add(2 * time.Second).Format(time.RFC3339), "type": "response_item", "payload": map[string]any{"type": "message", "id": "user-" + sessionID, "role": "user", "content": []any{map[string]any{"type": "input_text", "text": strings.Repeat("bounded-safe-request ", 80) + gateTranscriptTail}}}},
		map[string]any{"timestamp": base.Add(3 * time.Second).Format(time.RFC3339), "type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "id": "call-" + sessionID, "call_id": "call-" + sessionID, "name": "exec_command", "input": string(input)}},
		map[string]any{"timestamp": base.Add(4 * time.Second).Format(time.RFC3339), "type": "response_item", "payload": map[string]any{"type": "custom_tool_call_output", "id": "out-" + sessionID, "call_id": "call-" + sessionID, "output": "exit code: 0\nPASS\n" + gateToolCanary}},
		gateTokenLine(base.Add(5*time.Second), 3),
	}
	return encodeGateLines(t, lines, malformed)
}

func gateSharedSessionBody(t *testing.T, projectRoot, foreignRoot string) []byte {
	t.Helper()
	base := gateSessionStart.Add(4 * time.Hour)
	projectInput, _ := json.Marshal(map[string]string{"cmd": "go test ./...", "workdir": projectRoot})
	foreignInput, _ := json.Marshal(map[string]string{"patch": "*** Begin Patch\n*** Add File: foreign.md\n+safe\n*** End Patch", "workdir": foreignRoot})
	lines := []any{
		map[string]any{"timestamp": base.Format(time.RFC3339), "type": "session_meta", "payload": map[string]any{"id": gateSharedID, "cwd": projectRoot, "source": "gate-a"}},
		map[string]any{"timestamp": base.Add(time.Second).Format(time.RFC3339), "type": "turn_context", "payload": map[string]any{"cwd": foreignRoot, "model": "fixture-model"}},
		map[string]any{"timestamp": base.Add(2 * time.Second).Format(time.RFC3339), "type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "id": "shared-a", "call_id": "shared-a", "name": "exec_command", "input": string(projectInput)}},
		map[string]any{"timestamp": base.Add(3 * time.Second).Format(time.RFC3339), "type": "response_item", "payload": map[string]any{"type": "custom_tool_call_output", "id": "shared-a-out", "call_id": "shared-a", "output": "exit code: 0\nPASS\n" + gateToolCanary}},
		map[string]any{"timestamp": base.Add(4 * time.Second).Format(time.RFC3339), "type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "id": "shared-b", "call_id": "shared-b", "name": "apply_patch", "input": string(foreignInput)}},
		map[string]any{"timestamp": base.Add(5 * time.Second).Format(time.RFC3339), "type": "response_item", "payload": map[string]any{"type": "custom_tool_call_output", "id": "shared-b-out", "call_id": "shared-b", "output": "Done!"}},
		gateTokenLine(base.Add(6*time.Second), 2),
	}
	return encodeGateLines(t, lines, false)
}

func gateTokenLine(timestamp time.Time, total int) any {
	usage := map[string]any{"input_tokens": total - 1, "cached_input_tokens": 0, "cache_write_input_tokens": 0, "output_tokens": 1, "reasoning_output_tokens": 0, "total_tokens": total}
	return map[string]any{"timestamp": timestamp.Format(time.RFC3339), "type": "event_msg", "payload": map[string]any{"type": "token_count", "info": map[string]any{"last_token_usage": usage, "total_token_usage": usage}}}
}

func encodeGateLines(t *testing.T, lines []any, malformed bool) []byte {
	t.Helper()
	var body bytes.Buffer
	for index, line := range lines {
		encoded, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		body.Write(encoded)
		body.WriteByte('\n')
		if malformed && index == 1 {
			body.WriteString("{malformed-gate-a-record\n")
		}
	}
	return body.Bytes()
}

func appendGateRecord(t *testing.T, path, workRoot string) {
	t.Helper()
	input, _ := json.Marshal(map[string]string{"cmd": "go test ./internal/append", "workdir": workRoot})
	lines := []any{
		map[string]any{"timestamp": gateSessionStart.Add(6 * time.Hour).Format(time.RFC3339), "type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "id": "append-call", "call_id": "append-call", "name": "exec_command", "input": string(input)}},
		map[string]any{"timestamp": gateSessionStart.Add(6*time.Hour + time.Second).Format(time.RFC3339), "type": "response_item", "payload": map[string]any{"type": "custom_tool_call_output", "id": "append-out", "call_id": "append-call", "output": "exit code: 0\nPASS\n" + gateToolCanary}},
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(encodeGateLines(t, lines, false))
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		t.Fatal(err)
	}
}

func readGateManifest(t *testing.T, path string) gateManifest {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Gate A manifest: %v", err)
	}
	var manifest gateManifest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatalf("decode Gate A manifest: %v", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatal("Gate A manifest contains trailing data")
	}
	return manifest
}

func loadGateProjectView(t *testing.T, store *memorystore.Store, digest string) memory.ProjectView {
	t.Helper()
	body, err := store.LoadObject(memorystore.ObjectProjectView, digest)
	if err != nil {
		t.Fatal(err)
	}
	var view memory.ProjectView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func loadGateSessionView(t *testing.T, store *memorystore.Store, manifest memory.GenerationManifest, sessionID string) memory.SessionView {
	t.Helper()
	for _, dependency := range manifest.SessionViews {
		if dependency.SessionID != sessionID {
			continue
		}
		body, err := store.LoadObject(memorystore.ObjectSessionView, dependency.Digest)
		if err != nil {
			t.Fatal(err)
		}
		var view memory.SessionView
		if err := json.Unmarshal(body, &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	t.Fatalf("SessionView %q is missing", sessionID)
	return memory.SessionView{}
}

func assertGateSharedUsage(t *testing.T, view memory.ProjectView) {
	t.Helper()
	for _, usage := range view.AssociatedUsage {
		if usage.SessionID == gateSharedID {
			if !usage.Shared {
				t.Fatal("shared Session usage was not marked shared")
			}
			return
		}
	}
	t.Fatal("shared Session usage is missing")
}

func assertGateExcludedCases(t *testing.T, manifest memory.GenerationManifest) {
	t.Helper()
	for _, dependency := range manifest.SessionViews {
		if dependency.SessionID == gateFirstSeenID || dependency.SessionID == gateForeignID {
			t.Fatalf("non-counted source entered target generation: %q", dependency.SessionID)
		}
	}
}

func assertSuccessorOnlyAppend(t *testing.T, before, after map[string]gateSnapshotEntry, first, second memory.GenerationManifest) {
	t.Helper()
	for _, old := range first.ObservationChunkDigests {
		if !containsGateString(second.ObservationChunkDigests, old) {
			t.Fatalf("append dropped old chunk %q", old)
		}
	}
	if len(second.ObservationChunkDigests) != len(first.ObservationChunkDigests)+1 {
		t.Fatalf("append chunks before=%d after=%d", len(first.ObservationChunkDigests), len(second.ObservationChunkDigests))
	}
	added := gateAddedByCollection(before, after)
	want := map[string]int{"generations": 1, "observations": 1, "project-views": 1, "sessions": 1}
	if !equalGateIntMaps(added, want) {
		t.Fatalf("append wrote outside exact successors: got=%v want=%v", added, want)
	}
}

func assertAdapterSupersession(t *testing.T, oldActive map[string]string, successor memory.GenerationManifest) {
	t.Helper()
	for key, oldRevision := range oldActive {
		newRevision, active := successor.ActiveRevisions[key]
		if !active || newRevision == oldRevision || successor.SupersededRevisions[oldRevision] != newRevision {
			t.Fatalf("adapter lineage missing for stable key %q", key)
		}
	}
}

func assertWithdrawal(t *testing.T, before, after memory.GenerationManifest) {
	t.Helper()
	found := false
	for key, revision := range before.ActiveRevisions {
		if _, remains := after.ActiveRevisions[key]; remains {
			continue
		}
		if after.WithdrawnRevisions[key] != revision {
			t.Fatalf("removed revision lacks withdrawal lineage for key %q", key)
		}
		found = true
	}
	if !found {
		t.Fatal("replacement/truncation did not withdraw an active revision")
	}
}

func assertGatePrivateStore(t *testing.T, dataRoot string) {
	t.Helper()
	err := filepath.WalkDir(dataRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, forbidden := range []string{gateToolCanary, gateTranscriptTail, "/private/non-counted-canary"} {
			if bytes.Contains(body, []byte(forbidden)) {
				return fmt.Errorf("private store copied forbidden source body")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertPlatformEquivalentGatePaths(t *testing.T) {
	t.Helper()
	windows, err := platform.PathKey("windows", platform.CaseSensitive, `Modules\Core.md`)
	if err != nil {
		t.Fatal(err)
	}
	darwin, err := platform.PathKey("darwin", platform.CaseInsensitive, "modules/core.md")
	if err != nil {
		t.Fatal(err)
	}
	if windows != darwin || windows != "modules/core.md" {
		t.Fatalf("platform path identities differ: windows=%q darwin=%q", windows, darwin)
	}
}

func assertNoForbiddenGateDependencies(t *testing.T, repositoryRoot string) {
	t.Helper()
	queue := []string{"internal/scan", "internal/source/codex", "internal/memorystore", "internal/sourcecatalog", "internal/sessionview", "internal/projectview"}
	seen := make(map[string]bool)
	for len(queue) > 0 {
		relative := queue[0]
		queue = queue[1:]
		if seen[relative] {
			continue
		}
		seen[relative] = true
		directory := filepath.Join(repositoryRoot, filepath.FromSlash(relative))
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(directory, entry.Name()), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, importSpec := range file.Imports {
				path, err := strconv.Unquote(importSpec.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if path == "net/http" || path == "net/rpc" || strings.Contains(path, "/internal/agent") {
					t.Fatalf("forbidden Gate A dependency %q", path)
				}
				const module = "github.com/neomei/SessionReviewer/"
				if strings.HasPrefix(path, module+"internal/") {
					queue = append(queue, strings.TrimPrefix(path, module))
				}
			}
		}
	}
}

type gateSnapshotEntry struct {
	mode     fs.FileMode
	size     int64
	modified time.Time
	digest   string
}

func snapshotGateTree(t *testing.T, root string) map[string]gateSnapshotEntry {
	t.Helper()
	result := make(map[string]gateSnapshotEntry)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		digest := ""
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(body)
			digest = hex.EncodeToString(sum[:])
		}
		result[filepath.ToSlash(relative)] = gateSnapshotEntry{mode: info.Mode(), size: info.Size(), modified: info.ModTime(), digest: digest}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func snapshotGateImmutable(t *testing.T, dataRoot string) map[string]gateSnapshotEntry {
	t.Helper()
	memoryRoot := filepath.Join(dataRoot, "projects", gateProjectID, "memory-v1")
	result := make(map[string]gateSnapshotEntry)
	for _, collection := range []string{"generations", "observations", "sessions", "project-probes", "project-views"} {
		for relative, entry := range snapshotGateTree(t, filepath.Join(memoryRoot, collection)) {
			if relative == "." {
				continue
			}
			result[collection+"/"+relative] = entry
		}
	}
	return result
}

func gateAddedByCollection(before, after map[string]gateSnapshotEntry) map[string]int {
	result := make(map[string]int)
	for path := range after {
		if _, exists := before[path]; exists {
			continue
		}
		result[strings.SplitN(path, "/", 2)[0]]++
	}
	return result
}

func equalGateSnapshots(first, second map[string]gateSnapshotEntry) bool {
	if len(first) != len(second) {
		return false
	}
	for path, value := range first {
		other, exists := second[path]
		if !exists || value.mode != other.mode || value.size != other.size || !value.modified.Equal(other.modified) || value.digest != other.digest {
			return false
		}
	}
	return true
}

func gateRepositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(root)
}

func resolveGateBinding(t *testing.T, projectID, root string) projectidentity.Binding {
	t.Helper()
	binding, err := projectidentity.Resolve(config.ProjectMapping{ID: projectID, Root: root}, root, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func writeGateFixtureFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func containsGateString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func cloneStringMap(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func equalGateIntMaps(first, second map[string]int) bool {
	if len(first) != len(second) {
		return false
	}
	for key, value := range first {
		if second[key] != value {
			return false
		}
	}
	return true
}
