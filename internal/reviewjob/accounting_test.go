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

type recordingPricingResolver struct {
	prices map[string]accounting.Pricing
	calls  []time.Time
}

func (resolver *recordingPricingResolver) Resolve(model string, at time.Time) (accounting.Pricing, bool) {
	resolver.calls = append(resolver.calls, at)
	pricing, ok := resolver.prices[model]
	return pricing, ok
}

func TestReviewAccountingPinsOneCanonicalSnapshotAcrossRetry(t *testing.T) {
	snapshot := time.Date(2026, 8, 29, 12, 0, 0, 123, time.UTC)
	resolver := &recordingPricingResolver{prices: map[string]accounting.Pricing{"model": fixturePricing(1, 0, 0, 1)}}
	first, err := AddReviewResult(ReviewAccounting{}, agent.Result{Model: "model", Usage: accounting.TokenUsage{InputTokens: 10, TotalTokens: 10}}, snapshot, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if !first.SnapshotAt.Equal(snapshot) || first.SnapshotAt.Location() != time.UTC {
		t.Fatalf("snapshot=%v want canonical %v", first.SnapshotAt, snapshot)
	}
	body, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var retried ReviewAccounting
	if err := json.Unmarshal(body, &retried); err != nil {
		t.Fatal(err)
	}
	second, err := AddReviewResult(retried, agent.Result{Model: "model", Usage: accounting.TokenUsage{OutputTokens: 5, TotalTokens: 5}}, snapshot, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if second.Models[0].TokenUsage != (accounting.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}) {
		t.Fatalf("retry usage=%+v", second.Models[0].TokenUsage)
	}
	if len(resolver.calls) != 2 || !resolver.calls[0].Equal(snapshot) || !resolver.calls[1].Equal(snapshot) {
		t.Fatalf("resolver calls=%v want pinned snapshot twice", resolver.calls)
	}

	before, _ := json.Marshal(second)
	if _, err := AddReviewResult(second, agent.Result{Model: "model", Usage: accounting.TokenUsage{InputTokens: 1, TotalTokens: 1}}, snapshot.Add(time.Nanosecond), resolver); err == nil {
		t.Fatal("accepted a different pricing snapshot inside one review job")
	}
	after, _ := json.Marshal(second)
	if !bytes.Equal(before, after) || len(resolver.calls) != 2 {
		t.Fatalf("snapshot mismatch mutated state or called resolver\nbefore=%s\nafter=%s calls=%v", before, after, resolver.calls)
	}
	if _, err := AddReviewResult(ReviewAccounting{}, agent.Result{Model: "model", Usage: accounting.TokenUsage{InputTokens: 1, TotalTokens: 1}}, snapshot.In(time.FixedZone("UTC-alias", 0)), resolver); err == nil {
		t.Fatal("accepted a non-canonical UTC snapshot")
	}
}

func TestReviewAccountingRejectsNondeterministicResolverAtPinnedSnapshot(t *testing.T) {
	snapshot := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	first, err := AddReviewResult(ReviewAccounting{}, agent.Result{Model: "model", Usage: accounting.TokenUsage{InputTokens: 10, TotalTokens: 10}}, snapshot, fixturePricingResolver{"model": fixturePricing(1, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(first)
	if _, err := AddReviewResult(first, agent.Result{Model: "model", Usage: accounting.TokenUsage{InputTokens: 1, TotalTokens: 1}}, snapshot, fixturePricingResolver{}); err == nil {
		t.Fatal("accepted priced-to-unknown resolver drift at one snapshot")
	}
	after, _ := json.Marshal(first)
	if !bytes.Equal(before, after) {
		t.Fatal("resolver drift mutated input accounting")
	}
}

func TestReviewAccountingTransitionValidatesExactDeltasIncludingLegacyMigration(t *testing.T) {
	snapshot := snapshotAt(t)
	before := validReviewAccountingFixture()
	validAfter, err := AddReviewResult(before, agent.Result{Model: "fixture-model", Usage: accounting.TokenUsage{InputTokens: 2, CachedInputTokens: 1, OutputTokens: 1, ReasoningOutputTokens: 1, TotalTokens: 3}}, snapshot, fixturePricingResolver{"fixture-model": fixturePricing(1, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReviewAccountingTransition(before, validAfter); err != nil {
		t.Fatalf("valid additive delta rejected: %v", err)
	}

	legacyBody := []byte(`{"token_usage":{"input_tokens":10,"cached_input_tokens":2,"cache_write_input_tokens":1,"output_tokens":3,"reasoning_output_tokens":1,"total_tokens":13},"cost_usd":99}`)
	var legacy ReviewAccounting
	if err := json.Unmarshal(legacyBody, &legacy); err != nil {
		t.Fatal(err)
	}
	validMigration, err := AddReviewResult(legacy, agent.Result{Model: "", Usage: accounting.TokenUsage{InputTokens: 2, CachedInputTokens: 1, OutputTokens: 1, ReasoningOutputTokens: 1, TotalTokens: 3}}, snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReviewAccountingTransition(legacy, validMigration); err != nil {
		t.Fatalf("valid legacy additive migration rejected: %v", err)
	}

	reclassified := legacy.legacy.TokenUsage
	reclassified.CachedInputTokens++
	invalidMigration := ReviewAccounting{
		SnapshotAt: snapshot,
		Models: []accounting.ModelAccounting{{
			ModelUsage: accounting.ModelUsage{Model: "", TokenUsage: reclassified},
		}},
		TotalTokens: legacy.legacy.TokenUsage.TotalTokens,
	}
	if err := ValidateReviewAccounting(invalidMigration); err != nil {
		t.Fatalf("candidate must be valid in isolation to exercise transition: %v", err)
	}
	if err := validateReviewAccountingTransition(legacy, invalidMigration); err == nil {
		t.Fatal("legacy migration accepted cached-token reclassification with no additive input delta")
	}
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
		SnapshotAt: snapshotAt(t),
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
		SnapshotAt:      at,
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
		SnapshotAt: at,
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

func snapshotAt(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
}
