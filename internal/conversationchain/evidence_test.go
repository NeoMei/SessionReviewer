package conversationchain

import (
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/memory"
)

func TestValidateRetainedEvidenceAuthenticatesProjectionAndTurnInterval(t *testing.T) {
	verification := retainedRevision("verification", "verification", "passed", 3, 'e', testTime1, map[string]string{"component": "unit", "passed": "12", "failed": "0"}, "ignored")
	view := retainedView("codex", "session-1", "source-1", []memory.ObservationRevision{verification})
	messages := []SourceMessage{
		retainedMessage(RoleUser, "", "run tests", testTime1, 2, 'c'),
		retainedMessage(RoleAssistant, "final_answer", "passed", testTime2, 4, 'a'),
	}
	document, _, err := Materialize(MaterializeInput{View: view, Messages: messages, Revisions: []memory.ObservationRevision{verification}, SourceCoverage: completeVisibleCoverage(4, 2), RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRetainedEvidence(document, view, []memory.ObservationRevision{verification}); err != nil {
		t.Fatalf("valid retained evidence rejected: %v", err)
	}

	mutations := []struct {
		name string
		edit func(*Document)
	}{
		{name: "passed label on failed fact", edit: func(doc *Document) { doc.TurnUnits[0].Results[0].VerificationState = "failed" }},
		{name: "kind", edit: func(doc *Document) { doc.TurnUnits[0].Results[0].Kind = "deployment" }},
		{name: "source ordinal", edit: func(doc *Document) { doc.TurnUnits[0].Results[0].SourceRef.RecordOrdinal = 4 }},
		{name: "source hash", edit: func(doc *Document) { doc.TurnUnits[0].Results[0].SourceRef.SourceHash = strings.Repeat("f", 64) }},
		{name: "excerpt", edit: func(doc *Document) { doc.TurnUnits[0].Results[0].Excerpt = "component=other" }},
		{name: "action result role", edit: func(doc *Document) {
			result := doc.TurnUnits[0].Results[0]
			doc.TurnUnits[0].Results = nil
			doc.TurnUnits[0].Actions = []Action{{RevisionID: result.RevisionID, SourceRef: result.SourceRef, Kind: result.Kind, Excerpt: result.Excerpt}}
		}},
		{name: "message source identity", edit: func(doc *Document) { doc.TurnUnits[0].UserMessage.SourceRef.SourceIdentity = "foreign" }},
		{name: "duplicate evidence", edit: func(doc *Document) {
			doc.TurnUnits[0].Results = append(doc.TurnUnits[0].Results, doc.TurnUnits[0].Results[0])
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := document
			changed.TurnUnits = append([]TurnUnit(nil), document.TurnUnits...)
			changed.TurnUnits[0].Results = append([]Result(nil), document.TurnUnits[0].Results...)
			changed.TurnUnits[0].Actions = append([]Action(nil), document.TurnUnits[0].Actions...)
			test.edit(&changed)
			changed.Digest = CanonicalDigest(changed)
			if err := ValidateRetainedEvidence(changed, view, []memory.ObservationRevision{verification}); err == nil {
				t.Fatal("semantically forged retained evidence accepted")
			}
		})
	}
}

func TestValidateRetainedEvidenceRejectsFactOutsideCorrespondingUserTurn(t *testing.T) {
	verification := retainedRevision("verification", "verification", "passed", 5, 'e', testTime2, map[string]string{"passed": "1", "failed": "0"}, "")
	view := retainedView("codex", "session-1", "source-1", []memory.ObservationRevision{verification})
	messages := []SourceMessage{
		retainedMessage(RoleUser, "", "first", testTime1, 1, 'a'),
		retainedMessage(RoleAssistant, "final_answer", "first answer", testTime1, 2, 'b'),
		retainedMessage(RoleUser, "", "second", testTime2, 4, 'c'),
		retainedMessage(RoleAssistant, "final_answer", "second answer", testTime2, 6, 'd'),
	}
	document, _, err := Materialize(MaterializeInput{View: view, Messages: messages, Revisions: []memory.ObservationRevision{verification}, SourceCoverage: completeVisibleCoverage(6, 4), RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"})
	if err != nil {
		t.Fatal(err)
	}
	document.TurnUnits[0].Results = append(document.TurnUnits[0].Results, document.TurnUnits[1].Results[0])
	document.TurnUnits[1].Results = nil
	document.Digest = CanonicalDigest(document)
	if err := ValidateRetainedEvidence(document, view, []memory.ObservationRevision{verification}); err == nil {
		t.Fatal("evidence outside its source-ordinal turn interval accepted")
	}
}

func TestValidateRetainedEvidenceRejectsFailedFactRelabeledPassedAfterCanonicalRehash(t *testing.T) {
	failed := retainedRevision("verification", "verification", "failed", 3, 'e', testTime1, map[string]string{"component": "unit", "passed": "0", "failed": "1"}, "")
	view := retainedView("codex", "session-1", "source-1", []memory.ObservationRevision{failed})
	document, _, err := Materialize(MaterializeInput{
		View: view,
		Messages: []SourceMessage{
			retainedMessage(RoleUser, "", "run tests", testTime1, 2, 'c'),
			retainedMessage(RoleAssistant, "final_answer", "failed", testTime2, 4, 'a'),
		},
		Revisions: []memory.ObservationRevision{failed}, SourceCoverage: completeVisibleCoverage(4, 2),
		RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	document.TurnUnits[0].Results[0].VerificationState = "passed"
	document.Digest = CanonicalDigest(document)
	body, err := Render(document)
	if err != nil {
		t.Fatalf("forged document was not structurally valid: %v", err)
	}
	forged, err := Parse(body)
	if err != nil {
		t.Fatalf("forged document was not canonical: %v", err)
	}
	if err := ValidateRetainedEvidence(forged, view, []memory.ObservationRevision{failed}); err == nil {
		t.Fatal("active failed fact authenticated a retained passed result")
	}
}

func TestValidateRetainedEvidenceAuthenticatesDependencyProof(t *testing.T) {
	verification := retainedRevision("verification", "verification", "passed", 3, 'e', testTime1, map[string]string{"passed": "1", "failed": "0"}, "")
	view := retainedView("codex", "session-1", "source-1", []memory.ObservationRevision{verification})
	document, _, err := Materialize(MaterializeInput{
		View: view,
		Messages: []SourceMessage{
			retainedMessage(RoleUser, "", "run tests", testTime1, 2, 'c'),
			retainedMessage(RoleAssistant, "final_answer", "passed", testTime2, 4, 'a'),
		},
		Revisions: []memory.ObservationRevision{verification}, SourceCoverage: completeVisibleCoverage(4, 2),
		RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name string
		edit func(*Document)
	}{
		{"missing proof", func(doc *Document) { doc.DependencyProofV1 = nil }},
		{"arbitrary dependency digest", func(doc *Document) { doc.DependencyDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"view", func(doc *Document) { doc.DependencyProofV1.SessionViewDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"source record", func(doc *Document) { doc.DependencyProofV1.SourceRecordDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"active revisions", func(doc *Document) { doc.DependencyProofV1.ActiveRevisionIDs = []string{} }},
		{"rule", func(doc *Document) { doc.DependencyProofV1.RuleVersion = "visible-turn-v2" }},
		{"visible message missing from proof", func(doc *Document) { doc.DependencyProofV1.VisibleRecords = doc.DependencyProofV1.VisibleRecords[1:] }},
		{"visible message hash mismatch", func(doc *Document) { doc.DependencyProofV1.VisibleRecords[0].SourceHash = strings.Repeat("f", 64) }},
		{"visible message source identity mismatch", func(doc *Document) { doc.TurnUnits[0].UserMessage.SourceRef.SourceIdentity = "foreign" }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneDocumentWithProof(document)
			test.edit(&changed)
			if test.name != "arbitrary dependency digest" && changed.DependencyProofV1 != nil {
				changed.DependencyDigest = dependencyProofDigest(*changed.DependencyProofV1)
			}
			changed.Digest = CanonicalDigest(changed)
			body, renderErr := Render(changed)
			localMismatch := test.name == "arbitrary dependency digest" || test.name == "view" || test.name == "rule" ||
				test.name == "visible message missing from proof" || test.name == "visible message hash mismatch"
			if localMismatch {
				if renderErr == nil {
					t.Fatal("document-local dependency proof mismatch remained structurally valid")
				}
				return
			}
			if renderErr != nil {
				t.Fatalf("forged document was not structurally valid: %v", renderErr)
			}
			forged, parseErr := Parse(body)
			if parseErr != nil {
				t.Fatalf("forged document was not canonical: %v", parseErr)
			}
			if err := ValidateRetainedEvidence(forged, view, []memory.ObservationRevision{verification}); err == nil {
				t.Fatal("inconsistent dependency proof authenticated")
			}
		})
	}
}

func cloneDocumentWithProof(document Document) Document {
	changed := document
	changed.TurnUnits = append([]TurnUnit(nil), document.TurnUnits...)
	if document.DependencyProofV1 != nil {
		proof := *document.DependencyProofV1
		proof.VisibleRecords = append([]DependencyRecordProofV1(nil), proof.VisibleRecords...)
		proof.ActiveRevisionIDs = append([]string(nil), proof.ActiveRevisionIDs...)
		changed.DependencyProofV1 = &proof
	}
	return changed
}
