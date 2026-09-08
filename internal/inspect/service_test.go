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

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

type eventFixture struct {
	dataRoot, projectRoot, projectID, generationID string
	sessionDigests                                 map[string]string
}

type eventFixtureIdentity struct {
	provider, sessionID string
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

func TestLoadSessionEventPageReadsRetainedEventsWhenSourceUnavailableWithoutWrites(t *testing.T) {
	fixture := buildEventFixtureCustomizedAt(t, t.TempDir(), "project-events-retained", "generation-events-retained", []string{"session-1"}, nil, func(_ string, view *memory.SessionView) {
		view.TerminalState = memory.Missing
		view.SourceAvailability = memory.SourceUnavailable
	})
	if entries, err := os.ReadDir(fixture.projectRoot); err != nil || len(entries) != 0 {
		t.Fatalf("raw source fixture root entries=%v err=%v", entries, err)
	}
	before := snapshotEventTree(t, fixture.dataRoot)
	request := EventPageRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID, Limit: 2}

	first, err := LoadSessionEventPage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 3 || first.RangeStart != 0 || first.RangeEnd != 2 || len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatalf("first retained page=%+v", first)
	}
	if first.Items[0].RevisionID == "" || first.Items[1].RevisionID == "" || first.Items[0].RevisionID == first.Items[1].RevisionID {
		t.Fatalf("retained revision IDs=%q,%q", first.Items[0].RevisionID, first.Items[1].RevisionID)
	}
	if !strings.Contains(first.Items[0].Excerpt, "[REDACTED:OPENAI_KEY]") || first.Items[1].Excerpt != "" {
		t.Fatalf("retained excerpts=%q,%q", first.Items[0].Excerpt, first.Items[1].Excerpt)
	}
	wantCoverage := (Coverage{Seen: 5, Indexed: 3, Undecodable: 2})
	if first.Coverage != wantCoverage {
		t.Fatalf("first retained coverage=%+v want=%+v", first.Coverage, wantCoverage)
	}

	nextRequest := request
	nextRequest.Cursor = *first.NextCursor
	next, err := LoadSessionEventPage(context.Background(), nextRequest)
	if err != nil || next.RangeStart != 2 || next.RangeEnd != 3 || len(next.Items) != 1 || next.Items[0].RevisionID == "" || next.Items[0].Excerpt != "focused tests passed" || next.Coverage != wantCoverage {
		t.Fatalf("next retained page=%+v err=%v", next, err)
	}

	wrongGeneration := request
	wrongGeneration.ExpectedGenerationID = "generation-events-wrong"
	if _, err := LoadSessionEventPage(context.Background(), wrongGeneration); eventErrorCode(err) != CodeGenerationMismatch {
		t.Fatalf("wrong generation code=%q err=%v", eventErrorCode(err), err)
	}
	if after := snapshotEventTree(t, fixture.dataRoot); !reflect.DeepEqual(before, after) {
		t.Fatalf("retained event inspection changed private tree\nbefore=%v\nafter=%v", before, after)
	}
	if entries, err := os.ReadDir(fixture.projectRoot); err != nil || len(entries) != 0 {
		t.Fatalf("retained event inspection created raw source files entries=%v err=%v", entries, err)
	}
}

func TestLoadSessionEventPageAnchorUsesOneBasedOrdinalWithSparseSequences(t *testing.T) {
	fixture := buildEventFixtureCustomizedAt(t, t.TempDir(), "project-events-sparse", "generation-events-sparse", []string{"session-1"}, func(observations []memory.ObservationRevision) {
		for index := range observations {
			observations[index].Key.Sequence = (index + 1) * 10
			observations[index].RevisionID = memory.ObservationRevisionID(observations[index])
		}
	}, nil)
	request := EventPageRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID, Anchor: 3, Limit: 2}
	page, err := LoadSessionEventPage(context.Background(), request)
	if err != nil || page.RangeStart != 2 || page.RangeEnd != 3 || len(page.Items) != 1 || page.Items[0].Sequence != 30 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestSafeEventExcerptRedactsDelimitedAbsolutePaths(t *testing.T) {
	tests := []struct {
		name, input, secret string
	}{
		{"file URL", "open file:///Users/alice/private.txt now", "/Users/alice/private.txt"},
		{"labelled Unix", "path:/Users/alice/private.txt", "/Users/alice/private.txt"},
		{"labelled UNC", `share:\\server\private\file.txt`, `\\server\private\file.txt`},
		{"labelled drive", `path:C:\Users\alice\private.txt`, `C:\Users\alice\private.txt`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := safeEventExcerpt(test.input)
			if strings.Contains(got, test.secret) || !strings.Contains(got, "[REDACTED:ABSOLUTE_PATH]") || len(got) > eventExcerptBytes {
				t.Fatalf("unsafe excerpt=%q", got)
			}
		})
	}
}

func TestSafeEventExcerptRedactsTagDelimitedPathsAndPreservesClosingMarkup(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"root tag", "<root>/Users/private/repo</root>", "<root>[REDACTED:ABSOLUTE_PATH]</root>"},
		{"cwd tag", "prefix <cwd>/opt/private/project</cwd> suffix", "prefix <cwd>[REDACTED:ABSOLUTE_PATH]</cwd> suffix"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := safeEventExcerpt(test.input); got != test.want {
				t.Fatalf("excerpt=%q want=%q", got, test.want)
			}
		})
	}
}

func TestLoadSessionEventPageRejectsDivergentSummaryFromImmutableRevision(t *testing.T) {
	fixture := buildEventFixtureCustomizedAt(t, t.TempDir(), "project-events-divergent", "generation-events-divergent", []string{"session-1"}, nil, func(_ string, view *memory.SessionView) {
		view.ObservationSummaries[0].Excerpt = "summary diverged from immutable revision"
	})
	request := EventPageRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID, Limit: 2}
	if _, err := LoadSessionEventPage(context.Background(), request); eventErrorCode(err) != CodeInvalidArgument {
		t.Fatalf("code=%q err=%v", eventErrorCode(err), err)
	}
}

func TestLoadSessionEventPageRejectsConcurrentPublishedGenerationAdvance(t *testing.T) {
	fixture := buildEventFixture(t, "project-events-race", "generation-events-race", "session-1")
	writer, err := memorystore.Open(fixture.dataRoot, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	prepared, manifest, err := writer.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	manifest.GenerationID = "generation-concurrent"
	manifest.CreatedAt = "2026-09-07T00:00:05Z"
	projectBody, err := writer.LoadObject(memorystore.ObjectProjectView, manifest.ProjectViewDigest)
	if err != nil {
		t.Fatal(err)
	}
	var project memory.ProjectView
	if err := json.Unmarshal(projectBody, &project); err != nil {
		t.Fatal(err)
	}
	views := make(map[sessionindex.SessionKey]*memory.SessionView, len(manifest.SessionViews))
	for _, dependency := range manifest.SessionViews {
		viewBody, err := writer.LoadObject(memorystore.ObjectSessionView, dependency.Digest)
		if err != nil {
			t.Fatal(err)
		}
		var view memory.SessionView
		if err := json.Unmarshal(viewBody, &view); err != nil {
			t.Fatal(err)
		}
		views[sessionindex.SessionKey{Provider: dependency.Provider, SessionID: dependency.SessionID}] = &view
	}
	index, err := sessionindex.Build(sessionindex.BuildInput{ProjectView: project, Manifest: manifest, SessionViews: views, GeneratedAt: time.Date(2026, 9, 7, 0, 0, 5, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	manifest.SessionIndexDigest, err = writer.PutSessionIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	successor, err := writer.AdvancePrepared(prepared, manifest)
	if err != nil {
		t.Fatal(err)
	}
	proof := memory.PublicationProof{Version: 4, ProjectID: fixture.projectID, GenerationID: manifest.GenerationID, ManifestDigest: successor.ManifestDigest, ProjectViewDigest: successor.ProjectViewDigest, ReviewSHA256: strings.Repeat("1", 64), HistorySHA256: strings.Repeat("2", 64), LedgerSHA256: strings.Repeat("3", 64), SessionIndexSHA256: strings.TrimPrefix(manifest.SessionIndexDigest, "sha256:"), JournalVerified: true}
	before := snapshotEventTree(t, fixture.dataRoot)
	inspectCheckpoint = func(phase string) {
		if phase == "before_published_recheck" {
			if err := writer.CommitPublished(manifest.GenerationID, proof); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() { inspectCheckpoint = nil })
	request := EventPageRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID, Limit: 2}
	if _, err := LoadSessionEventPage(context.Background(), request); eventErrorCode(err) != CodeGenerationMismatch {
		t.Fatalf("code=%q err=%v", eventErrorCode(err), err)
	}
	inspectCheckpoint = nil
	after := snapshotEventTree(t, fixture.dataRoot)
	delete(before, filepath.Join("projects", fixture.projectID, "memory-v1", "published_generation"))
	delete(after, filepath.Join("projects", fixture.projectID, "memory-v1", "published_generation"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("inspection changed data other than injected concurrent publication pointer")
	}
}

func TestLoadSessionEventPageClassifiesFailedFinalPublishedReadAsInvalidArgument(t *testing.T) {
	fixture := buildEventFixture(t, "project-events-recheck-error", "generation-events-recheck-error", "session-1")
	inspectCheckpoint = func(phase string) {
		if phase == "before_published_recheck" {
			manifest := filepath.Join(fixture.dataRoot, "projects", fixture.projectID, "memory-v1", "generations", fixture.generationID+".json")
			if err := os.WriteFile(manifest, []byte("corrupt-generation\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() { inspectCheckpoint = nil })
	request := EventPageRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID, Limit: 2}
	if _, err := LoadSessionEventPage(context.Background(), request); eventErrorCode(err) != CodeInvalidArgument {
		t.Fatalf("code=%q err=%v", eventErrorCode(err), err)
	}
}

func TestLoadSessionEventPageObservesCancellationDuringItemWork(t *testing.T) {
	for _, phase := range []string{"event_item", "event_sort"} {
		t.Run(phase, func(t *testing.T) {
			fixture := buildEventFixture(t, "project-events-cancel-"+phase, "generation-events-cancel-"+phase, "session-1")
			ctx, cancel := context.WithCancelCause(context.Background())
			cause := errors.New("cancel item work")
			inspectCheckpoint = func(current string) {
				if current == phase {
					cancel(cause)
				}
			}
			t.Cleanup(func() { inspectCheckpoint = nil })
			request := EventPageRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID, Limit: 2}
			if _, err := LoadSessionEventPage(ctx, request); eventErrorCode(err) != CodeInvalidArgument || !strings.Contains(err.Error(), "timed out") {
				t.Fatalf("code=%q err=%v", eventErrorCode(err), err)
			}
			inspectCheckpoint = nil
		})
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
	return buildEventFixtureCustomizedAt(t, dataRoot, projectID, generationID, sessionIDs, nil, nil)
}

func buildEventFixtureCustomizedAt(t *testing.T, dataRoot, projectID, generationID string, sessionIDs []string, mutateObservations func([]memory.ObservationRevision), mutateView func(string, *memory.SessionView)) eventFixture {
	identities := make([]eventFixtureIdentity, len(sessionIDs))
	for index, sessionID := range sessionIDs {
		identities[index] = eventFixtureIdentity{provider: "codex", sessionID: sessionID}
	}
	return buildEventFixtureIdentitiesAt(t, dataRoot, projectID, generationID, identities, mutateObservations, mutateView, nil)
}

func buildEventFixtureIdentitiesAt(t *testing.T, dataRoot, projectID, generationID string, identities []eventFixtureIdentity, mutateObservations func([]memory.ObservationRevision), mutateView func(string, *memory.SessionView), buildConversation func(memory.SessionView, []memory.ObservationRevision) *conversationchain.Document) eventFixture {
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

	views := make(map[sessionindex.SessionKey]*memory.SessionView, len(identities))
	dependencies := make([]memory.SessionViewDependency, 0, len(identities))
	lineages := make([]memory.SessionLineageDependency, 0, len(identities))
	chains := make([]memory.ConversationChainDependency, 0, len(identities))
	sourceDigests := make([]string, 0, len(identities))
	measurements := make([]memory.SessionIndexMeasurement, 0, len(identities))
	sessionDigests := make(map[string]string, len(identities))
	for _, identity := range identities {
		provider, sessionID := identity.provider, identity.sessionID
		observations := fixtureObservationsForProvider(t, projectID, provider, sessionID)
		if mutateObservations != nil {
			mutateObservations(observations)
		}
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
		sourceIdentity := fixtureSourceIdentity(provider, sessionID)
		sourceDigest := testDigest(projectID + "-" + provider + "-" + sessionID + "-source")
		view := memory.SessionView{SchemaVersion: 1, ProjectID: projectID, Provider: provider, SessionID: sessionID, SourceIdentity: sourceIdentity, SourceRecordDigest: sourceDigest, UsageRecordDigest: sourceDigest, StartedAt: "2026-09-07T00:00:00Z", EndedAt: "2026-09-07T00:00:03Z", TerminalState: memory.Indexed, SourceAvailability: memory.SourceAvailable, ActiveRevisionIDs: active, ObservationSummaries: summaries, ObservationChunkDigests: []string{chunkDigest}, DependencyDigest: testDigest(projectID + "-" + provider + "-" + sessionID + "-dependency"), MaterializerVersion: "v1"}
		if mutateView != nil {
			mutateView(sessionID, &view)
		}
		if buildConversation != nil {
			record := memory.SourceRecord{SchemaVersion: 1, Provider: provider, SessionID: sessionID, SourceIdentity: sourceIdentity, StartedAt: view.StartedAt, EndedAt: view.EndedAt, FrozenBoundary: memory.FrozenBoundary{Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: 5, ByteOffset: 500}}, SourceHash: strings.Repeat("d", 64)}, Availability: memory.SourceAvailable, Usage: accounting.SessionUsage{StartedAt: view.StartedAt, EndedAt: view.EndedAt, DurationMS: 3000, Models: []accounting.ModelUsage{}}, ProjectIDs: []string{projectID}}
			catalog, err := sourcecatalog.Open(dataRoot)
			if err != nil {
				t.Fatal(err)
			}
			sourceDigest, err = catalog.UpsertSource(record)
			closeErr := catalog.Close()
			if err != nil || closeErr != nil {
				t.Fatalf("store provider-neutral source: digest=%q err=%v close=%v", sourceDigest, err, closeErr)
			}
			view.SourceRecordDigest = sourceDigest
			view.UsageRecordDigest = sourceDigest
			view.SourceAvailability = memory.SourceAvailable
		}
		view.Digest, err = memory.SessionViewDigest(view)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.PutSessionView(view); err != nil {
			t.Fatal(err)
		}
		lineage := memory.SessionLineage{SchemaVersion: 1, ProjectID: projectID, Provider: provider, SessionID: sessionID, SourceIdentity: view.SourceIdentity, ActiveRevisions: lineageActive, SupersededRevisions: map[string]string{}, WithdrawnRevisions: map[string]string{}}
		lineage.Digest, err = memory.SessionLineageDigest(lineage)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.PutSessionLineage(lineage); err != nil {
			t.Fatal(err)
		}
		key := sessionindex.SessionKey{Provider: provider, SessionID: sessionID}
		viewCopy := view
		views[key] = &viewCopy
		dependencies = append(dependencies, memory.SessionViewDependency{Provider: provider, SessionID: sessionID, Digest: view.Digest})
		lineages = append(lineages, memory.SessionLineageDependency{Provider: provider, SessionID: sessionID, Digest: lineage.Digest})
		sourceDigests = append(sourceDigests, view.SourceRecordDigest)
		measurements = append(measurements, memory.SessionIndexMeasurement{Provider: provider, SessionID: sessionID, RecordCount: eventUint64(5), Seen: 5, Indexed: 3, Undecodable: 2})
		if buildConversation != nil {
			chain := buildConversation(view, observations)
			if chain != nil {
				if _, err := store.PutConversationChain(*chain); err != nil {
					t.Fatal(err)
				}
				chains = append(chains, memory.ConversationChainDependency{Provider: provider, SessionID: sessionID, SessionViewDigest: view.Digest, Digest: chain.Digest})
			}
		}
		sessionDigests[sessionID] = view.Digest
	}
	project := memory.ProjectView{SchemaVersion: 1, ProjectID: projectID, Generation: 1, StartedAt: "2026-09-07T00:00:00Z", EndedAt: "2026-09-07T00:00:03Z", SourceSessions: len(identities), TerminalCounts: memory.TerminalCounts{Indexed: len(identities)}, SessionViewDependencies: dependencies, ObservationRevisionIDs: []string{}, ProbeStateDigest: probe.Digest, LiveState: memory.StateSnapshot{Branch: "main", Head: probe.Head}, WitnessedState: []memory.DerivedRecord{}, DerivedRecords: []memory.DerivedRecord{}, AggregationCoverage: memory.ProjectAggregationCoverage{ObservationSummariesSeen: len(identities) * 3, EventReferences: memory.AggregationChannelCoverage{Seen: len(identities) * 3, Dropped: len(identities) * 3, Truncated: true}, SelectedEvidenceRevisions: memory.AggregationChannelCoverage{}}, AssociatedUsage: []memory.AssociatedUsage{}, DependencyDigest: testDigest(projectID + "-project-dependency"), ReducerVersion: "v1"}
	project.Digest, err = memory.ProjectViewDigest(project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProjectView(project); err != nil {
		t.Fatal(err)
	}
	manifest := memory.GenerationManifest{SchemaVersion: 1, GenerationID: generationID, ProjectID: projectID, CreatedAt: "2026-09-07T00:00:04Z", SourceRecordDigests: sourceDigests, SessionViews: dependencies, SessionLineages: lineages, ProbeStateDigest: probe.Digest, ProbeCheck: memory.ProbeCheck{SchemaVersion: 1, CheckedAt: "2026-09-07T00:00:04Z", StateDigest: probe.Digest, Available: true, Diagnostics: []memory.Diagnostic{}}, ProjectViewDigest: project.Digest, SessionIndexMeasurements: measurements, ConversationChains: chains}
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
	return eventFixture{dataRoot: dataRoot, projectRoot: projectRoot, projectID: projectID, generationID: generationID, sessionDigests: sessionDigests}
}

func fixtureObservations(t *testing.T, projectID, sessionID string) []memory.ObservationRevision {
	return fixtureObservationsForProvider(t, projectID, "codex", sessionID)
}

func fixtureObservationsForProvider(t *testing.T, projectID, provider, sessionID string) []memory.ObservationRevision {
	t.Helper()
	inputs := []struct{ kind, subject, operation, outcome, excerpt string }{
		{"request", "request-1", "user_request", "", "open /Users/private/repo/secret.txt token sk-abcdefghijklmnopqrstuvwxyz1234567890 " + strings.Repeat("界", 200)},
		{"command", "command-1", "command_finished", "success", ""},
		{"verification", "verification-1", "verification", "passed", "focused tests passed"},
	}
	result := make([]memory.ObservationRevision, len(inputs))
	for index, input := range inputs {
		sequence := index + 1
		sourceIdentity := fixtureSourceIdentity(provider, sessionID)
		value := memory.ObservationRevision{SchemaVersion: 1, Key: memory.ObservationKey{Provider: provider, SessionID: sessionID, SourceIdentity: sourceIdentity, Sequence: sequence, ProjectID: projectID, Kind: input.kind, Subject: input.subject}, Ref: memory.SourceRef{Provider: provider, SessionID: sessionID, SourceIdentity: sourceIdentity, Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: sequence, ByteOffset: int64(sequence * 100)}}, SourceHash: strings.Repeat(string(rune('a'+index)), 64)}, Timestamp: "2026-09-07T00:00:0" + string(rune('1'+index)) + "Z", Operation: input.operation, Outcome: input.outcome, Excerpt: input.excerpt, AdapterID: provider + "-jsonl", AdapterVersion: "v1"}
		value.RevisionID = memory.ObservationRevisionID(value)
		if err := memory.ValidateObservationRevision(value); err != nil {
			t.Fatal(err)
		}
		result[index] = value
	}
	return result
}

func fixtureSourceIdentity(provider, sessionID string) string {
	if provider == "codex" {
		return "source-" + sessionID
	}
	return "source-" + provider + "-" + sessionID
}

func TestConversationAvailableProviderWithoutReaderOrRetainedChainIsUnsupported(t *testing.T) {
	const nativeID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	fixture := buildEventFixtureIdentitiesAt(t, t.TempDir(), "project-conversation-unsupported", "generation-conversation-unsupported", []eventFixtureIdentity{{provider: "claude", sessionID: nativeID}}, nil, nil, func(memory.SessionView, []memory.ObservationRevision) *conversationchain.Document {
		return nil
	})
	request := ConversationRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "claude", SessionID: nativeID, ExpectedGenerationID: fixture.generationID, Limit: 20}
	if _, err := LoadConversationPage(context.Background(), request); eventErrorCode(err) != "visible_reader_unsupported" {
		t.Fatalf("available provider without reader/retained chain error=%v", err)
	}
}

func TestRetainedConversationPublishedSameNativeIDAcrossProviders(t *testing.T) {
	const nativeID = "99999999-9999-4999-8999-999999999999"
	t.Setenv("SESSION_REVIEWER_SESSIONS_ROOT", t.TempDir())
	fixture := buildEventFixtureIdentitiesAt(t, t.TempDir(), "project-retained-providers", "generation-retained-providers", []eventFixtureIdentity{{provider: "codex", sessionID: nativeID}, {provider: "claude", sessionID: nativeID}}, nil, nil, func(view memory.SessionView, revisions []memory.ObservationRevision) *conversationchain.Document {
		document, _, err := conversationchain.Materialize(conversationchain.MaterializeInput{
			View: view,
			Messages: []conversationchain.SourceMessage{
				{Role: conversationchain.RoleUser, Text: "question from " + view.Provider, OccurredAt: "2026-09-07T00:00:01Z", RecordHash: strings.Repeat("a", 64), RecordOrdinal: 1},
				{Role: conversationchain.RoleAssistant, Phase: "final_answer", Text: "answer from " + view.Provider, OccurredAt: "2026-09-07T00:00:02Z", RecordHash: strings.Repeat("b", 64), RecordOrdinal: 2},
				{Role: conversationchain.RoleUser, Text: "second question from " + view.Provider, OccurredAt: "2026-09-07T00:00:03Z", RecordHash: strings.Repeat("c", 64), RecordOrdinal: 3},
				{Role: conversationchain.RoleAssistant, Phase: "final_answer", Text: "second answer from " + view.Provider, OccurredAt: "2026-09-07T00:00:04Z", RecordHash: strings.Repeat("d", 64), RecordOrdinal: 4},
			},
			Revisions:      revisions,
			SourceCoverage: conversationchain.VisibleCoverage{SourceRecords: 5, VisibleMessages: 4, CapturedMessages: 4, Complete: true},
			RuleVersion:    "visible-turn-v1", RedactionVersion: "redaction-v1",
		})
		if err != nil {
			t.Fatal(err)
		}
		return &document
	})
	cursors := make(map[string]string, 2)
	for _, provider := range []string{"codex", "claude"} {
		request := ConversationRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: provider, SessionID: nativeID, ExpectedGenerationID: fixture.generationID, Limit: 1}
		page, err := LoadConversationPage(context.Background(), request)
		if err != nil || page.Provider != provider || page.SessionID != nativeID || page.BodyAvailability != "retained_excerpt" || len(page.TurnUnits) != 1 || page.TurnUnits[0].UserMessage.SourceRef.Provider != provider || page.NextCursor == nil {
			t.Fatalf("provider %s retained page=%+v err=%v", provider, page, err)
		}
		cursors[provider] = *page.NextCursor
		request.Cursor = cursors[provider]
		next, err := LoadConversationPage(context.Background(), request)
		if err != nil || next.Provider != provider || next.RangeStart != 1 || len(next.TurnUnits) != 1 {
			t.Fatalf("provider %s own cursor page=%+v err=%v", provider, next, err)
		}
		request.Cursor = ""
		request.Limit = 20
		request.TurnUnitID = page.TurnUnits[0].TurnUnitID
		detail, err := LoadConversationPage(context.Background(), request)
		if err != nil || len(detail.Messages) != 2 || detail.Messages[1].VisibleExcerpt != "answer from "+provider || detail.Messages[1].Text != nil || detail.Messages[1].SourceRef.Provider != provider {
			t.Fatalf("provider %s retained detail=%+v err=%v", provider, detail, err)
		}
	}
	for provider, other := range map[string]string{"codex": "claude", "claude": "codex"} {
		request := ConversationRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: provider, SessionID: nativeID, ExpectedGenerationID: fixture.generationID, Limit: 1, Cursor: cursors[other]}
		if _, err := LoadConversationPage(context.Background(), request); eventErrorCode(err) != CodeStaleCursor {
			t.Fatalf("provider %s accepted %s cursor: %v", provider, other, err)
		}
	}
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
