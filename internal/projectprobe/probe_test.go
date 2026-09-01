package projectprobe

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
)

func TestMain(m *testing.M) {
	if marker := os.Getenv("PROJECTPROBE_FAKE_GIT_MARKER"); marker != "" {
		_ = os.WriteFile(marker, []byte("PATH git executed"), 0o600)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

var approvedGitCalls = [][]string{
	{"git", "rev-parse", "--show-toplevel"},
	{"git", "symbolic-ref", "--short", "-q", "HEAD"},
	{"git", "rev-parse", "HEAD"},
	{"git", "status", "--porcelain=v1", "-z"},
	{"git", "remote", "get-url", "--all", "origin"},
}

func TestRunProducesStableStateAndTimestampedChecksUsingOnlyApprovedGit(t *testing.T) {
	root, binding := newBinding(t)
	writeFile(t, root, "VERSION", "1.2.3\n")
	writeFile(t, root, "docs/session-review/项目回顾.md", "human text that must not be persisted")
	runner := &recordingRunner{responses: map[string]runnerResponse{
		callKey("rev-parse", "--show-toplevel"):          {output: []byte(root + "\n")},
		callKey("symbolic-ref", "--short", "-q", "HEAD"): {output: []byte("main\n")},
		callKey("rev-parse", "HEAD"):                     {output: []byte(strings.Repeat("a", 40) + "\n")},
		callKey("status", "--porcelain=v1", "-z"):        {output: []byte(" M VERSION\x00?? new.txt\x00")},
		callKey("remote", "get-url", "--all", "origin"):  {output: []byte("git@github.com:owner/private.git\nhttps://example.invalid/owner/private.git\n")},
	}}
	times := []time.Time{
		time.Date(2026, 9, 1, 1, 2, 3, 4, time.UTC),
		time.Date(2026, 9, 1, 1, 2, 4, 5, time.FixedZone("offset", 8*60*60)),
	}
	now := func() time.Time {
		value := times[0]
		times = times[1:]
		return value
	}
	options := Options{
		Binding:                 binding,
		VersionFiles:            []string{"VERSION"},
		RequiredProjectionFiles: []string{"docs/session-review/项目回顾.md", "docs/session-review/项目历史.md"},
		Now:                     now,
		RunGit:                  runner.run,
	}

	firstState, firstCheck, err := Run(context.Background(), options)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	secondState, secondCheck, err := Run(context.Background(), options)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if firstState.Digest != secondState.Digest || !reflect.DeepEqual(firstState, secondState) {
		t.Fatalf("identical live state churned:\nfirst=%+v\nsecond=%+v", firstState, secondState)
	}
	if firstCheck.CheckedAt != "2026-09-01T01:02:03.000000004Z" || secondCheck.CheckedAt != "2026-08-31T17:02:04.000000005Z" {
		t.Fatalf("checks did not use normalized UTC times: %q %q", firstCheck.CheckedAt, secondCheck.CheckedAt)
	}
	if firstCheck.StateDigest != firstState.Digest || secondCheck.StateDigest != secondState.Digest || !firstCheck.Available || !secondCheck.Available {
		t.Fatalf("invalid checks: %+v %+v", firstCheck, secondCheck)
	}
	if firstState.ProjectID != "project-a" || firstState.CanonicalRoot != root || firstState.Branch != "main" || firstState.Head != strings.Repeat("a", 40) || firstState.DirtyPathCount != 2 {
		t.Fatalf("unexpected state: %+v", firstState)
	}
	versionHash := fmt.Sprintf("%x", sha256.Sum256([]byte("1.2.3\n")))
	projectionHash := fmt.Sprintf("%x", sha256.Sum256([]byte("human text that must not be persisted")))
	wantVersions := []memory.ProbeFile{{Path: "VERSION", Exists: true, ContentHash: versionHash}}
	wantRequired := []memory.ProbeFile{
		{Path: "docs/session-review/项目历史.md", Exists: false},
		{Path: "docs/session-review/项目回顾.md", Exists: true, ContentHash: projectionHash},
	}
	if !reflect.DeepEqual(firstState.VersionFiles, wantVersions) || !reflect.DeepEqual(firstState.RequiredProjectionFiles, wantRequired) {
		t.Fatalf("unexpected file state: versions=%+v required=%+v", firstState.VersionFiles, firstState.RequiredProjectionFiles)
	}
	if len(firstState.RemoteIdentityHashes) != 2 || strings.Contains(fmt.Sprintf("%+v", firstState), "private.git") || strings.Contains(fmt.Sprintf("%+v", firstState), "human text") {
		t.Fatalf("raw private material escaped into state: %+v", firstState)
	}
	if err := memory.ValidateProjectProbeState(firstState); err != nil {
		t.Fatalf("state fails memory contract: %v", err)
	}
	if err := memory.ValidateProbeCheck(firstCheck); err != nil {
		t.Fatalf("check fails memory contract: %v", err)
	}
	wantCalls := append(append([][]string{}, approvedGitCalls...), approvedGitCalls...)
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("unexpected executable or argv sequence:\ngot  %q\nwant %q", runner.calls, wantCalls)
	}

	options.VersionFiles[0] = "changed"
	firstState.VersionFiles[0].Path = "changed"
	firstCheck.Diagnostics = append(firstCheck.Diagnostics, memory.Diagnostic{Code: "changed"})
	if secondState.VersionFiles[0].Path != "VERSION" || len(secondCheck.Diagnostics) != 0 {
		t.Fatal("Run returned data aliased to caller or a prior result")
	}
}

func TestRestrictedGitRunnerRejectsEveryNonAllowlistedExecutableOrArgv(t *testing.T) {
	runner := &recordingRunner{}
	bad := [][]string{
		{"sh", "-c", "git status"},
		{"bash", "-lc", "git status"},
		{"npm", "test"},
		{"go", "test", "./..."},
		{"git", "fetch"},
		{"git", "ls-remote", "origin"},
		{"git", "status"},
		{"git", "status", "--porcelain=v2", "-z"},
		{"git", "remote", "get-url", "origin"},
		{"git", "-c", "credential.helper=x", "rev-parse", "HEAD"},
	}
	for _, command := range bad {
		if _, err := runApprovedGit(context.Background(), runner.run, command[0], command[1:]...); err == nil {
			t.Fatalf("unsafe command accepted: %q", command)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("rejected commands reached runner: %q", runner.calls)
	}
	for _, command := range approvedGitCalls {
		if _, err := runApprovedGit(context.Background(), runner.run, command[0], command[1:]...); err != nil {
			t.Fatalf("approved command rejected %q: %v", command, err)
		}
	}
	if !reflect.DeepEqual(runner.calls, approvedGitCalls) {
		t.Fatalf("approved calls changed: %q", runner.calls)
	}
}

func TestProductionGitContextIsBoundedAndEnvironmentDisablesInteractiveSideEffects(t *testing.T) {
	ctx, cancel := boundedGitContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 11*time.Second {
		t.Fatalf("production Git context is not bounded: deadline=%v ok=%v", deadline, ok)
	}
	parent, parentCancel := context.WithTimeout(context.Background(), time.Second)
	defer parentCancel()
	bounded, boundedCancel := boundedGitContext(parent)
	defer boundedCancel()
	parentDeadline, _ := parent.Deadline()
	boundedDeadline, _ := bounded.Deadline()
	if !boundedDeadline.Equal(parentDeadline) {
		t.Fatalf("bounded context extended its parent: parent=%v bounded=%v", parentDeadline, boundedDeadline)
	}

	environment := safeGitEnvironment([]string{
		"PATH=/evil/bin", "HOME=/evil/home", "GIT_DIR=/evil", "GIT_WORK_TREE=/evil",
		"git_dir=C:/evil-lower", "git_work_tree=C:/evil-lower", "git_config_count=9",
		"git_config_key_0=core.fsmonitor", "git_config_value_0=evil.exe",
		"GIT_TERMINAL_PROMPT=1", "GIT_OPTIONAL_LOCKS=1", "GCM_INTERACTIVE=Always",
		"git_terminal_prompt=1", "git_askpass=C:/evil.exe", "ssh_askpass=C:/evil.exe",
		"GIT_ASKPASS=/evil", "SSH_ASKPASS=/evil", "GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=/evil",
		"LD_PRELOAD=/evil.so", "DYLD_INSERT_LIBRARIES=/evil.dylib", "SystemRoot=C:/Windows", "TEMP=C:/Temp",
	})
	joined := strings.Join(environment, "\n")
	for _, forbidden := range []string{"PATH=", "HOME=", "GIT_DIR=/evil", "git_dir=", "git_work_tree=", "git_config_count=9", "git_config_value_0=evil.exe", "git_terminal_prompt=1", "git_askpass=", "ssh_askpass=", "LD_PRELOAD=", "DYLD_INSERT_LIBRARIES=", "GIT_TERMINAL_PROMPT=1", "GIT_ASKPASS=/evil", "SSH_ASKPASS=/evil", "GIT_CONFIG_VALUE_0=/evil"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("unsafe Git environment survived: %q", forbidden)
		}
	}
	for _, required := range []string{"SYSTEMROOT=C:/Windows", "TEMP=C:/Temp", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GCM_INTERACTIVE=Never", "GIT_PAGER=", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=", "core.fsmonitor", "diff.ignoreSubmodules", "core.hooksPath", "core.pager"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("safe Git environment omitted %q: %q", required, environment)
		}
	}
}

func TestRunDefaultUsesAuthenticatedAbsoluteGitAndIgnoresFakePATH(t *testing.T) {
	trustedGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("Git unavailable: %v", err)
	}
	trustedGit, err = filepath.Abs(trustedGit)
	if err != nil {
		t.Fatal(err)
	}
	trustedGit, err = filepath.EvalSymlinks(trustedGit)
	if err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := projectidentity.Resolve(config.ProjectMapping{ID: "project-a", Root: repoRoot}, repoRoot, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	fakeDirectory := t.TempDir()
	fakeName := "git"
	if runtime.GOOS == "windows" {
		fakeName = "git.exe"
	}
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copyExecutable(t, testExecutable, filepath.Join(fakeDirectory, fakeName))
	marker := filepath.Join(t.TempDir(), "path-git-ran")
	t.Setenv("PATH", fakeDirectory)
	t.Setenv("PROJECTPROBE_FAKE_GIT_MARKER", marker)
	if _, _, err := Run(context.Background(), Options{Binding: binding, GitExecutable: trustedGit, Now: time.Now}); err != nil {
		t.Fatalf("authenticated absolute Git failed: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fake PATH Git was executed: %v", err)
	}
}

func TestRunDefaultRejectsMissingRelativeAndInProjectGitExecutable(t *testing.T) {
	root, binding := newBinding(t)
	inProject := filepath.Join(root, "git")
	writeFileMode(t, inProject, "not git", 0o700)
	for _, executable := range []string{"", "git", inProject} {
		if _, _, err := Run(context.Background(), Options{Binding: binding, GitExecutable: executable, Now: time.Now}); err == nil {
			t.Fatalf("unsafe production Git executable accepted: %q", executable)
		}
	}
}

func TestUnavailableCommandDiagnosticsDoNotChurnWithPrivateErrorText(t *testing.T) {
	root, binding := newBinding(t)
	firstRunner := successfulRunner(root)
	firstRunner.responses[callKey("remote", "get-url", "--all", "origin")] = runnerResponse{err: errors.New("secret-a")}
	secondRunner := successfulRunner(root)
	secondRunner.responses[callKey("remote", "get-url", "--all", "origin")] = runnerResponse{err: errors.New("secret-b")}
	options := Options{Binding: binding, Now: time.Now, RunGit: firstRunner.run}
	first, _, err := Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	options.RunGit = secondRunner.run
	second, _, err := Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest || !reflect.DeepEqual(first.Diagnostics, []memory.Diagnostic{{Code: "git_remote_unavailable"}}) {
		t.Fatalf("private runner error text churned stable availability state: first=%+v second=%+v", first.Diagnostics, second.Diagnostics)
	}
}

func TestRunRejectsUnauthenticatedBindingAndGitTopLevelMismatch(t *testing.T) {
	root, binding := newBinding(t)
	runner := successfulRunner(root)
	wrong := binding
	wrong.RootIdentity.File = "999999999"
	if _, _, err := Run(context.Background(), Options{Binding: wrong, Now: time.Now, RunGit: runner.run}); err == nil {
		t.Fatal("Run accepted a binding whose physical root identity changed")
	}
	if len(runner.calls) != 0 {
		t.Fatal("Git ran before binding authentication")
	}

	other, _ := newBinding(t)
	runner = successfulRunner(other)
	if _, _, err := Run(context.Background(), Options{Binding: binding, Now: time.Now, RunGit: runner.run}); err == nil {
		t.Fatal("Run accepted a Git top-level outside the authenticated binding")
	}
}

func TestRunRejectsGitCommonDirectoryReplacementBeforeAndAfterProbe(t *testing.T) {
	root, binding := newBinding(t)
	replaceGitDirectory(t, root)
	runner := successfulRunner(root)
	if _, _, err := Run(context.Background(), Options{Binding: binding, Now: time.Now, RunGit: runner.run}); err == nil {
		t.Fatal("Run accepted common-directory replacement before Git")
	}
	if len(runner.calls) != 0 {
		t.Fatal("Git ran before common-directory reauthentication")
	}

	root, binding = newBinding(t)
	writeFile(t, root, "VERSION", "1")
	runner = successfulRunner(root)
	runner.after = func(call []string) {
		if reflect.DeepEqual(call, approvedGitCalls[len(approvedGitCalls)-1]) {
			replaceGitDirectory(t, root)
		}
	}
	if _, _, err := Run(context.Background(), Options{Binding: binding, VersionFiles: []string{"VERSION"}, Now: time.Now, RunGit: runner.run}); err == nil {
		t.Fatal("Run accepted common-directory replacement after Git and file probing")
	}
}

func TestRunRejectsUnsafeOrAliasedDeclaredPathsBeforeExecutingGit(t *testing.T) {
	root, binding := newBinding(t)
	tests := []struct {
		name     string
		versions []string
		required []string
	}{
		{name: "absolute", versions: []string{filepath.Join(root, "VERSION")}},
		{name: "traversal", versions: []string{"../VERSION"}},
		{name: "unclean", versions: []string{"a/../VERSION"}},
		{name: "backslash alias", versions: []string{`docs\VERSION`}},
		{name: "overlong", versions: []string{strings.Repeat("a", 1025)}},
		{name: "exact duplicate", versions: []string{"VERSION", "VERSION"}},
		{name: "case alias", versions: []string{"Version", "version"}},
		{name: "cross-list alias", versions: []string{"VERSION"}, required: []string{"version"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := successfulRunner(root)
			_, _, err := Run(context.Background(), Options{Binding: binding, VersionFiles: test.versions, RequiredProjectionFiles: test.required, Now: time.Now, RunGit: runner.run})
			if err == nil {
				t.Fatal("Run accepted unsafe or duplicate declared path")
			}
			if len(runner.calls) != 0 {
				t.Fatal("Git ran before declared paths were validated")
			}
		})
	}
}

func TestRunCancellationBeforeFileProbeReturnsNoPartialState(t *testing.T) {
	root, binding := newBinding(t)
	writeFile(t, root, "VERSION", "1")
	ctx, cancel := context.WithCancel(context.Background())
	runner := successfulRunner(root)
	runner.after = func(call []string) {
		if reflect.DeepEqual(call, approvedGitCalls[len(approvedGitCalls)-1]) {
			cancel()
		}
	}
	state, check, err := Run(ctx, Options{Binding: binding, VersionFiles: []string{"VERSION"}, Now: time.Now, RunGit: runner.run})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(state, memory.ProjectProbeState{}) || !reflect.DeepEqual(check, memory.ProbeCheck{}) {
		t.Fatalf("cancellation returned partial success: state=%+v check=%+v err=%v", state, check, err)
	}
}

func TestProbeFilesChecksCancellationAfterEachRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reads := 0
	reader := func(string, int64) ([]byte, bool, error) {
		reads++
		cancel()
		return []byte("private body"), true, nil
	}
	files, diagnostics, err := probeFilesWithReader(ctx, []string{"VERSION", "OTHER"}, nil, reader)
	if !errors.Is(err, context.Canceled) || files != nil || diagnostics != nil || reads != 1 {
		t.Fatalf("file-loop cancellation leaked partial data: files=%+v diagnostics=%+v reads=%d err=%v", files, diagnostics, reads, err)
	}
}

func TestRunReservesDiagnosticBudgetForGitAndDeclaredFiles(t *testing.T) {
	root, binding := newBinding(t)
	paths := make([]string, 4093)
	for index := range paths {
		paths[index] = fmt.Sprintf("missing/%04d", index)
	}
	runner := successfulRunner(root)
	if _, _, err := Run(context.Background(), Options{Binding: binding, VersionFiles: paths, Now: time.Now, RunGit: runner.run}); err == nil {
		t.Fatal("Run accepted more file diagnostics than leave room for Git diagnostics")
	}
	if len(runner.calls) != 0 {
		t.Fatal("Git ran before diagnostic budget validation")
	}
}

func TestRunQuarantinesUnsafeFilesWithoutPersistingTheirBodies(t *testing.T) {
	root, binding := newBinding(t)
	writeFile(t, root, "secret.txt", "TOP-SECRET-CONTENT")
	paths := []string{"directory.txt", "large.txt"}
	wantDiagnostics := 2
	if err := os.Symlink("secret.txt", filepath.Join(root, "redirect.txt")); err == nil {
		paths = append(paths, "redirect.txt")
		wantDiagnostics++
	}
	if err := os.Mkdir(filepath.Join(root, "directory.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "large.txt", strings.Repeat("x", maxProbeFileBytes+1))
	runner := successfulRunner(root)
	state, check, err := Run(context.Background(), Options{
		Binding:      binding,
		VersionFiles: paths,
		Now:          func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) },
		RunGit:       runner.run,
	})
	if err != nil {
		t.Fatalf("unsafe leaf should be isolated as diagnostics: %v", err)
	}
	for _, file := range state.VersionFiles {
		if file.Exists || file.ContentHash != "" {
			t.Fatalf("unsafe file was treated as authenticated: %+v", file)
		}
	}
	if check.Available || len(state.Diagnostics) != wantDiagnostics || !reflect.DeepEqual(state.Diagnostics, check.Diagnostics) {
		t.Fatalf("unsafe file diagnostics not reflected in check: state=%+v check=%+v", state.Diagnostics, check)
	}
	encoded := fmt.Sprintf("%+v %+v", state, check)
	if strings.Contains(encoded, "TOP-SECRET-CONTENT") || strings.Contains(encoded, strings.Repeat("x", 32)) {
		t.Fatal("unsafe file body escaped into diagnostics")
	}
}

func TestRunBoundsAndHashesMalformedGitOutputAndContinuesPartialFailures(t *testing.T) {
	root, binding := newBinding(t)
	secret := "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	runner := &recordingRunner{responses: map[string]runnerResponse{
		callKey("rev-parse", "--show-toplevel"):          {output: []byte(root + "\n")},
		callKey("symbolic-ref", "--short", "-q", "HEAD"): {output: []byte("bad branch\n")},
		callKey("rev-parse", "HEAD"):                     {output: []byte("not-a-head-" + secret + "\n")},
		callKey("status", "--porcelain=v1", "-z"):        {output: []byte(" M ok.txt\x00?? bad\npath\x00")},
		callKey("remote", "get-url", "--all", "origin"):  {output: []byte("https://user:" + secret + "@example.invalid/repo.git\n")},
	}}
	state, check, err := Run(context.Background(), Options{Binding: binding, Now: time.Now, RunGit: runner.run})
	if err != nil {
		t.Fatalf("malformed optional Git data should be isolated: %v", err)
	}
	if state.Branch != "" || state.Head != "" || state.DirtyPathCount != 1 || len(state.RemoteIdentityHashes) != 0 || check.Available {
		t.Fatalf("malformed Git data contaminated state: %+v %+v", state, check)
	}
	if len(state.Diagnostics) != 4 || !reflect.DeepEqual(state.Diagnostics, check.Diagnostics) {
		t.Fatalf("expected four deterministic diagnostics, got %+v", state.Diagnostics)
	}
	if strings.Contains(fmt.Sprintf("%+v %+v", state, check), secret) {
		t.Fatal("malicious Git output escaped into persisted values")
	}

	runner = successfulRunner(root)
	runner.responses[callKey("remote", "get-url", "--all", "origin")] = runnerResponse{err: errors.New("credential " + secret)}
	state, check, err = Run(context.Background(), Options{Binding: binding, Now: time.Now, RunGit: runner.run})
	if err != nil || check.Available || len(state.Diagnostics) != 1 || strings.Contains(fmt.Sprintf("%+v", state.Diagnostics), secret) {
		t.Fatalf("partial Git failure was not bounded and isolated: err=%v state=%+v check=%+v", err, state, check)
	}
}

func TestRunBoundsRemoteIdentitiesAtMemoryMaximum(t *testing.T) {
	root, binding := newBinding(t)
	lines := make([]string, 300)
	for index := range lines {
		lines[index] = fmt.Sprintf("https://example.invalid/repo-%03d.git", index)
	}
	runner := successfulRunner(root)
	runner.responses[callKey("remote", "get-url", "--all", "origin")] = runnerResponse{output: []byte(strings.Join(lines, "\n") + "\n")}
	state, check, err := Run(context.Background(), Options{Binding: binding, Now: time.Now, RunGit: runner.run})
	if err != nil {
		t.Fatalf("excess remotes should be isolated, not invalidate state: %v", err)
	}
	if len(state.RemoteIdentityHashes) != 256 || check.Available || !hasDiagnostic(state.Diagnostics, "git_remote_excess") {
		t.Fatalf("remote bound not enforced: hashes=%d diagnostics=%+v", len(state.RemoteIdentityHashes), state.Diagnostics)
	}
	if err := memory.ValidateProjectProbeState(state); err != nil {
		t.Fatalf("bounded remote state is not validator-safe: %v", err)
	}
}

func TestWindowsLocalRemoteGrammarIsStrictAndHashedOnly(t *testing.T) {
	valid := []string{`C:/Repos/Owner/repo.git`, `D:\Repos\Owner\repo.git`, `//server/share`, `//server/share/repo.git`, `\\server\share\other.git`}
	hashes, malformed, excess := parseRemoteIdentities([]byte(strings.Join(valid, "\n") + "\n"))
	if malformed || excess || len(hashes) != len(valid) {
		t.Fatalf("strict Windows local remotes rejected: hashes=%v malformed=%v excess=%v", hashes, malformed, excess)
	}
	for _, remote := range valid {
		if strings.Contains(strings.Join(hashes, "\n"), remote) {
			t.Fatal("raw Windows remote escaped hashing")
		}
	}
	for _, invalid := range []string{`relative/repo.git`, `C:relative\repo.git`, `//server`, `\\server`, `//user@server/share`, `https://token@example.invalid/repo.git`, `https://user:secret@example.invalid/repo.git`} {
		if validRemote(invalid) {
			t.Fatalf("relative, confusable, or credential-bearing remote accepted: %q", invalid)
		}
	}
}

func TestRunHonorsCancellationAndRejectsRootSwap(t *testing.T) {
	root, binding := newBinding(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	runner := successfulRunner(root)
	if _, _, err := Run(canceled, Options{Binding: binding, Now: time.Now, RunGit: runner.run}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run did not preserve cancellation: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("Git ran after cancellation")
	}

	parent := filepath.Dir(root)
	moved := filepath.Join(parent, filepath.Base(root)+"-moved")
	runner = successfulRunner(root)
	runner.after = func(call []string) {
		if reflect.DeepEqual(call, approvedGitCalls[len(approvedGitCalls)-1]) {
			if err := os.Rename(root, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, _, err := Run(context.Background(), Options{Binding: binding, Now: time.Now, RunGit: runner.run}); err == nil {
		t.Fatal("Run accepted a replacement at the authenticated root path")
	}
}

func newBinding(t *testing.T) (string, projectidentity.Binding) {
	t.Helper()
	root := filepath.Clean(t.TempDir())
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	binding, err := projectidentity.Resolve(config.ProjectMapping{ID: "project-a", Root: root}, root, "darwin")
	if err != nil {
		t.Fatalf("resolve binding: %v", err)
	}
	return binding.CanonicalRoot, binding
}

func writeFile(t *testing.T, root, relative, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeFileMode(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func replaceGitDirectory(t *testing.T, root string) {
	t.Helper()
	original := filepath.Join(root, ".git")
	moved := filepath.Join(root, fmt.Sprintf(".git-old-%d", time.Now().UnixNano()))
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
}

func copyExecutable(t *testing.T, source, destination string) {
	t.Helper()
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
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

func hasDiagnostic(values []memory.Diagnostic, code string) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}

type runnerResponse struct {
	output []byte
	err    error
}

type recordingRunner struct {
	responses map[string]runnerResponse
	calls     [][]string
	after     func([]string)
}

func (runner *recordingRunner) run(_ context.Context, executable string, args ...string) ([]byte, error) {
	call := append([]string{executable}, args...)
	runner.calls = append(runner.calls, append([]string(nil), call...))
	response := runner.responses[callKey(args...)]
	if runner.after != nil {
		runner.after(call)
	}
	return append([]byte(nil), response.output...), response.err
}

func successfulRunner(root string) *recordingRunner {
	return &recordingRunner{responses: map[string]runnerResponse{
		callKey("rev-parse", "--show-toplevel"):          {output: []byte(root + "\n")},
		callKey("symbolic-ref", "--short", "-q", "HEAD"): {output: []byte("main\n")},
		callKey("rev-parse", "HEAD"):                     {output: []byte(strings.Repeat("a", 40) + "\n")},
		callKey("status", "--porcelain=v1", "-z"):        {output: []byte{}},
		callKey("remote", "get-url", "--all", "origin"):  {output: []byte("git@example.invalid:owner/repo.git\n")},
	}}
}

func callKey(args ...string) string {
	return strings.Join(args, "\x00")
}
