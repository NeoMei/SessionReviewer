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

func TestMain(m *testing.M) {
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

func TestVerifyPinsExactCodex0147CapabilityContract(t *testing.T) {
	callsPath := filepath.Join(t.TempDir(), "calls.jsonl")
	t.Setenv("SESSIONREVIEWER_FAKE_CALLS_PATH", callsPath)
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "success")
	adapter := New()
	capability, err := adapter.Verify(context.Background(), fakeExecutable)
	if err != nil {
		t.Fatal(err)
	}
	want := agent.Capability{
		Provider:           "codex",
		Version:            "0.147.0",
		ProposalOnly:       true,
		NoTools:            true,
		ReadOnly:           true,
		StructuredOutput:   true,
		NativeCancellation: true,
		ModelProvenance:    agent.ModelProvenanceUnavailable,
	}
	if !reflect.DeepEqual(capability, want) {
		t.Fatalf("capability=%+v want=%+v", capability, want)
	}
	calls := readCalls(t, callsPath)
	if len(calls) != 4 || !reflect.DeepEqual(calls[0], []string{"--version"}) ||
		!reflect.DeepEqual(calls[1], []string{"exec", "--help"}) ||
		!reflect.DeepEqual(calls[2], []string{"features", "list"}) ||
		len(calls[3]) < 3 || !reflect.DeepEqual(calls[3][:2], []string{"debug", "prompt-input"}) ||
		calls[3][len(calls[3])-1] != capabilityProbe {
		t.Fatalf("verification calls=%v", calls)
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
		{name: "newer version", path: fakeExecutable, version: "codex-cli 0.148.0", code: agent.CodeIncompatible},
		{name: "prerelease", path: fakeExecutable, version: "codex-cli 0.147.0-alpha.1", code: agent.CodeIncompatible},
		{name: "noncanonical patch version", path: fakeExecutable, version: "codex-cli 0.147.01", code: agent.CodeIncompatible},
		{name: "missing flag", path: fakeExecutable, mode: "verify-missing-flag", code: agent.CodeIncompatible},
		{name: "missing stable feature", path: fakeExecutable, mode: "verify-missing-feature", code: agent.CodeIncompatible},
		{name: "unstable required feature", path: fakeExecutable, mode: "verify-unstable-feature", code: agent.CodeIncompatible},
		{name: "malformed feature table", path: fakeExecutable, mode: "verify-malformed-features", code: agent.CodeIncompatible},
		{name: "noncanonical feature state", path: fakeExecutable, mode: "verify-noncanonical-feature-state", code: agent.CodeIncompatible},
		{name: "malformed prompt probe", path: fakeExecutable, mode: "verify-malformed-probe", code: agent.CodeIncompatible},
		{name: "probe marker outside user prompt", path: fakeExecutable, mode: "verify-marker-outside-user", code: agent.CodeIncompatible},
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

func TestGenerateProposalUsesOnlyTheFixedNoToolsInvocationAndStdinPrompt(t *testing.T) {
	adapter := verifiedAdapter(t)
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("SESSIONREVIEWER_FAKE_CAPTURE_PATH", capturePath)
	result, err := adapter.GenerateProposal(context.Background(), validRequest(t, []byte("PROMPT_ONLY_ON_STDIN")))
	if err != nil {
		t.Fatalf("%v (cause: %v)", err, errors.Unwrap(err))
	}
	if !bytes.Equal(result.Proposal, mustRead(t, proposalFixture)) {
		t.Fatal("proposal bytes changed")
	}
	if result.Model != "" {
		t.Fatalf("0.147.x result invented model %q", result.Model)
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
		"--skip-git-repo-check", "--output-schema", capture.SchemaPath,
		"--disable", "shell_tool", "--disable", "apps",
		"--disable", "browser_use", "--disable", "browser_use_external",
		"--disable", "browser_use_full_cdp_access", "--disable", "computer_use",
		"--disable", "image_generation", "--disable", "workspace_dependencies",
		"--disable", "skill_search", "--disable", "remote_plugin", "-",
	}
	if !reflect.DeepEqual(capture.Args, wantArgs) {
		t.Fatalf("args=%q want=%q", capture.Args, wantArgs)
	}
	if capture.Stdin != "PROMPT_ONLY_ON_STDIN" || strings.Contains(strings.Join(capture.Args, "\x00"), capture.Stdin) {
		t.Fatalf("prompt placement args=%q stdin=%q", capture.Args, capture.Stdin)
	}
	if capture.Schema != string(schemaFixture) || capture.SchemaMode != 0o600 || !filepath.IsAbs(capture.SchemaPath) {
		t.Fatalf("schema capture path=%q mode=%#o bytes=%d", capture.SchemaPath, capture.SchemaMode, len(capture.Schema))
	}
	if runtime.GOOS != "windows" && capture.CWDMode != 0o700 {
		t.Fatalf("private run directory mode=%#o want=0700", capture.CWDMode)
	}
	if info, statErr := os.Stat(capture.CWD); !os.IsNotExist(statErr) || info != nil {
		t.Fatalf("private run directory was not removed: info=%v err=%v", info, statErr)
	}
}

func TestGenerateProposalNeverInventsModelFromStderr(t *testing.T) {
	adapter := verifiedAdapter(t)
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "success-stderr-model")
	result, err := adapter.GenerateProposal(context.Background(), validRequest(t, []byte("prompt")))
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "" {
		t.Fatalf("0.147.x stderr invented model provenance %q", result.Model)
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
	}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			adapter := verifiedAdapter(t)
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
			adapter := verifiedAdapter(t)
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
	adapter := verifiedAdapter(t)
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "ignored-term-child")
	t.Setenv("SESSIONREVIEWER_FAKE_CHILD_PID_PATH", pidPath)
	request := validRequest(t, []byte("prompt"))
	request.Deadline = time.Now().Add(200 * time.Millisecond)
	result, err := adapter.GenerateProposal(context.Background(), request)
	if !reflect.DeepEqual(result, agent.Result{}) {
		t.Fatalf("timeout returned result: %+v", result)
	}
	assertCode(t, err, agent.CodeTimeout)
	assertRecordedProcessStops(t, pidPath)
}

func TestCancelIsIdempotentAndKillsIgnoredProcessTree(t *testing.T) {
	adapter := verifiedAdapter(t)
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
	adapter := verifiedAdapter(t)
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
	adapter := verifiedAdapter(t)
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
			adapter := verifiedAdapter(t)
			pidPath := filepath.Join(t.TempDir(), "child.pid")
			t.Setenv("SESSIONREVIEWER_FAKE_MODE", mode)
			t.Setenv("SESSIONREVIEWER_FAKE_CHILD_PID_PATH", pidPath)
			_, _ = adapter.GenerateProposal(context.Background(), validRequest(t, []byte("prompt")))
			assertRecordedProcessStops(t, pidPath)
		})
	}
}

func TestGenerateProposalRechecksVerifiedPhysicalIdentity(t *testing.T) {
	directory := t.TempDir()
	verifiedPath := filepath.Join(directory, "codex")
	copyFile(t, fakeExecutable, verifiedPath, 0o700)
	adapter := New()
	if _, err := adapter.Verify(context.Background(), verifiedPath); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(directory, "replacement")
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
	path := filepath.Join(directory, "codex")
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

func verifiedAdapter(t *testing.T) *Adapter {
	t.Helper()
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "success")
	t.Setenv("SESSIONREVIEWER_FAKE_VERSION", "")
	t.Setenv("SESSIONREVIEWER_FAKE_PROPOSAL_PATH", proposalFixture)
	adapter := New()
	if _, err := adapter.Verify(context.Background(), fakeExecutable); err != nil {
		t.Fatal(err)
	}
	return adapter
}

func validRequest(t *testing.T, prompt []byte) agent.Request {
	t.Helper()
	workingDirectory := t.TempDir()
	if err := os.Chmod(workingDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	return agent.Request{
		Prompt:           append([]byte(nil), prompt...),
		OutputSchema:     append([]byte(nil), schemaFixture...),
		WorkingDirectory: workingDirectory,
		Deadline:         time.Now().Add(10 * time.Second),
	}
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
	waitForFile(t, path)
	pid, err := strconv.Atoi(strings.TrimSpace(string(mustRead(t, path))))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
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
