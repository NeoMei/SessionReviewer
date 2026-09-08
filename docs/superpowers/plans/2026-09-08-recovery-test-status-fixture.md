# Event Recovery Test Status Fixture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute after retained-query fix is committed and frozen; test-only and independently reviewed.

**Goal:** Event-recovery tests provide a valid steady CLI status and do not accidentally start settling retries that outlive their one-shot snapshot mocks.

**Architecture:** Reuse existing `tests/fixtures/sync-status.ts`. Correct only the recovery test's invalid status stubs; verify the real host does not perform an unintended settling refresh with fake time. No production retry/error behavior changes.

**Tech Stack:** Existing Vitest/jsdom, real ProjectEvolutionView, existing syncStatusFixture.

**Spec:** `docs/verification/2026-09-08-release-execution.md` repeated clean frontend/UI gates. This repairs test fidelity, not source or project semantics.

## Global Constraints

- Modify only `obsidian-plugin/tests/session-event-recovery.test.ts`; no production code, dependencies, timeout constants, skips, unhandled-error suppression or retry-to-pass.
- Existing recovery/selection/stale-page assertions remain intact; clean up mounted views/timers even if the new regression fails.
- No CLI process, Agent, network, real source/Vault, main, remote or release changes.

### Task 1: Supply valid status and prove no accidental refresh

**Files:** Modify `obsidian-plugin/tests/session-event-recovery.test.ts` only.

**Interfaces:** Existing `syncStatusFixture(projectId, overrides?)` from `./fixtures/sync-status`; existing snapshot/project helpers and ProjectEvolutionView onOpen/onClose. Keep production interfaces unchanged.

- [ ] Add a single local helper `readyStatus()` initially returning `{}` and use it at all12 `status: vi.fn().mockResolvedValue({})` sites in this test file. Add a deterministic host regression using `vi.useFakeTimers()` and a repository with stable valid snapshot return. `try/finally` always awaits view.onClose and restores real timers.

```ts
vi.useFakeTimers();
const repository = { discover: vi.fn().mockResolvedValue([project]), load: vi.fn().mockResolvedValue(snapshot("generation-1",100,DIGEST_1)), watch: vi.fn().mockReturnValue(vi.fn()) };
const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, {status: vi.fn().mockResolvedValue(readyStatus())} as unknown as CliRunner);
Object.assign(view,{app:{workspace:{openLinkText:vi.fn()}}});
try {
  await view.onOpen();
  await vi.advanceTimersByTimeAsync(1000);
  expect(repository.load).toHaveBeenCalledTimes(1);
  expect(view.contentEl.textContent).toContain("项目");
} finally {
  await view.onClose();
  vi.useRealTimers();
}
```

- [ ] RED run only the new named test; invalid readyStatus should cause extra repository loads through real settling retry, not a compile error or intentionally undefined snapshot. Controller independent actual-host probe already proves status{} -> load2/undefined.kind error and fullvalidstatus -> load1/noerrors, under `/tmp/session-reviewer-scan-chain-qa.6VBnCG/controller-refresh-fixture.ts/mjs`.
- [ ] Correct `readyStatus()` to `return syncStatusFixture(project.projectId);` with import. Reuse full existing contract fixture; do not hand-copy partial arrays. Run new test GREEN then all session-event-recovery tests. Existing tests should remain on their intended event-recovery path; don't modify their expected call counts to hide unintended settling.
- [ ] Ensure the new test asserts meaningful actual host content using a stable available heading/status selector if the generic text above is insufficient; no source-text assertion or fake component assertion.
- [ ] Run `npm run check` once after final edits: lint, entire test suite with zero unhandled exceptions, typecheck/build. Do not reclassify 472 passing assertions plus unhandled exception as clean.
- [ ] `git diff --check`; report RED/GREEN exact commands/output, all12stubs corrected, cleanup/timer evidence. Commit only test file, ignored report at this plan workspace/task-1-report.md; obtain independent scoped spec/quality review.

## Preflight

| Boundary | Check |
|---|---|
| readyStatus / real readCliStatus | Full fixture has open_conflicts, hidden_conflict_ids, pending_operations; no accidental catch-to-sync_status_failed |
| real host / fake time | Stable snapshot avoids undefined mock artifact; extra loads detect actual unwanted settling |
| regression / existing tests | Same helper used by every corrected stub; new regression goes RED if helper returns invalid empty status |
| timer cleanup / assertion failures | finally closes view before restoring timers; no leaked test job |

No production error-handling change is justified by undefined typed test returns. User-visible production refresh rejection handling remains subject to broader final review, not silently declared verified by this fixture fix.
