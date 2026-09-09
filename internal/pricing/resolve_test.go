package pricing

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/modelpricewatch"
)

type catalogLoaderFunc func(context.Context, time.Time) (modelpricewatch.CatalogSet, modelpricewatch.Freshness, error)

func (f catalogLoaderFunc) LoadOrRefresh(ctx context.Context, at time.Time) (modelpricewatch.CatalogSet, modelpricewatch.Freshness, error) {
	return f(ctx, at)
}

type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

func TestServiceResolveUsesCurrentStaleAndExpiredCatalogTruthfully(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	request := resolutionFixture(now)
	for _, test := range []struct {
		name     string
		age      time.Duration
		want     PriceStatus
		complete bool
	}{
		{"current", time.Hour, PriceCurrent, true}, {"stale", 24*time.Hour + time.Second, PriceStaleEstimate, true}, {"expired", 7*24*time.Hour + time.Second, PricePending, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			loader := catalogLoaderFunc(func(context.Context, time.Time) (modelpricewatch.CatalogSet, modelpricewatch.Freshness, error) {
				status := modelpricewatch.FreshCurrent
				if test.age > 24*time.Hour {
					status = modelpricewatch.FreshStale
				}
				if test.age > 7*24*time.Hour {
					status = modelpricewatch.FreshExpired
				}
				return resolutionCatalog(), modelpricewatch.Freshness{Status: status, RetrievedAt: now.Add(-test.age), Age: test.age}, nil
			})
			service, err := NewService(loader, []Alias{{Route: request.Route, ListingID: "provider-model"}}, map[string]UsageAdapter{"codex": CodexUsageAdapter{}}, fixedClock{now})
			if err != nil {
				t.Fatal(err)
			}
			got, err := service.Resolve(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != test.want || got.PricingComplete != test.complete || (got.TotalCostUSD != nil) != test.complete {
				t.Fatalf("got=%#v", got)
			}
			if test.complete && math.Abs(got.KnownSubtotalUSD-0.00015) > 1e-15 {
				t.Fatalf("cost=%v", got.KnownSubtotalUSD)
			}
			if test.complete && (got.Rates.CacheWriteInput != nil || got.LineCostsUSD.CacheWriteInput != nil || got.Rates.ReasoningOutput != nil || got.LineCostsUSD.ReasoningOutput != nil) {
				t.Fatalf("unknown zero-quantity dimensions became numeric zero: %#v", got)
			}
		})
	}
}

func TestResolveSelectsApplicableHistoricalBaselineAndRejectsFutureOnly(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	request := resolutionFixture(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	fresh := modelpricewatch.Freshness{Status: modelpricewatch.FreshCurrent, RetrievedAt: now}
	input := ResolveInput{Request: request, Catalog: resolutionCatalog(), Freshness: fresh, Aliases: []Alias{{Route: request.Route, ListingID: "provider-model"}}, Adapter: CodexUsageAdapter{}, CreatedAt: now}
	got, err := Resolve(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != PriceCurrent || got.Rates.Input == nil || *got.Rates.Input != 2 {
		t.Fatalf("historical=%#v", got)
	}
	catalog := resolutionCatalog()
	history := catalog.History.Models["provider-model"]
	history.History = history.History[1:]
	catalog.History.Models["provider-model"] = history
	input.Catalog = catalog
	got, err = Resolve(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != PricePending || got.TotalCostUSD != nil || got.AuditReason != "no_applicable_historical_price" {
		t.Fatalf("future=%#v", got)
	}
}

func TestResolveIsDeterministicAndNeverUsesCallerTotals(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	request := resolutionFixture(now)
	in := ResolveInput{Request: request, Catalog: resolutionCatalog(), Freshness: modelpricewatch.Freshness{Status: modelpricewatch.FreshCurrent, RetrievedAt: now}, Aliases: []Alias{{Route: request.Route, ListingID: "provider-model"}}, Adapter: CodexUsageAdapter{}, CreatedAt: now}
	a, err := Resolve(in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Resolve(in)
	if err != nil {
		t.Fatal(err)
	}
	if a.SnapshotID != b.SnapshotID || math.Abs(a.KnownSubtotalUSD-0.00015) > 1e-15 {
		t.Fatalf("a=%#v b=%#v", a, b)
	}
}

func TestServiceSupplementRecomputesFromReviewedRatesAndQuantities(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	request := resolutionFixture(now)
	service, err := NewService(nil, nil, map[string]UsageAdapter{"codex": CodexUsageAdapter{}}, fixedClock{now})
	if err != nil {
		t.Fatal(err)
	}
	one, two, zero := 1.0, 2.0, 0.0
	supplement := Supplement{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID, UsageRecordDigest: request.UsageRecordDigest, BillingHost: request.Route.Host, BilledModelID: request.Route.ModelID, BillingMode: request.Route.Mode, BillingRuleVersion: "codex-token-count-v1", Region: nil, EffectiveFrom: "2026-01-01T00:00:00Z", Rates: Rates{Input: &one, CachedInput: &zero, CacheWriteInput: &zero, Output: &two, ReasoningOutput: &two}, SourceURL: "https://example.test/reviewed", AuditReason: "Reviewed official route and effective period."}
	got, err := service.Supplement(context.Background(), request, supplement, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != PriceManualSupplement || got.SourceKind != "manual" || math.Abs(got.KnownSubtotalUSD-0.00014) > 1e-15 || got.TotalCostUSD == nil {
		t.Fatalf("got=%#v", got)
	}
	supplement.EffectiveFrom = "2026-09-09T00:00:00Z"
	if _, err := service.Supplement(context.Background(), request, supplement, nil); err == nil {
		t.Fatal("accepted future supplement")
	}
}

func TestServiceResolveMissingObservedRouteStaysPending(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	request := resolutionFixture(now)
	request.Route = BillingRoute{}
	service, err := NewService(nil, nil, map[string]UsageAdapter{"codex": CodexUsageAdapter{}}, fixedClock{now})
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.Resolve(context.Background(), request)
	if err != nil || got.Status != PricePending || got.BillingHost != "unknown" || got.BilledModelID != request.Usage.Model || got.AuditReason != "observed_billing_route_missing" {
		t.Fatalf("got=%#v err=%v", got, err)
	}
}

func TestServiceResolveUnknownRouteDoesNotFailOnUnreviewedQuantities(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	request := resolutionFixture(now)
	request.Route = BillingRoute{}
	request.Usage.CachedInputTokens = request.Usage.InputTokens + 1
	service, err := NewService(nil, nil, map[string]UsageAdapter{"codex": CodexUsageAdapter{}}, fixedClock{now})
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.Resolve(context.Background(), request)
	if err != nil || got.Status != PricePending || got.AuditReason != "observed_billing_route_missing" {
		t.Fatalf("got=%#v err=%v", got, err)
	}
}

func TestServiceResolveUnknownProviderAndRouteStaysPending(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	request := resolutionFixture(now)
	request.Provider = "claude"
	request.Route = BillingRoute{}
	service, err := NewService(nil, nil, map[string]UsageAdapter{"codex": CodexUsageAdapter{}}, fixedClock{now})
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.Resolve(context.Background(), request)
	if err != nil || got.Status != PricePending || got.BillingHost != "unknown" || got.BilledModelID != request.Usage.Model {
		t.Fatalf("got=%#v err=%v", got, err)
	}
}

func resolutionFixture(at time.Time) ResolutionRequest {
	return ResolutionRequest{ProjectID: "project-p", Provider: "codex", SessionID: "session-s", UsageRecordDigest: "sha256:" + strings.Repeat("a", 64), Route: BillingRoute{Host: "api.example.test", ModelID: "model-exact", Mode: "api"}, Usage: accounting.ModelUsage{Model: "model-exact", TokenUsage: accounting.TokenUsage{InputTokens: 100, CachedInputTokens: 20, OutputTokens: 30, ReasoningOutputTokens: 10, TotalTokens: 130}}, PricedAt: at}
}
func resolutionCatalog() modelpricewatch.CatalogSet {
	one, two, half := 1.0, 2.0, .5
	listing := modelpricewatch.Listing{ID: "provider-model", Provider: "Provider", Model: "Model", Status: "active", InputPerMTok: &one, OutputPerMTok: &two, CachedInputPerMTok: &half, LastUpdated: "2026-09-06", PricingURL: "https://example.test/pricing", DetailURL: "https://modelpricewatch.com/models/provider-model/"}
	history := modelpricewatch.ModelHistory{Model: "Model", Provider: "Provider", History: []modelpricewatch.HistoryEntry{{Date: "2026-01-01", InputPerMTok: &two, OutputPerMTok: &two, CachedInputPerMTok: &half, Event: "baseline", Changes: []modelpricewatch.Change{}}, {Date: "2026-09-06", InputPerMTok: &one, OutputPerMTok: &two, CachedInputPerMTok: &half, Event: "snapshot", Changes: []modelpricewatch.Change{}}}}
	return modelpricewatch.CatalogSet{Models: modelpricewatch.Catalog{Count: 1, Updated: "2026-09-06", Listings: map[string]modelpricewatch.Listing{"provider-model": listing}}, History: modelpricewatch.HistoryCatalog{Count: 1, Updated: "2026-09-06", Models: map[string]modelpricewatch.ModelHistory{"provider-model": history}}}
}
