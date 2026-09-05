import { isAlias, isMap, isNode, isScalar, parseAllDocuments } from "yaml";
import type { MachineLedgerV4, ReviewPresentationV4 } from "../contracts/review-v4";
import { parseReviewPresentationV4 } from "./contracts-v4";
import { sha256Text } from "./hash";

const MAX_DOCUMENT_BYTES = 64 << 20;
const MAX_FRONTMATTER_BYTES = 1 << 20;
const MAX_FIELD_BYTES = 16_384;
const MAX_PROBLEM_QUESTION_BYTES = 4_096;
const MAX_BLOCKS = 65_536;
const MAX_NODES = 10_000;
const MAX_DEPTH = 100;
const ID = /^[A-Za-z0-9][A-Za-z0-9._:-]*$/;
const RESERVED_FRONTMATTER = new Set(["id", "entity_type", "project_id", "schema_version", "document_format", "revision", "generation_id", "minimum_reader_version", "minimum_writer_version"]);

export interface MarkdownPair { review: string; history: string }
export interface MarkdownField { entity: string; name: string; value: string }
export interface MarkdownRead {
  presentation: ReviewPresentationV4;
  changedFields: readonly { entity: string; name: string }[];
  changedDocuments: readonly ("review" | "history")[];
  fields: readonly MarkdownField[];
}

export type MarkdownRejectionCode =
  | "markdown_format_invalid"
  | "markdown_field_duplicate"
  | "markdown_field_missing"
  | "markdown_structure_edit_requires_command"
  | "markdown_generated_region_modified"
  | "markdown_baseline_missing";

export class MarkdownRejectionError extends Error {
  constructor(public readonly code: MarkdownRejectionCode, message = code) { super(message); this.name = "MarkdownRejectionError"; }
}

export function markdownCodeOf(error: unknown): MarkdownRejectionCode | undefined {
  return error instanceof MarkdownRejectionError ? error.code : undefined;
}

interface Block extends MarkdownField { generated: boolean }
interface ParsedDocument { blocks: Block[]; frontmatter: Map<string, unknown> }

export function readMarkdownIdentityV4(source: string): { projectId: string; document: "review" | "history" } {
  const frontmatter = parseFrontmatter(source);
  if (frontmatter.get("schema_version") !== 4 || frontmatter.get("document_format") !== "review-markdown-v1") fail("markdown_format_invalid");
  const projectId = frontmatter.get("project_id");
  const entityType = frontmatter.get("entity_type");
  if (typeof projectId !== "string" || !ID.test(projectId) || (entityType !== "project-review" && entityType !== "project-history")) fail("markdown_format_invalid");
  return { projectId, document: entityType === "project-review" ? "review" : "history" };
}

export function parseMarkdownV4(pair: MarkdownPair, ledger: MachineLedgerV4): MarkdownRead {
  const base = ledger.document_projection?.presentation_base;
  if (!base || ledger.document_projection?.format !== "review-markdown-v1") fail("markdown_baseline_missing");
  const review = parseDocument(pair.review, "review", base);
  const history = parseDocument(pair.history, "history", base);
  validateAnchors(pair.review, expectedAnchors(base, "review"));
  validateAnchors(pair.history, expectedAnchors(base, "history"));
  const expected = expectedBlocks(base);
  validateStructure(review.blocks, expected.filter((block) => documentFor(block.entity) === "review"));
  validateStructure(history.blocks, expected.filter((block) => documentFor(block.entity) === "history"));
  const fields = [...review.blocks, ...history.blocks].filter((block) => !block.generated);
  const expectedFields = new Map(expected.filter((block) => !block.generated).map((block) => [`${block.entity}\0${block.name}`, block.value]));
  const presentation = structuredClone(base);
  const entities = presentationEntities(presentation);
  const changedFields: { entity: string; name: string }[] = [];
  for (const field of fields) {
    const before = expectedFields.get(`${field.entity}\0${field.name}`);
    if (before === undefined) fail("markdown_structure_edit_requires_command");
    if (normalizeLines(before) !== normalizeLines(field.value)) {
      changedFields.push({ entity: field.entity, name: field.name });
      setPresentationField(presentation, entities, field.entity, field.name, field.value);
      recordPatch(presentation, field.entity, field.name, before, field.value);
    }
  }
  if (changedFields.length > 0) {
    presentation.revision += 1;
    const changed = new Set(changedFields.map((field) => field.entity));
    for (const item of presentation.decisions) if (changed.has(`decision:${item.id}`)) item.revision += 1;
    for (const item of presentation.problem_nodes) if (changed.has(`problem:${item.id}`)) item.revision += 1;
    presentation.human_patches.sort(comparePatchLike);
    presentation.generated_baselines.sort(comparePatchLike);
  }
  const changedDocuments: ("review" | "history")[] = [];
  if (sha256Text(pair.review) !== ledger.review_sha256) changedDocuments.push("review");
  if (sha256Text(pair.history) !== ledger.history_sha256) changedDocuments.push("history");
  if (changedFields.length === 0 && changedDocuments.length > 0) presentation.revision += 1;
  let validatedPresentation: ReviewPresentationV4;
  try { validatedPresentation = parseReviewPresentationV4(JSON.stringify(presentation)); }
  catch { fail("markdown_format_invalid"); }
  return { presentation: validatedPresentation, changedFields, changedDocuments, fields };
}

function parseDocument(source: string, document: "review" | "history", base: ReviewPresentationV4): ParsedDocument {
  if (Buffer.byteLength(source, "utf8") > MAX_DOCUMENT_BYTES || source.includes("\0") || /\r(?!\n)/.test(source)) fail("markdown_format_invalid");
  const frontmatter = parseFrontmatter(source);
  const want = document === "review"
    ? { id: `review-${base.project_id}`, entity_type: "project-review" }
    : { id: `history-${base.project_id}`, entity_type: "project-history" };
  const identity: Record<string, unknown> = {
    ...want, project_id: base.project_id, schema_version: 4, document_format: "review-markdown-v1",
    revision: base.revision, generation_id: base.generation_id, minimum_reader_version: "0.4.1", minimum_writer_version: "0.4.1"
  };
  for (const [key, value] of Object.entries(identity)) if (frontmatter.get(key) !== value) fail("markdown_structure_edit_requires_command");
  const blocks = scanBlocks(source);
  for (const block of blocks) if (documentFor(block.entity) !== document) fail("markdown_format_invalid");
  return { blocks, frontmatter };
}

function parseFrontmatter(source: string): Map<string, unknown> {
  if (!source.startsWith("---\n") && !source.startsWith("---\r\n")) fail("markdown_format_invalid");
  const match = /^(?:---\r?\n)([\s\S]*?)(?:\r?\n---(?:\r?\n|$))/.exec(source);
  if (!match || Buffer.byteLength(match[1], "utf8") > MAX_FRONTMATTER_BYTES) fail("markdown_format_invalid");
  const docs = parseAllDocuments(match[1], { strict: true, uniqueKeys: true, keepSourceTokens: true, prettyErrors: false });
  const document = docs[0];
  if (docs.length !== 1 || document.errors.length !== 0 || !isMap(document.contents)) fail("markdown_format_invalid");
  let count = 0;
  const visit = (candidate: unknown, depth: number): void => {
    if (!isNode(candidate)) fail("markdown_format_invalid");
    const node = candidate;
    if (depth > MAX_DEPTH || ++count > MAX_NODES || isAlias(node)) fail("markdown_format_invalid");
    const tags = new Set([undefined, "tag:yaml.org,2002:map", "tag:yaml.org,2002:seq", "tag:yaml.org,2002:str", "tag:yaml.org,2002:int", "tag:yaml.org,2002:float", "tag:yaml.org,2002:bool", "tag:yaml.org,2002:null", "tag:yaml.org,2002:timestamp"]);
    if (!tags.has(node.tag)) fail("markdown_format_invalid");
    if (isMap(node)) for (const item of node.items) {
      if (!isScalar(item.key) || typeof item.key.value !== "string" || item.key.value === "" || item.key.value === "<<") fail("markdown_format_invalid");
      visit(item.key, depth + 1); visit(item.value, depth + 1);
    }
    else if ("items" in node && Array.isArray(node.items)) for (const item of node.items) visit(item, depth + 1);
  };
  visit(document.contents, 1);
  const result = new Map<string, unknown>();
  for (const pair of document.contents.items) {
    if (!isScalar(pair.key) || typeof pair.key.value !== "string" || pair.key.value === "<<") fail("markdown_format_invalid");
    const key = pair.key.value;
    if (key.startsWith("session_reviewer_") && !RESERVED_FRONTMATTER.has(key)) fail("markdown_format_invalid");
    const value: unknown = isScalar(pair.value) ? pair.value.value : undefined;
    if (RESERVED_FRONTMATTER.has(key) && (!isScalar(pair.value) || (typeof value === "number" && !Number.isSafeInteger(value)))) fail("markdown_format_invalid");
    result.set(key, value);
  }
  return result;
}

function scanBlocks(source: string): Block[] {
  const lines = physicalLines(source);
  const blocks: Block[] = [];
  const seen = new Set<string>();
  let active: { entity: string; name: string; generated: boolean; valueStart: number } | undefined;
  let fence: { char: string; length: number } | undefined;
  let indented = false;
  for (const line of lines) {
    if (indented) { if (line.text === "" || /^(?: {4}|\t)/.test(line.text)) continue; indented = false; }
    if (fence) { if (fenceClose(line.text, fence)) fence = undefined; continue; }
    const openedFence = fenceOpen(line.text); if (openedFence) { fence = openedFence; continue; }
    if (/^(?: {4}|\t)/.test(line.text)) { indented = true; continue; }
    const marker = markerLine(line.text);
    if (!marker) continue;
    if (marker.closing) {
      if (!active || active.entity !== marker.entity || active.name !== marker.name || active.generated !== marker.generated) fail("markdown_format_invalid");
      let end = line.start;
      if (end > active.valueStart && source[end - 1] === "\n") { end--; if (end > active.valueStart && source[end - 1] === "\r") end--; }
      const value = source.slice(active.valueStart, end);
      const limit = active.generated ? MAX_DOCUMENT_BYTES : active.entity.startsWith("problem:") && active.name === "question" ? MAX_PROBLEM_QUESTION_BYTES : MAX_FIELD_BYTES;
      if (Buffer.byteLength(value, "utf8") > limit) fail("markdown_format_invalid");
      blocks.push({ ...active, value }); active = undefined; continue;
    }
    if (active) fail("markdown_format_invalid");
    if (blocks.length >= MAX_BLOCKS) fail("markdown_format_invalid");
    const key = `${marker.entity}\0${marker.name}`;
    if (seen.has(key)) fail("markdown_field_duplicate");
    seen.add(key); active = { entity: marker.entity, name: marker.name, generated: marker.generated, valueStart: line.next };
  }
  if (active) fail("markdown_format_invalid");
  return blocks;
}

function markerLine(line: string): { entity: string; name: string; generated: boolean; closing: boolean } | undefined {
  if (!line.startsWith("<!-- session-reviewer:v4-") && !line.startsWith("<!-- /session-reviewer:v4-")) return undefined;
  const match = /^<!-- (\/)?session-reviewer:v4-(field|generated) entity="([^"]+)" name="([^"]+)" -->$/.exec(line);
  if (!match) fail("markdown_format_invalid");
  const generated = match[2] === "generated", entity = match[3], name = match[4];
  if (!validKey(entity, name, generated)) fail("markdown_format_invalid");
  return { closing: match[1] === "/", generated, entity, name };
}

function validateStructure(actual: Block[], expected: Block[]): void {
  const byKey = new Map(expected.map((block) => [`${block.entity}\0${block.name}`, block]));
  const actualKeys = new Set(actual.map((block) => `${block.entity}\0${block.name}`));
  for (const block of expected) if (!actualKeys.has(`${block.entity}\0${block.name}`)) fail("markdown_field_missing");
  if (actual.length !== expected.length) fail("markdown_structure_edit_requires_command");
  for (const block of actual) {
    const want = byKey.get(`${block.entity}\0${block.name}`);
    if (!want || want.generated !== block.generated) fail("markdown_structure_edit_requires_command");
    if (block.generated && normalizeLines(block.value) !== normalizeLines(want.value)) fail("markdown_generated_region_modified");
  }
}

function expectedAnchors(p: ReviewPresentationV4, document: "review" | "history"): string[] {
  const ids = document === "history" ? p.timeline.map((x) => `milestone-${anchor(x.id)}`) : [
    ...p.decisions.map((x) => `decision-${anchor(x.id)}`), ...p.risks.map((x) => `risk-${anchor(x.id)}`),
    ...p.open_loops.map((x) => `open-loop-${anchor(x.id)}`), ...p.problem_nodes.map((x) => `problem-${anchor(x.id)}`)
  ];
  return ids.map((id) => `<a id="${id}"></a>`);
}

function validateAnchors(source: string, expected: string[]): void {
  const counts = new Map<string, number>();
  for (const line of topLevelLines(source)) if (line.startsWith('<a id="') && line.endsWith('"></a>')) counts.set(line, (counts.get(line) ?? 0) + 1);
  for (const anchorLine of expected) if (counts.get(anchorLine) !== 1) fail("markdown_structure_edit_requires_command");
}

function topLevelLines(source: string): string[] {
  const out: string[] = []; let fence: { char: string; length: number } | undefined; let indented = false;
  for (const line of physicalLines(source)) {
    if (indented) { if (line.text === "" || /^(?: {4}|\t)/.test(line.text)) continue; indented = false; }
    if (fence) { if (fenceClose(line.text, fence)) fence = undefined; continue; }
    const opened = fenceOpen(line.text); if (opened) { fence = opened; continue; }
    if (/^(?: {4}|\t)/.test(line.text)) { indented = true; continue; }
    out.push(line.text);
  }
  return out;
}

function expectedBlocks(p: ReviewPresentationV4): Block[] {
  const out: Block[] = [];
  const field = (entity: string, name: string, value: string): void => { out.push({ entity, name, value, generated: false }); };
  for (const name of ["goal", "stage", "status", "next_action", "last_verification"] as const) field("project-overview", name, p.current_state[name]);
  out.push({ entity: "project-overview", name: "problem-tree", value: renderProblemTree(p), generated: true });
  out.push({ entity: "project-overview", name: "pinned-decisions", value: renderPinned(p), generated: true });
  out.push({ entity: "project-overview", name: "recent-milestones", value: renderRecent(p), generated: true });
  for (const x of p.decisions) for (const name of ["title", "rationale", "impact", "reevaluate_when"] as const) field(`decision:${x.id}`, name, x[name]);
  for (const x of p.risks) for (const name of ["title", "detail", "status"] as const) field(`risk:${x.id}`, name, x[name]);
  for (const x of p.open_loops) for (const name of ["title", "question", "next_experiment", "completion_criterion", "status"] as const) field(`open-loop:${x.id}`, name, x[name]);
  for (const x of p.problem_nodes) for (const name of ["question", "completion_criterion", "current_conclusion"] as const) field(`problem:${x.id}`, name, x[name]);
  for (const x of p.timeline) {
    field(`milestone:${x.id}`, "title", x.title); field(`milestone:${x.id}`, "summary", x.summary);
    field(`milestone:${x.id}`, "conclusion", x.closed_loop.conclusion.text); field(`milestone:${x.id}`, "impact_and_follow_up", x.closed_loop.impact_and_follow_up.text);
    out.push({ entity: `milestone:${x.id}`, name: "evidence", value: renderEvidence(x), generated: true });
  }
  return out;
}

interface PresentationEntities {
  decisions: Map<string, ReviewPresentationV4["decisions"][number]>;
  risks: Map<string, ReviewPresentationV4["risks"][number]>;
  openLoops: Map<string, ReviewPresentationV4["open_loops"][number]>;
  problems: Map<string, ReviewPresentationV4["problem_nodes"][number]>;
  milestones: Map<string, ReviewPresentationV4["timeline"][number]>;
}

function presentationEntities(p: ReviewPresentationV4): PresentationEntities {
  return {
    decisions: new Map(p.decisions.map((item) => [item.id, item])), risks: new Map(p.risks.map((item) => [item.id, item])),
    openLoops: new Map(p.open_loops.map((item) => [item.id, item])), problems: new Map(p.problem_nodes.map((item) => [item.id, item])),
    milestones: new Map(p.timeline.map((item) => [item.id, item]))
  };
}

function setPresentationField(p: ReviewPresentationV4, entities: PresentationEntities, entity: string, name: string, value: string): void {
  const [kind, ...rest] = entity.split(":"); const id = rest.join(":");
  if (kind === "project-overview") { (p.current_state as unknown as Record<string, string>)[name] = value; return; }
  const item = (kind === "decision" ? entities.decisions : kind === "risk" ? entities.risks : kind === "open-loop" ? entities.openLoops : kind === "problem" ? entities.problems : entities.milestones).get(id) as unknown as Record<string, unknown> | undefined;
  if (!item) fail("markdown_structure_edit_requires_command");
  if (kind === "milestone" && (name === "conclusion" || name === "impact_and_follow_up")) {
    const closed = item.closed_loop as ReviewPresentationV4["timeline"][number]["closed_loop"];
    if (name === "conclusion") {
      if (value === "" && closed.conclusion.kind !== "missing") fail("markdown_structure_edit_requires_command");
      if (value !== "") { closed.conclusion.text = value; closed.conclusion.kind = "human_confirmed"; closed.conclusion.missing_reason = null; }
    } else {
      const segment = closed.impact_and_follow_up;
      if (value === "" && segment.text !== "") fail("markdown_structure_edit_requires_command");
      if (value !== "") { segment.text = value; if (segment.state === "missing") segment.state = "present"; segment.missing_reason = null; }
    }
    return;
  }
  item[name] = value;
}

function recordPatch(p: ReviewPresentationV4, entity: string, field: string, before: string, after: string): void {
  let baseline = p.generated_baselines.find((item) => item.entity_id === entity && item.field === field);
  if (!baseline) {
    const identity = { schema_version: 1, entity_id: entity, field, kind: "scalar", value: before, values: null };
    baseline = { generation_id: p.generation_id, entity_id: entity, field, kind: "scalar", value: before, generated_hash: sha256Text(JSON.stringify(identity)) };
    p.generated_baselines.push(baseline);
  }
  const patchIndex = p.human_patches.findIndex((item) => item.entity_id === entity && item.field === field);
  if (after === baseline.value) { if (patchIndex >= 0) p.human_patches.splice(patchIndex, 1); return; }
  const patch = { entity_id: entity, field, operation: "set" as const, value: after, base_generated_hash: baseline.generated_hash };
  if (patchIndex >= 0) p.human_patches[patchIndex] = patch; else p.human_patches.push(patch);
}

function comparePatchLike(left: { entity_id: string; field: string }, right: { entity_id: string; field: string }): number {
  if (left.entity_id !== right.entity_id) return left.entity_id < right.entity_id ? -1 : 1;
  if (left.field !== right.field) return left.field < right.field ? -1 : 1;
  return 0;
}

function renderProblemTree(p: ReviewPresentationV4): string {
  if (!p.problem_nodes.length) return "- 暂无正式问题";
  const children = new Map<string, typeof p.problem_nodes>();
  for (const node of p.problem_nodes) { const key = node.primary_parent_id ?? ""; const siblings = children.get(key); if (siblings) siblings.push(node); else children.set(key, [node]); }
  const lines: string[] = []; const walk = (parent: string, depth: number): void => { for (const node of children.get(parent) ?? []) { lines.push(`${"  ".repeat(depth)}- [正式问题](#problem-${anchor(node.id)})`); walk(node.id, depth + 1); } };
  walk("", 0); return lines.join("\n");
}
function renderPinned(p: ReviewPresentationV4): string { const values = p.decisions.filter((x) => x.pinned).map((x) => `- [决策](#decision-${anchor(x.id)})`); return values.length ? values.join("\n") : "- 暂无置顶决策"; }
function renderRecent(p: ReviewPresentationV4): string { const shown = Math.min(5, p.timeline.length); return [`共 ${p.timeline.length} 条，显示 ${shown} 条；[查看完整历史](项目历史.md)。`, ...p.timeline.slice(-shown).map((x) => `- [里程碑](项目历史.md#milestone-${anchor(x.id)})`)].join("\n"); }
function renderEvidence(item: ReviewPresentationV4["timeline"][number]): string {
  const lines = [`- 发生时间：${item.occurred_at}`, `- 类型：${item.kind}`];
  const addRefs = (label: string, values: readonly { provider: string; session_id: string; turn_unit_id: string }[]): void => {
    if (!values.length) { lines.push(`- ${label}：无`); return; }
    for (const value of values) lines.push(`- ${label}：${value.provider}/${value.session_id}#${value.turn_unit_id}`);
  };
  const segment = (label: string, x: typeof item.closed_loop.trigger_question): void => { lines.push(`- ${label}状态：${x.state}`); if (x.text) lines.push(`  - 文本：${x.text.replaceAll("\n", "\n    ")}`); if (x.missing_reason) lines.push(`  - 缺失原因：${x.missing_reason}`); addRefs(`${label}引用`, x.source_turn_refs); };
  segment("触发", item.closed_loop.trigger_question); lines.push(`- 结论来源：${item.closed_loop.conclusion.kind}`); addRefs("结论引用", item.closed_loop.conclusion.source_turn_refs);
  segment("执行", item.closed_loop.execution); segment("验证", item.closed_loop.verification);
  lines.push(`- 影响/后续状态：${item.closed_loop.impact_and_follow_up.state}`); addRefs("影响/后续引用", item.closed_loop.impact_and_follow_up.source_turn_refs);
  const c = item.closed_loop.coverage; lines.push(`- Coverage：source=${c.source_turns}, captured=${c.captured_turns}, truncated=${c.truncated_turns}, unavailable=${c.source_unavailable_turns}`);
  addRefs("全部引用", item.closed_loop.source_turn_refs); lines.push(`- 关联决策：${item.decision_ids.length ? item.decision_ids.join(", ") : "无"}`); return lines.join("\n");
}
function anchor(value: string): string { return `x${Buffer.from(value, "utf8").toString("hex")}`; }
function documentFor(entity: string): "review" | "history" { return entity.startsWith("milestone:") ? "history" : "review"; }
function validKey(entity: string, name: string, generated: boolean): boolean {
  const [kind, ...rest] = entity.split(":"); const hasId = rest.length > 0 && ID.test(rest.join(":"));
  if (generated) return kind === "project-overview" && !hasId && ["problem-tree", "pinned-decisions", "recent-milestones"].includes(name) || kind === "milestone" && hasId && name === "evidence";
  const fields: Record<string, string[]> = { "project-overview": ["goal", "stage", "status", "next_action", "last_verification"], decision: ["title", "rationale", "impact", "reevaluate_when"], risk: ["title", "detail", "status"], "open-loop": ["title", "question", "next_experiment", "completion_criterion", "status"], problem: ["question", "completion_criterion", "current_conclusion"], milestone: ["title", "summary", "conclusion", "impact_and_follow_up"] };
  return !!fields[kind]?.includes(name) && (kind === "project-overview" ? !hasId : hasId);
}
function physicalLines(source: string): { text: string; start: number; next: number }[] { const out = []; for (let start = 0; start < source.length;) { const lf = source.indexOf("\n", start); const next = lf < 0 ? source.length : lf + 1; let end = lf < 0 ? source.length : lf; if (end > start && source[end - 1] === "\r") end--; out.push({ text: source.slice(start, end), start, next }); start = next; } return out; }
function fenceOpen(line: string): { char: string; length: number } | undefined { const match = /^ {0,3}(`{3,}|~{3,})(.*)$/.exec(line); if (!match || (match[1][0] === "`" && match[2].includes("`"))) return undefined; return { char: match[1][0], length: match[1].length }; }
function fenceClose(line: string, fence: { char: string; length: number }): boolean { return new RegExp(`^ {0,3}${fence.char}{${fence.length},}[ \\t]*$`).test(line); }
function normalizeLines(value: string): string { return value.replaceAll("\r\n", "\n"); }
function fail(code: MarkdownRejectionCode): never { throw new MarkdownRejectionError(code); }
