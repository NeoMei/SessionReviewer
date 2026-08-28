// Package agent defines the provider-neutral, proposal-only Agent boundary.
package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
)

// Adapter verifies and invokes one proposal-only Agent implementation.
type Adapter interface {
	Verify(context.Context, string) (Capability, error)
	GenerateProposal(context.Context, Request) (Result, error)
	Cancel(context.Context) error
}

// Capability is the verified behavior contract of an Adapter implementation.
type Capability struct {
	Provider           string
	Version            string
	ProposalOnly       bool
	NoTools            bool
	ReadOnly           bool
	StructuredOutput   bool
	NativeCancellation bool
}

// Request contains the complete private input for one ephemeral Agent run.
// WorkingDirectory is process configuration and must not be embedded in Prompt.
type Request struct {
	Prompt           []byte
	OutputSchema     []byte
	WorkingDirectory string
	Deadline         time.Time
}

// Result contains untrusted proposal bytes plus private review-run accounting.
type Result struct {
	Proposal []byte
	Model    string
	Usage    accounting.TokenUsage
}

// ErrorCode is the complete public Agent error vocabulary.
type ErrorCode string

const (
	CodeUnconfigured  ErrorCode = "E_AGENT_UNCONFIGURED"
	CodeIncompatible  ErrorCode = "E_AGENT_INCOMPATIBLE"
	CodeAuth          ErrorCode = "E_AGENT_AUTH"
	CodeBusy          ErrorCode = "E_AGENT_BUSY"
	CodeTimeout       ErrorCode = "E_AGENT_TIMEOUT"
	CodeToolForbidden ErrorCode = "E_AGENT_TOOL_FORBIDDEN"
	CodeCancelled     ErrorCode = "E_AGENT_CANCELLED"
)

// Error retains a private cause while rendering only its reviewed safe code.
type Error struct {
	code  ErrorCode
	cause error
}

// NewError wraps cause with one reviewed safe code. Unknown codes are a
// programmer error because silently accepting one expands the public contract.
func NewError(code ErrorCode, cause error) *Error {
	if !code.valid() {
		panic(fmt.Sprintf("invalid agent error code %q", code))
	}
	return &Error{code: code, cause: cause}
}

func (err *Error) Error() string {
	if err == nil {
		return ""
	}
	return string(err.code)
}

func (err *Error) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func (err *Error) Code() ErrorCode {
	if err == nil {
		return ""
	}
	return err.code
}

// CodeOf finds a safe Agent code through ordinary error wrapping.
func CodeOf(err error) (ErrorCode, bool) {
	var agentErr *Error
	if !errors.As(err, &agentErr) || agentErr == nil || !agentErr.code.valid() {
		return "", false
	}
	return agentErr.code, true
}

func (code ErrorCode) valid() bool {
	switch code {
	case CodeUnconfigured, CodeIncompatible, CodeAuth, CodeBusy, CodeTimeout, CodeToolForbidden, CodeCancelled:
		return true
	default:
		return false
	}
}
