package reviewjob

import (
	"fmt"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
)

// PricingCatalog is an immutable, date-stamped snapshot of authoritative
// public USD list prices. Its metadata describes the catalog convention; only
// entries claim prices for models.
type PricingCatalog struct {
	asOf    string
	source  string
	entries map[string]accounting.Pricing
}

// NewPricingCatalog validates metadata and every entry, and copies entries so
// later caller mutation cannot change resolution.
func NewPricingCatalog(asOf, source string, entries map[string]accounting.Pricing) (*PricingCatalog, error) {
	metadata := accounting.Pricing{Currency: "USD", Source: source, AsOf: asOf}
	if _, err := accounting.PriceUsage(accounting.TokenUsage{}, metadata); err != nil {
		return nil, fmt.Errorf("pricing catalog metadata: %w", err)
	}

	catalog := &PricingCatalog{asOf: asOf, source: source, entries: make(map[string]accounting.Pricing, len(entries))}
	for model, pricing := range entries {
		if strings.TrimSpace(model) == "" || strings.TrimSpace(model) != model {
			return nil, fmt.Errorf("pricing catalog model %q is invalid", model)
		}
		if pricing.AsOf != asOf || pricing.Source != source {
			return nil, fmt.Errorf("pricing catalog model %q does not match catalog metadata", model)
		}
		if err := validateCatalogPricing(pricing); err != nil {
			return nil, fmt.Errorf("pricing catalog model %q: %w", model, err)
		}
		catalog.entries[model] = pricing
	}
	return catalog, nil
}

func (catalog *PricingCatalog) Resolve(model string, _ time.Time) (accounting.Pricing, bool) {
	if catalog == nil {
		return accounting.Pricing{}, false
	}
	pricing, ok := catalog.entries[model]
	return pricing, ok
}

func (catalog *PricingCatalog) AsOf() string {
	if catalog == nil {
		return ""
	}
	return catalog.asOf
}

func (catalog *PricingCatalog) Source() string {
	if catalog == nil {
		return ""
	}
	return catalog.source
}

func (catalog *PricingCatalog) Len() int {
	if catalog == nil {
		return 0
	}
	return len(catalog.entries)
}

// Ruling P6: no production-supported Adapter currently advertises an
// authoritative model. The checked-in catalog is therefore intentionally
// empty. Its date/source record only the convention to use when a future
// verified Adapter and reviewed official model price are added together.
var productionPricingCatalog = mustPricingCatalog(
	"2026-08-29",
	"https://developers.openai.com/api/docs/pricing",
	map[string]accounting.Pricing{},
)

func ProductionPricingCatalog() *PricingCatalog { return productionPricingCatalog }

func mustPricingCatalog(asOf, source string, entries map[string]accounting.Pricing) *PricingCatalog {
	catalog, err := NewPricingCatalog(asOf, source, entries)
	if err != nil {
		panic(err)
	}
	return catalog
}

func validateCatalogPricing(pricing accounting.Pricing) error {
	maximum := int64(reviewAccountingMaxSafeInteger)
	samples := []accounting.TokenUsage{
		{InputTokens: maximum, TotalTokens: maximum},
		{InputTokens: maximum, CachedInputTokens: maximum, TotalTokens: maximum},
		{InputTokens: maximum, CacheWriteInputTokens: maximum, TotalTokens: maximum},
		{OutputTokens: maximum, ReasoningOutputTokens: maximum, TotalTokens: maximum},
	}
	for _, usage := range samples {
		if _, err := accounting.PriceUsage(usage, pricing); err != nil {
			return err
		}
	}
	return nil
}
