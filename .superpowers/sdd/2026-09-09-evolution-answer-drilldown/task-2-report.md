# Task 2 report: on-demand exact milestone answer

## Status

DONE. The v4 milestone conclusion now offers a bounded, authenticated, ephemeral answer-body expansion backed only by Task 1's accepted fixed-turn viewer.

## Implementation

- Added `renderV4Answer(presentation, milestone, index, load)` with the required disposable element contract.
- Built choices only from conclusion `source_turn_refs`, resolved through exactly one provider/Session/turn/view chain dependency. Canonical qualified/unqualified aliases collapse; different snapshots remain separate and retain original reference order.
- Required exact project, generation, ProjectView, and unique Session-index binding. A nullable/unavailable current Session view does not block an exactly retained historical dependency.
- Constructed the exact request only on explicit activation, setting both view fields to the resolved dependency view. No fallback, automatic retry, eager paging, or persisted body state was added.
- Reused `renderConversation(..., { turnUnitId })`; collapse, source changes, milestone changes, tab exit, shell replacement, and disposal invalidate/dispose the private viewer. Rail-only full/recent/page redraws preserve it because the selected milestone is unchanged.
- Preserved accepted human conclusion text above the expansion and added no workflow-resolution inference or placeholder source/problem action.
- Added bounded responsive styles for source selection, unavailable reasons, and embedded conversation content.

## TDD evidence

RED command:

`cd obsidian-plugin && npm test -- --run tests/v4-shell.test.ts`

Expected failure: 1/17 failed because the actual `renderV4Shell` returned no `[data-action="expand-milestone-answer"]`; keyboard activation crashed at the absent element. A second RED with `tests/evolution-answer.test.ts` also failed import resolution because `render-v4-answer.ts` did not exist.

GREEN commands and results:

- `npm test -- --run tests/evolution-answer.test.ts tests/v4-shell.test.ts` — 31/31 pass.
- `npm test -- --run tests/evolution-answer.test.ts tests/v4-shell.test.ts tests/render-conversation-turn.test.ts` — 52/52 pass.
- `npm run build` — strict TypeScript and production bundle pass.
- `npm run lint` — pass, pristine.
- `npm run check` — 30 files, 531/531 tests, lint and build pass.
- `git diff --check` — pass.

Controller-owned actual evidence received before commit: fresh actual CLI scan and historical rescan bytes passed strict TypeScript parsing and Chrome replay at 1200/390 for current full body and historical retained excerpt, keyboard expansion/collapse, delayed tab disposal, rail-only connected-detail preservation with no extra read, zero initial/no-CLI loads, and zero console/overflow errors. Screenshots were inspected. I reran the adjusted harness against the final source and it exited 0. Message paging was separately proven by the accepted Task 1 viewer tests/evidence; it is not claimed as part of this host replay. This is controlled real-CLI transport evidence, not native provider navigation.

## Coverage

Tests cover available full body, retained historical excerpt, null current view, zero Agent messages, multiple source turns, canonical aliases versus distinct snapshots, absent reader/index, wrong project/generation/ProjectView, missing/ambiguous Session row, source/body truncation, explicit same-request retry, stale generation copy, literal HTML-as-text, raw tool fields omitted, zero unrelated-tab/no-CLI calls, keyboard activation, and all owned lifecycle boundaries.

## Owned files

- `obsidian-plugin/src/view/render-v4-answer.ts`
- `obsidian-plugin/src/view/render-v4-evolution.ts`
- `obsidian-plugin/src/view/render-v4-shell.ts`
- `obsidian-plugin/styles.css`
- `obsidian-plugin/tests/evolution-answer.test.ts`
- `obsidian-plugin/tests/v4-shell.test.ts`
- `.superpowers/sdd/2026-09-09-evolution-answer-drilldown/task-2-report.md`

## Self-review and concerns

- Confirmed no legacy renderer, projection text, parser, CLI, native navigation, dependency, or persisted state was changed.
- Confirmed answer source resolution is indexed once per rendered milestone and requires an exact unique dependency.
- Confirmed fixed-turn viewer owns page validation, raw field suppression, failure copy, retry, paging, and late-response epochs; this host does not duplicate those mechanisms.
- The first full-check attempt failed in `tests/package.test.ts` while its nested package build ran, plus an unhandled late rejection in `tests/render-scan-records.test.ts` (`ProjectEvolutionView.refresh` received an undefined mocked snapshot). It occurred while controller-owned scratch/ledger activity and this implementation were both in flight, did not identify a Task 2 assertion failure, and was not dismissed as proof. After strict TypeScript fixes, a fresh complete `npm run check` passed all 531 tests, lint, package test, and production build; focused Task 2 suites also stayed green.
- Remaining gates are explicitly outside this task: original-Session destination, related-problem action, optional candidates, native combined acceptance, independent review, merge, install, and release.
