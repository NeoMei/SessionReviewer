import type {
  MachineLedger,
  ModelAccounting,
  Pricing,
  ProjectAccounting,
  ProjectModelSummary,
  SessionAccounting,
  SessionReport
} from "../contracts/review-v2";

const MAX_LEDGER_BYTES = 16 << 20;
const SHA256 = /^[0-9a-f]{64}$/;
const EPSILON = 1e-9;

type JsonObject = Record<string, unknown>;

export function parseLedger(source: string): MachineLedger {
  if (Buffer.byteLength(source, "utf8") > MAX_LEDGER_BYTES) throw new Error(`machine ledger exceeds ${MAX_LEDGER_BYTES} bytes`);
  rejectDuplicateJsonKeys(source);
  let value: unknown;
  try {
    value = JSON.parse(source);
  } catch (error) {
    throw new Error(`decode machine ledger: ${message(error)}`);
  }
  const root = object(value, "$", [
    "schema_version", "project_id", "accepted_revision", "review_sha256", "history_sha256",
    "last_successful_sync", "accounting", "sessions", "evidence", "legacy_compatibility"
  ], ["schema_version", "project_id", "accepted_revision", "review_sha256", "history_sha256", "accounting", "sessions", "evidence", "legacy_compatibility"]);
  if (integer(root.schema_version, "$.schema_version") !== 2) throw new Error("unsupported review ledger schema version");
  const projectId = nonempty(root.project_id, "$.project_id");
  const acceptedRevision = integer(root.accepted_revision, "$.accepted_revision");
  const reviewSha256 = hash(root.review_sha256, "$.review_sha256");
  const historySha256 = hash(root.history_sha256, "$.history_sha256");
  const parsedAccounting = parseProjectAccounting(root.accounting, "$.accounting");
  const sessions = array(root.sessions, "$.sessions").map((item, index) => parseSession(item, `$.sessions[${index}]`, projectId));
  unique(sessions.map((session) => session.id), "session report identity");
  unique(sessions.map((session) => session.sessionId), "session identity");
  const accounting = {
    ...parsedAccounting,
    pricingComplete: sessions.every((session) => session.accounting === undefined || session.accounting.pricingComplete)
  };
  validateProjectAggregate(accounting, sessions);

  const sessionIds = new Set(sessions.map((session) => session.sessionId));
  const evidence = parseEvidence(root.evidence, sessionIds);
  const legacyCompatibility = parseLegacy(root.legacy_compatibility, projectId, sessionIds);
  const lastSuccessfulSync = root.last_successful_sync === undefined ? undefined : string(root.last_successful_sync, "$.last_successful_sync");
  return {
    schemaVersion: 2,
    projectId,
    acceptedRevision,
    reviewSha256,
    historySha256,
    ...(lastSuccessfulSync === undefined ? {} : { lastSuccessfulSync }),
    accounting,
    sessions,
    evidence,
    legacyCompatibility
  };
}

function parseProjectAccounting(value: unknown, path: string): ProjectAccounting {
  const row = object(value, path, ["total_duration_ms", "total_tokens", "total_cost_usd", "models"]);
  const models = array(row.models, `${path}.models`).map((item, index): ProjectModelSummary => {
    const model = object(item, `${path}.models[${index}]`, ["model", "total_tokens", "total_cost_usd", "token_share_pct", "cost_share_pct"]);
    return {
      model: nonempty(model.model, `${path}.models[${index}].model`),
      totalTokens: integer(model.total_tokens, `${path}.models[${index}].total_tokens`),
      totalCostUsd: finite(model.total_cost_usd, `${path}.models[${index}].total_cost_usd`),
      tokenSharePct: finite(model.token_share_pct, `${path}.models[${index}].token_share_pct`),
      costSharePct: finite(model.cost_share_pct, `${path}.models[${index}].cost_share_pct`)
    };
  });
  unique(models.map((model) => model.model), "project accounting model");
  const result = {
    totalDurationMs: integer(row.total_duration_ms, `${path}.total_duration_ms`),
    totalTokens: integer(row.total_tokens, `${path}.total_tokens`),
    totalCostUsd: finite(row.total_cost_usd, `${path}.total_cost_usd`),
    models,
    pricingComplete: true
  };
  const tokens = sum(models.map((model) => model.totalTokens));
  const cost = sum(models.map((model) => model.totalCostUsd));
  if (tokens !== result.totalTokens || !near(cost, result.totalCostUsd)) throw new Error("project aggregate total differs from model rows");
  for (const model of models) {
    const tokenShare = result.totalTokens === 0 ? 0 : model.totalTokens / result.totalTokens * 100;
    const costShare = result.totalCostUsd === 0 ? 0 : model.totalCostUsd / result.totalCostUsd * 100;
    if (!near(tokenShare, model.tokenSharePct) || !near(costShare, model.costSharePct)) throw new Error(`project aggregate shares differ for model "${model.model}"`);
  }
  return result;
}

function parseSession(value: unknown, path: string, projectId: string): SessionReport {
  const allowed = [
    "id", "project_id", "session_id", "revision", "initial_goal", "goal_changes", "phases", "files", "commits",
    "verification", "decisions_added", "decisions_revised", "open_loops_created", "open_loops_closed",
    "previous_session_id", "next_session_id", "evidence", "accounting"
  ];
  const required = allowed.filter((key) => key !== "accounting");
  const row = object(value, path, allowed, required);
  const id = nonempty(row.id, `${path}.id`);
  if (string(row.project_id, `${path}.project_id`) !== projectId) throw new Error(`session "${id}" has a different project ID`);
  integer(row.revision, `${path}.revision`);
  string(row.initial_goal, `${path}.initial_goal`);
  for (const key of ["goal_changes", "files", "commits", "verification", "decisions_added", "decisions_revised", "open_loops_created", "open_loops_closed"] as const) strings(row[key], `${path}.${key}`);
  array(row.phases, `${path}.phases`).forEach((phase, index) => {
    const item = object(phase, `${path}.phases[${index}]`, ["title", "summary", "evidence"]);
    string(item.title, `${path}.phases[${index}].title`);
    string(item.summary, `${path}.phases[${index}].summary`);
    validateEvidenceRefs(item.evidence, `${path}.phases[${index}].evidence`);
  });
  validateEvidenceRefs(row.evidence, `${path}.evidence`);
  const accounting = row.accounting === undefined ? undefined : parseSessionAccounting(row.accounting, `${path}.accounting`);
  return {
    id,
    projectId,
    sessionId: nonempty(row.session_id, `${path}.session_id`),
    previousSessionId: string(row.previous_session_id, `${path}.previous_session_id`),
    nextSessionId: string(row.next_session_id, `${path}.next_session_id`),
    ...(accounting === undefined ? {} : { accounting })
  };
}

function parseSessionAccounting(value: unknown, path: string): SessionAccounting {
  const row = object(value, path, ["started_at", "ended_at", "duration_ms", "models", "total_tokens", "total_cost_usd"]);
  const startedAt = isoTime(row.started_at, `${path}.started_at`);
  const endedAt = isoTime(row.ended_at, `${path}.ended_at`);
  const durationMs = integer(row.duration_ms, `${path}.duration_ms`);
  if (Date.parse(endedAt) - Date.parse(startedAt) !== durationMs) throw new Error(`${path} duration does not match timestamps`);
  const models = array(row.models, `${path}.models`).map((item, index) => parseModelAccounting(item, `${path}.models[${index}]`));
  const sorted = [...models].sort((left, right) => left.model.localeCompare(right.model));
  if (models.some((model, index) => model.model !== sorted[index]?.model)) throw new Error(`${path} models must be unique and sorted`);
  unique(models.map((model) => model.model), "session accounting model");
  const totalTokens = integer(row.total_tokens, `${path}.total_tokens`);
  const totalCostUsd = finite(row.total_cost_usd, `${path}.total_cost_usd`);
  if (sum(models.map((model) => model.totalTokens)) !== totalTokens || !near(sum(models.map((model) => model.costUsd)), totalCostUsd)) {
    throw new Error(`${path} aggregate total differs from model rows`);
  }
  return { startedAt, endedAt, durationMs, models, totalTokens, totalCostUsd, pricingComplete: models.every((model) => model.pricing !== undefined) };
}

function parseModelAccounting(value: unknown, path: string): ModelAccounting {
  const row = object(value, path, [
    "model", "input_tokens", "cached_input_tokens", "cache_write_input_tokens", "output_tokens",
    "reasoning_output_tokens", "total_tokens", "pricing", "cost_usd"
  ]);
  const inputTokens = integer(row.input_tokens, `${path}.input_tokens`);
  const cachedInputTokens = integer(row.cached_input_tokens, `${path}.cached_input_tokens`);
  const cacheWriteInputTokens = integer(row.cache_write_input_tokens, `${path}.cache_write_input_tokens`);
  const outputTokens = integer(row.output_tokens, `${path}.output_tokens`);
  const reasoningOutputTokens = integer(row.reasoning_output_tokens, `${path}.reasoning_output_tokens`);
  const totalTokens = integer(row.total_tokens, `${path}.total_tokens`);
  if (cachedInputTokens + cacheWriteInputTokens > inputTokens || totalTokens !== inputTokens + outputTokens) {
    throw new Error(`${path} token totals are invalid`);
  }
  const pricing = parsePricing(row.pricing, `${path}.pricing`);
  const costUsd = finite(row.cost_usd, `${path}.cost_usd`);
  if (pricing === undefined) {
    if (costUsd !== 0) throw new Error(`${path} unknown pricing must not claim a cost`);
  } else {
    const uncached = inputTokens - cachedInputTokens - cacheWriteInputTokens;
    const calculated = (uncached * pricing.inputPerMillion + cachedInputTokens * pricing.cachedInputPerMillion + cacheWriteInputTokens * pricing.cacheWriteInputPerMillion + outputTokens * pricing.outputPerMillion) / 1_000_000;
    if (!near(calculated, costUsd)) throw new Error(`${path} cost does not match pricing`);
  }
  return {
    model: nonempty(row.model, `${path}.model`), inputTokens, cachedInputTokens, cacheWriteInputTokens,
    outputTokens, reasoningOutputTokens, totalTokens, ...(pricing === undefined ? {} : { pricing }), costUsd
  };
}

function parsePricing(value: unknown, path: string): Pricing | undefined {
  const row = object(value, path, ["currency", "input_per_million", "cached_input_per_million", "cache_write_input_per_million", "output_per_million", "source", "as_of"]);
  if (row.currency === "" && row.input_per_million === 0 && row.cached_input_per_million === 0 &&
      row.cache_write_input_per_million === 0 && row.output_per_million === 0 && row.source === "" && row.as_of === "") {
    return undefined;
  }
  if (row.currency !== "USD") throw new Error(`${path}.currency must be USD`);
  const source = nonempty(row.source, `${path}.source`);
  let url: URL;
  try { url = new URL(source); } catch { throw new Error(`${path}.source must be an HTTPS URL`); }
  if (url.protocol !== "https:" || url.username || url.password) throw new Error(`${path}.source must be an HTTPS URL without credentials`);
  const asOf = string(row.as_of, `${path}.as_of`);
  if (!/^\d{4}-\d{2}-\d{2}$/.test(asOf) || Number.isNaN(Date.parse(`${asOf}T00:00:00Z`))) throw new Error(`${path}.as_of must use YYYY-MM-DD`);
  return {
    currency: "USD",
    inputPerMillion: finite(row.input_per_million, `${path}.input_per_million`),
    cachedInputPerMillion: finite(row.cached_input_per_million, `${path}.cached_input_per_million`),
    cacheWriteInputPerMillion: finite(row.cache_write_input_per_million, `${path}.cache_write_input_per_million`),
    outputPerMillion: finite(row.output_per_million, `${path}.output_per_million`),
    source,
    asOf
  };
}

function parseEvidence(value: unknown, sessionIds: Set<string>): ReadonlyMap<string, readonly unknown[]> {
  const result = new Map<string, readonly unknown[]>();
  array(value, "$.evidence").forEach((entry, index) => {
    const row = object(entry, `$.evidence[${index}]`, ["id", "refs"]);
    const id = nonempty(row.id, `$.evidence[${index}].id`);
    if (result.has(id)) throw new Error(`duplicate evidence owner identity "${id}"`);
    result.set(id, validateEvidenceRefs(row.refs, `$.evidence[${index}].refs`, sessionIds));
  });
  return result;
}

function validateEvidenceRefs(value: unknown, path: string, sessionIds?: Set<string>): readonly unknown[] {
  const seen = new Set<string>();
  return array(value, path).map((entry, index) => {
    const row = object(entry, `${path}[${index}]`, ["evidence_id", "session_id", "jsonl_line", "source_hash", "summary"]);
    const id = nonempty(row.evidence_id, `${path}[${index}].evidence_id`);
    if (seen.has(id)) throw new Error(`duplicate evidence identity "${id}"`);
    seen.add(id);
    const sessionId = nonempty(row.session_id, `${path}[${index}].session_id`);
    if (sessionIds && !sessionIds.has(sessionId)) throw new Error(`evidence "${id}" references missing session "${sessionId}"`);
    positiveInteger(row.jsonl_line, `${path}[${index}].jsonl_line`);
    hash(row.source_hash, `${path}[${index}].source_hash`);
    string(row.summary, `${path}[${index}].summary`);
    return row;
  });
}

function parseLegacy(value: unknown, projectId: string, sessionIds: Set<string>): Readonly<Record<string, unknown>> {
  const legacy = object(value, "$.legacy_compatibility", ["current_state", "timeline", "decisions", "open_loops", "current_risks"]);
  const current = object(legacy.current_state, "$.legacy_compatibility.current_state", [
    "project_id", "revision", "goal", "last_verified", "branch", "uncommitted_changes", "blockers", "open_risks",
    "next_action", "first_inspection", "last_updated", "source_sessions", "evidence"
  ]);
  if (string(current.project_id, "legacy.current_state.project_id") !== projectId) throw new Error("legacy current-state project ID differs");
  integer(current.revision, "legacy.current_state.revision");
  for (const key of ["goal", "last_verified", "branch", "next_action", "first_inspection", "last_updated"] as const) string(current[key], `legacy.current_state.${key}`);
  for (const key of ["uncommitted_changes", "blockers", "open_risks", "source_sessions"] as const) strings(current[key], `legacy.current_state.${key}`);
  validateEvidenceRefs(current.evidence, "legacy.current_state.evidence", sessionIds);
  const timeline = array(legacy.timeline, "legacy.timeline");
  const decisions = array(legacy.decisions, "legacy.decisions");
  const loops = array(legacy.open_loops, "legacy.open_loops");
  const risks = array(legacy.current_risks, "legacy.current_risks");
  const decisionIds = decisions.map((item, index) => validateLegacyDecision(item, index, projectId, sessionIds));
  const loopIds = loops.map((item, index) => validateLegacyLoop(item, index, projectId, sessionIds));
  const timelineIds = timeline.map((item, index) => validateLegacyEvent(item, index, new Set(decisionIds), new Set(loopIds), sessionIds));
  unique(decisionIds, "legacy decision identity");
  unique(loopIds, "legacy open-loop identity");
  unique(timelineIds, "legacy timeline identity");
  risks.forEach((item, index) => {
    const row = object(item, `legacy.current_risks[${index}]`, ["risk_id", "kind", "source_key"]);
    nonempty(row.risk_id, `legacy.current_risks[${index}].risk_id`);
    if (row.kind !== "blocker" && row.kind !== "open_risk") throw new Error("legacy risk kind is invalid");
    hash(row.source_key, `legacy.current_risks[${index}].source_key`);
  });
  return legacy;
}

function validateLegacyEvent(value: unknown, index: number, decisions: Set<string>, loops: Set<string>, sessions: Set<string>): string {
  const path = `legacy.timeline[${index}]`;
  const row = object(value, path, ["id", "occurred_at", "revision", "class", "title", "summary", "evidence", "decision_ids", "open_loop_ids"]);
  const id = nonempty(row.id, `${path}.id`);
  string(row.occurred_at, `${path}.occurred_at`); integer(row.revision, `${path}.revision`); string(row.class, `${path}.class`); string(row.title, `${path}.title`); string(row.summary, `${path}.summary`);
  validateEvidenceRefs(row.evidence, `${path}.evidence`, sessions);
  references(strings(row.decision_ids, `${path}.decision_ids`), decisions, `${path}.decision_ids`);
  references(strings(row.open_loop_ids, `${path}.open_loop_ids`), loops, `${path}.open_loop_ids`);
  return id;
}

function validateLegacyDecision(value: unknown, index: number, projectId: string, sessions: Set<string>): string {
  const path = `legacy.decisions[${index}]`;
  const row = object(value, path, ["id", "project_id", "title", "status", "revision", "tags", "supersedes", "source_sessions", "evidence", "context", "rationale", "consequences", "reevaluate_when", "alternatives", "rejected_paths"]);
  const id = nonempty(row.id, `${path}.id`);
  if (string(row.project_id, `${path}.project_id`) !== projectId) throw new Error(`${path} project ID differs`);
  integer(row.revision, `${path}.revision`);
  for (const key of ["title", "status", "context", "rationale", "consequences", "reevaluate_when"] as const) string(row[key], `${path}.${key}`);
  for (const key of ["tags", "supersedes", "alternatives", "rejected_paths"] as const) strings(row[key], `${path}.${key}`);
  references(strings(row.source_sessions, `${path}.source_sessions`), sessions, `${path}.source_sessions`);
  validateEvidenceRefs(row.evidence, `${path}.evidence`, sessions);
  return id;
}

function validateLegacyLoop(value: unknown, index: number, projectId: string, sessions: Set<string>): string {
  const path = `legacy.open_loops[${index}]`;
  const row = object(value, path, ["id", "project_id", "title", "status", "revision", "tags", "source_sessions", "evidence", "question", "attempts", "blocker", "next_experiment", "completion_criterion"]);
  const id = nonempty(row.id, `${path}.id`);
  if (string(row.project_id, `${path}.project_id`) !== projectId) throw new Error(`${path} project ID differs`);
  integer(row.revision, `${path}.revision`);
  for (const key of ["title", "status", "question", "blocker", "next_experiment", "completion_criterion"] as const) string(row[key], `${path}.${key}`);
  strings(row.tags, `${path}.tags`); strings(row.attempts, `${path}.attempts`);
  references(strings(row.source_sessions, `${path}.source_sessions`), sessions, `${path}.source_sessions`);
  validateEvidenceRefs(row.evidence, `${path}.evidence`, sessions);
  return id;
}

function validateProjectAggregate(accounting: ProjectAccounting, sessions: SessionReport[]): void {
  const reports = sessions.flatMap((session) => session.accounting ? [session.accounting] : []);
  if (reports.length === 0) return;
  if (sum(reports.map((item) => item.durationMs)) !== accounting.totalDurationMs || sum(reports.map((item) => item.totalTokens)) !== accounting.totalTokens || !near(sum(reports.map((item) => item.totalCostUsd)), accounting.totalCostUsd)) {
    throw new Error("project aggregate total differs from session rows");
  }
}

function object(value: unknown, path: string, allowed: readonly string[], required: readonly string[] = allowed): JsonObject {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new Error(`${path} must be an object`);
  const result = value as JsonObject;
  for (const key of Object.keys(result)) if (!allowed.includes(key)) throw new Error(`unknown JSON object key "${key}" at ${path}`);
  for (const key of required) if (!(key in result)) throw new Error(`missing required JSON object key "${key}" at ${path}`);
  return result;
}

function array(value: unknown, path: string): unknown[] {
  if (!Array.isArray(value)) throw new Error(`${path} must be an array`);
  return value;
}

function string(value: unknown, path: string): string {
  if (typeof value !== "string") throw new Error(`${path} must be a string`);
  return value;
}

function nonempty(value: unknown, path: string): string {
  const result = string(value, path);
  if (!result.trim()) throw new Error(`${path} is required`);
  return result;
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

function finite(value: unknown, path: string): number {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) throw new Error(`${path} must be finite and nonnegative`);
  return value;
}

function strings(value: unknown, path: string): string[] {
  return array(value, path).map((item, index) => string(item, `${path}[${index}]`));
}

function hash(value: unknown, path: string): string {
  const result = string(value, path);
  if (!SHA256.test(result)) throw new Error(`${path} must be a lowercase SHA-256 value`);
  return result;
}

function isoTime(value: unknown, path: string): string {
  const result = string(value, path);
  if (Number.isNaN(Date.parse(result))) throw new Error(`${path} must be an RFC3339 timestamp`);
  return result;
}

function unique(values: string[], kind: string): void {
  const seen = new Set<string>();
  for (const value of values) {
    if (seen.has(value)) throw new Error(`duplicate ${kind} "${value}"`);
    seen.add(value);
  }
}

function references(values: string[], known: Set<string>, path: string): void {
  for (const value of values) if (!known.has(value)) throw new Error(`${path} references missing identity "${value}"`);
}

function near(left: number, right: number): boolean {
  return Math.abs(left - right) <= EPSILON * Math.max(1, Math.abs(left), Math.abs(right));
}

function sum(values: number[]): number {
  return values.reduce((total, value) => total + value, 0);
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function rejectDuplicateJsonKeys(source: string): void {
  let cursor = 0;
  const whitespace = (): void => { while (/\s/.test(source[cursor] ?? "")) cursor += 1; };
  const parseString = (): string => {
    const start = cursor;
    cursor += 1;
    while (cursor < source.length) {
      if (source[cursor] === "\\") { cursor += 2; continue; }
      if (source[cursor] === '"') {
        cursor += 1;
        try { return JSON.parse(source.slice(start, cursor)) as string; } catch { throw new Error("decode machine ledger: malformed JSON string"); }
      }
      cursor += 1;
    }
    throw new Error("decode machine ledger: unterminated JSON string");
  };
  const parseValue = (): void => {
    whitespace();
    const token = source[cursor];
    if (token === "{") {
      cursor += 1;
      whitespace();
      const keys = new Set<string>();
      if (source[cursor] === "}") { cursor += 1; return; }
      while (cursor < source.length) {
        whitespace();
        if (source[cursor] !== '"') throw new Error("decode machine ledger: object key must be a string");
        const key = parseString();
        if (keys.has(key)) throw new Error(`duplicate JSON object key "${key}"`);
        keys.add(key);
        whitespace();
        if (source[cursor] !== ":") throw new Error("decode machine ledger: missing object colon");
        cursor += 1;
        parseValue();
        whitespace();
        if (source[cursor] === "}") { cursor += 1; return; }
        if (source[cursor] !== ",") throw new Error("decode machine ledger: malformed object");
        cursor += 1;
      }
      throw new Error("decode machine ledger: unterminated object");
    }
    if (token === "[") {
      cursor += 1;
      whitespace();
      if (source[cursor] === "]") { cursor += 1; return; }
      while (cursor < source.length) {
        parseValue();
        whitespace();
        if (source[cursor] === "]") { cursor += 1; return; }
        if (source[cursor] !== ",") throw new Error("decode machine ledger: malformed array");
        cursor += 1;
      }
      throw new Error("decode machine ledger: unterminated array");
    }
    if (token === '"') { parseString(); return; }
    const primitive = /^(?:-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false|null)/.exec(source.slice(cursor));
    if (!primitive) throw new Error("decode machine ledger: malformed JSON value");
    cursor += primitive[0].length;
  };
  parseValue();
  whitespace();
  if (cursor !== source.length) throw new Error("decode machine ledger: trailing JSON data");
}
