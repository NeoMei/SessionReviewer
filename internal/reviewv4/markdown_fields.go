package reviewv4

type FieldKey struct {
	Entity string
	Name   string
}

type FieldSpec struct {
	EntityKind string `json:"entity_kind"`
	Name       string `json:"name"`
	Document   string `json:"document"`
}

var markdownFieldSpecs = []FieldSpec{
	{"project-overview", "goal", "review"},
	{"project-overview", "stage", "review"},
	{"project-overview", "status", "review"},
	{"project-overview", "next_action", "review"},
	{"project-overview", "last_verification", "review"},
	{"decision", "title", "review"},
	{"decision", "rationale", "review"},
	{"decision", "impact", "review"},
	{"decision", "reevaluate_when", "review"},
	{"risk", "title", "review"},
	{"risk", "detail", "review"},
	{"risk", "status", "review"},
	{"open-loop", "title", "review"},
	{"open-loop", "question", "review"},
	{"open-loop", "next_experiment", "review"},
	{"open-loop", "completion_criterion", "review"},
	{"open-loop", "status", "review"},
	{"problem", "question", "review"},
	{"problem", "completion_criterion", "review"},
	{"problem", "current_conclusion", "review"},
	{"milestone", "title", "history"},
	{"milestone", "summary", "history"},
	{"milestone", "conclusion", "history"},
	{"milestone", "impact_and_follow_up", "history"},
}

func MarkdownFieldSpecs() []FieldSpec {
	return append([]FieldSpec(nil), markdownFieldSpecs...)
}
