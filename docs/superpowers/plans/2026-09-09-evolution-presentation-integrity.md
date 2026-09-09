# Evolution Presentation Integrity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute after generated-milestone-publication is independently reviewed; this bounded UI correction does not close the remaining answer/source/problem action work.

**Goal:** Present accepted milestone chronology, answer completeness and semantic authority honestly in the existing v4 evolution layout.

**Architecture:** One v4 ordering helper serves the rail and shell default selection. The existing five-segment detail renders coverage and explicit conclusion provenance without changing stored fields or invoking any loader. Keep the approved five top tabs, timeline/detail geometry and full-history mode.

**Tech Stack:** Existing TypeScript ES2021, Vitest/jsdom, Obsidian DOM helpers and CSS. No dependencies.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§3,4,4.1,9,10; `docs/superpowers/specs/2026-09-05-v4-human-markdown-codec-design.md` human/evidence authority. This adapts the read-presentation portion of `2026-09-04-conversation-chain-evolution-closure.md` Task6 to the actual separate v4 host, not legacy BrowserModel/render-evolution.

## Global Constraints

- Ordinary scans and deterministic projection start zero Agent processes. No model summarizes a reply unless the user explicitly requests an AI candidate.
- Existing human edits win over generated fields. UI presentation never rewrites accepted text, timestamps, source identity, hashes, decisions or workflow state.
- Fixed detail order: 触发问题、Agent 结论、执行与变更、结果与验证、对项目的影响与后续.
- `execution_verified` is a machine evidence state. It never implies the human workflow state `resolved`.
- Do not infer a whole milestone's human provenance from an edited conclusion. A confirmed conclusion and a machine-qualified milestone can coexist.
- Answer bodies, authenticated original-Session destinations, problem actions and optional candidates remain explicit subsequent gates. This task must not add fake or no-op action controls.
- No migration, new platform support, public contract change, daily-Vault install, network, Agent execution or release in this task.

### Task 1: Correct v4 milestone chronology and visible evidence status

**Files:** Create `obsidian-plugin/src/view/v4-milestone-order.ts` and `obsidian-plugin/tests/evolution-presentation.test.ts`. Modify `src/view/render-v4-evolution.ts`, `src/view/render-v4-shell.ts`, `tests/v4-shell.test.ts` and `styles.css` only for the existing detail's bounded status/coverage presentation. Do not modify legacy renderers, parsers or publication code.

**Interfaces:** Export `orderV4Milestones(items: readonly TimelineEntryV4[]): TimelineEntryV4[]` (oldest known instant first, stable ID tie-break) and `defaultV4MilestoneId(items: readonly TimelineEntryV4[]): string | null` (latest known instant; deterministic fallback if no known time). Both operate on copies and never mutate source arrays. The shell preserves a still-valid user selection; defaulting and rail selection call the same helper.

- [ ] Write owned RED tests using `v4PresentationFixture` and `v4SnapshotFixture` from `tests/fixtures/v4-shell.ts`. Reverse input order deliberately; assert timezone-equivalent instants tie by ID, later actual instants win over textual offsets, and sub-millisecond fractions remain ordered rather than rounded together. Assert recent mode, full mode, shell first render and leave/re-enter default selection all agree. Preserve an explicitly selected older milestone across mode changes and unrelated tabs. Unknown/empty dates remain verbatim, use deterministic fallback and do not displace the latest known instant.

```ts
const fixture = v4PresentationFixture();
const seed = fixture.timeline[0];
const items = [
  { ...structuredClone(seed), id: "earlier-offset", occurred_at: "2026-09-08T08:00:00+08:00" },
  { ...structuredClone(seed), id: "later-utc", occurred_at: "2026-09-08T01:00:00Z" }
];
expect(orderV4Milestones(items).map(item => item.id)).toEqual(["earlier-offset", "later-utc"]);
expect(defaultV4MilestoneId(items)).toBe("later-utc");
expect(items[0].occurred_at).toBe("2026-09-08T08:00:00+08:00");
```

- [ ] Write owned RED detail tests: a present excerpt with `truncated_turns:1` must retain exact text and show `回答可能不完整`; captured/source mismatch shows partial coverage; nonzero unavailable count shows `来源不可用`. All zero/fully captured is not shown as unresolved, and counts are never equated to number of verified answers. Visible source-qualified human/AI-confirmed/ordinary excerpts receive distinct labels. Snapshot-qualified refs display their exact selected view in expandable provenance, never substitute current generation/view. Missing answer/execution/verification retain their distinct existing phrases. Unknown accepted milestone kinds receive a neutral label, not fabricated human confirmation.

```ts
const loop = fixture.timeline[0].closed_loop;
loop.conclusion.kind = "visible_answer_excerpt";
loop.conclusion.text = "Retained incomplete answer.";
loop.coverage = { source_turns: 1, captured_turns: 1, truncated_turns: 1, source_unavailable_turns: 0 };
const root = renderV4Evolution(fixture, normalizeV4ViewState(undefined, fixture.project_id), () => {}, () => {});
expect(root.textContent).toContain("Retained incomplete answer.");
expect(root.textContent).toContain("回答可能不完整");
```

- [ ] Run RED: `npm test -- tests/evolution-presentation.test.ts tests/v4-shell.test.ts` from `obsidian-plugin`. Record actual assertion failures, not a missing export alone. Existing controller `evolution-instant-probe.ts` and `evolution-coverage-probe.ts` independently reproduce both defects; do not depend on external files in owned tests.
- [ ] Implement the single ordering helper. Represent sortable timestamps as seconds plus normalized fractional digits (or an exact integer), not lossy millisecond-only numbers. A valid RFC3339 offset must normalize to an instant before comparison; preserve original strings for display. Give unknown values a deterministic ordering, then select the newest known item explicitly. Keep stable-ID ties deterministic and input arrays unchanged. Use this helper in both shell and rail; remove duplicate `timeline.at(-1)` defaults there.
- [ ] Render compact, local status labels from declared values: conclusion `visible_answer_excerpt → 原回答摘录`, `human_confirmed → 人工确认`, `ai_candidate_confirmed → AI 整理 · 已确认`, `missing → 未捕获 Agent 回答`; segment `present → 已捕获`, `partial → 部分捕获`, `missing → 缺少证据`. Continue using distinct missing-reason copy. Machine milestone kind uses a closed local map (`machine_verification → 机器验证`, `machine_commit → 提交记录`, `machine_release → 发布记录`, `machine_deployment → 部署记录`, `machine_version → 版本记录`); unknown kinds stay neutral. These labels describe evidence and never claim project completion. Do not translate or rewrite user-authored title/summary/text.
- [ ] Add one compact closure coverage disclosure using the four exact counters; surface incomplete/unavailable warnings without requiring expansion. Keep detailed identifiers in `details` using text nodes and wrapping; no raw machine enum as the primary explanation, no `innerHTML`, no answer/source fetch on initial render or tab switch. Preserve keyboard focus, full/recent pagination and narrow layout.
- [ ] Run focused GREEN, `npm run check`, `git diff --check`, and repository pane-layout script with existing Playwright/Chrome. Controller rebuilds and runs the two original RED probes plus actual Go-published presentation→strict TS parser→Chrome at1200/390. Test recent/full, keyboard provenance expansion, selected identity across tabs, exact answer/coverage, no horizontal overflow or console errors; inspect screenshots. No repeated full Go suite is needed for a TypeScript-only change unless Go-backed wire tests reveal a concrete seam issue.
- [ ] Commit exact files and report; independent spec/quality review before the next UI/runtime task. Record that original-Session destinations and answer/problem controls are still open, so this task cannot be reported as original Task6 or product completion.

## Preflight and boundaries

| Shared surface | Requirement | Checked relationship |
|---|---|---|
| ordering helper → rail / shell | same actual chronology and default ID | current implementations both independently use array/text order; both consumers are in scope |
| accepted closure → visible labels | exact text and provenance retained | no contract or stored-state rewrite; human confirmation applies only to the declared conclusion |
| closure coverage → warning | missing/truncated/unavailable remain explicit | this is captured evidence coverage, never workflow completion |
| publication Task2 → renderer | actual authenticated presentation | controller test input must come from scan output once Task2 closes, not a hand-filled UI fixture alone |
| original closure Task6 → later actions | bounded exact-turn answer and real native destination | not implemented by this task; no stub controls accepted as substitutes |

Ruling: Keep exact time precision for ordering and use one shared default selector — the accepted history must not depend on timezone spelling or input array order — cost if wrong is local ordering correction, never mutation of source timestamps.
