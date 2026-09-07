import type { ConversationPageV1, VisibleCoverageV1, VisibleMessageV1, VisibleTurnV1 } from "../contracts/conversation-page";
import { parseStrictWireDocument } from "./contracts-v4";

const ID = /^[A-Za-z0-9][A-Za-z0-9._:-]*$/;
const DIGEST = /^sha256:[0-9a-f]{64}$/;
const SHA256 = /^[0-9a-f]{64}$/;
const ROOT_KEYS = [
  "schema_version", "minimum_reader_version", "mode", "project_id", "provider", "session_id", "generation_id",
  "session_view_digest", "dependency_digest", "redaction_version", "turn_unit_id", "total", "range_start", "range_end",
  "first_cursor", "previous_cursor", "next_cursor", "last_cursor", "turn_units", "messages", "coverage"
] as const;
const MESSAGE_KEYS = ["role", "phase", "revision_id", "source_ref", "occurred_at", "visible_excerpt", "truncated", "text", "text_truncated"] as const;
const SOURCE_KEYS = ["provider", "session_id", "source_identity", "record_ordinal", "source_hash"] as const;
const TURN_KEYS = ["turn_unit_id", "ordinal", "started_at", "ended_at", "user_message", "answer_state", "assistant_message_count"] as const;
const COVERAGE_KEYS = ["source_records", "visible_messages", "captured_messages", "truncated_messages", "truncated_bodies", "context_messages", "orphan_messages", "oversized_records", "malformed_records", "complete"] as const;

export function parseConversationPageV1(source: string): ConversationPageV1 {
  return parseStrictWireDocument(source, "conversation page", parseConversationPageDocument);
}

function parseConversationPageDocument(row: Record<string, unknown>): ConversationPageV1 {
  exact(row, ROOT_KEYS, "$");
  constant(row.schema_version, 1, "$.schema_version");
  constant(row.minimum_reader_version, "0.4.0", "$.minimum_reader_version");
  const mode = choice(row.mode, ["turn_index", "turn_messages"] as const, "$.mode");
  const projectId = id(row.project_id, "$.project_id");
  void projectId;
  constant(row.provider, "codex", "$.provider");
  const sessionId = id(row.session_id, "$.session_id");
  id(row.generation_id, "$.generation_id");
  digest(row.session_view_digest, "$.session_view_digest");
  digest(row.dependency_digest, "$.dependency_digest");
  constant(row.redaction_version, "visible-redaction-v1", "$.redaction_version");
  const turnUnitId = nullableId(row.turn_unit_id, "$.turn_unit_id");
  const total = count(row.total, "$.total");
  const rangeStart = count(row.range_start, "$.range_start");
  const rangeEnd = count(row.range_end, "$.range_end");
  if (rangeStart > rangeEnd || rangeEnd > total) throw new Error("conversation page range does not reconcile");
  for (const key of ["first_cursor", "previous_cursor", "next_cursor", "last_cursor"] as const) cursor(row[key], `$.${key}`);
  const turns = array(row.turn_units, "$.turn_units", 64).map((item, index) => parseTurn(item, `$.turn_units[${index}]`, sessionId, mode));
  const messages = array(row.messages, "$.messages", 64).map((item, index) => parseMessage(item, `$.messages[${index}]`, sessionId, mode === "turn_messages"));
  parseCoverage(row.coverage, "$.coverage");

  if (mode === "turn_index") {
    if (turnUnitId !== null || messages.length !== 0 || turns.length !== rangeEnd - rangeStart) throw new Error("turn index page shape does not reconcile");
    for (let index = 0; index < turns.length; index += 1) {
      if (turns[index]?.ordinal !== rangeStart + index + 1) throw new Error("turn index ordinals are not canonical");
      if (index > 0 && Date.parse(turns[index - 1].started_at) > Date.parse(turns[index].started_at)) throw new Error("turn index is not chronological");
    }
  } else {
    if (turnUnitId === null || turns.length !== 1 || turns[0]?.turn_unit_id !== turnUnitId || messages.length !== rangeEnd - rangeStart) {
      throw new Error("selected turn page shape does not reconcile");
    }
    if (total !== 1 + turns[0].assistant_message_count) throw new Error("selected turn message total does not reconcile");
    if (messages.length > 0) {
      const userCount = messages.filter((message) => message.role === "user").length;
      if (rangeStart === 0 && (messages[0]?.role !== "user" || userCount !== 1)) throw new Error("selected turn must start with one user message");
      if (rangeStart > 0 && userCount !== 0) throw new Error("later selected pages cannot repeat the user message");
    }
    for (let index = 1; index < messages.length; index += 1) {
      if (Date.parse(messages[index - 1].occurred_at) > Date.parse(messages[index].occurred_at)) throw new Error("selected messages are not chronological");
    }
  }
  if (total === 0 && (rangeStart !== 0 || rangeEnd !== 0 || row.first_cursor !== null || row.previous_cursor !== null || row.next_cursor !== null || row.last_cursor !== null)) {
    throw new Error("empty conversation page cannot have cursors");
  }
  return row as unknown as ConversationPageV1;
}

function parseTurn(value: unknown, path: string, sessionId: string, mode: "turn_index" | "turn_messages"): VisibleTurnV1 {
  const row = object(value, path);
  exact(row, TURN_KEYS, path);
  id(row.turn_unit_id, `${path}.turn_unit_id`);
  positive(row.ordinal, `${path}.ordinal`);
  timestamp(row.started_at, `${path}.started_at`);
  if (row.ended_at !== null) timestamp(row.ended_at, `${path}.ended_at`);
  const user = parseMessage(row.user_message, `${path}.user_message`, sessionId, false);
  if (user.role !== "user" || user.phase !== null || user.text !== null || user.text_truncated) throw new Error(`${path}.user_message must be a preview-only user message`);
  const answerState = choice(row.answer_state, ["no_answer", "partial", "answered"] as const, `${path}.answer_state`);
  const assistantCount = count(row.assistant_message_count, `${path}.assistant_message_count`);
  if ((answerState === "no_answer") !== (assistantCount === 0)) throw new Error(`${path}.answer_state does not match assistant count`);
  void mode;
  return row as unknown as VisibleTurnV1;
}

function parseMessage(value: unknown, path: string, sessionId: string, selected: boolean): VisibleMessageV1 {
  const row = object(value, path);
  exact(row, MESSAGE_KEYS, path);
  const role = choice(row.role, ["user", "assistant"] as const, `${path}.role`);
  const phase = row.phase === null ? null : choice(row.phase, ["commentary", "final_answer"] as const, `${path}.phase`);
  if (role === "user" ? phase !== null : false) throw new Error(`${path}.phase is invalid for user role`);
  digest(row.revision_id, `${path}.revision_id`);
  const source = object(row.source_ref, `${path}.source_ref`);
  exact(source, SOURCE_KEYS, `${path}.source_ref`);
  constant(source.provider, "codex", `${path}.source_ref.provider`);
  if (id(source.session_id, `${path}.source_ref.session_id`) !== sessionId) throw new Error(`${path}.source_ref crosses Session`);
  id(source.source_identity, `${path}.source_ref.source_identity`);
  positive(source.record_ordinal, `${path}.source_ref.record_ordinal`);
  if (typeof source.source_hash !== "string" || !SHA256.test(source.source_hash)) throw new Error(`${path}.source_ref.source_hash is invalid`);
  timestamp(row.occurred_at, `${path}.occurred_at`);
  text(row.visible_excerpt, `${path}.visible_excerpt`, 4096);
  bool(row.truncated, `${path}.truncated`);
  if (selected) text(row.text, `${path}.text`, 65536);
  else if (row.text !== null) throw new Error(`${path}.text must be null in a preview`);
  bool(row.text_truncated, `${path}.text_truncated`);
  if (!selected && row.text_truncated) throw new Error(`${path}.text_truncated must be false in a preview`);
  return row as unknown as VisibleMessageV1;
}

function parseCoverage(value: unknown, path: string): VisibleCoverageV1 {
  const row = object(value, path);
  exact(row, COVERAGE_KEYS, path);
  const sourceRecords = count(row.source_records, `${path}.source_records`);
  const visible = count(row.visible_messages, `${path}.visible_messages`);
  const captured = count(row.captured_messages, `${path}.captured_messages`);
  const truncated = count(row.truncated_messages, `${path}.truncated_messages`);
  const truncatedBodies = count(row.truncated_bodies, `${path}.truncated_bodies`);
  count(row.context_messages, `${path}.context_messages`);
  const orphan = count(row.orphan_messages, `${path}.orphan_messages`);
  const oversized = count(row.oversized_records, `${path}.oversized_records`);
  const malformed = count(row.malformed_records, `${path}.malformed_records`);
  const complete = bool(row.complete, `${path}.complete`);
  if (captured + (row.context_messages as number) + orphan !== visible || truncated > captured || truncatedBodies > truncated ||
      visible + oversized + malformed > sourceRecords) throw new Error(`${path} counters do not reconcile`);
  if (complete !== (orphan === 0 && oversized === 0 && malformed === 0)) throw new Error(`${path}.complete is inconsistent`);
  return row as unknown as VisibleCoverageV1;
}

function object(value: unknown, path: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new Error(`${path} must be an object`);
  return value as Record<string, unknown>;
}
function exact(row: Record<string, unknown>, keys: readonly string[], path: string): void {
  if (Object.keys(row).some((key) => !keys.includes(key)) || keys.some((key) => !Object.prototype.hasOwnProperty.call(row, key))) throw new Error(`${path} has an invalid exact shape`);
}
function array(value: unknown, path: string, max: number): unknown[] {
  if (!Array.isArray(value) || value.length > max) throw new Error(`${path} must be a bounded array`);
  return value;
}
function text(value: unknown, path: string, max: number): string {
  if (typeof value !== "string" || Buffer.byteLength(value, "utf8") > max) throw new Error(`${path} must be bounded text`);
  return value;
}
function id(value: unknown, path: string): string {
  const result = text(value, path, 256);
  if (!ID.test(result)) throw new Error(`${path} must be an ID`);
  return result;
}
function nullableId(value: unknown, path: string): string | null { return value === null ? null : id(value, path); }
function digest(value: unknown, path: string): string { if (typeof value !== "string" || !DIGEST.test(value)) throw new Error(`${path} must be a digest`); return value; }
function count(value: unknown, path: string): number { if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) throw new Error(`${path} must be a nonnegative safe integer`); return value; }
function positive(value: unknown, path: string): number { const result = count(value, path); if (result < 1) throw new Error(`${path} must be positive`); return result; }
function bool(value: unknown, path: string): boolean { if (typeof value !== "boolean") throw new Error(`${path} must be boolean`); return value; }
function cursor(value: unknown, path: string): void { if (value !== null && (typeof value !== "string" || Buffer.byteLength(value, "utf8") > 4096 || value.includes("\0"))) throw new Error(`${path} must be a bounded cursor`); }
function timestamp(value: unknown, path: string): string {
  const result = text(value, path, 128);
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(result) || Number.isNaN(Date.parse(result))) {
    throw new Error(`${path} must be an RFC3339 timestamp`);
  }
  return result;
}
function choice<const T extends readonly unknown[]>(value: unknown, values: T, path: string): T[number] { if (!values.includes(value)) throw new Error(`${path} has an invalid enum`); return value; }
function constant(value: unknown, expected: unknown, path: string): void { if (value !== expected) throw new Error(`${path} must equal ${String(expected)}`); }
