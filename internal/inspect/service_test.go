package inspect

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

type eventFixture struct {
	dataRoot, projectID, generationID string
	sessionDigests                    map[string]string
}

func TestLoadSessionEventPagePaginatesAndNavigatesDeterministically(t *testing.T) {
	fixture := buildEventFixture(t, "project-events-a", "generation-events-a", "session-1", "session-2")
	request := EventPageRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID, Limit: 2}

	first, err := LoadSessionEventPage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 3 || first.RangeStart != 0 || first.RangeEnd != 2 || len(first.Items) != 2 || first.Items[0].Sequence != 1 || first.Items[1].Sequence != 2 || first.PreviousCursor != nil || first.NextCursor == nil || first.FirstCursor == nil || first.LastCursor == nil {
		t.Fatalf("first page=%+v", first)
	}
	if first.Items[0].Kind != "message" || first.Items[1].Kind != "command" {
		t.Fatalf("truthful public kinds=%+v", first.Items)
	}
	if strings.Contains(first.Items[0].Excerpt, "sk-abcdefghijklmnopqrstuvwxyz1234567890") || !strings.Contains(first.Items[0].Excerpt, "[REDACTED:OPENAI_KEY]") || strings.Contains(first.Items[0].Excerpt, "/Users/private/repo") || !strings.Contains(first.Items[0].Excerpt, "[REDACTED:ABSOLUTE_PATH]") || len(first.Items[0].Excerpt) > 512 {
		t.Fatalf("unsafe or unbounded excerpt bytes=%d value=%q", len(first.Items[0].Excerpt), first.Items[0].Excerpt)
	}

	nextRequest := request
	nextRequest.Cursor = *first.NextCursor
	next, err := LoadSessionEventPage(context.Background(), nextRequest)
	if err != nil {
		t.Fatal(err)
	}
	if next.Total != 3 || next.RangeStart != 2 || next.RangeEnd != 3 || len(next.Items) != 1 || next.Items[0].Sequence != 3 || next.NextCursor != nil || next.PreviousCursor == nil {
		t.Fatalf("next page=%+v", next)
	}

	lastRequest := request
	lastRequest.Cursor = *first.LastCursor
	last, err := LoadSessionEventPage(context.Background(), lastRequest)
	if err != nil || !reflect.DeepEqual(last, next) {
		t.Fatalf("last=%+v next=%+v err=%v", last, next, err)
	}
	firstRequest := request
	firstRequest.Cursor = *next.FirstCursor
	fromFirst, err := LoadSessionEventPage(context.Background(), firstRequest)
	if err != nil || !reflect.DeepEqual(fromFirst, first) {
		t.Fatalf("from first=%+v first=%+v err=%v", fromFirst, first, err)
	}
	anchorRequest := request
	anchorRequest.Anchor = 3
	anchored, err := LoadSessionEventPage(context.Background(), anchorRequest)
	if err != nil || !reflect.DeepEqual(anchored, next) {
		t.Fatalf("anchored=%+v next=%+v err=%v", anchored, next, err)
	}
	if next.Coverage != (Coverage{Seen: 5, Indexed: 3, Undecodable: 2}) {
		t.Fatalf("coverage=%+v", next.Coverage)
	}
}

func TestLoadSessionEventPageRejectsGenerationAnchorAndCursorMismatch(t *testing.T) {
	fixture := buildEventFixture(t, "project-events-a", "generation-events-a", "session-1", "session-2")
	base := EventPageRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID, Limit: 1}
	page, err := LoadSessionEventPage(context.Background(), base)
	if err != nil || page.NextCursor == nil {
		t.Fatalf("page=%+v err=%v", page, err)
	}

	tests := []struct {
		name string
		edit func(*EventPageRequest)
		code string
	}{
		{"changed generation", func(r *EventPageRequest) { r.ExpectedGenerationID = "generation-changed" }, "generation_mismatch"},
		{"anchor zero", func(r *EventPageRequest) { r.Anchor = -1 }, "anchor_out_of_range"},
		{"anchor beyond total", func(r *EventPageRequest) { r.Anchor = 4 }, "anchor_out_of_range"},
		{"changed limit cursor", func(r *EventPageRequest) { r.Cursor = *page.NextCursor; r.Limit = 2 }, "stale_cursor"},
		{"wrong session cursor", func(r *EventPageRequest) { r.Cursor = *page.NextCursor; r.SessionID = "session-2" }, "stale_cursor"},
		{"tampered cursor", func(r *EventPageRequest) { r.Cursor = *page.NextCursor + "x" }, "stale_cursor"},
		{"forged public checksum cursor", func(r *EventPageRequest) { r.Cursor = forgePublicChecksumCursor(t, *page.NextCursor, 2) }, "stale_cursor"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.edit(&request)
			_, err := LoadSessionEventPage(context.Background(), request)
			if eventErrorCode(err) != test.code {
				t.Fatalf("code=%q err=%v", eventErrorCode(err), err)
			}
		})
	}

	other := buildEventFixtureAt(t, fixture.dataRoot, "project-events-b", "generation-events-b", "session-1")
	crossProject := EventPageRequest{DataRoot: fixture.dataRoot, ProjectID: other.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: other.generationID, Cursor: *page.NextCursor, Limit: 1}
	if _, err := LoadSessionEventPage(context.Background(), crossProject); eventErrorCode(err) != "stale_cursor" {
		t.Fatalf("cross-project cursor code=%q err=%v", eventErrorCode(err), err)
	}
}

func TestLoadSessionEventPageRejectsUnknownUnavailableAndCorruptStateWithoutLeakingPaths(t *testing.T) {
	fixture := buildEventFixture(t, "project-events-a", "generation-events-a", "session-1")
	base := EventPageRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID, Limit: 2}

	unknown := base
	unknown.SessionID = "session-missing"
	if _, err := LoadSessionEventPage(context.Background(), unknown); eventErrorCode(err) != "invalid_argument" || strings.Contains(err.Error(), fixture.dataRoot) {
		t.Fatalf("unknown session err=%v", err)
	}
	absent := base
	absent.ProjectID = "project-absent"
	if _, err := LoadSessionEventPage(context.Background(), absent); eventErrorCode(err) != "invalid_argument" || strings.Contains(err.Error(), fixture.dataRoot) {
		t.Fatalf("absent state err=%v", err)
	}

	digest := strings.TrimPrefix(fixture.sessionDigests["session-1"], "sha256:")
	objectPath := filepath.Join(fixture.dataRoot, "projects", fixture.projectID, "memory-v1", "sessions", digest+".json")
	if err := os.WriteFile(objectPath, []byte("corrupt-private-canary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSessionEventPage(context.Background(), base); eventErrorCode(err) != "invalid_argument" || strings.Contains(err.Error(), "corrupt-private-canary") || strings.Contains(err.Error(), fixture.dataRoot) {
		t.Fatalf("corrupt state err=%v", err)
	}
}

func TestLoadSessionEventPageLeavesPrivateTreeAndPermissionsUnchanged(t *testing.T) {
	fixture := buildEventFixture(t, "project-events-a", "generation-events-a", "session-1")
	before := snapshotEventTree(t, fixture.dataRoot)
	request := EventPageRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID, Limit: 2}
	if _, err := LoadSessionEventPage(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	after := snapshotEventTree(t, fixture.dataRoot)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("read-only inspection changed private tree\nbefore=%v\nafter=%v", before, after)
	}
}

func TestLoadSessionEventPageAuthenticatesLegacyExactRootWithoutPersistingAlias(t *testing.T) {
	fixture := buildEventFixture(t, "project-events-legacy", "generation-events-legacy", "session-1")
	configPath := filepath.Join(fixture.dataRoot, "config.toml")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Projects[0].AuthenticatedAliases = nil
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	before := snapshotEventTree(t, fixture.dataRoot)
	request := EventPageRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID, Limit: 2}
	page, err := LoadSessionEventPage(context.Background(), request)
	if err != nil || page.Total != 3 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if after := snapshotEventTree(t, fixture.dataRoot); !reflect.DeepEqual(before, after) {
		t.Fatal("legacy exact-root authentication persisted an alias or changed private state")
	}
}

func eventErrorCode(err error) string {
	var contractErr *Error
	if errors.As(err, &contractErr) {
		return contractErr.Code
	}
	return ""
}

func buildEventFixture(t *testing.T, projectID, generationID string, sessionIDs ...string) eventFixture {
	t.Helper()
	return buildEventFixtureAt(t, t.TempDir(), projectID, generationID, sessionIDs...)
}

func buildEventFixtureAt(t *testing.T, dataRoot, projectID, generationID string, sessionIDs ...string) eventFixture {
	t.Helper()
	projectRoot := t.TempDir()
	legacy := config.ProjectMapping{ID: projectID, Root: projectRoot}
	binding, err := projectidentity.Resolve(legacy, projectRoot, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	mapping := legacy
	mapping.AuthenticatedAliases = []config.AuthenticatedProjectAlias{binding.AuthenticatedAlias}
	cfg, err := config.Load(filepath.Join(dataRoot, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Projects = append(cfg.Projects, mapping)
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	probe := memory.ProjectProbeState{SchemaVersion: 1, ProjectID: projectID, CanonicalRoot: projectRoot, Branch: "main", Head: strings.Repeat("a", 40), RemoteIdentityHashes: []string{}, VersionFiles: []memory.ProbeFile{}, RequiredProjectionFiles: []memory.ProbeFile{}, ProbeVersion: "v1", Diagnostics: []memory.Diagnostic{}}
	probe.Digest, err = memory.ProjectProbeStateDigest(probe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProbeState(probe); err != nil {
		t.Fatal(err)
	}

	views := make(map[sessionindex.SessionKey]*memory.SessionView, len(sessionIDs))
	dependencies := make([]memory.SessionViewDependency, 0, len(sessionIDs))
	lineages := make([]memory.SessionLineageDependency, 0, len(sessionIDs))
	sourceDigests := make([]string, 0, len(sessionIDs))
	measurements := make([]memory.SessionIndexMeasurement, 0, len(sessionIDs))
	sessionDigests := make(map[string]string, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		observations := fixtureObservations(t, projectID, sessionID)
		chunkDigest, err := store.PutObservationChunk(observations)
		if err != nil {
			t.Fatal(err)
		}
		active := make([]string, len(observations))
		summaries := make([]memory.ObservationSummary, len(observations))
		lineageActive := make(map[string]string, len(observations))
		for index, observation := range observations {
			active[index] = observation.RevisionID
			summaries[index] = memory.ObservationSummary{RevisionID: observation.RevisionID, Sequence: observation.Key.Sequence, Kind: observation.Key.Kind, Subject: observation.Key.Subject, OccurredAt: observation.Timestamp, Operation: observation.Operation, Object: observation.Object, Outcome: observation.Outcome, Fields: observation.Fields, Excerpt: observation.Excerpt}
			keyDigest, err := memory.Digest(observation.Key)
			if err != nil {
				t.Fatal(err)
			}
			lineageActive[keyDigest] = observation.RevisionID
		}
		sourceDigest := testDigest(projectID + "-" + sessionID + "-source")
		view := memory.SessionView{SchemaVersion: 1, ProjectID: projectID, Provider: "codex", SessionID: sessionID, SourceIdentity: "source-" + sessionID, SourceRecordDigest: sourceDigest, UsageRecordDigest: sourceDigest, StartedAt: "2026-09-07T00:00:00Z", EndedAt: "2026-09-07T00:00:03Z", TerminalState: memory.Indexed, SourceAvailability: memory.SourceAvailable, ActiveRevisionIDs: active, ObservationSummaries: summaries, ObservationChunkDigests: []string{chunkDigest}, DependencyDigest: testDigest(projectID + "-" + sessionID + "-dependency"), MaterializerVersion: "v1"}
		view.Digest, err = memory.SessionViewDigest(view)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.PutSessionView(view); err != nil {
			t.Fatal(err)
		}
		lineage := memory.SessionLineage{SchemaVersion: 1, ProjectID: projectID, Provider: "codex", SessionID: sessionID, SourceIdentity: view.SourceIdentity, ActiveRevisions: lineageActive, SupersededRevisions: map[string]string{}, WithdrawnRevisions: map[string]string{}}
		lineage.Digest, err = memory.SessionLineageDigest(lineage)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.PutSessionLineage(lineage); err != nil {
			t.Fatal(err)
		}
		key := sessionindex.SessionKey{Provider: "codex", SessionID: sessionID}
		viewCopy := view
		views[key] = &viewCopy
		dependencies = append(dependencies, memory.SessionViewDependency{Provider: "codex", SessionID: sessionID, Digest: view.Digest})
		lineages = append(lineages, memory.SessionLineageDependency{Provider: "codex", SessionID: sessionID, Digest: lineage.Digest})
		sourceDigests = append(sourceDigests, sourceDigest)
		measurements = append(measurements, memory.SessionIndexMeasurement{Provider: "codex", SessionID: sessionID, RecordCount: eventUint64(5), Seen: 5, Indexed: 3, Undecodable: 2})
		sessionDigests[sessionID] = view.Digest
	}
	project := memory.ProjectView{SchemaVersion: 1, ProjectID: projectID, Generation: 1, StartedAt: "2026-09-07T00:00:00Z", EndedAt: "2026-09-07T00:00:03Z", SourceSessions: len(sessionIDs), TerminalCounts: memory.TerminalCounts{Indexed: len(sessionIDs)}, SessionViewDependencies: dependencies, ObservationRevisionIDs: []string{}, ProbeStateDigest: probe.Digest, LiveState: memory.StateSnapshot{Branch: "main", Head: probe.Head}, WitnessedState: []memory.DerivedRecord{}, DerivedRecords: []memory.DerivedRecord{}, AggregationCoverage: memory.ProjectAggregationCoverage{ObservationSummariesSeen: len(sessionIDs) * 3, EventReferences: memory.AggregationChannelCoverage{Seen: len(sessionIDs) * 3, Dropped: len(sessionIDs) * 3, Truncated: true}, SelectedEvidenceRevisions: memory.AggregationChannelCoverage{}}, AssociatedUsage: []memory.AssociatedUsage{}, DependencyDigest: testDigest(projectID + "-project-dependency"), ReducerVersion: "v1"}
	project.Digest, err = memory.ProjectViewDigest(project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProjectView(project); err != nil {
		t.Fatal(err)
	}
	manifest := memory.GenerationManifest{SchemaVersion: 1, GenerationID: generationID, ProjectID: projectID, CreatedAt: "2026-09-07T00:00:04Z", SourceRecordDigests: sourceDigests, SessionViews: dependencies, SessionLineages: lineages, ProbeStateDigest: probe.Digest, ProbeCheck: memory.ProbeCheck{SchemaVersion: 1, CheckedAt: "2026-09-07T00:00:04Z", StateDigest: probe.Digest, Available: true, Diagnostics: []memory.Diagnostic{}}, ProjectViewDigest: project.Digest, SessionIndexMeasurements: measurements}
	index, err := sessionindex.Build(sessionindex.BuildInput{ProjectView: project, Manifest: manifest, SessionViews: views, GeneratedAt: time.Date(2026, 9, 7, 0, 0, 4, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	manifest.SessionIndexDigest, err = store.PutSessionIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareGeneration(manifest)
	if err != nil {
		t.Fatal(err)
	}
	proof := memory.PublicationProof{Version: 4, ProjectID: projectID, GenerationID: generationID, ManifestDigest: prepared.ManifestDigest, ProjectViewDigest: prepared.ProjectViewDigest, ReviewSHA256: strings.Repeat("1", 64), HistorySHA256: strings.Repeat("2", 64), LedgerSHA256: strings.Repeat("3", 64), SessionIndexSHA256: strings.TrimPrefix(manifest.SessionIndexDigest, "sha256:"), JournalVerified: true}
	if err := store.CommitPublished(generationID, proof); err != nil {
		t.Fatal(err)
	}
	return eventFixture{dataRoot: dataRoot, projectID: projectID, generationID: generationID, sessionDigests: sessionDigests}
}

func fixtureObservations(t *testing.T, projectID, sessionID string) []memory.ObservationRevision {
	t.Helper()
	inputs := []struct{ kind, subject, operation, outcome, excerpt string }{
		{"request", "request-1", "user_request", "", "open /Users/private/repo/secret.txt token sk-abcdefghijklmnopqrstuvwxyz1234567890 " + strings.Repeat("界", 200)},
		{"command", "command-1", "command_finished", "success", ""},
		{"verification", "verification-1", "verification", "passed", "focused tests passed"},
	}
	result := make([]memory.ObservationRevision, len(inputs))
	for index, input := range inputs {
		sequence := index + 1
		value := memory.ObservationRevision{SchemaVersion: 1, Key: memory.ObservationKey{Provider: "codex", SessionID: sessionID, SourceIdentity: "source-" + sessionID, Sequence: sequence, ProjectID: projectID, Kind: input.kind, Subject: input.subject}, Ref: memory.SourceRef{Provider: "codex", SessionID: sessionID, SourceIdentity: "source-" + sessionID, Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: sequence, ByteOffset: int64(sequence * 100)}}, SourceHash: strings.Repeat(string(rune('a'+index)), 64)}, Timestamp: "2026-09-07T00:00:0" + string(rune('1'+index)) + "Z", Operation: input.operation, Outcome: input.outcome, Excerpt: input.excerpt, AdapterID: "codex-jsonl", AdapterVersion: "v1"}
		value.RevisionID = memory.ObservationRevisionID(value)
		if err := memory.ValidateObservationRevision(value); err != nil {
			t.Fatal(err)
		}
		result[index] = value
	}
	return result
}

func testDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func eventUint64(value uint64) *uint64 { return &value }

type eventTreeEntry struct {
	Mode fs.FileMode
	Size int64
	Hash [sha256.Size]byte
}

func snapshotEventTree(t *testing.T, root string) map[string]eventTreeEntry {
	t.Helper()
	result := map[string]eventTreeEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		item := eventTreeEntry{Mode: info.Mode(), Size: info.Size()}
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			item.Hash = sha256.Sum256(body)
		}
		result[relative] = item
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func forgePublicChecksumCursor(t *testing.T, token string, offset uint64) string {
	t.Helper()
	body, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	var cursor eventCursor
	if err := json.Unmarshal(body, &cursor); err != nil {
		t.Fatal(err)
	}
	cursor.Offset, cursor.Checksum = offset, ""
	body, err = json.Marshal(cursor)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	cursor.Checksum = hex.EncodeToString(sum[:])
	body, err = json.Marshal(cursor)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(body)
}
