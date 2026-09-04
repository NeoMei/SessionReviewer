package reviewv4

import (
	"github.com/neomei/SessionReviewer/internal/pricing"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

type SessionKey struct{ Provider, SessionID string }
type ProcessingState string

const (
	ProcessingComplete    ProcessingState = "complete"
	ProcessingPartial     ProcessingState = "partial"
	ProcessingError       ProcessingState = "error"
	ProcessingUnprocessed ProcessingState = "unprocessed"
)

type DecisionStatus string

const (
	DecisionActive     DecisionStatus = "active"
	DecisionSuperseded DecisionStatus = "superseded"
	DecisionArchived   DecisionStatus = "archived"
)

type CandidateStatus string

const (
	CandidatePending     CandidateStatus = "pending"
	CandidateConfirmed   CandidateStatus = "confirmed"
	CandidateIgnored     CandidateStatus = "ignored"
	CandidateNotDecision CandidateStatus = "not_decision"
	CandidateStale       CandidateStatus = "stale"
)

type PriceStatus = pricing.PriceStatus

const (
	PricePending          = pricing.PricePending
	PriceCurrent          = pricing.PriceCurrent
	PricePromotion        = pricing.PricePromotion
	PriceStaleEstimate    = pricing.PriceStaleEstimate
	PriceManualSupplement = pricing.PriceManualSupplement
	PriceAmbiguous        = pricing.PriceAmbiguous
	PriceLegacyUnverified = pricing.PriceLegacyUnverified
	PriceSuperseded       = pricing.PriceSuperseded
)

type CurrentState struct {
	Goal             string `json:"goal" required:"true"`
	Stage            string `json:"stage" required:"true"`
	Status           string `json:"status" required:"true"`
	NextAction       string `json:"next_action" required:"true"`
	LastVerification string `json:"last_verification" required:"true"`
}
type Timeline struct {
	ID           string   `json:"id" required:"true"`
	GenerationID string   `json:"generation_id" required:"true"`
	OccurredAt   string   `json:"occurred_at" required:"true"`
	Kind         string   `json:"kind" required:"true"`
	Title        string   `json:"title" required:"true"`
	Summary      string   `json:"summary" required:"true"`
	DecisionIDs  []string `json:"decision_ids" required:"true"`
}
type SessionRef struct {
	Provider  string `json:"provider" required:"true"`
	SessionID string `json:"session_id" required:"true"`
}
type Decision struct {
	ID             string         `json:"id" required:"true"`
	Kind           string         `json:"kind" required:"true"`
	OccurredAt     string         `json:"occurred_at" required:"true"`
	Title          string         `json:"title" required:"true"`
	Rationale      string         `json:"rationale" required:"true"`
	Impact         string         `json:"impact" required:"true"`
	Status         DecisionStatus `json:"status" required:"true"`
	ReevaluateWhen string         `json:"reevaluate_when" required:"true"`
	Supersedes     []string       `json:"supersedes" required:"true"`
	MilestoneIDs   []string       `json:"milestone_ids" required:"true"`
	SessionRefs    []SessionRef   `json:"session_refs" required:"true"`
	Provenance     string         `json:"provenance" required:"true"`
	Pinned         bool           `json:"pinned" required:"true"`
	Revision       int            `json:"revision" required:"true"`
}
type Risk struct {
	ID     string `json:"id" required:"true"`
	Title  string `json:"title" required:"true"`
	Status string `json:"status" required:"true"`
	Detail string `json:"detail" required:"true"`
}
type OpenLoop struct {
	ID                  string `json:"id" required:"true"`
	Title               string `json:"title" required:"true"`
	Status              string `json:"status" required:"true"`
	Question            string `json:"question" required:"true"`
	NextExperiment      string `json:"next_experiment" required:"true"`
	CompletionCriterion string `json:"completion_criterion" required:"true"`
}
type Patch struct {
	EntityID          string   `json:"entity_id" required:"true"`
	Field             string   `json:"field" required:"true"`
	Operation         string   `json:"operation" required:"true"`
	Value             *string  `json:"value,omitempty"`
	Values            []string `json:"values,omitempty"`
	BaseGeneratedHash string   `json:"base_generated_hash" required:"true"`
}
type Baseline struct {
	GenerationID  string   `json:"generation_id" required:"true"`
	EntityID      string   `json:"entity_id" required:"true"`
	Field         string   `json:"field" required:"true"`
	Kind          string   `json:"kind" required:"true"`
	Value         *string  `json:"value,omitempty"`
	Values        []string `json:"values,omitempty"`
	GeneratedHash string   `json:"generated_hash" required:"true"`
}
type Presentation struct {
	SchemaVersion        int          `json:"schema_version" required:"true"`
	MinimumReaderVersion string       `json:"minimum_reader_version" required:"true"`
	MinimumWriterVersion string       `json:"minimum_writer_version" required:"true"`
	ProjectID            string       `json:"project_id" required:"true"`
	GenerationID         string       `json:"generation_id" required:"true"`
	ProjectViewDigest    string       `json:"project_view_digest" required:"true"`
	Revision             int          `json:"revision" required:"true"`
	CurrentState         CurrentState `json:"current_state" required:"true"`
	Timeline             []Timeline   `json:"timeline" required:"true"`
	Decisions            []Decision   `json:"decisions" required:"true"`
	Risks                []Risk       `json:"risks" required:"true"`
	OpenLoops            []OpenLoop   `json:"open_loops" required:"true"`
	HumanPatches         []Patch      `json:"human_patches" required:"true"`
	OrphanPatches        []Patch      `json:"orphan_patches" required:"true"`
	GeneratedBaselines   []Baseline   `json:"generated_baselines" required:"true"`
}
type Accounting struct {
	TotalDurationMS uint64   `json:"total_duration_ms" required:"true"`
	TotalTokens     uint64   `json:"total_tokens" required:"true"`
	TotalCostUSD    *float64 `json:"total_cost_usd" required:"true" nullable:"true"`
	Models          []Model  `json:"models" required:"true"`
}
type Model struct {
	Model        string   `json:"model" required:"true"`
	TotalTokens  uint64   `json:"total_tokens" required:"true"`
	TotalCostUSD *float64 `json:"total_cost_usd" required:"true" nullable:"true"`
}
type LedgerSession struct {
	Provider           string          `json:"provider" required:"true"`
	SessionID          string          `json:"session_id" required:"true"`
	ProcessingState    ProcessingState `json:"processing_state" required:"true"`
	SourceAvailability string          `json:"source_availability" required:"true"`
	SessionViewDigest  *string         `json:"session_view_digest" required:"true" nullable:"true"`
	UsageRecordDigest  *string         `json:"usage_record_digest" required:"true" nullable:"true"`
}
type SyncHashes struct {
	ReviewSHA256       string `json:"review_sha256" required:"true"`
	HistorySHA256      string `json:"history_sha256" required:"true"`
	LedgerSHA256       string `json:"ledger_sha256" required:"true"`
	SessionIndexDigest string `json:"session_index_digest" required:"true"`
}
type MachineLedger struct {
	SchemaVersion             int                `json:"schema_version" required:"true"`
	MinimumReaderVersion      string             `json:"minimum_reader_version" required:"true"`
	MinimumWriterVersion      string             `json:"minimum_writer_version" required:"true"`
	ProjectID                 string             `json:"project_id" required:"true"`
	GenerationID              string             `json:"generation_id" required:"true"`
	ProjectViewDigest         string             `json:"project_view_digest" required:"true"`
	AcceptedRevision          int                `json:"accepted_revision" required:"true"`
	ReviewSHA256              string             `json:"review_sha256" required:"true"`
	HistorySHA256             string             `json:"history_sha256" required:"true"`
	Accounting                Accounting         `json:"accounting" required:"true"`
	Sessions                  []LedgerSession    `json:"sessions" required:"true"`
	HumanPatches              []Patch            `json:"human_patches" required:"true"`
	OrphanPatches             []Patch            `json:"orphan_patches" required:"true"`
	GeneratedBaselines        []Baseline         `json:"generated_baselines" required:"true"`
	PricingSnapshots          []pricing.Snapshot `json:"pricing_snapshots" required:"true"`
	CurrentPricingSnapshotIDs []string           `json:"current_pricing_snapshot_ids" required:"true"`
	SyncHashes                SyncHashes         `json:"sync_hashes" required:"true"`
}
type Accepted struct {
	Review       Presentation
	History      []byte
	Ledger       MachineLedger
	SessionIndex sessionindex.Document
}
