import { describe, expect, it, vi } from "vitest";
import { CliRunner } from "../src/cli/runner";

describe("CLI runner", () => {
  it("uses execFile without shell and rejects non-allowlisted arguments", async () => {
    const execFile = vi.fn((_file, _args, _options, callback: (error: Error | null, stdout: string, stderr: string) => void) => callback(null, '{"project_id":"project-0123456789abcdef"}', ""));
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
      callback(null, args[0] === "version" ? '{"version":"0.2.0","review_schema_version":2}' : "{}", "");
    });
    const runner = new CliRunner("C:\\Tools\\session-reviewer.exe", execFile as never);
    await expect(runner.verifyExecutable()).resolves.toEqual({ version: "0.2.0", reviewSchemaVersion: 2 });
  });

  it("runs review commands with exact argv and maps snake_case status", async () => {
    const execFile = vi.fn((_file: string, args: readonly string[], _options: unknown, callback: (error: Error | null, stdout: string, stderr: string) => void) => {
      if (args[0] === "review" && args[1] === "agent") {
        return callback(null, JSON.stringify({ schema_version: 1, kind: "codex", compatible: true, version: "0.147.1" }), "");
      }
      return callback(null, JSON.stringify({
        schema_version: 1,
        job_id: "job-abc.def_1",
        project_id: "project-0123456789abcdef",
        state: "running",
        phase: "reviewing",
        attempt: 1,
        session_index: 0,
        session_count: 3,
        accepted_packets: 1,
        accepted_sessions: 0,
        can_retry: false,
        can_cancel: true,
        can_sync_only: false,
        review_usage: { total_tokens: 120, total_cost_usd: 0.01, pricing_complete: true },
        unknown_future_field: "ignored"
      }), "");
    });
    const runner = new CliRunner("/usr/local/bin/session-reviewer", execFile);
    await expect(runner.reviewStatus("project-0123456789abcdef")).resolves.toEqual({
      schemaVersion: 1,
      jobId: "job-abc.def_1",
      projectId: "project-0123456789abcdef",
      state: "running",
      phase: "reviewing",
      attempt: 1,
      sessionIndex: 0,
      sessionCount: 3,
      acceptedPackets: 1,
      acceptedSessions: 0,
      errorCode: undefined,
      canRetry: false,
      retryExpectedAttempt: undefined,
      retryExpectedRevision: undefined,
      canCancel: true,
      canSyncOnly: false,
      reviewUsage: { totalTokens: 120, totalCostUsd: 0.01, pricingComplete: true }
    });
    expect(execFile).toHaveBeenLastCalledWith(
      "/usr/local/bin/session-reviewer",
      ["review", "status", "--project-id", "project-0123456789abcdef", "--json"],
      expect.objectContaining({ shell: false, windowsHide: true, timeout: 10_000 }),
      expect.any(Function)
    );
    await runner.startReview("project-0123456789abcdef", "/usr/local/bin/codex");
    expect(execFile).toHaveBeenLastCalledWith(
      "/usr/local/bin/session-reviewer",
      ["review", "start", "--project-id", "project-0123456789abcdef", "--agent-executable", "/usr/local/bin/codex", "--json"],
      expect.anything(),
      expect.any(Function)
    );
    await runner.cancelReview("job-abc.def_1");
    expect(execFile).toHaveBeenLastCalledWith(
      "/usr/local/bin/session-reviewer",
      ["review", "cancel", "--job-id", "job-abc.def_1", "--json"],
      expect.anything(),
      expect.any(Function)
    );
    await runner.retryReview("job-abc.def_1", "/usr/local/bin/codex", 2, 7);
    expect(execFile).toHaveBeenLastCalledWith(
      "/usr/local/bin/session-reviewer",
      ["review", "retry", "--job-id", "job-abc.def_1", "--agent-executable", "/usr/local/bin/codex", "--expected-attempt", "2", "--expected-revision", "7", "--json"],
      expect.anything(),
      expect.any(Function)
    );
    await runner.syncProject("project-0123456789abcdef");
    expect(execFile).toHaveBeenLastCalledWith(
      "/usr/local/bin/session-reviewer",
      ["sync", "--project-id", "project-0123456789abcdef"],
      expect.anything(),
      expect.any(Function)
    );
    await runner.verifyAgent("/usr/local/bin/codex");
    expect(execFile).toHaveBeenLastCalledWith(
      "/usr/local/bin/session-reviewer",
      ["review", "agent", "verify", "--executable", "/usr/local/bin/codex", "--json"],
      expect.anything(),
      expect.any(Function)
    );
  });

  it("parses operational failure JSON from a failed exit and reports a fixed generic failure otherwise", async () => {
    const busyBody = JSON.stringify({
      schema_version: 1,
      project_id: "project-0123456789abcdef",
      state: "idle",
      attempt: 0,
      session_index: 0,
      session_count: 0,
      accepted_packets: 0,
      accepted_sessions: 0,
      error_code: "E_AGENT_BUSY",
      can_retry: false,
      can_cancel: false,
      can_sync_only: false
    });
    const exitOne = vi.fn((_file: string, _args: readonly string[], _options: unknown, callback: (error: Error | null, stdout: string, stderr: string) => void) => {
      callback(
        Object.assign(new Error("exit status 1"), { stdout: busyBody }),
        busyBody,
        ""
      );
    });
    const runner = new CliRunner("/usr/local/bin/session-reviewer", exitOne);
    const status = await runner.reviewStatus("project-0123456789abcdef");
    expect(status).toMatchObject({ state: "idle", errorCode: "E_AGENT_BUSY", canCancel: false });
    expect(JSON.stringify(status)).not.toContain("private stderr");

    const malformed = vi.fn((_file: string, _args: readonly string[], _options: unknown, callback: (error: Error | null, stdout: string, stderr: string) => void) => {
      callback(Object.assign(new Error("exit status 2"), { stdout: "not json" }), "usage text", "");
    });
    const broken = new CliRunner("/usr/local/bin/session-reviewer", malformed);
    await expect(broken.reviewStatus("project-0123456789abcdef")).rejects.toThrow("SessionReviewer review command failed");
  });

  it("rejects review status with missing or wrongly typed required fields", async () => {
    const execFile = vi.fn((_file: string, args: readonly string[], _options: unknown, callback: (error: Error | null, stdout: string, stderr: string) => void) => {
      callback(null, JSON.stringify({ schema_version: 1, project_id: "project-0123456789abcdef" }), "");
    });
    const runner = new CliRunner("/usr/local/bin/session-reviewer", execFile);
    await expect(runner.reviewStatus("project-0123456789abcdef")).rejects.toThrow("SessionReviewer review command failed");
  });

  it("returns typed agent verification for compatible and incompatible probes", async () => {
    const execFile = vi.fn((_file: string, _args: readonly string[], _options: unknown, callback: (error: Error | null, stdout: string, stderr: string) => void) => {
      callback(null, JSON.stringify({ schema_version: 1, kind: "codex", compatible: true, version: "0.147.1" }), "");
    });
    const runner = new CliRunner("/usr/local/bin/session-reviewer", execFile);
    await expect(runner.verifyAgent("/usr/local/bin/codex")).resolves.toEqual({
      schemaVersion: 1,
      kind: "codex",
      compatible: true,
      version: "0.147.1",
      errorCode: undefined
    });

    const incompatible = vi.fn((_file: string, _args: readonly string[], _options: unknown, callback: (error: Error | null, stdout: string, stderr: string) => void) => {
      callback(
        Object.assign(new Error("exit status 1"), { stdout: JSON.stringify({ schema_version: 1, kind: "codex", compatible: false, error_code: "E_AGENT_INCOMPATIBLE" }) }),
        "",
        ""
      );
    });
    const stale = new CliRunner("/usr/local/bin/session-reviewer", incompatible);
    await expect(stale.verifyAgent("/usr/local/bin/codex")).resolves.toEqual({
      schemaVersion: 1,
      kind: "codex",
      compatible: false,
      errorCode: "E_AGENT_INCOMPATIBLE"
    });
  });
});
