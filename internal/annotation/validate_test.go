package annotation

import (
	"os"
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
	base := StoreRecord{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: "p", Annotations: []Annotation{{ID: "a", ProjectID: "p", EntityID: "e", Field: "f", Status: "pending", Text: "candidate", GenerationID: "g", SchemaVersion: 1, AnalysisProfile: "profile", AgentRunID: "run", Dependencies: []Dependency{}, Revision: 1, CreatedAt: "now"}}, ExtractionRuns: []Run{{RunID: "run", ProjectID: "p", Status: "completed", ExtractorVersion: "v1", PromptSchemaVersion: "v1", DependencyDigests: []string{}, CreatedAt: "now", UpdatedAt: "now"}}}
	bad := base
	bad.Annotations = append([]Annotation(nil), base.Annotations...)
	bad.Annotations[0].ConfirmedDecisionID = &decisionID
	if err := Validate(bad); err == nil {
		t.Fatal("accepted confirmed decision on pending candidate")
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
