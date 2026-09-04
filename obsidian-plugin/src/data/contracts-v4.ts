import { sha256Text } from "./hash";
import type {
  AgentAnnotationEntryV1,
  AgentAnnotationV1,
  AnnotationDependencyV1,
  AnnotationExtractionRunV1,
  BillableQuantitiesV1,
  CandidateListV1,
  CoverageV1,
  DecisionV4,
  GeneratedBaselineV4,
  HumanPatchV4,
  LedgerAccountingModelV4,
  LedgerAccountingV4,
  LedgerSessionV4,
  MachineLedgerV4,
  PricingLineCostsV1,
  PricingRatesV1,
  PricingSnapshotV1,
  PricingSupplementV1,
  ReviewPresentationV4,
  SessionEventItemV1,
  SessionEventPageV1,
  SessionFactCountsV1,
  SessionIndexCoverageV1,
  SessionIndexEntryV1,
  SessionIndexV1,
  SessionReferenceV4,
  SessionSummaryBlockV1,
  SessionSummaryEntryV1,
  SessionSummaryErrorBlockV1,
  SessionSummaryErrorEntryV1,
  SessionSummaryRulesV1,
  SessionSummaryV1,
  TimelineEntryV4
} from "../contracts/review-v4";

const MAX_JSON_BYTES = 64 << 20;
const MAX_SAFE = Number.MAX_SAFE_INTEGER;
const ID = /^[A-Za-z0-9][A-Za-z0-9._:-]*$/;
const DIGEST = /^sha256:[0-9a-f]{64}$/;
const SHA256 = /^[0-9a-f]{64}$/;
const ZERO_DIGEST = `sha256:${"0".repeat(64)}`;
const ZERO_SHA256 = "0".repeat(64);
const SORT_VERSION = "started-at-desc-null-last-provider-session-v1";
const COVERAGE_KEYS = ["seen", "indexed", "collapsed", "unprojected", "undecodable", "truncated"] as const;
const PRICE_DIMENSIONS = ["input", "cached_input", "cache_write_input", "output", "reasoning_output"] as const;

type JsonObject = Record<string, unknown>;

export function parseReviewPresentationV4(source: string): ReviewPresentationV4 {
  const row = documentObject(source, "review presentation");
  exact(row, "$", [
    "schema_version", "minimum_reader_version", "minimum_writer_version", "project_id", "generation_id",
    "project_view_digest", "revision", "current_state", "timeline", "decisions", "risks", "open_loops",
    "human_patches", "orphan_patches", "generated_baselines"
  ]);
  constant(row.schema_version, 4, "$.schema_version");
  version(row.minimum_reader_version, "$.minimum_reader_version");
  version(row.minimum_writer_version, "$.minimum_writer_version");
  const projectID = id(row.project_id, "$.project_id");
  void projectID;
  const generationID = id(row.generation_id, "$.generation_id");
  digest(row.project_view_digest, "$.project_view_digest");
  integer(row.revision, "$.revision");

  const current = object(row.current_state, "$.current_state");
  exact(current, "$.current_state", ["goal", "stage", "status", "next_action", "last_verification"]);
  for (const key of ["goal", "stage", "status", "next_action", "last_verification"] as const) {
    text(current[key], `$.current_state.${key}`, 16384);
  }

  const timeline = boundedArray(row.timeline, "$.timeline", 65536);
  const timelineIDs = new Set<string>();
  for (let index = 0; index < timeline.length; index += 1) {
    const item = parseTimeline(timeline[index], `$.timeline[${index}]`, generationID);
    addUnique(timelineIDs, item.id, "timeline identity");
  }

  const decisions = boundedArray(row.decisions, "$.decisions", 65536);
  const decisionByID = new Map<string, DecisionV4>();
  for (let index = 0; index < decisions.length; index += 1) {
    const item = parseDecision(decisions[index], `$.decisions[${index}]`);
    if (decisionByID.has(item.id)) throw new Error(`duplicate decision "${item.id}"`);
    decisionByID.set(item.id, item);
  }
  const successors = new Map<string, number>();
  for (const item of decisionByID.values()) {
    for (const target of item.supersedes) {
      if (target === item.id) throw new Error("decision cannot supersede itself");
      if (!decisionByID.has(target)) throw new Error(`decision "${item.id}" supersedes missing "${target}"`);
      successors.set(target, (successors.get(target) ?? 0) + 1);
    }
  }
  if (decisionCycle(decisionByID)) throw new Error("decision supersession graph contains cycle");
  for (const item of decisionByID.values()) {
    if (item.status === "superseded" && !successors.has(item.id)) {
      throw new Error(`superseded decision "${item.id}" has no successor`);
    }
    for (const milestone of item.milestone_ids) {
      if (!timelineIDs.has(milestone)) throw new Error(`decision references missing milestone "${milestone}"`);
    }
  }
  for (let index = 0; index < timeline.length; index += 1) {
    const item = timeline[index] as TimelineEntryV4;
    for (const decisionID of item.decision_ids) {
      if (!decisionByID.has(decisionID)) throw new Error(`timeline references missing decision "${decisionID}"`);
    }
  }

  parseUniqueEntityArray(row.risks, "$.risks", 65536, ["id", "title", "status", "detail"],
    ["title", "status", "detail"]);
  parseUniqueEntityArray(row.open_loops, "$.open_loops", 65536,
    ["id", "title", "status", "question", "next_experiment", "completion_criterion"],
    ["title", "status", "question", "next_experiment", "completion_criterion"]);
  parsePatchArray(row.human_patches, "$.human_patches");
  parsePatchArray(row.orphan_patches, "$.orphan_patches");
  parseBaselineArray(row.generated_baselines, "$.generated_baselines");
  return row as unknown as ReviewPresentationV4;
}

export function parseMachineLedgerV4(source: string): MachineLedgerV4 {
  const row = documentObject(source, "machine ledger");
  exact(row, "$", [
    "schema_version", "minimum_reader_version", "minimum_writer_version", "project_id", "generation_id",
    "project_view_digest", "accepted_revision", "review_sha256", "history_sha256", "accounting", "sessions",
    "human_patches", "orphan_patches", "generated_baselines", "pricing_snapshots",
    "current_pricing_snapshot_ids", "sync_hashes"
  ]);
  constant(row.schema_version, 4, "$.schema_version");
  version(row.minimum_reader_version, "$.minimum_reader_version");
  version(row.minimum_writer_version, "$.minimum_writer_version");
  const projectID = id(row.project_id, "$.project_id");
  id(row.generation_id, "$.generation_id");
  digest(row.project_view_digest, "$.project_view_digest");
  integer(row.accepted_revision, "$.accepted_revision");
  const reviewHash = sha256(row.review_sha256, "$.review_sha256");
  const historyHash = sha256(row.history_sha256, "$.history_sha256");

  const accounting = parseLedgerAccounting(row.accounting, "$.accounting");
  const sessions = boundedArray(row.sessions, "$.sessions", 65536);
  const identities = new Set<string>();
  for (let index = 0; index < sessions.length; index += 1) {
    const session = parseLedgerSession(sessions[index], `$.sessions[${index}]`);
    addUnique(identities, identityKey(session.provider, session.session_id), "ledger session identity");
  }
  parsePatchArray(row.human_patches, "$.human_patches");
  parsePatchArray(row.orphan_patches, "$.orphan_patches");
  parseBaselineArray(row.generated_baselines, "$.generated_baselines");

  const pricingRows = boundedArray(row.pricing_snapshots, "$.pricing_snapshots", 65536);
  const pricingByID = new Map<string, PricingSnapshotV1>();
  for (let index = 0; index < pricingRows.length; index += 1) {
    const snapshot = validatePricingSnapshot(pricingRows[index], `$.pricing_snapshots[${index}]`);
    if (snapshot.project_id !== projectID) throw new Error("pricing snapshot project mismatch");
    if (pricingByID.has(snapshot.snapshot_id)) throw new Error(`duplicate pricing snapshot "${snapshot.snapshot_id}"`);
    pricingByID.set(snapshot.snapshot_id, snapshot);
  }
  const currentIDs = idArray(row.current_pricing_snapshot_ids, "$.current_pricing_snapshot_ids", 65536);
  const seenCurrent = new Set<string>();
  let currentPricingIncomplete = false;
  for (const snapshotID of currentIDs) {
    addUnique(seenCurrent, snapshotID, "current pricing snapshot reference");
    const snapshot = pricingByID.get(snapshotID);
    if (!snapshot || snapshot.status === "superseded") throw new Error("invalid current pricing snapshot reference");
    currentPricingIncomplete ||= !snapshot.pricing_complete;
  }
  if (currentPricingIncomplete && accounting.total_cost_usd !== null) {
    throw new Error("aggregate price must be null when a current snapshot is incomplete");
  }

  const sync = object(row.sync_hashes, "$.sync_hashes");
  exact(sync, "$.sync_hashes", ["review_sha256", "history_sha256", "ledger_sha256", "session_index_digest"]);
  const syncReview = sha256(sync.review_sha256, "$.sync_hashes.review_sha256");
  const syncHistory = sha256(sync.history_sha256, "$.sync_hashes.history_sha256");
  const selfHash = sha256(sync.ledger_sha256, "$.sync_hashes.ledger_sha256");
  digest(sync.session_index_digest, "$.sync_hashes.session_index_digest");
  if (reviewHash !== syncReview || historyHash !== syncHistory) {
    throw new Error("top-level and synchronization hashes disagree");
  }

  const ledger = row as unknown as MachineLedgerV4;
  if (selfHash !== ZERO_SHA256 && canonicalLedgerSHA256(ledger) !== selfHash) {
    throw new Error("machine ledger self digest mismatch");
  }
  return ledger;
}

export function parseSessionIndexV1(source: string): SessionIndexV1 {
  const row = documentObject(source, "session index");
  exact(row, "$", [
    "schema_version", "minimum_reader_version", "digest", "project_id", "generation_id", "project_view_digest",
    "generated_at", "sort_version", "coverage", "sessions"
  ]);
  constant(row.schema_version, 1, "$.schema_version");
  version(row.minimum_reader_version, "$.minimum_reader_version");
  const claimedDigest = digest(row.digest, "$.digest");
  id(row.project_id, "$.project_id");
  id(row.generation_id, "$.generation_id");
  digest(row.project_view_digest, "$.project_view_digest");
  text(row.generated_at, "$.generated_at", 128, true);
  constant(row.sort_version, SORT_VERSION, "$.sort_version");
  const claimedCoverage = parseIndexCoverage(row.coverage, "$.coverage");
  const sessions = boundedArray(row.sessions, "$.sessions", 65536);
  const identities = new Set<string>();
  const calculated: SessionIndexCoverageV1 = {
    total: sessions.length,
    complete: 0,
    partial: 0,
    error: 0,
    unprocessed: 0,
    source_available: 0,
    source_unavailable: 0,
    started_at_known: 0,
    ended_at_known: 0,
    usage_known: 0
  };
  const parsedSessions: SessionIndexEntryV1[] = [];
  for (let index = 0; index < sessions.length; index += 1) {
    const session = parseIndexEntry(sessions[index], `$.sessions[${index}]`);
    addUnique(identities, identityKey(session.provider, session.session_id), "session identity");
    calculated[session.processing_state] += 1;
    if (session.source_availability === "available") calculated.source_available += 1;
    else calculated.source_unavailable += 1;
    calculated.started_at_known += 1;
    calculated.ended_at_known += 1;
    if (session.usage_record_digest !== null) calculated.usage_known += 1;
    parsedSessions.push(session);
  }
  if (!sameIndexCoverage(claimedCoverage, calculated)) throw new Error("index coverage does not reconcile");
  checkedSum("$.coverage processing states", claimedCoverage.complete, claimedCoverage.partial,
    claimedCoverage.error, claimedCoverage.unprocessed);
  if (claimedCoverage.complete + claimedCoverage.partial + claimedCoverage.error + claimedCoverage.unprocessed !== claimedCoverage.total) {
    throw new Error("index processing-state coverage does not reconcile");
  }
  checkedSum("$.coverage sources", claimedCoverage.source_available, claimedCoverage.source_unavailable);
  if (claimedCoverage.source_available + claimedCoverage.source_unavailable !== claimedCoverage.total) {
    throw new Error("index source coverage does not reconcile");
  }
  for (let index = 1; index < parsedSessions.length; index += 1) {
    if (compareIndexEntries(parsedSessions[index - 1], parsedSessions[index]) > 0) {
      throw new Error("sessions are not in canonical order");
    }
  }
  const result = row as unknown as SessionIndexV1;
  if (claimedDigest !== ZERO_DIGEST && canonicalIndexDigest(result) !== claimedDigest) {
    throw new Error("session index digest mismatch");
  }
  return result;
}

export function parseSessionSummaryV1(source: string): SessionSummaryV1 {
  const row = documentObject(source, "session summary");
  exact(row, "$", [
    "schema_version", "minimum_reader_version", "project_id", "provider", "session_id", "generation_id",
    "session_view_digest", "phase_boundaries", "key_operations", "verification_results", "errors",
    "unresolved_questions", "rules", "coverage"
  ]);
  parseInspectionIdentity(row);
  parseSummaryBlock(row.phase_boundaries, "$.phase_boundaries");
  parseSummaryBlock(row.key_operations, "$.key_operations");
  parseSummaryBlock(row.verification_results, "$.verification_results");
  parseSummaryErrorBlock(row.errors, "$.errors");
  parseSummaryBlock(row.unresolved_questions, "$.unresolved_questions");
  parseSummaryRules(row.rules, "$.rules");
  parseCoverage(row.coverage, "$.coverage");
  return row as unknown as SessionSummaryV1;
}

export function parseSessionEventPageV1(source: string): SessionEventPageV1 {
  const row = documentObject(source, "session event page");
  exact(row, "$", [
    "schema_version", "minimum_reader_version", "project_id", "provider", "session_id", "generation_id",
    "session_view_digest", "total", "range_start", "range_end", "items", "previous_cursor", "next_cursor",
    "first_cursor", "last_cursor", "coverage"
  ]);
  parseInspectionIdentity(row);
  const total = integer(row.total, "$.total");
  const rangeStart = integer(row.range_start, "$.range_start");
  const rangeEnd = integer(row.range_end, "$.range_end");
  const items = boundedArray(row.items, "$.items", 100);
  if (rangeStart > rangeEnd || rangeEnd > total || items.length !== rangeEnd - rangeStart) {
    throw new Error("event page range does not reconcile");
  }
  for (const key of ["previous_cursor", "next_cursor", "first_cursor", "last_cursor"] as const) {
    nullableText(row[key], `$.${key}`, 4096);
  }
  if (total === 0 && (rangeStart !== 0 || rangeEnd !== 0 || row.previous_cursor !== null || row.next_cursor !== null ||
    row.first_cursor !== null || row.last_cursor !== null)) {
    throw new Error("empty event page cannot have a range or cursors");
  }
  const coverage = parseCoverage(row.coverage, "$.coverage");
  if (total !== coverage.indexed) throw new Error("event page total does not match indexed coverage");
  const parsedItems: SessionEventItemV1[] = [];
  for (let index = 0; index < items.length; index += 1) {
    parsedItems.push(parseEventItem(items[index], `$.items[${index}]`));
  }
  assertCanonicalOrder(parsedItems, compareSummaryEntry, "event items");
  return row as unknown as SessionEventPageV1;
}

export function parseAgentAnnotationV1(source: string): AgentAnnotationV1 {
  const row = documentObject(source, "agent annotation");
  exact(row, "$", ["schema_version", "minimum_reader_version", "project_id", "annotations", "extraction_runs"]);
  constant(row.schema_version, 1, "$.schema_version");
  version(row.minimum_reader_version, "$.minimum_reader_version");
  const projectID = id(row.project_id, "$.project_id");
  const annotations = boundedArray(row.annotations, "$.annotations", 65536);
  const runs = boundedArray(row.extraction_runs, "$.extraction_runs", 65536);
  const runIDs = new Set<string>();
  for (let index = 0; index < runs.length; index += 1) {
    const run = parseExtractionRun(runs[index], `$.extraction_runs[${index}]`, projectID);
    addUnique(runIDs, run.run_id, "extraction run");
  }
  const annotationIDs = new Set<string>();
  for (let index = 0; index < annotations.length; index += 1) {
    const annotation = parseAnnotation(annotations[index], `$.annotations[${index}]`, projectID);
    addUnique(annotationIDs, annotation.id, "annotation");
    if (!runIDs.has(annotation.agent_run_id)) {
      throw new Error(`annotation "${annotation.id}" references missing extraction run`);
    }
  }
  return row as unknown as AgentAnnotationV1;
}

export function parseCandidateListV1(source: string): CandidateListV1 {
  return parseAgentAnnotationV1(source);
}

export function parsePricingSnapshotV1(source: string): PricingSnapshotV1 {
  return validatePricingSnapshot(documentObject(source, "pricing snapshot"), "$" );
}

export function parsePricingSupplementV1(source: string): PricingSupplementV1 {
  const row = documentObject(source, "pricing supplement");
  exact(row, "$", [
    "schema_version", "minimum_reader_version", "project_id", "provider", "session_id", "usage_record_digest",
    "billing_host", "billed_model_id", "billing_mode", "billing_rule_version", "region", "effective_from",
    "effective_until", "rates", "source_url", "detail_url", "audit_reason", "supersedes_snapshot_id"
  ]);
  constant(row.schema_version, 1, "$.schema_version");
  version(row.minimum_reader_version, "$.minimum_reader_version");
  id(row.project_id, "$.project_id");
  id(row.provider, "$.provider");
  id(row.session_id, "$.session_id");
  digest(row.usage_record_digest, "$.usage_record_digest");
  text(row.billing_host, "$.billing_host", 4096, true);
  text(row.billed_model_id, "$.billed_model_id", 4096, true);
  text(row.billing_mode, "$.billing_mode", 4096, true);
  id(row.billing_rule_version, "$.billing_rule_version");
  nullableText(row.region, "$.region", 128);
  text(row.effective_from, "$.effective_from", 128, true);
  nullableText(row.effective_until, "$.effective_until", 128);
  parseRates(row.rates, "$.rates");
  httpsURL(row.source_url, "$.source_url");
  nullableURL(row.detail_url, "$.detail_url");
  text(row.audit_reason, "$.audit_reason", 4096, true);
  nullableText(row.supersedes_snapshot_id, "$.supersedes_snapshot_id", 256);
  return row as unknown as PricingSupplementV1;
}

export function assertSnapshotBindings(ledger: MachineLedgerV4, index: SessionIndexV1): void {
  if (ledger.project_id !== index.project_id || ledger.generation_id !== index.generation_id ||
    ledger.project_view_digest !== index.project_view_digest || ledger.sync_hashes.session_index_digest !== index.digest) {
    throw new Error("ledger and session index snapshot binding mismatch");
  }
}

function parseTimeline(value: unknown, path: string, generationID: string): TimelineEntryV4 {
  const row = object(value, path);
  exact(row, path, ["id", "generation_id", "occurred_at", "kind", "title", "summary", "decision_ids"]);
  const parsedGeneration = id(row.generation_id, `${path}.generation_id`);
  if (parsedGeneration !== generationID) throw new Error(`${path}.generation_id does not match presentation`);
  id(row.id, `${path}.id`);
  text(row.occurred_at, `${path}.occurred_at`, 128);
  id(row.kind, `${path}.kind`);
  text(row.title, `${path}.title`, 16384);
  text(row.summary, `${path}.summary`, 16384);
  idArray(row.decision_ids, `${path}.decision_ids`, 256, true);
  return row as unknown as TimelineEntryV4;
}

function parseDecision(value: unknown, path: string): DecisionV4 {
  const row = object(value, path);
  exact(row, path, [
    "id", "kind", "occurred_at", "title", "rationale", "impact", "status", "reevaluate_when", "supersedes",
    "milestone_ids", "session_refs", "provenance", "pinned", "revision"
  ]);
  id(row.id, `${path}.id`);
  oneOf(row.kind, `${path}.kind`, ["decision", "agreement"]);
  text(row.occurred_at, `${path}.occurred_at`, 128);
  text(row.title, `${path}.title`, 16384);
  text(row.rationale, `${path}.rationale`, 16384);
  text(row.impact, `${path}.impact`, 16384);
  oneOf(row.status, `${path}.status`, ["active", "superseded", "archived"]);
  text(row.reevaluate_when, `${path}.reevaluate_when`, 16384);
  idArray(row.supersedes, `${path}.supersedes`, 256, true);
  idArray(row.milestone_ids, `${path}.milestone_ids`, 256, true);
  const refs = boundedArray(row.session_refs, `${path}.session_refs`, 256);
  const identities = new Set<string>();
  for (let index = 0; index < refs.length; index += 1) {
    const ref = parseSessionReference(refs[index], `${path}.session_refs[${index}]`);
    addUnique(identities, identityKey(ref.provider, ref.session_id), "decision session reference");
  }
  oneOf(row.provenance, `${path}.provenance`, ["human_created", "migrated", "ai_candidate_confirmed"]);
  boolean(row.pinned, `${path}.pinned`);
  positiveInteger(row.revision, `${path}.revision`);
  return row as unknown as DecisionV4;
}

function parseSessionReference(value: unknown, path: string): SessionReferenceV4 {
  const row = object(value, path);
  exact(row, path, ["provider", "session_id"]);
  id(row.provider, `${path}.provider`);
  id(row.session_id, `${path}.session_id`);
  return row as unknown as SessionReferenceV4;
}

function parseUniqueEntityArray(
  value: unknown,
  path: string,
  maximum: number,
  keys: readonly string[],
  textKeys: readonly string[]
): void {
  const values = boundedArray(value, path, maximum);
  const identities = new Set<string>();
  for (let index = 0; index < values.length; index += 1) {
    const itemPath = `${path}[${index}]`;
    const row = object(values[index], itemPath);
    exact(row, itemPath, keys);
    const itemID = id(row.id, `${itemPath}.id`);
    addUnique(identities, itemID, `${path} identity`);
    for (const key of textKeys) text(row[key], `${itemPath}.${key}`, 16384);
  }
}

function parsePatchArray(value: unknown, path: string): HumanPatchV4[] {
  const values = boundedArray(value, path, 65536);
  return values.map((item, index) => parsePatch(item, `${path}[${index}]`));
}

function parsePatch(value: unknown, path: string): HumanPatchV4 {
  const row = object(value, path);
  exact(row, path, ["entity_id", "field", "operation", "value", "values", "base_generated_hash"],
    ["entity_id", "field", "operation", "base_generated_hash"]);
  id(row.entity_id, `${path}.entity_id`);
  id(row.field, `${path}.field`);
  oneOf(row.operation, `${path}.operation`, ["set", "suppress", "restore_default"]);
  if (row.value !== undefined) text(row.value, `${path}.value`, 16384);
  if (row.values !== undefined) stringArray(row.values, `${path}.values`, 256, 16384);
  sha256(row.base_generated_hash, `${path}.base_generated_hash`);
  return row as unknown as HumanPatchV4;
}

function parseBaselineArray(value: unknown, path: string): GeneratedBaselineV4[] {
  const values = boundedArray(value, path, 65536);
  return values.map((item, index) => parseBaseline(item, `${path}[${index}]`));
}

function parseBaseline(value: unknown, path: string): GeneratedBaselineV4 {
  const row = object(value, path);
  exact(row, path, ["generation_id", "entity_id", "field", "kind", "value", "values", "generated_hash"],
    ["generation_id", "entity_id", "field", "kind", "generated_hash"]);
  id(row.generation_id, `${path}.generation_id`);
  id(row.entity_id, `${path}.entity_id`);
  id(row.field, `${path}.field`);
  id(row.kind, `${path}.kind`);
  if (row.value !== undefined) text(row.value, `${path}.value`, 16384);
  if (row.values !== undefined) stringArray(row.values, `${path}.values`, 256, 16384);
  sha256(row.generated_hash, `${path}.generated_hash`);
  return row as unknown as GeneratedBaselineV4;
}

function parseLedgerAccounting(value: unknown, path: string): LedgerAccountingV4 {
  const row = object(value, path);
  exact(row, path, ["total_duration_ms", "total_tokens", "total_cost_usd", "models"]);
  integer(row.total_duration_ms, `${path}.total_duration_ms`);
  const totalTokens = integer(row.total_tokens, `${path}.total_tokens`);
  const totalCost = nullableMoney(row.total_cost_usd, `${path}.total_cost_usd`);
  const models = boundedArray(row.models, `${path}.models`, 256);
  const modelNames = new Set<string>();
  const parsedModels: LedgerAccountingModelV4[] = [];
  let modelTokens = 0;
  let modelCost = 0;
  let modelCostsComplete = true;
  for (let index = 0; index < models.length; index += 1) {
    const modelPath = `${path}.models[${index}]`;
    const model = object(models[index], modelPath);
    exact(model, modelPath, ["model", "total_tokens", "total_cost_usd"]);
    const name = text(model.model, `${modelPath}.model`, 16384);
    addUnique(modelNames, name, "accounting model");
    const tokens = integer(model.total_tokens, `${modelPath}.total_tokens`);
    modelTokens = checkedAdd(modelTokens, tokens, `${path} model token total`);
    const cost = nullableMoney(model.total_cost_usd, `${modelPath}.total_cost_usd`);
    if (cost === null) modelCostsComplete = false;
    else modelCost += cost;
    parsedModels.push(model as unknown as LedgerAccountingModelV4);
  }
  if (models.length > 0 && modelTokens !== totalTokens) throw new Error("accounting token total does not reconcile");
  if (models.length > 0 && !modelCostsComplete && totalCost !== null) {
    throw new Error("aggregate price must be null when an included model cost is unknown");
  }
  if (models.length > 0 && modelCostsComplete && totalCost !== null && !nearlyEqual(totalCost, modelCost)) {
    throw new Error("accounting cost total does not reconcile");
  }
  void parsedModels;
  return row as unknown as LedgerAccountingV4;
}

function parseLedgerSession(value: unknown, path: string): LedgerSessionV4 {
  const row = object(value, path);
  exact(row, path, ["provider", "session_id", "processing_state", "source_availability", "session_view_digest", "usage_record_digest"]);
  id(row.provider, `${path}.provider`);
  id(row.session_id, `${path}.session_id`);
  oneOf(row.processing_state, `${path}.processing_state`, ["complete", "partial", "error", "unprocessed"]);
  oneOf(row.source_availability, `${path}.source_availability`, ["available", "unavailable"]);
  nullableDigest(row.session_view_digest, `${path}.session_view_digest`);
  nullableDigest(row.usage_record_digest, `${path}.usage_record_digest`);
  return row as unknown as LedgerSessionV4;
}

function parseIndexCoverage(value: unknown, path: string): SessionIndexCoverageV1 {
  const row = object(value, path);
  const keys = [
    "total", "complete", "partial", "error", "unprocessed", "source_available", "source_unavailable",
    "started_at_known", "ended_at_known", "usage_known"
  ] as const;
  exact(row, path, keys);
  for (const key of keys) integer(row[key], `${path}.${key}`);
  return row as unknown as SessionIndexCoverageV1;
}

function parseIndexEntry(value: unknown, path: string): SessionIndexEntryV1 {
  const row = object(value, path);
  exact(row, path, [
    "provider", "session_id", "processing_state", "state_reason_codes", "source_availability", "source_terminal_state",
    "started_at", "ended_at", "duration_ms", "warning_count", "record_count", "indexed_event_count", "coverage",
    "fact_counts", "session_view_digest", "usage_record_digest", "summary_digest", "last_seen_generation_id",
    "last_successful_generation_id"
  ]);
  id(row.provider, `${path}.provider`);
  id(row.session_id, `${path}.session_id`);
  oneOf(row.processing_state, `${path}.processing_state`, ["complete", "partial", "error", "unprocessed"]);
  const reasons = boundedArray(row.state_reason_codes, `${path}.state_reason_codes`, 64);
  const seenReasons = new Set<string>();
  const allowedReasons = [
    "not_discovered", "duplicate_candidate", "freeze_terminal", "malformed_source_records", "unsupported_source_records",
    "source_missing", "source_unreadable", "source_ambiguous", "source_unsupported", "source_unavailable",
    "partial_observations", "unprojected_facts", "undecodable_facts", "scan_cancelled"
  ] as const;
  for (let index = 0; index < reasons.length; index += 1) {
    const reason = oneOf(reasons[index], `${path}.state_reason_codes[${index}]`, allowedReasons);
    addUnique(seenReasons, reason, "state reason");
  }
  oneOf(row.source_availability, `${path}.source_availability`, ["available", "unavailable"]);
  nullableText(row.source_terminal_state, `${path}.source_terminal_state`, 64);
  text(row.started_at, `${path}.started_at`, 128, true);
  text(row.ended_at, `${path}.ended_at`, 128, true);
  nullableInteger(row.duration_ms, `${path}.duration_ms`);
  integer(row.warning_count, `${path}.warning_count`);
  nullableInteger(row.record_count, `${path}.record_count`);
  const indexedEvents = integer(row.indexed_event_count, `${path}.indexed_event_count`);
  const coverage = parseCoverage(row.coverage, `${path}.coverage`);
  if (indexedEvents !== coverage.indexed) throw new Error(`${path} indexed event coverage does not reconcile`);
  parseFactCounts(row.fact_counts, `${path}.fact_counts`);
  nullableDigest(row.session_view_digest, `${path}.session_view_digest`);
  nullableDigest(row.usage_record_digest, `${path}.usage_record_digest`);
  nullableDigest(row.summary_digest, `${path}.summary_digest`);
  nullableText(row.last_seen_generation_id, `${path}.last_seen_generation_id`, 256);
  nullableText(row.last_successful_generation_id, `${path}.last_successful_generation_id`, 256);
  return row as unknown as SessionIndexEntryV1;
}

function parseFactCounts(value: unknown, path: string): SessionFactCountsV1 {
  const row = object(value, path);
  const keys = ["file_change", "command", "verification", "error", "artifact"] as const;
  exact(row, path, keys);
  for (const key of keys) integer(row[key], `${path}.${key}`);
  return row as unknown as SessionFactCountsV1;
}

function parseInspectionIdentity(row: JsonObject): void {
  constant(row.schema_version, 1, "$.schema_version");
  version(row.minimum_reader_version, "$.minimum_reader_version");
  id(row.project_id, "$.project_id");
  id(row.provider, "$.provider");
  id(row.session_id, "$.session_id");
  id(row.generation_id, "$.generation_id");
  digest(row.session_view_digest, "$.session_view_digest");
}

function parseCoverage(value: unknown, path: string): CoverageV1 {
  const row = object(value, path);
  exact(row, path, COVERAGE_KEYS);
  const values = COVERAGE_KEYS.map((key) => integer(row[key], `${path}.${key}`));
  const total = checkedSum(path, ...values.slice(1));
  if (total !== values[0]) throw new Error(`${path} does not reconcile`);
  return row as unknown as CoverageV1;
}

function parseSummaryBlock(value: unknown, path: string): SessionSummaryBlockV1 {
  const row = object(value, path);
  exact(row, path, ["total", "shown", "omitted", "coverage", "items"]);
  const total = integer(row.total, `${path}.total`);
  const shown = integer(row.shown, `${path}.shown`);
  const omitted = integer(row.omitted, `${path}.omitted`);
  const items = boundedArray(row.items, `${path}.items`, 32);
  if (shown > total || omitted !== total - shown || items.length !== shown) throw new Error(`${path} does not reconcile`);
  parseCoverage(row.coverage, `${path}.coverage`);
  const parsed = items.map((item, index) => parseSummaryEntry(item, `${path}.items[${index}]`));
  assertCanonicalOrder(parsed, compareSummaryEntry, `${path} items`);
  return row as unknown as SessionSummaryBlockV1;
}

function parseSummaryErrorBlock(value: unknown, path: string): SessionSummaryErrorBlockV1 {
  const row = object(value, path);
  exact(row, path, ["total", "shown", "omitted", "coverage", "items"]);
  const total = integer(row.total, `${path}.total`);
  const shown = integer(row.shown, `${path}.shown`);
  const omitted = integer(row.omitted, `${path}.omitted`);
  const items = boundedArray(row.items, `${path}.items`, 32);
  if (shown > total || omitted !== total - shown || items.length !== shown) throw new Error(`${path} does not reconcile`);
  parseCoverage(row.coverage, `${path}.coverage`);
  const parsed = items.map((item, index) => parseSummaryErrorEntry(item, `${path}.items[${index}]`));
  assertCanonicalOrder(parsed, compareSummaryEntry, `${path} items`);
  return row as unknown as SessionSummaryErrorBlockV1;
}

function parseSummaryEntry(value: unknown, path: string): SessionSummaryEntryV1 {
  const row = object(value, path);
  exact(row, path, ["occurred_at", "sequence", "revision_id", "text", "source_revision_ids"]);
  text(row.occurred_at, `${path}.occurred_at`, 128);
  positiveInteger(row.sequence, `${path}.sequence`);
  id(row.revision_id, `${path}.revision_id`);
  text(row.text, `${path}.text`, 512);
  idArray(row.source_revision_ids, `${path}.source_revision_ids`, 64, true);
  return row as unknown as SessionSummaryEntryV1;
}

function parseSummaryErrorEntry(value: unknown, path: string): SessionSummaryErrorEntryV1 {
  const row = object(value, path);
  exact(row, path, ["code", "occurred_at", "sequence", "revision_id", "text", "source_revision_ids"]);
  id(row.code, `${path}.code`);
  text(row.occurred_at, `${path}.occurred_at`, 128);
  positiveInteger(row.sequence, `${path}.sequence`);
  id(row.revision_id, `${path}.revision_id`);
  text(row.text, `${path}.text`, 512);
  idArray(row.source_revision_ids, `${path}.source_revision_ids`, 64, true);
  return row as unknown as SessionSummaryErrorEntryV1;
}

function parseSummaryRules(value: unknown, path: string): SessionSummaryRulesV1 {
  const row = object(value, path);
  exact(row, path, ["rule_id", "rule_version", "dependency_digests"]);
  id(row.rule_id, `${path}.rule_id`);
  id(row.rule_version, `${path}.rule_version`);
  const dependencies = boundedArray(row.dependency_digests, `${path}.dependency_digests`, 128);
  const seen = new Set<string>();
  for (let index = 0; index < dependencies.length; index += 1) {
    addUnique(seen, digest(dependencies[index], `${path}.dependency_digests[${index}]`), "rule dependency digest");
  }
  return row as unknown as SessionSummaryRulesV1;
}

function parseEventItem(value: unknown, path: string): SessionEventItemV1 {
  const row = object(value, path);
  exact(row, path, ["kind", "excerpt", "revision_id", "sequence", "occurred_at"]);
  oneOf(row.kind, `${path}.kind`, [
    "message", "tool_call", "tool_result", "cwd_change", "usage", "skip", "file_change", "command",
    "verification", "error", "artifact"
  ]);
  text(row.excerpt, `${path}.excerpt`, 512);
  id(row.revision_id, `${path}.revision_id`);
  positiveInteger(row.sequence, `${path}.sequence`);
  text(row.occurred_at, `${path}.occurred_at`, 128);
  return row as unknown as SessionEventItemV1;
}

function parseExtractionRun(value: unknown, path: string, projectID: string): AnnotationExtractionRunV1 {
  const row = object(value, path);
  exact(row, path, [
    "run_id", "project_id", "status", "extractor_version", "prompt_schema_version", "dependency_digests",
    "created_at", "updated_at"
  ]);
  id(row.run_id, `${path}.run_id`);
  if (id(row.project_id, `${path}.project_id`) !== projectID) throw new Error(`${path}.project_id does not match store`);
  oneOf(row.status, `${path}.status`, ["pending", "running", "completed", "failed", "cancelled"]);
  id(row.extractor_version, `${path}.extractor_version`);
  id(row.prompt_schema_version, `${path}.prompt_schema_version`);
  const dependencies = boundedArray(row.dependency_digests, `${path}.dependency_digests`, 256);
  const seen = new Set<string>();
  for (let index = 0; index < dependencies.length; index += 1) {
    addUnique(seen, digest(dependencies[index], `${path}.dependency_digests[${index}]`), "extraction dependency digest");
  }
  text(row.created_at, `${path}.created_at`, 128);
  text(row.updated_at, `${path}.updated_at`, 128);
  return row as unknown as AnnotationExtractionRunV1;
}

function parseAnnotation(value: unknown, path: string, projectID: string): AgentAnnotationEntryV1 {
  const row = object(value, path);
  exact(row, path, [
    "id", "project_id", "entity_id", "field", "status", "text", "generation_id", "schema_version",
    "analysis_profile", "agent_run_id", "dependencies", "revision", "created_at", "confirmed_decision_id"
  ]);
  const annotationID = id(row.id, `${path}.id`);
  if (id(row.project_id, `${path}.project_id`) !== projectID) throw new Error(`${path}.project_id does not match store`);
  id(row.entity_id, `${path}.entity_id`);
  id(row.field, `${path}.field`);
  const status = oneOf(row.status, `${path}.status`, ["pending", "confirmed", "ignored", "not_decision", "stale"]);
  text(row.text, `${path}.text`, 4096);
  id(row.generation_id, `${path}.generation_id`);
  constant(row.schema_version, 1, `${path}.schema_version`);
  id(row.analysis_profile, `${path}.analysis_profile`);
  id(row.agent_run_id, `${path}.agent_run_id`);
  const dependencies = boundedArray(row.dependencies, `${path}.dependencies`, 256);
  const seenDependencies = new Set<string>();
  for (let index = 0; index < dependencies.length; index += 1) {
    const dependency = parseAnnotationDependency(dependencies[index], `${path}.dependencies[${index}]`);
    addUnique(seenDependencies, `${dependency.kind}\u0000${dependency.revision_id}`, "annotation dependency");
  }
  positiveInteger(row.revision, `${path}.revision`);
  text(row.created_at, `${path}.created_at`, 128);
  const confirmedID = nullableText(row.confirmed_decision_id, `${path}.confirmed_decision_id`, 256);
  if (status === "confirmed") {
    if (confirmedID === null || !ID.test(confirmedID)) throw new Error(`confirmed candidate "${annotationID}" has no valid decision`);
  } else if (confirmedID !== null) {
    throw new Error(`candidate "${annotationID}" is not confirmed but has a decision`);
  }
  return row as unknown as AgentAnnotationEntryV1;
}

function parseAnnotationDependency(value: unknown, path: string): AnnotationDependencyV1 {
  const row = object(value, path);
  exact(row, path, ["kind", "revision_id", "digest"]);
  oneOf(row.kind, `${path}.kind`, ["observation", "session_view"]);
  id(row.revision_id, `${path}.revision_id`);
  digest(row.digest, `${path}.digest`);
  return row as unknown as AnnotationDependencyV1;
}

function validatePricingSnapshot(value: unknown, path: string): PricingSnapshotV1 {
  const row = object(value, path);
  exact(row, path, [
    "schema_version", "minimum_reader_version", "snapshot_id", "project_id", "provider", "session_id",
    "usage_record_digest", "billing_host", "billed_model_id", "billing_mode", "billing_rule_version", "region",
    "priced_at", "created_at", "status", "modelpricewatch_listing_id", "source_kind", "source_url", "detail_url",
    "source_last_updated", "retrieved_at", "promo", "promo_until", "rates", "billable_quantities", "line_costs_usd",
    "missing_billing_dimensions", "known_subtotal_usd", "total_cost_usd", "pricing_complete",
    "supersedes_snapshot_id", "audit_reason"
  ]);
  constant(row.schema_version, 1, `${path}.schema_version`);
  version(row.minimum_reader_version, `${path}.minimum_reader_version`);
  id(row.snapshot_id, `${path}.snapshot_id`);
  id(row.project_id, `${path}.project_id`);
  id(row.provider, `${path}.provider`);
  id(row.session_id, `${path}.session_id`);
  digest(row.usage_record_digest, `${path}.usage_record_digest`);
  text(row.billing_host, `${path}.billing_host`, 4096, true);
  text(row.billed_model_id, `${path}.billed_model_id`, 4096, true);
  text(row.billing_mode, `${path}.billing_mode`, 4096, true);
  id(row.billing_rule_version, `${path}.billing_rule_version`);
  nullableText(row.region, `${path}.region`, 128);
  text(row.priced_at, `${path}.priced_at`, 128, true);
  text(row.created_at, `${path}.created_at`, 128, true);
  const status = oneOf(row.status, `${path}.status`, [
    "pending", "current", "promotion", "stale_estimate", "manual_supplement", "ambiguous", "legacy_unverified", "superseded"
  ]);
  void status;
  const listingID = nullableText(row.modelpricewatch_listing_id, `${path}.modelpricewatch_listing_id`, 256);
  const sourceKind = oneOf(row.source_kind, `${path}.source_kind`, ["modelpricewatch", "official", "manual", "unresolved"]);
  const sourceURL = nullableURL(row.source_url, `${path}.source_url`);
  nullableURL(row.detail_url, `${path}.detail_url`);
  nullableText(row.source_last_updated, `${path}.source_last_updated`, 128);
  const retrievedAt = nullableText(row.retrieved_at, `${path}.retrieved_at`, 128);
  boolean(row.promo, `${path}.promo`);
  nullableText(row.promo_until, `${path}.promo_until`, 128);
  if (sourceKind === "unresolved") {
    if (sourceURL !== null || row.pricing_complete === true) throw new Error("unresolved pricing cannot carry resolved source evidence");
  } else if (sourceURL === null) {
    throw new Error("resolved pricing requires HTTPS source evidence");
  }
  if (sourceKind === "modelpricewatch" && (listingID === null || listingID === "" || retrievedAt === null)) {
    throw new Error("modelpricewatch pricing requires listing and retrieval evidence");
  }

  const rates = parseRates(row.rates, `${path}.rates`);
  const quantities = parseQuantities(row.billable_quantities, `${path}.billable_quantities`);
  const costs = parseLineCosts(row.line_costs_usd, `${path}.line_costs_usd`);
  const missingRows = boundedArray(row.missing_billing_dimensions, `${path}.missing_billing_dimensions`, 32);
  const missing = new Set<string>();
  for (let index = 0; index < missingRows.length; index += 1) {
    addUnique(missing, text(missingRows[index], `${path}.missing_billing_dimensions[${index}]`, 4096, true),
      "missing billing dimension");
  }
  const knownSubtotal = money(row.known_subtotal_usd, `${path}.known_subtotal_usd`);
  const totalCost = nullableMoney(row.total_cost_usd, `${path}.total_cost_usd`);
  const pricingComplete = boolean(row.pricing_complete, `${path}.pricing_complete`);
  nullableText(row.supersedes_snapshot_id, `${path}.supersedes_snapshot_id`, 256);
  text(row.audit_reason, `${path}.audit_reason`, 4096, true);

  let calculatedSubtotal = 0;
  for (const dimension of PRICE_DIMENSIONS) {
    const rate = rates[dimension];
    const quantity = quantities[dimension];
    const cost = costs[dimension];
    if (quantity > 0 && (rate === null || cost === null) && !missing.has(dimension)) {
      throw new Error(`unknown billed dimension ${dimension} is not reported`);
    }
    if (rate !== null && cost !== null) {
      const expected = quantity * rate / 1_000_000;
      if (!nearlyEqual(cost, expected)) throw new Error(`line cost ${dimension} does not match rate and quantity`);
      calculatedSubtotal += cost;
    } else if ((rate === null) !== (cost === null)) {
      throw new Error(`rate and line cost availability disagree for ${dimension}`);
    }
  }
  if (!nearlyEqual(knownSubtotal, calculatedSubtotal)) throw new Error("known subtotal does not equal known line costs");
  if (pricingComplete) {
    for (const dimension of PRICE_DIMENSIONS) {
      if (rates[dimension] === null || costs[dimension] === null) {
        throw new Error("complete pricing contains an unknown amount");
      }
    }
    if (totalCost === null || missing.size !== 0 || !nearlyEqual(totalCost, knownSubtotal)) {
      throw new Error("complete pricing total or missing dimensions do not reconcile");
    }
  } else if (totalCost !== null) {
    throw new Error("incomplete pricing total must be null");
  }
  return row as unknown as PricingSnapshotV1;
}

function parseRates(value: unknown, path: string): PricingRatesV1 {
  const row = object(value, path);
  exact(row, path, PRICE_DIMENSIONS);
  for (const key of PRICE_DIMENSIONS) nullableMoney(row[key], `${path}.${key}`);
  return row as unknown as PricingRatesV1;
}

function parseQuantities(value: unknown, path: string): BillableQuantitiesV1 {
  const row = object(value, path);
  exact(row, path, PRICE_DIMENSIONS);
  for (const key of PRICE_DIMENSIONS) integer(row[key], `${path}.${key}`);
  return row as unknown as BillableQuantitiesV1;
}

function parseLineCosts(value: unknown, path: string): PricingLineCostsV1 {
  const row = object(value, path);
  exact(row, path, PRICE_DIMENSIONS);
  for (const key of PRICE_DIMENSIONS) nullableMoney(row[key], `${path}.${key}`);
  return row as unknown as PricingLineCostsV1;
}

function documentObject(source: string, kind: string): JsonObject {
  assertValidUnicode(source, "JSON source");
  const bytes = Buffer.byteLength(source, "utf8");
  if (bytes > MAX_JSON_BYTES) throw new Error(`${kind} exceeds ${MAX_JSON_BYTES} bytes`);
  rejectDuplicateJsonKeys(source);
  let value: unknown;
  try {
    value = JSON.parse(source);
  } catch (error) {
    throw new Error(`decode ${kind}: ${message(error)}`);
  }
  assertJsonUnicode(value, "$", new Set<unknown>());
  return object(value, "$" );
}

function object(value: unknown, path: string): JsonObject {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new Error(`${path} must be an object`);
  return value as JsonObject;
}

function exact(value: JsonObject, path: string, allowed: readonly string[], required: readonly string[] = allowed): void {
  for (const key of Object.keys(value)) {
    if (!allowed.includes(key)) throw new Error(`unknown exact JSON object key "${key}" at ${path}`);
  }
  for (const key of required) {
    if (!Object.prototype.hasOwnProperty.call(value, key)) throw new Error(`missing required JSON object key "${key}" at ${path}`);
  }
}

function boundedArray(value: unknown, path: string, maximum: number): unknown[] {
  if (!Array.isArray(value)) throw new Error(`${path} must be an array`);
  if (value.length > maximum) throw new Error(`${path} exceeds ${maximum} items`);
  return value;
}

function stringArray(value: unknown, path: string, maximum: number, maximumText: number): string[] {
  return boundedArray(value, path, maximum).map((item, index) => text(item, `${path}[${index}]`, maximumText));
}

function idArray(value: unknown, path: string, maximum: number, unique = false): string[] {
  const values = boundedArray(value, path, maximum);
  const seen = new Set<string>();
  return values.map((item, index) => {
    const result = id(item, `${path}[${index}]`);
    if (unique) addUnique(seen, result, `${path} ID`);
    return result;
  });
}

function text(value: unknown, path: string, maximum: number, nonempty = false): string {
  if (typeof value !== "string") throw new Error(`${path} must be a string`);
  if (nonempty && value.length === 0) throw new Error(`${path} must not be empty`);
  if (Array.from(value).length > maximum) throw new Error(`${path} exceeds ${maximum} characters`);
  return value;
}

function nullableText(value: unknown, path: string, maximum: number): string | null {
  if (value === null) return null;
  return text(value, path, maximum);
}

function id(value: unknown, path: string): string {
  const result = text(value, path, 256, true);
  if (!ID.test(result)) throw new Error(`${path} must be a valid ID`);
  return result;
}

function digest(value: unknown, path: string): string {
  if (typeof value !== "string" || !DIGEST.test(value)) throw new Error(`${path} must be a sha256 digest`);
  return value;
}

function nullableDigest(value: unknown, path: string): string | null {
  if (value === null) return null;
  return digest(value, path);
}

function sha256(value: unknown, path: string): string {
  if (typeof value !== "string" || !SHA256.test(value)) throw new Error(`${path} must be a lowercase SHA-256 value`);
  return value;
}

function integer(value: unknown, path: string): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value)) throw new Error(`${path} must be a safe integer`);
  if (value < 0) throw new Error(`${path} must be nonnegative`);
  return value;
}

function positiveInteger(value: unknown, path: string): number {
  const result = integer(value, path);
  if (result < 1) throw new Error(`${path} must be positive`);
  return result;
}

function nullableInteger(value: unknown, path: string): number | null {
  if (value === null) return null;
  return integer(value, path);
}

function money(value: unknown, path: string): number {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
    throw new Error(`${path} must be finite and nonnegative`);
  }
  return value;
}

function nullableMoney(value: unknown, path: string): number | null {
  if (value === null) return null;
  return money(value, path);
}

function boolean(value: unknown, path: string): boolean {
  if (typeof value !== "boolean") throw new Error(`${path} must be a boolean`);
  return value;
}

function constant(value: unknown, expected: unknown, path: string): void {
  if (value !== expected) throw new Error(`${path} must equal ${String(expected)}`);
}

function version(value: unknown, path: string): void {
  constant(value, "0.4.0", path);
}

function oneOf<T extends string>(value: unknown, path: string, allowed: readonly T[]): T {
  if (typeof value !== "string" || !allowed.includes(value as T)) throw new Error(`${path} is not in the closed enum`);
  return value as T;
}

function httpsURL(value: unknown, path: string): string {
  const result = text(value, path, 2048, true);
  if (!result.startsWith("https://") || result.length <= "https://".length || /[\s\p{Cc}]/u.test(result)) {
    throw new Error(`${path} must be an HTTPS URL without whitespace or control characters`);
  }
  return result;
}

function nullableURL(value: unknown, path: string): string | null {
  if (value === null) return null;
  return httpsURL(value, path);
}

function addUnique(seen: Set<string>, value: string, kind: string): void {
  if (seen.has(value)) throw new Error(`duplicate ${kind} "${value}"`);
  seen.add(value);
}

function identityKey(provider: string, sessionID: string): string {
  return `${provider}\u0000${sessionID}`;
}

function checkedAdd(left: number, right: number, path: string): number {
  if (left > MAX_SAFE - right) throw new Error(`${path} addition overflow`);
  return left + right;
}

function checkedSum(path: string, ...values: number[]): number {
  let total = 0;
  for (const value of values) total = checkedAdd(total, value, path);
  return total;
}

function sameIndexCoverage(left: SessionIndexCoverageV1, right: SessionIndexCoverageV1): boolean {
  return Object.keys(left).every((key) => left[key as keyof SessionIndexCoverageV1] === right[key as keyof SessionIndexCoverageV1]) &&
    Object.keys(right).length === Object.keys(left).length;
}

function compareGoStrings(left: string, right: string): number {
  return Buffer.compare(Buffer.from(left, "utf8"), Buffer.from(right, "utf8"));
}

function compareIndexEntries(left: SessionIndexEntryV1, right: SessionIndexEntryV1): number {
  const started = compareGoStrings(right.started_at, left.started_at);
  if (started !== 0) return started;
  const provider = compareGoStrings(left.provider, right.provider);
  return provider !== 0 ? provider : compareGoStrings(left.session_id, right.session_id);
}

function compareSummaryEntry(
  left: SessionSummaryEntryV1 | SessionSummaryErrorEntryV1 | SessionEventItemV1,
  right: SessionSummaryEntryV1 | SessionSummaryErrorEntryV1 | SessionEventItemV1
): number {
  const occurred = compareGoStrings(left.occurred_at, right.occurred_at);
  if (occurred !== 0) return occurred;
  if (left.sequence !== right.sequence) return left.sequence - right.sequence;
  return compareGoStrings(left.revision_id, right.revision_id);
}

function assertCanonicalOrder<T>(values: readonly T[], compare: (left: T, right: T) => number, kind: string): void {
  for (let index = 1; index < values.length; index += 1) {
    if (compare(values[index - 1], values[index]) > 0) throw new Error(`${kind} are not in canonical order`);
  }
}

function decisionCycle(decisions: ReadonlyMap<string, DecisionV4>): boolean {
  const state = new Map<string, number>();
  const visit = (decisionID: string): boolean => {
    if (state.get(decisionID) === 1) return true;
    if (state.get(decisionID) === 2) return false;
    state.set(decisionID, 1);
    for (const target of decisions.get(decisionID)?.supersedes ?? []) {
      if (visit(target)) return true;
    }
    state.set(decisionID, 2);
    return false;
  };
  for (const decisionID of decisions.keys()) if (visit(decisionID)) return true;
  return false;
}

function nearlyEqual(left: number, right: number): boolean {
  return Math.abs(left - right) <= 1e-12 * Math.max(1, Math.abs(left), Math.abs(right));
}

function canonicalIndexDigest(index: SessionIndexV1): string {
  const body = {
    schema_version: index.schema_version,
    minimum_reader_version: index.minimum_reader_version,
    project_id: index.project_id,
    generation_id: index.generation_id,
    project_view_digest: index.project_view_digest,
    generated_at: index.generated_at,
    sort_version: index.sort_version,
    coverage: orderedIndexCoverage(index.coverage),
    sessions: index.sessions.map(orderedIndexEntry)
  };
  return `sha256:${sha256Text(goJSON(body))}`;
}

function canonicalLedgerSHA256(ledger: MachineLedgerV4): string {
  const body = {
    schema_version: ledger.schema_version,
    minimum_reader_version: ledger.minimum_reader_version,
    minimum_writer_version: ledger.minimum_writer_version,
    project_id: ledger.project_id,
    generation_id: ledger.generation_id,
    project_view_digest: ledger.project_view_digest,
    accepted_revision: ledger.accepted_revision,
    review_sha256: ledger.review_sha256,
    history_sha256: ledger.history_sha256,
    accounting: orderedAccounting(ledger.accounting),
    sessions: ledger.sessions.map(orderedLedgerSession),
    human_patches: ledger.human_patches.map(orderedPatch),
    orphan_patches: ledger.orphan_patches.map(orderedPatch),
    generated_baselines: ledger.generated_baselines.map(orderedBaseline),
    pricing_snapshots: ledger.pricing_snapshots.map(orderedPricingSnapshot),
    current_pricing_snapshot_ids: ledger.current_pricing_snapshot_ids,
    sync_hashes: {
      review_sha256: ledger.sync_hashes.review_sha256,
      history_sha256: ledger.sync_hashes.history_sha256,
      session_index_digest: ledger.sync_hashes.session_index_digest
    }
  };
  return sha256Text(goJSON(body));
}

function orderedIndexCoverage(value: SessionIndexCoverageV1): SessionIndexCoverageV1 {
  return {
    total: value.total,
    complete: value.complete,
    partial: value.partial,
    error: value.error,
    unprocessed: value.unprocessed,
    source_available: value.source_available,
    source_unavailable: value.source_unavailable,
    started_at_known: value.started_at_known,
    ended_at_known: value.ended_at_known,
    usage_known: value.usage_known
  };
}

function orderedCoverage(value: CoverageV1): CoverageV1 {
  return {
    seen: value.seen,
    indexed: value.indexed,
    collapsed: value.collapsed,
    unprojected: value.unprojected,
    undecodable: value.undecodable,
    truncated: value.truncated
  };
}

function orderedFactCounts(value: SessionFactCountsV1): SessionFactCountsV1 {
  return {
    file_change: value.file_change,
    command: value.command,
    verification: value.verification,
    error: value.error,
    artifact: value.artifact
  };
}

function orderedIndexEntry(value: SessionIndexEntryV1): JsonObject {
  return {
    provider: value.provider,
    session_id: value.session_id,
    processing_state: value.processing_state,
    state_reason_codes: value.state_reason_codes,
    source_availability: value.source_availability,
    source_terminal_state: value.source_terminal_state,
    started_at: value.started_at,
    ended_at: value.ended_at,
    duration_ms: value.duration_ms,
    warning_count: value.warning_count,
    record_count: value.record_count,
    indexed_event_count: value.indexed_event_count,
    coverage: orderedCoverage(value.coverage),
    fact_counts: orderedFactCounts(value.fact_counts),
    session_view_digest: value.session_view_digest,
    usage_record_digest: value.usage_record_digest,
    summary_digest: value.summary_digest,
    last_seen_generation_id: value.last_seen_generation_id,
    last_successful_generation_id: value.last_successful_generation_id
  };
}

function orderedAccounting(value: LedgerAccountingV4): JsonObject {
  return {
    total_duration_ms: value.total_duration_ms,
    total_tokens: value.total_tokens,
    total_cost_usd: value.total_cost_usd,
    models: value.models.map((model) => ({
      model: model.model,
      total_tokens: model.total_tokens,
      total_cost_usd: model.total_cost_usd
    }))
  };
}

function orderedLedgerSession(value: LedgerSessionV4): JsonObject {
  return {
    provider: value.provider,
    session_id: value.session_id,
    processing_state: value.processing_state,
    source_availability: value.source_availability,
    session_view_digest: value.session_view_digest,
    usage_record_digest: value.usage_record_digest
  };
}

function orderedPatch(value: HumanPatchV4): JsonObject {
  return {
    entity_id: value.entity_id,
    field: value.field,
    operation: value.operation,
    ...(Object.prototype.hasOwnProperty.call(value, "value") ? { value: value.value } : {}),
    ...(Object.prototype.hasOwnProperty.call(value, "values") ? { values: value.values } : {}),
    base_generated_hash: value.base_generated_hash
  };
}

function orderedBaseline(value: GeneratedBaselineV4): JsonObject {
  return {
    generation_id: value.generation_id,
    entity_id: value.entity_id,
    field: value.field,
    kind: value.kind,
    ...(Object.prototype.hasOwnProperty.call(value, "value") ? { value: value.value } : {}),
    ...(Object.prototype.hasOwnProperty.call(value, "values") ? { values: value.values } : {}),
    generated_hash: value.generated_hash
  };
}

function orderedPricingSnapshot(value: PricingSnapshotV1): JsonObject {
  return {
    schema_version: value.schema_version,
    minimum_reader_version: value.minimum_reader_version,
    snapshot_id: value.snapshot_id,
    project_id: value.project_id,
    provider: value.provider,
    session_id: value.session_id,
    usage_record_digest: value.usage_record_digest,
    billing_host: value.billing_host,
    billed_model_id: value.billed_model_id,
    billing_mode: value.billing_mode,
    billing_rule_version: value.billing_rule_version,
    region: value.region,
    priced_at: value.priced_at,
    created_at: value.created_at,
    status: value.status,
    modelpricewatch_listing_id: value.modelpricewatch_listing_id,
    source_kind: value.source_kind,
    source_url: value.source_url,
    detail_url: value.detail_url,
    source_last_updated: value.source_last_updated,
    retrieved_at: value.retrieved_at,
    promo: value.promo,
    promo_until: value.promo_until,
    rates: orderedPriceDimensions(value.rates),
    billable_quantities: orderedPriceDimensions(value.billable_quantities),
    line_costs_usd: orderedPriceDimensions(value.line_costs_usd),
    missing_billing_dimensions: value.missing_billing_dimensions,
    known_subtotal_usd: value.known_subtotal_usd,
    total_cost_usd: value.total_cost_usd,
    pricing_complete: value.pricing_complete,
    supersedes_snapshot_id: value.supersedes_snapshot_id,
    audit_reason: value.audit_reason
  };
}

function orderedPriceDimensions(value: PricingRatesV1 | BillableQuantitiesV1 | PricingLineCostsV1): JsonObject {
  return {
    input: value.input,
    cached_input: value.cached_input,
    cache_write_input: value.cache_write_input,
    output: value.output,
    reasoning_output: value.reasoning_output
  };
}

function goJSON(value: unknown): string {
  if (value === null) return "null";
  if (typeof value === "string") return goJSONString(value);
  if (typeof value === "number") {
    if (!Number.isFinite(value)) throw new Error("canonical JSON cannot encode a non-finite number");
    if (Object.is(value, -0)) return "-0";
    return String(value);
  }
  if (typeof value === "boolean") return value ? "true" : "false";
  if (Array.isArray(value)) return `[${value.map(goJSON).join(",")}]`;
  if (typeof value === "object" && value !== null) {
    const entries = Object.entries(value as JsonObject).filter(([, child]) => child !== undefined);
    return `{${entries.map(([key, child]) => `${goJSONString(key)}:${goJSON(child)}`).join(",")}}`;
  }
  throw new Error("canonical JSON contains an unsupported value");
}

function goJSONString(value: string): string {
  return JSON.stringify(value).replace(/[<>&\u2028\u2029]/g, (character) => {
    const code = character.codePointAt(0);
    return `\\u${code?.toString(16).padStart(4, "0")}`;
  });
}

function assertValidUnicode(value: string, path: string): void {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) throw new Error(`${path} contains an unpaired Unicode surrogate`);
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      throw new Error(`${path} contains an unpaired Unicode surrogate`);
    }
  }
}

function assertJsonUnicode(value: unknown, path: string, seen: Set<unknown>): void {
  if (typeof value === "string") {
    assertValidUnicode(value, path);
    return;
  }
  if (typeof value !== "object" || value === null) return;
  if (seen.has(value)) return;
  seen.add(value);
  if (Array.isArray(value)) {
    value.forEach((child, index) => assertJsonUnicode(child, `${path}[${index}]`, seen));
    return;
  }
  for (const [key, child] of Object.entries(value as JsonObject)) {
    assertValidUnicode(key, `${path} key`);
    assertJsonUnicode(child, `${path}.${key}`, seen);
  }
}

function rejectDuplicateJsonKeys(source: string): void {
  let cursor = 0;
  const whitespace = (): void => {
    while (source[cursor] === " " || source[cursor] === "\t" || source[cursor] === "\r" || source[cursor] === "\n") cursor += 1;
  };
  const parseString = (): string => {
    const start = cursor;
    cursor += 1;
    while (cursor < source.length) {
      if (source[cursor] === "\\") {
        cursor += 2;
        continue;
      }
      if (source[cursor] === '"') {
        cursor += 1;
        try {
          const decoded = JSON.parse(source.slice(start, cursor)) as unknown;
          if (typeof decoded !== "string") throw new Error("not a string");
          assertValidUnicode(decoded, "JSON string");
          return decoded;
        } catch (error) {
          throw new Error(`decode JSON: malformed string: ${message(error)}`);
        }
      }
      cursor += 1;
    }
    throw new Error("decode JSON: unterminated string");
  };
  const parseValue = (): void => {
    whitespace();
    const token = source[cursor];
    if (token === "{") {
      cursor += 1;
      whitespace();
      const keys = new Set<string>();
      if (source[cursor] === "}") {
        cursor += 1;
        return;
      }
      while (cursor < source.length) {
        whitespace();
        if (source[cursor] !== '"') throw new Error("decode JSON: object key must be a string");
        const key = parseString();
        if (keys.has(key)) throw new Error(`duplicate JSON object key "${key}"`);
        keys.add(key);
        whitespace();
        if (source[cursor] !== ":") throw new Error("decode JSON: missing object colon");
        cursor += 1;
        parseValue();
        whitespace();
        if (source[cursor] === "}") {
          cursor += 1;
          return;
        }
        if (source[cursor] !== ",") throw new Error("decode JSON: malformed object");
        cursor += 1;
      }
      throw new Error("decode JSON: unterminated object");
    }
    if (token === "[") {
      cursor += 1;
      whitespace();
      if (source[cursor] === "]") {
        cursor += 1;
        return;
      }
      while (cursor < source.length) {
        parseValue();
        whitespace();
        if (source[cursor] === "]") {
          cursor += 1;
          return;
        }
        if (source[cursor] !== ",") throw new Error("decode JSON: malformed array");
        cursor += 1;
      }
      throw new Error("decode JSON: unterminated array");
    }
    if (token === '"') {
      parseString();
      return;
    }
    const primitive = /^(?:-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false|null)/.exec(source.slice(cursor));
    if (!primitive) throw new Error("decode JSON: malformed value");
    cursor += primitive[0].length;
  };
  parseValue();
  whitespace();
  if (cursor !== source.length) throw new Error("decode JSON: trailing data");
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
