package codex

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandAttributionRejectsAggregateShellExit(t *testing.T) {
	revisions, _ := decodeToolEnvelopeSession(t, []map[string]any{
		{"type": "function_call", "call_id": "compound-success", "name": "exec_command", "arguments": `{"cmd":"go test ./... || true"}`},
		{"type": "function_call_output", "call_id": "compound-success", "output": `{"exit_code":0,"output":"FAIL"}`},
		{"type": "function_call", "call_id": "direct-success", "name": "exec_command", "arguments": `{"cmd":"go test ./..."}`},
		{"type": "function_call_output", "call_id": "direct-success", "output": `{"exit_code":0,"output":"PASS"}`},
		{"type": "function_call", "call_id": "direct-failure", "name": "exec_command", "arguments": `{"cmd":"go test ./..."}`},
		{"type": "function_call_output", "call_id": "direct-failure", "output": `{"exit_code":1,"output":"FAIL"}`},
	})

	finished := findToolObservation(revisions, "compound-success", "command_finished")
	if finished == nil || finished.Outcome != "success" || finished.Fields["exit_code"] != "0" {
		t.Fatalf("generic compound command finish missing: %+v", revisions)
	}
	started := findToolObservation(revisions, "compound-success", "command_started")
	if started == nil || started.Fields["command_signature"] != "other" {
		t.Fatalf("compound command signature was not normalized generic: %+v", started)
	}
	for _, fact := range revisions {
		if fact.Key.Kind == "verification" && fact.Fields["tool_id"] == "compound-success" {
			t.Fatal("shell aggregate exit was attributed to child verification")
		}
	}
	for callID, outcome := range map[string]string{"direct-success": "passed", "direct-failure": "failed"} {
		verification := findToolObservation(revisions, callID, "verification")
		if verification == nil || verification.Outcome != outcome {
			t.Fatalf("direct %s verification=%+v want outcome=%s", callID, verification, outcome)
		}
	}
}

func TestCommandClassificationRejectsAmbiguousShellSyntax(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{name: "semicolon", command: "go test ./...; true"},
		{name: "newline", command: "go test ./...\ntrue"},
		{name: "and", command: "go test ./... && true"},
		{name: "or", command: "go test ./... || true"},
		{name: "pipeline", command: "go test ./... | tee result"},
		{name: "background", command: "go test ./... &"},
		{name: "output redirection", command: "go test ./... > result"},
		{name: "input redirection", command: "go test ./... < input"},
		{name: "command substitution", command: "go test $(printf ./...)"},
		{name: "input process substitution", command: "go test <(printf ./...)"},
		{name: "output process substitution", command: "go test >(consume)"},
		{name: "backticks", command: "go test `printf ./...`"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classified := classifyCommand(test.command)
			if classified.signature != "other" || classified.verification != "" || classified.verificationOperation != "" || classified.gitOperation != "" {
				t.Fatalf("ambiguous command received specialized classification: %+v", classified)
			}
		})
	}
}

func TestCommandClassificationPreservesSupportedLiteralCommands(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		signature string
		component string
		operation string
		git       string
	}{
		{name: "go test", command: "go test ./...", signature: "go:test", component: "package", operation: "test"},
		{name: "quoted go test", command: `go test -run 'CommandAttribution|CommandClassification' -count=1 "./internal/source/codex"`, signature: "go:test", component: "package", operation: "test"},
		{name: "go build", command: "go build ./...", signature: "go:build", component: "package", operation: "build"},
		{name: "go vet", command: "go vet ./...", signature: "go:vet", component: "package", operation: "lint"},
		{name: "npm test", command: "npm test", signature: "npm:test", component: "npm:test", operation: "test"},
		{name: "npm run test", command: "npm run test -- --runInBand", signature: "npm:test", component: "npm:test", operation: "test"},
		{name: "npm run build", command: "npm run build", signature: "npm:build", component: "npm:build", operation: "build"},
		{name: "npm run lint", command: "npm run lint", signature: "npm:lint", component: "npm:lint", operation: "lint"},
		{name: "git status", command: "git status --branch --porcelain=v1", signature: "git:status", git: "status"},
		{name: "git head", command: "git rev-parse HEAD", signature: "git:head", git: "head"},
		{name: "git branch", command: "git branch --show-current", signature: "git:branch", git: "branch"},
		{name: "git tag", command: "git describe --exact-match --tags", signature: "git:tag", git: "tag"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classified := classifyCommand(test.command)
			if classified.signature != test.signature || classified.verification != test.component ||
				classified.verificationOperation != test.operation || classified.gitOperation != test.git {
				t.Fatalf("classification=%+v want signature=%q component=%q operation=%q git=%q", classified, test.signature, test.component, test.operation, test.git)
			}
		})
	}
}

func TestCommandClassificationBoundsLiteralParsing(t *testing.T) {
	tests := []string{
		`go test "unterminated`,
		"go test ./... " + strings.Repeat("x", maxClassifiedArgumentBytes+1),
		"go test ./... " + strings.Repeat("x ", maxClassifiedArguments),
		"go test ./... " + strings.Repeat(" ", maxClassifiedCommandBytes),
	}
	for _, command := range tests {
		if classified := classifyCommand(command); classified != (commandClass{signature: "other"}) {
			t.Fatalf("malformed or oversized command received classification: %+v", classified)
		}
	}
}

func TestCommandClassificationWithholdsNonExecutingAndUnknownModes(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{name: "go test short help", command: "go test -h"},
		{name: "go test long help", command: "go test --help"},
		{name: "go test list separated", command: "go test -list TestName ./..."},
		{name: "go test list equals", command: "go test -list=TestName ./..."},
		{name: "go test compile only", command: "go test -c ./..."},
		{name: "go test compile only equals", command: "go test -c=true ./..."},
		{name: "go test zero count separated", command: "go test -count 0 ./..."},
		{name: "go test zero count equals", command: "go test -count=0 ./..."},
		{name: "go test zero count padded", command: "go test -count=00 ./..."},
		{name: "go test invalid count", command: "go test -count=not-a-count ./..."},
		{name: "go test exec wrapper", command: "go test -exec true ./..."},
		{name: "go test exec wrapper equals", command: "go test -exec=true ./..."},
		{name: "go test tool exec wrapper", command: "go test -toolexec true ./..."},
		{name: "go build tool exec wrapper equals", command: "go build -toolexec=true ./..."},
		{name: "go test unknown flag", command: "go test -future-mode ./..."},
		{name: "go build dry run", command: "go build -n ./..."},
		{name: "go build dry run equals", command: "go build -n=true ./..."},
		{name: "go build help", command: "go build -h"},
		{name: "go build unknown flag", command: "go build -future-mode ./..."},
		{name: "go vet dry run", command: "go vet -n ./..."},
		{name: "go vet help", command: "go vet --help"},
		{name: "go vet unknown flag", command: "go vet -future-mode ./..."},
		{name: "npm help", command: "npm help test"},
		{name: "npm test help", command: "npm test --help"},
		{name: "npm test dry run", command: "npm test --dry-run"},
		{name: "npm test dry run equals", command: "npm test --dry-run=true"},
		{name: "npm test ignore scripts", command: "npm test --ignore-scripts"},
		{name: "npm test ignore scripts equals", command: "npm test --ignore-scripts=true"},
		{name: "npm test ignore scripts separated", command: "npm test --ignore-scripts true"},
		{name: "npm run test dry run", command: "npm run test --dry-run"},
		{name: "npm run test unknown flag", command: "npm run test --future-mode"},
		{name: "npm forwarded list tests", command: "npm test -- --listTests"},
		{name: "npm forwarded list", command: "npm run test -- --list"},
		{name: "npm forwarded unknown flag", command: "npm test -- --future-mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classified := classifyCommand(test.command)
			if classified.verification != "" || classified.verificationOperation != "" || classified.gitOperation != "" {
				t.Fatalf("non-executing or unknown mode received specialized classification: %+v", classified)
			}
		})
	}
}

func TestCommandAttributionWithholdsNoExecutionVerification(t *testing.T) {
	commands := []string{
		"go test -list=TestName ./...",
		"go test -c ./...",
		"go build -n ./...",
		"go vet --help",
		"npm test --dry-run",
		"npm run test --ignore-scripts=true",
	}
	items := make([]map[string]any, 0, len(commands)*2)
	for index, command := range commands {
		callID := "no-exec-" + string(rune('a'+index))
		arguments, err := json.Marshal(map[string]string{"cmd": command})
		if err != nil {
			t.Fatal(err)
		}
		items = append(items,
			map[string]any{"type": "function_call", "call_id": callID, "name": "exec_command", "arguments": string(arguments)},
			map[string]any{"type": "function_call_output", "call_id": callID, "output": `{"exit_code":0,"output":"PASS"}`},
		)
	}
	revisions, _ := decodeToolEnvelopeSession(t, items)
	for index := range commands {
		callID := "no-exec-" + string(rune('a'+index))
		if findToolObservation(revisions, callID, "command_finished") == nil {
			t.Fatalf("generic command finish missing for %s: %+v", callID, revisions)
		}
		if verification := findToolObservation(revisions, callID, "verification"); verification != nil {
			t.Fatalf("no-execution command produced verification: %+v", verification)
		}
	}
}

func TestCommandAttributionRejectsCompoundGitOutputAndNormalizesCanaries(t *testing.T) {
	const executableCanary = "PRIVATE-EXECUTABLE-CANARY"
	const operandCanary = "PRIVATE-OPERAND-CANARY"
	const validHead = "0123456789abcdef0123456789abcdef01234567"
	revisions, _ := decodeToolEnvelopeSession(t, []map[string]any{
		{"type": "function_call", "call_id": "compound-git", "name": "exec_command", "arguments": `{"cmd":"git rev-parse HEAD || printf 0123456789abcdef0123456789abcdef01234567"}`},
		{"type": "function_call_output", "call_id": "compound-git", "output": `{"exit_code":0,"output":"` + validHead + `"}`},
		{"type": "function_call", "call_id": "unknown-executable", "name": "exec_command", "arguments": `{"cmd":"` + executableCanary + ` --token secret"}`},
		{"type": "function_call_output", "call_id": "unknown-executable", "output": `{"exit_code":0,"output":"done"}`},
		{"type": "function_call", "call_id": "unknown-operand", "name": "exec_command", "arguments": `{"cmd":"go test '` + operandCanary + `'"}`},
		{"type": "function_call_output", "call_id": "unknown-operand", "output": `{"exit_code":1,"output":"FAIL"}`},
	})
	if findToolObservation(revisions, "compound-git", "command_finished") == nil {
		t.Fatalf("compound git lost generic completion: %+v", revisions)
	}
	if git := findToolObservation(revisions, "compound-git", "git_observation"); git != nil {
		t.Fatalf("compound git fabricated typed observation: %+v", git)
	}
	operandVerification := findToolObservation(revisions, "unknown-operand", "verification")
	if operandVerification == nil || operandVerification.Fields["component"] != "other" || operandVerification.Outcome != "failed" {
		t.Fatalf("literal unknown operand was not conservatively normalized: %+v", operandVerification)
	}
	persisted, err := json.Marshal(revisions)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{executableCanary, operandCanary} {
		if strings.Contains(string(persisted), canary) {
			t.Fatalf("raw command canary persisted: %s", persisted)
		}
	}
}
