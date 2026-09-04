# Session Index Publication and Query Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish a complete cumulative Session index with every successful scan and expose bounded, generation-bound summary, event, and search queries without copying raw transcripts into the Vault.

**Architecture:** Build `session-index-v1` from the current generation manifest plus the last accepted index, preserve absent identities as `source_unavailable`, and add it as the fourth atomic projection file. A read-only inspect service loads immutable SessionView/Observation objects by authenticated digest and returns deterministic summaries or cursor pages. Provider orchestration merges enabled adapters while isolating provider-level availability failures.

**Tech Stack:** Go 1.26, existing `memory`, `memorystore`, `scan`, `presentation`, `publication`, `pathguard`, `source` adapters, JSON CLI.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md`

## Global Constraints

- Prerequisite: `2026-09-04-obsidian-context-gate-0-contracts.md` is complete.
- Begin from released 0.3.5 v3 (`ea5b1ba`) in the isolated implementation worktree and preserve the original dirty user changes. No destructive Git cleanup.
- `session-index.json` is complete or not published. Limits are 65,536 entries and 64 MiB; overflow returns `session_index_capacity_exceeded` and retains the previous accepted generation.
- Index order is `started_at desc nulls last, provider asc, session_id asc`. Identity is always `(project_id, provider, session_id)`.
- Raw messages, hidden reasoning, instructions, absolute paths, secrets, and full tool output never enter the index, Vault, or inspect response.
- Claude Code and OpenCode end-to-end acceptance is blocked until their real SourceAdapters exist. If absent, execute Tasks 5 and 6 of `docs/superpowers/plans/2026-08-30-multi-agent-session-review.md`; UI labels alone do not satisfy this plan.
- Every task runs focused tests, all Go gates, and conditional authorized commits as described in Gate 0.

## File Structure and Ownership

- `internal/source/manager.go`: fan-in of enabled SourceAdapters and provider diagnostics.
- `internal/sessionindex/build.go`: cumulative index construction and stable ordering.
- `internal/presentation/render.go`: creates the four-file scan render plan.
- `internal/publication/`: journals, syncs, verifies, and recovers the fourth file.
- `internal/inspect/service.go`: read-only generation-bound queries.
- `internal/inspect/cursor.go`: authenticated opaque page cursors and ordinal anchors.
- `internal/cli/inspect.go`: strict root dispatch and JSON diagnostics.

---

### Task 1: Compose enabled providers without cross-provider data loss

**Files:**
- Create: `internal/source/manager.go`, `manager_test.go`
- Modify: `internal/scan/service.go`, `service_test.go`
- Modify: `internal/contextupdate/service.go`
- Modify: `internal/config/config.go`, `config_test.go`

**Interfaces:**

```go
type NamedAdapter struct { Provider string; Adapter source.Adapter; Required bool }
type ProviderDiagnostic struct { Provider, Code string }
func DiscoverAll(ctx context.Context, adapters []NamedAdapter) (source.Discovery, []ProviderDiagnostic, error)

type scan.Options struct {
    // existing fields
    Adapters []source.NamedAdapter
}
```

- [ ] **Step 1: Write RED tests** for Codex+Claude+OpenCode candidates with the same native Session ID, one uninstalled optional provider, and one corrupt configured provider. Assert optional unavailability produces a provider diagnostic while candidates from other providers remain; corruption of a configured provider fails closed.

```go
func TestDiscoverAllKeepsOtherProvidersWhenOptionalAdapterUnavailable(t *testing.T) {
    got, diagnostics, err := DiscoverAll(context.Background(), []NamedAdapter{
        {Provider: "codex", Adapter: fakeAdapter{sessions: []string{"same"}}, Required: true},
        {Provider: "claude", Adapter: fakeAdapter{err: ErrProviderUnavailable}, Required: false},
        {Provider: "opencode", Adapter: fakeAdapter{sessions: []string{"same"}}, Required: false},
    })
    if err != nil || len(got.Candidates) != 2 || diagnostics[0].Provider != "claude" { t.Fatalf("got=%+v diagnostics=%+v err=%v", got, diagnostics, err) }
}
```
- [ ] **Step 2: Run RED:** `go test ./internal/source ./internal/scan ./internal/contextupdate -run 'Provider|Adapter' -count=1`.
- [ ] **Step 3: Implement deterministic fan-in.** Sort adapters by provider, candidates by provider/session, reject provider spoofing, and replace the singular `Options.Adapter` use. Instantiate only real verified adapters in `contextupdate`; do not synthesize empty Claude/OpenCode adapters.

```go
for _, named := range sortedAdapters(adapters) {
    discovered, err := named.Adapter.Discover(ctx)
    if errors.Is(err, ErrProviderUnavailable) && !named.Required { diagnostics = append(diagnostics, ProviderDiagnostic{Provider: named.Provider, Code: "provider_unavailable"}); continue }
    if err != nil { return source.Discovery{}, diagnostics, fmt.Errorf("discover %s: %w", named.Provider, err) }
    if err := appendVerifiedProvider(&combined, named.Provider, discovered); err != nil { return source.Discovery{}, diagnostics, err }
}
```
- [ ] **Step 4: Run GREEN:** `gofmt -w internal/source internal/scan internal/contextupdate internal/config && go test ./internal/source ./internal/scan ./internal/contextupdate ./internal/config -count=1 && go test ./... && go vet ./... && go mod tidy -diff`.
- [ ] **Step 5: Commit when authorized:** `git add internal/source internal/scan internal/contextupdate internal/config && git commit -m "feat: compose enabled session providers"`.

---

### Task 2: Build the cumulative complete Session index

**Files:**
- Create: `internal/sessionindex/build.go`, `build_test.go`
- Modify: `internal/memorystore/store.go`, `store_test.go`
- Modify: `internal/scan/service.go`, `service_test.go`

**Interfaces:**

```go
type BuildInput struct {
    ProjectView memory.ProjectView
    Manifest memory.GenerationManifest
    SessionViews map[SessionKey]*memory.SessionView
    Previous *sessionindex.Document
    GeneratedAt time.Time
}
func Build(BuildInput) (sessionindex.Document, error)
```

Processing-state mapping is explicit: clean indexed -> `complete`; indexed with diagnostics or unprojected/undecodable facts -> `partial`; unreadable/missing/ambiguous/unsupported terminal failure without a usable SessionView -> `error`; discovered but not processed -> `unprocessed`. Source availability is computed independently.

- [ ] **Step 1: Write RED tests with 154 mixed Sessions.** Include old identities absent from the new discovery, null start times, duplicate native IDs across providers, partial/errored sessions, and an unchanged prior entry. Assert no identity disappears and the coverage sums equal total.

```go
func TestBuildRetainsAbsentPriorSessionAsSourceUnavailable(t *testing.T) {
    previous := indexWith(entry("claude", "old", "complete", "available"))
    got, err := Build(BuildInput{ProjectView: projectView(), Manifest: manifest(), Previous: &previous, GeneratedAt: fixedTime})
    if err != nil { t.Fatal(err) }
    row := requireEntry(t, got, SessionKey{Provider: "claude", SessionID: "old"})
    if row.ProcessingState != "complete" || row.SourceAvailability != "source_unavailable" { t.Fatalf("row=%+v", row) }
}
```
- [ ] **Step 2: Add capacity RED cases:** 65,537 entries and a rendered document above 64 MiB both return `session_index_capacity_exceeded`; the memorystore published pointer remains unchanged.
- [ ] **Step 3: Run RED:** `go test ./internal/sessionindex ./internal/scan ./internal/memorystore -run 'Build|Capacity|Cumulative' -count=1`.
- [ ] **Step 4: Implement immutable build and canonical render.** Preserve prior factual counts for absent sources, change only source availability/last-seen fields, and bind digest/project/generation/ProjectView digest before storing the object.

```go
for key, prior := range previousByKey(in.Previous) {
    if _, seen := next[key]; seen { continue }
    retained := prior
    retained.SourceAvailability = sessionindex.SourceUnavailable
    next[key] = retained
}
doc.Sessions = stableSessionOrder(maps.Values(next))
doc.Coverage = calculateCoverage(doc.Sessions)
```
- [ ] **Step 5: Run GREEN and commit when authorized:** `gofmt -w internal/sessionindex internal/scan internal/memorystore && go test ./internal/sessionindex ./internal/scan ./internal/memorystore -count=1 && go test ./... && go vet ./... && go mod tidy -diff`; then `git add internal/sessionindex internal/scan internal/memorystore && git commit -m "feat: build cumulative session index"`.

---

### Task 3: Publish and recover the four-file atomic set

**Files:**
- Modify: `internal/reviewv2/types.go` or its post-Gate-0 compatibility shim
- Modify: `internal/presentation/render.go`, `render_test.go`
- Modify: `internal/publication/types.go`, `service.go`, `journal.go`
- Modify: `internal/publication/service_test.go`, `recovery_test.go`, `journal_test.go`
- Modify: `internal/contextupdate/service.go`

**Interfaces:**

```go
const SessionIndexRelativePath = "docs/session-review/.session-reviewer/session-index.json"

type RenderInput struct {
    // existing fields
    SessionIndex []byte
}
```

- [ ] **Step 1: Extend render tests** to require exactly four scan files and exact expected/preimage bytes for `session-index.json`.

```go
func TestRenderIncludesSessionIndexAsFourthAtomicFile(t *testing.T) {
    plan := renderPlan(t, []byte(`{"schema_version":1}`))
    if len(plan.Files) != 4 || plan.Files[3].Relative != reviewv4.SessionIndexRelativePath { t.Fatalf("files=%+v", plan.Files) }
}
```
- [ ] **Step 2: Extend journal crash-point tests** across each write, Project→Vault sync, verification, and rollback. Assert no observable mixed generation after recovery.
- [ ] **Step 3: Run RED:** `go test ./internal/presentation ./internal/publication ./internal/contextupdate -run 'SessionIndex|FourFile|Recovery' -count=1`.
- [ ] **Step 4: Implement the fourth mapping.** Add `.session-reviewer/session-index.json` to `vaultRelativePath`, proof hashes, verification, repair/status diagnostics, and the existing 64 MiB safe read ceiling. Replace comments and assertions that say “3 files”.

```go
case reviewv4.SessionIndexRelativePath:
    return path.Join(vaultReviewPath, ".session-reviewer/session-index.json")
```
- [ ] **Step 5: Run GREEN and commit when authorized:** `gofmt -w internal/presentation internal/publication internal/contextupdate internal/reviewv2 && go test ./internal/presentation ./internal/publication ./internal/contextupdate -count=1 && go test ./... && go vet ./... && go mod tidy -diff`; then commit `feat: publish session index atomically`.

---

### Task 4: Implement deterministic Session summaries

**Files:**
- Create: `internal/inspect/service.go`, `summary.go`, `service_test.go`, `summary_test.go`
- Modify: `internal/memorystore/store.go`

**Interfaces:**

```go
type Store interface {
    LoadPublished() (string, memory.GenerationManifest, error)
    LoadObject(kind memorystore.ObjectKind, digest string) ([]byte, error)
}
type SummaryRequest struct { ProjectID, Provider, SessionID, ExpectedGenerationID string }
func (s *Service) SessionSummary(context.Context, SummaryRequest) (SessionSummary, error)
```

- [ ] **Step 1: Write RED tests** for dependency authentication, 32-item section caps, 512-byte excerpts, deterministic ordering, source-unavailable reuse, generation mismatch, and a malicious absolute path in an Observation field.

```go
func TestSessionSummaryCapsSectionsAndReportsOmitted(t *testing.T) {
    service := serviceWithObservations(40, func(i int) memory.ObservationSummary { return verification(i) })
    got, err := service.SessionSummary(context.Background(), summaryRequest())
    if err != nil { t.Fatal(err) }
    if len(got.Verifications.Items) != 32 || got.Verifications.Total != 40 || got.Verifications.Omitted != 8 { t.Fatalf("section=%+v", got.Verifications) }
}
```
- [ ] **Step 2: Run RED:** `go test ./internal/inspect -run SessionSummary -count=1`.
- [ ] **Step 3: Implement typed rule projection.** Load only digests referenced by the accepted index/manifest; emit phase boundaries, operations, verification, errors, and unresolved facts with per-section total/shown/omitted counts, rule ID/version, revision IDs, and dependency digest.

```go
func boundedSection(items []inspect.Item) inspect.Section {
    sort.SliceStable(items, func(i, j int) bool { return eventLess(items[i], items[j]) })
    total := len(items)
    if total > 32 { items = items[:32] }
    return inspect.Section{Total: total, Shown: len(items), Omitted: total-len(items), Items: items}
}
```
- [ ] **Step 4: Run GREEN and commit when authorized:** `gofmt -w internal/inspect internal/memorystore && go test ./internal/inspect ./internal/memorystore -count=1 && go test ./... && go vet ./... && go mod tidy -diff`; then commit `feat: add deterministic session summaries`.

---

### Task 5: Implement authenticated event pages and bounded search

**Files:**
- Create: `internal/inspect/cursor.go`, `events.go`, `search.go`
- Create: `internal/inspect/cursor_test.go`, `events_test.go`, `search_test.go`

**Interfaces:**

```go
type EventRequest struct { ProjectID, Provider, SessionID, ExpectedGenerationID, Cursor string; Anchor, Limit int }
type SearchRequest struct { ProjectID, ExpectedGenerationID, QueryKind, Query, Cursor string; Limit int }
func (s *Service) SessionEvents(context.Context, EventRequest) (SessionEventPage, error)
func (s *Service) SessionSearch(context.Context, SearchRequest) (SearchPage, error)
```

Cursor payload contains project/provider/session/generation/sort-version/filter-digest/page-size/start ordinal and an HMAC; the opaque encoding is at most 4096 bytes.

- [ ] **Step 1: Write RED paging tests** for empty, first, middle, last, exact multiples, anchor 0/total+1, cursor identity mixing, changed page size/filter, stale generation, tampering, and 101 limit.

```go
func TestSessionEventsRejectsCursorFromAnotherProvider(t *testing.T) {
    cursor := signedCursor(t, cursorPayload{ProjectID: "project-p", Provider: "codex", SessionID: "same", GenerationID: "g1", PageSize: 100})
    _, err := service.SessionEvents(context.Background(), EventRequest{ProjectID: "project-p", Provider: "claude", SessionID: "same", ExpectedGenerationID: "g1", Cursor: cursor, Limit: 100})
    if codeOf(err) != "stale_cursor" { t.Fatalf("err=%v", err) }
}
```
- [ ] **Step 2: Write RED privacy/bounds tests.** A response above the configured byte ceiling fails `response_too_large`; search query above 256 UTF-8 bytes fails; returned events omit raw prompts, reasoning, paths, and tool output.
- [ ] **Step 3: Run RED:** `go test ./internal/inspect -run 'Cursor|SessionEvents|SessionSearch' -count=1`.
- [ ] **Step 4: Implement stable sort** `occurred_at asc, sequence asc, revision_id asc`, one-based ranges, null cursors at total zero, ordinal anchors, normalized literal text matching, and no filesystem interpretation of query text.

```go
if request.Anchor != 0 && (request.Anchor < 1 || request.Anchor > total) {
    return SessionEventPage{}, contractError("anchor_out_of_range", "anchor is outside the current result")
}
start := pageStart(request.Anchor, request.Limit, total)
return buildPage(sortedEvents, start, request.Limit, cursorSigner)
```
- [ ] **Step 5: Run GREEN and commit when authorized:** `gofmt -w internal/inspect && go test ./internal/inspect -count=1 && go test ./... && go vet ./... && go mod tidy -diff`; then commit `feat: page and search session facts safely`.

---

### Task 6: Expose strict read-only inspect CLI commands

**Files:**
- Create: `internal/cli/inspect.go`, `inspect_test.go`
- Modify: `internal/cli/run.go`, `run_test.go`
- Modify: `internal/cli/contracts.go`

- [ ] **Step 1: Add exact JSON acceptance tests** for all three commands, typed errors, stdout-only success, stderr-only diagnostics, exit 2 for syntax, and nonzero service failures.

```go
func TestInspectSessionEventsRequiresExpectedGeneration(t *testing.T) {
    code, _, stderr := runCLI("inspect", "session-events", "--project-id", "project-p", "--provider", "codex", "--session-id", "s1", "--limit", "100", "--json")
    if code != 2 || !strings.Contains(stderr, "expected-generation-id") { t.Fatalf("code=%d stderr=%q", code, stderr) }
}
```
- [ ] **Step 2: Run RED:** `go test ./internal/cli -run Inspect -count=1`.
- [ ] **Step 3: Add `inspect` root dispatch** and wire the Gate-0 parser to `inspect.Service`. Require `--json`, explicit project/provider/session/generation where specified, and reject unknown flags or file paths.

```go
case "inspect":
    return runInspect(args[1:], stdout, stderr, inspectDependencies())
```
- [ ] **Step 4: Run GREEN:** `gofmt -w internal/cli && go test ./internal/cli -count=1 && go test ./... && go vet ./... && go mod tidy -diff`.
- [ ] **Step 5: Commit when authorized:** `git add internal/cli && git commit -m "feat: expose bounded session inspection"`.

---

### Task 7: Integration, performance, and cross-provider acceptance

**Files:**
- Create: `test/sessionindex/gate_test.go`
- Create: `testdata/sessionindex/154-sessions/`
- Modify: `.github/workflows/ci.yml`
- Create: `docs/session-review/session-index-acceptance.md`

- [ ] **Step 1: Run a 154-Session fixture scan twice** and assert first publication contains 154 index entries, second identical scan changes no canonical bytes, and every entry can resolve a summary or a typed unavailable/error response.

```go
func TestGateSessionIndexIsCompleteAndRepeatable(t *testing.T) {
    first := runFixtureScan(t, "testdata/sessionindex/154-sessions")
    second := runFixtureScan(t, "testdata/sessionindex/154-sessions")
    if first.Index.Coverage.Total != 154 || !bytes.Equal(first.IndexBytes, second.IndexBytes) { t.Fatalf("first=%+v second=%+v", first.Index.Coverage, second.Index.Coverage) }
}
```
- [ ] **Step 2: Run a long-Session fixture with 2,438 events** and assert page ranges/cursors reach the final event without loading the full event set into the plugin-facing response.
- [ ] **Step 3: Run failure injection** at index capacity, one corrupt Session, one unavailable source, stale cursor, and each publication crash point. Verify previous generation remains usable.
- [ ] **Step 4: Run macOS and Windows CI:** `go test ./... && go vet ./... && go mod tidy -diff`.
- [ ] **Step 5: With real enabled adapters, scan one Codex, one Claude Code, and one OpenCode Session for the same project.** Record namespaced identities and provider-isolated failure behavior. If either non-Codex adapter is absent, mark this plan incomplete and execute the named prerequisite plan tasks.
- [ ] **Step 6: Record commands, timings, peak sizes, generation IDs, hashes, and screenshots/observations in `docs/session-review/session-index-acceptance.md`; commit evidence when authorized.**
