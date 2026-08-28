package reviewjob

import (
	"errors"
	"time"

	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

// legacyStoredJobV1 is the exact mandatory wire emitted at 22097a5 and
// bdc3a11. It is intentionally explicit instead of embedding Job: embedding
// would make a future Job field an accidentally accepted historical field.
type legacyStoredJobV1 struct {
	Revision int         `json:"revision"`
	Job      legacyJobV1 `json:"job"`
}

type legacyJobV1 struct {
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

	PacketDigest      string           `json:"packet_digest,omitempty"`
	ResultDigest      string           `json:"result_digest,omitempty"`
	ReviewAccounting  ReviewAccounting `json:"review_usage"`
	Error             SafeError        `json:"error,omitempty"`
	SyncOnlyAvailable bool             `json:"sync_only_available"`
	PrivateError      string           `json:"private_error,omitempty"`
}

func migrateLegacyStoredJobV1(record legacyStoredJobV1) (storedJob, error) {
	if record.Revision < 1 || record.Revision > maxSafeInteger {
		return storedJob{}, errors.New("legacy review job revision is invalid")
	}
	legacy := record.Job
	job := Job{
		SchemaVersion: legacy.SchemaVersion, ID: legacy.ID, ProjectID: legacy.ProjectID,
		ProjectIdentity: legacy.ProjectIdentity, Agent: legacy.Agent, State: legacy.State,
		Phase: legacy.Phase, Attempt: legacy.Attempt,
		FrozenSessions: append([]FrozenSession(nil), legacy.FrozenSessions...),
		SessionIndex:   legacy.SessionIndex, CurrentPacket: legacy.CurrentPacket,
		AcceptedPackets: legacy.AcceptedPackets, AcceptedSessions: legacy.AcceptedSessions,
		CreatedAt: legacy.CreatedAt, StartedAt: legacy.StartedAt, UpdatedAt: legacy.UpdatedAt,
		CompletedAt: legacy.CompletedAt, CancellationRequested: legacy.CancellationRequested,
		Owner: legacy.Owner, PacketDigest: legacy.PacketDigest, ResultDigest: legacy.ResultDigest,
		ReviewAccounting: legacy.ReviewAccounting, Error: legacy.Error, PrivateError: legacy.PrivateError,
	}
	if active(job.State) && (job.PacketDigest != "" || job.ResultDigest != "") {
		job.PayloadState = PayloadRetained
	} else if terminal(job.State) && (job.PacketDigest != "" || job.ResultDigest != "") {
		// The historical worker deleted private payloads before every terminal
		// CAS. Digests in a terminal record therefore are audit metadata, not
		// authority to assume that recoverable bytes remain.
		job.PayloadState = PayloadCleanupComplete
	}
	// Validate the common historical state before interpreting its public
	// sync-only bit. This also rejects malformed counts, cursors and identities.
	if err := Validate(job); err != nil {
		return storedJob{}, err
	}
	if legacy.SyncOnlyAvailable && (legacy.State != Failed || legacy.AcceptedPackets == 0) {
		return storedJob{}, errors.New("legacy sync-only state is invalid")
	}
	if legacy.SyncOnlyAvailable {
		if legacyProvesAcceptedSyncPending(legacy) {
			job.AcceptedSyncPending = true
		} else {
			// bdc3a11 set sync_only_available after any earlier accepted packet,
			// including later Agent/cleanup failures. Without exact sync-conflict
			// and cursor evidence, force receipt inspection and expose no sync-only
			// action.
			job.Error = SafeError{Code: ApplyRecovery}
		}
	}
	if err := Validate(job); err != nil {
		return storedJob{}, err
	}
	return storedJob{Revision: record.Revision, Job: job}, nil
}

func legacyProvesAcceptedSyncPending(job legacyJobV1) bool {
	if job.State != Failed || job.Error.Code != SyncConflict || job.AcceptedPackets == 0 ||
		job.CurrentPacket.Line == 0 || job.SessionIndex < 0 || job.SessionIndex >= len(job.FrozenSessions) {
		return false
	}
	upper := job.FrozenSessions[job.SessionIndex].Upper
	return job.CurrentPacket.Line < upper.Line || job.CurrentPacket == upper
}
