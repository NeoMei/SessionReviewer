package inspect

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/sessionview"
)

func TestLoadSessionSummaryUsesPublishedIdentity(t *testing.T) {
	fixture := buildEventFixture(t, "project-summary", "generation-summary", "session-1")
	before := snapshotEventTree(t, fixture.dataRoot)
	got, err := LoadSessionSummary(context.Background(), SummaryRequest{
		DataRoot: fixture.dataRoot, ProjectID: fixture.projectID,
		Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != "project-summary" || got.Provider != "codex" || got.SessionID != "session-1" || got.GenerationID != fixture.generationID || got.SessionViewDigest != fixture.sessionDigests["session-1"] || got.Coverage != (Coverage{Seen: 5, Indexed: 3, Undecodable: 2}) {
		t.Fatalf("summary=%+v", got)
	}
	if got.KeyOperations.Total != 1 || got.VerificationResults.Total != 1 || got.UnresolvedQuestions.Total != 0 || got.Errors.Total != 0 {
		t.Fatalf("unexpected projection=%+v", got)
	}
	if err := ValidateSummary(got); err != nil {
		t.Fatal(err)
	}
	if after := snapshotEventTree(t, fixture.dataRoot); !reflect.DeepEqual(before, after) {
		t.Fatal("summary inspection changed configured or private state")
	}
}

func TestLoadSessionSummaryRejectsWrongIdentityAndDivergentRevision(t *testing.T) {
	fixture := buildEventFixture(t, "project-summary-auth", "generation-summary-auth", "session-1")
	base := SummaryRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID}
	tests := []struct {
		name string
		edit func(*SummaryRequest)
		code string
	}{
		{"generation", func(r *SummaryRequest) { r.ExpectedGenerationID = "generation-other" }, CodeGenerationMismatch},
		{"provider", func(r *SummaryRequest) { r.Provider = "claude" }, CodeInvalidArgument},
		{"Session", func(r *SummaryRequest) { r.SessionID = "session-other" }, CodeInvalidArgument},
		{"project", func(r *SummaryRequest) { r.ProjectID = "project-other" }, CodeInvalidArgument},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.edit(&request)
			if _, err := LoadSessionSummary(context.Background(), request); eventErrorCode(err) != test.code {
				t.Fatalf("code=%q err=%v", eventErrorCode(err), err)
			}
		})
	}

	divergent := buildEventFixtureCustomizedAt(t, t.TempDir(), "project-summary-divergent", "generation-summary-divergent", []string{"session-1"}, nil, func(_ string, view *memory.SessionView) {
		view.ObservationSummaries[0].Excerpt = "does not match immutable revision"
	})
	if _, err := LoadSessionSummary(context.Background(), SummaryRequest{DataRoot: divergent.dataRoot, ProjectID: divergent.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: divergent.generationID}); eventErrorCode(err) != CodeInvalidArgument {
		t.Fatalf("divergent code=%q err=%v", eventErrorCode(err), err)
	}
}

func TestLoadSessionSummaryWorksWithoutSourceLogsAndObservesCancellation(t *testing.T) {
	fixture := buildEventFixture(t, "project-summary-source", "generation-summary-source", "session-1")
	request := SummaryRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID}
	if _, err := LoadSessionSummary(context.Background(), request); err != nil {
		t.Fatalf("retained summary required unavailable source logs: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	inspectCheckpoint = func(phase string) {
		if phase == "summary_item" {
			cancel()
		}
	}
	t.Cleanup(func() { inspectCheckpoint = nil })
	if _, err := LoadSessionSummary(ctx, request); eventErrorCode(err) != CodeInvalidArgument || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("cancellation code=%q err=%v", eventErrorCode(err), err)
	}
}

func TestLoadSessionSummaryWorksWhenPublishedSourceIsMarkedUnavailable(t *testing.T) {
	fixture := buildEventFixtureCustomizedAt(t, t.TempDir(), "project-summary-retained", "generation-summary-retained", []string{"session-1"}, nil, func(_ string, view *memory.SessionView) {
		view.TerminalState = memory.Missing
		view.SourceAvailability = memory.SourceUnavailable
	})
	request := SummaryRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID}
	got, err := LoadSessionSummary(context.Background(), request)
	if err != nil || got.KeyOperations.Total != 1 || got.VerificationResults.Total != 1 {
		t.Fatalf("retained summary=%+v err=%v", got, err)
	}
}

func TestLoadSessionSummaryExcludesBookkeepingTypedOperations(t *testing.T) {
	fixture := buildEventFixtureCustomizedAt(t, t.TempDir(), "project-summary-bookkeeping", "generation-summary-bookkeeping", []string{"session-1"}, func(observations []memory.ObservationRevision) {
		operations := []struct {
			kind, operation, outcome string
			fields                   map[string]string
		}{
			{kind: "artifact", operation: "session_started"},
			{kind: "artifact", operation: "cwd_changed"},
			{kind: "command", operation: "command_finished", outcome: "success", fields: map[string]string{"exit_code": "0"}},
		}
		for index := range observations {
			observations[index].Key.Kind = operations[index].kind
			observations[index].Operation = operations[index].operation
			observations[index].Outcome = operations[index].outcome
			observations[index].Fields = operations[index].fields
			observations[index].Excerpt = ""
			observations[index].RevisionID = memory.ObservationRevisionID(observations[index])
		}
	}, nil)
	before := snapshotEventTree(t, fixture.dataRoot)
	got, err := LoadSessionSummary(context.Background(), SummaryRequest{
		DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex",
		SessionID: "session-1", ExpectedGenerationID: fixture.generationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyOperations.Total != 1 || len(got.KeyOperations.Items) != 1 {
		t.Fatalf("key operations=%+v", got.KeyOperations)
	}
	if item := got.KeyOperations.Items[0]; item.Text != "命令已完成 · 成功 · 退出码 0" || !reflect.DeepEqual(item.SourceRevisionIDs, []string{item.RevisionID}) {
		t.Fatalf("typed command=%+v", item)
	}
	if got.Coverage != (Coverage{Seen: 5, Indexed: 3, Undecodable: 2}) {
		t.Fatalf("accepted coverage changed: %+v", got.Coverage)
	}
	if got.Rules.RuleVersion != "summary-typed-fact-text-v3" {
		t.Fatalf("rule version=%q", got.Rules.RuleVersion)
	}
	if after := snapshotEventTree(t, fixture.dataRoot); !reflect.DeepEqual(before, after) {
		t.Fatal("summary inspection changed configured or private state")
	}
}

func TestSessionSummaryReducerExcludesBookkeepingAndKeepsRealTypedOperations(t *testing.T) {
	sessionStarted := summaryTestRevision(t, 1, "artifact", "", "", map[string]string{"status": "session_started"})
	cwdChanged := summaryTestRevision(t, 2, "artifact", "", "", map[string]string{"status": "cwd_changed"})
	command := summaryTestRevision(t, 3, "command", "success", "", map[string]string{"status": "command_finished", "exit_code": "0"})
	artifact := summaryTestRevision(t, 4, "artifact", "success", "", map[string]string{"status": "artifact_created", "artifact_id": "artifact-1"})

	got, err := reduceSessionSummary(context.Background(), summaryTestInput([]memory.ObservationRevision{sessionStarted, cwdChanged, command, artifact}))
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{command.RevisionID, artifact.RevisionID}
	gotIDs := []string{got.KeyOperations.Items[0].RevisionID, got.KeyOperations.Items[1].RevisionID}
	if got.KeyOperations.Total != 2 || !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("key operations=%+v", got.KeyOperations)
	}
	if got.Coverage != (Coverage{Seen: 4, Indexed: 4}) {
		t.Fatalf("accepted coverage changed: %+v", got.Coverage)
	}

	metadataOnly, err := reduceSessionSummary(context.Background(), summaryTestInput([]memory.ObservationRevision{sessionStarted, cwdChanged}))
	if err != nil {
		t.Fatal(err)
	}
	if metadataOnly.KeyOperations.Total != 0 || len(metadataOnly.KeyOperations.Items) != 0 {
		t.Fatalf("metadata-only operations=%+v", metadataOnly.KeyOperations)
	}
}

func TestLoadSessionSummaryRejectsTamperedImmutableDependency(t *testing.T) {
	fixture := buildEventFixture(t, "project-summary-tamper", "generation-summary-tamper", "session-1")
	path := filepath.Join(fixture.dataRoot, "projects", fixture.projectID, "memory-v1", "sessions", strings.TrimPrefix(fixture.sessionDigests["session-1"], "sha256:")+".json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := SummaryRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID}
	if _, err := LoadSessionSummary(context.Background(), request); eventErrorCode(err) != CodeInvalidArgument {
		t.Fatalf("tampered dependency code=%q err=%v", eventErrorCode(err), err)
	}
}

func TestLoadSessionSummaryRejectsConcurrentPublishedGenerationAdvance(t *testing.T) {
	fixture := buildEventFixture(t, "project-summary-race", "generation-summary-race", "session-1")
	writer, err := memorystore.Open(fixture.dataRoot, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	prepared, manifest, err := writer.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	manifest.GenerationID = "generation-summary-concurrent"
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
		viewBody, loadErr := writer.LoadObject(memorystore.ObjectSessionView, dependency.Digest)
		if loadErr != nil {
			t.Fatal(loadErr)
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
	inspectCheckpoint = func(phase string) {
		if phase == "before_published_recheck" {
			if err := writer.CommitPublished(manifest.GenerationID, proof); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() { inspectCheckpoint = nil })
	request := SummaryRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID}
	if _, err := LoadSessionSummary(context.Background(), request); eventErrorCode(err) != CodeGenerationMismatch {
		t.Fatalf("generation race code=%q err=%v", eventErrorCode(err), err)
	}
}

func TestSessionSummaryReducerCapsSortsRedactsAndRetainsSources(t *testing.T) {
	revisions := make([]memory.ObservationRevision, 40)
	for index := range revisions {
		revisions[index] = summaryTestRevision(t, index+1, "verification", "passed", "verified /Users/alice/private token sk-abcdefghijklmnopqrstuvwxyz1234567890 "+strings.Repeat("界", 200), map[string]string{"component": "runtime", "status": "test"})
	}
	reversed := slices.Clone(revisions)
	slices.Reverse(reversed)
	input := summaryTestInput(revisions)
	got, err := reduceSessionSummary(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.revisions = reversed
	reversedGot, err := reduceSessionSummary(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, reversedGot) {
		t.Fatal("reversing authenticated inputs changed summary")
	}
	block := got.VerificationResults
	if block.Total != 40 || block.Shown != 32 || block.Omitted != 8 || block.Coverage != (Coverage{Seen: 40, Indexed: 32, Unprojected: 8}) {
		t.Fatalf("block=%+v", block)
	}
	for index, item := range block.Items {
		if !utf8.ValidString(item.Text) || len(item.Text) > 512 || strings.Contains(item.Text, "/Users/alice") || strings.Contains(item.Text, "sk-abcdefghijklmnopqrstuvwxyz1234567890") || len(item.SourceRevisionIDs) != 1 || item.SourceRevisionIDs[0] != item.RevisionID {
			t.Fatalf("unsafe item %d=%+v", index, item)
		}
		if index > 0 && entryLess(item, block.Items[index-1]) {
			t.Fatalf("items are not canonical at %d", index)
		}
	}
}

func TestSessionSummaryReducerKeepsFailuresUnresolvedUntilExactRecovery(t *testing.T) {
	bare := summaryTestRevision(t, 1, "request", "", "please investigate", nil)
	recoveredFailure := summaryTestRevision(t, 2, "verification", "failed", "tests failed", map[string]string{"component": "runtime", "status": "test"})
	unresolvedFailure := summaryTestRevision(t, 3, "verification", "failure", "lint failed", map[string]string{"component": "lint", "status": "lint"})
	explicitRequestFailure := summaryTestRevision(t, 4, "request", "failed", "requested check failed", nil)
	recovery := summaryTestRevision(t, 5, "verification", "passed", "tests passed", map[string]string{"component": "runtime", "status": "test"})
	input := summaryTestInput([]memory.ObservationRevision{bare, recoveredFailure, unresolvedFailure, explicitRequestFailure, recovery})
	input.view.MaterializerVersion = sessionview.MaterializerVersion + "-visible-turn-v3"
	recoveryID, _ := summaryRecoveryRecordIdentity(recoveredFailure.RevisionID, recovery.RevisionID, summaryRecoveryIdentity(recoveredFailure))
	input.view.DerivedRecords = []memory.DerivedRecord{{
		ID: recoveryID, Kind: "recovery_link", Subject: "test:runtime", OccurredAt: recovery.Timestamp,
		DependencyRevisionIDs: []string{recoveredFailure.RevisionID, recovery.RevisionID}, RuleID: "matching-operation-component", RuleVersion: "session-view-v1",
		Fields: map[string]string{"operation": "test", "component": "runtime", "outcome": "recovered"},
	}}
	got, err := reduceSessionSummary(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got.VerificationResults.Total != 3 || got.Errors.Total != 3 || got.UnresolvedQuestions.Total != 2 {
		t.Fatalf("failure projection=%+v", got)
	}
	for _, item := range got.Errors.Items {
		if item.RevisionID == recoveredFailure.RevisionID && item.Code != "verification_failed" {
			t.Fatalf("failed verification was mislabeled: %+v", item)
		}
	}
	for _, item := range got.UnresolvedQuestions.Items {
		if item.RevisionID == bare.RevisionID || item.RevisionID == recoveredFailure.RevisionID {
			t.Fatalf("unsupported or recovered fact shown unresolved: %+v", item)
		}
	}
	input.view.DerivedRecords[0].ID = "recovery-tampered"
	tampered, err := reduceSessionSummary(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if tampered.UnresolvedQuestions.Total != 3 {
		t.Fatalf("tampered recovery link closed a failure: %+v", tampered.UnresolvedQuestions)
	}
	input.view.DerivedRecords[0].ID = recoveryID
	input.view.DerivedRecords[0].DependencyRevisionIDs[0], input.view.DerivedRecords[0].DependencyRevisionIDs[1] = input.view.DerivedRecords[0].DependencyRevisionIDs[1], input.view.DerivedRecords[0].DependencyRevisionIDs[0]
	tampered, err = reduceSessionSummary(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if tampered.UnresolvedQuestions.Total != 3 {
		t.Fatalf("reordered recovery dependencies closed a failure: %+v", tampered.UnresolvedQuestions)
	}
}

func TestSessionSummaryRecoveryRequiresUnambiguousTypedOutcomes(t *testing.T) {
	tests := []struct {
		name            string
		failureKind     string
		failureOutcome  string
		failureFields   map[string]string
		successKind     string
		successOutcome  string
		successFields   map[string]string
		wantUnresolved  uint64
		wantSuccessText string
	}{
		{
			name: "consistent supported recovery", failureKind: "verification", failureOutcome: "failed",
			failureFields: map[string]string{"component": "runtime", "status": "test", "exit_code": "1"},
			successKind:   "verification", successOutcome: "passed",
			successFields:  map[string]string{"component": "runtime", "status": "test", "exit_code": "0"},
			wantUnresolved: 0, wantSuccessText: "通过",
		},
		{
			name: "success conflicts with nonzero exit", failureKind: "verification", failureOutcome: "failed",
			failureFields: map[string]string{"component": "runtime", "status": "test", "exit_code": "1"},
			successKind:   "verification", successOutcome: "passed",
			successFields:  map[string]string{"component": "runtime", "status": "test", "exit_code": "1"},
			wantUnresolved: 1, wantSuccessText: "结果冲突",
		},
		{
			name: "failure conflicts with zero exit", failureKind: "verification", failureOutcome: "failed",
			failureFields: map[string]string{"component": "runtime", "status": "test", "exit_code": "0"},
			successKind:   "verification", successOutcome: "passed",
			successFields:  map[string]string{"component": "runtime", "status": "test", "exit_code": "0"},
			wantUnresolved: 1, wantSuccessText: "通过",
		},
		{
			name: "unknown success outcome", failureKind: "verification", failureOutcome: "failed",
			failureFields: map[string]string{"component": "runtime", "status": "test", "exit_code": "1"},
			successKind:   "verification", successOutcome: "maybe",
			successFields:  map[string]string{"component": "runtime", "status": "test", "exit_code": "0"},
			wantUnresolved: 1, wantSuccessText: "结果未知",
		},
		{
			name: "unknown success operation", failureKind: "verification", failureOutcome: "failed",
			failureFields: map[string]string{"component": "runtime", "status": "deploy", "exit_code": "1"},
			successKind:   "verification", successOutcome: "passed",
			successFields:  map[string]string{"component": "runtime", "status": "deploy", "exit_code": "0"},
			wantUnresolved: 1, wantSuccessText: "结果未知",
		},
		{
			name: "command started cannot recover", failureKind: "command", failureOutcome: "failed",
			failureFields: map[string]string{"component": "runtime", "status": "command_started", "exit_code": "1"},
			successKind:   "command", successOutcome: "passed",
			successFields:  map[string]string{"component": "runtime", "status": "command_started", "exit_code": "0"},
			wantUnresolved: 1, wantSuccessText: "结果未记录",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := summaryTestRevision(t, 1, test.failureKind, test.failureOutcome, "failure", test.failureFields)
			success := summaryTestRevision(t, 2, test.successKind, test.successOutcome, "success", test.successFields)
			other := summaryTestRevision(t, 3, test.failureKind, "failed", "other exact failure", test.failureFields)
			input := summaryTestInput([]memory.ObservationRevision{failure, success, other})
			id, subject := summaryRecoveryRecordIdentity(failure.RevisionID, success.RevisionID, summaryRecoveryIdentity(failure))
			input.view.DerivedRecords = []memory.DerivedRecord{{
				ID: id, Kind: "recovery_link", Subject: subject, OccurredAt: success.Timestamp,
				DependencyRevisionIDs: []string{failure.RevisionID, success.RevisionID}, RuleID: "matching-operation-component", RuleVersion: "session-view-v1",
				Fields: map[string]string{"operation": summaryRecoveryIdentity(failure).operation, "component": "runtime", "outcome": "recovered"},
			}}
			got, err := reduceSessionSummary(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if got.UnresolvedQuestions.Total != test.wantUnresolved+1 {
				t.Fatalf("unresolved=%d, want %d", got.UnresolvedQuestions.Total, test.wantUnresolved+1)
			}
			if !strings.Contains(summaryFactText(success), test.wantSuccessText) {
				t.Fatalf("success text=%q, want %q", summaryFactText(success), test.wantSuccessText)
			}
			if got.UnresolvedQuestions.Items[len(got.UnresolvedQuestions.Items)-1].RevisionID != other.RevisionID {
				t.Fatalf("recovery did not preserve exact failure identity: %+v", got.UnresolvedQuestions)
			}
		})
	}
}

func TestSessionSummaryReducerIncludesOnlyDependencyClosedSessionPhases(t *testing.T) {
	first := summaryTestRevision(t, 1, "release", "success", "release one", nil)
	second := summaryTestRevision(t, 2, "release", "success", "release two", nil)
	input := summaryTestInput([]memory.ObservationRevision{first, second})
	input.project.DerivedRecords = []memory.DerivedRecord{
		{ID: "phase-selected", Kind: "phase_boundary", Subject: "release-2", OccurredAt: second.Timestamp, DependencyRevisionIDs: []string{first.RevisionID, second.RevisionID}, RuleID: "structural-boundary", RuleVersion: "project-view-v1", Fields: map[string]string{"trigger": "release_change"}},
		{ID: "phase-cross-session", Kind: "phase_boundary", Subject: "foreign", OccurredAt: second.Timestamp, DependencyRevisionIDs: []string{second.RevisionID, testDigest("foreign-revision")}, RuleID: "structural-boundary", RuleVersion: "project-view-v1", Fields: map[string]string{"trigger": "release_change"}},
	}
	got, err := reduceSessionSummary(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got.PhaseBoundaries.Total != 1 || len(got.PhaseBoundaries.Items) != 1 || got.PhaseBoundaries.Items[0].RevisionID != "phase-selected" || !reflect.DeepEqual(got.PhaseBoundaries.Items[0].SourceRevisionIDs, []string{first.RevisionID, second.RevisionID}) {
		t.Fatalf("phase boundaries=%+v", got.PhaseBoundaries)
	}
}

func TestSessionSummaryReducerAccountsForUnprojectablePhaseRecords(t *testing.T) {
	revisions := make([]memory.ObservationRevision, 65)
	dependencies := make([]string, len(revisions))
	for index := range revisions {
		revisions[index] = summaryTestRevision(t, index+1, "release", "success", "release", nil)
		dependencies[index] = revisions[index].RevisionID
	}
	input := summaryTestInput(revisions)
	input.project.DerivedRecords = []memory.DerivedRecord{{
		ID: "phase-too-many-sources", Kind: "phase_boundary", Subject: "bounded", OccurredAt: revisions[len(revisions)-1].Timestamp,
		DependencyRevisionIDs: dependencies, RuleID: "structural-boundary", RuleVersion: "project-view-v1", Fields: map[string]string{"trigger": "release_change"},
	}}
	got, err := reduceSessionSummary(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got.PhaseBoundaries.Total != 1 || got.PhaseBoundaries.Shown != 0 || got.PhaseBoundaries.Omitted != 1 || got.PhaseBoundaries.Coverage != (Coverage{Seen: 1, Unprojected: 1}) || len(got.PhaseBoundaries.Items) != 0 {
		t.Fatalf("unprojectable phase record was not accounted for: %+v", got.PhaseBoundaries)
	}
}

func summaryTestInput(revisions []memory.ObservationRevision) summaryInput {
	active := make([]string, len(revisions))
	for index := range revisions {
		active[index] = revisions[index].RevisionID
	}
	return summaryInput{
		request:      SummaryRequest{ProjectID: "project-summary-reducer", Provider: "codex", SessionID: "session-1", ExpectedGenerationID: "generation-summary-reducer"},
		generationID: "generation-summary-reducer",
		view:         memory.SessionView{ProjectID: "project-summary-reducer", Provider: "codex", SessionID: "session-1", Digest: testDigest("summary-view"), DependencyDigest: testDigest("summary-view-dependency"), ActiveRevisionIDs: active, MaterializerVersion: "session-view-v1"},
		revisions:    revisions,
		entry:        sessionindex.Entry{Coverage: sessionindex.Coverage{Seen: uint64(len(revisions)), Indexed: uint64(len(revisions))}},
		project:      memory.ProjectView{Digest: testDigest("summary-project"), DependencyDigest: testDigest("summary-project-dependency")},
	}
}

func summaryTestRevision(t *testing.T, sequence int, kind, outcome, excerpt string, fields map[string]string) memory.ObservationRevision {
	t.Helper()
	value := memory.ObservationRevision{
		SchemaVersion: 1,
		Key:           memory.ObservationKey{Provider: "codex", SessionID: "session-1", SourceIdentity: "source-session-1", Sequence: sequence, ProjectID: "project-summary-reducer", Kind: kind, Subject: kind + "-fact"},
		Ref:           memory.SourceRef{Provider: "codex", SessionID: "session-1", SourceIdentity: "source-session-1", Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: sequence, ByteOffset: int64(sequence * 100)}}, SourceHash: strings.Repeat("a", 64)},
		Timestamp:     "2026-09-07T00:" + twoDigits(sequence/60) + ":" + twoDigits(sequence%60) + "Z",
		Operation:     fields["status"], Outcome: outcome, Fields: fields, Excerpt: excerpt, AdapterID: "codex-jsonl", AdapterVersion: "v1",
	}
	value.RevisionID = memory.ObservationRevisionID(value)
	return value
}

func twoDigits(value int) string {
	return string([]byte{'0' + byte(value/10), '0' + byte(value%10)})
}
