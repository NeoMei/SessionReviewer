package accounting

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/session"
)

const maxSafeInteger = 1<<53 - 1

type TokenUsage struct {
	InputTokens           int64 `json:"input_tokens" yaml:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens" yaml:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens" yaml:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens" yaml:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens" yaml:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens" yaml:"total_tokens"`
}

type ModelUsage struct {
	Model      string `json:"model" yaml:"model"`
	TokenUsage `json:",inline" yaml:",inline"`
}

type SessionUsage struct {
	StartedAt   string       `json:"started_at" yaml:"started_at"`
	EndedAt     string       `json:"ended_at" yaml:"ended_at"`
	DurationMS  int64        `json:"duration_ms" yaml:"duration_ms"`
	Models      []ModelUsage `json:"models" yaml:"models"`
	TotalTokens int64        `json:"total_tokens" yaml:"total_tokens"`
}

type Pricing struct {
	Currency                  string  `json:"currency" yaml:"currency"`
	InputPerMillion           float64 `json:"input_per_million" yaml:"input_per_million"`
	CachedInputPerMillion     float64 `json:"cached_input_per_million" yaml:"cached_input_per_million"`
	CacheWriteInputPerMillion float64 `json:"cache_write_input_per_million" yaml:"cache_write_input_per_million"`
	OutputPerMillion          float64 `json:"output_per_million" yaml:"output_per_million"`
	Source                    string  `json:"source" yaml:"source"`
	AsOf                      string  `json:"as_of" yaml:"as_of"`
}

type ModelAccounting struct {
	ModelUsage `json:",inline" yaml:",inline"`
	Pricing    Pricing `json:"pricing" yaml:"pricing"`
	CostUSD    float64 `json:"cost_usd" yaml:"cost_usd"`
}

type SessionAccounting struct {
	StartedAt    string            `json:"started_at" yaml:"started_at"`
	EndedAt      string            `json:"ended_at" yaml:"ended_at"`
	DurationMS   int64             `json:"duration_ms" yaml:"duration_ms"`
	Models       []ModelAccounting `json:"models" yaml:"models"`
	TotalTokens  int64             `json:"total_tokens" yaml:"total_tokens"`
	TotalCostUSD float64           `json:"total_cost_usd" yaml:"total_cost_usd"`
}

type ProjectModelSummary struct {
	Model         string  `json:"model" yaml:"model"`
	TotalTokens   int64   `json:"total_tokens" yaml:"total_tokens"`
	TotalCostUSD  float64 `json:"total_cost_usd" yaml:"total_cost_usd"`
	TokenSharePct float64 `json:"token_share_pct" yaml:"token_share_pct"`
	CostSharePct  float64 `json:"cost_share_pct" yaml:"cost_share_pct"`
}

type ProjectSummary struct {
	TotalDurationMS int64                 `json:"total_duration_ms" yaml:"total_duration_ms"`
	TotalTokens     int64                 `json:"total_tokens" yaml:"total_tokens"`
	TotalCostUSD    float64               `json:"total_cost_usd" yaml:"total_cost_usd"`
	Models          []ProjectModelSummary `json:"models" yaml:"models"`
}

func FormatDurationMS(value int64) string {
	if value < 0 {
		return "invalid"
	}
	remaining := value
	days := remaining / 86_400_000
	remaining %= 86_400_000
	hours := remaining / 3_600_000
	remaining %= 3_600_000
	minutes := remaining / 60_000
	remaining %= 60_000
	seconds := remaining / 1_000
	millis := remaining % 1_000
	parts := make([]string, 0, 5)
	if days > 0 {
		parts = append(parts, strconv.FormatInt(days, 10)+"d")
	}
	if hours > 0 {
		parts = append(parts, strconv.FormatInt(hours, 10)+"h")
	}
	if minutes > 0 {
		parts = append(parts, strconv.FormatInt(minutes, 10)+"m")
	}
	if seconds > 0 {
		parts = append(parts, strconv.FormatInt(seconds, 10)+"s")
	}
	if millis > 0 || len(parts) == 0 {
		parts = append(parts, strconv.FormatInt(millis, 10)+"ms")
	}
	return strings.Join(parts, " ")
}

func Aggregate(sessions []*SessionAccounting) (ProjectSummary, error) {
	type modelTotal struct {
		tokens int64
		cost   float64
	}
	byModel := make(map[string]modelTotal)
	var result ProjectSummary
	for _, session := range sessions {
		if session == nil {
			continue
		}
		if session.DurationMS > math.MaxInt64-result.TotalDurationMS || session.TotalTokens > math.MaxInt64-result.TotalTokens {
			return ProjectSummary{}, errors.New("project accounting totals overflow")
		}
		result.TotalDurationMS += session.DurationMS
		result.TotalTokens += session.TotalTokens
		result.TotalCostUSD += session.TotalCostUSD
		if math.IsNaN(result.TotalCostUSD) || math.IsInf(result.TotalCostUSD, 0) {
			return ProjectSummary{}, errors.New("project accounting cost overflows")
		}
		for _, model := range session.Models {
			value := byModel[model.Model]
			if model.TotalTokens > math.MaxInt64-value.tokens {
				return ProjectSummary{}, errors.New("project model token total overflows")
			}
			value.tokens += model.TotalTokens
			value.cost += model.CostUSD
			if math.IsNaN(value.cost) || math.IsInf(value.cost, 0) {
				return ProjectSummary{}, errors.New("project model cost overflows")
			}
			byModel[model.Model] = value
		}
	}
	for model, value := range byModel {
		item := ProjectModelSummary{Model: model, TotalTokens: value.tokens, TotalCostUSD: value.cost}
		if result.TotalTokens > 0 {
			item.TokenSharePct = float64(value.tokens) * 100 / float64(result.TotalTokens)
		}
		if result.TotalCostUSD > 0 {
			item.CostSharePct = value.cost * 100 / result.TotalCostUSD
		}
		result.Models = append(result.Models, item)
	}
	sort.Slice(result.Models, func(i, j int) bool { return result.Models[i].Model < result.Models[j].Model })
	return result, nil
}

type Accumulator struct {
	started     time.Time
	ended       time.Time
	activeModel string
	models      map[string]TokenUsage
	lastTotal   *TokenUsage
}

func NewAccumulator(started time.Time) *Accumulator {
	return &Accumulator{started: started, ended: started, activeModel: "unknown", models: make(map[string]TokenUsage)}
}

func (a *Accumulator) Observe(record session.Record) error {
	if a == nil {
		return errors.New("accounting accumulator is required")
	}
	if record.Timestamp != "" {
		parsed, err := time.Parse(time.RFC3339Nano, record.Timestamp)
		if err != nil {
			return fmt.Errorf("invalid accounting timestamp at line %d", record.Line)
		}
		if a.started.IsZero() {
			a.started = parsed
		}
		if parsed.After(a.ended) {
			a.ended = parsed
		}
	}
	if record.Type == "turn_context" {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return errors.New("malformed turn_context accounting payload")
		}
		if strings.TrimSpace(payload.Model) != "" {
			a.activeModel = payload.Model
		}
		return nil
	}
	if record.Type != "event_msg" {
		return nil
	}
	var payload struct {
		Type string `json:"type"`
		Info *struct {
			Last  *TokenUsage `json:"last_token_usage"`
			Total *TokenUsage `json:"total_token_usage"`
		} `json:"info"`
	}
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		return errors.New("malformed event accounting payload")
	}
	if payload.Type != "token_count" {
		return nil
	}
	if payload.Info == nil || payload.Info.Last == nil {
		return errors.New("token_count omits last_token_usage")
	}
	usage := *payload.Info.Last
	if payload.Info.Total != nil {
		if err := ValidateTokenUsage(*payload.Info.Total); err != nil {
			return fmt.Errorf("invalid cumulative token_count at line %d: %w", record.Line, err)
		}
	}
	if isContextOnlyTokenHeartbeat(usage) {
		if a.lastTotal == nil || payload.Info.Total == nil || *a.lastTotal != *payload.Info.Total {
			return fmt.Errorf("invalid token_count at line %d: aggregate-only usage lacks an unchanged cumulative snapshot", record.Line)
		}
		return nil
	}
	if err := ValidateTokenUsage(usage); err != nil {
		return fmt.Errorf("invalid token_count at line %d: %w", record.Line, err)
	}
	current := a.models[a.activeModel]
	if err := addUsage(&current, usage); err != nil {
		return err
	}
	a.models[a.activeModel] = current
	if payload.Info.Total != nil {
		copy := *payload.Info.Total
		a.lastTotal = &copy
	}
	return nil
}

func isContextOnlyTokenHeartbeat(value TokenUsage) bool {
	return value.InputTokens == 0 && value.CachedInputTokens == 0 && value.CacheWriteInputTokens == 0 && value.OutputTokens == 0 && value.ReasoningOutputTokens == 0 && value.TotalTokens > 0
}

func (a *Accumulator) Snapshot() *SessionUsage {
	if a == nil {
		return nil
	}
	result := &SessionUsage{Models: make([]ModelUsage, 0, len(a.models))}
	if !a.started.IsZero() {
		result.StartedAt = a.started.Format(time.RFC3339Nano)
	}
	if !a.ended.IsZero() {
		result.EndedAt = a.ended.Format(time.RFC3339Nano)
	}
	if !a.started.IsZero() && !a.ended.Before(a.started) {
		result.DurationMS = a.ended.Sub(a.started).Milliseconds()
	}
	for model, usage := range a.models {
		result.Models = append(result.Models, ModelUsage{Model: model, TokenUsage: usage})
		result.TotalTokens += usage.TotalTokens
	}
	sort.Slice(result.Models, func(i, j int) bool { return result.Models[i].Model < result.Models[j].Model })
	return result
}

func ValidateTokenUsage(value TokenUsage) error {
	values := []int64{value.InputTokens, value.CachedInputTokens, value.CacheWriteInputTokens, value.OutputTokens, value.ReasoningOutputTokens, value.TotalTokens}
	for _, item := range values {
		if item < 0 || item > maxSafeInteger {
			return errors.New("token count is negative or exceeds the safe integer range")
		}
	}
	if value.CachedInputTokens+value.CacheWriteInputTokens > value.InputTokens {
		return errors.New("cached and cache-write tokens exceed input tokens")
	}
	if value.ReasoningOutputTokens > value.OutputTokens {
		return errors.New("reasoning tokens exceed output tokens")
	}
	if value.TotalTokens != value.InputTokens+value.OutputTokens {
		return errors.New("total tokens do not equal input plus output")
	}
	return nil
}

func addUsage(target *TokenUsage, value TokenUsage) error {
	fields := []*int64{&target.InputTokens, &target.CachedInputTokens, &target.CacheWriteInputTokens, &target.OutputTokens, &target.ReasoningOutputTokens, &target.TotalTokens}
	adds := []int64{value.InputTokens, value.CachedInputTokens, value.CacheWriteInputTokens, value.OutputTokens, value.ReasoningOutputTokens, value.TotalTokens}
	for i := range fields {
		if adds[i] > maxSafeInteger-*fields[i] {
			return errors.New("cumulative token count exceeds safe integer range")
		}
		*fields[i] += adds[i]
	}
	return nil
}

func ValidateSessionAccounting(report *SessionAccounting, usage *SessionUsage) error {
	if report == nil || usage == nil {
		return errors.New("session accounting and packet usage are required")
	}
	if err := ValidateSessionUsage(usage); err != nil {
		return err
	}
	if report.StartedAt != usage.StartedAt || report.EndedAt != usage.EndedAt || report.DurationMS != usage.DurationMS || report.TotalTokens != usage.TotalTokens || len(report.Models) != len(usage.Models) {
		return errors.New("session accounting does not match packet usage")
	}
	var total float64
	for index, model := range report.Models {
		want := usage.Models[index]
		if model.ModelUsage != want {
			return errors.New("model accounting does not match packet usage")
		}
		if err := validatePricing(model.Pricing); err != nil {
			return fmt.Errorf("model %q pricing: %w", model.Model, err)
		}
		uncached := model.InputTokens - model.CachedInputTokens - model.CacheWriteInputTokens
		cost := (float64(uncached)*model.Pricing.InputPerMillion + float64(model.CachedInputTokens)*model.Pricing.CachedInputPerMillion + float64(model.CacheWriteInputTokens)*model.Pricing.CacheWriteInputPerMillion + float64(model.OutputTokens)*model.Pricing.OutputPerMillion) / 1_000_000
		if !nearlyEqual(cost, model.CostUSD) {
			return fmt.Errorf("model %q cost does not match per-million pricing", model.Model)
		}
		total += cost
	}
	if !nearlyEqual(total, report.TotalCostUSD) {
		return errors.New("total session cost does not equal model costs")
	}
	return nil
}

func ValidateStoredSessionAccounting(report *SessionAccounting) error {
	if report == nil {
		return nil
	}
	usage := &SessionUsage{StartedAt: report.StartedAt, EndedAt: report.EndedAt, DurationMS: report.DurationMS, Models: make([]ModelUsage, len(report.Models)), TotalTokens: report.TotalTokens}
	for index, model := range report.Models {
		usage.Models[index] = model.ModelUsage
	}
	return ValidateSessionAccounting(report, usage)
}

func ValidateSessionUsage(usage *SessionUsage) error {
	if usage == nil || usage.DurationMS < 0 || usage.DurationMS > maxSafeInteger || usage.Models == nil {
		return errors.New("invalid session usage")
	}
	started, err := time.Parse(time.RFC3339Nano, usage.StartedAt)
	if err != nil {
		return errors.New("invalid session usage start time")
	}
	ended, err := time.Parse(time.RFC3339Nano, usage.EndedAt)
	if err != nil || ended.Before(started) || ended.Sub(started).Milliseconds() != usage.DurationMS {
		return errors.New("invalid session usage duration")
	}
	var total int64
	previous := ""
	for _, model := range usage.Models {
		if strings.TrimSpace(model.Model) == "" || (previous != "" && model.Model <= previous) {
			return errors.New("session usage models must be unique and sorted")
		}
		if err := ValidateTokenUsage(model.TokenUsage); err != nil {
			return err
		}
		if model.TotalTokens > maxSafeInteger-total {
			return errors.New("session total tokens exceed safe integer range")
		}
		total += model.TotalTokens
		previous = model.Model
	}
	if total != usage.TotalTokens {
		return errors.New("session total tokens do not equal model totals")
	}
	return nil
}

func validatePricing(value Pricing) error {
	if value.Currency != "USD" || strings.TrimSpace(value.Source) == "" || strings.TrimSpace(value.AsOf) == "" {
		return errors.New("currency, source, and as_of are required")
	}
	parsed, err := url.Parse(value.Source)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("source must be an HTTPS pricing URL without credentials")
	}
	date, err := time.Parse("2006-01-02", value.AsOf)
	if err != nil || date.Format("2006-01-02") != value.AsOf {
		return errors.New("as_of must use YYYY-MM-DD")
	}
	for _, rate := range []float64{value.InputPerMillion, value.CachedInputPerMillion, value.CacheWriteInputPerMillion, value.OutputPerMillion} {
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
			return errors.New("rates must be finite and nonnegative")
		}
	}
	return nil
}

func nearlyEqual(left, right float64) bool {
	return math.Abs(left-right) <= 1e-9*math.Max(1, math.Max(math.Abs(left), math.Abs(right)))
}
