export type SessionInspectErrorCode = "stale_cursor" | "generation_mismatch" | "anchor_out_of_range" | "unavailable";

const SESSION_EVENTS_FAILED = "无法读取扫描 Session；请刷新项目后重试，并确认 CLI 已更新。";

export class SessionInspectError extends Error {
  constructor(readonly code: SessionInspectErrorCode) {
    super(SESSION_EVENTS_FAILED);
    this.name = "SessionInspectError";
  }
}
