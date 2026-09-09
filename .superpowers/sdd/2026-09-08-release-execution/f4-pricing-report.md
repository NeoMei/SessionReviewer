# F4 pricing backend report

Date: 2026-09-09

Scope: callable ModelPriceWatch/pricing backend plus the pricing-only CLI query and publication path. This report does not claim the user-facing F4 workflow is complete; controller-owned native Obsidian acceptance and whole-feature integration remain separate gates.

## Commits

- `24e5df4 feat: decode ModelPriceWatch catalogs strictly`
- `0bf797c feat: cache ModelPriceWatch catalogs safely`
- `0d5a69f feat: resolve auditable model pricing snapshots`
- `f537910 fix: price codex reported output once`
- `b8f4728 fix: keep unknown pricing inputs nonblocking`
- `e012dd7 fix: preserve unknown zero-quantity rates`
- `a7d587e fix: guard pricing across rate boundaries`
- `149ecfe feat: bind reviewed prices to prior snapshots`
- `ed4f15b feat: accept reviewed catalog pricing`

## Callable API

`internal/modelpricewatch`:

- `DecodeModels(io.Reader, int64) (Catalog, error)`
- `DecodeHistory(io.Reader, int64) (HistoryCatalog, error)`
- `NewClient(HTTPDoer) *Client`
- `(*Client).Fetch(context.Context, Validators) (FetchResult, error)`
- `NewCache(privateRoot string, Fetcher) (*Cache, error)`
- `(*Cache).LoadOrRefresh(context.Context, time.Time) (CatalogSet, Freshness, error)`

The client requests only the two fixed HTTPS ModelPriceWatch endpoints, sends conditional validators, accepts redirects only on the fixed HTTPS origin, requires JSON success responses, and fully validates the pair before use. The cache serializes callers with a persistent cross-process lock, writes private generation files, and atomically publishes one active pointer only after both catalogs validate. Successful retrieval time and failed-attempt time are separate. Current is `<=24h`, stale estimate is `>24h && <=7d`, and expired catalogs cannot yield a new priced snapshot. Failed attempts are throttled for 24 hours without replacing validated bytes or their retrieval time.

`internal/pricing`:

- `NewService(CatalogLoader, []Alias, map[string]UsageAdapter, Clock) (*Service, error)`
- `(*Service).Resolve(context.Context, ResolutionRequest) (Snapshot, error)`
- `(*Service).ResolveReviewedListing(context.Context, ResolutionRequest, string) (Snapshot, error)`
- `(*Service).Supplement(context.Context, ResolutionRequest, Supplement, *Snapshot) (Snapshot, error)`
- `Resolve(ResolveInput) (Snapshot, error)` for deterministic pure resolution
- `Aggregate([]Snapshot) (AggregateResult, error)`
- `MatchListing(BillingRoute, []Alias, modelpricewatch.Catalog, time.Time) Match`
- `CodexUsageAdapter`, `ClaudeUsageAdapter`, and `OpenCodeUsageAdapter`

`ResolutionRequest` binds `ProjectID`, `Provider`, `SessionID`, authenticated `UsageRecordDigest`, actual `BillingRoute{Host, ModelID, Mode, Region}`, `accounting.ModelUsage`, and canonical UTC `StartedAt` and `PricedAt` (`ended_at`). The caller must obtain host/model/mode/region from actual runtime/source billing evidence. Provider or display-model labels never create a route. Missing route fields produce an immutable `pending` snapshot with explicit `unknown` host/mode and retain `Usage.Model` for display; they do not contact the catalog or block scanning.

`ResolveReviewedListing` is the narrow runtime for a human-confirmed exact route and listing ID. It uses the same fixed-origin cached catalog and all temporal/quantity checks, while avoiding a persistent alias configuration for a one-time reviewed selection. It accepts no caller rates or totals. The controller can authenticate the current source usage and publish the resulting snapshot through the same ledger CAS transaction used by supplements.

`internal/cli` exposes these pricing-only commands:

- `pricing refresh [--data-dir PATH] --json` refreshes the private fixed-origin cache and returns aggregate freshness.
- `pricing catalog list [--data-dir PATH] --json` returns sorted, bounded public listing summaries for explicit user selection. Because ModelPriceWatch does not attest the user's actual billing route, every returned `billing_host`, `billing_mode`, and `region` is `null`; the UI must ask the user for those values.
- `pricing catalog accept ... --json` reads an exact `pricing-catalog-selection-v1` body from stdin, bounded to 64 KiB. The body binds project/provider/session/usage identity, confirmed host/model/mode/region, listing ID, and predecessor. Unknown fields and caller-supplied rates, quantities, line costs, or totals are rejected.
- `pricing supplement ... --json` reads the existing strict supplement body and derives quantities and costs server-side.

Both publication commands re-read the accepted project/Vault pair under the project publication lock, compare the exact ledger SHA-256 preimage, authenticate the Session index and private source usage digest, recover the source billing interval and per-model quantities, require the current predecessor, recompute aggregate completeness across every authenticated usage identity, and publish through the existing guarded Markdown transaction. Catalog network I/O finishes before taking the project publication lock. Supersession changes only the predecessor lifecycle status; the remaining historical payload stays byte-for-byte equal.

Exact aliases use the whole route tuple. Case, whitespace, host, mode, model, or region differences do not fuzzy-match. Duplicate exact aliases, unstructured `price_note`, and uncertain promotions remain ambiguous. Historical selection uses the nearest entry no later than `PricedAt`; future-only history and unreviewed correction/backfill applicability remain pending. If an applicable rate changes in `(StartedAt, PricedAt]`, automatic resolution returns `ambiguous_billing_period`; an unchanged catalog observation inside the interval does not create false ambiguity. A manual supplement may price the usage only when its declared effective interval covers the whole Session.

Codex quantities follow the repository's established accounting semantics: input includes cache dimensions and `OutputTokens` already includes reasoning. The adapter subtracts cache input/write from input, bills reported output once, and retains reasoning only as audit metadata. Claude and OpenCode automatic quantities require their explicit reviewed normalization versions; missing proof stays unknown. Unknown rates remain `null`; numeric zero remains an explicit free rate. Zero-quantity dimensions may keep a null rate without preventing a complete known total.

Manual supplements accept only reviewed identity, effective interval, rates, HTTPS source, audit reason, and optional predecessor. The service derives quantities through the versioned adapter and recomputes every line cost, subtotal, and nullable total. Snapshot IDs are deterministic over canonical JSON pricing evidence and exclude only the mutable lifecycle `status`; changing rates, quantities, cost, provenance, route, time, or predecessor still changes the generated ID. Supersession and aggregation use `(provider, session_id, usage_record_digest, billed_model_id)`, reject missing predecessors, forks, cycles, disconnected effective leaves, and model identity changes, and select only current chain leaves.

## Focused verification

- `go test ./internal/modelpricewatch ./internal/pricing -count=1` — PASS
- `go vet ./internal/modelpricewatch ./internal/pricing` — PASS
- `go vet ./internal/modelpricewatch ./internal/pricing ./internal/cli` — PASS
- Focused CLI catalog/supplement/accounting/help tests — PASS. The catalog acceptance fixture drives the real CLI publication path through a real private cache and fixed-origin client backed by a fake HTTP server, verifies only the two fixed ModelPriceWatch URLs are requested, rejects a missing listing without ledger mutation, computes cost from captured usage, preserves immutable predecessor evidence except lifecycle status, verifies project/Vault readback, and verifies rescan retention.
- Windows compile-only checks with `GOOS=windows GOARCH=amd64 go test -c` for `internal/modelpricewatch`, `internal/pricing`, and `internal/cli` — PASS; all outputs are PE32+ x86-64 Windows executables.
- `SESSION_REVIEWER_LIVE_MODELPRICEWATCH=1 go test ./internal/modelpricewatch -run LiveCatalogSmoke -count=1 -v` — PASS.

Live smoke recorded aggregate metadata only: adapter version `1`, models count `248`, history count `248`, and both catalogs updated `2026-09-06`. No response body or catalog was committed.

The real `pricing catalog list` command also completed against the fixed public endpoints using a fresh private temporary cache: status `current`, model/history/listing counts `248/248/248`, listing IDs sorted, and every route field `null`. Only these aggregate assertions were recorded; public response bytes were not committed.

## Shared integration review

The separate shared ledger fix `69f3faa` correctly adds `billed_model_id` to pricing identity in Go and TypeScript and tests same-usage multi-model snapshots and cross-model supersession rejection; no finding.

The controller's scan integration `13bcf2d` preserves old snapshot bytes, keeps per-model/digest current selection, and derives nullable model/project totals. Commit `71bd821` correctly binds `StartedAt` and uses `Accounting.EndedAt` as `PricedAt`; unknown-route scans remain nonblocking, including legal Codex usage where reasoning metadata exceeds reported output. Its focused time test checks `PricedAt`; a direct assertion for propagated `StartedAt` would strengthen regression coverage but no implementation defect was found.

## Remaining integration and acceptance

- Keep automatic scans pending until actual billing route evidence exists; catalog selection is a separate human-confirmed action and never guesses from display labels.
- Verify the controller's Obsidian catalog form exposes public price conditions, labels the billing host as a host, prevents acceptance of an expired catalog, and reports uncertain post-publication readback without claiming the write failed.
- Complete native temporary-Vault query, route confirmation, publication, reload/readback, and failure-path checks.
- Run final shared contract and release gates before describing F4 as a delivered user capability.
