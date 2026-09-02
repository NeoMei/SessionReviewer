package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/contextupdate"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/scanjob"
)

var (
	scanIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

func runScan(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "start":
			return runScanStart(args[1:], stdout, stderr)
		case "status":
			return runScanStatus(args[1:], stdout, stderr)
		case "worker":
			return runScanWorker(args[1:], stdout, stderr)
		}
	}
	return runScanForeground(args, stdout, stderr)
}

func runScanForeground(args []string, stdout, stderr io.Writer) int {
	var projectID, sessionsRoot, dataDir string
	jsonOut := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOut = true
		case "--project-id":
			if i+1 < len(args) {
				projectID = args[i+1]
				i++
			}
		case "--sessions-root":
			if i+1 < len(args) {
				sessionsRoot = args[i+1]
				i++
			}
		case "--data-dir":
			if i+1 < len(args) {
				dataDir = args[i+1]
				i++
			}
		default:
			fmt.Fprintf(stderr, "unknown flag: %s\n", args[i])
			return 2
		}
	}

	dataDir = resolveDataDir(dataDir)
	projectID = resolveProjectID(dataDir, projectID)
	if projectID == "" {
		fmt.Fprintln(stderr, "unable to resolve project ID")
		return 2
	}

	res, err := contextupdate.Run(context.Background(), contextupdate.Options{
		ProjectID:    projectID,
		SessionsRoot: sessionsRoot,
		DataRoot:     dataDir,
	})
	if err != nil {
		if jsonOut {
			_ = json.NewEncoder(stdout).Encode(map[string]any{"error": err.Error(), "state": "failed"})
		} else {
			fmt.Fprintf(stderr, "scan error: %v\n", err)
		}
		return 1
	}
	if jsonOut {
		_ = json.NewEncoder(stdout).Encode(res)
	} else {
		fmt.Fprintf(stdout, "Scan completed: %s (generation %s, %d sessions)\n", res.State, res.GenerationID, res.IndexedSessions)
	}
	return 0
}

func runScanStart(args []string, stdout, stderr io.Writer) int {
	var projectID, sessionsRoot, dataDir string
	jsonOut := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOut = true
		case "--project-id":
			if i+1 < len(args) {
				projectID = args[i+1]
				i++
			}
		case "--sessions-root":
			if i+1 < len(args) {
				sessionsRoot = args[i+1]
				i++
			}
		case "--data-dir":
			if i+1 < len(args) {
				dataDir = args[i+1]
				i++
			}
		default:
			fmt.Fprintf(stderr, "unknown flag: %s\n", args[i])
			return 2
		}
	}

	dataDir = resolveDataDir(dataDir)
	projectID = resolveProjectID(dataDir, projectID)
	if projectID == "" {
		fmt.Fprintln(stderr, "unable to resolve project ID")
		return 2
	}

	status, err := scanjob.Start(context.Background(), scanjob.StartOptions{
		ProjectID:    projectID,
		SessionsRoot: sessionsRoot,
		DataRoot:     dataDir,
	})
	if err != nil {
		if errors.Is(err, scanjob.ErrJobAlreadyRunning) {
			if jsonOut {
				_ = json.NewEncoder(stdout).Encode(status)
			}
			return 0
		}
		fmt.Fprintf(stderr, "start scan job failed: %v\n", err)
		return 1
	}
	if jsonOut {
		_ = json.NewEncoder(stdout).Encode(status)
	} else {
		fmt.Fprintf(stdout, "Started scan job %s for %s (%s)\n", status.JobID, status.ProjectID, status.State)
	}
	return 0
}

func runScanStatus(args []string, stdout, stderr io.Writer) int {
	var projectID, dataDir string
	jsonOut := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOut = true
		case "--project-id":
			if i+1 < len(args) {
				projectID = args[i+1]
				i++
			}
		case "--data-dir":
			if i+1 < len(args) {
				dataDir = args[i+1]
				i++
			}
		default:
			fmt.Fprintf(stderr, "unknown flag: %s\n", args[i])
			return 2
		}
	}

	dataDir = resolveDataDir(dataDir)
	projectID = resolveProjectID(dataDir, projectID)
	if projectID == "" {
		fmt.Fprintln(stderr, "unable to resolve project ID")
		return 2
	}

	status, err := scanjob.Status(context.Background(), dataDir, projectID)
	if err != nil && !errors.Is(err, scanjob.ErrNoActiveJob) {
		fmt.Fprintf(stderr, "get status failed: %v\n", err)
		return 1
	}
	if jsonOut {
		_ = json.NewEncoder(stdout).Encode(status)
	} else {
		fmt.Fprintf(stdout, "Job: %s State: %s Phase: %s Generation: %s\n", status.JobID, status.State, status.Phase, status.GenerationID)
	}
	return 0
}

func runScanWorker(args []string, stdout, stderr io.Writer) int {
	var jobID, dataDir, projectID string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--job-id":
			if i+1 < len(args) {
				jobID = args[i+1]
				i++
			}
		case "--data-dir":
			if i+1 < len(args) {
				dataDir = args[i+1]
				i++
			}
		case "--project-id":
			if i+1 < len(args) {
				projectID = args[i+1]
				i++
			}
		}
	}
	if jobID == "" || dataDir == "" || projectID == "" {
		fmt.Fprintln(stderr, "worker requires --job-id, --data-dir, --project-id")
		return 2
	}
	if err := scanjob.RunWorker(context.Background(), dataDir, projectID, jobID); err != nil {
		fmt.Fprintf(stderr, "worker error: %v\n", err)
		return 1
	}
	return 0
}

func resolveDataDir(explicit string) string {
	if explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err == nil {
			return filepath.Clean(abs)
		}
		return filepath.Clean(explicit)
	}
	dir, err := platform.DataDir(platform.CurrentEnv())
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
