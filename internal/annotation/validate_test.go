package annotation

import (
	"os"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func TestParseFrozenValidFixture(t *testing.T) {
	b, e := os.ReadFile("../../testdata/contracts/v4/agent-annotation-v1.valid.json")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Parse(b); e != nil {
		t.Fatal(e)
	}
}

func TestValidateStoreRecordRequiresProjectIdentity(t *testing.T) {
	if err := Validate(StoreRecord{SchemaVersion: 1, MinimumReaderVersion: "0.4.0"}); err == nil {
		t.Fatal("accepted missing project")
	}
}

func TestParseRejectsFrozenInvalidFixture(t *testing.T) {
	b, err := os.ReadFile("../../testdata/contracts/v4/agent-annotation-v1.invalid.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(b); err == nil {
		t.Fatal("accepted frozen invalid fixture")
	} else if got := strictjson.CodeOf(err); got != "wire_shape_invalid" {
		t.Fatalf("rejection code = %q, want wire_shape_invalid: %v", got, err)
	}
}

func TestValidateAnnotationGraphAndClosedStatuses(t *testing.T) {
	decisionID := "decision-1"
	entityID, field := "e", "f"
	base := StoreRecord{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: "p", Annotations: []Annotation{{ID: "a", ProjectID: "p", AnnotationKind: "decision_candidate", EntityID: &entityID, Field: &field, Status: "pending", Text: "candidate", GenerationID: "g", SchemaVersion: 1, AnalysisProfile: "profile", AgentRunID: "run", Dependencies: []Dependency{}, Revision: 1, CreatedAt: "now", ConfirmedEntityID: nil}}, ExtractionRuns: []Run{{RunID: "run", ProjectID: "p", Status: "completed", ExtractorVersion: "v1", PromptSchemaVersion: "v1", DependencyDigests: []string{}, CreatedAt: "now", UpdatedAt: "now"}}}
	bad := base
	bad.Annotations = append([]Annotation(nil), base.Annotations...)
	bad.Annotations[0].ConfirmedEntityID = &decisionID
	if err := Validate(bad); err == nil {
		t.Fatal("accepted confirmed entity on pending candidate")
	}
	bad = base
	bad.Annotations = append([]Annotation(nil), base.Annotations...)
	bad.Annotations[0].Status = "confirmed"
	if err := Validate(bad); err == nil {
		t.Fatal("accepted confirmed candidate without decision")
	}
	bad = base
	bad.Annotations = append(bad.Annotations, bad.Annotations[0])
	if err := Validate(bad); err == nil {
		t.Fatal("accepted duplicate annotation identity")
	}
}

func TestMilestoneConclusionAnnotationUsesGenericConfirmationAndNoDecisionFields(t *testing.T) {
	confirmed := "milestone-1"
	store := StoreRecord{
		SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: "p",
		Annotations: []Annotation{{
			ID: "summary-1", ProjectID: "p", AnnotationKind: "milestone_conclusion_candidate", Status: "confirmed", Text: "Bounded conclusion",
			GenerationID: "g", SchemaVersion: 1, AnalysisProfile: "profile", AgentRunID: "run", Dependencies: []Dependency{{Kind: "source_turn", RevisionID: "turn-1", Digest: "sha256:" + strings.Repeat("1", 64)}},
			Revision: 1, CreatedAt: "now", ConfirmedEntityID: &confirmed, TargetMilestoneID: stringPointer("milestone-1"), PromptSchemaVersion: stringPointer("summary-v1"),
		}},
		ExtractionRuns: []Run{{RunID: "run", ProjectID: "p", Status: "completed", ExtractorVersion: "v1", PromptSchemaVersion: "summary-v1", DependencyDigests: []string{"sha256:" + strings.Repeat("1", 64)}, CreatedAt: "now", UpdatedAt: "now"}},
	}
	if err := Validate(store); err != nil {
		t.Fatalf("generic milestone annotation rejected: %v", err)
	}
	store.Annotations[0].EntityID = stringPointer("decision-only")
	if err := Validate(store); err == nil {
		t.Fatal("accepted decision-only entity field on milestone conclusion")
	}
	store.Annotations[0].EntityID = nil
	store.Annotations[0].Dependencies[0].Kind = "session_view"
	if err := Validate(store); err == nil {
		t.Fatal("accepted milestone conclusion without source-turn dependency")
	}
}

func stringPointer(value string) *string { return &value }
