// Package sessionview deterministically materializes project-scoped Session
// observations into durable views without opening source transcripts or using a
// model.
package sessionview

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/memory"
)

const MaterializerVersion = "session-view-v1"

// Input binds one frozen catalog source to the exact active observations and
// immutable chunks selected for a project generation. UsageRecordDigest is a
// reference to SourceCatalog; the usage payload itself is never copied here.
type Input struct {
	ProjectID               string
	Source                  memory.SourceRecord
	SourceRecordDigest      string
	UsageRecordDigest       string
	Observations            []memory.ObservationRevision
	ObservationChunkDigests []string
	TerminalState           memory.TerminalState
	Diagnostics             []memory.Diagnostic
	Previous                *memory.SessionView
	MaterializerVersion     string
}

// Materialize constructs a deterministic SessionView. The returned bool is
// false only when the prior view is byte-identical by canonical self identity.
func Materialize(input Input) (memory.SessionView, bool, error) {
	if err := validateInput(input); err != nil {
		return memory.SessionView{}, false, err
	}

	observations := append([]memory.ObservationRevision(nil), input.Observations...)
	sort.Slice(observations, func(i, j int) bool {
		if observations[i].Key.Sequence != observations[j].Key.Sequence {
			return observations[i].Key.Sequence < observations[j].Key.Sequence
		}
		return observations[i].RevisionID < observations[j].RevisionID
	})
	observations = deduplicateRevisions(observations)

	activeRevisionIDs := revisionIDs(observations)
	chunkDigests := sortedUniqueStrings(input.ObservationChunkDigests)
	derivedRecords := deriveRecoveryLinks(observations)
	diagnostics := sortedUniqueDiagnostics(input.Diagnostics)
	if input.TerminalState == memory.Missing && input.Previous != nil {
		activeRevisionIDs = append([]string(nil), input.Previous.ActiveRevisionIDs...)
		chunkDigests = append([]string(nil), input.Previous.ObservationChunkDigests...)
		derivedRecords = cloneDerivedRecords(input.Previous.DerivedRecords)
		diagnostics = sortedUniqueDiagnostics(append(append([]memory.Diagnostic(nil), input.Previous.Diagnostics...), diagnostics...))
	}

	availabilityDigest, err := memory.Digest(struct {
		Provider       string                    `json:"provider"`
		SessionID      string                    `json:"session_id"`
		SourceIdentity string                    `json:"source_identity"`
		FrozenBoundary memory.FrozenBoundary     `json:"frozen_boundary"`
		Availability   memory.SourceAvailability `json:"availability"`
		TerminalState  memory.TerminalState      `json:"terminal_state"`
	}{
		Provider: input.Source.Provider, SessionID: input.Source.SessionID,
		SourceIdentity: input.Source.SourceIdentity, FrozenBoundary: input.Source.FrozenBoundary,
		Availability: input.Source.Availability, TerminalState: input.TerminalState,
	})
	if err != nil {
		return memory.SessionView{}, false, fmt.Errorf("digest frozen source availability: %w", err)
	}
	dependencyDigest, err := memory.Digest(struct {
		ActiveRevisionIDs        []string `json:"active_revision_ids"`
		SourceAvailabilityDigest string   `json:"source_availability_digest"`
		UsageRecordDigest        string   `json:"usage_record_digest"`
		MaterializerVersion      string   `json:"materializer_version"`
	}{
		ActiveRevisionIDs: activeRevisionIDs, SourceAvailabilityDigest: availabilityDigest,
		UsageRecordDigest: input.UsageRecordDigest, MaterializerVersion: MaterializerVersion,
	})
	if err != nil {
		return memory.SessionView{}, false, fmt.Errorf("digest SessionView dependencies: %w", err)
	}

	view := memory.SessionView{
		SchemaVersion:           memory.MemorySchemaVersion,
		ProjectID:               input.ProjectID,
		Provider:                input.Source.Provider,
		SessionID:               input.Source.SessionID,
		SourceRecordDigest:      input.SourceRecordDigest,
		StartedAt:               input.Source.StartedAt,
		EndedAt:                 input.Source.EndedAt,
		TerminalState:           input.TerminalState,
		SourceAvailability:      input.Source.Availability,
		ActiveRevisionIDs:       activeRevisionIDs,
		ObservationChunkDigests: chunkDigests,
		DerivedRecords:          derivedRecords,
		Diagnostics:             diagnostics,
		DependencyDigest:        dependencyDigest,
		MaterializerVersion:     MaterializerVersion,
	}
	view.Digest, err = memory.SessionViewDigest(view)
	if err != nil {
		return memory.SessionView{}, false, fmt.Errorf("digest SessionView: %w", err)
	}
	if err := memory.ValidateSessionView(view); err != nil {
		return memory.SessionView{}, false, fmt.Errorf("validate SessionView: %w", err)
	}
	if input.Previous != nil && input.Previous.Digest == view.Digest {
		return cloneSessionView(*input.Previous), false, nil
	}
	return view, true, nil
}

func validateInput(input Input) error {
	if input.MaterializerVersion != MaterializerVersion {
		return fmt.Errorf("unsupported materializer version %q", input.MaterializerVersion)
	}
	if err := memory.ValidateSourceRecord(input.Source); err != nil {
		return fmt.Errorf("validate source record: %w", err)
	}
	wantSourceDigest, err := memory.Digest(input.Source)
	if err != nil || input.SourceRecordDigest != wantSourceDigest {
		return errors.New("source record digest does not match source")
	}
	if !isDigest(input.UsageRecordDigest) {
		return errors.New("invalid usage record digest")
	}
	if !containsString(input.Source.ProjectIDs, input.ProjectID) {
		return errors.New("source is not associated with target project")
	}
	if !validTerminalState(input.TerminalState) {
		return errors.New("invalid terminal state")
	}
	if input.TerminalState == memory.Missing && input.Source.Availability != memory.SourceUnavailable {
		return errors.New("missing source must be unavailable")
	}
	if input.TerminalState == memory.Missing && (len(input.Observations) != 0 || len(input.ObservationChunkDigests) != 0) {
		return errors.New("missing source cannot claim fresh observations")
	}
	if input.Source.Availability == memory.SourceUnavailable && input.TerminalState != memory.Missing {
		return errors.New("unavailable source must use missing terminal state")
	}
	if len(input.Observations) > 65536 || len(input.ObservationChunkDigests) > 65536 {
		return errors.New("too many SessionView dependencies")
	}
	for _, digest := range input.ObservationChunkDigests {
		if !isDigest(digest) {
			return errors.New("invalid observation chunk digest")
		}
	}
	for _, observation := range input.Observations {
		if err := memory.ValidateObservationRevision(observation); err != nil {
			return fmt.Errorf("validate active observation: %w", err)
		}
		if observation.Key.ProjectID != input.ProjectID || observation.Key.Provider != input.Source.Provider ||
			observation.Key.SessionID != input.Source.SessionID || observation.Key.SourceIdentity != input.Source.SourceIdentity {
			return errors.New("active observation does not belong to source and project")
		}
	}
	if input.Previous != nil {
		if err := memory.ValidateSessionView(*input.Previous); err != nil {
			return fmt.Errorf("validate previous SessionView: %w", err)
		}
		if input.Previous.ProjectID != input.ProjectID || input.Previous.Provider != input.Source.Provider || input.Previous.SessionID != input.Source.SessionID {
			return errors.New("previous SessionView does not belong to source and project")
		}
	}
	return nil
}

func deduplicateRevisions(values []memory.ObservationRevision) []memory.ObservationRevision {
	seen := make(map[string]struct{}, len(values))
	result := values[:0]
	for _, value := range values {
		if _, duplicate := seen[value.RevisionID]; duplicate {
			continue
		}
		seen[value.RevisionID] = struct{}{}
		result = append(result, value)
	}
	return result
}

func revisionIDs(values []memory.ObservationRevision) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.RevisionID
	}
	return result
}

func deriveRecoveryLinks(observations []memory.ObservationRevision) []memory.DerivedRecord {
	pending := make(map[string][]memory.ObservationRevision)
	var result []memory.DerivedRecord
	for _, observation := range observations {
		operation := normalizedIdentity(firstNonEmpty(observation.Fields["status"], observation.Operation))
		component := normalizedIdentity(observation.Fields["component"])
		if operation == "" || component == "" {
			continue
		}
		key := operation + "\x00" + component
		switch normalizedIdentity(observation.Outcome) {
		case "failure", "failed", "error":
			pending[key] = append(pending[key], observation)
		case "success", "passed":
			failures := pending[key]
			remaining := failures[:0]
			for _, failure := range failures {
				if failure.Key.Sequence < observation.Key.Sequence {
					result = append(result, recoveryRecord(failure, observation, operation, component))
				} else {
					remaining = append(remaining, failure)
				}
			}
			if len(remaining) == 0 {
				delete(pending, key)
			} else {
				pending[key] = remaining
			}
		}
	}
	return result
}

func recoveryRecord(failure, success memory.ObservationRevision, operation, component string) memory.DerivedRecord {
	identityDigest, _ := memory.Digest(struct {
		Failure string `json:"failure"`
		Success string `json:"success"`
		Rule    string `json:"rule"`
	}{Failure: failure.RevisionID, Success: success.RevisionID, Rule: "matching-operation-component"})
	subject := operation + ":" + component
	if len(subject) > 256 || utf8.RuneCountInString(subject) > 256 {
		subject = "recovery:" + strings.TrimPrefix(identityDigest, "sha256:")[:16]
	}
	return memory.DerivedRecord{
		ID:                    "recovery-" + strings.TrimPrefix(identityDigest, "sha256:")[:32],
		Kind:                  "recovery_link",
		Subject:               subject,
		OccurredAt:            success.Timestamp,
		DependencyRevisionIDs: []string{failure.RevisionID, success.RevisionID},
		RuleID:                "matching-operation-component",
		RuleVersion:           MaterializerVersion,
		Fields: map[string]string{
			"operation": operation,
			"component": component,
			"outcome":   "recovered",
		},
	}
}

func sortedUniqueStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	write := 0
	for _, value := range result {
		if write > 0 && result[write-1] == value {
			continue
		}
		result[write] = value
		write++
	}
	return result[:write]
}

func sortedUniqueDiagnostics(values []memory.Diagnostic) []memory.Diagnostic {
	result := append([]memory.Diagnostic(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		left := result[i].Code + "\x00" + result[i].Path + "\x00" + result[i].DetailHash
		right := result[j].Code + "\x00" + result[j].Path + "\x00" + result[j].DetailHash
		return left < right
	})
	write := 0
	for _, value := range result {
		if write > 0 && result[write-1] == value {
			continue
		}
		result[write] = value
		write++
	}
	return result[:write]
}

func cloneDerivedRecords(values []memory.DerivedRecord) []memory.DerivedRecord {
	if values == nil {
		return nil
	}
	result := make([]memory.DerivedRecord, len(values))
	for index, value := range values {
		result[index] = value
		result[index].DependencyRevisionIDs = append([]string(nil), value.DependencyRevisionIDs...)
		if value.Fields != nil {
			result[index].Fields = make(map[string]string, len(value.Fields))
			for key, field := range value.Fields {
				result[index].Fields[key] = field
			}
		}
	}
	return result
}

func cloneSessionView(value memory.SessionView) memory.SessionView {
	value.ActiveRevisionIDs = append([]string(nil), value.ActiveRevisionIDs...)
	value.ObservationChunkDigests = append([]string(nil), value.ObservationChunkDigests...)
	value.DerivedRecords = cloneDerivedRecords(value.DerivedRecords)
	value.Diagnostics = append([]memory.Diagnostic(nil), value.Diagnostics...)
	return value
}

func normalizedIdentity(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func validTerminalState(value memory.TerminalState) bool {
	switch value {
	case memory.Indexed, memory.Unsupported, memory.Missing, memory.Unreadable, memory.Ambiguous:
		return true
	default:
		return false
	}
}

func isDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
