import type { ConversationPageV1, VisibleMessageV1, VisibleTurnV1 } from "../contracts/conversation-page";
import type { ConversationRequest } from "../cli/runner";
import { validateConversationPage } from "../data/conversation-page-validation";
import { button, element } from "./dom";

const PAGE_SIZE = 20;

type ConversationNavigation = {
  cursor: string;
  direction: "first" | "previous" | "next" | "last";
  sourceStart: number;
  sourceEnd: number;
  sourcePage: ConversationPageV1;
};

export type ConversationIdentity = Omit<ConversationRequest, "limit" | "turnUnitId" | "cursor" | "messageCursor">;
export type ConversationLoader = (request: ConversationRequest) => Promise<ConversationPageV1>;
export type ConversationElement = HTMLElement & {
  updateIdentity: (identity: ConversationIdentity) => void;
  dispose: () => void;
};

export interface ConversationViewOptions {
  turnUnitId?: string;
}

export function renderConversation(
  initialIdentity: ConversationIdentity,
  load: ConversationLoader,
  options: ConversationViewOptions = {}
): ConversationElement {
  const root = element("section", { className: "sr-conversation", attrs: { "aria-label": "问答记录" } }) as ConversationElement;
  const fixedTurnUnitId = options.turnUnitId;
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
  const focusAction = (): string | undefined => {
    const active = root.ownerDocument.activeElement;
    return active instanceof HTMLElement && root.contains(active) ? active.dataset.action : undefined;
  };
  const restoreFocus = (action: string | undefined): void => {
    if (!action) return;
    const target = root.querySelector<HTMLButtonElement>(`[data-action="${action}"]`);
    if (target && !target.disabled) target.focus();
    else root.querySelector<HTMLButtonElement>(".sr-conversation-navigation button:not(:disabled)")?.focus();
  };

  const loadIndex = async (navigation?: ConversationNavigation): Promise<void> => {
    const requestEpoch = ++epoch;
    const identityKey = key(identity);
    loadingIndex = true;
    loadingMessages = false;
    error = "";
    retry = () => { void loadIndex(navigation); };
    if (navigation === undefined) {
      messagePage = undefined;
      selectedTurn = undefined;
    }
    draw();
    try {
      const page = validateConversationPage(await load({ ...base(), limit: PAGE_SIZE, ...(navigation === undefined ? {} : { cursor: navigation.cursor }) }));
      if (!current(requestEpoch, identityKey)) return;
	  assertBoundPage(page, identity, "turn_index", navigation);
      indexPage = page;
      loadingIndex = false;
      retry = undefined;
      selectedTurn = page.turn_units[0];
      draw();
      if (selectedTurn) void loadMessages(selectedTurn);
    } catch (reason) {
      if (!current(requestEpoch, identityKey)) return;
      loadingIndex = false;
      error = safeMessage(reason);
      draw();
    }
  };

  const loadMessages = async (turn: VisibleTurnV1 | string, navigation?: ConversationNavigation): Promise<void> => {
    const requestEpoch = ++epoch;
    const identityKey = key(identity);
    const turnUnitId = typeof turn === "string" ? turn : turn.turn_unit_id;
    const acceptedTurn = typeof turn === "string" ? undefined : turn;
    const restoreAction = focusAction();
    if (acceptedTurn) selectedTurn = acceptedTurn;
    if (navigation === undefined) messagePage = undefined;
    loadingMessages = true;
    error = "";
    retry = () => { void loadMessages(turn, navigation); };
    draw();
    try {
      const page = validateConversationPage(await load({
        ...base(),
        limit: PAGE_SIZE,
        turnUnitId,
        ...(navigation === undefined ? {} : { messageCursor: navigation.cursor })
      }));
      if (!current(requestEpoch, identityKey) || (acceptedTurn !== undefined && selectedTurn?.turn_unit_id !== turnUnitId)) return;
	  assertBoundPage(page, identity, "turn_messages", navigation, turnUnitId, acceptedTurn);
      if (acceptedTurn === undefined) selectedTurn = page.turn_units[0];
      messagePage = page;
      loadingMessages = false;
      retry = undefined;
      draw();
      restoreFocus(restoreAction);
    } catch (reason) {
      if (!current(requestEpoch, identityKey) || (acceptedTurn !== undefined && selectedTurn?.turn_unit_id !== turnUnitId)) return;
      loadingMessages = false;
      error = safeMessage(reason);
      draw();
      restoreFocus(restoreAction);
    }
  };

  const draw = (): void => {
    if (disposed) return;
    const nodes: Node[] = [
      element("div", { className: "sr-conversation-heading" }, [
        element("h3", { text: "问答记录" }),
        element("p", { text: fixedTurnUnitId === undefined
          ? "仅显示用户与 Agent 的可见消息；与下方已索引执行事实分开。"
          : "仅显示本问答中用户与 Agent 的可见消息；不包含原始工具输出。" })
      ])
    ];
    if (fixedTurnUnitId !== undefined) {
      const statusText = loadingMessages ? "正在读取问答记录…" : error || (messagePage ? "已读取问答记录。" : "");
      nodes.push(element("p", { className: error ? "sr-conversation-error" : "sr-loading", text: statusText, attrs: { role: "status", "aria-live": "polite" } }));
    } else {
      if (loadingIndex) nodes.push(element("p", { className: "sr-loading", text: "正在读取问答记录…" }));
      if (error) nodes.push(element("p", { className: "sr-conversation-error", text: error }));
    }
    if (error) {
      const retryButton = button("重试", { "data-action": "retry-conversation-page" });
      retryButton.addEventListener("click", () => retry?.());
      nodes.push(retryButton);
    }
    if (indexPage) {
      nodes.push(renderCoverage(indexPage));
      nodes.push(renderNavigation(indexPage, "turn", (navigation) => { void loadIndex(navigation); }));
      const browser = element("div", { className: "sr-conversation-browser" });
      const list = element("div", { className: "sr-turn-list", attrs: { "aria-label": "用户问题" } });
      for (const turn of indexPage.turn_units) {
        const node = button("", { "data-turn-unit-id": turn.turn_unit_id, "aria-selected": String(turn.turn_unit_id === selectedTurn?.turn_unit_id) });
        node.disabled = loadingIndex;
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
    } else if (fixedTurnUnitId !== undefined && selectedTurn !== undefined) {
      nodes.push(renderCoverage(messagePage!));
      nodes.push(renderDetail(selectedTurn, messagePage, loadingMessages, (turn, cursor) => { void loadMessages(turn, cursor); }));
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
    if (fixedTurnUnitId === undefined) void loadIndex();
    else void loadMessages(fixedTurnUnitId);
  };
  root.dispose = () => {
    disposed = true;
    epoch += 1;
    indexPage = undefined;
    messagePage = undefined;
    selectedTurn = undefined;
    error = "";
    retry = undefined;
    root.replaceChildren();
  };
  draw();
  if (fixedTurnUnitId === undefined) void loadIndex();
  else void loadMessages(fixedTurnUnitId);
  return root;
}

function key(identity: ConversationIdentity): string {
  return [identity.projectId, identity.provider, identity.sessionId, identity.expectedGenerationId, identity.expectedSessionViewDigest, identity.sessionViewDigest ?? ""].join("\0");
}

function renderCoverage(page: ConversationPageV1): HTMLElement {
  const coverage = page.coverage;
	const capturedLabel = page.body_availability === "retained_excerpt" ? "保留摘录" : "已读取";
	const context = coverage.diagnostics_available === false ? "未知" : coverage.context_messages.toLocaleString("en-US");
  const section = element("div", { className: "sr-conversation-coverage" }, [
    element("p", { text: "可见消息 " + coverage.visible_messages.toLocaleString("en-US") + " · " + capturedLabel + " " + coverage.captured_messages.toLocaleString("en-US") + " · 上下文包装 " + context })
  ]);
	if (page.body_availability === "retained_excerpt") section.append(element("p", { className: "sr-coverage-warning", text: "原始消息正文当前不可用；下方显示扫描时保留的认证摘录，并非完整正文。" }));
	if (coverage.diagnostics_available === false) section.append(element("p", { className: "sr-coverage-warning", text: "历史保留记录未包含完整来源覆盖诊断；未知不等于零或完整。" }));
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
  navigate: (turn: VisibleTurnV1, navigation: ConversationNavigation) => void
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
	if ((page.action_total ?? 0) > 0 || (page.result_total ?? 0) > 0) {
		messages.append(element("p", { className: "sr-retained-evidence", text: `已认证执行证据：操作 ${(page.actions ?? []).length}/${page.action_total ?? 0}，结果 ${(page.results ?? []).length}/${page.result_total ?? 0}${page.evidence_truncated ? "；其余请在已索引执行事实中查看。" : "。"}` }));
	}
  if (page.messages.length === 0) messages.append(element("p", { className: "sr-empty", text: "当前分页没有可见消息。" }));
  detail.append(messages);
  return detail;
}

function renderMessage(message: VisibleMessageV1): HTMLElement {
  const article = element("article", { className: "sr-message sr-message-" + message.role });
  article.append(element("strong", { className: "sr-message-label", text: messageLabel(message) }));
	if (message.truncated && message.text !== null && !message.text_truncated) article.append(element("p", { className: "sr-truncation", text: "列表预览曾截断；下方为已读取正文。" }));
	article.append(element("pre", { className: "sr-message-body", text: message.text ?? message.visible_excerpt }));
	if (message.text === null) article.append(element("p", { className: "sr-truncation", text: "仅保留认证摘录；完整正文不可用。" }));
  if (message.text_truncated) article.append(element("p", { className: "sr-truncation", text: "正文已截断；超出单条消息读取上限的部分未显示。" }));
  return article;
}

function renderNavigation(page: ConversationPageV1, kind: "turn" | "message", load: (navigation: ConversationNavigation) => void): HTMLElement {
  const navigation = element("div", { className: "sr-conversation-navigation" });
  const definitions = [
    ["首页", "first-" + kind + "-page", page.first_cursor, "first"],
    ["上一页", "previous-" + kind + "-page", page.previous_cursor, "previous"],
    ["下一页", "next-" + kind + "-page", page.next_cursor, "next"],
    ["末页", "last-" + kind + "-page", page.last_cursor, "last"]
  ] as const;
  for (const [label, action, cursor, direction] of definitions) {
    const control = button(label, { "data-action": action });
    const alreadyThere = (action.startsWith("first-") && page.range_start === 0) || (action.startsWith("last-") && page.range_end === page.total);
    control.disabled = cursor === null || alreadyThere;
    control.addEventListener("click", () => { if (cursor !== null) load({ cursor, direction, sourceStart: page.range_start, sourceEnd: page.range_end, sourcePage: page }); });
    navigation.append(control);
  }
  navigation.append(element("span", { text: page.total === 0 ? "0 / 0" : (page.range_start + 1) + "–" + page.range_end + " / " + page.total }));
  return navigation;
}

function answerLabel(turn: VisibleTurnV1): string {
  return { no_answer: "尚无 Agent 回答", partial: "回答不完整", answered: "已回答（不代表已验证）" }[turn.answer_state];
}

function answerExplanation(turn: VisibleTurnV1): string {
  return {
    no_answer: "尚无 Agent 回答。",
	partial: "已捕获的 Agent 回答不完整；可能包含过程说明、截断或中断的最终回答，不表示问题已解决。",
    answered: "已记录 Agent 回答；不代表执行已验证或问题已解决。"
  }[turn.answer_state];
}

function assertBoundPage(page: ConversationPageV1, identity: ConversationIdentity, mode: "turn_index" | "turn_messages", navigation?: ConversationNavigation, turnUnitId?: string, selectedTurn?: VisibleTurnV1): void {
	const selectedViewDigest = identity.sessionViewDigest ?? identity.expectedSessionViewDigest;
	const expectedReaderVersion = identity.sessionViewDigest === undefined ? "0.4.0" : "0.4.3";
	if (page.project_id !== identity.projectId || page.provider !== identity.provider || page.session_id !== identity.sessionId ||
		page.generation_id !== identity.expectedGenerationId || page.session_view_digest !== selectedViewDigest ||
		page.minimum_reader_version !== expectedReaderVersion ||
		(identity.sessionViewDigest !== undefined && page.evidence_session_view_digest !== undefined && page.evidence_session_view_digest !== selectedViewDigest) || page.mode !== mode ||
		(mode === "turn_messages" && page.turn_unit_id !== turnUnitId) || (navigation === undefined && page.range_start !== 0) || page.range_end - page.range_start > PAGE_SIZE ||
		(navigation?.direction === "first" && page.range_start !== 0) || (navigation?.direction === "last" && page.range_end !== page.total) ||
		(navigation?.direction === "next" && page.range_start !== navigation.sourceEnd) || (navigation?.direction === "previous" && page.range_end !== navigation.sourceStart) ||
		(navigation !== undefined && !samePageDependency(page, navigation.sourcePage)) ||
		(selectedTurn !== undefined && (page.turn_units.length !== 1 || !sameTurnIdentity(page.turn_units[0], selectedTurn)))) {
		throw new Error("问答响应与当前 Session 绑定不一致。");
	}
}

function samePageDependency(left: ConversationPageV1, right: ConversationPageV1): boolean {
  return left.dependency_digest === right.dependency_digest && left.total === right.total && left.body_availability === right.body_availability &&
    left.evidence_session_view_digest === right.evidence_session_view_digest && sameCoverage(left.coverage, right.coverage);
}

function sameCoverage(left: ConversationPageV1["coverage"], right: ConversationPageV1["coverage"]): boolean {
  return left.source_records === right.source_records && left.visible_messages === right.visible_messages && left.captured_messages === right.captured_messages &&
    left.truncated_messages === right.truncated_messages && left.truncated_bodies === right.truncated_bodies && left.context_messages === right.context_messages &&
    left.orphan_messages === right.orphan_messages && left.oversized_records === right.oversized_records && left.malformed_records === right.malformed_records &&
    left.complete === right.complete && left.diagnostics_available === right.diagnostics_available;
}

function sameTurnIdentity(left: VisibleTurnV1, right: VisibleTurnV1): boolean {
  return left.turn_unit_id === right.turn_unit_id && left.ordinal === right.ordinal && left.started_at === right.started_at &&
    left.ended_at === right.ended_at && left.answer_state === right.answer_state && left.assistant_message_count === right.assistant_message_count &&
    left.action_count === right.action_count && left.result_count === right.result_count && sameMessageIdentity(left.user_message, right.user_message);
}

function sameMessageIdentity(left: VisibleMessageV1, right: VisibleMessageV1): boolean {
  return left.role === right.role && left.phase === right.phase && left.revision_id === right.revision_id && left.occurred_at === right.occurred_at &&
    left.visible_excerpt === right.visible_excerpt && left.truncated === right.truncated && left.source_ref.provider === right.source_ref.provider &&
    left.source_ref.session_id === right.source_ref.session_id && left.source_ref.source_identity === right.source_ref.source_identity &&
    left.source_ref.record_ordinal === right.source_ref.record_ordinal && left.source_ref.source_hash === right.source_ref.source_hash;
}

function messageLabel(message: VisibleMessageV1): string {
  if (message.role === "user") return "用户";
  if (message.phase === "commentary") return "Agent · 过程说明";
  if (message.phase === "final_answer") return "Agent · 最终回答";
  return "Agent · 回答";
}

function safeMessage(reason: unknown): string {
  if (reason instanceof Error && reason.message === "问答响应与当前 Session 绑定不一致。") return reason.message;
  const code = typeof reason === "object" && reason !== null && "code" in reason ? (reason as { code?: unknown }).code : undefined;
  return {
    source_unavailable: "问答来源暂不可用；现有执行事实仍可阅读。",
    retained_evidence_unavailable: "未找到与当前 Session 一致的保留问答证据。",
    retained_evidence_ambiguous: "找到多份无法自动区分的保留问答证据。",
    visible_reader_unsupported: "当前来源的问答读取器不可用；这不表示 Session 没有 Agent 回答。",
    generation_mismatch: "项目已更新；请刷新后重新读取问答。",
    stale_cursor: "问答分页已失效；请从首页重新读取。",
    unsupported_provider: "当前来源暂不支持问答读取。"
  }[typeof code === "string" ? code : ""] ?? "无法读取问答记录；请刷新项目后重试。";
}
