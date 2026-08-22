package cli

import (
	"fmt"
	"io"
)

var Version = "dev"

const rootHelp = `SessionReviewer prepares bounded evidence for durable session review.

Usage: session-reviewer <command> [options]

Commands:
  init                  Preview or write project and Obsidian initialization
  prepare review        Prepare review evidence, optionally from the start
  prepare checkpoint    Prepare incremental checkpoint evidence
  version               Print the version

Options:
  init: --project --vault [--data-dir] [--write]
  prepare: --output [--sessions-root] [--cwd] [--session]
           [--current-session-id] [--data-dir] [--from-start for review]

Examples:
  session-reviewer init --project /path/to/project --vault /path/to/vault
  session-reviewer init --project /path/to/project --vault /path/to/vault --write
  session-reviewer prepare review --output /path/to/project/review.json
  session-reviewer prepare checkpoint --session SESSION_ID --sessions-root /path/to/sessions --output /path/to/project/checkpoint.json

Run session-reviewer init --help or session-reviewer prepare <mode> --help for details.
`

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, rootHelp)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, rootHelp)
		return 0
	case "version":
		fmt.Fprintln(stdout, Version)
		return 0
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "prepare":
		return runPrepare(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
}

func isHelpToken(arg string) bool {
	return arg == "help" || arg == "-h" || arg == "--help"
}
