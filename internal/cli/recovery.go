package cli

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/neomei/SessionReviewer/internal/recovery"
)

const resumeHelp = `Render a recovery card from the accepted Markdown ledger.

Usage:
  session-reviewer resume --ledger-only [options]

Options:
  --ledger-only   Read accepted Markdown only (required)
  --project PATH  Project root; defaults to the current directory

Ledger-only recovery does not process pending sessions or interpret pending evidence.

Examples:
  session-reviewer resume --ledger-only
  session-reviewer resume --ledger-only --project /path/to/project
`

const historyHelp = `Render project history from the accepted Markdown ledger.

Usage:
  session-reviewer history --ledger-only [options]

Options:
  --ledger-only   Read accepted Markdown only (required)
  --project PATH  Project root; defaults to the current directory

Ledger-only history does not process pending sessions or interpret pending evidence.

Examples:
  session-reviewer history --ledger-only
  session-reviewer history --ledger-only --project /path/to/project
`

func runRecovery(command string, args []string, stdout, stderr io.Writer) int {
	help := resumeHelp
	if command == "history" {
		help = historyHelp
	}
	if len(args) == 1 && isHelpToken(args[0]) {
		fmt.Fprint(stdout, help)
		return 0
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, help) }
	ledgerOnly := flags.Bool("ledger-only", false, "read accepted Markdown only")
	projectRoot := flags.String("project", "", "project root")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "%s does not accept positional arguments or prompts\n", command)
		return 2
	}
	if !*ledgerOnly {
		fmt.Fprintf(stderr, "%s requires --ledger-only\n", command)
		return 2
	}
	if *projectRoot == "" {
		resolved, err := os.Getwd()
		if err != nil {
			return writeDiagnostic(stderr, command, err)
		}
		*projectRoot = resolved
	}
	var markdown string
	if command == "resume" {
		view, err := recovery.ResumeLedgerOnly(*projectRoot)
		if err != nil {
			return writeDiagnostic(stderr, command, err)
		}
		markdown = view.Markdown()
	} else {
		view, err := recovery.HistoryLedgerOnly(*projectRoot)
		if err != nil {
			return writeDiagnostic(stderr, command, err)
		}
		markdown = view.Markdown()
	}
	if _, err := io.WriteString(stdout, markdown); err != nil {
		return writeDiagnostic(stderr, command, err)
	}
	return 0
}
