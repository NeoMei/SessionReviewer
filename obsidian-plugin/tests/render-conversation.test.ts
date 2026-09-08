import { describe, expect, it, vi } from "vitest";
import type { ConversationPageV1 } from "../src/contracts/conversation-page";
import type { ConversationRequest } from "../src/cli/runner";
import { renderConversation } from "../src/view/render-conversation";
import { conversationPage, selectedConversationPage } from "./fixtures/conversation";

function page(value: Record<string, unknown>): ConversationPageV1 {
  return value as unknown as ConversationPageV1;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

const identity = {
  projectId: "project-p",
  provider: "codex",
  sessionId: "session-1",
  expectedGenerationId: "generation-1",
  expectedSessionViewDigest: `sha256:${"1".repeat(64)}`
};

describe("selected Session conversation", () => {
  it("renders an authenticated provider-neutral retained conversation", async () => {
    const provider = "claude";
    const response = (selected: boolean) => selected ? selectedConversationPage({ provider, body_availability: "retained_excerpt" }) : conversationPage({ provider, body_availability: "retained_excerpt" });
    const bind = (value: Record<string, unknown>) => {
      for (const turn of value.turn_units as Record<string, unknown>[]) {
        const user = turn.user_message as Record<string, unknown>;
        user.source_ref = { ...(user.source_ref as Record<string, unknown>), provider };
      }
      for (const message of value.messages as Record<string, unknown>[]) {
        message.source_ref = { ...(message.source_ref as Record<string, unknown>), provider };
        message.phase = null;
        message.text = null;
      }
      return page(value);
    };
    const root = renderConversation({ ...identity, provider }, (request) => Promise.resolve(bind(response(request.turnUnitId !== undefined))));
    await settle();
    await settle();
    expect(root.textContent).toContain("如何恢复可见问答？");
    expect(root.textContent).toContain("仅保留认证摘录");
  });

  it("never claims a retained-only truncated excerpt has a loaded full body", async () => {
    const retained = selectedConversationPage({
      body_availability: "retained_excerpt",
      messages: [
        { ...(selectedConversationPage().messages as Record<string, unknown>[])[0], phase: null, text: null },
        { ...(selectedConversationPage().messages as Record<string, unknown>[])[1], phase: null, text: null, truncated: true }
      ],
      coverage: { ...(selectedConversationPage().coverage as Record<string, unknown>), truncated_messages: 1, diagnostics_available: true }
    });
    const root = renderConversation(identity, (request) => Promise.resolve(page(request.turnUnitId ? retained : conversationPage())));
    await settle();
    await settle();
    expect(root.textContent).toContain("仅保留认证摘录；完整正文不可用。");
    expect(root.textContent).not.toContain("下方为已读取正文");
  });

  it("does not render legacy unknown coverage counters as zero", async () => {
    const legacy = conversationPage({
      body_availability: "retained_excerpt",
      coverage: { ...(conversationPage().coverage as Record<string, unknown>), source_records: 3, visible_messages: 3, captured_messages: 2, diagnostics_available: false, complete: false }
    });
    const root = renderConversation(identity, () => Promise.resolve(page(legacy)));
    await settle();
    expect(root.textContent).toContain("上下文包装 未知");
    expect(root.textContent).not.toContain("上下文包装 0");
  });

  it("renders loading, user/Agent labels, safe full bodies, phases, truncation and honest coverage", async () => {
    const index = page(conversationPage({
      turn_units: [
        (conversationPage().turn_units as Record<string, unknown>[])[0],
        { ...(conversationPage().turn_units as Record<string, unknown>[])[0], turn_unit_id: "turn-2", ordinal: 2, answer_state: "partial", assistant_message_count: 1 },
        { ...(conversationPage().turn_units as Record<string, unknown>[])[0], turn_unit_id: "turn-3", ordinal: 3, answer_state: "no_answer", assistant_message_count: 0 }
      ],
      total: 3,
      range_end: 3,
      coverage: { ...(conversationPage().coverage as Record<string, unknown>), oversized_records: 2, complete: false }
    }));
    const selected = page(selectedConversationPage({
      messages: [
        ...(selectedConversationPage().messages as Record<string, unknown>[]),
        { ...(selectedConversationPage().messages as Record<string, unknown>[])[1], revision_id: `sha256:${"5".repeat(64)}`, phase: "commentary", text: "<img src=x onerror=alert(1)>\n过程全文", text_truncated: true }
      ],
      total: 3,
      range_end: 3,
      coverage: { ...(selectedConversationPage().coverage as Record<string, unknown>), source_records: 3, visible_messages: 3, captured_messages: 3, truncated_bodies: 1, oversized_records: 2, complete: false }
    }));
    const pending = deferred<ConversationPageV1>();
    const load = vi.fn((request: ConversationRequest) => request.turnUnitId ? Promise.resolve(selected) : pending.promise);
    const root = renderConversation(identity, load);

    expect(root.textContent).toContain("正在读取问答记录");
    pending.resolve(index);
    await settle();
    expect(root.matches('[aria-label="问答记录"]')).toBe(true);
    expect(root.textContent).toContain("用户问题");
    expect(root.textContent).toContain("已回答（不代表已验证）");
    expect(root.textContent).toContain("回答不完整");
    expect(root.textContent).toContain("尚无 Agent 回答");
    expect(root.textContent).toContain("2 条超限源记录被省略");

    await settle();
    expect(root.textContent).toContain("用户");
    expect(root.textContent).toContain("Agent · 最终回答");
    expect(root.textContent).toContain("Agent · 过程说明");
    expect(root.textContent).toContain("完整最终回答\n第二行");
    expect(root.textContent).toContain("正文已截断");
    expect(root.querySelector("img")).toBeNull();
    expect(root.textContent).toContain("<img src=x onerror=alert(1)>");
  });

  it("retries the same failed index page and provides first/middle/last controls for turns and messages", async () => {
	const first = page(conversationPage({ next_cursor: "next-index", total: 60 }));
	const middle = page(conversationPage({ previous_cursor: "previous-index", next_cursor: "next-index-2", range_start: 1, range_end: 2, total: 60 }));
	const selectedMiddle = page(selectedConversationPage({ next_cursor: "next-message", range_end: 2, total: 60 }));
    const load = vi.fn()
      .mockRejectedValueOnce(new Error("问答暂不可用"))
	  .mockResolvedValueOnce(first)
	  .mockResolvedValueOnce(page(selectedConversationPage()))
	  .mockResolvedValueOnce(middle)
	  .mockResolvedValueOnce(selectedMiddle)
      .mockResolvedValue(page(conversationPage()));
    const root = renderConversation(identity, load);
    await settle();
    expect(root.textContent).toContain("问答暂不可用");
    root.querySelector<HTMLButtonElement>('[data-action="retry-conversation-page"]')?.click();
    await settle();
	await settle();
	root.querySelector<HTMLButtonElement>('[data-action="next-turn-page"]')?.click();
	await settle();
	await settle();
	expect(root.textContent).toContain("2–2 / 60");
    for (const action of ["first-turn-page", "previous-turn-page", "next-turn-page", "last-turn-page"]) {
      expect(root.querySelector(`[data-action="${action}"]`)).not.toBeNull();
    }
	expect(root.textContent).toContain("1–2 / 60");
    for (const action of ["first-message-page", "previous-message-page", "next-message-page", "last-message-page"]) {
      expect(root.querySelector(`[data-action="${action}"]`)).not.toBeNull();
    }
    root.querySelector<HTMLButtonElement>('[data-action="next-message-page"]')?.click();
    await settle();
    expect(load).toHaveBeenLastCalledWith(expect.objectContaining({ turnUnitId: "turn-1", messageCursor: "next-message" }));
  });

  it("suppresses stale responses after identity replacement and disposal", async () => {
    const pendingA = deferred<ConversationPageV1>();
    const pendingB = deferred<ConversationPageV1>();
    const load = vi.fn((request: ConversationRequest) => request.sessionId === "session-1" ? pendingA.promise : pendingB.promise);
    const root = renderConversation(identity, load);
    root.updateIdentity({ ...identity, sessionId: "session-2" });
    pendingA.resolve(page(conversationPage({ session_id: "session-1", turn_units: [{ ...(conversationPage().turn_units as Record<string, unknown>[])[0], user_message: { ...((conversationPage().turn_units as Record<string, unknown>[])[0].user_message as Record<string, unknown>), visible_excerpt: "过期问答" } }] })));
    pendingB.resolve(page(conversationPage({ session_id: "session-2", turn_units: [{ ...(conversationPage().turn_units as Record<string, unknown>[])[0], user_message: { ...((conversationPage().turn_units as Record<string, unknown>[])[0].user_message as Record<string, unknown>), source_ref: { ...(((conversationPage().turn_units as Record<string, unknown>[])[0].user_message as Record<string, unknown>).source_ref as Record<string, unknown>), session_id: "session-2" }, visible_excerpt: "当前问答" } }] })));
    await settle();
    expect(root.textContent).toContain("当前问答");
    expect(root.textContent).not.toContain("过期问答");

    const disposedPending = deferred<ConversationPageV1>();
    const disposed = renderConversation(identity, () => disposedPending.promise);
    disposed.dispose();
    disposedPending.resolve(page(conversationPage()));
    await settle();
    expect(disposed.textContent).toBe("");
  });

  it("keeps an index-page request authoritative when an old turn row is clicked during loading", async () => {
    const firstTurn = (conversationPage().turn_units as Record<string, unknown>[])[0];
    const secondTurn = {
      ...firstTurn,
      turn_unit_id: "turn-2",
      ordinal: 2,
      user_message: {
        ...(firstTurn?.user_message as Record<string, unknown>),
        revision_id: `sha256:${"6".repeat(64)}`,
        visible_excerpt: "第二页问题"
      }
    };
    const first = page(conversationPage({ total: 2, range_end: 1, next_cursor: "next-index" }));
    const next = page(conversationPage({
      total: 2,
      range_start: 1,
      range_end: 2,
      previous_cursor: "previous-index",
      turn_units: [secondTurn]
    }));
    const nextPending = deferred<ConversationPageV1>();
    const load = vi.fn((request: ConversationRequest) => {
      if (!request.turnUnitId && request.cursor === "next-index") return nextPending.promise;
      if (!request.turnUnitId) return Promise.resolve(first);
      return Promise.resolve(page(selectedConversationPage({
        turn_unit_id: request.turnUnitId,
        turn_units: [request.turnUnitId === "turn-2" ? secondTurn : firstTurn]
      })));
    });
    const root = renderConversation(identity, load);
    await settle();
    await settle();

    root.querySelector<HTMLButtonElement>('[data-action="next-turn-page"]')?.click();
    await settle();
    const oldRow = root.querySelector<HTMLButtonElement>('[data-turn-unit-id="turn-1"]');
    expect(oldRow?.disabled).toBe(true);
    oldRow?.click();
    nextPending.resolve(next);
    await settle();
    await settle();

    expect(root.textContent).toContain("第二页问题");
    expect(root.textContent).not.toContain("正在读取问答记录");
  });

	it("rejects supplied pages with another identity or mode before following their turns", async () => {
		for (const response of [conversationPage({ session_id: "session-other" }), selectedConversationPage()]) {
			const load = vi.fn().mockResolvedValue(page(response));
			const root = renderConversation(identity, load);
			await settle();
			expect(root.textContent).toContain("绑定不一致");
			expect(load).toHaveBeenCalledTimes(1);
			root.dispose();
		}
	});

	it("labels retained excerpts as unavailable full bodies", async () => {
		const retainedIndex = page(conversationPage({ body_availability: "retained_excerpt" }));
		const retainedDetail = page(selectedConversationPage({
			body_availability: "retained_excerpt",
			messages: (selectedConversationPage().messages as Record<string, unknown>[]).map((item) => ({ ...item, phase: null, text: null }))
		}));
		const root = renderConversation(identity, (request) => Promise.resolve(request.turnUnitId ? retainedDetail : retainedIndex));
		await settle();
		await settle();
		expect(root.textContent).toContain("认证摘录");
		expect(root.textContent).toContain("完整正文不可用");
	});

  it("rejects skipped and dependency-changed supplied pages while preserving the last authenticated page", async () => {
    const first = page(conversationPage({ total: 3, range_end: 1, next_cursor: "second" }));
    const skippedTurn = { ...(conversationPage().turn_units as Record<string, unknown>[])[0], turn_unit_id: "turn-3", ordinal: 3,
      user_message: { ...((conversationPage().turn_units as Record<string, unknown>[])[0].user_message as Record<string, unknown>), visible_excerpt: "SKIPPED_SECOND_PAGE" } };
    const skipped = page(conversationPage({ total: 3, range_start: 2, range_end: 3, previous_cursor: "second", turn_units: [skippedTurn] }));
    const changed = page(conversationPage({ total: 3, range_start: 1, range_end: 2, previous_cursor: "first", next_cursor: "third",
      dependency_digest: `sha256:${"9".repeat(64)}`, turn_units: [{ ...skippedTurn, turn_unit_id: "turn-2", ordinal: 2 }] }));
    for (const response of [skipped, changed]) {
      const load = vi.fn().mockResolvedValueOnce(first).mockResolvedValueOnce(page(selectedConversationPage())).mockResolvedValueOnce(response);
      const root = renderConversation(identity, load);
      await settle();
      await settle();
      root.querySelector<HTMLButtonElement>('[data-action="next-turn-page"]')!.click();
      await settle();
      expect(root.textContent).toContain("绑定不一致");
      expect(root.textContent).not.toContain("SKIPPED_SECOND_PAGE");
      root.dispose();
    }
  });

  it("rejects a selected response whose turn summary differs from the selected index row", async () => {
    const altered = page(selectedConversationPage({ turn_units: [{ ...(selectedConversationPage().turn_units as Record<string, unknown>[])[0], answer_state: "partial" }] }));
    const root = renderConversation(identity, (request) => Promise.resolve(request.turnUnitId ? altered : page(conversationPage())));
    await settle();
    await settle();
    expect(root.textContent).toContain("绑定不一致");
  });
});
