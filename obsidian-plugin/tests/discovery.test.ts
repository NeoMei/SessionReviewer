import { describe, expect, it, vi } from "vitest";
import { discoverRuntime } from "../src/cli/discovery";
import type { CliRunner } from "../src/cli/runner";

describe("runtime discovery", () => {
  it("uses the standard agent-installed locations without any plugin settings", async () => {
    const home = "/Users/Neo";
    const cli = "/Users/Neo/.local/bin/session-reviewer";
    const agent = "/Users/Neo/.npm-global/bin/codex";

    const verifyExecutable = vi.fn().mockResolvedValue({ version: "0.2.9", reviewSchemaVersion: 2 });
    const verifyAgent = vi.fn(async (candidate: string) => ({
      schemaVersion: 1 as const,
      kind: "codex" as const,
      compatible: candidate === agent,
      version: candidate === agent ? "0.150.1" : undefined
    }));
    const runner = { executable: cli, verifyExecutable, verifyAgent } as unknown as CliRunner;

    const runtime = await discoverRuntime({
      home,
      platform: "darwin",
      env: { PATH: "" },
      executableExists: async (candidate: string) => candidate === cli || candidate === agent,
      createRunner: (candidate: string) => candidate === cli ? runner : (() => { throw new Error("unexpected CLI"); })()
    });

    expect(runtime).toEqual({ runner, agentExecutable: agent });
    expect(verifyExecutable).toHaveBeenCalledOnce();
    expect(verifyAgent).toHaveBeenCalledWith(agent);
  });

  it.skipIf(!process.env.SESSIONREVIEWER_REAL_CLI || !process.env.SESSIONREVIEWER_REAL_AGENT)("discovers and verifies the real local Agent installation", async () => {
    const runtime = await discoverRuntime();

    expect(runtime?.runner.executable).toBe(process.env.SESSIONREVIEWER_REAL_CLI);
    expect(runtime?.agentExecutable).toBe(process.env.SESSIONREVIEWER_REAL_AGENT);
  });

  it("selects the native Codex executable from a Windows npm installation", async () => {
    const localAppData = String.raw`C:\Users\Neo\AppData\Local`;
    const appData = String.raw`C:\Users\Neo\AppData\Roaming`;
    const cli = String.raw`C:\Users\Neo\AppData\Local\SessionReviewer\session-reviewer.exe`;
    const agent = String.raw`C:\Users\Neo\AppData\Roaming\npm\node_modules\@openai\codex\node_modules\@openai\codex-win32-x64\vendor\x86_64-pc-windows-msvc\bin\codex.exe`;
    const verifyExecutable = vi.fn().mockResolvedValue({ version: "0.2.10", reviewSchemaVersion: 2 });
    const verifyAgent = vi.fn().mockResolvedValue({ schemaVersion: 1, kind: "codex", compatible: true, version: "0.150.1" });
    const runner = { executable: cli, verifyExecutable, verifyAgent } as unknown as CliRunner;

    const runtime = await discoverRuntime({
      home: String.raw`C:\Users\Neo`,
      platform: "win32",
      env: { LOCALAPPDATA: localAppData, APPDATA: appData, PATH: "" },
      executableExists: async (candidate) => candidate === cli || candidate === agent,
      createRunner: (candidate: string) => candidate === cli ? runner : (() => { throw new Error("unexpected CLI"); })()
    });

    expect(runtime).toEqual({ runner, agentExecutable: agent });
    expect(verifyAgent).toHaveBeenCalledWith(agent);
  });
});
