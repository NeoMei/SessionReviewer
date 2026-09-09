# Task 1 report: exact fixed-turn conversation viewer

## Implemented

- Extended `renderConversation` compatibly with `ConversationViewOptions { turnUnitId?: string }`; omitted options keep the existing index-first flow.
- Fixed mode sends the first request directly as the exact identity plus `limit: 20` and `turnUnitId`, without an index request or cursor. Identity changes preserve the fixed turn while invalidating stale work.
- Authenticates every supplied object structurally before renderer-specific binding checks. The first fixed page must contain exactly one matching selected turn; later pages retain its turn metadata, dependency, coverage, total, availability and contiguous range.
- Reused the existing coverage, detail, message and pagination renderers without a question-index list. Added one polite fixed-mode live status, bounded known/generic errors, pagination/retry focus restoration, and response-reference clearing on dispose.
- Kept fixed copy accurate: it describes this question's visible messages and excludes raw tool output; default Sessions copy and behavior remain unchanged.
- Mechanically extracted the complete object validator to browser-safe `conversation-page-validation.ts`; the strict wire parser delegates to the same function and retains its duplicate-key, Unicode and 64 MiB wire checks. Only `Buffer.byteLength` in object text/cursor bounds changed to `TextEncoder`, with exact UTF-8 boundary tests.

## TDD evidence

### RED

Command:

```text
cd obsidian-plugin
npm test -- tests/render-conversation-turn.test.ts tests/render-conversation.test.ts
```

Relevant output before implementation:

```text
Test Files  1 failed | 1 passed (2)
Tests  13 failed | 21 passed (34)
expected first request ... turnUnitId: "turn-1"
received first request without turnUnitId
```

The new test file loaded and ran; failures were intended behavior assertions for the absent fixed mode (exact request, detail rendering, structural/range/dependency checks, navigation/focus, live status, retry and lifecycle), not missing exports or fixture errors. Existing renderer regressions passed at RED.

### GREEN

Focused command:

```text
npm test -- tests/render-conversation-turn.test.ts tests/render-conversation.test.ts tests/conversation-page.test.ts
```

Output:

```text
Test Files  3 passed (3)
Tests  85 passed (85)
```

Final full command:

```text
npm run check && git diff --check
```

Output:

```text
eslint: exit 0
Test Files  29 passed (29)
Tests  516 passed (516)
tsc --noEmit --skipLibCheck: exit 0
production esbuild: exit 0
git diff --check: exit 0
```

The first full-check attempt stopped at four new-test lint errors (unused imports/constant and one unnecessary assertion); these were mechanically removed before the successful final run.

## Test coverage

- Exact request identity (project/provider/Session/generation/current or selected view/turn), no index/cursor, unrelated index turn cannot appear, full assistant body shown.
- Commentary/final order, retained-only excerpt labeling, truncation labels and fixed-mode copy.
- Wrong project/provider/Session/generation/view/mode/turn; invalid topology; 21-message PAGE_SIZE overflow; 65-element structural overflow; UTF-8 text/cursor bounds at 65,536/65,537 and 4,096/4,097 bytes.
- Changed dependency, coverage and turn metadata; skipped next/previous pages; exact first/last cursor requests; accepted-page retention on rejection.
- Failed request retry, bounded known/generic messages without arbitrary transport detail, one polite fixed live status, focus restoration, late responses after identity replacement and disposal.

## Existing fixture repairs

`render-conversation.test.ts` now parses complete fixtures instead of type-casting them. Mechanical corrections were: reconcile source/visible/captured/oversized coverage; match the first selected turn's assistant count to its three messages; make middle-page ordinal/turn ID/selected metadata canonical; and use a structurally valid project mismatch instead of changing a Session root while leaving its source refs unchanged. Default-mode request, selection and pagination behavior was not changed.

## Controller integration evidence (reported externally)

After behavior-ready and final-copy builds, the controller reported GREEN for actual CLI current and historical fixed pages and real Chrome at 1200/390: exact request, full/retained bodies, malformed cross-provider rejection, keyboard retry, delayed disposal, no overflow and no console errors. It also reported GREEN for default published available/removed/removed-rescanned/restored pages and current-to-historical-to-current keyboard playback. This is integration evidence, not native Obsidian acceptance.

## Files changed

- `obsidian-plugin/src/view/render-conversation.ts`
- `obsidian-plugin/src/data/conversation-page.ts`
- `obsidian-plugin/src/data/conversation-page-validation.ts` (new)
- `obsidian-plugin/tests/render-conversation-turn.test.ts` (new)
- `obsidian-plugin/tests/render-conversation.test.ts`
- `.superpowers/sdd/2026-09-09-evolution-answer-drilldown/task-1-report.md`

## Self-review and concerns

- Re-read the brief/context, inspected the final diff, verified both renderer modes use the same object validator, and confirmed the extracted validator has no Node/contracts-v4 import.
- Confirmed no body is placed in save state, no shell/evolution button or controller document is touched, and no unowned workspace file is staged.
- Remaining boundary: this commit exposes renderer capability only. Host wiring is Task 2; independent spec/quality review and native Obsidian acceptance remain outstanding.
