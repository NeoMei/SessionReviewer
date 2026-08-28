package reviewjob

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/agent"
)

const reviewAccountingMaxSafeInteger = 1<<53 - 1

// PricingResolver supplies an authoritative public list price for the exact
// provider-reported model at the review-run time. A false result means no cost
// may be inferred for that model.
type PricingResolver interface {
	Resolve(model string, at time.Time) (accounting.Pricing, bool)
}

// ReviewAccounting is private accounting for Agent review runs. It is
// deliberately separate from source-session accounting in the machine ledger.
// TotalCostUSD is absent whenever any actual run lacks authoritative pricing.
type ReviewAccounting struct {
	SnapshotAt      time.Time                    `json:"-"`
	Models          []accounting.ModelAccounting `json:"models"`
	TotalTokens     int64                        `json:"total_tokens"`
	TotalCostUSD    *float64                     `json:"total_cost_usd,omitempty"`
	PricingComplete bool                         `json:"pricing_complete"`

	legacy *legacyReviewUsage
}

type legacyReviewUsage struct {
	TokenUsage accounting.TokenUsage `json:"token_usage"`
	CostUSD    float64               `json:"cost_usd"`
}

type reviewAccountingWire struct {
	SnapshotAt      *time.Time                   `json:"snapshot_at,omitempty"`
	Models          []accounting.ModelAccounting `json:"models"`
	TotalTokens     int64                        `json:"total_tokens"`
	TotalCostUSD    *float64                     `json:"total_cost_usd,omitempty"`
	PricingComplete bool                         `json:"pricing_complete"`
}

func (value ReviewAccounting) MarshalJSON() ([]byte, error) {
	if value.legacy != nil {
		return json.Marshal(*value.legacy)
	}
	var snapshot *time.Time
	if !value.SnapshotAt.IsZero() {
		copy := value.SnapshotAt
		snapshot = &copy
	}
	return json.Marshal(reviewAccountingWire{
		SnapshotAt:      snapshot,
		Models:          value.Models,
		TotalTokens:     value.TotalTokens,
		TotalCostUSD:    value.TotalCostUSD,
		PricingComplete: value.PricingComplete,
	})
}

func (value *ReviewAccounting) UnmarshalJSON(body []byte) error {
	if value == nil {
		return errors.New("review accounting destination is required")
	}
	if err := rejectDuplicateJSONFields(body); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	_, hasLegacyUsage := fields["token_usage"]
	_, hasLegacyCost := fields["cost_usd"]
	if hasLegacyUsage || hasLegacyCost {
		if len(fields) != 2 || !hasLegacyUsage || !hasLegacyCost {
			return errors.New("legacy review accounting shape is incomplete or mixed")
		}
		var legacy legacyReviewUsage
		if err := json.Unmarshal(body, &legacy); err != nil {
			return err
		}
		*value = ReviewAccounting{legacy: &legacy}
		return nil
	}
	allowed := map[string]bool{"snapshot_at": true, "models": true, "total_tokens": true, "total_cost_usd": true, "pricing_complete": true}
	for name := range fields {
		if !allowed[name] {
			return fmt.Errorf("unknown review accounting field %q", name)
		}
	}
	for _, required := range []string{"models", "total_tokens", "pricing_complete"} {
		if _, ok := fields[required]; !ok {
			return fmt.Errorf("review accounting field %q is required", required)
		}
	}
	var wire reviewAccountingWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return err
	}
	*value = ReviewAccounting{
		Models:          wire.Models,
		TotalTokens:     wire.TotalTokens,
		TotalCostUSD:    wire.TotalCostUSD,
		PricingComplete: wire.PricingComplete,
	}
	if wire.SnapshotAt != nil {
		value.SnapshotAt = *wire.SnapshotAt
	}
	return nil
}

// AddReviewResult returns a new aggregate containing one actual Agent result.
// It never aliases or mutates current. Model is used exactly as reported; an
// empty or unresolved model retains usage and makes the aggregate unpriced.
func AddReviewResult(current ReviewAccounting, result agent.Result, at time.Time, resolver PricingResolver) (ReviewAccounting, error) {
	if !canonicalReviewSnapshot(at) {
		return ReviewAccounting{}, errors.New("review accounting time must be canonical UTC")
	}
	if err := ValidateReviewAccounting(current); err != nil {
		return ReviewAccounting{}, fmt.Errorf("invalid current review accounting: %w", err)
	}
	if err := accounting.ValidateTokenUsage(result.Usage); err != nil {
		return ReviewAccounting{}, fmt.Errorf("invalid review usage: %w", err)
	}

	next := cloneReviewAccounting(current)
	if next.legacy != nil {
		next = migrateLegacyReviewAccounting(next, at)
	}
	if next.SnapshotAt.IsZero() {
		next.SnapshotAt = at
	} else if !next.SnapshotAt.Equal(at) {
		return ReviewAccounting{}, errors.New("review accounting snapshot cannot change within a job")
	}
	pricing, priced := accounting.Pricing{}, false
	var cost float64
	if strings.TrimSpace(result.Model) != "" && resolver != nil {
		pricing, priced = resolver.Resolve(result.Model, next.SnapshotAt)
		if priced {
			var err error
			cost, err = accounting.PriceUsage(result.Usage, pricing)
			if err != nil {
				return ReviewAccounting{}, fmt.Errorf("model %q pricing: %w", result.Model, err)
			}
		}
	}

	index := sort.Search(len(next.Models), func(index int) bool {
		return next.Models[index].Model >= result.Model
	})
	if index < len(next.Models) && next.Models[index].Model == result.Model {
		row := next.Models[index]
		existingPriced := row.Pricing != (accounting.Pricing{})
		if existingPriced != priced || (priced && row.Pricing != pricing) {
			return ReviewAccounting{}, fmt.Errorf("model %q pricing resolution changed during aggregation", result.Model)
		}
		combined, err := addReviewTokenUsage(row.TokenUsage, result.Usage)
		if err != nil {
			return ReviewAccounting{}, fmt.Errorf("model %q usage: %w", result.Model, err)
		}
		row.TokenUsage = combined
		if priced {
			row.CostUSD, err = accounting.PriceUsage(combined, pricing)
			if err != nil {
				return ReviewAccounting{}, fmt.Errorf("model %q pricing: %w", result.Model, err)
			}
		}
		next.Models[index] = row
	} else {
		row := accounting.ModelAccounting{
			ModelUsage: accounting.ModelUsage{Model: result.Model, TokenUsage: result.Usage},
		}
		if priced {
			row.Pricing = pricing
			row.CostUSD = cost
		}
		next.Models = append(next.Models, accounting.ModelAccounting{})
		copy(next.Models[index+1:], next.Models[index:])
		next.Models[index] = row
	}

	if err := recomputeReviewTotals(&next); err != nil {
		return ReviewAccounting{}, err
	}
	if err := ValidateReviewAccounting(next); err != nil {
		return ReviewAccounting{}, fmt.Errorf("invalid resulting review accounting: %w", err)
	}
	return next, nil
}

// ValidateReviewAccounting recomputes all private review-run totals. Unknown
// pricing is represented only by a zero Pricing row, zero row cost, an absent
// aggregate cost, and PricingComplete=false.
func ValidateReviewAccounting(value ReviewAccounting) error {
	if value.legacy != nil {
		if !value.SnapshotAt.IsZero() || value.Models != nil || value.TotalTokens != 0 || value.TotalCostUSD != nil || value.PricingComplete {
			return errors.New("legacy review accounting cannot mix with snapshot accounting")
		}
		if err := accounting.ValidateTokenUsage(value.legacy.TokenUsage); err != nil {
			return fmt.Errorf("legacy review usage: %w", err)
		}
		if math.IsNaN(value.legacy.CostUSD) || math.IsInf(value.legacy.CostUSD, 0) || value.legacy.CostUSD < 0 {
			return errors.New("legacy review usage cost must be finite and nonnegative")
		}
		return nil
	}
	if value.TotalTokens < 0 || value.TotalTokens > reviewAccountingMaxSafeInteger {
		return errors.New("review accounting token total is outside the safe integer range")
	}
	if len(value.Models) == 0 {
		if !value.SnapshotAt.IsZero() || value.TotalTokens != 0 || value.TotalCostUSD != nil || value.PricingComplete {
			return errors.New("empty review accounting has nonempty totals")
		}
		return nil
	}
	if !canonicalReviewSnapshot(value.SnapshotAt) {
		return errors.New("nonempty review accounting requires a canonical UTC snapshot")
	}

	var totalTokens int64
	var totalCost float64
	complete := true
	previous := ""
	for index, model := range value.Models {
		if index > 0 && model.Model <= previous {
			return errors.New("review accounting models must be unique and sorted")
		}
		if err := accounting.ValidateTokenUsage(model.TokenUsage); err != nil {
			return fmt.Errorf("model %q usage: %w", model.Model, err)
		}
		if model.TotalTokens > reviewAccountingMaxSafeInteger-totalTokens {
			return errors.New("review accounting token total exceeds the safe integer range")
		}
		totalTokens += model.TotalTokens
		if strings.TrimSpace(model.Model) == "" && model.Pricing != (accounting.Pricing{}) {
			return errors.New("empty authoritative model must remain unpriced")
		}
		if model.Pricing == (accounting.Pricing{}) {
			complete = false
			if math.Float64bits(model.CostUSD) != math.Float64bits(0) {
				return fmt.Errorf("unpriced model %q has a cost", model.Model)
			}
		} else {
			cost, err := accounting.PriceUsage(model.TokenUsage, model.Pricing)
			if err != nil {
				return fmt.Errorf("model %q pricing: %w", model.Model, err)
			}
			if !reviewCostsCanonical(cost, model.CostUSD) {
				return fmt.Errorf("model %q cost does not match usage", model.Model)
			}
			totalCost += cost
			if math.IsNaN(totalCost) || math.IsInf(totalCost, 0) {
				return errors.New("review accounting cost overflows")
			}
		}
		previous = model.Model
	}
	if totalTokens != value.TotalTokens {
		return errors.New("review accounting token total does not equal model totals")
	}
	if complete != value.PricingComplete {
		return errors.New("review accounting pricing completeness does not match model prices")
	}
	if !complete {
		if value.TotalCostUSD != nil {
			return errors.New("incomplete review pricing must omit total cost")
		}
		return nil
	}
	if value.TotalCostUSD == nil || !reviewCostsCanonical(totalCost, *value.TotalCostUSD) {
		return errors.New("review accounting total cost does not equal model costs")
	}
	return nil
}

func cloneReviewAccounting(value ReviewAccounting) ReviewAccounting {
	clone := value
	clone.Models = append([]accounting.ModelAccounting(nil), value.Models...)
	if value.TotalCostUSD != nil {
		cost := *value.TotalCostUSD
		clone.TotalCostUSD = &cost
	}
	if value.legacy != nil {
		legacy := *value.legacy
		clone.legacy = &legacy
	}
	return clone
}

func migrateLegacyReviewAccounting(value ReviewAccounting, at time.Time) ReviewAccounting {
	if value.legacy == nil || value.legacy.TokenUsage == (accounting.TokenUsage{}) {
		return ReviewAccounting{}
	}
	return ReviewAccounting{
		SnapshotAt: at,
		Models: []accounting.ModelAccounting{{
			ModelUsage: accounting.ModelUsage{Model: "", TokenUsage: value.legacy.TokenUsage},
		}},
		TotalTokens: value.legacy.TokenUsage.TotalTokens,
	}
}

func reviewAccountingPublicTotals(value ReviewAccounting) (int64, *float64, bool) {
	if value.legacy != nil {
		return value.legacy.TokenUsage.TotalTokens, nil, false
	}
	if value.TotalCostUSD == nil {
		return value.TotalTokens, nil, value.PricingComplete
	}
	cost := *value.TotalCostUSD
	return value.TotalTokens, &cost, value.PricingComplete
}

func hasReviewAccounting(value ReviewAccounting) bool {
	if value.legacy != nil {
		return value.legacy.TokenUsage != (accounting.TokenUsage{})
	}
	return len(value.Models) != 0
}

func canonicalReviewSnapshot(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Equal(value.UTC())
}

func validateReviewAccountingTransition(before, after ReviewAccounting) error {
	if before.legacy != nil {
		if after.legacy != nil {
			if *before.legacy != *after.legacy {
				return errors.New("legacy review accounting is immutable until migration")
			}
			return nil
		}
		if before.legacy.TokenUsage == (accounting.TokenUsage{}) {
			return nil
		}
		index := sort.Search(len(after.Models), func(index int) bool { return after.Models[index].Model >= "" })
		if index >= len(after.Models) || after.Models[index].Model != "" {
			return errors.New("legacy review usage tokens were not preserved during migration")
		}
		if _, err := reviewUsageDelta(after.Models[index].TokenUsage, before.legacy.TokenUsage); err != nil {
			return fmt.Errorf("legacy review usage migration is not additive: %w", err)
		}
		return nil
	}
	if len(before.Models) == 0 {
		return nil
	}
	if after.legacy != nil || !after.SnapshotAt.Equal(before.SnapshotAt) {
		return errors.New("pinned review pricing snapshot cannot change")
	}
	for _, previous := range before.Models {
		index := sort.Search(len(after.Models), func(index int) bool { return after.Models[index].Model >= previous.Model })
		if index >= len(after.Models) || after.Models[index].Model != previous.Model {
			return fmt.Errorf("review accounting model %q cannot be removed", previous.Model)
		}
		next := after.Models[index]
		if next.Pricing != previous.Pricing {
			return fmt.Errorf("review accounting model %q pricing snapshot cannot change", previous.Model)
		}
		if _, err := reviewUsageDelta(next.TokenUsage, previous.TokenUsage); err != nil {
			return fmt.Errorf("review accounting model %q usage is not additive: %w", previous.Model, err)
		}
	}
	return nil
}

func reviewUsageDelta(total, previous accounting.TokenUsage) (accounting.TokenUsage, error) {
	totalFields := []int64{total.InputTokens, total.CachedInputTokens, total.CacheWriteInputTokens, total.OutputTokens, total.ReasoningOutputTokens, total.TotalTokens}
	previousFields := []int64{previous.InputTokens, previous.CachedInputTokens, previous.CacheWriteInputTokens, previous.OutputTokens, previous.ReasoningOutputTokens, previous.TotalTokens}
	deltaFields := make([]int64, len(totalFields))
	for index := range totalFields {
		if totalFields[index] < previousFields[index] {
			return accounting.TokenUsage{}, errors.New("cumulative token count decreased")
		}
		deltaFields[index] = totalFields[index] - previousFields[index]
	}
	delta := accounting.TokenUsage{
		InputTokens:           deltaFields[0],
		CachedInputTokens:     deltaFields[1],
		CacheWriteInputTokens: deltaFields[2],
		OutputTokens:          deltaFields[3],
		ReasoningOutputTokens: deltaFields[4],
		TotalTokens:           deltaFields[5],
	}
	if err := accounting.ValidateTokenUsage(delta); err != nil {
		return accounting.TokenUsage{}, err
	}
	return delta, nil
}

func addReviewTokenUsage(left, right accounting.TokenUsage) (accounting.TokenUsage, error) {
	result := left
	targets := []*int64{
		&result.InputTokens,
		&result.CachedInputTokens,
		&result.CacheWriteInputTokens,
		&result.OutputTokens,
		&result.ReasoningOutputTokens,
		&result.TotalTokens,
	}
	adds := []int64{
		right.InputTokens,
		right.CachedInputTokens,
		right.CacheWriteInputTokens,
		right.OutputTokens,
		right.ReasoningOutputTokens,
		right.TotalTokens,
	}
	for index := range targets {
		if adds[index] > reviewAccountingMaxSafeInteger-*targets[index] {
			return accounting.TokenUsage{}, errors.New("cumulative token count exceeds the safe integer range")
		}
		*targets[index] += adds[index]
	}
	if err := accounting.ValidateTokenUsage(result); err != nil {
		return accounting.TokenUsage{}, err
	}
	return result, nil
}

func recomputeReviewTotals(value *ReviewAccounting) error {
	value.TotalTokens = 0
	value.PricingComplete = true
	var totalCost float64
	for index := range value.Models {
		model := value.Models[index]
		if model.TotalTokens > reviewAccountingMaxSafeInteger-value.TotalTokens {
			return errors.New("review accounting token total exceeds the safe integer range")
		}
		value.TotalTokens += model.TotalTokens
		if model.Pricing == (accounting.Pricing{}) {
			value.Models[index].CostUSD = 0
			value.PricingComplete = false
			continue
		}
		cost, err := accounting.PriceUsage(model.TokenUsage, model.Pricing)
		if err != nil {
			return fmt.Errorf("model %q pricing: %w", model.Model, err)
		}
		value.Models[index].CostUSD = cost
		totalCost += cost
		if math.IsNaN(totalCost) || math.IsInf(totalCost, 0) {
			return errors.New("review accounting cost overflows")
		}
	}
	if value.PricingComplete {
		value.TotalCostUSD = new(float64)
		*value.TotalCostUSD = totalCost
	} else {
		value.TotalCostUSD = nil
	}
	return nil
}

func reviewCostsCanonical(left, right float64) bool {
	if math.IsNaN(left) || math.IsNaN(right) || math.IsInf(left, 0) || math.IsInf(right, 0) || left < 0 || right < 0 {
		return false
	}
	return math.Float64bits(left) == math.Float64bits(right)
}
