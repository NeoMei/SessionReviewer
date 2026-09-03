export type ViewKind = "evolution" | "decisions" | "usage";

export interface SourceRange {
  start: number;
  end: number;
}

export interface EditableField {
  document: "review" | "history";
  unitId: string;
  field: EditableFieldName;
  value: string | string[];
  range: SourceRange;
}

export type EditableFieldName =
  | "goal"
  | "stage"
  | "status"
  | "next_action"
  | "risk.title"
  | "risk.status"
  | "risk.detail"
  | "decision.title"
  | "decision.rationale"
  | "decision.impact"
  | "event.title"
  | "event.meaning"
  | "event.summary"
  | "event.why"
  | "event.changes"
  | "event.results"
  | "event.next";

export interface RiskModel {
  id: string;
  title: string;
  status: string;
  detail: string;
}

export interface DecisionModel {
  id: string;
  occurredAt: string;
  title: string;
  rationale: string;
  impact: string;
  status: string;
}

export interface ReviewModel {
  projectId: string;
  revision: number;
  name: string;
  goal: string;
  stage: string;
  status: string;
  nextAction: string;
  lastVerification: string;
  risks: RiskModel[];
  decisions: DecisionModel[];
  fields: EditableField[];
}

export interface HistoryEvent {
  id: string;
  occurredAt: string;
  kind: string;
  title: string;
  meaning: string;
  summary: string;
  why: string;
  changes: string[];
  results: string[];
  decisionIds: string[];
  next: string;
}

export interface HistoryModel {
  projectId: string;
  revision: number;
  events: HistoryEvent[];
  fields: EditableField[];
}

export interface Pricing {
  currency: "USD";
  inputPerMillion: number;
  cachedInputPerMillion: number;
  cacheWriteInputPerMillion: number;
  outputPerMillion: number;
  source: string;
  asOf: string;
}

export interface ModelAccounting {
  model: string;
  inputTokens: number;
  cachedInputTokens: number;
  cacheWriteInputTokens: number;
  outputTokens: number;
  reasoningOutputTokens: number;
  totalTokens: number;
  pricing?: Pricing;
  costUsd: number;
}

export interface SessionAccounting {
  startedAt: string;
  endedAt: string;
  durationMs: number;
  models: ModelAccounting[];
  totalTokens: number;
  totalCostUsd: number;
  pricingComplete: boolean;
}

export interface SessionReport {
  id: string;
  projectId: string;
  sessionId: string;
  previousSessionId: string;
  nextSessionId: string;
  accounting?: SessionAccounting;
}

export interface ProjectModelSummary {
  model: string;
  totalTokens: number;
  totalCostUsd: number;
  tokenSharePct: number;
  costSharePct: number;
}

export interface ProjectAccounting {
  totalDurationMs: number;
  totalTokens: number;
  totalCostUsd: number;
  models: ProjectModelSummary[];
  pricingComplete: boolean;
}

export interface HumanPatchWire {
  entity_id: string;
  field: string;
  operation: "set" | "suppress" | "restore_default";
  value?: string;
  values?: string[];
  base_generated_hash: string;
}

export interface GeneratedBaselineWire {
  generation_id: string;
  entity_id: string;
  field: string;
  kind: "scalar" | "list" | "unsupported";
  value?: string;
  values?: string[];
  generated_hash: string;
}

export interface MachineLedgerV3 {
  schemaVersion: 3;
  minimumWriterVersion: string;
  projectId: string;
  generationId: string;
  projectViewDigest: string;
  acceptedRevision: number;
  reviewSha256: string;
  historySha256: string;
  lastSuccessfulSync?: string;
  accounting: ProjectAccounting;
  sessions: SessionReport[];
  humanPatches: HumanPatchWire[];
  orphanPatches: HumanPatchWire[];
  generatedBaselines: GeneratedBaselineWire[];
  legacyCompatibility?: Readonly<Record<string, unknown>>;
}

export type MachineLedger = MachineLedgerV3;

export interface BrowserSource {
  reviewPath: string;
  historyPath: string;
  ledgerPath: string;
  reviewText: string;
  historyText: string;
  reviewSha256: string;
  historySha256: string;
}

export interface BrowserModel {
  review: ReviewModel;
  events: HistoryEvent[];
  accounting: ProjectAccounting;
  sessions: SessionReport[];
  lastSuccessfulSync?: string;
  source: BrowserSource;
  historyFields?: EditableField[];
}

export interface ScanStatus {
  schema_version: 1;
  job_id: string;
  project_id: string;
  state: "queued" | "running" | "completed" | "completed_with_issues" | "failed";
  phase: "discovering" | "extracting" | "reducing" | "rendering" | "syncing";
  session_count: number;
  indexed_count: number;
  issue_count: number;
  generation_id?: string;
  error_code?: string;
  error_message?: string;
}
