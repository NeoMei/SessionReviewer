package reviewjob

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/agent"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

type fixturePricingResolver map[string]accounting.Pricing

func (resolver fixturePricingResolver) Resolve(model string, _ time.Time) (accounting.Pricing, bool) {
	pricing, ok := resolver[model]
	return pricing, ok
}

func TestReviewAccountingAggregatesPacketsByModelInStableOrder(t *testing.T) {
	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	resolver := fixturePricingResolver{
		"z-model": fixturePricing(2, .5, 3, 10),
		"a-model": fixturePricing(4, .4, 5, 20),
	}
	results := []agent.Result{
		{Model: "z-model", Usage: accounting.TokenUsage{InputTokens: 1_000, CachedInputTokens: 200, CacheWriteInputTokens: 100, OutputTokens: 100, ReasoningOutputTokens: 75, TotalTokens: 1_100}},
		{Model: "a-model", Usage: accounting.TokenUsage{InputTokens: 500, CachedInputTokens: 100, OutputTokens: 50, ReasoningOutputTokens: 10, TotalTokens: 550}},
		{Model: "z-model", Usage: accounting.TokenUsage{InputTokens: 100, OutputTokens: 10, ReasoningOutputTokens: 5, TotalTokens: 110}},
	}

	var got ReviewAccounting
	var err error
	for _, result := range results {
		got, err = AddReviewResult(got, result, at, resolver)
		if err != nil {
			t.Fatal(err)
		}
	}

	if !got.PricingComplete || got.TotalCostUSD == nil {
		t.Fatalf("accounting is unexpectedly incomplete: %+v", got)
	}
	if got.TotalTokens != 1_760 || math.Abs(*got.TotalCostUSD-.00574) > 1e-15 {
		t.Fatalf("totals=%+v", got)
	}
	if len(got.Models) != 2 || got.Models[0].Model != "a-model" || got.Models[1].Model != "z-model" {
		t.Fatalf("models are not deterministically sorted: %+v", got.Models)
	}
	wantA := accounting.TokenUsage{InputTokens: 500, CachedInputTokens: 100, OutputTokens: 50, ReasoningOutputTokens: 10, TotalTokens: 550}
	wantZ := accounting.TokenUsage{InputTokens: 1_100, CachedInputTokens: 200, CacheWriteInputTokens: 100, OutputTokens: 110, ReasoningOutputTokens: 80, TotalTokens: 1_210}
	if got.Models[0].TokenUsage != wantA || got.Models[1].TokenUsage != wantZ {
		t.Fatalf("model usage=%+v", got.Models)
	}
	if math.Abs(got.Models[0].CostUSD-.00264) > 1e-15 || math.Abs(got.Models[1].CostUSD-.0031) > 1e-15 {
		t.Fatalf("model costs=%+v", got.Models)
	}
}

func TestReviewAccountingUnknownAndEmptyModelsRetainExactTokensWithoutCost(t *testing.T) {
	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	results := []agent.Result{
		{Model: "known", Usage: accounting.TokenUsage{InputTokens: 20, OutputTokens: 5, TotalTokens: 25}},
		{Model: "unlisted", Usage: accounting.TokenUsage{InputTokens: 30, CachedInputTokens: 10, OutputTokens: 7, TotalTokens: 37}},
		{Model: "", Usage: accounting.TokenUsage{InputTokens: 40, OutputTokens: 8, TotalTokens: 48}},
	}
	resolver := fixturePricingResolver{"known": fixturePricing(1, .1, 2, 4)}

	var got ReviewAccounting
	for _, result := range results {
		var err error
		got, err = AddReviewResult(got, result, at, resolver)
		if err != nil {
			t.Fatal(err)
		}
	}

	if got.PricingComplete || got.TotalCostUSD != nil || got.TotalTokens != 110 {
		t.Fatalf("unknown pricing semantics violated: %+v", got)
	}
	if len(got.Models) != 3 || got.Models[0].Model != "" || got.Models[0].TotalTokens != 48 || got.Models[2].Model != "unlisted" || got.Models[2].TotalTokens != 37 {
		t.Fatalf("unknown model usage was not retained exactly: %+v", got.Models)
	}
	if got.Models[1].Model != "known" || got.Models[1].CostUSD == 0 {
		t.Fatalf("known model lost its priced usage: %+v", got.Models[1])
	}

	emptyPrice := fixturePricing(1, 0, 0, 1)
	emptyCost := .00001
	if err := ValidateReviewAccounting(ReviewAccounting{
		Models: []accounting.ModelAccounting{{
			ModelUsage: accounting.ModelUsage{TokenUsage: accounting.TokenUsage{InputTokens: 10, TotalTokens: 10}},
			Pricing:    emptyPrice,
			CostUSD:    emptyCost,
		}},
		TotalTokens:     10,
		TotalCostUSD:    &emptyCost,
		PricingComplete: true,
	}); err == nil {
		t.Fatal("accepted invented pricing attribution for an empty authoritative model")
	}
}

func TestReviewAccountingRejectsOverflowAndNonFinitePricingWithoutMutatingInput(t *testing.T) {
	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	current := ReviewAccounting{
		Models:          make([]accounting.ModelAccounting, 1, 2),
		TotalTokens:     1<<53 - 1,
		PricingComplete: false,
	}
	current.Models[0] = accounting.ModelAccounting{ModelUsage: accounting.ModelUsage{Model: "unknown", TokenUsage: accounting.TokenUsage{InputTokens: 1<<53 - 1, TotalTokens: 1<<53 - 1}}}
	before, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddReviewResult(current, agent.Result{Model: "unknown", Usage: accounting.TokenUsage{InputTokens: 1, TotalTokens: 1}}, at, fixturePricingResolver{}); err == nil {
		t.Fatal("accepted review accounting total beyond the safe integer range")
	}
	after, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("input mutated on error\nbefore=%s\nafter=%s", before, after)
	}

	nonfinite := fixturePricing(1, 0, 0, 1)
	nonfinite.OutputPerMillion = math.Inf(1)
	if _, err := AddReviewResult(ReviewAccounting{}, agent.Result{Model: "bad-price", Usage: accounting.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, at, fixturePricingResolver{"bad-price": nonfinite}); err == nil {
		t.Fatal("accepted non-finite review pricing")
	}
}

func TestReviewAccountingDoesNotAliasInputsOrMutateMachineLedgerAccounting(t *testing.T) {
	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	zero := 0.0
	current := ReviewAccounting{
		Models: []accounting.ModelAccounting{{
			ModelUsage: accounting.ModelUsage{Model: "a-model", TokenUsage: accounting.TokenUsage{InputTokens: 10, TotalTokens: 10}},
			Pricing:    fixturePricing(1, 0, 0, 1),
			CostUSD:    .00001,
		}},
		TotalTokens:     10,
		TotalCostUSD:    &zero,
		PricingComplete: true,
	}
	*current.TotalCostUSD = .00001
	ledger := reviewv2.MachineLedger{Accounting: accounting.ProjectSummary{
		TotalDurationMS: 123,
		TotalTokens:     456,
		TotalCostUSD:    7.89,
		Models:          []accounting.ProjectModelSummary{{Model: "source-session-model", TotalTokens: 456, TotalCostUSD: 7.89, TokenSharePct: 100, CostSharePct: 100}},
	}}
	currentBefore, _ := json.Marshal(current)
	ledgerBefore, _ := json.Marshal(ledger.Accounting)

	got, err := AddReviewResult(current, agent.Result{Model: "b-model", Usage: accounting.TokenUsage{InputTokens: 5, OutputTokens: 1, TotalTokens: 6}}, at, fixturePricingResolver{"b-model": fixturePricing(1, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	currentAfter, _ := json.Marshal(current)
	ledgerAfter, _ := json.Marshal(ledger.Accounting)
	if !bytes.Equal(currentBefore, currentAfter) {
		t.Fatalf("review accounting input mutated\nbefore=%s\nafter=%s", currentBefore, currentAfter)
	}
	if !bytes.Equal(ledgerBefore, ledgerAfter) {
		t.Fatalf("source-session MachineLedger.Accounting changed\nbefore=%s\nafter=%s", ledgerBefore, ledgerAfter)
	}

	got.Models[0].InputTokens++
	*got.TotalCostUSD++
	if !reflect.DeepEqual(current.Models[0].TokenUsage, accounting.TokenUsage{InputTokens: 10, TotalTokens: 10}) || math.Abs(*current.TotalCostUSD-.00001) > 1e-15 {
		t.Fatalf("returned accounting aliases input: current=%+v got=%+v", current, got)
	}
}

func fixturePricing(input, cached, cacheWrite, output float64) accounting.Pricing {
	return accounting.Pricing{
		Currency:                  "USD",
		InputPerMillion:           input,
		CachedInputPerMillion:     cached,
		CacheWriteInputPerMillion: cacheWrite,
		OutputPerMillion:          output,
		Source:                    "https://example.com/pricing",
		AsOf:                      "2026-08-29",
	}
}
