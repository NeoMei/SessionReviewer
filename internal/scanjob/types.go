package scanjob

import (
	"time"
)

type State string

const (
	StateQueued              State = "queued"
	StateRunning             State = "running"
	StateCompleted           State = "completed"
	StateCompletedWithIssues State = "completed_with_issues"
	StateFailed              State = "failed"
)

type Phase string

const (
	PhaseDiscovering Phase = "discovering"
	PhaseExtracting  Phase = "extracting"
	PhaseReducing    Phase = "reducing"
	PhaseRendering   Phase = "rendering"
	PhaseSyncing     Phase = "syncing"
)

type PublicStatus struct {
	SchemaVersion int    `json:"schema_version"`
	JobID         string `json:"job_id"`
	ProjectID     string `json:"project_id"`
	State         string `json:"state"`
	Phase         string `json:"phase"`
	SessionCount  int    `json:"session_count"`
	IndexedCount  int    `json:"indexed_count"`
	IssueCount    int    `json:"issue_count"`
	GenerationID  string `json:"generation_id,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
	ErrorMessage  string `json:"error_message,omitempty"`
}

type JobRecord struct {
	SchemaVersion int       `json:"schema_version"`
	JobID         string    `json:"job_id"`
	ProjectID     string    `json:"project_id"`
	SessionsRoot  string    `json:"sessions_root,omitempty"`
	State         State     `json:"state"`
	Phase         Phase     `json:"phase"`
	PID           int       `json:"pid,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	SessionCount  int       `json:"session_count"`
	IndexedCount  int       `json:"indexed_count"`
	IssueCount    int       `json:"issue_count"`
	GenerationID  string    `json:"generation_id,omitempty"`
	ErrorCode     string    `json:"error_code,omitempty"`
	ErrorMessage  string    `json:"error_message,omitempty"`
}
