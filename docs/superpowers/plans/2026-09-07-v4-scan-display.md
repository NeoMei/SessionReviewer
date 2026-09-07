# v4 Scan Display Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make already scanned Session content browsable in the real Obsidian v4 panel.

**Architecture:** Read validated published private observations through the existing event-page wire contract and a new CLI dispatch. The plugin consumes that read-only endpoint, renders Session coverage and paged event excerpts, and remembers project selection. Existing Markdown/ledger acceptance semantics remain untouched.

**Tech Stack:** Go; TypeScript DOM; Vitest/jsdom; Obsidian API.

**Spec:** docs/superpowers/specs/2026-09-07-v4-scan-display.md

## Global Constraints

- No raw Codex JSONL reads, rescans or model calls for display.
- No edits to machine ledgers, accounting, source records, user review documents or shared project mappings.
- Reuse `inspect.SessionEventPage` / `SessionEventPageV1`; limits 1–100; excerpt bound 512 bytes.
- Bind project, provider, session, generation, session-view digest and page cursor; reject stale/mismatched input.
- Preserve top navigation and existing Markdown read-only/acceptance boundaries and legacy rendering.
- Work only in `codex/v4-scan-display`; no merge, push or public release. Local install with backup is approved.
- The current Session has 83 indexed events and 4684 undecodable records; do not claim full source coverage or invent missing agent replies.

## Task 1: Read-only published Session event API

**Files:** Create `internal/inspect/service.go`, `internal/inspect/service_test.go`, `internal/cli/inspect.go`, `internal/cli/inspect_test.go`; modify `internal/cli/run.go`; extend existing read-only store helpers only if required.

**Interfaces:** Consumes `ParseInspectContract`, `memorystore.OpenReadOnly/LoadPublished/LoadObject`, validated memory observations and existing `inspect.RenderEventPage`. Produces the CLI argv specified in the spec and canonical `SessionEventPage` JSON for Task 2. Nonzero failures emit `{ "error": { "code": "stale_generation", "message": "..." } }` (with the corresponding bounded safe error code for other failures).

- [ ] Write failing CLI and service tests using isolated fixture stores, never real user sources. Literal expectations: 3 events with limit 2 returns sequences 1/2, range 1–2, total 3; next returns sequence 3, range 3–3 and null next; first/last and anchor navigate deterministically.

```go
// Test at the real command boundary; fixture helper publishes immutable test observations.
code := Run([]string{"inspect", "session-events", "--project-id", fixture.projectID,
  "--provider", "codex", "--session-id", "session-1", "--expected-generation-id", fixture.generation,
  "--limit", "2", "--json"}, &out, &errOut)
if code != 0 { t.Fatalf("code=%d stderr=%s", code, errOut.String()) }
page, err := inspect.ParseEventPage(out.Bytes())
if err != nil || page.Total != 3 || page.RangeStart != 1 || page.RangeEnd != 2 { t.Fatalf("page=%+v err=%v", page, err) }
```

- [ ] Run `go test ./internal/cli ./internal/inspect` and record expected RED before production code.
- [ ] Implement `inspect` root dispatch, exact request parsing and environment data-dir resolution. Resolve mapping; open only existing private store; authenticate published snapshot and requested Session membership; load validated selected revisions and project immutable view. Sort in canonical event order. Derive bounded safe excerpts, preserving truthful kinds, timestamps and coverage. Do not output hidden reasoning or secrets.

```go
// Required flow, using concrete existing types at each boundary:
request, err := ParseInspectContract(args)
// validate request -> resolve mapping -> OpenReadOnly -> LoadPublished
// reject different generation -> select requested indexed session -> validate immutable digest
// resolve deterministic events -> validate bound cursor -> slice page -> RenderEventPage
```

- [ ] Add rejected malformed/tampered/changed-generation/wrong-project/wrong-session cursors, unknown Session, absent private state and corrupted immutable object tests. Compare fixture data tree contents and permissions before/after to prove read-only behavior. Include non-ASCII excerpt boundary and no-secret output tests.
- [ ] Run focused tests, then `go test ./...`; commit implementation and tests, report RED/GREEN evidence and exact API behavior for Task 2.

## Task 2: v4 panel Session/event browser and project lifecycle

**Files:** Create `obsidian-plugin/src/view/render-scan-records.ts` and its focused tests. Modify `src/cli/runner.ts`, `src/view/project-view.ts`, `src/view/presentation.ts`, `src/view/render-shell.ts` (state shape only if needed), `src/data/repository.ts`, `styles.css` plus corresponding CLI/repository/view tests. Paths above are relative to `obsidian-plugin/`.

**Interfaces:** Consumes Task 1 CLI through `CliRunner.getSessionEvents(request)` returning validated `SessionEventPageV1`. Request fields: `{projectId, provider, sessionId, expectedGenerationId, limit, cursor?, anchor?}`. Public Session list comes only from `MarkdownSnapshot` public-valid index. Produces selectable/paged event excerpts and persistent project UI state; no private filesystem reads in plugin.

- [ ] Write failing tests for the real runner argv/response validation and real DOM renderer. Use contract-valid fixtures and distinguish host/CLI mocks from real view logic.

```ts
expect(root.querySelector('[aria-label="扫描 Session"]')).not.toBeNull();
expect(root.textContent).toContain('部分');
expect(root.textContent).toContain('用户问题示例');
// Dispatch next-page click, await real runner result, then assert second page's literal excerpt.
expect(root.textContent).toContain('后续执行结果');
expect(root.textContent).not.toContain('用户问题示例');
```

- [ ] Run `npx vitest run tests/cli.test.ts tests/repository.test.ts tests/review-job-view.test.ts` plus new focused renderer test and record RED.
- [ ] Implement strict CLI allowlist for `inspect session-events`, validate input and parse page with existing contract parser; compare returned identity/digest/generation against the requested public entry. Fail with a readable retryable message on old CLI, invalid JSON, mismatch or stale generation; never silently return empty.
- [ ] Render `扫描记录` from validated index with coverage, searchable bounded Session list, previous/next/first/last event controls, and selected event details. Use textContent-only excerpts, preserve whitespace and label truncation/excerpt honestly. Surface absent agent responses as absence, not generated summaries. Render empty/CLI-unavailable/unavailable-source states. Preserve existing read-only notices and legacy view.
- [ ] Make selector visible on all nonempty project discovery states; use readable folder-based v4 names with stable IDs; persist selection immediately; reset event cache and ignore late responses when switching/project generation changes/closing. Add explicit refresh that rediscovers projects and current descriptor format while preserving a still-existing selected project.
- [ ] Tests must cover more than one event page and Session page, switching during pending request, reload persistence, refreshing a newly added project, malformed selected project escape via selector, missing CLI and partial coverage.
- [ ] Run `npm run check` and commit tests/implementation. Report exact build and test results and any integration needs.

## Local acceptance after both reviewed tasks

- [ ] Controller runs whole-branch review, `go test ./...`, `npm run check`, and builds a labelled local-repair CLI and plugin without publishing a release.
- [ ] Back up installed CLI/plugin/config, install verified artifacts and restart only SessionReviewer using BRAT. Never replace unrelated plugin state.
- [ ] Query actual current Session through CLI: verify 83 total, no missing/duplicate entries across pages, inspect exit status and matching generation. Confirm data hashes remain unchanged after read calls.
- [ ] Native Obsidian: project selector uses SessionReviewer name; Session list shows one partial Session; opening it shows actual first-page excerpts and right-hand detail; paging reaches last event; switch AgentWiki and back; reopen panel retains SessionReviewer. Report parser coverage limit separately.
