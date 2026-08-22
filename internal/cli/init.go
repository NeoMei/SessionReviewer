package cli

import (
	"flag"
	"fmt"
	"io"
	"runtime"

	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/project"
)

const initHelp = `Preview project and Obsidian initialization before writing it.

Usage:
  session-reviewer init --project PATH --vault PATH [options]

Options:
  --project PATH   Existing project root (required)
  --vault PATH     Existing Obsidian vault root (required)
  --data-dir PATH  Machine-local SessionReviewer data directory
  --write          Perform the exact writes shown by a fresh preview

Examples:
  session-reviewer init --project /path/to/project --vault /path/to/vault
  session-reviewer init --project /path/to/project --vault /path/to/vault --data-dir /path/to/data --write
`

func runInit(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && isHelpToken(args[0]) {
		fmt.Fprint(stdout, initHelp)
		return 0
	}
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, initHelp) }
	projectRoot := flags.String("project", "", "project root")
	vaultRoot := flags.String("vault", "", "Obsidian vault root")
	dataRoot := flags.String("data-dir", "", "machine data directory")
	write := flags.Bool("write", false, "perform the previewed writes")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *projectRoot == "" || *vaultRoot == "" {
		fmt.Fprintln(stderr, "init requires --project and --vault")
		return 2
	}
	if *dataRoot == "" {
		resolved, err := platform.DataDir(platform.CurrentEnv())
		if err != nil {
			return writeDiagnostic(stderr, "init", err)
		}
		*dataRoot = resolved
	}
	options := project.InitOptions{
		ProjectRoot: *projectRoot,
		VaultRoot:   *vaultRoot,
		DataDir:     *dataRoot,
		GOOS:        runtime.GOOS,
	}
	preview, err := project.PreviewInitialization(options)
	if err != nil {
		return writeDiagnostic(stderr, "init", err)
	}
	fmt.Fprintf(stdout, "action: %s\nproject_id: %s\nledger: %s\nconfig: %s\nwritten: false\n", preview.Action, preview.ProjectID, preview.LedgerRoot, preview.ConfigPath)
	if !*write {
		return 0
	}
	result, err := project.Initialize(options)
	if err != nil {
		return writeDiagnostic(stderr, "init", err)
	}
	fmt.Fprintf(stdout, "project_id: %s\nledger: %s\nconfig: %s\nwritten: true\n", result.ProjectID, result.LedgerRoot, result.ConfigPath)
	return 0
}
