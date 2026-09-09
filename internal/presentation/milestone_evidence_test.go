package presentation

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func TestClosureEvidenceUsesExactFivePartContract(t *testing.T) {
	messages := []conversationchain.SourceMessage{
		{Role: conversationchain.RoleUser, Text: "run verification", OccurredAt: milestoneTime1, RecordOrdinal: 1, RecordHash: strings.Repeat("a", 64)},
		{Role: conversationchain.RoleAssistant, Phase: "commentary", Text: "working", OccurredAt: milestoneTime2, RecordOrdinal: 3, RecordHash: strings.Repeat("c", 64)},
		{Role: conversationchain.RoleAssistant, Phase: "final_answer", Text: "verification passed", OccurredAt: milestoneTime3, RecordOrdinal: 5, RecordHash: strings.Repeat("e", 64)},
	}
	input := milestoneProjectInput(t, milestoneFixtureSpec{messages: messages, facts: []milestoneFactSpec{
		{kind: "command", operation: "command_started", line: 2, timestamp: milestoneTime1, fields: map[string]string{"command_signature": "go:test", "tool_id": "call-1"}},
		{kind: "command", operation: "command_finished", outcome: "success", line: 4, timestamp: milestoneTime2, fields: map[string]string{"exit_code": "0", "tool_id": "call-1"}},
		{kind: "verification", operation: "verification", outcome: "passed", line: 4, timestamp: milestoneTime2, fields: map[string]string{"passed": "12", "failed": "0", "tool_id": "call-1"}},
	}})
	got, err := ProjectMilestones(input)
	if err != nil || len(got.Timeline) != 1 {
		t.Fatalf("verified turn missing: projection=%+v err=%v", got, err)
	}
	loop := got.Timeline[0].ClosedLoop
	if loop.TriggerQuestion.State != "present" || loop.TriggerQuestion.Text != "run verification" || loop.Conclusion.Kind != reviewv4.ConclusionVisibleAnswerExcerpt || loop.Conclusion.Text != "verification passed" {
		t.Fatalf("trigger or last answer changed: %+v", loop)
	}
	if loop.Execution.State != "present" || !strings.Contains(loop.Execution.Text, "命令执行已开始") || !strings.Contains(loop.Execution.Text, "命令执行已结束（通过）") {
		t.Fatalf("execution evidence missing: %+v", loop.Execution)
	}
	if loop.Verification.State != "present" || !strings.Contains(loop.Verification.Text, "验证记录（通过）") {
		t.Fatalf("verification evidence missing: %+v", loop.Verification)
	}
	if loop.ImpactAndFollowUp.State != "missing" || loop.ImpactAndFollowUp.Text != "" || missingReason(loop.ImpactAndFollowUp.MissingReason) != "not_captured" {
		t.Fatalf("project impact was invented: %+v", loop.ImpactAndFollowUp)
	}
	if len(loop.SourceTurnRefs) != 1 || loop.SourceTurnRefs[0].SessionViewDigest != input.Sessions[0].View.Digest || !reflect.DeepEqual(loop.SourceTurnRefs, loop.TriggerQuestion.SourceTurnRefs) {
		t.Fatalf("closure lost exact snapshot-qualified source: %+v", loop)
	}
	assertProjectionValid(t, input, got)
}

func TestClosureEvidenceUsesTypedMissingReasonsAndZeroText(t *testing.T) {
	tests := []struct {
		name                 string
		messages             []conversationchain.SourceMessage
		facts                []milestoneFactSpec
		wantConclusionReason string
		wantExecutionReason  string
		wantVerifyReason     string
	}{
		{name: "verification only without answer or execution", messages: milestoneMessages("verify", ""), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"passed": "1", "failed": "0"}}}, wantConclusionReason: "no_visible_answer", wantExecutionReason: "no_execution_evidence"},
		{name: "commit without verification", messages: milestoneMessages("commit", "done"), facts: []milestoneFactSpec{{kind: "commit", operation: "commit_created", outcome: "observed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"git_head": strings.Repeat("a", 40)}}}, wantVerifyReason: "not_verified"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := milestoneProjectInput(t, milestoneFixtureSpec{messages: test.messages, facts: test.facts})
			got, err := ProjectMilestones(input)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Timeline) != 1 {
				t.Fatalf("qualified turn missing: %+v", got)
			}
			loop := got.Timeline[0].ClosedLoop
			if test.wantConclusionReason != "" && (loop.Conclusion.Kind != reviewv4.ConclusionMissing || loop.Conclusion.Text != "" || missingReason(loop.Conclusion.MissingReason) != test.wantConclusionReason) {
				t.Fatalf("invalid missing conclusion: %+v", loop.Conclusion)
			}
			if test.wantExecutionReason != "" && (loop.Execution.State != "missing" || loop.Execution.Text != "" || missingReason(loop.Execution.MissingReason) != test.wantExecutionReason) {
				t.Fatalf("invalid missing execution: %+v", loop.Execution)
			}
			if test.wantVerifyReason != "" && (loop.Verification.State != "missing" || loop.Verification.Text != "" || missingReason(loop.Verification.MissingReason) != test.wantVerifyReason) {
				t.Fatalf("invalid missing verification: %+v", loop.Verification)
			}
			if loop.ImpactAndFollowUp.State != "missing" || loop.ImpactAndFollowUp.Text != "" || missingReason(loop.ImpactAndFollowUp.MissingReason) != "not_captured" {
				t.Fatalf("impact/follow-up filler appeared: %+v", loop.ImpactAndFollowUp)
			}
			assertProjectionValid(t, input, got)
		})
	}
}

func TestClosureEvidenceDoesNotRenderContradictoryVerificationAsPassed(t *testing.T) {
	for _, test := range []struct {
		name   string
		fields map[string]string
	}{
		{name: "exit and failure contradiction", fields: map[string]string{"status": "test", "exit_code": "1", "passed": "true", "failed": "true"}},
		{name: "explicit passed false", fields: map[string]string{"status": "test", "exit_code": "0", "passed": "false", "failed": "false"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := milestoneProjectInput(t, milestoneFixtureSpec{messages: milestoneMessages("commit after checks", "committed"), facts: []milestoneFactSpec{
				{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2, fields: test.fields},
				{kind: "commit", operation: "commit_created", outcome: "observed", line: 2, timestamp: milestoneTime3, fields: map[string]string{"git_head": strings.Repeat("a", 40)}},
			}})
			got, err := ProjectMilestones(input)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Timeline) != 1 || got.Timeline[0].Kind != "machine_commit" {
				t.Fatalf("independent commit qualification missing: %+v", got)
			}
			verification := got.Timeline[0].ClosedLoop.Verification
			if verification.State != "present" || !strings.Contains(verification.Text, "验证记录（证据冲突）") || strings.Contains(verification.Text, "验证记录（通过）") {
				t.Fatalf("contradictory verification was presented as passed: %+v", verification)
			}
		})
	}
}

func TestClosureEvidencePreservesPartialAndTruncatedCoverage(t *testing.T) {
	long := strings.Repeat("界", 1800) + " /Users/alice/private/answer"
	messages := []conversationchain.SourceMessage{
		{Role: conversationchain.RoleUser, Text: "question", OccurredAt: milestoneTime1, RecordOrdinal: 1, RecordHash: strings.Repeat("a", 64)},
		{Role: conversationchain.RoleAssistant, Phase: "commentary", Text: long, OccurredAt: milestoneTime2, RecordOrdinal: 3, RecordHash: strings.Repeat("c", 64)},
	}
	coverage := conversationchain.VisibleCoverage{SourceRecords: 70000, VisibleMessages: 2, CapturedMessages: 2, TruncatedMessages: 1, TruncatedBodies: 1, OversizedRecords: 1, Complete: false}
	input := milestoneProjectInput(t, milestoneFixtureSpec{messages: messages, facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime3, fields: map[string]string{"passed": "1", "failed": "0"}}}, coverage: &coverage})
	got, err := ProjectMilestones(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Timeline) != 1 {
		t.Fatalf("qualified turn missing: %+v", got)
	}
	loop := got.Timeline[0].ClosedLoop
	if loop.Conclusion.Kind != reviewv4.ConclusionVisibleAnswerExcerpt || len([]byte(loop.Conclusion.Text)) > 4096 || !utf8.ValidString(loop.Conclusion.Text) || strings.Contains(loop.Conclusion.Text, "/Users/alice") {
		t.Fatalf("answer was unbounded or unsanitized: bytes=%d text=%q", len(loop.Conclusion.Text), loop.Conclusion.Text)
	}
	if loop.Coverage != (reviewv4.ClosedLoopCoverage{SourceTurns: 1, CapturedTurns: 1, TruncatedTurns: 1}) {
		t.Fatalf("partial/oversized coverage hidden: %+v", loop.Coverage)
	}
	assertProjectionValid(t, input, got)
}

func TestClosureEvidenceRedactsAgainAndNeverUsesRawToolOrHiddenCanaries(t *testing.T) {
	secret := "sk-abcdefghijklmnopqrstuvwxyz1234567890"
	path := "/Users/alice/private/project"
	input := milestoneProjectInput(t, milestoneFixtureSpec{messages: milestoneMessages("do not leak "+secret+" "+path, "done "+secret+" "+path), facts: []milestoneFactSpec{
		{kind: "command", operation: "command_finished", outcome: "success", line: 2, timestamp: milestoneTime2, fields: map[string]string{"exit_code": "0", "tool_id": "call-1"}, excerpt: "RAW_TOOL_OUTPUT_CANARY " + secret},
		{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"component": path, "passed": "1", "failed": "0"}, excerpt: "HIDDEN_ROLE_CANARY prompt=" + secret},
	}})
	got, err := ProjectMilestones(input)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("%+v", got)
	for _, canary := range []string{secret, path, "RAW_TOOL_OUTPUT_CANARY", "HIDDEN_ROLE_CANARY", "prompt="} {
		if strings.Contains(body, canary) {
			t.Fatalf("private source leaked %q: %s", canary, body)
		}
	}
}

func TestProjectMilestonesCountsUnassignedQualifyingFactsWithoutMisattachment(t *testing.T) {
	messages := []conversationchain.SourceMessage{{Role: conversationchain.RoleUser, Text: "unrelated later question", OccurredAt: milestoneTime2, RecordOrdinal: 3, RecordHash: strings.Repeat("c", 64)}}
	input := milestoneProjectInput(t, milestoneFixtureSpec{messages: messages, facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 1, timestamp: milestoneTime1, fields: map[string]string{"passed": "1", "failed": "0"}}}})
	got, err := ProjectMilestones(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.QualifyingFacts != 1 || got.UnassignedQualifyingFacts != 1 || len(got.Timeline) != 0 || len(got.ChainDependencies) != 0 {
		t.Fatalf("unassigned fact was hidden or attached: %+v", got)
	}
}

func TestProjectMilestonesUsesFirstMiddleAndLastTurnsAcrossThousands(t *testing.T) {
	const turns = 3001
	messages := make([]conversationchain.SourceMessage, 0, turns)
	facts := make([]milestoneFactSpec, 0, 3)
	qualified := map[int]bool{0: true, turns / 2: true, turns - 1: true}
	for index := 0; index < turns; index++ {
		line := index*2 + 1
		messages = append(messages, conversationchain.SourceMessage{Role: conversationchain.RoleUser, Text: fmt.Sprintf("question-%d", index), OccurredAt: milestoneTime1, RecordOrdinal: uint64(line), RecordHash: fmt.Sprintf("%064x", line)})
		if qualified[index] {
			facts = append(facts, milestoneFactSpec{kind: "verification", operation: "verification", outcome: "passed", line: line, timestamp: fmt.Sprintf("2026-09-08T01:%02d:%02dZ", index%60, index%60), fields: map[string]string{"passed": "1", "failed": "0"}})
		}
	}
	input := milestoneProjectInput(t, milestoneFixtureSpec{messages: messages, facts: facts})
	got, err := ProjectMilestones(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Timeline) != 3 || got.QualifyingFacts != 3 || len(got.ChainDependencies) != 1 || len(got.ChainDependencies[0].TurnUnitIDs) != turns {
		t.Fatalf("long-chain edge turns lost: milestones=%d facts=%d dependencies=%+v", len(got.Timeline), got.QualifyingFacts, got.ChainDependencies)
	}
}

func TestProjectMilestonesRejectsMalformedAuthenticatedInputs(t *testing.T) {
	base := milestoneProjectInput(t, milestoneFixtureSpec{messages: milestoneMessages("verify", "passed"), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"passed": "1", "failed": "0"}}}})
	mixed := milestoneSessionInput(t, milestoneFixtureSpec{provider: "claude", sessionID: "session-1", sourceIdentity: "source-claude", messages: milestoneMessages("verify", "passed"), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"passed": "1", "failed": "0"}}}})
	if got, err := ProjectMilestones(MilestoneInput{ProjectID: base.ProjectID, GenerationID: base.GenerationID, ProjectViewDigest: base.ProjectViewDigest, Sessions: []MilestoneSessionInput{base.Sessions[0], mixed}}); err != nil || len(got.Timeline) != 2 || got.Timeline[0].ID == got.Timeline[1].ID {
		t.Fatalf("provider namespacing failed: projection=%+v err=%v", got, err)
	}
	tests := []struct {
		name string
		edit func(*MilestoneInput)
	}{
		{name: "foreign input project", edit: func(input *MilestoneInput) { input.ProjectID = "project-foreign" }},
		{name: "foreign chain project", edit: func(input *MilestoneInput) { input.Sessions[0].Chain.ProjectID = "project-foreign" }},
		{name: "foreign view", edit: func(input *MilestoneInput) { input.Sessions[0].View.ProjectID = "project-foreign" }},
		{name: "bad chain digest", edit: func(input *MilestoneInput) { input.Sessions[0].Chain.Digest = digest64("f") }},
		{name: "rehashed omitted active evidence", edit: func(input *MilestoneInput) {
			input.Sessions[0].Chain.TurnUnits[0].Results = nil
			input.Sessions[0].Chain.Digest = conversationchain.CanonicalDigest(input.Sessions[0].Chain)
		}},
		{name: "inactive revision", edit: func(input *MilestoneInput) { input.Sessions[0].Revisions = nil }},
		{name: "substituted revision", edit: func(input *MilestoneInput) {
			input.Sessions[0].Revisions[0].Outcome = "failed"
			input.Sessions[0].Revisions[0].RevisionID = memory.ObservationRevisionID(input.Sessions[0].Revisions[0])
		}},
		{name: "duplicate Session snapshot", edit: func(input *MilestoneInput) {
			duplicate := input.Sessions[0]
			duplicate.View.Digest = digest64("8")
			input.Sessions = append(input.Sessions, duplicate)
		}},
		{name: "invalid generation", edit: func(input *MilestoneInput) { input.GenerationID = "bad generation" }},
		{name: "invalid project view digest", edit: func(input *MilestoneInput) { input.ProjectViewDigest = "bad" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneMilestoneInput(base)
			test.edit(&input)
			if got, err := ProjectMilestones(input); err == nil || len(got.Timeline) != 0 || len(got.ChainDependencies) != 0 {
				t.Fatalf("malformed input returned success or partial output: projection=%+v err=%v", got, err)
			}
		})
	}
}

func TestProjectMilestonesRejectsMalformedSessionEvenWhenItHasNoQualifyingFacts(t *testing.T) {
	valid := milestoneSessionInput(t, milestoneFixtureSpec{sessionID: "session-valid", sourceIdentity: "source-valid", messages: milestoneMessages("verify", "passed"), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"passed": "true", "failed": "false"}}}})
	malformed := milestoneSessionInput(t, milestoneFixtureSpec{sessionID: "session-malformed", sourceIdentity: "source-malformed", messages: milestoneMessages("ordinary request", "ordinary answer")})
	malformed.Chain.Digest = digest64("f")
	input := MilestoneInput{ProjectID: "project-1", GenerationID: "generation-1", ProjectViewDigest: digest64("9"), Sessions: []MilestoneSessionInput{valid, malformed}}
	if got, err := ProjectMilestones(input); err == nil || len(got.Timeline) != 0 || got.QualifyingFacts != 0 {
		t.Fatalf("malformed unreferenced input was skipped or partial success returned: projection=%+v err=%v", got, err)
	}
}

func TestProjectMilestonesFailsClosedAboveCapacity(t *testing.T) {
	if testing.Short() {
		t.Skip("capacity fixture")
	}
	inputs := make([]MilestoneSessionInput, 0, 257)
	for session := 0; session < 257; session++ {
		turns := 255
		if session == 256 {
			turns = 257
		}
		messages := make([]conversationchain.SourceMessage, 0, turns)
		facts := make([]milestoneFactSpec, 0, turns)
		for index := 0; index < turns; index++ {
			line := index*2 + 1
			messages = append(messages, conversationchain.SourceMessage{Role: conversationchain.RoleUser, Text: "q", OccurredAt: milestoneTime1, RecordOrdinal: uint64(line), RecordHash: fmt.Sprintf("%064x", session*1000000+line)})
			facts = append(facts, milestoneFactSpec{kind: "verification", operation: "verification", outcome: "passed", line: line, timestamp: milestoneTime2, fields: map[string]string{"passed": "1", "failed": "0"}})
		}
		inputs = append(inputs, milestoneSessionInput(t, milestoneFixtureSpec{sessionID: fmt.Sprintf("session-%d", session), sourceIdentity: fmt.Sprintf("source-%d", session), messages: messages, facts: facts}))
	}
	input := MilestoneInput{ProjectID: "project-1", GenerationID: "generation-1", ProjectViewDigest: digest64("9"), Sessions: inputs}
	got, err := ProjectMilestones(input)
	if err == nil || len(got.Timeline) != 0 || len(got.ChainDependencies) != 0 || got.QualifyingFacts != 0 {
		t.Fatalf("capacity overflow returned partial success: projection counts=%d/%d/%d err=%v", len(got.Timeline), len(got.ChainDependencies), got.QualifyingFacts, err)
	}
}

func missingReason(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func cloneMilestoneInput(input MilestoneInput) MilestoneInput {
	copyInput := input
	copyInput.Sessions = append([]MilestoneSessionInput(nil), input.Sessions...)
	for index := range copyInput.Sessions {
		copyInput.Sessions[index].Revisions = append([]memory.ObservationRevision(nil), input.Sessions[index].Revisions...)
	}
	return copyInput
}

func TestClosureEvidenceProjectionContainsNoCanaryBytes(t *testing.T) {
	input := milestoneProjectInput(t, milestoneFixtureSpec{messages: milestoneMessages("q", "a"), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"passed": "1", "failed": "0"}, excerpt: "raw-tool-output"}}})
	got, err := ProjectMilestones(input)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(fmt.Sprintf("%+v", got)), []byte("raw-tool-output")) {
		t.Fatal("raw observation excerpt entered projection")
	}
}
