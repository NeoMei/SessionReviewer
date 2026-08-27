import { describe, expect, it } from "vitest";
import { renderReadyView } from "../src/view/render-shell";
import { browserModelFixture } from "./fixtures/browser";

describe("keyboard navigation", () => {
  it("navigates tabs and event nodes with roving focus", () => {
    const root = renderReadyView(browserModelFixture(), { fullHistory: true });
    document.body.append(root);
    const first = root.querySelector<HTMLElement>('[data-event-id="timeline-release"]')!;
    first.focus();
    first.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    expect(document.activeElement?.getAttribute("data-event-id")).toBe("timeline-trust-chain");
    document.activeElement?.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(root.querySelector('[data-role="detail-title"]')?.textContent).toBe("信任链与 dry-run 边界修复");

    const evolution = root.querySelector<HTMLElement>('[data-view="evolution"]')!;
    evolution.focus();
    evolution.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    expect(root.querySelector('[data-view="decisions"]')?.getAttribute("aria-selected")).toBe("true");
  });
});
