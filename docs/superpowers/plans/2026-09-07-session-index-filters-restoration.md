# Session Index Filters and Navigation Restoration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Restore accepted behavior, not a new information architecture.

**Goal:** Locate any accepted Session using local index filters and preserve the selected Session across tabs, refresh and project switches without requiring CLI.

**Architecture:** A pure local selection module filters the validated index without changing its canonical order or mutating it. A compact disclosure in the existing Session rail exposes filters; bounded per-project navigation state passes through the existing v4 state store. Summary/Q&A/event loaders retain their own authenticated identities and disposal.

**Tech Stack:** Existing TypeScript, Obsidian DOM, Vitest/jsdom; no new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§7.1–7.3,12.3,13.1/2/12,18.4. Prerequisite: summary UI task approved before touching shared host files.

## Global Constraints

- 主视图固定顺序：项目演进、问题脉络、决策与约定、全部 Sessions、用量。No second product-category tree.
- Preserve the complete validated index, including error/unprocessed and source-unavailable Sessions; never filter them out by default.
- Four processing states and source availability are orthogonal. Unknown dates remain unknown, never zero or a guessed date.
- Any list shows total, current range and not-shown count; bounded page size remains 25, never creates all Session DOM nodes.
- No CLI is needed for provider/date/processing/source-availability filters or provider/ID text search. Private branch/file/error search is a separate runtime task and must not be advertised as implemented here.
- No model calls, network, raw-source reads or persisted transcript/excerpt text. Persist only bounded UI filters, page and namespaced selection in existing plugin settings.
- No schema/dependency/version changes, migration, cross-platform expansion, real Vault installation, merge or release.

### Task 1: Restore local index filters, bounded navigation and per-project selection

**Files:**
- Create `obsidian-plugin/src/state/session-browser-state.ts`, `obsidian-plugin/tests/session-browser-state.test.ts`.
- Modify `obsidian-plugin/src/state/v4-view-state.ts`, `obsidian-plugin/tests/v4-view-state.test.ts`.
- Modify `obsidian-plugin/src/view/render-scan-records.ts`, `render-v4-shell.ts`, `obsidian-plugin/styles.css`.
- Modify `obsidian-plugin/tests/render-scan-records.test.ts`, `v4-shell.test.ts` and state-related host tests only as required; do not weaken existing behavioral assertions.
- Reuse summary UI and conversation components unchanged. Do not refactor their loading code into filters.

**Preflight clarification (2026-09-07):** A selected eligible Session must remain visible: filter changes reset to page0 when selecting a fallback, but if the retained eligible selection lies on another filtered page, locate that page instead. This takes precedence over the unconditional page0 wording below. Live invalid/backwards dates display a polite error and preserve the previous valid applied filter/selection; invalid persisted dates normalize to defaults. Reuse the v4 pane container introduced by d57b4e3 for responsive filter controls, preserving its no-clipping behavior.

**Interfaces:**

```ts
export interface SessionBrowserState {
  query: string;
  provider: string | null;
  processingState: 'complete' | 'partial' | 'error' | 'unprocessed' | null;
  sourceAvailability: 'available' | 'unavailable' | null;
  dateFrom: string | null;
  dateTo: string | null;
  unknownDateOnly: boolean;
  page: number;
  selected: { provider: string; sessionId: string } | null;
}
export function normalizeSessionBrowserState(value: unknown): SessionBrowserState;
export function filterSessions(
  sessions: SessionIndexEntryV1[], state: SessionBrowserState
): SessionIndexEntryV1[];
// Add optional sessionBrowser to V4ViewState so older in-memory callers remain valid.
// normalizeV4ViewState always returns its normalized state.
// ScanRecordsOptions adds initialState?: unknown and onStateChange?: (state: SessionBrowserState) => void.
```

- [ ] **RED pure behavior tests.** Hand-checked fixtures cover all four processing states, both availabilities, two providers sharing a native Session ID, exact inclusive date boundaries and null dates. Query matches only provider/ID, literal case-insensitive. Provider/date/state/availability combine with AND. An active date interval excludes null dates; unknown-date-only exclusively selects null started_at and is mutually exclusive with interval input. Date filtering uses the displayed local calendar date of started_at (not UTC substring); freeze test TZ to assert an offset-boundary Session. Reject malformed/nonexistent calendar dates, backwards interval, non-boolean toggles, huge page, query >256 UTF-8 bytes and selection ID >128 safe ASCII bytes to safe defaults. Ignore extra properties. Input arrays/entries stay byte-equivalent after filtering; retain canonical input order.

```ts
expect(filterSessions(fixtures, {...defaults, provider:'claude', sourceAvailability:'unavailable'})
  .map(s => [s.provider,s.session_id])).toEqual([['claude','same-id']]);
expect(normalizeSessionBrowserState({page:-1,query:'x'.repeat(257)}).page).toBe(0);
```

- [ ] **RED rendered navigation tests.** Build 154 Sessions including first/last, errors and unavailable entries. Initial first page renders at most25 row controls and shows `1–25 / 共154` (spacing localized) with129 not shown. First/previous/next/last reach the earliest Session. A filter reducing to0 displays `0–0 / 共0`, preserves global accepted count154 and shows no stale Session detail. Changing filters resets to page0 and selects first match only if the prior selected namespaced Session is no longer eligible. Page changes select a visible row; clicking row preserves page. Clear filters restores full accepted list. No CLI still supports all local controls and makes no loader calls; retained summary behavior remains as in prior task when CLI exists.

- [ ] **RED state/lifecycle integration tests.** Through actual v4 host, select a second provider's same-ID Session, move pages, filter, switch to another top tab then back: restore filters/page/selection. Refresh index after new generation retaining selected Session: select its containing page, request new digest only, never reuse stale cursor or source content. Removed/nonmatching selection falls back deterministically. Project A→B→A keeps independent state; malicious saved props never persist. Editing filters must not replace focused controls/lose caret. A saveState update in Sessions must not recreate records or re-fetch the same summary/event page on each keystroke; navigate away/refresh/close disposes once and rejects late responses.

- [ ] **Run RED.** `npm --prefix obsidian-plugin test -- session-browser-state.test.ts v4-view-state.test.ts render-scan-records.test.ts v4-shell.test.ts`; record expected missing-state/filter/pagination failures.

- [ ] **Implement pure state/filter logic.** Use existing safe identity grammar and UTF-8 byte helper patterns. Read date components via `Date` in the current local timezone; reject invalid date range inputs and render a polite bounded error instead of silently swapping endpoints. Unknown-date-only clears interval fields; setting date interval clears unknown-date-only. Pure normalization defaults invalid persisted fields and caps page at2621 (65,536/25 pages). Never interpret query as a path or regex.

```ts
const matches = sessions.filter(session =>
  (!state.provider || session.provider === state.provider) &&
  (!state.processingState || session.processing_state === state.processingState) &&
  (!state.sourceAvailability || session.source_availability === state.sourceAvailability)
  // then literal query and validated local-calendar date predicates
);
```

- [ ] **Implement compact rail controls.** Keep the existing search outside a native `筛选` disclosure; add labeled provider/status/availability selects, from/to date inputs, unknown-date checkbox and `清除筛选`. Options use human-readable processing/availability labels, providers come from exact index distinct values. Rows show time (unknown explicit), processing and source availability alongside provider/Session identity. Show full identity in detail and via row accessible label; don't hide relevant state behind hover. Header adds source distribution and known started_at range, unknown count; distinguish Session source range from index.generated_at scan time. Use first/last alongside previous/next; current filtered total/range and not-shown remain visible.

- [ ] **Wire persistence without remount.** Host passes normalized sessionBrowser state and saves changes using a state-only path (no shell draw). Preserve top-tab equality checks including nested Session state where relevant. Existing main.ts serialization already persists v4 state; if it requires code changes, report the precise gap before broadening files. On returning to Sessions, normalize against current index and locate selected identity's filtered page. Keep event page cursors/cache out of persisted settings. No overwriting a cross-leaf newer state with an old snapshot.

- [ ] **GREEN and QA.** Focused command, `npm --prefix obsidian-plugin run check`, `git diff --check`. With a temporary Playwright harness outside repo, mount actual v4 host using clearly labeled154-Session fixtures at1280 and390 widths; assert page identity, nonblank/no overlay/no console errors, last-page selection, filter→empty→clear, tab return, keyboard focus and no horizontal overflow. Save screenshots outside repo. Native Obsidian acceptance remains separate; do not install or call this product-complete.

- [ ] **Commit exact task files.** Message `feat(obsidian): restore full session filters and navigation`. Report RED/GREEN, state/lifecycle results, screenshot paths and explicit S07 private-search/provider/native remaining gates.
