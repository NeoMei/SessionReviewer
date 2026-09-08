package conversationchain

import (
	"bytes"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/memory"
)

const (
	testTime1 = "2026-09-08T01:00:00Z"
	testTime2 = "2026-09-08T01:00:01Z"
	testTime3 = "2026-09-08T01:00:02Z"
)

func TestMaterializeRetainedBuildsCausalTurnsAndTypedEvidence(t *testing.T) {
	messages := []SourceMessage{
		retainedMessage(RoleUser, "", "run the checks", testTime2, 1, 'a'),
		retainedMessage(RoleAssistant, "commentary", "checking", testTime1, 2, 'b'),
		retainedMessage(RoleAssistant, "final_answer", "checks passed", testTime3, 5, 'e'),
		retainedMessage(RoleUser, "", "what next?", testTime1, 7, 'f'),
	}
	secret := "sk-abcdefghijklmnopqrstuvwxyz1234567890"
	started := retainedRevision("command", "command_started", "", 3, 'c', testTime3, map[string]string{"command_signature": "go-test", "tool_id": "call-1"}, "/Users/neomei/private/project api_key="+secret)
	finished := retainedRevision("command", "command_finished", "success", 4, 'd', testTime1, map[string]string{"exit_code": "0", "tool_id": "call-1"}, strings.Repeat("x", 900))
	verified := retainedRevision("verification", "verification", "passed", 4, 'd', testTime1, map[string]string{"component": "go:all", "passed": "true", "failed": "false"}, "go test ./...")
	view := retainedView("codex", "session-1", "source-1", []memory.ObservationRevision{verified, started, finished})

	doc, report, err := Materialize(MaterializeInput{View: view, Messages: messages, Revisions: []memory.ObservationRevision{finished, verified, started}, SourceCoverage: completeVisibleCoverage(7, 4), RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if report != (MaterializeReport{}) {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(doc.TurnUnits) != 2 || len(doc.TurnUnits[0].Actions) != 1 || len(doc.TurnUnits[0].Results) != 2 || doc.TurnUnits[1].AnswerState != AnswerNone {
		t.Fatalf("incorrect causal segmentation: %+v", doc)
	}
	if doc.TurnUnits[0].Actions[0].RevisionID != started.RevisionID || doc.TurnUnits[0].Results[0].RevisionID != finished.RevisionID || doc.TurnUnits[0].Results[1].RevisionID != verified.RevisionID {
		t.Fatalf("typed evidence lost source order or exact identity: %+v", doc.TurnUnits[0])
	}
	if got := doc.TurnUnits[0].Actions[0].SourceRef; got.RecordOrdinal != 3 || got.SourceHash != strings.Repeat("c", 64) || got.SourceIdentity != view.SourceIdentity {
		t.Fatalf("typed evidence source reference changed: %+v", got)
	}
	if doc.TurnUnits[0].Results[0].VerificationState != "passed" || doc.TurnUnits[0].Results[1].VerificationState != "passed" {
		t.Fatalf("authoritative outcomes not retained: %+v", doc.TurnUnits[0].Results)
	}
	if doc.TurnUnits[0].StartedAt != testTime2 || doc.TurnUnits[0].AssistantMessages[0].OccurredAt != testTime1 {
		t.Fatal("source order was replaced by timestamp order")
	}
	body, err := Render(doc)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(`"text":`)) {
		t.Fatal("full visible-body property persisted")
	}
	if bytes.Contains(body, []byte("/Users/neomei/private/project")) || bytes.Contains(body, []byte(secret)) || len(doc.TurnUnits[0].Results[0].Excerpt) > retainedEvidenceBytes {
		t.Fatalf("private or unbounded evidence persisted: %s", body)
	}
	visible, _ := MaterializeVisible(view.Provider, view.SessionID, view.SourceIdentity, messages)
	if doc.TurnUnits[0].TurnUnitID != visible[0].TurnUnitID || doc.TurnUnits[1].TurnUnitID != visible[1].TurnUnitID || doc.TurnUnits[0].UserMessage.RevisionID != visible[0].UserMessage.RevisionID {
		t.Fatal("retained materialization changed exposed visible identities")
	}
	again, _, err := Materialize(MaterializeInput{View: view, Messages: messages, Revisions: []memory.ObservationRevision{started, finished, verified}, SourceCoverage: completeVisibleCoverage(7, 4), RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"})
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := Render(again)
	if !bytes.Equal(body, body2) {
		t.Fatal("identical semantic inputs produced different canonical bytes")
	}
}

func TestMaterializeRetainedAnswerStates(t *testing.T) {
	tests := []struct {
		name     string
		messages []SourceMessage
		want     AnswerState
		count    int
	}{
		{"multiple assistant messages", []SourceMessage{retainedMessage(RoleUser, "", "q", testTime1, 1, 'a'), retainedMessage(RoleAssistant, "commentary", "work", testTime2, 2, 'b'), retainedMessage(RoleAssistant, "final_answer", "done", testTime3, 3, 'c')}, AnswerAnswered, 2},
		{"commentary only", []SourceMessage{retainedMessage(RoleUser, "", "q", testTime1, 1, 'a'), retainedMessage(RoleAssistant, "commentary", "work", testTime2, 2, 'b')}, AnswerPartial, 1},
		{"empty final answer", []SourceMessage{retainedMessage(RoleUser, "", "q", testTime1, 1, 'a'), retainedMessage(RoleAssistant, "final_answer", "", testTime2, 2, 'b')}, AnswerPartial, 1},
		{"final followed by interrupted commentary", []SourceMessage{retainedMessage(RoleUser, "", "q", testTime1, 1, 'a'), retainedMessage(RoleAssistant, "final_answer", "done", testTime2, 2, 'b'), retainedMessage(RoleAssistant, "commentary", "extra", testTime3, 3, 'c')}, AnswerPartial, 2},
		{"unanswered final user", []SourceMessage{retainedMessage(RoleUser, "", "q", testTime1, 1, 'a')}, AnswerNone, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := retainedView("codex", "session-1", "source-1", nil)
			doc, _, err := Materialize(MaterializeInput{View: view, Messages: test.messages, SourceCoverage: completeVisibleCoverage(uint64(len(test.messages)), uint64(len(test.messages))), RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"})
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.TurnUnits) != 1 || doc.TurnUnits[0].AnswerState != test.want || len(doc.TurnUnits[0].AssistantMessages) != test.count {
				t.Fatalf("got %+v", doc.TurnUnits)
			}
		})
	}
}

func TestMaterializeRetainedIgnoresAmbientUserAndBoundsUTF8(t *testing.T) {
	large := strings.Repeat("界", 30000)
	messages := []SourceMessage{
		retainedMessage(RoleUser, "", "question", testTime1, 1, 'a'),
		retainedMessage(RoleUser, "", "<environment_context>ambient</environment_context>", testTime2, 2, 'b'),
		retainedMessage(RoleAssistant, "final_answer", large, testTime3, 3, 'c'),
	}
	view := retainedView("codex", "session-1", "source-1", nil)
	doc, _, err := Materialize(MaterializeInput{View: view, Messages: messages, SourceCoverage: completeVisibleCoverage(3, 3), RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.TurnUnits) != 1 || doc.TurnUnits[0].AnswerState != AnswerAnswered || !doc.TurnUnits[0].AssistantMessages[0].Truncated || len(doc.TurnUnits[0].AssistantMessages[0].VisibleExcerpt) > 4096 {
		t.Fatalf("ambient split or multibyte bound failed: %+v", doc)
	}
}

func TestMaterializeRetainedRedactsSecretsBeforeExcerptBounds(t *testing.T) {
	secret := "sk-" + strings.Repeat("a", 40)
	path := "/Users/alice/private/session.txt"
	tests := []struct {
		name, text, forbidden, marker string
	}{
		{"credential crossing excerpt boundary", strings.Repeat("x", 4080) + " " + secret, "sk-aaaaaaaa", "[REDACTED:OP"},
		{"path crossing excerpt boundary", strings.Repeat("x", 4068) + " " + path, path, "[REDACTED:ABSOLUTE_PATH]"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			messages := []SourceMessage{
				retainedMessage(RoleUser, "", "q", testTime1, 1, 'a'),
				retainedMessage(RoleAssistant, "final_answer", test.text, testTime2, 2, 'b'),
			}
			view := retainedView("codex", "session-1", "source-1", nil)
			doc, _, err := Materialize(MaterializeInput{View: view, Messages: messages, SourceCoverage: completeVisibleCoverage(2, 2), RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"})
			if err != nil {
				t.Fatal(err)
			}
			body, err := Render(doc)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(body, []byte(test.forbidden)) || !bytes.Contains(body, []byte(test.marker)) {
				t.Fatalf("pre-boundary secret or path leaked: %s", body)
			}
			visible, _ := MaterializeVisible(view.Provider, view.SessionID, view.SourceIdentity, messages)
			if doc.TurnUnits[0].TurnUnitID != visible[0].TurnUnitID || doc.TurnUnits[0].AssistantMessages[0].RevisionID != visible[0].Messages[1].RevisionID {
				t.Fatal("pre-boundary sanitization changed source-derived IDs")
			}
		})
	}
}

func TestMaterializeRetainedFailsClosedOnInvalidCallerInput(t *testing.T) {
	baseMessage := retainedMessage(RoleUser, "", "q", testTime1, 1, 'a')
	baseRevision := retainedRevision("verification", "verification", "passed", 2, 'b', testTime2, map[string]string{"passed": "true", "failed": "false"}, "verified")
	baseView := retainedView("codex", "session-1", "source-1", []memory.ObservationRevision{baseRevision})
	tests := []struct {
		name string
		edit func(*MaterializeInput)
	}{
		{"malformed message timestamp", func(input *MaterializeInput) { input.Messages[0].OccurredAt = "not-a-time" }},
		{"message zero ordinal", func(input *MaterializeInput) { input.Messages[0].RecordOrdinal = 0 }},
		{"message malformed hash", func(input *MaterializeInput) { input.Messages[0].RecordHash = "bad" }},
		{"duplicate message coordinate", func(input *MaterializeInput) { input.Messages = append(input.Messages, input.Messages[0]) }},
		{"inactive revision", func(input *MaterializeInput) { input.View = retainedView("codex", "session-1", "source-1", nil) }},
		{"missing active revision", func(input *MaterializeInput) { input.Revisions = nil }},
		{"duplicate fact revision", func(input *MaterializeInput) { input.Revisions = append(input.Revisions, input.Revisions[0]) }},
		{"wrong revision source", func(input *MaterializeInput) { input.Revisions[0].Ref.SourceIdentity = "source-2" }},
		{"wrong revision hash", func(input *MaterializeInput) { input.Revisions[0].Ref.SourceHash = "bad" }},
		{"wrong revision ordinal shape", func(input *MaterializeInput) { input.Revisions[0].Ref.Location.JSONL.Line = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := MaterializeInput{View: baseView, Messages: []SourceMessage{baseMessage}, Revisions: []memory.ObservationRevision{baseRevision}, SourceCoverage: completeVisibleCoverage(2, 1), RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"}
			test.edit(&input)
			if _, _, err := Materialize(input); err == nil {
				t.Fatal("invalid caller input was accepted")
			}
		})
	}
}

func TestMaterializeRetainedReportsUnsupportedUnassignedAndIncompleteCoverage(t *testing.T) {
	unassigned := retainedRevision("verification", "verification", "passed", 1, 'a', testTime1, map[string]string{"passed": "true", "failed": "false"}, "before user")
	request := retainedRevision("request", "request", "", 3, 'c', testTime2, nil, "bookkeeping")
	startup := retainedRevision("tool", "startup", "", 4, 'd', testTime2, nil, "startup bookkeeping")
	cwd := retainedRevision("request", "cwd", "", 5, 'e', testTime2, nil, "cwd bookkeeping")
	view := retainedView("codex", "session-1", "source-1", []memory.ObservationRevision{unassigned, request, startup, cwd})
	messages := []SourceMessage{retainedMessage(RoleUser, "", "q", testTime1, 2, 'b'), retainedMessage(RoleAssistant, "final_answer", "done", testTime2, 6, 'f')}
	coverage := completeVisibleCoverage(6, 2)
	coverage.Complete = false
	coverage.MalformedRecords = 1
	doc, report, err := Materialize(MaterializeInput{View: view, Messages: messages, Revisions: []memory.ObservationRevision{request, cwd, unassigned, startup}, SourceCoverage: coverage, RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if report.UnassignedFacts != 1 || report.UnsupportedFacts != 3 || !report.SourceIncomplete {
		t.Fatalf("coverage loss was hidden: %+v", report)
	}
	if doc.TurnUnits[0].AnswerState != AnswerPartial || len(doc.TurnUnits[0].Actions) != 0 || len(doc.TurnUnits[0].Results) != 0 {
		t.Fatalf("incomplete or unsupported facts were misrepresented: %+v", doc.TurnUnits[0])
	}
}

func TestMaterializeRetainedUsesProviderInStableIdentity(t *testing.T) {
	message := retainedMessage(RoleUser, "", "same native id", testTime1, 1, 'a')
	materialize := func(provider string) Document {
		view := retainedView(provider, "session-1", "source-1", nil)
		doc, _, err := Materialize(MaterializeInput{View: view, Messages: []SourceMessage{message}, SourceCoverage: completeVisibleCoverage(1, 1), RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"})
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	if materialize("codex").TurnUnits[0].TurnUnitID == materialize("claude").TurnUnits[0].TurnUnitID {
		t.Fatal("same native ID collided across providers")
	}
}

func TestMaterializeRetainedAssignsEvidenceToFirstMiddleAndLastTurns(t *testing.T) {
	messages := []SourceMessage{
		retainedMessage(RoleUser, "", "first", testTime1, 1, 'a'),
		retainedMessage(RoleUser, "", "middle", testTime2, 4, 'd'),
		retainedMessage(RoleUser, "", "last", testTime3, 7, '7'),
	}
	facts := []memory.ObservationRevision{
		retainedRevision("command", "command_started", "", 2, 'b', testTime3, map[string]string{"command_signature": "first action"}, "unretained raw first"),
		retainedRevision("command", "command_started", "", 5, 'e', testTime1, map[string]string{"command_signature": "middle action"}, "unretained raw middle"),
		retainedRevision("command", "command_started", "", 8, '8', testTime2, map[string]string{"command_signature": "last action"}, "unretained raw last"),
	}
	view := retainedView("codex", "session-1", "source-1", facts)
	doc, _, err := Materialize(MaterializeInput{View: view, Messages: messages, Revisions: []memory.ObservationRevision{facts[2], facts[0], facts[1]}, SourceCoverage: completeVisibleCoverage(8, 3), RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.TurnUnits) != 3 || doc.TurnUnits[0].Actions[0].Excerpt != "command_signature=first action" || doc.TurnUnits[1].Actions[0].Excerpt != "command_signature=middle action" || doc.TurnUnits[2].Actions[0].Excerpt != "command_signature=last action" {
		t.Fatalf("first/middle/last source assignment failed: %+v", doc.TurnUnits)
	}
}

func TestMaterializeRetainedUsesSourceLineBeforeIndependentFactSequence(t *testing.T) {
	first := retainedRevision("command", "command_started", "", 2, 'b', testTime2, map[string]string{"command_signature": "sequence 20"}, "unretained raw 20")
	first.Key.Sequence = 20
	first.RevisionID = memory.ObservationRevisionID(first)
	second := retainedRevision("command", "command_started", "", 2, 'b', testTime2, map[string]string{"command_signature": "sequence 10"}, "unretained raw 10")
	second.Key.Sequence = 10
	second.Key.Subject = "command-second"
	second.RevisionID = memory.ObservationRevisionID(second)
	view := retainedView("codex", "session-1", "source-1", []memory.ObservationRevision{first, second})
	doc, _, err := Materialize(MaterializeInput{View: view, Messages: []SourceMessage{retainedMessage(RoleUser, "", "q", testTime1, 1, 'a')}, Revisions: []memory.ObservationRevision{first, second}, SourceCoverage: completeVisibleCoverage(2, 1), RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.TurnUnits[0].Actions) != 2 || doc.TurnUnits[0].Actions[0].Excerpt != "command_signature=sequence 10" || doc.TurnUnits[0].Actions[1].Excerpt != "command_signature=sequence 20" {
		t.Fatalf("same-record facts ignored sequence secondary order: %+v", doc.TurnUnits[0].Actions)
	}
}

func TestRetainedTurnCursorAdvancesMonotonicallyAcrossSortedFacts(t *testing.T) {
	turns := []TurnUnit{
		{UserMessage: Message{SourceRef: SourceRef{RecordOrdinal: 2}}},
		{UserMessage: Message{SourceRef: SourceRef{RecordOrdinal: 5}}},
		{UserMessage: Message{SourceRef: SourceRef{RecordOrdinal: 8}}},
	}
	var cursor retainedTurnCursor
	ordinals := []uint64{1, 2, 4, 5, 7, 8, 9}
	want := []int{-1, 0, 0, 1, 1, 2, 2}
	for index, ordinal := range ordinals {
		if got := cursor.locate(turns, ordinal); got != want[index] {
			t.Fatalf("ordinal %d located at %d, want %d", ordinal, got, want[index])
		}
	}
}

func TestMaterializeRetainedDependencyDigestBindsRuleAndRedactionVersions(t *testing.T) {
	view := retainedView("codex", "session-1", "source-1", nil)
	message := retainedMessage(RoleUser, "", "q", testTime1, 1, 'a')
	materialize := func(rule, redaction string) Document {
		doc, _, err := Materialize(MaterializeInput{View: view, Messages: []SourceMessage{message}, SourceCoverage: completeVisibleCoverage(1, 1), RuleVersion: rule, RedactionVersion: redaction})
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	base := materialize("visible-turn-v1", "redaction-v1")
	if base.DependencyDigest == materialize("visible-turn-v2", "redaction-v1").DependencyDigest || base.DependencyDigest == materialize("visible-turn-v1", "redaction-v2").DependencyDigest {
		t.Fatal("dependency digest ignored a materialization rule dependency")
	}
}

func TestMaterializeRetainedTypedFactSemanticsDoNotInferFromText(t *testing.T) {
	assertionCanary := "user says PASSED and stdout says exit 0"
	unsupportedCanary := "unsupported commit operation"
	extraFieldCanary := "raw-tool-output-canary"
	neutralExcerptCanary := "raw-neutral-excerpt-canary"
	facts := []memory.ObservationRevision{
		retainedRevision("file", "file_change", "success", 2, 'b', testTime2, map[string]string{"path": "/private/a.go"}, "patched"),
		retainedRevision("file", "file_change", "failure", 3, 'c', testTime2, map[string]string{"path": "/private/b.go", "failed": "true"}, "failed"),
		retainedRevision("command", "command_finished", "", 4, 'd', testTime2, map[string]string{"exit_code": "", "model": extraFieldCanary}, assertionCanary),
		retainedRevision("commit", "commit_created", "observed", 5, 'e', testTime2, map[string]string{"git_head": strings.Repeat("a", 40)}, neutralExcerptCanary),
		retainedRevision("release", "release_published", "observed", 6, 'f', testTime2, map[string]string{"release_id": "v1"}, "release"),
		retainedRevision("deployment", "deployment", "observed", 7, '7', testTime2, map[string]string{"status": "ready"}, "deployment"),
		retainedRevision("version", "version", "observed", 8, '8', testTime2, map[string]string{"version": "1.0.0"}, "version"),
		retainedRevision("branch", "git_observation", "observed", 9, '9', testTime2, map[string]string{"branch": "main"}, "branch"),
		retainedRevision("commit", "startup", "observed", 10, '0', testTime2, map[string]string{"git_head": strings.Repeat("b", 40)}, unsupportedCanary),
	}
	view := retainedView("codex", "session-1", "source-1", facts)
	doc, report, err := Materialize(MaterializeInput{View: view, Messages: []SourceMessage{retainedMessage(RoleUser, "", "q", testTime1, 1, 'a')}, Revisions: facts, SourceCoverage: completeVisibleCoverage(10, 1), RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"})
	if err != nil {
		t.Fatal(err)
	}
	turn := doc.TurnUnits[0]
	if len(turn.Actions) != 1 || len(turn.Results) != 7 || report.UnsupportedFacts != 1 {
		t.Fatalf("typed fact mapping changed: %+v", turn)
	}
	if turn.Results[0].VerificationState != "failed" || turn.Results[1].VerificationState != "unknown" || turn.Results[2].VerificationState != "unknown" {
		t.Fatalf("result state inferred from untrusted strings: %+v", turn.Results)
	}
	body, err := Render(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{assertionCanary, unsupportedCanary, extraFieldCanary, neutralExcerptCanary} {
		if bytes.Contains(body, []byte(canary)) {
			t.Fatalf("unsupported evidence %q persisted: %s", canary, body)
		}
	}
	if !bytes.Contains(body, []byte("exit_code=")) || !bytes.Contains(body, []byte("git_head=")) {
		t.Fatalf("supported typed fields were lost: %s", body)
	}
}

func retainedMessage(role Role, phase, text, occurredAt string, ordinal uint64, hashByte byte) SourceMessage {
	return SourceMessage{Role: role, Phase: phase, Text: text, OccurredAt: occurredAt, RecordOrdinal: ordinal, RecordHash: strings.Repeat(string(hashByte), 64)}
}

func retainedRevision(kind, operation, outcome string, line int, hashByte byte, timestamp string, fields map[string]string, excerpt string) memory.ObservationRevision {
	value := memory.ObservationRevision{
		SchemaVersion: memory.MemorySchemaVersion,
		Key:           memory.ObservationKey{Provider: "codex", SessionID: "session-1", SourceIdentity: "source-1", Sequence: line, ProjectID: "project-1", Kind: kind, Subject: kind + "-" + string(hashByte)},
		Ref:           memory.SourceRef{Provider: "codex", SessionID: "session-1", SourceIdentity: "source-1", Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: line, ByteOffset: int64(line * 10)}}, SourceHash: strings.Repeat(string(hashByte), 64)},
		Timestamp:     timestamp, Operation: operation, Object: "/Users/neomei/project", Outcome: outcome, Fields: fields, Excerpt: excerpt, AdapterID: "codex", AdapterVersion: "v1",
	}
	value.RevisionID = memory.ObservationRevisionID(value)
	return value
}

func retainedView(provider, sessionID, sourceIdentity string, revisions []memory.ObservationRevision) memory.SessionView {
	values := append([]memory.ObservationRevision(nil), revisions...)
	for index := range values {
		if values[index].Key.Provider == "codex" && provider != "codex" {
			values[index].Key.Provider = provider
			values[index].Ref.Provider = provider
			values[index].RevisionID = memory.ObservationRevisionID(values[index])
		}
	}
	// SessionView summaries use their existing canonical time, sequence, revision order.
	for i := 1; i < len(values); i++ {
		for j := i; j > 0; j-- {
			left, right := values[j-1], values[j]
			if left.Timestamp < right.Timestamp || (left.Timestamp == right.Timestamp && (left.Key.Sequence < right.Key.Sequence || (left.Key.Sequence == right.Key.Sequence && left.RevisionID < right.RevisionID))) {
				break
			}
			values[j-1], values[j] = values[j], values[j-1]
		}
	}
	view := memory.SessionView{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: "project-1", Provider: provider, SessionID: sessionID, SourceIdentity: sourceIdentity,
		SourceRecordDigest: "sha256:" + strings.Repeat("1", 64), UsageRecordDigest: "sha256:" + strings.Repeat("2", 64),
		StartedAt: testTime1, EndedAt: testTime3, TerminalState: memory.Indexed, SourceAvailability: memory.SourceAvailable,
		ObservationChunkDigests: []string{}, DerivedRecords: []memory.DerivedRecord{}, Diagnostics: []memory.Diagnostic{},
		DependencyDigest: "sha256:" + strings.Repeat("3", 64), MaterializerVersion: "v1",
	}
	for _, revision := range values {
		view.ActiveRevisionIDs = append(view.ActiveRevisionIDs, revision.RevisionID)
		view.ObservationSummaries = append(view.ObservationSummaries, memory.ObservationSummary{RevisionID: revision.RevisionID, Sequence: revision.Key.Sequence, Kind: revision.Key.Kind, Subject: revision.Key.Subject, OccurredAt: revision.Timestamp, Operation: revision.Operation, Object: revision.Object, Outcome: revision.Outcome, Fields: revision.Fields, Excerpt: revision.Excerpt})
	}
	if view.ActiveRevisionIDs == nil {
		view.ActiveRevisionIDs = []string{}
		view.ObservationSummaries = []memory.ObservationSummary{}
	}
	view.Digest, _ = memory.SessionViewDigest(view)
	return view
}

func completeVisibleCoverage(sourceRecords, visible uint64) VisibleCoverage {
	return VisibleCoverage{SourceRecords: sourceRecords, VisibleMessages: visible, CapturedMessages: visible, Complete: true}
}
