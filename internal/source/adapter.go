// Package source defines provider-neutral contracts for discovering, freezing,
// decoding, and selectively reading authenticated session sources.
package source

import (
	"context"
	"errors"
	"fmt"

	"github.com/neomei/SessionReviewer/internal/memory"
)

const MaxReadBytes int64 = 64 << 10

var ErrReadLimit = errors.New("source read limit must be between 1 byte and 64 KiB")

// Adapter is implemented by a provider-specific, deterministic source decoder.
// Read must verify ref.SourceHash before returning at most limit bytes. Read is
// for explicit, bounded analysis only; callers must not persist its complete
// output as an Observation or raw-source archive.
type Adapter interface {
	Discover(context.Context) (Discovery, error)
	Freeze(context.Context, Candidate) (Boundary, error)
	Decode(context.Context, Boundary, func(memory.ObservationRevision) error) (DecodeReport, error)
	Read(context.Context, memory.SourceRef, int64) ([]byte, error)
}

// LeaseLifecycle is implemented by stateful adapters whose Discover and
// Freeze results retain adapter-owned bookkeeping. Each Candidate returned by
// Discover is owned by the caller until Freeze consumes it or the caller
// abandons it. Each Boundary returned by a successful Freeze is owned by the
// caller until Decode consumes it or the caller abandons it. Abandon methods
// must be idempotent and must not invalidate another occurrence with the same
// stable Handle. Stateless adapters do not need to implement this interface.
type LeaseLifecycle interface {
	AbandonCandidate(Candidate)
	AbandonBoundary(Boundary)
}

// Candidate identifies one logical provider Session. Handle is a stable opaque
// content boundary key. Lease is a single-owner occurrence token and is opaque
// outside the adapter that returned it.
type Candidate struct {
	Provider        string
	SessionID       string
	StartedAt       string
	InitialCWD      string
	Handle          string
	Lease           string
	CatalogBaseline CatalogBaselineSnapshot
}

// CatalogBaselineSnapshot is content-free and bound to the adapter's opaque
// Handle. PriorSource is a defensive copy captured during Discover; nil means
// the source was absent and ExpectedDigest must be empty.
type CatalogBaselineSnapshot struct {
	Handle         string
	ExpectedDigest string
	PriorSource    *memory.SourceRecord
}

type Discovery struct {
	Candidates []Candidate
	Issues     []Issue
}

// Issue is content-free and safe to retain as terminal scan metadata.
type Issue struct {
	Code          string
	Provider      string
	SessionID     string
	Path          string
	TerminalState memory.TerminalState
}

// Boundary is one immutable logical source prefix. Handle remains stable for
// equivalent content. Lease is the caller-owned occurrence token and is valid
// only for the originating Adapter.
type Boundary struct {
	Candidate      Candidate
	SourceIdentity string
	Frozen         memory.FrozenBoundary
	Segments       []SegmentBoundary
	TerminalState  memory.TerminalState
	Issues         []Issue
	Handle         string
	Lease          string
}

// SegmentBoundary authenticates one ordered physical member of a logical
// boundary without exposing its host path.
type SegmentBoundary struct {
	Ordinal    int
	Size       int64
	SourceHash string
}

// RevisionSupersession describes immutable stable-key lineage only. It does not
// infer a predecessor revision ID; selecting active, superseded, or withdrawn
// revisions belongs to generation construction.
type RevisionSupersession struct {
	Key                 memory.ObservationKey
	StableKeyDigest     string
	SuccessorRevisionID string
	SupersededAdapter   string
	SuccessorAdapter    string
}

// QuarantinedRevision records only typed identity and candidate associations;
// it deliberately carries no raw provider payload.
type QuarantinedRevision struct {
	Ref                 memory.SourceRef
	Timestamp           string
	Kind                string
	Subject             string
	CandidateProjectIDs []string
	ReasonCode          string
}

type DecodeReport struct {
	BoundaryRelation      BoundaryRelation
	ProposedSource        memory.SourceRecord
	ExpectedCatalogDigest string
	TerminalState         memory.TerminalState
	MalformedLines        int
	UnsupportedRecords    int
	EmittedRevisions      int
	Diagnostics           []memory.Diagnostic
	Quarantined           []QuarantinedRevision
	Supersessions         []RevisionSupersession
}

type BoundaryRelation string

const (
	BoundaryInitial     BoundaryRelation = "initial"
	BoundaryUnchanged   BoundaryRelation = "unchanged"
	BoundaryAppend      BoundaryRelation = "append"
	BoundaryReplacement BoundaryRelation = "replacement"
)

func ValidateReadLimit(limit int64) error {
	if limit < 1 || limit > MaxReadBytes {
		return fmt.Errorf("%w: got %d", ErrReadLimit, limit)
	}
	return nil
}
