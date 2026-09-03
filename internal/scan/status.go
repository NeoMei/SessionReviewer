package scan

type State string

const (
	Completed           State = "completed"
	CompletedWithIssues State = "completed_with_issues"
	Failed              State = "failed"
)

type Result struct {
	SchemaVersion     int    `json:"schema_version"`
	ProjectID         string `json:"project_id"`
	GenerationID      string `json:"generation_id"`
	State             State  `json:"state"`
	SourceSessions    int    `json:"source_sessions"`
	IndexedSessions   int    `json:"indexed_sessions"`
	TerminalSessions  int    `json:"terminal_sessions"`
	IssueSessions     int    `json:"issue_sessions"`
	ProjectViewDigest string `json:"project_view_digest"`
	ReviewRunTokens   int64  `json:"review_run_tokens"`
	Prepared          bool   `json:"prepared"`
}

// Progress is a bounded extraction snapshot. SourceSessions remains zero while
// global provider candidates are decoded, then becomes the fixed count of
// authenticated target-project sources before they are materialized.
type Progress struct {
	SourceSessions   int
	TerminalSessions int
	IndexedSessions  int
	IssueSessions    int
}
