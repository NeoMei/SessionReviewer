# Task 1 report: positive CLI status fixture reliability

## Implemented

- Added a local `readyStatus(projectId)` helper in `obsidian-plugin/tests/render-scan-records.test.ts` that delegates to the authoritative `syncStatusFixture(projectId)` fixture.
- Replaced exactly 2 existing positive `status: vi.fn().mockResolvedValue({})` stubs with `readyStatus(project.projectId)`. Deliberately failing status mocks were unchanged.
- Added `does not reload a steady valid CLI status`, which uses a stable `projectSnapshot` response and fake timers, confirms the real host renders all 5 tabs, advances 4000 ms, and asserts that the repository is loaded exactly once.
- The regression always closes the view and restores real timers in `finally`, including on assertion failure.

## TDD evidence

### RED

Command:

```text
npm test -- tests/render-scan-records.test.ts -t 'steady valid CLI status'
```

Before `readyStatus` used the shared fixture, it deliberately returned `{}`. The test reached its load-count assertion and failed as intended:

```text
Tests  1 failed | 51 skipped (52)
AssertionError: expected "vi.fn()" to be called 1 times, but got 4 times
```

The stable `mockResolvedValue(projectSnapshot(...))` prevented an undefined snapshot. RED had no compile error or unhandled exception; the failure was the expected extra repository loads.

### GREEN

Focused command:

```text
npm test -- tests/render-scan-records.test.ts -t 'steady valid CLI status'
```

Result: 1 passed, 51 skipped, exit 0.

Full owned test-file command:

```text
npm test -- tests/render-scan-records.test.ts
```

Result: 52 passed, exit 0.

## Final verification

Command (run once after final test code):

```text
npm run check
```

Result: exit 0. ESLint passed; Vitest passed 30 files and 532 tests; TypeScript no-emit checking and the production esbuild completed. The output contained no unhandled exception.

`git diff --check` passed after the report was written.

## Files changed

- `obsidian-plugin/tests/render-scan-records.test.ts`
- `.superpowers/sdd/2026-09-09-scan-records-status-fixture/task-1-report.md`

## Self-review

- Re-read the task brief and inspected the final diff for exact scope.
- The regression fails if the steady valid status path schedules reloads, and it exercises the actual `ProjectEvolutionView` host rather than a mocked rendering path.
- No production code, dependencies, timeouts, skips, error suppression, Go sources, unrelated dirty documentation, network resources, or real Vault data were changed.
- No issues or concerns found.
