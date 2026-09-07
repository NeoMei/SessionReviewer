import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import type { SessionSummaryEntryV1, SessionSummaryV1 } from "../../src/contracts/review-v4";
import { parseSessionSummaryV1 } from "../../src/data/contracts-v4";

const here = dirname(fileURLToPath(import.meta.url));
export const SESSION_SUMMARY_SOURCE = readFileSync(resolve(here, "v4/session-summary-v1.valid.json"), "utf8");

export function sessionSummaryFixture(overrides: Partial<Pick<SessionSummaryV1,
  "project_id" | "provider" | "session_id" | "generation_id" | "session_view_digest">> = {}): SessionSummaryV1 {
  const fixture = structuredClone(parseSessionSummaryV1(SESSION_SUMMARY_SOURCE));
  Object.assign(fixture, overrides);
  return fixture;
}

export function summaryEntry(sequence: number, text = `操作 ${sequence}`): SessionSummaryEntryV1 {
  return {
    occurred_at: `2026-09-07T00:${String(sequence).padStart(2, "0")}:00Z`,
    sequence,
    revision_id: `revision-${sequence}`,
    text,
    source_revision_ids: [`source-${sequence}`]
  };
}

export function populatedSessionSummary(overrides: Parameters<typeof sessionSummaryFixture>[0] = {}): SessionSummaryV1 {
  const fixture = sessionSummaryFixture(overrides);
  fixture.phase_boundaries = {
    total: 1, shown: 1, omitted: 0,
    coverage: { seen: 1, indexed: 1, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 },
    items: [summaryEntry(1, "进入实现阶段")]
  };
  fixture.key_operations = {
    total: 40, shown: 32, omitted: 8,
    coverage: { seen: 40, indexed: 32, collapsed: 8, unprojected: 0, undecodable: 0, truncated: 0 },
    items: Array.from({ length: 32 }, (_value, index) => summaryEntry(index + 1, index === 0 ? "<script>window.pwned = true</script>" : `关键操作 ${index + 1}`))
  };
  fixture.verification_results = {
    total: 1, shown: 1, omitted: 0,
    coverage: { seen: 1, indexed: 1, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 },
    items: [summaryEntry(2, "聚焦测试通过")]
  };
  return fixture;
}
