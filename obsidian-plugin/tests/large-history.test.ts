import { describe, expect, it } from "vitest";
import { renderReadyView } from "../src/view/render-shell";
import { browserModelFixture } from "./fixtures/browser";

describe("large history", () => {
  it("bounds rendered options for twenty thousand events", () => {
    const model = browserModelFixture();
    model.events = Array.from({ length: 20_000 }, (_, index) => ({
      ...model.events[0],
      id: `event-${String(index).padStart(5, "0")}`,
      occurredAt: `2026-08-${String(27 - index % 27).padStart(2, "0")}`,
      title: `Event ${index}`
    }));
    const root = renderReadyView(model, { fullHistory: true });
    expect(root.querySelectorAll('[role="option"]').length).toBeLessThanOrEqual(80);
    expect(root.querySelector('[data-event-id="event-00000"]')).not.toBeNull();
  });
});
