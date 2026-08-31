package sessionview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/memory"
)

func TestMaterializeReusesUnchangedDependencyDigestByteForByte(t *testing.T) {
	input := fixtureInput(t, nil)
	first, changed, err := Materialize(input)
	if err != nil || !changed {
		t.Fatalf("first materialization changed=%v err=%v", changed, err)
	}
	input.Previous = &first
	second, changed, err := Materialize(input)
	if err != nil || changed {
		t.Fatalf("second materialization changed=%v err=%v", changed, err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("unchanged materialization churned\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}
}

func TestMaterializeSortsAndDeduplicatesOnlyExactRevisionIdentity(t *testing.T) {
	input := fixtureInput(t, nil)
	first := observation(t, input.Source, input.ProjectID, 9, "file", "same-subject", "file_change", "a.go", "success", nil)
	second := observation(t, input.Source, input.ProjectID, 3, "command", "same-subject", "command_finished", ".", "success", map[string]string{"command_signature": "go:test"})
	tied := observation(t, input.Source, input.ProjectID, 3, "verification", "same-subject", "verification", "package", "passed", map[string]string{"component": "package", "status": "test"})
	input.Observations = []memory.ObservationRevision{first, tied, second, second}
	input.ObservationChunkDigests = []string{testDigest(t, "chunk-b"), testDigest(t, "chunk-a"), testDigest(t, "chunk-b")}

	view, _, err := Materialize(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.ActiveRevisionIDs) != 3 || view.ActiveRevisionIDs[2] != first.RevisionID {
		t.Fatalf("active revisions=%v want two sequence-3 facts followed by %s", view.ActiveRevisionIDs, first.RevisionID)
	}
	if view.ActiveRevisionIDs[0] >= view.ActiveRevisionIDs[1] {
		t.Fatalf("same-sequence revisions are not ordered by ID: %v", view.ActiveRevisionIDs[:2])
	}
	if !contains(view.ActiveRevisionIDs[:2], second.RevisionID) || !contains(view.ActiveRevisionIDs[:2], tied.RevisionID) {
		t.Fatalf("typed facts sharing a subject were collapsed: %v", view.ActiveRevisionIDs)
	}
	wantChunks := []string{testDigest(t, "chunk-b"), testDigest(t, "chunk-a")}
	if fmt.Sprint(view.ObservationChunkDigests) != fmt.Sprint(wantChunks) {
		t.Fatalf("chunk digests=%v want first-seen order %v", view.ObservationChunkDigests, wantChunks)
	}
}

func TestMaterializeCarriesCompactTypedSummariesForProjectReduction(t *testing.T) {
	input := fixtureInput(t, nil)
	view, _, err := Materialize(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.ObservationSummaries) != len(view.ActiveRevisionIDs) || len(view.ObservationSummaries) != 2 {
		t.Fatalf("summary coverage=%d active=%d", len(view.ObservationSummaries), len(view.ActiveRevisionIDs))
	}
	for index, summary := range view.ObservationSummaries {
		if summary.RevisionID != view.ActiveRevisionIDs[index] {
			t.Fatalf("summary %d dependency=%s active=%s", index, summary.RevisionID, view.ActiveRevisionIDs[index])
		}
	}
	request := view.ObservationSummaries[0]
	if request.Sequence != 1 || request.Kind != "request" || request.Subject != "request-1" || request.OccurredAt != "2026-08-31T10:00:01Z" ||
		request.Operation != "user_request" || request.Excerpt != "review the project" {
		t.Fatalf("request summary lost typed content: %+v", request)
	}
	verification := view.ObservationSummaries[1]
	if verification.Sequence != 10 || verification.Kind != "verification" || verification.Operation != "verification" ||
		verification.Object != "package" || verification.Outcome != "passed" || verification.Fields["component"] != "package" || verification.Fields["status"] != "test" {
		t.Fatalf("verification summary lost typed content: %+v", verification)
	}
	body, err := json.Marshal(view.ObservationSummaries)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"source_ref", "source_hash", "source_location", "adapter_id", "adapter_version", "total_tokens", "raw_tool_output", "assistant_message", "transcript"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("summary copied forbidden content %q: %s", forbidden, body)
		}
	}
}

func TestMaterializeAppendCreatesSuccessorWithoutChurningPriorFacts(t *testing.T) {
	input := fixtureInput(t, nil)
	first, _, err := Materialize(input)
	if err != nil {
		t.Fatal(err)
	}
	appended := observation(t, input.Source, input.ProjectID, 20, "artifact", "artifact-2", "artifact_created", "report.json", "success", map[string]string{"artifact_id": "artifact-2"})
	input.Previous = &first
	input.Observations = append(append([]memory.ObservationRevision(nil), input.Observations...), appended)
	input.ObservationChunkDigests = append(append([]string(nil), input.ObservationChunkDigests...), testDigest(t, "chunk-2"))

	second, changed, err := Materialize(input)
	if err != nil || !changed {
		t.Fatalf("append materialization changed=%v err=%v", changed, err)
	}
	if len(second.ActiveRevisionIDs) != len(first.ActiveRevisionIDs)+1 {
		t.Fatalf("append revision count=%d want %d", len(second.ActiveRevisionIDs), len(first.ActiveRevisionIDs)+1)
	}
	for _, prior := range first.ActiveRevisionIDs {
		if !contains(second.ActiveRevisionIDs, prior) {
			t.Fatalf("append lost prior fact %s: %v", prior, second.ActiveRevisionIDs)
		}
	}
	if second.Digest == first.Digest || second.DependencyDigest == first.DependencyDigest {
		t.Fatalf("append failed to create successor: first=%+v second=%+v", first, second)
	}
}

func TestMaterializeMissingSourcePreservesPriorFactsWithoutClaimingFreshness(t *testing.T) {
	input := fixtureInput(t, nil)
	prior, _, err := Materialize(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Previous = &prior
	input.Source.Availability = memory.SourceUnavailable
	input.SourceRecordDigest = mustDigest(t, input.Source)
	input.TerminalState = memory.Missing
	input.Observations = nil
	input.ObservationChunkDigests = nil
	input.Diagnostics = []memory.Diagnostic{{Code: "source_missing"}}

	missing, changed, err := Materialize(input)
	if err != nil || !changed {
		t.Fatalf("missing materialization changed=%v err=%v", changed, err)
	}
	if missing.TerminalState != memory.Missing || missing.SourceAvailability != memory.SourceUnavailable {
		t.Fatalf("missing view claims fresh source: %+v", missing)
	}
	if fmt.Sprint(missing.ActiveRevisionIDs) != fmt.Sprint(prior.ActiveRevisionIDs) ||
		fmt.Sprint(missing.ObservationChunkDigests) != fmt.Sprint(prior.ObservationChunkDigests) ||
		fmt.Sprint(missing.ObservationSummaries) != fmt.Sprint(prior.ObservationSummaries) ||
		fmt.Sprint(missing.DerivedRecords) != fmt.Sprint(prior.DerivedRecords) || missing.SourceRecordDigest != prior.SourceRecordDigest || missing.UsageRecordDigest != prior.UsageRecordDigest {
		t.Fatalf("missing source discarded retained facts\nprior=%+v\nmissing=%+v", prior, missing)
	}
}

func TestMaterializeMissingSourceRejectsFreshObservationClaims(t *testing.T) {
	input := fixtureInput(t, nil)
	input.Source.Availability = memory.SourceUnavailable
	input.SourceRecordDigest = mustDigest(t, input.Source)
	input.TerminalState = memory.Missing
	if _, _, err := Materialize(input); err == nil {
		t.Fatal("missing source accepted observations as freshly scanned facts")
	}
}

func TestMaterializeAssignsExactlyOneRequestedTerminalState(t *testing.T) {
	for _, state := range []memory.TerminalState{memory.Indexed, memory.Unsupported, memory.Missing, memory.Unreadable, memory.Ambiguous} {
		t.Run(string(state), func(t *testing.T) {
			input := fixtureInput(t, nil)
			input.TerminalState = state
			if state == memory.Missing {
				input.Source.Availability = memory.SourceUnavailable
				input.SourceRecordDigest = mustDigest(t, input.Source)
				input.UsageRecordDigest = input.SourceRecordDigest
				input.Observations = nil
				input.ObservationChunkDigests = nil
			}
			view, _, err := Materialize(input)
			if err != nil {
				t.Fatal(err)
			}
			if view.TerminalState != state {
				t.Fatalf("terminal state=%q want %q", view.TerminalState, state)
			}
			if err := memory.ValidateSessionView(view); err != nil {
				t.Fatalf("terminal SessionView is invalid: %v", err)
			}
		})
	}

	input := fixtureInput(t, nil)
	input.TerminalState = memory.TerminalState("partially-indexed")
	if _, _, err := Materialize(input); err == nil {
		t.Fatal("materializer accepted a non-terminal state")
	}
}

func TestMaterializeLinksFailureOnlyToLaterMatchingOperationAndComponent(t *testing.T) {
	input := fixtureInput(t, nil)
	failure := observation(t, input.Source, input.ProjectID, 2, "verification", "failed-test", "verification", "package", "failed", map[string]string{"component": " Package ", "status": " TEST "})
	wrongComponent := observation(t, input.Source, input.ProjectID, 3, "verification", "wrong-component", "verification", "other", "passed", map[string]string{"component": "other", "status": "test"})
	wrongOperation := observation(t, input.Source, input.ProjectID, 4, "verification", "wrong-operation", "verification", "package", "passed", map[string]string{"component": "package", "status": "build"})
	matchingSuccess := observation(t, input.Source, input.ProjectID, 5, "verification", "matching-success", "verification", "package", "passed", map[string]string{"component": "package", "status": "test"})
	laterFailure := observation(t, input.Source, input.ProjectID, 6, "verification", "later-failure", "verification", "package", "failed", map[string]string{"component": "package", "status": "test"})
	input.Observations = []memory.ObservationRevision{laterFailure, matchingSuccess, wrongOperation, failure, wrongComponent}

	view, _, err := Materialize(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.DerivedRecords) != 1 {
		t.Fatalf("derived records=%+v want one compatible recovery", view.DerivedRecords)
	}
	recovery := view.DerivedRecords[0]
	if fmt.Sprint(recovery.DependencyRevisionIDs) != fmt.Sprint([]string{failure.RevisionID, matchingSuccess.RevisionID}) {
		t.Fatalf("recovery dependencies=%v", recovery.DependencyRevisionIDs)
	}
	if recovery.Fields["operation"] != "test" || recovery.Fields["component"] != "package" || recovery.Fields["outcome"] != "recovered" {
		t.Fatalf("recovery normalization=%+v", recovery)
	}
	if recovery.OccurredAt != matchingSuccess.Timestamp {
		t.Fatalf("recovery time=%q want later success %q", recovery.OccurredAt, matchingSuccess.Timestamp)
	}
}

func TestMaterializeDoesNotTreatArbitraryObjectAsRecoveryComponent(t *testing.T) {
	input := fixtureInput(t, nil)
	failure := observation(t, input.Source, input.ProjectID, 2, "command", "failed-command", "command_finished", "/private/project", "failure", map[string]string{"command_signature": "go:test"})
	success := observation(t, input.Source, input.ProjectID, 3, "command", "successful-command", "command_finished", "/private/project", "success", map[string]string{"command_signature": "go:test"})
	input.Observations = []memory.ObservationRevision{failure, success}
	view, _, err := Materialize(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.DerivedRecords) != 0 {
		t.Fatalf("arbitrary object was promoted to a recovery component: %+v", view.DerivedRecords)
	}
}

func TestMaterializeDoesNotTreatRevisionIDOrderAsLaterSourceSequence(t *testing.T) {
	input := fixtureInput(t, nil)
	var failure, success memory.ObservationRevision
	for index := 0; index < 100; index++ {
		failure = observation(t, input.Source, input.ProjectID, 7, "verification", fmt.Sprintf("failure-%d", index), "verification", "package", "failed", map[string]string{"component": "package", "status": "test"})
		success = observation(t, input.Source, input.ProjectID, 7, "verification", fmt.Sprintf("success-%d", index), "verification", "package", "passed", map[string]string{"component": "package", "status": "test"})
		if failure.RevisionID < success.RevisionID {
			break
		}
	}
	if failure.RevisionID >= success.RevisionID {
		t.Fatal("fixture could not expose revision-ID temporal ordering trap")
	}
	input.Observations = []memory.ObservationRevision{success, failure}
	view, _, err := Materialize(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.DerivedRecords) != 0 {
		t.Fatalf("same-sequence facts were falsely ordered by revision ID: %+v", view.DerivedRecords)
	}
}

func TestMaterializeReferencesSharedCatalogUsageWithoutCopyingTotals(t *testing.T) {
	input := fixtureInput(t, nil)
	input.Source.ProjectIDs = []string{"project-a", "project-b"}
	input.SourceRecordDigest = mustDigest(t, input.Source)
	input.UsageRecordDigest = input.SourceRecordDigest
	view, _, err := Materialize(input)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"total_tokens", "input_tokens", "output_tokens", "models"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("SessionView copied SourceCatalog usage field %q: %s", forbidden, body)
		}
	}
	if view.UsageRecordDigest != input.UsageRecordDigest || view.UsageRecordDigest != view.SourceRecordDigest {
		t.Fatalf("usage reference=%q source=%q want authenticated catalog digest %q", view.UsageRecordDigest, view.SourceRecordDigest, input.UsageRecordDigest)
	}

	unauthenticated := input
	unauthenticated.UsageRecordDigest = testDigest(t, "unrelated-usage-record")
	if _, _, err := Materialize(unauthenticated); err == nil {
		t.Fatal("materializer accepted a usage digest not authenticated by SourceCatalog")
	}

	changedUsage := input
	changedUsage.Previous = &view
	changedUsage.Source.Usage.Models[0].TokenUsage.InputTokens++
	changedUsage.Source.Usage.Models[0].TokenUsage.TotalTokens++
	changedUsage.Source.Usage.TotalTokens++
	changedUsage.SourceRecordDigest = mustDigest(t, changedUsage.Source)
	changedUsage.UsageRecordDigest = changedUsage.SourceRecordDigest
	changedView, changed, err := Materialize(changedUsage)
	if err != nil || !changed || changedView.DependencyDigest == view.DependencyDigest || changedView.UsageRecordDigest == view.UsageRecordDigest {
		t.Fatalf("authenticated usage successor did not invalidate view: changed=%v err=%v", changed, err)
	}
}

func TestMaterializeRejectsTwoActiveRevisionsForOneStableObservationKey(t *testing.T) {
	input := fixtureInput(t, nil)
	replacement := input.Observations[0]
	replacement.AdapterVersion = "v2"
	replacement.Outcome = "failed"
	replacement.Fields = map[string]string{"component": "package", "status": "test", "failed": "true"}
	replacement.RevisionID = memory.ObservationRevisionID(replacement)
	if replacement.RevisionID == input.Observations[0].RevisionID {
		t.Fatal("fixture did not create a successor revision")
	}
	input.Observations = append(input.Observations, replacement)
	if _, _, err := Materialize(input); err == nil {
		t.Fatal("materializer accepted multiple active revisions for one stable ObservationKey")
	}
}

func TestMaterializeSummaryFieldsDoNotAliasInputOrPreviousView(t *testing.T) {
	input := fixtureInput(t, nil)
	first, _, err := Materialize(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Observations[0].Fields["component"] = "mutated-input"
	if got := summaryByRevision(t, first, input.Observations[0].RevisionID).Fields["component"]; got != "package" {
		t.Fatalf("materialized summary aliased observation fields: %q", got)
	}

	unchanged := fixtureInput(t, &first)
	second, changed, err := Materialize(unchanged)
	if err != nil || changed {
		t.Fatalf("unchanged materialization changed=%v err=%v", changed, err)
	}
	secondSummary := summaryByRevision(t, second, unchanged.Observations[0].RevisionID)
	secondSummary.Fields["component"] = "mutated-output"
	if summaryByRevision(t, first, unchanged.Observations[0].RevisionID).Fields["component"] == "mutated-output" {
		t.Fatal("unchanged result aliased previous SessionView summary fields")
	}
}

func TestMaterializeSourceAvailabilityChangeInvalidatesDependency(t *testing.T) {
	input := fixtureInput(t, nil)
	available, _, err := Materialize(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Previous = &available
	input.Source.Availability = memory.SourceUnavailable
	input.SourceRecordDigest = mustDigest(t, input.Source)
	input.TerminalState = memory.Missing
	input.Observations = nil
	input.ObservationChunkDigests = nil
	unavailable, changed, err := Materialize(input)
	if err != nil || !changed {
		t.Fatalf("availability change changed=%v err=%v", changed, err)
	}
	if unavailable.DependencyDigest == available.DependencyDigest {
		t.Fatal("source availability did not participate in dependency identity")
	}
}

func TestMaterializeRejectsForeignObservationsAndWrongVersion(t *testing.T) {
	input := fixtureInput(t, nil)
	input.Observations[0].Key.ProjectID = "project-b"
	input.Observations[0].RevisionID = memory.ObservationRevisionID(input.Observations[0])
	if _, _, err := Materialize(input); err == nil {
		t.Fatal("materializer accepted a foreign-project observation")
	}

	input = fixtureInput(t, nil)
	input.MaterializerVersion = "session-view-v2"
	if _, _, err := Materialize(input); err == nil {
		t.Fatal("materializer accepted an unsupported materializer version")
	}
}

func fixtureInput(t *testing.T, previous *memory.SessionView) Input {
	t.Helper()
	source := memory.SourceRecord{
		SchemaVersion:  memory.MemorySchemaVersion,
		Provider:       "codex",
		SessionID:      "session-1",
		SourceIdentity: "source-1",
		StartedAt:      "2026-08-31T10:00:00Z",
		EndedAt:        "2026-08-31T10:01:00Z",
		FrozenBoundary: memory.FrozenBoundary{
			Location:   memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: 20, ByteOffset: 2048}},
			SourceHash: strings.Repeat("a", 64),
		},
		Availability: memory.SourceAvailable,
		Usage: accounting.SessionUsage{
			StartedAt: "2026-08-31T10:00:00Z", EndedAt: "2026-08-31T10:01:00Z", DurationMS: 60000,
			Models:      []accounting.ModelUsage{{Model: "gpt-5", TokenUsage: accounting.TokenUsage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10}}},
			TotalTokens: 10,
		},
		ProjectIDs: []string{"project-a"},
	}
	request := observation(t, source, "project-a", 1, "request", "request-1", "user_request", "", "", nil)
	request.Excerpt = "review the project"
	request.RevisionID = memory.ObservationRevisionID(request)
	verification := observation(t, source, "project-a", 10, "verification", "verify-1", "verification", "package", "passed", map[string]string{"component": "package", "status": "test"})
	sourceDigest := mustDigest(t, source)
	return Input{
		ProjectID:               "project-a",
		Source:                  source,
		SourceRecordDigest:      sourceDigest,
		UsageRecordDigest:       sourceDigest,
		Observations:            []memory.ObservationRevision{verification, request},
		ObservationChunkDigests: []string{testDigest(t, "chunk-1")},
		TerminalState:           memory.Indexed,
		Diagnostics:             []memory.Diagnostic{},
		Previous:                previous,
		MaterializerVersion:     MaterializerVersion,
	}
}

func observation(t *testing.T, source memory.SourceRecord, projectID string, sequence int, kind, subject, operation, object, outcome string, fields map[string]string) memory.ObservationRevision {
	t.Helper()
	value := memory.ObservationRevision{
		SchemaVersion: memory.MemorySchemaVersion,
		Key: memory.ObservationKey{
			Provider: source.Provider, SessionID: source.SessionID, SourceIdentity: source.SourceIdentity,
			Sequence: sequence, ProjectID: projectID, Kind: kind, Subject: subject,
		},
		Ref: memory.SourceRef{
			Provider: source.Provider, SessionID: source.SessionID, SourceIdentity: source.SourceIdentity,
			Location:   memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: sequence, ByteOffset: int64(sequence * 100)}},
			SourceHash: source.FrozenBoundary.SourceHash,
		},
		Timestamp:      fmt.Sprintf("2026-08-31T10:00:%02dZ", sequence%60),
		Operation:      operation,
		Object:         object,
		Outcome:        outcome,
		Fields:         fields,
		AdapterID:      "codex-jsonl",
		AdapterVersion: "v1",
	}
	value.RevisionID = memory.ObservationRevisionID(value)
	if err := memory.ValidateObservationRevision(value); err != nil {
		t.Fatalf("invalid observation fixture: %v", err)
	}
	return value
}

func mustDigest(t *testing.T, value any) string {
	t.Helper()
	digest, err := memory.Digest(value)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func testDigest(t *testing.T, label string) string {
	t.Helper()
	return mustDigest(t, label)
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func summaryByRevision(t *testing.T, view memory.SessionView, revisionID string) memory.ObservationSummary {
	t.Helper()
	for _, summary := range view.ObservationSummaries {
		if summary.RevisionID == revisionID {
			return summary
		}
	}
	t.Fatalf("summary %s not found: %+v", revisionID, view.ObservationSummaries)
	return memory.ObservationSummary{}
}
