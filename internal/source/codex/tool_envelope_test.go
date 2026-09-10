package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/source"
)

func TestModernToolExecCommandUsesStructuredTerminalMetadata(t *testing.T) {
	tests := []struct {
		name               string
		output             any
		wantFinished       bool
		wantCommandOutcome string
		wantVerification   string
		wantExitCode       string
	}{
		{
			name:         "success",
			output:       `{"exit_code":0,"output":"PASS"}`,
			wantFinished: true, wantCommandOutcome: "success", wantVerification: "passed", wantExitCode: "0",
		},
		{
			name:         "authoritative failure ignores success prose in stdout",
			output:       `{"exit_code":1,"output":"exit code: 0"}`,
			wantFinished: true, wantCommandOutcome: "failure", wantVerification: "failed", wantExitCode: "1",
		},
		{
			name:   "live session without exit is unfinished",
			output: `{"exit_code":null,"output":"PASS","session_id":42}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observations, _ := decodeToolEnvelopeSession(t, []map[string]any{
				{"type": "function_call", "call_id": "modern-exec", "name": "exec_command", "arguments": `{"cmd":"go test ./..."}`},
				{"type": "function_call_output", "call_id": "modern-exec", "output": test.output},
			})
			started := findToolObservation(observations, "modern-exec", "command_started")
			if started == nil || started.Fields["command_signature"] != "go:test" {
				t.Fatalf("modern command start missing: %+v", observations)
			}
			finished := findToolObservation(observations, "modern-exec", "command_finished")
			verification := findToolObservation(observations, "modern-exec", "verification")
			if !test.wantFinished {
				if finished != nil || verification != nil {
					t.Fatalf("unfinished command produced terminal facts: %+v", observations)
				}
				return
			}
			if finished == nil || finished.Outcome != test.wantCommandOutcome || finished.Fields["exit_code"] != test.wantExitCode {
				t.Fatalf("command finish=%+v want outcome=%s exit=%s", finished, test.wantCommandOutcome, test.wantExitCode)
			}
			if verification == nil || verification.Outcome != test.wantVerification || verification.Fields["exit_code"] != test.wantExitCode {
				t.Fatalf("verification=%+v want outcome=%s exit=%s", verification, test.wantVerification, test.wantExitCode)
			}
		})
	}
}

func TestTerminalMetadataRejectsSpoofsConflictsAndWrapperCompletion(t *testing.T) {
	tests := []struct {
		name       string
		output     any
		wantFinish bool
		wantExit   string
	}{
		{name: "direct header", output: "Process exited with code 0\nOutput:\nPASS", wantFinish: true, wantExit: "0"},
		{name: "direct marker in body", output: "Output:\nProcess exited with code 0"},
		{name: "legacy marker in delimited body", output: "Output:\nexit code: 0"},
		{name: "conflicting legacy markers", output: "exit code: 0\nexit code: 1"},
		{name: "wrapper completion only", output: "Script completed"},
		{name: "contradictory finished and live", output: `{"exit_code":0,"output":"PASS","session_id":42}`},
		{name: "malformed structured exit", output: `{"exit_code":"0","output":"PASS"}`},
		{name: "duplicate structured exit", output: `{"exit_code":0,"exit_code":1,"output":"PASS"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observations, _ := decodeToolEnvelopeSession(t, []map[string]any{
				{"type": "custom_tool_call", "call_id": "terminal-case", "name": "exec_command", "input": `{"cmd":"go test ./..."}`},
				{"type": "custom_tool_call_output", "call_id": "terminal-case", "output": test.output},
			})
			finished := findToolObservation(observations, "terminal-case", "command_finished")
			if !test.wantFinish {
				if finished != nil || findToolObservation(observations, "terminal-case", "verification") != nil {
					t.Fatalf("untrusted terminal data produced verified facts: %+v", observations)
				}
				return
			}
			if finished == nil || finished.Fields["exit_code"] != test.wantExit || finished.Outcome != "success" {
				t.Fatalf("trusted direct header not decoded: %+v", observations)
			}
		})
	}
}

func TestToolEnvelopeKeepsCustomCallsAndUnknownOrReasoningPrivate(t *testing.T) {
	observations, report := decodeToolEnvelopeSession(t, []map[string]any{
		{"type": "custom_tool_call", "call_id": "legacy-exec", "name": "exec_command", "input": `{"cmd":"go test ./..."}`},
		{"type": "custom_tool_call_output", "call_id": "legacy-exec", "output": "exit code: 0"},
		{"type": "function_call", "call_id": "unknown-modern", "name": "unrecognized_tool", "arguments": `{"private":"MUST-NOT-PERSIST"}`},
		{"type": "function_call_output", "call_id": "unknown-modern", "output": "MUST-NOT-PERSIST"},
		{"type": "reasoning", "summary": []any{"PRIVATE-HIDDEN"}},
	})
	if findToolObservation(observations, "legacy-exec", "command_finished") == nil {
		t.Fatalf("existing custom call stopped working: %+v", observations)
	}
	for _, observation := range observations {
		if observation.Key.Subject == "unknown-modern" || observation.Fields["tool_id"] == "unknown-modern" {
			t.Fatalf("unknown tool became evidence: %+v", observation)
		}
	}
	persisted, err := json.Marshal(struct {
		Observations []memory.ObservationRevision
		Diagnostics  any
	}{observations, report.Diagnostics})
	if err != nil {
		t.Fatal(err)
	}
	if containsAny(string(persisted), "MUST-NOT-PERSIST", "PRIVATE-HIDDEN") {
		t.Fatalf("opaque tool or reasoning data persisted: %s", persisted)
	}
}

func TestToolEnvelopeDuplicateIDAcrossLegacyAndModernInvalidatesResults(t *testing.T) {
	observations, report := decodeToolEnvelopeSession(t, []map[string]any{
		{"type": "custom_tool_call", "call_id": "cross-form", "name": "exec_command", "input": `{"cmd":"go test ./..."}`},
		{"type": "custom_tool_call_output", "call_id": "cross-form", "output": "exit code: 0"},
		{"type": "function_call", "call_id": "cross-form", "name": "exec_command", "arguments": `{"cmd":"go build ./..."}`},
		{"type": "function_call_output", "call_id": "cross-form", "output": `{"exit_code":0,"output":"PASS"}`},
	})
	for _, observation := range observations {
		if observation.Key.Subject == "cross-form" || observation.Fields["tool_id"] == "cross-form" {
			t.Fatalf("duplicate cross-form call retained facts: %+v", observation)
		}
	}
	if !hasDiagnostic(report.Diagnostics, "duplicate_tool_call_id") || !hasDiagnostic(report.Diagnostics, "ambiguous_tool_call_output") {
		t.Fatalf("duplicate diagnostics missing: %+v", report.Diagnostics)
	}
}

func TestNativePatchRawInputUsesOnlyInputTargetsOnSuccess(t *testing.T) {
	fixture := newAdapterFixture(t)
	target := filepath.Join(fixture.projectA, "internal", "safe.go")
	other := filepath.Join(fixture.projectA, "internal", "stdout-only.go")
	patch := "*** Begin Patch\n*** Add File: internal/safe.go\n+package safe\n*** End Patch"
	observations, _ := decodeToolEnvelopeSessionWithFixture(t, fixture, []map[string]any{
		{"type": "custom_tool_call", "call_id": "native-patch", "name": "apply_patch", "input": patch},
		{"type": "custom_tool_call_output", "call_id": "native-patch", "output": "Success. Updated the following files:\nA internal/stdout-only.go"},
	})
	changes := toolObservations(observations, "native-patch", "file_change")
	if len(changes) != 1 || changes[0].Object != target || changes[0].Outcome != "success" || changes[0].Fields["path"] != target {
		t.Fatalf("native patch facts=%+v want only %s", changes, target)
	}
	if changes[0].Object == other {
		t.Fatalf("output-only target became evidence: %+v", changes[0])
	}
}

func TestNativePatchMalformedFailedAndDuplicateNeverProduceSuccess(t *testing.T) {
	tests := []struct {
		name  string
		items []map[string]any
	}{
		{
			name: "malformed raw patch",
			items: []map[string]any{
				{"type": "custom_tool_call", "call_id": "patch-case", "name": "apply_patch", "input": "*** Add File: unsafe.go\n+unsafe"},
				{"type": "custom_tool_call_output", "call_id": "patch-case", "output": "Success. Updated the following files:\nA unsafe.go"},
			},
		},
		{
			name: "failed patch",
			items: []map[string]any{
				{"type": "custom_tool_call", "call_id": "patch-case", "name": "apply_patch", "input": "*** Begin Patch\n*** Add File: unsafe.go\n+unsafe\n*** End Patch"},
				{"type": "custom_tool_call_output", "call_id": "patch-case", "output": "Failed!"},
			},
		},
		{
			name: "structured failure overrides success-shaped body",
			items: []map[string]any{
				{"type": "custom_tool_call", "call_id": "patch-case", "name": "apply_patch", "input": "*** Begin Patch\n*** Add File: unsafe.go\n+unsafe\n*** End Patch"},
				{"type": "custom_tool_call_output", "call_id": "patch-case", "output": `{"exit_code":1,"output":"Success. Updated the following files:\nA unsafe.go"}`},
			},
		},
		{
			name: "zero exit with unknown patch body",
			items: []map[string]any{
				{"type": "custom_tool_call", "call_id": "patch-case", "name": "apply_patch", "input": "*** Begin Patch\n*** Add File: unsafe.go\n+unsafe\n*** End Patch"},
				{"type": "custom_tool_call_output", "call_id": "patch-case", "output": `{"exit_code":0,"output":"unexpected wrapper text"}`},
			},
		},
		{
			name: "zero exit with empty patch body",
			items: []map[string]any{
				{"type": "custom_tool_call", "call_id": "patch-case", "name": "apply_patch", "input": "*** Begin Patch\n*** Add File: unsafe.go\n+unsafe\n*** End Patch"},
				{"type": "custom_tool_call_output", "call_id": "patch-case", "output": `{"exit_code":0,"output":""}`},
			},
		},
		{
			name: "duplicate across raw and modern",
			items: []map[string]any{
				{"type": "custom_tool_call", "call_id": "patch-case", "name": "apply_patch", "input": "*** Begin Patch\n*** Add File: unsafe.go\n+unsafe\n*** End Patch"},
				{"type": "custom_tool_call_output", "call_id": "patch-case", "output": "Success. Updated the following files:\nA unsafe.go"},
				{"type": "function_call", "call_id": "patch-case", "name": "apply_patch", "arguments": `{"patch":"*** Begin Patch\\n*** Add File: other.go\\n+other\\n*** End Patch"}`},
				{"type": "function_call_output", "call_id": "patch-case", "output": "Done!"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observations, _ := decodeToolEnvelopeSession(t, test.items)
			for _, observation := range toolObservations(observations, "patch-case", "file_change") {
				if observation.Outcome == "success" {
					t.Fatalf("invalid patch produced successful change: %+v", observations)
				}
			}
		})
	}
}

func decodeToolEnvelopeSession(t *testing.T, items []map[string]any) ([]memory.ObservationRevision, source.DecodeReport) {
	t.Helper()
	return decodeToolEnvelopeSessionWithFixture(t, newAdapterFixture(t), items)
}

func decodeToolEnvelopeSessionWithFixture(t *testing.T, fixture *adapterFixture, items []map[string]any) ([]memory.ObservationRevision, source.DecodeReport) {
	t.Helper()
	const sessionID = "tool-envelope"
	body := encodedRecord(t, "2026-09-08T00:00:00Z", "session_meta", map[string]any{"id": sessionID, "cwd": filepath.ToSlash(fixture.projectA)})
	for index, item := range items {
		body += encodedRecord(t, fmt.Sprintf("2026-09-08T00:00:%02dZ", index+1), "response_item", item)
	}
	body += usageEvent(fmt.Sprintf("2026-09-08T00:00:%02dZ", len(items)+1))
	if err := os.WriteFile(filepath.Join(fixture.sessions, sessionID+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := fixture.adapter(t, "v2", "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, sessionID))
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)
	return observations, report
}

func findToolObservation(observations []memory.ObservationRevision, callID, operation string) *memory.ObservationRevision {
	for index := range observations {
		if observations[index].Operation == operation && observations[index].Fields["tool_id"] == callID {
			return &observations[index]
		}
	}
	return nil
}

func toolObservations(observations []memory.ObservationRevision, callID, operation string) []memory.ObservationRevision {
	var found []memory.ObservationRevision
	for _, observation := range observations {
		if observation.Operation == operation && observation.Fields["tool_id"] == callID {
			found = append(found, observation)
		}
	}
	return found
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
