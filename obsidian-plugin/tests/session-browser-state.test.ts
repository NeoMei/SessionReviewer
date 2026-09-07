import { afterAll, beforeAll, describe, expect, it } from "vitest";
import type { SessionIndexEntryV1 } from "../src/contracts/review-v4";
import { filterSessions, normalizeSessionBrowserState, type SessionBrowserState } from "../src/state/session-browser-state";

const originalTimezone = process.env.TZ;

const defaults: SessionBrowserState = {
  query: "", provider: null, processingState: null, sourceAvailability: null,
  dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: null
};

function session(
  provider: string,
  sessionId: string,
  processingState: SessionIndexEntryV1["processing_state"],
  sourceAvailability: SessionIndexEntryV1["source_availability"],
  startedAt: string | null
): SessionIndexEntryV1 {
  return {
    provider, session_id: sessionId, processing_state: processingState, source_availability: sourceAvailability,
    started_at: startedAt, ended_at: null, duration_ms: null, warning_count: 0, record_count: 0, indexed_event_count: 0,
    state_reason_codes: [], source_terminal_state: null,
    coverage: { seen: 0, indexed: 0, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 },
    fact_counts: { file_change: 0, command: 0, verification: 0, error: 0, artifact: 0 },
    session_view_digest: null, usage_record_digest: null, summary_digest: null,
    last_seen_generation_id: null, last_successful_generation_id: null
  };
}

const fixtures = [
  session("codex", "same-id", "complete", "available", "2026-09-07T00:00:00+08:00"),
  session("claude", "same-id", "partial", "unavailable", "2026-09-07T23:59:59+08:00"),
  session("opencode", "ERROR.literal[1]", "error", "available", "2026-09-06T16:30:00Z"),
  session("codex", "unknown-date", "unprocessed", "unavailable", null)
];

beforeAll(() => { process.env.TZ = "Asia/Shanghai"; });
afterAll(() => { process.env.TZ = originalTimezone; });

describe("Session browser state", () => {
  it("normalizes only bounded persisted values and ignores malicious properties", () => {
    expect(normalizeSessionBrowserState({
      query: "🙂".repeat(64), provider: "claude", processingState: "partial", sourceAvailability: "unavailable",
      dateFrom: "2024-02-29", dateTo: "2026-09-07", unknownDateOnly: false, page: 2621,
      selected: { provider: "claude", sessionId: "same-id", rawContent: "secret" }, rawContent: "secret", cursor: "stale"
    })).toEqual({
      query: "🙂".repeat(64), provider: "claude", processingState: "partial", sourceAvailability: "unavailable",
      dateFrom: "2024-02-29", dateTo: "2026-09-07", unknownDateOnly: false, page: 2621,
      selected: { provider: "claude", sessionId: "same-id" }
    });
    expect(normalizeSessionBrowserState({
      query: "🙂".repeat(65), provider: "../unsafe", processingState: "done", sourceAvailability: "missing",
      dateFrom: "2025-02-29", dateTo: "2024-01-01", unknownDateOnly: "yes", page: 2622,
      selected: { provider: "codex", sessionId: "x".repeat(129) }
    })).toEqual(defaults);
    expect(normalizeSessionBrowserState({ page: -1, query: "x".repeat(257) }).page).toBe(0);
  });

  it("combines exact provider, processing, availability, literal query and inclusive local dates with AND", () => {
    expect(filterSessions(fixtures, { ...defaults, provider: "claude", sourceAvailability: "unavailable" })
      .map((value) => [value.provider, value.session_id])).toEqual([["claude", "same-id"]]);
    expect(filterSessions(fixtures, {
      ...defaults, query: "error.literal[1]", provider: "opencode", processingState: "error",
      sourceAvailability: "available", dateFrom: "2026-09-07", dateTo: "2026-09-07"
    }).map((value) => value.session_id)).toEqual(["ERROR.literal[1]"]);
    expect(filterSessions(fixtures, { ...defaults, dateFrom: "2026-09-07", dateTo: "2026-09-07" })
      .map((value) => value.session_id)).toEqual(["same-id", "same-id", "ERROR.literal[1]"]);
  });

  it("treats unknown dates as an exclusive filter and excludes them from active intervals", () => {
    expect(filterSessions(fixtures, { ...defaults, dateFrom: "2026-09-07" }).map((value) => value.session_id))
      .toEqual(["same-id", "same-id", "ERROR.literal[1]"]);
    expect(filterSessions(fixtures, { ...defaults, unknownDateOnly: true, dateFrom: "2026-09-07", dateTo: "2026-09-07" })
      .map((value) => value.session_id)).toEqual(["unknown-date"]);
  });

  it("keeps canonical order and never mutates input entries or array bytes", () => {
    const before = JSON.stringify(fixtures);
    const result = filterSessions(fixtures, { ...defaults, query: "same-id" });
    expect(result.map((value) => value.provider)).toEqual(["codex", "claude"]);
    expect(JSON.stringify(fixtures)).toBe(before);
    expect(result[0]).toBe(fixtures[0]);
  });
});
