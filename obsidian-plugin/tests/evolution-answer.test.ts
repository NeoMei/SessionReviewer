import { describe, expect, it, vi } from "vitest";
import type { ConversationRequest } from "../src/cli/runner";
import type { ConversationPageV1 } from "../src/contracts/conversation-page";
import type { ReviewPresentationV4, SessionIndexV1, SourceTurnRefV4, TimelineEntryV4 } from "../src/contracts/review-v4";
import { parseConversationPageV1 } from "../src/data/conversation-page";
import { renderV4Answer } from "../src/view/render-v4-answer";
import { selectedConversationPage, visibleMessage, visibleTurn } from "./fixtures/conversation";
import { v4IndexFixture, v4PresentationFixture } from "./fixtures/v4-shell";

const CURRENT_VIEW = `sha256:${"2".repeat(64)}`;
const HISTORICAL_VIEW = `sha256:${"8".repeat(64)}`;

function qualified(
  refs: SourceTurnRefV4[] = [{ provider: "claude", session_id: "session-1", turn_unit_id: "turn-current", session_view_digest: CURRENT_VIEW }]
): { presentation: ReviewPresentationV4; milestone: TimelineEntryV4; index: SessionIndexV1 } {
  const presentation = v4PresentationFixture();
  const milestone = presentation.timeline[0];
  milestone.closed_loop.conclusion.source_turn_refs = refs;
  presentation.chain_dependencies = [
    { provider: "claude", session_id: "session-1", session_view_digest: CURRENT_VIEW, dependency_digest: `sha256:${"3".repeat(64)}`, turn_unit_ids: ["turn-current"] },
    { provider: "claude", session_id: "session-1", session_view_digest: HISTORICAL_VIEW, dependency_digest: `sha256:${"4".repeat(64)}`, turn_unit_ids: ["turn-historical"] }
  ];
  return { presentation, milestone, index: v4IndexFixture() };
}

function pageFor(request: ConversationRequest, overrides: Record<string, unknown> = {}): ConversationPageV1 {
  const preview = visibleMessage("user", {
    source_ref: { provider: request.provider, session_id: request.sessionId, source_identity: "source-1", record_ordinal: 1, source_hash: "3".repeat(64) },
    text: null
  });
  const user = { ...preview, text: "目标问题" };
  const assistant = visibleMessage("assistant", {
    source_ref: { provider: request.provider, session_id: request.sessionId, source_identity: "source-1", record_ordinal: 2, source_hash: "4".repeat(64) },
    visible_excerpt: "回答摘录",
    text: "<img src=x onerror=alert(1)> 完整回答"
  });
  return parseConversationPageV1(JSON.stringify(selectedConversationPage({
    minimum_reader_version: "0.4.3",
    project_id: request.projectId,
    provider: request.provider,
    session_id: request.sessionId,
    generation_id: request.expectedGenerationId,
    session_view_digest: request.sessionViewDigest,
    evidence_session_view_digest: request.sessionViewDigest,
    dependency_digest: `sha256:${(request.turnUnitId === "turn-historical" ? "4" : "3").repeat(64)}`,
    turn_unit_id: request.turnUnitId,
    turn_units: [visibleTurn({ turn_unit_id: request.turnUnitId, user_message: preview })],
    messages: [user, assistant],
    ...overrides
  })));
}

async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

describe("v4 milestone exact answer", () => {
  it("renders text safely and sends one exact authenticated request only on activation", async () => {
    const { presentation, milestone, index } = qualified();
    const load = vi.fn((request: ConversationRequest) => {
      const base = pageFor(request);
      return Promise.resolve(pageFor(request, {
        turn_units: [{ ...base.turn_units[0], action_count: 1 }],
        actions: [{ revision_id: `sha256:${"5".repeat(64)}`, source_ref: base.turn_units[0].user_message.source_ref, kind: "tool_call", tool_name: "raw_tool_call", excerpt: "raw private tool fields" }],
        action_total: 1
      }));
    });
    const root = renderV4Answer(presentation, milestone, index, load);

    expect(load).not.toHaveBeenCalled();
    expect(root.textContent).toContain(`claude / Session session-1 / turn turn-current / view ${CURRENT_VIEW}`);
    root.querySelector<HTMLButtonElement>('[data-action="expand-milestone-answer"]')!.click();
    await settle();

    expect(load).toHaveBeenCalledTimes(1);
    expect(load).toHaveBeenCalledWith({ projectId: "project-p", provider: "claude", sessionId: "session-1", expectedGenerationId: "generation-1", expectedSessionViewDigest: CURRENT_VIEW, sessionViewDigest: CURRENT_VIEW, limit: 20, turnUnitId: "turn-current" });
    expect(root.textContent).toContain("<img src=x onerror=alert(1)> 完整回答");
    expect(root.querySelector("img")).toBeNull();
    expect(root.textContent).not.toContain("raw_tool_call");
    expect(root.textContent).not.toContain("raw private tool fields");
    expect(root.textContent).toContain("已认证执行证据：操作 1/1");
  });

  it("collapses canonical aliases, preserves distinct snapshots, and selection alone never loads", () => {
    const { presentation, milestone, index } = qualified([
      { provider: "claude", session_id: "session-1", turn_unit_id: "turn-current" },
      { provider: "claude", session_id: "session-1", turn_unit_id: "turn-current", session_view_digest: CURRENT_VIEW },
      { provider: "claude", session_id: "session-1", turn_unit_id: "turn-historical", session_view_digest: HISTORICAL_VIEW }
    ]);
    const load = vi.fn(() => new Promise<never>(() => {}));
    const root = renderV4Answer(presentation, milestone, index, load);
    const selector = root.querySelector<HTMLSelectElement>('[aria-label="选择回答来源"]')!;

    expect(selector.options).toHaveLength(2);
    expect([...selector.options].map((option) => option.textContent)).toEqual([
      `claude / Session session-1 / turn turn-current / view ${CURRENT_VIEW}`,
      `claude / Session session-1 / turn turn-historical / view ${HISTORICAL_VIEW}`
    ]);
    selector.selectedIndex = 1;
    selector.dispatchEvent(new Event("change", { bubbles: true }));
    expect(load).not.toHaveBeenCalled();
    root.querySelector<HTMLButtonElement>('[data-action="expand-milestone-answer"]')!.click();
    expect(load).toHaveBeenCalledWith(expect.objectContaining({ turnUnitId: "turn-historical", expectedSessionViewDigest: HISTORICAL_VIEW, sessionViewDigest: HISTORICAL_VIEW }));
  });

  it("allows a retained historical reference when the current Session view is null", async () => {
    const { presentation, milestone, index } = qualified([
      { provider: "claude", session_id: "session-1", turn_unit_id: "turn-historical", session_view_digest: HISTORICAL_VIEW }
    ]);
    index.sessions[0].session_view_digest = null;
    index.sessions[0].source_availability = "unavailable";
    const load = vi.fn((request: ConversationRequest) => {
      const preview = visibleMessage("user", { source_ref: { provider: request.provider, session_id: request.sessionId, source_identity: "source-1", record_ordinal: 1, source_hash: "3".repeat(64) }, text: null });
      return Promise.resolve(pageFor(request, {
      body_availability: "retained_excerpt",
      turn_units: [visibleTurn({ turn_unit_id: request.turnUnitId, user_message: preview })],
      messages: [
        preview,
        visibleMessage("assistant", { source_ref: { provider: request.provider, session_id: request.sessionId, source_identity: "source-1", record_ordinal: 2, source_hash: "4".repeat(64) }, phase: null, text: null, visible_excerpt: "历史保留回答" })
      ]
    }));
    });
    const root = renderV4Answer(presentation, milestone, index, load);

    root.querySelector<HTMLButtonElement>('[data-action="expand-milestone-answer"]')!.click();
    await settle();
    expect(root.textContent).toContain("历史保留回答");
    expect(root.textContent).toContain("原始消息正文当前不可用");
    expect(load).toHaveBeenCalledWith(expect.objectContaining({ expectedSessionViewDigest: HISTORICAL_VIEW, sessionViewDigest: HISTORICAL_VIEW }));
  });

  it("shows zero Agent messages and bounded truncation without treating either as a resolved workflow", async () => {
    const { presentation, milestone, index } = qualified();
    const preview = visibleMessage("user", {
      source_ref: { provider: "claude", session_id: "session-1", source_identity: "source-1", record_ordinal: 1, source_hash: "3".repeat(64) },
      text: null
    });
    const noAnswer = (request: ConversationRequest) => pageFor(request, {
      total: 1,
      range_end: 1,
      turn_units: [visibleTurn({ turn_unit_id: request.turnUnitId, user_message: preview, answer_state: "no_answer", assistant_message_count: 0 })],
      messages: [{ ...preview, text: "尚未回答的问题" }],
      coverage: { source_records: 3, visible_messages: 1, captured_messages: 1, truncated_messages: 1, truncated_bodies: 1, context_messages: 0, orphan_messages: 0, oversized_records: 1, malformed_records: 0, complete: false }
    });
    const root = renderV4Answer(presentation, milestone, index, (request) => Promise.resolve(noAnswer(request)));

    root.querySelector<HTMLButtonElement>('[data-action="expand-milestone-answer"]')!.click();
    await settle();
    expect(root.textContent).toContain("尚无 Agent 回答。");
    expect(root.textContent).toContain("1 条超限源记录被省略");
    expect(root.textContent).toContain("1 条消息正文超出读取上限");
    expect(root.textContent).toContain("不应据此断定没有其他可见内容");
    expect(root.textContent).not.toContain("问题已解决");
  });

  it("keeps failure bounded and retries the same stale-generation request only when asked", async () => {
    const { presentation, milestone, index } = qualified();
    const stale = () => Object.assign(new Error("private generation details"), { code: "generation_mismatch" });
    const load = vi.fn().mockRejectedValue(stale());
    const root = renderV4Answer(presentation, milestone, index, load);

    root.querySelector<HTMLButtonElement>('[data-action="expand-milestone-answer"]')!.click();
    await settle();
    expect(root.textContent).toContain("项目已更新；请刷新后重新读取问答。");
    expect(root.textContent).not.toContain("private generation details");
    expect(load).toHaveBeenCalledTimes(1);
    root.querySelector<HTMLButtonElement>('[data-action="retry-conversation-page"]')!.click();
    await settle();
    expect(load).toHaveBeenCalledTimes(2);
    expect(load.mock.calls[0]).toEqual(load.mock.calls[1]);
  });

  it.each([
    ["absent index", (value: ReturnType<typeof qualified>) => { value.index = undefined as unknown as SessionIndexV1; }],
    ["wrong project", (value: ReturnType<typeof qualified>) => { value.index.project_id = "project-other"; }],
    ["wrong generation", (value: ReturnType<typeof qualified>) => { value.index.generation_id = "generation-other"; }],
    ["wrong ProjectView", (value: ReturnType<typeof qualified>) => { value.index.project_view_digest = `sha256:${"9".repeat(64)}`; }],
    ["missing Session", (value: ReturnType<typeof qualified>) => { value.index.sessions = []; }],
    ["ambiguous Session", (value: ReturnType<typeof qualified>) => { value.index.sessions.push(structuredClone(value.index.sessions[0])); }]
  ])("disables loading for %s without trying another Session", (_label, mutate) => {
    const value = qualified();
    mutate(value);
    const load = vi.fn(() => new Promise<never>(() => {}));
    const root = renderV4Answer(value.presentation, value.milestone, value.index, load);
    const control = root.querySelector<HTMLButtonElement>('[data-action="expand-milestone-answer"]')!;

    expect(control.disabled).toBe(true);
    expect(root.textContent).toContain("无法读取回答正文");
    control.click();
    expect(load).not.toHaveBeenCalled();
  });

  it("disposes the private viewer on collapse, source change, and host disposal", () => {
    const { presentation, milestone, index } = qualified([
      { provider: "claude", session_id: "session-1", turn_unit_id: "turn-current", session_view_digest: CURRENT_VIEW },
      { provider: "claude", session_id: "session-1", turn_unit_id: "turn-historical", session_view_digest: HISTORICAL_VIEW }
    ]);
    const load = vi.fn(() => new Promise<ConversationPageV1>(() => {}));
    const root = renderV4Answer(presentation, milestone, index, load);
    const toggle = () => root.querySelector<HTMLButtonElement>('[data-action="expand-milestone-answer"]')!.click();

    toggle();
    expect(root.querySelector(".sr-conversation")).not.toBeNull();
    toggle();
    expect(root.querySelector(".sr-conversation")).toBeNull();
    toggle();
    const selector = root.querySelector<HTMLSelectElement>('[aria-label="选择回答来源"]')!;
    selector.selectedIndex = 1;
    selector.dispatchEvent(new Event("change", { bubbles: true }));
    expect(root.querySelector(".sr-conversation")).toBeNull();
    root.querySelector<HTMLButtonElement>('[data-action="expand-milestone-answer"]')!.click();
    root.dispose();
    expect(root.textContent).toBe("");
    expect(load).toHaveBeenCalledTimes(3);
  });
});
