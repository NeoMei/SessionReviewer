package main

import (
	"bytes"
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

var requiredFeatures = []struct {
	name  string
	stage string
}{
	{name: "shell_tool", stage: "stable"},
	{name: "apps", stage: "stable"},
	{name: "view_image", stage: "stable"},
	{name: "unified_exec", stage: "stable"},
	{name: "shell_zsh_fork", stage: "under development"},
	{name: "unified_exec_zsh_fork", stage: "under development"},
	{name: "shell_snapshot", stage: "stable"},
	{name: "deferred_executor", stage: "under development"},
	{name: "code_mode", stage: "under development"},
	{name: "code_mode_buffered_exec", stage: "under development"},
	{name: "code_mode_host", stage: "stable"},
	{name: "code_mode_only", stage: "under development"},
	{name: "web_search_request", stage: "deprecated"},
	{name: "web_search_cached", stage: "deprecated"},
	{name: "standalone_web_search", stage: "under development"},
	{name: "memories", stage: "stable"},
	{name: "external_agent_memory_import", stage: "under development"},
	{name: "local_thread_store_compression", stage: "under development"},
	{name: "chronicle", stage: "under development"},
	{name: "exec_permission_approvals", stage: "under development"},
	{name: "hooks", stage: "stable"},
	{name: "request_permissions_tool", stage: "under development"},
	{name: "network_proxy", stage: "experimental"},
	{name: "respect_system_proxy", stage: "under development"},
	{name: "multi_agent", stage: "stable"},
	{name: "multi_agent_v2", stage: "stable"},
	{name: "enable_mcp_apps", stage: "under development"},
	{name: "mcp_2026_07_28", stage: "under development"},
	{name: "deferred_tool_world_state", stage: "under development"},
	{name: "non_prefixed_mcp_tool_names", stage: "under development"},
	{name: "tool_suggest", stage: "stable"},
	{name: "recommended_plugins", stage: "stable"},
	{name: "plugins", stage: "stable"},
	{name: "executor_capability_discovery", stage: "under development"},
	{name: "in_app_browser", stage: "stable"},
	{name: "in_app_updates", stage: "stable"},
	{name: "browser_use", stage: "stable"},
	{name: "browser_use_external", stage: "stable"},
	{name: "browser_use_full_cdp_access", stage: "stable"},
	{name: "computer_use", stage: "stable"},
	{name: "image_generation", stage: "stable"},
	{name: "workspace_dependencies", stage: "stable"},
	{name: "skill_mcp_dependency_install", stage: "stable"},
	{name: "skill_search", stage: "stable"},
	{name: "remote_plugin", stage: "stable"},
	{name: "plugin_sharing", stage: "stable"},
	{name: "default_mode_request_user_input", stage: "under development"},
	{name: "guardian_approval", stage: "stable"},
	{name: "guardianv2", stage: "under development"},
	{name: "goals", stage: "stable"},
	{name: "token_budget", stage: "under development"},
	{name: "rollout_budget", stage: "under development"},
	{name: "current_time_reminder", stage: "under development"},
	{name: "tool_call_mcp_elicitation", stage: "stable"},
	{name: "auth_elicitation", stage: "stable"},
	{name: "artifact", stage: "under development"},
	{name: "realtime_conversation", stage: "under development"},
	{name: "prevent_idle_sleep", stage: "experimental"},
	{name: "remote_compaction_v2", stage: "stable"},
	{name: "use_agent_identity", stage: "under development"},
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
		case "verify-success-with-child":
			spawnChild()
		case "verify-error-with-child":
			spawnChild()
			fmt.Fprintln(os.Stderr, "probe error with inherited stderr")
			os.Exit(12)
		case "verify-writes-cwd":
			_ = os.WriteFile("probe-canary", []byte("must not reach the next probe"), 0o600)
		}
		version := os.Getenv("SESSIONREVIEWER_FAKE_VERSION")
		if version == "" {
			version = defaultVersion
		}
		fmt.Println(version)
		return
	case len(os.Args) == 3 && os.Args[1] == "exec" && os.Args[2] == "--help":
		if mode == "verify-writes-cwd" {
			if _, err := os.Stat("probe-canary"); err == nil {
				os.Exit(13)
			}
		}
		writeExecHelp(mode)
		return
	case len(os.Args) >= 3 && os.Args[1] == "features" && os.Args[2] == "list":
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
		"--disable <FEATURE>", "--config <key=value>",
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
	for index, feature := range requiredFeatures {
		if mode == "verify-missing-feature" && feature.name == "view_image" {
			continue
		}
		stage := feature.stage
		if mode == "verify-unstable-feature" && feature.name == "remote_plugin" {
			stage = "under development"
		}
		state := "false"
		if mode == "verify-enabled-feature" && feature.name == "multi_agent" {
			state = "true"
		}
		if mode == "verify-noncanonical-feature-state" && index == 0 {
			state = "TRUE"
		}
		fmt.Printf("%-40s %-18s %s\n", feature.name, stage, state)
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
	case "invalid-utf8":
		writeEvent(map[string]any{"type": "thread.started", "thread_id": "thread-1"})
		writeEvent(map[string]any{"type": "turn.started"})
		line, _ := json.Marshal(map[string]any{
			"type": "item.completed",
			"item": map[string]any{"id": "item-1", "type": "agent_message", "text": validProposal()},
		})
		marker := []byte("Build a durable review ledger")
		position := bytes.Index(line, marker)
		if position < 0 {
			os.Exit(6)
		}
		line[position] = 0xff
		_, _ = os.Stdout.Write(append(line, '\n'))
		writeEvent(map[string]any{"type": "turn.completed", "usage": validUsage()})
	case "deep-embedded-proposal":
		proposal := strings.TrimSpace(validProposal())
		proposal = strings.TrimSuffix(proposal, "}") + `,"deep":` +
			strings.Repeat("[", 129) + "0" + strings.Repeat("]", 129) + "}"
		writeSuccess(proposal, validUsage())
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
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		os.Exit(5)
	}
	pidPath := os.Getenv("SESSIONREVIEWER_FAKE_CHILD_PID_PATH")
	if pidPath != "" {
		publishPID(pidPath, command.Process.Pid)
	}
}

func publishPID(path string, pid int) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".child-pid-*")
	if err != nil {
		return
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return
	}
	if _, err := io.WriteString(temporary, strconv.Itoa(pid)); err != nil {
		_ = temporary.Close()
		return
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return
	}
	if err := temporary.Close(); err != nil {
		return
	}
	_ = os.Rename(temporaryPath, path)
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
