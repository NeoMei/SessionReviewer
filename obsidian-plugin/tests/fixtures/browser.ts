import type { BrowserModel } from "../../src/contracts/review-v2";

export function browserModelFixture(): BrowserModel {
  return {
    review: {
      projectId: "project-0123456789abcdef",
      revision: 2,
      name: "SessionReviewer",
      goal: "让项目上下文真正可恢复。",
      stage: "v2 收尾",
      status: "新项目总结页正在验收。",
      nextAction: "完成 Obsidian 真实交互验收。",
      lastVerification: "npm test 已通过。",
      risks: [{ id: "risk-ui", title: "UI 验收", status: "进行中", detail: "需要真实 Vault 验证。" }],
      decisions: [
        { id: "decision-local-cli", occurredAt: "2026-08-25", title: "Skill + 本地 CLI", rationale: "原始会话不上传。", impact: "人机信息分层。", status: "已采用" },
        { id: "decision-wide-browser", occurredAt: "2026-08-26", title: "宽版演进浏览器", rationale: "窄屏堆叠信息难读。", impact: "节点与详情联动。", status: "已采用" }
      ],
      fields: []
    },
    events: [
      {
        id: "timeline-release", occurredAt: "2026-08-27", kind: "发布", title: "v0.1.0 公开发布", meaning: "从本地工具进入可安装产品。",
        summary: "完成首个发布包。", why: "核心信任链已稳定。", changes: ["生成 CLI 发布包"], results: ["校验和通过"], decisionIds: ["decision-wide-browser"], next: "验收项目浏览器。"
      },
      {
        id: "timeline-trust-chain", occurredAt: "2026-08-25", kind: "里程碑", title: "信任链与 dry-run 边界修复", meaning: "从能运行进入可放心发布。",
        summary: "修复 receipt 信任边界。", why: "真实 Vault 暴露了边界。", changes: ["receipt 纳入可信状态"], results: ["重复 dry-run 为零变更"], decisionIds: ["decision-local-cli"], next: "验证安装器权限。"
      }
    ],
    accounting: {
      totalDurationMs: 180000,
      totalTokens: 350,
      totalCostUsd: 0.0038,
      pricingComplete: true,
      models: [{ model: "gpt-test", totalTokens: 350, totalCostUsd: 0.0038, tokenSharePct: 100, costSharePct: 100 }]
    },
    sessions: [{
      id: "session-report-a", projectId: "project-0123456789abcdef", sessionId: "session-a", previousSessionId: "", nextSessionId: "",
      accounting: {
        startedAt: "2026-08-25T07:00:00Z", endedAt: "2026-08-25T07:03:00Z", durationMs: 180000, totalTokens: 350, totalCostUsd: 0.0038, pricingComplete: true,
        models: [{ model: "gpt-test", inputTokens: 200, cachedInputTokens: 0, cacheWriteInputTokens: 0, outputTokens: 150, reasoningOutputTokens: 0, totalTokens: 350, costUsd: 0.0038,
          pricing: { currency: "USD", inputPerMillion: 4, cachedInputPerMillion: 0.4, cacheWriteInputPerMillion: 5, outputPerMillion: 20, source: "https://example.com/pricing", asOf: "2026-08-25" } }]
      }
    }],
    lastSuccessfulSync: "2026-08-27T08:00:00Z",
    source: { reviewPath: "Projects/A/项目回顾.md", historyPath: "Projects/A/项目历史.md", ledgerPath: "Projects/A/.session-reviewer/ledger.json", reviewText: "", historyText: "", reviewSha256: "a".repeat(64), historySha256: "b".repeat(64) }
  };
}
