import type { SessionEventPageV1, SessionIndexEntryV1, SessionIndexV1 } from "../contracts/review-v4";
import type { SessionEventRequest } from "../cli/runner";
import { button, element } from "./dom";

const SESSION_PAGE_SIZE = 25;
const EVENT_PAGE_SIZE = 25;
const EMPTY_EXCERPT = "（该索引事件没有可用摘录）";

export interface ScanRecordsOptions {
  loadSessionEvents?: (request: SessionEventRequest) => Promise<SessionEventPageV1>;
  cliUnavailable?: boolean;
  eventPageCache?: Map<string, SessionEventPageV1>;
}

export type ScanRecordsElement = HTMLElement & { dispose: () => void };
type EventNavigation = { cursor?: string; anchor?: number };
type SessionHandlers = {
  onQuery: (value: string) => void;
  onPage: (value: number) => void;
  onSelect: (session: SessionIndexEntryV1) => void;
};
type SessionRailElement = HTMLElement & {
  update: (sessions: SessionIndexEntryV1[], selected: SessionIndexEntryV1 | undefined, query: string, page: number) => void;
};

export function renderScanRecords(index: SessionIndexV1, options: ScanRecordsOptions = {}): ScanRecordsElement {
  const root = element("section", { className: "sr-scan-records", attrs: { "aria-label": "扫描记录" } }) as ScanRecordsElement;
  let query = "";
  let sessionPage = 0;
  let selected = index.sessions[0];
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

  const selectSession = (session: SessionIndexEntryV1): void => {
    requestEpoch += 1;
    selected = session;
    eventPage = undefined;
    selectedEvent = 0;
    loading = false;
    loadError = "";
    draw();
    void load();
  };

  const draw = (): void => {
    if (disposed) return;
    sessionRail.update(index.sessions, selected, query, sessionPage);
    const nextEventArea = renderEventArea(selected, eventPage, selectedEvent, loading, loadError, retryNavigation, options, {
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
    if (!options.eventPageCache) cache.clear();
    root.replaceChildren();
  };
  sessionRail = renderSessions({
    onQuery: (value) => { query = value; sessionPage = 0; draw(); },
    onPage: (value) => { sessionPage = value; draw(); },
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
  const search = element("input", { attrs: { type: "search", "aria-label": "搜索 Session", placeholder: "搜索 provider 或 Session ID" } });
  search.addEventListener("input", () => handlers.onQuery(search.value));
  const list = element("div", { className: "sr-session-list" });
  const navigation = element("div", { className: "sr-session-navigation" });
  rail.append(search, list, navigation);
  rail.update = (sessions, selected, query, page) => {
    if (search.value !== query) search.value = query;
    const normalized = query.trim().toLocaleLowerCase();
    const filtered = normalized
      ? sessions.filter((session) => `${session.provider} ${session.session_id}`.toLocaleLowerCase().includes(normalized))
      : sessions;
    const pageCount = Math.max(1, Math.ceil(filtered.length / SESSION_PAGE_SIZE));
    const safePage = Math.min(page, pageCount - 1);
    const shown = filtered.slice(safePage * SESSION_PAGE_SIZE, (safePage + 1) * SESSION_PAGE_SIZE);
    list.replaceChildren();
    for (const session of shown) {
      const node = button("", {
        "data-session-id": session.session_id,
        "aria-selected": String(sessionIdentity(session) === sessionIdentity(selected))
      });
      const label = element("strong", { className: "sr-session-label", text: `${session.provider} / ${session.session_id}` });
      label.title = `${session.provider} / ${session.session_id}`;
      node.append(label, element("span", { className: "sr-session-state", text: presentProcessingState(session.processing_state) }));
      node.addEventListener("click", () => handlers.onSelect(session));
      list.append(node);
    }
    if (shown.length === 0) list.append(element("p", { className: "sr-empty", text: "没有匹配的 Session。" }));
    const previous = button("上一页", { "data-action": "previous-session-page" });
    previous.disabled = safePage === 0;
    previous.addEventListener("click", () => handlers.onPage(safePage - 1));
    const next = button("下一页", { "data-action": "next-session-page" });
    next.disabled = safePage + 1 >= pageCount;
    next.addEventListener("click", () => handlers.onPage(safePage + 1));
    navigation.replaceChildren(previous, element("span", { text: `${safePage + 1} / ${pageCount}` }), next);
  };
  return rail;
}

function renderEventArea(
  session: SessionIndexEntryV1 | undefined,
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
  if (session.source_availability === "unavailable") {
    area.append(element("p", { className: "sr-event-unavailable", text: "来源不可用；公开覆盖信息仍可阅读。" }));
  }
  if (options.cliUnavailable || !options.loadSessionEvents) {
    area.append(element("p", { className: "sr-event-unavailable", text: "无法读取扫描记录：CLI 不可用。刷新项目或更新 CLI 后可重试。" }));
    return area;
  }
  if (session.indexed_event_count === 0 || session.session_view_digest === null) {
    area.append(element("p", { className: "sr-empty", text: "这个 Session 没有可读的已索引事件。" }));
    return area;
  }
  if (loading) {
    area.append(element("p", { className: "sr-loading", text: "正在读取已索引事件…" }));
    return area;
  }
  if (loadError) {
    const retry = button("重试", { "data-action": "retry-event-page" });
    retry.addEventListener("click", () => handlers.onLoad(retryNavigation));
    area.append(element("p", { className: "sr-event-error", text: loadError }), retry);
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
  area.append(renderEventNavigation(page, handlers.onLoad), content);
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
  return `Session 覆盖：共 ${formatCount(coverage.total)} · 完整 ${formatCount(coverage.complete)} · 部分 ${formatCount(coverage.partial)} · 错误 ${formatCount(coverage.error)} · 未处理 ${formatCount(coverage.unprocessed)} · 来源不可用 ${formatCount(coverage.source_unavailable)}`;
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

function formatCount(value: number | null): string {
  return value === null ? "未知" : value.toLocaleString("en-US");
}

function sessionIdentity(session: SessionIndexEntryV1 | undefined): string {
  return session ? `${session.provider}\0${session.session_id}` : "";
}
