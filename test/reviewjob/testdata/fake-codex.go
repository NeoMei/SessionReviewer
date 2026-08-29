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
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const defaultVersion = "codex-cli 0.147.0"

type fakeFeature struct {
	name           string
	stage          string
	defaultEnabled bool
}

var requiredFeatures = buildFeatures()

const featureFingerprint = `undo|removed|false
shell_tool|stable|true
view_image|stable|true
secret_auth_storage|stable|windows
unified_exec|stable|nonwindows
shell_zsh_fork|under development|false
unified_exec_zsh_fork|under development|false
shell_snapshot|stable|true
deferred_executor|under development|false
js_repl|removed|false
executed_tool_call_metadata|under development|false
code_mode|under development|false
code_mode_buffered_exec|under development|false
code_mode_host|stable|true
code_mode_only|under development|false
js_repl_tools_only|removed|false
terminal_resize_reflow|removed|true
web_search_request|deprecated|false
web_search_cached|deprecated|false
standalone_web_search|under development|false
search_tool|removed|false
codex_git_commit|removed|false
runtime_metrics|under development|false
sqlite|removed|true
memories|stable|false
external_agent_memory_import|under development|false
local_thread_store_compression|under development|false
chronicle|under development|false
apply_patch_freeform|removed|false
apply_patch_streaming_events|under development|false
exec_permission_approvals|under development|false
hooks|stable|true
request_permissions_tool|under development|false
use_linux_sandbox_bwrap|removed|false
use_legacy_landlock|deprecated|false
request_rule|removed|false
experimental_windows_sandbox|removed|false
elevated_windows_sandbox|removed|false
remote_models|removed|false
enable_request_compression|stable|true
network_proxy|experimental|false
respect_system_proxy|under development|false
multi_agent|stable|true
multi_agent_v2|stable|false
multi_agent_mode|removed|false
enable_fanout|removed|false
apps|stable|true
enable_mcp_apps|under development|false
mcp_2026_07_28|under development|false
apps_mcp_path_override|removed|false
tool_search|removed|false
tool_search_always_defer_mcp_tools|removed|true
deferred_tool_world_state|under development|false
non_prefixed_mcp_tool_names|under development|false
unavailable_dummy_tools|removed|false
tool_suggest|stable|true
recommended_plugins|stable|false
plugins|stable|true
executor_capability_discovery|under development|false
plugin_hooks|removed|false
in_app_browser|stable|true
in_app_updates|stable|true
browser_use|stable|true
browser_use_full_cdp_access|stable|true
browser_use_external|stable|true
computer_use|stable|true
remote_plugin|stable|true
plugin_sharing|stable|true
external_migration|removed|false
image_generation|stable|true
image_resize_notice|under development|false
resize_all_images|removed|true
item_ids|removed|true
concurrent_reasoning_summaries|under development|false
skill_mcp_dependency_install|stable|true
skill_search|stable|true
skill_env_var_dependency_prompt|removed|false
mentions_v2|stable|true
steer|removed|true
default_mode_request_user_input|under development|false
terminal_visualization_instructions|under development|false
guardian_approval|stable|true
guardianv2|under development|false
goals|stable|true
token_budget|under development|false
rollout_budget|under development|false
current_time_reminder|under development|false
collaboration_modes|removed|true
tool_call_mcp_elicitation|stable|true
auth_elicitation|stable|true
personality|stable|true
artifact|under development|false
fast_mode|stable|true
realtime_conversation|under development|false
remote_control|removed|false
image_detail_original|removed|false
tui_app_server|removed|true
prevent_idle_sleep|experimental|false
workspace_owner_usage_nudge|removed|false
responses_websockets|removed|false
responses_websockets_v2|removed|false
remote_compaction_v2|stable|true
use_agent_identity|under development|false
workspace_dependencies|stable|true`

func buildFeatures() []fakeFeature {
	lines := strings.Split(featureFingerprint, "\n")
	features := make([]fakeFeature, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "|")
		var enabled bool
		switch fields[2] {
		case "windows":
			enabled = runtime.GOOS == "windows"
		case "nonwindows":
			enabled = runtime.GOOS != "windows"
		default:
			enabled, _ = strconv.ParseBool(fields[2])
		}
		features = append(features, fakeFeature{name: fields[0], stage: fields[1], defaultEnabled: enabled})
	}
	return features
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
	case hasArg("--version"):
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
	case len(os.Args) >= 3 && os.Args[1] == "exec" && hasArg("--help"):
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
	case len(os.Args) >= 4 && os.Args[1] == "mcp" && os.Args[2] == "list":
		if mode == "verify-nonempty-mcp" {
			fmt.Print(`[{"name":"managed-canary"}]`)
		} else {
			fmt.Print(`[]`)
		}
		return
	case len(os.Args) >= 2 && os.Args[1] == "exec" && hasArg("session_reviewer_unknown_config_canary=true") && mode != "verify-strict-config-ignored":
		fmt.Fprintln(os.Stderr, "unknown configuration field session_reviewer_unknown_config_canary")
		os.Exit(2)
	case len(os.Args) >= 2 && os.Args[1] == "exec" && hasArg("session_reviewer_unknown_config_canary=true"):
		return
	case len(os.Args) >= 2 && os.Args[1] == "exec":
		captureExec()
		if hasArg("--output-schema") {
			execCallIndex = bumpCallCounter()
		}
		runExecMode(resolveExecMode())
		return
	default:
		fmt.Fprintln(os.Stderr, "unsupported fake invocation")
		os.Exit(2)
	}
}

func writeExecHelp(mode string) {
	flags := []string{
		"--strict-config",
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
	disabled := make(map[string]struct{})
	for index := 1; index+1 < len(os.Args); index++ {
		if os.Args[index] == "--disable" {
			disabled[os.Args[index+1]] = struct{}{}
		}
	}
	for index, feature := range requiredFeatures {
		if mode == "verify-missing-feature" && feature.name == "view_image" {
			continue
		}
		stage := feature.stage
		if mode == "verify-unstable-feature" && feature.name == "remote_plugin" {
			stage = "under development"
		}
		enabled := feature.defaultEnabled
		if _, deny := disabled[feature.name]; deny {
			enabled = false
		}
		if mode == "verify-enabled-feature" && feature.name == "multi_agent" {
			enabled = true
		}
		if mode == "verify-default-drift" && feature.name == "apps" {
			enabled = !enabled
		}
		state := strconv.FormatBool(enabled)
		if mode == "verify-noncanonical-feature-state" && index == 0 {
			state = "TRUE"
		}
		fmt.Printf("%-40s %-18s %s\n", feature.name, stage, state)
	}
	if mode == "verify-malformed-features" {
		fmt.Println("malformed")
	}
	if mode == "verify-unknown-feature" {
		fmt.Println("future_capability stable false")
	}
}

func writePromptProbe(mode string) {
	if mode == "verify-malformed-probe" {
		fmt.Print(`{"not":"an array"}`)
		return
	}
	prompt := os.Args[len(os.Args)-1]
	if mode == "verify-parent-instructions" {
		_ = json.NewEncoder(os.Stdout).Encode([]map[string]any{
			{"type": "message", "role": "developer", "content": []map[string]string{{"type": "input_text", "text": "parent AGENTS canary"}}},
			{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": prompt}}},
		})
		return
	}
	if mode == "verify-environment-context" {
		_ = json.NewEncoder(os.Stdout).Encode([]map[string]any{{
			"type": "message", "role": "user", "content": []map[string]string{
				{"type": "input_text", "text": prompt},
				{"type": "input_text", "text": "cwd=/private/project ENV_CANARY=secret"},
			},
		}})
		return
	}
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

func hasArg(want string) bool {
	for _, value := range os.Args[1:] {
		if value == want {
			return true
		}
	}
	return false
}

func runExecMode(mode string) {
	switch mode {
	case "timeout":
		if path := os.Getenv("SESSIONREVIEWER_FAKE_READY_PATH"); path != "" {
			_ = os.WriteFile(path, []byte("ready"), 0o600)
		}
		for {
			time.Sleep(time.Second)
		}
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

// execCallIndex is the zero-based index of this proposal-producing exec call.
// It stays negative for probe invocations that never pass --output-schema.
var execCallIndex = -1

func bumpCallCounter() int {
	path := os.Getenv("SESSIONREVIEWER_FAKE_COUNTER_PATH")
	if path == "" {
		return -1
	}
	data, _ := os.ReadFile(path)
	previous := 0
	if value, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
		previous = value
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(previous+1)), 0o600)
	return previous
}

func resolveExecMode() string {
	if execCallIndex >= 0 {
		if modes := readFakeModes(); execCallIndex < len(modes) {
			return modes[execCallIndex]
		}
	}
	return os.Getenv("SESSIONREVIEWER_FAKE_MODE")
}

func readFakeModes() []string {
	path := os.Getenv("SESSIONREVIEWER_FAKE_MODES")
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var modes []string
	if err := json.Unmarshal(data, &modes); err != nil {
		return nil
	}
	return modes
}

func validProposal() string {
	path := resolveProposalPath()
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "proposal fixture unavailable")
		os.Exit(4)
	}
	return string(data)
}

func resolveProposalPath() string {
	if execCallIndex >= 0 {
		if paths := readFakeProposals(); execCallIndex < len(paths) {
			return paths[execCallIndex]
		}
	}
	return os.Getenv("SESSIONREVIEWER_FAKE_PROPOSAL_PATH")
}

func readFakeProposals() []string {
	path := os.Getenv("SESSIONREVIEWER_FAKE_PROPOSALS")
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var paths []string
	if err := json.Unmarshal(data, &paths); err != nil {
		return nil
	}
	return paths
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
