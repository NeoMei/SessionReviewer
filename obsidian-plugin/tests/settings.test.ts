import { describe, expect, it, vi } from "vitest";
import { CliSettingsTab } from "../src/cli/settings";
import { Notice } from "./mocks/obsidian";

import { beforeEach } from "vitest";
import { Setting } from "./mocks/obsidian";

describe("CLI settings", () => {
  beforeEach(() => {
    Setting.instances.splice(0);
  });

  it("renders the title with Obsidian's setting heading component", () => {
    const tab = new CliSettingsTab({} as never, {} as never, "", vi.fn(), "", vi.fn());

    tab.display();

    const heading = tab.containerEl.querySelector(".setting-item.mod-heading .setting-item-name");
    expect(heading?.textContent).toBe("CLI");
    expect(tab.containerEl.querySelector("h2")).toBeNull();
  });

  it("adds a separate Codex path field without changing the existing CLI section", () => {
    const tab = new CliSettingsTab({} as never, {} as never, "/bin/sr", vi.fn(), "/bin/codex", vi.fn());

    tab.display();

    const names = Array.from(tab.containerEl.querySelectorAll(".setting-item-name")).map((node) => node.textContent);
    expect(names).toEqual(["CLI", "CLI 可执行文件", "Codex 可执行文件"]);
    const [heading, cliSetting, codexSetting] = Setting.instances;
    expect(heading?.buttonControls).toHaveLength(0);
    expect(cliSetting?.textControls[0]?.value).toBe("/bin/sr");
    expect(codexSetting?.textControls[0]?.value).toBe("/bin/codex");
  });

  it("saves the Codex path only after successful verification", async () => {
    const saveCodexPath = vi.fn();
    const verifyAgent = vi.fn().mockResolvedValue({ schemaVersion: 1, kind: "codex", compatible: true, version: "0.147.1" });
    const tab = new CliSettingsTab({} as never, {} as never, "/bin/sr", vi.fn(), "/bin/codex", saveCodexPath, () => ({
      verifyExecutable: vi.fn(),
      verifyAgent
    }));
    tab.display();

    const codexSetting = Setting.instances.at(-1);
    await codexSetting?.buttonControls[0]?.onClickHandler?.();

    expect(verifyAgent).toHaveBeenCalledWith("/bin/codex");
    expect(saveCodexPath).toHaveBeenCalledWith("/bin/codex");
  });

  it("refuses to save when the Codex probe reports incompatibility", async () => {
    const saveCodexPath = vi.fn();
    const verifyAgent = vi.fn().mockResolvedValue({ schemaVersion: 1, kind: "codex", compatible: false, errorCode: "E_AGENT_INCOMPATIBLE" });
    const tab = new CliSettingsTab({} as never, {} as never, "/bin/sr", vi.fn(), "/bin/codex", saveCodexPath, () => ({
      verifyExecutable: vi.fn(),
      verifyAgent
    }));
    tab.display();

    const codexSetting = Setting.instances.at(-1);
    await codexSetting?.buttonControls[0]?.onClickHandler?.();

    expect(saveCodexPath).not.toHaveBeenCalled();
  });

  it("runs the Codex probe through the configured SessionReviewer CLI, not the Codex binary", async () => {
    const saveCodexPath = vi.fn();
    const verifyAgent = vi.fn().mockResolvedValue({ schemaVersion: 1, kind: "codex", compatible: true, version: "0.147.1" });
    const runnerExecutables: string[] = [];
    const tab = new CliSettingsTab({} as never, {} as never, "/bin/sr", vi.fn(), "/bin/codex", saveCodexPath, (executable) => {
      runnerExecutables.push(executable);
      return {
        verifyExecutable: vi.fn(),
        verifyAgent
      };
    });
    tab.display();

    const codexSetting = Setting.instances.at(-1);
    await codexSetting?.buttonControls[0]?.onClickHandler?.();

    expect(runnerExecutables).toEqual(["/bin/sr"]);
    expect(verifyAgent).toHaveBeenCalledWith("/bin/codex");
    expect(saveCodexPath).toHaveBeenCalledWith("/bin/codex");
  });

  it("explains the missing Codex path instead of surfacing a runner failure", async () => {
    Notice.instances.length = 0;
    const saveCodexPath = vi.fn();
    const verifyAgent = vi.fn();
    const tab = new CliSettingsTab({} as never, {} as never, "/bin/sr", vi.fn(), "", saveCodexPath, () => ({
      verifyExecutable: vi.fn(),
      verifyAgent
    }));
    tab.display();

    const codexSetting = Setting.instances.at(-1);
    await codexSetting?.buttonControls[0]?.onClickHandler?.();

    expect(Notice.instances).toContain("请先填写 Codex 可执行文件的绝对路径。");
    expect(verifyAgent).not.toHaveBeenCalled();
    expect(saveCodexPath).not.toHaveBeenCalled();
  });

  it("explains the missing SessionReviewer CLI before probing Codex", async () => {
    Notice.instances.length = 0;
    const saveCodexPath = vi.fn();
    const verifyAgent = vi.fn();
    const tab = new CliSettingsTab({} as never, {} as never, "", vi.fn(), "/bin/codex", saveCodexPath, () => ({
      verifyExecutable: vi.fn(),
      verifyAgent
    }));
    tab.display();

    const codexSetting = Setting.instances.at(-1);
    await codexSetting?.buttonControls[0]?.onClickHandler?.();

    expect(Notice.instances).toContain("尚未配置 SessionReviewer CLI，请先在上方验证并保存。");
    expect(verifyAgent).not.toHaveBeenCalled();
    expect(saveCodexPath).not.toHaveBeenCalled();
  });

  it("explains the missing CLI path instead of surfacing a runner failure", async () => {
    Notice.instances.length = 0;
    const saveCliPath = vi.fn();
    const verifyExecutable = vi.fn();
    const tab = new CliSettingsTab({} as never, {} as never, "", saveCliPath, "/bin/codex", vi.fn(), () => ({
      verifyExecutable,
      verifyAgent: vi.fn()
    }));
    tab.display();

    const cliSetting = Setting.instances[1];
    await cliSetting?.buttonControls[0]?.onClickHandler?.();

    expect(Notice.instances).toContain("请先填写 SessionReviewer CLI 可执行文件的绝对路径。");
    expect(verifyExecutable).not.toHaveBeenCalled();
    expect(saveCliPath).not.toHaveBeenCalled();
  });
});
