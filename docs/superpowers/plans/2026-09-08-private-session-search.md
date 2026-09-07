# Private Session Search Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. This completes the remaining search portion of the accepted Session-index and all-Sessions plans; do not rebuild completed local filters or event paging.

**Goal:** Make branch/file/error search usable in the existing 全部 Sessions tab while keeping private fact text out of the Vault.

**Architecture:** Authenticate the published project graph once, match only supported typed retained facts, and return a bounded page of namespaced identities. The plugin keeps remote search separate from local index filters and merges the returned identities with the authoritative index. No source log, shell, or model is needed.

**Tech Stack:** Existing Go private store/inspect contracts, TypeScript plugin runner and Vitest.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§7,12.3,17.1/2,18.4; original `2026-09-04-session-index-publication-query.md` Tasks5–6 and `2026-09-04-obsidian-all-sessions-view.md`.

## Global Constraints

- Identity is always `(project_id, provider, session_id)`; index order is `started_at desc nulls last, provider asc, session_id asc`.
- `session-search` only returns matching `(provider, session_id)`, match type, total and paging cursors. It does not return matched paths, error text, commands or query echoes.
- `query` maximum256 UTF-8bytes, page size1..100, cursor maximum4096bytes, response maximum1MiB, existing inspect timeout5seconds. The query is normalized literal text, never a filesystem path, regular expression or shell program.
- Source-unavailable retained facts remain searchable. A missing/unprocessed Session with no retained view is a non-searchable item, not a fabricated match; expose aggregate inspected/unavailable counts without leaking private text.
- No CLI means remote search disabled with the existing single recovery entry; local provider/date/state/availability filters remain working.
- Zero Agent processes, no raw transcript/tool output, no private facts copied to Markdown/index, no schema weakening, no unrelated renderer changes.

### Task 1: Authenticate and query project-wide retained facts

**Files:** Create `internal/inspect/search.go`, `search_test.go`, `project_read.go`; modify `internal/inspect/service.go` only to share existing authentication primitives, `internal/cli/inspect.go`, `inspect_test.go`; add `schemas/session-search-page-v1.schema.json` and synthetic contract fixtures.

**Interfaces:** Preserve `LoadSessionEventPage`, `LoadSessionSummary` and `LoadConversationPage` signatures. Add:

```go
type SearchRequest struct {
    DataRoot, ProjectID, ExpectedGenerationID, QueryKind, Query, Cursor string
    Limit int
}
type SearchHit struct {
    Provider string `json:"provider"`
    SessionID string `json:"session_id"`
    MatchKind string `json:"match_kind"`
}
// SearchPage is a separate contract, not SessionEventPage with empty facts.
func LoadSessionSearch(context.Context, SearchRequest) (SearchPage, error)
func RenderSearchPage(SearchPage) ([]byte, error)
```

- [ ] RED service tests publish real temporary private-store fixtures with multiple providers and repeated native IDs, branch `codex/topic`, file `internal/search.go`, error-signature facts, source-unavailable retained view, and unavailable view. Search matches the correct typed field once per Session; unrelated text in an assistant/request excerpt must not match. For branch search use `Fields["branch"]` on branch facts; for file search use validated project-relative `Fields["path"]` on file/git-status facts; for error search use typed error code/operation and `Fields["error_signature"]` on error/failure facts. Matching uses `strings.Contains(strings.ToLower(field), strings.ToLower(strings.TrimSpace(query)))`; reject empty normalized query, preserve literal separators/metacharacters.
- [ ] RED 154-Session paging with limit100 yields100 then54 unique namespaced hits in accepted index order. Empty result has range0–0 and all cursors null. Wire range is zero-based half-open as the established inspect page contracts; UI renders one-based. Include query256/257bytes, multibyte edge, invalidUTF-8, limit0/101, identity/filter/limit/generation crossing, cursor tamper and retained generation corruption.
- [ ] RED read-only/cancellation tests snapshot files/hashes before and after; no writes/source opens/model calls. A changed physical project binding or published manifest after matching rejects all results. Corrupt selected view/revision must fail closed, not be silently skipped. Reuse existing checkpoint technique without global production instrumentation.

```go
if got.Total != 154 || len(got.Items) != 100 || got.NextCursor == nil {
    t.Fatalf("incomplete first search page: %+v", got)
}
// Query text is not a path: `../../outside` cannot trigger any file read.
```

- [ ] Run focused RED `go test ./internal/inspect ./internal/cli -run 'SessionSearch|SearchCursor|SearchReadOnly' -count=1`.
- [ ] Extract the existing config/physical binding/read-only store/published manifest/ProjectView/index authentication into focused shared helpers in `project_read.go`. Both existing per-Session inspection and new search must retain reauthentication and final manifest comparison. Load selected active revisions with `loadSelectedRevisions`; avoid reopening and decoding the whole project once per Session. Bind cursor MAC to project/generation/index digest/query-kind/normalized-query digest/order version/limit/offset using existing private authentication material; never key only by caller text.
- [ ] Route the already parsed `session-search` command in `runInspect`; document fixed argv in help and preserve JSON error conventions. Validate RenderSearchPage identities, counts, cursor caps, total/range/items, strict fields and response size. Add Go canonical fixture bytes for frontend parity.
- [ ] GREEN focused tests, `go test ./internal/inspect ./internal/cli -count=1`, scoped vet, one full `go test ./...`, `git diff --check`; commit exact files. Independent task review before UI wiring.

### Task 2: Wire bounded private search into the existing Sessions experience

**Files:** Add plugin contract/parser for search page and synthetic fixtures; modify `obsidian-plugin/src/cli/runner.ts`, `src/view/render-scan-records.ts`, `src/view/render-v4-shell.ts`, `src/view/project-view.ts`, `src/view/presentation.ts`, focused Session-state code only if necessary; tests in `tests/cli.test.ts` and `tests/session-search.test.ts`.

**Interfaces:** Add `SessionSearchRequest` with project/generation/queryKind/query/cursor/limit and `CliRunner.searchSessions`. Pass optional `searchSessions` loader through existing host/presentation options. Do not overload local query input with a remote implicit call on each keystroke. A labeled selector `分支 / 文件 / 错误特征`, bounded text input and explicit `搜索` action control private search.

- [ ] RED fixed-argv runner test asserts `inspect session-search --project-id ... --expected-generation-id ... --query-kind ... --query ... --limit ... --json`, optional cursor, `shell:false`,5second timeout,1MiB cap and strict identity/range/match-kind validation. Query metacharacters remain a single literal argument. Accept production Go fixture exactly.
- [ ] RED real-host tests cover explicit submit, loading/error/retry, retained-source results, next/previous/first/last cursors, remote hit total distinct from local-filtered rows, zero results disposing old detail, stale response after project/query/generation switch, and removal/clear of private search restoring the current local index filters. Returned unknown identities or duplicates fail closed. Do not silently stop at the first100matches.
- [ ] RED missing-CLI test proves local filters remain enabled and remote search gives one recovery action, with no remote loader calls. Keyboard Enter submits, focus stays stable, state updates use existing latest-state patch merge.

```ts
expect(execArgs).toContain("--query-kind");
expect(execOptions.shell).toBe(false);
expect(panel.textContent).toContain("154");
// A zero-result remote page must not retain another Session's summary/detail.
```

- [ ] Implement parser/runner and remote state without changing the approved tab order or tree meaning. Local filters apply to current remote identities and state clearly that only the current remote page is being filtered; remote totals and navigation remain visible. Keep remote query text out of saved Vault files and persist only ordinary plugin-local view preferences if needed.
- [ ] GREEN focused CLI/host tests and full `npm --prefix obsidian-plugin run check`. Render desktop580px pane and390px viewport using actual components; assert identity, content, no overlays/console errors, keyboard and no horizontal clipping. Native candidate acceptance belongs to the final combined runtime gate, not a synthetic screenshot claim. Commit exact files for independent review.

## Plan self-review

This plan closes only the private-search portion of S07. Already accepted local-filter and event-page behavior is preserved. Five-tab native acceptance, provider adapters and all semantic/pricing tasks remain in the release execution queue. No new public persistent data contract is introduced except the versioned read-only response; existing index/ledger bytes are unchanged.
