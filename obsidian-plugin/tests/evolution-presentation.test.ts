import { describe, expect, it, vi } from "vitest";
import type { TimelineEntryV4 } from "../src/contracts/review-v4";
import { normalizeV4ViewState } from "../src/state/v4-view-state";
import { renderV4Evolution } from "../src/view/render-v4-evolution";
import { defaultV4MilestoneId, orderV4Milestones } from "../src/view/v4-milestone-order";
import { v4PresentationFixture } from "./fixtures/v4-shell";

function milestone(seed: TimelineEntryV4, id: string, occurredAt: string, title = id): TimelineEntryV4 {
  return { ...structuredClone(seed), id, occurred_at: occurredAt, title };
}

describe("v4 evolution chronology", () => {
  it("orders exact instants without mutating source values and breaks equivalent ties by ID", () => {
    const seed = v4PresentationFixture().timeline[0];
    const items = [
      milestone(seed, "earlier-offset", "2026-09-08T08:00:00+08:00"),
      milestone(seed, "later-utc", "2026-09-08T01:00:00Z"),
      milestone(seed, "z-equivalent", "2026-09-08T00:00:00.000000000Z"),
      milestone(seed, "a-equivalent", "2026-09-08T00:00:00Z")
    ];

    expect(orderV4Milestones(items).map((item) => item.id)).toEqual(["a-equivalent", "earlier-offset", "z-equivalent", "later-utc"]);
    expect(defaultV4MilestoneId(items)).toBe("later-utc");
    expect(items.map((item) => [item.id, item.occurred_at])).toEqual([
      ["earlier-offset", "2026-09-08T08:00:00+08:00"],
      ["later-utc", "2026-09-08T01:00:00Z"],
      ["z-equivalent", "2026-09-08T00:00:00.000000000Z"],
      ["a-equivalent", "2026-09-08T00:00:00Z"]
    ]);
  });

  it("orders sub-millisecond fractions exactly and has deterministic unknown-only and empty fallbacks", () => {
    const seed = v4PresentationFixture().timeline[0];
    const items = [
      milestone(seed, "nano-2", "2026-09-08T00:00:00.000000002Z"),
      milestone(seed, "unknown-z", ""),
      milestone(seed, "nano-1", "2026-09-08T00:00:00.000000001Z"),
      milestone(seed, "unknown-a", "not a date")
    ];

    expect(orderV4Milestones(items).map((item) => item.id)).toEqual(["unknown-a", "unknown-z", "nano-1", "nano-2"]);
    expect(defaultV4MilestoneId(items)).toBe("nano-2");
    expect(defaultV4MilestoneId(items.filter((item) => item.id.startsWith("unknown")))).toBe("unknown-z");
    expect(defaultV4MilestoneId([])).toBeNull();
  });

  it("defaults the rendered detail to the latest actual instant independent of input order", () => {
    const fixture = v4PresentationFixture();
    const seed = fixture.timeline[0];
    fixture.timeline = [
      milestone(seed, "later-utc", "2026-09-08T01:00:00Z", "Later actual instant"),
      milestone(seed, "earlier-offset", "2026-09-08T08:00:00+08:00", "Earlier actual instant")
    ];

    const root = renderV4Evolution(fixture, normalizeV4ViewState(undefined, fixture.project_id), vi.fn(), vi.fn());

    expect(root.querySelector(".sr-v4-milestone-detail h2")?.textContent).toBe("Later actual instant");
  });

  it("uses actual instants and stable IDs in both recent and full rail modes", () => {
    const fixture = v4PresentationFixture();
    const seed = fixture.timeline[0];
    fixture.timeline = [
      milestone(seed, "z-offset", "2026-09-08T08:00:00+08:00"),
      milestone(seed, "a-zulu", "2026-09-08T00:00:00Z"),
      milestone(seed, "fine-later", "2026-09-08T00:00:00.000000002Z"),
      milestone(seed, "fine-earlier", "2026-09-08T00:00:00.000000001Z")
    ];
    const root = renderV4Evolution(fixture, normalizeV4ViewState(undefined, fixture.project_id), vi.fn(), vi.fn());

    expect([...root.querySelectorAll<HTMLElement>("[data-v4-milestone-id]")].map((node) => node.dataset.v4MilestoneId)).toEqual([
      "fine-later", "fine-earlier", "z-offset", "a-zulu"
    ]);
    root.querySelector<HTMLButtonElement>('[data-v4-history-mode="full"]')!.click();
    expect([...root.querySelectorAll<HTMLElement>("[data-v4-milestone-id]")].map((node) => node.dataset.v4MilestoneId)).toEqual([
      "a-zulu", "z-offset", "fine-earlier", "fine-later"
    ]);
  });

  it("keeps unknown source dates verbatim without letting them displace the latest known instant", () => {
    const fixture = v4PresentationFixture();
    const seed = fixture.timeline[0];
    fixture.timeline = [
      milestone(seed, "known-latest", "2026-09-08T01:00:00Z", "Known latest"),
      milestone(seed, "unknown", "date retained verbatim", "Unknown date")
    ];
    const root = renderV4Evolution(fixture, normalizeV4ViewState(undefined, fixture.project_id), vi.fn(), vi.fn());

    expect(root.querySelector(".sr-v4-milestone-detail h2")?.textContent).toBe("Known latest");
    expect(root.querySelector('[data-v4-milestone-id="unknown"]')?.textContent).toContain("date retained verbatim");
  });
});

describe("v4 evolution evidence presentation", () => {
  it("retains an incomplete original answer and surfaces exact closure coverage warnings", () => {
    const fixture = v4PresentationFixture();
    const loop = fixture.timeline[0].closed_loop;
    loop.conclusion.kind = "visible_answer_excerpt";
    loop.conclusion.text = "  Retained incomplete answer.\n";
    loop.conclusion.source_turn_refs = [{ provider: "codex", session_id: "session-1", turn_unit_id: "turn-1" }];
    loop.source_turn_refs = structuredClone(loop.conclusion.source_turn_refs);
    loop.coverage = { source_turns: 3, captured_turns: 1, truncated_turns: 1, source_unavailable_turns: 1 };

    const root = renderV4Evolution(fixture, normalizeV4ViewState(undefined, fixture.project_id), vi.fn(), vi.fn());
    const conclusion = root.querySelector('[data-v4-segment="conclusion"]')!;

    expect(conclusion.querySelector(".sr-v4-segment-text")?.textContent).toBe("  Retained incomplete answer.\n");
    expect(conclusion.textContent).toContain("原回答摘录");
    expect(root.textContent).toContain("来源 3 · 已捕获 1 · 截断 1 · 来源不可用 1");
    expect(root.textContent).toContain("部分捕获");
    expect(root.textContent).toContain("回答可能不完整");
    expect(root.textContent).toContain("来源不可用");
    expect(root.textContent).not.toContain("已验证回答 1");
  });

  it("does not describe zero or fully captured coverage as unresolved", () => {
    for (const coverage of [
      { source_turns: 0, captured_turns: 0, truncated_turns: 0, source_unavailable_turns: 0 },
      { source_turns: 1, captured_turns: 1, truncated_turns: 0, source_unavailable_turns: 0 }
    ]) {
      const fixture = v4PresentationFixture();
      fixture.timeline[0].closed_loop.coverage = coverage;
      const root = renderV4Evolution(fixture, normalizeV4ViewState(undefined, fixture.project_id), vi.fn(), vi.fn());
      expect(root.querySelector(".sr-v4-closure-warnings")).toBeNull();
    }
  });

  it("shows declared evidence labels, exact snapshot-qualified provenance, and distinct missing copy", () => {
    const fixture = v4PresentationFixture();
    const item = fixture.timeline[0];
    const digest = `sha256:${"a".repeat(64)}`;
    item.kind = "machine_verification";
    item.closed_loop.trigger_question = {
      state: "present", text: "Ordinary source excerpt", missing_reason: null,
      source_turn_refs: [{ provider: "codex", session_id: "session-1", turn_unit_id: "turn-1", session_view_digest: digest }]
    };
    item.closed_loop.conclusion = { kind: "ai_candidate_confirmed", text: "AI-confirmed excerpt", missing_reason: null, source_turn_refs: [] };
    item.closed_loop.execution = { state: "missing", text: "", missing_reason: "no_execution_evidence", source_turn_refs: [] };
    item.closed_loop.verification = { state: "missing", text: "", missing_reason: "not_verified", source_turn_refs: [] };
    item.closed_loop.impact_and_follow_up = { state: "partial", text: "Partial follow-up", missing_reason: null, source_turn_refs: [] };

    const root = renderV4Evolution(fixture, normalizeV4ViewState(undefined, fixture.project_id), vi.fn(), vi.fn());

    expect(root.querySelector(".sr-detail-kicker")?.textContent).toContain("机器验证");
    expect(root.querySelector('[data-v4-segment="trigger_question"]')?.textContent).toContain("已捕获");
    expect(root.querySelector('[data-v4-segment="conclusion"]')?.textContent).toContain("AI 整理 · 已确认");
    expect(root.querySelector('[data-v4-segment="impact_and_follow_up"]')?.textContent).toContain("部分捕获");
    expect(root.textContent).toContain("未发现执行证据");
    expect(root.textContent).toContain("待验证");
    expect(root.querySelector('[data-v4-segment="trigger_question"] details')?.textContent).toContain(`快照视图：${digest}`);
    expect(root.textContent).not.toContain("machine_verification");
  });

  it("keeps human confirmation local to the conclusion and treats unknown milestone kinds neutrally", () => {
    const fixture = v4PresentationFixture();
    fixture.timeline[0].kind = "future_kind";
    fixture.timeline[0].closed_loop.conclusion.kind = "human_confirmed";
    const root = renderV4Evolution(fixture, normalizeV4ViewState(undefined, fixture.project_id), vi.fn(), vi.fn());

    expect(root.querySelector(".sr-detail-kicker")?.textContent).toContain("其他里程碑");
    expect(root.querySelector(".sr-detail-kicker")?.textContent).not.toContain("人工确认");
    expect(root.querySelector('[data-v4-segment="conclusion"]')?.textContent).toContain("人工确认");
  });
});
