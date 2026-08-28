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
	"strings"
	"sync"
	"time"

	"github.com/neomei/SessionReviewer/internal/agent"
)

const (
	capabilityProbe = "SESSIONREVIEWER_CODEX_CAPABILITY_PROBE_V1"
	probeTimeout    = 5 * time.Second
	maxProbeStdout  = 2 << 20
	maxProbeStderr  = 256 << 10
)

var (
	versionPattern = regexp.MustCompile(`^codex-cli (0\.147\.(?:0|[1-9][0-9]*))\r?\n?$`)
	requiredFlags  = []string{
		"ephemeral",
		"ignore-user-config",
		"ignore-rules",
		"sandbox",
		"json",
		"color",
		"skip-git-repo-check",
		"output-schema",
		"disable",
	}
	requiredStableFeatures = []string{
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
)

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

// Verify pins one absolute physical executable and the exact reviewed 0.147.x
// capability contract. Any failed re-verification leaves the Adapter
// unconfigured rather than retaining stale trust.
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
	probeDirectory, err := os.MkdirTemp("", "session-reviewer-codex-verify-")
	if err != nil {
		return agent.Capability{}, agent.NewError(agent.CodeUnconfigured, err)
	}
	defer os.RemoveAll(probeDirectory)
	if err := os.Chmod(probeDirectory, 0o700); err != nil {
		return agent.Capability{}, agent.NewError(agent.CodeUnconfigured, err)
	}

	versionOutput, _, err := runProbe(ctx, physical, identity, probeDirectory, []string{"--version"})
	if err != nil {
		return agent.Capability{}, mapProbeError(ctx, err)
	}
	match := versionPattern.FindSubmatch(versionOutput)
	if match == nil {
		return agent.Capability{}, agent.NewError(agent.CodeIncompatible, errors.New("unsupported Codex version"))
	}
	version := string(match[1])

	helpOutput, _, err := runProbe(ctx, physical, identity, probeDirectory, []string{"exec", "--help"})
	if err != nil {
		return agent.Capability{}, mapProbeError(ctx, err)
	}
	if err := validateExecHelp(string(helpOutput)); err != nil {
		return agent.Capability{}, agent.NewError(agent.CodeIncompatible, err)
	}

	featureOutput, _, err := runProbe(ctx, physical, identity, probeDirectory, []string{"features", "list"})
	if err != nil {
		return agent.Capability{}, mapProbeError(ctx, err)
	}
	if err := validateFeatures(string(featureOutput)); err != nil {
		return agent.Capability{}, agent.NewError(agent.CodeIncompatible, err)
	}

	probeOutput, _, err := runProbe(ctx, physical, identity, probeDirectory, []string{"debug", "prompt-input", capabilityProbe})
	if err != nil {
		return agent.Capability{}, mapProbeError(ctx, err)
	}
	if err := validatePromptProbe(probeOutput); err != nil {
		return agent.Capability{}, agent.NewError(agent.CodeIncompatible, err)
	}
	if err := recheckExecutable(physical, identity); err != nil {
		return agent.Capability{}, agent.NewError(agent.CodeIncompatible, err)
	}

	capability := agent.Capability{
		Provider:           "codex",
		Version:            version,
		ProposalOnly:       true,
		NoTools:            true,
		ReadOnly:           true,
		StructuredOutput:   true,
		NativeCancellation: true,
		ModelProvenance:    agent.ModelProvenanceUnavailable,
	}
	adapter.mu.Lock()
	adapter.executable = physical
	adapter.executableIdentity = identity
	adapter.capability = capability
	succeeded = true
	adapter.mu.Unlock()
	return capability, nil
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

func runProbe(ctx context.Context, executable string, expected *executableIdentity, directory string, args []string) ([]byte, []byte, error) {
	if err := recheckExecutable(executable, expected); err != nil {
		return nil, nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	stdout := newBoundedBuffer(maxProbeStdout)
	stderr := newBoundedBuffer(maxProbeStderr)
	command := exec.Command(executable, args...)
	command.Dir = directory
	command.Stdout = stdout
	command.Stderr = stderr
	process, err := startManagedProcess(command)
	if err != nil {
		return nil, nil, err
	}
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
	releaseErr := releaseManagedProcess(process)
	if stdout.Exceeded() || stderr.Exceeded() {
		return nil, nil, errors.New("Codex capability probe exceeded output bounds")
	}
	if err != nil || stopErr != nil || exitErr != nil || cleanupErr != nil || releaseErr != nil {
		return stdout.Bytes(), stderr.Bytes(), errors.Join(err, stopErr, exitErr, cleanupErr, releaseErr)
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

func validateFeatures(output string) error {
	features := make(map[string]string)
	for lineNumber, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			return fmt.Errorf("malformed Codex feature row %d", lineNumber+1)
		}
		if state := fields[len(fields)-1]; state != "true" && state != "false" {
			return fmt.Errorf("malformed Codex feature state at row %d", lineNumber+1)
		}
		name := fields[0]
		if _, duplicate := features[name]; duplicate {
			return fmt.Errorf("duplicate Codex feature %q", name)
		}
		features[name] = strings.Join(fields[1:len(fields)-1], " ")
	}
	for _, name := range requiredStableFeatures {
		if features[name] != "stable" {
			return fmt.Errorf("required stable Codex feature %q is unavailable", name)
		}
	}
	return nil
}

func validatePromptProbe(output []byte) error {
	if err := validateJSONNoDuplicates(output); err != nil {
		return errors.New("Codex prompt-input probe is malformed")
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	var messages []struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := decoder.Decode(&messages); err != nil || messages == nil || len(messages) == 0 {
		return errors.New("Codex prompt-input probe is malformed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Codex prompt-input probe has trailing data")
	}
	found := false
	for _, message := range messages {
		if message.Type != "message" || message.Role != "user" {
			continue
		}
		for _, content := range message.Content {
			if content.Type == "input_text" && content.Text == capabilityProbe {
				found = true
			}
		}
	}
	if !found {
		return errors.New("Codex prompt-input probe omitted the harmless marker")
	}
	return nil
}
