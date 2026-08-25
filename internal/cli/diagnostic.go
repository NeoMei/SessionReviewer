package cli

import (
	"errors"
	"fmt"
	"io"

	applyengine "github.com/neomei/SessionReviewer/internal/apply"
	"github.com/neomei/SessionReviewer/internal/cursor"
	"github.com/neomei/SessionReviewer/internal/prepare"
	"github.com/neomei/SessionReviewer/internal/project"
)

type Diagnostic struct {
	Code    string
	Message string
	Hint    string
}

func writeDiagnostic(w io.Writer, action string, err error) int {
	diagnostic := fallbackDiagnostic(action)
	switch {
	case errors.Is(err, cursor.ErrStale) && action == "apply":
		diagnostic = Diagnostic{
			Code:    "E_APPLY_CURSOR_STALE",
			Message: "accepted cursor does not match the proposal evidence",
			Hint:    "prepare a fresh evidence packet and Skill proposal; do not edit cursor files",
		}
	case errors.Is(err, applyengine.ErrPendingReceiptConflict) && action == "apply":
		diagnostic = Diagnostic{
			Code:    "E_APPLY_RECEIPT_CONFLICT",
			Message: "another proposal owns an unfinished apply transaction",
			Hint:    "retry with the same --proposal and --evidence inputs to recover that transaction",
		}
	case errors.Is(err, prepare.ErrCursorSourceDrift):
		diagnostic = Diagnostic{
			Code:    "E_CURSOR_DRIFT",
			Message: "accepted session source changed",
			Hint:    "run prepare review --from-start; this does not repair the cursor",
		}
	case errors.Is(err, prepare.ErrSessionNotFound):
		diagnostic = Diagnostic{
			Code:    "E_SESSION_NOT_FOUND",
			Message: "selected session was not found",
			Hint:    "check --session and --sessions-root, or omit --session to use current-session discovery",
		}
	case errors.Is(err, prepare.ErrSessionAmbiguous):
		diagnostic = Diagnostic{
			Code:    "E_SESSION_AMBIGUOUS",
			Message: "current session is ambiguous",
			Hint:    "pass --session or --current-session-id explicitly",
		}
	case errors.Is(err, prepare.ErrSessionSegmentConflict):
		diagnostic = Diagnostic{
			Code:    "E_SESSION_SEGMENT_CONFLICT",
			Message: "selected session has conflicting rollout segments",
			Hint:    "use --sessions-root containing only one project's session segments, or back up and repair duplicate session metadata",
		}
	case errors.Is(err, prepare.ErrSessionFormatUnsupported):
		diagnostic = Diagnostic{
			Code:    "E_SESSION_FORMAT_UNSUPPORTED",
			Message: "selected session record format is unsupported or invalid",
			Hint:    "upgrade SessionReviewer and retry; if already current, preserve the source and report this code with session-reviewer version",
		}
	case errors.Is(err, prepare.ErrSessionDiscoveryLimit):
		diagnostic = Diagnostic{
			Code:    "E_SESSION_DISCOVERY_LIMIT",
			Message: "session discovery exceeds supported safety limits",
			Hint:    "narrow --sessions-root to the relevant Codex session tree, or select a smaller archived source",
		}
	case errors.Is(err, prepare.ErrProjectNotInitialized):
		diagnostic = Diagnostic{
			Code:    "E_PROJECT_NOT_INITIALIZED",
			Message: "project is not initialized",
			Hint:    "run session-reviewer init to preview, then repeat with --write",
		}
	case errors.Is(err, prepare.ErrUnsafeOutput):
		diagnostic = Diagnostic{
			Code:    "E_OUTPUT_UNSAFE",
			Message: "evidence output path is unsafe",
			Hint:    "choose a regular file under the project and outside session/data roots",
		}
	case errors.Is(err, project.ErrInitializationStateChanged):
		diagnostic = Diagnostic{
			Code:    "E_INIT_STATE_CHANGED",
			Message: "initialization state changed after preview",
			Hint:    "inspect the roots and rerun init preview before retrying --write",
		}
	case errors.Is(err, project.ErrNestedInitializationRoots):
		diagnostic = Diagnostic{
			Code:    "E_INIT_ROOTS_NESTED",
			Message: "project and vault roots overlap",
			Hint:    "choose separate roots; neither may contain the other",
		}
	case errors.Is(err, project.ErrInvalidInitializationRoot):
		diagnostic = Diagnostic{
			Code:    "E_INIT_ROOT_INVALID",
			Message: "an initialization root is missing or unsafe",
			Hint:    "check --project, --vault, and --data-dir; project and vault must name existing real directories",
		}
	case errors.Is(err, project.ErrCorruptInitializationConfig):
		diagnostic = Diagnostic{
			Code:    "E_INIT_CONFIG_CORRUPT",
			Message: "initialization configuration is unreadable",
			Hint:    "repair or restore config.toml, then rerun init preview",
		}
	case errors.Is(err, project.ErrConflictingInitializationIdentity):
		diagnostic = Diagnostic{
			Code:    "E_INIT_IDENTITY_CONFLICT",
			Message: "project identity conflicts with existing state",
			Hint:    "use the mapped --vault, or reconcile config.toml and project-overview.md before retrying",
		}
	}
	fmt.Fprintf(w, "%s: %s\nrecovery: %s\n", diagnostic.Code, diagnostic.Message, diagnostic.Hint)
	return 1
}

func fallbackDiagnostic(action string) Diagnostic {
	switch action {
	case "init":
		return Diagnostic{
			Code:    "E_INIT_FAILED",
			Message: "initialization failed",
			Hint:    "check permissions and rerun init preview",
		}
	case "prepare":
		return Diagnostic{
			Code:    "E_PREPARE_FAILED",
			Message: "prepare failed",
			Hint:    "run session-reviewer help and retry with explicit paths",
		}
	case "apply":
		return Diagnostic{
			Code:    "E_APPLY_FAILED",
			Message: "proposal application failed",
			Hint:    "inspect the accepted ledger, then retry with the original --proposal and --evidence inputs",
		}
	case "resume", "history":
		return Diagnostic{
			Code:    "E_RECOVERY_FAILED",
			Message: "ledger-only recovery failed",
			Hint:    "inspect and repair the accepted Markdown ledger, then retry --ledger-only",
		}
	case "sync":
		return Diagnostic{
			Code:    "E_SYNC_FAILED",
			Message: "synchronization failed",
			Hint:    "run sync --dry-run and sync status --json, then repair the reported mapping or conflict before retrying",
		}
	default:
		return Diagnostic{
			Code:    "E_OPERATION_FAILED",
			Message: "operation failed",
			Hint:    "run session-reviewer help and retry",
		}
	}
}
