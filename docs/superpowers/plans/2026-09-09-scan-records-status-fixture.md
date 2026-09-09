# Scan Records Host Status Fixture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Test-only correction after the active implementation/review closes; never overlap implementers.

**Goal:** Make scan-record host tests represent a valid steady CLI status and prove they do not accidentally start settling retries.

**Architecture:** Reuse existing syncStatusFixture in a local helper used by all empty positive-status stubs in render-scan-records.test.ts. Exercise the real host with controlled time and a stable snapshot, retain real negative-status tests and production retry logic.

**Tech Stack:** Existing Vitest/jsdom and ProjectEvolutionView. No dependencies.

**Spec:** docs/verification/2026-09-08-release-execution.md repeated clean test gates. This is the same already-reviewed test-fidelity contract as 2026-09-08-recovery-test-status-fixture.md, applied to the remaining scan-record host fixture, not a repeated implementation of that completed task.

## Global Constraints

- Modify only obsidian-plugin/tests/render-scan-records.test.ts and this plan's task report. No production retry/error/timeout changes, skips, global error suppression or retry-until-pass.
- Keep existing cache, selection, recovery, generation and noCLI assertions intact.
- No Agent, network, real source/Vault, merge, push or release.

### Task 1: Correct positive CLI fixtures and prove steady host behavior

**Files:** Modify obsidian-plugin/tests/render-scan-records.test.ts. Report .superpowers/sdd/2026-09-09-scan-records-status-fixture/task-1-report.md.

**Interfaces:** Use syncStatusFixture(projectId, overrides?) from ./fixtures/sync-status, existing projectSnapshot and ProjectEvolutionView. No public interface changes.

- [ ] Introduce local readyStatus(projectId: string), initially returning {}, and route every existing positive status mockResolvedValue({}) in this file through it with the actual project ID. Do not change deliberately failing status mocks.
- [ ] Add deterministic regression with a stable valid projectSnapshot return (never undefined). Before fixing helper it must fail on extra repository loads, not fixture shape or unhandled errors:

```ts
vi.useFakeTimers();
const project = {projectId: "project-p", root: "Projects/SessionReviewer", name: "SessionReviewer", format: "markdown-v4" as const};
const repository = {
  discover: vi.fn().mockResolvedValue([project]),
  load: vi.fn().mockResolvedValue(projectSnapshot(project.projectId, project.name)),
  watch: vi.fn().mockReturnValue(vi.fn())
};
const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, {status: vi.fn().mockResolvedValue(readyStatus(project.projectId))} as unknown as CliRunner);
Object.assign(view, {app: {workspace: {openLinkText: vi.fn()}}});
try {
  await view.onOpen();
  expect(view.contentEl.querySelectorAll('[role="tab"]')).toHaveLength(5);
  await vi.advanceTimersByTimeAsync(4000);
  expect(repository.load).toHaveBeenCalledTimes(1);
} finally {
  await view.onClose();
  vi.useRealTimers();
}
```

- [ ] Run only the new test RED: npm test -- tests/render-scan-records.test.ts -t 'steady valid CLI status'. Record actual extra-load assertion. Existing controller external actual-host probe status-fixture-controller.mjs proves malformed {} starts retry and exhausts a finite mock, while complete syncStatusFixture starts none. Owned test must not import external files.
- [ ] Change helper to return syncStatusFixture(projectId) and add the fixture import. Run new test GREEN and full render-scan-records.test.ts; preserve all previous assertions.
- [ ] Run npm run check once after final code: all tests, lint, TypeScript/build with no unhandled exceptions. Run git diff --check. Report exact replaced stub count, RED/GREEN and teardown evidence. Commit only owned test/report, then independent spec/quality review.

## Preflight

| Shared surface | Producer / consumer | Finding |
|---|---|---|
| Positive readyStatus helper / actual readCliStatus | full arrays and counts / no accidental catch-to-sync_status_failed | reuse existing fixture, not handwritten partial status |
| Stable snapshot / controlled timers | repeat-safe source / bounded settling detection | extra loads are the RED, not undefined.kind |
| New regression / existing cache tests | same helper / unchanged generation and selected Session expectations | helper is exercised by old tests too |
| Failure cleanup / test runtime | finally onClose / restore real timers | no test should leak a view or timer |

Controller reproduction2026-09-09: exact current CLI-exported presentation/ledger/index, actual ProjectEvolutionView and manual timers: valid syncStatusFixture yields load1/timer0; status{} yields timer1 then load2 and undefined.kind from deliberately finite mock. The undefined result is test-created; no production repository failure was proven. Frozen full plugin531 passed, so this is a deterministic reliability defect rather than a current all-suite failure.
