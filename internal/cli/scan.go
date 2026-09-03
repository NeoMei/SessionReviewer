package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/contextupdate"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/scanjob"
)

const scanHelp = `Execute or monitor zero-token project scans.

Usage:
  session-reviewer scan [--project-id ID] [--sessions-root PATH] [--data-dir PATH] [--json]
  session-reviewer scan start [--project-id ID] [--sessions-root PATH] [--data-dir PATH] [--json]
  session-reviewer scan status [--project-id ID] [--data-dir PATH] [--json]
`

type exactScanFlags struct {
	values map[string]string
	json   bool
}

func runScan(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && isHelpToken(args[0]) {
		fmt.Fprint(stdout, scanHelp)
		return 0
	}
	if len(args) > 0 {
		switch args[0] {
		case "start":
			if len(args) == 2 && isHelpToken(args[1]) {
				fmt.Fprint(stdout, scanHelp)
				return 0
			}
			return runScanStart(args[1:], stdout, stderr)
		case "status":
			if len(args) == 2 && isHelpToken(args[1]) {
				fmt.Fprint(stdout, scanHelp)
				return 0
			}
			return runScanStatus(args[1:], stdout, stderr)
		case "worker":
			return runScanWorker(args[1:], stdout, stderr)
		}
		if !strings.HasPrefix(args[0], "--") {
			fmt.Fprintf(stderr, "unknown scan command %q\n", args[0])
			return 2
		}
	}
	return runScanForeground(args, stdout, stderr)
}

func runScanForeground(args []string, stdout, stderr io.Writer) int {
	flags, ok := parseScanFlags(args, []string{"project-id", "sessions-root", "data-dir"}, true)
	if !ok {
		fmt.Fprintln(stderr, "scan has invalid or incomplete flags")
		return 2
	}

	dataDir := resolveDataDir(flags.values["data-dir"])
	projectID := resolveProjectID(dataDir, flags.values["project-id"])
	if !safeReviewID(projectID) {
		fmt.Fprintln(stderr, "unable to resolve project ID")
		return 2
	}
	sessionsRoot, err := resolveScanSessionsRoot(flags.values["sessions-root"])
	if err != nil {
		fmt.Fprintf(stderr, "unable to resolve sessions root: %v\n", err)
		return 2
	}

	res, err := contextupdate.Run(context.Background(), contextupdate.Options{
		ProjectID:    projectID,
		SessionsRoot: sessionsRoot,
		DataRoot:     dataDir,
	})
	if err != nil {
		if flags.json {
			_ = json.NewEncoder(stdout).Encode(map[string]any{"error": err.Error(), "state": "failed"})
		} else {
			fmt.Fprintf(stderr, "scan error: %v\n", err)
		}
		return 1
	}
	if flags.json {
		_ = json.NewEncoder(stdout).Encode(res)
	} else {
		fmt.Fprintf(stdout, "Scan completed: %s (generation %s, %d sessions)\n", res.State, res.GenerationID, res.IndexedSessions)
	}
	return 0
}

func runScanStart(args []string, stdout, stderr io.Writer) int {
	flags, ok := parseScanFlags(args, []string{"project-id", "sessions-root", "data-dir"}, true)
	if !ok {
		fmt.Fprintln(stderr, "scan start has invalid or incomplete flags")
		return 2
	}

	dataDir := resolveDataDir(flags.values["data-dir"])
	projectID := resolveProjectID(dataDir, flags.values["project-id"])
	if !safeReviewID(projectID) {
		fmt.Fprintln(stderr, "unable to resolve project ID")
		return 2
	}
	sessionsRoot, err := resolveScanSessionsRoot(flags.values["sessions-root"])
	if err != nil {
		fmt.Fprintf(stderr, "unable to resolve sessions root: %v\n", err)
		return 2
	}

	status, err := scanjob.Start(context.Background(), scanjob.StartOptions{
		ProjectID:    projectID,
		SessionsRoot: sessionsRoot,
		DataRoot:     dataDir,
	})
	if err != nil {
		if errors.Is(err, scanjob.ErrJobAlreadyRunning) {
			if flags.json {
				_ = json.NewEncoder(stdout).Encode(status)
			}
			return 0
		}
		fmt.Fprintf(stderr, "start scan job failed: %v\n", err)
		return 1
	}
	if flags.json {
		_ = json.NewEncoder(stdout).Encode(status)
	} else {
		fmt.Fprintf(stdout, "Started scan job %s for %s (%s)\n", status.JobID, status.ProjectID, status.State)
	}
	return 0
}

func runScanStatus(args []string, stdout, stderr io.Writer) int {
	flags, ok := parseScanFlags(args, []string{"project-id", "data-dir"}, true)
	if !ok {
		fmt.Fprintln(stderr, "scan status has invalid or incomplete flags")
		return 2
	}

	dataDir := resolveDataDir(flags.values["data-dir"])
	projectID := resolveProjectID(dataDir, flags.values["project-id"])
	if !safeReviewID(projectID) {
		fmt.Fprintln(stderr, "unable to resolve project ID")
		return 2
	}

	status, err := scanjob.Status(context.Background(), dataDir, projectID)
	if err != nil && !errors.Is(err, scanjob.ErrNoActiveJob) {
		fmt.Fprintf(stderr, "get status failed: %v\n", err)
		return 1
	}
	if flags.json {
		_ = json.NewEncoder(stdout).Encode(status)
	} else {
		fmt.Fprintf(stdout, "Job: %s State: %s Phase: %s Generation: %s\n", status.JobID, status.State, status.Phase, status.GenerationID)
	}
	return 0
}

func runScanWorker(args []string, stdout, stderr io.Writer) int {
	flags, ok := parseScanFlags(args, []string{"job-id", "data-dir", "project-id"}, false)
	jobID := flags.values["job-id"]
	dataDir := flags.values["data-dir"]
	projectID := flags.values["project-id"]
	if !ok || !safeReviewID(jobID) || !safeReviewID(projectID) || !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) != dataDir {
		fmt.Fprintln(stderr, "worker requires --job-id, --data-dir, --project-id")
		return 2
	}
	if err := scanjob.RunWorker(context.Background(), dataDir, projectID, jobID); err != nil {
		fmt.Fprintf(stderr, "worker error: %v\n", err)
		return 1
	}
	return 0
}

func parseScanFlags(args, valueNames []string, allowJSON bool) (exactScanFlags, bool) {
	allowed := make(map[string]bool, len(valueNames))
	for _, name := range valueNames {
		allowed[name] = true
	}
	parsed := exactScanFlags{values: make(map[string]string)}
	seen := make(map[string]bool)
	for index := 0; index < len(args); index++ {
		token := args[index]
		if !strings.HasPrefix(token, "--") || token == "--" || strings.Contains(token, "=") {
			return exactScanFlags{}, false
		}
		name := strings.TrimPrefix(token, "--")
		if seen[name] {
			return exactScanFlags{}, false
		}
		seen[name] = true
		if name == "json" {
			if !allowJSON {
				return exactScanFlags{}, false
			}
			parsed.json = true
			continue
		}
		if !allowed[name] {
			return exactScanFlags{}, false
		}
		index++
		if index >= len(args) || args[index] == "" || strings.HasPrefix(args[index], "--") {
			return exactScanFlags{}, false
		}
		parsed.values[name] = args[index]
	}
	return parsed, true
}

func resolveScanSessionsRoot(explicit string) (string, error) {
	resolved, err := platform.ResolveSessionsRoot(explicit, currentEnv())
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(resolved.Path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func resolveDataDir(explicit string) string {
	if explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err == nil {
			return filepath.Clean(abs)
		}
		return filepath.Clean(explicit)
	}
	dir, err := platform.DataDir(currentEnv())
	if err != nil {
		return ""
	}
	return dir
}

func resolveProjectID(dataDir, explicit string) string {
	if explicit != "" {
		return strings.TrimSpace(explicit)
	}
	cfg, err := config.Load(filepath.Join(dataDir, "config.toml"))
	if err != nil {
		return ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for _, p := range cfg.Projects {
		if p.Root == cwd {
			return p.ID
		}
	}
	if len(cfg.Projects) == 1 {
		return cfg.Projects[0].ID
	}
	return ""
}
