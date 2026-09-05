package reviewv4

import (
	"errors"
	"reflect"
	"testing"

	"github.com/neomei/SessionReviewer/internal/strictjson"
)

type markdownFieldCatalog struct {
	SchemaVersion    int         `json:"schema_version" required:"true"`
	Format           string      `json:"format" required:"true"`
	Fields           []FieldSpec `json:"fields" required:"true"`
	GeneratedRegions []FieldSpec `json:"generated_regions" required:"true"`
}

func TestMarkdownFieldCatalogHasOneOwner(t *testing.T) {
	want := []FieldSpec{
		{"project-overview", "goal", "review"}, {"project-overview", "stage", "review"},
		{"project-overview", "status", "review"}, {"project-overview", "next_action", "review"},
		{"project-overview", "last_verification", "review"},
		{"decision", "title", "review"}, {"decision", "rationale", "review"},
		{"decision", "impact", "review"}, {"decision", "reevaluate_when", "review"},
		{"risk", "title", "review"}, {"risk", "detail", "review"}, {"risk", "status", "review"},
		{"open-loop", "title", "review"}, {"open-loop", "question", "review"},
		{"open-loop", "next_experiment", "review"}, {"open-loop", "completion_criterion", "review"},
		{"open-loop", "status", "review"},
		{"problem", "question", "review"}, {"problem", "completion_criterion", "review"},
		{"problem", "current_conclusion", "review"},
		{"milestone", "title", "history"}, {"milestone", "summary", "history"},
		{"milestone", "conclusion", "history"}, {"milestone", "impact_and_follow_up", "history"},
	}
	got := MarkdownFieldSpecs()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("field catalog mismatch\ngot:  %+v\nwant: %+v", got, want)
	}
	seen := map[string]bool{}
	for _, field := range got {
		key := field.EntityKind + "/" + field.Name
		if seen[key] || (field.Document != "review" && field.Document != "history") {
			t.Fatalf("invalid field ownership: %+v", field)
		}
		seen[key] = true
	}
	if len(seen) != 24 {
		t.Fatalf("fields=%d, want 24", len(seen))
	}
	got[0].Name = "mutated"
	if reflect.DeepEqual(got, MarkdownFieldSpecs()) {
		t.Fatal("catalog did not return a defensive copy")
	}
}

func TestMarkdownFieldCatalogMatchesSharedArtifactAndGeneratedRegions(t *testing.T) {
	var catalog markdownFieldCatalog
	if err := strictjson.Decode(mustRead(t, "../../schemas/review-markdown-v1.fields.json"), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.SchemaVersion != 1 || catalog.Format != "review-markdown-v1" {
		t.Fatalf("invalid shared catalog identity: %+v", catalog)
	}
	if !reflect.DeepEqual(catalog.Fields, MarkdownFieldSpecs()) {
		t.Fatalf("shared catalog differs from Go registry: %+v", catalog.Fields)
	}
	wantRegions := []FieldSpec{
		{"project-overview", "problem-tree", "review"},
		{"project-overview", "pinned-decisions", "review"},
		{"project-overview", "recent-milestones", "review"},
		{"milestone", "evidence", "history"},
	}
	if !reflect.DeepEqual(catalog.GeneratedRegions, wantRegions) {
		t.Fatalf("generated regions mismatch: %+v", catalog.GeneratedRegions)
	}
}

func TestMarkdownErrorIsClosedAndDoesNotEchoCause(t *testing.T) {
	secret := errors.New("secret-body")
	err := &MarkdownError{Code: "markdown_format_invalid", Relative: "项目回顾.md", Entity: "decision:d:1", Field: "title", Cause: secret}
	if got := err.Error(); got != "markdown_format_invalid" {
		t.Fatalf("Error() = %q", got)
	}
	if !errors.Is(err, secret) {
		t.Fatal("MarkdownError does not unwrap cause")
	}
	if got := MarkdownCodeOf(err); got != "markdown_format_invalid" {
		t.Fatalf("MarkdownCodeOf() = %q", got)
	}
	if got := MarkdownCodeOf(errors.New("other")); got != "" {
		t.Fatalf("non-Markdown code = %q", got)
	}
	if got := MarkdownCodeOf(&MarkdownError{Code: "unregistered"}); got != "" {
		t.Fatalf("unregistered Markdown code = %q", got)
	}
}
