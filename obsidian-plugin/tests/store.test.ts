import { describe, expect, it, vi } from "vitest";
import { BrowserStore } from "../src/state/store";

describe("browser store", () => {
  it("publishes immutable snapshots and supports unsubscribe", () => {
    const store = new BrowserStore();
    const listener = vi.fn();
    const unsubscribe = store.subscribe(listener);
    store.set({ kind: "empty" });
    expect(listener).toHaveBeenCalledOnce();
    expect(Object.isFrozen(store.get())).toBe(true);
    unsubscribe();
    store.set({ kind: "empty", diagnostic: { code: "stale_snapshot", message: "stale" } });
    expect(listener).toHaveBeenCalledOnce();
  });
});
