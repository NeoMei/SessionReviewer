import type { Snapshot } from "../data/repository";
import type { ConversationRequest, SessionEventRequest } from "../cli/runner";
import type { ConversationPageV1 } from "../contracts/conversation-page";
import type { SessionEventPageV1 } from "../contracts/review-v4";
import { element } from "./dom";
import type { V4ViewState } from "../state/v4-view-state";
import { renderV4Shell, type V4ShellElement } from "./render-v4-shell";
import type { ScanRecordsElement } from "./render-scan-records";

export type StatusTone = "success" | "warning" | "danger" | "neutral";

export interface PresentedStatus {
  label: string;
  tone: StatusTone;
}

const STATUSES: Record<string, PresentedStatus> = {
  accepted: { label: "已采纳", tone: "success" },
  at_risk: { label: "有风险", tone: "warning" },
  blocked: { label: "受阻", tone: "danger" },
  closed: { label: "已关闭", tone: "success" },
  open: { label: "待处理", tone: "warning" },
  rejected: { label: "未采纳", tone: "neutral" },
  verified: { label: "已验证", tone: "success" }
};

const STAGES: Record<string, string> = {
  main: "主线阶段"
};

const EVENT_KINDS: Record<string, string> = {
  accepted: "已采纳",
  decision: "决策",
  milestone: "里程碑",
  release: "发布",
  verified: "已验证"
};

export function presentStatus(value: string): PresentedStatus {
  return STATUSES[value.trim().toLocaleLowerCase()] ?? { label: value, tone: "neutral" };
}

export function presentStage(value: string): string {
  return STAGES[value.trim().toLocaleLowerCase()] ?? value;
}

export function presentEventKind(value: string): string {
  return EVENT_KINDS[value.trim().toLocaleLowerCase()] ?? value;
}

export function presentDateTime(value: string): string {
  const dateOnly = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (dateOnly) return `${dateOnly[1]}年${Number(dateOnly[2])}月${Number(dateOnly[3])}日`;
  const parsed = new Date(value);
  if (Number.isNaN(parsed.valueOf())) return value;
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false
  }).format(parsed);
}

export function summarizeRisk(detail: string, limit = 72): string {
  const normalized = detail.trim().replace(/^问题[：:]\s*/, "");
  const sentenceEnd = normalized.search(/[。！？\n]/);
  const firstSentence = sentenceEnd >= 0 ? normalized.slice(0, sentenceEnd) : normalized;
  if (firstSentence.length <= limit) return firstSentence;
  return `${firstSentence.slice(0, limit).trimEnd()}…`;
}

export function renderMarkdownV4View(
  snapshot: Extract<Snapshot, { kind: "markdown-v4" | "markdown-v4-stale" }>,
  open: (path: string) => void,
  options: {
    cliUnavailable?: boolean;
    loadSessionEvents?: (request: SessionEventRequest) => Promise<SessionEventPageV1>;
    loadConversation?: (request: ConversationRequest) => Promise<ConversationPageV1>;
    eventPageCache?: Map<string, SessionEventPageV1>;
    initialState?: unknown;
    saveState?: (state: V4ViewState) => void | Promise<void>;
  } = {}
): HTMLElement & { scanRecords?: ScanRecordsElement; dispose?: () => void } {
  const current = snapshot.kind === "markdown-v4-stale" ? snapshot.lastValid : snapshot;
  const state = current.state;
  const root = element("div", { className: "session-reviewer-browser sr-v4-readonly" });
  const status = snapshot.kind === "markdown-v4-stale"
    ? "已过期 · 只读"
    : state.kind === "pending_edit" ? "有未同步修改 · 只读"
      : state.kind === "public_valid" ? "待私有验证 · 只读" : "当前快照不可验证 · 只读";
  const trusted = state.kind === "public_valid" || state.kind === "pending_edit" ? state : undefined;
  if (!trusted) root.append(element("h1", { text: "暂时无法打开项目脉络" }));
  const trustDetails = element("details", { className: "sr-v4-trust-details" }, [
    element("summary", { text: "校验与只读说明" }),
    element("p", { text: "公开文件校验不等于私有接受证明；请在原生 Markdown 中阅读或编辑白名单正文。结构操作当前不可用。" }),
    element("p", { text: `project_id ${current.descriptor.projectId} · generation ${trusted?.value.presentation.generation_id ?? "未验证"} · revision ${trusted?.value.presentation.revision ?? "未验证"}` })
  ]);
  if (snapshot.kind === "markdown-v4-stale") {
    const reason = markdownSnapshotReason(snapshot.state);
    trustDetails.append(element("p", { className: "sr-v4-stale-reason", text: `当前快照拒绝原因：${reason}` }));
  } else if (state.kind === "invalid" || state.kind === "unverified" || state.kind === "read_failed") {
    const reason = markdownSnapshotReason(state);
    trustDetails.append(element("p", { className: "sr-v4-reason", text: `拒绝原因：${reason}` }));
  }
  if (options.cliUnavailable) trustDetails.append(element("p", { className: "sr-v4-cli-status", text: "CLI 不可用：只能原生阅读或编辑 Markdown，不能验证或同步。" }));
  let shell: V4ShellElement | undefined;
  if (trusted) {
    shell = renderV4Shell(current.descriptor, trusted.value.presentation, state.kind === "public_valid" ? state.index : undefined, trusted.ledger, open, { ...options, snapshotStatus: status, trustDetail: trustDetails });
    root.append(shell);
    Object.defineProperty(root, "scanRecords", { configurable: true, get: () => shell?.scanRecords });
    (root as HTMLElement & { dispose?: () => void }).dispose = () => shell?.dispose();
  } else root.append(element("p", { className: "sr-v4-status", text: status }), trustDetails);
  const actions = element("div", { className: "sr-v4-actions" });
  for (const [label, relative] of [["打开项目回顾", "项目回顾.md"], ["打开项目历史", "项目历史.md"]] as const) {
    const button = element("button", { text: label, attrs: { type: "button" } });
    button.addEventListener("click", () => open(`${current.descriptor.root}/${relative}`));
    actions.append(button);
  }
  root.append(actions);
  if (state.kind === "pending_edit") {
    root.append(element("p", { text: `检测到 ${state.value.changedFields.length} 个白名单字段修改；尚未同步，不会触发写入。` }));
  }
  return root;
}

function markdownSnapshotReason(state: Extract<Snapshot, { kind: "markdown-v4-stale" }>["state"] | Extract<Snapshot, { kind: "markdown-v4" }>["state"]): string {
  if (state.kind === "invalid") return state.code;
  if (state.kind === "unverified") return state.reason;
  if (state.kind === "read_failed") {
    const category = state.category === "missing" ? "文件缺失" : state.category === "permission-denied" ? "无权读取" : "读取失败";
    return `${state.document}：${category}`;
  }
  return "";
}
