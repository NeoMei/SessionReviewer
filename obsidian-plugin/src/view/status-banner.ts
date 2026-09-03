import type { Diagnostic } from "../data/repository";
import type { ScanStatus } from "../contracts/review-v3";
import { button, element } from "./dom";

export interface StatusPresentation {
  title: string;
  explanation: string;
  action?: string;
}

const PRESENTATIONS: Record<Diagnostic["code"], StatusPresentation> = {
  migration_required: { title: "项目需要迁移", explanation: "旧版记录仍在，尚未生成人类文档。", action: "先预览迁移" },
  content_conflict: { title: "两边修改了同一内容", explanation: "需要比较 Project 和 Obsidian 候选内容后显式选择。", action: "处理冲突" },
  machine_ledger_modified: { title: "机器账本被改动", explanation: "用量、哈希或证据账本不再可信。", action: "修复机器账本" },
  history_parse_failed: { title: "项目历史暂时无法解析", explanation: "页面仍显示上一次可信快照。", action: "打开项目历史" },
  review_parse_failed: { title: "项目回顾暂时无法解析", explanation: "页面仍显示上一次可信快照。", action: "打开项目回顾" },
  stale_snapshot: { title: "正在显示上次可信内容", explanation: "当前文件身份、引用或 revision 不一致。", action: "重新加载" },
  sync_not_run: { title: "等待同步到代码目录", explanation: "新的人类内容已显示，机器用量仍来自上次验收。", action: "立即同步" },
  cli_unavailable: { title: "尚未发现 SessionReviewer", explanation: "阅读和 Markdown 编辑仍可用；如需更新脉络或同步，请确保已安装 SessionReviewer。" }
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

const SCAN_PHASE_LABELS: Record<string, string> = {
  discovering: "正在发现会话",
  extracting: "正在提取脉络",
  reducing: "正在整合结构",
  rendering: "正在渲染文档",
  syncing: "正在同步"
};

const REVIEW_FAILURE_TEXT: Record<string, string> = {
  E_PROPOSAL_UNSAFE_INPUT: "Session 中仍含未清理的敏感信息，已安全停止，未写入项目。",
  E_SESSION_SEGMENT_CONFLICT: "发现同一 Session 的重复或冲突分段，请检查 Session 文件后重试。",
  E_APPLY_RECOVERY: "写入状态需要恢复，请重试。",
  E_SYNC_CONFLICT: "已更新，但同步存在冲突，请先处理冲突。",
  E_SYNC_PARTIAL: "已更新，但部分内容尚未同步。"
};

export function scanActionLabel(status: ScanStatus | undefined): string {
  if (status && (status.state === "queued" || status.state === "running")) {
    return status.phase ? (SCAN_PHASE_LABELS[status.phase] ?? "正在更新") : "正在更新";
  }
  return "更新项目脉络";
}

export const reviewActionLabel = scanActionLabel;

export function renderScanJobBanner(status: ScanStatus, actions: { onCancel?: () => void; onRetry?: () => void; onSyncOnly?: () => void } = {}): HTMLElement | null {
  if (!status.state || status.state === "completed") return null;
  const banner = element("section", { className: "sr-review-banner", attrs: { role: "status", "data-scan-state": status.state } });
  let title = "项目脉络更新中";
  let detail = status.phase ? (SCAN_PHASE_LABELS[status.phase] ?? status.phase) : "正在处理";
  if ((status.state === "queued" || status.state === "running") && status.session_count > 0) {
    detail += ` · 已处理 ${status.indexed_count}/${status.session_count}`;
    if (status.issue_count > 0) detail += ` · ${status.issue_count} 需检查`;
  }
  if (status.state === "completed_with_issues") {
    title = "项目脉络已更新（部分需检查）";
    detail = `共 ${status.session_count} 个 Session · ${status.indexed_count} 已索引 · ${status.issue_count} 需检查`;
  } else if (status.state === "failed") {
    title = "项目脉络更新失败";
    detail = status.error_message || (status.error_code ? reviewFailureText(status.error_code, `错误：${status.error_code}`) : "扫描或同步遇到错误，可重试。");
  }
  banner.append(element("strong", { text: title }));
  banner.append(element("p", { className: "sr-review-meta", text: detail }));
  const actionGroup = element("div", { className: "sr-review-actions" });
  if (actions.onCancel) {
    const cancel = button("取消", { "data-review-action": "cancel" });
    cancel.addEventListener("click", actions.onCancel);
    actionGroup.append(cancel);
  }
  if (actions.onRetry) {
    const retry = button("重试", { "data-review-action": "retry" });
    retry.addEventListener("click", actions.onRetry);
    actionGroup.append(retry);
  }
  banner.append(actionGroup);
  return banner;
}

export function renderReviewJobBanner(status: ScanStatus, actions: { onCancel?: () => void; onRetry?: () => void; onSyncOnly?: () => void } = {}): HTMLElement | null {
  return renderScanJobBanner(status, actions);
}

export function reviewFailureText(errorCode: string | undefined, fallback: string): string {
  return (errorCode !== undefined && REVIEW_FAILURE_TEXT[errorCode]) || (errorCode ? `错误：${errorCode}` : fallback);
}
