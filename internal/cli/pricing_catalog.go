package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/neomei/SessionReviewer/internal/modelpricewatch"
	"github.com/neomei/SessionReviewer/internal/pricing"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

type PricingCatalogSelection struct {
	SchemaVersion            int     `json:"schema_version" required:"true"`
	MinimumReaderVersion     string  `json:"minimum_reader_version" required:"true"`
	ProjectID                string  `json:"project_id" required:"true"`
	Provider                 string  `json:"provider" required:"true"`
	SessionID                string  `json:"session_id" required:"true"`
	UsageRecordDigest        string  `json:"usage_record_digest" required:"true"`
	BillingHost              string  `json:"billing_host" required:"true"`
	BilledModelID            string  `json:"billed_model_id" required:"true"`
	BillingMode              string  `json:"billing_mode" required:"true"`
	Region                   *string `json:"region" required:"true" nullable:"true"`
	ModelPriceWatchListingID string  `json:"modelpricewatch_listing_id" required:"true"`
	SupersedesSnapshotID     *string `json:"supersedes_snapshot_id" required:"true" nullable:"true"`
}

type pricingCatalogFactory func(string) (pricing.CatalogLoader, error)

func ParsePricingCatalogContract(args []string) (PricingRequest, error) {
	if len(args) < 2 || args[0] != "catalog" {
		return PricingRequest{}, contractError("pricing catalog subcommand is required")
	}
	if args[1] == "list" {
		flags, err := parseContractFlags(args[2:], map[string]bool{"data-dir": true, "json": true})
		if err != nil {
			return PricingRequest{}, err
		}
		if err := validateInspectDataDir(flags.values["data-dir"]); err != nil {
			return PricingRequest{}, err
		}
		return PricingRequest{Command: "catalog-list", DataDir: flags.values["data-dir"]}, nil
	}
	if args[1] != "accept" {
		return PricingRequest{}, contractError("unknown pricing catalog subcommand")
	}
	flags, err := parseContractFlags(args[2:], map[string]bool{"project-id": true, "provider": true, "session-id": true, "usage-record-digest": true, "expected-ledger-sha256": true, "data-dir": true, "json": true})
	if err != nil {
		return PricingRequest{}, err
	}
	if err = requireFlags(flags, "project-id", "provider", "session-id", "usage-record-digest", "expected-ledger-sha256"); err != nil {
		return PricingRequest{}, err
	}
	if err = requireSafeIDs(flags, "project-id", "provider", "session-id"); err != nil {
		return PricingRequest{}, err
	}
	if err = requireDigest(flags.values["usage-record-digest"]); err != nil {
		return PricingRequest{}, err
	}
	if err = requireBareSHA(flags.values["expected-ledger-sha256"]); err != nil {
		return PricingRequest{}, err
	}
	if err = validateInspectDataDir(flags.values["data-dir"]); err != nil {
		return PricingRequest{}, err
	}
	return PricingRequest{Command: "catalog-accept", DataDir: flags.values["data-dir"], ProjectID: flags.values["project-id"], Provider: flags.values["provider"], SessionID: flags.values["session-id"], UsageRecordDigest: flags.values["usage-record-digest"], ExpectedLedgerSHA256: flags.values["expected-ledger-sha256"]}, nil
}

// PricingCatalogListing is the bounded public metadata needed to select an
// exact ModelPriceWatch entry. Billing route fields are deliberately null:
// ModelPriceWatch does not attest the user's actual host, mode, or region.
type PricingCatalogListing struct {
	ListingID                string   `json:"listing_id"`
	Provider                 string   `json:"provider"`
	Model                    string   `json:"model"`
	Category                 string   `json:"category"`
	Status                   string   `json:"status"`
	BillingHost              *string  `json:"billing_host"`
	BillingMode              *string  `json:"billing_mode"`
	Region                   *string  `json:"region"`
	InputPerMTok             *float64 `json:"input_per_mtok"`
	CachedInputPerMTok       *float64 `json:"cached_input_per_mtok"`
	OutputPerMTok            *float64 `json:"output_per_mtok"`
	Promo                    bool     `json:"promo"`
	PromoUntil               *string  `json:"promo_until"`
	PriceNote                *string  `json:"price_note"`
	HasUnstructuredCondition bool     `json:"has_unstructured_condition"`
	PricingURL               string   `json:"pricing_url"`
	DetailURL                string   `json:"detail_url"`
	LastUpdated              string   `json:"last_updated"`
}

type pricingCatalogListResponse struct {
	SchemaVersion        int                     `json:"schema_version"`
	MinimumReaderVersion string                  `json:"minimum_reader_version"`
	Status               string                  `json:"status"`
	RetrievedAt          string                  `json:"retrieved_at"`
	ModelCount           int                     `json:"model_count"`
	HistoryCount         int                     `json:"history_count"`
	RefreshError         string                  `json:"refresh_error"`
	Listings             []PricingCatalogListing `json:"listings"`
}

func pricingCatalogList(ctx context.Context, dataRoot string, factory pricingCatalogFactory) (pricingCatalogListResponse, error) {
	loader, err := factory(dataRoot)
	if err != nil {
		return pricingCatalogListResponse{}, err
	}
	set, fresh, err := loader.LoadOrRefresh(ctx, time.Now().UTC())
	if err != nil {
		return pricingCatalogListResponse{}, err
	}
	ids := make([]string, 0, len(set.Models.Listings))
	for id := range set.Models.Listings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	listings := make([]PricingCatalogListing, 0, len(ids))
	for _, id := range ids {
		row := set.Models.Listings[id]
		listings = append(listings, PricingCatalogListing{
			ListingID:                row.ID,
			Provider:                 row.Provider,
			Model:                    row.Model,
			Category:                 row.Category,
			Status:                   row.Status,
			InputPerMTok:             row.InputPerMTok,
			CachedInputPerMTok:       row.CachedInputPerMTok,
			OutputPerMTok:            row.OutputPerMTok,
			Promo:                    row.Promo,
			PromoUntil:               row.PromoUntil,
			PriceNote:                row.PriceNote,
			HasUnstructuredCondition: row.HasUnstructuredConditions,
			PricingURL:               row.PricingURL,
			DetailURL:                row.DetailURL,
			LastUpdated:              row.LastUpdated,
		})
	}
	return pricingCatalogListResponse{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", Status: string(fresh.Status), RetrievedAt: fresh.RetrievedAt.UTC().Format(time.RFC3339), ModelCount: set.Models.Count, HistoryCount: set.History.Count, RefreshError: fresh.LastRefreshError, Listings: listings}, nil
}

func parsePricingCatalogSelection(body []byte) (PricingCatalogSelection, error) {
	var selection PricingCatalogSelection
	if len(body) > MaxDecisionInputBytes {
		return selection, errors.New("pricing catalog selection exceeds input limit")
	}
	if err := strictjson.Decode(body, &selection); err != nil {
		return selection, err
	}
	if selection.SchemaVersion != 1 || selection.MinimumReaderVersion != "0.4.0" ||
		!safeContractID(selection.ProjectID) || !safeContractID(selection.Provider) || !safeSessionContractID(selection.SessionID) ||
		requireDigest(selection.UsageRecordDigest) != nil || !exactCatalogValue(selection.BillingHost) ||
		!exactCatalogValue(selection.BilledModelID) || !exactCatalogValue(selection.BillingMode) ||
		(selection.Region != nil && (!exactCatalogValue(*selection.Region) || len(*selection.Region) > 128)) || !safeContractID(selection.ModelPriceWatchListingID) ||
		(selection.SupersedesSnapshotID != nil && !safeContractID(*selection.SupersedesSnapshotID)) {
		return selection, errors.New("pricing catalog selection is invalid")
	}
	return selection, nil
}

func renderPricingCatalogSelection(selection PricingCatalogSelection) ([]byte, error) {
	body, err := strictjson.Encode(selection)
	if err != nil {
		return nil, err
	}
	parsed, err := parsePricingCatalogSelection(body)
	if err != nil || !reflect.DeepEqual(parsed, selection) {
		return nil, errors.Join(errors.New("rendered catalog selection is invalid"), err)
	}
	return body, nil
}

func exactCatalogValue(value string) bool {
	if value == "" || len(value) > 4096 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func readPricingCatalogSelection(input io.Reader) (PricingCatalogSelection, error) {
	body, err := io.ReadAll(io.LimitReader(input, MaxDecisionInputBytes+1))
	if err != nil || len(body) > MaxDecisionInputBytes {
		return PricingCatalogSelection{}, errors.New("pricing catalog selection exceeds input limit")
	}
	return parsePricingCatalogSelection(bytes.Clone(body))
}

func validatePricingCatalogIdentity(request PricingRequest, selection PricingCatalogSelection) error {
	if selection.ProjectID != request.ProjectID || selection.Provider != request.Provider || selection.SessionID != request.SessionID || selection.UsageRecordDigest != request.UsageRecordDigest {
		return errors.New("catalog selection identity differs from requested usage")
	}
	return nil
}

func applyPricingCatalogSelection(ctx context.Context, request PricingRequest, selection PricingCatalogSelection, factory pricingCatalogFactory) (pricing.Snapshot, error) {
	dataRoot := resolveDataDir(request.DataDir)
	if factory == nil {
		return pricing.Snapshot{}, ContractError{Code: "pricing_catalog_unavailable", Message: "public pricing catalog unavailable; existing prices are unchanged"}
	}
	loader, err := factory(dataRoot)
	if err != nil {
		return pricing.Snapshot{}, ContractError{Code: "pricing_catalog_unavailable", Message: "public pricing catalog unavailable; existing prices are unchanged"}
	}
	// Public catalog I/O happens before the project publication lock. The
	// authenticated source identity and ledger preimage are rechecked afterward.
	set, fresh, err := loader.LoadOrRefresh(ctx, time.Now().UTC())
	if err != nil {
		return pricing.Snapshot{}, ContractError{Code: "pricing_catalog_unavailable", Message: "public pricing catalog unavailable; existing prices are unchanged"}
	}
	if _, exists := set.Models.Listings[selection.ModelPriceWatchListingID]; !exists {
		return pricing.Snapshot{}, ContractError{Code: "pricing_listing_unavailable", Message: "selected listing is not in the current public pricing catalog"}
	}
	loaded := loadedPricingCatalog{set: set, fresh: fresh}
	return applyAuthenticatedPricing(ctx, request, pricingTarget{ModelID: selection.BilledModelID, SupersedesSnapshotID: selection.SupersedesSnapshotID}, func(auth authenticatedPricingUsage) (pricing.Snapshot, error) {
		service, err := pricing.NewService(loaded, nil, map[string]pricing.UsageAdapter{"codex": pricing.CodexUsageAdapter{}}, nil)
		if err != nil {
			return pricing.Snapshot{}, err
		}
		return service.ResolveReviewedListing(ctx, pricing.ResolutionRequest{ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID, UsageRecordDigest: request.UsageRecordDigest, Route: pricing.BillingRoute{Host: selection.BillingHost, ModelID: selection.BilledModelID, Mode: selection.BillingMode, Region: selection.Region}, Usage: auth.Usage, StartedAt: auth.StartedAt, PricedAt: auth.EndedAt, Prior: auth.Prior}, selection.ModelPriceWatchListingID)
	})
}

type loadedPricingCatalog struct {
	set   modelpricewatch.CatalogSet
	fresh modelpricewatch.Freshness
}

func (l loadedPricingCatalog) LoadOrRefresh(context.Context, time.Time) (modelpricewatch.CatalogSet, modelpricewatch.Freshness, error) {
	return l.set, l.fresh, nil
}
