export type ViewKind = "evolution" | "decisions" | "sessions" | "usage";

export type SessionIdentity = Readonly<{ provider: string; sessionId: string }>;

export type ProcessingState = "complete" | "partial" | "error" | "unprocessed";
export type SourceAvailability = "available" | "unavailable";
export type DecisionStatus = "active" | "superseded" | "archived";
export type CandidateStatus = "pending" | "confirmed" | "ignored" | "not_decision" | "stale";
export type PriceStatus =
  | "pending"
  | "current"
  | "promotion"
  | "stale_estimate"
  | "manual_supplement"
  | "ambiguous"
  | "legacy_unverified"
  | "superseded";

export interface SessionReferenceV4 {
  provider: string;
  session_id: string;
}

export interface CurrentStateV4 {
  goal: string;
  stage: string;
  status: string;
  next_action: string;
  last_verification: string;
}

export interface TimelineEntryV4 {
  id: string;
  generation_id: string;
  occurred_at: string;
  kind: string;
  title: string;
  summary: string;
  decision_ids: string[];
}

export interface DecisionV4 {
  id: string;
  kind: "decision" | "agreement";
  occurred_at: string;
  title: string;
  rationale: string;
  impact: string;
  status: DecisionStatus;
  reevaluate_when: string;
  supersedes: string[];
  milestone_ids: string[];
  session_refs: SessionReferenceV4[];
  provenance: "human_created" | "migrated" | "ai_candidate_confirmed";
  pinned: boolean;
  revision: number;
}

export interface RiskV4 {
  id: string;
  title: string;
  status: string;
  detail: string;
}

export interface OpenLoopV4 {
  id: string;
  title: string;
  status: string;
  question: string;
  next_experiment: string;
  completion_criterion: string;
}

export interface HumanPatchV4 {
  entity_id: string;
  field: string;
  operation: "set" | "suppress" | "restore_default";
  value?: string;
  values?: string[];
  base_generated_hash: string;
}

export interface GeneratedBaselineV4 {
  generation_id: string;
  entity_id: string;
  field: string;
  kind: string;
  value?: string;
  values?: string[];
  generated_hash: string;
}

export interface ReviewPresentationV4 {
  schema_version: 4;
  minimum_reader_version: "0.4.0";
  minimum_writer_version: "0.4.0";
  project_id: string;
  generation_id: string;
  project_view_digest: string;
  revision: number;
  current_state: CurrentStateV4;
  timeline: TimelineEntryV4[];
  decisions: DecisionV4[];
  risks: RiskV4[];
  open_loops: OpenLoopV4[];
  human_patches: HumanPatchV4[];
  orphan_patches: HumanPatchV4[];
  generated_baselines: GeneratedBaselineV4[];
}

export interface PricingRatesV1 {
  input: number | null;
  cached_input: number | null;
  cache_write_input: number | null;
  output: number | null;
  reasoning_output: number | null;
}

export interface BillableQuantitiesV1 {
  input: number;
  cached_input: number;
  cache_write_input: number;
  output: number;
  reasoning_output: number;
}

export interface PricingLineCostsV1 {
  input: number | null;
  cached_input: number | null;
  cache_write_input: number | null;
  output: number | null;
  reasoning_output: number | null;
}

export interface PricingSnapshotV1 {
  schema_version: 1;
  minimum_reader_version: "0.4.0";
  snapshot_id: string;
  project_id: string;
  provider: string;
  session_id: string;
  usage_record_digest: string;
  billing_host: string;
  billed_model_id: string;
  billing_mode: string;
  billing_rule_version: string;
  region: string | null;
  priced_at: string;
  created_at: string;
  status: PriceStatus;
  modelpricewatch_listing_id: string | null;
  source_kind: "modelpricewatch" | "official" | "manual" | "unresolved";
  source_url: string | null;
  detail_url: string | null;
  source_last_updated: string | null;
  retrieved_at: string | null;
  promo: boolean;
  promo_until: string | null;
  rates: PricingRatesV1;
  billable_quantities: BillableQuantitiesV1;
  line_costs_usd: PricingLineCostsV1;
  missing_billing_dimensions: string[];
  known_subtotal_usd: number;
  total_cost_usd: number | null;
  pricing_complete: boolean;
  supersedes_snapshot_id: string | null;
  audit_reason: string;
}

export interface LedgerAccountingModelV4 {
  model: string;
  total_tokens: number;
  total_cost_usd: number | null;
}

export interface LedgerAccountingV4 {
  total_duration_ms: number;
  total_tokens: number;
  total_cost_usd: number | null;
  models: LedgerAccountingModelV4[];
}

export interface LedgerSessionV4 {
  provider: string;
  session_id: string;
  processing_state: ProcessingState;
  source_availability: SourceAvailability;
  session_view_digest: string | null;
  usage_record_digest: string | null;
}

export interface SyncHashesV4 {
  review_sha256: string;
  history_sha256: string;
  ledger_sha256: string;
  session_index_digest: string;
}

export interface MachineLedgerV4 {
  schema_version: 4;
  minimum_reader_version: "0.4.0";
  minimum_writer_version: "0.4.0";
  project_id: string;
  generation_id: string;
  project_view_digest: string;
  accepted_revision: number;
  review_sha256: string;
  history_sha256: string;
  accounting: LedgerAccountingV4;
  sessions: LedgerSessionV4[];
  human_patches: HumanPatchV4[];
  orphan_patches: HumanPatchV4[];
  generated_baselines: GeneratedBaselineV4[];
  pricing_snapshots: PricingSnapshotV1[];
  current_pricing_snapshot_ids: string[];
  sync_hashes: SyncHashesV4;
}

export interface CoverageV1 {
  seen: number;
  indexed: number;
  collapsed: number;
  unprojected: number;
  undecodable: number;
  truncated: number;
}

export interface SessionIndexCoverageV1 {
  total: number;
  complete: number;
  partial: number;
  error: number;
  unprocessed: number;
  source_available: number;
  source_unavailable: number;
  started_at_known: number;
  ended_at_known: number;
  usage_known: number;
}

export interface SessionFactCountsV1 {
  file_change: number;
  command: number;
  verification: number;
  error: number;
  artifact: number;
}

export type SessionStateReasonCodeV1 =
  | "not_discovered"
  | "duplicate_candidate"
  | "freeze_terminal"
  | "malformed_source_records"
  | "unsupported_source_records"
  | "source_missing"
  | "source_unreadable"
  | "source_ambiguous"
  | "source_unsupported"
  | "source_unavailable"
  | "partial_observations"
  | "unprojected_facts"
  | "undecodable_facts"
  | "scan_cancelled";

export interface SessionIndexEntryV1 {
  provider: string;
  session_id: string;
  processing_state: ProcessingState;
  state_reason_codes: SessionStateReasonCodeV1[];
  source_availability: SourceAvailability;
  source_terminal_state: string | null;
  started_at: string;
  ended_at: string;
  duration_ms: number | null;
  warning_count: number;
  record_count: number | null;
  indexed_event_count: number;
  coverage: CoverageV1;
  fact_counts: SessionFactCountsV1;
  session_view_digest: string | null;
  usage_record_digest: string | null;
  summary_digest: string | null;
  last_seen_generation_id: string | null;
  last_successful_generation_id: string | null;
}

export interface SessionIndexV1 {
  schema_version: 1;
  minimum_reader_version: "0.4.0";
  digest: string;
  project_id: string;
  generation_id: string;
  project_view_digest: string;
  generated_at: string;
  sort_version: "started-at-desc-null-last-provider-session-v1";
  coverage: SessionIndexCoverageV1;
  sessions: SessionIndexEntryV1[];
}

export interface SessionSummaryEntryV1 {
  occurred_at: string;
  sequence: number;
  revision_id: string;
  text: string;
  source_revision_ids: string[];
}

export interface SessionSummaryErrorEntryV1 extends SessionSummaryEntryV1 {
  code: string;
}

export interface SessionSummaryBlockV1 {
  total: number;
  shown: number;
  omitted: number;
  coverage: CoverageV1;
  items: SessionSummaryEntryV1[];
}

export interface SessionSummaryErrorBlockV1 {
  total: number;
  shown: number;
  omitted: number;
  coverage: CoverageV1;
  items: SessionSummaryErrorEntryV1[];
}

export interface SessionSummaryRulesV1 {
  rule_id: string;
  rule_version: string;
  dependency_digests: string[];
}

export interface SessionSummaryV1 {
  schema_version: 1;
  minimum_reader_version: "0.4.0";
  project_id: string;
  provider: string;
  session_id: string;
  generation_id: string;
  session_view_digest: string;
  phase_boundaries: SessionSummaryBlockV1;
  key_operations: SessionSummaryBlockV1;
  verification_results: SessionSummaryBlockV1;
  errors: SessionSummaryErrorBlockV1;
  unresolved_questions: SessionSummaryBlockV1;
  rules: SessionSummaryRulesV1;
  coverage: CoverageV1;
}

export type SessionEventKindV1 =
  | "message"
  | "tool_call"
  | "tool_result"
  | "cwd_change"
  | "usage"
  | "skip"
  | "file_change"
  | "command"
  | "verification"
  | "error"
  | "artifact";

export interface SessionEventItemV1 {
  kind: SessionEventKindV1;
  excerpt: string;
  revision_id: string;
  sequence: number;
  occurred_at: string;
}

export interface SessionEventPageV1 {
  schema_version: 1;
  minimum_reader_version: "0.4.0";
  project_id: string;
  provider: string;
  session_id: string;
  generation_id: string;
  session_view_digest: string;
  total: number;
  range_start: number;
  range_end: number;
  items: SessionEventItemV1[];
  previous_cursor: string | null;
  next_cursor: string | null;
  first_cursor: string | null;
  last_cursor: string | null;
  coverage: CoverageV1;
}

export interface AnnotationDependencyV1 {
  kind: "observation" | "session_view";
  revision_id: string;
  digest: string;
}

export interface AgentAnnotationEntryV1 {
  id: string;
  project_id: string;
  entity_id: string;
  field: string;
  status: CandidateStatus;
  text: string;
  generation_id: string;
  schema_version: 1;
  analysis_profile: string;
  agent_run_id: string;
  dependencies: AnnotationDependencyV1[];
  revision: number;
  created_at: string;
  confirmed_decision_id: string | null;
}

export interface AnnotationExtractionRunV1 {
  run_id: string;
  project_id: string;
  status: "pending" | "running" | "completed" | "failed" | "cancelled";
  extractor_version: string;
  prompt_schema_version: string;
  dependency_digests: string[];
  created_at: string;
  updated_at: string;
}

export interface AgentAnnotationV1 {
  schema_version: 1;
  minimum_reader_version: "0.4.0";
  project_id: string;
  annotations: AgentAnnotationEntryV1[];
  extraction_runs: AnnotationExtractionRunV1[];
}

export type CandidateListV1 = AgentAnnotationV1;

export interface PricingSupplementV1 {
  schema_version: 1;
  minimum_reader_version: "0.4.0";
  project_id: string;
  provider: string;
  session_id: string;
  usage_record_digest: string;
  billing_host: string;
  billed_model_id: string;
  billing_mode: string;
  billing_rule_version: string;
  region: string | null;
  effective_from: string;
  effective_until: string | null;
  rates: PricingRatesV1;
  source_url: string;
  detail_url: string | null;
  audit_reason: string;
  supersedes_snapshot_id: string | null;
}
