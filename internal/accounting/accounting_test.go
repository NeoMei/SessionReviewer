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
	first := &SessionAccounting{DurationMS: 1000, TotalTokens: 100, TotalCostUSD: 1, Models: []ModelAccounting{{ModelUsage: ModelUsage{Model: "a", TokenUsage: TokenUsage{TotalTokens: 100}}, CostUSD: 1}}}
	second := &SessionAccounting{DurationMS: 2000, TotalTokens: 300, TotalCostUSD: 3, Models: []ModelAccounting{{ModelUsage: ModelUsage{Model: "b", TokenUsage: TokenUsage{TotalTokens: 300}}, CostUSD: 3}}}
	summary, err := Aggregate([]*SessionAccounting{first, second})
	if err != nil || summary.TotalDurationMS != 3000 || summary.TotalTokens != 400 || summary.TotalCostUSD != 4 || len(summary.Models) != 2 || summary.Models[0].TokenSharePct != 25 || summary.Models[1].CostSharePct != 75 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	if _, err := Aggregate([]*SessionAccounting{{DurationMS: math.MaxInt64}, {DurationMS: 1}}); err == nil {
		t.Fatal("accepted overflowing project duration")
	}
}
