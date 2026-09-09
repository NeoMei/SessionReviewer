package presentation

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

const (
	milestoneTime1 = "2026-09-08T01:00:00Z"
	milestoneTime2 = "2026-09-08T01:00:01Z"
	milestoneTime3 = "2026-09-08T01:00:02Z"
)

type milestoneFactSpec struct {
	kind, operation, outcome, timestamp, excerpt string
	line                                         int
	fields                                       map[string]string
}

type milestoneFixtureSpec struct {
	provider, sessionID, sourceIdentity string
	messages                            []conversationchain.SourceMessage
	facts                               []milestoneFactSpec
	coverage                            *conversationchain.VisibleCoverage
}

func TestProjectMilestonesQualifiesOnlyClosedMachineEvidence(t *testing.T) {
	tests := []struct {
		name      string
		messages  []conversationchain.SourceMessage
		facts     []milestoneFactSpec
		wantKind  string
		wantFacts uint64
	}{
		{name: "user only", messages: milestoneMessages("question", "")},
		{name: "successful generic command", messages: milestoneMessages("run it", "done"), facts: []milestoneFactSpec{{kind: "command", operation: "command_finished", outcome: "success", line: 2, timestamp: milestoneTime2, fields: map[string]string{"exit_code": "0"}}}},
		{name: "successful patch", messages: milestoneMessages("patch it", "done"), facts: []milestoneFactSpec{{kind: "file", operation: "file_change", outcome: "success", line: 2, timestamp: milestoneTime2, fields: map[string]string{"path": "safe.go"}}}},
		{name: "failed verification", messages: milestoneMessages("test it", "failed"), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "failed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"passed": "0", "failed": "1"}}}},
		{name: "unknown verification", messages: milestoneMessages("test it", "unknown"), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "unknown", line: 2, timestamp: milestoneTime2}}},
		{name: "conflicting passed verification", messages: milestoneMessages("test it", "conflict"), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"exit_code": "1", "failed": "1"}}}},
		{name: "real producer conflicting boolean", messages: milestoneMessages("test it", "conflict"), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"component": "go:all", "status": "test", "exit_code": "0", "passed": "true", "failed": "true", "tool_id": "call-1"}}}},
		{name: "plain command failure", messages: milestoneMessages("run it", "failed"), facts: []milestoneFactSpec{{kind: "command", operation: "command_finished", outcome: "failure", line: 2, timestamp: milestoneTime2, fields: map[string]string{"exit_code": "1"}}}},
		{name: "passed verification", messages: milestoneMessages("test it", "passed"), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"passed": "12", "failed": "0"}}}, wantKind: "machine_verification", wantFacts: 1},
		{name: "real producer passed verification", messages: milestoneMessages("test it", "passed"), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"component": "go:all", "status": "test", "exit_code": "0", "passed": "true", "failed": "false", "tool_id": "call-1"}}}, wantKind: "machine_verification", wantFacts: 1},
		{name: "commit", messages: milestoneMessages("commit it", "committed"), facts: []milestoneFactSpec{{kind: "commit", operation: "commit_created", outcome: "observed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"git_head": strings.Repeat("a", 40)}}}, wantKind: "machine_commit", wantFacts: 1},
		{name: "release", messages: milestoneMessages("release it", "released"), facts: []milestoneFactSpec{{kind: "release", operation: "release_published", outcome: "observed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"release_id": "v1"}}}, wantKind: "machine_release", wantFacts: 1},
		{name: "deployment", messages: milestoneMessages("deploy it", "deployed"), facts: []milestoneFactSpec{{kind: "deployment", operation: "deployment", outcome: "observed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"target": "production"}}}, wantKind: "machine_deployment", wantFacts: 1},
		{name: "version", messages: milestoneMessages("version it", "versioned"), facts: []milestoneFactSpec{{kind: "version", operation: "version", outcome: "observed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"version": "1.2.3"}}}, wantKind: "machine_version", wantFacts: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := milestoneProjectInput(t, milestoneFixtureSpec{messages: test.messages, facts: test.facts})
			got, err := ProjectMilestones(input)
			if err != nil {
				t.Fatal(err)
			}
			if got.QualifyingFacts != test.wantFacts || got.UnassignedQualifyingFacts != 0 {
				t.Fatalf("fact counts=%d/%d want=%d/0", got.QualifyingFacts, got.UnassignedQualifyingFacts, test.wantFacts)
			}
			if test.wantKind == "" {
				if len(got.Timeline) != 0 || len(got.ChainDependencies) != 0 {
					t.Fatalf("unqualified fact created output: %+v", got)
				}
				return
			}
			if len(got.Timeline) != 1 || got.Timeline[0].Kind != test.wantKind || len(got.ChainDependencies) != 1 {
				t.Fatalf("qualified output=%+v", got)
			}
			if got.Timeline[0].Title != "Machine-observed "+strings.TrimPrefix(test.wantKind, "machine_") || got.Timeline[0].Summary == "" {
				t.Fatalf("machine event was not neutrally marked: %+v", got.Timeline[0])
			}
		})
	}
}

func TestProjectMilestonesEmitsOneMilestoneForMultipleFactsInOneTurn(t *testing.T) {
	input := milestoneProjectInput(t, milestoneFixtureSpec{
		messages: milestoneMessages("verify and commit", "done"),
		facts: []milestoneFactSpec{
			{kind: "command", operation: "command_finished", outcome: "success", line: 2, timestamp: milestoneTime2, fields: map[string]string{"exit_code": "0", "tool_id": "call-1"}},
			{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"passed": "1", "failed": "0", "tool_id": "call-1"}},
			{kind: "commit", operation: "commit_created", outcome: "observed", line: 2, timestamp: milestoneTime3, fields: map[string]string{"git_head": strings.Repeat("a", 40)}},
		},
	})
	got, err := ProjectMilestones(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Timeline) != 1 || got.QualifyingFacts != 2 || got.Timeline[0].Kind != "machine_commit" {
		t.Fatalf("one-turn qualification was duplicated or weak: %+v", got)
	}
}

func TestProjectMilestonesStableIdentityUsesFixedGeneratorFamily(t *testing.T) {
	baseSpec := milestoneFixtureSpec{
		messages: milestoneMessages("verify", "passed"),
		facts:    []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"passed": "1", "failed": "0"}}},
	}
	base, err := ProjectMilestones(milestoneProjectInput(t, baseSpec))
	if err != nil {
		t.Fatal(err)
	}
	extendedSpec := baseSpec
	extendedSpec.facts = append(append([]milestoneFactSpec(nil), baseSpec.facts...), milestoneFactSpec{kind: "release", operation: "release_published", outcome: "observed", line: 2, timestamp: milestoneTime3, fields: map[string]string{"release_id": "v1"}})
	extended, err := ProjectMilestones(milestoneProjectInput(t, extendedSpec))
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Timeline) != 1 || len(extended.Timeline) != 1 || base.Timeline[0].ID != extended.Timeline[0].ID {
		t.Fatalf("qualified-turn-v1 identity evolved: base=%+v extended=%+v", base.Timeline, extended.Timeline)
	}
	if base.Timeline[0].Kind != "machine_verification" || extended.Timeline[0].Kind != "machine_release" {
		t.Fatalf("display kind did not evolve independently: %q -> %q", base.Timeline[0].Kind, extended.Timeline[0].Kind)
	}
}

func TestProjectMilestonesSortsCanonicalInstantsAndShuffledInput(t *testing.T) {
	late := milestoneSessionInput(t, milestoneFixtureSpec{sessionID: "session-late", sourceIdentity: "source-late", messages: milestoneMessagesAt("late", "done", milestoneTime1, milestoneTime2), facts: []milestoneFactSpec{{kind: "commit", operation: "commit_created", outcome: "observed", line: 2, timestamp: milestoneTime3, fields: map[string]string{"git_head": strings.Repeat("a", 40)}}}})
	early := milestoneSessionInput(t, milestoneFixtureSpec{provider: "claude", sessionID: "session-early", sourceIdentity: "source-early", messages: milestoneMessagesAt("early", "done", milestoneTime3, milestoneTime3), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime1, fields: map[string]string{"passed": "1", "failed": "0"}}}})
	input := MilestoneInput{ProjectID: "project-1", GenerationID: "generation-1", ProjectViewDigest: digest64("9"), Sessions: []MilestoneSessionInput{late, early}}
	got, err := ProjectMilestones(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Sessions = []MilestoneSessionInput{early, late}
	for index := range input.Sessions {
		sort.Slice(input.Sessions[index].Revisions, func(i, j int) bool { return i > j })
	}
	again, err := ProjectMilestones(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, again) || len(got.Timeline) != 2 || got.Timeline[0].OccurredAt != milestoneTime1 || got.Timeline[1].OccurredAt != milestoneTime3 {
		t.Fatalf("projection depends on caller order or source ordinal timestamps: first=%+v again=%+v", got, again)
	}
}

func TestProjectMilestonesSortsFractionalAndEqualRFC3339Instants(t *testing.T) {
	later := milestoneSessionInput(t, milestoneFixtureSpec{sessionID: "session-later", sourceIdentity: "source-later", messages: milestoneMessages("later", "done"), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: "2026-09-08T01:00:02.9Z", fields: map[string]string{"passed": "true", "failed": "false"}}}})
	earlier := milestoneSessionInput(t, milestoneFixtureSpec{sessionID: "session-earlier", sourceIdentity: "source-earlier", messages: milestoneMessages("earlier", "done"), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: "2026-09-08T01:00:02Z", fields: map[string]string{"passed": "true", "failed": "false"}}}})
	input := MilestoneInput{ProjectID: "project-1", GenerationID: "generation-1", ProjectViewDigest: digest64("9"), Sessions: []MilestoneSessionInput{later, earlier}}
	got, err := ProjectMilestones(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Timeline) != 2 || got.Timeline[0].OccurredAt != "2026-09-08T01:00:02Z" || got.Timeline[1].OccurredAt != "2026-09-08T01:00:02.9Z" {
		t.Fatalf("fractional instants sorted lexically: %+v", got.Timeline)
	}

	utcZ := milestoneSessionInput(t, milestoneFixtureSpec{sessionID: "session-z", sourceIdentity: "source-z", messages: milestoneMessages("same instant z", "done"), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: "2026-09-08T01:00:02Z", fields: map[string]string{"passed": "true", "failed": "false"}}}})
	utcOffset := milestoneSessionInput(t, milestoneFixtureSpec{sessionID: "session-offset", sourceIdentity: "source-offset", messages: milestoneMessages("same instant offset", "done"), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: "2026-09-08T01:00:02+00:00", fields: map[string]string{"passed": "true", "failed": "false"}}}})
	equal := MilestoneInput{ProjectID: "project-1", GenerationID: "generation-1", ProjectViewDigest: digest64("9"), Sessions: []MilestoneSessionInput{utcZ, utcOffset}}
	equalGot, err := ProjectMilestones(equal)
	if err != nil {
		t.Fatal(err)
	}
	if len(equalGot.Timeline) != 2 || equalGot.Timeline[0].ID > equalGot.Timeline[1].ID {
		t.Fatalf("equal instants did not use stable ID tie-break: %+v", equalGot.Timeline)
	}
}

func TestProjectMilestonesSelectsLatestSameCategoryFactByInstantThenIdentity(t *testing.T) {
	input := milestoneProjectInput(t, milestoneFixtureSpec{messages: milestoneMessages("verify twice", "done"), facts: []milestoneFactSpec{
		{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: "2026-09-08T01:00:02Z", fields: map[string]string{"component": "old", "passed": "true", "failed": "false"}},
		{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: "2026-09-08T01:00:02+00:00", fields: map[string]string{"component": "new", "passed": "true", "failed": "false"}},
	}})
	got, err := ProjectMilestones(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Timeline) != 1 || !strings.Contains(got.Timeline[0].Summary, "component=new") {
		t.Fatalf("equal-instant strongest fact ignored canonical sequence: %+v", got.Timeline)
	}
}

func milestoneProjectInput(t *testing.T, spec milestoneFixtureSpec) MilestoneInput {
	t.Helper()
	return MilestoneInput{ProjectID: "project-1", GenerationID: "generation-1", ProjectViewDigest: digest64("9"), Sessions: []MilestoneSessionInput{milestoneSessionInput(t, spec)}}
}

func milestoneSessionInput(t *testing.T, spec milestoneFixtureSpec) MilestoneSessionInput {
	t.Helper()
	if spec.provider == "" {
		spec.provider = "codex"
	}
	if spec.sessionID == "" {
		spec.sessionID = "session-1"
	}
	if spec.sourceIdentity == "" {
		spec.sourceIdentity = "source-1"
	}
	revisions := make([]memory.ObservationRevision, 0, len(spec.facts))
	for index, fact := range spec.facts {
		if fact.timestamp == "" {
			fact.timestamp = milestoneTime2
		}
		value := memory.ObservationRevision{
			SchemaVersion: memory.MemorySchemaVersion,
			Key:           memory.ObservationKey{Provider: spec.provider, SessionID: spec.sessionID, SourceIdentity: spec.sourceIdentity, Sequence: fact.line*10 + index, ProjectID: "project-1", Kind: fact.kind, Subject: fmt.Sprintf("%s-%d", fact.kind, index)},
			Ref:           memory.SourceRef{Provider: spec.provider, SessionID: spec.sessionID, SourceIdentity: spec.sourceIdentity, Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: fact.line, ByteOffset: int64(fact.line * 10)}}, SourceHash: fmt.Sprintf("%064x", fact.line*100+index+1)},
			Timestamp:     fact.timestamp, Operation: fact.operation, Outcome: fact.outcome, Fields: fact.fields, Excerpt: fact.excerpt, AdapterID: spec.provider, AdapterVersion: "v1",
		}
		value.RevisionID = memory.ObservationRevisionID(value)
		revisions = append(revisions, value)
	}
	view := milestoneSessionView(spec.provider, spec.sessionID, spec.sourceIdentity, revisions)
	coverage := conversationchain.VisibleCoverage{SourceRecords: milestoneMaxOrdinal(spec.messages, spec.facts), VisibleMessages: uint64(len(spec.messages)), CapturedMessages: uint64(len(spec.messages)), Complete: true}
	if spec.coverage != nil {
		coverage = *spec.coverage
	}
	chain, _, err := conversationchain.Materialize(conversationchain.MaterializeInput{View: view, Messages: spec.messages, Revisions: revisions, SourceCoverage: coverage, RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"})
	if err != nil {
		t.Fatalf("materialize milestone fixture: %v", err)
	}
	return MilestoneSessionInput{View: view, Chain: chain, Revisions: revisions}
}

func milestoneSessionView(provider, sessionID, sourceIdentity string, revisions []memory.ObservationRevision) memory.SessionView {
	values := append([]memory.ObservationRevision(nil), revisions...)
	sort.Slice(values, func(i, j int) bool {
		if values[i].Timestamp != values[j].Timestamp {
			return values[i].Timestamp < values[j].Timestamp
		}
		if values[i].Key.Sequence != values[j].Key.Sequence {
			return values[i].Key.Sequence < values[j].Key.Sequence
		}
		return values[i].RevisionID < values[j].RevisionID
	})
	view := memory.SessionView{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: "project-1", Provider: provider, SessionID: sessionID, SourceIdentity: sourceIdentity,
		SourceRecordDigest: digest64("1"), UsageRecordDigest: digest64("2"), StartedAt: milestoneTime1, EndedAt: milestoneTime3,
		TerminalState: memory.Indexed, SourceAvailability: memory.SourceAvailable, ActiveRevisionIDs: []string{}, ObservationSummaries: []memory.ObservationSummary{},
		ObservationChunkDigests: []string{}, DerivedRecords: []memory.DerivedRecord{}, Diagnostics: []memory.Diagnostic{}, DependencyDigest: digest64("3"), MaterializerVersion: "v1",
	}
	for _, revision := range values {
		view.ActiveRevisionIDs = append(view.ActiveRevisionIDs, revision.RevisionID)
		view.ObservationSummaries = append(view.ObservationSummaries, memory.ObservationSummary{RevisionID: revision.RevisionID, Sequence: revision.Key.Sequence, Kind: revision.Key.Kind, Subject: revision.Key.Subject, OccurredAt: revision.Timestamp, Operation: revision.Operation, Object: revision.Object, Outcome: revision.Outcome, Fields: revision.Fields, Excerpt: revision.Excerpt})
	}
	view.Digest, _ = memory.SessionViewDigest(view)
	return view
}

func milestoneMessages(question, answer string) []conversationchain.SourceMessage {
	return milestoneMessagesAt(question, answer, milestoneTime1, milestoneTime3)
}

func milestoneMessagesAt(question, answer, questionTime, answerTime string) []conversationchain.SourceMessage {
	messages := []conversationchain.SourceMessage{{Role: conversationchain.RoleUser, Text: question, OccurredAt: questionTime, RecordOrdinal: 1, RecordHash: strings.Repeat("a", 64)}}
	if answer != "" {
		messages = append(messages, conversationchain.SourceMessage{Role: conversationchain.RoleAssistant, Phase: "final_answer", Text: answer, OccurredAt: answerTime, RecordOrdinal: 3, RecordHash: strings.Repeat("c", 64)})
	}
	return messages
}

func milestoneMaxOrdinal(messages []conversationchain.SourceMessage, facts []milestoneFactSpec) uint64 {
	var maximum uint64
	for _, message := range messages {
		if message.RecordOrdinal > maximum {
			maximum = message.RecordOrdinal
		}
	}
	for _, fact := range facts {
		if uint64(fact.line) > maximum {
			maximum = uint64(fact.line)
		}
	}
	return maximum
}

func assertProjectionValid(t *testing.T, input MilestoneInput, got MilestoneProjection) {
	t.Helper()
	presentation := reviewv4.Presentation{
		SchemaVersion: 4, MinimumReaderVersion: "0.4.3", MinimumWriterVersion: "0.4.3", ProjectID: input.ProjectID, GenerationID: input.GenerationID, ProjectViewDigest: input.ProjectViewDigest,
		Timeline: got.Timeline, Decisions: []reviewv4.Decision{}, Risks: []reviewv4.Risk{}, OpenLoops: []reviewv4.OpenLoop{}, ProblemRootIDs: []string{}, ProblemNodes: []reviewv4.ProblemNode{}, ChainDependencies: got.ChainDependencies, HumanPatches: []reviewv4.Patch{}, OrphanPatches: []reviewv4.Patch{}, GeneratedBaselines: []reviewv4.Baseline{},
	}
	if err := reviewv4.ValidatePresentation(presentation); err != nil {
		t.Fatalf("projector returned invalid v4 values: %v\n%+v", err, got)
	}
}
