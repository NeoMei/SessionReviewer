package pricing

import (
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/modelpricewatch"
)

func TestMatchListingRequiresExactObservedBillingRoute(t *testing.T) {
	catalog := matchCatalog()
	aliases := []Alias{{Route: BillingRoute{Host: "api.example.test", ModelID: "model-exact", Mode: "api"}, ListingID: "provider-model"}}
	at := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	got := MatchListing(BillingRoute{Host: "api.example.test", ModelID: "model-exact", Mode: "api"}, aliases, catalog, at)
	if got.Status != PriceCurrent || got.ListingID == nil || *got.ListingID != "provider-model" {
		t.Fatalf("exact=%#v", got)
	}
	for _, route := range []BillingRoute{
		{Host: "API.example.test", ModelID: "model-exact", Mode: "api"},
		{Host: "api.example.test", ModelID: "MODEL-EXACT", Mode: "api"},
		{Host: "api.example.test", ModelID: "model-exact", Mode: "API"},
		{Host: "api.example.test", ModelID: "model-exact", Mode: "api", Region: strptr("us")},
	} {
		got = MatchListing(route, aliases, catalog, at)
		if got.Status != PricePending || got.ListingID != nil {
			t.Fatalf("fuzzy route %#v matched %#v", route, got)
		}
	}
}

func TestMatchListingRejectsConflictMissingAndUnstructuredConditions(t *testing.T) {
	at := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	route := BillingRoute{Host: "api.example.test", ModelID: "model-exact", Mode: "api"}
	tests := []struct {
		name    string
		aliases []Alias
		catalog modelpricewatch.Catalog
		status  PriceStatus
		reason  string
	}{
		{"conflict", []Alias{{route, "provider-model"}, {route, "other"}}, matchCatalog(), PriceAmbiguous, "conflicting_exact_aliases"},
		{"missing listing", []Alias{{route, "absent"}}, matchCatalog(), PricePending, "listing_missing"},
		{"empty aliases", nil, matchCatalog(), PricePending, "no_exact_billing_route_alias"},
	}
	conditional := matchCatalog()
	row := conditional.Listings["provider-model"]
	row.HasUnstructuredConditions = true
	conditional.Listings["provider-model"] = row
	tests = append(tests, struct {
		name    string
		aliases []Alias
		catalog modelpricewatch.Catalog
		status  PriceStatus
		reason  string
	}{"note", []Alias{{route, "provider-model"}}, conditional, PriceAmbiguous, "unstructured_price_conditions"})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := MatchListing(route, test.aliases, test.catalog, at)
			if got.Status != test.status || got.Reason != test.reason {
				t.Fatalf("got=%#v", got)
			}
		})
	}
}

func matchCatalog() modelpricewatch.Catalog {
	one, two := 1.0, 2.0
	return modelpricewatch.Catalog{Count: 1, Updated: "2026-09-06", Listings: map[string]modelpricewatch.Listing{"provider-model": {ID: "provider-model", Provider: "Provider", Model: "Model", Status: "active", InputPerMTok: &one, OutputPerMTok: &two, LastUpdated: "2026-09-06", PricingURL: "https://example.test/pricing", DetailURL: "https://modelpricewatch.com/models/provider-model/"}}}
}
