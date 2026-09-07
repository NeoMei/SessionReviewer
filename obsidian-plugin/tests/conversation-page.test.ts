import { describe, expect, it, vi } from "vitest";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { CliRunner, ConversationQueryError } from "../src/cli/runner";
import { parseConversationPageV1 } from "../src/data/conversation-page";
import {
  conversationPage,
  selectedConversationPage,
  visibleCoverage as coverage,
  visibleMessage as message,
  visibleTurn as turn
} from "./fixtures/conversation";

const VIEW_DIGEST = `sha256:${"1".repeat(64)}`;
const SOURCE_HASH = "3".repeat(64);

describe("conversation page wire", () => {
  it("accepts the shared backend fixture", () => {
    const source = readFileSync(resolve(process.cwd(), "../testdata/contracts/v4/conversation-page-v1.valid.json"), "utf8");
    expect(parseConversationPageV1(source).total).toBe(0);
  });

  it("accepts bound index and selected-message pages", () => {
    expect(parseConversationPageV1(JSON.stringify(conversationPage())).turn_units[0]?.answer_state).toBe("answered");
    const selected = parseConversationPageV1(JSON.stringify(selectedConversationPage()));
    expect(selected.messages.map((item) => item.text)).toEqual(["如何恢复可见问答？", "完整最终回答\n第二行"]);

    const preview = message("user", { visible_excerpt: "被截断的问题预览", truncated: true });
    const fullBody = parseConversationPageV1(JSON.stringify(selectedConversationPage({
      turn_units: [turn({ user_message: preview })],
      messages: [
        message("user", { visible_excerpt: "被截断的问题预览", truncated: true, text: "被截断的问题预览后的完整正文", text_truncated: true }),
        message("assistant", { text: "完整最终回答\n第二行" })
      ],
      coverage: coverage({ truncated_messages: 1, truncated_bodies: 1 })
    })));
    expect(fullBody.messages[0]?.text).toContain("完整正文");
  });

  it("accepts the production Go-rendered index after user redaction expands past the body limit", () => {
    const directory = mkdtempSync(resolve(tmpdir(), "session-reviewer-conversation-wire-"));
    const output = resolve(directory, "conversation-page.json");
    try {
      execFileSync("go", ["test", "./internal/inspect", "-run", "^TestConversationEmitsProductionExpansionWireForFrontend$", "-count=1"], {
        cwd: resolve(process.cwd(), ".."),
        env: { ...process.env, SESSION_REVIEWER_CONVERSATION_WIRE_OUT: output },
        // Cold CI workers compile the Go producer before checking its wire output.
        timeout: 120_000,
        stdio: "pipe"
      });
      const page = parseConversationPageV1(readFileSync(output, "utf8"));
      expect(page.turn_units[0]?.user_message).toMatchObject({ text: null, text_truncated: false });
    } finally {
      rmSync(directory, { recursive: true, force: true });
    }
  }, 150_000);

  it.each([
    ["unknown root field", () => conversationPage({ extra: true })],
    ["cross-Session source", () => conversationPage({ turn_units: [turn({ user_message: message("user", { source_ref: { provider: "codex", session_id: "session-other", source_identity: "source-1", record_ordinal: 1, source_hash: SOURCE_HASH } }) })] })],
    ["forged range", () => conversationPage({ range_end: 2 })],
    ["index body", () => conversationPage({ turn_units: [turn({ user_message: message("user", { text: "must stay preview-only" }) })] })],
    ["selected excerpt substitution", () => selectedConversationPage({ messages: [message("user", { text: null }), message("assistant", { text: null })] })],
    ["unanswered with assistant count", () => conversationPage({ turn_units: [turn({ answer_state: "no_answer", assistant_message_count: 1 })] })],
    ["noncanonical turn ordinal", () => conversationPage({ turn_units: [turn({ ordinal: 2 })] })],
    ["selected message total", () => selectedConversationPage({ total: 3 })],
    ["selected user revision mismatch", () => selectedConversationPage({ messages: [message("user", { revision_id: `sha256:${"9".repeat(64)}`, text: "如何恢复可见问答？" }), message("assistant", { text: "完整最终回答" })] })],
    ["selected user source ordinal mismatch", () => selectedConversationPage({ messages: [message("user", { source_ref: { provider: "codex", session_id: "session-1", source_identity: "source-1", record_ordinal: 99, source_hash: SOURCE_HASH }, text: "如何恢复可见问答？" }), message("assistant", { text: "完整最终回答" })] })],
    ["selected user timestamp mismatch", () => selectedConversationPage({ messages: [message("user", { occurred_at: "2026-09-07T00:00:01Z", text: "如何恢复可见问答？" }), message("assistant", { text: "完整最终回答" })] })],
    ["selected user excerpt mismatch", () => selectedConversationPage({ messages: [message("user", { visible_excerpt: "不同的问题预览", text: "如何恢复可见问答？" }), message("assistant", { text: "完整最终回答" })] })],
    ["selected user truncation mismatch", () => selectedConversationPage({ messages: [message("user", { truncated: true, text: "如何恢复可见问答？" }), message("assistant", { text: "完整最终回答" })], coverage: coverage({ truncated_messages: 1 }) })],
    ["malformed timestamp", () => conversationPage({ turn_units: [turn({ started_at: "today" })] })],
    ["false complete coverage", () => conversationPage({ coverage: coverage({ oversized_records: 1 }) })]
  ])("rejects %s", (_label, make) => {
    expect(() => parseConversationPageV1(JSON.stringify(make()))).toThrow();
  });

  it("rejects duplicate keys and invalid Unicode before accepting JSON shape", () => {
    const valid = JSON.stringify(conversationPage());
    expect(() => parseConversationPageV1(valid.replace('{"schema_version":1', '{"schema_version":1,"schema_version":1'))).toThrow(/duplicate/i);
    expect(() => parseConversationPageV1(valid.replace("project-p", "\\ud800"))).toThrow(/unicode|surrogate/i);
  });
});

describe("conversation CLI query", () => {
  it("uses exact fixed argv for index and selected message cursors", async () => {
    const execFile = vi.fn((_file: string, args: readonly string[], _options: unknown, callback: (error: Error | null, stdout: string, stderr: string) => void) => {
      callback(null, JSON.stringify(args.includes("--turn-unit-id") ? selectedConversationPage() : conversationPage()), "");
    });
    const runner = new CliRunner("/bin/session-reviewer", execFile);
    await runner.getConversation({
      projectId: "project-p", provider: "codex", sessionId: "session-1", expectedGenerationId: "generation-1",
      expectedSessionViewDigest: VIEW_DIGEST, limit: 20, cursor: "index-token"
    });
    await runner.getConversation({
      projectId: "project-p", provider: "codex", sessionId: "session-1", expectedGenerationId: "generation-1",
      expectedSessionViewDigest: VIEW_DIGEST, limit: 20, turnUnitId: "turn-1", messageCursor: "message-token"
    });
    await runner.getConversation({
      projectId: "project-p", provider: "codex", sessionId: "session-1", expectedGenerationId: "generation-1",
      expectedSessionViewDigest: VIEW_DIGEST, limit: 20, turnUnitId: "turn-1"
    });

    expect(execFile.mock.calls[0]?.[1]).toEqual([
      "inspect", "conversation-chain", "--project-id", "project-p", "--provider", "codex", "--session-id", "session-1",
      "--expected-generation-id", "generation-1", "--limit", "20", "--json", "--cursor", "index-token"
    ]);
    expect(execFile.mock.calls[1]?.[1]).toEqual([
      "inspect", "conversation-chain", "--project-id", "project-p", "--provider", "codex", "--session-id", "session-1",
      "--expected-generation-id", "generation-1", "--limit", "20", "--json", "--turn-unit-id", "turn-1", "--message-cursor", "message-token"
    ]);
    expect(execFile.mock.calls[2]?.[1]).toEqual([
      "inspect", "conversation-chain", "--project-id", "project-p", "--provider", "codex", "--session-id", "session-1",
      "--expected-generation-id", "generation-1", "--limit", "20", "--json", "--turn-unit-id", "turn-1"
    ]);
    expect(execFile.mock.calls[0]?.[2]).toEqual(expect.objectContaining({ shell: false, timeout: 10_000, maxBuffer: 1 << 20 }));
  });

  it("rejects response binding mismatches and invalid cursor modes", async () => {
    const execFile = vi.fn((_file: string, _args: readonly string[], _options: unknown, callback: (error: Error | null, stdout: string, stderr: string) => void) => callback(null, JSON.stringify(conversationPage({ generation_id: "generation-old" })), ""));
    const runner = new CliRunner("/bin/session-reviewer", execFile);
    const base = {
      projectId: "project-p", provider: "codex", sessionId: "session-1", expectedGenerationId: "generation-1",
      expectedSessionViewDigest: VIEW_DIGEST, limit: 20
    };
    await expect(runner.getConversation(base)).rejects.toBeInstanceOf(ConversationQueryError);
    await expect(runner.getConversation({ ...base, turnUnitId: "turn-1", cursor: "wrong-mode" })).rejects.toThrow(/cursor/i);
    await expect(runner.getConversation({ ...base, messageCursor: "wrong-mode" })).rejects.toThrow(/turn/i);
  });

  it.each([
    ["uncursored index starting after zero", { range_start: 1, range_end: 1, total: 1, turn_units: [] }],
    ["index page larger than the requested limit", { total: 2, range_end: 2, turn_units: [turn(), turn({ turn_unit_id: "turn-2", ordinal: 2 })] }]
  ])("rejects %s even when the other response bindings match", async (_label, overrides) => {
    const execFile = vi.fn((_file: string, _args: readonly string[], _options: unknown, callback: (error: Error | null, stdout: string, stderr: string) => void) => {
      callback(null, JSON.stringify(conversationPage(overrides)), "");
    });
    const runner = new CliRunner("/bin/session-reviewer", execFile);
    await expect(runner.getConversation({
      projectId: "project-p", provider: "codex", sessionId: "session-1", expectedGenerationId: "generation-1",
      expectedSessionViewDigest: VIEW_DIGEST, limit: 1
    })).rejects.toBeInstanceOf(ConversationQueryError);
  });

  it("rejects an uncursored selected response that starts after the user message", async () => {
    const response = selectedConversationPage({
      range_start: 1,
      range_end: 2,
      previous_cursor: "previous-message",
      messages: [message("assistant", { text: "完整最终回答\n第二行" })]
    });
    const runner = new CliRunner("/bin/session-reviewer", (_file, _args, _options, callback) => callback(null, JSON.stringify(response), ""));
    await expect(runner.getConversation({
      projectId: "project-p", provider: "codex", sessionId: "session-1", expectedGenerationId: "generation-1",
      expectedSessionViewDigest: VIEW_DIGEST, limit: 1, turnUnitId: "turn-1"
    })).rejects.toBeInstanceOf(ConversationQueryError);
  });

  it("accepts an authenticated page smaller than the requested limit", async () => {
    const response = conversationPage({ total: 2, range_end: 1, next_cursor: "next-index" });
    const runner = new CliRunner("/bin/session-reviewer", (_file, _args, _options, callback) => callback(null, JSON.stringify(response), ""));
    await expect(runner.getConversation({
      projectId: "project-p", provider: "codex", sessionId: "session-1", expectedGenerationId: "generation-1",
      expectedSessionViewDigest: VIEW_DIGEST, limit: 2
    })).resolves.toMatchObject({ range_start: 0, range_end: 1, next_cursor: "next-index" });
  });

  it.each([
    ["empty nonterminal page", { range_end: 0, next_cursor: "next-index", turn_units: [] }],
    ["missing previous cursor", { range_start: 1, range_end: 1, previous_cursor: null, turn_units: [] }],
    ["missing next cursor", { total: 2, range_end: 1, next_cursor: null }]
  ])("rejects %s cursor/range inconsistency", async (_label, overrides) => {
    const execFile = vi.fn((_file: string, _args: readonly string[], _options: unknown, callback: (error: Error | null, stdout: string, stderr: string) => void) => {
      callback(null, JSON.stringify(conversationPage(overrides)), "");
    });
    const runner = new CliRunner("/bin/session-reviewer", execFile);
    await expect(runner.getConversation({
      projectId: "project-p", provider: "codex", sessionId: "session-1", expectedGenerationId: "generation-1",
      expectedSessionViewDigest: VIEW_DIGEST, limit: 1, cursor: "page-token"
    })).rejects.toBeInstanceOf(ConversationQueryError);
  });

  it.each([
    ["source_unavailable", "问答来源暂不可用"],
    ["generation_mismatch", "项目已更新"],
    ["stale_cursor", "分页已失效"],
    ["unsupported_provider", "暂不支持问答读取"]
  ])("maps %s without exposing stderr", async (code, expected) => {
    const runner = new CliRunner("/bin/session-reviewer", (_file, _args, _options, callback) => callback(
      Object.assign(new Error("/private/path"), { code: 1 }),
      JSON.stringify({ error: { code, message: "/private/source detail" } }),
      "/private/stderr"
    ));
    await runner.getConversation({
      projectId: "project-p", provider: "codex", sessionId: "session-1", expectedGenerationId: "generation-1",
      expectedSessionViewDigest: VIEW_DIGEST, limit: 20
    }).catch((error: ConversationQueryError) => {
      expect(error.message).toContain(expected);
      expect(error.message).not.toContain("/private");
      expect(error.code).toBe(code);
    });
  });
});
