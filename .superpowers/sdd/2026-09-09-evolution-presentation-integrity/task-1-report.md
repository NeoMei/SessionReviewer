# Task 1 implementation report

## Outcome

Implemented the bounded v4 evolution presentation correction. A single copy-preserving chronology helper now orders RFC3339 timestamps by exact instant (including offsets and arbitrary fractional-second precision), gives unknown timestamps deterministic ID order, and explicitly selects the latest known instant. Both the rail and shell default use that helper while a valid explicit older selection remains stable.

The milestone detail now keeps all accepted title/summary/body text verbatim, presents closed local evidence labels, exposes the four closure-coverage counters, and surfaces partial/truncated/unavailable warnings without expansion. Snapshot-qualified refs display their exact `session_view_digest` inside expandable provenance. Missing answer/execution/verification copy remains distinct. Unknown milestone kinds are neutral. No actions, loaders, fetches, parser changes, publication changes, or model/Agent work were added.

This is only the bounded chronology/status/coverage correction. Original-Session destinations, answer-body controls, problem actions, optional candidates, broader machine-fact readability, original Task 6, product completion, native install, release, and deployment remain open.

## Root cause and TDD evidence

Root-cause inspection found three independent presentation defects in owned code:

- `render-v4-evolution.ts` sorted `occurred_at` with textual `localeCompare`, so equivalent instants with different offsets and sub-millisecond precision were displayed incorrectly.
- `render-v4-evolution.ts` and `render-v4-shell.ts` independently defaulted with `timeline.at(-1)`, making the selected detail depend on input order.
- Milestone detail exposed raw enums as its only status explanation and omitted aggregate closure coverage and snapshot-qualified view provenance.

RED command:

```text
cd obsidian-plugin
npm test -- tests/evolution-presentation.test.ts tests/v4-shell.test.ts
```

Observed baseline result: exit 1; 2 test files, 7 failed and 16 passed. Actual assertions reproduced `Earlier actual instant` instead of `Later actual instant`, raw textual rail order instead of actual-instant order, `Unknown date` displacing the latest known instant, absent evidence segment/status/coverage DOM, and shell first render selecting the wrong offset-spelled tail item. This RED exercised real renderer/shell behavior and did not depend on a missing helper export or controller artifacts.

GREEN command after implementation:

```text
npm test -- tests/evolution-presentation.test.ts tests/v4-shell.test.ts
```

Observed result: exit 0; 2 files and 25 tests passed.

The first full-suite run caught two existing provenance compatibility assertions because raw audit fields and accepted text had been removed from the expanded detail. I restored those as secondary `<details>` text while keeping the human labels primary. The next focused repository run caught the remaining raw missing-reason audit assertion; it was likewise retained as secondary provenance. This preserved old auditability without making raw enums the primary explanation.

## Verification

Fresh final plugin verification:

```text
cd obsidian-plugin
npm run check
```

Exit 0: ESLint clean; 28 test files and 495 tests passed; `tsc --noEmit --skipLibCheck` and production esbuild completed.

```text
git diff --check
```

Exit 0, no whitespace errors.

```text
node scripts/check-v4-pane-layout.mjs \
  --playwright-module /Users/neomei/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs \
  --browser-executable '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
```

Exit 0: `PASS v4 pane layout regression`; screenshots at `/var/folders/0n/49qgdd8x7kgcvh719fw743mh0000gn/T/session-reviewer-v4-pane-L1Xr50`. Inspected `narrow-580.png`: content stays inside the shell and the five-tab/narrow stacked layout remains readable. The unchanged harness also covers 1200/580/760/390 widths and console errors.

Controller independently reported both original instant/coverage DOM probes GREEN, then actual scan publication -> strict TypeScript parser -> real Chrome GREEN at 1200/390. Its checks covered nanosecond/offset defaulting, exact incomplete answer and warnings, exact snapshot view after keyboard expansion, older selection across full/recent mode and tabs, five-tab shell, complete history, no horizontal overflow, and no console errors. Controller inspected the 390 screenshot. This is controller-owned integration evidence, not a native Vault/install/release claim.

No full Go suite was run because this is TypeScript/CSS-only and the controller's actual Go-published wire/browser seam passed.

## Files

- `obsidian-plugin/src/view/v4-milestone-order.ts` (new)
- `obsidian-plugin/src/view/render-v4-evolution.ts`
- `obsidian-plugin/src/view/render-v4-shell.ts`
- `obsidian-plugin/tests/evolution-presentation.test.ts` (new)
- `obsidian-plugin/tests/v4-shell.test.ts`
- `obsidian-plugin/styles.css`
- `.superpowers/sdd/2026-09-09-evolution-presentation-integrity/task-1-report.md` (this report)

The controller-owned concurrent `docs/verification/2026-09-08-release-execution.md` modification was neither edited nor staged.

## Self-review and concerns

- Confirmed helper returns copies and never rewrites source timestamps/arrays.
- Replaced locale-sensitive ID/fraction comparison with deterministic code-unit comparison during self-review.
- Valid explicit selection wins over default chronology; initial null/invalid selection and leave/re-enter both use the same helper.
- Unknown dates remain visible verbatim, sort deterministically, and cannot displace a known latest instant.
- Labels describe declared evidence only; machine evidence does not imply human resolution or project completion.
- All content and provenance use text nodes; no `innerHTML`, initial fetch, tab-switch fetch, fake control, migration, or contract expansion.
- Existing five segments and five tabs remain in the same order.
- Raw status/missing identifiers remain only as secondary expanded provenance for compatibility; the compact visible label is human-readable.
