package reviewv2

import (
	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/ledger"
)

const (
	SchemaVersion = 2

	ReviewRelativePath        = "docs/session-review/项目回顾.md"
	HistoryRelativePath       = "docs/session-review/项目历史.md"
	MachineLedgerRelativePath = "docs/session-review/.session-reviewer/ledger.json"

	MaxDocumentBytes      = 4 << 20
	MaxMachineLedgerBytes = 16 << 20
)

type Risk struct {
	ID     string
	Title  string
	Status string
	Detail string
}

type Decision struct {
	ID         string
	OccurredAt string
	Title      string
	Rationale  string
	Impact     string
	Status     string
}

type Event struct {
	ID          string
	OccurredAt  string
	Kind        string
	Title       string
	Meaning     string
	Summary     string
	Why         string
	Next        string
	Changes     []string
	Results     []string
	DecisionIDs []string
}

type Review struct {
	ProjectID        string
	Revision         int
	Name             string
	Goal             string
	Stage            string
	Status           string
	NextAction       string
	LastVerification string
	Risks            []Risk
	Decisions        []Decision
}

type MachineLedger struct {
	SchemaVersion       int                             `json:"schema_version"`
	ProjectID           string                          `json:"project_id"`
	AcceptedRevision    int                             `json:"accepted_revision"`
	ReviewSHA256        string                          `json:"review_sha256"`
	HistorySHA256       string                          `json:"history_sha256"`
	LastSuccessfulSync  string                          `json:"last_successful_sync,omitempty"`
	Accounting          accounting.ProjectSummary       `json:"accounting"`
	Sessions            []ledger.SessionReport          `json:"sessions"`
	Evidence            map[string][]ledger.EvidenceRef `json:"evidence"`
	LegacyCompatibility LegacyCompatibility             `json:"legacy_compatibility"`
}

type CurrentRiskProvenance struct {
	RiskID string `json:"risk_id"`
	Kind   string `json:"kind"`
}

// LegacyCompatibility retains the accepted inputs that proposal, resume, and
// history still consume but which are not fully visible in the two v2 human
// documents. Slices give the machine codec deterministic, duplicate-safe IDs.
type LegacyCompatibility struct {
	CurrentState ledger.CurrentState     `json:"current_state"`
	Timeline     []ledger.TimelineEvent  `json:"timeline"`
	Decisions    []ledger.Decision       `json:"decisions"`
	OpenLoops    []ledger.OpenLoop       `json:"open_loops"`
	CurrentRisks []CurrentRiskProvenance `json:"current_risks"`
}

type State struct {
	Review  Review
	Events  []Event
	Machine MachineLedger
}
