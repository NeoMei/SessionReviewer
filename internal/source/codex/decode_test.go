package codex

import (
	"context"
	"encoding/json"
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
	if fmt.Sprint(report.ProjectIDs) != fmt.Sprint([]string{"project-a", "project-b"}) {
		t.Fatalf("report project IDs=%v", report.ProjectIDs)
	}
	record, found, err := fixture.catalog.GetSource("codex", "session-shared")
	if err != nil || !found {
		t.Fatalf("catalog source found=%v err=%v", found, err)
	}
	if record.Usage.TotalTokens != 3 || fmt.Sprint(record.ProjectIDs) != fmt.Sprint([]string{"project-a", "project-b"}) {
		t.Fatalf("shared source catalog record=%+v", record)
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
	if report.UnsupportedRecords < 1 {
		t.Fatalf("unsupported record count=%d", report.UnsupportedRecords)
	}
	record, found, err := fixture.catalog.GetSource("codex", "session-project-a")
	if err != nil || !found || record.Usage.TotalTokens != 15 {
		t.Fatalf("catalog usage record=%+v found=%v err=%v", record, found, err)
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
	if report.MalformedLines != 1 || report.UnsupportedRecords != 1 {
		t.Fatalf("decode report=%+v", report)
	}
	foundLater := false
	for _, observation := range observations {
		if observation.Operation == "user_request" && observation.Key.Subject == "user-after-malformed" {
			foundLater = true
		}
	}
	if !foundLater {
		t.Fatalf("later valid line not decoded: %+v", observations)
	}
	if boundary.Frozen.Location.JSONL == nil || boundary.Frozen.Location.JSONL.Line != 5 {
		t.Fatalf("malformed line did not advance frozen boundary: %+v", boundary.Frozen)
	}
}

func TestAmbiguousAuthenticatedRootsQuarantineOnlyAffectedRevisions(t *testing.T) {
	fixture := newAdapterFixture(t)
	fixture.installFixture(t, "session-project-a.jsonl")
	duplicateBinding := authenticateBinding(t, "project-shadow", fixture.projectA)
	fixture.bindings = []projectidentity.Binding{fixture.bindings[0], duplicateBinding}
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "session-project-a"))
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)
	if len(observations) != 0 || len(report.Quarantined) == 0 {
		t.Fatalf("ambiguous observations=%d quarantined=%d report=%+v", len(observations), len(report.Quarantined), report)
	}
	if report.TerminalState != memory.Indexed || report.CatalogRecordDigest == "" {
		t.Fatalf("one-revision quarantine poisoned source decoding: %+v", report)
	}
	for _, quarantined := range report.Quarantined {
		if quarantined.ReasonCode != "ambiguous_project_root" || len(quarantined.CandidateProjectIDs) != 2 {
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
	if fmt.Sprint(report.ProjectIDs) != fmt.Sprint([]string{"project-a"}) {
		t.Fatalf("ambiguous patch retained project effects: %v", report.ProjectIDs)
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
