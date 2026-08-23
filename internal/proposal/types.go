package proposal

import "github.com/neomei/SessionReviewer/internal/ledger"

// Proposal is the complete schema-v1 semantic change envelope.
type Proposal struct {
	SchemaVersion        int                    `json:"schema_version"`
	ProjectID            string                 `json:"project_id"`
	SessionID            string                 `json:"session_id"`
	FromCursor           int                    `json:"from_cursor"`
	ToCursor             int                    `json:"to_cursor"`
	EvidencePacketSHA256 string                 `json:"evidence_packet_sha256"`
	NewDecisions         []ledger.Decision      `json:"new_decisions"`
	UpdatedDecisions     []DecisionPatch        `json:"updated_decisions"`
	OpenLoops            []OpenLoopChange       `json:"open_loops"`
	TimelineEvents       []ledger.TimelineEvent `json:"timeline_events"`
	CurrentStatePatch    CurrentStatePatch      `json:"current_state_patch"`
	SessionReport        ledger.SessionReport   `json:"session_report"`
	EvidenceLinks        []EvidenceLink         `json:"evidence_links"`
}

type DecisionPatch struct {
	ID               string                `json:"id"`
	ExpectedRevision int                   `json:"expected_revision"`
	Title            *string               `json:"title,omitempty"`
	Status           *string               `json:"status,omitempty"`
	Tags             *[]string             `json:"tags,omitempty"`
	Supersedes       *[]string             `json:"supersedes,omitempty"`
	SourceSessions   *[]string             `json:"source_sessions,omitempty"`
	Evidence         *[]ledger.EvidenceRef `json:"evidence,omitempty"`
	Context          *string               `json:"context,omitempty"`
	Rationale        *string               `json:"rationale,omitempty"`
	Consequences     *string               `json:"consequences,omitempty"`
	ReevaluateWhen   *string               `json:"reevaluate_when,omitempty"`
	Alternatives     *[]string             `json:"alternatives,omitempty"`
	RejectedPaths    *[]string             `json:"rejected_paths,omitempty"`
}

type OpenLoopChange struct {
	Operation string           `json:"operation"`
	Entity    *ledger.OpenLoop `json:"entity,omitempty"`
	Patch     *OpenLoopPatch   `json:"patch,omitempty"`
}

type OpenLoopPatch struct {
	ID                  string                `json:"id"`
	ExpectedRevision    int                   `json:"expected_revision"`
	Title               *string               `json:"title,omitempty"`
	Status              *string               `json:"status,omitempty"`
	Tags                *[]string             `json:"tags,omitempty"`
	SourceSessions      *[]string             `json:"source_sessions,omitempty"`
	Evidence            *[]ledger.EvidenceRef `json:"evidence,omitempty"`
	Question            *string               `json:"question,omitempty"`
	Blocker             *string               `json:"blocker,omitempty"`
	NextExperiment      *string               `json:"next_experiment,omitempty"`
	CompletionCriterion *string               `json:"completion_criterion,omitempty"`
	Attempts            *[]string             `json:"attempts,omitempty"`
}

type CurrentStatePatch struct {
	ExpectedRevision   int                   `json:"expected_revision"`
	Goal               *string               `json:"goal,omitempty"`
	LastVerified       *string               `json:"last_verified,omitempty"`
	Branch             *string               `json:"branch,omitempty"`
	NextAction         *string               `json:"next_action,omitempty"`
	FirstInspection    *string               `json:"first_inspection,omitempty"`
	LastUpdated        *string               `json:"last_updated,omitempty"`
	UncommittedChanges *[]string             `json:"uncommitted_changes,omitempty"`
	Blockers           *[]string             `json:"blockers,omitempty"`
	OpenRisks          *[]string             `json:"open_risks,omitempty"`
	SourceSessions     *[]string             `json:"source_sessions,omitempty"`
	Evidence           *[]ledger.EvidenceRef `json:"evidence,omitempty"`
}

type EvidenceLink struct {
	EntityID   string `json:"entity_id"`
	EvidenceID string `json:"evidence_id"`
	Relation   string `json:"relation"`
}
