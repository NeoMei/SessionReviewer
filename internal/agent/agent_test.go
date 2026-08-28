package agent_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/agent"
)

type contractAdapter struct{}

func (contractAdapter) Verify(context.Context, string) (agent.Capability, error) {
	return agent.Capability{}, nil
}

func (contractAdapter) GenerateProposal(context.Context, agent.Request) (agent.Result, error) {
	return agent.Result{}, nil
}

func (contractAdapter) Cancel(context.Context) error { return nil }

func TestAdapterContractCarriesProposalOnlyRequestAndResult(t *testing.T) {
	var _ agent.Adapter = contractAdapter{}

	deadline := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	request := agent.Request{
		Prompt:           []byte("prompt"),
		OutputSchema:     []byte(`{"type":"object"}`),
		WorkingDirectory: "/private/job/canary",
		Deadline:         deadline,
	}
	if string(request.Prompt) != "prompt" || string(request.OutputSchema) != `{"type":"object"}` ||
		request.WorkingDirectory != "/private/job/canary" || !request.Deadline.Equal(deadline) {
		t.Fatalf("request lost contract fields: %+v", request)
	}

	result := agent.Result{
		Proposal: []byte(`{"schema_version":1}`),
		Model:    "codex-default",
		Usage:    accounting.TokenUsage{InputTokens: 11, OutputTokens: 7, TotalTokens: 18},
	}
	if string(result.Proposal) != `{"schema_version":1}` || result.Model != "codex-default" ||
		result.Usage.InputTokens != 11 || result.Usage.OutputTokens != 7 || result.Usage.TotalTokens != 18 {
		t.Fatalf("result lost contract fields: %+v", result)
	}

	capability := agent.Capability{
		Containment:     agent.ContainmentRestrictedReadOnly,
		ModelProvenance: agent.ModelProvenanceUnavailable,
	}
	if capability.Containment != agent.ContainmentRestrictedReadOnly {
		t.Fatalf("capability lost containment contract: %+v", capability)
	}
	if capability.ModelProvenance != agent.ModelProvenanceUnavailable {
		t.Fatalf("capability lost model provenance contract: %+v", capability)
	}
}

func TestAgentErrorsExposeOnlyAllowedCodesAndRetainCause(t *testing.T) {
	allowed := []agent.ErrorCode{
		agent.CodeUnconfigured,
		agent.CodeIncompatible,
		agent.CodeAuth,
		agent.CodeBusy,
		agent.CodeTimeout,
		agent.CodeToolForbidden,
		agent.CodeCancelled,
	}
	want := []string{
		"E_AGENT_UNCONFIGURED",
		"E_AGENT_INCOMPATIBLE",
		"E_AGENT_AUTH",
		"E_AGENT_BUSY",
		"E_AGENT_TIMEOUT",
		"E_AGENT_TOOL_FORBIDDEN",
		"E_AGENT_CANCELLED",
	}
	cause := errors.New("secret stderr and /private/job/canary")
	for i, code := range allowed {
		t.Run(want[i], func(t *testing.T) {
			err := agent.NewError(code, cause)
			if got := err.Error(); got != want[i] {
				t.Fatalf("Error()=%q want %q", got, want[i])
			}
			if got := fmt.Sprintf("%v", err); got != want[i] {
				t.Fatalf("formatted error leaked cause: %q", got)
			}
			if !errors.Is(err, cause) {
				t.Fatal("internal cause is not retained in the error chain")
			}
			wrapped := fmt.Errorf("worker: %w", err)
			if got, ok := agent.CodeOf(wrapped); !ok || got != code {
				t.Fatalf("CodeOf()=(%q,%v) want (%q,true)", got, ok, code)
			}
		})
	}
	if _, ok := agent.CodeOf(errors.New("ordinary failure")); ok {
		t.Fatal("ordinary error was exposed as a safe agent error")
	}
}

func TestNewErrorRejectsCodesOutsideThePublicVocabulary(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewError accepted an unreviewed public code")
		}
	}()
	_ = agent.NewError(agent.ErrorCode("E_AGENT_INTERNAL_CANARY"), errors.New("cause"))
}
