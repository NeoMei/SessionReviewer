package reviewjob

import (
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
	Models          []accounting.ModelAccounting `json:"models"`
	TotalTokens     int64                        `json:"total_tokens"`
	TotalCostUSD    *float64                     `json:"total_cost_usd,omitempty"`
	PricingComplete bool                         `json:"pricing_complete"`
}

// AddReviewResult returns a new aggregate containing one actual Agent result.
// It never aliases or mutates current. Model is used exactly as reported; an
// empty or unresolved model retains usage and makes the aggregate unpriced.
func AddReviewResult(current ReviewAccounting, result agent.Result, at time.Time, resolver PricingResolver) (ReviewAccounting, error) {
	if at.IsZero() {
		return ReviewAccounting{}, errors.New("review accounting time is required")
	}
	if err := ValidateReviewAccounting(current); err != nil {
		return ReviewAccounting{}, fmt.Errorf("invalid current review accounting: %w", err)
	}
	if err := accounting.ValidateTokenUsage(result.Usage); err != nil {
		return ReviewAccounting{}, fmt.Errorf("invalid review usage: %w", err)
	}

	next := cloneReviewAccounting(current)
	pricing, priced := accounting.Pricing{}, false
	var cost float64
	if strings.TrimSpace(result.Model) != "" && resolver != nil {
		pricing, priced = resolver.Resolve(result.Model, at)
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
	if value.TotalTokens < 0 || value.TotalTokens > reviewAccountingMaxSafeInteger {
		return errors.New("review accounting token total is outside the safe integer range")
	}
	if len(value.Models) == 0 {
		if value.TotalTokens != 0 || value.TotalCostUSD != nil || value.PricingComplete {
			return errors.New("empty review accounting has nonempty totals")
		}
		return nil
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
			if model.CostUSD != 0 {
				return fmt.Errorf("unpriced model %q has a cost", model.Model)
			}
		} else {
			cost, err := accounting.PriceUsage(model.TokenUsage, model.Pricing)
			if err != nil {
				return fmt.Errorf("model %q pricing: %w", model.Model, err)
			}
			if !reviewCostsEqual(cost, model.CostUSD) {
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
	if value.TotalCostUSD == nil || !reviewCostsEqual(totalCost, *value.TotalCostUSD) {
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
	return clone
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

func reviewCostsEqual(left, right float64) bool {
	if math.IsNaN(left) || math.IsNaN(right) || math.IsInf(left, 0) || math.IsInf(right, 0) || left < 0 || right < 0 {
		return false
	}
	return math.Abs(left-right) <= 1e-9*math.Max(1, math.Max(math.Abs(left), math.Abs(right)))
}
