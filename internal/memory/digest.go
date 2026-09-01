package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
)

var unorderedJSONArrays = map[string]struct{}{
	"active_revision_ids":       {},
	"associated_usage":          {},
	"dependency_revision_ids":   {},
	"observation_revision_ids":  {},
	"project_ids":               {},
	"remote_identity_hashes":    {},
	"required_projection_files": {},
	"version_files":             {},
}

var unorderedJSONObjects = map[string]struct{}{
	"active_revisions":     {},
	"superseded_revisions": {},
	"withdrawn_revisions":  {},
}

// Digest returns a deterministic digest of a normalized defensive JSON copy.
// Ordered arrays, including SessionView dependencies, retain caller order.
func Digest(value any) (string, error) {
	return DigestContext(context.Background(), value)
}

func DigestContext(ctx context.Context, value any) (string, error) {
	if ctx == nil {
		return "", errors.New("digest context is required")
	}
	if err := validateCanonicalValueContext(ctx, reflect.ValueOf(value), make(map[digestVisit]bool)); err != nil {
		return "", err
	}
	hash := sha256.New()
	writer := digestContextHashWriter{ctx: ctx, writer: hash}
	encoder := canonicalEncoder{ctx: ctx, writer: &writer, normalizeSets: true, visiting: make(map[digestVisit]bool)}
	if err := encoder.encode(reflect.ValueOf(value), ""); err != nil {
		return "", err
	}
	if err := canonicalCheckpoint(ctx, digestPhaseHashing); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

type digestContextHashWriter struct {
	ctx    context.Context
	writer digestWriter
}

func (writer *digestContextHashWriter) Write(body []byte) (int, error) {
	written := 0
	for len(body) > 0 {
		if err := canonicalCheckpoint(writer.ctx, digestPhaseHashing); err != nil {
			return written, err
		}
		chunk := min(len(body), canonicalContextChunkSize)
		count, err := writer.writer.Write(body[:chunk])
		written += count
		if err != nil || count != chunk {
			return written, errors.Join(err, errors.New("short canonical digest write"))
		}
		body = body[chunk:]
	}
	return written, nil
}

// ObservationRevisionID deliberately excludes the stored revision_id. It
// combines the stable key with the normalized observed payload, source hash,
// and adapter version so adapter re-decoding creates an immutable successor.
func ObservationRevisionID(value ObservationRevision) string {
	return ObservationRevisionIDContext(context.Background(), value)
}

func ObservationRevisionIDContext(ctx context.Context, value ObservationRevision) string {
	identity := struct {
		Key            ObservationKey    `json:"key"`
		Timestamp      string            `json:"timestamp"`
		Operation      string            `json:"operation,omitempty"`
		Object         string            `json:"object,omitempty"`
		Outcome        string            `json:"outcome,omitempty"`
		Fields         map[string]string `json:"fields,omitempty"`
		Excerpt        string            `json:"excerpt,omitempty"`
		SourceHash     string            `json:"source_hash"`
		AdapterVersion string            `json:"adapter_version"`
	}{
		Key:            value.Key,
		Timestamp:      value.Timestamp,
		Operation:      value.Operation,
		Object:         value.Object,
		Outcome:        value.Outcome,
		Fields:         value.Fields,
		Excerpt:        value.Excerpt,
		SourceHash:     value.Ref.SourceHash,
		AdapterVersion: value.AdapterVersion,
	}
	digest, err := DigestContext(ctx, identity)
	if err != nil {
		return ""
	}
	return digest
}

type SessionViewIdentity struct {
	SchemaVersion           int                  `json:"schema_version"`
	ProjectID               string               `json:"project_id"`
	Provider                string               `json:"provider"`
	SessionID               string               `json:"session_id"`
	SourceIdentity          string               `json:"source_identity"`
	SourceRecordDigest      string               `json:"source_record_digest"`
	UsageRecordDigest       string               `json:"usage_record_digest"`
	StartedAt               string               `json:"started_at"`
	EndedAt                 string               `json:"ended_at"`
	TerminalState           TerminalState        `json:"terminal_state"`
	SourceAvailability      SourceAvailability   `json:"source_availability"`
	ActiveRevisionIDs       []string             `json:"active_revision_ids"`
	ObservationSummaries    []ObservationSummary `json:"observation_summaries"`
	ObservationChunkDigests []string             `json:"observation_chunk_digests"`
	DerivedRecords          []DerivedRecord      `json:"derived_records"`
	Diagnostics             []Diagnostic         `json:"diagnostics"`
	DependencyDigest        string               `json:"dependency_digest"`
	MaterializerVersion     string               `json:"materializer_version"`
}

type SessionLineageIdentity struct {
	SchemaVersion         int               `json:"schema_version"`
	ProjectID             string            `json:"project_id"`
	Provider              string            `json:"provider"`
	SessionID             string            `json:"session_id"`
	SourceIdentity        string            `json:"source_identity"`
	ActiveRevisions       map[string]string `json:"active_revisions"`
	SupersededRevisions   map[string]string `json:"superseded_revisions"`
	WithdrawnRevisions    map[string]string `json:"withdrawn_revisions"`
	PreviousLineageDigest string            `json:"previous_lineage_digest,omitempty"`
}

type ProjectProbeStateIdentity struct {
	SchemaVersion           int          `json:"schema_version"`
	ProjectID               string       `json:"project_id"`
	CanonicalRoot           string       `json:"canonical_root"`
	Branch                  string       `json:"branch,omitempty"`
	Head                    string       `json:"head,omitempty"`
	DirtyPathCount          int          `json:"dirty_path_count"`
	RemoteIdentityHashes    []string     `json:"remote_identity_hashes"`
	VersionFiles            []ProbeFile  `json:"version_files"`
	RequiredProjectionFiles []ProbeFile  `json:"required_projection_files"`
	ProbeVersion            string       `json:"probe_version"`
	Diagnostics             []Diagnostic `json:"diagnostics"`
}

type ProjectViewIdentity struct {
	SchemaVersion           int                        `json:"schema_version"`
	ProjectID               string                     `json:"project_id"`
	Generation              int                        `json:"generation"`
	StartedAt               string                     `json:"started_at"`
	EndedAt                 string                     `json:"ended_at"`
	SourceSessions          int                        `json:"source_sessions"`
	TerminalCounts          TerminalCounts             `json:"terminal_counts"`
	SessionViewDependencies []SessionViewDependency    `json:"session_view_dependencies"`
	ObservationRevisionIDs  []string                   `json:"observation_revision_ids"`
	AggregationCoverage     ProjectAggregationCoverage `json:"aggregation_coverage"`
	ProbeStateDigest        string                     `json:"probe_state_digest"`
	LiveState               StateSnapshot              `json:"live_state"`
	WitnessedState          []DerivedRecord            `json:"witnessed_state"`
	DerivedRecords          []DerivedRecord            `json:"derived_records"`
	AssociatedUsage         []AssociatedUsage          `json:"associated_usage"`
	PreviousViewDigest      string                     `json:"previous_view_digest,omitempty"`
	DependencyDigest        string                     `json:"dependency_digest"`
	ReducerVersion          string                     `json:"reducer_version"`
}

func SessionViewDigest(value SessionView) (string, error) {
	return SessionViewDigestContext(context.Background(), value)
}

func SessionViewDigestContext(ctx context.Context, value SessionView) (string, error) {
	return DigestContext(ctx, SessionViewIdentity{
		SchemaVersion:           value.SchemaVersion,
		ProjectID:               value.ProjectID,
		Provider:                value.Provider,
		SessionID:               value.SessionID,
		SourceIdentity:          value.SourceIdentity,
		SourceRecordDigest:      value.SourceRecordDigest,
		UsageRecordDigest:       value.UsageRecordDigest,
		StartedAt:               value.StartedAt,
		EndedAt:                 value.EndedAt,
		TerminalState:           value.TerminalState,
		SourceAvailability:      value.SourceAvailability,
		ActiveRevisionIDs:       value.ActiveRevisionIDs,
		ObservationSummaries:    value.ObservationSummaries,
		ObservationChunkDigests: value.ObservationChunkDigests,
		DerivedRecords:          value.DerivedRecords,
		Diagnostics:             value.Diagnostics,
		DependencyDigest:        value.DependencyDigest,
		MaterializerVersion:     value.MaterializerVersion,
	})
}

func SessionLineageDigest(value SessionLineage) (string, error) {
	return SessionLineageDigestContext(context.Background(), value)
}

func SessionLineageDigestContext(ctx context.Context, value SessionLineage) (string, error) {
	return DigestContext(ctx, SessionLineageIdentity{
		SchemaVersion:         value.SchemaVersion,
		ProjectID:             value.ProjectID,
		Provider:              value.Provider,
		SessionID:             value.SessionID,
		SourceIdentity:        value.SourceIdentity,
		ActiveRevisions:       value.ActiveRevisions,
		SupersededRevisions:   value.SupersededRevisions,
		WithdrawnRevisions:    value.WithdrawnRevisions,
		PreviousLineageDigest: value.PreviousLineageDigest,
	})
}

func ProjectProbeStateDigest(value ProjectProbeState) (string, error) {
	return ProjectProbeStateDigestContext(context.Background(), value)
}

func ProjectProbeStateDigestContext(ctx context.Context, value ProjectProbeState) (string, error) {
	return DigestContext(ctx, ProjectProbeStateIdentity{
		SchemaVersion:           value.SchemaVersion,
		ProjectID:               value.ProjectID,
		CanonicalRoot:           value.CanonicalRoot,
		Branch:                  value.Branch,
		Head:                    value.Head,
		DirtyPathCount:          value.DirtyPathCount,
		RemoteIdentityHashes:    value.RemoteIdentityHashes,
		VersionFiles:            value.VersionFiles,
		RequiredProjectionFiles: value.RequiredProjectionFiles,
		ProbeVersion:            value.ProbeVersion,
		Diagnostics:             value.Diagnostics,
	})
}

func ProjectViewDigest(value ProjectView) (string, error) {
	return ProjectViewDigestContext(context.Background(), value)
}

func ProjectViewDigestContext(ctx context.Context, value ProjectView) (string, error) {
	return DigestContext(ctx, ProjectViewIdentity{
		SchemaVersion:           value.SchemaVersion,
		ProjectID:               value.ProjectID,
		Generation:              value.Generation,
		StartedAt:               value.StartedAt,
		EndedAt:                 value.EndedAt,
		SourceSessions:          value.SourceSessions,
		TerminalCounts:          value.TerminalCounts,
		SessionViewDependencies: value.SessionViewDependencies,
		ObservationRevisionIDs:  value.ObservationRevisionIDs,
		AggregationCoverage:     value.AggregationCoverage,
		ProbeStateDigest:        value.ProbeStateDigest,
		LiveState:               value.LiveState,
		WitnessedState:          value.WitnessedState,
		DerivedRecords:          value.DerivedRecords,
		AssociatedUsage:         value.AssociatedUsage,
		PreviousViewDigest:      value.PreviousViewDigest,
		DependencyDigest:        value.DependencyDigest,
		ReducerVersion:          value.ReducerVersion,
	})
}

type digestVisit struct {
	typeOf reflect.Type
	ptr    uintptr
}

func digestCheckpoint(checkpoints []func() error) error {
	if len(checkpoints) == 0 || checkpoints[0] == nil {
		return nil
	}
	return checkpoints[0]()
}

type digestWriter interface {
	Write([]byte) (int, error)
}
