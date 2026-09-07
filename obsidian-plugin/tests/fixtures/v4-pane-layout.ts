import ledgerContract from "../../../testdata/contracts/v4/machine-ledger-v4.valid.json";
import projectionContract from "../../../testdata/contracts/v4/markdown/ledger.json";
import indexContract from "../../../testdata/contracts/v4/markdown/index.json";
import { renderMarkdownV4View } from "../../src/view/presentation";
import type { ConversationPageV1 } from "../../src/contracts/conversation-page";
import type { MachineLedgerV4, ReviewPresentationV4, SessionEventPageV1, SessionIndexV1, SessionSummaryV1 } from "../../src/contracts/review-v4";

declare global {
  function createEl<K extends keyof HTMLElementTagNameMap>(tag: K): HTMLElementTagNameMap[K];
}

const digest = (digit: string): `sha256:${string}` => `sha256:${digit.repeat(64)}`;
const longQuestion = `如何确保${"超长未分隔问题文本".repeat(24)}仍在当前面板内完整换行？`;

function conversationPage(selected: boolean): ConversationPageV1 {
  const user = {
    role: "user" as const,
    phase: null,
    revision_id: digest("4"),
    source_ref: { provider: "codex" as const, session_id: "session-1", source_identity: "fixture-source", record_ordinal: 1, source_hash: "5".repeat(64) },
    occurred_at: "2026-09-07T00:00:00Z",
    visible_excerpt: longQuestion,
    truncated: false,
    text: selected ? longQuestion : null,
    text_truncated: false
  };
  const assistant = {
    role: "assistant" as const,
    phase: "final_answer" as const,
    revision_id: digest("6"),
    source_ref: { provider: "codex" as const, session_id: "session-1", source_identity: "fixture-source", record_ordinal: 2, source_hash: "7".repeat(64) },
    occurred_at: "2026-09-07T00:01:00Z",
    visible_excerpt: "已保留问题树、上下文和证据顺序。",
    truncated: false,
    text: selected ? "已保留问题树、上下文和证据顺序。" : null,
    text_truncated: false
  };
  const turn = {
    turn_unit_id: "turn-layout",
    ordinal: 1,
    started_at: user.occurred_at,
    ended_at: assistant.occurred_at,
    user_message: user,
    answer_state: "answered" as const,
    assistant_message_count: 1
  };
  return {
    schema_version: 1,
    minimum_reader_version: "0.4.0",
    mode: selected ? "turn_messages" : "turn_index",
    project_id: "project-p",
    provider: "codex",
    session_id: "session-1",
    generation_id: "generation-1",
    session_view_digest: digest("2"),
    dependency_digest: digest("3"),
    redaction_version: "visible-redaction-v1",
    turn_unit_id: selected ? turn.turn_unit_id : null,
    total: selected ? 2 : 1,
    range_start: 0,
    range_end: selected ? 2 : 1,
    first_cursor: "first",
    previous_cursor: null,
    next_cursor: null,
    last_cursor: "last",
    turn_units: [turn],
    messages: selected ? [user, assistant] : [],
    coverage: {
      source_records: 2,
      visible_messages: 2,
      captured_messages: 2,
      truncated_messages: 0,
      truncated_bodies: 0,
      context_messages: 0,
      orphan_messages: 0,
      oversized_records: 0,
      malformed_records: 0,
      complete: true
    }
  };
}

function sessionEvents(): SessionEventPageV1 {
  return {
    schema_version: 1,
    minimum_reader_version: "0.4.0",
    project_id: "project-p",
    provider: "codex",
    session_id: "session-1",
    generation_id: "generation-1",
    session_view_digest: digest("2"),
    total: 1,
    range_start: 0,
    range_end: 1,
    items: [{ sequence: 1, revision_id: digest("8"), occurred_at: "2026-09-07T00:02:00Z", kind: "verification", excerpt: longQuestion }],
    previous_cursor: null,
    next_cursor: null,
    first_cursor: "first",
    last_cursor: "last",
    coverage: { seen: 1, indexed: 1, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 }
  };
}

function sessionSummary(): SessionSummaryV1 {
  const empty = { total: 0, shown: 0, omitted: 0, coverage: { seen: 0, indexed: 0, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 }, items: [] };
  return {
    schema_version: 1,
    minimum_reader_version: "0.4.0",
    project_id: "project-p",
    provider: "codex",
    session_id: "session-1",
    generation_id: "generation-1",
    session_view_digest: digest("2"),
    phase_boundaries: empty,
    key_operations: empty,
    verification_results: empty,
    errors: empty,
    unresolved_questions: empty,
    rules: { rule_id: "fixture", rule_version: "v1", dependency_digests: [] },
    coverage: { seen: 0, indexed: 0, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 }
  };
}

export function mountV4PaneLayoutFixture(target: HTMLElement): HTMLElement {
  globalThis.createEl = (tag) => document.createElement(tag);
  const ledger = structuredClone(ledgerContract) as unknown as MachineLedgerV4;
  const index = structuredClone(indexContract) as unknown as SessionIndexV1;
  index.sessions[0].provider = "codex";
  const presentation = structuredClone(projectionContract.document_projection.presentation_base) as unknown as ReviewPresentationV4;
  const template = presentation.problem_nodes[0];
  presentation.current_state.goal = `验证${"面板宽度容器内容不越界".repeat(16)}`;
  presentation.problem_root_ids = ["problem:root"];
  presentation.problem_nodes = [
    { ...structuredClone(template), id: "problem:root", question: "根问题：如何恢复项目上下文？", primary_parent_id: null, sibling_order: 0 },
    { ...structuredClone(template), id: "problem:child", question: "子问题：如何在宽窗口的窄面板中布局？", primary_parent_id: "problem:root", sibling_order: 0 },
    {
      ...structuredClone(template),
      id: "problem:deep",
      question: longQuestion,
      primary_parent_id: "problem:child",
      sibling_order: 0,
      source_turn_refs: [{ provider: "codex", session_id: "session-1", turn_unit_id: "turn-layout" }]
    }
  ];
  presentation.timeline[0].closed_loop.trigger_question = {
    state: "present",
    text: "宽窗口内的窄 Obsidian pane 为何裁切右侧详情？",
    missing_reason: null,
    source_turn_refs: [{ provider: "codex", session_id: "session-1", turn_unit_id: "turn-layout" }]
  };
  presentation.timeline[0].closed_loop.source_turn_refs = [{ provider: "codex", session_id: "session-1", turn_unit_id: "turn-layout" }];
  ledger.accounting = { total_duration_ms: 60_000, total_tokens: 15, total_cost_usd: null, models: [{ model: "model-1", total_tokens: 15, total_cost_usd: null }] };
  const snapshot = {
    kind: "markdown-v4" as const,
    descriptor: { projectId: "project-p", root: "Projects/Fixture", name: "SessionReviewer 面板响应式夹具", format: "markdown-v4" as const },
    loadedAt: 1,
    state: {
      kind: "public_valid" as const,
      value: { presentation, changedFields: [], changedDocuments: [], fields: [] },
      ledger,
      index
    }
  };
  const root = renderMarkdownV4View(snapshot, () => undefined, {
    loadConversation: async (request) => conversationPage(Boolean(request.turnUnitId)),
    loadSessionEvents: async () => sessionEvents(),
    loadSessionSummary: async () => sessionSummary()
  });
  target.append(root);
  return root;
}

export { longQuestion };
