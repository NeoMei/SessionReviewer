package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/agent"
)

var (
	fakeExecutable  string
	proposalFixture string
	schemaFixture   []byte
)

// sessionReviewerTestChildHelperEnv marks a re-executed test binary as a
// probe child. Probe children start with a relocated working directory and a
// filtered test set, so they must not rebuild fixtures relative to the
// relocated cwd before running the filtered tests.
const sessionReviewerTestChildHelperEnv = "SESSIONREVIEWER_TEST_CHILD_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(sessionReviewerTestChildHelperEnv) == "1" {
		os.Exit(m.Run())
	}
	directory, err := os.MkdirTemp("", "session-reviewer-codex-test-")
	if err != nil {
		panic(err)
	}
	fakeExecutable = filepath.Join(directory, "fake-codex")
	if runtime.GOOS == "windows" {
		fakeExecutable += ".exe"
	}
	command := exec.Command("go", "build", "-o", fakeExecutable, "./testdata/fake-agent.go")
	if output, buildErr := command.CombinedOutput(); buildErr != nil {
		panic(fmt.Sprintf("build fake Codex: %v: %s", buildErr, output))
	}
	proposalFixture, err = filepath.Abs("../../../testdata/proposals/valid-first.json")
	if err != nil {
		panic(err)
	}
	schemaFixture, err = os.ReadFile("../../../schemas/proposal-v1.schema.json")
	if err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(directory)
	os.Exit(code)
}

// This catches reintroducing a patch-pinned 0.147-only gate or requiring an
// operator digest for a normal reviewed Codex installation. The restricted
// adapter does not claim that Codex exposes an empty tool registry.
func TestVerifyAcceptsReviewedCodex0150AsRestrictedContainment(t *testing.T) {
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "success")
	t.Setenv("SESSIONREVIEWER_FAKE_VERSION", "codex-cli 0.150.1")
	capability, err := New().Verify(context.Background(), fakeExecutable)
	if err != nil {
		t.Fatalf("reviewed Codex 0.150 rejected: %v", err)
	}
	want := agent.Capability{
		Provider: "codex", Version: "0.150.1", ProposalOnly: true, NoTools: false,
		ReadOnly: true, Containment: agent.ContainmentRestrictedReadOnly,
		StructuredOutput: true, NativeCancellation: true,
		ModelProvenance: agent.ModelProvenanceUnavailable,
	}
	if !reflect.DeepEqual(capability, want) {
		t.Fatalf("capability=%+v want=%+v", capability, want)
	}
}

func TestVerify0150RunsEveryRestrictedContainmentProbe(t *testing.T) {
	callsPath := filepath.Join(t.TempDir(), "calls.jsonl")
	t.Setenv("SESSIONREVIEWER_FAKE_CALLS_PATH", callsPath)
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "success")
	t.Setenv("SESSIONREVIEWER_FAKE_VERSION", "codex-cli 0.150.1")
	adapter := New()
	capability, err := adapter.Verify(context.Background(), fakeExecutable)
	if err != nil || capability.Containment != agent.ContainmentRestrictedReadOnly || capability.NoTools {
		t.Fatalf("0.150.1 verification capability=%+v err=%v", capability, err)
	}
	calls := readCalls(t, callsPath)
	if len(calls) != 6 || !reflect.DeepEqual(calls[0], []string{"--strict-config", "--version"}) ||
		!reflect.DeepEqual(calls[1], []string{"exec", "--strict-config", "--help"}) ||
		!reflect.DeepEqual(calls[2], []string{"features", "list"}) ||
		len(calls[3]) < 7 || !reflect.DeepEqual(calls[3][:2], []string{"features", "list"}) ||
		!containsAdjacent(calls[3], "--disable", "view_image") ||
		!containsAdjacent(calls[3], "--disable", "multi_agent") ||
		len(calls[4]) < 3 || !reflect.DeepEqual(calls[4][:2], []string{"debug", "prompt-input"}) ||
		calls[4][len(calls[4])-1] != capabilityProbe ||
		!containsAdjacent(calls[4], "--config", `web_search="disabled"`) ||
		!containsAdjacent(calls[4], "--config", "project_doc_max_bytes=0") ||
		!containsAdjacent(calls[4], "--config", "project_root_markers=[]") ||
		!containsAdjacent(calls[4], "--config", "include_environment_context=false") ||
		!containsAdjacent(calls[4], "--config", "mcp_servers={}") ||
		!reflect.DeepEqual(calls[5], []string{"exec", "--strict-config", "--ignore-user-config", "--config", "session_reviewer_unknown_config_canary=true", "-"}) {
		t.Fatalf("verification calls=%v", calls)
	}
	for _, index := range []int{2, 3, 4} {
		if containsValue(calls[index], "--strict-config") {
			t.Fatalf("0.150 subcommand probe %d used unsupported --strict-config: %v", index, calls[index])
		}
	}
}

func TestCapabilityCannotBeMislabeledNoToolsOrConstructedFor0147(t *testing.T) {
	t.Setenv("SESSIONREVIEWER_FAKE_VERSION", "codex-cli 0.147.0")
	capability, err := New().Verify(context.Background(), fakeExecutable)
	assertCode(t, err, agent.CodeIncompatible)
	if capability.NoTools || capability.Containment != "" || !reflect.DeepEqual(capability, agent.Capability{}) {
		t.Fatalf("0.147.x capability was constructible or mislabeled NoTools: %+v", capability)
	}
}

func TestVerifyUsesAFreshEmptyPrivateDirectoryForEveryProbe(t *testing.T) {
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "verify-writes-cwd")
	t.Setenv("SESSIONREVIEWER_FAKE_VERSION", "codex-cli 0.150.1")
	capability, err := New().Verify(context.Background(), fakeExecutable)
	if err != nil || capability.Containment != agent.ContainmentRestrictedReadOnly {
		t.Fatalf("capability=%+v err=%v", capability, err)
	}
}

func TestVerifyRejectsUnconfiguredAndIncompatibleExecutables(t *testing.T) {
	nonExecutable := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(nonExecutable, []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		path    string
		mode    string
		version string
		code    agent.ErrorCode
	}{
		{name: "relative path", path: filepath.Base(fakeExecutable), code: agent.CodeUnconfigured},
		{name: "directory", path: t.TempDir(), code: agent.CodeUnconfigured},
		{name: "non executable", path: nonExecutable, code: agent.CodeUnconfigured},
		{name: "older version", path: fakeExecutable, version: "codex-cli 0.146.9", code: agent.CodeIncompatible},
		{name: "newer version", path: fakeExecutable, version: "codex-cli 0.151.0", code: agent.CodeIncompatible},
		{name: "prerelease", path: fakeExecutable, version: "codex-cli 0.150.1-alpha.1", code: agent.CodeIncompatible},
		{name: "noncanonical patch version", path: fakeExecutable, version: "codex-cli 0.150.01", code: agent.CodeIncompatible},
		{name: "missing flag", path: fakeExecutable, mode: "verify-missing-flag", code: agent.CodeIncompatible},
		{name: "missing stable feature", path: fakeExecutable, mode: "verify-missing-feature", code: agent.CodeIncompatible},
		{name: "deny feature remained enabled", path: fakeExecutable, mode: "verify-enabled-feature", code: agent.CodeIncompatible},
		{name: "malformed feature table", path: fakeExecutable, mode: "verify-malformed-features", code: agent.CodeIncompatible},
		{name: "noncanonical feature state", path: fakeExecutable, mode: "verify-noncanonical-feature-state", code: agent.CodeIncompatible},
		{name: "malformed prompt probe", path: fakeExecutable, mode: "verify-malformed-probe", code: agent.CodeIncompatible},
		{name: "probe marker outside user prompt", path: fakeExecutable, mode: "verify-marker-outside-user", code: agent.CodeIncompatible},
		{name: "environment context leaked", path: fakeExecutable, mode: "verify-environment-context", code: agent.CodeIncompatible},
		{name: "strict config ignored", path: fakeExecutable, mode: "verify-strict-config-ignored", code: agent.CodeIncompatible},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && test.name == "non executable" {
				t.Skip("Windows executable permission is extension-based")
			}
			t.Setenv("SESSIONREVIEWER_FAKE_MODE", test.mode)
			t.Setenv("SESSIONREVIEWER_FAKE_VERSION", test.version)
			_, err := New().Verify(context.Background(), test.path)
			assertCode(t, err, test.code)
		})
	}
}

func containsAdjacent(values []string, first, second string) bool {
	for index := 0; index+1 < len(values); index++ {
		if values[index] == first && values[index+1] == second {
			return true
		}
	}
	return false
}

func containsValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestVerifyBoundsProbeOutputAndCancelsTheProbeTree(t *testing.T) {
	for _, mode := range []string{"verify-huge-stdout", "verify-huge-stderr"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("SESSIONREVIEWER_FAKE_MODE", mode)
			capability, err := New().Verify(context.Background(), fakeExecutable)
			if !reflect.DeepEqual(capability, agent.Capability{}) {
				t.Fatalf("failed probe returned capability: %+v", capability)
			}
			assertCode(t, err, agent.CodeIncompatible)
			if strings.Contains(fmt.Sprint(err), "probe-stderr-canary") {
				t.Fatalf("public verification error leaked probe stderr: %v", err)
			}
		})
	}

	t.Run("deadline kills ignored process tree", func(t *testing.T) {
		pidPath := filepath.Join(t.TempDir(), "child.pid")
		t.Setenv("SESSIONREVIEWER_FAKE_MODE", "verify-ignored-term-child")
		t.Setenv("SESSIONREVIEWER_FAKE_CHILD_PID_PATH", pidPath)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		capability, err := New().Verify(ctx, fakeExecutable)
		if !reflect.DeepEqual(capability, agent.Capability{}) {
			t.Fatalf("timed-out probe returned capability: %+v", capability)
		}
		assertCode(t, err, agent.CodeTimeout)
		assertRecordedProcessStops(t, pidPath)
	})
}

func TestVerifyReturnsBeforeOrphanInheritedPipesClose(t *testing.T) {
	for _, test := range []struct {
		mode string
		code agent.ErrorCode
	}{
		{mode: "verify-success-with-child"},
		{mode: "verify-error-with-child", code: agent.CodeIncompatible},
	} {
		t.Run(test.mode, func(t *testing.T) {
			pidPath := filepath.Join(t.TempDir(), "child.pid")
			t.Setenv("SESSIONREVIEWER_FAKE_MODE", test.mode)
			t.Setenv("SESSIONREVIEWER_FAKE_CHILD_PID_PATH", pidPath)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			started := time.Now()
			capability, err := New().Verify(ctx, fakeExecutable)
			if test.code == "" {
				if err != nil || capability.Containment != agent.ContainmentRestrictedReadOnly {
					t.Fatalf("successful orphan probe capability=%+v err=%v", capability, err)
				}
			} else {
				if !reflect.DeepEqual(capability, agent.Capability{}) {
					t.Fatalf("failed orphan probe returned capability: %+v", capability)
				}
				assertCode(t, err, test.code)
			}
			if elapsed := time.Since(started); elapsed >= time.Second {
				t.Fatalf("probe waited %s for inherited pipe EOF", elapsed)
			}
			assertRecordedProcessStops(t, pidPath)
		})
	}
}

func TestVerifyRejectsConcurrentVerificationAsBusy(t *testing.T) {
	adapter := New()
	readyPath := filepath.Join(t.TempDir(), "verify.ready")
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "verify-slow")
	t.Setenv("SESSIONREVIEWER_FAKE_READY_PATH", readyPath)
	first := make(chan error, 1)
	go func() {
		_, err := adapter.Verify(context.Background(), fakeExecutable)
		first <- err
	}()
	waitForFile(t, readyPath)
	capability, err := adapter.Verify(context.Background(), fakeExecutable)
	if !reflect.DeepEqual(capability, agent.Capability{}) {
		t.Fatalf("busy verification returned capability: %+v", capability)
	}
	assertCode(t, err, agent.CodeBusy)
	if err := <-first; err != nil {
		t.Fatalf("first verification failed: %v", err)
	}
}

func TestGenerateProposalUsesTheFixedRestrictedReadOnlyInvocationAndStdinPrompt(t *testing.T) {
	adapter := containedRunnerForTest(t)
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("SESSIONREVIEWER_FAKE_CAPTURE_PATH", capturePath)
	request := validRequest(t, []byte("PROMPT_ONLY_ON_STDIN"))
	result, err := adapter.GenerateProposal(context.Background(), request)
	if err != nil {
		t.Fatalf("%v (cause: %v)", err, errors.Unwrap(err))
	}
	if !bytes.Equal(result.Proposal, mustRead(t, proposalFixture)) {
		t.Fatal("proposal bytes changed")
	}
	if result.Model != "" {
		t.Fatalf("reviewed Codex result invented model %q", result.Model)
	}
	wantUsage := accounting.TokenUsage{
		InputTokens: 101, CachedInputTokens: 11, CacheWriteInputTokens: 7,
		OutputTokens: 23, ReasoningOutputTokens: 3, TotalTokens: 124,
	}
	if result.Usage != wantUsage {
		t.Fatalf("usage=%+v want=%+v", result.Usage, wantUsage)
	}

	var capture struct {
		Args       []string `json:"args"`
		Stdin      string   `json:"stdin"`
		CWD        string   `json:"cwd"`
		SchemaPath string   `json:"schema_path"`
		Schema     string   `json:"schema"`
		SchemaMode uint32   `json:"schema_mode"`
		CWDMode    uint32   `json:"cwd_mode"`
	}
	if err := json.Unmarshal(mustRead(t, capturePath), &capture); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{
		"exec", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"--sandbox", "read-only", "--json", "--color", "never",
		"--skip-git-repo-check", "--output-schema", "proposal-schema.json",
		"--strict-config",
		"--config", `web_search="disabled"`,
		"--config", "tools.update_plan.enabled=false",
		"--config", "tools.experimental_request_user_input.enabled=false",
		"--config", "project_doc_max_bytes=0",
		"--config", "project_root_markers=[]",
		"--config", "project_doc_fallback_filenames=[]",
		"--config", "include_environment_context=false",
		"--config", "include_permissions_instructions=false",
		"--config", "include_apps_instructions=false",
		"--config", "include_collaboration_mode_instructions=false",
		"--config", "skills.include_instructions=false",
		"--config", "orchestrator.skills.enabled=false",
		"--config", "orchestrator.mcp.enabled=false",
		"--config", `developer_instructions=""`,
		"--config", `instructions=""`,
		"--config", "mcp_servers={}",
		"--disable", "shell_tool", "--disable", "apps",
		"--disable", "view_image", "--disable", "shell_zsh_fork",
		"--disable", "unified_exec_zsh_fork",
		"--disable", "shell_snapshot", "--disable", "deferred_executor",
		"--disable", "code_mode", "--disable", "code_mode_buffered_exec",
		"--disable", "code_mode_only", "--disable", "code_mode_interrupt",
		"--disable", "standalone_web_search",
		"--disable", "memories", "--disable", "external_agent_memory_import",
		"--disable", "local_thread_store_compression", "--disable", "chronicle",
		"--disable", "exec_permission_approvals", "--disable", "hooks",
		"--disable", "request_permissions_tool", "--disable", "network_proxy",
		"--disable", "respect_system_proxy",
		"--disable", "multi_agent", "--disable", "multi_agent_v2",
		"--disable", "enable_mcp_apps", "--disable", "mcp_2026_07_28",
		"--disable", "deferred_tool_world_state", "--disable", "non_prefixed_mcp_tool_names",
		"--disable", "tool_suggest",
		"--disable", "recommended_plugins", "--disable", "plugins",
		"--disable", "executor_capability_discovery", "--disable", "in_app_browser",
		"--disable", "in_app_updates",
		"--disable", "browser_use", "--disable", "in_app_chat",
		"--disable", "in_app_dictation", "--disable", "in_app_local_automation",
		"--disable", "browser_use_external",
		"--disable", "browser_use_full_cdp_access", "--disable", "computer_use",
		"--disable", "image_generation", "--disable", "workspace_dependencies",
		"--disable", "skill_mcp_dependency_install", "--disable", "skill_search",
		"--disable", "remote_plugin", "--disable", "plugin_sharing",
		"--disable", "default_mode_request_user_input", "--disable", "guardian_approval",
		"--disable", "guardianv2", "--disable", "guardian_enhanced_node_repl_transcripts",
		"--disable", "guardian_ext", "--disable", "guardian_node_repl_transcript_images",
		"--disable", "guardian_reuse_parent_compaction", "--disable", "goals",
		"--disable", "token_budget", "--disable", "rollout_budget",
		"--disable", "current_time_reminder",
		"--disable", "tool_call_mcp_elicitation", "--disable", "auth_elicitation",
		"--disable", "artifact", "--disable", "realtime_conversation",
		"--disable", "prevent_idle_sleep", "--disable", "remote_compaction_v2",
		"--disable", "use_agent_identity", "--disable", "apply_patch_preserve_line_endings",
		"--disable", "background_paginated_rollout_migration", "--disable", "content_item_kinds",
		"--disable", "cwd_relative_turn_diffs", "--disable", "psp",
		"--disable", "retain_client_developer_messages", "--disable", "send_async_message",
		"--disable", "shell_snapshot_v2", "--disable", "transcript_v2",
		"--disable", "unified_image_budget", "--disable", "personality", "-",
	}
	if !reflect.DeepEqual(capture.Args, wantArgs) {
		t.Fatalf("args=%q want=%q", capture.Args, wantArgs)
	}
	if capture.Stdin != "PROMPT_ONLY_ON_STDIN" || strings.Contains(strings.Join(capture.Args, "\x00"), capture.Stdin) {
		t.Fatalf("prompt placement args=%q stdin=%q", capture.Args, capture.Stdin)
	}
	for _, root := range request.ForbiddenRoots {
		if strings.Contains(strings.Join(capture.Args, "\x00"), root.CanonicalPath) ||
			strings.Contains(capture.Stdin, root.CanonicalPath) {
			t.Fatalf("adapter-private %s root leaked into Agent input", root.Kind)
		}
	}
	if capture.Schema != string(schemaFixture) || (runtime.GOOS != "windows" && capture.SchemaMode != 0o600) || capture.SchemaPath != "proposal-schema.json" {
		t.Fatalf("schema capture path=%q mode=%#o bytes=%d", capture.SchemaPath, capture.SchemaMode, len(capture.Schema))
	}
	if runtime.GOOS != "windows" && capture.CWDMode != 0o700 {
		t.Fatalf("private run directory mode=%#o want=0700", capture.CWDMode)
	}
	physicalRoot, resolveErr := filepath.EvalSymlinks(request.WorkingDirectory)
	relativeCWD, relativeErr := filepath.Rel(physicalRoot, capture.CWD)
	if resolveErr != nil || relativeErr != nil || relativeCWD == "." || relativeCWD == ".." || strings.HasPrefix(relativeCWD, ".."+string(os.PathSeparator)) {
		t.Fatalf("Codex cwd=%q escaped pinned private root=%q resolveErr=%v relativeErr=%v", capture.CWD, physicalRoot, resolveErr, relativeErr)
	}
	if info, statErr := os.Stat(capture.CWD); !os.IsNotExist(statErr) || info != nil {
		t.Fatalf("private run directory was not removed: info=%v err=%v", info, statErr)
	}
	if entries, readErr := os.ReadDir(request.WorkingDirectory); readErr != nil || len(entries) != 0 {
		t.Fatalf("private working root not empty after run: entries=%v err=%v", entries, readErr)
	}
}

func TestGenerateProposalAvoidsCodexDiagnosticItemsFromRedundantDisableFlags(t *testing.T) {
	adapter := containedRunnerForTest(t)
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "noisy-disable-diagnostics")
	result, err := adapter.GenerateProposal(context.Background(), validRequest(t, []byte("prompt")))
	if err != nil {
		t.Fatalf("fixed restricted invocation triggered non-proposal diagnostics: %v (cause: %v)", err, errors.Unwrap(err))
	}
	if !bytes.Equal(result.Proposal, mustRead(t, proposalFixture)) {
		t.Fatal("proposal bytes changed")
	}
}

func TestGenerateProposalRejectsNonEmptyWorkingRootWithoutStartingCodex(t *testing.T) {
	adapter := containedRunnerForTest(t)
	request := validRequest(t, []byte("prompt"))
	if err := os.WriteFile(filepath.Join(request.WorkingDirectory, "foreign"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(t.TempDir(), "must-not-run.json")
	t.Setenv("SESSIONREVIEWER_FAKE_CAPTURE_PATH", capturePath)
	result, err := adapter.GenerateProposal(context.Background(), request)
	if !reflect.DeepEqual(result, agent.Result{}) {
		t.Fatalf("unsafe root returned result: %+v", result)
	}
	assertCode(t, err, agent.CodeUnconfigured)
	if got := fmt.Sprint(err); got != string(agent.CodeUnconfigured) {
		t.Fatalf("unsafe root leaked private detail: %q", got)
	}
	if _, statErr := os.Stat(capturePath); !os.IsNotExist(statErr) {
		t.Fatalf("Codex started for unsafe root: %v", statErr)
	}
}

func TestGenerateProposalRequiresCanonicalDisjointProjectAndVaultRoots(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *agent.Request)
	}{
		{name: "missing roots", mutate: func(_ *testing.T, request *agent.Request) {
			request.ForbiddenRoots = nil
		}},
		{name: "missing vault", mutate: func(_ *testing.T, request *agent.Request) {
			request.ForbiddenRoots = request.ForbiddenRoots[:1]
		}},
		{name: "duplicate project", mutate: func(_ *testing.T, request *agent.Request) {
			request.ForbiddenRoots[1].Kind = agent.ForbiddenRootProject
		}},
		{name: "working root nested in project", mutate: func(t *testing.T, request *agent.Request) {
			project := canonicalDirectory(t, t.TempDir())
			working := filepath.Join(project, "private-work")
			if err := os.Mkdir(working, 0o700); err != nil {
				t.Fatal(err)
			}
			request.WorkingDirectory = working
			request.ForbiddenRoots[0].CanonicalPath = project
		}},
		{name: "working root is ancestor of project", mutate: func(t *testing.T, request *agent.Request) {
			working := canonicalDirectory(t, t.TempDir())
			project := filepath.Join(working, "project")
			if err := os.Mkdir(project, 0o700); err != nil {
				t.Fatal(err)
			}
			request.WorkingDirectory = working
			request.ForbiddenRoots[0].CanonicalPath = project
		}},
		{name: "symlink project is not canonical", mutate: func(t *testing.T, request *agent.Request) {
			if runtime.GOOS == "windows" {
				t.Skip("symlink creation requires privileges on some Windows hosts")
			}
			target := canonicalDirectory(t, t.TempDir())
			link := filepath.Join(t.TempDir(), "project-link")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			request.ForbiddenRoots[0].CanonicalPath = link
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := containedRunnerForTest(t)
			request := validRequest(t, []byte("prompt"))
			test.mutate(t, &request)
			capturePath := filepath.Join(t.TempDir(), "must-not-run.json")
			t.Setenv("SESSIONREVIEWER_FAKE_CAPTURE_PATH", capturePath)
			result, err := adapter.GenerateProposal(context.Background(), request)
			if !reflect.DeepEqual(result, agent.Result{}) {
				t.Fatalf("unsafe root returned result: %+v", result)
			}
			assertCode(t, err, agent.CodeUnconfigured)
			if _, statErr := os.Stat(capturePath); !os.IsNotExist(statErr) {
				t.Fatalf("Codex started for unsafe roots: %v", statErr)
			}
		})
	}
}

func TestForbiddenRootIdentityReplacementFailsClosed(t *testing.T) {
	request := validRequest(t, []byte("prompt"))
	working, err := openPrivateRoot(request.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer working.close()
	roots, err := openForbiddenRoots(request.ForbiddenRoots)
	if err != nil {
		t.Fatal(err)
	}
	defer roots.close()
	if err := roots.recheckDisjoint(working); err != nil {
		t.Fatalf("initial roots rejected: %v", err)
	}
	project := request.ForbiddenRoots[0].CanonicalPath
	moved := project + "-moved"
	if err := os.Rename(project, moved); err != nil {
		if windowsDeniedNamespaceReplacement(err) {
			return
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := roots.recheckDisjoint(working); !errors.Is(err, errForbiddenRootIdentityChanged) {
		t.Fatalf("replacement root err=%v want identity failure", err)
	}
}

func TestGenerateProposalNeverInventsModelFromStderr(t *testing.T) {
	adapter := containedRunnerForTest(t)
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "success-stderr-model")
	result, err := adapter.GenerateProposal(context.Background(), validRequest(t, []byte("prompt")))
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "" {
		t.Fatalf("reviewed Codex stderr invented model provenance %q", result.Model)
	}
	if result.Usage.TotalTokens != 124 {
		t.Fatalf("authoritative usage was lost with unknown model: %+v", result.Usage)
	}
}

func TestGenerateProposalRejectsStrictOutputMatrixWithoutLeakingPrivateErrors(t *testing.T) {
	tests := []struct {
		mode string
		code agent.ErrorCode
	}{
		{mode: "malformed-jsonl", code: agent.CodeIncompatible},
		{mode: "missing-final", code: agent.CodeIncompatible},
		{mode: "schema-invalid", code: agent.CodeIncompatible},
		{mode: "auth-error", code: agent.CodeAuth},
		{mode: "huge-stdout", code: agent.CodeIncompatible},
		{mode: "huge-stderr", code: agent.CodeIncompatible},
		{mode: "exit-after-valid-output", code: agent.CodeIncompatible},
		{mode: "invalid-usage", code: agent.CodeIncompatible},
		{mode: "missing-usage", code: agent.CodeIncompatible},
		{mode: "duplicate-final", code: agent.CodeIncompatible},
		{mode: "event-after-complete", code: agent.CodeIncompatible},
		{mode: "model-spoof", code: agent.CodeIncompatible},
		{mode: "duplicate-json-key", code: agent.CodeIncompatible},
		{mode: "unknown-field", code: agent.CodeIncompatible},
		{mode: "unknown-event", code: agent.CodeIncompatible},
		{mode: "trailing-no-newline", code: agent.CodeIncompatible},
		{mode: "turn-failed-before-start", code: agent.CodeIncompatible},
		{mode: "invalid-utf8", code: agent.CodeIncompatible},
		{mode: "deep-embedded-proposal", code: agent.CodeIncompatible},
	}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			adapter := containedRunnerForTest(t)
			t.Setenv("SESSIONREVIEWER_FAKE_MODE", test.mode)
			result, err := adapter.GenerateProposal(context.Background(), validRequest(t, []byte("prompt")))
			if !reflect.DeepEqual(result, agent.Result{}) {
				t.Fatalf("error returned nonzero result: %+v", result)
			}
			assertCode(t, err, test.code)
			text := fmt.Sprintf("%v", err)
			for _, canary := range []string{"stderr-canary", "/private/job", "Unauthorized"} {
				if strings.Contains(text, canary) {
					t.Fatalf("public error leaked %q: %q", canary, text)
				}
			}
		})
	}
}

func TestGenerateProposalRejectsRawInvalidUTF8BeforeJSONDecoding(t *testing.T) {
	adapter := containedRunnerForTest(t)
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "invalid-utf8")
	result, err := adapter.GenerateProposal(context.Background(), validRequest(t, []byte("prompt")))
	if !reflect.DeepEqual(result, agent.Result{}) {
		t.Fatalf("invalid UTF-8 returned result: %+v", result)
	}
	assertCode(t, err, agent.CodeIncompatible)
	if got := fmt.Sprint(err); got != string(agent.CodeIncompatible) {
		t.Fatalf("invalid UTF-8 leaked a private decoder error: %q", got)
	}
}

func TestGenerateProposalAppliesDepthBoundToEmbeddedProposal(t *testing.T) {
	adapter := containedRunnerForTest(t)
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "deep-embedded-proposal")
	result, err := adapter.GenerateProposal(context.Background(), validRequest(t, []byte("prompt")))
	if !reflect.DeepEqual(result, agent.Result{}) {
		t.Fatalf("deep embedded proposal returned result: %+v", result)
	}
	assertCode(t, err, agent.CodeIncompatible)
	if !errors.Is(err, errJSONDepthExceeded) {
		t.Fatalf("deep proposal bypassed the shared depth guard: cause=%v", errors.Unwrap(err))
	}
	if got := fmt.Sprint(err); got != string(agent.CodeIncompatible) {
		t.Fatalf("deep proposal leaked a private decoder error: %q", got)
	}
}

func TestParseJSONLRetainsStructuredAuthFailureForSafeMapping(t *testing.T) {
	output := []byte("{\"thread_id\":\"thread-1\",\"type\":\"thread.started\"}\n" +
		"{\"type\":\"turn.started\"}\n" +
		"{\"error\":{\"message\":\"401 Unauthorized\"},\"type\":\"turn.failed\"}\n")
	lines := bytes.Split(bytes.TrimSuffix(output, []byte{'\n'}), []byte{'\n'})
	if err := validateJSONNoDuplicates(lines[0]); err != nil {
		t.Fatalf("thread duplicate validation: %v", err)
	}
	if err := validateJSONNoDuplicates(lines[2]); err != nil {
		t.Fatalf("failure duplicate validation: %v", err)
	}
	var thread struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
	}
	if err := decodeStrict(lines[0], &thread); err != nil || thread.ThreadID == "" {
		t.Fatalf("thread=%+v err=%v", thread, err)
	}
	if message, err := parseFailureEvent(lines[2], "turn.failed"); err != nil || message != "401 Unauthorized" {
		t.Fatalf("failure event message=%q err=%v", message, err)
	}
	parsed, err := parseJSONL(output)
	if err != nil || parsed.failure != "401 Unauthorized" {
		t.Fatalf("parsed=%+v err=%v", parsed, err)
	}
}

func TestGenerateProposalRejectsEveryNormalizedToolCall(t *testing.T) {
	for _, mode := range []string{"tool-call", "normalized-tool-request"} {
		t.Run(mode, func(t *testing.T) {
			adapter := containedRunnerForTest(t)
			t.Setenv("SESSIONREVIEWER_FAKE_MODE", mode)
			result, err := adapter.GenerateProposal(context.Background(), validRequest(t, []byte("prompt")))
			if !reflect.DeepEqual(result, agent.Result{}) {
				t.Fatalf("forbidden tool returned result: %+v", result)
			}
			assertCode(t, err, agent.CodeToolForbidden)
		})
	}
}

func TestToolKindNormalizationRejectsSeparatorAndCaseVariants(t *testing.T) {
	for _, kind := range []string{
		"tool.call",
		"ToolCall",
		"McpToolRequest",
		"MCP-TOOL-CALL",
		"customToolCall",
		"collab/tool_request",
		"FunctionCall",
		"custom.function.call",
		"FunctionRequest",
		"CommandExecution",
		"FileChange",
		"WebSearch",
		"TodoList",
		"ComputerUse",
	} {
		t.Run(kind, func(t *testing.T) {
			if !isToolKind(kind) {
				t.Fatalf("normalized tool kind %q was not recognized", kind)
			}
		})
	}
}

func TestGenerateProposalTimesOutAndKillsIgnoredProcessTree(t *testing.T) {
	adapter := containedRunnerForTest(t)
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "ignored-term-child")
	t.Setenv("SESSIONREVIEWER_FAKE_CHILD_PID_PATH", pidPath)
	request := validRequest(t, []byte("prompt"))
	// The deadline must stay comfortably above scheduler jitter on loaded
	// machines: the fake agent records the ignored child's PID as its first
	// action, and a kill that wins that race leaves no PID to assert on.
	request.Deadline = time.Now().Add(200 * time.Millisecond)
	result, err := adapter.GenerateProposal(context.Background(), request)
	if !reflect.DeepEqual(result, agent.Result{}) {
		t.Fatalf("timeout returned result: %+v", result)
	}
	assertCode(t, err, agent.CodeTimeout)
	assertRecordedProcessStops(t, pidPath)
}

func TestCancelIsIdempotentAndKillsIgnoredProcessTree(t *testing.T) {
	adapter := containedRunnerForTest(t)
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "ignored-term-child")
	t.Setenv("SESSIONREVIEWER_FAKE_CHILD_PID_PATH", pidPath)
	resultChannel := make(chan struct {
		result agent.Result
		err    error
	}, 1)
	request := validRequest(t, []byte("prompt"))
	go func() {
		result, err := adapter.GenerateProposal(context.Background(), request)
		resultChannel <- struct {
			result agent.Result
			err    error
		}{result: result, err: err}
	}()
	waitForFile(t, pidPath)
	if err := adapter.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := <-resultChannel
	if !reflect.DeepEqual(got.result, agent.Result{}) {
		t.Fatalf("cancel returned result: %+v", got.result)
	}
	assertCode(t, got.err, agent.CodeCancelled)
	assertRecordedProcessStops(t, pidPath)
}

func TestContextCancellationKillsIgnoredProcessTree(t *testing.T) {
	adapter := containedRunnerForTest(t)
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "ignored-term-child")
	t.Setenv("SESSIONREVIEWER_FAKE_CHILD_PID_PATH", pidPath)
	ctx, cancel := context.WithCancel(context.Background())
	resultChannel := make(chan struct {
		result agent.Result
		err    error
	}, 1)
	request := validRequest(t, []byte("prompt"))
	go func() {
		result, err := adapter.GenerateProposal(ctx, request)
		resultChannel <- struct {
			result agent.Result
			err    error
		}{result: result, err: err}
	}()
	waitForFile(t, pidPath)
	cancel()
	got := <-resultChannel
	if !reflect.DeepEqual(got.result, agent.Result{}) {
		t.Fatalf("context cancellation returned result: %+v", got.result)
	}
	assertCode(t, got.err, agent.CodeCancelled)
	assertRecordedProcessStops(t, pidPath)
}

func TestGenerateProposalRejectsConcurrentRunAsBusy(t *testing.T) {
	adapter := containedRunnerForTest(t)
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "ignored-term-child")
	t.Setenv("SESSIONREVIEWER_FAKE_CHILD_PID_PATH", pidPath)
	first := make(chan error, 1)
	firstRequest := validRequest(t, []byte("first"))
	go func() {
		_, err := adapter.GenerateProposal(context.Background(), firstRequest)
		first <- err
	}()
	waitForFile(t, pidPath)
	result, err := adapter.GenerateProposal(context.Background(), validRequest(t, []byte("second")))
	if !reflect.DeepEqual(result, agent.Result{}) {
		t.Fatalf("busy call returned result: %+v", result)
	}
	assertCode(t, err, agent.CodeBusy)
	if err := adapter.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCode(t, <-first, agent.CodeCancelled)
	assertRecordedProcessStops(t, pidPath)
}

func TestGenerateProposalCleansOrphanChildAfterEveryExit(t *testing.T) {
	for _, mode := range []string{"success-with-child", "exit-after-valid-output"} {
		t.Run(mode, func(t *testing.T) {
			adapter := containedRunnerForTest(t)
			pidPath := filepath.Join(t.TempDir(), "child.pid")
			t.Setenv("SESSIONREVIEWER_FAKE_MODE", mode)
			t.Setenv("SESSIONREVIEWER_FAKE_CHILD_PID_PATH", pidPath)
			_, _ = adapter.GenerateProposal(context.Background(), validRequest(t, []byte("prompt")))
			assertRecordedProcessStops(t, pidPath)
		})
	}
}

func TestGenerateProposalReturnsBeforeOrphanInheritedPipesClose(t *testing.T) {
	for _, test := range []struct {
		mode string
		code agent.ErrorCode
	}{
		{mode: "success-with-child"},
		{mode: "exit-after-valid-output", code: agent.CodeIncompatible},
	} {
		t.Run(test.mode, func(t *testing.T) {
			adapter := containedRunnerForTest(t)
			pidPath := filepath.Join(t.TempDir(), "child.pid")
			t.Setenv("SESSIONREVIEWER_FAKE_MODE", test.mode)
			t.Setenv("SESSIONREVIEWER_FAKE_CHILD_PID_PATH", pidPath)
			request := validRequest(t, []byte("prompt"))
			request.Deadline = time.Now().Add(5 * time.Second)
			result, err := adapter.GenerateProposal(context.Background(), request)
			if test.code == "" {
				if err != nil || len(result.Proposal) == 0 {
					t.Fatalf("result=%+v err=%v cause=%v", result, err, errors.Unwrap(err))
				}
			} else {
				if !reflect.DeepEqual(result, agent.Result{}) {
					t.Fatalf("failed orphan run returned result: %+v", result)
				}
				assertCode(t, err, test.code)
			}
			assertRecordedProcessStops(t, pidPath)
		})
	}
}

func TestGenerateProposalRechecksVerifiedPhysicalIdentity(t *testing.T) {
	directory := t.TempDir()
	verifiedPath := testExecutablePath(directory, "codex")
	copyFile(t, fakeExecutable, verifiedPath, 0o700)
	physical, identity, err := resolveExecutable(verifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	// Same-package test-only construction isolates executable replacement checks
	// from the separate production verification contract.
	adapter := &Adapter{executable: physical, executableIdentity: identity}
	replacement := testExecutablePath(directory, "replacement")
	copyFile(t, fakeExecutable, replacement, 0o700)
	if err := os.Rename(replacement, verifiedPath); err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(directory, "must-not-run.json")
	t.Setenv("SESSIONREVIEWER_FAKE_CAPTURE_PATH", capturePath)
	result, err := adapter.GenerateProposal(context.Background(), validRequest(t, []byte("prompt")))
	if !reflect.DeepEqual(result, agent.Result{}) {
		t.Fatalf("identity mismatch returned result: %+v", result)
	}
	assertCode(t, err, agent.CodeIncompatible)
	if _, statErr := os.Stat(capturePath); !os.IsNotExist(statErr) {
		t.Fatalf("replacement executable ran: %v", statErr)
	}
}

func TestExecutableIdentityDetectsInPlaceContentDrift(t *testing.T) {
	directory := t.TempDir()
	path := testExecutablePath(directory, "codex")
	copyFile(t, fakeExecutable, path, 0o700)
	physical, identity, err := resolveExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{0}, 0); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recheckExecutable(physical, identity); err == nil {
		t.Fatal("same-inode executable content drift retained verification trust")
	}
}

func TestNativeTerminationRefusesMismatchedStartToken(t *testing.T) {
	command := exec.Command(fakeExecutable, "fake-child")
	process, err := startManagedProcess(command)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = terminateManagedProcess(process, 10*time.Millisecond)
		_ = command.Wait()
		_ = releaseManagedProcess(process)
	}()
	if err := terminateManagedProcessWithToken(process, process.startToken+"-stale", 10*time.Millisecond); !errors.Is(err, errProcessIdentityChanged) {
		t.Fatalf("wrong start token err=%v want errProcessIdentityChanged", err)
	}
	if !processAlive(command.Process.Pid) {
		t.Fatal("mismatched start token targeted the process")
	}
}

func TestActiveRunSignalsStopBeforeWaitingForProcessReadiness(t *testing.T) {
	run := newActiveRun()
	stopped := make(chan error, 1)
	go func() { stopped <- run.stop() }()
	select {
	case <-run.stopSignal:
	case <-time.After(time.Second):
		t.Fatal("stop request was not signaled before process readiness")
	}
	run.setProcess(nil)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}

func TestWaitForProcessExitIsBounded(t *testing.T) {
	wait := make(chan error)
	started := time.Now()
	_, err := waitForProcessExit(wait, 10*time.Millisecond)
	if !errors.Is(err, errProcessExitTimeout) {
		t.Fatalf("bounded wait err=%v want errProcessExitTimeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded wait took %s", elapsed)
	}
}

func TestJSONInspectionRejectsExcessiveNesting(t *testing.T) {
	value := strings.Repeat("[", maxJSONDepth+1) + "0" + strings.Repeat("]", maxJSONDepth+1)
	if err := validateJSONNoDuplicates([]byte(value)); err == nil {
		t.Fatal("accepted JSON beyond the reviewed nesting bound")
	}
}

func TestVerifyRejectsUnreviewedCodex0147WithoutRetainingState(t *testing.T) {
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "success")
	t.Setenv("SESSIONREVIEWER_FAKE_VERSION", "codex-cli 0.147.0")
	adapter := New()
	capability, err := adapter.Verify(context.Background(), fakeExecutable)
	if !reflect.DeepEqual(capability, agent.Capability{}) {
		t.Fatalf("0.147.x returned an unsupported execution capability: %+v", capability)
	}
	assertCode(t, err, agent.CodeIncompatible)
	if adapter.executable != "" || adapter.executableIdentity != nil || adapter.capability != (agent.Capability{}) {
		t.Fatalf("failed verification retained runnable state: %+v", adapter)
	}
	if got := fmt.Sprint(err); got != string(agent.CodeIncompatible) {
		t.Fatalf("public failure leaked private capability details: %q", got)
	}
}

func TestVerifyRealCodex0147FailsClosedWhenProvided(t *testing.T) {
	path := os.Getenv("SESSIONREVIEWER_REAL_CODEX_0147")
	if path == "" {
		t.Skip("set SESSIONREVIEWER_REAL_CODEX_0147 to an exact native 0.147.x binary")
	}
	capability, err := New().Verify(context.Background(), path)
	if !reflect.DeepEqual(capability, agent.Capability{}) {
		t.Fatalf("real 0.147.x returned unsupported capability: %+v", capability)
	}
	assertCode(t, err, agent.CodeIncompatible)
	if got := fmt.Sprint(err); got != string(agent.CodeIncompatible) {
		t.Fatalf("real probe leaked private details: %q", got)
	}
}

func TestVerifyRealCodex0150WhenProvided(t *testing.T) {
	path := os.Getenv("SESSIONREVIEWER_REAL_CODEX_0150")
	if path == "" {
		t.Skip("set SESSIONREVIEWER_REAL_CODEX_0150 to an exact native 0.150.x binary")
	}
	capability, err := New().Verify(context.Background(), path)
	if err != nil {
		t.Fatalf("real 0.150.x verification failed: %v cause=%v", err, errors.Unwrap(err))
	}
	if capability.Provider != "codex" || capability.Version == "" || capability.NoTools ||
		capability.Containment != agent.ContainmentRestrictedReadOnly {
		t.Fatalf("real 0.150.x capability=%+v", capability)
	}
}

func TestFeaturePolicyRequiresReviewedRowsAndDisablesRestrictedCapabilities(t *testing.T) {
	baseline := reviewedFeatureOutput(false)
	if err := validateFeatureTable(baseline, false); err != nil {
		t.Fatalf("reviewed baseline rejected: %v", err)
	}
	restricted := reviewedFeatureOutput(true)
	if err := validateFeatureTable(restricted, true); err != nil {
		t.Fatalf("reviewed restricted table rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{name: "missing row", mutate: func(value string) string {
			rows := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
			return strings.Join(rows[1:], "\n") + "\n"
		}},
		{name: "duplicate row", mutate: func(value string) string { return value + strings.SplitN(value, "\n", 2)[0] + "\n" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateFeatureTable(test.mutate(baseline), false); err == nil {
				t.Fatal("accepted malformed or incomplete reviewed feature table")
			}
		})
	}
	if err := validateFeatureTable(strings.Replace(restricted, "apps stable false", "apps stable true", 1), true); err == nil {
		t.Fatal("accepted an enabled restricted feature")
	}
}

func TestPromptProbeAcceptsBoundedBuiltinContextButRequiresFinalMarker(t *testing.T) {
	clean := []byte(`[{"id":"probe","type":"message","role":"user","content":[{"type":"input_text","text":"` + capabilityProbe + `"}]}]`)
	if err := validatePromptProbe(clean); err != nil {
		t.Fatalf("clean isolated prompt rejected: %v", err)
	}
	builtin := []byte(`[{"type":"message","role":"developer","content":[{"type":"input_text","text":"reviewed built-in context"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"` + capabilityProbe + `"}]}]`)
	if err := validatePromptProbe(builtin); err != nil {
		t.Fatalf("bounded built-in context rejected: %v", err)
	}
	for _, invalid := range [][]byte{
		[]byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"` + capabilityProbe + `"},{"type":"input_text","text":"cwd=/private/project ENV_CANARY=secret"}]}]`),
		[]byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"` + capabilityProbe + `"}]},{"type":"message","role":"developer","content":[{"type":"input_text","text":"late context"}]}]`),
	} {
		if err := validatePromptProbe(invalid); err == nil {
			t.Fatalf("accepted malformed prompt boundary: %s", invalid)
		}
	}
}

func reviewedFeatureOutput(restricted bool) string {
	var output strings.Builder
	for _, feature := range reviewedFeatures {
		enabled := feature.defaultEnabled
		if restricted && feature.disabled {
			enabled = false
		}
		fmt.Fprintf(&output, "%s %s %t\n", feature.name, feature.stage, enabled)
	}
	return output.String()
}

// containedRunnerForTest is deliberately confined to this same-package test
// file so generation tests can focus on the already-verified run boundary.
func containedRunnerForTest(t *testing.T) *Adapter {
	t.Helper()
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "success")
	t.Setenv("SESSIONREVIEWER_FAKE_VERSION", "")
	t.Setenv("SESSIONREVIEWER_FAKE_PROPOSAL_PATH", proposalFixture)
	physical, identity, err := resolveExecutable(fakeExecutable)
	if err != nil {
		t.Fatal(err)
	}
	return &Adapter{executable: physical, executableIdentity: identity}
}

func validRequest(t *testing.T, prompt []byte) agent.Request {
	t.Helper()
	workingDirectory := t.TempDir()
	if err := prepareOwnedPrivateDirectory(workingDirectory); err != nil {
		t.Fatal(err)
	}
	return agent.Request{
		Prompt:           append([]byte(nil), prompt...),
		OutputSchema:     append([]byte(nil), schemaFixture...),
		WorkingDirectory: workingDirectory,
		ForbiddenRoots: []agent.ForbiddenRoot{
			{Kind: agent.ForbiddenRootProject, CanonicalPath: canonicalDirectory(t, t.TempDir())},
			{Kind: agent.ForbiddenRootVault, CanonicalPath: canonicalDirectory(t, t.TempDir())},
		},
		Deadline: time.Now().Add(10 * time.Second),
	}
}

func canonicalDirectory(t *testing.T, path string) string {
	t.Helper()
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(physical)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(absolute)
}

func testExecutablePath(directory, name string) string {
	path := filepath.Join(directory, name)
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	return path
}

func windowsDeniedNamespaceReplacement(err error) bool {
	return runtime.GOOS == "windows" && (errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.Errno(32)))
}

func assertCode(t *testing.T, err error, want agent.ErrorCode) {
	t.Helper()
	got, ok := agent.CodeOf(err)
	if !ok || got != want {
		t.Fatalf("error=%v cause=%v code=(%q,%v) want=(%q,true)", err, errors.Unwrap(err), got, ok, want)
	}
}

func readCalls(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var result [][]string
	for {
		var call []string
		if err := decoder.Decode(&call); errors.Is(err, io.EOF) {
			return result
		} else if err != nil {
			t.Fatal(err)
		}
		result = append(result, call)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func assertRecordedProcessStops(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	pid := 0
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatalf("timed out waiting for valid PID in %s", path)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d remains alive", pid)
}

func copyFile(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}
