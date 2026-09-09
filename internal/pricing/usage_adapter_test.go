package pricing

import (
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
)

func TestInclusiveUsageAdapterProducesMutuallyExclusiveQuantities(t *testing.T) {
	usage := accounting.ModelUsage{Model: "m", TokenUsage: accounting.TokenUsage{InputTokens: 100, CachedInputTokens: 20, CacheWriteInputTokens: 5, OutputTokens: 30, ReasoningOutputTokens: 7, TotalTokens: 130}}
	for _, adapter := range []UsageAdapter{CodexUsageAdapter{}, ClaudeUsageAdapter{Normalization: ClaudeInclusiveInputV1}, OpenCodeUsageAdapter{ProviderID: "openai", Normalization: OpenCodeInclusiveInputV1}} {
		got, missing, err := adapter.Billable(usage)
		if err != nil || len(missing) != 0 {
			t.Fatalf("%T got=%#v missing=%v err=%v", adapter, got, missing, err)
		}
		if got.Quantities != (Quantities{Input: 75, CachedInput: 20, CacheWriteInput: 5, Output: 23, ReasoningOutput: 7}) {
			t.Fatalf("%T quantities=%#v", adapter, got.Quantities)
		}
		if got.RuleVersion == "" {
			t.Fatalf("%T missing rule version", adapter)
		}
	}
}

func TestUsageAdaptersRejectUnprovenOrInconsistentShapes(t *testing.T) {
	usage := accounting.ModelUsage{Model: "m", TokenUsage: accounting.TokenUsage{InputTokens: 10, CachedInputTokens: 11, TotalTokens: 10}}
	if _, _, err := (CodexUsageAdapter{}).Billable(usage); err == nil {
		t.Fatal("accepted overlapping cached input greater than inclusive input")
	}
	valid := accounting.ModelUsage{Model: "m", TokenUsage: accounting.TokenUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}}
	got, missing, err := (ClaudeUsageAdapter{}).Billable(valid)
	if err != nil || got.RuleVersion != "" || len(missing) != 5 {
		t.Fatalf("unreviewed claude got=%#v missing=%v err=%v", got, missing, err)
	}
	if _, _, err := (OpenCodeUsageAdapter{Normalization: OpenCodeInclusiveInputV1}).Billable(valid); err == nil {
		t.Fatal("accepted opencode usage without provider id")
	}
}
