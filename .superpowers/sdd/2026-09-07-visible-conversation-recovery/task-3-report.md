# Task 3 implementation report

Status: plugin implementation complete. Focused tests and the final plugin check/build pass. Controller still owns independent whole-branch review, native installation, selected real Session rescan/sync, and Obsidian acceptance. This task did not read or write real source logs, private store, Vault files, installed plugin/CLI, release state, `main`, or the controller verification document.

## Implemented scope

- Added exact `ConversationPageV1`, `VisibleTurnV1`, `VisibleMessageV1`, and coverage types plus a strict page parser. It rejects duplicate keys, invalid Unicode, unknown/missing fields, invalid enums/timestamps/digests/source refs, cross-Session records, mismatched ranges/modes, impossible answer counts, noncanonical turn order, selected-message inconsistencies, and dishonest coverage.
- Added `CliRunner.getConversation` with fixed `execFile` argv only. Turn index uses optional `--cursor`; selected turns use `--turn-unit-id` plus optional `--message-cursor`. Request validation rejects mixed cursor modes. The runner checks project/provider/Session/generation/view/mode/turn response bindings and maps `source_unavailable`, `generation_mismatch`, `stale_cursor`, and `unsupported_provider` to bounded Chinese messages without raw stderr, paths, or backend details.
- Added a separately disposable `renderConversation` child inside the selected Session detail. It loads turn index and selected-message pages, defaults to the first visible question, labels `用户`, `Agent · 过程说明`, `Agent · 最终回答`, and legacy/unclassified `Agent · 回答`, renders full selected `text` with text nodes, and visibly distinguishes preview clipping from body truncation.
- Added first/previous/next/last controls for both turn and message pages using returned cursors. Loading, error, same-page retry, no-answer, partial-only, empty-result, and coverage warnings are explicit.
- Kept `问答记录` separate from `已索引执行事实` in the existing selected Session area, without another top-level navigation category. A zero-fact Session with an authenticated Session view can still load Q/A. Missing/old CLI leaves existing facts and legacy views readable.
- Kept the Session search input mounted through event/conversation redraws. Conversation identity binds project, provider, Session, generation, and Session-view digest; identity changes and disposal advance epochs so stale responses cannot update the view. Event clicks and search input do not recreate or rerequest the same conversation child.
- Coverage wording says oversized records are omitted source records with unknown roles and explicitly does not call them missing Agent answers. Assistant text is labeled as conversation content, never completion or verification evidence.

## TDD RED/GREEN evidence

### Strict wire and CLI

- RED: `npm test -- --run tests/conversation-page.test.ts tests/render-conversation.test.ts` failed because the new parser and renderer modules did not exist.
- GREEN: `npm test -- --run tests/conversation-page.test.ts` passed 14 tests after the first strict parser/runner implementation.
- Additional RED exposed missing allowlisting for selected-turn calls without a message cursor; GREEN after accepting only the exact 15-argument fixed form.
- Additional RED covered duplicate keys/invalid Unicode through the shared strict JSON boundary, noncanonical turn ordinals, mismatched selected-message totals, and malformed timestamps; GREEN after shared-boundary and reconciliation checks.

### Conversation view and selected Session integration

- RED: initial renderer tests failed until the independent conversation component existed; subsequent behavioral failures exposed an incorrect descendant assertion and a test double that manually requested the already-default selected turn. The assertions were corrected to exercise the real default-loading behavior.
- GREEN: `tests/render-conversation.test.ts` contained 3 renderer tests and passed them for labels, safe full bodies, phases, truncation, honest coverage, retry, both paging modes, identity replacement, and disposal. The earlier displayed count of 17 included 14 parser/runner tests registered a second time through an accidental import from another `*.test.ts` module.
- RED: zero-fact and persistent-child assertions failed before integration into `render-scan-records`.
- GREEN: `tests/render-scan-records.test.ts` passed 12 tests after Q/A/facts separation, zero-fact loading, search focus retention, and no-rerequest behavior.
- RED: the new conversation layout test failed before styles existed.
- GREEN: the focused combined command reported 4 files / 59 test registrations. Of those, 40 were distinct tests; 19 parser/runner tests were registered again because the renderer spec imported their `*.test.ts` module. The shared builders now live in `tests/fixtures/conversation.ts`, so Vitest no longer double-registers that suite.

## Final verification

- First `npm run check`: lint reached the test phase, then package reproducibility failed because its nested build exposed a TypeScript `unknown` narrowing error hidden by Vitest's piped subprocess output. Directly reproducing `scripts/build-obsidian-plugin.sh` showed exact `src/data/conversation-page.ts` diagnostics.
- Focused package rerun after the single narrowing fix: `npm test -- --run tests/package.test.ts` passed 2/2.
- Final `npm run check`: PASS, exit 0.
  - ESLint: PASS, no diagnostics.
  - Vitest: 22 test files passed and reported 327 registrations; 308 were distinct tests and 19 were the parser/runner suite registered a second time by the renderer-spec import corrected below.
  - Build: `tsc --noEmit --skipLibCheck && node esbuild.config.mjs production` PASS.
- `git diff --check`: PASS.

## Files

- `obsidian-plugin/src/contracts/conversation-page.ts`
- `obsidian-plugin/src/data/conversation-page.ts`
- `obsidian-plugin/src/data/contracts-v4.ts`
- `obsidian-plugin/src/cli/runner.ts`
- `obsidian-plugin/src/view/render-conversation.ts`
- `obsidian-plugin/src/view/render-scan-records.ts`
- `obsidian-plugin/src/view/presentation.ts`
- `obsidian-plugin/src/view/project-view.ts`
- `obsidian-plugin/styles.css`
- `obsidian-plugin/tests/conversation-page.test.ts`
- `obsidian-plugin/tests/fixtures/conversation.ts`
- `obsidian-plugin/tests/render-conversation.test.ts`
- `obsidian-plugin/tests/render-scan-records.test.ts`
- `obsidian-plugin/tests/styles.test.ts`
- `.superpowers/sdd/2026-09-07-visible-conversation-recovery/task-3-report.md`

## Concerns and remaining gates

- No native Obsidian UI, real first/middle/last page, real 73-user/1,372-assistant body, project switch, installed artifact, or CLI/Vault consistency claim is made here. Those are controller acceptance gates after independent review.
- Conversation bodies are intentionally not cached beyond the mounted selected Session child. Selecting the same turn again rereads that turn; event facts retain their existing project/generation cache.
- The backend reports incomplete conservative coverage when oversized source records have unknown roles. The UI preserves that boundary and does not infer that those records are missing replies.

## Review fix round 1

Three Important findings and the duplicate-test-registration minor finding were corrected after review:

1. `CliRunner.getConversation` now binds the returned range to the requested limit, requires uncursored index and selected-message calls to start at zero, rejects empty pages for nonempty results, and reconciles first/previous/next/last cursor presence with the returned range. It still accepts deterministic byte-budget pages smaller than the requested limit.
2. On the first selected-message page, the returned user message must match the turn preview's role, phase, revision, complete source reference, timestamp, visible excerpt, and preview-truncation flag. Only the selected body fields `text` and `text_truncated` may differ; a positive regression covers an expanded/truncated body.
3. While a turn-index page is loading, retained rows from the prior page are disabled. They therefore cannot advance the shared epoch and strand the authoritative index request in a permanent loading state.
4. Conversation fixtures were moved from `conversation-page.test.ts` into `tests/fixtures/conversation.ts`, eliminating cross-imported test registration.

RED: `npm test -- --run tests/conversation-page.test.ts tests/render-conversation.test.ts` reported 2 failed files, 10 failed and 22 passed tests. The failures reproduced three selected-user metadata mismatches, request-limit and uncursored-range acceptance, invalid nonempty cursor/range shapes, and the old-row/index-loading race.

GREEN after the minimal fixes: the same command passed all 32 tests. After adding positive adaptive-page and body-expansion assertions plus the remaining timestamp/truncation metadata cases, the focused command passed 2 files / 35 tests.

Final `npm run check`: PASS, exit 0. ESLint passed with no diagnostics; Vitest passed 22 files / 321 tests; `tsc --noEmit --skipLibCheck && node esbuild.config.mjs production` passed. The reduction from the earlier displayed 327 registrations reflects removal of 19 duplicate registrations plus 13 new review regressions, not deleted production coverage.

`git diff --check`: PASS before the final check and report update.
