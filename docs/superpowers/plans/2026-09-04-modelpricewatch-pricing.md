# ModelPriceWatch Pricing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve Session usage against auditable, immutable price snapshots from ModelPriceWatch, official sources, or manual supplements while keeping unknown and conditional prices visibly incomplete.

**Architecture:** A global private cache refreshes the ModelPriceWatch models and history catalogs at most once per 24 hours. A reviewed alias table maps actual billing route tuples to one listing ID; no fuzzy model/provider match is allowed. Provider-specific UsageAdapters produce mutually exclusive billable quantities. Accepted pricing creates immutable `pricing-snapshot-v1` records in machine-ledger-v4, and Obsidian renders known subtotal, completeness, age, promotion, and full source links.

**Tech Stack:** Go 1.26 `net/http`, existing pathguard/atomicfile locks, strict JSON, v4 ledger/publication, TypeScript 5.8, Obsidian 1.13, Vitest.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md`

## Global Constraints

- Prerequisites: Gate 0 and Session index publication are complete. Obsidian usage work can follow after the four-tab shell exists.
- Verified live API shapes on 2026-09-04: `models.json` is `{count, updated, data: Listing[]}` and includes `id`, `provider`, `model`, nullable price fields, `promo`, `promo_until`, `price_note`, `pricing_url`, `last_updated`, and `detail_url`; `price-history.json` is `{count, updated, data: {listingID: {model, provider, history[]}}}`. Treat this as an adapter version, not a perpetual guarantee.
- Endpoints are fixed HTTPS URLs: `https://modelpricewatch.com/api/v1/models.json` and `https://modelpricewatch.com/api/v1/price-history.json`. Redirects may remain only on the same origin and must end at HTTPS.
- Each response has a 128 MiB download and parse ceiling, must have successful status, JSON content type, supported schema, unique fields, and complete EOF.
- Cache age uses local `retrieved_at`: current <=24h; stale estimate >24h and <=7d; older than 7d cannot create a newly priced snapshot.
- `price_note` is evidence text only. Any tier, region, batch, cache, modality, promotion, or other condition not represented structurally makes automatic application pending/ambiguous.
- A public zero rate is numeric `0`; unknown is `null`. Missing price never becomes zero cost and never blocks scanning.
- Listing directory/cache refresh does not rewrite existing snapshots. Corrections create a superseding snapshot.
- The UI must link the full ModelPriceWatch detail URL and official pricing URL when available and state that estimates are not invoices.

## File Structure and Ownership

- `internal/modelpricewatch/client.go`: fixed-origin HTTP refresh and strict adapters.
- `internal/modelpricewatch/cache.go`: global private lock, metadata, atomic files.
- `internal/pricing/aliases.go`: reviewed billing-route-to-listing mapping.
- `internal/pricing/usage_adapter.go`: provider-specific mutually exclusive billable quantities.
- `internal/pricing/resolve.go`: temporal applicability and snapshot creation.
- `internal/reviewv4/`: snapshot chains and nullable accounting aggregates.
- `obsidian-plugin/src/view/render-usage.ts`: one full-width card per model with status and source links.

---

### Task 1: Implement strict ModelPriceWatch response adapters

**Files:**
- Create: `internal/modelpricewatch/types.go`, `decode.go`, `decode_test.go`
- Create: `internal/modelpricewatch/testdata/models-min.json`, `history-min.json`

**Interfaces:**

```go
type Catalog struct { Count int; Updated string; Listings map[string]Listing }
type HistoryCatalog struct { Count int; Updated string; Models map[string]ModelHistory }
func DecodeModels(io.Reader, int64) (Catalog, error)
func DecodeHistory(io.Reader, int64) (HistoryCatalog, error)
```

- [ ] **Step 1: Capture trimmed synthetic fixtures matching the verified live shape.** Do not commit the full remote catalogs.

```json
{"count":1,"updated":"2026-09-03","data":[{"id":"openai-gpt-test","provider":"OpenAI","model":"GPT Test","input_per_mtok":1.0,"output_per_mtok":2.0,"cached_input_per_mtok":null,"promo":false,"promo_until":null,"price_note":null,"pricing_url":"https://example.com/pricing","last_updated":"2026-09-03","detail_url":"https://modelpricewatch.com/models/openai-gpt-test/"}]}
```
- [ ] **Step 2: Write RED tests** for nullable cache rate, explicit zero, duplicate listing ID, count mismatch, duplicate JSON field, unknown top-level field, invalid date/URL, malformed/truncated JSON, nonfinite/negative price, and 128 MiB+1 input.

```go
func TestDecodeModelsPreservesUnknownAndFreeRates(t *testing.T) {
    catalog, err := DecodeModels(strings.NewReader(modelsFixture(nil, ptr(0.0))), 128<<20)
    if err != nil { t.Fatal(err) }
    row := catalog.Listings["openai-gpt-test"]
    if row.CachedInputPerMTok != nil || row.OutputPerMTok == nil || *row.OutputPerMTok != 0 { t.Fatalf("row=%+v", row) }
}
```
- [ ] **Step 3: Add a RED conditional-note test:** a listing with `price_note` containing tier/region/promo terms is decoded but marked `HasUnstructuredConditions=true`, never directly “current”.
- [ ] **Step 4: Run RED:** `go test ./internal/modelpricewatch -run Decode -count=1`.
- [ ] **Step 5: Implement strict streaming decode** with exact allowed fields, count/ID/history-key consistency, and an adapter schema-version constant. Preserve nullable rates and source URLs/dates.

```go
func DecodeModels(reader io.Reader, limit int64) (Catalog, error) {
    body, err := readCompleteBounded(reader, limit)
    if err != nil { return Catalog{}, err }
    if err := rejectDuplicateJSONKeys(body); err != nil { return Catalog{}, err }
    var wire modelsWire
    if err := decodeStrict(body, &wire); err != nil { return Catalog{}, err }
    return validateModelsWire(wire)
}
```
- [ ] **Step 6: Run all Go gates and commit when authorized** with message `feat: decode ModelPriceWatch catalogs strictly`.

---

### Task 2: Refresh a global private cache safely and infrequently

**Files:**
- Create: `internal/modelpricewatch/client.go`, `client_test.go`
- Create: `internal/modelpricewatch/cache.go`, `cache_test.go`
- Modify: `internal/platform/paths.go`, `paths_test.go`

**Interfaces:**

```go
type HTTPDoer interface { Do(*http.Request) (*http.Response, error) }
type Cache struct { /* private root and lock */ }
func (c *Cache) LoadOrRefresh(ctx context.Context, now time.Time) (CatalogSet, Freshness, error)
```

- [ ] **Step 1: Write RED HTTP tests** for fixed URLs, user agent, ETag/If-Modified-Since, 304 reuse, same-origin HTTPS redirects, cross-origin/downgrade redirect refusal, status/content-type/length failures, timeout, and partial body.

```go
func TestClientRejectsCrossOriginRedirect(t *testing.T) {
    server := redirectServer(t, "https://evil.example/models.json")
    client := NewClient(server.HTTPClient(), server.ModelsURL(), server.HistoryURL())
    _, err := client.Fetch(context.Background(), Validators{})
    if codeOf(err) != "unsafe_redirect" { t.Fatalf("err=%v", err) }
}
```
- [ ] **Step 2: Write RED cache tests** proving two projects/processes trigger at most one refresh per 24h, 24h+1 may refresh, failed refresh keeps old bytes and retrieved time, and files/lock have private permissions on macOS/Windows.
- [ ] **Step 3: Run RED:** `go test ./internal/modelpricewatch ./internal/platform -run 'Client|Cache|Refresh' -count=1`.
- [ ] **Step 4: Implement separate atomically replaced catalog files plus metadata.** Validate both new responses before replacing either active pair; do not expose half-updated models/history.

```go
return c.withGlobalLock(func() (CatalogSet, Freshness, error) {
    current := c.loadValidatedPair()
    if now.Sub(current.RetrievedAt) <= 24*time.Hour { return current.Catalogs, FreshCurrent, nil }
    next, validators, err := c.client.Fetch(ctx, current.Validators)
    if err != nil { return staleOrError(current, now, err) }
    return c.replaceValidatedPairAtomically(next, validators, now)
})
```
- [ ] **Step 5: Run all Go gates and commit when authorized** with message `feat: cache ModelPriceWatch catalogs safely`.

---

### Task 3: Match exact billing routes and reject ambiguous conditions

**Files:**
- Create: `internal/pricing/aliases.go`, `aliases_test.go`
- Create: `config/modelpricewatch-aliases.json`
- Modify: `internal/config/config.go`, `config_test.go`

**Interfaces:**

```go
type BillingRoute struct { Host, ModelID, Mode string; Region *string }
type Alias struct { Route BillingRoute; ListingID string }
type Match struct { Status PriceStatus; ListingID *string; Reason string }
func MatchListing(route BillingRoute, aliases []Alias, catalog modelpricewatch.Catalog, at time.Time) Match
```

- [ ] **Step 1: Write RED tests** for exact tuple match, case/whitespace mismatch, same model at two hosts, duplicate tuple to different listings, missing listing, retired listing, unknown region/mode, structured promotion, and unstructured `price_note`.

```go
func TestMatchListingDoesNotFuzzyMatchModelName(t *testing.T) {
    aliases := []Alias{{Route: BillingRoute{Host: "api.openai.com", ModelID: "gpt-exact", Mode: "api"}, ListingID: "openai-gpt"}}
    got := MatchListing(BillingRoute{Host: "api.openai.com", ModelID: "GPT-EXACT", Mode: "api"}, aliases, catalog(), fixedTime)
    if got.Status != pricing.Pending || got.ListingID != nil { t.Fatalf("got=%+v", got) }
}
```
- [ ] **Step 2: Run RED:** `go test ./internal/pricing ./internal/config -run 'Alias|MatchListing' -count=1`.
- [ ] **Step 3: Implement reviewed aliases.** Validate uniqueness at startup; do not derive aliases by lowercasing, substring, provider name, or model display name. Return `pending` for no exact mapping and `ambiguous` for conflicts/conditions.

```go
key := routeKey{Host: route.Host, ModelID: route.ModelID, Mode: route.Mode, Region: route.Region}
listingID, ok := exactAliases[key]
if !ok { return Match{Status: Pending, Reason: "no_exact_billing_route_alias"} }
listing := catalog.Listings[listingID]
if listing.HasUnstructuredConditions { return Match{Status: Ambiguous, ListingID: &listingID, Reason: "unstructured_price_conditions"} }
```
- [ ] **Step 4: Seed only aliases proven by real billing metadata and catalog listing IDs.** An empty alias table is valid and yields pending snapshots.
- [ ] **Step 5: Run all Go gates and commit when authorized** with message `feat: match exact model billing routes`.

---

### Task 4: Produce provider-specific billable quantities

**Files:**
- Create: `internal/pricing/usage_adapter.go`, `usage_adapter_test.go`
- Create: `internal/pricing/codex_usage.go`, `claude_usage.go`, `opencode_usage.go`
- Modify: `internal/accounting/accounting.go`, `accounting_test.go`

**Interfaces:**

```go
type BillableQuantities struct { Input, CachedInput, CacheWriteInput, Output, ReasoningOutput int64; RuleVersion string }
type UsageAdapter interface {
    Provider() string
    Billable(accounting.ModelUsage) (BillableQuantities, []string, error)
}
```

- [ ] **Step 1: Write RED provider fixtures** covering overlapping total/input/cache fields, Claude cache-read/cache-creation, OpenCode providerID/modelID, reasoning included in output vs separately billed, missing model, and totals mismatch.

```go
func TestClaudeBillableKeepsCacheReadAndCreationExclusive(t *testing.T) {
    got, missing, err := ClaudeUsageAdapter{}.Billable(usageWith(100, 20, 5, 30, 0))
    if err != nil || len(missing) != 0 { t.Fatalf("missing=%v err=%v", missing, err) }
    if got.Input != 75 || got.CachedInput != 20 || got.CacheWriteInput != 5 || got.Output != 30 { t.Fatalf("got=%+v", got) }
}
```
- [ ] **Step 2: Assert quantities are nonnegative and mutually exclusive.** The sum/relationship rule is provider-version-specific; no generic subtraction may create negative or double-counted quantities.
- [ ] **Step 3: Run RED:** `go test ./internal/pricing ./internal/accounting -run 'Billable|UsageAdapter' -count=1`.
- [ ] **Step 4: Implement explicit versioned rules** and persist route metadata plus rule version with each usage association. Unsupported shapes produce missing dimensions, not guessed cost.

```go
return BillableQuantities{Input: usage.InputTokens-usage.CachedInputTokens-usage.CacheWriteInputTokens, CachedInput: usage.CachedInputTokens, CacheWriteInput: usage.CacheWriteInputTokens, Output: usage.OutputTokens, RuleVersion: "claude-usage-v1"}, nil, nil
```
- [ ] **Step 5: Run all Go gates and commit when authorized** with message `feat: derive provider billable quantities`.

---

### Task 5: Create immutable pricing snapshots and nullable aggregates

**Files:**
- Create: `internal/pricing/resolve.go`, `resolve_test.go`, `aggregate.go`, `aggregate_test.go`
- Modify: `internal/reviewv4/types.go`, `codec.go`, tests
- Modify: `internal/presentation/render.go`, tests
- Modify: `internal/contextupdate/service.go`

**Interfaces:**

```go
type ResolveInput struct { ProjectID string; Session SessionKey; UsageDigest string; Route BillingRoute; Usage accounting.ModelUsage; PricedAt time.Time; Catalog CatalogSet; Prior *Snapshot }
func Resolve(ResolveInput) (pricing.Snapshot, error)
func Aggregate(usages []UsageAssociation, snapshots []Snapshot) accounting.ProjectSummary
```

- [ ] **Step 1: Write RED temporal tests** for catalog <=24h current, 24h+1 to 7d stale estimate with age, >7d pending, historical price selection by `priced_at`, promotion status/end, and future-only price history.

```go
func TestResolveUsesStaleEstimateOnlyThroughSevenDays(t *testing.T) {
    current := resolveAtAge(t, 24*time.Hour+time.Second)
    if current.Status != StaleEstimate { t.Fatalf("status=%s", current.Status) }
    expired := resolveAtAge(t, 7*24*time.Hour+time.Second)
    if expired.Status != Pending || expired.TotalCostUSD != nil { t.Fatalf("snapshot=%+v", expired) }
}
```
- [ ] **Step 2: Write RED snapshot tests** for nullable rates/line costs, exact decimal calculation, known subtotal, incomplete total null, complete total, explicit free price, missing dimensions, and deterministic snapshot ID.
- [ ] **Step 3: Add chain tests** for manual supplement/correction, `supersedes_snapshot_id`, cycle/branch rejection, immutable old snapshots, and aggregation selecting the newest valid chain head only.
- [ ] **Step 4: Run RED:** `go test ./internal/pricing ./internal/reviewv4 ./internal/presentation ./internal/contextupdate -run 'Resolve|Snapshot|Aggregate|Pricing' -count=1`.
- [ ] **Step 5: Implement resolution independent of scan success.** A catalog/network/match failure emits unresolved pricing state while Token usage and Session publication continue. Store accepted snapshots inside ledger v4 and validate the entire chain on load.

```go
match := MatchListing(in.Route, in.Aliases, in.Catalog.Models, in.PricedAt)
if match.Status == Pending || match.Status == Ambiguous {
    return unresolvedSnapshot(in, match.Status, match.Reason), nil
}
return pricedSnapshot(in, selectApplicableHistory(in.Catalog.History, *match.ListingID, in.PricedAt))
```
- [ ] **Step 6: Run all Go gates and commit when authorized** with message `feat: persist auditable pricing snapshots`.

---

### Task 6: Expose guarded manual supplement CLI

**Files:**
- Create: `internal/cli/pricing.go`, `pricing_test.go`
- Modify: `internal/cli/run.go`, `run_test.go`, `contracts.go`
- Modify: `internal/pricing/resolve.go`, `resolve_test.go`

**Interfaces:**

```go
type SupplementInput struct {
    SchemaVersion int
    Route BillingRoute
    PricedAt string
    Rates NullableRates
    SourceURL string
    AuditReason string
    SupersedesSnapshotID *string
}
func (s *Service) Supplement(context.Context, SupplementRequest) (Snapshot, error)
```

- [ ] **Step 1: Write RED CLI tests** for exact project/provider/session/usage-digest/ledger-SHA argv, 64 KiB stdin, invalid source URL, missing audit reason, stale ledger preimage, wrong usage digest, unknown dimensions, and attempted caller-provided line costs/totals.

```go
func TestPricingSupplementRejectsCallerCalculatedCost(t *testing.T) {
    body := `{"schema_version":1,"route":{"host":"api.example","model_id":"m","mode":"api","region":null},"priced_at":"2026-09-04T00:00:00Z","rates":{"input":1},"line_costs_usd":{"input":999},"source_url":"https://example.com/pricing","audit_reason":"manual invoice check"}`
    code := runPricing([]string{"supplement", "--project-id", "project-p", "--provider", "codex", "--session-id", "s1", "--usage-record-digest", validDigest, "--expected-ledger-sha256", validSHA, "--json"}, strings.NewReader(body))
    if code != 2 { t.Fatalf("code=%d", code) }
}
```

- [ ] **Step 2: Run RED:** `go test ./internal/cli ./internal/pricing -run Supplement -count=1`.
- [ ] **Step 3: Implement strict stdin decode and root dispatch.** Reload ledger/usage under lock, verify preimages, derive billable quantities server-side, recompute line costs/subtotal/total, append a `manual_supplement` snapshot, and publish ledger through the guarded human transaction.

```go
case "pricing":
    return runPricing(args[1:], os.Stdin, stdout, stderr, pricingDependencies())
```

- [ ] **Step 4: Run all Go gates and commit when authorized** with message `feat: add guarded manual pricing supplements`.

---

### Task 7: Render honest full-width usage cards in Obsidian

**Files:**
- Modify: `obsidian-plugin/src/contracts/review-v4.ts`
- Modify: `obsidian-plugin/src/data/contracts-v4.ts`
- Modify: `obsidian-plugin/src/cli/runner.ts`, `obsidian-plugin/tests/cli.test.ts`
- Modify: `obsidian-plugin/src/view/render-usage.ts`
- Modify: `obsidian-plugin/src/styles.css`
- Create: `obsidian-plugin/tests/pricing-view.test.ts`

- [ ] **Step 1: Write RED card tests** for each price status, known subtotal vs total unavailable, explicit free, missing dimensions, cache age, promotion/end, manual supplement, superseded audit chain, and disclaimer.

```ts
it("never renders an unknown total as zero", () => {
  const panel = renderUsage(modelWithPricing({ knownSubtotalUsd: 1.25, totalCostUsd: null, pricingComplete: false }));
  expect(panel.textContent).toContain("已知小计 $1.25");
  expect(panel.textContent).toContain("总费用暂不可用");
  expect(panel.textContent).not.toContain("总费用 $0");
});
```
- [ ] **Step 2: Assert one full-width card per model** at desktop widths, with an incomplete last row allowed only if responsive layout later uses multiple columns. Full clickable `定价来源` URLs must not collapse into an unlabeled icon.
- [ ] **Step 3: Add URL safety tests.** Render only validated HTTPS ModelPriceWatch/official source URLs; unsafe/malformed URLs become non-clickable text with an invalid-source diagnostic.
- [ ] **Step 4: Run RED:** `cd obsidian-plugin && npm test -- pricing-view.test.ts styles.test.ts`.
- [ ] **Step 5: Implement status-localized cards** showing rate dimensions, quantities, line costs, known subtotal, total/completeness, `last_updated`, `retrieved_at` age, ModelPriceWatch detail attribution, official pricing link, and “估算并非账单”.

```ts
const total = snapshot.pricingComplete && snapshot.totalCostUsd !== null
  ? definition("总费用", formatUsd(snapshot.totalCostUsd))
  : definition("总费用", "暂不可用");
card.append(definition("已知小计", formatUsd(snapshot.knownSubtotalUsd)), total, pricingSources(snapshot));
```

Add a “人工补价/纠错” form that submits `pricing-supplement-v1` through a fixed `CliRunner.pricingSupplement` method; the form sends rates and audit input only, never calculated cost fields.

```ts
await cli.pricingSupplement(model.review.projectId, identity, usageDigest, model.source.ledgerSha256, {
  schemaVersion: 1, route, pricedAt, rates, sourceUrl, auditReason, supersedesSnapshotId
});
```
- [ ] **Step 6: Run `npm run check` and commit when authorized** with message `feat: present auditable model pricing`.

---

### Task 8: Network, migration, and installed-bundle acceptance

**Files:**
- Create: `docs/session-review/pricing-acceptance.md`
- Modify: `.github/workflows/ci.yml`

- [ ] **Step 1: Run deterministic fake-server integration tests** for first refresh, 304, concurrent projects, timeout, rate limit, malformed/oversized response, partial pair, and stale fallback. No CI test depends on live ModelPriceWatch availability.
- [ ] **Step 2: Run a read-only live adapter probe** against both fixed endpoints, recording HTTP status/content type, top-level shape, adapter version, catalog `updated`, count consistency, and no response body in logs. A shape change blocks release and requires an adapter/test update.
- [ ] **Step 3: Migrate v3 priced/unpriced accounting.** Existing price rows become `legacy_unverified`; unknown totals become null; no historical source/date/host is invented.
- [ ] **Step 4: In a disposable real Vault, verify** current, promotion, stale estimate, manual supplement, ambiguous, pending, legacy unverified, and superseded-chain cards; then disconnect network and confirm scan/index still update.
- [ ] **Step 5: Verify source links open the exact ModelPriceWatch detail and official pricing URLs, all dates/statuses are visible, and no unknown price displays `$0`.
- [ ] **Step 6: Run macOS/Windows Go and plugin gates** and record commands, cache ages, fixture routes, snapshot IDs, ledger/bundle hashes, screenshots, and live-probe metadata in the acceptance document; commit when authorized.
