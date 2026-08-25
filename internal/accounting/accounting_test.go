package accounting

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/session"
)

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
	withinTolerance.TotalCostUSD += 1e-9
	withinTolerance.Models[0].TokenSharePct += 1e-6
	if err := ValidateProjectSummary(withinTolerance, sessions); err != nil {
		t.Fatalf("values at tolerance boundary rejected: %v", err)
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
