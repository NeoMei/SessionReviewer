# F4 pricing backend report

Date: 2026-09-09

Scope: independent callable backend only. This report does not claim the user-facing F4 workflow is complete; the shared scan caller, supplement CLI, and native Obsidian usage UI remain controller-owned integration gates.

## Commits

- `24e5df4 feat: decode ModelPriceWatch catalogs strictly`
- `0bf797c feat: cache ModelPriceWatch catalogs safely`
- `0d5a69f feat: resolve auditable model pricing snapshots`
- `f537910 fix: price codex reported output once`
- `b8f4728 fix: keep unknown pricing inputs nonblocking`
- `e012dd7 fix: preserve unknown zero-quantity rates`

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
- `(*Service).Supplement(context.Context, ResolutionRequest, Supplement, *Snapshot) (Snapshot, error)`
- `Resolve(ResolveInput) (Snapshot, error)` for deterministic pure resolution
- `Aggregate([]Snapshot) (AggregateResult, error)`
- `MatchListing(BillingRoute, []Alias, modelpricewatch.Catalog, time.Time) Match`
- `CodexUsageAdapter`, `ClaudeUsageAdapter`, and `OpenCodeUsageAdapter`

`ResolutionRequest` binds `ProjectID`, `Provider`, `SessionID`, authenticated `UsageRecordDigest`, actual `BillingRoute{Host, ModelID, Mode, Region}`, `accounting.ModelUsage`, and canonical UTC `PricedAt`. The caller must obtain host/model/mode/region from actual runtime/source billing evidence. Provider or display-model labels never create a route. Missing route fields produce an immutable `pending` snapshot with explicit `unknown` host/mode and retain `Usage.Model` for display; they do not contact the catalog or block scanning.

Exact aliases use the whole route tuple. Case, whitespace, host, mode, model, or region differences do not fuzzy-match. Duplicate exact aliases, unstructured `price_note`, and uncertain promotions remain ambiguous. Historical selection uses the nearest entry no later than `PricedAt`; future-only history and unreviewed correction/backfill applicability remain pending.

Codex quantities follow the repository's established accounting semantics: input includes cache dimensions and `OutputTokens` already includes reasoning. The adapter subtracts cache input/write from input, bills reported output once, and retains reasoning only as audit metadata. Claude and OpenCode automatic quantities require their explicit reviewed normalization versions; missing proof stays unknown. Unknown rates remain `null`; numeric zero remains an explicit free rate. Zero-quantity dimensions may keep a null rate without preventing a complete known total.

Manual supplements accept only reviewed identity, effective interval, rates, HTTPS source, audit reason, and optional predecessor. The service derives quantities through the versioned adapter and recomputes every line cost, subtotal, and nullable total. Snapshot IDs are deterministic over canonical JSON values. Supersession and aggregation use `(provider, session_id, usage_record_digest, billed_model_id)`, reject missing predecessors, forks, cycles, disconnected effective leaves, and model identity changes, and select only current chain leaves.

## Focused verification

- `go test ./internal/modelpricewatch ./internal/pricing -count=1` — PASS
- `go vet ./internal/modelpricewatch ./internal/pricing` — PASS
- Windows compile-only checks with `GOOS=windows GOARCH=amd64 go test -c` for both packages — PASS; both outputs are PE32+ x86-64 Windows executables.
- `SESSION_REVIEWER_LIVE_MODELPRICEWATCH=1 go test ./internal/modelpricewatch -run LiveCatalogSmoke -count=1 -v` — PASS.

Live smoke recorded aggregate metadata only: adapter version `1`, models count `248`, history count `248`, and both catalogs updated `2026-09-06`. No response body or catalog was committed.

## Shared integration review

The separate shared ledger fix `69f3faa` correctly adds `billed_model_id` to pricing identity in Go and TypeScript and tests same-usage multi-model snapshots and cross-model supersession rejection; no finding.

The controller's scan integration `13bcf2d` preserves old snapshot bytes, keeps per-model/digest current selection, and derives nullable model/project totals. Review found that its initial billing time used `Accounting.StartedAt`; the accepted rule requires `EndedAt`. The controller has an uncommitted correction visible in the shared worktree. Unknown-route scans are supported by backend commits `f537910` and `b8f4728`, including legal Codex usage where reasoning metadata exceeds reported output.

## Remaining controller wiring

- Commit and verify the `EndedAt` scan correction and the shared context-update integration.
- Supply exact observed billing route metadata before enabling automatic catalog resolution; current scan fallback must remain pending.
- Wire the private global cache/service lifecycle without making scans depend on network availability.
- Complete guarded supplement CLI/CAS publication and the native usage cards/source links.
- Run shared contract, actual scan, native client, and release gates before describing F4 as a delivered user capability.
