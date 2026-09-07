package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	inspectapi "github.com/neomei/SessionReviewer/internal/inspect"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func TestInspectSessionEventsRunsAtRealCommandBoundary(t *testing.T) {
	fixture := newCLIInspectFixture(t)
	setCurrentEnv(t, platform.Env{GOOS: runtime.GOOS, Home: fixture.home})
	before := snapshotCLITree(t, fixture.data)
	var out, errOut bytes.Buffer
	code := Run([]string{"inspect", "session-events", "--project-id", fixture.projectID,
		"--provider", "codex", "--session-id", "session-1", "--expected-generation-id", fixture.generationID,
		"--limit", "2", "--json"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
	page, err := inspectapi.ParseEventPage(out.Bytes())
	if err != nil || page.Total != 3 || page.RangeStart != 0 || page.RangeEnd != 2 || len(page.Items) != 2 || page.Items[0].Sequence != 1 || page.Items[1].Sequence != 2 || page.NextCursor == nil {
		t.Fatalf("page=%+v err=%v raw=%s", page, err, out.String())
	}
	out.Reset()
	errOut.Reset()
	code = Run([]string{"inspect", "session-events", "--project-id", fixture.projectID,
		"--provider", "codex", "--session-id", "session-1", "--expected-generation-id", fixture.generationID,
		"--cursor", *page.NextCursor, "--limit", "2", "--json"}, &out, &errOut)
	next, err := inspectapi.ParseEventPage(out.Bytes())
	if code != 0 || err != nil || next.RangeStart != 2 || next.RangeEnd != 3 || len(next.Items) != 1 || next.Items[0].Sequence != 3 || next.NextCursor != nil {
		t.Fatalf("code=%d next=%+v err=%v stderr=%s", code, next, err, errOut.String())
	}
	if after := snapshotCLITree(t, fixture.data); after != before {
		t.Fatal("inspect CLI mutated fixture data tree or permissions")
	}
}

func TestInspectSessionEventsWritesBoundedMachineErrors(t *testing.T) {
	fixture := newCLIInspectFixture(t)
	setCurrentEnv(t, platform.Env{GOOS: runtime.GOOS, Home: fixture.home})
	tests := []struct {
		name string
		args []string
		code string
	}{
		{"generation", []string{"inspect", "session-events", "--project-id", fixture.projectID, "--provider", "codex", "--session-id", "session-1", "--expected-generation-id", "generation-other", "--limit", "2", "--json"}, "generation_mismatch"},
		{"anchor", []string{"inspect", "session-events", "--project-id", fixture.projectID, "--provider", "codex", "--session-id", "session-1", "--expected-generation-id", fixture.generationID, "--anchor", "4", "--limit", "2", "--json"}, "anchor_out_of_range"},
		{"malformed", []string{"inspect", "session-events", "--project-id", fixture.projectID, "--provider", "codex", "--session-id", "session-1", "--expected-generation-id", fixture.generationID, "--limit", "0", "--json"}, "invalid_argument"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := Run(test.args, &out, &errOut); code == 0 {
				t.Fatalf("unexpected success stdout=%s", out.String())
			}
			var diagnostic inspectDiagnostic
			if err := strictjson.Decode(out.Bytes(), &diagnostic); err != nil || diagnostic.Error.Code != test.code || diagnostic.Error.Message == "" || len(diagnostic.Error.Message) > 512 {
				t.Fatalf("diagnostic=%+v err=%v raw=%s", diagnostic, err, out.String())
			}
			if errOut.Len() != 0 || strings.Contains(out.String(), fixture.data) || strings.Contains(out.String(), fixture.projectRoot) {
				t.Fatalf("unsafe output stdout=%s stderr=%s", out.String(), errOut.String())
			}
		})
	}
}

type cliInspectFixture struct{ home, data, projectRoot, projectID, generationID string }

func newCLIInspectFixture(t *testing.T) cliInspectFixture {
	t.Helper()
	home := t.TempDir()
	dataRoot := filepath.Join(home, ".local", "share", "session-reviewer")
	projectRoot := t.TempDir()
	projectID, generationID := "project-inspect-cli", "generation-inspect-cli"
	legacy := config.ProjectMapping{ID: projectID, Root: projectRoot}
	binding, err := projectidentity.Resolve(legacy, projectRoot, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	mapping := legacy
	mapping.AuthenticatedAliases = []config.AuthenticatedProjectAlias{binding.AuthenticatedAlias}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{mapping}}); err != nil {
		t.Fatal(err)
	}
	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	probe := memory.ProjectProbeState{SchemaVersion: 1, ProjectID: projectID, CanonicalRoot: projectRoot, Branch: "main", Head: strings.Repeat("a", 40), RemoteIdentityHashes: []string{}, VersionFiles: []memory.ProbeFile{}, RequiredProjectionFiles: []memory.ProbeFile{}, ProbeVersion: "v1", Diagnostics: []memory.Diagnostic{}}
	probe.Digest, err = memory.ProjectProbeStateDigest(probe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProbeState(probe); err != nil {
		t.Fatal(err)
	}
	observations := make([]memory.ObservationRevision, 3)
	for index := range observations {
		sequence := index + 1
		kind := []string{"request", "command", "verification"}[index]
		observations[index] = memory.ObservationRevision{SchemaVersion: 1, Key: memory.ObservationKey{Provider: "codex", SessionID: "session-1", SourceIdentity: "source-session-1", Sequence: sequence, ProjectID: projectID, Kind: kind, Subject: kind + "-1"}, Ref: memory.SourceRef{Provider: "codex", SessionID: "session-1", SourceIdentity: "source-session-1", Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: sequence, ByteOffset: int64(sequence * 100)}}, SourceHash: strings.Repeat(string(rune('a'+index)), 64)}, Timestamp: "2026-09-07T00:00:0" + string(rune('1'+index)) + "Z", Operation: kind, Excerpt: "safe excerpt", AdapterID: "codex-jsonl", AdapterVersion: "v1"}
		observations[index].RevisionID = memory.ObservationRevisionID(observations[index])
	}
	chunkDigest, err := store.PutObservationChunk(observations)
	if err != nil {
		t.Fatal(err)
	}
	summaries := make([]memory.ObservationSummary, 3)
	active := make([]string, 3)
	lineageActive := map[string]string{}
	for index, observation := range observations {
		active[index] = observation.RevisionID
		summaries[index] = memory.ObservationSummary{RevisionID: observation.RevisionID, Sequence: observation.Key.Sequence, Kind: observation.Key.Kind, Subject: observation.Key.Subject, OccurredAt: observation.Timestamp, Operation: observation.Operation, Excerpt: observation.Excerpt}
		keyDigest, err := memory.Digest(observation.Key)
		if err != nil {
			t.Fatal(err)
		}
		lineageActive[keyDigest] = observation.RevisionID
	}
	sourceDigest := cliInspectDigest("source")
	view := memory.SessionView{SchemaVersion: 1, ProjectID: projectID, Provider: "codex", SessionID: "session-1", SourceIdentity: "source-session-1", SourceRecordDigest: sourceDigest, UsageRecordDigest: sourceDigest, StartedAt: "2026-09-07T00:00:00Z", EndedAt: "2026-09-07T00:00:03Z", TerminalState: memory.Indexed, SourceAvailability: memory.SourceAvailable, ActiveRevisionIDs: active, ObservationSummaries: summaries, ObservationChunkDigests: []string{chunkDigest}, DerivedRecords: []memory.DerivedRecord{}, Diagnostics: []memory.Diagnostic{}, DependencyDigest: cliInspectDigest("dependency"), MaterializerVersion: "v1"}
	view.Digest, err = memory.SessionViewDigest(view)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSessionView(view); err != nil {
		t.Fatal(err)
	}
	lineage := memory.SessionLineage{SchemaVersion: 1, ProjectID: projectID, Provider: "codex", SessionID: "session-1", SourceIdentity: view.SourceIdentity, ActiveRevisions: lineageActive, SupersededRevisions: map[string]string{}, WithdrawnRevisions: map[string]string{}}
	lineage.Digest, err = memory.SessionLineageDigest(lineage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSessionLineage(lineage); err != nil {
		t.Fatal(err)
	}
	dependency := memory.SessionViewDependency{Provider: "codex", SessionID: "session-1", Digest: view.Digest}
	project := memory.ProjectView{SchemaVersion: 1, ProjectID: projectID, Generation: 1, StartedAt: view.StartedAt, EndedAt: view.EndedAt, SourceSessions: 1, TerminalCounts: memory.TerminalCounts{Indexed: 1}, SessionViewDependencies: []memory.SessionViewDependency{dependency}, ObservationRevisionIDs: []string{}, ProbeStateDigest: probe.Digest, LiveState: memory.StateSnapshot{Branch: "main", Head: probe.Head}, WitnessedState: []memory.DerivedRecord{}, DerivedRecords: []memory.DerivedRecord{}, AggregationCoverage: memory.ProjectAggregationCoverage{ObservationSummariesSeen: 3, EventReferences: memory.AggregationChannelCoverage{Seen: 3, Dropped: 3, Truncated: true}, SelectedEvidenceRevisions: memory.AggregationChannelCoverage{}}, AssociatedUsage: []memory.AssociatedUsage{}, DependencyDigest: cliInspectDigest("project"), ReducerVersion: "v1"}
	project.Digest, err = memory.ProjectViewDigest(project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProjectView(project); err != nil {
		t.Fatal(err)
	}
	manifest := memory.GenerationManifest{SchemaVersion: 1, GenerationID: generationID, ProjectID: projectID, CreatedAt: "2026-09-07T00:00:04Z", SourceRecordDigests: []string{sourceDigest}, SessionViews: []memory.SessionViewDependency{dependency}, SessionLineages: []memory.SessionLineageDependency{{Provider: "codex", SessionID: "session-1", Digest: lineage.Digest}}, ProbeStateDigest: probe.Digest, ProbeCheck: memory.ProbeCheck{SchemaVersion: 1, CheckedAt: "2026-09-07T00:00:04Z", StateDigest: probe.Digest, Available: true, Diagnostics: []memory.Diagnostic{}}, ProjectViewDigest: project.Digest, SessionIndexMeasurements: []memory.SessionIndexMeasurement{{Provider: "codex", SessionID: "session-1", Seen: 3, Indexed: 3}}}
	index, err := sessionindex.Build(sessionindex.BuildInput{ProjectView: project, Manifest: manifest, SessionViews: map[sessionindex.SessionKey]*memory.SessionView{{Provider: "codex", SessionID: "session-1"}: &view}, GeneratedAt: time.Date(2026, 9, 7, 0, 0, 4, 0, time.UTC)})
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
	return cliInspectFixture{home: home, data: dataRoot, projectRoot: projectRoot, projectID: projectID, generationID: generationID}
}

func cliInspectDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
