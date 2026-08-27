import type { Snapshot } from "../data/repository";

type Listener = (snapshot: Snapshot) => void;

export class BrowserStore {
  private snapshot: Snapshot = Object.freeze({ kind: "empty" });
  private readonly listeners = new Set<Listener>();

  get(): Snapshot {
    return this.snapshot;
  }

  set(snapshot: Snapshot): void {
    this.snapshot = Object.freeze(snapshot);
    for (const listener of this.listeners) listener(this.snapshot);
  }

  subscribe(listener: Listener): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }
}
