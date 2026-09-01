package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/scan"
)

func TestScanCoreAcceptsExactPrivateFlagsAndEmitsOneSafeZeroTokenObject(t *testing.T) {
	var request scanCoreRequest
	called := 0
	execute := func(_ context.Context, value scanCoreRequest) (scan.Result, error) {
		called++
		request = value
		return scan.Result{
			SchemaVersion: 1, ProjectID: "project-a", GenerationID: "scan-1234",
			State: scan.CompletedWithIssues, SourceSessions: 154, IndexedSessions: 151,
			TerminalSessions: 154, IssueSessions: 3, ProjectViewDigest: "sha256:" + strings.Repeat("a", 64),
			ReviewRunTokens: 0, Prepared: true,
		}, nil
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runScanCore(context.Background(), []string{
		"--project-id", "project-a", "--sessions-root", "/private/session-path-canary",
		"--data-dir", "/private/data-path-canary", "--json",
	}, &stdout, &stderr, execute)
	if code != 0 || called != 1 {
		t.Fatalf("code=%d called=%d stderr=%q", code, called, stderr.String())
	}
	if request.ProjectID != "project-a" || request.SessionsRoot != "/private/session-path-canary" || request.DataRoot != "/private/data-path-canary" {
		t.Fatalf("executor received wrong request: %+v", request)
	}
	if bytes.Count(stdout.Bytes(), []byte("\n")) != 1 || stderr.Len() != 0 {
		t.Fatalf("output is not one JSON line: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "session-path-canary") || strings.Contains(stdout.String(), "data-path-canary") {
		t.Fatalf("private path entered JSON output: %s", stdout.String())
	}
	var result scan.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode scan result: %v", err)
	}
	if result.ReviewRunTokens != 0 || !result.Prepared || result.TerminalSessions != 154 {
		t.Fatalf("unexpected scan result: %+v", result)
	}
}

func TestScanCoreRejectsAnythingOutsideExactPrivateFlagSetWithoutExecution(t *testing.T) {
	tests := [][]string{
		{"--project-id", "project-a", "--sessions-root", "/sessions", "--data-dir", "/data"},
		{"--project-id", "project-a", "--project-id", "project-b", "--sessions-root", "/sessions", "--data-dir", "/data", "--json"},
		{"--project-id", "project-a", "--sessions-root", "/sessions", "--data-dir", "/data", "--json", "--extra"},
		{"--project-id=project-a", "--sessions-root", "/sessions", "--data-dir", "/data", "--json"},
		{"--project-id", "project-a", "--sessions-root", "/sessions", "--data-dir", "/data", "--json", "positional"},
	}
	for _, args := range tests {
		called := false
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := runScanCore(context.Background(), args, &stdout, &stderr, func(context.Context, scanCoreRequest) (scan.Result, error) {
			called = true
			return scan.Result{}, nil
		})
		if code != 2 || called {
			t.Fatalf("args=%v code=%d called=%v", args, code, called)
		}
		assertSafeFailedScanJSON(t, stdout.Bytes(), stderr.Bytes())
	}
}

func TestScanCoreRedactsExecutionErrorsAndRejectsInvalidResultContracts(t *testing.T) {
	tests := []scanCoreExecutor{
		func(context.Context, scanCoreRequest) (scan.Result, error) {
			return scan.Result{}, errors.New("/private/error-path-canary secret-session-text-canary")
		},
		func(context.Context, scanCoreRequest) (scan.Result, error) {
			return scan.Result{SchemaVersion: 1, ProjectID: "project-a", State: scan.Completed, ReviewRunTokens: 1, Prepared: true}, nil
		},
	}
	for _, execute := range tests {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := runScanCore(context.Background(), []string{"--project-id", "project-a", "--sessions-root", "/sessions", "--data-dir", "/data", "--json"}, &stdout, &stderr, execute)
		if code != 1 {
			t.Fatalf("invalid execution code=%d output=%q", code, stdout.String())
		}
		assertSafeFailedScanJSON(t, stdout.Bytes(), stderr.Bytes())
	}
}

func TestScanCoreIsNotWiredIntoPublicRootDispatch(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Run([]string{"scan", "--json"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("private scan unexpectedly entered public dispatch: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func assertSafeFailedScanJSON(t *testing.T, stdout, stderr []byte) {
	t.Helper()
	if len(stderr) != 0 || bytes.Count(stdout, []byte("\n")) != 1 || len(stdout) > 1024 {
		t.Fatalf("unsafe failure output: stdout=%q stderr=%q", stdout, stderr)
	}
	if bytes.Contains(stdout, []byte("/private/")) || bytes.Contains(stdout, []byte("canary")) || bytes.Contains(stdout, []byte("secret-session-text")) {
		t.Fatalf("failure output leaked input or error text: %q", stdout)
	}
	var result scan.Result
	if err := json.Unmarshal(stdout, &result); err != nil || result.State != scan.Failed || result.ReviewRunTokens != 0 || result.Prepared {
		t.Fatalf("invalid failed result: %+v err=%v", result, err)
	}
}
