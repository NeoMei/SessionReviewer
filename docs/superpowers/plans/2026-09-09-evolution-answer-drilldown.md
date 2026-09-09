# Evolution Answer Drilldown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute after evolution-presentation-integrity passes independent review. This is the answer portion of the accepted closure task, not completion of original-Session navigation or problem actions.

**Goal:** Let a user expand the authenticated visible answer messages of the exact milestone source turn without fetching unrelated questions or persisting bodies.

**Architecture:** Extend the existing conversation renderer with an optional fixed-turn mode, reusing its message pagination, identity checks, coverage and disposal. The evolution shell creates this viewer only after an explicit answer click, resolves references against accepted chain dependencies, and disposes it when the panel/selection is replaced. Keep CLI `getConversation` and its fixed-argv authenticated backend unchanged.

**Tech Stack:** Existing TypeScript, Vitest/jsdom, Obsidian DOM helpers and ConversationPageV1. No dependencies or new public wire format.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§4.1,9,10,17.3 and `2026-09-05-v4-human-markdown-codec-design.md` human/source authority. Adapts the answer subpart of `2026-09-04-conversation-chain-evolution-closure.md` Task6 to the actual v4 shell.

## Global Constraints

- Ordinary scans and deterministic projection start zero Agent processes. No model summarizes a reply unless the user explicitly requests an AI candidate.
- Existing human edits win over generated fields. UI never rewrites accepted conclusion text or marks an expanded source answer as a replacement human conclusion.
- Answer bodies are on-demand, bounded authenticated reads. Do not persist them in view state, Vault, private derived stores, logs or diagnostics; never render raw tool outputs or hidden reasoning.
- Snapshot-qualified references remain exact. Omitted view coordinates resolve only from one matching accepted chain dependency; ambiguity is an unavailable state, never a latest-view guess.
- Keep five top tabs and five closure sections in their accepted order. This task adds no placeholder original-Session or problem buttons.
- No migration, provider activation, native daily-Vault install, network, merge, push or release in these task commits.

### Task 1: Add an exact fixed-turn mode to the existing conversation viewer

**Files:** Modify `obsidian-plugin/src/view/render-conversation.ts`; create `obsidian-plugin/tests/render-conversation-turn.test.ts`; retain existing `tests/render-conversation.test.ts` regressions. Reuse `tests/fixtures/conversation.ts` and the existing page validator. A mechanical shared-validator extraction may modify `src/data/conversation-page.ts` as specified below; do not change wire types, CLI runner or Go query service.

**Interfaces:** Extend the existing function compatibly:

```ts
export interface ConversationViewOptions { turnUnitId?: string }
export function renderConversation(
  initialIdentity: ConversationIdentity,
  load: ConversationLoader,
  options: ConversationViewOptions = {}
): ConversationElement;
```

The default mode is unchanged. A provided fixed ID requests `turn_messages` directly with `limit:20` and no index cursor. Identity changes preserve that fixed ID but invalidate in-flight work. Different turns are separate viewer instances, disposed by the host.

- [ ] Write RED tests with complete parsed `selectedConversationPage` fixtures. First request must have exact provider/Session/generation/selected view/turn and no `cursor`; no index request occurs. A controlled real-loader boundary returns the exact full assistant text and the renderer displays it; an unrelated first turn must never appear. Tests distinguish full body from retained-only excerpt and preserve commentary/final ordering and truncation labels.

```ts
const calls: ConversationRequest[] = [];
const root = renderConversation(identity, async request => {
  calls.push(request);
  return parseConversationPageV1(JSON.stringify(selectedConversationPage()));
}, { turnUnitId: "turn-1" });
await settle();
expect(calls).toEqual([{ ...identity, limit: 20, turnUnitId: "turn-1" }]);
expect(root.textContent).toContain("完整最终回答\n第二行");
expect(root.querySelector(".sr-turn-list")).toBeNull();
```

- [ ] Add behavioral REDs for wrong turn/view/project/provider/generation, invalid topology, an oversized page, changed dependency/coverage/turn metadata on pagination, skipped next or previous pages, first/last navigation, failed request retry and late response after dispose/identity change. Derive expected identities and page ranges independently; don't assert against the implementation's helper output. Tests use synthetic transport only, not user sources.
- [ ] Run `npm test -- tests/render-conversation-turn.test.ts tests/render-conversation.test.ts`, record intended assertion failures (not missing-export/fixture errors). Refactor only the shared request/render flow needed for fixed-turn mode. For its initial response there is no preloaded turn preview: authenticate page binding and exactly one matching turn before setting selectedTurn; subsequent pages additionally retain the first accepted turn identity and dependency/coverage. Keep existing index mode behavior.
- [ ] Validate supplied pages using the existing structural page validator before trusting first fixed-turn response, then retain the existing request-specific range/identity validation. Preserve a renderer-safe dependency boundary: do not import the Node-bound parser (`contracts-v4` imports Node hashing) into the browser renderer. If required, mechanically move the complete structural validator from `data/conversation-page.ts` to `data/conversation-page-validation.ts` and export `validateConversationPage(value: unknown): ConversationPageV1`; the original strict JSON parser must call the same validator. Use browser-native TextEncoder byte lengths for the extracted validator's text/cursor bounds rather than relying on Buffer, preserving UTF-8 byte semantics with multibyte boundary tests. Byte/duplicate-key/Unicode JSON checks stay at the wire parser. This is extraction, not weakened or duplicated validation; run existing parser/CLI fixtures and actual browser bundling. Reuse the same object validation for both renderer modes. Preserve epoch/disposed guards. Display only bounded generic/known typed errors, never arbitrary transport stdout.
- [ ] Render fixed mode with existing coverage, message detail and pagination, no question-index list. Keep a single polite live status for loading/error/completion; preserve button focus after paging/retry. Existing `dispose` clears local response references as well as DOM and invalidates pending work. No body values go to saveState.
- [ ] Run focused GREEN, full `npm run check`, and `git diff --check`. Commit exact code/tests/report and pass independent spec/quality review before shell integration. This is a renderer capability, not yet a visible product button.

### Task 2: Wire on-demand exact answer expansion into the v4 milestone detail

**Files:** Create `obsidian-plugin/src/view/render-v4-answer.ts` and `obsidian-plugin/tests/evolution-answer.test.ts`; modify `render-v4-evolution.ts`, `render-v4-shell.ts`, and bounded styles in `styles.css`. Add host regressions to `tests/v4-shell.test.ts`. Do not change legacy renderers, accepted projection text, parsers, CLI or native navigation.

**Interfaces:** Use the fixed-turn viewer from Task1. Export from the new file:

```ts
export type V4AnswerElement = HTMLElement & { dispose: () => void };
export function renderV4Answer(
  presentation: ReviewPresentationV4,
  milestone: TimelineEntryV4,
  index: SessionIndexV1 | undefined,
  load: ConversationLoader | undefined
): V4AnswerElement;
```

Extend the evolution renderer with an optional final options argument carrying index/loadConversation/cliUnavailable, and return an element with `dispose()`. Existing callers remain valid. The shell forwards its already available loader only when CLI is usable and disposes the old evolution instance before every replacement and shell disposal. Do not call the component or loader from the controller while rendering a different tab.

- [ ] Add RED through actual `renderV4Shell`: zero loader calls initially, on unrelated tab switches, in no-CLI mode even with a supplied loader, or with absent/unbound index; one exact request only after keyboard activation of `查看回答正文`. Use literal expected qualified source identity and explicit historical/current view differences. Preserve human conclusion text above the expanded body and do not promote evidence to resolved workflow.
- [ ] Build answer choices from `closed_loop.conclusion.source_turn_refs` only. For each, resolve against `presentation.chain_dependencies` matching provider, Session, turn membership and the supplied view if present. Exactly one dependency is required. Collapse canonical aliases without merging two different snapshots. If multiple exact turns remain, show an accessible labeled selector in original reference order; selecting alone does not fetch. Display provider/Session/turn/view so cross-Session sources are distinguishable.
- [ ] Require index project/generation/ProjectView identity to match the presentation and a matching Session row. Its nullable current view must not prevent a qualified retained reference from being queried: use the resolved evidence view for `expectedSessionViewDigest` and explicitly set `sessionViewDigest` to that same value; backend still authenticates current generation and retained root. A missing row or ambiguous binding disables the action with a reason, without trying another Session.

```ts
const requestIdentity: ConversationIdentity = {
  projectId: presentation.project_id,
  provider: ref.provider,
  sessionId: ref.session_id,
  expectedGenerationId: presentation.generation_id,
  expectedSessionViewDigest: dependency.session_view_digest,
  sessionViewDigest: dependency.session_view_digest
};
// Construct only on explicit activation:
viewer = renderConversation(requestIdentity, load, { turnUnitId: ref.turn_unit_id });
```

- [ ] Add `查看回答正文` / `收起回答正文` button with aria-expanded/aria-controls and a private in-memory viewer. Collapse, changing source choice, selecting another milestone, leaving the tab, project replacement and disposal all dispose the old viewer and discard its body references. A late resolved request cannot repopulate it. Reopen performs a new authenticated bounded read, never loads all pages automatically or writes page data into persisted selection state.
- [ ] Test available full body, unavailable-source retained excerpt, historical snapshot, zero assistant messages, multiple source turns, unavailable reader, source record/body truncation, explicit retry and stale generation. Stale generation keeps original excerpt and offers bounded failure copy; no automatic retry with a newer view/another turn. Source text containing HTML is visible text, never markup. Raw tool fields are not rendered in answer body.
- [ ] Run focused RED/GREEN and full plugin check. Controller tests actual temporary CLI scan→qualified milestone→exact current answer page and historical retained page through strict TypeScript parsing and real shell in Chrome1200/390. Exercise keyboard expansion/paging/collapse, switching milestone/tab during delayed response, zero initial/noCLI loads, no overflow/console errors, inspect screenshots. Temporary evidence stays outside shipped source.
- [ ] Commit exact files/report and pass independent review. Original-Session destination, related-problem action, optional candidates and native combined acceptance stay explicit remaining gates; a working answer button does not close original Task6 or product delivery.

## Preflight

| Shared surface | Producer / consumer | Checked relationship |
|---|---|---|
| Task1 existing viewer / fixed mode | authenticated page / detail and paging | same parser, range/dependency guards and disposal; no separate conversation implementation |
| Task1 / Task2 | optional fixed turn / click-created viewer | only the host controls on-demand construction and lifecycle |
| public reference / private query | exact accepted dependency / explicitly selected view | no fallback to latest or first question; CLI authenticates private roots |
| public state / persisted selection | visible body / saveState | body remains ephemeral, only existing milestone/tab selection persists |
| answer / other closure actions | bounded text read / native destination and problem route | source and problem actions remain separate working deliverables, not stubs |

Ruling: Reuse the existing conversation viewer with an optional fixed-turn mode instead of creating a second pagination/security implementation — existing Session browsing remains the default — cost if wrong is a bounded shared-renderer refactor with both mode regressions before integration.

Controller preflight2026-09-09: actual scan-exported shell fixture passes exact human-conclusion and zero-initial-body-query controls, then fails missing `查看回答正文` action (external `evolution-answer-entry-probe.ts`). This is a UI-entry RED, not a backend failure. The Go current full-body and historical retained-body lifecycle already passes separately. No implementation yet.
