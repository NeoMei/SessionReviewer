package cli

import (
	"fmt"
	"io"
)

var Version = "dev"

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: session-reviewer <command> [options]")
		return 2
	}
	switch args[0] {
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
