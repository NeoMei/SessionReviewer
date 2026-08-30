import type { Diagnostic } from "../data/repository";
import { ACTIVE_REVIEW_STATES, type ReviewPhase, type ReviewState, type ReviewStatus } from "../cli/runner";
import { button, element } from "./dom";

export interface StatusPresentation {
  title: string;
  explanation: string;
  action?: string;
}

const PRESENTATIONS: Record<Diagnostic["code"], StatusPresentation> = {
  migration_required: { title: "项目需要迁移", explanation: "旧版记录仍在，尚未生成两份 v2 人类文档。", action: "先预览迁移" },
  content_conflict: { title: "两边修改了同一内容", explanation: "需要比较 Project 和 Obsidian 候选内容后显式选择。", action: "处理冲突" },
  machine_ledger_modified: { title: "机器账本被改动", explanation: "用量、哈希或证据账本不再可信。", action: "修复机器账本" },
  history_parse_failed: { title: "项目历史暂时无法解析", explanation: "页面仍显示上一次可信快照。", action: "打开项目历史" },
  review_parse_failed: { title: "项目回顾暂时无法解析", explanation: "页面仍显示上一次可信快照。", action: "打开项目回顾" },
  stale_snapshot: { title: "正在显示上次可信内容", explanation: "当前文件身份、引用或 revision 不一致。", action: "重新加载" },
  sync_not_run: { title: "等待同步到代码目录", explanation: "新的人类内容已显示，机器用量仍来自上次验收。", action: "查看同步状态" },
  cli_unavailable: { title: "尚未发现 SessionReviewer", explanation: "阅读和 Markdown 编辑仍可用；如需总结或同步，请先在 Agent 中运行一次 SessionReviewer。" }
};

export function statusPresentation(code: Diagnostic["code"]): StatusPresentation {
  return PRESENTATIONS[code];
}

export function renderStatusBanner(diagnostic: Diagnostic, action?: () => void): HTMLElement {
  const presentation = statusPresentation(diagnostic.code);
  const banner = element("section", { className: `sr-banner sr-banner-${diagnostic.code}`, attrs: { role: "status" } });
  banner.append(element("strong", { text: presentation.title }), element("p", { text: `${presentation.explanation} ${diagnostic.message}`.trim() }));
  if (presentation.action && action) {
    const control = button(presentation.action, { "data-status-action": diagnostic.code });
    control.addEventListener("click", action);
    banner.append(control);
  }
  return banner;
}

const REVIEW_PHASE_LABELS: Record<ReviewPhase, string> = {
  preflight: "正在检查",
  scanning: "正在扫描会话",
  preparing: "正在准备材料",
  reviewing: "正在总结",
  applying: "正在写入",
  syncing: "正在同步"
};

const REVIEW_FAILURE_TEXT: Record<string, string> = {
  E_AGENT_UNCONFIGURED: "未发现可用的 Codex，请先在 Agent 中运行一次 SessionReviewer。",
  E_AGENT_INCOMPATIBLE: "当前 Codex 版本暂不兼容自动总结。",
  E_AGENT_AUTH: "Codex 登录已失效，请先在终端重新登录。",
  E_AGENT_BUSY: "已有自动总结任务正在运行。",
  E_AGENT_TIMEOUT: "自动总结等待超时，可重试。",
  E_AGENT_TOOL_FORBIDDEN: "自动总结尝试调用工具，已安全停止。",
  E_AGENT_CANCELLED: "自动总结已取消。",
  E_PROPOSAL_REJECTED: "总结结果未通过校验，未写入项目。",
  E_SESSION_DISCOVERY: "项目 Session 扫描失败，请检查 Session 文件后重试。",
  E_APPLY_RECOVERY: "写入状态需要恢复，请重试。",
  E_SYNC_CONFLICT: "已总结，但同步存在冲突，请先处理冲突。",
  E_SYNC_PARTIAL: "已总结，但部分内容尚未同步。"
};

const REVIEW_BANNER_TITLES: Record<ReviewState, string> = {
  idle: "",
  queued: "自动总结进行中",
  running: "自动总结进行中",
  retrying: "自动总结进行中",
  cancel_requested: "正在取消自动总结",
  completed: "总结完成",
  failed: "自动总结失败",
  cancelled: "自动总结已取消"
};

export interface ReviewBannerActions {
  onCancel?: () => void;
  onRetry?: () => void;
  onSyncOnly?: () => void;
}

export function reviewPhaseLabel(status: Pick<ReviewStatus, "phase">): string {
  return status.phase ? REVIEW_PHASE_LABELS[status.phase] : "正在检查";
}

export function reviewFailureText(errorCode: string | undefined, fallback: string): string {
  return (errorCode !== undefined && REVIEW_FAILURE_TEXT[errorCode]) || fallback;
}

export function reviewActionLabel(status: ReviewStatus | undefined): string {
  if (status && ACTIVE_REVIEW_STATES.includes(status.state)) return reviewPhaseLabel(status);
  return "总结并同步";
}

export function renderReviewJobBanner(status: ReviewStatus, actions: ReviewBannerActions = {}): HTMLElement | null {
  if (status.state === "idle") return null;
  const banner = element("section", { className: "sr-review-banner", attrs: { role: "status", "data-review-state": status.state } });
  banner.append(element("strong", { text: REVIEW_BANNER_TITLES[status.state] }));
  banner.append(element("p", { className: "sr-review-meta", text: reviewBannerDetail(status) }));
  const controls: HTMLButtonElement[] = [];
  if (status.canCancel && actions.onCancel) controls.push(reviewControl("取消", "cancel", actions.onCancel));
  if (status.canRetry && actions.onRetry) controls.push(reviewControl("重试", "retry", actions.onRetry));
  if (status.canSyncOnly && actions.onSyncOnly) controls.push(reviewControl("仅同步已有修改", "sync-only", actions.onSyncOnly));
  if (controls.length > 0) {
    const group = element("div", { className: "sr-review-actions" });
    group.append(...controls);
    banner.append(group);
  }
  return banner;
}

function reviewBannerDetail(status: ReviewStatus): string {
  if (status.state === "completed") {
    return status.reviewUsage ? `本次总结使用 ${status.reviewUsage.totalTokens.toLocaleString("en-US")} tokens。` : "总结内容已写入项目回顾。";
  }
  if (status.state === "failed") return reviewFailureText(status.errorCode, "自动总结失败，可重试。");
  if (status.state === "cancelled") return reviewFailureText(status.errorCode, "自动总结已取消。");
  const phase = reviewPhaseLabel(status);
  return status.sessionCount > 0 ? `${phase} · ${status.sessionIndex} / ${status.sessionCount} 会话` : phase;
}

function reviewControl(label: string, action: string, handler: () => void): HTMLButtonElement {
  const control = button(label, { "data-review-action": action });
  control.addEventListener("click", handler);
  return control;
}
