import { readFileSync } from "node:fs";
import { describe, expect, it, vi } from "vitest";
import { CliRunner } from "../src/cli/runner";
import { parseSessionSummaryV1 } from "../src/data/contracts-v4";
import { parseConversationPageV1 } from "../src/data/conversation-page";
import { conversationPage, selectedConversationPage } from "./fixtures/conversation";
import { sessionSummaryFixture } from "./fixtures/session-summary";

const binding = { projectId: "project-p", provider: "opencode" as const, sessionId: "ses_nativeChild", expectedGenerationId: "generation-1", expectedSessionViewDigest: `sha256:${"1".repeat(64)}` };
const identity = { provider: binding.provider, session_id: binding.sessionId };
function conversation(selected = false): Record<string, unknown> {
  // Match the native OpenCode wire, including every nested source reference.
  return JSON.parse(JSON.stringify(selected ? selectedConversationPage() : conversationPage()).replaceAll('"codex"', '"opencode"').replaceAll('"session-1"', '"ses_nativeChild"')) as Record<string, unknown>;
}
function events(): Record<string, unknown> {
  return { ...JSON.parse(readFileSync("tests/fixtures/v4/session-event-page-v1.valid.json", "utf8")) as Record<string, unknown>, ...identity };
}

describe("case-sensitive native Session identity", () => {
  it.each(["summary", "events", "conversation", "messages"])("preserves mixed case through %s request, fixed argv and response validation", async (kind) => {
    const response = kind === "summary" ? sessionSummaryFixture(identity) : kind === "events" ? events() : conversation(kind === "messages");
    const exec = vi.fn((_file: string, _args: readonly string[], _options: unknown, done: (error: Error | null, stdout: string, stderr: string) => void) => done(null, JSON.stringify(response), ""));
    const runner = new CliRunner("/bin/sr", exec);
    const result = kind === "summary" ? await runner.getSessionSummary(binding) : kind === "events" ? await runner.getSessionEvents({ ...binding, limit: 20 }) : await runner.getConversation({ ...binding, limit: 20, ...(kind === "messages" ? { turnUnitId: "turn-1" } : {}) });
    expect(result).toMatchObject(identity);
    expect(exec.mock.calls[0]?.[1]).toContain("ses_nativeChild");
    expect(exec.mock.calls[0]?.[2]).toMatchObject({ shell: false });
  });

  it("preserves native IDs in launch targets and search responses", async () => {
    const target = { project_id: binding.projectId, ...identity, generation_id: binding.expectedGenerationId, session_view_digest: binding.expectedSessionViewDigest };
    const runner = new CliRunner("/bin/sr", (_file, args, _options, done) => done(null, JSON.stringify(args[0] === "sessions" ? { schema_version: 1, ...target, state: "launch_requested" } : { schema_version: 1, project_id: binding.projectId, generation_id: binding.expectedGenerationId, total: 2, items: [identity, { ...identity, session_id: "ses_nativechild" }].map(item => ({ ...item, match_kind: "file" })), next_cursor: null, previous_cursor: null }), ""));
    await expect(runner.openNativeSession(target)).resolves.toMatchObject(identity);
    await expect(runner.getSessionSearch({ projectId: binding.projectId, expectedGenerationId: binding.expectedGenerationId, queryKind: "file", query: "test.go", limit: 20 })).resolves.toMatchObject({ total: 2 });
  });

  it.each(["../session", "ses/Child", "ses\\Child", "ses:Child", "ses\0Child", "s".repeat(129)])("rejects unsafe Session identity %j at ingress and response contracts", async (sessionId) => {
    const exec = vi.fn();
    await expect(new CliRunner("/bin/sr", exec).getSessionSummary({ ...binding, sessionId })).rejects.toThrow();
    expect(exec).not.toHaveBeenCalled();
    expect(() => parseSessionSummaryV1(JSON.stringify(sessionSummaryFixture({ ...identity, session_id: sessionId })))).toThrow();
    expect(() => parseConversationPageV1(JSON.stringify({ ...conversation(), session_id: sessionId }))).toThrow();
  });

  it("keeps provider and project ingress strict and response binding case-sensitive", async () => {
    const exec = vi.fn();
    const runner = new CliRunner("/bin/sr", exec);
    await expect(runner.getSessionSummary({ ...binding, provider: "OpenCode" })).rejects.toThrow();
    await expect(runner.getSessionSummary({ ...binding, projectId: "project-P" })).rejects.toThrow();
    expect(exec).not.toHaveBeenCalled();
    const wrong = new CliRunner("/bin/sr", (_file, _args, _options, done) => done(null, JSON.stringify(sessionSummaryFixture({ ...identity, session_id: "ses_nativechild" })), ""));
    await expect(wrong.getSessionSummary(binding)).rejects.toThrow();
  });
});
