# Obsidian All Sessions View Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Obsidian users a complete, filterable, virtualized Session list and bounded detail browser that remains honest when the CLI or source data is unavailable.

**Architecture:** The repository loads the two Markdown files, v4 ledger, and `session-index-v1` as one generation-bound snapshot. Index-only filters run locally over the complete compact list. Summary, event pages, and semantic searches call only the fixed read-only CLI methods. UI state stores identities and ordinals rather than entire event arrays, so long Sessions remain bounded.

**Tech Stack:** TypeScript 5.8, Obsidian 1.13, existing DOM render helpers, Vitest/jsdom, fixed-argv Node `execFile` wrapper.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md`

## Global Constraints

- Prerequisites: Gate 0 and `2026-09-04-session-index-publication-query.md` are complete.
- Tab order and labels are exactly `项目演进`, `决策与约定`, `全部 Sessions`, `用量`.
- The index list is complete. Virtualization changes DOM node count only; it never slices the data model or hides total/current-range counts.
- Without a verified CLI, date/provider/processing/source-availability filters and the full index still work. Only summary, deep events, and branch/file/error searches are disabled, with one recovery action.
- CLI calls use `execFile` with `shell:false`, absolute executable, fixed arrays, 10-second timeout, and bounded stdout/stderr. No user string becomes a path or executable.
- Stale cursor/generation responses refresh the snapshot and restore the closest valid ordinal in the same `(provider, session_id)`; never jump to another Session silently.
- All UI is keyboard accessible and respects Obsidian light/dark themes and reduced motion.

## File Structure and Ownership

- `obsidian-plugin/src/contracts/review-v4.ts`: browser model and query response types.
- `obsidian-plugin/src/data/repository.ts`: four-file snapshot loading, hash/generation validation, watchers.
- `obsidian-plugin/src/cli/runner.ts`: fixed inspect methods and strict response parsing.
- `obsidian-plugin/src/state/store.ts`: persisted filter/selection/page state, no event payload cache.
- `obsidian-plugin/src/view/render-shell.ts`: four-tab navigation.
- `obsidian-plugin/src/view/render-sessions.ts`: coverage, filters, list, detail, paging and recovery states.
- `obsidian-plugin/src/view/virtual-list.ts`: bounded DOM window over a complete array.

---

### Task 1: Load and watch the generation-bound Session index

**Files:**
- Modify: `obsidian-plugin/src/contracts/review-v4.ts`
- Modify: `obsidian-plugin/src/data/repository.ts`
- Create: `obsidian-plugin/tests/session-index-repository.test.ts`
- Modify: `obsidian-plugin/tests/repository.test.ts`, `discovery.test.ts`

**Interfaces:**

```ts
export interface BrowserSourceV4 extends BrowserSource {
  sessionIndexPath: string;
  sessionIndexSha256: string;
  ledgerSha256: string;
}
export interface BrowserModelV4 extends BrowserModel {
  generationId: string;
  sessionIndex: SessionIndexV1;
}
```

- [ ] **Step 1: Write RED repository tests** for a valid four-file snapshot, missing index, malformed index, mixed project/generation/ProjectView digest, tampered digest, v3 migration-required state, and last-valid snapshot fallback.

```ts
it("rejects a session index from another generation", async () => {
  const vault = fourFileVault({ ledgerGeneration: "g2", indexGeneration: "g1" });
  const snapshot = await new ProjectRepository(vault).load(project("project-p"));
  expect(snapshot.kind).toBe("empty");
  expect(snapshot.kind === "empty" && snapshot.diagnostic?.code).toBe("stale_snapshot");
});
```
- [ ] **Step 2: Run RED:** `cd obsidian-plugin && npm test -- session-index-repository.test.ts repository.test.ts`; expect missing index support.
- [ ] **Step 3: Load `.session-reviewer/session-index.json`** with the Gate-0 strict parser before constructing `BrowserModelV4`. Add it to the watcher set and validate exact project/generation/ProjectView bindings against ledger v4.

```ts
const sessionIndexPath = `${project.root}/.session-reviewer/session-index.json`;
const sessionIndex = parseSessionIndexV1(await this.vault.read(sessionIndexPath));
assertSnapshotBindings(machine, sessionIndex);
```
- [ ] **Step 4: Run GREEN:** `cd obsidian-plugin && npm run check`.
- [ ] **Step 5: Commit when authorized:** `git add obsidian-plugin/src/contracts obsidian-plugin/src/data obsidian-plugin/tests && git commit -m "feat: load complete session index in Obsidian"`.

---

### Task 2: Add fixed-argv inspect methods and bounded parsers

**Files:**
- Modify: `obsidian-plugin/src/cli/runner.ts`
- Modify: `obsidian-plugin/tests/cli.test.ts`

**Interfaces:**

```ts
sessionSummary(projectId: string, key: SessionIdentity, expectedGenerationId: string): Promise<SessionSummaryV1>;
sessionEvents(projectId: string, key: SessionIdentity, expectedGenerationId: string, page: { cursor?: string; anchor?: number; limit: number }): Promise<SessionEventPageV1>;
sessionSearch(projectId: string, expectedGenerationId: string, request: { kind: "branch"|"file"|"error"; query: string; cursor?: string; limit: number }): Promise<SessionSearchPageV1>;
```

- [ ] **Step 1: Add RED argv tests** that assert provider and Session ID are separate arguments, cursor/anchor are exclusive, limit is 1–100, query is at most 256 UTF-8 bytes, and no shell or path argument is accepted.

```ts
it("uses separate provider and session identity arguments", async () => {
  await runner.sessionSummary("project-p", { provider: "claude", sessionId: "same" }, "g1");
  expect(exec.args).toEqual(["inspect", "session-summary", "--project-id", "project-p", "--provider", "claude", "--session-id", "same", "--expected-generation-id", "g1", "--json"]);
  expect(exec.options.shell).toBe(false);
});
```
- [ ] **Step 2: Add RED response tests** for malformed JSON, duplicate identity, wrong generation/session, oversized stdout, timeout, `stale_cursor`, and `anchor_out_of_range`.
- [ ] **Step 3: Run RED:** `cd obsidian-plugin && npm test -- cli.test.ts`.
- [ ] **Step 4: Implement methods through the existing private `run`/`runJSON` path** and Gate-0 parsers; raise typed `CliContractError` carrying code and current summary when present.

```ts
async sessionSummary(projectId: string, key: SessionIdentity, generation: string): Promise<SessionSummaryV1> {
  validateIdentity(key);
  validateProject(projectId);
  return parseSessionSummaryV1(await this.runJSON(["inspect", "session-summary", "--project-id", projectId, "--provider", key.provider, "--session-id", key.sessionId, "--expected-generation-id", generation, "--json"]));
}
```
- [ ] **Step 5: Run `npm run check` and commit when authorized** with message `feat: add safe session inspect client`.

---

### Task 3: Model complete filters and persistent Session view state

**Files:**
- Create: `obsidian-plugin/src/state/session-filter.ts`, `session-filter.test.ts`
- Modify: `obsidian-plugin/src/state/store.ts`
- Modify: `obsidian-plugin/src/view/render-shell.ts`
- Modify: `obsidian-plugin/tests/store.test.ts`, `view.test.ts`

**Interfaces:**

```ts
export interface SessionFilter {
  providers: string[];
  processingStates: ProcessingState[];
  sourceAvailability: SourceAvailability[];
  startedFrom?: string;
  startedTo?: string;
}
export interface ViewState {
  view: "evolution"|"decisions"|"sessions"|"usage";
  selectedSession?: SessionIdentity;
  sessionFilter: SessionFilter;
  sessionOrdinal: number;
  sessionEventOrdinal: number;
}
export function filterSessions(index: SessionIndexV1, filter: SessionFilter): SessionIndexEntry[];
```

- [ ] **Step 1: Write RED tests** using 154 entries, duplicate native IDs across providers, null dates, and every processing/source state. Assert stable order and exact filtered totals.

```ts
it("keeps duplicate native IDs from different providers", () => {
  const rows = filterSessions(indexOf(entry("codex", "same"), entry("claude", "same")), emptyFilter());
  expect(rows.map((row) => `${row.provider}/${row.sessionId}`)).toEqual(["claude/same", "codex/same"]);
});
```
- [ ] **Step 2: Run RED:** `cd obsidian-plugin && npm test -- session-filter.test.ts store.test.ts view.test.ts`.
- [ ] **Step 3: Implement immutable filter normalization and persisted state migration.** Drop unavailable provider filters, retain selection only by full identity, and clamp ordinals only after the same filtered dataset is recomputed.

```ts
export function sameSession(left?: SessionIdentity, right?: SessionIdentity): boolean {
  return left !== undefined && right !== undefined && left.provider === right.provider && left.sessionId === right.sessionId;
}
```
- [ ] **Step 4: Change shell tabs to the exact four-item order** with ArrowLeft/Right/Home/End keyboard behavior and `sessions` panel dispatch.
- [ ] **Step 5: Run `npm run check` and commit when authorized** with message `feat: model complete session navigation state`.

---

### Task 4: Preserve readable Project Evolution progressive disclosure

**Files:**
- Modify: `obsidian-plugin/src/view/render-evolution.ts`
- Modify: `obsidian-plugin/src/view/render-shell.ts`
- Modify: `obsidian-plugin/tests/large-history.test.ts`, `view.test.ts`
- Modify: `internal/presentation/project.go`, `project_test.go`

- [ ] **Step 1: Write RED projection tests** proving deterministic machine evidence creates only neutral milestones (verification, commit, release, rollback, major error) and never invents reason, meaning, direction, or next action.

```go
func TestProjectDoesNotPromoteAtomicFactsOrInventMeaning(t *testing.T) {
    output := projectFromFacts(t, 40, withNoHumanSemantics())
    if len(output.Events) >= 40 { t.Fatalf("atomic facts leaked as milestones: %d", len(output.Events)) }
    for _, event := range output.Events { if event.Why != "" || event.Next != "" { t.Fatalf("invented semantics: %+v", event) } }
}
```

- [ ] **Step 2: Write RED UI tests** for recent mode showing milestone total plus omitted count, “查看全部”, complete search/virtual list mode, and distinct `机器验证`/`人工确认` source labels.

```ts
it("shows the milestone total when recent mode is compact", () => {
  const panel = renderEvolution(modelWithMilestones(73), { ...defaultViewState(), fullHistory: false }, noopUpdate);
  expect(panel.textContent).toContain("共 73 个里程碑");
  expect(panel.textContent).toContain("查看全部");
});
```

- [ ] **Step 3: Run RED:** `go test ./internal/presentation -run Milestone -count=1 && (cd obsidian-plugin && npm test -- large-history.test.ts view.test.ts)`.
- [ ] **Step 4: Implement typed milestone selection and explicit totals.** Remove atomic event-ID lists from human Markdown and browser cards; keep evidence identity behind the Session query surface.

```ts
const visibleMilestones = state.fullHistory ? filteredMilestones : filteredMilestones.slice(0, RECENT_MILESTONE_LIMIT);
heading.append(element("span", { text: `共 ${filteredMilestones.length} 个里程碑` }));
if (!state.fullHistory && visibleMilestones.length < filteredMilestones.length) heading.append(showAllButton(update));
```

- [ ] **Step 5: Run Go/plugin full gates and commit when authorized** with message `feat: keep project evolution complete and readable`.

---

### Task 5: Render coverage, full virtual list, and index-only fallback

**Files:**
- Create: `obsidian-plugin/src/view/render-sessions.ts`
- Modify: `obsidian-plugin/src/view/virtual-list.ts`
- Modify: `obsidian-plugin/src/styles.css`
- Create: `obsidian-plugin/tests/all-sessions-view.test.ts`
- Modify: `obsidian-plugin/tests/large-history.test.ts`, `styles.test.ts`, `accessibility.test.ts`

- [ ] **Step 1: Write RED DOM tests** for the coverage line `共 154 · 完整 140 · 部分 8 · 错误 4 · 未处理 2`, all local filters, null timestamps, warnings, provider badges, source unavailable, and zero-result messaging.

```ts
it("shows the complete coverage instead of the rendered window size", () => {
  const root = renderSessions(modelWith154Sessions(), defaultSessionViewState(), noopUpdate);
  expect(root.querySelector("[data-role=session-coverage]")?.textContent).toContain("共 154");
  expect(root.querySelectorAll("[data-session-row]").length).toBeLessThan(200);
});
```
- [ ] **Step 2: Add a 10,000-entry virtualization test.** Assert the model total remains 10,000, the rendered window stays below 200 rows, PageUp/PageDown/Home/End reach correct ordinals, and selection is full provider/session identity.
- [ ] **Step 3: Add no-CLI tests.** The 154 index rows remain navigable; deep controls are disabled; exactly one “配置 SessionReviewer CLI” recovery action is exposed.
- [ ] **Step 4: Run RED:** `cd obsidian-plugin && npm test -- all-sessions-view.test.ts large-history.test.ts accessibility.test.ts styles.test.ts`.
- [ ] **Step 5: Implement coverage cards, filters, virtual rows, focus management, ARIA list semantics, and theme-token CSS.** Never use `slice(-N)` to define the logical dataset.

```ts
const visible = virtualWindow(filteredSessions, state.sessionOrdinal, viewportRows, overscanRows);
list.setAttribute("aria-setsize", String(filteredSessions.length));
visible.forEach(({ item, ordinal }) => list.append(renderSessionRow(item, ordinal, filteredSessions.length)));
```
- [ ] **Step 6: Run `npm run check` and commit when authorized** with message `feat: render complete all sessions view`.

---

### Task 6: Render summaries and bounded event pages

**Files:**
- Modify: `obsidian-plugin/src/view/render-sessions.ts`
- Create: `obsidian-plugin/src/state/session-detail.ts`, `session-detail.test.ts`
- Create: `obsidian-plugin/tests/session-detail-view.test.ts`

**Interfaces:**

```ts
type SessionDetailState =
  | { kind: "idle" }
  | { kind: "loading"; key: SessionIdentity }
  | { kind: "ready"; summary: SessionSummaryV1; page?: SessionEventPageV1 }
  | { kind: "unavailable"; reason: string }
  | { kind: "stale"; requestedOrdinal: number };
```

- [ ] **Step 1: Write RED interaction tests** for opening a Session, summary section omitted counts, next/previous/first/last page, ordinal jump, 2,438-event range labels, loading cancellation when selection changes, and CLI unavailable.

```ts
it("shows a bounded middle page range", async () => {
  cli.events.resolve(eventPage({ total: 2438, rangeStart: 1001, rangeEnd: 1100 }));
  await openSessionAndJump(view, 1001);
  expect(view.textContent).toContain("当前 1,001–1,100 / 共 2,438 条");
});
```
- [ ] **Step 2: Add stale-cursor RED test.** Return `stale_cursor`, reload the repository, verify the same identity still exists, request the closest valid ordinal in the new generation, and announce the refresh; if identity vanished, return to the list without selecting a neighbor.
- [ ] **Step 3: Run RED:** `cd obsidian-plugin && npm test -- session-detail.test.ts session-detail-view.test.ts`.
- [ ] **Step 4: Implement one-request-at-a-time detail state** with request tokens/abort semantics, explicit range/total labels, coverage/omitted notices, and no retained full event history.

```ts
const requestId = ++this.latestRequest;
  const page = await this.cli.sessionEvents(this.projectId, key, generation, request);
if (requestId !== this.latestRequest || !sameSession(key, this.selected)) return;
this.state = { kind: "ready", summary: this.summary, page };
```
- [ ] **Step 5: Run `npm run check` and commit when authorized** with message `feat: browse bounded session details`.

---

### Task 7: Add branch, file, and error search without weakening local filters

**Files:**
- Modify: `obsidian-plugin/src/view/render-sessions.ts`
- Modify: `obsidian-plugin/src/state/session-filter.ts`
- Create: `obsidian-plugin/tests/session-search-view.test.ts`

- [ ] **Step 1: Write RED tests** for literal branch/file/error queries, 256-byte boundary, paged matches, generation refresh, no-CLI disabled state, and local filter intersection with server-returned identities.

```ts
it("intersects semantic hits with local provider filters", async () => {
  cli.search.resolve(searchPage(hit("codex", "s1"), hit("claude", "s2")));
  const rows = await submitSearch(viewWithProviderFilter("claude"), "file", "src/app.ts");
  expect(rows).toEqual(["claude/s2"]);
});
```
- [ ] **Step 2: Run RED:** `cd obsidian-plugin && npm test -- session-search-view.test.ts`.
- [ ] **Step 3: Implement debounced explicit-submit search** using only `CliRunner.sessionSearch`; never place query text in a path, HTML, selector, or executable argument position without text escaping.

```ts
const response = await cli.sessionSearch(model.review.projectId, model.generationId, { kind, query, limit: 100 });
const hitKeys = new Set(response.items.map((item) => `${item.provider}\u0000${item.sessionId}`));
const visible = locallyFiltered.filter((row) => hitKeys.has(`${row.provider}\u0000${row.sessionId}`));
```
- [ ] **Step 4: Show server match total/current range separately from local index filter total.** Clearing semantic search restores the complete locally filtered set without rescanning.
- [ ] **Step 5: Run `npm run check` and commit when authorized** with message `feat: search session facts from Obsidian`.

---

### Task 8: Installed-bundle Obsidian acceptance

**Files:**
- Modify: `obsidian-plugin/manifest.json`, `versions.json`, `package.json` only if a version bump is authorized
- Create: `docs/session-review/obsidian-all-sessions-acceptance.md`

- [ ] **Step 1: Build:** `cd obsidian-plugin && npm run check`; install the resulting `main.js`, `manifest.json`, and `styles.css` into a disposable real Vault.
- [ ] **Step 2: Open a project with at least 154 indexed Sessions.** Verify tab order, exact coverage totals, earliest and latest Session reachability, filters, keyboard navigation, and stable selection after reload.
- [ ] **Step 3: Open the 2,438-event Session.** Verify summary, first/middle/last page, current range/total, no UI freeze, and bounded DOM node count.
- [ ] **Step 4: Temporarily invalidate the configured CLI.** Verify full index/local filters remain, deep controls disable with one recovery action, then recover after restoring the CLI.
- [ ] **Step 5: Trigger a new generation while a detail page is open.** Verify stale recovery stays on the same identity/closest ordinal or returns safely to the list.
- [ ] **Step 6: Validate light/dark themes, 100%/150% zoom, and macOS/Windows Obsidian Desktop.** Record screenshots, plugin bundle hashes, Vault fixture generation ID, and results in the acceptance document.
