import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { renderReadyView } from "../src/view/render-shell";
import { browserModelFixture } from "./fixtures/browser";

const pluginStyles = readFileSync(resolve(process.cwd(), "styles.css"), "utf8");

afterEach(() => {
  document.body.replaceChildren();
  document.head.querySelectorAll("[data-test-style]").forEach((node) => node.remove());
});

function installStyles(): void {
  const obsidianStyles = document.createElement("style");
  obsidianStyles.dataset.testStyle = "obsidian";
  obsidianStyles.textContent = `
    button { align-items: center; display: inline-flex; height: 30px; justify-content: center; white-space: nowrap; }
    button:not(.clickable-icon) { background-color: rgb(238, 238, 238); box-shadow: 0 1px 2px rgb(0 0 0 / 20%); }
  `;
  const sessionReviewerStyles = document.createElement("style");
  sessionReviewerStyles.dataset.testStyle = "session-reviewer";
  sessionReviewerStyles.textContent = pluginStyles;
  document.head.append(obsidianStyles, sessionReviewerStyles);
}

describe("timeline node layout", () => {
  it("expands and wraps instead of inheriting Obsidian's fixed button layout", () => {
    installStyles();

    const browser = document.createElement("div");
    browser.className = "session-reviewer-browser";
    const node = document.createElement("button");
    node.className = "sr-timeline-node";
    node.append(document.createElement("span"), document.createElement("strong"));
    browser.append(node);
    document.body.append(browser);

    const layout = getComputedStyle(node);
    expect(layout.height).toBe("auto");
    expect(layout.whiteSpace).toBe("normal");
  });
});

describe("decision card layout", () => {
  it("keeps dense decision text readable and the related-event action lightweight", () => {
    installStyles();
    const view = renderReadyView(browserModelFixture());
    document.body.append(view);
    view.querySelector<HTMLButtonElement>('[data-view="decisions"]')?.click();

    const panel = view.querySelector<HTMLElement>('[aria-label="关键决策"]')!;
    const grid = panel.querySelector<HTMLElement>(".sr-card-grid")!;
    const definition = panel.querySelector<HTMLElement>(".sr-definition")!;
    const relatedEvent = panel.querySelector<HTMLButtonElement>('[data-action="open-related-event"]')!;

    expect(getComputedStyle(grid).gridTemplateColumns).toBe("repeat(auto-fit, minmax(min(100%, 460px), 1fr))");
    expect(getComputedStyle(definition).gridTemplateColumns).toBe("minmax(0, 1fr)");
    expect(getComputedStyle(relatedEvent).height).toBe("auto");
    expect(getComputedStyle(relatedEvent).backgroundColor).toBe("rgba(0, 0, 0, 0)");
    expect(getComputedStyle(relatedEvent).justifyContent).toBe("flex-start");
  });
});

describe("risk card layout", () => {
  it("uses per-item disclosure cards instead of a dense three-column wall", () => {
    installStyles();
    const view = renderReadyView(browserModelFixture());
    document.body.append(view);

    const risk = view.querySelector<HTMLElement>("details.sr-risk")!;
    const summary = view.querySelector<HTMLElement>(".sr-risk-summary")!;

    expect(getComputedStyle(risk).display).toBe("block");
    expect(getComputedStyle(risk).gridTemplateColumns).toBe("");
    expect(getComputedStyle(summary).whiteSpace).toBe("normal");
  });
});

describe("usage model card layout", () => {
  it("uses one full-width card with compact metric and pricing groups", () => {
    installStyles();
    const view = renderReadyView(browserModelFixture());
    document.body.append(view);
    view.querySelector<HTMLButtonElement>('[data-view="usage"]')?.click();

    const grid = view.querySelector<HTMLElement>(".sr-model-grid");
    const card = view.querySelector<HTMLElement>(".sr-model-card");
    const metrics = view.querySelector<HTMLElement>(".sr-model-metrics");
    const pricing = view.querySelector<HTMLElement>(".sr-model-pricing");
    expect(grid).toBeTruthy();
    expect(card).toBeTruthy();
    expect(metrics).toBeTruthy();
    expect(pricing).toBeTruthy();
    if (!grid || !card || !metrics || !pricing) return;

    expect(getComputedStyle(grid).gridTemplateColumns).toBe("minmax(0, 1fr)");
    expect(getComputedStyle(metrics).gridTemplateColumns).toBe("repeat(4, minmax(0, 1fr))");
    expect(getComputedStyle(pricing).gridTemplateColumns).toBe("repeat(4, minmax(0, 1fr))");
    expect(getComputedStyle(card.querySelector<HTMLElement>(".sr-definition")!).display).toBe("flex");
  });
});
