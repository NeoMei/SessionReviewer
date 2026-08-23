package cli

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	applyengine "github.com/neomei/SessionReviewer/internal/apply"
	"github.com/neomei/SessionReviewer/internal/platform"
)

const applyHelp = `Validate and apply a Skill proposal to the accepted Markdown ledger.

Usage:
  session-reviewer apply --proposal PATH --evidence PATH [options]

Options:
  --proposal PATH  Skill proposal JSON (required)
  --evidence PATH  Exact bounded evidence packet JSON (required)
  --project PATH   Project root; defaults to the current directory
  --data-dir PATH  Machine-local SessionReviewer data directory

The apply command validates a Skill proposal against its exact evidence packet.
It prints identifiers, cursor range, changed relative paths, and apply status only.

Examples:
  session-reviewer apply --proposal proposal.json --evidence evidence.json
  session-reviewer apply --proposal proposal.json --evidence evidence.json --project /path/to/project --data-dir /path/to/data
`

const maxApplyOutputBytes = 1 << 20

func runApply(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && isHelpToken(args[0]) {
		fmt.Fprint(stdout, applyHelp)
		return 0
	}
	flags := flag.NewFlagSet("apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, applyHelp) }
	proposalPath := flags.String("proposal", "", "Skill proposal JSON")
	evidencePath := flags.String("evidence", "", "bounded evidence packet JSON")
	projectRoot := flags.String("project", "", "project root")
	dataRoot := flags.String("data-dir", "", "machine data directory")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "apply does not accept positional arguments")
		return 2
	}
	if strings.TrimSpace(*proposalPath) == "" || strings.TrimSpace(*evidencePath) == "" {
		fmt.Fprintln(stderr, "apply requires --proposal and --evidence")
		return 2
	}
	if *projectRoot == "" {
		resolved, err := resolveImplicitProjectRoot()
		if err != nil {
			return writeDiagnostic(stderr, "apply", err)
		}
		*projectRoot = resolved
	}
	if *dataRoot == "" {
		resolved, err := platform.DataDir(currentEnv())
		if err != nil {
			return writeDiagnostic(stderr, "apply", err)
		}
		*dataRoot = resolved
	}
	result, err := applyengine.Run(applyengine.Options{
		ProposalPath: *proposalPath,
		EvidencePath: *evidencePath,
		ProjectRoot:  *projectRoot,
		DataDir:      *dataRoot,
	})
	if err != nil {
		return writeDiagnostic(stderr, "apply", err)
	}
	body, err := formatApplyResult(result)
	if err != nil {
		return writeDiagnostic(stderr, "apply", err)
	}
	if _, err := io.WriteString(stdout, body); err != nil {
		return writeDiagnostic(stderr, "apply", err)
	}
	return 0
}

func formatApplyResult(result applyengine.Result) (string, error) {
	changed := append([]string(nil), result.ChangedFiles...)
	sort.Strings(changed)
	var out strings.Builder
	fmt.Fprintf(&out, "project_id: %s\nsession_id: %s\ncursor_range: %d-%d\n", result.ProjectID, result.SessionID, result.FromCursor, result.ToCursor)
	if len(changed) == 0 {
		out.WriteString("changed_files: []\n")
	} else {
		out.WriteString("changed_files:\n")
		for _, relative := range changed {
			fmt.Fprintf(&out, "  - %s\n", relative)
			if out.Len() > maxApplyOutputBytes {
				return "", fmt.Errorf("apply result exceeds output limit")
			}
		}
	}
	fmt.Fprintf(&out, "cursor_advanced: %t\nalready_applied: %t\n", result.CursorAdvanced, result.AlreadyApplied)
	if out.Len() > maxApplyOutputBytes {
		return "", fmt.Errorf("apply result exceeds output limit")
	}
	return out.String(), nil
}
