import { describe, expect, it, vi } from "vitest";
import { CliSettingsTab } from "../src/cli/settings";

describe("CLI settings", () => {
  it("renders the title with Obsidian's setting heading component", () => {
    const tab = new CliSettingsTab({} as never, {} as never, "", vi.fn());

    tab.display();

    const heading = tab.containerEl.querySelector(".setting-item.mod-heading .setting-item-name");
    expect(heading?.textContent).toBe("CLI");
    expect(tab.containerEl.querySelector("h2")).toBeNull();
  });
});
