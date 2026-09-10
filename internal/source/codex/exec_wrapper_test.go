package codex

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecWrapperSingletonLiteralCommandUsesAttributedStructuredResult(t *testing.T) {
	fixture := newAdapterFixture(t)
	input := "// @exec: {\"yield_time_ms\": 10000}\n  text ( await tools.exec_command( {\"cmd\":\"go test ./...\",\"workdir\":" + quotedJSON(t, filepath.ToSlash(fixture.projectA)) + "} ) ) ;  "
	observations, report := decodeToolEnvelopeSessionWithFixture(t, fixture, []map[string]any{
		{"type": "custom_tool_call", "call_id": "wrapper-exec", "name": "exec", "input": input},
		{"type": "custom_tool_call_output", "call_id": "wrapper-exec", "output": wrapperOutput(
			"Script completed\nWall time 0.1 seconds\nOutput:\nexit code: 91",
			`{"exit_code":0,"output":"PASS","wall_time_seconds":0.1}`,
		)},
	})
	started := findToolObservation(observations, "wrapper-exec", "command_started")
	finished := findToolObservation(observations, "wrapper-exec", "command_finished")
	verification := findToolObservation(observations, "wrapper-exec", "verification")
	if started == nil || finished == nil || verification == nil {
		t.Fatalf("wrapper command facts missing: observations=%+v report=%+v", observations, report)
	}
	if started.Key.Subject != "wrapper-exec" || finished.Key.Subject != "wrapper-exec" ||
		started.Fields["command_signature"] != "go:test" || finished.Outcome != "success" ||
		finished.Fields["exit_code"] != "0" || verification.Outcome != "passed" {
		t.Fatalf("wrapper command facts=%+v", observations)
	}
	if started.Ref.SourceHash == "" || finished.Ref.SourceHash == "" || started.Ref.SourceHash == finished.Ref.SourceHash {
		t.Fatalf("source record hashes were not preserved: start=%q finish=%q", started.Ref.SourceHash, finished.Ref.SourceHash)
	}
}

func TestExecWrapperBoundLiteralPatchUsesInputTargetsOnly(t *testing.T) {
	fixture := newAdapterFixture(t)
	target := filepath.Join(fixture.projectA, "internal", "wrapped.go")
	patch := "*** Begin Patch\n*** Add File: internal/wrapped.go\n+package wrapped\n*** End Patch"
	input := "const r = await tools.apply_patch(" + quotedJSON(t, patch) + ");\ntext(r);"
	observations, report := decodeToolEnvelopeSessionWithFixture(t, fixture, []map[string]any{
		{"type": "custom_tool_call", "call_id": "wrapper-patch", "name": "exec", "input": input},
		{"type": "custom_tool_call_output", "call_id": "wrapper-patch", "output": wrapperOutput(
			"Script completed\nWall time 0.1 seconds\nOutput:",
			`{"output":"Success. Updated the following files:\nA output-only.go"}`,
		)},
	})
	changes := toolObservations(observations, "wrapper-patch", "file_change")
	if len(changes) != 1 || changes[0].Object != target || changes[0].Outcome != "success" {
		t.Fatalf("wrapper patch facts=%+v report=%+v", changes, report)
	}
	if strings.Contains(changes[0].Object, "output-only.go") || changes[0].Key.Subject == "wrapper-patch-child" {
		t.Fatalf("output target or fabricated child identity became evidence: %+v", changes[0])
	}
}

func TestExecWrapperOutputSelectorNeverProvesCommandTerminalStatus(t *testing.T) {
	observations, report := decodeToolEnvelopeSession(t, []map[string]any{
		{"type": "custom_tool_call", "call_id": "wrapper-body", "name": "exec", "input": `const r = await tools.exec_command({"cmd":"go test ./..."}); text(r.output);`},
		{"type": "custom_tool_call_output", "call_id": "wrapper-body", "output": wrapperOutput(
			"Script completed\nWall time 0.1 seconds\nOutput:",
			"exit code: 0",
		)},
	})
	if findToolObservation(observations, "wrapper-body", "command_started") == nil {
		t.Fatalf("supported body-only wrapper lost command start: %+v", observations)
	}
	if findToolObservation(observations, "wrapper-body", "command_finished") != nil || findToolObservation(observations, "wrapper-body", "verification") != nil {
		t.Fatalf("body-only selector invented terminal evidence: %+v", observations)
	}
	if report.UnsupportedRecords == 0 || !hasDiagnostic(report.Diagnostics, "unsupported_exec_wrapper") {
		t.Fatalf("body-only terminal gap was not visible: %+v", report)
	}
}

func TestExecWrapperCallWithoutOutputRemainsVisiblePartialCoverage(t *testing.T) {
	observations, report := decodeToolEnvelopeSession(t, []map[string]any{
		{"type": "custom_tool_call", "call_id": "wrapper-missing-output", "name": "exec", "input": `text(await tools.exec_command({"cmd":"go test ./..."}));`},
	})
	if findToolObservation(observations, "wrapper-missing-output", "command_started") == nil {
		t.Fatalf("supported wrapper lost command start: %+v", observations)
	}
	if findToolObservation(observations, "wrapper-missing-output", "command_finished") != nil || findToolObservation(observations, "wrapper-missing-output", "verification") != nil {
		t.Fatalf("missing wrapper output invented terminal evidence: %+v", observations)
	}
	if report.UnsupportedRecords == 0 || !hasDiagnostic(report.Diagnostics, "unsupported_exec_wrapper") {
		t.Fatalf("missing wrapper output disappeared: %+v", report)
	}
}

func TestExecWrapperBodyOnlyPatchNeverProvesOutcome(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: unsafe.go\n+unsafe\n*** End Patch"
	for _, body := range []string{
		"Done!",
		"Failed!",
		"Success. Updated the following files:\nA unsafe.go",
	} {
		observations, report := decodeToolEnvelopeSession(t, []map[string]any{
			{"type": "custom_tool_call", "call_id": "wrapper-body-patch", "name": "exec", "input": "const r = await tools.apply_patch(" + quotedJSON(t, patch) + "); text(r.output);"},
			{"type": "custom_tool_call_output", "call_id": "wrapper-body-patch", "output": wrapperOutput(
				"Script completed\nWall time 0.1 seconds\nOutput:", body,
			)},
		})
		if changes := toolObservations(observations, "wrapper-body-patch", "file_change"); len(changes) != 0 {
			t.Fatalf("body-only patch invented file change for %q: %+v", body, changes)
		}
		if report.UnsupportedRecords == 0 || !hasDiagnostic(report.Diagnostics, "unsupported_exec_wrapper") {
			t.Fatalf("body-only patch outcome disappeared for %q: %+v", body, report)
		}
	}
}

func TestExecWrapperWholeResultPatchRequiresSupportedSuccessBody(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: unsafe.go\n+unsafe\n*** End Patch"
	for _, test := range []struct {
		result          string
		wantFailure     bool
		wantUnsupported bool
	}{
		{result: `{"exit_code":1,"output":"Failed!"}`, wantFailure: true},
		{result: `{"exit_code":1,"output":"Success. Updated the following files:\nA unsafe.go"}`, wantUnsupported: true},
		{result: `{"exit_code":0,"output":"Failed!"}`, wantUnsupported: true},
		{result: `{"exit_code":0,"output":"unexpected wrapper text"}`, wantUnsupported: true},
	} {
		observations, report := decodeToolEnvelopeSession(t, []map[string]any{
			{"type": "custom_tool_call", "call_id": "wrapper-patch-invalid", "name": "exec", "input": "text(await tools.apply_patch(" + quotedJSON(t, patch) + "));"},
			{"type": "custom_tool_call_output", "call_id": "wrapper-patch-invalid", "output": wrapperOutput(
				"Script completed\nWall time 0.1 seconds\nOutput:", test.result,
			)},
		})
		changes := toolObservations(observations, "wrapper-patch-invalid", "file_change")
		if test.wantFailure && (len(changes) != 1 || changes[0].Outcome != "failure") {
			t.Fatalf("structured patch failure was lost: %+v", changes)
		}
		if !test.wantFailure && len(changes) != 0 {
			t.Fatalf("unknown patch result produced change: %+v", changes)
		}
		if test.wantUnsupported && (report.UnsupportedRecords == 0 || !hasDiagnostic(report.Diagnostics, "unsupported_exec_wrapper")) {
			t.Fatalf("unsupported patch result disappeared: %+v", report)
		}
	}
}

func TestExecWrapperJSONStringsPreserveEscapesAndToolLikeText(t *testing.T) {
	input := `text(await tools.exec_command({"cmd":"printf \"}) tools.exec_command({\\\"fake\\\":true})\""}));`
	observations, report := decodeToolEnvelopeSession(t, []map[string]any{
		{"type": "custom_tool_call", "call_id": "wrapper-escaped", "name": "exec", "input": input},
		{"type": "custom_tool_call_output", "call_id": "wrapper-escaped", "output": wrapperOutput(
			"Script completed\nWall time 0.1 seconds\nOutput:",
			`{"exit_code":0,"output":""}`,
		)},
	})
	if findToolObservation(observations, "wrapper-escaped", "command_finished") == nil || report.UnsupportedRecords != 0 {
		t.Fatalf("escaped literal was not preserved as one call: observations=%+v report=%+v", observations, report)
	}
}

func TestExecWrapperDuplicateSourceCallIDInvalidatesPriorFacts(t *testing.T) {
	call := map[string]any{"type": "custom_tool_call", "call_id": "wrapper-duplicate", "name": "exec", "input": `text(await tools.exec_command({"cmd":"go test ./..."}));`}
	output := map[string]any{"type": "custom_tool_call_output", "call_id": "wrapper-duplicate", "output": wrapperOutput(
		"Script completed\nWall time 0.1 seconds\nOutput:",
		`{"exit_code":0,"output":"PASS"}`,
	)}
	observations, report := decodeToolEnvelopeSession(t, []map[string]any{call, output, call, output})
	for _, operation := range []string{"command_started", "command_finished", "verification"} {
		if findToolObservation(observations, "wrapper-duplicate", operation) != nil {
			t.Fatalf("duplicate wrapper retained %s evidence: %+v", operation, observations)
		}
	}
	if !hasDiagnostic(report.Diagnostics, "duplicate_tool_call_id") || !hasDiagnostic(report.Diagnostics, "ambiguous_tool_call_output") {
		t.Fatalf("duplicate wrapper diagnostics missing: %+v", report.Diagnostics)
	}
}

func TestExecWrapperInputBudgetRejectsOversizedSource(t *testing.T) {
	input := strings.Repeat(" ", maxExecWrapperBytes) + `text(await tools.exec_command({"cmd":"go test ./..."}));`
	if _, supported := decodeLiteralExecWrapper(input); supported {
		t.Fatal("oversized wrapper source was accepted")
	}
}

func TestExecWrapperMissingOrAmbiguousResultsRemainVisiblePartialCoverage(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		output any
	}{
		{
			name:   "structured result lacks terminal metadata",
			input:  `text(await tools.exec_command({"cmd":"go test ./..."}));`,
			output: wrapperOutput("Script completed\nWall time 0.1 seconds\nOutput:", `{"output":"PASS"}`),
		},
		{
			name:   "wrapper status without emitted result",
			input:  `text(await tools.exec_command({"cmd":"go test ./..."}));`,
			output: wrapperOutput("Script completed\nWall time 0.1 seconds\nOutput:"),
		},
		{
			name:   "multiple emitted result blocks",
			input:  `text(await tools.exec_command({"cmd":"go test ./..."}));`,
			output: wrapperOutput("Script completed\nWall time 0.1 seconds\nOutput:", `{"exit_code":0,"output":"PASS"}`, `{"exit_code":1,"output":"FAIL"}`),
		},
		{
			name:   "wrapper status is not child status",
			input:  `text(await tools.exec_command({"cmd":"go test ./..."}));`,
			output: wrapperOutput("Script completed\nWall time 0.1 seconds\nOutput:\nexit code: 0", "not structured child metadata"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observations, report := decodeToolEnvelopeSession(t, []map[string]any{
				{"type": "custom_tool_call", "call_id": "wrapper-result", "name": "exec", "input": test.input},
				{"type": "custom_tool_call_output", "call_id": "wrapper-result", "output": test.output},
			})
			if findToolObservation(observations, "wrapper-result", "command_finished") != nil || findToolObservation(observations, "wrapper-result", "verification") != nil {
				t.Fatalf("ambiguous wrapper result invented terminal evidence: %+v", observations)
			}
			if report.UnsupportedRecords == 0 || !hasDiagnostic(report.Diagnostics, "unsupported_exec_wrapper") {
				t.Fatalf("unsupported wrapper result disappeared: %+v", report)
			}
		})
	}
}

func TestExecWrapperRejectsNonLiteralAndNonWholeInputProgramsWithoutLeakingSource(t *testing.T) {
	secret := "PRIVATE-WRAPPER-CANARY"
	tests := []struct {
		name  string
		input string
	}{
		{name: "multiple calls", input: `const a = await tools.exec_command({"cmd":"go test ./..."}); const b = await tools.exec_command({"cmd":"go build ./..."}); text(a); text(b);`},
		{name: "commented call", input: `// text(await tools.exec_command({"cmd":"go test ./..."}));\ntext("` + secret + `");`},
		{name: "string contained call", input: `text("tools.exec_command({\\"cmd\\":\\"` + secret + `\\"})");`},
		{name: "conditional dead code", input: `if (false) { text(await tools.exec_command({"cmd":"` + secret + `"})); }`},
		{name: "loop", input: `for (;;) { text(await tools.exec_command({"cmd":"` + secret + `"})); }`},
		{name: "dynamic argument", input: `const input = {cmd:"` + secret + `"}; text(await tools.exec_command(input));`},
		{name: "binding shadows tools", input: `const tools = await tools.exec_command({"cmd":"go test ./..."}); text(tools);`},
		{name: "binding shadows emitter", input: `const text = await tools.exec_command({"cmd":"go test ./..."}); text(text);`},
		{name: "arbitrary binding", input: `const result = await tools.exec_command({"cmd":"go test ./..."}); text(result);`},
		{name: "reserved binding", input: `const await = await tools.exec_command({"cmd":"go test ./..."}); text(await);`},
		{name: "interpolated argument", input: "text(await tools.exec_command({\"cmd\": `go test ${target}`}));"},
		{name: "computed call", input: `text(await tools["exec_command"]({"cmd":"go test ./..."}));`},
		{name: "trailing executable statement", input: `text(await tools.exec_command({"cmd":"go test ./..."})); text("` + secret + `");`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observations, report := decodeToolEnvelopeSession(t, []map[string]any{
				{"type": "custom_tool_call", "call_id": "wrapper-rejected", "name": "exec", "input": test.input},
				{"type": "custom_tool_call_output", "call_id": "wrapper-rejected", "output": wrapperOutput(
					"Script completed\nWall time 0.1 seconds\nOutput:",
					`{"exit_code":0,"output":"`+secret+`"}`,
				)},
			})
			for _, operation := range []string{"command_started", "command_finished", "verification", "file_change"} {
				if findToolObservation(observations, "wrapper-rejected", operation) != nil {
					t.Fatalf("rejected wrapper invented %s evidence: %+v", operation, observations)
				}
			}
			if report.UnsupportedRecords == 0 || !hasDiagnostic(report.Diagnostics, "unsupported_exec_wrapper") {
				t.Fatalf("rejected wrapper disappeared: %+v", report)
			}
			persisted, err := json.Marshal(struct {
				Observations any
				Diagnostics  any
			}{observations, report.Diagnostics})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(persisted), secret) || strings.Contains(string(persisted), "tools.exec_command") {
				t.Fatalf("wrapper source or output leaked: %s", persisted)
			}
		})
	}
}

func wrapperOutput(status string, emitted ...string) []map[string]string {
	blocks := []map[string]string{{"type": "input_text", "text": status}}
	for _, text := range emitted {
		blocks = append(blocks, map[string]string{"type": "input_text", "text": text})
	}
	return blocks
}

func quotedJSON(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
