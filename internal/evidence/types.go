package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// CursorBoundary binds a JSONL line to the hash of its exact source record.
type CursorBoundary struct {
	Line       int    `json:"line"`
	SourceHash string `json:"source_hash,omitempty"`
}

// Packet is the stable schema-v2 envelope passed to semantic consumers.
type Packet struct {
	SchemaVersion  int            `json:"schema_version"`
	ProjectID      string         `json:"project_id"`
	SessionID      string         `json:"session_id"`
	CWD            string         `json:"cwd"`
	FromCursor     int            `json:"from_cursor"`
	ToCursor       int            `json:"to_cursor"`
	ExpectedCursor CursorBoundary `json:"expected_cursor"`
	NextCursor     CursorBoundary `json:"next_cursor"`
	HasMore        bool           `json:"has_more"`
	Events         []Item         `json:"events"`
	Warnings       []string       `json:"warnings,omitempty"`
}

// Digest hashes the deterministic JSON encoding of the exact packet.
func Digest(p Packet) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
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
