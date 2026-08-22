package evidence

// Packet is the stable schema-v1 envelope passed to semantic consumers.
type Packet struct {
	SchemaVersion int      `json:"schema_version"`
	ProjectID     string   `json:"project_id,omitempty"`
	SessionID     string   `json:"session_id"`
	CWD           string   `json:"cwd"`
	FromCursor    int      `json:"from_cursor"`
	ToCursor      int      `json:"to_cursor"`
	HasMore       bool     `json:"has_more"`
	Events        []Item   `json:"events"`
	Warnings      []string `json:"warnings,omitempty"`
}

// Item is one allowlisted, redacted evidence event with source provenance.
type Item struct {
	ID         string `json:"id"`
	ItemID     string `json:"item_id,omitempty"`
	Timestamp  string `json:"timestamp"`
	JSONLLine  int    `json:"jsonl_line"`
	SourceHash string `json:"source_hash"`
	Kind       string `json:"kind"`
	Role       string `json:"role,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	Summary    string `json:"summary"`
}
