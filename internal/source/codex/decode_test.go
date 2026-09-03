package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/source"
)

func TestDecodeEmitsExactObservedRulesWithPerObservationProjectAffinity(t *testing.T) {
	fixture := newAdapterFixture(t)
	fixture.installFixture(t, "session-shared.jsonl")
	adapter := fixture.adapter(t, "v1")
	candidate := discoverCandidate(t, adapter, "session-shared")
	boundary, err := adapter.Freeze(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)

	wantProject := map[string]string{
		"session_started":  "project-a",
		"cwd_changed":      "project-b",
		"user_request":     "project-b",
		"command_started":  "project-a",
		"command_finished": "project-a",
		"verification":     "project-a",
		"file_change":      "project-b",
	}
	seen := make(map[string]bool)
	for _, observation := range observations {
		projectID, wanted := wantProject[observation.Operation]
		if !wanted {
			continue
		}
		if observation.Key.ProjectID != projectID {
			t.Fatalf("operation %s project=%s want %s: %+v", observation.Operation, observation.Key.ProjectID, projectID, observation)
		}
		seen[observation.Operation] = true
	}
	for operation := range wantProject {
		if !seen[operation] {
			t.Errorf("operation %q not emitted; observations=%+v", operation, observations)
		}
	}
	if fmt.Sprint(report.ProposedSource.ProjectIDs) != fmt.Sprint([]string{"project-a", "project-b"}) {
		t.Fatalf("report project IDs=%v", report.ProposedSource.ProjectIDs)
	}
	if _, found, err := fixture.catalog.GetSource("codex", "session-shared"); err != nil || found {
		t.Fatalf("Decode mutated catalog found=%v err=%v", found, err)
	}
	if report.ExpectedCatalogDigest != "" || report.BoundaryRelation != source.BoundaryInitial {
		t.Fatalf("initial proposal metadata=%+v", report)
	}
	if report.ProposedSource.Usage.TotalTokens != 3 || fmt.Sprint(report.ProposedSource.ProjectIDs) != fmt.Sprint([]string{"project-a", "project-b"}) {
		t.Fatalf("shared source proposal=%+v", report.ProposedSource)
	}
}

func TestDecodeParsesGitAndVerificationFactsWithoutCopyingMessagesOutputsOrUsage(t *testing.T) {
	fixture := newAdapterFixture(t)
	fixture.installFixture(t, "session-project-a.jsonl")
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "session-project-a"))
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)

	var gitFound, verificationFound, redactionFound bool
	for _, observation := range observations {
		if observation.Operation == "git_observation" && observation.Fields["branch"] == "main" && observation.Fields["status"] == "dirty" {
			gitFound = true
		}
		if observation.Operation == "verification" && observation.Fields["status"] == "test" && observation.Fields["exit_code"] == "0" && observation.Outcome == "passed" {
			verificationFound = true
		}
		if strings.Contains(observation.Excerpt, "[REDACTED:") {
			redactionFound = true
		}
		if utf8.RuneCountInString(observation.Excerpt) > 512 {
			t.Fatalf("excerpt has %d code points", utf8.RuneCountInString(observation.Excerpt))
		}
	}
	if !gitFound || !verificationFound || !redactionFound {
		t.Fatalf("typed facts missing: git=%v verification=%v redaction=%v observations=%+v", gitFound, verificationFound, redactionFound, observations)
	}
	body, err := json.Marshal(observations)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		longUserSecret,
		"COMPLETE-USER-MESSAGE-MUST-NOT-PERSIST",
		"TOOL-OUTPUT-MUST-NOT-PERSIST",
		"UNSUPPORTED-RAW-MUST-NOT-PERSIST",
		"total_tokens",
		"input_tokens",
	} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("observation persistence copied forbidden source content %q: %s", forbidden, body)
		}
	}
	if report.UnsupportedRecords != 0 {
		t.Fatalf("known reasoning record counted as unsupported: %d", report.UnsupportedRecords)
	}
	if _, found, err := fixture.catalog.GetSource("codex", "session-project-a"); err != nil || found {
		t.Fatalf("Decode mutated catalog found=%v err=%v", found, err)
	}
	if report.ProposedSource.Usage.TotalTokens != 15 {
		t.Fatalf("proposal usage record=%+v", report.ProposedSource)
	}
}

func TestGitStatusGrammarRejectsProseAsAStatusEntry(t *testing.T) {
	if fields, valid := parseGitOutput("status", "exit code: 0\n## main...origin/main\nthis prose only sounds dirty"); valid {
		t.Fatalf("prose accepted as typed Git status: %+v", fields)
	}
	fields, valid := parseGitOutput("status", "exit code: 0\n## main...origin/main\n M internal/source/decode.go")
	if !valid || fields["branch"] != "main" || fields["status"] != "dirty" {
		t.Fatalf("valid porcelain rejected: fields=%+v valid=%v", fields, valid)
	}
}

func TestBoundedExcerptRejectsOversizedMarkerWithoutExceedingPersistenceLimits(t *testing.T) {
	input := strings.Repeat("界", 600) + " [REDACTED:" + strings.Repeat("A", 600) + "] COMPLETE-MESSAGE"
	excerpt := boundedExcerpt(input)
	if utf8.RuneCountInString(excerpt) > 512 {
		t.Fatalf("excerpt has %d code points", utf8.RuneCountInString(excerpt))
	}
	if len(excerpt) > 1024 {
		t.Fatalf("excerpt has %d bytes", len(excerpt))
	}
	if excerpt == input || strings.Contains(excerpt, "COMPLETE-MESSAGE") {
		t.Fatalf("excerpt retained complete input: %q", excerpt)
	}
}

func TestBoundedExcerptPreservesCompleteRedactedContentWhenWithinLimits(t *testing.T) {
	const redacted = "Use [REDACTED:NAMED_SECRET] for the configured service."
	if excerpt := boundedExcerpt(redacted); excerpt != redacted {
		t.Fatalf("bounded excerpt=%q want complete redacted content %q", excerpt, redacted)
	}
}

func TestVerificationComponentNormalizesArbitraryCommandToken(t *testing.T) {
	const raw = "ARBITRARY-COMPONENT-MUST-NOT-PERSIST"
	classified := classifyCommand("go test " + raw)
	if classified.verification != "other" {
		t.Fatalf("verification component=%q want normalized other class", classified.verification)
	}
	if strings.Contains(classified.signature+classified.verification, raw) {
		t.Fatalf("classified command retained arbitrary token: %+v", classified)
	}
}

func TestVerificationComponentAcceptsKnownSafePackageGrammar(t *testing.T) {
	classified := classifyCommand("go test ./...")
	if classified.signature != "go:test" || classified.verification != "package" || classified.verificationOperation != "test" {
		t.Fatalf("safe Go package grammar was not classified: %+v", classified)
	}
}

func TestCommandClassificationNeverPersistsUnknownExecutableOrSecretOperand(t *testing.T) {
	fixture := newAdapterFixture(t)
	workdir := filepath.ToSlash(fixture.projectA)
	const unknownExecutable = "unknown-executable-canary"
	const secretOperand = "sk-round-two-secret-shaped-12345678901234567890"
	unknownInput, err := json.Marshal(map[string]string{"cmd": unknownExecutable + " status", "workdir": workdir})
	if err != nil {
		t.Fatal(err)
	}
	secretInput, err := json.Marshal(map[string]string{"cmd": "go " + secretOperand, "workdir": workdir})
	if err != nil {
		t.Fatal(err)
	}
	body := encodedRecord(t, "2026-08-31T12:30:00Z", "session_meta", map[string]any{"id": "command-privacy", "cwd": workdir}) +
		encodedRecord(t, "2026-08-31T12:30:01Z", "response_item", map[string]any{"type": "custom_tool_call", "call_id": "call-unknown", "name": "exec_command", "input": string(unknownInput)}) +
		encodedRecord(t, "2026-08-31T12:30:02Z", "response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "call-unknown", "output": "exit code: 0"}) +
		encodedRecord(t, "2026-08-31T12:30:03Z", "response_item", map[string]any{"type": "custom_tool_call", "call_id": "call-secret-operand", "name": "exec_command", "input": string(secretInput)}) +
		encodedRecord(t, "2026-08-31T12:30:04Z", "response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "call-secret-operand", "output": "exit code: 0"}) +
		usageEvent("2026-08-31T12:30:05Z")
	if err := os.WriteFile(filepath.Join(fixture.sessions, "command-privacy.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := fixture.adapter(t, "v2", "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "command-privacy"))
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)
	persisted, err := json.Marshal(struct {
		Observations []memory.ObservationRevision
		Report       source.DecodeReport
	}{Observations: observations, Report: report})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{unknownExecutable, secretOperand} {
		if strings.Contains(string(persisted), forbidden) {
			t.Fatalf("command source token %q entered persisted effects: %s", forbidden, persisted)
		}
	}
	wantSignatures := map[string]string{"call-unknown": "other", "call-secret-operand": "go:other"}
	for _, observation := range observations {
		if want, found := wantSignatures[observation.Key.Subject]; found && observation.Operation == "command_started" {
			if got := observation.Fields["command_signature"]; got != want {
				t.Fatalf("command %s signature=%q want %q", observation.Key.Subject, got, want)
			}
			delete(wantSignatures, observation.Key.Subject)
		}
	}
	if len(wantSignatures) != 0 {
		t.Fatalf("command signatures not observed: %v", wantSignatures)
	}
}

func TestAffinityDoesNotFallBackToBroaderProjectWhenNestedBindingLosesAuthentication(t *testing.T) {
	fixture := newAdapterFixture(t)
	nested := filepath.Join(fixture.projectA, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.bindings = []projectidentity.Binding{
		fixture.bindings[0],
		authenticateBinding(t, "project-nested", nested),
	}
	implementation, ok := fixture.adapter(t, "v1").(*adapter)
	if !ok {
		t.Fatal("Codex adapter has unexpected implementation type")
	}
	if err := os.Rename(nested, nested+"-moved"); err != nil {
		t.Fatal(err)
	}

	projectIDs, reason := implementation.classifyAffinity(filepath.Join(nested, "future.go"), true)
	if reason != "ambiguous_project_root" || fmt.Sprint(projectIDs) != fmt.Sprint([]string{"project-a", "project-nested"}) {
		t.Fatalf("affinity fell back after nested binding changed: projects=%v reason=%q", projectIDs, reason)
	}
}

func TestDecodeRejectsBoundaryMetadataChangedAfterFreeze(t *testing.T) {
	fixture := newAdapterFixture(t)
	fixture.installFixture(t, "session-project-a.jsonl")
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "session-project-a"))
	if err != nil {
		t.Fatal(err)
	}
	boundary.Candidate.InitialCWD = fixture.projectB
	if _, err := adapter.Decode(context.Background(), boundary, func(memory.ObservationRevision) error { return nil }); err == nil {
		t.Fatal("Decode accepted boundary metadata changed after freeze")
	}
}

func TestDecodeTreatsFrozenMutationAndVisitorFailureAsFatalWithoutCatalogWrite(t *testing.T) {
	fixture := newAdapterFixture(t)
	path := fixture.installFixture(t, "session-project-a.jsonl")
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "session-project-a"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, boundary.Frozen.Location.JSONL.ByteOffset/2); err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Decode(context.Background(), boundary, func(memory.ObservationRevision) error { return nil })
	if err == nil {
		t.Fatal("frozen truncation was not fatal")
	}

	fixture = newAdapterFixture(t)
	fixture.installFixture(t, "session-project-a.jsonl")
	adapter = fixture.adapter(t, "v1")
	boundary, err = adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "session-project-a"))
	if err != nil {
		t.Fatal(err)
	}
	visitorErr := errors.New("visitor-integrity-canary")
	_, err = adapter.Decode(context.Background(), boundary, func(memory.ObservationRevision) error { return visitorErr })
	if !errors.Is(err, visitorErr) {
		t.Fatalf("visitor error was swallowed: %v", err)
	}
	if _, found, getErr := fixture.catalog.GetSource("codex", "session-project-a"); getErr != nil || found {
		t.Fatalf("visitor failure mutated catalog found=%v err=%v", found, getErr)
	}
}

func TestDecodeDiagnosticsStayBoundedWhileUnsupportedCountRemainsExact(t *testing.T) {
	decoder := recordDecoder{}
	for range 5000 {
		decoder.unsupported()
	}
	if decoder.report.UnsupportedRecords != 5000 {
		t.Fatalf("unsupported count=%d", decoder.report.UnsupportedRecords)
	}
	if len(decoder.report.Diagnostics) != 4096 {
		t.Fatalf("diagnostics=%d want 4096", len(decoder.report.Diagnostics))
	}
}

func TestMalformedLineAdvancesBoundaryAndLaterValidLineDecodes(t *testing.T) {
	fixture := newAdapterFixture(t)
	fixture.installFixture(t, "session-malformed.jsonl")
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "session-malformed"))
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)
	if report.MalformedLines != 1 || report.UnsupportedRecords != 1 || report.TerminalState != memory.Unreadable {
		t.Fatalf("decode report=%+v", report)
	}
	foundLaterRequest, foundLaterTool, foundLaterUsage := false, false, report.ProposedSource.Usage.TotalTokens == 2
	for _, observation := range observations {
		if observation.Operation == "user_request" && observation.Key.Subject == "user-after-malformed" {
			foundLaterRequest = true
		}
		if observation.Operation == "verification" && observation.Key.Subject == "call-after-malformed" && observation.Outcome == "passed" {
			foundLaterTool = true
		}
	}
	if !foundLaterRequest || !foundLaterTool || !foundLaterUsage {
		t.Fatalf("post-malformed continuation request=%v tool=%v usage=%v observations=%+v report=%+v", foundLaterRequest, foundLaterTool, foundLaterUsage, observations, report)
	}
	if len(report.Diagnostics) == 0 || len(report.Diagnostics) > maxDiagnostics {
		t.Fatalf("malformed diagnostic is absent or unbounded: %d", len(report.Diagnostics))
	}
	if boundary.Frozen.Location.JSONL == nil || boundary.Frozen.Location.JSONL.Line != 7 {
		t.Fatalf("malformed line did not advance frozen boundary: %+v", boundary.Frozen)
	}
}

func TestDecodeClassifiesUnsupportedOnlySourceAndCrossProjectSource(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		fixture := newAdapterFixture(t)
		body := encodedRecord(t, "2026-08-31T14:00:00Z", "session_meta", map[string]any{"id": "unsupported-only", "cwd": fixture.projectA}) +
			encodedRecord(t, "2026-08-31T14:00:01Z", "future_record", map[string]any{"version": 99}) +
			usageEvent("2026-08-31T14:00:02Z")
		if err := os.WriteFile(filepath.Join(fixture.sessions, "unsupported-only.jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		adapter := fixture.adapter(t, "v1")
		boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "unsupported-only"))
		if err != nil {
			t.Fatal(err)
		}
		observations, report := decodeBoundary(t, adapter, boundary)
		if report.TerminalState != memory.Unsupported || report.UnsupportedRecords != 1 || len(observations) != 1 {
			t.Fatalf("unsupported source classification=%+v observations=%+v", report, observations)
		}
	})

	t.Run("cross project", func(t *testing.T) {
		fixture := newAdapterFixture(t)
		fixture.installFixture(t, "session-shared.jsonl")
		adapter := fixture.adapter(t, "v1")
		boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "session-shared"))
		if err != nil {
			t.Fatal(err)
		}
		_, report := decodeBoundary(t, adapter, boundary)
		if report.TerminalState != memory.Ambiguous || len(report.ProposedSource.ProjectIDs) != 2 {
			t.Fatalf("cross-project source classification=%+v", report)
		}
	})
}

func TestDecodeIgnoresKnownNonEvidenceCodexRecordsWithoutIssue(t *testing.T) {
	fixture := newAdapterFixture(t)
	body := encodedRecord(t, "2026-08-31T14:00:00Z", "session_meta", map[string]any{
		"id": "benign-records", "cwd": fixture.projectA,
		"source": map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": "parent-session"}}},
	}) +
		encodedRecord(t, "2026-08-31T14:00:00Z", "session_meta", map[string]any{"id": "parent-session", "cwd": fixture.projectA, "source": "vscode"}) +
		encodedRecord(t, "2026-08-31T14:00:01Z", "response_item", map[string]any{"type": "message", "id": "user-message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "continue"}}}) +
		encodedRecord(t, "2026-08-31T14:00:02Z", "response_item", map[string]any{"type": "message", "id": "assistant-message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "done"}}}) +
		encodedRecord(t, "2026-08-31T14:00:03Z", "response_item", map[string]any{"type": "reasoning", "summary": []any{}}) +
		encodedRecord(t, "2026-08-31T14:00:04Z", "response_item", map[string]any{"type": "custom_tool_call", "call_id": "call-browser", "name": "browser", "input": "{}"}) +
		encodedRecord(t, "2026-08-31T14:00:05Z", "response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "call-browser", "output": "opaque"}) +
		encodedRecord(t, "2026-08-31T14:00:06Z", "response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "call-browser", "output": "second streamed result"}) +
		encodedRecord(t, "2026-08-31T14:00:07Z", "event_msg", map[string]any{"type": "item_completed"}) +
		encodedRecord(t, "2026-08-31T14:00:08Z", "world_state", map[string]any{"id": "snapshot"}) +
		encodedRecord(t, "2026-08-31T14:00:09Z", "compacted", map[string]any{"message": "opaque summary"}) +
		encodedRecord(t, "2026-08-31T14:00:10Z", "inter_agent_communication_metadata", map[string]any{"sender": "opaque"}) +
		encodedRecord(t, "2026-08-31T14:00:11Z", "response_item", map[string]any{"type": "agent_message", "message": "opaque"}) +
		encodedRecord(t, "2026-08-31T14:00:12Z", "event_msg", map[string]any{"type": "thread_settings_applied", "thread_settings": map[string]any{"model": "opaque"}}) +
		encodedRecord(t, "2026-08-31T14:00:13Z", "event_msg", map[string]any{"type": "agent_reasoning", "text": "opaque"}) +
		encodedRecord(t, "2026-08-31T14:00:14Z", "event_msg", map[string]any{"type": "patch_apply_end", "success": true}) +
		encodedRecord(t, "2026-08-31T14:00:15Z", "event_msg", map[string]any{"type": "sub_agent_activity"}) +
		encodedRecord(t, "2026-08-31T14:00:16Z", "event_msg", map[string]any{"type": "mcp_tool_call_end"}) +
		encodedRecord(t, "2026-08-31T14:00:17Z", "event_msg", map[string]any{"type": "context_compacted"}) +
		encodedRecord(t, "2026-08-31T14:00:18Z", "event_msg", map[string]any{"type": "web_search_end"}) +
		encodedRecord(t, "2026-08-31T14:00:19Z", "event_msg", map[string]any{"type": "thread_rolled_back"}) +
		encodedRecord(t, "2026-08-31T14:00:20Z", "response_item", map[string]any{"type": "web_search_call", "id": "search-1"}) +
		usageEvent("2026-08-31T14:00:21Z")
	if err := os.WriteFile(filepath.Join(fixture.sessions, "benign-records.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "benign-records"))
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)
	if report.TerminalState != memory.Indexed || report.UnsupportedRecords != 0 || len(report.Diagnostics) != 0 {
		t.Fatalf("known non-evidence records became scan issues: %+v", report)
	}
	if len(observations) != 2 {
		t.Fatalf("observations=%d want session start plus user request", len(observations))
	}
}

func TestDecodeRejectsUnrelatedAdditionalSessionMetadata(t *testing.T) {
	fixture := newAdapterFixture(t)
	body := encodedRecord(t, "2026-08-31T14:00:00Z", "session_meta", map[string]any{"id": "metadata-mismatch", "cwd": fixture.projectA}) +
		encodedRecord(t, "2026-08-31T14:00:01Z", "session_meta", map[string]any{"id": "unrelated-session", "cwd": fixture.projectA}) +
		usageEvent("2026-08-31T14:00:02Z")
	if err := os.WriteFile(filepath.Join(fixture.sessions, "metadata-mismatch.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "metadata-mismatch"))
	if err != nil {
		t.Fatal(err)
	}
	_, report := decodeBoundary(t, adapter, boundary)
	if len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != "malformed_payload" {
		t.Fatalf("unrelated metadata was accepted: %+v", report)
	}
}

func TestDecodeCanonicalizesHistoricalCWDThatDroppedRootTrailingSpace(t *testing.T) {
	fixture := newAdapterFixture(t)
	originalRoot := fixture.projectA
	spacedRoot := originalRoot + " "
	if err := os.Rename(originalRoot, spacedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(originalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.projectA = spacedRoot
	fixture.bindings[0] = authenticateBinding(t, "project-a", spacedRoot)
	body := encodedRecord(t, "2026-08-31T14:00:00Z", "session_meta", map[string]any{"id": "trailing-root", "cwd": spacedRoot}) +
		encodedRecord(t, "2026-08-31T14:00:01Z", "turn_context", map[string]any{"cwd": originalRoot}) +
		encodedRecord(t, "2026-08-31T14:00:02Z", "response_item", map[string]any{"type": "message", "id": "request-after-context", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "continue"}}}) +
		usageEvent("2026-08-31T14:00:03Z")
	if err := os.WriteFile(filepath.Join(fixture.sessions, "trailing-root.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "trailing-root"))
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)
	if len(report.Quarantined) != 0 || len(report.Diagnostics) != 0 || report.UnsupportedRecords != 0 {
		t.Fatalf("historical root alias was not recovered: %+v", report)
	}
	if len(observations) != 2 || observations[0].Operation != "session_started" || observations[1].Operation != "user_request" {
		t.Fatalf("observations=%+v", observations)
	}
}

func TestAmbiguousAuthenticatedRootsQuarantineOnlyAffectedRevisions(t *testing.T) {
	fixture := newAdapterFixture(t)
	nested := filepath.Join(fixture.projectA, "ambiguous")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	firstNested := authenticateBinding(t, "project-nested-a", nested)
	secondNested := authenticateBinding(t, "project-nested-b", nested)
	fixture.bindings = []projectidentity.Binding{fixture.bindings[0], firstNested, secondNested}
	input, err := json.Marshal(map[string]string{"cmd": "go test ./...", "workdir": nested})
	if err != nil {
		t.Fatal(err)
	}
	body := encodedRecord(t, "2026-08-31T15:00:00Z", "session_meta", map[string]any{"id": "partially-ambiguous", "cwd": fixture.projectA}) +
		encodedRecord(t, "2026-08-31T15:00:01Z", "response_item", map[string]any{"type": "custom_tool_call", "call_id": "ambiguous-call", "name": "exec_command", "input": string(input)}) +
		usageEvent("2026-08-31T15:00:02Z")
	if err := os.WriteFile(filepath.Join(fixture.sessions, "partially-ambiguous.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "partially-ambiguous"))
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)
	if len(observations) != 1 || observations[0].Operation != "session_started" || len(report.Quarantined) == 0 {
		t.Fatalf("ambiguous observations=%d quarantined=%d report=%+v", len(observations), len(report.Quarantined), report)
	}
	if report.TerminalState != memory.Indexed || report.ProposedSource.SessionID == "" {
		t.Fatalf("one-revision quarantine poisoned source decoding: %+v", report)
	}
	for _, quarantined := range report.Quarantined {
		if quarantined.ReasonCode != "ambiguous_project_root" || len(quarantined.CandidateProjectIDs) < 2 {
			t.Fatalf("quarantine metadata=%+v", quarantined)
		}
	}
}

func TestMalformedRecordMetadataNeverEntersQuarantineAndLaterRecordsContinue(t *testing.T) {
	fixture := newAdapterFixture(t)
	duplicateBinding := authenticateBinding(t, "project-shadow", fixture.projectA)
	fixture.bindings = []projectidentity.Binding{fixture.bindings[0], duplicateBinding}
	badTimestamp := strings.Repeat("RAW-TIMESTAMP-", 80)
	body := fmt.Sprintf("{\"timestamp\":\"2026-08-31T13:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"metadata-quarantine\",\"cwd\":%q}}\n", filepath.ToSlash(fixture.projectA)) +
		fmt.Sprintf("{\"timestamp\":%q,\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"bad-metadata\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"bad timestamp\"}]}}\n", badTimestamp) +
		"{\"timestamp\":\"2026-08-31T13:00:02Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"later-valid\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"later request\"}]}}\n" +
		usageEvent("2026-08-31T13:00:03Z")
	if err := os.WriteFile(filepath.Join(fixture.sessions, "metadata-quarantine.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "metadata-quarantine"))
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)
	if len(observations) != 0 {
		t.Fatalf("ambiguous observations emitted: %+v", observations)
	}
	bodyJSON, err := json.Marshal(report.Quarantined)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bodyJSON), badTimestamp) || strings.Contains(string(bodyJSON), "bad-metadata") {
		t.Fatalf("malformed record metadata entered quarantine: %s", bodyJSON)
	}
	if !hasDiagnostic(report.Diagnostics, "malformed_observation") {
		t.Fatalf("bounded malformed observation diagnostic missing: %+v", report.Diagnostics)
	}
	foundLater := false
	for _, quarantined := range report.Quarantined {
		if quarantined.Subject == "later-valid" {
			foundLater = true
		}
	}
	if !foundLater {
		t.Fatalf("later valid record did not continue to quarantine: %+v", report.Quarantined)
	}
}

func TestDuplicateToolCallIDInvalidatesResultPairing(t *testing.T) {
	fixture := newAdapterFixture(t)
	workdir := filepath.ToSlash(fixture.projectA)
	firstInput, err := json.Marshal(map[string]string{"cmd": "go test ./...", "workdir": workdir})
	if err != nil {
		t.Fatal(err)
	}
	secondInput, err := json.Marshal(map[string]string{"cmd": "go build ./...", "workdir": workdir})
	if err != nil {
		t.Fatal(err)
	}
	body := encodedRecord(t, "2026-08-31T14:00:00Z", "session_meta", map[string]any{"id": "duplicate-call", "cwd": workdir}) +
		encodedRecord(t, "2026-08-31T14:00:01Z", "response_item", map[string]any{"type": "custom_tool_call", "call_id": "call-duplicate", "name": "exec_command", "input": string(firstInput)}) +
		encodedRecord(t, "2026-08-31T14:00:02Z", "response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "call-duplicate", "output": "exit code: 0"}) +
		encodedRecord(t, "2026-08-31T14:00:03Z", "response_item", map[string]any{"type": "custom_tool_call", "call_id": "call-duplicate", "name": "exec_command", "input": string(secondInput)}) +
		encodedRecord(t, "2026-08-31T14:00:04Z", "response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "call-duplicate", "output": "exit code: 0"}) +
		encodedRecord(t, "2026-08-31T14:00:05Z", "response_item", map[string]any{"type": "message", "id": "after-duplicate", "role": "user", "content": []map[string]string{{"type": "input_text", "text": "continue"}}}) +
		usageEvent("2026-08-31T14:00:06Z")
	if err := os.WriteFile(filepath.Join(fixture.sessions, "duplicate-call.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "duplicate-call"))
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)
	for _, observation := range observations {
		if observation.Key.Subject == "call-duplicate" || observation.Fields["tool_id"] == "call-duplicate" {
			t.Fatalf("ambiguous call retained an attributable observation: %+v", observation)
		}
	}
	for _, lineage := range report.Supersessions {
		if lineage.Key.Subject == "call-duplicate" {
			t.Fatalf("ambiguous call retained lineage: %+v", lineage)
		}
	}
	for _, quarantined := range report.Quarantined {
		if quarantined.Subject == "call-duplicate" {
			t.Fatalf("ambiguous call retained quarantine: %+v", quarantined)
		}
	}
	if !hasDiagnostic(report.Diagnostics, "duplicate_tool_call_id") {
		t.Fatalf("duplicate tool-call diagnostic missing: %+v", report.Diagnostics)
	}
	foundLater := false
	for _, observation := range observations {
		if observation.Key.Subject == "after-duplicate" {
			foundLater = true
		}
	}
	if !foundLater {
		t.Fatalf("later record did not decode after duplicate call: %+v", observations)
	}
}

func TestDuplicateApplyPatchCallIDInvalidatesPathFactsQuarantineAndLineage(t *testing.T) {
	fixture := newAdapterFixture(t)
	fixture.bindings = append(fixture.bindings, authenticateBinding(t, "project-b-shadow", fixture.projectB))
	workdir := filepath.ToSlash(fixture.projectA)
	targetA := filepath.ToSlash(filepath.Join(fixture.projectA, "internal", "from-ambiguous-call.go"))
	targetB := filepath.ToSlash(filepath.Join(fixture.projectB, "ambiguous", "from-ambiguous-call.go"))
	patch := "*** Begin Patch\n*** Add File: " + targetA + "\n+safe\n*** Add File: " + targetB + "\n+safe\n*** End Patch"
	firstInput, err := json.Marshal(map[string]string{"patch": patch, "workdir": workdir})
	if err != nil {
		t.Fatal(err)
	}
	secondInput, err := json.Marshal(map[string]string{"patch": "*** Begin Patch\n*** Add File: later.go\n+ignored\n*** End Patch", "workdir": workdir})
	if err != nil {
		t.Fatal(err)
	}
	body := encodedRecord(t, "2026-08-31T15:00:00Z", "session_meta", map[string]any{"id": "duplicate-patch", "cwd": workdir}) +
		encodedRecord(t, "2026-08-31T15:00:01Z", "response_item", map[string]any{"type": "custom_tool_call", "call_id": "call-duplicate-patch", "name": "apply_patch", "input": string(firstInput)}) +
		encodedRecord(t, "2026-08-31T15:00:02Z", "response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "call-duplicate-patch", "output": "Done!"}) +
		encodedRecord(t, "2026-08-31T15:00:03Z", "response_item", map[string]any{"type": "custom_tool_call", "call_id": "call-duplicate-patch", "name": "apply_patch", "input": string(secondInput)}) +
		encodedRecord(t, "2026-08-31T15:00:04Z", "response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "call-duplicate-patch", "output": "Done!"}) +
		encodedRecord(t, "2026-08-31T15:00:05Z", "response_item", map[string]any{"type": "message", "id": "after-duplicate-patch", "role": "user", "content": []map[string]string{{"type": "input_text", "text": "continue after patch"}}}) +
		usageEvent("2026-08-31T15:00:06Z")
	if err := os.WriteFile(filepath.Join(fixture.sessions, "duplicate-patch.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := fixture.adapter(t, "v2", "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "duplicate-patch"))
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)
	for _, observation := range observations {
		if observation.Fields["tool_id"] == "call-duplicate-patch" || observation.Operation == "file_change" {
			t.Fatalf("ambiguous patch retained an attributable observation: %+v", observation)
		}
	}
	for _, lineage := range report.Supersessions {
		if lineage.Key.Kind == "file" {
			t.Fatalf("ambiguous patch retained file lineage: %+v", lineage)
		}
	}
	if len(report.Quarantined) != 0 {
		t.Fatalf("ambiguous patch retained quarantine effects: %+v", report.Quarantined)
	}
	if fmt.Sprint(report.ProposedSource.ProjectIDs) != fmt.Sprint([]string{"project-a"}) {
		t.Fatalf("ambiguous patch retained project effects: %v", report.ProposedSource.ProjectIDs)
	}
	if !hasDiagnostic(report.Diagnostics, "duplicate_tool_call_id") || !hasDiagnostic(report.Diagnostics, "ambiguous_tool_call_output") {
		t.Fatalf("bounded duplicate diagnostics missing: %+v", report.Diagnostics)
	}
	foundLater := false
	for _, observation := range observations {
		if observation.Key.Subject == "after-duplicate-patch" {
			foundLater = true
		}
	}
	if !foundLater {
		t.Fatalf("later unrelated record did not decode: %+v", observations)
	}
}

func hasDiagnostic(diagnostics []memory.Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func usageEvent(timestamp string) string {
	return fmt.Sprintf("{\"timestamp\":%q,\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"last_token_usage\":{\"input_tokens\":1,\"cached_input_tokens\":0,\"cache_write_input_tokens\":0,\"output_tokens\":1,\"reasoning_output_tokens\":0,\"total_tokens\":2},\"total_token_usage\":{\"input_tokens\":1,\"cached_input_tokens\":0,\"cache_write_input_tokens\":0,\"output_tokens\":1,\"reasoning_output_tokens\":0,\"total_tokens\":2}}}}\n", timestamp)
}

func encodedRecord(t *testing.T, timestamp, recordType string, payload any) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"timestamp": timestamp, "type": recordType, "payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	return string(body) + "\n"
}

func TestAdapterVersionCreatesStableKeySuccessorsWithoutMutatingAnActiveSet(t *testing.T) {
	fixture := newAdapterFixture(t)
	fixture.installFixture(t, "session-shared.jsonl")

	v1 := fixture.adapter(t, "v1")
	v1Boundary, err := v1.Freeze(context.Background(), discoverCandidate(t, v1, "session-shared"))
	if err != nil {
		t.Fatal(err)
	}
	v1Observations, _ := decodeBoundary(t, v1, v1Boundary)

	v2 := fixture.adapter(t, "v2", "v1")
	v2Boundary, err := v2.Freeze(context.Background(), discoverCandidate(t, v2, "session-shared"))
	if err != nil {
		t.Fatal(err)
	}
	v2Observations, report := decodeBoundary(t, v2, v2Boundary)
	if len(v1Observations) == 0 || len(v1Observations) != len(v2Observations) {
		t.Fatalf("revision counts v1=%d v2=%d", len(v1Observations), len(v2Observations))
	}
	v1BySlot := make(map[string]memory.ObservationRevision, len(v1Observations))
	for _, observation := range v1Observations {
		v1BySlot[observationSlot(observation.Key)] = observation
	}
	successors := make(map[string]string, len(report.Supersessions))
	for _, item := range report.Supersessions {
		if item.SupersededAdapter != "v1" || item.SuccessorAdapter != "v2" {
			t.Fatalf("adapter lineage=%+v", item)
		}
		wantKeyDigest, err := memory.Digest(item.Key)
		if err != nil {
			t.Fatal(err)
		}
		if item.StableKeyDigest != wantKeyDigest {
			t.Fatalf("lineage stable key digest=%q want %q", item.StableKeyDigest, wantKeyDigest)
		}
		successors[item.StableKeyDigest] = item.SuccessorRevisionID
	}
	for _, current := range v2Observations {
		previous, found := v1BySlot[observationSlot(current.Key)]
		if !found || !reflect.DeepEqual(previous.Key, current.Key) {
			t.Fatalf("stable key missing for v2 revision: %+v", current.Key)
		}
		if previous.RevisionID == current.RevisionID {
			t.Fatalf("adapter version did not change revision ID for %+v", current.Key)
		}
		keyDigest, err := memory.Digest(current.Key)
		if err != nil {
			t.Fatal(err)
		}
		if successors[keyDigest] != current.RevisionID {
			t.Fatalf("key-based successor metadata missing: key=%s new=%s report=%+v", keyDigest, current.RevisionID, report.Supersessions)
		}
	}
}

func observationSlot(key memory.ObservationKey) string {
	return strings.Join([]string{
		key.Provider, key.SessionID, key.SourceIdentity, fmt.Sprint(key.Sequence),
		key.ProjectID, key.Kind, key.Subject,
	}, "\x00")
}

var _ source.Adapter
