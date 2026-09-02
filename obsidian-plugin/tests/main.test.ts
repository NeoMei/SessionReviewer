import { describe, expect, it, vi } from "vitest";
import SessionReviewerPlugin from "../src/main";

describe("plugin lifecycle", () => {
  it("auto-discovers the runtime, removes the settings tab, and clears legacy path fields", async () => {
    const plugin = new SessionReviewerPlugin({} as never, {} as never);
    const addSettingTab = vi.fn();
    const saveData = vi.fn().mockResolvedValue(undefined);
    const loadData = vi.fn().mockResolvedValue({ cliPath: "/bin/sr", codexPath: "/bin/codex", viewState: { fullHistory: true } });
    const runtimeResolver = vi.fn().mockResolvedValue({
      runner: { executable: "/bin/sr" }
    });

    Object.assign(plugin, {
      app: { workspace: { getLeaf: vi.fn(), revealLeaf: vi.fn() } },
      addSettingTab,
      saveData,
      loadData,
      registerView: vi.fn(),
      addRibbonIcon: vi.fn(),
      addCommand: vi.fn(),
      runtimeResolver
    });

    await plugin.onload();

    expect(runtimeResolver).toHaveBeenCalledWith({ legacyCliPath: "/bin/sr" });
    expect(addSettingTab).not.toHaveBeenCalled();
    expect(saveData).toHaveBeenLastCalledWith({ viewState: expect.anything() as never });
  });
});