package pricing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/modelpricewatch"
)

type Clock interface{ Now() time.Time }
type CatalogLoader interface {
	LoadOrRefresh(context.Context, time.Time) (modelpricewatch.CatalogSet, modelpricewatch.Freshness, error)
}
type ResolutionRequest struct {
	ProjectID, Provider, SessionID, UsageRecordDigest string
	Route                                             BillingRoute
	Usage                                             accounting.ModelUsage
	PricedAt                                          time.Time
	Prior                                             *Snapshot
}
type ResolveInput struct {
	Request   ResolutionRequest
	Catalog   modelpricewatch.CatalogSet
	Freshness modelpricewatch.Freshness
	Aliases   []Alias
	Adapter   UsageAdapter
	CreatedAt time.Time
}
type Service struct {
	loader   CatalogLoader
	aliases  []Alias
	adapters map[string]UsageAdapter
	clock    Clock
}
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

func NewService(loader CatalogLoader, aliases []Alias, adapters map[string]UsageAdapter, clock Clock) (*Service, error) {
	if clock == nil {
		clock = systemClock{}
	}
	for provider, adapter := range adapters {
		if provider == "" || adapter == nil || adapter.Provider() != provider {
			return nil, errors.New("pricing adapter registry is invalid")
		}
	}
	return &Service{loader: loader, aliases: append([]Alias(nil), aliases...), adapters: adapters, clock: clock}, nil
}
func (s *Service) Resolve(ctx context.Context, request ResolutionRequest) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, errors.New("pricing service is required")
	}
	if err := validateRequestIdentity(request); err != nil {
		return Snapshot{}, err
	}
	adapter := s.adapters[request.Provider]
	if adapter == nil {
		return unresolvedFrom(request, s.clock.Now(), nil, nil, "usage_adapter_unavailable")
	}
	if !validRoute(request.Route) {
		request.Route = unresolvedRoute(request.Route, request.Usage.Model)
		return unresolvedFrom(request, s.clock.Now(), adapter, nil, "observed_billing_route_missing")
	}
	if s.loader == nil {
		return unresolvedFrom(request, s.clock.Now(), adapter, nil, "catalog_unavailable")
	}
	catalog, fresh, err := s.loader.LoadOrRefresh(ctx, s.clock.Now())
	if err != nil {
		return unresolvedFrom(request, s.clock.Now(), adapter, nil, "catalog_unavailable")
	}
	return Resolve(ResolveInput{Request: request, Catalog: catalog, Freshness: fresh, Aliases: s.aliases, Adapter: adapter, CreatedAt: s.clock.Now()})
}

func Resolve(in ResolveInput) (Snapshot, error) {
	r := in.Request
	if err := validateRequest(r); err != nil {
		return Snapshot{}, err
	}
	if in.Adapter == nil {
		return unresolvedFrom(r, in.CreatedAt, nil, nil, "usage_adapter_unavailable")
	}
	billable, missing, err := in.Adapter.Billable(r.Usage)
	if err != nil {
		return Snapshot{}, err
	}
	if len(missing) > 0 {
		return unresolvedWithQuantities(r, in.CreatedAt, billable, missing, "usage_dimensions_unreviewed")
	}
	if in.Freshness.Status == modelpricewatch.FreshExpired {
		return unresolvedWithQuantities(r, in.CreatedAt, billable, allDimensions(), "catalog_expired")
	}
	match := MatchListing(r.Route, in.Aliases, in.Catalog.Models, r.PricedAt)
	if match.Status == PricePending || match.Status == PriceAmbiguous {
		return unresolvedMatched(r, in.CreatedAt, billable, match)
	}
	listing := in.Catalog.Models.Listings[*match.ListingID]
	rates, reason := ratesAt(in.Catalog.History, *match.ListingID, r.PricedAt, listing)
	if reason != "" {
		return unresolvedMatched(r, in.CreatedAt, billable, Match{Status: PricePending, ListingID: match.ListingID, Reason: reason})
	}
	if rates.CacheWriteInput == nil && billable.Quantities.CacheWriteInput == 0 {
		zero := 0.0
		rates.CacheWriteInput = &zero
	}
	if rates.ReasoningOutput == nil && billable.Quantities.ReasoningOutput > 0 && rates.Output != nil {
		rates.ReasoningOutput = rates.Output
	}
	status := match.Status
	if in.Freshness.Status == modelpricewatch.FreshStale {
		status = PriceStaleEstimate
	}
	s := baseSnapshot(r, in.CreatedAt, billable)
	s.Status = status
	s.SourceKind = "modelpricewatch"
	s.ModelPriceWatchListingID = match.ListingID
	s.SourceURL = &listing.PricingURL
	s.DetailURL = &listing.DetailURL
	s.SourceLastUpdated = &listing.LastUpdated
	retrieved := in.Freshness.RetrievedAt.UTC().Format(time.RFC3339)
	s.RetrievedAt = &retrieved
	s.Promo = listing.Promo
	s.PromoUntil = listing.PromoUntil
	s.Rates = rates
	s.AuditReason = match.Reason
	computeCosts(&s)
	finalizeID(&s)
	return s, ValidateSnapshot(s)
}

func (s *Service) Supplement(_ context.Context, request ResolutionRequest, supplement Supplement, prior *Snapshot) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, errors.New("pricing service is required")
	}
	if err := ValidateSupplement(supplement); err != nil {
		return Snapshot{}, err
	}
	if err := validateRequest(request); err != nil {
		return Snapshot{}, err
	}
	if supplement.ProjectID != request.ProjectID || supplement.Provider != request.Provider || supplement.SessionID != request.SessionID || supplement.UsageRecordDigest != request.UsageRecordDigest || supplement.BillingHost != request.Route.Host || supplement.BilledModelID != request.Route.ModelID || supplement.BillingMode != request.Route.Mode || !optionalEqual(supplement.Region, request.Route.Region) {
		return Snapshot{}, errors.New("supplement identity does not match usage and route")
	}
	from, err := time.Parse(time.RFC3339, supplement.EffectiveFrom)
	if err != nil || request.PricedAt.Before(from) {
		return Snapshot{}, errors.New("supplement is not effective at priced time")
	}
	if supplement.EffectiveUntil != nil {
		until, err := time.Parse(time.RFC3339, *supplement.EffectiveUntil)
		if err != nil || request.PricedAt.After(until) {
			return Snapshot{}, errors.New("supplement is not effective at priced time")
		}
	}
	adapter := s.adapters[request.Provider]
	if adapter == nil {
		return Snapshot{}, errors.New("usage adapter unavailable")
	}
	billable, missing, err := adapter.Billable(request.Usage)
	if err != nil || len(missing) > 0 {
		return Snapshot{}, errors.New("usage dimensions are not reviewed")
	}
	if billable.RuleVersion != supplement.BillingRuleVersion {
		return Snapshot{}, errors.New("supplement billing rule version does not match adapter")
	}
	snap := baseSnapshot(request, s.clock.Now(), billable)
	snap.Status = PriceManualSupplement
	snap.SourceKind = "manual"
	snap.SourceURL = &supplement.SourceURL
	snap.DetailURL = supplement.DetailURL
	snap.Rates = supplement.Rates
	snap.AuditReason = supplement.AuditReason
	snap.SupersedesSnapshotID = supplement.SupersedesSnapshotID
	if prior != nil {
		if supplement.SupersedesSnapshotID == nil || *supplement.SupersedesSnapshotID != prior.SnapshotID {
			return Snapshot{}, errors.New("supplement does not supersede supplied prior snapshot")
		}
		if identity(*prior) != identity(snap) {
			return Snapshot{}, errors.New("supplement changes pricing identity")
		}
	}
	computeCosts(&snap)
	finalizeID(&snap)
	return snap, ValidateSnapshot(snap)
}

func validateRequest(r ResolutionRequest) error {
	if err := validateRequestCore(r); err != nil {
		return err
	}
	if !validRoute(r.Route) {
		return errors.New("resolution billing route is invalid")
	}
	return nil
}
func validateRequestCore(r ResolutionRequest) error {
	if err := validateRequestIdentity(r); err != nil {
		return err
	}
	return accounting.ValidateTokenUsage(r.Usage.TokenUsage)
}
func validateRequestIdentity(r ResolutionRequest) error {
	if r.PricedAt.IsZero() || r.PricedAt.Location() != time.UTC {
		return errors.New("priced time must be canonical UTC")
	}
	if r.ProjectID == "" || r.Provider == "" || r.SessionID == "" || !digestRE.MatchString(r.UsageRecordDigest) {
		return errors.New("resolution identity is invalid")
	}
	return nil
}
func unresolvedRoute(route BillingRoute, usageModel string) BillingRoute {
	if strings.TrimSpace(route.Host) == "" {
		route.Host = "unknown"
	}
	if strings.TrimSpace(route.ModelID) == "" {
		route.ModelID = strings.TrimSpace(usageModel)
	}
	if route.ModelID == "" {
		route.ModelID = "unknown"
	}
	if strings.TrimSpace(route.Mode) == "" {
		route.Mode = "unknown"
	}
	return route
}
func baseSnapshot(r ResolutionRequest, created time.Time, b BillableQuantities) Snapshot {
	return Snapshot{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: r.ProjectID, Provider: r.Provider, SessionID: r.SessionID, UsageRecordDigest: r.UsageRecordDigest, BillingHost: r.Route.Host, BilledModelID: r.Route.ModelID, BillingMode: r.Route.Mode, BillingRuleVersion: b.RuleVersion, Region: r.Route.Region, PricedAt: r.PricedAt.Format(time.RFC3339), CreatedAt: created.UTC().Format(time.RFC3339), BillableQuantities: b.Quantities, MissingBillingDimensions: []string{}, AuditReason: "unresolved"}
}
func unresolvedFrom(r ResolutionRequest, created time.Time, adapter UsageAdapter, _ *modelpricewatch.CatalogSet, reason string) (Snapshot, error) {
	b := BillableQuantities{RuleVersion: "unresolved-v1"}
	missing := allDimensions()
	if adapter != nil {
		var err error
		b, missing, err = adapter.Billable(r.Usage)
		if err != nil {
			b = BillableQuantities{RuleVersion: "unresolved-v1"}
			missing = allDimensions()
		}
		if len(missing) == 0 {
			missing = allDimensions()
		}
	}
	return unresolvedWithQuantities(r, created, b, missing, reason)
}
func unresolvedWithQuantities(r ResolutionRequest, created time.Time, b BillableQuantities, missing []string, reason string) (Snapshot, error) {
	s := baseSnapshot(r, created, b)
	s.Status = PricePending
	s.SourceKind = "unresolved"
	s.MissingBillingDimensions = append([]string(nil), missing...)
	s.AuditReason = reason
	finalizeID(&s)
	return s, ValidateSnapshot(s)
}
func unresolvedMatched(r ResolutionRequest, created time.Time, b BillableQuantities, m Match) (Snapshot, error) {
	s, err := unresolvedWithQuantities(r, created, b, allDimensions(), m.Reason)
	if err != nil {
		return Snapshot{}, err
	}
	s.Status = m.Status
	s.ModelPriceWatchListingID = m.ListingID
	finalizeID(&s)
	return s, ValidateSnapshot(s)
}
func ratesAt(history modelpricewatch.HistoryCatalog, id string, at time.Time, current modelpricewatch.Listing) (Rates, string) {
	model, ok := history.Models[id]
	if !ok {
		return Rates{}, "history_missing"
	}
	var selected *modelpricewatch.HistoryEntry
	for index := range model.History {
		entry := &model.History[index]
		date, err := time.Parse("2006-01-02", entry.Date)
		if err != nil || date.After(at) {
			continue
		}
		if selected == nil || entry.Date > selected.Date {
			selected = entry
		}
	}
	if selected == nil {
		return Rates{}, "no_applicable_historical_price"
	}
	if selected.Event == "correction" || selected.Event == "backfill" {
		return Rates{}, "historical_correction_requires_review"
	}
	return Rates{Input: selected.InputPerMTok, CachedInput: selected.CachedInputPerMTok, Output: selected.OutputPerMTok}, ""
}
func computeCosts(s *Snapshot) {
	rates := []*float64{s.Rates.Input, s.Rates.CachedInput, s.Rates.CacheWriteInput, s.Rates.Output, s.Rates.ReasoningOutput}
	q := []uint64{s.BillableQuantities.Input, s.BillableQuantities.CachedInput, s.BillableQuantities.CacheWriteInput, s.BillableQuantities.Output, s.BillableQuantities.ReasoningOutput}
	costs := []**float64{&s.LineCostsUSD.Input, &s.LineCostsUSD.CachedInput, &s.LineCostsUSD.CacheWriteInput, &s.LineCostsUSD.Output, &s.LineCostsUSD.ReasoningOutput}
	names := []string{"input", "cached_input", "cache_write_input", "output", "reasoning_output"}
	missing := []string{}
	subtotal := 0.0
	for i := range rates {
		if rates[i] == nil {
			if q[i] > 0 {
				missing = append(missing, names[i])
			}
			continue
		}
		v := float64(q[i]) * *rates[i] / 1_000_000
		*costs[i] = &v
		subtotal += v
	}
	s.MissingBillingDimensions = missing
	s.KnownSubtotalUSD = subtotal
	s.PricingComplete = len(missing) == 0
	if s.PricingComplete {
		total := subtotal
		s.TotalCostUSD = &total
	}
}
func finalizeID(s *Snapshot) {
	copy := *s
	copy.SnapshotID = ""
	body, err := json.Marshal(copy)
	if err != nil {
		panic(fmt.Sprintf("marshal validated pricing snapshot: %v", err))
	}
	sum := sha256.Sum256([]byte(body))
	s.SnapshotID = "pricing-" + hex.EncodeToString(sum[:16])
}
