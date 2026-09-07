import type { ConversationPageV1, VisibleMessageV1, VisibleTurnV1 } from "../contracts/conversation-page";
import type { ConversationRequest } from "../cli/runner";
import { button, element } from "./dom";

const PAGE_SIZE = 20;

export type ConversationIdentity = Omit<ConversationRequest, "limit" | "turnUnitId" | "cursor" | "messageCursor">;
export type ConversationLoader = (request: ConversationRequest) => Promise<ConversationPageV1>;
export type ConversationElement = HTMLElement & {
  updateIdentity: (identity: ConversationIdentity) => void;
  dispose: () => void;
};

export function renderConversation(initialIdentity: ConversationIdentity, load: ConversationLoader): ConversationElement {
  const root = element("section", { className: "sr-conversation", attrs: { "aria-label": "问答记录" } }) as ConversationElement;
  let identity = initialIdentity;
  let indexPage: ConversationPageV1 | undefined;
  let messagePage: ConversationPageV1 | undefined;
  let selectedTurn: VisibleTurnV1 | undefined;
  let loadingIndex = false;
  let loadingMessages = false;
  let error = "";
  let retry: (() => void) | undefined;
  let epoch = 0;
  let disposed = false;

  const base = (): ConversationIdentity => ({ ...identity });
  const current = (requestEpoch: number, identityKey: string): boolean =>
    !disposed && requestEpoch === epoch && identityKey === key(identity);

  const loadIndex = async (cursor?: string): Promise<void> => {
    const requestEpoch = ++epoch;
    const identityKey = key(identity);
    loadingIndex = true;
    loadingMessages = false;
    error = "";
    retry = () => { void loadIndex(cursor); };
    messagePage = undefined;
    selectedTurn = undefined;
    draw();
    try {
      const page = await load({ ...base(), limit: PAGE_SIZE, ...(cursor === undefined ? {} : { cursor }) });
      if (!current(requestEpoch, identityKey)) return;
      indexPage = page;
      loadingIndex = false;
      retry = undefined;
      selectedTurn = page.turn_units[0];
      draw();
      if (selectedTurn) void loadMessages(selectedTurn);
    } catch (reason) {
      if (!current(requestEpoch, identityKey)) return;
      indexPage = undefined;
      loadingIndex = false;
      error = safeMessage(reason);
      draw();
    }
  };

  const loadMessages = async (turn: VisibleTurnV1, messageCursor?: string): Promise<void> => {
    const requestEpoch = ++epoch;
    const identityKey = key(identity);
    selectedTurn = turn;
    messagePage = undefined;
    loadingMessages = true;
    error = "";
    retry = () => { void loadMessages(turn, messageCursor); };
    draw();
    try {
      const page = await load({
        ...base(),
        limit: PAGE_SIZE,
        turnUnitId: turn.turn_unit_id,
        ...(messageCursor === undefined ? {} : { messageCursor })
      });
      if (!current(requestEpoch, identityKey) || selectedTurn?.turn_unit_id !== turn.turn_unit_id) return;
      messagePage = page;
      loadingMessages = false;
      retry = undefined;
      draw();
    } catch (reason) {
      if (!current(requestEpoch, identityKey) || selectedTurn?.turn_unit_id !== turn.turn_unit_id) return;
      loadingMessages = false;
      error = safeMessage(reason);
      draw();
    }
  };

  const draw = (): void => {
    if (disposed) return;
    const nodes: Node[] = [
      element("div", { className: "sr-conversation-heading" }, [
        element("h3", { text: "问答记录" }),
        element("p", { text: "仅显示用户与 Agent 的可见消息；与下方已索引执行事实分开。" })
      ])
    ];
    if (loadingIndex) nodes.push(element("p", { className: "sr-loading", text: "正在读取问答记录…" }));
    if (error) {
      nodes.push(element("p", { className: "sr-conversation-error", text: error }));
      const retryButton = button("重试", { "data-action": "retry-conversation-page" });
      retryButton.addEventListener("click", () => retry?.());
      nodes.push(retryButton);
    }
    if (indexPage) {
      nodes.push(renderCoverage(indexPage));
      nodes.push(renderNavigation(indexPage, "turn", (cursor) => { void loadIndex(cursor); }));
      const browser = element("div", { className: "sr-conversation-browser" });
      const list = element("div", { className: "sr-turn-list", attrs: { "aria-label": "用户问题" } });
      for (const turn of indexPage.turn_units) {
        const node = button("", { "data-turn-unit-id": turn.turn_unit_id, "aria-selected": String(turn.turn_unit_id === selectedTurn?.turn_unit_id) });
        node.append(
          element("span", { className: "sr-turn-meta", text: "第 " + turn.ordinal + " 个用户问题 · " + answerLabel(turn) }),
          element("span", { className: "sr-turn-excerpt", text: turn.user_message.visible_excerpt || "（问题文本为空）" })
        );
        if (turn.user_message.truncated) node.append(element("span", { className: "sr-truncation", text: "问题预览已截断；选择后读取正文。" }));
        node.addEventListener("click", () => { void loadMessages(turn); });
        list.append(node);
      }
      if (indexPage.turn_units.length === 0) list.append(element("p", { className: "sr-empty", text: "这个 Session 没有可见的用户问题。" }));
      browser.append(list, renderDetail(selectedTurn, messagePage, loadingMessages, (turn, cursor) => { void loadMessages(turn, cursor); }));
      nodes.push(browser);
    }
    root.replaceChildren(...nodes);
  };

  root.updateIdentity = (nextIdentity) => {
    if (key(nextIdentity) === key(identity)) return;
    epoch += 1;
    identity = nextIdentity;
    indexPage = undefined;
    messagePage = undefined;
    selectedTurn = undefined;
    loadingIndex = false;
    loadingMessages = false;
    error = "";
    retry = undefined;
    void loadIndex();
  };
  root.dispose = () => {
    disposed = true;
    epoch += 1;
    root.replaceChildren();
  };
  draw();
  void loadIndex();
  return root;
}

function key(identity: ConversationIdentity): string {
  return [identity.projectId, identity.provider, identity.sessionId, identity.expectedGenerationId, identity.expectedSessionViewDigest].join("\0");
}

function renderCoverage(page: ConversationPageV1): HTMLElement {
  const coverage = page.coverage;
  const section = element("div", { className: "sr-conversation-coverage" }, [
    element("p", { text: "可见消息 " + coverage.visible_messages.toLocaleString("en-US") + " · 已读取 " + coverage.captured_messages.toLocaleString("en-US") + " · 上下文包装 " + coverage.context_messages.toLocaleString("en-US") })
  ]);
  if (!coverage.complete) section.append(element("p", { className: "sr-coverage-warning", text: "问答分组覆盖不完整，不应据此断定没有其他可见内容。" }));
  if (coverage.oversized_records > 0) {
    section.append(element("p", { className: "sr-coverage-warning", text: coverage.oversized_records.toLocaleString("en-US") + " 条超限源记录被省略；其角色未知，不表示缺少同数量的 Agent 回答。" }));
  }
  if (coverage.malformed_records > 0) section.append(element("p", { className: "sr-coverage-warning", text: coverage.malformed_records.toLocaleString("en-US") + " 条源记录格式错误。" }));
  if (coverage.truncated_bodies > 0) section.append(element("p", { className: "sr-coverage-warning", text: coverage.truncated_bodies.toLocaleString("en-US") + " 条消息正文超出读取上限，已明确截断。" }));
  return section;
}

function renderDetail(
  turn: VisibleTurnV1 | undefined,
  page: ConversationPageV1 | undefined,
  loading: boolean,
  navigate: (turn: VisibleTurnV1, cursor: string) => void
): HTMLElement {
  const detail = element("section", { className: "sr-conversation-detail", attrs: { "aria-label": "问答详情" } });
  if (!turn) {
    detail.append(element("p", { className: "sr-empty", text: "选择一个用户问题查看完整可见消息。" }));
    return detail;
  }
  detail.append(element("h4", { text: "第 " + turn.ordinal + " 个用户问题" }), element("p", { className: "sr-answer-state", text: answerExplanation(turn) }));
  if (loading) {
    detail.append(element("p", { className: "sr-loading", text: "正在读取问题与 Agent 消息…" }));
    return detail;
  }
  if (!page) return detail;
  detail.append(renderNavigation(page, "message", (cursor) => navigate(turn, cursor)));
  const messages = element("div", { className: "sr-message-list" });
  for (const message of page.messages) messages.append(renderMessage(message));
  if (page.messages.length === 0) messages.append(element("p", { className: "sr-empty", text: "当前分页没有可见消息。" }));
  detail.append(messages);
  return detail;
}

function renderMessage(message: VisibleMessageV1): HTMLElement {
  const article = element("article", { className: "sr-message sr-message-" + message.role });
  article.append(element("strong", { className: "sr-message-label", text: messageLabel(message) }));
  if (message.truncated && !message.text_truncated) article.append(element("p", { className: "sr-truncation", text: "列表预览曾截断；下方为已读取正文。" }));
  article.append(element("pre", { className: "sr-message-body", text: message.text ?? message.visible_excerpt }));
  if (message.text_truncated) article.append(element("p", { className: "sr-truncation", text: "正文已截断；超出单条消息读取上限的部分未显示。" }));
  return article;
}

function renderNavigation(page: ConversationPageV1, kind: "turn" | "message", load: (cursor: string) => void): HTMLElement {
  const navigation = element("div", { className: "sr-conversation-navigation" });
  const definitions = [
    ["首页", "first-" + kind + "-page", page.first_cursor],
    ["上一页", "previous-" + kind + "-page", page.previous_cursor],
    ["下一页", "next-" + kind + "-page", page.next_cursor],
    ["末页", "last-" + kind + "-page", page.last_cursor]
  ] as const;
  for (const [label, action, cursor] of definitions) {
    const control = button(label, { "data-action": action });
    const alreadyThere = (action.startsWith("first-") && page.range_start === 0) || (action.startsWith("last-") && page.range_end === page.total);
    control.disabled = cursor === null || alreadyThere;
    control.addEventListener("click", () => { if (cursor !== null) load(cursor); });
    navigation.append(control);
  }
  navigation.append(element("span", { text: page.total === 0 ? "0 / 0" : (page.range_start + 1) + "–" + page.range_end + " / " + page.total }));
  return navigation;
}

function answerLabel(turn: VisibleTurnV1): string {
  return { no_answer: "尚无 Agent 回答", partial: "仅有过程说明", answered: "已回答（不代表已验证）" }[turn.answer_state];
}

function answerExplanation(turn: VisibleTurnV1): string {
  return {
    no_answer: "尚无 Agent 回答。",
    partial: "仅有过程说明，尚无最终回答；不表示问题已解决。",
    answered: "已记录 Agent 回答；不代表执行已验证或问题已解决。"
  }[turn.answer_state];
}

function messageLabel(message: VisibleMessageV1): string {
  if (message.role === "user") return "用户";
  if (message.phase === "commentary") return "Agent · 过程说明";
  if (message.phase === "final_answer") return "Agent · 最终回答";
  return "Agent · 回答";
}

function safeMessage(reason: unknown): string {
  return reason instanceof Error && reason.message.length <= 512 ? reason.message : "无法读取问答记录；请刷新项目后重试。";
}
