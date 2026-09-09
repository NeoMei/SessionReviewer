package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/modelpricewatch"
	"github.com/neomei/SessionReviewer/internal/pricing"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

type pricingCatalogDoer func(*http.Request) (*http.Response, error)

type pricingCatalogLoaderFunc func(context.Context, time.Time) (modelpricewatch.CatalogSet, modelpricewatch.Freshness, error)

func (f pricingCatalogLoaderFunc) LoadOrRefresh(ctx context.Context, at time.Time) (modelpricewatch.CatalogSet, modelpricewatch.Freshness, error) {
	return f(ctx, at)
}

func (f pricingCatalogDoer) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestPricingCatalogContractAndBodyAreExactAndBounded(t *testing.T) {
	root := t.TempDir()
	args := []string{"catalog", "accept", "--project-id", "project-p", "--provider", "codex", "--session-id", "session-1", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA, "--data-dir", root, "--json"}
	request, err := ParsePricingCatalogContract(args)
	if err != nil || request.Command != "catalog-accept" || request.DataDir != root {
		t.Fatalf("request=%+v err=%v", request, err)
	}
	list, err := ParsePricingCatalogContract([]string{"catalog", "list", "--data-dir", root, "--json"})
	if err != nil || list.Command != "catalog-list" || list.DataDir != root {
		t.Fatalf("list request=%+v err=%v", list, err)
	}
	for _, invalid := range [][]string{
		append(append([]string(nil), args...), "payload.json"),
		{"catalog", "accept", "--project-id", "project-p", "--provider", "codex", "--session-id", "session-1", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA, "--url", "https://example.test", "--json"},
		{"catalog", "--help"},
	} {
		if _, err := ParsePricingCatalogContract(invalid); err == nil {
			t.Fatalf("accepted invalid catalog argv: %v", invalid)
		}
	}
	selection := PricingCatalogSelection{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: "project-p", Provider: "opencode", SessionID: "ses_nativeChild", UsageRecordDigest: contractTestDigest, BillingHost: "api.example.test", BilledModelID: "model-a", BillingMode: "api", ModelPriceWatchListingID: "provider-model-test", SupersedesSnapshotID: stringPointer("pricing-old")}
	body, err := renderPricingCatalogSelection(selection)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parsePricingCatalogSelection(body)
	if err != nil || !reflect.DeepEqual(parsed, selection) {
		t.Fatalf("parsed=%+v err=%v", parsed, err)
	}
	withRates := bytes.TrimSuffix(body, []byte("}"))
	withRates = append(withRates, []byte(`,"rates":{"input":1}}`)...)
	if _, err := parsePricingCatalogSelection(withRates); err == nil {
		t.Fatal("accepted caller-provided catalog rates")
	}
}

func TestPricingCatalogListReturnsSortedPublicSelectionMetadata(t *testing.T) {
	str := func(value string) *string { return &value }
	number := func(value float64) *float64 { return &value }
	set := modelpricewatch.CatalogSet{
		Models: modelpricewatch.Catalog{Count: 2, Listings: map[string]modelpricewatch.Listing{
			"z-listing": {ID: "z-listing", Provider: "Provider Z", Model: "Model Z", Category: "chat", Status: "active", InputPerMTok: number(1), OutputPerMTok: number(2), Promo: true, PromoUntil: str("2026-10-01"), PriceNote: str("batch only"), HasUnstructuredConditions: true, PricingURL: "https://example.test/z", DetailURL: "https://modelpricewatch.com/models/z-listing/", LastUpdated: "2026-09-09"},
			"a-listing": {ID: "a-listing", Provider: "Provider A", Model: "Model A", Category: "chat", Status: "active", InputPerMTok: number(0), OutputPerMTok: number(3), PricingURL: "https://example.test/a", DetailURL: "https://modelpricewatch.com/models/a-listing/", LastUpdated: "2026-09-08"},
		}},
		History: modelpricewatch.HistoryCatalog{Count: 2},
	}
	factory := func(string) (pricing.CatalogLoader, error) {
		return pricingCatalogLoaderFunc(func(context.Context, time.Time) (modelpricewatch.CatalogSet, modelpricewatch.Freshness, error) {
			return set, modelpricewatch.Freshness{Status: modelpricewatch.FreshCurrent, RetrievedAt: time.Date(2026, 9, 9, 1, 2, 3, 0, time.UTC)}, nil
		}), nil
	}
	var out, diag bytes.Buffer
	if code := runPricingWithCatalogFactory([]string{"catalog", "list", "--data-dir", t.TempDir(), "--json"}, bytes.NewReader([]byte("ignored")), &out, &diag, factory); code != 0 {
		t.Fatalf("catalog list failed %d %s %s", code, &out, &diag)
	}
	var response pricingCatalogListResponse
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ModelCount != 2 || response.HistoryCount != 2 || len(response.Listings) != 2 || response.Listings[0].ListingID != "a-listing" || response.Listings[1].ListingID != "z-listing" {
		t.Fatalf("unexpected list response: %+v", response)
	}
	selected := response.Listings[1]
	if selected.BillingHost != nil || selected.BillingMode != nil || selected.Region != nil || selected.PriceNote == nil || !selected.HasUnstructuredCondition || selected.InputPerMTok == nil || *selected.InputPerMTok != 1 {
		t.Fatalf("listing lost public metadata or invented route: %+v", selected)
	}
}

func TestPricingCatalogAcceptPublishesServerCalculatedCatalogSnapshot(t *testing.T) {
	projectRoot, vaultRoot, dataRoot, sessionsRoot := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = projectRoot
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v %s", err, output)
		}
	}
	const sessionID = "88888888-8888-4888-8888-888888888888"
	var source bytes.Buffer
	for _, record := range []map[string]any{
		{"timestamp": "2026-09-09T00:00:00Z", "type": "session_meta", "payload": map[string]any{"id": sessionID, "cwd": projectRoot}},
		{"timestamp": "2026-09-09T00:00:01Z", "type": "event_msg", "payload": map[string]any{"type": "token_count", "info": map[string]any{"last_token_usage": map[string]any{"input_tokens": 100, "cached_input_tokens": 20, "output_tokens": 10, "total_tokens": 110}}}},
	} {
		if err := json.NewEncoder(&source).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sessionsRoot, "rollout-2026-09-09T00-00-00-"+sessionID+".jsonl"), source.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	runCurrentInitCLI(t, []string{"init", "--project", projectRoot, "--vault", vaultRoot, "--data-dir", dataRoot, "--write"})
	cfg, err := config.Load(filepath.Join(dataRoot, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	projectID := cfg.Projects[0].ID
	scanArgs := []string{"scan", "--project-id", projectID, "--sessions-root", sessionsRoot, "--data-dir", dataRoot, "--json"}
	runCurrentInitCLI(t, scanArgs)
	load := func() (reviewv4.MachineLedger, []byte) {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(projectRoot, reviewv2.MachineLedgerRelativePath))
		if err != nil {
			t.Fatal(err)
		}
		ledger, err := reviewv4.DecodeLedger(body)
		if err != nil {
			t.Fatal(err)
		}
		return ledger, body
	}
	before, ledgerBody := load()
	if len(before.PricingSnapshots) != 1 {
		t.Fatalf("pending snapshots=%+v", before.PricingSnapshots)
	}
	prior := before.PricingSnapshots[0]
	selection := PricingCatalogSelection{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: projectID, Provider: "codex", SessionID: sessionID, UsageRecordDigest: prior.UsageRecordDigest, BillingHost: "api.example.test", BilledModelID: prior.BilledModelID, BillingMode: "api", ModelPriceWatchListingID: "provider-model-test", SupersedesSnapshotID: &prior.SnapshotID}
	body, err := renderPricingCatalogSelection(selection)
	if err != nil {
		t.Fatal(err)
	}
	models, err := os.ReadFile(filepath.Join("..", "modelpricewatch", "testdata", "models-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	history, err := os.ReadFile(filepath.Join("..", "modelpricewatch", "testdata", "history-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	models = bytes.ReplaceAll(models, []byte(`"cached_input_per_mtok":null`), []byte(`"cached_input_per_mtok":0.5`))
	history = bytes.ReplaceAll(history, []byte(`"cached_input_per_mtok":null`), []byte(`"cached_input_per_mtok":0.5`))
	history = bytes.Replace(history, []byte(`"event":"correction"`), []byte(`"event":"snapshot"`), 1)
	requests := []string{}
	factory := func(root string) (pricing.CatalogLoader, error) {
		doer := pricingCatalogDoer(func(request *http.Request) (*http.Response, error) {
			requests = append(requests, request.URL.String())
			responseBody := models
			if request.URL.String() == modelpricewatch.HistoryURL {
				responseBody = history
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(responseBody)), Request: request}, nil
		})
		return modelpricewatch.NewCache(filepath.Join(root, "modelpricewatch"), modelpricewatch.NewClient(doer))
	}
	args := []string{"catalog", "accept", "--project-id", projectID, "--provider", "codex", "--session-id", sessionID, "--usage-record-digest", prior.UsageRecordDigest, "--expected-ledger-sha256", testBareSHA(ledgerBody), "--data-dir", dataRoot, "--json"}
	var out, diag bytes.Buffer
	missing := selection
	missing.ModelPriceWatchListingID = "missing-listing"
	missingBody, err := renderPricingCatalogSelection(missing)
	if err != nil {
		t.Fatal(err)
	}
	if code := runPricingWithCatalogFactory(args, bytes.NewReader(missingBody), &out, &diag, factory); code != 1 || !strings.Contains(out.String(), "pricing_listing_unavailable") {
		t.Fatalf("missing listing result=%d %s %s", code, &out, &diag)
	}
	_, unchanged := load()
	if !bytes.Equal(unchanged, ledgerBody) {
		t.Fatal("missing listing changed the pricing ledger")
	}
	out.Reset()
	diag.Reset()
	if code := runPricingWithCatalogFactory(args, bytes.NewReader(body), &out, &diag, factory); code != 0 {
		t.Fatalf("catalog accept failed %d %s %s", code, &out, &diag)
	}
	if !reflect.DeepEqual(requests, []string{modelpricewatch.ModelsURL, modelpricewatch.HistoryURL}) {
		t.Fatalf("catalog queried unexpected endpoints: %v", requests)
	}
	after, projectLedger := load()
	if len(after.PricingSnapshots) != 2 || after.Accounting.TotalCostUSD == nil || *after.Accounting.TotalCostUSD <= 0 {
		t.Fatalf("catalog price was not aggregated: %+v", after.Accounting)
	}
	historical := after.PricingSnapshots[0]
	if historical.Status != pricing.PriceSuperseded || historical.SnapshotID != prior.SnapshotID {
		t.Fatalf("prior lifecycle not retained: before=%+v after=%+v", prior, historical)
	}
	historical.Status = prior.Status
	if !reflect.DeepEqual(historical, prior) {
		t.Fatal("catalog acceptance mutated immutable prior pricing payload")
	}
	current := after.PricingSnapshots[1]
	if current.Status != pricing.PriceCurrent || current.ModelPriceWatchListingID == nil || *current.ModelPriceWatchListingID != "provider-model-test" || current.SourceKind != "modelpricewatch" || current.Rates.Input == nil || current.TotalCostUSD == nil {
		t.Fatalf("wrong catalog snapshot: %+v", current)
	}
	vaultLedger, err := os.ReadFile(filepath.Join(vaultRoot, cfg.Projects[0].VaultReviewPath, ".session-reviewer/ledger.json"))
	if err != nil || !bytes.Equal(vaultLedger, projectLedger) {
		t.Fatalf("Vault catalog price readback differs: %v", err)
	}
	runCurrentInitCLI(t, scanArgs)
	rescanned, _ := load()
	if len(rescanned.PricingSnapshots) != 2 || rescanned.Accounting.TotalCostUSD == nil || *rescanned.Accounting.TotalCostUSD != *after.Accounting.TotalCostUSD {
		t.Fatal("rescan rewrote or lost accepted catalog price")
	}
}

func TestPricingCatalogRejectsOversizedOrMismatchedBodyBeforeCatalogAccess(t *testing.T) {
	args := []string{"catalog", "accept", "--project-id", "project-p", "--provider", "codex", "--session-id", "session-1", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA, "--json"}
	called := false
	factory := func(string) (pricing.CatalogLoader, error) {
		called = true
		return nil, nil
	}
	for _, body := range [][]byte{
		bytes.Repeat([]byte(" "), MaxDecisionInputBytes+1),
		[]byte(`{"schema_version":1,"minimum_reader_version":"0.4.0","project_id":"other"}`),
	} {
		var out, diag bytes.Buffer
		if code := runPricingWithCatalogFactory(args, bytes.NewReader(body), &out, &diag, factory); code != 2 {
			t.Fatalf("invalid body exit=%d out=%s", code, &out)
		}
	}
	if called {
		t.Fatal("invalid body accessed catalog")
	}
}

func stringPointer(value string) *string { return &value }
