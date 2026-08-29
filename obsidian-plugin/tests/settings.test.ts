import { describe, expect, it, vi } from "vitest";
import { CliSettingsTab } from "../src/cli/settings";

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
});
