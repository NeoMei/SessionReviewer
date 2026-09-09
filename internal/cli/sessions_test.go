package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/sessionlaunch"
)

func TestRunSessionsDispatchesAuthenticatedOpenAndReturnsBoundJSON(t *testing.T) {
	old := openNativeSession
	defer func() { openNativeSession = old }()
	digest := "sha256:" + strings.Repeat("a", 64)
	openNativeSession = func(_ context.Context, request sessionlaunch.Request, _ sessionlaunch.Options) (sessionlaunch.Response, error) {
		if request.ProjectID != "project-p" || request.Provider != "codex" || request.SessionID != "session-1" || request.ExpectedGenerationID != "generation-1" || request.ExpectedSessionViewDigest != digest {
			t.Fatalf("request=%+v", request)
		}
		return sessionlaunch.Response{SchemaVersion: 1, ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID, GenerationID: request.ExpectedGenerationID, SessionViewDigest: digest, State: "launch_requested"}, nil
	}
	var stdout, stderr bytes.Buffer
	code := runSessions([]string{"open", "--project-id", "project-p", "--provider", "codex", "--session-id", "session-1", "--expected-generation-id", "generation-1", "--expected-session-view-digest", digest, "--data-dir", t.TempDir(), "--json"}, &stdout, &stderr)
	var got sessionlaunch.Response
	if code != 0 || stderr.Len() != 0 || json.Unmarshal(stdout.Bytes(), &got) != nil || got.State != "launch_requested" || got.SessionViewDigest != digest {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRunSessionsReturnsActionableBoundedLauncherError(t *testing.T) {
	old := openNativeSession
	defer func() { openNativeSession = old }()
	openNativeSession = func(context.Context, sessionlaunch.Request, sessionlaunch.Options) (sessionlaunch.Response, error) {
		return sessionlaunch.Response{}, &sessionlaunch.Error{Code: "session_launcher_unavailable", Message: "configure this provider launcher before opening the Session"}
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	var stdout, stderr bytes.Buffer
	code := runSessions([]string{"open", "--project-id", "project-p", "--provider", "codex", "--session-id", "session-1", "--expected-generation-id", "generation-1", "--expected-session-view-digest", digest, "--data-dir", t.TempDir(), "--json"}, &stdout, &stderr)
	if code != 1 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"code":"session_launcher_unavailable"`) || !strings.Contains(stdout.String(), "configure this provider") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRunSessionsRejectsPrivateWorkerThroughPublicParser(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runSessions([]string{"worker", "--launch-token", "bad"}, &stdout, &stderr); code == 0 || !strings.Contains(stdout.String(), "invalid_argument") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRunSessionWorkerPassesProductionReauthentication(t *testing.T) {
	oldConsume, oldExecute, oldRuntime := consumeNativeSession, executeNativeSession, sessionLaunchRuntime
	defer func() {
		consumeNativeSession, executeNativeSession, sessionLaunchRuntime = oldConsume, oldExecute, oldRuntime
	}()
	sessionLaunchRuntime = func() (string, string, error) { return t.TempDir(), filepath.Join(t.TempDir(), "self"), nil }
	consumeNativeSession = func(_ context.Context, token string, options sessionlaunch.WorkerOptions) (sessionlaunch.ExecPlan, error) {
		if token != strings.Repeat("a", 64) || options.Authenticate == nil {
			t.Fatalf("token=%q options=%+v", token, options)
		}
		return sessionlaunch.ExecPlan{}, nil
	}
	executeNativeSession = func(sessionlaunch.ExecPlan) error { return nil }
	var stderr bytes.Buffer
	if code := runSessionWorker(strings.Repeat("a", 64), &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}
