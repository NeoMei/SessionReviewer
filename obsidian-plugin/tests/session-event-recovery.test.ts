import { WorkspaceLeaf } from "obsidian";
import { describe, expect, it, vi } from "vitest";
import { SessionInspectError, type CliRunner } from "../src/cli/runner";
import type { SessionEventPageV1 } from "../src/contracts/review-v4";
import type { Snapshot } from "../src/data/repository";
import { ProjectEvolutionView } from "../src/view/project-view";

const DIGEST_1 = `sha256:${"1".repeat(64)}`;
const DIGEST_2 = `sha256:${"2".repeat(64)}`;
const PROJECT_DIGEST = `sha256:${"3".repeat(64)}`;
const project = { projectId: "project-p", root: "Projects/P", name: "P", format: "markdown-v4" as const };

function snapshot(generation: string, total: number, digest: string): Extract<Snapshot, { kind: "markdown-v4" }> {
  const session = { provider: "codex", session_id: "session-long", processing_state: "complete" as const, state_reason_codes: [], source_availability: "available" as const, source_terminal_state: null, started_at: "2026-09-08T00:00:00Z", ended_at: null, duration_ms: null, warning_count: 0, record_count: total, indexed_event_count: total, coverage: { seen: total, indexed: total, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 }, fact_counts: { file_change: 0, command: total, verification: 0, error: 0, artifact: 0 }, session_view_digest: digest, usage_record_digest: null, summary_digest: null, last_seen_generation_id: generation, last_successful_generation_id: generation };
  const index = { schema_version: 1 as const, minimum_reader_version: "0.4.0" as const, digest: `sha256:${"0".repeat(64)}`, project_id: project.projectId, generation_id: generation, project_view_digest: PROJECT_DIGEST, generated_at: "2026-09-08T00:00:00Z", sort_version: "started-at-desc-null-last-provider-session-v1" as const, coverage: { total: 1, complete: 1, partial: 0, error: 0, unprocessed: 0, source_available: 1, source_unavailable: 0, started_at_known: 1, ended_at_known: 0, usage_known: 0 }, sessions: [session] };
  return { kind: "markdown-v4", descriptor: project, state: { kind: "public_valid", index, value: { presentation: { schema_version: 4, minimum_reader_version: "0.4.0", minimum_writer_version: "0.4.0", project_id: project.projectId, generation_id: generation, project_view_digest: PROJECT_DIGEST, revision: 1, current_state: { goal: "g", stage: "s", status: "x", next_action: "n", last_verification: "" }, timeline: [], decisions: [], risks: [], open_loops: [], problem_map_revision: 0, problem_root_ids: [], problem_nodes: [], chain_dependencies: [], human_patches: [], orphan_patches: [], generated_baselines: [] }, changedFields: [], changedDocuments: [], fields: [] }, ledger: { schema_version: 4, minimum_reader_version: "0.4.0", minimum_writer_version: "0.4.0", project_id: project.projectId, generation_id: generation, project_view_digest: PROJECT_DIGEST, accepted_revision: 1, review_sha256: "0".repeat(64), history_sha256: "0".repeat(64), accounting: { total_duration_ms: 0, total_tokens: 0, total_cost_usd: null, models: [] }, sessions: [], human_patches: [], orphan_patches: [], generated_baselines: [], pricing_snapshots: [], current_pricing_snapshot_ids: [], sync_hashes: { review_sha256: "0".repeat(64), history_sha256: "0".repeat(64), ledger_sha256: "0".repeat(64), session_index_digest: `sha256:${"0".repeat(64)}` } } }, loadedAt: 1 };
}

function page(request: { expectedGenerationId: string; expectedSessionViewDigest: string; anchor?: number }, total: number): SessionEventPageV1 {
  const anchor = request.anchor ?? 1;
  const start = Math.min(Math.floor((anchor - 1) / 25) * 25, Math.max(0, total - 25));
  const end = Math.min(start + 25, total);
  return { schema_version: 1, minimum_reader_version: "0.4.0", project_id: project.projectId, provider: "codex", session_id: "session-long", generation_id: request.expectedGenerationId, session_view_digest: request.expectedSessionViewDigest, total, range_start: start, range_end: end, items: Array.from({ length: end - start }, (_v, i) => ({ kind: "command", excerpt: `event-${start + i + 1}`, revision_id: `revision-${start + i + 1}`, sequence: start + i + 1, occurred_at: "2026-09-08T00:00:00Z" })), previous_cursor: start === 0 ? null : "previous", next_cursor: end === total ? null : "next", first_cursor: "first", last_cursor: "last", coverage: { seen: total, indexed: total, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 } };
}

async function settle(): Promise<void> { await new Promise((resolve) => setTimeout(resolve, 0)); }
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done; }); return { promise, resolve }; }

describe("actual ProjectEvolutionView event recovery", () => {
  it("refreshes once after stale navigation and reanchors the same namespaced Session with clamping", async () => {
    const first = snapshot("generation-1", 2_438, DIGEST_1);
    const second = snapshot("generation-2", 1_000, DIGEST_2);
    const repository = { discover: vi.fn().mockResolvedValue([project]), load: vi.fn().mockResolvedValueOnce(first).mockResolvedValueOnce(second), watch: vi.fn().mockReturnValue(vi.fn()) };
    const getSessionEvents = vi.fn((request: { expectedGenerationId: string; expectedSessionViewDigest: string; anchor?: number }) => {
      if (request.expectedGenerationId === "generation-1" && request.anchor === 1_220) return Promise.reject(new SessionInspectError("stale_cursor"));
      return Promise.resolve(page(request, request.expectedGenerationId === "generation-1" ? 2_438 : 1_000));
    });
    const runner = { status: vi.fn().mockResolvedValue({}), getSessionEvents };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, runner as unknown as CliRunner, undefined, undefined, { "project-p": { projectId: "project-p", view: "sessions", selectedMilestoneId: null, selectedProblemId: null, sessionBrowser: { query: "session-long", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: { provider: "codex", sessionId: "session-long" } } } });
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });
    await view.onOpen(); await settle();
    const input = view.contentEl.querySelector<HTMLInputElement>('[aria-label="跳转到事件序号"]')!;
    input.value = "1220";
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle(); await settle(); await settle();
    expect(repository.load).toHaveBeenCalledTimes(2);
    expect(getSessionEvents).toHaveBeenLastCalledWith(expect.objectContaining({ provider: "codex", sessionId: "session-long", expectedGenerationId: "generation-2", expectedSessionViewDigest: DIGEST_2, anchor: 1_000 }));
    expect(view.contentEl.querySelector('[data-event-ordinal="1000"]')?.getAttribute("aria-selected")).toBe("true");
    expect(view.contentEl.textContent).toContain("项目索引已更新");
    expect(view.contentEl.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')?.value).toBe("session-long");
    await view.onClose();
  });

  it("does not loop after a second stale response", async () => {
    const repository = { discover: vi.fn().mockResolvedValue([project]), load: vi.fn().mockResolvedValueOnce(snapshot("generation-1", 100, DIGEST_1)).mockResolvedValueOnce(snapshot("generation-2", 100, DIGEST_2)), watch: vi.fn().mockReturnValue(vi.fn()) };
    const getSessionEvents = vi.fn((request: { expectedGenerationId: string; expectedSessionViewDigest: string; anchor?: number }) => request.anchor === undefined ? Promise.resolve(page(request, 100)) : Promise.reject(new SessionInspectError("stale_cursor")));
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, { status: vi.fn().mockResolvedValue({}), getSessionEvents } as unknown as CliRunner, undefined, undefined, { "project-p": { projectId: "project-p", view: "sessions", selectedMilestoneId: null, selectedProblemId: null, sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: { provider: "codex", sessionId: "session-long" } } } });
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });
    await view.onOpen(); await settle();
    const input = view.contentEl.querySelector<HTMLInputElement>('[aria-label="跳转到事件序号"]')!;
    input.value = "50"; input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle(); await settle(); await settle();
    expect(repository.load).toHaveBeenCalledTimes(2);
    expect(view.contentEl.querySelector('[data-event-page-error]')?.textContent).toContain("无法读取扫描 Session");
    expect(view.contentEl.querySelector('[data-action="retry-event-page"]')).not.toBeNull();
    await view.onClose();
  });

  it("recovers the ordinary default Session even when its implicit selection was not persisted", async () => {
    const repository = { discover: vi.fn().mockResolvedValue([project]), load: vi.fn().mockResolvedValueOnce(snapshot("generation-1", 100, DIGEST_1)).mockResolvedValueOnce(snapshot("generation-2", 100, DIGEST_2)), watch: vi.fn().mockReturnValue(vi.fn()) };
    const getSessionEvents = vi.fn((request: { expectedGenerationId: string; expectedSessionViewDigest: string; anchor?: number }) => request.expectedGenerationId === "generation-1" && request.anchor === 50 ? Promise.reject(new SessionInspectError("generation_mismatch")) : Promise.resolve(page(request, 100)));
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, { status: vi.fn().mockResolvedValue({}), getSessionEvents } as unknown as CliRunner);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });
    await view.onOpen();
    view.contentEl.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click(); await settle();
    const input = view.contentEl.querySelector<HTMLInputElement>('[aria-label="跳转到事件序号"]')!;
    input.value = "50"; input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle(); await settle(); await settle();
    expect(repository.load).toHaveBeenCalledTimes(2);
    expect(getSessionEvents).toHaveBeenLastCalledWith(expect.objectContaining({ expectedGenerationId: "generation-2", anchor: 50 }));
    await view.onClose();
  });

  it("keeps the implicit original Session when refresh inserts a new first row", async () => {
    const first = snapshot("generation-1", 100, DIGEST_1);
    const second = snapshot("generation-2", 100, DIGEST_2);
    if (second.state.kind !== "public_valid") throw new Error("expected public index");
    second.state.index.sessions.unshift({ ...second.state.index.sessions[0], session_id: "session-new" });
    second.state.index.coverage.total = 2;
    second.state.index.coverage.complete = 2;
    second.state.index.coverage.source_available = 2;
    second.state.index.coverage.started_at_known = 2;
    const repository = { discover: vi.fn().mockResolvedValue([project]), load: vi.fn().mockResolvedValueOnce(first).mockResolvedValueOnce(second), watch: vi.fn().mockReturnValue(vi.fn()) };
    const getSessionEvents = vi.fn((request: { sessionId: string; expectedGenerationId: string; expectedSessionViewDigest: string; anchor?: number }) => {
      if (request.expectedGenerationId === "generation-1" && request.anchor === 50) return Promise.reject(new SessionInspectError("generation_mismatch"));
      return Promise.resolve({ ...page(request, 100), session_id: request.sessionId });
    });
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, { status: vi.fn().mockResolvedValue({}), getSessionEvents } as unknown as CliRunner);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });
    await view.onOpen();
    view.contentEl.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click(); await settle();
    const input = view.contentEl.querySelector<HTMLInputElement>('[aria-label="跳转到事件序号"]')!;
    input.value = "50"; input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle(); await settle(); await settle();
    expect(view.contentEl.querySelector('[aria-label="Session 覆盖"] h3')?.textContent).toBe("codex / session-long");
    expect(getSessionEvents).toHaveBeenLastCalledWith(expect.objectContaining({ sessionId: "session-long", expectedGenerationId: "generation-2", anchor: 50 }));
    await view.onClose();
  });

  it("recovers the same implicit Session again after a new first row was inserted", async () => {
    const withNewFirst = (generation: string, digest: string) => {
      const result = snapshot(generation, 100, digest);
      if (result.state.kind !== "public_valid") throw new Error("expected public index");
      result.state.index.sessions.unshift({ ...result.state.index.sessions[0], session_id: "session-new" });
      Object.assign(result.state.index.coverage, { total: 2, complete: 2, source_available: 2, started_at_known: 2 });
      return result;
    };
    const repository = {
      discover: vi.fn().mockResolvedValue([project]),
      load: vi.fn()
        .mockResolvedValueOnce(snapshot("generation-1", 100, DIGEST_1))
        .mockResolvedValueOnce(withNewFirst("generation-2", DIGEST_2))
        .mockResolvedValueOnce(withNewFirst("generation-3", `sha256:${"4".repeat(64)}`)),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    let stale = false;
    const getSessionEvents = vi.fn((request: { sessionId: string; expectedGenerationId: string; expectedSessionViewDigest: string; anchor?: number; cursor?: string }) => {
      if (stale && request.cursor) {
        stale = false;
        return Promise.reject(new SessionInspectError("generation_mismatch"));
      }
      const anchored = request.cursor ? { ...request, anchor: 26 } : request;
      return Promise.resolve({ ...page(anchored, 100), session_id: request.sessionId });
    });
    const saveV4State = vi.fn();
    const initial = { "project-p": { projectId: "project-p", view: "sessions" as const, selectedMilestoneId: null, selectedProblemId: null, sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: null } } };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, { status: vi.fn().mockResolvedValue({}), getSessionEvents } as unknown as CliRunner, undefined, undefined, initial, saveV4State);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });
    await view.onOpen(); await settle();
    view.contentEl.querySelector<HTMLButtonElement>('[data-action="next-event-page"]')!.click(); await settle();
    stale = true;
    view.contentEl.querySelector<HTMLButtonElement>('[data-action="next-event-page"]')!.click();
    await settle(); await settle(); await settle();
    expect(view.contentEl.querySelector('[aria-label="Session 覆盖"] h3')?.textContent).toBe("codex / session-long");
    stale = true;
    view.contentEl.querySelector<HTMLButtonElement>('[data-action="next-event-page"]')!.click();
    await settle(); await settle(); await settle();
    expect(repository.load).toHaveBeenCalledTimes(3);
    expect(getSessionEvents).toHaveBeenLastCalledWith(expect.objectContaining({ sessionId: "session-long", expectedGenerationId: "generation-3", anchor: 26 }));
    expect(view.contentEl.querySelector('[data-event-ordinal="26"]')?.getAttribute("aria-selected")).toBe("true");
    expect(saveV4State).not.toHaveBeenCalled();
    await view.onClose();
  });

  it("keeps host and renderer on the recovered implicit Session across an ordinary refresh", async () => {
    const withNewFirst = () => {
      const result = snapshot("generation-2", 100, DIGEST_2);
      if (result.state.kind !== "public_valid") throw new Error("expected public index");
      result.state.index.sessions.unshift({ ...result.state.index.sessions[0], session_id: "session-new" });
      Object.assign(result.state.index.coverage, { total: 2, complete: 2, source_available: 2, started_at_known: 2 });
      return result;
    };
    const repository = {
      discover: vi.fn().mockResolvedValue([project]),
      load: vi.fn().mockResolvedValueOnce(snapshot("generation-1", 100, DIGEST_1)).mockResolvedValueOnce(withNewFirst()).mockResolvedValueOnce(withNewFirst()),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const getSessionEvents = vi.fn((request: { sessionId: string; expectedGenerationId: string; expectedSessionViewDigest: string; anchor?: number }) => {
      if (request.expectedGenerationId === "generation-1" && request.anchor === 50) return Promise.reject(new SessionInspectError("generation_mismatch"));
      return Promise.resolve({ ...page(request, 100), session_id: request.sessionId });
    });
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, { status: vi.fn().mockResolvedValue({}), getSessionEvents } as unknown as CliRunner);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });
    await view.onOpen();
    view.contentEl.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click(); await settle();
    const input = view.contentEl.querySelector<HTMLInputElement>('[aria-label="跳转到事件序号"]')!;
    input.value = "50"; input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle(); await settle(); await settle();
    expect(view.contentEl.querySelector('[aria-label="Session 覆盖"] h3')?.textContent).toBe("codex / session-long");
    view.contentEl.querySelector<HTMLButtonElement>('[data-action="refresh-v4-status"]')!.click();
    await settle(); await settle();
    expect(view.contentEl.querySelector('[aria-label="Session 覆盖"] h3')?.textContent).toBe("codex / session-long");
    expect(getSessionEvents).toHaveBeenLastCalledWith(expect.objectContaining({ sessionId: "session-long", expectedGenerationId: "generation-2" }));
    await view.onClose();
  });

  it("lets a newer explicit shared selection override recovered implicit state on refresh", async () => {
    const withNewFirst = () => {
      const result = snapshot("generation-2", 100, DIGEST_2);
      if (result.state.kind !== "public_valid") throw new Error("expected public index");
      result.state.index.sessions.unshift({ ...result.state.index.sessions[0], session_id: "session-new" });
      Object.assign(result.state.index.coverage, { total: 2, complete: 2, source_available: 2, started_at_known: 2 });
      return result;
    };
    const repository = {
      discover: vi.fn().mockResolvedValue([project]),
      load: vi.fn().mockResolvedValueOnce(snapshot("generation-1", 100, DIGEST_1)).mockResolvedValueOnce(withNewFirst()).mockResolvedValueOnce(withNewFirst()),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const getSessionEvents = vi.fn((request: { sessionId: string; expectedGenerationId: string; expectedSessionViewDigest: string; anchor?: number }) => {
      if (request.expectedGenerationId === "generation-1" && request.anchor === 50) return Promise.reject(new SessionInspectError("generation_mismatch"));
      return Promise.resolve({ ...page(request, 100), session_id: request.sessionId });
    });
    let shared = { projectId: "project-p", view: "sessions" as const, selectedMilestoneId: null, selectedProblemId: null, sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: null as null | { provider: string; sessionId: string } } };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, { status: vi.fn().mockResolvedValue({}), getSessionEvents } as unknown as CliRunner, undefined, undefined, { "project-p": shared }, undefined, () => shared);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });
    await view.onOpen(); await settle();
    const input = view.contentEl.querySelector<HTMLInputElement>('[aria-label="跳转到事件序号"]')!;
    input.value = "50"; input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle(); await settle(); await settle();
    expect(view.contentEl.querySelector('[aria-label="Session 覆盖"] h3')?.textContent).toBe("codex / session-long");
    shared = { ...shared, sessionBrowser: { ...shared.sessionBrowser, selected: { provider: "codex", sessionId: "session-new" } } };
    view.contentEl.querySelector<HTMLButtonElement>('[data-action="refresh-v4-status"]')!.click();
    await settle(); await settle();
    expect(view.contentEl.querySelector('[aria-label="Session 覆盖"] h3')?.textContent).toBe("codex / session-new");
    expect(getSessionEvents).toHaveBeenLastCalledWith(expect.objectContaining({ sessionId: "session-new", expectedGenerationId: "generation-2" }));
    await view.onClose();
  });

  it("does not let a deferred recovery override a newer explicit shared selection", async () => {
    const pending = deferred<Extract<Snapshot, { kind: "markdown-v4" }>>();
    const second = snapshot("generation-2", 100, DIGEST_2);
    if (second.state.kind !== "public_valid") throw new Error("expected public index");
    second.state.index.sessions.unshift({ ...second.state.index.sessions[0], session_id: "session-new" });
    Object.assign(second.state.index.coverage, { total: 2, complete: 2, source_available: 2, started_at_known: 2 });
    const repository = { discover: vi.fn().mockResolvedValue([project]), load: vi.fn().mockResolvedValueOnce(snapshot("generation-1", 100, DIGEST_1)).mockReturnValueOnce(pending.promise), watch: vi.fn().mockReturnValue(vi.fn()) };
    const getSessionEvents = vi.fn((request: { sessionId: string; expectedGenerationId: string; expectedSessionViewDigest: string; anchor?: number }) => {
      if (request.expectedGenerationId === "generation-1" && request.anchor === 50) return Promise.reject(new SessionInspectError("generation_mismatch"));
      return Promise.resolve({ ...page(request, 100), session_id: request.sessionId });
    });
    let shared = { projectId: "project-p", view: "sessions" as const, selectedMilestoneId: null, selectedProblemId: null, sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: null as null | { provider: string; sessionId: string } } };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, { status: vi.fn().mockResolvedValue({}), getSessionEvents } as unknown as CliRunner, undefined, undefined, { "project-p": shared }, undefined, () => shared);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });
    await view.onOpen(); await settle();
    const input = view.contentEl.querySelector<HTMLInputElement>('[aria-label="跳转到事件序号"]')!;
    input.value = "50"; input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle();
    shared = { ...shared, sessionBrowser: { ...shared.sessionBrowser, selected: { provider: "codex", sessionId: "session-new" } } };
    pending.resolve(second);
    await settle(); await settle(); await settle();
    expect(view.contentEl.querySelector('[aria-label="Session 覆盖"] h3')?.textContent).toBe("codex / session-new");
    expect(getSessionEvents).toHaveBeenLastCalledWith(expect.objectContaining({ sessionId: "session-new", expectedGenerationId: "generation-2" }));
    expect(getSessionEvents.mock.calls.some(([request]) => request.sessionId === "session-long" && request.expectedGenerationId === "generation-2" && request.anchor === 50)).toBe(false);
    expect(view.contentEl.querySelector('.sr-event-recovery-status')).toBeNull();
    await view.onClose();
  });

  it("lets newer cached event navigation cancel an older pending recovery", async () => {
    const refresh = deferred<Extract<Snapshot, { kind: "markdown-v4" }>>();
    const repository = { discover: vi.fn().mockResolvedValue([project]), load: vi.fn().mockResolvedValueOnce(snapshot("generation-1", 2_438, DIGEST_1)).mockReturnValueOnce(refresh.promise), watch: vi.fn().mockReturnValue(vi.fn()) };
    const getSessionEvents = vi.fn((request: { expectedGenerationId: string; expectedSessionViewDigest: string; anchor?: number; cursor?: string }) => {
      if (request.cursor === "next") return Promise.reject(new SessionInspectError("stale_cursor"));
      return Promise.resolve(page(request, 2_438));
    });
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, { status: vi.fn().mockResolvedValue({}), getSessionEvents } as unknown as CliRunner, undefined, undefined, { "project-p": { projectId: "project-p", view: "sessions", selectedMilestoneId: null, selectedProblemId: null, sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: { provider: "codex", sessionId: "session-long" } } } });
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });
    await view.onOpen(); await settle();
    const jump = async (ordinal: string) => {
      const input = view.contentEl.querySelector<HTMLInputElement>('[aria-label="跳转到事件序号"]')!;
      input.value = ordinal; input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
      await settle();
    };
    await jump("1220");
    await jump("26");
    view.contentEl.querySelector<HTMLButtonElement>('[data-action="next-event-page"]')!.click();
    await settle();
    await jump("1220");
    expect(view.contentEl.querySelector('[data-event-ordinal="1220"]')?.getAttribute("aria-selected")).toBe("true");
    refresh.resolve(snapshot("generation-2", 2_438, DIGEST_2));
    await settle(); await settle();
    expect(view.contentEl.querySelector('[data-event-ordinal="1220"]')?.getAttribute("aria-selected")).toBe("true");
    expect(getSessionEvents.mock.calls.some(([request]) => request.expectedGenerationId === "generation-2")).toBe(false);
    expect(view.contentEl.querySelector('.sr-event-recovery-status')).toBeNull();
    await view.onClose();
  });

  it("shows an explicit unavailable state instead of another detail when refresh removes the Session", async () => {
    const removed = snapshot("generation-2", 0, DIGEST_2);
    if (removed.state.kind !== "public_valid") throw new Error("expected public index");
    removed.state.index.sessions = [];
    removed.state.index.coverage = { total: 0, complete: 0, partial: 0, error: 0, unprocessed: 0, source_available: 0, source_unavailable: 0, started_at_known: 0, ended_at_known: 0, usage_known: 0 };
    const repository = { discover: vi.fn().mockResolvedValue([project]), load: vi.fn().mockResolvedValueOnce(snapshot("generation-1", 100, DIGEST_1)).mockResolvedValueOnce(removed), watch: vi.fn().mockReturnValue(vi.fn()) };
    const getSessionEvents = vi.fn((request: { expectedGenerationId: string; expectedSessionViewDigest: string; anchor?: number }) => request.anchor === undefined ? Promise.resolve(page(request, 100)) : Promise.reject(new SessionInspectError("stale_cursor")));
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, { status: vi.fn().mockResolvedValue({}), getSessionEvents } as unknown as CliRunner, undefined, undefined, { "project-p": { projectId: "project-p", view: "sessions", selectedMilestoneId: null, selectedProblemId: null, sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: { provider: "codex", sessionId: "session-long" } } } });
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });
    await view.onOpen(); await settle();
    const input = view.contentEl.querySelector<HTMLInputElement>('[aria-label="跳转到事件序号"]')!;
    input.value = "50"; input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle(); await settle();
    expect(view.contentEl.textContent).toContain("原 Session 已不在已验证索引中");
    expect(view.contentEl.querySelector('[aria-label="Session 覆盖"]')).toBeNull();
    expect(getSessionEvents.mock.calls.some(([request]) => request.expectedGenerationId === "generation-2")).toBe(false);
    await view.onClose();
  });

  it("invalidates pending recovery when a local filter changes and leaves no loading loop", async () => {
    const pending = deferred<Extract<Snapshot, { kind: "markdown-v4" }>>();
    const repository = { discover: vi.fn().mockResolvedValue([project]), load: vi.fn().mockResolvedValueOnce(snapshot("generation-1", 100, DIGEST_1)).mockReturnValueOnce(pending.promise), watch: vi.fn().mockReturnValue(vi.fn()) };
    const getSessionEvents = vi.fn((request: { expectedGenerationId: string; expectedSessionViewDigest: string; anchor?: number }) => request.anchor === undefined ? Promise.resolve(page(request, 100)) : Promise.reject(new SessionInspectError("stale_cursor")));
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, { status: vi.fn().mockResolvedValue({}), getSessionEvents } as unknown as CliRunner, undefined, undefined, { "project-p": { projectId: "project-p", view: "sessions", selectedMilestoneId: null, selectedProblemId: null, sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: { provider: "codex", sessionId: "session-long" } } } });
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });
    await view.onOpen(); await settle();
    const jump = view.contentEl.querySelector<HTMLInputElement>('[aria-label="跳转到事件序号"]')!;
    jump.value = "50"; jump.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })); await settle();
    const search = view.contentEl.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')!;
    search.value = "no-match"; search.dispatchEvent(new Event("input", { bubbles: true }));
    pending.resolve(snapshot("generation-2", 100, DIGEST_2)); await settle(); await settle();
    expect(view.contentEl.querySelector('.sr-loading')).toBeNull();
    expect(view.contentEl.querySelector('[aria-label="Session 覆盖"]')).toBeNull();
    expect(view.contentEl.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')?.value).toBe("no-match");
    expect(getSessionEvents.mock.calls.some(([request]) => request.expectedGenerationId === "generation-2")).toBe(false);
    await view.onClose();
  });

  it("keeps the authenticated page readable when repository refresh rejects", async () => {
    const repository = { discover: vi.fn().mockResolvedValue([project]), load: vi.fn().mockResolvedValueOnce(snapshot("generation-1", 100, DIGEST_1)).mockRejectedValueOnce(new Error("private repository failure")), watch: vi.fn().mockReturnValue(vi.fn()) };
    const getSessionEvents = vi.fn((request: { expectedGenerationId: string; expectedSessionViewDigest: string; anchor?: number }) => request.anchor === undefined ? Promise.resolve(page(request, 100)) : Promise.reject(new SessionInspectError("stale_cursor")));
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, { status: vi.fn().mockResolvedValue({}), getSessionEvents } as unknown as CliRunner, undefined, undefined, { "project-p": { projectId: "project-p", view: "sessions", selectedMilestoneId: null, selectedProblemId: null, sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: { provider: "codex", sessionId: "session-long" } } } });
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });
    await view.onOpen(); await settle();
    const input = view.contentEl.querySelector<HTMLInputElement>('[aria-label="跳转到事件序号"]')!;
    input.value = "50"; input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle(); await settle();
    expect(view.contentEl.querySelector('[data-event-ordinal="1"]')).not.toBeNull();
    expect(view.contentEl.querySelector('[data-event-page-error]')?.textContent).toContain("当前已认证事件仍可阅读");
    expect(view.contentEl.textContent).not.toContain("private repository failure");
    expect(view.contentEl.querySelector('[data-action="retry-event-page"]')).not.toBeNull();
    await view.onClose();
  });
});
