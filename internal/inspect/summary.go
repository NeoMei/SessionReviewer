package inspect

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

const summaryItemLimit = 32

// SummaryRequest names one immutable published Session summary.
type SummaryRequest struct {
	DataRoot             string
	ProjectID            string
	Provider             string
	SessionID            string
	ExpectedGenerationID string
}

type summaryInput struct {
	request      SummaryRequest
	generationID string
	view         memory.SessionView
	revisions    []memory.ObservationRevision
	entry        sessionindex.Entry
	project      memory.ProjectView
}

// LoadSessionSummary authenticates the published generation and derives a
// bounded summary only from its immutable Session and Project view objects.
func LoadSessionSummary(ctx context.Context, request SummaryRequest) (SessionSummary, error) {
	var summary SessionSummary
	_, err := inspectPublishedSession(ctx, EventPageRequest{
		DataRoot: request.DataRoot, ProjectID: request.ProjectID, Provider: request.Provider,
		SessionID: request.SessionID, ExpectedGenerationID: request.ExpectedGenerationID, Limit: 1,
	}, nil, func(authenticated authenticatedSession) error {
		var reduceErr error
		summary, reduceErr = reduceSessionSummary(ctx, summaryInput{
			request: request, generationID: authenticated.generationID, view: authenticated.view,
			revisions: authenticated.revisions, entry: authenticated.entry, project: authenticated.project,
		})
		return reduceErr
	})
	if err != nil {
		return SessionSummary{}, err
	}
	return summary, nil
}

func reduceSessionSummary(ctx context.Context, input summaryInput) (SessionSummary, error) {
	operations := make([]Entry, 0)
	verifications := make([]Entry, 0)
	errors := make([]ErrorEntry, 0)
	unresolved := make([]Entry, 0)
	recovered := validatedRecoveredFailures(input.view, input.revisions)

	for _, fact := range input.revisions {
		if err := inspectionCheckpoint(ctx, "summary_item"); err != nil {
			return SessionSummary{}, publicError(CodeInvalidArgument, "inspection timed out")
		}
		entry := summaryEntry(fact)
		if isSummaryOperation(fact) {
			operations = append(operations, entry)
		}
		switch fact.Key.Kind {
		case "verification", "test", "build", "lint":
			verifications = append(verifications, entry)
		}
		if summaryFailed(fact.Outcome) {
			errors = append(errors, ErrorEntry{
				Code: summaryErrorCode(fact.Key.Kind), OccurredAt: entry.OccurredAt, Sequence: entry.Sequence,
				RevisionID: entry.RevisionID, Text: entry.Text, SourceRevisionIDs: entry.SourceRevisionIDs,
			})
			if _, closed := recovered[fact.RevisionID]; !closed {
				unresolved = append(unresolved, entry)
			}
		} else if fact.Key.Kind == "error" {
			errors = append(errors, ErrorEntry{
				Code: "observed_error", OccurredAt: entry.OccurredAt, Sequence: entry.Sequence,
				RevisionID: entry.RevisionID, Text: entry.Text, SourceRevisionIDs: entry.SourceRevisionIDs,
			})
		}
	}

	phases, phaseUnprojected, err := summaryPhaseBoundaries(ctx, input.project.DerivedRecords, input.revisions)
	if err != nil {
		return SessionSummary{}, err
	}
	summary := SessionSummary{
		SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: input.request.ProjectID,
		Provider: input.request.Provider, SessionID: input.request.SessionID, GenerationID: input.generationID,
		SessionViewDigest:   input.view.Digest,
		PhaseBoundaries:     makeSummaryBlock(phases, phaseUnprojected),
		KeyOperations:       makeSummaryBlock(operations, 0),
		VerificationResults: makeSummaryBlock(verifications, 0),
		Errors:              makeSummaryErrorBlock(errors),
		UnresolvedQuestions: makeSummaryBlock(unresolved, 0),
		Rules:               Rules{RuleID: "summary-rules", RuleVersion: "summary-typed-fact-text-v2", DependencyDigests: summaryDependencyDigests(input)},
		Coverage:            coverageFromIndex(input.entry.Coverage),
	}
	if err := ValidateSummary(summary); err != nil {
		return SessionSummary{}, publicError(CodeInvalidArgument, "published Session summary is invalid")
	}
	return summary, nil
}

func summaryEntry(fact memory.ObservationRevision) Entry {
	return Entry{
		OccurredAt: fact.Timestamp, Sequence: uint64(fact.Key.Sequence), RevisionID: fact.RevisionID,
		Text: summaryFactText(fact), SourceRevisionIDs: []string{fact.RevisionID},
	}
}

func summaryFailed(outcome string) bool {
	switch strings.ToLower(strings.TrimSpace(outcome)) {
	case "failed", "failure", "error":
		return true
	default:
		return false
	}
}

func summarySucceeded(outcome string) bool {
	switch strings.ToLower(strings.TrimSpace(outcome)) {
	case "passed", "success":
		return true
	default:
		return false
	}
}

func summaryErrorCode(kind string) string {
	switch kind {
	case "verification", "test", "build", "lint":
		return "verification_failed"
	case "error":
		return "observed_error"
	default:
		return "operation_failed"
	}
}

type summaryRecoveryKey struct {
	kind, operation, component string
}

func validatedRecoveredFailures(view memory.SessionView, revisions []memory.ObservationRevision) map[string]struct{} {
	byID := make(map[string]memory.ObservationRevision, len(revisions))
	for _, revision := range revisions {
		byID[revision.RevisionID] = revision
	}
	result := make(map[string]struct{})
	for _, record := range view.DerivedRecords {
		if record.Kind != "recovery_link" || record.RuleID != "matching-operation-component" || record.RuleVersion != view.MaterializerVersion || len(record.DependencyRevisionIDs) != 2 || record.DependencyRevisionIDs[0] == record.DependencyRevisionIDs[1] {
			continue
		}
		failure, failureFound := byID[record.DependencyRevisionIDs[0]]
		success, successFound := byID[record.DependencyRevisionIDs[1]]
		identity := summaryRecoveryIdentity(failure)
		expectedID, expectedSubject := summaryRecoveryRecordIdentity(failure.RevisionID, success.RevisionID, identity)
		if !failureFound || !successFound || failure.Key.Sequence >= success.Key.Sequence || !summaryFailed(failure.Outcome) || !summarySucceeded(success.Outcome) || identity != summaryRecoveryIdentity(success) || identity.operation == "" || identity.component == "" || record.ID != expectedID || record.OccurredAt != success.Timestamp || record.Subject != expectedSubject || len(record.Fields) != 3 || record.Fields["operation"] != identity.operation || record.Fields["component"] != identity.component || record.Fields["outcome"] != "recovered" {
			continue
		}
		result[failure.RevisionID] = struct{}{}
	}
	return result
}

func summaryRecoveryIdentity(fact memory.ObservationRevision) summaryRecoveryKey {
	operation := strings.TrimSpace(fact.Fields["status"])
	if operation == "" {
		operation = strings.TrimSpace(fact.Operation)
	}
	return summaryRecoveryKey{
		kind: summaryNormalizedIdentity(fact.Key.Kind), operation: summaryNormalizedIdentity(operation),
		component: summaryNormalizedIdentity(fact.Fields["component"]),
	}
}

func summaryRecoveryRecordIdentity(failureID, successID string, identity summaryRecoveryKey) (string, string) {
	digest, _ := memory.Digest(struct {
		Failure string `json:"failure"`
		Success string `json:"success"`
		Rule    string `json:"rule"`
	}{Failure: failureID, Success: successID, Rule: "matching-operation-component"})
	value := strings.TrimPrefix(digest, "sha256:")
	subject := identity.operation + ":" + identity.component
	if len(subject) > 256 || utf8.RuneCountInString(subject) > 256 {
		subject = "recovery:" + value[:16]
	}
	return "recovery-" + value[:32], subject
}

func summaryNormalizedIdentity(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func summaryPhaseBoundaries(ctx context.Context, records []memory.DerivedRecord, revisions []memory.ObservationRevision) ([]Entry, uint64, error) {
	byID := make(map[string]memory.ObservationRevision, len(revisions))
	for _, revision := range revisions {
		byID[revision.RevisionID] = revision
	}
	entries := make([]Entry, 0)
	var unprojected uint64
	for _, record := range records {
		if inspectionCheckpoint(ctx, "summary_phase") != nil {
			return nil, 0, publicError(CodeInvalidArgument, "inspection timed out")
		}
		if record.Kind != "phase_boundary" || len(record.DependencyRevisionIDs) == 0 {
			continue
		}
		sequence, selected := 0, true
		for _, dependency := range record.DependencyRevisionIDs {
			revision, exists := byID[dependency]
			if !exists {
				selected = false
				break
			}
			if revision.Key.Sequence > sequence {
				sequence = revision.Key.Sequence
			}
		}
		if !selected {
			continue
		}
		if len(record.DependencyRevisionIDs) > 64 {
			unprojected++
			continue
		}
		entries = append(entries, Entry{
			OccurredAt: record.OccurredAt, Sequence: uint64(sequence), RevisionID: record.ID,
			Text: safeEventExcerpt(record.Subject), SourceRevisionIDs: append([]string(nil), record.DependencyRevisionIDs...),
		})
	}
	return entries, unprojected, nil
}

func makeSummaryBlock(entries []Entry, extraUnprojected uint64) Block {
	sort.Slice(entries, func(i, j int) bool { return entryLess(entries[i], entries[j]) })
	total := uint64(len(entries)) + extraUnprojected
	shown := len(entries)
	if shown > summaryItemLimit {
		shown = summaryItemLimit
	}
	items := append([]Entry(nil), entries[:shown]...)
	return Block{
		Total: total, Shown: uint64(shown), Omitted: total - uint64(shown),
		Coverage: Coverage{Seen: total, Indexed: uint64(shown), Unprojected: total - uint64(shown)}, Items: items,
	}
}

func makeSummaryErrorBlock(entries []ErrorEntry) ErrorBlock {
	sort.Slice(entries, func(i, j int) bool {
		left := Entry{OccurredAt: entries[i].OccurredAt, Sequence: entries[i].Sequence, RevisionID: entries[i].RevisionID}
		right := Entry{OccurredAt: entries[j].OccurredAt, Sequence: entries[j].Sequence, RevisionID: entries[j].RevisionID}
		return entryLess(left, right)
	})
	total := uint64(len(entries))
	shown := len(entries)
	if shown > summaryItemLimit {
		shown = summaryItemLimit
	}
	return ErrorBlock{
		Total: total, Shown: uint64(shown), Omitted: total - uint64(shown),
		Coverage: Coverage{Seen: total, Indexed: uint64(shown), Unprojected: total - uint64(shown)},
		Items:    append([]ErrorEntry(nil), entries[:shown]...),
	}
}

func coverageFromIndex(value sessionindex.Coverage) Coverage {
	return Coverage{
		Seen: value.Seen, Indexed: value.Indexed, Collapsed: value.Collapsed,
		Unprojected: value.Unprojected, Undecodable: value.Undecodable, Truncated: value.Truncated,
	}
}

func summaryDependencyDigests(input summaryInput) []string {
	values := []string{input.view.DependencyDigest, input.project.DependencyDigest}
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if value == "" || len(result) != 0 && result[len(result)-1] == value {
			continue
		}
		result = append(result, value)
	}
	return result
}
