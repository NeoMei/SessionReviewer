package accounting

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/session"
)

func TestPriceUsageAppliesEachRateOnceAndDoesNotChargeReasoningTwice(t *testing.T) {
	usage := TokenUsage{
		InputTokens:           1_000,
		CachedInputTokens:     200,
		CacheWriteInputTokens: 100,
		OutputTokens:          100,
		ReasoningOutputTokens: 75,
		TotalTokens:           1_100,
	}
	pricing := Pricing{
		Currency:                  "USD",
		InputPerMillion:           2,
		CachedInputPerMillion:     .5,
		CacheWriteInputPerMillion: 3,
		OutputPerMillion:          10,
		Source:                    "https://example.com/pricing",
		AsOf:                      "2026-08-29",
	}

	got, err := PriceUsage(usage, pricing)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got-.0028) > 1e-15 {
		t.Fatalf("cost=%0.15f want=0.0028", got)
	}
}

func TestPriceUsageRejectsUnsafeUsagePricingAndArithmetic(t *testing.T) {
	validUsage := TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
	validPricing := Pricing{Currency: "USD", InputPerMillion: 1, OutputPerMillion: 1, Source: "https://example.com/pricing", AsOf: "2026-08-29"}

	tests := []struct {
		name    string
		usage   TokenUsage
		pricing Pricing
		want    string
	}{
		{name: "negative tokens", usage: TokenUsage{InputTokens: -1}, pricing: validPricing, want: "token count"},
		{name: "unsafe integer", usage: TokenUsage{InputTokens: 1 << 53, TotalTokens: 1 << 53}, pricing: validPricing, want: "safe integer"},
		{name: "cached exceeds input", usage: TokenUsage{InputTokens: 1, CachedInputTokens: 2, TotalTokens: 1}, pricing: validPricing, want: "cached"},
		{name: "wrong total", usage: TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 1}, pricing: validPricing, want: "total tokens"},
		{name: "nonfinite rate", usage: validUsage, pricing: Pricing{Currency: "USD", InputPerMillion: math.NaN(), Source: "https://example.com/pricing", AsOf: "2026-08-29"}, want: "finite"},
		{name: "invalid source", usage: validUsage, pricing: Pricing{Currency: "USD", InputPerMillion: 1, Source: "http://example.com/pricing", AsOf: "2026-08-29"}, want: "HTTPS"},
		{name: "invalid as of", usage: validUsage, pricing: Pricing{Currency: "USD", InputPerMillion: 1, Source: "https://example.com/pricing", AsOf: "29-08-2026"}, want: "YYYY-MM-DD"},
		{name: "multiplication overflow", usage: validUsage, pricing: Pricing{Currency: "USD", InputPerMillion: math.MaxFloat64, OutputPerMillion: math.MaxFloat64, Source: "https://example.com/pricing", AsOf: "2026-08-29"}, want: "overflows"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := PriceUsage(test.usage, test.pricing); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("PriceUsage() error=%v want substring %q", err, test.want)
			}
		})
	}
}

func TestAccumulatorAttributesUsageAcrossModelsAndValidatesCost(t *testing.T) {
	started := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	accumulator := NewAccumulator(started)
	records := []session.Record{
		{Line: 1, Timestamp: "2026-08-24T10:01:00Z", Type: "turn_context", Payload: json.RawMessage(`{"model":"gpt-5.6-sol"}`)},
		{Line: 2, Timestamp: "2026-08-24T10:02:00Z", Type: "event_msg", Payload: json.RawMessage(`{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000,"cached_input_tokens":400,"cache_write_input_tokens":100,"output_tokens":200,"reasoning_output_tokens":50,"total_tokens":1200}}}`)},
		{Line: 3, Timestamp: "2026-08-24T10:03:00Z", Type: "turn_context", Payload: json.RawMessage(`{"model":"gpt-5.6-luna"}`)},
		{Line: 4, Timestamp: "2026-08-24T10:04:00Z", Type: "event_msg", Payload: json.RawMessage(`{"type":"token_count","info":{"last_token_usage":{"input_tokens":500,"cached_input_tokens":100,"cache_write_input_tokens":0,"output_tokens":50,"reasoning_output_tokens":10,"total_tokens":550}}}`)},
	}
	for _, record := range records {
		if err := accumulator.Observe(record); err != nil {
			t.Fatal(err)
		}
	}
	usage := accumulator.Snapshot()
	if usage.DurationMS != 240000 || usage.TotalTokens != 1750 || len(usage.Models) != 2 {
		t.Fatalf("usage=%+v", usage)
	}
	report := &SessionAccounting{StartedAt: usage.StartedAt, EndedAt: usage.EndedAt, DurationMS: usage.DurationMS, TotalTokens: usage.TotalTokens}
	for _, model := range usage.Models {
		pricing := Pricing{Currency: "USD", InputPerMillion: 4, CachedInputPerMillion: .4, CacheWriteInputPerMillion: 5, OutputPerMillion: 20, Source: "https://platform.openai.com/pricing", AsOf: "2026-08-24"}
		uncached := model.InputTokens - model.CachedInputTokens - model.CacheWriteInputTokens
		cost := (float64(uncached)*4 + float64(model.CachedInputTokens)*.4 + float64(model.CacheWriteInputTokens)*5 + float64(model.OutputTokens)*20) / 1_000_000
		report.Models = append(report.Models, ModelAccounting{ModelUsage: model, Pricing: pricing, CostUSD: cost})
		report.TotalCostUSD += cost
	}
	if err := ValidateSessionAccounting(report, usage); err != nil {
		t.Fatal(err)
	}
	report.TotalCostUSD++
	if err := ValidateSessionAccounting(report, usage); err == nil {
		t.Fatal("accepted incorrect total cost")
	}
}

func TestAccumulatorIgnoresContextOnlyTokenHeartbeatWhenCumulativeUsageIsUnchanged(t *testing.T) {
	started := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	accumulator := NewAccumulator(started)
	records := []session.Record{
		{Line: 1, Timestamp: "2026-08-24T10:01:00Z", Type: "turn_context", Payload: json.RawMessage(`{"model":"gpt-5.6-sol"}`)},
		{Line: 2, Timestamp: "2026-08-24T10:02:00Z", Type: "event_msg", Payload: json.RawMessage(`{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0,"total_tokens":15},"total_token_usage":{"input_tokens":10,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0,"total_tokens":15}}}`)},
		{Line: 3, Timestamp: "2026-08-24T10:03:00Z", Type: "event_msg", Payload: json.RawMessage(`{"type":"token_count","info":{"last_token_usage":{"input_tokens":0,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0,"total_tokens":2048},"total_token_usage":{"input_tokens":10,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0,"total_tokens":15}}}`)},
	}
	for _, record := range records {
		if err := accumulator.Observe(record); err != nil {
			t.Fatal(err)
		}
	}
	usage := accumulator.Snapshot()
	if usage.TotalTokens != 15 || len(usage.Models) != 1 || usage.Models[0].TotalTokens != 15 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestAccumulatorRejectsContextOnlyTokenHeartbeatWithoutMatchingCumulativeUsage(t *testing.T) {
	started := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	accumulator := NewAccumulator(started)
	record := session.Record{Line: 1, Timestamp: "2026-08-24T10:01:00Z", Type: "event_msg", Payload: json.RawMessage(`{"type":"token_count","info":{"last_token_usage":{"input_tokens":0,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0,"total_tokens":2048},"total_token_usage":{"input_tokens":10,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0,"total_tokens":15}}}`)}
	if err := accumulator.Observe(record); err == nil {
		t.Fatal("accepted aggregate-only token event without a matching prior cumulative snapshot")
	}
}

func TestAccumulatorAcceptsHostTokenCountWhenReasoningExceedsVisibleOutput(t *testing.T) {
	started := time.Date(2026, 8, 30, 8, 20, 49, 0, time.UTC)
	accumulator := NewAccumulator(started)
	record := session.Record{
		Line:      16,
		Timestamp: "2026-08-30T08:20:49.025Z",
		Type:      "event_msg",
		Payload:   json.RawMessage(`{"type":"token_count","info":{"total_token_usage":{"input_tokens":36483,"cached_input_tokens":128,"cache_write_input_tokens":0,"output_tokens":210,"reasoning_output_tokens":376,"total_tokens":36693},"last_token_usage":{"input_tokens":36483,"cached_input_tokens":128,"cache_write_input_tokens":0,"output_tokens":210,"reasoning_output_tokens":376,"total_tokens":36693}}}`),
	}
	if err := accumulator.Observe(record); err != nil {
		t.Fatalf("observe host token_count: %v", err)
	}
	usage := accumulator.Snapshot()
	if usage.TotalTokens != 36693 || len(usage.Models) != 1 {
		t.Fatalf("usage=%+v", usage)
	}
	got := usage.Models[0].TokenUsage
	want := TokenUsage{InputTokens: 36483, CachedInputTokens: 128, OutputTokens: 210, ReasoningOutputTokens: 376, TotalTokens: 36693}
	if got != want {
		t.Fatalf("model usage=%+v want %+v", got, want)
	}
	cost, err := PriceUsage(got, Pricing{Currency: "USD", InputPerMillion: 1, CachedInputPerMillion: 0, CacheWriteInputPerMillion: 0, OutputPerMillion: 1, Source: "https://example.com/pricing", AsOf: "2026-08-30"})
	if err != nil {
		t.Fatalf("price host token_count: %v", err)
	}
	if cost <= 0 {
		t.Fatalf("cost=%v", cost)
	}
}

func TestFormatDurationMS(t *testing.T) {
	if got := FormatDurationMS(90_061_007); got != "1d 1h 1m 1s 7ms" {
		t.Fatalf("duration=%q", got)
	}
	if got := FormatDurationMS(0); got != "0ms" {
		t.Fatalf("zero=%q", got)
	}
}

func TestAggregateComputesModelSharesAndRejectsOverflow(t *testing.T) {
	first := validAggregateSession("a", "2026-08-25T00:00:00Z", "2026-08-25T00:00:01Z", 100, 1)
	second := validAggregateSession("b", "2026-08-25T00:00:00Z", "2026-08-25T00:00:02Z", 300, 3)
	summary, err := Aggregate([]*SessionAccounting{first, second})
	if err != nil || summary.TotalDurationMS != 3000 || summary.TotalTokens != 400 || summary.TotalCostUSD != 4 || len(summary.Models) != 2 || summary.Models[0].TokenSharePct != 25 || summary.Models[1].CostSharePct != 75 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	if _, err := Aggregate([]*SessionAccounting{{DurationMS: math.MaxInt64}, {DurationMS: 1}}); err == nil {
		t.Fatal("accepted overflowing project duration")
	}
}

func TestValidateProjectSummaryRecomputesEveryModelWithExplicitTolerances(t *testing.T) {
	sessions := []*SessionAccounting{
		validAggregateSession("a", "2026-08-25T00:00:00Z", "2026-08-25T00:00:01Z", 100, 1),
		validAggregateSession("b", "2026-08-25T00:00:00Z", "2026-08-25T00:00:02Z", 300, 3),
	}
	valid, err := Aggregate(sessions)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateProjectSummary(valid, sessions); err != nil {
		t.Fatalf("valid summary rejected: %v", err)
	}

	tests := map[string]func(*ProjectSummary){
		"total tokens": func(value *ProjectSummary) { value.TotalTokens++ },
		"total cost":   func(value *ProjectSummary) { value.TotalCostUSD += 1.0001e-9 },
		"model tokens": func(value *ProjectSummary) { value.Models[0].TotalTokens++ },
		"model cost":   func(value *ProjectSummary) { value.Models[0].TotalCostUSD += 1.0001e-9 },
		"token share":  func(value *ProjectSummary) { value.Models[0].TokenSharePct += 1.0001e-6 },
		"cost share":   func(value *ProjectSummary) { value.Models[0].CostSharePct += 1.0001e-6 },
		"nonfinite":    func(value *ProjectSummary) { value.Models[0].TotalCostUSD = math.NaN() },
		"negative":     func(value *ProjectSummary) { value.Models[0].TotalTokens = -1 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := valid
			changed.Models = append([]ProjectModelSummary(nil), valid.Models...)
			mutate(&changed)
			if err := ValidateProjectSummary(changed, sessions); err == nil {
				t.Fatalf("accepted mismatched summary: %+v", changed)
			}
		})
	}

	withinTolerance := valid
	withinTolerance.Models = append([]ProjectModelSummary(nil), valid.Models...)
	withinTolerance.TotalCostUSD = math.Nextafter(valid.TotalCostUSD+1e-9, valid.TotalCostUSD)
	withinTolerance.Models[0].TokenSharePct = math.Nextafter(valid.Models[0].TokenSharePct+1e-6, valid.Models[0].TokenSharePct)
	if err := ValidateProjectSummary(withinTolerance, sessions); err != nil {
		t.Fatalf("values at tolerance boundary rejected: %v", err)
	}
}

func TestAbsoluteToleranceUsesExactInclusiveNextafterBoundary(t *testing.T) {
	for _, tolerance := range []float64{1e-9, 1e-6} {
		if !withinAbsoluteTolerance(0, tolerance, tolerance) {
			t.Fatalf("exact tolerance %g was rejected", tolerance)
		}
		outside := math.Nextafter(tolerance, math.Inf(1))
		if withinAbsoluteTolerance(0, outside, tolerance) {
			t.Fatalf("nextafter outside tolerance accepted: tolerance=%g outside=%g", tolerance, outside)
		}
	}
}

func TestAggregateRejectsStoredSessionMismatchAndInvalidNumbers(t *testing.T) {
	valid := validAggregateSession("a", "2026-08-25T00:00:00Z", "2026-08-25T00:00:01Z", 100, 1)
	tests := map[string]func(*SessionAccounting){
		"session token mismatch": func(value *SessionAccounting) { value.TotalTokens++ },
		"session cost mismatch":  func(value *SessionAccounting) { value.TotalCostUSD += .01 },
		"negative duration":      func(value *SessionAccounting) { value.DurationMS = -1 },
		"nonfinite model cost":   func(value *SessionAccounting) { value.Models[0].CostUSD = math.Inf(1) },
		"negative model token":   func(value *SessionAccounting) { value.Models[0].InputTokens = -1 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := *valid
			changed.Models = append([]ModelAccounting(nil), valid.Models...)
			mutate(&changed)
			if _, err := Aggregate([]*SessionAccounting{&changed}); err == nil {
				t.Fatalf("accepted invalid stored accounting: %+v", changed)
			}
		})
	}
}

func validAggregateSession(model, started, ended string, tokens int64, cost float64) *SessionAccounting {
	start, _ := time.Parse(time.RFC3339Nano, started)
	end, _ := time.Parse(time.RFC3339Nano, ended)
	pricing := Pricing{
		Currency:        "USD",
		InputPerMillion: 10_000,
		Source:          "https://example.com/pricing",
		AsOf:            "2026-08-25",
	}
	return &SessionAccounting{
		StartedAt:   started,
		EndedAt:     ended,
		DurationMS:  end.Sub(start).Milliseconds(),
		TotalTokens: tokens,
		Models: []ModelAccounting{{
			ModelUsage: ModelUsage{Model: model, TokenUsage: TokenUsage{InputTokens: tokens, TotalTokens: tokens}},
			Pricing:    pricing,
			CostUSD:    cost,
		}},
		TotalCostUSD: cost,
	}
}
