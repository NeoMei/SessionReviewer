package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/neomei/SessionReviewer/internal/sessionlaunch"
)

const sessionsHelp = `Open one authenticated published Session in its native interactive CLI.

Usage:
  session-reviewer sessions launcher verify --provider codex|claude|opencode
    --executable ABS [--data-dir ABS] --json
  session-reviewer sessions open --project-id ID --provider ID --session-id ID
    --expected-generation-id ID --expected-session-view-digest DIGEST [--data-dir ABS] --json
`

var openNativeSession = sessionlaunch.Open
var verifyNativeLauncher = sessionlaunch.Verify
var consumeNativeSession = sessionlaunch.Consume
var executeNativeSession = sessionlaunch.Execute

func runSessions(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && isHelpToken(args[0]) {
		fmt.Fprint(stdout, sessionsHelp)
		return 0
	}
	if len(args) == 3 && args[0] == "worker" && args[1] == "--launch-token" && len(args[2]) == 64 {
		return runSessionWorker(args[2], stderr)
	}
	request, err := ParseSessionOpenContract(args)
	if err != nil {
		writeInspectError(stdout, err)
		return 2
	}
	dataRoot := resolveDataDir(request.DataDir)
	if dataRoot == "" {
		writeInspectError(stdout, contractError("SessionReviewer data directory is unavailable"))
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if request.Command == "launcher" {
		value, e := verifyNativeLauncher(ctx, dataRoot, request.Provider, request.Executable)
		if e != nil {
			writeSessionError(stdout, e)
			return 1
		}
		return writeSessionJSON(stdout, stderr, value)
	}
	runtimeRoot, self, e := sessionLaunchRuntime()
	if e != nil {
		writeSessionError(stdout, e)
		return 1
	}
	value, e := openNativeSession(ctx, sessionlaunch.Request{DataRoot: dataRoot, ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID, ExpectedGenerationID: request.ExpectedGenerationID, ExpectedSessionViewDigest: request.ExpectedSessionViewDigest}, sessionlaunch.Options{GOOS: runtime.GOOS, RuntimeRoot: runtimeRoot, SelfExecutable: self})
	if e != nil {
		writeSessionError(stdout, e)
		return 1
	}
	return writeSessionJSON(stdout, stderr, value)
}

func runSessionWorker(token string, stderr io.Writer) int {
	runtimeRoot, self, err := sessionLaunchRuntime()
	if err != nil {
		fmt.Fprintln(stderr, "session launch worker unavailable")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	plan, err := consumeNativeSession(ctx, token, sessionlaunch.WorkerOptions{GOOS: runtime.GOOS, RuntimeRoot: runtimeRoot, SelfExecutable: self, Authenticate: sessionlaunch.Authenticate})
	if err != nil {
		fmt.Fprintln(stderr, "session launch authorization expired or changed")
		return 1
	}
	if err := executeNativeSession(plan); err != nil {
		fmt.Fprintln(stderr, "native Session could not be opened")
		return 1
	}
	return 0
}

var sessionLaunchRuntime = defaultSessionLaunchRuntime

func defaultSessionLaunchRuntime() (string, string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", "", err
	}
	self, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(cacheRoot, "SessionReviewer", "session-launch-v1"), self, nil
}
func writeSessionJSON(stdout, stderr io.Writer, value any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintln(stderr, "session launch output failed")
		return 1
	}
	return 0
}

func writeSessionError(output io.Writer, err error) {
	code, message := "session_launch_failed", "native Session launch failed"
	var launchErr *sessionlaunch.Error
	if errors.As(err, &launchErr) {
		code, message = launchErr.Code, launchErr.Message
	}
	_ = json.NewEncoder(output).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}
