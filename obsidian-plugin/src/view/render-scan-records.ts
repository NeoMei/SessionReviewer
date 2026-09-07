import type { SessionEventPageV1, SessionIndexEntryV1, SessionIndexV1 } from "../contracts/review-v4";
import type { SessionEventRequest, SessionSummaryRequest } from "../cli/runner";
import { button, element } from "./dom";
import { renderConversation, type ConversationElement, type ConversationLoader } from "./render-conversation";
import { renderSessionSummary, type SessionSummaryElement } from "./render-session-summary";
import type { SessionSummaryV1 } from "../contracts/review-v4";
import {
  DEFAULT_SESSION_BROWSER_STATE,
  filterSessions,
  normalizeSessionBrowserState,
  validCalendarDate,
  type SessionBrowserState
} from "../state/session-browser-state";

const SESSION_PAGE_SIZE = 25;
const EVENT_PAGE_SIZE = 25;
const EMPTY_EXCERPT = "（该索引事件没有可用摘录）";

export interface ScanRecordsOptions {
  loadSessionEvents?: (request: SessionEventRequest) => Promise<SessionEventPageV1>;
  loadConversation?: ConversationLoader;
  loadSessionSummary?: (request: SessionSummaryRequest) => Promise<SessionSummaryV1>;
  cliUnavailable?: boolean;
  eventPageCache?: Map<string, SessionEventPageV1>;
  initialState?: unknown;
  onStateChange?: (state: SessionBrowserState) => void;
}

export type ScanRecordsElement = HTMLElement & { dispose: () => void };
type EventNavigation = { cursor?: string; anchor?: number };
type SessionHandlers = {
  onFilter: (patch: Partial<SessionBrowserState>) => void;
  onPage: (value: number) => void;
  onSelect: (session: SessionIndexEntryV1) => void;
};
type SessionRailElement = HTMLElement & {
  update: (sessions: SessionIndexEntryV1[], filtered: SessionIndexEntryV1[], selected: SessionIndexEntryV1 | undefined, state: SessionBrowserState, error: string) => void;
};

export function renderScanRecords(index: SessionIndexV1, options: ScanRecordsOptions = {}): ScanRecordsElement {
  const root = element("section", { className: "sr-scan-records", attrs: { "aria-label": "扫描记录" } }) as ScanRecordsElement;
  let state = normalizeSessionBrowserState(options.initialState);
  let filtered = filterSessions(index.sessions, state);
  let selected = findSelected(index.sessions, state.selected);
  let filterError = "";
  if (selected && filtered.includes(selected)) state.page = Math.floor(filtered.indexOf(selected) / SESSION_PAGE_SIZE);
  else {
    state.page = 0;
    selected = filtered[0];
    state.selected = selected ? sessionSelection(selected) : null;
  }
  let eventPage: SessionEventPageV1 | undefined;
  let selectedEvent = 0;
  let loading = false;
  let loadError = "";
  let retryNavigation: EventNavigation | undefined;
  let requestEpoch = 0;
  let disposed = false;
  const cache = options.eventPageCache ?? new Map<string, SessionEventPageV1>();
  const heading = element("div", { className: "sr-scan-heading" }, [
    element("h2", { text: "扫描记录" }),
    element("p", { text: presentIndexCoverage(index) })
  ]);
  const browser = element("div", { className: "sr-scan-browser" });
  let eventArea: HTMLElement | undefined;
  let conversation: ConversationElement | undefined;
  let conversationIdentity = "";
  let summary: SessionSummaryElement | undefined;
  let summaryIdentity = "";
  let sessionRail: SessionRailElement;

  const eligible = (session: SessionIndexEntryV1 | undefined): session is SessionIndexEntryV1 =>
    Boolean(session && session.source_availability === "available" && session.session_view_digest && session.indexed_event_count > 0);

  const requestFor = (session: SessionIndexEntryV1, navigation?: EventNavigation): SessionEventRequest => ({
    projectId: index.project_id,
    provider: session.provider,
    sessionId: session.session_id,
    expectedGenerationId: index.generation_id,
    expectedSessionViewDigest: session.session_view_digest!,
    limit: EVENT_PAGE_SIZE,
    ...navigation
  });

  const conversationFor = (session: SessionIndexEntryV1 | undefined): HTMLElement => {
    const available = Boolean(session && session.source_availability === "available" && session.session_view_digest && options.loadConversation);
    if (!available || !session || !session.session_view_digest || !options.loadConversation) {
      conversation?.dispose();
      conversation = undefined;
      conversationIdentity = "";
      return element("section", { className: "sr-conversation sr-conversation-unavailable", attrs: { "aria-label": "问答记录" } }, [
        element("h3", { text: "问答记录" }),
        element("p", { text: options.cliUnavailable || !options.loadConversation ? "无法读取问答记录：CLI 不可用或版本过旧。完整 Session 清单仍可浏览。" : "该 Session 的问答来源不可用。已索引执行事实仍可阅读。" })
      ]);
    }
    const identity = `${index.project_id}\0${session.provider}\0${session.session_id}\0${index.generation_id}\0${session.session_view_digest}`;
    const binding = {
      projectId: index.project_id,
      provider: session.provider,
      sessionId: session.session_id,
      expectedGenerationId: index.generation_id,
      expectedSessionViewDigest: session.session_view_digest
    };
    if (!conversation) conversation = renderConversation(binding, options.loadConversation);
    else if (conversationIdentity !== identity) conversation.updateIdentity(binding);
    conversationIdentity = identity;
    return conversation;
  };

  const summaryFor = (session: SessionIndexEntryV1 | undefined): HTMLElement | undefined => {
    if (!session?.session_view_digest) {
      summary?.dispose();
      summary = undefined;
      summaryIdentity = "";
      return element("section", { className: "sr-session-summary sr-summary-unavailable", attrs: { "aria-label": "Session 保留摘要" } }, [
        element("h3", { text: "Session 摘要" }),
        element("p", { text: "当前 Session 没有可验证的摘要绑定；未显示保留摘要。" })
      ]);
    }
    if (!options.loadSessionSummary) {
      summary?.dispose();
      summary = undefined;
      summaryIdentity = "";
      return element("section", { className: "sr-session-summary sr-summary-unavailable", attrs: { "aria-label": "Session 保留摘要" } }, [
        element("h3", { text: "Session 摘要" }),
        element("p", { text: "无法读取 Session 摘要：CLI 不可用或版本过旧。完整 Session 清单仍可浏览。" })
      ]);
    }
    const binding: SessionSummaryRequest = {
      projectId: index.project_id,
      provider: session.provider,
      sessionId: session.session_id,
      expectedGenerationId: index.generation_id,
      expectedSessionViewDigest: session.session_view_digest
    };
    const identity = sessionIdentity(session);
    if (!summary) summary = renderSessionSummary(binding, options.loadSessionSummary);
    else if (summaryIdentity !== identity) summary.updateIdentity(binding);
    summaryIdentity = identity;
    return summary;
  };

  const load = async (navigation?: EventNavigation): Promise<void> => {
    if (!eligible(selected) || !options.loadSessionEvents || disposed) return;
    const epoch = ++requestEpoch;
    const identity = sessionIdentity(selected);
    const cacheKey = `${identity}\0${navigation?.cursor ?? `anchor:${navigation?.anchor ?? "first"}`}`;
    const cached = cache.get(cacheKey);
    loading = !cached;
    loadError = "";
    retryNavigation = navigation;
    eventPage = cached;
    selectedEvent = 0;
    draw();
    if (cached) {
      retryNavigation = undefined;
      return;
    }
    try {
      const page = await options.loadSessionEvents(requestFor(selected, navigation));
      if (disposed || epoch !== requestEpoch || identity !== sessionIdentity(selected)) return;
      cache.set(cacheKey, page);
      eventPage = page;
      loading = false;
      retryNavigation = undefined;
      draw();
    } catch (error) {
      if (disposed || epoch !== requestEpoch || identity !== sessionIdentity(selected)) return;
      loading = false;
      eventPage = undefined;
      loadError = error instanceof Error ? error.message : "无法读取扫描 Session；请刷新项目后重试。";
      draw();
    }
  };

  const setSelected = (session: SessionIndexEntryV1 | undefined): boolean => {
    if (sessionIdentity(session) === sessionIdentity(selected)) return false;
    requestEpoch += 1;
    selected = session;
    eventPage = undefined;
    selectedEvent = 0;
    loading = false;
    loadError = "";
    return true;
  };

  const selectSession = (session: SessionIndexEntryV1): void => {
    const changed = setSelected(session);
    commitState({ ...state, selected: sessionSelection(session) });
    draw();
    if (changed) void load();
  };

  const draw = (): void => {
    if (disposed) return;
    sessionRail.update(index.sessions, filtered, selected, state, filterError);
    const nextEventArea = renderEventArea(selected, summaryFor(selected), conversationFor(selected), eventPage, selectedEvent, loading, loadError, retryNavigation, options, {
      onEvent: (value) => { selectedEvent = value; draw(); },
      onLoad: (navigation) => { void load(navigation); }
    });
    if (eventArea) eventArea.replaceWith(nextEventArea);
    else browser.append(nextEventArea);
    eventArea = nextEventArea;
  };

  root.dispose = () => {
    disposed = true;
    requestEpoch += 1;
    conversation?.dispose();
    conversation = undefined;
    conversationIdentity = "";
    summary?.dispose();
    summary = undefined;
    summaryIdentity = "";
    if (!options.eventPageCache) cache.clear();
    root.replaceChildren();
  };
  const commitState = (next: SessionBrowserState): void => {
    state = next;
    options.onStateChange?.(structuredClone(state));
  };
  const applyFilter = (patch: Partial<SessionBrowserState>): void => {
    const previous = state;
    const next = { ...state, ...patch };
    if (new TextEncoder().encode(next.query).byteLength > 256) {
      filterError = "搜索内容最多 256 个 UTF-8 字节。";
      sessionRail.update(index.sessions, filtered, selected, previous, filterError);
      return;
    }
    if (patch.unknownDateOnly === true) {
      next.dateFrom = null;
      next.dateTo = null;
    } else if ((patch.dateFrom !== undefined && patch.dateFrom !== null) || (patch.dateTo !== undefined && patch.dateTo !== null)) {
      next.unknownDateOnly = false;
    }
    if ((next.dateFrom !== null && !validCalendarDate(next.dateFrom)) || (next.dateTo !== null && !validCalendarDate(next.dateTo)) ||
      (next.dateFrom !== null && next.dateTo !== null && next.dateFrom > next.dateTo)) {
      filterError = "日期范围无效，请输入真实日期，且起始日期不能晚于结束日期。";
      sessionRail.update(index.sessions, filtered, selected, previous, filterError);
      return;
    }
    filterError = "";
    const nextFiltered = filterSessions(index.sessions, next);
    let selectionChanged = false;
    if (selected && nextFiltered.includes(selected)) {
      next.page = Math.floor(nextFiltered.indexOf(selected) / SESSION_PAGE_SIZE);
      next.selected = sessionSelection(selected);
    }
    else {
      next.page = 0;
      const fallback = nextFiltered[0];
      next.selected = fallback ? sessionSelection(fallback) : null;
      selectionChanged = setSelected(fallback);
    }
    filtered = nextFiltered;
    selected = findSelected(index.sessions, next.selected);
    commitState(next);
    draw();
    if (selectionChanged) void load();
  };
  sessionRail = renderSessions({
    onFilter: applyFilter,
    onPage: (value) => {
      const pageCount = Math.max(1, Math.ceil(filtered.length / SESSION_PAGE_SIZE));
      const page = Math.min(Math.max(0, value), pageCount - 1);
      const fallback = filtered[page * SESSION_PAGE_SIZE];
      const next = { ...state, page, selected: fallback ? sessionSelection(fallback) : null };
      const selectionChanged = setSelected(fallback);
      commitState(next);
      draw();
      if (selectionChanged) void load();
    },
    onSelect: selectSession
  });
  browser.append(sessionRail);
  root.append(heading, browser);
  draw();
  void load();
  return root;
}

function renderSessions(handlers: SessionHandlers): SessionRailElement {
  const rail = element("aside", { className: "sr-session-rail", attrs: { "aria-label": "扫描 Session" } }) as SessionRailElement;
  const search = element("input", { attrs: { type: "search", "aria-label": "搜索 Session", placeholder: "搜索 provider 或 Session ID", maxlength: "256" } });
  search.addEventListener("input", () => handlers.onFilter({ query: search.value }));
  const filters = element("details", { className: "sr-session-filters" });
  filters.append(element("summary", { text: "筛选" }));
  const provider = filterSelect("Provider", "筛选 Provider", [["", "全部 Provider"]]);
  const processing = filterSelect("处理状态", "筛选处理状态", [["", "全部状态"], ["complete", "完整"], ["partial", "部分"], ["error", "错误"], ["unprocessed", "未处理"]]);
  const availability = filterSelect("来源可用性", "筛选来源可用性", [["", "全部来源"], ["available", "可用"], ["unavailable", "不可用"]]);
  const dateFrom = labeledInput("开始日期", "起始日期", "date");
  const dateTo = labeledInput("结束日期", "结束日期", "date");
  const unknown = element("input", { attrs: { type: "checkbox", "aria-label": "仅未知日期" } });
  const unknownLabel = element("label", { className: "sr-session-filter-toggle" }, [unknown, element("span", { text: "仅未知日期" })]);
  const clear = button("清除筛选", { "data-action": "clear-session-filters" });
  const filterError = element("p", { className: "sr-session-filter-error", attrs: { role: "status", "aria-live": "polite" } });
  provider.control.addEventListener("change", () => handlers.onFilter({ provider: provider.control.value || null }));
  processing.control.addEventListener("change", () => handlers.onFilter({ processingState: processing.control.value as SessionBrowserState["processingState"] || null }));
  availability.control.addEventListener("change", () => handlers.onFilter({ sourceAvailability: availability.control.value as SessionBrowserState["sourceAvailability"] || null }));
  dateFrom.control.addEventListener("change", () => handlers.onFilter({ dateFrom: dateFrom.control.value || null }));
  dateTo.control.addEventListener("change", () => handlers.onFilter({ dateTo: dateTo.control.value || null }));
  unknown.addEventListener("change", () => handlers.onFilter({ unknownDateOnly: unknown.checked }));
  clear.addEventListener("click", () => handlers.onFilter({ ...DEFAULT_SESSION_BROWSER_STATE }));
  filters.append(provider.wrapper, processing.wrapper, availability.wrapper, dateFrom.wrapper, dateTo.wrapper, unknownLabel, clear, filterError);
  const list = element("div", { className: "sr-session-list" });
  const navigation = element("div", { className: "sr-session-navigation" });
  rail.append(search, filters, list, navigation);
  rail.update = (sessions, filtered, selected, state, error) => {
    if (search.value !== state.query) search.value = state.query;
    const providerValues = [...new Set(sessions.map((session) => session.provider))];
    provider.control.replaceChildren(element("option", { text: "全部 Provider", attrs: { value: "" } }));
    for (const value of providerValues) provider.control.append(element("option", { text: value, attrs: { value } }));
    provider.control.value = state.provider ?? "";
    processing.control.value = state.processingState ?? "";
    availability.control.value = state.sourceAvailability ?? "";
    dateFrom.control.value = state.dateFrom ?? "";
    dateTo.control.value = state.dateTo ?? "";
    unknown.checked = state.unknownDateOnly;
    filterError.textContent = error;
    const pageCount = Math.max(1, Math.ceil(filtered.length / SESSION_PAGE_SIZE));
    const safePage = Math.min(state.page, pageCount - 1);
    const shown = filtered.slice(safePage * SESSION_PAGE_SIZE, (safePage + 1) * SESSION_PAGE_SIZE);
    list.replaceChildren();
    for (const session of shown) {
      const node = button("", {
        "data-session-id": session.session_id,
        "aria-label": `${session.provider} / ${session.session_id}，${presentStartedAt(session.started_at)}，${presentProcessingState(session.processing_state)}，来源${session.source_availability === "available" ? "可用" : "不可用"}`,
        "aria-selected": String(sessionIdentity(session) === sessionIdentity(selected))
      });
      const label = element("strong", { className: "sr-session-label", text: `${session.provider} / ${session.session_id}` });
      label.title = `${session.provider} / ${session.session_id}`;
      node.append(label, element("span", { className: "sr-session-state", text: `${presentStartedAt(session.started_at)} · ${presentProcessingState(session.processing_state)} · 来源${session.source_availability === "available" ? "可用" : "不可用"}` }));
      node.addEventListener("click", () => handlers.onSelect(session));
      list.append(node);
    }
    if (shown.length === 0) list.append(element("p", { className: "sr-empty", text: "没有匹配的 Session。" }));
    const first = button("首页", { "data-action": "first-session-page" });
    first.disabled = safePage === 0;
    first.addEventListener("click", () => handlers.onPage(0));
    const previous = button("上一页", { "data-action": "previous-session-page" });
    previous.disabled = safePage === 0;
    previous.addEventListener("click", () => handlers.onPage(safePage - 1));
    const next = button("下一页", { "data-action": "next-session-page" });
    next.disabled = safePage + 1 >= pageCount;
    next.addEventListener("click", () => handlers.onPage(safePage + 1));
    const last = button("末页", { "data-action": "last-session-page" });
    last.disabled = safePage + 1 >= pageCount;
    last.addEventListener("click", () => handlers.onPage(pageCount - 1));
    const start = filtered.length === 0 ? 0 : safePage * SESSION_PAGE_SIZE + 1;
    const end = Math.min((safePage + 1) * SESSION_PAGE_SIZE, filtered.length);
    const omitted = Math.max(0, filtered.length - (end - start + (filtered.length === 0 ? 0 : 1)));
    navigation.replaceChildren(first, previous, element("span", { text: `${start}–${end} / 共${filtered.length} · 未展示 ${omitted}` }), next, last);
  };
  return rail;
}

function filterSelect(label: string, ariaLabel: string, options: Array<readonly [string, string]>): { wrapper: HTMLElement; control: HTMLSelectElement } {
  const control = element("select", { attrs: { "aria-label": ariaLabel } });
  for (const [value, text] of options) control.append(element("option", { text, attrs: { value } }));
  return { wrapper: element("label", { className: "sr-session-filter-field" }, [element("span", { text: label }), control]), control };
}

function labeledInput(label: string, ariaLabel: string, type: string): { wrapper: HTMLElement; control: HTMLInputElement } {
  const control = element("input", { attrs: { type, "aria-label": ariaLabel } });
  return { wrapper: element("label", { className: "sr-session-filter-field" }, [element("span", { text: label }), control]), control };
}

function renderEventArea(
  session: SessionIndexEntryV1 | undefined,
  summary: HTMLElement | undefined,
  conversation: HTMLElement,
  page: SessionEventPageV1 | undefined,
  selectedEvent: number,
  loading: boolean,
  loadError: string,
  retryNavigation: EventNavigation | undefined,
  options: ScanRecordsOptions,
  handlers: { onEvent: (value: number) => void; onLoad: (navigation?: EventNavigation) => void }
): HTMLElement {
  const area = element("div", { className: "sr-event-area" });
  if (!session) {
    area.append(element("p", { className: "sr-empty", text: "公开索引中没有 Session。" }));
    return area;
  }
  area.append(renderSessionCoverage(session));
  if (summary) area.append(summary);
  area.append(conversation);
  const facts = element("section", { className: "sr-execution-facts", attrs: { "aria-label": "已索引执行事实" } }, [element("h3", { text: "已索引执行事实" })]);
  area.append(facts);
  if (session.source_availability === "unavailable") {
    facts.append(element("p", { className: "sr-event-unavailable", text: "来源不可用；公开覆盖信息仍可阅读。" }));
  }
  if (options.cliUnavailable || !options.loadSessionEvents) {
    facts.append(element("p", { className: "sr-event-unavailable", text: "无法读取扫描记录：CLI 不可用。刷新项目或更新 CLI 后可重试。" }));
    return area;
  }
  if (session.indexed_event_count === 0 || session.session_view_digest === null) {
    facts.append(element("p", { className: "sr-empty", text: "这个 Session 没有可读的已索引事件。" }));
    return area;
  }
  if (loading) {
    facts.append(element("p", { className: "sr-loading", text: "正在读取已索引事件…" }));
    return area;
  }
  if (loadError) {
    const retry = button("重试", { "data-action": "retry-event-page" });
    retry.addEventListener("click", () => handlers.onLoad(retryNavigation));
    facts.append(element("p", { className: "sr-event-error", text: loadError }), retry);
    return area;
  }
  if (!page) return area;
  const content = element("div", { className: "sr-event-browser" });
  const list = element("div", { className: "sr-event-list", attrs: { "aria-label": "已索引事件" } });
  for (const [index, event] of page.items.entries()) {
    const ordinal = page.range_start + index + 1;
    const node = button("", { "data-event-ordinal": String(ordinal), "aria-selected": String(index === selectedEvent) });
    node.append(
      element("span", { className: "sr-event-meta", text: `${ordinal} · ${presentEventKind(event.kind)} · ${presentEventTime(event.occurred_at)}` }),
      element("span", { className: "sr-event-excerpt", text: event.excerpt || EMPTY_EXCERPT })
    );
    node.addEventListener("click", () => handlers.onEvent(index));
    list.append(node);
  }
  if (page.items.length === 0) list.append(element("p", { className: "sr-empty", text: "这个 Session 没有已索引事件。" }));
  const detail = element("section", { className: "sr-event-detail", attrs: { "aria-label": "事件详情" } });
  const event = page.items[selectedEvent];
  if (event) {
    detail.append(
      element("h3", { text: `事件 ${page.range_start + selectedEvent + 1} / ${page.total}` }),
      element("p", { text: `类型：${presentEventKind(event.kind)}` }),
      element("p", { text: `序列：${event.sequence}` }),
      element("p", { text: `时间：${presentEventTime(event.occurred_at)}` }),
      element("p", { text: `修订：${event.revision_id}` }),
      element("strong", { text: "索引摘录（非完整对话）" }),
      element("pre", { text: event.excerpt || EMPTY_EXCERPT })
    );
  }
  content.append(list, detail);
  facts.append(renderEventNavigation(page, handlers.onLoad), content);
  return area;
}

function renderEventNavigation(page: SessionEventPageV1, load: (navigation?: EventNavigation) => void): HTMLElement {
  const navigation = element("div", { className: "sr-event-navigation" });
  const definitions = [
    ["首页", "first-event-page", page.first_cursor],
    ["上一页", "previous-event-page", page.previous_cursor],
    ["下一页", "next-event-page", page.next_cursor],
    ["末页", "last-event-page", page.last_cursor]
  ] as const;
  for (const [label, action, cursor] of definitions) {
    const control = button(label, { "data-action": action });
    const alreadyThere = (action === "first-event-page" && page.range_start === 0) || (action === "last-event-page" && page.range_end === page.total);
    control.disabled = cursor === null || alreadyThere;
    control.addEventListener("click", () => { if (cursor !== null) load({ cursor }); });
    navigation.append(control);
  }
  const range = page.total === 0 ? "0 / 0" : `${page.range_start + 1}–${page.range_end} / ${page.total}`;
  navigation.append(element("span", { text: range }));
  return navigation;
}

function renderSessionCoverage(session: SessionIndexEntryV1): HTMLElement {
  const excluded = session.coverage.collapsed + session.coverage.unprojected + session.coverage.undecodable + session.coverage.truncated;
  const reasons = session.state_reason_codes.length > 0
    ? session.state_reason_codes.map((reason) => presentReason(reason)).join("；")
    : "无省略原因";
  return element("section", { className: "sr-session-coverage", attrs: { "aria-label": "Session 覆盖" } }, [
    element("h3", { text: `${session.provider} / ${session.session_id}` }),
    element("p", { text: `处理状态：${presentProcessingState(session.processing_state)} · 来源${session.source_availability === "available" ? "可用" : "不可用"}` }),
    element("p", { text: `记录 ${formatCount(session.record_count)} · 已见 ${formatCount(session.coverage.seen)} · 已索引 ${formatCount(session.coverage.indexed)} · 已排除 ${formatCount(excluded)}` }),
    element("p", { text: `折叠 ${formatCount(session.coverage.collapsed)} · 未投影 ${formatCount(session.coverage.unprojected)} · 未解码 ${formatCount(session.coverage.undecodable)} · 截断 ${formatCount(session.coverage.truncated)}` }),
    element("p", { text: `省略原因：${reasons}` })
  ]);
}

function presentIndexCoverage(index: SessionIndexV1): string {
  const coverage = index.coverage;
  const providers = [...new Set(index.sessions.map((session) => session.provider))]
    .map((provider) => `${provider} ${index.sessions.filter((session) => session.provider === provider).length}`)
    .join(" / ") || "无";
  const known = index.sessions.flatMap((session) => session.started_at === null ? [] : [session.started_at]).sort();
  const sourceRange = known.length === 0 ? "未知" : `${presentEventTime(known[0])} – ${presentEventTime(known.at(-1)!)}`;
  return `Session 覆盖：共 ${formatCount(coverage.total)} · 完整 ${formatCount(coverage.complete)} · 部分 ${formatCount(coverage.partial)} · 错误 ${formatCount(coverage.error)} · 未处理 ${formatCount(coverage.unprocessed)} · 来源不可用 ${formatCount(coverage.source_unavailable)} · 来源分布 ${providers} · Session 开始时间 ${sourceRange} · 时间未知 ${coverage.total - coverage.started_at_known} · 索引生成时间 ${presentEventTime(index.generated_at)}`;
}

function presentProcessingState(value: SessionIndexEntryV1["processing_state"]): string {
  return { complete: "完整", partial: "部分", error: "错误", unprocessed: "未处理" }[value];
}

function presentReason(value: SessionIndexEntryV1["state_reason_codes"][number]): string {
  const labels: Record<typeof value, string> = {
    not_discovered: "未发现", duplicate_candidate: "重复候选", freeze_terminal: "终止时冻结", malformed_source_records: "源记录格式错误",
    unsupported_source_records: "不支持的源记录", source_missing: "源缺失", source_unreadable: "源不可读", source_ambiguous: "源不明确",
    source_unsupported: "源不受支持", source_unavailable: "源不可用", partial_observations: "仅有部分观察", unprojected_facts: "未投影事实",
    undecodable_facts: "不可解码事实", scan_cancelled: "扫描已取消"
  };
  return `${labels[value]} (${value})`;
}

function presentEventKind(value: SessionEventPageV1["items"][number]["kind"]): string {
  const labels: Record<typeof value, string> = {
    message: "消息", tool_call: "工具调用", tool_result: "工具结果", cwd_change: "工作目录变更", usage: "用量", skip: "跳过",
    file_change: "文件变更", command: "命令", verification: "验证", error: "错误", artifact: "产物"
  };
  return labels[value];
}

function presentEventTime(value: string): string {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.valueOf())) return value;
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false
  }).format(parsed);
}

function presentStartedAt(value: string | null): string {
  return value === null ? "时间未知" : presentEventTime(value);
}

function formatCount(value: number | null): string {
  return value === null ? "未知" : value.toLocaleString("en-US");
}

function sessionIdentity(session: SessionIndexEntryV1 | undefined): string {
  return session ? `${session.provider}\0${session.session_id}` : "";
}

function sessionSelection(session: SessionIndexEntryV1): NonNullable<SessionBrowserState["selected"]> {
  return { provider: session.provider, sessionId: session.session_id };
}

function findSelected(sessions: SessionIndexEntryV1[], selected: SessionBrowserState["selected"]): SessionIndexEntryV1 | undefined {
  return selected === null ? undefined : sessions.find((session) => session.provider === selected.provider && session.session_id === selected.sessionId);
}
