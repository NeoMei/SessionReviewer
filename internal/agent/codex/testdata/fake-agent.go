package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const defaultVersion = "codex-cli 0.147.0"

var requiredFeatures = []string{
	"shell_tool",
	"apps",
	"browser_use",
	"browser_use_external",
	"browser_use_full_cdp_access",
	"computer_use",
	"image_generation",
	"workspace_dependencies",
	"skill_search",
	"remote_plugin",
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "fake-child" {
		signal.Ignore(os.Interrupt, syscall.SIGTERM)
		for {
			time.Sleep(time.Second)
		}
	}
	recordCall()
	mode := os.Getenv("SESSIONREVIEWER_FAKE_MODE")
	switch {
	case len(os.Args) == 2 && os.Args[1] == "--version":
		switch mode {
		case "verify-huge-stdout":
			_, _ = io.WriteString(os.Stdout, strings.Repeat("x", 3<<20))
		case "verify-huge-stderr":
			_, _ = io.WriteString(os.Stderr, strings.Repeat("probe-stderr-canary", 20000))
		case "verify-ignored-term-child":
			spawnChild()
			signal.Ignore(os.Interrupt, syscall.SIGTERM)
			for {
				time.Sleep(time.Second)
			}
		case "verify-slow":
			if path := os.Getenv("SESSIONREVIEWER_FAKE_READY_PATH"); path != "" {
				_ = os.WriteFile(path, []byte("ready"), 0o600)
			}
			time.Sleep(300 * time.Millisecond)
		}
		version := os.Getenv("SESSIONREVIEWER_FAKE_VERSION")
		if version == "" {
			version = defaultVersion
		}
		fmt.Println(version)
		return
	case len(os.Args) == 3 && os.Args[1] == "exec" && os.Args[2] == "--help":
		writeExecHelp(mode)
		return
	case len(os.Args) == 3 && os.Args[1] == "features" && os.Args[2] == "list":
		writeFeatures(mode)
		return
	case len(os.Args) >= 3 && os.Args[1] == "debug" && os.Args[2] == "prompt-input":
		writePromptProbe(mode)
		return
	case len(os.Args) >= 2 && os.Args[1] == "exec":
		captureExec()
		runExecMode(mode)
		return
	default:
		fmt.Fprintln(os.Stderr, "unsupported fake invocation")
		os.Exit(2)
	}
}

func writeExecHelp(mode string) {
	flags := []string{
		"--ephemeral", "--ignore-user-config", "--ignore-rules", "--sandbox <MODE>",
		"--json", "--color <COLOR>", "--skip-git-repo-check", "--output-schema <FILE>",
		"--disable <FEATURE>",
	}
	missing := ""
	if mode == "verify-missing-flag" {
		missing = "--ignore-rules"
	}
	fmt.Println("Run Codex non-interactively")
	for _, flag := range flags {
		if !strings.HasPrefix(flag, missing) || missing == "" {
			fmt.Printf("      %s\n", flag)
		}
	}
	fmt.Println("          [possible values: read-only, workspace-write, danger-full-access]")
}

func writeFeatures(mode string) {
	for index, name := range requiredFeatures {
		if mode == "verify-missing-feature" && name == "remote_plugin" {
			continue
		}
		stage := "stable"
		if mode == "verify-unstable-feature" && name == "remote_plugin" {
			stage = "under development"
		}
		state := "false"
		if mode == "verify-noncanonical-feature-state" && index == 0 {
			state = "TRUE"
		}
		fmt.Printf("%-40s %-18s %s\n", name, stage, state)
	}
	if mode == "verify-malformed-features" {
		fmt.Println("malformed")
	}
}

func writePromptProbe(mode string) {
	if mode == "verify-malformed-probe" {
		fmt.Print(`{"not":"an array"}`)
		return
	}
	prompt := os.Args[len(os.Args)-1]
	if mode == "verify-marker-outside-user" {
		_ = json.NewEncoder(os.Stdout).Encode([]map[string]any{{
			"id":     "probe",
			"type":   "message",
			"role":   "developer",
			"marker": prompt,
			"content": []map[string]string{{
				"type": "input_text",
				"text": "different prompt",
			}},
		}})
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode([]map[string]any{{
		"id":   "probe",
		"type": "message",
		"role": "user",
		"content": []map[string]string{{
			"type": "input_text",
			"text": prompt,
		}},
	}})
}

func runExecMode(mode string) {
	switch mode {
	case "", "success":
		writeSuccess(validProposal(), validUsage())
	case "success-stderr-model":
		fmt.Fprintln(os.Stderr, "configured/default model: invented-stderr-model")
		writeSuccess(validProposal(), validUsage())
	case "malformed-jsonl":
		fmt.Println(`{"type":"thread.started","thread_id":"thread-1"}`)
		fmt.Println(`{broken`)
	case "missing-final":
		writeEvent(map[string]any{"type": "thread.started", "thread_id": "thread-1"})
		writeEvent(map[string]any{"type": "turn.started"})
		writeEvent(map[string]any{"type": "turn.completed", "usage": validUsage()})
	case "schema-invalid":
		writeSuccess(`{"schema_version":1}`, validUsage())
	case "tool-call":
		writeEvent(map[string]any{"type": "thread.started", "thread_id": "thread-1"})
		writeEvent(map[string]any{"type": "turn.started"})
		writeEvent(map[string]any{"type": "item.completed", "item": map[string]any{"id": "item-tool", "type": "mcp_tool_call", "server": "secret", "tool": "write"}})
		writeAgentAndUsage(validProposal(), validUsage())
	case "normalized-tool-request":
		writeEvent(map[string]any{"type": "tool.request", "name": "write"})
		writeSuccess(validProposal(), validUsage())
	case "auth-error":
		writeEvent(map[string]any{"type": "thread.started", "thread_id": "thread-1"})
		writeEvent(map[string]any{"type": "turn.started"})
		writeEvent(map[string]any{"type": "turn.failed", "error": map[string]any{"message": "401 Unauthorized at /private/job/auth-canary"}})
		fmt.Fprintln(os.Stderr, "secret stderr /private/job/stderr-canary")
		os.Exit(7)
	case "timeout":
		for {
			time.Sleep(time.Second)
		}
	case "ignored-term-child":
		spawnChild()
		signal.Ignore(os.Interrupt, syscall.SIGTERM)
		for {
			time.Sleep(time.Second)
		}
	case "huge-stdout":
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", 9<<20)+"\n")
	case "huge-stderr":
		_, _ = io.WriteString(os.Stderr, strings.Repeat("stderr-canary", 100000))
		writeSuccess(validProposal(), validUsage())
		os.Exit(9)
	case "exit-after-valid-output":
		spawnChild()
		writeSuccess(validProposal(), validUsage())
		os.Exit(11)
	case "success-with-child":
		spawnChild()
		writeSuccess(validProposal(), validUsage())
	case "invalid-usage":
		usage := validUsage()
		usage["cached_input_tokens"] = int64(1000)
		writeSuccess(validProposal(), usage)
	case "missing-usage":
		writeEvent(map[string]any{"type": "thread.started", "thread_id": "thread-1"})
		writeEvent(map[string]any{"type": "turn.started"})
		writeEvent(map[string]any{"type": "item.completed", "item": map[string]any{"id": "item-1", "type": "agent_message", "text": validProposal()}})
		writeEvent(map[string]any{"type": "turn.completed"})
	case "duplicate-final":
		writeEvent(map[string]any{"type": "thread.started", "thread_id": "thread-1"})
		writeEvent(map[string]any{"type": "turn.started"})
		writeEvent(map[string]any{"type": "item.completed", "item": map[string]any{"id": "item-1", "type": "agent_message", "text": validProposal()}})
		writeAgentAndUsage(validProposal(), validUsage())
	case "event-after-complete":
		writeSuccess(validProposal(), validUsage())
		writeEvent(map[string]any{"type": "item.completed", "item": map[string]any{"id": "late", "type": "reasoning", "text": "late"}})
	case "model-spoof":
		writeEvent(map[string]any{"type": "thread.started", "thread_id": "thread-1"})
		writeEvent(map[string]any{"type": "turn.started"})
		writeEvent(map[string]any{"type": "item.completed", "item": map[string]any{"id": "item-1", "type": "agent_message", "text": validProposal()}})
		writeEvent(map[string]any{"type": "turn.completed", "model": "invented-model", "usage": validUsage()})
	case "duplicate-json-key":
		fmt.Println(`{"type":"thread.started","type":"thread.started","thread_id":"thread-1"}`)
	case "unknown-field":
		writeEvent(map[string]any{"type": "thread.started", "thread_id": "thread-1", "model": "must-not-be-trusted"})
	case "unknown-event":
		writeEvent(map[string]any{"type": "thread.started", "thread_id": "thread-1"})
		writeEvent(map[string]any{"type": "turn.started"})
		writeEvent(map[string]any{"type": "future.event"})
	case "trailing-no-newline":
		writeEvent(map[string]any{"type": "thread.started", "thread_id": "thread-1"})
		writeEvent(map[string]any{"type": "turn.started"})
		writeEvent(map[string]any{"type": "item.completed", "item": map[string]any{"id": "item-1", "type": "agent_message", "text": validProposal()}})
		data, _ := json.Marshal(map[string]any{"type": "turn.completed", "usage": validUsage()})
		_, _ = os.Stdout.Write(data)
	case "turn-failed-before-start":
		writeEvent(map[string]any{"type": "thread.started", "thread_id": "thread-1"})
		writeEvent(map[string]any{"type": "turn.failed", "error": map[string]any{"message": "401 failure before turn"}})
	default:
		fmt.Fprintln(os.Stderr, "unknown fake mode")
		os.Exit(2)
	}
}

func writeSuccess(proposal string, usage map[string]int64) {
	writeEvent(map[string]any{"type": "thread.started", "thread_id": "thread-1"})
	writeEvent(map[string]any{"type": "turn.started"})
	writeEvent(map[string]any{"type": "item.completed", "item": map[string]any{"id": "reasoning-1", "type": "reasoning", "text": "bounded"}})
	writeAgentAndUsage(proposal, usage)
}

func writeAgentAndUsage(proposal string, usage map[string]int64) {
	writeEvent(map[string]any{"type": "item.completed", "item": map[string]any{"id": "item-1", "type": "agent_message", "text": proposal}})
	writeEvent(map[string]any{"type": "turn.completed", "usage": usage})
}

func writeEvent(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		os.Exit(3)
	}
}

func validUsage() map[string]int64 {
	return map[string]int64{
		"input_tokens":             101,
		"cached_input_tokens":      11,
		"cache_write_input_tokens": 7,
		"output_tokens":            23,
		"reasoning_output_tokens":  3,
	}
}

func validProposal() string {
	path := os.Getenv("SESSIONREVIEWER_FAKE_PROPOSAL_PATH")
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "proposal fixture unavailable")
		os.Exit(4)
	}
	return string(data)
}

func spawnChild() {
	command := exec.Command(os.Args[0], "fake-child")
	command.Env = os.Environ()
	if err := command.Start(); err != nil {
		os.Exit(5)
	}
	pidPath := os.Getenv("SESSIONREVIEWER_FAKE_CHILD_PID_PATH")
	if pidPath != "" {
		_ = os.WriteFile(pidPath, []byte(strconv.Itoa(command.Process.Pid)), 0o600)
	}
}

func recordCall() {
	path := os.Getenv("SESSIONREVIEWER_FAKE_CALLS_PATH")
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_ = json.NewEncoder(file).Encode(os.Args[1:])
}

func captureExec() {
	path := os.Getenv("SESSIONREVIEWER_FAKE_CAPTURE_PATH")
	if path == "" {
		return
	}
	stdin, _ := io.ReadAll(os.Stdin)
	schemaPath := ""
	for index, value := range os.Args {
		if value == "--output-schema" && index+1 < len(os.Args) {
			schemaPath = os.Args[index+1]
		}
	}
	schema, _ := os.ReadFile(schemaPath)
	mode := uint32(0)
	if info, err := os.Stat(schemaPath); err == nil {
		mode = uint32(info.Mode().Perm())
	}
	cwdMode := uint32(0)
	if info, err := os.Stat("."); err == nil {
		cwdMode = uint32(info.Mode().Perm())
	}
	capture := map[string]any{
		"args":        os.Args[1:],
		"stdin":       string(stdin),
		"cwd":         mustGetwd(),
		"schema_path": schemaPath,
		"schema":      string(schema),
		"schema_mode": mode,
		"cwd_mode":    cwdMode,
	}
	data, _ := json.Marshal(capture)
	_ = os.WriteFile(path, data, 0o600)
}

func mustGetwd() string {
	value, _ := os.Getwd()
	value, _ = filepath.Abs(value)
	return value
}
