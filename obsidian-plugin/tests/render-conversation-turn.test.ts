import { describe, expect, it, vi } from "vitest";
import type { ConversationPageV1 } from "../src/contracts/conversation-page";
import type { ConversationRequest } from "../src/cli/runner";
import { parseConversationPageV1 } from "../src/data/conversation-page";
import { validateConversationPage } from "../src/data/conversation-page-validation";
import { renderConversation, type ConversationIdentity } from "../src/view/render-conversation";
import { conversationPage, selectedConversationPage, visibleMessage, visibleTurn } from "./fixtures/conversation";

const viewDigest = `sha256:${"1".repeat(64)}`;
const otherViewDigest = `sha256:${"8".repeat(64)}`;
const identity: ConversationIdentity = {
  projectId: "project-p",
  provider: "codex",
  sessionId: "session-1",
  expectedGenerationId: "generation-1",
  expectedSessionViewDigest: viewDigest
};

function parsed(overrides: Record<string, unknown> = {}): ConversationPageV1 {
  return parseConversationPageV1(JSON.stringify(selectedConversationPage(overrides)));
}

function asPage(value: Record<string, unknown>): ConversationPageV1 {
  return value as unknown as ConversationPageV1;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

function message(
  role: "user" | "assistant",
  index: number,
  overrides: Record<string, unknown> = {}
): Record<string, unknown> {
  const second = String(index % 60).padStart(2, "0");
  return visibleMessage(role, {
    revision_id: `sha256:${index.toString(16).padStart(64, "0")}`,
    source_ref: {
      provider: "codex",
      session_id: "session-1",
      source_identity: "source-1",
      record_ordinal: index + 1,
      source_hash: index.toString(16).padStart(64, "0")
    },
    occurred_at: `2026-09-07T00:00:${second}Z`,
    visible_excerpt: role === "user" ? "目标问题" : `回答 ${index}`,
    text: role === "user" ? "目标问题" : `回答正文 ${index}`,
    ...overrides
  });
}

function pagedTurn(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  const user = message("user", 0, { text: null });
  return visibleTurn({
    user_message: user,
    assistant_message_count: 20,
    ...overrides
  });
}

function fixedPage(
  rangeStart: number,
  rangeEnd: number,
  overrides: Record<string, unknown> = {}
): ConversationPageV1 {
  const all = [message("user", 0), ...Array.from({ length: 20 }, (_, index) => message("assistant", index + 1))];
  return parsed({
    total: 21,
    range_start: rangeStart,
    range_end: rangeEnd,
    first_cursor: "first-message",
    previous_cursor: rangeStart > 0 ? "previous-message" : null,
    next_cursor: rangeEnd < 21 ? "next-message" : null,
    last_cursor: "last-message",
    turn_units: [pagedTurn()],
    messages: all.slice(rangeStart, rangeEnd),
    coverage: {
      source_records: 21,
      visible_messages: 21,
      captured_messages: 21,
      truncated_messages: 0,
      truncated_bodies: 0,
      context_messages: 0,
      orphan_messages: 0,
      oversized_records: 0,
      malformed_records: 0,
      complete: true
    },
    ...overrides
  });
}

describe("fixed-turn conversation viewer", () => {
  it("requests only the exact turn and renders the complete authenticated answer", async () => {
    const calls: ConversationRequest[] = [];
    const unrelatedIndex = conversationPage({
      turn_units: [visibleTurn({ user_message: visibleMessage("user", { visible_excerpt: "无关的第一问" }) })]
    });
    const root = renderConversation(identity, async (request) => {
      calls.push(request);
      return parseConversationPageV1(JSON.stringify(request.turnUnitId === "turn-1" ? selectedConversationPage() : unrelatedIndex));
    }, { turnUnitId: "turn-1" });

    await settle();

    expect(calls).toEqual([{ ...identity, limit: 20, turnUnitId: "turn-1" }]);
    expect(root.textContent).toContain("完整最终回答\n第二行");
    expect(root.textContent).not.toContain("无关的第一问");
    expect(root.textContent).toContain("仅显示本问答中用户与 Agent 的可见消息；不包含原始工具输出。");
    expect(root.textContent).not.toContain("与下方已索引执行事实分开");
    expect(root.querySelector(".sr-turn-list")).toBeNull();
  });

  it("preserves UTF-8 byte boundaries in the browser-safe object validator", () => {
    const validBody = selectedConversationPage({
      messages: [visibleMessage("user", { text: "如何恢复可见问答？" }), visibleMessage("assistant", { text: "汉".repeat(21_845) + "a" })]
    });
    expect(() => validateConversationPage(validBody)).not.toThrow();
    const oversizedBody = selectedConversationPage({
      messages: [visibleMessage("user", { text: "如何恢复可见问答？" }), visibleMessage("assistant", { text: "汉".repeat(21_845) + "ab" })]
    });
    expect(() => validateConversationPage(oversizedBody)).toThrow(/bounded text/);

    const validCursor = { ...fixedPage(0, 20), next_cursor: "汉".repeat(1_365) + "a" };
    expect(() => validateConversationPage(validCursor)).not.toThrow();
    expect(() => validateConversationPage({ ...validCursor, next_cursor: "汉".repeat(1_365) + "ab" })).toThrow(/bounded cursor/);
  });

  it("preserves commentary/final ordering and distinguishes full text from retained excerpts", async () => {
    const commentary = message("assistant", 1, { phase: "commentary", visible_excerpt: "过程摘录", text: "过程全文" });
    const final = message("assistant", 2, { phase: "final_answer", visible_excerpt: "仅摘录", truncated: true, text: "完整最终回答\n第二行" });
    const full = parsed({
      total: 3,
      range_end: 3,
      messages: [message("user", 0), commentary, final],
      turn_units: [visibleTurn({ user_message: message("user", 0, { text: null }), assistant_message_count: 2 })],
      coverage: { ...selectedConversationPage().coverage as Record<string, unknown>, source_records: 3, visible_messages: 3, captured_messages: 3 }
    });
    const root = renderConversation(identity, () => Promise.resolve(full), { turnUnitId: "turn-1" });

    await settle();

    const labels = [...root.querySelectorAll(".sr-message-label")].map((node) => node.textContent);
    expect(labels).toEqual(["用户", "Agent · 过程说明", "Agent · 最终回答"]);
    expect(root.textContent).toContain("列表预览曾截断；下方为已读取正文。");
    expect(root.textContent).not.toContain("仅保留认证摘录；完整正文不可用。");

    const retained = selectedConversationPage({
      body_availability: "retained_excerpt",
      messages: (selectedConversationPage().messages as Record<string, unknown>[]).map((item) => ({ ...item, phase: null, text: null }))
    });
    const retainedRoot = renderConversation(identity, () => Promise.resolve(parseConversationPageV1(JSON.stringify(retained))), { turnUnitId: "turn-1" });
    await settle();
    expect(retainedRoot.textContent).toContain("仅保留认证摘录；完整正文不可用。");
    expect(retainedRoot.textContent).not.toContain("列表预览曾截断；下方为已读取正文。");
  });

  const providerPage = selectedConversationPage({ provider: "claude" });
  for (const turn of providerPage.turn_units as Record<string, unknown>[]) {
    const user = turn.user_message as Record<string, unknown>;
    user.source_ref = { ...(user.source_ref as Record<string, unknown>), provider: "claude" };
  }
  providerPage.messages = (providerPage.messages as Record<string, unknown>[]).map((item) => ({
    ...item,
    source_ref: { ...(item.source_ref as Record<string, unknown>), provider: "claude" }
  }));
  const sessionPage = selectedConversationPage({ session_id: "session-other" });
  for (const turn of sessionPage.turn_units as Record<string, unknown>[]) {
    const user = turn.user_message as Record<string, unknown>;
    user.source_ref = { ...(user.source_ref as Record<string, unknown>), session_id: "session-other" };
  }
  sessionPage.messages = (sessionPage.messages as Record<string, unknown>[]).map((item) => ({
    ...item,
    source_ref: { ...(item.source_ref as Record<string, unknown>), session_id: "session-other" }
  }));

  it.each([
    ["project", selectedConversationPage({ project_id: "project-other" })],
    ["provider", providerPage],
    ["Session", sessionPage],
    ["generation", selectedConversationPage({ generation_id: "generation-other" })],
    ["selected view", selectedConversationPage({ session_view_digest: otherViewDigest })],
    ["mode", conversationPage()],
    ["turn", selectedConversationPage({ turn_unit_id: "turn-other", turn_units: [visibleTurn({ turn_unit_id: "turn-other" })] })]
  ])("rejects a first response bound to the wrong %s", async (_label, response) => {
    const validResponse = parseConversationPageV1(JSON.stringify(response));
    const root = renderConversation(identity, () => Promise.resolve(validResponse), { turnUnitId: "turn-1" });
    await settle();
    expect(root.textContent).toContain("问答响应与当前 Session 绑定不一致。");
    expect(root.textContent).not.toContain("完整最终回答");
  });

  it("rejects a structurally valid page larger than the requested page size", async () => {
    const messages = [message("user", 0), ...Array.from({ length: 20 }, (_, index) => message("assistant", index + 1))];
    const response = parsed({
      total: 21,
      range_end: 21,
      turn_units: [pagedTurn()],
      messages,
      coverage: { ...selectedConversationPage().coverage as Record<string, unknown>, source_records: 21, visible_messages: 21, captured_messages: 21 }
    });
    const root = renderConversation(identity, () => Promise.resolve(response), { turnUnitId: "turn-1" });
    await settle();
    expect(root.textContent).toContain("问答响应与当前 Session 绑定不一致。");
    expect(root.querySelector(".sr-message-list")).toBeNull();
  });

  it.each([
    ["invalid cursor topology", selectedConversationPage({ next_cursor: "unexpected-next" })],
    ["more than the structural array limit", selectedConversationPage({ messages: Array.from({ length: 65 }, (_, index) => message(index === 0 ? "user" : "assistant", index)) })],
    ["a multibyte body over its UTF-8 limit", selectedConversationPage({
      messages: [
        visibleMessage("user", { text: "如何恢复可见问答？" }),
        visibleMessage("assistant", { text: "汉".repeat(21_846) })
      ]
    })]
  ])("rejects %s before rendering it", async (_label, response) => {
    expect(() => validateConversationPage(selectedConversationPage())).not.toThrow();
    const root = renderConversation(identity, () => Promise.resolve(asPage(response)), { turnUnitId: "turn-1" });
    await settle();
    expect(root.textContent).toContain("无法读取问答记录；请刷新项目后重试。");
    expect(root.querySelector(".sr-message-list")).toBeNull();
  });

  it.each([
    ["dependency", { dependency_digest: `sha256:${"9".repeat(64)}` }],
    ["coverage", { coverage: { source_records: 22, visible_messages: 21, captured_messages: 21, truncated_messages: 0, truncated_bodies: 0, context_messages: 0, orphan_messages: 0, oversized_records: 1, malformed_records: 0, complete: false } }],
    ["turn metadata", { turn_units: [pagedTurn({ answer_state: "partial" })] }]
  ])("rejects changed %s on pagination and retains the accepted page", async (_label, overrides) => {
    const first = fixedPage(0, 20);
    const changed = fixedPage(20, 21, overrides);
    const load = vi.fn().mockResolvedValueOnce(first).mockResolvedValueOnce(changed);
    const root = renderConversation(identity, load, { turnUnitId: "turn-1" });
    await settle();
    root.querySelector<HTMLButtonElement>('[data-action="next-message-page"]')!.click();
    await settle();
    expect(root.textContent).toContain("问答响应与当前 Session 绑定不一致。");
    expect(root.textContent).toContain("回答正文 19");
    expect(root.textContent).not.toContain("回答正文 20");
  });

  it("rejects skipped next and previous pages while retaining the accepted page", async () => {
    const first = fixedPage(0, 20);
    const skippedNext = asPage({ ...fixedPage(20, 21), range_start: 19, previous_cursor: "previous-message", messages: [message("assistant", 20), message("assistant", 21)] });
    const nextRoot = renderConversation(identity, vi.fn().mockResolvedValueOnce(first).mockResolvedValueOnce(skippedNext), { turnUnitId: "turn-1" });
    await settle();
    nextRoot.querySelector<HTMLButtonElement>('[data-action="next-message-page"]')!.click();
    await settle();
    expect(nextRoot.textContent).toContain("绑定不一致");
    expect(nextRoot.textContent).toContain("回答正文 19");

    const last = fixedPage(20, 21);
    const skippedPrevious = asPage({ ...fixedPage(0, 20), range_end: 19, next_cursor: "next-message", messages: [message("user", 0), ...Array.from({ length: 18 }, (_, index) => message("assistant", index + 1))] });
    const previousRoot = renderConversation(identity, vi.fn().mockResolvedValueOnce(first).mockResolvedValueOnce(last).mockResolvedValueOnce(skippedPrevious), { turnUnitId: "turn-1" });
    await settle();
    previousRoot.querySelector<HTMLButtonElement>('[data-action="next-message-page"]')!.click();
    await settle();
    previousRoot.querySelector<HTMLButtonElement>('[data-action="previous-message-page"]')!.click();
    await settle();
    expect(previousRoot.textContent).toContain("绑定不一致");
    expect(previousRoot.textContent).toContain("回答正文 20");
  });

  it("uses exact first/last cursors and preserves navigation focus", async () => {
    const first = fixedPage(0, 20);
    const last = fixedPage(20, 21);
    const load = vi.fn().mockResolvedValueOnce(first).mockResolvedValueOnce(last).mockResolvedValueOnce(first);
    const root = renderConversation(identity, load, { turnUnitId: "turn-1" });
    document.body.append(root);
    await settle();

    const lastButton = root.querySelector<HTMLButtonElement>('[data-action="last-message-page"]')!;
    lastButton.focus();
    lastButton.click();
    await settle();
    expect(load.mock.calls[1]?.[0]).toEqual({ ...identity, limit: 20, turnUnitId: "turn-1", messageCursor: "last-message" });
    expect(document.activeElement).toBe(root.querySelector('[data-action="first-message-page"]'));

    const firstButton = root.querySelector<HTMLButtonElement>('[data-action="first-message-page"]')!;
    firstButton.focus();
    firstButton.click();
    await settle();
    expect(load.mock.calls[2]?.[0]).toEqual({ ...identity, limit: 20, turnUnitId: "turn-1", messageCursor: "first-message" });
    expect(document.activeElement).toBe(root.querySelector('[data-action="next-message-page"]'));
    root.dispose();
  });

  it("shows one polite status, retries the same failed request, and preserves retry focus", async () => {
    const privateFailure = () => Object.assign(new Error("private stdout /Users/person/source"), { code: "conversation_failed" });
    const load = vi.fn()
      .mockRejectedValueOnce(privateFailure())
      .mockRejectedValueOnce(privateFailure())
      .mockResolvedValueOnce(parsed());
    const root = renderConversation(identity, load, { turnUnitId: "turn-1" });
    document.body.append(root);
    await settle();

    expect(root.querySelectorAll('[role="status"][aria-live="polite"]')).toHaveLength(1);
    expect(root.textContent).toContain("无法读取问答记录；请刷新项目后重试。");
    expect(root.textContent).not.toContain("/Users/person/source");
    const retry = root.querySelector<HTMLButtonElement>('[data-action="retry-conversation-page"]')!;
    retry.focus();
    retry.click();
    await settle();
    const replacementRetry = root.querySelector<HTMLButtonElement>('[data-action="retry-conversation-page"]')!;
    expect(document.activeElement).toBe(replacementRetry);
    replacementRetry.click();
    await settle();
    expect(load.mock.calls).toEqual([
      [{ ...identity, limit: 20, turnUnitId: "turn-1" }],
      [{ ...identity, limit: 20, turnUnitId: "turn-1" }],
      [{ ...identity, limit: 20, turnUnitId: "turn-1" }]
    ]);
    expect(root.querySelectorAll('[role="status"][aria-live="polite"]')).toHaveLength(1);
    expect(root.querySelector('[role="status"]')?.textContent).toBe("已读取问答记录。");
    root.dispose();
  });

  it("keeps the fixed turn across identity changes and ignores both stale and disposed responses", async () => {
    const oldResponse = deferred<ConversationPageV1>();
    const newResponse = deferred<ConversationPageV1>();
    const load = vi.fn((request: ConversationRequest) => request.sessionId === "session-1" ? oldResponse.promise : newResponse.promise);
    const root = renderConversation(identity, load, { turnUnitId: "turn-1" });
    root.updateIdentity({ ...identity, sessionId: "session-2" });
    oldResponse.resolve(parsed());
    const nextPage = selectedConversationPage({ session_id: "session-2" });
    const nextTurn = (nextPage.turn_units as Record<string, unknown>[])[0];
    nextTurn.user_message = { ...(nextTurn.user_message as Record<string, unknown>), source_ref: { ...((nextTurn.user_message as Record<string, unknown>).source_ref as Record<string, unknown>), session_id: "session-2" } };
    nextPage.messages = (nextPage.messages as Record<string, unknown>[]).map((item) => ({ ...item, source_ref: { ...(item.source_ref as Record<string, unknown>), session_id: "session-2" }, text: item.role === "assistant" ? "CURRENT_FIXED_ANSWER" : item.text }));
    newResponse.resolve(parseConversationPageV1(JSON.stringify(nextPage)));
    await settle();

    expect(load.mock.calls.map(([request]) => request)).toEqual([
      { ...identity, limit: 20, turnUnitId: "turn-1" },
      { ...identity, sessionId: "session-2", limit: 20, turnUnitId: "turn-1" }
    ]);
    expect(root.textContent).toContain("CURRENT_FIXED_ANSWER");
    expect(root.textContent).not.toContain("完整最终回答");

    const disposedResponse = deferred<ConversationPageV1>();
    const disposed = renderConversation(identity, () => disposedResponse.promise, { turnUnitId: "turn-1" });
    disposed.dispose();
    disposedResponse.resolve(parsed());
    await settle();
    expect(disposed.textContent).toBe("");
  });
});
