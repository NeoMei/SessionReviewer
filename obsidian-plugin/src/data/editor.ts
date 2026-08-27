import type { EditableField, EditableFieldName } from "../contracts/review-v2";
import { sha256Text } from "./hash";
import { parseHistory, parseReview } from "./markdown";
import type { VaultPort } from "./vault-port";

const ALLOWED = new Set<EditableFieldName>([
  "goal", "stage", "status", "next_action",
  "risk.title", "risk.status", "risk.detail",
  "decision.title", "decision.rationale", "decision.impact",
  "event.title", "event.meaning", "event.summary", "event.why", "event.changes", "event.results", "event.next"
]);
const LIST_FIELDS = new Set<EditableFieldName>(["event.changes", "event.results"]);
const TITLE_FIELDS = new Set<EditableFieldName>(["risk.title", "decision.title", "event.title"]);
const MAX_TEXT_RUNES = 20_000;
const MAX_LIST_ITEMS = 200;

export interface EditRequest {
  path: string;
  expectedSha256: string;
  document: "review" | "history";
  unitId: string;
  field: EditableFieldName;
  value: string | string[];
}

export class ReviewEditor {
  constructor(private readonly vault: VaultPort) {}

  async apply(request: EditRequest): Promise<{ sha256: string }> {
    validateRequest(request);
    let resultHash = "";
    await this.vault.process(request.path, (current) => {
      if (sha256Text(current) !== request.expectedSha256) throw new Error("stale edit: file changed; reload before editing");
      const next = patchAllowedField(current, request);
      if (request.document === "review") parseReview(next);
      else parseHistory(next);
      resultHash = sha256Text(next);
      return next;
    });
    return { sha256: resultHash };
  }
}

export function patchAllowedField(source: string, request: EditRequest): string {
  if (!ALLOWED.has(request.field)) throw new Error(`field is read-only: ${String(request.field)}`);
  const parsed = request.document === "review" ? parseReview(source) : parseHistory(source);
  const field = parsed.fields.find((candidate) => candidate.unitId === request.unitId && candidate.field === request.field);
  if (!field) {
    if (request.field === "event.summary") throw new Error("event summary cannot be inserted by this plugin version");
    throw new Error(`editable field does not exist: ${request.unitId}/${request.field}`);
  }
  const replacement = renderReplacement(source, field, request);
  return source.slice(0, field.range.start) + replacement + source.slice(field.range.end);
}

function validateRequest(request: EditRequest): void {
  if (!ALLOWED.has(request.field)) throw new Error(`field is read-only: ${String(request.field)}`);
  if (request.document === "review" && request.field.startsWith("event.")) throw new Error("field is read-only in review document");
  if (request.document === "history" && !request.field.startsWith("event.")) throw new Error("field is read-only in history document");
  const values = Array.isArray(request.value) ? request.value : [request.value];
  if (values.length === 0 || values.some((value) => !value.trim())) throw new Error(`${request.field} cannot be empty`);
  if (values.length > MAX_LIST_ITEMS) throw new Error(`${request.field} exceeds ${MAX_LIST_ITEMS} items`);
  if (values.some((value) => Array.from(value).length > MAX_TEXT_RUNES)) throw new Error(`${request.field} exceeds configured rune limit`);
  if (TITLE_FIELDS.has(request.field) && values.some((value) => /[\r\n]/.test(value) || value.trim().startsWith("#"))) throw new Error(`${request.field} must be one plain heading line`);
  if (LIST_FIELDS.has(request.field) && !Array.isArray(request.value)) throw new Error(`${request.field} requires a list value`);
  if (!LIST_FIELDS.has(request.field) && Array.isArray(request.value)) throw new Error(`${request.field} requires a text value`);
}

function renderReplacement(source: string, field: EditableField, request: EditRequest): string {
  if (TITLE_FIELDS.has(request.field)) {
    const value = (request.value as string).trim();
    if (request.field === "event.title") {
      const event = parseHistory(source).events.find((candidate) => candidate.id === request.unitId);
      if (!event) throw new Error(`event does not exist: ${request.unitId}`);
      return `## ${event.occurredAt} · ${value}\n`;
    }
    return `### ${value}\n`;
  }
  const original = source.slice(field.range.start, field.range.end);
  const trailing = original.match(/\n+$/)?.[0] ?? "\n";
  if (LIST_FIELDS.has(request.field)) return (request.value as string[]).map((item) => `- ${item.trim()}`).join("\n") + trailing;
  return (request.value as string).trim() + trailing;
}
