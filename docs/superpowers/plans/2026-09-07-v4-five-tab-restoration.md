# V4 Five-tab Restoration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement task-by-task. This is the first subsystem repair, not the entire product delivery.

**Goal:** Replace the v4 standalone diagnostic page with the approved five-tab navigation and compact project recovery header while preserving authenticated read boundaries.

**Architecture:** Keep the v4 repository parser and snapshot trust states. Render its validated presentation through a new v4 shell, not the legacy v3 data contract. Existing scan and conversation components mount only in 全部 Sessions. Panels are independent and state is identity-only; disposal invalidates in-flight reads. Later subsystem tasks replace/extend data-backed panel implementations without changing this shell.

**Tech Stack:** Existing TypeScript/DOM/Obsidian/Vitest; no new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§3,4,5,6,7,8,12.3; full product gate: `docs/verification/2026-09-07-spec-restoration.md`.

## Global Constraints

- Tab order and labels are exactly `项目演进`, `问题脉络`, `决策与约定`, `全部 Sessions`, `用量`.
- The left rail uses real question sentences only; top-level content categories remain exclusively in the top tab bar.
- Ordinary scans and deterministic projection start zero Agent processes.
- Only public-validated or valid draft presentation is rendered. Public hashes do not authorize structural writes. Invalid data must fail closed; stale valid data is explicitly stale.
- Existing human edits win over generated fields. No real user document/config edits in this implementation task.
- Full product spec remains open even after this shell passes. No inert action buttons or fake data. Empty data has a truthful, useful state.
- Preserve legacy v3 behavior; no migrations, schema changes, new cross-platform scope, version bump, release or real-vault installation in this task.

### Task 1: Restore the v4 navigation host and persistent per-project view selection

**Files:**
- Create: `obsidian-plugin/src/view/render-v4-shell.ts`, `obsidian-plugin/src/state/v4-view-state.ts`, `obsidian-plugin/tests/v4-shell.test.ts`, `obsidian-plugin/tests/v4-view-state.test.ts`.
- Modify: `obsidian-plugin/src/view/presentation.ts`, `project-view.ts`, `obsidian-plugin/src/main.ts`, `obsidian-plugin/styles.css`, `obsidian-plugin/tests/render-scan-records.test.ts`, `view.test.ts` and fixture helpers as required.
- Create separated basic data-backed renderers `render-v4-evolution.ts`, `render-v4-problems.ts`, `render-v4-decisions.ts`, `render-v4-usage.ts` if needed to avoid an overgrown shell. These display actual validated data, not pretend full workflows.

**Interfaces:**

```ts
export type V4Tab = 'evolution'|'problems'|'decisions'|'sessions'|'usage';
export interface V4ViewState {
  projectId: string;
  view: V4Tab;
  selectedMilestoneId: string|null;
  selectedProblemId: string|null;
}
export function normalizeV4ViewState(value: unknown, projectId: string): V4ViewState;
```

Keep `renderMarkdownV4View(snapshot, open, options)` as compatibility entry; delegate rendering to the new shell. Extend options with optional initial state/save callback using the above contract. Existing `scanRecords` lifecycle remains accessible via the returned element and must correctly dispose on tab change, snapshot refresh and view close. Add a dedicated v4 persistence key in plugin settings; do not coerce legacy v3 ViewState into v4.

- [ ] **RED navigation test:** construct a complete validated v4 fixture (reuse real contract fixture; do not use `as never` partial presentation), render via actual `ProjectEvolutionView` and click every tab. Verify sessions list is reached only on 全部 Sessions; selecting 用量 shows actual fixture model totals; selecting 决策与约定 shows actual decision title; project switch and reload restore each project's selected tab without sharing selection IDs.

```ts
expect([...root.querySelectorAll('[role="tab"]')].map(x => x.textContent))
  .toEqual(['项目演进','问题脉络','决策与约定','全部 Sessions','用量']);
root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
expect(root.querySelector('[aria-label="扫描 Session"]')).not.toBeNull();
root.querySelector<HTMLButtonElement>('[data-v4-tab="evolution"]')!.click();
expect(root.querySelector('[aria-label="扫描 Session"]')).toBeNull();
```

- [ ] **RED behavior tests:** default 项目演进; ArrowLeft/Right/Home/End roving tab focus and selected panel; no model invocation or scan triggered by tabs; pending/stale/invalid snapshots preserve trust boundaries; late conversation responses after tab/project switch cannot update detached/other panel. Legacy three-tab view unchanged.
- [ ] **Run RED:** `npm --prefix obsidian-plugin test -- v4-shell.test.ts v4-view-state.test.ts render-scan-records.test.ts view.test.ts` and record expected missing-shell failure.
- [ ] **Implement host and compact header:** project name, goal/stage/status/next action in compact grid. Empty field displays 未填写 with working native Markdown open action, not four blank paragraphs. Render actual risks/open loops and top three active decisions (pinned first then time/ID). Put provenance/read-only detail behind a details disclosure; keep an accurate short status visible. A sync status success must not be promoted to private snapshot acceptance.
- [ ] **Implement data-backed tab mounting:** evolution displays accepted timeline with total/selectable entries and all five existing closed-loop fields; missing segments retain explicit missing state. Problems displays formal parent/child hierarchy from `problem_nodes` and truthful empty state (no generated parents). Decisions renders accepted records with active default and archived/superseded filter. Usage must read actual ledger accounting/pricing snapshots, threading the validated ledger through repository if necessary, never derive fake token counts from Session count. Until full backend operations arrive, native Markdown navigation is a real read/edit route, not an inert button; outstanding structure/candidate/pricing workflows remain in global ledger.
- [ ] **Use approved layout relationships:** top tabs, compact header, bounded content pane, calm theme-token surfaces, restrained purple active indication; no global CSS, giant blank first screen or diagnostic wall. Reference main checkout `.superpowers/brainstorm/73848-1788534572/content/problem-context-layout-v3.html` for geometry, not sample content or numerical confidence. At narrower widths stack detail without changing tree order; support light/dark/reduced motion.
- [ ] **Preserve readable hierarchy and bounded lists:** for populated problem data retain left tree, center ancestor path/current question/direct children/related nodes, and right evidence references, not a new two-column layout. For populated evolution data show a recent compact rail with total and working in-panel full-history access; the full dataset stays reachable through bounded paging/windowing rather than rendering every item or delegating “all history” solely to raw Markdown. Use the approved `evolution-detail-closed-loop.html` geometry and five segment order. Add actual behavior tests for deep-tree selection and reaching first/last milestones beyond the recent window.
- [ ] **GREEN:** focused tests then `npm --prefix obsidian-plugin run check` once. Existing scan tests must explicitly select 全部 Sessions when testing host; do not weaken assertions on totals/coverage, project switching, authenticated responses or stale writes.
- [ ] **Commit** code and tests only; write report with RED/GREEN outputs, exact implemented/remaining behavior and concerns. Do not call the entire product complete.

## Self-review / cross-task integration

This plan owns navigation/persistence/compact presentation only. The acceptance ledger keeps complete closure projection, candidate actions, full Sessions search, decision lifecycle and pricing service blocked until their own tasks and actual end-to-end proof. Reuse existing schema and readonly query contracts; structural commands never bypass CLI/CAS to make buttons appear functional.
