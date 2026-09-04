package sessionindex

const SortVersion = "started-at-desc-null-last-provider-session-v1"

type ProcessingState string

const (
	ProcessingComplete    ProcessingState = "complete"
	ProcessingPartial     ProcessingState = "partial"
	ProcessingError       ProcessingState = "error"
	ProcessingUnprocessed ProcessingState = "unprocessed"
)

type SessionKey struct {
	Provider  string
	SessionID string
}

type Coverage struct {
	Seen        uint64 `json:"seen" required:"true"`
	Indexed     uint64 `json:"indexed" required:"true"`
	Collapsed   uint64 `json:"collapsed" required:"true"`
	Unprojected uint64 `json:"unprojected" required:"true"`
	Undecodable uint64 `json:"undecodable" required:"true"`
	Truncated   uint64 `json:"truncated" required:"true"`
}

type IndexCoverage struct {
	Total             uint64 `json:"total" required:"true"`
	Complete          uint64 `json:"complete" required:"true"`
	Partial           uint64 `json:"partial" required:"true"`
	Error             uint64 `json:"error" required:"true"`
	Unprocessed       uint64 `json:"unprocessed" required:"true"`
	SourceAvailable   uint64 `json:"source_available" required:"true"`
	SourceUnavailable uint64 `json:"source_unavailable" required:"true"`
	StartedAtKnown    uint64 `json:"started_at_known" required:"true"`
	EndedAtKnown      uint64 `json:"ended_at_known" required:"true"`
	UsageKnown        uint64 `json:"usage_known" required:"true"`
}

type FactCounts struct {
	FileChange   uint64 `json:"file_change" required:"true"`
	Command      uint64 `json:"command" required:"true"`
	Verification uint64 `json:"verification" required:"true"`
	Error        uint64 `json:"error" required:"true"`
	Artifact     uint64 `json:"artifact" required:"true"`
}

type Entry struct {
	Provider                   string          `json:"provider" required:"true"`
	SessionID                  string          `json:"session_id" required:"true"`
	ProcessingState            ProcessingState `json:"processing_state" required:"true"`
	StateReasonCodes           []string        `json:"state_reason_codes" required:"true"`
	SourceAvailability         string          `json:"source_availability" required:"true"`
	SourceTerminalState        *string         `json:"source_terminal_state" required:"true" nullable:"true"`
	StartedAt                  string          `json:"started_at" required:"true"`
	EndedAt                    string          `json:"ended_at" required:"true"`
	DurationMS                 *uint64         `json:"duration_ms" required:"true" nullable:"true"`
	WarningCount               uint64          `json:"warning_count" required:"true"`
	RecordCount                *uint64         `json:"record_count" required:"true" nullable:"true"`
	IndexedEventCount          uint64          `json:"indexed_event_count" required:"true"`
	Coverage                   Coverage        `json:"coverage" required:"true"`
	FactCounts                 FactCounts      `json:"fact_counts" required:"true"`
	SessionViewDigest          *string         `json:"session_view_digest" required:"true" nullable:"true"`
	UsageRecordDigest          *string         `json:"usage_record_digest" required:"true" nullable:"true"`
	SummaryDigest              *string         `json:"summary_digest" required:"true" nullable:"true"`
	LastSeenGenerationID       *string         `json:"last_seen_generation_id" required:"true" nullable:"true"`
	LastSuccessfulGenerationID *string         `json:"last_successful_generation_id" required:"true" nullable:"true"`
}

type Document struct {
	SchemaVersion        int           `json:"schema_version" required:"true"`
	MinimumReaderVersion string        `json:"minimum_reader_version" required:"true"`
	Digest               string        `json:"digest" required:"true"`
	ProjectID            string        `json:"project_id" required:"true"`
	GenerationID         string        `json:"generation_id" required:"true"`
	ProjectViewDigest    string        `json:"project_view_digest" required:"true"`
	GeneratedAt          string        `json:"generated_at" required:"true"`
	SortVersion          string        `json:"sort_version" required:"true"`
	Coverage             IndexCoverage `json:"coverage" required:"true"`
	Sessions             []Entry       `json:"sessions" required:"true"`
}
