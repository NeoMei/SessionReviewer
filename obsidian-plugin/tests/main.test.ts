import { describe, expect, it, vi } from "vitest";
import SessionReviewerPlugin, { VIEW_TYPE } from "../src/main";

describe("plugin lifecycle", () => {
  it("registers one desktop project-evolution view and open command", async () => {
    const registerView = vi.fn();
    const addCommand = vi.fn();
    const addRibbonIcon = vi.fn();
    const plugin = new SessionReviewerPlugin({ workspace: {} } as never, {} as never);
    Object.assign(plugin, { registerView, addCommand, addRibbonIcon, addSettingTab: vi.fn(), registerEvent: vi.fn() });
    await plugin.onload();
    expect(VIEW_TYPE).toBe("session-reviewer-project-evolution");
    expect(registerView).toHaveBeenCalledOnce();
    expect(addCommand).toHaveBeenCalledWith(expect.objectContaining({ id: "open-project-evolution" }));
    expect(addRibbonIcon).toHaveBeenCalledWith("history", "SessionReviewer：打开项目脉络", expect.any(Function));
  });

  it("persists the Codex path next to the CLI path without extra credentials", async () => {
    const registerView = vi.fn();
    const addCommand = vi.fn();
    const addRibbonIcon = vi.fn();
    const addSettingTab = vi.fn();
    const saveData = vi.fn();
    const plugin = new SessionReviewerPlugin({ workspace: {} } as never, {} as never);
    Object.assign(plugin, {
      registerView,
      addCommand,
      addRibbonIcon,
      addSettingTab,
      registerEvent: vi.fn(),
      loadData: vi.fn().mockResolvedValue({ cliPath: "/bin/sr", codexPath: "/bin/codex" }),
      saveData
    });
    await plugin.onload();

    const settingsTab = addSettingTab.mock.calls[0]?.[0] as { saveCodexPath?: (path: string) => Promise<void> };
    await settingsTab.saveCodexPath?.("/bin/codex-next");

    expect(saveData).toHaveBeenLastCalledWith({ viewState: expect.anything(), cliPath: "/bin/sr", codexPath: "/bin/codex-next" });
    const payload = saveData.mock.lastCall?.[0] as Record<string, unknown>;
    expect(Object.keys(payload).sort()).toEqual(["cliPath", "codexPath", "viewState"]);
  });
});
