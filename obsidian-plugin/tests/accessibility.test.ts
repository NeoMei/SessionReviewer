import { describe, expect, it } from "vitest";
import { renderReadyView } from "../src/view/render-shell";
import { renderReviewJobBanner } from "../src/view/status-banner";
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

describe("review action accessibility", () => {
  it("keeps the review action keyboard-reachable and announces the job banner", () => {
    const root = renderReadyView(browserModelFixture(), undefined, undefined, undefined, { label: "总结并同步", disabled: false, onStart: () => {} });
    document.body.append(root);
    const action = root.querySelector<HTMLButtonElement>(".sr-review-action")!;
    expect(action).toBeInstanceOf(HTMLButtonElement);
    action.focus();
    expect(document.activeElement).toBe(action);

    const banner = renderReviewJobBanner(
      {
        schemaVersion: 1, projectId: "project-x", state: "running", jobId: "job-1", phase: "reviewing", attempt: 1,
        sessionIndex: 1, sessionCount: 4, acceptedPackets: 0, acceptedSessions: 0, canRetry: false, canCancel: true, canSyncOnly: false
      },
      {}
    )!;
    document.body.append(banner);
    expect(banner.getAttribute("role")).toBe("status");
  });
});
