package cli

import (
	"errors"
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

func runPrepare(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "prepare requires review or checkpoint")
		return 2
	}
	mode := args[0]
	if mode != "review" && mode != "checkpoint" {
		fmt.Fprintf(stderr, "invalid prepare mode %q\n", mode)
		return 2
	}
	flags := flag.NewFlagSet("prepare "+mode, flag.ContinueOnError)
	flags.SetOutput(stderr)
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
			fmt.Fprintln(stderr, "prepare failed")
			return 1
		}
		*cwd = resolved
	}
	root, err := platform.ResolveSessionsRoot(*sessionsRoot, env)
	if err != nil {
		fmt.Fprintln(stderr, "prepare failed")
		return 1
	}
	*sessionsRoot = root.Path
	if *sessionID == "" {
		resolvedID, _, err := platform.ResolveCurrentSessionID(*currentSessionID, env)
		if err != nil {
			fmt.Fprintln(stderr, "prepare failed")
			return 1
		}
		*sessionID = resolvedID
	}
	if *dataRoot == "" {
		resolved, err := platform.DataDir(env)
		if err != nil {
			fmt.Fprintln(stderr, "prepare failed")
			return 1
		}
		*dataRoot = resolved
	}
	_, err = prepare.Run(prepare.Options{
		Mode: mode, SessionsRoot: *sessionsRoot, SessionID: *sessionID,
		CWD: *cwd, DataDir: *dataRoot, Output: *output, GOOS: runtime.GOOS,
		FromStart: *fromStart, Now: time.Now(), AmbiguityWindow: 5 * time.Minute,
	})
	if err != nil {
		if errors.Is(err, prepare.ErrCursorSourceDrift) {
			fmt.Fprintln(stderr, "prepare failed: accepted session source changed; use prepare review --from-start for recovery")
			return 1
		}
		fmt.Fprintln(stderr, "prepare failed")
		return 1
	}
	return 0
}
