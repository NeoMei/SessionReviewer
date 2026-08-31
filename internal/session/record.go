package session

import "encoding/json"

type Record struct {
	Line       int
	ByteOffset int64
	Timestamp  string
	Type       string
	Payload    json.RawMessage
	SourceHash string
}

type envelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type DecodeOptions struct {
	FromLine       int
	MaxRecordBytes int
	// SegmentBytes limits each StreamFiles member to an authenticated prefix.
	// Nil reads complete files; a non-nil slice must match the file count.
	SegmentBytes []int64
}

type DecodeSummary struct {
	Lines          int
	Records        int
	MalformedLines int
}
