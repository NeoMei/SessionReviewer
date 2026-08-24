package ledger

import "io/fs"

type FactClass string

const (
	Verified            FactClass = "verified"
	DecisionFact        FactClass = "decision"
	Inference           FactClass = "inference"
	Superseded          FactClass = "superseded"
	PendingConfirmation FactClass = "pending_confirmation"
)

type EvidenceRef struct {
	EvidenceID string `json:"evidence_id" yaml:"evidence_id"`
	SessionID  string `json:"session_id" yaml:"session_id"`
	JSONLLine  int    `json:"jsonl_line" yaml:"jsonl_line"`
	SourceHash string `json:"source_hash" yaml:"source_hash"`
	Summary    string `json:"summary" yaml:"summary"`
}

type Decision struct {
	ID             string        `json:"id"`
	ProjectID      string        `json:"project_id"`
	Title          string        `json:"title"`
	Status         string        `json:"status"`
	Revision       int           `json:"revision"`
	Tags           []string      `json:"tags"`
	Supersedes     []string      `json:"supersedes"`
	SourceSessions []string      `json:"source_sessions"`
	Evidence       []EvidenceRef `json:"evidence"`
	Context        string        `json:"context"`
	Rationale      string        `json:"rationale"`
	Consequences   string        `json:"consequences"`
	ReevaluateWhen string        `json:"reevaluate_when"`
	Alternatives   []string      `json:"alternatives"`
	RejectedPaths  []string      `json:"rejected_paths"`
}

type OpenLoop struct {
	ID                  string        `json:"id"`
	ProjectID           string        `json:"project_id"`
	Title               string        `json:"title"`
	Status              string        `json:"status"`
	Revision            int           `json:"revision"`
	Tags                []string      `json:"tags"`
	SourceSessions      []string      `json:"source_sessions"`
	Evidence            []EvidenceRef `json:"evidence"`
	Question            string        `json:"question"`
	Attempts            []string      `json:"attempts"`
	Blocker             string        `json:"blocker"`
	NextExperiment      string        `json:"next_experiment"`
	CompletionCriterion string        `json:"completion_criterion"`
}

type TimelineEvent struct {
	ID          string        `json:"id"`
	OccurredAt  string        `json:"occurred_at"`
	Revision    int           `json:"revision"`
	Class       FactClass     `json:"class"`
	Title       string        `json:"title"`
	Summary     string        `json:"summary"`
	Evidence    []EvidenceRef `json:"evidence"`
	DecisionIDs []string      `json:"decision_ids"`
	OpenLoopIDs []string      `json:"open_loop_ids"`
}

type CurrentState struct {
	ProjectID          string        `json:"project_id"`
	Revision           int           `json:"revision"`
	Goal               string        `json:"goal"`
	LastVerified       string        `json:"last_verified"`
	Branch             string        `json:"branch"`
	UncommittedChanges []string      `json:"uncommitted_changes"`
	Blockers           []string      `json:"blockers"`
	OpenRisks          []string      `json:"open_risks"`
	NextAction         string        `json:"next_action"`
	FirstInspection    string        `json:"first_inspection"`
	LastUpdated        string        `json:"last_updated"`
	SourceSessions     []string      `json:"source_sessions"`
	Evidence           []EvidenceRef `json:"evidence"`
}

type SessionPhase struct {
	Title    string        `json:"title"`
	Summary  string        `json:"summary"`
	Evidence []EvidenceRef `json:"evidence"`
}

type SessionReport struct {
	ID                string         `json:"id"`
	ProjectID         string         `json:"project_id"`
	SessionID         string         `json:"session_id"`
	Revision          int            `json:"revision"`
	InitialGoal       string         `json:"initial_goal"`
	GoalChanges       []string       `json:"goal_changes"`
	Phases            []SessionPhase `json:"phases"`
	Files             []string       `json:"files"`
	Commits           []string       `json:"commits"`
	Verification      []string       `json:"verification"`
	DecisionsAdded    []string       `json:"decisions_added"`
	DecisionsRevised  []string       `json:"decisions_revised"`
	OpenLoopsCreated  []string       `json:"open_loops_created"`
	OpenLoopsClosed   []string       `json:"open_loops_closed"`
	PreviousSessionID string         `json:"previous_session_id"`
	NextSessionID     string         `json:"next_session_id"`
	Evidence          []EvidenceRef  `json:"evidence"`
}

type State struct {
	ProjectID       string
	CurrentState    CurrentState
	Timeline        []TimelineEvent
	Decisions       map[string]Decision
	OpenLoops       map[string]OpenLoop
	Sessions        map[string]SessionReport
	documents       stateDocuments
	projectRoot     string
	projectRootInfo fs.FileInfo
}

type ChangeSet struct {
	Current   *CurrentState
	Timeline  []TimelineEvent
	Decisions []Decision
	OpenLoops []OpenLoop
	Sessions  []SessionReport
}

type PlannedFile struct {
	RelativePath   string
	Data           []byte
	Perm           fs.FileMode
	ExpectedData   []byte
	ExpectedExists bool
	ExpectedPerm   fs.FileMode
}

type WritePlan struct {
	ProjectRoot string
	Files       []PlannedFile
}

type loadedDocument struct {
	Document     Document
	RelativePath string
	Original     []byte
	Perm         fs.FileMode
}

type stateDocuments struct {
	overview  *loadedDocument
	current   *loadedDocument
	timeline  *loadedDocument
	decisions map[string]loadedDocument
	openLoops map[string]loadedDocument
	sessions  map[string]loadedDocument
}
