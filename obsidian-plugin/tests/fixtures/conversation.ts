const VIEW_DIGEST = `sha256:${"1".repeat(64)}`;
const DEPENDENCY_DIGEST = `sha256:${"2".repeat(64)}`;
const SOURCE_HASH = "3".repeat(64);
const REVISION_ID = `sha256:${"4".repeat(64)}`;

export function visibleMessage(role: "user" | "assistant", overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    role,
    phase: role === "assistant" ? "final_answer" : null,
    revision_id: REVISION_ID,
    source_ref: { provider: "codex", session_id: "session-1", source_identity: "source-1", record_ordinal: role === "user" ? 1 : 2, source_hash: SOURCE_HASH },
    occurred_at: role === "user" ? "2026-09-07T00:00:00Z" : "2026-09-07T00:01:00Z",
    visible_excerpt: role === "user" ? "如何恢复可见问答？" : "已经恢复。",
    truncated: false,
    text: null,
    text_truncated: false,
    ...overrides
  };
}

export function visibleTurn(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    turn_unit_id: "turn-1", ordinal: 1, started_at: "2026-09-07T00:00:00Z", ended_at: null,
    user_message: visibleMessage("user"), answer_state: "answered", assistant_message_count: 1, ...overrides
  };
}

export function visibleCoverage(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    source_records: 2, visible_messages: 2, captured_messages: 2, truncated_messages: 0, truncated_bodies: 0,
    context_messages: 0, orphan_messages: 0, oversized_records: 0, malformed_records: 0, complete: true, ...overrides
  };
}

export function conversationPage(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    schema_version: 1, minimum_reader_version: "0.4.0", mode: "turn_index", project_id: "project-p", provider: "codex",
    session_id: "session-1", generation_id: "generation-1", session_view_digest: VIEW_DIGEST, dependency_digest: DEPENDENCY_DIGEST,
    redaction_version: "visible-redaction-v1", turn_unit_id: null, total: 1, range_start: 0, range_end: 1,
    first_cursor: "first-index", previous_cursor: null, next_cursor: null, last_cursor: "last-index",
    turn_units: [visibleTurn()], messages: [], coverage: visibleCoverage(), ...overrides
  };
}

export function selectedConversationPage(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return conversationPage({
    mode: "turn_messages", turn_unit_id: "turn-1", total: 2, range_start: 0, range_end: 2,
    first_cursor: "first-message", last_cursor: "last-message", turn_units: [visibleTurn()],
    messages: [visibleMessage("user", { text: "如何恢复可见问答？" }), visibleMessage("assistant", { text: "完整最终回答\n第二行" })],
    ...overrides
  });
}
