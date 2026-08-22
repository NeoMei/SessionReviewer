package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/prepare"
)

var currentEnv = platform.CurrentEnv

const prepareHelp = `Prepare a bounded evidence packet for review or checkpointing.

Usage:
  session-reviewer prepare <mode> [options]

Modes:
  prepare review       Prepare evidence for review
  prepare checkpoint   Prepare incremental checkpoint evidence

Examples:
  session-reviewer prepare review --output /path/to/project/review.json
  session-reviewer prepare checkpoint --session SESSION_ID --output /path/to/project/checkpoint.json

Run session-reviewer prepare <mode> --help for mode options.
`

const prepareReviewHelp = `Prepare a bounded review evidence packet.

Usage:
  session-reviewer prepare review --output PATH [options]

Options:
  --output PATH               Evidence output file (required)
  --sessions-root PATH        Codex sessions root
  --cwd PATH                  Project working directory; defaults to the current directory
  --session ID                Explicit session ID; overrides current-session discovery
  --current-session-id ID     Current Codex thread/session ID when --session is omitted
  --data-dir PATH             Machine-local SessionReviewer data directory
  --from-start                Ignore the accepted cursor for this review

Examples:
  session-reviewer prepare review --output /path/to/project/review.json
  session-reviewer prepare review --session SESSION_ID --sessions-root /path/to/sessions --cwd /path/to/project --data-dir /path/to/data --output /path/to/project/review.json --from-start
`

const prepareCheckpointHelp = `Prepare a bounded incremental checkpoint evidence packet.

Usage:
  session-reviewer prepare checkpoint --output PATH [options]

Options:
  --output PATH               Evidence output file (required)
  --sessions-root PATH        Codex sessions root
  --cwd PATH                  Project working directory; defaults to the current directory
  --session ID                Explicit session ID; overrides current-session discovery
  --current-session-id ID     Current Codex thread/session ID when --session is omitted
  --data-dir PATH             Machine-local SessionReviewer data directory

Examples:
  session-reviewer prepare checkpoint --output /path/to/project/checkpoint.json
  session-reviewer prepare checkpoint --session SESSION_ID --sessions-root /path/to/sessions --cwd /path/to/project --data-dir /path/to/data --output /path/to/project/checkpoint.json
`

func runPrepare(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "prepare requires review or checkpoint")
		return 2
	}
	if len(args) == 1 && isHelpToken(args[0]) {
		fmt.Fprint(stdout, prepareHelp)
		return 0
	}
	mode := args[0]
	if mode != "review" && mode != "checkpoint" {
		fmt.Fprintf(stderr, "invalid prepare mode %q\n", mode)
		return 2
	}
	modeHelp := prepareReviewHelp
	if mode == "checkpoint" {
		modeHelp = prepareCheckpointHelp
	}
	if len(args) == 2 && isHelpToken(args[1]) {
		fmt.Fprint(stdout, modeHelp)
		return 0
	}
	flags := flag.NewFlagSet("prepare "+mode, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, modeHelp) }
	sessionsRoot := flags.String("sessions-root", "", "Codex sessions root")
	cwd := flags.String("cwd", "", "project working directory")
	sessionID := flags.String("session", "", "explicit session ID")
	currentSessionID := flags.String("current-session-id", "", "current Codex thread/session ID; --session overrides it")
	dataRoot := flags.String("data-dir", "", "machine data directory")
	output := flags.String("output", "", "evidence output path")
	fromStart := flags.Bool("from-start", false, "ignore accepted cursor")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "prepare does not accept positional arguments")
		return 2
	}
	if mode == "checkpoint" && *fromStart {
		fmt.Fprintln(stderr, "--from-start is valid only for review")
		return 2
	}
	if *output == "" {
		fmt.Fprintln(stderr, "prepare requires --output")
		return 2
	}
	env := currentEnv()
	if *cwd == "" {
		resolved, err := os.Getwd()
		if err != nil {
			return writeDiagnostic(stderr, "prepare", err)
		}
		*cwd = resolved
	}
	root, err := platform.ResolveSessionsRoot(*sessionsRoot, env)
	if err != nil {
		return writeDiagnostic(stderr, "prepare", err)
	}
	*sessionsRoot = root.Path
	if *sessionID == "" {
		resolvedID, _, err := platform.ResolveCurrentSessionID(*currentSessionID, env)
		if err != nil {
			return writeDiagnostic(stderr, "prepare", err)
		}
		*sessionID = resolvedID
	}
	if *dataRoot == "" {
		resolved, err := platform.DataDir(env)
		if err != nil {
			return writeDiagnostic(stderr, "prepare", err)
		}
		*dataRoot = resolved
	}
	_, err = prepare.Run(prepare.Options{
		Mode: mode, SessionsRoot: *sessionsRoot, SessionID: *sessionID,
		CWD: *cwd, DataDir: *dataRoot, Output: *output, GOOS: runtime.GOOS,
		FromStart: *fromStart, Now: time.Now(), AmbiguityWindow: 5 * time.Minute,
	})
	if err != nil {
		return writeDiagnostic(stderr, "prepare", err)
	}
	return 0
}
