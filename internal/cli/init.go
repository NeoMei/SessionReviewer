package cli

import (
	"errors"
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
			return writeInitDiagnostic(stderr, err)
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
		return writeInitDiagnostic(stderr, err)
	}
	fmt.Fprintf(stdout, "action: %s\nproject_id: %s\nledger: %s\nconfig: %s\nwritten: false\n", preview.Action, preview.ProjectID, preview.LedgerRoot, preview.ConfigPath)
	if !*write {
		return 0
	}
	result, err := project.Initialize(options)
	if err != nil {
		return writeInitDiagnostic(stderr, err)
	}
	fmt.Fprintf(stdout, "project_id: %s\nledger: %s\nconfig: %s\nwritten: true\n", result.ProjectID, result.LedgerRoot, result.ConfigPath)
	return 0
}

type initDiagnostic struct {
	Code    string
	Message string
	Hint    string
}

func writeInitDiagnostic(w io.Writer, err error) int {
	diagnostic := initDiagnostic{
		Code:    "E_INIT_FAILED",
		Message: "initialization failed",
		Hint:    "check permissions and rerun init preview",
	}
	switch {
	case errors.Is(err, project.ErrInitializationStateChanged):
		diagnostic = initDiagnostic{
			Code:    "E_INIT_STATE_CHANGED",
			Message: "initialization state changed after preview",
			Hint:    "inspect the roots and rerun init preview before retrying --write",
		}
	case errors.Is(err, project.ErrNestedInitializationRoots):
		diagnostic = initDiagnostic{
			Code:    "E_INIT_ROOTS_NESTED",
			Message: "project and vault roots overlap",
			Hint:    "choose separate roots; neither may contain the other",
		}
	case errors.Is(err, project.ErrInvalidInitializationRoot):
		diagnostic = initDiagnostic{
			Code:    "E_INIT_ROOT_INVALID",
			Message: "an initialization root is missing or unsafe",
			Hint:    "check --project, --vault, and --data-dir; project and vault must name existing real directories",
		}
	case errors.Is(err, project.ErrCorruptInitializationConfig):
		diagnostic = initDiagnostic{
			Code:    "E_INIT_CONFIG_CORRUPT",
			Message: "initialization configuration is unreadable",
			Hint:    "repair or restore config.toml, then rerun init preview",
		}
	case errors.Is(err, project.ErrConflictingInitializationIdentity):
		diagnostic = initDiagnostic{
			Code:    "E_INIT_IDENTITY_CONFLICT",
			Message: "project identity conflicts with existing state",
			Hint:    "use the mapped --vault, or reconcile config.toml and project-overview.md before retrying",
		}
	}
	fmt.Fprintf(w, "%s: %s\nrecovery: %s\n", diagnostic.Code, diagnostic.Message, diagnostic.Hint)
	return 1
}
