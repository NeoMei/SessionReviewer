package conversationchain

import (
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/memory"
)

func TestMaterializeRetainedKeepsNamedGenericToolFacts(t *testing.T) {
	call := retainedRevision("tool", "tool_call", "", 2, 'b', testTime2, map[string]string{"tool_id": "call_read", "tool_name": "read"}, "raw input must not persist")
	result := retainedRevision("tool", "tool_result", "completed", 2, 'b', testTime3, map[string]string{"tool_id": "call_read", "tool_name": "read", "status": "completed"}, "raw output must not persist")
	call.Key.Subject = "read-call"
	call.RevisionID = memory.ObservationRevisionID(call)
	result.Key.Subject = "read-result"
	result.RevisionID = memory.ObservationRevisionID(result)
	revisions := []memory.ObservationRevision{call, result}
	view := retainedView("codex", "session-1", "source-1", revisions)
	doc, report, err := Materialize(MaterializeInput{View: view, Revisions: revisions, Messages: []SourceMessage{retainedMessage(RoleUser, "", "read it", testTime1, 1, 'a')}, SourceCoverage: completeVisibleCoverage(2, 1), RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if report.UnsupportedFacts != 0 || len(doc.TurnUnits[0].Actions) != 1 || len(doc.TurnUnits[0].Results) != 1 {
		t.Fatalf("generic tool facts dropped: doc=%+v report=%+v", doc, report)
	}
	action, gotResult := doc.TurnUnits[0].Actions[0], doc.TurnUnits[0].Results[0]
	if action.Kind != "tool_call" || action.ToolName == nil || *action.ToolName != "read" || gotResult.Kind != "tool_result" || gotResult.VerificationState != "unknown" || !strings.Contains(gotResult.Excerpt, "status=completed") {
		t.Fatalf("generic tool falsely promoted or identity lost: action=%+v result=%+v", action, gotResult)
	}
	if strings.Contains(action.Excerpt, "raw") || strings.Contains(gotResult.Excerpt, "raw") {
		t.Fatal("unbounded raw tool input/output retained")
	}
	if err := ValidateRetainedEvidence(doc, view, revisions); err != nil {
		t.Fatal(err)
	}
	forged := "write"
	action.ToolName = &forged
	if err := validateRetainedAction(action, view, map[string]memory.ObservationRevision{call.RevisionID: call}, 1, 3, map[string]struct{}{}); err == nil {
		t.Fatal("forged generic tool name authenticated")
	}
}
