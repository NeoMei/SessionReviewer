package conversationchain

import (
	"encoding/json"
	"testing"

	"github.com/neomei/SessionReviewer/internal/memory"
)

func TestCanonicalCoordinatesRetainAndAuthenticateEvidence(t *testing.T) {
	revisions := []memory.ObservationRevision{
		retainedRevision("verification", "verification", "passed", 5, 'e', testTime3, map[string]string{"passed": "true"}, ""),
		retainedRevision("command", "command_started", "", 2, 'b', testTime2, nil, ""),
	}
	for i := range revisions {
		r := &revisions[i]
		r.Key.Provider, r.Ref.Provider = "opencode", "opencode"
		wire := `{"kind":"canonical","canonical":{"record":2}}`
		if i == 0 {
			wire = `{"kind":"canonical","canonical":{"record":5}}`
		}
		r.Ref.Location = memory.SourceLocation{}
		if err := json.Unmarshal([]byte(wire), &r.Ref.Location); err != nil {
			t.Fatal(err)
		}
		r.RevisionID = memory.ObservationRevisionID(*r)
	}
	view := retainedView("opencode", "session-1", "source-1", revisions)
	input := MaterializeInput{View: view, Revisions: revisions, Messages: []SourceMessage{
		retainedMessage(RoleUser, "", "start", testTime1, 1, 'a'),
		retainedMessage(RoleUser, "", "verify", testTime2, 4, 'd'),
		retainedMessage(RoleAssistant, "final_answer", "passed", testTime3, 6, 'f'),
	}, SourceCoverage: completeVisibleCoverage(6, 3), RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"}
	doc, _, err := Materialize(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.TurnUnits) != 2 || len(doc.TurnUnits[0].Actions) != 1 || len(doc.TurnUnits[1].Results) != 1 || doc.TurnUnits[0].Actions[0].SourceRef.RecordOrdinal != 2 || doc.TurnUnits[1].Results[0].SourceRef.RecordOrdinal != 5 {
		t.Fatalf("canonical evidence attached to wrong turn: %+v", doc.TurnUnits)
	}
	if err := ValidateRetainedEvidence(doc, view, revisions); err != nil {
		t.Fatal(err)
	}
	ref := doc.TurnUnits[1].Results[0].SourceRef
	ref.RecordOrdinal = 6
	if err := validateEvidenceRevisionSource(ref, revisions[0], view, 4, 7); err == nil {
		t.Fatal("mismatched canonical ordinal authenticated")
	}
	input.SourceCoverage.SourceRecords = 4
	input.Messages = input.Messages[:2]
	input.SourceCoverage.VisibleMessages, input.SourceCoverage.CapturedMessages = 2, 2
	if _, _, err := Materialize(input); err == nil {
		t.Fatal("canonical fact beyond frozen coverage accepted")
	}
}
