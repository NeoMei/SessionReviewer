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
