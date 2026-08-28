// Package reviewjob defines the durable private review-job record and its
// deliberately smaller public status projection.
package reviewjob

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

const PublicStatusSchemaVersion = 1

type State string

const (
	Queued          State = "queued"
	Running         State = "running"
	Completed       State = "completed"
	Failed          State = "failed"
	CancelRequested State = "cancel_requested"
	Cancelled       State = "cancelled"
	Retrying        State = "retrying"
)

type Phase string

const (
	Preflight Phase = "preflight"
	Scanning  Phase = "scanning"
	Preparing Phase = "preparing"
	Reviewing Phase = "reviewing"
	Applying  Phase = "applying"
	Syncing   Phase = "syncing"
)

// FrozenSession is the click-time bounded source interval for one session.
// It remains private because Upper contains source provenance.
type FrozenSession struct {
	SessionID string                  `json:"session_id"`
	StartedAt time.Time               `json:"started_at"`
	Upper     evidence.CursorBoundary `json:"upper"`
}

// VerifiedAgent records the authenticated executable identity selected when a
// job was started. Executable is intentionally private and must never appear
// in PublicStatus.
type VerifiedAgent struct {
	Kind       string                  `json:"kind"`
	Identity   pathguard.IdentityToken `json:"identity"`
	Version    string                  `json:"version"`
	Executable string                  `json:"executable"`
}

// Owner is lease diagnostic metadata. Kernel locks remain authoritative.
type Owner struct {
	ID         string    `json:"id"`
	AcquiredAt time.Time `json:"acquired_at"`
}

type ErrorCode string

const (
	AgentUnconfigured  ErrorCode = "E_AGENT_UNCONFIGURED"
	AgentIncompatible  ErrorCode = "E_AGENT_INCOMPATIBLE"
	AgentAuth          ErrorCode = "E_AGENT_AUTH"
	AgentBusy          ErrorCode = "E_AGENT_BUSY"
	AgentTimeout       ErrorCode = "E_AGENT_TIMEOUT"
	AgentToolForbidden ErrorCode = "E_AGENT_TOOL_FORBIDDEN"
	AgentCancelled     ErrorCode = "E_AGENT_CANCELLED"
	ProposalRejected   ErrorCode = "E_PROPOSAL_REJECTED"
	ApplyRecovery      ErrorCode = "E_APPLY_RECOVERY"
	SyncConflict       ErrorCode = "E_SYNC_CONFLICT"
	SyncPartial        ErrorCode = "E_SYNC_PARTIAL"
)

// SafeError is a stable public error classification. Detail is private and is
// kept separately on Job so a public status cannot accidentally serialize it.
type SafeError struct {
	Code ErrorCode `json:"code"`
}

// ReviewUsage is private durable accounting for the review run itself, not
// source-session accounting shown in the existing project usage cards.
type ReviewUsage struct {
	TokenUsage accounting.TokenUsage `json:"token_usage"`
	CostUSD    float64               `json:"cost_usd"`
}

// Job is the private persisted job record. Fields that could reveal source
// paths, source content, prompts, output, or diagnostics must be projected
// through ProjectStatus instead of serialized as a CLI response.
type Job struct {
	SchemaVersion   int                     `json:"schema_version"`
	ID              string                  `json:"id"`
	ProjectID       string                  `json:"project_id"`
	ProjectIdentity pathguard.IdentityToken `json:"project_identity"`
	Agent           VerifiedAgent           `json:"agent"`
	State           State                   `json:"state"`
	Phase           Phase                   `json:"phase,omitempty"`
	Attempt         int                     `json:"attempt"`

	FrozenSessions   []FrozenSession         `json:"frozen_sessions"`
	SessionIndex     int                     `json:"session_index"`
	CurrentPacket    evidence.CursorBoundary `json:"current_packet"`
	AcceptedPackets  int                     `json:"accepted_packets"`
	AcceptedSessions int                     `json:"accepted_sessions"`

	CreatedAt             time.Time `json:"created_at"`
	StartedAt             time.Time `json:"started_at,omitempty"`
	UpdatedAt             time.Time `json:"updated_at"`
	CompletedAt           time.Time `json:"completed_at,omitempty"`
	CancellationRequested time.Time `json:"cancellation_requested_at,omitempty"`
	Owner                 Owner     `json:"owner,omitempty"`

	PacketDigest      string      `json:"packet_digest,omitempty"`
	ResultDigest      string      `json:"result_digest,omitempty"`
	ReviewUsage       ReviewUsage `json:"review_usage"`
	Error             SafeError   `json:"error,omitempty"`
	SyncOnlyAvailable bool        `json:"sync_only_available"`
	PrivateError      string      `json:"private_error,omitempty"`
}

type PublicState string

const Idle PublicState = "idle"

type PublicReviewUsage struct {
	TokenUsage accounting.TokenUsage `json:"token_usage"`
	CostUSD    float64               `json:"cost_usd"`
}

// PublicStatus is the complete JSON response allowed to cross the CLI/plugin
// boundary. Its tags are a versioned snake_case protocol.
type PublicStatus struct {
	SchemaVersion    int                `json:"schema_version"`
	JobID            string             `json:"job_id,omitempty"`
	ProjectID        string             `json:"project_id"`
	State            PublicState        `json:"state"`
	Phase            Phase              `json:"phase,omitempty"`
	Attempt          int                `json:"attempt"`
	SessionIndex     int                `json:"session_index"`
	SessionCount     int                `json:"session_count"`
	AcceptedPackets  int                `json:"accepted_packets"`
	AcceptedSessions int                `json:"accepted_sessions"`
	ErrorCode        string             `json:"error_code,omitempty"`
	CanRetry         bool               `json:"can_retry"`
	CanCancel        bool               `json:"can_cancel"`
	CanSyncOnly      bool               `json:"can_sync_only"`
	ReviewUsage      *PublicReviewUsage `json:"review_usage,omitempty"`
}

// ProjectStatus creates the only public projection of a private Job. A nil job
// deliberately means this project is idle; it never inherits a historical ID.
func ProjectStatus(job *Job, projectID string) (PublicStatus, error) {
	if err := validateProjectID(projectID); err != nil {
		return PublicStatus{}, err
	}
	if job == nil {
		return PublicStatus{SchemaVersion: PublicStatusSchemaVersion, ProjectID: projectID, State: Idle}, nil
	}
	if err := Validate(*job); err != nil {
		return PublicStatus{}, err
	}
	if job.ProjectID != projectID {
		return PublicStatus{}, errors.New("job does not belong to project")
	}
	status := PublicStatus{
		SchemaVersion:    PublicStatusSchemaVersion,
		JobID:            job.ID,
		ProjectID:        job.ProjectID,
		State:            PublicState(job.State),
		Phase:            job.Phase,
		Attempt:          job.Attempt,
		SessionIndex:     job.SessionIndex,
		SessionCount:     len(job.FrozenSessions),
		AcceptedPackets:  job.AcceptedPackets,
		AcceptedSessions: job.AcceptedSessions,
		ErrorCode:        string(job.Error.Code),
		CanRetry:         job.State == Failed,
		CanCancel:        active(job.State),
		CanSyncOnly:      job.SyncOnlyAvailable,
	}
	if job.ReviewUsage.TokenUsage != (accounting.TokenUsage{}) || job.ReviewUsage.CostUSD != 0 {
		status.ReviewUsage = &PublicReviewUsage{TokenUsage: job.ReviewUsage.TokenUsage, CostUSD: job.ReviewUsage.CostUSD}
	}
	return status, nil
}

var safeID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
var lowercaseSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var prefixedSHA256 = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

const maxSafeInteger = 1<<53 - 1

// Validate rejects malformed private records before they are persisted or
// projected. It intentionally validates private fields that are absent from
// the public schema so bad private state cannot influence public status.
func Validate(job Job) error {
	if job.SchemaVersion != PublicStatusSchemaVersion {
		return fmt.Errorf("unsupported review job schema version %d", job.SchemaVersion)
	}
	if !validID(job.ID) || !validID(job.ProjectID) {
		return errors.New("job and project IDs must be safe stable IDs")
	}
	if !job.ProjectIdentity.Valid() || !job.Agent.Identity.Valid() {
		return errors.New("project and agent identities must be valid")
	}
	if strings.TrimSpace(job.Agent.Kind) == "" || strings.TrimSpace(job.Agent.Version) == "" || strings.TrimSpace(job.Agent.Executable) == "" {
		return errors.New("verified agent kind, version, and executable are required")
	}
	if !validState(job.State) || !validPhase(job.Phase) {
		return errors.New("job state or phase is unknown")
	}
	if job.Attempt < 1 || job.Attempt > maxSafeInteger || job.SessionIndex < 0 || job.SessionIndex > len(job.FrozenSessions) || job.AcceptedPackets < 0 || job.AcceptedPackets > maxSafeInteger || job.AcceptedSessions < 0 || job.AcceptedSessions > len(job.FrozenSessions) || job.AcceptedSessions > job.SessionIndex || job.AcceptedSessions > job.AcceptedPackets {
		return errors.New("job session index or accepted progress is impossible")
	}
	if active(job.State) && job.Phase == "" {
		return errors.New("active job phase is required")
	}
	if !active(job.State) && job.Phase != "" {
		return errors.New("terminal job must not retain a phase")
	}
	if err := validateTimes(job); err != nil {
		return err
	}
	if err := validateFrozenSessions(job.FrozenSessions); err != nil {
		return err
	}
	if err := validateBoundary(job.CurrentPacket); err != nil {
		return fmt.Errorf("current packet: %w", err)
	}
	if err := validateCurrentPacket(job.CurrentPacket, job.FrozenSessions, job.SessionIndex); err != nil {
		return err
	}
	if job.State == Completed && (job.SessionIndex != len(job.FrozenSessions) || job.AcceptedSessions != len(job.FrozenSessions)) {
		return errors.New("completed job must accept every frozen session")
	}
	for _, digest := range []string{job.PacketDigest, job.ResultDigest} {
		if digest != "" && !prefixedSHA256.MatchString(digest) {
			return errors.New("job digest must be prefixed lowercase SHA-256")
		}
	}
	if err := accounting.ValidateTokenUsage(job.ReviewUsage.TokenUsage); err != nil {
		return fmt.Errorf("review usage: %w", err)
	}
	if math.IsNaN(job.ReviewUsage.CostUSD) || math.IsInf(job.ReviewUsage.CostUSD, 0) || job.ReviewUsage.CostUSD < 0 {
		return errors.New("review usage cost must be finite and nonnegative")
	}
	if job.Error.Code != "" && !validErrorCode(job.Error.Code) {
		return errors.New("safe error code is invalid")
	}
	if job.SyncOnlyAvailable && (job.State != Failed || job.AcceptedPackets == 0) {
		return errors.New("sync-only availability requires a failed job with accepted packets")
	}
	if terminal(job.State) && job.Owner.ID != "" {
		return errors.New("terminal job must not retain live ownership")
	}
	if !terminal(job.State) && job.Owner.ID != "" && (!validID(job.Owner.ID) || !canonicalTime(job.Owner.AcquiredAt)) {
		return errors.New("job owner is invalid")
	}
	return nil
}

func validateTimes(job Job) error {
	for _, value := range []time.Time{job.CreatedAt, job.UpdatedAt} {
		if !canonicalTime(value) {
			return errors.New("job timestamps must be canonical UTC timestamps")
		}
	}
	for _, value := range []time.Time{job.StartedAt, job.CompletedAt, job.CancellationRequested} {
		if !value.IsZero() && !canonicalTime(value) {
			return errors.New("job timestamps must be canonical UTC timestamps")
		}
	}
	if job.UpdatedAt.Before(job.CreatedAt) || (!job.StartedAt.IsZero() && job.StartedAt.Before(job.CreatedAt)) || (!job.CompletedAt.IsZero() && job.CompletedAt.Before(job.CreatedAt)) {
		return errors.New("job timestamps are out of order")
	}
	if terminal(job.State) && job.CompletedAt.IsZero() {
		return errors.New("terminal job completion timestamp is required")
	}
	return nil
}

func validateFrozenSessions(sessions []FrozenSession) error {
	seen := make(map[string]struct{}, len(sessions))
	var previous FrozenSession
	for index, session := range sessions {
		if !validID(session.SessionID) || !canonicalTime(session.StartedAt) {
			return errors.New("frozen session ID or timestamp is invalid")
		}
		if _, ok := seen[session.SessionID]; ok {
			return errors.New("duplicate frozen session")
		}
		seen[session.SessionID] = struct{}{}
		if err := validateBoundary(session.Upper); err != nil || session.Upper.Line == 0 {
			return errors.New("frozen session upper boundary is invalid")
		}
		if index > 0 && (session.StartedAt.Before(previous.StartedAt) || (session.StartedAt.Equal(previous.StartedAt) && session.SessionID < previous.SessionID)) {
			return errors.New("frozen sessions are not in chronological order")
		}
		previous = session
	}
	return nil
}

func validateBoundary(boundary evidence.CursorBoundary) error {
	if boundary.Line < 0 || (boundary.Line == 0 && boundary.SourceHash != "") || (boundary.Line > 0 && !lowercaseSHA256.MatchString(boundary.SourceHash)) {
		return errors.New("cursor boundary is invalid")
	}
	return nil
}

func validateCurrentPacket(current evidence.CursorBoundary, sessions []FrozenSession, sessionIndex int) error {
	if current.Line == 0 {
		return nil
	}
	if sessionIndex >= len(sessions) {
		return errors.New("current packet requires a frozen session")
	}
	upper := sessions[sessionIndex].Upper
	if current.Line > upper.Line || (current.Line == upper.Line && current.SourceHash != upper.SourceHash) {
		return errors.New("current packet exceeds frozen session upper boundary")
	}
	return nil
}

func canonicalTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Equal(value.UTC())
}

func validID(value string) bool { return safeID.MatchString(value) }

func validateProjectID(value string) error {
	if !validID(value) {
		return errors.New("project ID must be a safe stable ID")
	}
	return nil
}

func validState(value State) bool {
	switch value {
	case Queued, Running, Completed, Failed, CancelRequested, Cancelled, Retrying:
		return true
	default:
		return false
	}
}

func validPhase(value Phase) bool {
	switch value {
	case "", Preflight, Scanning, Preparing, Reviewing, Applying, Syncing:
		return true
	default:
		return false
	}
}

func active(value State) bool {
	return value == Queued || value == Running || value == CancelRequested || value == Retrying
}

func terminal(value State) bool { return !active(value) }

func validErrorCode(value ErrorCode) bool {
	switch value {
	case AgentUnconfigured, AgentIncompatible, AgentAuth, AgentBusy, AgentTimeout, AgentToolForbidden, AgentCancelled, ProposalRejected, ApplyRecovery, SyncConflict, SyncPartial:
		return true
	default:
		return false
	}
}
