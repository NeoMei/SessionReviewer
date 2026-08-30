// Package codex implements the reviewed Codex CLI proposal-only Adapter.
package codex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neomei/SessionReviewer/internal/agent"
)

const (
	capabilityProbe = "SESSIONREVIEWER_CODEX_CAPABILITY_PROBE_V1"
	probeTimeout    = 8 * time.Second
	maxProbeStdout  = 2 << 20
	maxProbeStderr  = 256 << 10
)

var (
	versionPattern = regexp.MustCompile(`^codex-cli (0\.150\.1)\r?\n?$`)
	requiredFlags  = []string{
		"strict-config",
		"ephemeral",
		"ignore-user-config",
		"ignore-rules",
		"sandbox",
		"json",
		"color",
		"skip-git-repo-check",
		"output-schema",
		"disable",
		"config",
	}
	reviewedDisabledFeatureNames = []string{
		"shell_tool", "apps", "view_image", "shell_zsh_fork",
		"unified_exec_zsh_fork", "shell_snapshot", "deferred_executor", "code_mode",
		"code_mode_buffered_exec", "code_mode_host", "code_mode_only", "code_mode_interrupt", "web_search_request",
		"web_search_cached", "standalone_web_search", "memories", "external_agent_memory_import",
		"local_thread_store_compression", "chronicle", "exec_permission_approvals", "hooks",
		"request_permissions_tool", "network_proxy", "respect_system_proxy", "multi_agent",
		"multi_agent_v2", "enable_mcp_apps", "mcp_2026_07_28", "deferred_tool_world_state",
		"non_prefixed_mcp_tool_names", "tool_suggest", "recommended_plugins", "plugins",
		"executor_capability_discovery", "in_app_browser", "in_app_updates", "browser_use",
		"in_app_chat", "in_app_dictation", "in_app_local_automation",
		"browser_use_external", "browser_use_full_cdp_access", "computer_use", "image_generation",
		"workspace_dependencies", "skill_mcp_dependency_install", "skill_search", "remote_plugin",
		"plugin_sharing", "default_mode_request_user_input", "guardian_approval", "guardianv2",
		"guardian_enhanced_node_repl_transcripts", "guardian_ext", "guardian_node_repl_transcript_images",
		"guardian_reuse_parent_compaction", "goals", "token_budget", "rollout_budget", "current_time_reminder",
		"tool_call_mcp_elicitation", "auth_elicitation", "artifact", "realtime_conversation",
		"prevent_idle_sleep", "remote_compaction_v2", "use_agent_identity",
		"apply_patch_preserve_line_endings", "background_paginated_rollout_migration", "content_item_kinds",
		"cwd_relative_turn_diffs", "psp", "retain_client_developer_messages", "send_async_message",
		"shell_snapshot_v2", "transcript_v2", "unified_image_budget", "personality",
	}
	fixedConfigOverrides = []string{
		`web_search="disabled"`,
		"tools.update_plan.enabled=false",
		"tools.experimental_request_user_input.enabled=false",
		"project_doc_max_bytes=0",
		"project_root_markers=[]",
		"project_doc_fallback_filenames=[]",
		"include_environment_context=false",
		"include_permissions_instructions=false",
		"include_apps_instructions=false",
		"include_collaboration_mode_instructions=false",
		"skills.include_instructions=false",
		"orchestrator.skills.enabled=false",
		"orchestrator.mcp.enabled=false",
		`developer_instructions=""`,
		`instructions=""`,
		"mcp_servers={}",
	}
	reviewedFeatures = mustBuildReviewedFeatures()
)

type reviewedFeature struct {
	name           string
	stage          string
	defaultEnabled bool
	disabled       bool
}

// reviewedFeatureFingerprint contains every capability name that the 0.150.1
// restricted contract depends on. Defaults are retained for deterministic
// fixtures; production verification requires all rows and requires every
// restricted non-removed capability to be disabled.
const reviewedFeatureFingerprint = `undo|removed|false
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
workspace_dependencies|stable|true
apply_patch_preserve_line_endings|under development|false
background_paginated_rollout_migration|under development|false
code_mode_interrupt|under development|false
content_item_kinds|under development|false
cwd_relative_turn_diffs|under development|false
guardian_enhanced_node_repl_transcripts|under development|false
guardian_ext|under development|false
guardian_node_repl_transcript_images|under development|false
guardian_reuse_parent_compaction|under development|false
in_app_chat|stable|true
in_app_dictation|stable|true
in_app_local_automation|stable|true
psp|under development|false
retain_client_developer_messages|under development|false
send_async_message|removed|false
shell_snapshot_v2|under development|false
transcript_v2|under development|false
unified_image_budget|under development|false`

func mustBuildReviewedFeatures() []reviewedFeature {
	disabled := make(map[string]struct{}, len(reviewedDisabledFeatureNames))
	for _, name := range reviewedDisabledFeatureNames {
		disabled[name] = struct{}{}
	}
	lines := strings.Split(reviewedFeatureFingerprint, "\n")
	features := make([]reviewedFeature, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "|")
		if len(fields) != 3 {
			panic("malformed reviewed Codex feature fingerprint")
		}
		var enabled bool
		switch fields[2] {
		case "windows":
			enabled = runtime.GOOS == "windows"
		case "nonwindows":
			enabled = runtime.GOOS != "windows"
		default:
			parsed, err := strconv.ParseBool(fields[2])
			if err != nil {
				panic("malformed reviewed Codex feature default")
			}
			enabled = parsed
		}
		_, deny := disabled[fields[0]]
		features = append(features, reviewedFeature{
			name: fields[0], stage: fields[1], defaultEnabled: enabled, disabled: deny,
		})
	}
	if len(features) != 122 {
		panic("incomplete reviewed Codex feature fingerprint")
	}
	return features
}

// Adapter verifies and invokes one physically pinned Codex executable.
type Adapter struct {
	mu                 sync.Mutex
	executable         string
	executableIdentity *executableIdentity
	capability         agent.Capability
	active             *activeRun
	verifying          bool
}

type executableIdentity struct {
	info   os.FileInfo
	digest [sha256.Size]byte
}

var _ agent.Adapter = (*Adapter)(nil)

// New returns an unverified Adapter. Verify must succeed before generation.
func New() *Adapter { return &Adapter{} }

// Verify pins and probes one absolute physical executable against the reviewed
// restricted-read-only contract. Codex retains one non-disableable core exec
// capability, so the returned capability never claims an empty tool registry;
// generation still rejects every observed tool event.
func (adapter *Adapter) Verify(ctx context.Context, path string) (agent.Capability, error) {
	if adapter == nil {
		return agent.Capability{}, agent.NewError(agent.CodeUnconfigured, errors.New("nil Codex adapter"))
	}
	adapter.mu.Lock()
	if adapter.active != nil || adapter.verifying {
		adapter.mu.Unlock()
		return agent.Capability{}, agent.NewError(agent.CodeBusy, errors.New("Codex adapter is active"))
	}
	adapter.verifying = true
	adapter.executable = ""
	adapter.executableIdentity = nil
	adapter.capability = agent.Capability{}
	adapter.mu.Unlock()
	succeeded := false
	defer func() {
		adapter.mu.Lock()
		if !succeeded {
			adapter.executable = ""
			adapter.executableIdentity = nil
			adapter.capability = agent.Capability{}
		}
		adapter.verifying = false
		adapter.mu.Unlock()
	}()

	physical, identity, err := resolveExecutable(path)
	if err != nil {
		return agent.Capability{}, agent.NewError(agent.CodeUnconfigured, err)
	}
	probeRoot, err := createOwnedPrivateRoot("session-reviewer-codex-verify-")
	if err != nil {
		return agent.Capability{}, agent.NewError(agent.CodeUnconfigured, err)
	}
	defer probeRoot.cleanupOwned()

	versionOutput, _, err := runProbeInPrivateDirectory(ctx, physical, identity, probeRoot, []string{"--strict-config", "--version"})
	if err != nil {
		return agent.Capability{}, mapProbeError(ctx, err)
	}
	match := versionPattern.FindSubmatch(versionOutput)
	if match == nil {
		return agent.Capability{}, agent.NewError(agent.CodeIncompatible, errors.New("unsupported Codex version"))
	}

	helpOutput, _, err := runProbeInPrivateDirectory(ctx, physical, identity, probeRoot, []string{"exec", "--strict-config", "--help"})
	if err != nil {
		return agent.Capability{}, mapProbeError(ctx, err)
	}
	if err := validateExecHelp(string(helpOutput)); err != nil {
		return agent.Capability{}, agent.NewError(agent.CodeIncompatible, err)
	}

	baselineFeatureOutput, _, err := runProbeInPrivateDirectory(ctx, physical, identity, probeRoot, []string{"features", "list"})
	if err != nil {
		return agent.Capability{}, mapProbeError(ctx, err)
	}
	if err := validateFeatureTable(string(baselineFeatureOutput), false); err != nil {
		return agent.Capability{}, agent.NewError(agent.CodeIncompatible, err)
	}
	restrictedFeatureOutput, _, err := runProbeInPrivateDirectory(ctx, physical, identity, probeRoot, restrictedProbeArgs([]string{"features", "list"}))
	if err != nil {
		return agent.Capability{}, mapProbeError(ctx, err)
	}
	if err := validateFeatureTable(string(restrictedFeatureOutput), true); err != nil {
		return agent.Capability{}, agent.NewError(agent.CodeIncompatible, err)
	}

	probeArgs := restrictedProbeArgs([]string{"debug", "prompt-input"})
	probeArgs = append(probeArgs, capabilityProbe)
	probeOutput, _, err := runProbeInPrivateDirectory(ctx, physical, identity, probeRoot, probeArgs)
	if err != nil {
		return agent.Capability{}, mapProbeError(ctx, err)
	}
	if err := validatePromptProbe(probeOutput); err != nil {
		return agent.Capability{}, agent.NewError(agent.CodeIncompatible, err)
	}
	if err := probeStrictConfigRecognition(ctx, physical, identity, probeRoot); err != nil {
		return agent.Capability{}, mapProbeError(ctx, err)
	}
	if err := recheckExecutable(physical, identity); err != nil {
		return agent.Capability{}, agent.NewError(agent.CodeIncompatible, err)
	}

	capability := agent.Capability{
		Provider:           "codex",
		Version:            string(match[1]),
		ProposalOnly:       true,
		NoTools:            false,
		ReadOnly:           true,
		Containment:        agent.ContainmentRestrictedReadOnly,
		StructuredOutput:   true,
		NativeCancellation: true,
		ModelProvenance:    agent.ModelProvenanceUnavailable,
	}
	adapter.mu.Lock()
	adapter.executable = physical
	adapter.executableIdentity = identity
	adapter.capability = capability
	adapter.mu.Unlock()
	succeeded = true
	return capability, nil
}

func probeStrictConfigRecognition(ctx context.Context, executable string, identity *executableIdentity, root *privateRoot) error {
	const unknown = "session_reviewer_unknown_config_canary=true"
	stdout, stderr, err := runProbeInPrivateDirectory(ctx, executable, identity, root, []string{
		"exec", "--strict-config", "--ignore-user-config", "--config", unknown, "-",
	})
	if err == nil {
		return errors.New("Codex strict config accepted an unknown key")
	}
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if len(stdout) != 0 || !bytes.Contains(stderr, []byte("session_reviewer_unknown_config_canary")) ||
		!bytes.Contains(bytes.ToLower(stderr), []byte("unknown")) {
		return errors.New("Codex strict config rejection was not authoritative")
	}
	return nil
}

func resolveExecutable(path string) (string, *executableIdentity, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", nil, errors.New("Codex executable must be absolute")
	}
	physical, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil || !filepath.IsAbs(physical) {
		return "", nil, errors.New("Codex executable cannot be resolved")
	}
	identity, err := inspectExecutable(physical)
	if err != nil {
		return "", nil, errors.New("Codex executable is not an executable regular file")
	}
	return filepath.Clean(physical), identity, nil
}

func recheckExecutable(path string, expected *executableIdentity) error {
	current, err := inspectExecutable(path)
	if err != nil || expected == nil || !os.SameFile(expected.info, current.info) || expected.digest != current.digest {
		return errors.New("verified Codex executable identity changed")
	}
	return nil
}

func inspectExecutable(path string) (*executableIdentity, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || !executableAllowed(path, before) {
		return nil, errors.New("not an executable regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, err
	}
	after, err := file.Stat()
	pathInfo, pathErr := os.Stat(path)
	if err != nil || pathErr != nil || !os.SameFile(before, after) || !os.SameFile(after, pathInfo) ||
		before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
		return nil, errors.New("executable changed while measuring identity")
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return &executableIdentity{info: after, digest: digest}, nil
}

func runProbeInPrivateDirectory(ctx context.Context, executable string, expected *executableIdentity, root *privateRoot, args []string) ([]byte, []byte, error) {
	directory, err := root.createDirectory("probe-")
	if err != nil {
		return nil, nil, err
	}
	stdout, stderr, runErr := runProbe(ctx, executable, expected, directory, args)
	cleanupErr := directory.cleanup()
	return stdout, stderr, errors.Join(runErr, cleanupErr)
}

func runProbe(ctx context.Context, executable string, expected *executableIdentity, directory *privateDirectory, args []string) ([]byte, []byte, error) {
	if err := recheckExecutable(executable, expected); err != nil {
		return nil, nil, err
	}
	var err error
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	stdout := newBoundedBuffer(maxProbeStdout)
	stderr := newBoundedBuffer(maxProbeStderr)
	command := exec.Command(executable, args...)
	if err := directory.configureCommandDirectory(command); err != nil {
		return nil, nil, err
	}
	pipes, err := attachCommandIO(command, nil, stdout, stderr)
	if err != nil {
		return nil, nil, err
	}
	process, err := startManagedProcess(command, directory.recheckForStart)
	if err != nil {
		pipes.abort()
		return nil, nil, err
	}
	pipes.started()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	var stopErr error
	var exitErr error
	select {
	case err = <-wait:
	case <-probeCtx.Done():
		stopErr = terminateManagedProcess(process, processTerminationGrace)
		err, exitErr = waitForProcessExit(wait, processExitWait)
		if probeCtx.Err() != nil {
			err = probeCtx.Err()
		}
	}
	cleanupErr := terminateManagedProcess(process, 0)
	drainErr := pipes.finish(processExitWait)
	releaseErr := releaseManagedProcess(process)
	if stdout.Exceeded() || stderr.Exceeded() {
		return nil, nil, errors.New("Codex capability probe exceeded output bounds")
	}
	if err != nil || stopErr != nil || exitErr != nil || cleanupErr != nil || drainErr != nil || releaseErr != nil {
		return stdout.Bytes(), stderr.Bytes(), errors.Join(err, stopErr, exitErr, cleanupErr, drainErr, releaseErr)
	}
	return stdout.Bytes(), stderr.Bytes(), nil
}

func mapProbeError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return agent.NewError(agent.CodeCancelled, err)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return agent.NewError(agent.CodeTimeout, err)
	}
	return agent.NewError(agent.CodeIncompatible, err)
}

func validateExecHelp(help string) error {
	for _, flag := range requiredFlags {
		pattern := regexp.MustCompile(`(?m)^\s*(?:-[A-Za-z],\s*)?--` + regexp.QuoteMeta(flag) + `(?:\s|=|$)`)
		if !pattern.MatchString(help) {
			return fmt.Errorf("required Codex exec flag --%s is unavailable", flag)
		}
	}
	if !strings.Contains(help, "read-only") {
		return errors.New("Codex read-only sandbox mode is unavailable")
	}
	return nil
}

func validateFeatureTable(output string, restricted bool) error {
	type featureState struct {
		stage   string
		enabled bool
	}
	features := make(map[string]featureState)
	for lineNumber, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			return fmt.Errorf("malformed Codex feature row %d", lineNumber+1)
		}
		state := fields[len(fields)-1]
		if state != "true" && state != "false" {
			return fmt.Errorf("malformed Codex feature state at row %d", lineNumber+1)
		}
		name := fields[0]
		if _, duplicate := features[name]; duplicate {
			return fmt.Errorf("duplicate Codex feature %q", name)
		}
		features[name] = featureState{
			stage:   strings.Join(fields[1:len(fields)-1], " "),
			enabled: state == "true",
		}
	}
	for _, required := range reviewedFeatures {
		observed, exists := features[required.name]
		if !exists {
			return fmt.Errorf("reviewed Codex feature %q is unavailable", required.name)
		}
		if restricted && required.disabled && observed.stage != "removed" && observed.enabled {
			return fmt.Errorf("reviewed Codex feature %q changed effective default", required.name)
		}
	}
	return nil
}

func restrictionArgs(prefix []string) []string {
	args := append([]string(nil), prefix...)
	args = append(args, "--strict-config")
	for _, override := range fixedConfigOverrides {
		args = append(args, "--config", override)
	}
	for _, feature := range reviewedDisabledFeatureNames {
		args = append(args, "--disable", feature)
	}
	return args
}

func restrictedProbeArgs(prefix []string) []string {
	args := append([]string(nil), prefix...)
	for _, override := range fixedConfigOverrides {
		args = append(args, "--config", override)
	}
	for _, feature := range reviewedDisabledFeatureNames {
		args = append(args, "--disable", feature)
	}
	return args
}

func validatePromptProbe(output []byte) error {
	if err := validateJSONNoDuplicates(output); err != nil {
		return errors.New("Codex prompt-input probe is malformed")
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var messages []struct {
		ID                                     string          `json:"id,omitempty"`
		Type                                   string          `json:"type"`
		Role                                   string          `json:"role"`
		InternalChatMessageMetadataPassthrough json.RawMessage `json:"internal_chat_message_metadata_passthrough,omitempty"`
		Content                                []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := decoder.Decode(&messages); err != nil || messages == nil || len(messages) < 1 || len(messages) > 8 {
		return errors.New("Codex prompt-input probe is malformed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Codex prompt-input probe has trailing data")
	}
	for _, message := range messages[:len(messages)-1] {
		if message.Type != "message" || (message.Role != "developer" && message.Role != "user") ||
			len(message.Content) != 1 || message.Content[0].Type != "input_text" ||
			message.Content[0].Text == "" || strings.Contains(message.Content[0].Text, capabilityProbe) {
			return errors.New("Codex prompt-input probe contains invalid inherited context")
		}
	}
	message := messages[len(messages)-1]
	if message.Type != "message" || message.Role != "user" || len(message.Content) != 1 ||
		message.Content[0].Type != "input_text" || message.Content[0].Text != capabilityProbe {
		return errors.New("Codex prompt-input probe contains inherited context")
	}
	return nil
}
