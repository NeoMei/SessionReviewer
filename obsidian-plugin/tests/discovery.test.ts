import { describe, expect, it, vi } from "vitest";
import { discoverRuntime } from "../src/cli/discovery";
import type { CliRunner } from "../src/cli/runner";

describe("runtime discovery", () => {
  it("uses the standard agent-installed locations without any plugin settings", async () => {
    const home = "/Users/Neo";
    const cli = "/Users/Neo/.local/bin/session-reviewer";

    const verifyExecutable = vi.fn().mockResolvedValue({ version: "0.3.0", reviewSchemaVersion: 3 });
    const runner = { executable: cli, verifyExecutable } as unknown as CliRunner;

    const runtime = await discoverRuntime({
      home,
      platform: "darwin",
      env: { PATH: "" },
      executableExists: async (candidate: string) => candidate === cli,
      createRunner: (candidate: string) => candidate === cli ? runner : (() => { throw new Error("unexpected CLI"); })()
    });

    expect(runtime).toEqual({ runner });
    expect(verifyExecutable).toHaveBeenCalledOnce();
  });

  it("selects the native SessionReviewer executable on Windows", async () => {
    const localAppData = String.raw`C:\Users\Neo\AppData\Local`;
    const cli = String.raw`C:\Users\Neo\AppData\Local\SessionReviewer\session-reviewer.exe`;
    const verifyExecutable = vi.fn().mockResolvedValue({ version: "0.3.0", reviewSchemaVersion: 3 });
    const runner = { executable: cli, verifyExecutable } as unknown as CliRunner;

    const runtime = await discoverRuntime({
      home: String.raw`C:\Users\Neo`,
      platform: "win32",
      env: { LOCALAPPDATA: localAppData, PATH: "" },
      executableExists: async (candidate) => candidate === cli,
      createRunner: (candidate: string) => candidate === cli ? runner : (() => { throw new Error("unexpected CLI"); })()
    });

    expect(runtime).toEqual({ runner });
    expect(verifyExecutable).toHaveBeenCalledOnce();
  });
});