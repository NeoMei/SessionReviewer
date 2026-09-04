package conversationchain

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type AnswerState string

const (
	AnswerNone     AnswerState = "no_answer"
	AnswerAnswered AnswerState = "answered"
	AnswerPartial  AnswerState = "partial"
)

type SourceRef struct {
	Provider       string `json:"provider" required:"true"`
	SessionID      string `json:"session_id" required:"true"`
	SourceIdentity string `json:"source_identity" required:"true"`
	RecordOrdinal  uint64 `json:"record_ordinal" required:"true"`
	SourceHash     string `json:"source_hash" required:"true"`
}

type Message struct {
	Role           Role      `json:"role" required:"true"`
	RevisionID     string    `json:"revision_id" required:"true"`
	SourceRef      SourceRef `json:"source_ref" required:"true"`
	OccurredAt     string    `json:"occurred_at" required:"true"`
	VisibleExcerpt string    `json:"visible_excerpt" required:"true"`
	Truncated      bool      `json:"truncated" required:"true"`
}

type Action struct {
	RevisionID string    `json:"revision_id" required:"true"`
	SourceRef  SourceRef `json:"source_ref" required:"true"`
	Kind       string    `json:"kind" required:"true"`
	ToolName   *string   `json:"tool_name" required:"true" nullable:"true"`
	Excerpt    string    `json:"excerpt" required:"true"`
}

type Result struct {
	RevisionID        string    `json:"revision_id" required:"true"`
	SourceRef         SourceRef `json:"source_ref" required:"true"`
	Kind              string    `json:"kind" required:"true"`
	VerificationState string    `json:"verification_state" required:"true"`
	Excerpt           string    `json:"excerpt" required:"true"`
}

type TurnUnit struct {
	TurnUnitID        string      `json:"turn_unit_id" required:"true"`
	Ordinal           uint64      `json:"ordinal" required:"true"`
	StartedAt         string      `json:"started_at" required:"true"`
	EndedAt           *string     `json:"ended_at" required:"true" nullable:"true"`
	UserMessage       Message     `json:"user_message" required:"true"`
	AssistantMessages []Message   `json:"assistant_messages" required:"true"`
	Actions           []Action    `json:"actions" required:"true"`
	Results           []Result    `json:"results" required:"true"`
	AnswerState       AnswerState `json:"answer_state" required:"true"`
}

type Coverage struct {
	SourceMessages    uint64 `json:"source_messages" required:"true"`
	CapturedMessages  uint64 `json:"captured_messages" required:"true"`
	TurnUnits         uint64 `json:"turn_units" required:"true"`
	UnansweredUnits   uint64 `json:"unanswered_units" required:"true"`
	TruncatedMessages uint64 `json:"truncated_messages" required:"true"`
}

type Document struct {
	SchemaVersion           int        `json:"schema_version" required:"true"`
	MinimumReaderVersion    string     `json:"minimum_reader_version" required:"true"`
	Digest                  string     `json:"digest" required:"true"`
	ProjectID               string     `json:"project_id" required:"true"`
	Provider                string     `json:"provider" required:"true"`
	SessionID               string     `json:"session_id" required:"true"`
	SessionViewDigest       string     `json:"session_view_digest" required:"true"`
	DependencyDigest        string     `json:"dependency_digest" required:"true"`
	SegmentationRuleVersion string     `json:"segmentation_rule_version" required:"true"`
	Coverage                Coverage   `json:"coverage" required:"true"`
	TurnUnits               []TurnUnit `json:"turn_units" required:"true"`
}
