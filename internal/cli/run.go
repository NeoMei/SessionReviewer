package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/neomei/SessionReviewer/internal/buildinfo"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

var projectRootResolvedHook func(command, path string) error

func runProjectRootResolvedHook(command, path string) error {
	if projectRootResolvedHook == nil {
		return nil
	}
	return projectRootResolvedHook(command, path)
}

const rootHelp = `SessionReviewer prepares bounded evidence for durable session review.

Usage: session-reviewer <command> [options]

Commands:
  init                  Preview or write project and Obsidian initialization
  prepare review        Prepare review evidence, optionally from the start
  prepare checkpoint    Prepare incremental checkpoint evidence
  apply                 Validate and apply a Skill proposal
  resume                Render accepted ledger recovery state
  history               Render accepted cross-session history
  sync                  Synchronize editable Markdown with Obsidian
  review                Control durable Agent review jobs
  scan                  Execute or monitor zero-token project scans
  version               Print the version

Options:
  init: --project --vault [--data-dir] [--write]
  prepare: --output [--sessions-root] [--cwd] [--session]
           [--current-session-id] [--data-dir] [--from-start for review]
  apply: --proposal --evidence [--project] [--data-dir]
  resume/history: --ledger-only [--project]
  sync: [--dry-run] [--cwd | --project-id ID] [--data-dir]
        status [--json] [--cwd | --project-id ID] [--data-dir]
        repair-machine-ledger [--cwd | --project-id ID] [--data-dir]
        resolve --conflict ID --action <accept_project|accept_obsidian|manual_merge>
          [--file MERGED.md] [--cwd | --project-id ID] [--data-dir]
  review: agent verify|start|status|cancel|retry (JSON only)
  scan: [--project-id ID] [--sessions-root PATH] [--data-dir PATH] [--json]
        start [--project-id ID] [--data-dir PATH] [--json]
        status [--project-id ID] [--data-dir PATH] [--json]

Apply validates a Skill proposal against its exact bounded evidence packet.
Ledger-only resume and history do not process pending sessions.

Examples:
  session-reviewer init --project /path/to/project --vault /path/to/vault
  session-reviewer init --project /path/to/project --vault /path/to/vault --write
  session-reviewer prepare review --output /path/to/project/review.json
  session-reviewer prepare checkpoint --session SESSION_ID --sessions-root /path/to/sessions --output /path/to/project/checkpoint.json
  session-reviewer apply --proposal proposal.json --evidence evidence.json
  session-reviewer resume --ledger-only --project /path/to/project
  session-reviewer history --ledger-only --project /path/to/project
  session-reviewer sync --dry-run
  session-reviewer sync status --json --project-id PROJECT_ID
  session-reviewer sync repair-machine-ledger --project-id PROJECT_ID

Run session-reviewer <command> --help for details.
`

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, rootHelp)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "root help does not accept arguments")
			return 2
		}
		fmt.Fprint(stdout, rootHelp)
		return 0
	case "version":
		if len(args) == 1 {
			fmt.Fprintln(stdout, buildinfo.Current().Version)
			return 0
		}
		if len(args) == 2 && args[1] == "--json" {
			if err := json.NewEncoder(stdout).Encode(buildinfo.Current()); err != nil {
				return writeDiagnostic(stderr, "version", err)
			}
			return 0
		}
		fmt.Fprintln(stderr, "version accepts only --json")
		return 2
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "prepare":
		return runPrepare(args[1:], stdout, stderr)
	case "apply":
		return runApply(args[1:], stdout, stderr)
	case "resume":
		return runRecovery("resume", args[1:], stdout, stderr)
	case "history":
		return runRecovery("history", args[1:], stdout, stderr)
	case "sync":
		return runSync(args[1:], stdout, stderr)
	case "review":
		return runReview(args[1:], stdout, stderr)
	case "scan":
		return runScan(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, rootHelp)
		return 2
	}
}

func isHelpToken(arg string) bool {
	return arg == "help" || arg == "-h" || arg == "--help"
}

type resolvedProjectRoot struct {
	Path     string
	Expected os.FileInfo
}

func resolveProjectRoot(value string) (resolvedProjectRoot, error) {
	if value == "" {
		return resolveImplicitProjectRoot()
	}
	return resolveExplicitProjectRoot(value)
}

func resolveImplicitProjectRoot() (resolvedProjectRoot, error) {
	workingDirectoryInfo, err := os.Stat(".")
	if err != nil {
		return resolvedProjectRoot{}, fmt.Errorf("inspect current directory: %w", err)
	}
	logicalPath, err := os.Getwd()
	if err != nil {
		return resolvedProjectRoot{}, fmt.Errorf("read current directory: %w", err)
	}
	absolutePath, err := filepath.Abs(logicalPath)
	if err != nil {
		return resolvedProjectRoot{}, fmt.Errorf("make current directory absolute: %w", err)
	}
	physicalPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return resolvedProjectRoot{}, fmt.Errorf("resolve current directory: %w", err)
	}
	resolved, err := resolveExplicitProjectRoot(physicalPath)
	if err != nil {
		return resolvedProjectRoot{}, fmt.Errorf("open resolved current directory: %w", err)
	}
	if !os.SameFile(workingDirectoryInfo, resolved.Expected) {
		return resolvedProjectRoot{}, errors.New("resolved current directory identity changed")
	}
	return resolved, nil
}

func resolveExplicitProjectRoot(value string) (_ resolvedProjectRoot, retErr error) {
	directory, err := pathguard.Open(value)
	if err != nil {
		return resolvedProjectRoot{}, err
	}
	defer func() { retErr = errors.Join(retErr, directory.Close()) }()
	return resolvedProjectRoot{Path: directory.Path, Expected: directory.Info()}, nil
}
