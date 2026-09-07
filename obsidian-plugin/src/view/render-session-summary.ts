import type { SessionSummaryBlockV1, SessionSummaryErrorBlockV1, SessionSummaryEntryV1, SessionSummaryV1 } from "../contracts/review-v4";
import type { SessionSummaryRequest } from "../cli/runner";
import { button, element } from "./dom";

export type SessionSummaryElement = HTMLElement & {
  updateIdentity(request: SessionSummaryRequest): void;
  dispose(): void;
};

type SummaryBlock = SessionSummaryBlockV1 | SessionSummaryErrorBlockV1;

const BLOCKS: ReadonlyArray<readonly [keyof Pick<SessionSummaryV1,
  "phase_boundaries" | "key_operations" | "verification_results" | "errors" | "unresolved_questions">, string]> = [
  ["phase_boundaries", "阶段边界"],
  ["key_operations", "关键操作"],
  ["verification_results", "结果与验证"],
  ["errors", "错误"],
  ["unresolved_questions", "遗留问题"]
];

export function renderSessionSummary(
  request: SessionSummaryRequest,
  load: (request: SessionSummaryRequest) => Promise<SessionSummaryV1>
): SessionSummaryElement {
  const root = element("section", { className: "sr-session-summary", attrs: { "aria-label": "Session 保留摘要" } }) as SessionSummaryElement;
  let current = request;
  let currentKey = bindingKey(request);
  let epoch = 0;
  let disposed = false;

  const showLoading = (): void => {
    root.replaceChildren(
      element("h3", { text: "Session 摘要" }),
      element("p", { className: "sr-loading", text: "正在读取 Session 摘要…", attrs: { "aria-live": "polite" } })
    );
  };
  const showError = (): void => {
    const retry = button("重试", { "data-action": "retry-session-summary" });
    retry.addEventListener("click", () => { void startLoad(); });
    root.replaceChildren(
      element("h3", { text: "Session 摘要" }),
      element("p", { className: "sr-summary-error", text: "无法读取 Session 摘要；请刷新项目后重试，并确认 CLI 已更新。", attrs: { "aria-live": "polite" } }),
      retry
    );
  };
  const showSummary = (summary: SessionSummaryV1): void => {
    const body = element("div", { className: "sr-summary-blocks" });
    for (const [key, label] of BLOCKS) body.append(renderBlock(label, summary[key]));
    root.replaceChildren(element("h3", { text: "Session 摘要" }), body);
  };
  const startLoad = async (): Promise<void> => {
    if (disposed) return;
    const requested = current;
    const requestedKey = currentKey;
    const requestedEpoch = ++epoch;
    showLoading();
    try {
      const summary = await load(requested);
      if (disposed || requestedEpoch !== epoch || requestedKey !== currentKey) return;
      if (!matches(summary, requested)) throw new Error("Session summary binding mismatch");
      showSummary(summary);
    } catch {
      if (disposed || requestedEpoch !== epoch || requestedKey !== currentKey) return;
      showError();
    }
  };

  root.updateIdentity = (next) => {
    const nextKey = bindingKey(next);
    if (disposed || nextKey === currentKey) return;
    current = next;
    currentKey = nextKey;
    void startLoad();
  };
  root.dispose = () => {
    disposed = true;
    epoch += 1;
    root.replaceChildren();
  };
  void startLoad();
  return root;
}

function renderBlock(label: string, block: SummaryBlock): HTMLDetailsElement {
  const details = element("details", { className: "sr-summary-block" });
  const empty = block.items.length === 0 ? " · 没有捕获到匹配事实" : "";
  const disclosure = element("summary", { text: `${label} · 已返回 ${block.shown} / 共 ${block.total} · 未展示 ${block.omitted}${empty}` });
  enableKeyboardDisclosure(details, disclosure);
  const list = element("ol", { className: "sr-summary-items" });
  if (block.items.length === 0) list.append(element("li", { className: "sr-empty", text: "没有捕获到匹配事实。" }));
  else for (const item of block.items) list.append(renderEntry(item));
  details.append(disclosure, list);
  return details;
}

function renderEntry(item: SessionSummaryEntryV1): HTMLLIElement {
  const entry = element("li", { className: "sr-summary-item" });
  entry.append(
    element("p", { className: "sr-summary-item-meta", text: `${item.occurred_at} · 序列 ${item.sequence}` }),
    ...(isErrorEntry(item) ? [element("p", { className: "sr-summary-error-code", text: `错误代码：${item.code}` })] : []),
    element("p", { className: "sr-summary-item-text", text: item.text || "（该摘要条目没有可用摘录）" })
  );
  const sources = element("details", { className: "sr-summary-source" });
  const disclosure = element("summary", { text: `修订 ${item.revision_id} · 来源 ${item.source_revision_ids.length}` });
  enableKeyboardDisclosure(sources, disclosure);
  const list = element("ul");
  list.append(element("li", { text: item.revision_id }));
  for (const source of item.source_revision_ids) list.append(element("li", { text: source }));
  sources.append(disclosure, list);
  entry.append(sources);
  return entry;
}

function isErrorEntry(item: SessionSummaryEntryV1): item is SessionSummaryEntryV1 & { code: string } {
  return "code" in item && typeof item.code === "string";
}

function enableKeyboardDisclosure(details: HTMLDetailsElement, disclosure: HTMLElement): void {
  disclosure.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    details.open = !details.open;
  });
}

function bindingKey(request: SessionSummaryRequest): string {
  return [request.projectId, request.provider, request.sessionId, request.expectedGenerationId, request.expectedSessionViewDigest].join("\0");
}

function matches(summary: SessionSummaryV1, request: SessionSummaryRequest): boolean {
  return summary.project_id === request.projectId && summary.provider === request.provider && summary.session_id === request.sessionId &&
    summary.generation_id === request.expectedGenerationId && summary.session_view_digest === request.expectedSessionViewDigest;
}
