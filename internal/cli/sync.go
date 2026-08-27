package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/platform"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

const syncHelp = `Synchronize editable Session Review Markdown with the configured Obsidian vault.

Usage:
  session-reviewer sync [--dry-run] [--cwd PROJECT | --project-id ID] [--data-dir DATA]
  session-reviewer sync status [--json] [--cwd PROJECT | --project-id ID] [--data-dir DATA]
  session-reviewer sync resolve --conflict ID --action accept_project|accept_obsidian [--cwd PROJECT | --project-id ID] [--data-dir DATA]
  session-reviewer sync resolve --conflict ID --action manual_merge --file PATH [--cwd PROJECT | --project-id ID] [--data-dir DATA]
  session-reviewer sync repair-machine-ledger [--cwd PROJECT | --project-id ID] [--data-dir DATA]

Options:
  --dry-run       Print the deterministic plan without changing files or state
  --json          Emit sync status as JSON
  --cwd PATH      Project root; defaults to the current directory
  --project-id ID Select one configured project by stable ID; mutually exclusive with --cwd
  --data-dir PATH Machine-local SessionReviewer data directory

Examples:
  session-reviewer sync --dry-run
  session-reviewer sync
  session-reviewer sync status --json
`

func runSync(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && isHelpToken(args[0]) {
		fmt.Fprint(stdout, syncHelp)
		return 0
	}
	mode := "sync"
	if len(args) > 0 && (args[0] == "status" || args[0] == "resolve" || args[0] == "repair-machine-ledger") {
		mode = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("sync "+mode, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, syncHelp) }
	dryRun := flags.Bool("dry-run", false, "plan without writes")
	jsonOutput := flags.Bool("json", false, "emit JSON status")
	cwd := flags.String("cwd", "", "project root")
	projectID := flags.String("project-id", "", "configured stable project ID")
	dataDir := flags.String("data-dir", "", "machine data directory")
	conflictID := flags.String("conflict", "", "conflict identity")
	action := flags.String("action", "", "resolution action")
	manualFile := flags.String("file", "", "manual merge file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "sync does not accept positional arguments")
		return 2
	}
	if *cwd != "" && *projectID != "" {
		fmt.Fprintln(stderr, "--cwd and --project-id are mutually exclusive")
		return 2
	}
	if mode != "sync" && *dryRun {
		fmt.Fprintln(stderr, "--dry-run is valid only for sync")
		return 2
	}
	if mode != "status" && *jsonOutput {
		fmt.Fprintln(stderr, "--json is valid only for sync status")
		return 2
	}
	if mode != "resolve" && (*conflictID != "" || *action != "" || *manualFile != "") {
		fmt.Fprintln(stderr, "resolution flags are valid only for sync resolve")
		return 2
	}
	var resolution syncengine.Resolution
	if mode == "resolve" {
		resolution = syncengine.Resolution{ConflictID: *conflictID, Action: syncengine.ResolutionAction(*action), ManualFile: *manualFile}
		if *conflictID == "" || (*action != string(syncengine.AcceptProject) && *action != string(syncengine.AcceptObsidian) && *action != string(syncengine.ManualMerge)) {
			fmt.Fprintln(stderr, "sync resolve requires --conflict and an exact --action")
			return 2
		}
		if (*action == string(syncengine.ManualMerge)) != (*manualFile != "") {
			fmt.Fprintln(stderr, "--file is required only for manual_merge")
			return 2
		}
	}

	root, mapping, projectData, err := resolveSyncMapping(*cwd, *projectID, *dataDir)
	if err != nil {
		return writeDiagnostic(stderr, "sync", err)
	}
	engine, err := syncengine.NewEngine(syncengine.Options{
		ProjectRoot: root.Path, VaultRoot: mapping.VaultRoot, VaultReviewPath: mapping.VaultReviewPath,
		ProjectRootExpected: root.Expected,
		DataRoot:            projectData, ProjectID: mapping.ID, GOOS: runtime.GOOS, VaultCaseMode: mapping.VaultCaseMode,
		Retry: syncengine.DefaultRetryPolicy(), Now: time.Now,
	})
	if err != nil {
		return writeDiagnostic(stderr, "sync", err)
	}
	defer engine.Close()

	switch mode {
	case "status":
		status, err := engine.Status(context.Background())
		if err != nil {
			return writeDiagnostic(stderr, "sync", err)
		}
		if *jsonOutput {
			encoder := json.NewEncoder(stdout)
			encoder.SetEscapeHTML(false)
			if err := encoder.Encode(status); err != nil {
				return writeDiagnostic(stderr, "sync", err)
			}
		} else {
			fmt.Fprintln(stdout, status.String())
		}
		return 0
	case "resolve":
		report, err := engine.Resolve(context.Background(), resolution)
		if err != nil {
			return writeDiagnostic(stderr, "sync", err)
		}
		writeSyncReport(stdout, report)
		if writeSyncPartialFailure(stderr, report) {
			return 1
		}
		return 0
	case "repair-machine-ledger":
		report, err := engine.RepairMachineLedger(context.Background())
		if err != nil {
			return writeDiagnostic(stderr, "sync", err)
		}
		fmt.Fprintf(stdout, "machine=%s files=1\n", report.State)
		return 0
	default:
		report, err := engine.Reconcile(context.Background(), syncengine.ReconcileRequest{DryRun: *dryRun, Trigger: syncengine.TriggerCLI})
		if err != nil {
			if shouldWriteFailedSyncReport(report) {
				writeSyncReport(stdout, report)
			}
			return writeDiagnostic(stderr, "sync", err)
		}
		writeSyncReport(stdout, report)
		if writeSyncPartialFailure(stderr, report) {
			return 1
		}
		return 0
	}
}

func shouldWriteFailedSyncReport(report syncengine.Report) bool {
	return len(report.Operations) != 0 || len(report.Conflicts) != 0 || len(report.Issues) != 0 || len(report.Errors) != 0 ||
		report.QueueDepth != 0 || len(report.Derived.Operations) != 0 || report.Derived.State == syncengine.DerivedFailed ||
		report.Migration.Required || len(report.Migration.Creates) != 0 || len(report.Migration.Archives) != 0 ||
		len(report.Machine.Operations) != 0 || report.Machine.State == syncengine.MachineBlocked
}

func writeSyncPartialFailure(stderr io.Writer, report syncengine.Report) bool {
	if len(report.Errors) == 0 && len(report.Issues) == 0 {
		return false
	}
	fmt.Fprintln(stderr, "E_SYNC_PARTIAL: synchronization completed with blocked entities")
	fmt.Fprintln(stderr, "recovery: inspect the entity_error lines, repair the named document, then run sync again")
	return true
}

func resolveSyncMapping(cwd, projectID, dataDir string) (resolvedProjectRoot, config.ProjectMapping, string, error) {
	env := currentEnv()
	var err error
	if dataDir == "" {
		dataDir, err = platform.DataDir(env)
		if err != nil {
			return resolvedProjectRoot{}, config.ProjectMapping{}, "", err
		}
	}
	absoluteData, err := filepath.Abs(dataDir)
	if err != nil {
		return resolvedProjectRoot{}, config.ProjectMapping{}, "", err
	}
	cfg, err := config.Load(filepath.Join(absoluteData, "config.toml"))
	if err != nil {
		return resolvedProjectRoot{}, config.ProjectMapping{}, "", err
	}
	var root resolvedProjectRoot
	var mapping config.ProjectMapping
	matches := 0
	if projectID != "" {
		candidate, found := cfg.ProjectByID(projectID)
		if !found {
			return resolvedProjectRoot{}, config.ProjectMapping{}, "", fmt.Errorf("configured project ID was not found")
		}
		root, err = resolveExplicitProjectRoot(candidate.Root)
		if err != nil {
			return resolvedProjectRoot{}, config.ProjectMapping{}, "", fmt.Errorf("configured project root is unavailable or unsafe: %w", err)
		}
		mapping = candidate
		matches = 1
	} else {
		root, err = resolveProjectRoot(cwd)
		if err != nil {
			return resolvedProjectRoot{}, config.ProjectMapping{}, "", err
		}
		for _, candidate := range cfg.Projects {
			info, statErr := os.Stat(candidate.Root)
			if statErr == nil && os.SameFile(root.Expected, info) {
				mapping = candidate
				matches++
			}
		}
	}
	if matches != 1 || mapping.VaultRoot == "" || mapping.VaultReviewPath == "" || mapping.VaultCaseMode == "" {
		return resolvedProjectRoot{}, config.ProjectMapping{}, "", fmt.Errorf("project has no complete Obsidian sync mapping")
	}
	return root, mapping, filepath.Join(absoluteData, "projects", mapping.ID), nil
}

func writeSyncReport(output io.Writer, report syncengine.Report) {
	operations := append([]syncengine.Operation(nil), report.Operations...)
	sort.Slice(operations, func(i, j int) bool {
		if operations[i].EntityID != operations[j].EntityID {
			return operations[i].EntityID < operations[j].EntityID
		}
		return operations[i].Kind < operations[j].Kind
	})
	for _, operation := range operations {
		fmt.Fprintf(output, "%s %s %s\n", operation.Kind, operation.EntityID, operation.RelativePath)
	}
	derivedOperations := append([]syncengine.Operation(nil), report.Derived.Operations...)
	sort.Slice(derivedOperations, func(i, j int) bool {
		if derivedOperations[i].RelativePath != derivedOperations[j].RelativePath {
			return derivedOperations[i].RelativePath < derivedOperations[j].RelativePath
		}
		if derivedOperations[i].Target != derivedOperations[j].Target {
			return derivedOperations[i].Target < derivedOperations[j].Target
		}
		return derivedOperations[i].Kind < derivedOperations[j].Kind
	})
	for _, operation := range derivedOperations {
		fmt.Fprintf(output, "derived_operation %s %s\n", operation.Kind, operation.RelativePath)
	}
	errors := append([]syncengine.EntityError(nil), report.Errors...)
	sort.Slice(errors, func(i, j int) bool {
		if errors[i].EntityID != errors[j].EntityID {
			return errors[i].EntityID < errors[j].EntityID
		}
		return errors[i].Code < errors[j].Code
	})
	for _, entityError := range errors {
		fmt.Fprintf(output, "entity_error %s %s\n", entityError.EntityID, entityError.Code)
	}
	issueKinds := make([]string, 0, len(report.Issues))
	for _, issue := range report.Issues {
		issueKinds = append(issueKinds, string(issue.Kind))
	}
	sort.Strings(issueKinds)
	for _, kind := range issueKinds {
		fmt.Fprintf(output, "scan_issue %s\n", kind)
	}
	creates := append([]string(nil), report.Migration.Creates...)
	archives := append([]string(nil), report.Migration.Archives...)
	sort.Strings(creates)
	sort.Strings(archives)
	for _, relative := range creates {
		fmt.Fprintf(output, "migration_create %s\n", filepath.ToSlash(relative))
	}
	for _, relative := range archives {
		fmt.Fprintf(output, "migration_archive %s\n", filepath.ToSlash(relative))
	}
	fmt.Fprintf(output, "derived=%s files=%d\n", report.Derived.State, report.Derived.Files)
	migration := "current"
	if report.Migration.Required && report.Migration.DryRun {
		migration = "required"
	}
	fmt.Fprintf(output, "migration=%s\n", migration)
	fmt.Fprintf(output, "operations: %d\nconflicts: %d\nissues: %d\nerrors: %d\nqueue_depth: %d\n", len(operations), len(report.Conflicts), len(report.Issues), len(report.Errors), report.QueueDepth)
	fmt.Fprintf(output, "machine=%s files=1\n", report.Machine.State)
}
