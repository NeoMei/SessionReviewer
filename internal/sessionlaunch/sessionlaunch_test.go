package sessionlaunch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/pathguard"
)

func TestFixedProviderRoutes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	for _, test := range []struct {
		provider string
		want     []string
	}{
		{"codex", []string{"resume", "01234567-abcd"}},
		{"claude", []string{"--resume", "01234567-abcd"}},
		{"opencode", []string{root, "--session", "01234567-abcd"}},
	} {
		got, err := ProviderArguments(test.provider, "01234567-abcd", root)
		if err != nil || strings.Join(got, "\x00") != strings.Join(test.want, "\x00") {
			t.Fatalf("provider=%s args=%q err=%v", test.provider, got, err)
		}
	}
	for _, invalid := range []struct{ provider, id string }{{"unknown", "safe"}, {"codex", ""}, {"codex", "x y"}, {"codex", "codex/session"}} {
		if _, err := ProviderArguments(invalid.provider, invalid.id, root); err == nil {
			t.Fatalf("accepted provider=%q id=%q", invalid.provider, invalid.id)
		}
	}
}

func TestLauncherConfigurationPersistsVerifiedExecutableAndRejectsReplacement(t *testing.T) {
	dataRoot, executable := t.TempDir(), filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(executable, []byte("verified-codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	configuration, err := MeasureConfiguration("codex", "fixture-1", executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveConfiguration(dataRoot, configuration); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadConfiguration(dataRoot, "codex"); err != nil || got != configuration {
		t.Fatalf("configuration=%+v err=%v", got, err)
	}
	if err := os.WriteFile(executable, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfiguration(dataRoot, "codex"); err == nil {
		t.Fatal("replacement executable accepted")
	}
}

func TestLauncherPrivateReadsRejectRedirectedRootAndOversizedEnvelope(t *testing.T) {
	dataRoot, executable := t.TempDir(), filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(executable, []byte("verified-codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	configuration, err := MeasureConfiguration("codex", "fixture-1", executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveConfiguration(dataRoot, configuration); err != nil {
		t.Fatal(err)
	}
	redirectedRoot := filepath.Join(t.TempDir(), "redirected-data")
	if err := os.Symlink(dataRoot, redirectedRoot); err != nil {
		t.Skipf("symlink setup is unavailable: %v", err)
	}
	if _, err := LoadConfiguration(redirectedRoot, "codex"); err == nil {
		t.Fatal("launcher configuration accepted a redirected data root")
	}

	runtimeRoot := t.TempDir()
	token := strings.Repeat("a", 64)
	if err := os.WriteFile(filepath.Join(runtimeRoot, token+".json"), make([]byte, 64<<10+1), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Consume(context.Background(), token, WorkerOptions{
		GOOS:           "darwin",
		RuntimeRoot:    runtimeRoot,
		SelfExecutable: executable,
		Authenticate:   func(context.Context, Request) (BoundSession, error) { return BoundSession{}, nil },
	})
	if err == nil {
		t.Fatal("launch worker accepted an oversized envelope")
	}
}

func TestVerifyRunsOnlyPinnedVersionRouteAndPersistsResult(t *testing.T) {
	dataRoot, executable := t.TempDir(), filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = --version ] || exit 19\nprintf 'claude fixture 1.2.3\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := Verify(context.Background(), dataRoot, "claude", executable)
	if err != nil || got.Provider != "claude" || got.Version != "claude fixture 1.2.3" || got.Executable == "" {
		t.Fatalf("configuration=%+v err=%v", got, err)
	}
	loaded, err := LoadConfiguration(dataRoot, "claude")
	if err != nil || loaded != got {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestOpenUsesOneTimeIdentityEnvelopeAndBootstrapContainsNoSessionMaterial(t *testing.T) {
	dataRoot, runtimeRoot := t.TempDir(), filepath.Join(t.TempDir(), "launch-runtime")
	providerExecutable := filepath.Join(t.TempDir(), "codex")
	selfExecutable := filepath.Join(t.TempDir(), "session-reviewer")
	for path, body := range map[string]string{providerExecutable: "provider", selfExecutable: "self"} {
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configuration, err := MeasureConfiguration("codex", "fixture", providerExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveConfiguration(dataRoot, configuration); err != nil {
		t.Fatal(err)
	}
	projectRoot := filepath.Join(t.TempDir(), "project-secret")
	if err := os.Mkdir(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	project, err := os.Open(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	projectIdentity, err := pathguard.PhysicalFileIdentity(project)
	_ = project.Close()
	if err != nil {
		t.Fatal(err)
	}
	request := Request{DataRoot: dataRoot, ProjectID: "project-secret", Provider: "codex", SessionID: "session-secret", ExpectedGenerationID: "generation-secret", ExpectedSessionViewDigest: "sha256:" + strings.Repeat("a", 64)}
	bound := BoundSession{ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID, GenerationID: request.ExpectedGenerationID, SessionViewDigest: request.ExpectedSessionViewDigest, SourceIdentity: "source-secret", SourceRecordDigest: "sha256:" + strings.Repeat("b", 64), ProjectRoot: projectRoot, ProjectIdentity: projectIdentity}
	var bootstrap string
	response, err := Open(context.Background(), request, Options{
		GOOS: "darwin", RuntimeRoot: runtimeRoot, SelfExecutable: selfExecutable,
		Now:            func() time.Time { return time.Date(2026, 9, 9, 1, 2, 3, 0, time.UTC) },
		Random:         strings.NewReader(strings.Repeat("x", 64)),
		Authenticate:   func(context.Context, Request) (BoundSession, error) { return bound, nil },
		LaunchTerminal: func(path string) error { body, readErr := os.ReadFile(path); bootstrap = string(body); return readErr },
	})
	if err != nil || response.State != "launch_requested" || response.Provider != "codex" || response.SessionID != request.SessionID {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	for _, secret := range []string{request.ProjectID, request.SessionID, request.ExpectedGenerationID, request.ExpectedSessionViewDigest, projectRoot, providerExecutable} {
		if strings.Contains(bootstrap, secret) {
			t.Fatalf("bootstrap leaked %q: %s", secret, bootstrap)
		}
	}
	plan, err := Consume(context.Background(), response.launchToken, WorkerOptions{GOOS: "darwin", RuntimeRoot: runtimeRoot, SelfExecutable: selfExecutable, Now: func() time.Time { return time.Date(2026, 9, 9, 1, 2, 4, 0, time.UTC) }, Authenticate: func(context.Context, Request) (BoundSession, error) { return bound, nil }})
	if err != nil || plan.Executable != configuration.Executable || strings.Join(plan.Arguments, " ") != "resume session-secret" || plan.WorkingDirectory != projectRoot {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if _, err := Consume(context.Background(), response.launchToken, WorkerOptions{GOOS: "darwin", RuntimeRoot: runtimeRoot, SelfExecutable: selfExecutable, Now: time.Now, Authenticate: func(context.Context, Request) (BoundSession, error) { return bound, nil }}); err == nil {
		t.Fatal("launch token replay accepted")
	}
}

func TestOpenReportsWindowsUnsupportedBeforeWritingLaunchState(t *testing.T) {
	root := t.TempDir()
	_, err := Open(context.Background(), Request{}, Options{GOOS: "windows", RuntimeRoot: root})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("err=%v", err)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v", entries, readErr)
	}
}
