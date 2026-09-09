package pricing

import (
	"strings"
	"time"

	modelpricewatch "github.com/neomei/SessionReviewer/internal/modelpricewatch/catalog"
)

// BillingRoute is observed request metadata. Callers must supply the actual
// billing host and model ID; provider display labels are not substitutes.
type BillingRoute struct {
	Host    string
	ModelID string
	Mode    string
	Region  *string
}
type Alias struct {
	Route     BillingRoute
	ListingID string
}
type Match struct {
	Status    PriceStatus
	ListingID *string
	Reason    string
}

func MatchListing(route BillingRoute, aliases []Alias, catalog modelpricewatch.Catalog, at time.Time) Match {
	if !validRoute(route) {
		return Match{Status: PricePending, Reason: "incomplete_billing_route"}
	}
	matches := make([]string, 0, 1)
	for _, alias := range aliases {
		if routesEqual(route, alias.Route) {
			matches = append(matches, alias.ListingID)
		}
	}
	if len(matches) == 0 {
		return Match{Status: PricePending, Reason: "no_exact_billing_route_alias"}
	}
	listingID := matches[0]
	for _, id := range matches[1:] {
		if id != listingID {
			return Match{Status: PriceAmbiguous, Reason: "conflicting_exact_aliases"}
		}
	}
	listing, ok := catalog.Listings[listingID]
	if !ok {
		return Match{Status: PricePending, Reason: "listing_missing"}
	}
	if listing.Status != "active" {
		return Match{Status: PricePending, ListingID: &listingID, Reason: "listing_inactive"}
	}
	if listing.HasUnstructuredConditions {
		return Match{Status: PriceAmbiguous, ListingID: &listingID, Reason: "unstructured_price_conditions"}
	}
	status := PriceCurrent
	if listing.Promo {
		if listing.PromoUntil == nil {
			return Match{Status: PriceAmbiguous, ListingID: &listingID, Reason: "promotion_end_unknown"}
		}
		until, err := time.Parse("2006-01-02", *listing.PromoUntil)
		if err != nil || at.After(until.Add(24*time.Hour-time.Nanosecond)) {
			return Match{Status: PriceAmbiguous, ListingID: &listingID, Reason: "promotion_applicability_unknown"}
		}
		status = PricePromotion
	}
	return Match{Status: status, ListingID: &listingID, Reason: "exact_reviewed_alias"}
}
func validRoute(v BillingRoute) bool {
	return strings.TrimSpace(v.Host) == v.Host && v.Host != "" && strings.TrimSpace(v.ModelID) == v.ModelID && v.ModelID != "" && strings.TrimSpace(v.Mode) == v.Mode && v.Mode != "" && (v.Region == nil || (*v.Region != "" && strings.TrimSpace(*v.Region) == *v.Region))
}
func routesEqual(a, b BillingRoute) bool {
	return a.Host == b.Host && a.ModelID == b.ModelID && a.Mode == b.Mode && optionalEqual(a.Region, b.Region)
}
func optionalEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
