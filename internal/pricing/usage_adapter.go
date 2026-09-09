package pricing

import (
	"errors"

	"github.com/neomei/SessionReviewer/internal/accounting"
)

type BillableQuantities struct {
	Quantities  Quantities
	RuleVersion string
}
type UsageAdapter interface {
	Provider() string
	Billable(accounting.ModelUsage) (BillableQuantities, []string, error)
}

const (
	ClaudeInclusiveInputV1   = "claude-normalized-inclusive-v1"
	OpenCodeInclusiveInputV1 = "opencode-normalized-inclusive-v1"
)

type CodexUsageAdapter struct{}
type ClaudeUsageAdapter struct{ Normalization string }
type OpenCodeUsageAdapter struct {
	ProviderID    string
	Normalization string
}

func (CodexUsageAdapter) Provider() string    { return "codex" }
func (ClaudeUsageAdapter) Provider() string   { return "claude" }
func (OpenCodeUsageAdapter) Provider() string { return "opencode" }
func (CodexUsageAdapter) Billable(v accounting.ModelUsage) (BillableQuantities, []string, error) {
	return inclusiveBillable(v, "codex-token-count-v1")
}
func (a ClaudeUsageAdapter) Billable(v accounting.ModelUsage) (BillableQuantities, []string, error) {
	if a.Normalization != ClaudeInclusiveInputV1 {
		return unresolvedBillable(), allDimensions(), nil
	}
	return inclusiveBillable(v, "claude-normalized-inclusive-v1")
}
func (a OpenCodeUsageAdapter) Billable(v accounting.ModelUsage) (BillableQuantities, []string, error) {
	if a.ProviderID == "" {
		return BillableQuantities{}, nil, errors.New("opencode provider ID is required")
	}
	if a.Normalization != OpenCodeInclusiveInputV1 {
		return unresolvedBillable(), allDimensions(), nil
	}
	return inclusiveBillable(v, "opencode-"+a.ProviderID+"-normalized-inclusive-v1")
}
func inclusiveBillable(v accounting.ModelUsage, rule string) (BillableQuantities, []string, error) {
	if err := accounting.ValidateTokenUsage(v.TokenUsage); err != nil {
		return BillableQuantities{}, nil, err
	}
	if v.InputTokens < v.CachedInputTokens+v.CacheWriteInputTokens {
		return BillableQuantities{}, nil, errors.New("inclusive input is smaller than cached/cache-write components")
	}
	// accounting.TokenUsage defines OutputTokens as already including reasoning.
	// Keep ReasoningOutputTokens as audit metadata and charge reported output once.
	q := Quantities{Input: uint64(v.InputTokens - v.CachedInputTokens - v.CacheWriteInputTokens), CachedInput: uint64(v.CachedInputTokens), CacheWriteInput: uint64(v.CacheWriteInputTokens), Output: uint64(v.OutputTokens)}
	return BillableQuantities{Quantities: q, RuleVersion: rule}, nil, nil
}
func unresolvedBillable() BillableQuantities { return BillableQuantities{} }
func allDimensions() []string {
	return []string{"input", "cached_input", "cache_write_input", "output", "reasoning_output"}
}
