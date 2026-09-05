package publication

import (
	"context"
	"errors"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
)

// Stage indicates the current durable state in a publication lifecycle.
type Stage = publicationstate.Stage

const (
	StagePrepared         = publicationstate.StagePrepared
	StageProjectWritten   = publicationstate.StageProjectWritten
	StageVaultSynced      = publicationstate.StageVaultSynced
	StageVerified         = publicationstate.StageVerified
	StageBaseCommitted    = publicationstate.StageBaseCommitted
	StageCommitted        = publicationstate.StageCommitted
	StageRollbackRequired = publicationstate.StageRollbackRequired
)

type Kind = publicationstate.Kind

const (
	KindGeneration = publicationstate.KindGeneration
	KindMarkdown   = publicationstate.KindMarkdown
)

type Outcome = publicationstate.Outcome

const (
	OutcomeAccepted   = publicationstate.OutcomeAccepted
	OutcomeRolledBack = publicationstate.OutcomeRolledBack
)

// Destination captures one projected file's preimage and expected desired state.
type Destination = publicationstate.Destination
type IndexGuard = publicationstate.IndexGuard
type AcceptedMarkdownReceipt = publicationstate.AcceptedReceipt

// Intent captures the full durable intent of a cross-root publication.
type Intent = publicationstate.Intent

func MarkdownRevisionID(intent Intent) string { return publicationstate.MarkdownRevisionID(intent) }

// PublicationProof aliases memory.PublicationProof.
type PublicationProof = memory.PublicationProof

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
