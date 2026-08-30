import { describe, expect, it, vi } from "vitest";
import SessionReviewerPlugin, { VIEW_TYPE } from "../src/main";

describe("plugin lifecycle", () => {
  it("registers one desktop project-evolution view and open command", async () => {
    const registerView = vi.fn();
    const addCommand = vi.fn();
    const addRibbonIcon = vi.fn();
    const plugin = new SessionReviewerPlugin({ workspace: {} } as never, {} as never);
    Object.assign(plugin, {
      registerView,
      addCommand,
      addRibbonIcon,
      addSettingTab: vi.fn(),
      registerEvent: vi.fn(),
      runtimeResolver: vi.fn().mockResolvedValue(undefined)
    });
    await plugin.onload();
    expect(VIEW_TYPE).toBe("session-reviewer-project-evolution");
    expect(registerView).toHaveBeenCalledOnce();
    expect(addCommand).toHaveBeenCalledWith(expect.objectContaining({ id: "open-project-evolution" }));
    expect(addRibbonIcon).toHaveBeenCalledWith("history", "打开项目脉络", expect.any(Function));
  });

  it("auto-discovers the runtime, removes the settings tab, and clears legacy path fields", async () => {
    const registerView = vi.fn();
    const addCommand = vi.fn();
    const addRibbonIcon = vi.fn();
    const addSettingTab = vi.fn();
    const saveData = vi.fn();
    const runtime = { runner: {} as never, agentExecutable: "/bin/codex" };
    const runtimeResolver = vi.fn().mockResolvedValue(runtime);
    const plugin = new SessionReviewerPlugin({ workspace: {} } as never, {} as never);
    Object.assign(plugin, {
      registerView,
      addCommand,
      addRibbonIcon,
      addSettingTab,
      registerEvent: vi.fn(),
      loadData: vi.fn().mockResolvedValue({ cliPath: "/bin/sr", codexPath: "/bin/codex" }),
      saveData,
      runtimeResolver
    });
    await plugin.onload();

    expect(runtimeResolver).toHaveBeenCalledWith({ legacyCliPath: "/bin/sr", legacyAgentPath: "/bin/codex" });
    expect(addSettingTab).not.toHaveBeenCalled();
    expect(saveData).toHaveBeenLastCalledWith({ viewState: expect.anything() as unknown });
    const payload = saveData.mock.lastCall?.[0] as Record<string, unknown>;
    expect(Object.keys(payload)).toEqual(["viewState"]);
  });
});
