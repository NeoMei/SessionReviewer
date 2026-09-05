import type { Snapshot } from "../data/repository";
import { element } from "./dom";

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
  options: { cliUnavailable?: boolean } = {}
): HTMLElement {
  const current = snapshot.kind === "markdown-v4-stale" ? snapshot.lastValid : snapshot;
  const state = current.state;
  const root = element("div", { className: "session-reviewer-browser sr-v4-readonly" });
  root.append(element("h1", { text: "项目脉络（v4 Markdown）" }));
  const status = snapshot.kind === "markdown-v4-stale"
    ? "已过期 · 只读"
    : state.kind === "pending_edit" ? "有未同步修改 · 只读"
      : state.kind === "public_valid" ? "待私有验证 · 只读" : "当前快照不可验证 · 只读";
  root.append(element("p", { className: "sr-v4-status", text: status }));
  if (snapshot.kind === "markdown-v4-stale") {
    const reason = snapshot.state.kind === "invalid" ? snapshot.state.code : snapshot.state.reason;
    root.append(element("p", { className: "sr-v4-stale-reason", text: `当前快照拒绝原因：${reason}` }));
  } else if (state.kind === "invalid" || state.kind === "unverified") {
    const reason = state.kind === "invalid" ? state.code : state.reason;
    root.append(element("p", { className: "sr-v4-reason", text: `拒绝原因：${reason}` }));
  }
  root.append(element("p", { text: "公开文件校验不等于私有接受证明；请在原生 Markdown 中阅读或编辑白名单正文。结构操作当前不可用。" }));
  if (options.cliUnavailable) root.append(element("p", { className: "sr-v4-cli-status", text: "CLI 不可用：只能原生阅读或编辑 Markdown，不能验证或同步。" }));
  const presentation = state.kind === "public_valid" || state.kind === "pending_edit" ? state.value.presentation : undefined;
  if (presentation) {
    const summary = element("section", { className: "sr-v4-summary" }, [
      element("h2", { text: "当前状态" }),
      element("p", { text: `目标：${presentation.current_state.goal}` }),
      element("p", { text: `阶段：${presentation.current_state.stage}` }),
      element("p", { text: `状态：${presentation.current_state.status}` }),
      element("p", { text: `下一步：${presentation.current_state.next_action}` })
    ]);
    const milestone = presentation.timeline.at(-1);
    if (milestone) {
      const verification = milestone.closed_loop.verification;
      const details = [
        element("h2", { text: "最近里程碑" }),
        element("h3", { text: milestone.title }),
        element("p", { text: milestone.summary }),
        element("p", { text: `结论：${milestone.closed_loop.conclusion.text || "未记录"}` }),
        element("p", { text: `验证状态：${verification.state}` })
      ];
      if (verification.text) details.push(element("p", { text: `验证文本：${verification.text}` }));
      if (verification.missing_reason) details.push(element("p", { text: `验证缺失原因：${verification.missing_reason}` }));
      for (const ref of verification.source_turn_refs) details.push(element("p", { text: `验证引用：${ref.provider}/${ref.session_id}#${ref.turn_unit_id}` }));
      summary.append(element("section", { className: "sr-v4-milestone" }, details));
    }
    root.append(summary);
  }
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
