import { describe, expect, it } from "vitest";
import { renderReadyView } from "../src/view/render-shell";
import { browserModelFixture } from "./fixtures/browser";

function click(root: ParentNode, selector: string): void {
  const element = root.querySelector<HTMLElement>(selector);
  if (!element) throw new Error(`missing ${selector}`);
  element.click();
}

function text(root: ParentNode, selector: string): string {
  return root.querySelector(selector)?.textContent?.trim() ?? "";
}

describe("project evolution browser", () => {
  it("switches adjacent node detail and returns from a decision to its event", () => {
    const view = renderReadyView(browserModelFixture());
    click(view, '[data-event-id="timeline-release"]');
    expect(text(view, '[data-role="detail-title"]')).toBe("v0.1.0 公开发布");
    expect(view.querySelector('[data-event-id="timeline-release"]')?.getAttribute("aria-selected")).toBe("true");

    click(view, '[data-view="decisions"]');
    click(view, '[data-action="open-related-event"][data-event-id="timeline-trust-chain"]');
    expect(view.querySelector('[data-view="evolution"]')?.getAttribute("aria-selected")).toBe("true");
    expect(text(view, '[data-role="detail-title"]')).toBe("信任链与 dry-run 边界修复");
  });

  it("renders concise resume information and full usage provenance", () => {
    const view = renderReadyView(browserModelFixture());
    expect(text(view, '[data-role="resume-stage"]')).toContain("v2 收尾");
    expect(text(view, '[data-role="resume-next"]')).toContain("完成 Obsidian 真实交互验收");
    click(view, '[data-view="usage"]');
    expect(text(view, '[data-role="usage-total"]')).toContain("350");
    expect(view.textContent).toContain("$4 / 百万 tokens");
    expect(view.textContent).toContain("2026-08-25");
  });
});
