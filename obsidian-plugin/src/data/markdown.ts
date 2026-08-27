import type {
  DecisionModel,
  EditableField,
  EditableFieldName,
  HistoryEvent,
  HistoryModel,
  ReviewModel,
  RiskModel,
  SourceRange
} from "../contracts/review-v2";

const MAX_MARKDOWN_BYTES = 4 << 20;
const STABLE_ID = /^[a-z0-9][a-z0-9._-]{0,127}$/;

interface Line {
  text: string;
  start: number;
  end: number;
  next: number;
  fenced: boolean;
}

interface Heading extends Line {
  level: number;
  name: string;
}

interface MarkerBlock {
  kind: "risk" | "decision" | "event";
  id: string;
  body: SourceRange;
}

interface Identity {
  projectId: string;
  revision: number;
  bodyStart: number;
}

export function parseReview(input: string): ReviewModel {
  const source = prepare(input, "review");
  const identity = parseFrontmatter(source, "project-overview", "project_review");
  const lines = scanLines(source);
  const blocks = scanMarkers(lines, identity.bodyStart);
  const headings = scanHeadings(lines, identity.bodyStart);
  const root = rootHeading(headings, "");
  const fields: EditableField[] = [];

  const goal = requiredSection(source, headings, "项目目标", "goal", identity.projectId, fields);
  const stage = requiredSection(source, headings, "当前阶段", "stage", identity.projectId, fields);
  const status = requiredSection(source, headings, "当前状态", "status", identity.projectId, fields);
  const nextAction = requiredSection(source, headings, "下一步", "next_action", identity.projectId, fields);
  const lastVerification = requiredSection(source, headings, "最近验证");
  const riskContainer = requiredHeading(headings, 2, "风险与待办");
  const decisionContainer = requiredHeading(headings, 2, "关键决策");
  const risks: RiskModel[] = [];
  const decisions: DecisionModel[] = [];

  for (const block of blocks) {
    if (block.kind === "event") throw new Error("review document cannot contain event marker blocks");
    if (block.kind === "risk") {
      requireInside(block.body, sectionRange(headings, riskContainer, source.length), "risk");
      risks.push(parseRisk(source, lines, block, fields));
    } else {
      requireInside(block.body, sectionRange(headings, decisionContainer, source.length), "decision");
      decisions.push(parseDecision(source, lines, block, fields));
    }
  }

  return {
    projectId: identity.projectId,
    revision: identity.revision,
    name: root.name,
    goal,
    stage,
    status,
    nextAction,
    lastVerification,
    risks,
    decisions,
    fields
  };
}

export function parseHistory(input: string): HistoryModel {
  const source = prepare(input, "history");
  const identity = parseFrontmatter(source, "project-history", "project_history");
  const lines = scanLines(source);
  const blocks = scanMarkers(lines, identity.bodyStart);
  const headings = scanHeadings(lines, identity.bodyStart);
  rootHeading(headings, "项目历史");
  const fields: EditableField[] = [];
  const events: HistoryEvent[] = [];
  const seen = new Set<string>();

  for (const block of blocks) {
    if (block.kind !== "event") throw new Error("history document can contain only event marker blocks");
    if (seen.has(block.id)) throw new Error(`duplicate event identity "${block.id}"`);
    seen.add(block.id);
    events.push(parseEvent(source, lines, block, fields));
  }
  for (let index = 1; index < events.length; index += 1) {
    const previous = eventTime(events[index - 1] as HistoryEvent);
    const current = eventTime(events[index] as HistoryEvent);
    if (previous < current || (previous === current && (events[index - 1]?.id ?? "") > (events[index]?.id ?? ""))) {
      throw new Error("history events are not in reverse chronological order");
    }
  }
  return { projectId: identity.projectId, revision: identity.revision, events, fields };
}

function prepare(source: string, document: string): string {
  if (Buffer.byteLength(source, "utf8") > MAX_MARKDOWN_BYTES) throw new Error(`${document} exceeds ${MAX_MARKDOWN_BYTES} bytes`);
  return source.replaceAll("\r\n", "\n").replaceAll("\r", "\n");
}

function parseFrontmatter(source: string, expectedId: string, expectedType: string): Identity {
  if (!source.startsWith("---\n")) throw new Error("missing opening YAML frontmatter fence");
  const closing = source.indexOf("\n---\n", 4);
  if (closing < 0) throw new Error("missing closing YAML frontmatter fence");
  const values = new Map<string, string>();
  for (const raw of source.slice(4, closing).split("\n")) {
    const match = /^([A-Za-z0-9_-]+):\s*(.*?)\s*$/.exec(raw);
    if (!match) throw new Error("malformed YAML frontmatter");
    const key = match[1] as string;
    if (values.has(key)) throw new Error(`duplicate YAML frontmatter key "${key}"`);
    values.set(key, match[2] as string);
  }
  if (values.get("id") !== expectedId) throw new Error(`frontmatter id must be "${expectedId}"`);
  if (values.get("entity_type") !== expectedType) throw new Error(`frontmatter entity_type must be "${expectedType}"`);
  const projectId = values.get("project_id") ?? "";
  if (!projectId.startsWith("project-") || !STABLE_ID.test(projectId)) throw new Error("invalid frontmatter project_id");
  if (values.get("schema_version") !== "2") throw new Error("frontmatter schema_version must be 2");
  const revision = Number(values.get("revision"));
  if (!Number.isSafeInteger(revision) || revision < 1) throw new Error("frontmatter revision must be a positive integer");
  return { projectId, revision, bodyStart: closing + 5 };
}

function scanLines(source: string): Line[] {
  const result: Line[] = [];
  let start = 0;
  let fence: { marker: string; width: number } | undefined;
  while (start <= source.length) {
    const newline = source.indexOf("\n", start);
    const end = newline < 0 ? source.length : newline;
    const next = newline < 0 ? source.length : newline + 1;
    const text = source.slice(start, end);
    const run = /^ {0,3}(`{3,}|~{3,})/.exec(text)?.[1];
    const wasFenced = fence !== undefined;
    if (run && !fence) fence = { marker: run[0] as string, width: run.length };
    else if (run && fence && run[0] === fence.marker && run.length >= fence.width && /^ {0,3}(`+|~+)\s*$/.test(text)) fence = undefined;
    result.push({ text, start, end, next, fenced: wasFenced || Boolean(run) });
    if (next === source.length) break;
    start = next;
  }
  if (fence) throw new Error("unclosed fenced code");
  return result;
}

function scanMarkers(lines: Line[], bodyStart: number): MarkerBlock[] {
  const blocks: MarkerBlock[] = [];
  const ids = new Set<string>();
  let active: { kind: MarkerBlock["kind"]; id: string; start: number } | undefined;
  for (const line of lines) {
    if (line.start < bodyStart || line.fenced) continue;
    const open = /^<!-- session-reviewer:(risk|decision|event) id="([a-z0-9][a-z0-9._-]{0,127})" -->$/.exec(line.text);
    const close = /^<!-- \/session-reviewer:(risk|decision|event) -->$/.exec(line.text);
    if (open) {
      const kind = open[1] as MarkerBlock["kind"];
      const id = open[2] as string;
      if (active) throw new Error(`nested ${kind} marker inside ${active.kind} "${active.id}"`);
      if (ids.has(id)) throw new Error(`duplicate ${kind} identity "${id}"`);
      active = { kind, id, start: line.next };
    } else if (close) {
      const kind = close[1] as MarkerBlock["kind"];
      if (!active || active.kind !== kind) throw new Error(`closing ${kind} marker does not match an opening marker`);
      blocks.push({ kind, id: active.id, body: { start: active.start, end: line.start } });
      ids.add(active.id);
      active = undefined;
    } else if (line.text.trim().startsWith("<!--") && line.text.includes("session-reviewer:")) {
      throw new Error(`reserved session-reviewer comment at ${line.start} is not an exact marker line`);
    }
  }
  if (active) throw new Error(`unterminated ${active.kind} marker "${active.id}"`);
  return blocks;
}

function scanHeadings(lines: Line[], bodyStart: number, range?: SourceRange): Heading[] {
  const headings: Heading[] = [];
  for (const line of lines) {
    if (line.start < bodyStart || line.fenced || (range && (line.start < range.start || line.start >= range.end))) continue;
    const match = /^(#{1,6}) +(.*?)\s*#*\s*$/.exec(line.text);
    if (match && match[2]?.trim()) headings.push({ ...line, level: match[1]?.length ?? 0, name: match[2].trim() });
  }
  return headings;
}

function rootHeading(headings: Heading[], expected: string): Heading {
  const roots = headings.filter((heading) => heading.level === 1);
  if (roots.length !== 1 || (expected && roots[0]?.name !== expected)) throw new Error(`document must contain one level-one title${expected ? ` "${expected}"` : ""}`);
  return roots[0] as Heading;
}

function requiredHeading(headings: Heading[], level: number, name: string): Heading {
  const found = headings.filter((heading) => heading.level === level && heading.name === name);
  if (found.length !== 1) throw new Error(`${name} must appear exactly once`);
  return found[0] as Heading;
}

function sectionRange(headings: Heading[], heading: Heading, sourceEnd: number): SourceRange {
  const next = headings.find((candidate) => candidate.start > heading.start && candidate.level <= heading.level);
  return { start: heading.next, end: next?.start ?? sourceEnd };
}

function requiredSection(
  source: string,
  headings: Heading[],
  name: string,
  field?: EditableFieldName,
  projectId?: string,
  fields?: EditableField[]
): string {
  const heading = requiredHeading(headings, 2, name);
  const range = sectionRange(headings, heading, source.length);
  const value = clean(source.slice(range.start, range.end));
  if (!value) throw new Error(`${name} cannot be empty`);
  if (field && projectId && fields) fields.push({ document: "review", unitId: "project-overview", field, value, range });
  return value;
}

function parseRisk(source: string, lines: Line[], block: MarkerBlock, fields: EditableField[]): RiskModel {
  const headings = scanHeadings(lines, block.body.start, block.body);
  const title = firstBlockTitle(headings, 3, block, "risk");
  const status = blockSection(source, headings, block, 4, "状态");
  const detail = blockSection(source, headings, block, 4, "详情");
  pushField(fields, "review", block.id, "risk.title", title.name, { start: title.start, end: title.next });
  pushField(fields, "review", block.id, "risk.status", status.value, status.range);
  pushField(fields, "review", block.id, "risk.detail", detail.value, detail.range);
  return { id: block.id, title: title.name, status: status.value, detail: detail.value };
}

function parseDecision(source: string, lines: Line[], block: MarkerBlock, fields: EditableField[]): DecisionModel {
  const headings = scanHeadings(lines, block.body.start, block.body);
  const title = firstBlockTitle(headings, 3, block, "decision");
  const rationale = blockSection(source, headings, block, 4, "原因");
  const impact = blockSection(source, headings, block, 4, "影响");
  const occurredAt = optionalBlockSection(source, headings, block, 4, "日期")?.value ?? "";
  const status = optionalBlockSection(source, headings, block, 4, "状态")?.value ?? "";
  pushField(fields, "review", block.id, "decision.title", title.name, { start: title.start, end: title.next });
  pushField(fields, "review", block.id, "decision.rationale", rationale.value, rationale.range);
  pushField(fields, "review", block.id, "decision.impact", impact.value, impact.range);
  return { id: block.id, occurredAt, title: title.name, rationale: rationale.value, impact: impact.value, status };
}

function parseEvent(source: string, lines: Line[], block: MarkerBlock, fields: EditableField[]): HistoryEvent {
  const headings = scanHeadings(lines, block.body.start, block.body);
  const titleHeading = firstBlockTitle(headings, 2, block, "event");
  const split = titleHeading.name.indexOf(" · ");
  if (split < 1) throw new Error(`event "${block.id}" title must use date middle-dot title form`);
  const occurredAt = titleHeading.name.slice(0, split).trim();
  const title = titleHeading.name.slice(split + 3).trim();
  if (!title || Number.isNaN(Date.parse(occurredAt))) throw new Error(`event "${block.id}" has an invalid date or title`);
  const kind = blockSection(source, headings, block, 3, "事件类别").value;
  const meaning = blockSection(source, headings, block, 3, "节点意义");
  const summary = optionalBlockSection(source, headings, block, 3, "摘要");
  const why = blockSection(source, headings, block, 3, "为什么会走到这里");
  const changes = listSection(blockSection(source, headings, block, 3, "发生了什么"), block.id);
  const results = listSection(blockSection(source, headings, block, 3, "结果与验证"), block.id);
  const decisionsSection = optionalBlockSection(source, headings, block, 3, "关联决策");
  const decisionIds = decisionsSection ? listSection(decisionsSection, block.id) : [];
  const next = blockSection(source, headings, block, 3, "留下的问题或下一步");
  pushField(fields, "history", block.id, "event.title", title, { start: titleHeading.start, end: titleHeading.next });
  pushField(fields, "history", block.id, "event.meaning", meaning.value, meaning.range);
  if (summary) pushField(fields, "history", block.id, "event.summary", summary.value, summary.range);
  pushField(fields, "history", block.id, "event.why", why.value, why.range);
  pushField(fields, "history", block.id, "event.changes", changes, blockSection(source, headings, block, 3, "发生了什么").range);
  pushField(fields, "history", block.id, "event.results", results, blockSection(source, headings, block, 3, "结果与验证").range);
  pushField(fields, "history", block.id, "event.next", next.value, next.range);
  return { id: block.id, occurredAt, kind, title, meaning: meaning.value, summary: summary?.value ?? "", why: why.value, changes, results, decisionIds, next: next.value };
}

function firstBlockTitle(headings: Heading[], level: number, block: MarkerBlock, kind: string): Heading {
  if (headings[0]?.level !== level) throw new Error(`${kind} "${block.id}" must begin with one level-${level} title`);
  return headings[0];
}

function optionalBlockSection(source: string, headings: Heading[], block: MarkerBlock, level: number, name: string): { value: string; range: SourceRange } | undefined {
  const matches = headings.filter((heading) => heading.level === level && heading.name === name);
  if (matches.length > 1) throw new Error(`${block.kind} "${block.id}" has duplicate ${name} subsection`);
  const heading = matches[0];
  if (!heading) return undefined;
  const range = sectionRange(headings, heading, block.body.end);
  const value = clean(source.slice(range.start, range.end));
  if (!value) throw new Error(`${block.kind} "${block.id}" has empty ${name} subsection`);
  return { value, range };
}

function blockSection(source: string, headings: Heading[], block: MarkerBlock, level: number, name: string): { value: string; range: SourceRange } {
  const result = optionalBlockSection(source, headings, block, level, name);
  if (!result) throw new Error(`${block.kind} "${block.id}" is missing ${name}`);
  return result;
}

function listSection(section: { value: string }, id: string): string[] {
  const values = section.value.split("\n").filter(Boolean).map((line) => {
    if (!line.startsWith("- ")) throw new Error(`event "${id}" list field must contain bullet items`);
    return line.slice(2).trim();
  });
  if (values.length === 0 || values.some((value) => !value)) throw new Error(`event "${id}" list field cannot be empty`);
  return values;
}

function pushField(fields: EditableField[], document: "review" | "history", unitId: string, field: EditableFieldName, value: string | string[], range: SourceRange): void {
  fields.push({ document, unitId, field, value, range });
}

function requireInside(child: SourceRange, container: SourceRange, kind: string): void {
  if (child.start < container.start || child.end > container.end) throw new Error(`${kind} marker is outside its required section`);
}

function clean(value: string): string {
  return value.trim();
}

function eventTime(event: HistoryEvent): number {
  const parsed = Date.parse(event.occurredAt);
  if (Number.isNaN(parsed)) throw new Error(`event "${event.id}" has invalid occurred_at`);
  return parsed;
}
