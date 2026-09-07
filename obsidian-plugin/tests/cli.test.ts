import { describe, expect, it, vi } from "vitest";
import { CliRunner } from "../src/cli/runner";
import { syncStatusFixture } from "./fixtures/sync-status";

const DIGEST_A = `sha256:${"1".repeat(64)}`;

function eventPageFixture(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    schema_version: 1,
    minimum_reader_version: "0.4.0",
    project_id: "project-0123456789abcdef",
    provider: "codex",
    session_id: "session-1",
    generation_id: "generation-1",
    session_view_digest: DIGEST_A,
    total: 1,
    range_start: 0,
    range_end: 1,
    items: [{ kind: "message", excerpt: "用户问题示例", revision_id: "revision-1", sequence: 10, occurred_at: "2026-09-07T00:00:00Z" }],
    previous_cursor: null,
    next_cursor: null,
    first_cursor: "first-token",
    last_cursor: "last-token",
    coverage: { seen: 1, indexed: 1, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 },
    ...overrides
  };
}

describe("CLI runner", () => {
  it("runs inspect session-events with exact allowlisted argv and validates the bound page", async () => {
    const execFile = vi.fn((_file: string, _args: readonly string[], _options: unknown, callback: (error: Error | null, stdout: string, stderr: string) => void) => {
      callback(null, JSON.stringify(eventPageFixture()), "");
    });
    const runner = new CliRunner("/usr/local/bin/session-reviewer", execFile);

    const page = await runner.getSessionEvents({
      projectId: "project-0123456789abcdef",
      provider: "codex",
      sessionId: "session-1",
      expectedGenerationId: "generation-1",
      expectedSessionViewDigest: DIGEST_A,
      limit: 25,
      cursor: "opaque-cursor-token"
    });

    expect(page.items[0]?.excerpt).toBe("用户问题示例");
    expect(execFile).toHaveBeenCalledWith(
      "/usr/local/bin/session-reviewer",
      ["inspect", "session-events", "--project-id", "project-0123456789abcdef", "--provider", "codex", "--session-id", "session-1", "--expected-generation-id", "generation-1", "--limit", "25", "--json", "--cursor", "opaque-cursor-token"],
      expect.objectContaining({ shell: false, windowsHide: true, timeout: 10_000 }),
      expect.any(Function)
    );
  });

  it.each([
    { label: "wrong project", patch: { project_id: "project-other" } },
    { label: "wrong provider", patch: { provider: "claude" } },
    { label: "wrong Session", patch: { session_id: "session-other" } },
    { label: "stale generation", patch: { generation_id: "generation-old" } },
    { label: "wrong public digest", patch: { session_view_digest: `sha256:${"2".repeat(64)}` } },
    { label: "invalid event contract", patch: { range_end: 2 } }
  ])("rejects an inspect page with $label using retryable guidance", async ({ patch }) => {
    const runner = new CliRunner("/bin/session-reviewer", (_file, _args, _options, callback) => {
      callback(null, JSON.stringify(eventPageFixture(patch)), "");
    });

    await expect(runner.getSessionEvents({
      projectId: "project-0123456789abcdef",
      provider: "codex",
      sessionId: "session-1",
      expectedGenerationId: "generation-1",
      expectedSessionViewDigest: DIGEST_A,
      limit: 25
    })).rejects.toThrow("无法读取扫描 Session；请刷新项目后重试，并确认 CLI 已更新。");
  });

  it.each([
    { label: "old CLI failure", error: Object.assign(new Error("unknown command"), { code: 1 }), stdout: "" },
    { label: "malformed JSON", error: null, stdout: "not-json" },
    { label: "machine error payload", error: Object.assign(new Error("exit 1"), { code: 1 }), stdout: JSON.stringify({ error: { code: "stale_generation", message: "published generation changed" } }) }
  ])("never turns $label into an empty event page", async ({ error, stdout }) => {
    const runner = new CliRunner("/bin/session-reviewer", (_file, _args, _options, callback) => callback(error, stdout, "/private/secret"));

    await expect(runner.getSessionEvents({
      projectId: "project-0123456789abcdef",
      provider: "codex",
      sessionId: "session-1",
      expectedGenerationId: "generation-1",
      expectedSessionViewDigest: DIGEST_A,
      limit: 25,
      anchor: 1
    })).rejects.toThrow("无法读取扫描 Session；请刷新项目后重试，并确认 CLI 已更新。");
  });

  it("rejects unsafe inspect request values before execution", async () => {
    const execFile = vi.fn();
    const runner = new CliRunner("/bin/session-reviewer", execFile);

    await expect(runner.getSessionEvents({
      projectId: "project-0123456789abcdef",
      provider: "codex",
      sessionId: "session-1",
      expectedGenerationId: "generation-1",
      expectedSessionViewDigest: DIGEST_A,
      limit: 25,
      cursor: "cursor",
      anchor: 1
    })).rejects.toThrow(/cursor and anchor/);
    expect(execFile).not.toHaveBeenCalled();
  });

  it.each([
    { label: "uppercase provider", patch: { provider: "Codex" } },
    { label: "overlong Session ID", patch: { sessionId: `s${"x".repeat(128)}` } },
    { label: "flag-like cursor", patch: { cursor: "--project-id" } }
  ])("keeps $label outside the inspect argv allowlist", async ({ patch }) => {
    const execFile = vi.fn();
    const runner = new CliRunner("/bin/session-reviewer", execFile);

    await expect(runner.getSessionEvents({
      projectId: "project-0123456789abcdef",
      provider: "codex",
      sessionId: "session-1",
      expectedGenerationId: "generation-1",
      expectedSessionViewDigest: DIGEST_A,
      limit: 25,
      ...patch
    })).rejects.toThrow(/invalid/);
    expect(execFile).not.toHaveBeenCalled();
  });

  it.each([
    { code: "ENOENT", expected: "cli_unavailable" },
    { code: "EACCES", expected: "cli_unavailable" },
    { code: 1, expected: "sync_status_failed" },
    { code: "ETIMEDOUT", expected: "sync_status_failed" }
  ])("classifies status failure $code without echoing process output", async ({ code, expected }) => {
    const secret = "/private/customer/project secret-output";
    const runner = new CliRunner("/missing/session-reviewer", (_file, _args, _options, callback) => {
      callback(Object.assign(new Error(secret), { code }), JSON.stringify(syncStatusFixture()), secret.repeat(5000));
    });
    await expect(runner.status("project-0123456789abcdef")).rejects.toMatchObject({ code: expected });
    await runner.status("project-0123456789abcdef").catch((error: Error) => {
      expect(error.message.length).toBeLessThan(150);
      expect(error.message).not.toContain(secret);
    });
  });

  it.each([
    null, [], { project_id: "project-0123456789abcdef" },
    syncStatusFixture("project-other"), syncStatusFixture(undefined, { conflicted: -1 }),
    syncStatusFixture(undefined, { hidden_conflict_ids: "conflict-a" }),
    syncStatusFixture(undefined, { pending_operations: [{ entity_id: "x", kind: 3 }] }),
    syncStatusFixture(undefined, { machine_state: "accepted" }), syncStatusFixture(undefined, { machine_state: ["current"] })
  ])("rejects invalid sync Status payload %# without accepting success", async (payload) => {
    const runner = new CliRunner("/bin/session-reviewer", (_file, _args, _options, callback) => callback(null, JSON.stringify(payload), ""));
    await expect(runner.status("project-0123456789abcdef")).rejects.toMatchObject({ code: "sync_status_failed" });
  });

  it("returns the existing Status wire for a pending authenticated aggregate", async () => {
    const status = syncStatusFixture(undefined, { in_sync: 0, machine_state: "pending", derived_state: "pending", pending: [{ entity_id: "project-overview", kind: "update_project", target: "project", relative_path: "项目回顾.md" }] });
    const runner = new CliRunner("/bin/session-reviewer", (_file, _args, _options, callback) => callback(null, JSON.stringify(status), ""));
    await expect(runner.status("project-0123456789abcdef")).resolves.toEqual(status);
  });
  it("uses execFile without shell and rejects non-allowlisted arguments", async () => {
    const execFile = vi.fn((_file, _args, _options, callback: (error: Error | null, stdout: string, stderr: string) => void) => callback(null, JSON.stringify(syncStatusFixture()), ""));
    const runner = new CliRunner("/usr/local/bin/session-reviewer", execFile as never);
    await runner.status("project-0123456789abcdef");
    expect(execFile).toHaveBeenCalledWith(
      "/usr/local/bin/session-reviewer",
      ["sync", "status", "--json", "--project-id", "project-0123456789abcdef"],
      expect.objectContaining({ shell: false, windowsHide: true, timeout: 10_000, maxBuffer: 1 << 20 }),
      expect.any(Function)
    );
    const rawRun = (runner as unknown as { run: (args: readonly string[]) => Promise<unknown> }).run;
    await expect(rawRun.call(runner, ["sync", "--cwd", "x", "&&", "open", "/tmp"])).rejects.toThrow(/command is not allowed/);
  });

  it("verifies semantic version and review schema", async () => {
    const execFile = vi.fn((_file, args: string[], _options, callback: (error: Error | null, stdout: string, stderr: string) => void) => {
		callback(null, args[0] === "version" ? '{"version":"0.3.2","review_schema_version":3}' : "{}", "");
    });
    const runner = new CliRunner("C:\\Tools\\session-reviewer.exe", execFile as never);
    await expect(runner.verifyExecutable()).resolves.toEqual({ version: "0.3.2", reviewSchemaVersion: 3 });
  });

  it("runs scan commands with exact argv and maps snake_case status", async () => {
    const execFile = vi.fn((_file: string, args: readonly string[], _options: unknown, callback: (error: Error | null, stdout: string, stderr: string) => void) => {
      return callback(null, JSON.stringify({
        schema_version: 1,
        job_id: "scan-123456",
        project_id: "project-0123456789abcdef",
        state: "running",
        phase: "extracting",
        session_count: 5,
        indexed_count: 3,
        issue_count: 0,
      }), "");
    });
    const runner = new CliRunner("/usr/local/bin/session-reviewer", execFile);
    await expect(runner.getScanStatus("project-0123456789abcdef")).resolves.toEqual({
      schema_version: 1,
      job_id: "scan-123456",
      project_id: "project-0123456789abcdef",
      state: "running",
      phase: "extracting",
      session_count: 5,
      indexed_count: 3,
      issue_count: 0,
      generation_id: undefined,
      error_code: undefined,
	  error_message: undefined
    });
    expect(execFile).toHaveBeenLastCalledWith(
      "/usr/local/bin/session-reviewer",
      ["scan", "status", "--project-id", "project-0123456789abcdef", "--json"],
      expect.objectContaining({ shell: false, windowsHide: true, timeout: 10_000 }),
      expect.any(Function)
    );
    await runner.startScan("project-0123456789abcdef");
    expect(execFile).toHaveBeenLastCalledWith(
      "/usr/local/bin/session-reviewer",
      ["scan", "start", "--project-id", "project-0123456789abcdef", "--json"],
      expect.objectContaining({ timeout: 10_000 }),
      expect.any(Function)
    );
  });

  it("gives an explicit sync enough time for a large project", async () => {
	const execFile = vi.fn((_file: string, _args: readonly string[], _options: unknown, callback: (error: Error | null, stdout: string, stderr: string) => void) => callback(null, "synced", ""));
	const runner = new CliRunner("/usr/local/bin/session-reviewer", execFile);
	await runner.syncProject("project-0123456789abcdef");
	expect(execFile).toHaveBeenLastCalledWith(
		"/usr/local/bin/session-reviewer",
		["sync", "--project-id", "project-0123456789abcdef"],
		expect.objectContaining({ timeout: 120_000 }),
		expect.any(Function)
	);
  });

  it("retains the bounded worker error for a failed scan", async () => {
	const execFile = vi.fn((_file: string, _args: readonly string[], _options: unknown, callback: (error: Error | null, stdout: string, stderr: string) => void) => callback(null, JSON.stringify({
		schema_version: 1,
		job_id: "scan-123456",
		project_id: "project-0123456789abcdef",
		state: "failed",
		phase: "discovering",
		session_count: 0,
		indexed_count: 0,
		issue_count: 0,
		error_code: "scan_failed",
		error_message: "project association requires explicit confirmation",
	}), ""));
	const runner = new CliRunner("/usr/local/bin/session-reviewer", execFile);
	await expect(runner.getScanStatus("project-0123456789abcdef")).resolves.toMatchObject({
		error_code: "scan_failed",
		error_message: "project association requires explicit confirmation",
	});
  });

  it.each([
	{ label: "wrong project", patch: { project_id: "project-bbbbbbbbbbbbbbbb" } },
	{ label: "unknown phase", patch: { phase: "teleporting" } },
	{ label: "negative count", patch: { indexed_count: -1 } },
	{ label: "fractional count", patch: { session_count: 1.5 } },
	{ label: "count above total", patch: { session_count: 1, issue_count: 2 } },
	{ label: "unsafe job ID", patch: { job_id: "../scan" } },
	{ label: "unsafe generation ID", patch: { generation_id: "../generation" } },
	{ label: "unknown field", patch: { unexpected: true } },
	{ label: "unsafe error code", patch: { error_code: "BAD CODE" } },
  ])("rejects malformed scan status: $label", async ({ patch }) => {
	const execFile = vi.fn((_file: string, _args: readonly string[], _options: unknown, callback: (error: Error | null, stdout: string, stderr: string) => void) => {
		callback(null, JSON.stringify({
			schema_version: 1,
			job_id: "scan-123456",
			project_id: "project-0123456789abcdef",
			state: "running",
			phase: "extracting",
			session_count: 5,
			indexed_count: 3,
			issue_count: 0,
			...patch,
		}), "");
	});
	const runner = new CliRunner("/usr/local/bin/session-reviewer", execFile);
	await expect(runner.getScanStatus("project-0123456789abcdef")).rejects.toThrow("SessionReviewer scan command failed");
  });
});
