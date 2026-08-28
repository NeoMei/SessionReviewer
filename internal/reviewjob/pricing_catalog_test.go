package reviewjob

import (
	"math"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
)

func TestReviewAccountingProductionCatalogIsValidatedDateStampedAndEmpty(t *testing.T) {
	catalog := ProductionPricingCatalog()
	if catalog.AsOf() != "2026-08-29" || catalog.Source() != "https://developers.openai.com/api/docs/pricing" {
		t.Fatalf("production catalog metadata=%q %q", catalog.AsOf(), catalog.Source())
	}
	if catalog.Len() != 0 {
		t.Fatalf("production catalog must remain empty under Ruling P6, entries=%d", catalog.Len())
	}
	if _, ok := catalog.Resolve("gpt-5.6-sol", time.Now()); ok {
		t.Fatal("empty production catalog invented a model price")
	}
}

func TestReviewAccountingPricingCatalogValidatesAndCopiesInjectedEntries(t *testing.T) {
	entry := fixturePricing(2, .5, 3, 10)
	entries := map[string]accounting.Pricing{"fixture-model": entry}
	catalog, err := NewPricingCatalog("2026-08-29", "https://example.com/pricing", entries)
	if err != nil {
		t.Fatal(err)
	}
	entries["fixture-model"] = fixturePricing(99, 99, 99, 99)

	got, ok := catalog.Resolve("fixture-model", time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	if !ok || got != entry {
		t.Fatalf("resolved=%+v ok=%v want=%+v", got, ok, entry)
	}
	if _, ok := catalog.Resolve("unknown", time.Now()); ok {
		t.Fatal("catalog resolved an unknown model")
	}

	tests := []struct {
		name    string
		asOf    string
		source  string
		entries map[string]accounting.Pricing
	}{
		{name: "bad metadata date", asOf: "2026/08/29", source: "https://example.com/pricing", entries: map[string]accounting.Pricing{}},
		{name: "bad metadata source", asOf: "2026-08-29", source: "http://example.com/pricing", entries: map[string]accounting.Pricing{}},
		{name: "blank model", asOf: "2026-08-29", source: "https://example.com/pricing", entries: map[string]accounting.Pricing{" ": entry}},
		{name: "entry date mismatch", asOf: "2026-08-28", source: "https://example.com/pricing", entries: map[string]accounting.Pricing{"fixture-model": entry}},
		{name: "entry source mismatch", asOf: "2026-08-29", source: "https://different.example/pricing", entries: map[string]accounting.Pricing{"fixture-model": entry}},
		{name: "nonfinite rate", asOf: "2026-08-29", source: "https://example.com/pricing", entries: map[string]accounting.Pricing{"fixture-model": func() accounting.Pricing { invalid := entry; invalid.InputPerMillion = math.NaN(); return invalid }()}},
		{name: "finite but unsafe rate", asOf: "2026-08-29", source: "https://example.com/pricing", entries: map[string]accounting.Pricing{"fixture-model": func() accounting.Pricing { invalid := entry; invalid.InputPerMillion = math.MaxFloat64; return invalid }()}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewPricingCatalog(test.asOf, test.source, test.entries); err == nil {
				t.Fatal("accepted invalid pricing catalog")
			}
		})
	}
}
