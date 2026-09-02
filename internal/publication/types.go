package publication

import (
	"context"
	"errors"
	"time"
)

// Stage indicates the current durable state in a publication lifecycle.
type Stage string

const (
	StagePrepared         Stage = "prepared"
	StageProjectWritten   Stage = "project_written"
	StageVaultSynced      Stage = "vault_synced"
	StageVerified         Stage = "verified"
	StageCommitted        Stage = "committed"
	StageRollbackRequired Stage = "rollback_required"
)

// Destination captures one projected file's preimage and expected desired state.
type Destination struct {
	Side           string `json:"side"`
	Relative       string `json:"relative"`
	PreimageSHA256 string `json:"preimage_sha256,omitempty"`
	DesiredSHA256  string `json:"desired_sha256"`
	PreimageExists bool   `json:"preimage_exists"`
}

// Intent captures the full durable intent of a cross-root publication.
type Intent struct {
	Version           int           `json:"version"`
	ProjectID         string        `json:"project_id"`
	GenerationID      string        `json:"generation_id"`
	ManifestDigest    string        `json:"manifest_digest"`
	ProjectViewDigest string        `json:"project_view_digest"`
	Stage             Stage         `json:"stage"`
	CreatedAt         time.Time     `json:"created_at"`
	Destinations      []Destination `json:"destinations"`
}

// PublicationProof verifies that all public projections match before committing.
type PublicationProof struct {
	ProjectID         string `json:"project_id"`
	GenerationID      string `json:"generation_id"`
	ManifestDigest    string `json:"manifest_digest"`
	ProjectViewDigest string `json:"project_view_digest"`
	ReviewSHA256      string `json:"review_sha256"`
	HistorySHA256     string `json:"history_sha256"`
	LedgerSHA256      string `json:"ledger_sha256"`
	JournalVerified   bool   `json:"journal_verified"`
}

var (
	ErrNoActiveIntent          = errors.New("no active publication intent")
	ErrActiveIntentExists      = errors.New("an active publication intent already exists")
	ErrInvalidStageTransition  = errors.New("invalid publication stage transition")
	ErrStageMismatch           = errors.New("publication stage mismatch")
	ErrPublicationProofInvalid = errors.New("publication proof is invalid")
	ErrPreimageMismatch        = errors.New("preimage hash mismatch")
)

// RecoveryHandler is invoked during journal recovery to handle unfinished stages.
type RecoveryHandler interface {
	RecoverStage(ctx context.Context, intent Intent, j *Journal) error
}

// RecoveryHandlerFunc allows ordinary functions to act as a RecoveryHandler.
type RecoveryHandlerFunc func(ctx context.Context, intent Intent, j *Journal) error

// RecoverStage implements RecoveryHandler.
func (f RecoveryHandlerFunc) RecoverStage(ctx context.Context, intent Intent, j *Journal) error {
	return f(ctx, intent, j)
}
