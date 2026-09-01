package memory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/accounting"
)

const (
	MemorySchemaVersion = 1
	maxSafeInteger      = 1<<53 - 1
	maxExcerptBytes     = 1024
)

var (
	safeIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	fieldNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	digestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	gitObjectPattern  = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	semanticOnlyKinds = map[string]struct{}{
		"analysis": {}, "decision": {}, "intent": {}, "rationale": {},
		"recommendation": {}, "semantic": {}, "summary": {},
	}
	directObservationKinds = map[string]struct{}{
		"artifact": {}, "branch": {}, "build": {}, "command": {},
		"commit": {}, "deployment": {}, "error": {}, "file": {},
		"git_status": {}, "lint": {}, "release": {}, "request": {},
		"sync": {}, "tag": {}, "test": {}, "tool": {},
		"verification": {}, "version": {},
	}
	observationFieldNames = map[string]struct{}{
		"artifact_id": {}, "branch": {}, "command_signature": {}, "component": {},
		"duration_ms": {}, "error_signature": {}, "exit_code": {}, "failed": {},
		"file_hash": {}, "git_head": {}, "model": {}, "passed": {},
		"path": {}, "remote_hash": {}, "release_id": {}, "skipped": {},
		"status": {}, "tag": {}, "target": {}, "tool_id": {}, "version": {},
	}
	rawConversationFields = map[string]struct{}{
		"analysis": {}, "assistant_message": {}, "decision": {}, "full_transcript": {},
		"intent": {}, "prompt": {}, "rationale": {}, "raw_tool_output": {},
		"recommendation": {}, "semantic": {}, "summary": {}, "tool_output": {},
		"transcript": {}, "user_message": {},
	}
)

type TerminalState string

const (
	Indexed     TerminalState = "indexed"
	Unsupported TerminalState = "unsupported"
	Missing     TerminalState = "missing"
	Unreadable  TerminalState = "unreadable"
	Ambiguous   TerminalState = "ambiguous"
)

type SourceAvailability string

const (
	SourceAvailable   SourceAvailability = "available"
	SourceUnavailable SourceAvailability = "source_unavailable"
)

type SourceLocationKind string

const SourceLocationJSONL SourceLocationKind = "jsonl"

type JSONLSourceLocation struct {
	Line       int   `json:"line"`
	ByteOffset int64 `json:"byte_offset"`
}

type SourceLocation struct {
	Kind  SourceLocationKind   `json:"kind"`
	JSONL *JSONLSourceLocation `json:"jsonl,omitempty"`
}

type FrozenBoundary struct {
	Location   SourceLocation `json:"source_location"`
	SourceHash string         `json:"source_hash"`
}

type SourceRecord struct {
	SchemaVersion  int                     `json:"schema_version"`
	Provider       string                  `json:"provider"`
	SessionID      string                  `json:"session_id"`
	SourceIdentity string                  `json:"source_identity"`
	StartedAt      string                  `json:"started_at"`
	EndedAt        string                  `json:"ended_at"`
	FrozenBoundary FrozenBoundary          `json:"frozen_boundary"`
	Availability   SourceAvailability      `json:"availability"`
	Usage          accounting.SessionUsage `json:"usage"`
	ProjectIDs     []string                `json:"project_ids"`
}

type SourceRef struct {
	Provider       string         `json:"provider"`
	SessionID      string         `json:"session_id"`
	SourceIdentity string         `json:"source_identity"`
	Location       SourceLocation `json:"source_location"`
	SourceHash     string         `json:"source_hash"`
}

type ObservationKey struct {
	Provider       string `json:"provider"`
	SessionID      string `json:"session_id"`
	SourceIdentity string `json:"source_identity"`
	Sequence       int    `json:"sequence"`
	ProjectID      string `json:"project_id"`
	Kind           string `json:"kind"`
	Subject        string `json:"subject"`
}

type ObservationRevision struct {
	SchemaVersion  int               `json:"schema_version"`
	Key            ObservationKey    `json:"key"`
	RevisionID     string            `json:"revision_id"`
	Ref            SourceRef         `json:"source_ref"`
	Timestamp      string            `json:"timestamp"`
	Operation      string            `json:"operation,omitempty"`
	Object         string            `json:"object,omitempty"`
	Outcome        string            `json:"outcome,omitempty"`
	Fields         map[string]string `json:"fields,omitempty"`
	Excerpt        string            `json:"excerpt,omitempty"`
	AdapterID      string            `json:"adapter_id"`
	AdapterVersion string            `json:"adapter_version"`
}

type Diagnostic struct {
	Code       string `json:"code"`
	Path       string `json:"path,omitempty"`
	DetailHash string `json:"detail_hash,omitempty"`
}

type DerivedRecord struct {
	ID                    string            `json:"id"`
	Kind                  string            `json:"kind"`
	Subject               string            `json:"subject"`
	OccurredAt            string            `json:"occurred_at,omitempty"`
	DependencyRevisionIDs []string          `json:"dependency_revision_ids"`
	RuleID                string            `json:"rule_id"`
	RuleVersion           string            `json:"rule_version"`
	Fields                map[string]string `json:"fields,omitempty"`
}

// ObservationSummary is compact, dependency-bound materialized data for view
// reduction. It deliberately excludes source coordinates, adapter identity,
// usage payloads, and complete conversation or tool output.
type ObservationSummary struct {
	RevisionID string            `json:"revision_id"`
	Sequence   int               `json:"sequence"`
	Kind       string            `json:"kind"`
	Subject    string            `json:"subject"`
	OccurredAt string            `json:"occurred_at"`
	Operation  string            `json:"operation,omitempty"`
	Object     string            `json:"object,omitempty"`
	Outcome    string            `json:"outcome,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"`
	Excerpt    string            `json:"excerpt,omitempty"`
}

type SessionView struct {
	SchemaVersion           int                  `json:"schema_version"`
	Digest                  string               `json:"digest"`
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

type ProbeFile struct {
	Path        string `json:"path"`
	Exists      bool   `json:"exists"`
	ContentHash string `json:"content_hash,omitempty"`
}

type ProjectProbeState struct {
	SchemaVersion           int          `json:"schema_version"`
	Digest                  string       `json:"digest"`
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

type ProbeCheck struct {
	SchemaVersion int          `json:"schema_version"`
	CheckedAt     string       `json:"checked_at"`
	StateDigest   string       `json:"state_digest"`
	Available     bool         `json:"available"`
	Diagnostics   []Diagnostic `json:"diagnostics"`
}

type TerminalCounts struct {
	Indexed     int `json:"indexed"`
	Unsupported int `json:"unsupported"`
	Missing     int `json:"missing"`
	Unreadable  int `json:"unreadable"`
	Ambiguous   int `json:"ambiguous"`
}

type SessionViewDependency struct {
	Provider  string `json:"provider"`
	SessionID string `json:"session_id"`
	Digest    string `json:"digest"`
}

type StateSnapshot struct {
	Branch         string `json:"branch,omitempty"`
	Head           string `json:"head,omitempty"`
	DirtyPathCount int    `json:"dirty_path_count"`
}

type AssociatedUsage struct {
	Provider          string `json:"provider"`
	SessionID         string `json:"session_id"`
	UsageRecordDigest string `json:"usage_record_digest"`
	Shared            bool   `json:"shared"`
}

type ProjectView struct {
	SchemaVersion           int                     `json:"schema_version"`
	Digest                  string                  `json:"digest"`
	ProjectID               string                  `json:"project_id"`
	Generation              int                     `json:"generation"`
	StartedAt               string                  `json:"started_at"`
	EndedAt                 string                  `json:"ended_at"`
	SourceSessions          int                     `json:"source_sessions"`
	TerminalCounts          TerminalCounts          `json:"terminal_counts"`
	SessionViewDependencies []SessionViewDependency `json:"session_view_dependencies"`
	ObservationRevisionIDs  []string                `json:"observation_revision_ids"`
	ProbeStateDigest        string                  `json:"probe_state_digest"`
	LiveState               StateSnapshot           `json:"live_state"`
	WitnessedState          []DerivedRecord         `json:"witnessed_state"`
	DerivedRecords          []DerivedRecord         `json:"derived_records"`
	AssociatedUsage         []AssociatedUsage       `json:"associated_usage"`
	PreviousViewDigest      string                  `json:"previous_view_digest,omitempty"`
	DependencyDigest        string                  `json:"dependency_digest"`
	ReducerVersion          string                  `json:"reducer_version"`
}

type GenerationManifest struct {
	SchemaVersion           int                     `json:"schema_version"`
	GenerationID            string                  `json:"generation_id"`
	ProjectID               string                  `json:"project_id"`
	CreatedAt               string                  `json:"created_at"`
	SourceRecordDigests     []string                `json:"source_record_digests"`
	ObservationChunkDigests []string                `json:"observation_chunk_digests"`
	SessionViews            []SessionViewDependency `json:"session_views"`
	ProbeStateDigest        string                  `json:"probe_state_digest"`
	ProbeCheck              ProbeCheck              `json:"probe_check"`
	ProjectViewDigest       string                  `json:"project_view_digest"`
	ActiveRevisions         map[string]string       `json:"active_revisions"`
	SupersededRevisions     map[string]string       `json:"superseded_revisions"`
	WithdrawnRevisions      map[string]string       `json:"withdrawn_revisions"`
}

func ValidateSourceRecord(value SourceRecord) error {
	if err := validateVersion(value.SchemaVersion); err != nil {
		return err
	}
	if err := validateSourceIdentity(value.Provider, value.SessionID, value.SourceIdentity); err != nil {
		return err
	}
	if err := validateTimeRange(value.StartedAt, value.EndedAt); err != nil {
		return err
	}
	if err := validateSourceLocation(value.Provider, value.FrozenBoundary.Location); err != nil {
		return fmt.Errorf("invalid frozen source boundary: %w", err)
	}
	if !sha256Pattern.MatchString(value.FrozenBoundary.SourceHash) {
		return errors.New("invalid frozen source boundary")
	}
	if !validSourceAvailability(value.Availability) {
		return errors.New("invalid source availability")
	}
	if err := accounting.ValidateSessionUsage(&value.Usage); err != nil {
		return fmt.Errorf("invalid source usage: %w", err)
	}
	if value.Usage.StartedAt != value.StartedAt || value.Usage.EndedAt != value.EndedAt {
		return errors.New("source time range does not match usage")
	}
	if err := validateUniqueSafeIDs("project", value.ProjectIDs, 4096); err != nil {
		return err
	}
	return nil
}

func ValidateObservationRevision(value ObservationRevision) error {
	return ValidateObservationRevisionContext(context.Background(), value)
}

func ValidateObservationRevisionContext(ctx context.Context, value ObservationRevision) error {
	checkpoints, err := memoryValidationCheckpoints(ctx)
	if err != nil {
		return err
	}
	if err := digestCheckpoint(checkpoints); err != nil {
		return err
	}
	if err := validateVersion(value.SchemaVersion); err != nil {
		return err
	}
	if err := validateObservationKey(value.Key); err != nil {
		return err
	}
	if err := validateSourceRef(value.Ref); err != nil {
		return err
	}
	if value.Ref.Provider != value.Key.Provider || value.Ref.SessionID != value.Key.SessionID || value.Ref.SourceIdentity != value.Key.SourceIdentity {
		return errors.New("observation key and source reference disagree")
	}
	if err := validateTimestamp(value.Timestamp, false); err != nil {
		return fmt.Errorf("invalid observation timestamp: %w", err)
	}
	for name, text := range map[string]string{"operation": value.Operation, "object": value.Object, "outcome": value.Outcome} {
		if err := validateStructuredText(name, text, 512, true); err != nil {
			return err
		}
	}
	if err := validateObservationFields(value.Fields); err != nil {
		return err
	}
	if err := validateBoundedText("excerpt", value.Excerpt, maxExcerptBytes, true); err != nil {
		return err
	}
	if !safeIDPattern.MatchString(value.AdapterID) || !safeIDPattern.MatchString(value.AdapterVersion) {
		return errors.New("invalid adapter identity")
	}
	want := ObservationRevisionIDContext(ctx, value)
	if err := digestCheckpoint(checkpoints); err != nil {
		return err
	}
	if want == "" || value.RevisionID != want {
		return errors.New("observation revision_id does not match canonical identity")
	}
	return nil
}

func ValidateSessionView(value SessionView) error {
	return ValidateSessionViewContext(context.Background(), value)
}

func ValidateSessionViewContext(ctx context.Context, value SessionView) error {
	checkpoints, err := memoryValidationCheckpoints(ctx)
	if err != nil {
		return err
	}
	if err := digestCheckpoint(checkpoints); err != nil {
		return err
	}
	if err := validateVersion(value.SchemaVersion); err != nil {
		return err
	}
	if !validDigest(value.Digest) || !validDigest(value.SourceRecordDigest) || !validDigest(value.UsageRecordDigest) || !validDigest(value.DependencyDigest) {
		return errors.New("invalid SessionView digest")
	}
	if value.UsageRecordDigest != value.SourceRecordDigest {
		return errors.New("SessionView usage record is not authenticated by source catalog record")
	}
	if err := validateSourceIdentity(value.Provider, value.SessionID, value.SourceIdentity); err != nil {
		return err
	}
	if !safeIDPattern.MatchString(value.ProjectID) || !validTerminalState(value.TerminalState) || !validSourceAvailability(value.SourceAvailability) {
		return errors.New("invalid SessionView identity or state")
	}
	if !validTerminalAvailability(value.TerminalState, value.SourceAvailability) {
		return errors.New("SessionView terminal state and source availability disagree")
	}
	if err := validateTimeRange(value.StartedAt, value.EndedAt); err != nil {
		return err
	}
	if err := validateUniqueDigests("active revision", value.ActiveRevisionIDs, 65536, checkpoints...); err != nil {
		return err
	}
	if err := validateObservationSummaries(value.ObservationSummaries, value.ActiveRevisionIDs, checkpoints...); err != nil {
		return err
	}
	if err := validateUniqueDigests("observation chunk", value.ObservationChunkDigests, 65536, checkpoints...); err != nil {
		return err
	}
	if err := validateDerivedRecords(value.DerivedRecords, checkpoints...); err != nil {
		return err
	}
	if err := validateDerivedDependencyClosure(value.DerivedRecords, value.ActiveRevisionIDs, "active revision", checkpoints...); err != nil {
		return err
	}
	if err := validateDiagnostics(value.Diagnostics, checkpoints...); err != nil {
		return err
	}
	if !safeIDPattern.MatchString(value.MaterializerVersion) {
		return errors.New("invalid materializer version")
	}
	want, err := SessionViewDigestContext(ctx, value)
	if cause := digestCheckpoint(checkpoints); cause != nil {
		return cause
	}
	if err != nil || value.Digest != want {
		return errors.New("SessionView self digest does not match canonical identity")
	}
	return nil
}

func validateObservationSummaries(values []ObservationSummary, activeRevisionIDs []string, checkpoints ...func() error) error {
	if len(values) != len(activeRevisionIDs) {
		return errors.New("observation summary count does not match active revisions")
	}
	stableKeys := make(map[string]struct{}, len(values))
	for index, value := range values {
		if err := digestCheckpoint(checkpoints); err != nil {
			return err
		}
		if value.RevisionID != activeRevisionIDs[index] || !validDigest(value.RevisionID) {
			return errors.New("observation summary revision does not match active revision order")
		}
		if value.Sequence < 0 || value.Sequence > maxSafeInteger || !fieldNamePattern.MatchString(value.Kind) {
			return errors.New("invalid observation summary identity")
		}
		if _, observed := directObservationKinds[value.Kind]; !observed {
			return fmt.Errorf("observation summary kind %q is not directly observed", value.Kind)
		}
		if err := validateStructuredText("observation summary subject", value.Subject, 256, false); err != nil {
			return err
		}
		if err := validateTimestamp(value.OccurredAt, false); err != nil {
			return fmt.Errorf("invalid observation summary time: %w", err)
		}
		for name, text := range map[string]string{"operation": value.Operation, "object": value.Object, "outcome": value.Outcome} {
			if err := validateStructuredText(name, text, 512, true); err != nil {
				return err
			}
		}
		if err := validateObservationFields(value.Fields); err != nil {
			return err
		}
		if err := validateBoundedText("excerpt", value.Excerpt, maxExcerptBytes, true); err != nil {
			return err
		}
		if index > 0 {
			previous := values[index-1]
			if previous.Sequence > value.Sequence || (previous.Sequence == value.Sequence && previous.RevisionID >= value.RevisionID) {
				return errors.New("observation summaries are not in canonical sequence and revision order")
			}
		}
		stableKey := fmt.Sprintf("%d\x00%s\x00%s", value.Sequence, value.Kind, value.Subject)
		if _, duplicate := stableKeys[stableKey]; duplicate {
			return errors.New("multiple active revisions share one stable observation key")
		}
		stableKeys[stableKey] = struct{}{}
	}
	return nil
}

func ValidateProjectProbeState(value ProjectProbeState) error {
	return ValidateProjectProbeStateContext(context.Background(), value)
}

func ValidateProjectProbeStateContext(ctx context.Context, value ProjectProbeState) error {
	checkpoints, err := memoryValidationCheckpoints(ctx)
	if err != nil {
		return err
	}
	if err := digestCheckpoint(checkpoints); err != nil {
		return err
	}
	if err := validateVersion(value.SchemaVersion); err != nil {
		return err
	}
	if !validDigest(value.Digest) || !safeIDPattern.MatchString(value.ProjectID) || !safeIDPattern.MatchString(value.ProbeVersion) {
		return errors.New("invalid ProjectProbeState identity")
	}
	if err := validateBoundedText("canonical root", value.CanonicalRoot, 4096, false); err != nil {
		return err
	}
	if err := validateBoundedText("branch", value.Branch, 512, true); err != nil {
		return err
	}
	if value.Head != "" && !gitObjectPattern.MatchString(value.Head) {
		return errors.New("invalid Git HEAD")
	}
	if value.DirtyPathCount < 0 || value.DirtyPathCount > maxSafeInteger {
		return errors.New("invalid dirty path count")
	}
	if err := validateUniqueDigests("remote identity", value.RemoteIdentityHashes, 256, checkpoints...); err != nil {
		return err
	}
	if err := validateProbeFiles(value.VersionFiles, checkpoints...); err != nil {
		return fmt.Errorf("invalid version files: %w", err)
	}
	if err := validateProbeFiles(value.RequiredProjectionFiles, checkpoints...); err != nil {
		return fmt.Errorf("invalid required projection files: %w", err)
	}
	if err := validateDiagnostics(value.Diagnostics, checkpoints...); err != nil {
		return err
	}
	want, err := ProjectProbeStateDigestContext(ctx, value)
	if cause := digestCheckpoint(checkpoints); cause != nil {
		return cause
	}
	if err != nil || value.Digest != want {
		return errors.New("ProjectProbeState self digest does not match canonical identity")
	}
	return nil
}

func ValidateProbeCheck(value ProbeCheck) error {
	return ValidateProbeCheckContext(context.Background(), value)
}

func ValidateProbeCheckContext(ctx context.Context, value ProbeCheck) error {
	checkpoints, err := memoryValidationCheckpoints(ctx)
	if err != nil {
		return err
	}
	if err := digestCheckpoint(checkpoints); err != nil {
		return err
	}
	if err := validateVersion(value.SchemaVersion); err != nil {
		return err
	}
	if err := validateTimestamp(value.CheckedAt, true); err != nil {
		return fmt.Errorf("invalid probe check time: %w", err)
	}
	if !validDigest(value.StateDigest) {
		return errors.New("invalid probe state digest")
	}
	return validateDiagnostics(value.Diagnostics, checkpoints...)
}

func ValidateProjectView(value ProjectView) error {
	return ValidateProjectViewContext(context.Background(), value)
}

func ValidateProjectViewContext(ctx context.Context, value ProjectView) error {
	checkpoints, err := memoryValidationCheckpoints(ctx)
	if err != nil {
		return err
	}
	if err := digestCheckpoint(checkpoints); err != nil {
		return err
	}
	if err := validateVersion(value.SchemaVersion); err != nil {
		return err
	}
	if !validDigest(value.Digest) || !safeIDPattern.MatchString(value.ProjectID) || value.Generation < 1 || value.Generation > maxSafeInteger {
		return errors.New("invalid ProjectView identity")
	}
	if err := validateTimeRange(value.StartedAt, value.EndedAt); err != nil {
		return err
	}
	if value.SourceSessions < 0 || value.SourceSessions > maxSafeInteger || value.TerminalCounts.total() != value.SourceSessions {
		return errors.New("impossible terminal counts")
	}
	if len(value.SessionViewDependencies) != value.SourceSessions {
		return errors.New("SessionView dependency count does not match terminal sessions")
	}
	if err := validateSessionDependencies(value.SessionViewDependencies, value.SourceSessions, checkpoints...); err != nil {
		return err
	}
	if err := validateUniqueDigests("observation revision", value.ObservationRevisionIDs, 1_000_000, checkpoints...); err != nil {
		return err
	}
	if !validDigest(value.ProbeStateDigest) || !validDigest(value.DependencyDigest) || (value.PreviousViewDigest != "" && !validDigest(value.PreviousViewDigest)) {
		return errors.New("invalid ProjectView dependency digest")
	}
	if err := validateStateSnapshot(value.LiveState); err != nil {
		return err
	}
	if err := validateDerivedRecords(value.WitnessedState, checkpoints...); err != nil {
		return err
	}
	if err := validateDerivedRecords(value.DerivedRecords, checkpoints...); err != nil {
		return err
	}
	if err := validateDerivedDependencyClosure(value.WitnessedState, value.ObservationRevisionIDs, "selected observation", checkpoints...); err != nil {
		return err
	}
	if err := validateDerivedDependencyClosure(value.DerivedRecords, value.ObservationRevisionIDs, "selected observation", checkpoints...); err != nil {
		return err
	}
	witnessedIDs := make(map[string]struct{}, len(value.WitnessedState))
	for _, record := range value.WitnessedState {
		if err := digestCheckpoint(checkpoints); err != nil {
			return err
		}
		witnessedIDs[record.ID] = struct{}{}
	}
	for _, record := range value.DerivedRecords {
		if err := digestCheckpoint(checkpoints); err != nil {
			return err
		}
		if _, duplicate := witnessedIDs[record.ID]; duplicate {
			return fmt.Errorf("duplicate ProjectView derived record ID %q", record.ID)
		}
	}
	if err := validateAssociatedUsage(value.AssociatedUsage, checkpoints...); err != nil {
		return err
	}
	if !safeIDPattern.MatchString(value.ReducerVersion) {
		return errors.New("invalid reducer version")
	}
	want, err := ProjectViewDigestContext(ctx, value)
	if cause := digestCheckpoint(checkpoints); cause != nil {
		return cause
	}
	if err != nil || value.Digest != want {
		return errors.New("ProjectView self digest does not match canonical identity")
	}
	return nil
}

func ValidateGenerationManifest(value GenerationManifest) error {
	return ValidateGenerationManifestContext(context.Background(), value)
}

func ValidateGenerationManifestContext(ctx context.Context, value GenerationManifest) error {
	checkpoints, err := memoryValidationCheckpoints(ctx)
	if err != nil {
		return err
	}
	if err := digestCheckpoint(checkpoints); err != nil {
		return err
	}
	if err := validateVersion(value.SchemaVersion); err != nil {
		return err
	}
	if !safeIDPattern.MatchString(value.GenerationID) || !safeIDPattern.MatchString(value.ProjectID) {
		return errors.New("invalid generation identity")
	}
	if err := validateTimestamp(value.CreatedAt, true); err != nil {
		return fmt.Errorf("invalid generation time: %w", err)
	}
	if err := validateUniqueDigests("source record", value.SourceRecordDigests, 65536, checkpoints...); err != nil {
		return err
	}
	if err := validateUniqueDigests("observation chunk", value.ObservationChunkDigests, 65536, checkpoints...); err != nil {
		return err
	}
	if len(value.SessionViews) != len(value.SourceRecordDigests) {
		return errors.New("SessionView dependency count does not match frozen sources")
	}
	if err := validateSessionDependencies(value.SessionViews, len(value.SessionViews), checkpoints...); err != nil {
		return err
	}
	if !validDigest(value.ProbeStateDigest) || !validDigest(value.ProjectViewDigest) {
		return errors.New("invalid generation object digest")
	}
	if err := ValidateProbeCheckContext(ctx, value.ProbeCheck); err != nil {
		return err
	}
	if value.ProbeCheck.StateDigest != value.ProbeStateDigest {
		return errors.New("probe check does not reference generation probe state")
	}
	active := make(map[string]struct{}, len(value.ActiveRevisions))
	for key, revision := range value.ActiveRevisions {
		if err := digestCheckpoint(checkpoints); err != nil {
			return err
		}
		if !validDigest(key) || !validDigest(revision) {
			return errors.New("invalid active revision map")
		}
		if _, duplicate := active[revision]; duplicate {
			return errors.New("duplicate active revision ID")
		}
		active[revision] = struct{}{}
	}
	for previous, successor := range value.SupersededRevisions {
		if err := digestCheckpoint(checkpoints); err != nil {
			return err
		}
		if !validDigest(previous) || !validDigest(successor) || previous == successor {
			return errors.New("invalid superseded revision map")
		}
		if _, selected := active[previous]; selected {
			return errors.New("inactive superseded revision selected as active")
		}
	}
	for key, revision := range value.WithdrawnRevisions {
		if err := digestCheckpoint(checkpoints); err != nil {
			return err
		}
		if !validDigest(key) || !validDigest(revision) {
			return errors.New("invalid withdrawn revision map")
		}
		if _, selected := active[revision]; selected {
			return errors.New("inactive withdrawn revision selected as active")
		}
	}
	return nil
}

func memoryValidationCheckpoints(ctx context.Context) ([]func() error, error) {
	if ctx == nil {
		return nil, errors.New("memory validation context is required")
	}
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	return []func() error{func() error { return context.Cause(ctx) }}, nil
}

func (value TerminalCounts) total() int {
	counts := []int{value.Indexed, value.Unsupported, value.Missing, value.Unreadable, value.Ambiguous}
	total := 0
	for _, count := range counts {
		if count < 0 || count > maxSafeInteger-total {
			return -1
		}
		total += count
	}
	return total
}

func validateVersion(value int) error {
	if value != MemorySchemaVersion {
		return fmt.Errorf("schema_version must be exactly %d", MemorySchemaVersion)
	}
	return nil
}

func validateObservationKey(value ObservationKey) error {
	if err := validateSourceIdentity(value.Provider, value.SessionID, value.SourceIdentity); err != nil {
		return err
	}
	if value.Sequence < 0 || value.Sequence > maxSafeInteger || !safeIDPattern.MatchString(value.ProjectID) || !fieldNamePattern.MatchString(value.Kind) {
		return errors.New("invalid observation key")
	}
	if _, observed := directObservationKinds[value.Kind]; !observed {
		return fmt.Errorf("observation kind %q is not directly observed", value.Kind)
	}
	return validateStructuredText("observation subject", value.Subject, 256, false)
}

func validateSourceRef(value SourceRef) error {
	if err := validateSourceIdentity(value.Provider, value.SessionID, value.SourceIdentity); err != nil {
		return err
	}
	if err := validateSourceLocation(value.Provider, value.Location); err != nil {
		return err
	}
	if !sha256Pattern.MatchString(value.SourceHash) {
		return errors.New("invalid source reference")
	}
	return nil
}

func validateSourceIdentity(provider, sessionID, sourceIdentity string) error {
	if provider != "codex" {
		return fmt.Errorf("unsupported provider %q for private schema v1", provider)
	}
	if !safeIDPattern.MatchString(provider) || !safeIDPattern.MatchString(sessionID) || !safeIDPattern.MatchString(sourceIdentity) {
		return errors.New("invalid provider/session/source identity")
	}
	return nil
}

func validateSourceLocation(provider string, value SourceLocation) error {
	if provider != "codex" {
		return fmt.Errorf("unsupported provider %q for source location v1", provider)
	}
	if value.Kind != SourceLocationJSONL || value.JSONL == nil {
		return errors.New("Codex v1 requires an exact JSONL source location")
	}
	if value.JSONL.Line < 0 || value.JSONL.Line > maxSafeInteger || value.JSONL.ByteOffset < 0 || value.JSONL.ByteOffset > maxSafeInteger {
		return errors.New("invalid JSONL source coordinates")
	}
	return nil
}

func validateTimeRange(startedAt, endedAt string) error {
	start, err := parseTimestamp(startedAt)
	if err != nil {
		return errors.New("invalid start time")
	}
	end, err := parseTimestamp(endedAt)
	if err != nil || end.Before(start) {
		return errors.New("invalid end time")
	}
	return nil
}

func validateTimestamp(value string, requireUTC bool) error {
	parsed, err := parseTimestamp(value)
	if err != nil {
		return err
	}
	if requireUTC {
		_, offset := parsed.Zone()
		if offset != 0 {
			return errors.New("timestamp must be UTC")
		}
	}
	return nil
}

func parseTimestamp(value string) (time.Time, error) {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return time.Time{}, errors.New("invalid RFC3339Nano timestamp")
	}
	return time.Parse(time.RFC3339Nano, value)
}

func validateFields(fields map[string]string) error {
	if len(fields) > 64 {
		return errors.New("too many observation fields")
	}
	for name, value := range fields {
		if !fieldNamePattern.MatchString(name) {
			return fmt.Errorf("invalid field name %q", name)
		}
		if _, forbidden := rawConversationFields[name]; forbidden {
			return fmt.Errorf("raw conversation field %q is forbidden", name)
		}
		if err := validateStructuredText("field value", value, 512, true); err != nil {
			return err
		}
	}
	return nil
}

func validateObservationFields(fields map[string]string) error {
	if err := validateFields(fields); err != nil {
		return err
	}
	for name := range fields {
		if _, allowed := observationFieldNames[name]; !allowed {
			return fmt.Errorf("observation field %q is not a structured observed field", name)
		}
	}
	return nil
}

func validateDerivedRecords(records []DerivedRecord, checkpoints ...func() error) error {
	if len(records) > 65536 {
		return errors.New("too many derived records")
	}
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if err := digestCheckpoint(checkpoints); err != nil {
			return err
		}
		if !safeIDPattern.MatchString(record.ID) || !fieldNamePattern.MatchString(record.Kind) || !safeIDPattern.MatchString(record.RuleID) || !safeIDPattern.MatchString(record.RuleVersion) {
			return errors.New("invalid derived record identity")
		}
		if _, duplicate := seen[record.ID]; duplicate {
			return fmt.Errorf("duplicate derived record ID %q", record.ID)
		}
		seen[record.ID] = struct{}{}
		if _, forbidden := semanticOnlyKinds[record.Kind]; forbidden {
			return fmt.Errorf("semantic-only derived record kind %q is forbidden", record.Kind)
		}
		if err := validateBoundedText("derived subject", record.Subject, 256, false); err != nil {
			return err
		}
		if record.OccurredAt != "" {
			if err := validateTimestamp(record.OccurredAt, false); err != nil {
				return err
			}
		}
		if err := validateUniqueDigests("derived dependency", record.DependencyRevisionIDs, 4096, checkpoints...); err != nil {
			return err
		}
		if err := validateFields(record.Fields); err != nil {
			return err
		}
	}
	return nil
}

func validateDerivedDependencyClosure(records []DerivedRecord, selected []string, selectedName string, checkpoints ...func() error) error {
	allowed := make(map[string]struct{}, len(selected))
	for _, revisionID := range selected {
		if err := digestCheckpoint(checkpoints); err != nil {
			return err
		}
		allowed[revisionID] = struct{}{}
	}
	for _, record := range records {
		if err := digestCheckpoint(checkpoints); err != nil {
			return err
		}
		for _, dependency := range record.DependencyRevisionIDs {
			if err := digestCheckpoint(checkpoints); err != nil {
				return err
			}
			if _, exists := allowed[dependency]; !exists {
				return fmt.Errorf("derived record %q dependency is not an enclosing %s", record.ID, selectedName)
			}
		}
	}
	return nil
}

func validateDiagnostics(values []Diagnostic, checkpoints ...func() error) error {
	if len(values) > 4096 {
		return errors.New("too many diagnostics")
	}
	for _, value := range values {
		if err := digestCheckpoint(checkpoints); err != nil {
			return err
		}
		if !safeIDPattern.MatchString(value.Code) {
			return errors.New("invalid diagnostic code")
		}
		if err := validateBoundedText("diagnostic path", value.Path, 1024, true); err != nil {
			return err
		}
		if value.DetailHash != "" && !validDigest(value.DetailHash) {
			return errors.New("invalid diagnostic detail hash")
		}
	}
	return nil
}

func validateProbeFiles(values []ProbeFile, checkpoints ...func() error) error {
	if len(values) > 4096 {
		return errors.New("too many probe files")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := digestCheckpoint(checkpoints); err != nil {
			return err
		}
		if err := validateBoundedText("probe path", value.Path, 1024, false); err != nil {
			return err
		}
		if _, duplicate := seen[value.Path]; duplicate {
			return fmt.Errorf("duplicate probe path %q", value.Path)
		}
		seen[value.Path] = struct{}{}
		if value.Exists != (value.ContentHash != "") || (value.ContentHash != "" && !sha256Pattern.MatchString(value.ContentHash)) {
			return fmt.Errorf("probe path %q has inconsistent existence/hash", value.Path)
		}
	}
	return nil
}

func validateSessionDependencies(values []SessionViewDependency, maximum int, checkpoints ...func() error) error {
	if len(values) > maximum || len(values) > 65536 {
		return errors.New("too many SessionView dependencies")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := digestCheckpoint(checkpoints); err != nil {
			return err
		}
		if value.Provider != "codex" || !safeIDPattern.MatchString(value.SessionID) || !validDigest(value.Digest) {
			return errors.New("invalid SessionView dependency")
		}
		key := value.Provider + "\x00" + value.SessionID
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate SessionView dependency %q", value.SessionID)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateStateSnapshot(value StateSnapshot) error {
	if err := validateBoundedText("state branch", value.Branch, 512, true); err != nil {
		return err
	}
	if value.Head != "" && !gitObjectPattern.MatchString(value.Head) {
		return errors.New("invalid witnessed Git HEAD")
	}
	if value.DirtyPathCount < 0 || value.DirtyPathCount > maxSafeInteger {
		return errors.New("invalid witnessed dirty path count")
	}
	return nil
}

func validateAssociatedUsage(values []AssociatedUsage, checkpoints ...func() error) error {
	if len(values) > 65536 {
		return errors.New("too many associated usage rows")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := digestCheckpoint(checkpoints); err != nil {
			return err
		}
		if value.Provider != "codex" || !safeIDPattern.MatchString(value.SessionID) || !validDigest(value.UsageRecordDigest) {
			return errors.New("invalid associated usage row")
		}
		key := value.Provider + "\x00" + value.SessionID
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate associated usage row %q", value.SessionID)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateUniqueSafeIDs(name string, values []string, maximum int) error {
	if len(values) > maximum {
		return fmt.Errorf("too many %s IDs", name)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !safeIDPattern.MatchString(value) {
			return fmt.Errorf("invalid %s ID", name)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("duplicate %s ID", name)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateUniqueDigests(name string, values []string, maximum int, checkpoints ...func() error) error {
	if len(values) > maximum {
		return fmt.Errorf("too many %s digests", name)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := digestCheckpoint(checkpoints); err != nil {
			return err
		}
		if !validDigest(value) {
			return fmt.Errorf("invalid %s digest", name)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("duplicate %s ID", name)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateBoundedText(name, value string, maximum int, optional bool) error {
	if !utf8.ValidString(value) || (!optional && value == "") || len(value) > maximum {
		return fmt.Errorf("invalid or oversized %s", name)
	}
	return nil
}

func validateStructuredText(name, value string, maximum int, optional bool) error {
	if err := validateBoundedText(name, value, maximum, optional); err != nil {
		return err
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s must be structured single-line text", name)
		}
	}
	return nil
}

func validDigest(value string) bool {
	return digestPattern.MatchString(value)
}

func validTerminalState(value TerminalState) bool {
	switch value {
	case Indexed, Unsupported, Missing, Unreadable, Ambiguous:
		return true
	default:
		return false
	}
}

func validSourceAvailability(value SourceAvailability) bool {
	return value == SourceAvailable || value == SourceUnavailable
}

func validTerminalAvailability(state TerminalState, availability SourceAvailability) bool {
	if state == Missing {
		return availability == SourceUnavailable
	}
	if state == Indexed || state == Unsupported {
		return availability == SourceAvailable
	}
	return state == Unreadable || state == Ambiguous
}
