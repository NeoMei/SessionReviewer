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
}

type DecodeSummary struct {
	Lines          int
	Records        int
	MalformedLines int
}
