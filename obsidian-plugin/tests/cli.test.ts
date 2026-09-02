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
		callback(null, args[0] === "version" ? '{"version":"0.3.1","review_schema_version":3}' : "{}", "");
    });
    const runner = new CliRunner("C:\\Tools\\session-reviewer.exe", execFile as never);
    await expect(runner.verifyExecutable()).resolves.toEqual({ version: "0.3.1", reviewSchemaVersion: 3 });
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
      error_code: undefined
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
});
