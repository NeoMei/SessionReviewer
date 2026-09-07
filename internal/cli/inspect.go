package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	inspectapi "github.com/neomei/SessionReviewer/internal/inspect"
)

const inspectHelp = `Read one validated page from a published Session index.

Usage:
  session-reviewer inspect session-summary --project-id ID --provider ID --session-id ID
    --expected-generation-id ID --json
  session-reviewer inspect session-events --project-id ID --provider ID --session-id ID
    --expected-generation-id ID [--cursor TOKEN | --anchor ORDINAL]
    --limit 1..100 --json
  session-reviewer inspect conversation-chain --project-id ID --provider ID --session-id ID
    --expected-generation-id ID [--cursor TOKEN | --turn-unit-id ID [--message-cursor TOKEN]]
    --limit 1..64 --json
`

type inspectDiagnostic struct {
	Error inspectDiagnosticError `json:"error" required:"true"`
}

type inspectDiagnosticError struct {
	Code    string `json:"code" required:"true"`
	Message string `json:"message" required:"true"`
}

func runInspect(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && isHelpToken(args[0]) {
		fmt.Fprint(stdout, inspectHelp)
		return 0
	}
	request, err := ParseInspectContract(args)
	if err != nil {
		writeInspectError(stdout, err)
		return 2
	}
	if request.Command != "session-summary" && request.Command != "session-events" && request.Command != "conversation-chain" {
		writeInspectError(stdout, ContractError{Code: ContractCodeInvalidArgument, Message: "inspect subcommand is not implemented"})
		return 2
	}
	dataRoot := resolveDataDir("")
	if dataRoot == "" {
		writeInspectError(stdout, ContractError{Code: ContractCodeInvalidArgument, Message: "SessionReviewer data directory is unavailable"})
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), InspectExecutionTimeout)
	defer cancel()
	var body []byte
	if request.Command == "session-summary" {
		summary, loadErr := inspectapi.LoadSessionSummary(ctx, inspectapi.SummaryRequest{
			DataRoot: dataRoot, ProjectID: request.ProjectID, Provider: request.Provider,
			SessionID: request.SessionID, ExpectedGenerationID: request.ExpectedGenerationID,
		})
		err = loadErr
		if err == nil {
			body, err = inspectapi.RenderSummary(summary)
		}
	} else if request.Command == "conversation-chain" {
		page, loadErr := inspectapi.LoadConversationPage(ctx, inspectapi.ConversationRequest{DataRoot: dataRoot, ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID, ExpectedGenerationID: request.ExpectedGenerationID, TurnUnitID: request.TurnUnitID, Cursor: request.Cursor, MessageCursor: request.MessageCursor, Limit: request.Limit})
		err = loadErr
		if err == nil {
			body, err = inspectapi.RenderConversationPage(page)
		}
	} else {
		page, loadErr := inspectapi.LoadSessionEventPage(ctx, inspectapi.EventPageRequest{
			DataRoot: dataRoot, ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID,
			ExpectedGenerationID: request.ExpectedGenerationID, Cursor: request.Cursor, Anchor: request.Anchor, Limit: request.Limit,
		})
		err = loadErr
		if err == nil {
			body, err = inspectapi.RenderEventPage(page)
		}
	}
	if err != nil {
		writeInspectError(stdout, err)
		return 1
	}
	if len(body) > MaxInspectResponseBytes {
		writeInspectError(stdout, ContractError{Code: ContractCodeResponseTooLarge, Message: "inspection response exceeds its byte limit"})
		return 1
	}
	if _, err := stdout.Write(append(body, '\n')); err != nil {
		fmt.Fprintln(stderr, "inspect output failed")
		return 1
	}
	return 0
}

func writeInspectError(output io.Writer, err error) {
	code, message := ContractCodeInvalidArgument, "inspection request failed"
	var contractErr ContractError
	var inspectErr *inspectapi.Error
	switch {
	case errors.As(err, &contractErr):
		code, message = contractErr.Code, contractErr.Message
	case errors.As(err, &inspectErr):
		code, message = inspectErr.Code, inspectErr.Message
	}
	if len(message) > 512 {
		message = message[:512]
	}
	_ = json.NewEncoder(output).Encode(inspectDiagnostic{Error: inspectDiagnosticError{Code: code, Message: message}})
}
