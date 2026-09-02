import { afterEach, describe, expect, it } from "vitest";
import { renderReadyView } from "../src/view/render-shell";
import { renderReviewJobBanner } from "../src/view/status-banner";
import { browserModelFixture } from "./fixtures/browser";

afterEach(() => {
  document.body.replaceChildren();
});

describe("keyboard and accessible landmarks", () => {
  it("keeps evolution tabs reachable via arrow keys and tab list semantics", () => {
    const root = renderReadyView(browserModelFixture());
    document.body.append(root);

    const evolution = root.querySelector<HTMLButtonElement>('button[data-view="evolution"]')!;
    expect(evolution.getAttribute("aria-selected")).toBe("true");
    expect(root.querySelector<HTMLButtonElement>('button[data-view="decisions"]')!.getAttribute("aria-selected")).toBe("false");

    evolution.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true }));
    expect(root.querySelector<HTMLButtonElement>('button[data-view="decisions"]')!.getAttribute("aria-selected")).toBe("true");
  });

  it("keeps the review action keyboard-reachable and announces the job banner", () => {
    const root = renderReadyView(browserModelFixture(), undefined, undefined, undefined, { label: "更新项目脉络", disabled: false, onStart: () => {} });
    document.body.append(root);
    const action = root.querySelector<HTMLButtonElement>(".sr-review-action")!;
    expect(action).toBeInstanceOf(HTMLButtonElement);
    action.focus();
    expect(document.activeElement).toBe(action);

    const banner = renderReviewJobBanner(
      {
        schema_version: 1,
        project_id: "project-x",
        state: "running",
        job_id: "job-1",
        phase: "extracting",
        session_count: 4,
        indexed_count: 1,
        issue_count: 0
      },
      {}
    )!;
    document.body.append(banner);
    expect(banner.getAttribute("role")).toBe("status");
  });
});