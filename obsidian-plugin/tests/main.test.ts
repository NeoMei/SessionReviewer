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
});
