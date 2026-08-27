import type { Diagnostic } from "../data/repository";
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
  cli_unavailable: { title: "未连接 SessionReviewer CLI", explanation: "阅读和 Markdown 编辑仍可用，同步与冲突处理需要配置 CLI。", action: "配置 CLI" }
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
