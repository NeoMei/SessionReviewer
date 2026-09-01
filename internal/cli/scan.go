package cli

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"

	"github.com/neomei/SessionReviewer/internal/scan"
)

var (
	scanCoreIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	scanCoreDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type scanCoreRequest struct {
	ProjectID    string
	SessionsRoot string
	DataRoot     string
}

type scanCoreExecutor func(context.Context, scanCoreRequest) (scan.Result, error)

// runScanCore is the private Gate-A JSON boundary. Gate B owns public command
// composition and dispatch; callers must inject the authenticated adapter,
// catalog, store, project binding, and trusted ProjectProbe dependency.
func runScanCore(ctx context.Context, args []string, stdout, stderr io.Writer, execute scanCoreExecutor) int {
	_ = stderr
	request, ok := parseScanCoreArgs(args)
	if !ok || execute == nil {
		writeScanCoreResult(stdout, failedScanCoreResult())
		return 2
	}
	result, err := execute(ctx, request)
	if err != nil || !validScanCoreResult(request, result) {
		writeScanCoreResult(stdout, failedScanCoreResult())
		return 1
	}
	if err := writeScanCoreResult(stdout, result); err != nil {
		return 1
	}
	return 0
}

func parseScanCoreArgs(args []string) (scanCoreRequest, bool) {
	var result scanCoreRequest
	seen := map[string]bool{}
	jsonRequested := false
	for index := 0; index < len(args); index++ {
		name := args[index]
		if name == "--json" {
			if jsonRequested {
				return scanCoreRequest{}, false
			}
			jsonRequested = true
			continue
		}
		if name != "--project-id" && name != "--sessions-root" && name != "--data-dir" {
			return scanCoreRequest{}, false
		}
		if seen[name] || index+1 >= len(args) || args[index+1] == "" || len(args[index+1]) >= 2 && args[index+1][:2] == "--" {
			return scanCoreRequest{}, false
		}
		seen[name] = true
		index++
		switch name {
		case "--project-id":
			result.ProjectID = args[index]
		case "--sessions-root":
			result.SessionsRoot = args[index]
		case "--data-dir":
			result.DataRoot = args[index]
		}
	}
	if !jsonRequested || len(seen) != 3 || !scanCoreIDPattern.MatchString(result.ProjectID) {
		return scanCoreRequest{}, false
	}
	for _, root := range []string{result.SessionsRoot, result.DataRoot} {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return scanCoreRequest{}, false
		}
	}
	return result, true
}

func validScanCoreResult(request scanCoreRequest, result scan.Result) bool {
	if result.SchemaVersion != 1 || result.ProjectID != request.ProjectID || !scanCoreIDPattern.MatchString(result.ProjectID) ||
		!scanCoreIDPattern.MatchString(result.GenerationID) || !scanCoreDigestPattern.MatchString(result.ProjectViewDigest) ||
		result.ReviewRunTokens != 0 || !result.Prepared ||
		(result.State != scan.Completed && result.State != scan.CompletedWithIssues) {
		return false
	}
	if result.SourceSessions < 0 || result.IndexedSessions < 0 || result.TerminalSessions < 0 || result.IssueSessions < 0 ||
		result.SourceSessions != result.TerminalSessions || result.IndexedSessions > result.TerminalSessions || result.IssueSessions > result.TerminalSessions {
		return false
	}
	return (result.State == scan.Completed) == (result.IssueSessions == 0)
}

func failedScanCoreResult() scan.Result {
	return scan.Result{SchemaVersion: 1, State: scan.Failed, ReviewRunTokens: 0, Prepared: false}
}

func writeScanCoreResult(destination io.Writer, result scan.Result) error {
	encoder := json.NewEncoder(destination)
	encoder.SetEscapeHTML(true)
	return encoder.Encode(result)
}
