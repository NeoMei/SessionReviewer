package cli

import (
	"flag"
	"fmt"
	"io"
	"runtime"

	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/project"
)

func runInit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	projectRoot := flags.String("project", "", "project root")
	vaultRoot := flags.String("vault", "", "Obsidian vault root")
	dataRoot := flags.String("data-dir", "", "machine data directory")
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
			fmt.Fprintln(stderr, err)
			return 1
		}
		*dataRoot = resolved
	}
	result, err := project.Initialize(project.InitOptions{
		ProjectRoot: *projectRoot,
		VaultRoot:   *vaultRoot,
		DataDir:     *dataRoot,
		GOOS:        runtime.GOOS,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "project_id: %s\nledger: %s\nconfig: %s\n", result.ProjectID, result.LedgerRoot, result.ConfigPath)
	return 0
}
