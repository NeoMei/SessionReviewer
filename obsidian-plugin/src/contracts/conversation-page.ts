export type ConversationModeV1 = "turn_index" | "turn_messages";
export type ConversationAnswerStateV1 = "no_answer" | "partial" | "answered";

export interface VisibleMessageV1 {
  role: "user" | "assistant";
  phase: "commentary" | "final_answer" | null;
  revision_id: string;
  source_ref: {
    provider: "codex";
    session_id: string;
    source_identity: string;
    record_ordinal: number;
    source_hash: string;
  };
  occurred_at: string;
  visible_excerpt: string;
  truncated: boolean;
  text: string | null;
  text_truncated: boolean;
}

export interface VisibleTurnV1 {
  turn_unit_id: string;
  ordinal: number;
  started_at: string;
  ended_at: string | null;
  user_message: VisibleMessageV1;
  answer_state: ConversationAnswerStateV1;
  assistant_message_count: number;
}

export interface VisibleCoverageV1 {
  source_records: number;
  visible_messages: number;
  captured_messages: number;
  truncated_messages: number;
  truncated_bodies: number;
  context_messages: number;
  orphan_messages: number;
  oversized_records: number;
  malformed_records: number;
  complete: boolean;
}

export interface ConversationPageV1 {
  schema_version: 1;
  minimum_reader_version: "0.4.0";
  mode: ConversationModeV1;
  project_id: string;
  provider: "codex";
  session_id: string;
  generation_id: string;
  session_view_digest: string;
  dependency_digest: string;
  redaction_version: "visible-redaction-v1";
  turn_unit_id: string | null;
  total: number;
  range_start: number;
  range_end: number;
  first_cursor: string | null;
  previous_cursor: string | null;
  next_cursor: string | null;
  last_cursor: string | null;
  turn_units: VisibleTurnV1[];
  messages: VisibleMessageV1[];
  coverage: VisibleCoverageV1;
}
