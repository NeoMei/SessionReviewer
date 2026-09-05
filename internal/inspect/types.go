package inspect

type Coverage struct {
	Seen        uint64 `json:"seen" required:"true"`
	Indexed     uint64 `json:"indexed" required:"true"`
	Collapsed   uint64 `json:"collapsed" required:"true"`
	Unprojected uint64 `json:"unprojected" required:"true"`
	Undecodable uint64 `json:"undecodable" required:"true"`
	Truncated   uint64 `json:"truncated" required:"true"`
}

type Entry struct {
	OccurredAt        string   `json:"occurred_at" required:"true"`
	Sequence          uint64   `json:"sequence" required:"true"`
	RevisionID        string   `json:"revision_id" required:"true"`
	Text              string   `json:"text" required:"true"`
	SourceRevisionIDs []string `json:"source_revision_ids" required:"true"`
}

type ErrorEntry struct {
	Code              string   `json:"code" required:"true"`
	OccurredAt        string   `json:"occurred_at" required:"true"`
	Sequence          uint64   `json:"sequence" required:"true"`
	RevisionID        string   `json:"revision_id" required:"true"`
	Text              string   `json:"text" required:"true"`
	SourceRevisionIDs []string `json:"source_revision_ids" required:"true"`
}

type Block struct {
	Total    uint64   `json:"total" required:"true"`
	Shown    uint64   `json:"shown" required:"true"`
	Omitted  uint64   `json:"omitted" required:"true"`
	Coverage Coverage `json:"coverage" required:"true"`
	Items    []Entry  `json:"items" required:"true"`
}

type ErrorBlock struct {
	Total    uint64       `json:"total" required:"true"`
	Shown    uint64       `json:"shown" required:"true"`
	Omitted  uint64       `json:"omitted" required:"true"`
	Coverage Coverage     `json:"coverage" required:"true"`
	Items    []ErrorEntry `json:"items" required:"true"`
}

type Rules struct {
	RuleID            string   `json:"rule_id" required:"true"`
	RuleVersion       string   `json:"rule_version" required:"true"`
	DependencyDigests []string `json:"dependency_digests" required:"true"`
}

type SessionSummary struct {
	SchemaVersion        int        `json:"schema_version" required:"true"`
	MinimumReaderVersion string     `json:"minimum_reader_version" required:"true"`
	ProjectID            string     `json:"project_id" required:"true"`
	Provider             string     `json:"provider" required:"true"`
	SessionID            string     `json:"session_id" required:"true"`
	GenerationID         string     `json:"generation_id" required:"true"`
	SessionViewDigest    string     `json:"session_view_digest" required:"true"`
	PhaseBoundaries      Block      `json:"phase_boundaries" required:"true"`
	KeyOperations        Block      `json:"key_operations" required:"true"`
	VerificationResults  Block      `json:"verification_results" required:"true"`
	Errors               ErrorBlock `json:"errors" required:"true"`
	UnresolvedQuestions  Block      `json:"unresolved_questions" required:"true"`
	Rules                Rules      `json:"rules" required:"true"`
	Coverage             Coverage   `json:"coverage" required:"true"`
}

type EventItem struct {
	Kind       string `json:"kind" required:"true"`
	Excerpt    string `json:"excerpt" required:"true"`
	RevisionID string `json:"revision_id" required:"true"`
	Sequence   uint64 `json:"sequence" required:"true"`
	OccurredAt string `json:"occurred_at" required:"true"`
}

type SessionEventPage struct {
	SchemaVersion        int         `json:"schema_version" required:"true"`
	MinimumReaderVersion string      `json:"minimum_reader_version" required:"true"`
	ProjectID            string      `json:"project_id" required:"true"`
	Provider             string      `json:"provider" required:"true"`
	SessionID            string      `json:"session_id" required:"true"`
	GenerationID         string      `json:"generation_id" required:"true"`
	SessionViewDigest    string      `json:"session_view_digest" required:"true"`
	Total                uint64      `json:"total" required:"true"`
	RangeStart           uint64      `json:"range_start" required:"true"`
	RangeEnd             uint64      `json:"range_end" required:"true"`
	Items                []EventItem `json:"items" required:"true"`
	PreviousCursor       *string     `json:"previous_cursor" required:"true" nullable:"true"`
	NextCursor           *string     `json:"next_cursor" required:"true" nullable:"true"`
	FirstCursor          *string     `json:"first_cursor" required:"true" nullable:"true"`
	LastCursor           *string     `json:"last_cursor" required:"true" nullable:"true"`
	Coverage             Coverage    `json:"coverage" required:"true"`
}
