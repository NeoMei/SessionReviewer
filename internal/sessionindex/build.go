package sessionindex

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/memory"
)

const maxRenderedBytes = 64 << 20

var ErrCapacityExceeded = errors.New("session_index_capacity_exceeded")

type BuildInput struct {
	ProjectView  memory.ProjectView
	Manifest     memory.GenerationManifest
	SessionViews map[SessionKey]*memory.SessionView
	Previous     *Document
	GeneratedAt  time.Time
}

// Build creates the complete immutable public index for one private generation.
// Previous entries absent from the current ProjectView remain visible with their
// factual fields intact and only their source availability changed.
func Build(input BuildInput) (Document, error) {
	if input.GeneratedAt.IsZero() {
		return Document{}, errors.New("session index generation time is required")
	}
	if input.ProjectView.ProjectID == "" || input.ProjectView.ProjectID != input.Manifest.ProjectID || input.ProjectView.Digest != input.Manifest.ProjectViewDigest {
		return Document{}, errors.New("session index project binding mismatch")
	}
	measurements, err := measurementMap(input.Manifest.SessionIndexMeasurements)
	if err != nil {
		return Document{}, err
	}
	currentDependencies := make(map[SessionKey]string, len(input.Manifest.SessionViews))
	for _, dependency := range input.Manifest.SessionViews {
		key := SessionKey{Provider: dependency.Provider, SessionID: dependency.SessionID}
		if _, duplicate := currentDependencies[key]; duplicate {
			return Document{}, errors.New("duplicate manifest SessionView identity")
		}
		currentDependencies[key] = dependency.Digest
	}
	if len(input.ProjectView.SessionViewDependencies) != len(input.Manifest.SessionViews) {
		return Document{}, errors.New("ProjectView SessionViews do not match manifest dependencies")
	}
	for index, dependency := range input.ProjectView.SessionViewDependencies {
		manifestDependency := input.Manifest.SessionViews[index]
		if dependency != manifestDependency {
			return Document{}, errors.New("ProjectView SessionViews do not match manifest dependencies")
		}
	}
	if len(currentDependencies) != len(input.SessionViews) {
		return Document{}, errors.New("session index SessionViews do not match manifest dependencies")
	}
	previousEntries := map[SessionKey]Entry{}
	if input.Previous != nil {
		if len(input.Previous.Sessions) > 65536 {
			return Document{}, ErrCapacityExceeded
		}
		if err := Validate(*input.Previous); err != nil {
			return Document{}, fmt.Errorf("invalid previous session index: %w", err)
		}
		if input.Previous.ProjectID != input.Manifest.ProjectID {
			return Document{}, errors.New("previous session index belongs to another project")
		}
		for _, prior := range input.Previous.Sessions {
			key := SessionKey{Provider: prior.Provider, SessionID: prior.SessionID}
			if _, duplicate := previousEntries[key]; duplicate {
				return Document{}, errors.New("previous session index contains duplicate identity")
			}
			previousEntries[key] = prior
		}
	}
	next := make(map[SessionKey]Entry, len(input.SessionViews))
	for key, view := range input.SessionViews {
		if view == nil || key.Provider != view.Provider || key.SessionID != view.SessionID || view.ProjectID != input.Manifest.ProjectID {
			return Document{}, errors.New("SessionView identity does not match session index key")
		}
		if digest, authenticated := currentDependencies[key]; !authenticated || digest != view.Digest {
			return Document{}, errors.New("SessionView is not authenticated by manifest")
		}
		entry, err := buildEntry(*view, measurements[key], input.Manifest.GenerationID)
		if err != nil {
			return Document{}, fmt.Errorf("build session index entry %s/%s: %w", key.Provider, key.SessionID, err)
		}
		if prior, exists := previousEntries[key]; exists && view.SourceAvailability != memory.SourceAvailable {
			retained := cloneEntry(prior)
			retained.SourceAvailability = "unavailable"
			retained.SourceTerminalState = entry.SourceTerminalState
			retained.SessionViewDigest = entry.SessionViewDigest
			retained.UsageRecordDigest = entry.UsageRecordDigest
			retained.LastSeenGenerationID = stringPointer(input.Manifest.GenerationID)
			entry = retained
		}
		next[key] = entry
	}
	if input.Previous != nil {
		for key, prior := range previousEntries {
			if _, current := next[key]; current {
				continue
			}
			retained := cloneEntry(prior)
			retained.SourceAvailability = "unavailable"
			next[key] = retained
		}
	}
	if len(next) > 65536 {
		return Document{}, ErrCapacityExceeded
	}
	entries := make([]Entry, 0, len(next))
	for _, entry := range next {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return less(entries[i], entries[j]) })
	document := Document{
		SchemaVersion: 1, MinimumReaderVersion: "0.4.0",
		ProjectID: input.Manifest.ProjectID, GenerationID: input.Manifest.GenerationID,
		ProjectViewDigest: input.Manifest.ProjectViewDigest,
		GeneratedAt:       input.GeneratedAt.UTC().Format(time.RFC3339Nano), SortVersion: SortVersion,
		Coverage: calculateIndexCoverage(entries), Sessions: entries,
	}
	body, err := Render(document)
	if err != nil {
		if strings.Contains(err.Error(), "json exceeds 67108864 bytes") {
			return Document{}, ErrCapacityExceeded
		}
		return Document{}, err
	}
	if len(body) > maxRenderedBytes {
		return Document{}, ErrCapacityExceeded
	}
	return Parse(body)
}

func measurementMap(values []memory.SessionIndexMeasurement) (map[SessionKey]memory.SessionIndexMeasurement, error) {
	result := make(map[SessionKey]memory.SessionIndexMeasurement, len(values))
	for _, value := range values {
		key := SessionKey{Provider: value.Provider, SessionID: value.SessionID}
		if _, duplicate := result[key]; duplicate {
			return nil, errors.New("duplicate session index measurement")
		}
		result[key] = value
	}
	return result, nil
}

func buildEntry(view memory.SessionView, measurement memory.SessionIndexMeasurement, generationID string) (Entry, error) {
	terminal := string(view.TerminalState)
	availability := "available"
	if view.SourceAvailability != memory.SourceAvailable {
		availability = "unavailable"
	}
	reasons := publicReasonCodes(view)
	coverage := Coverage{
		Seen: measurement.Seen, Indexed: measurement.Indexed, Collapsed: measurement.Collapsed,
		Unprojected: measurement.Unprojected, Undecodable: measurement.Undecodable, Truncated: measurement.Truncated,
	}
	if measurement.Provider == "" {
		coverage.Seen = uint64(len(view.ObservationSummaries))
		coverage.Indexed = coverage.Seen
	}
	if !reconcileCoverage(coverage) {
		return Entry{}, errors.New("source measurement coverage does not reconcile")
	}
	state := ProcessingComplete
	switch view.TerminalState {
	case memory.Indexed:
		if len(view.Diagnostics) != 0 || coverage.Collapsed != 0 || coverage.Unprojected != 0 || coverage.Undecodable != 0 || coverage.Truncated != 0 {
			state = ProcessingPartial
		}
	case memory.Unsupported, memory.Missing, memory.Unreadable, memory.Ambiguous:
		if len(view.ObservationSummaries) != 0 {
			state = ProcessingPartial
		} else {
			state = ProcessingError
		}
	default:
		return Entry{}, errors.New("unsupported SessionView terminal state")
	}
	started, ended := nullableTime(view.StartedAt), nullableTime(view.EndedAt)
	entry := Entry{
		Provider: view.Provider, SessionID: view.SessionID, ProcessingState: state,
		StateReasonCodes: reasons, SourceAvailability: availability, SourceTerminalState: &terminal,
		StartedAt: started, EndedAt: ended, DurationMS: durationMS(started, ended),
		WarningCount: uint64(len(view.Diagnostics)), RecordCount: cloneUint64(measurement.RecordCount),
		IndexedEventCount: coverage.Indexed, Coverage: coverage, FactCounts: countFacts(view.ObservationSummaries),
		SessionViewDigest: stringPointer(view.Digest), UsageRecordDigest: stringPointer(view.UsageRecordDigest),
		LastSeenGenerationID: stringPointer(generationID),
	}
	if view.TerminalState == memory.Indexed {
		entry.LastSuccessfulGenerationID = stringPointer(generationID)
	}
	return entry, nil
}

func publicReasonCodes(view memory.SessionView) []string {
	set := map[string]bool{}
	for _, diagnostic := range view.Diagnostics {
		if stateReasons[diagnostic.Code] {
			set[diagnostic.Code] = true
		}
	}
	switch view.TerminalState {
	case memory.Unsupported:
		set["source_unsupported"] = true
	case memory.Missing:
		set["source_missing"] = true
	case memory.Unreadable:
		set["source_unreadable"] = true
	case memory.Ambiguous:
		set["source_ambiguous"] = true
	}
	result := make([]string, 0, len(set))
	for reason := range set {
		result = append(result, reason)
	}
	sort.Strings(result)
	return result
}

func nullableTime(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func durationMS(started, ended *string) *uint64 {
	if started == nil || ended == nil {
		return nil
	}
	start, startErr := time.Parse(time.RFC3339Nano, *started)
	end, endErr := time.Parse(time.RFC3339Nano, *ended)
	if startErr != nil || endErr != nil || end.Before(start) {
		return nil
	}
	value := uint64(end.Sub(start) / time.Millisecond)
	return &value
}

func countFacts(values []memory.ObservationSummary) FactCounts {
	var result FactCounts
	for _, value := range values {
		switch value.Kind {
		case "file", "file_change":
			result.FileChange++
		case "command", "tool":
			result.Command++
		case "test", "verification", "build":
			result.Verification++
		case "error":
			result.Error++
		case "artifact", "commit", "release", "deployment":
			result.Artifact++
		}
	}
	return result
}

func calculateIndexCoverage(entries []Entry) IndexCoverage {
	result := IndexCoverage{Total: uint64(len(entries))}
	for _, entry := range entries {
		switch entry.ProcessingState {
		case ProcessingComplete:
			result.Complete++
		case ProcessingPartial:
			result.Partial++
		case ProcessingError:
			result.Error++
		case ProcessingUnprocessed:
			result.Unprocessed++
		}
		if entry.SourceAvailability == "available" {
			result.SourceAvailable++
		} else {
			result.SourceUnavailable++
		}
		if entry.StartedAt != nil {
			result.StartedAtKnown++
		}
		if entry.EndedAt != nil {
			result.EndedAtKnown++
		}
		if entry.UsageRecordDigest != nil {
			result.UsageKnown++
		}
	}
	return result
}

func cloneEntry(value Entry) Entry {
	value.StateReasonCodes = append([]string(nil), value.StateReasonCodes...)
	return value
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func cloneUint64(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
