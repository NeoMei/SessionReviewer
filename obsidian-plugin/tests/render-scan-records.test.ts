import { WorkspaceLeaf } from "obsidian";
import { describe, expect, it, vi } from "vitest";
import type { CliRunner } from "../src/cli/runner";
import type { SessionEventPageV1, SessionIndexEntryV1, SessionIndexV1 } from "../src/contracts/review-v4";
import type { Snapshot } from "../src/data/repository";
import { renderMarkdownV4View } from "../src/view/presentation";
import { ProjectEvolutionView } from "../src/view/project-view";
import { defaultViewState } from "../src/view/render-shell";

const VIEW_DIGEST = `sha256:${"1".repeat(64)}`;
const PROJECT_DIGEST = `sha256:${"2".repeat(64)}`;

function sessionFixture(overrides: Partial<SessionIndexEntryV1> = {}): SessionIndexEntryV1 {
  return {
    provider: "codex",
    session_id: "session-1",
    processing_state: "partial",
    state_reason_codes: ["unsupported_source_records"],
    source_availability: "available",
    source_terminal_state: null,
    started_at: "2026-09-06T16:00:00Z",
    ended_at: "2026-09-06T17:36:34.520Z",
    duration_ms: 5_794_520,
    warning_count: 1,
    record_count: 4_767,
    indexed_event_count: 2,
    coverage: { seen: 4_767, indexed: 2, collapsed: 0, unprojected: 0, undecodable: 4_765, truncated: 0 },
    fact_counts: { file_change: 0, command: 0, verification: 0, error: 0, artifact: 1 },
    session_view_digest: VIEW_DIGEST,
    usage_record_digest: null,
    summary_digest: null,
    last_seen_generation_id: "generation-1",
    last_successful_generation_id: "generation-1",
    ...overrides
  };
}

function indexFixture(sessions: SessionIndexEntryV1[] = [sessionFixture()]): SessionIndexV1 {
  const counts = {
    complete: sessions.filter((session) => session.processing_state === "complete").length,
    partial: sessions.filter((session) => session.processing_state === "partial").length,
    error: sessions.filter((session) => session.processing_state === "error").length,
    unprocessed: sessions.filter((session) => session.processing_state === "unprocessed").length
  };
  return {
    schema_version: 1,
    minimum_reader_version: "0.4.0",
    digest: `sha256:${"0".repeat(64)}`,
    project_id: "project-p",
    generation_id: "generation-1",
    project_view_digest: PROJECT_DIGEST,
    generated_at: "2026-09-07T00:00:00Z",
    sort_version: "started-at-desc-null-last-provider-session-v1",
    coverage: {
      total: sessions.length,
      ...counts,
      source_available: sessions.filter((session) => session.source_availability === "available").length,
      source_unavailable: sessions.filter((session) => session.source_availability === "unavailable").length,
      started_at_known: sessions.filter((session) => session.started_at !== null).length,
      ended_at_known: sessions.filter((session) => session.ended_at !== null).length,
      usage_known: sessions.filter((session) => session.usage_record_digest !== null).length
    },
    sessions
  };
}

function snapshot(index = indexFixture()): Extract<Snapshot, { kind: "markdown-v4" }> {
  return {
    kind: "markdown-v4",
    descriptor: { projectId: "project-p", root: "Projects/SessionReviewer", name: "SessionReviewer", format: "markdown-v4" },
    state: {
      kind: "public_valid",
      index,
      value: {
        presentation: {
          current_state: { goal: "goal", stage: "stage", status: "status", next_action: "next", last_verification: "" },
          timeline: []
        }
      } as never
    },
    loadedAt: 1
  };
}

function projectSnapshot(projectId: string, name: string, excerptDigest = VIEW_DIGEST): Extract<Snapshot, { kind: "markdown-v4" }> {
  const index = indexFixture([sessionFixture({ session_id: `session-${name.toLocaleLowerCase()}`, session_view_digest: excerptDigest })]);
  index.project_id = projectId;
  return {
    ...snapshot(index),
    descriptor: { projectId, root: `Projects/${name}`, name, format: "markdown-v4" }
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function eventPage(overrides: Partial<SessionEventPageV1> = {}): SessionEventPageV1 {
  return {
    schema_version: 1,
    minimum_reader_version: "0.4.0",
    project_id: "project-p",
    provider: "codex",
    session_id: "session-1",
    generation_id: "generation-1",
    session_view_digest: VIEW_DIGEST,
    total: 2,
    range_start: 0,
    range_end: 1,
    items: [{ kind: "message", excerpt: "用户问题示例\n  保留空白", revision_id: "revision-1", sequence: 10, occurred_at: "2026-09-06T16:00:00Z" }],
    previous_cursor: null,
    next_cursor: "next-token",
    first_cursor: "first-token",
    last_cursor: "last-token",
    coverage: { seen: 4_767, indexed: 2, collapsed: 0, unprojected: 0, undecodable: 4_765, truncated: 0 },
    ...overrides
  };
}

async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

describe("v4 scanned Session renderer", () => {
  it("renders partial coverage and pages through literal indexed excerpts", async () => {
    const loadSessionEvents = vi.fn()
      .mockResolvedValueOnce(eventPage())
      .mockResolvedValueOnce(eventPage({
        range_start: 1,
        range_end: 2,
        items: [{ kind: "tool_result", excerpt: "后续执行结果", revision_id: "revision-2", sequence: 20, occurred_at: "2026-09-06T17:36:34.520Z" }],
        previous_cursor: "previous-token",
        next_cursor: null
      }));
    const root = renderMarkdownV4View(snapshot(), () => {}, { loadSessionEvents });

    expect(root.querySelector('[aria-label="扫描 Session"]')).not.toBeNull();
    expect(root.textContent).toContain("部分");
    expect(root.textContent).toContain("未解码 4,765");
    await settle();
    expect(root.textContent).toContain("用户问题示例\n  保留空白");
    root.querySelector<HTMLButtonElement>('[data-action="next-event-page"]')?.click();
    await settle();

    expect(root.textContent).toContain("后续执行结果");
    expect(root.textContent).not.toContain("用户问题示例");
    expect(loadSessionEvents).toHaveBeenNthCalledWith(2, expect.objectContaining({ cursor: "next-token", limit: 25 }));
  });

  it("retries a failed next page with the same cursor instead of returning to page one", async () => {
    const secondPage = eventPage({
      range_start: 1,
      range_end: 2,
      items: [{ kind: "tool_result", excerpt: "重试后的第二页", revision_id: "revision-2", sequence: 20, occurred_at: "2026-09-06T17:36:34.520Z" }],
      previous_cursor: "previous-token",
      next_cursor: null
    });
    const loadSessionEvents = vi.fn()
      .mockResolvedValueOnce(eventPage())
      .mockRejectedValueOnce(new Error("第二页暂时不可用"))
      .mockResolvedValueOnce(secondPage);
    const root = renderMarkdownV4View(snapshot(), () => {}, { loadSessionEvents });
    await settle();

    root.querySelector<HTMLButtonElement>('[data-action="next-event-page"]')?.click();
    await settle();
    expect(root.textContent).toContain("第二页暂时不可用");
    root.querySelector<HTMLButtonElement>('[data-action="retry-event-page"]')?.click();
    await settle();

    expect(root.textContent).toContain("重试后的第二页");
    expect(root.textContent).not.toContain("用户问题示例");
    expect(loadSessionEvents).toHaveBeenNthCalledWith(2, expect.objectContaining({ cursor: "next-token" }));
    expect(loadSessionEvents).toHaveBeenNthCalledWith(3, expect.objectContaining({ cursor: "next-token" }));
  });

  it("pages and searches a bounded Session list", () => {
    const sessions = Array.from({ length: 26 }, (_value, index) => sessionFixture({
      session_id: `session-${String(index + 1).padStart(2, "0")}`,
      processing_state: "complete",
      state_reason_codes: [],
      indexed_event_count: 0,
      coverage: { seen: 0, indexed: 0, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 },
      session_view_digest: null
    }));
    const root = renderMarkdownV4View(snapshot(indexFixture(sessions)), () => {}, { cliUnavailable: true });

    expect(root.querySelectorAll("[data-session-id]")).toHaveLength(25);
    expect(root.textContent).not.toContain("session-26");
    root.querySelector<HTMLButtonElement>('[data-action="next-session-page"]')?.click();
    expect(root.textContent).toContain("session-26");
    const search = root.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')!;
    search.value = "session-03";
    search.dispatchEvent(new Event("input"));
    expect(root.querySelectorAll("[data-session-id]")).toHaveLength(1);
    expect(root.textContent).toContain("session-03");
  });

  it("states unavailable sources, empty excerpts and absent responses without inventing roles", async () => {
    const loadSessionEvents = vi.fn().mockResolvedValue(eventPage({
      items: [{ kind: "artifact", excerpt: "", revision_id: "revision-1", sequence: 10, occurred_at: "2026-09-06T16:00:00Z" }]
    }));
    const root = renderMarkdownV4View(snapshot(), () => {}, { loadSessionEvents });
    await settle();

    expect(root.textContent).toContain("该索引事件没有可用摘录");
    expect(root.textContent).toContain("索引摘录（非完整对话）");
    expect(root.textContent).not.toContain("用户说");
    expect(root.textContent).not.toContain("助手回复");

    const unavailable = sessionFixture({
      source_availability: "unavailable",
      processing_state: "error",
      state_reason_codes: ["source_unavailable"],
      indexed_event_count: 0,
      coverage: { seen: 0, indexed: 0, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 },
      session_view_digest: null
    });
    const unavailableRoot = renderMarkdownV4View(snapshot(indexFixture([unavailable])), () => {}, { cliUnavailable: true });
    expect(unavailableRoot.textContent).toContain("来源不可用");
    expect(unavailableRoot.textContent).toContain("无法读取扫描记录：CLI 不可用");
  });
});

describe("v4 project and event lifecycle", () => {
  it("persists a project switch immediately and ignores the old pending event response", async () => {
    const projectA = { projectId: "project-a", root: "Projects/A", name: "A", format: "markdown-v4" as const };
    const projectB = { projectId: "project-b", root: "Projects/B", name: "B", format: "markdown-v4" as const };
    const pendingA = deferred<SessionEventPageV1>();
    const repository = {
      discover: vi.fn().mockResolvedValue([projectA, projectB]),
      load: vi.fn(async (project: typeof projectA) => project.projectId === projectA.projectId ? projectSnapshot(projectA.projectId, "A") : projectSnapshot(projectB.projectId, "B")),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const runner = {
      status: vi.fn().mockResolvedValue({}),
      getSessionEvents: vi.fn((request: { projectId: string }) => request.projectId === projectA.projectId
        ? pendingA.promise
        : Promise.resolve(eventPage({ project_id: projectB.projectId, session_id: "session-b", items: [{ kind: "message", excerpt: "B 项目记录", revision_id: "revision-b", sequence: 1, occurred_at: "2026-09-07T00:00:00Z" }] })))
    };
    const saveState = vi.fn();
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, runner as unknown as CliRunner, defaultViewState(), saveState);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });

    await view.onOpen();
    const picker = view.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')!;
    picker.value = projectB.projectId;
    picker.dispatchEvent(new Event("change"));
    expect(saveState).toHaveBeenCalledWith(expect.objectContaining({ projectId: projectB.projectId }));
    await settle();
    expect(view.contentEl.textContent).toContain("B 项目记录");

    pendingA.resolve(eventPage({ project_id: projectA.projectId, session_id: "session-a", items: [{ kind: "message", excerpt: "A 过期记录", revision_id: "revision-a", sequence: 1, occurred_at: "2026-09-07T00:00:00Z" }] }));
    await settle();
    expect(view.contentEl.textContent).not.toContain("A 过期记录");
    await view.onClose();
  });

  it("restores a persisted project selection on reload", async () => {
    const projectA = { projectId: "project-a", root: "Projects/A", name: "A" };
    const projectB = { projectId: "project-b", root: "Projects/B", name: "B" };
    const repository = {
      discover: vi.fn().mockResolvedValue([projectA, projectB]),
      load: vi.fn(async (project: typeof projectA) => ({ kind: "migration_required", descriptor: project, diagnostic: { code: "migration_required", message: project.name } })),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, undefined, { ...defaultViewState(), projectId: projectB.projectId });

    await view.onOpen();

    expect(repository.load).toHaveBeenCalledWith(projectB, undefined);
    expect(view.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')?.value).toBe(projectB.projectId);
    expect(view.contentEl.textContent).toContain("B");
    await view.onClose();
  });

  it("rediscovers newly added projects and preserves the current project on explicit refresh", async () => {
    const projectA = { projectId: "project-a", root: "Projects/A", name: "A" };
    const projectB = { projectId: "project-b", root: "Projects/B", name: "B" };
    const repository = {
      discover: vi.fn().mockResolvedValueOnce([projectA]).mockResolvedValueOnce([projectA, projectB]),
      load: vi.fn(async (project: typeof projectA) => ({ kind: "migration_required", descriptor: project, diagnostic: { code: "migration_required", message: project.name } })),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never);

    await view.onOpen();
    expect(view.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')?.value).toBe(projectA.projectId);
    view.contentEl.querySelector<HTMLButtonElement>('[data-action="refresh-projects"]')?.click();
    await settle();

    expect(repository.discover).toHaveBeenCalledTimes(2);
    expect(view.contentEl.querySelectorAll('[aria-label="选择项目"] option')).toHaveLength(2);
    expect(view.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')?.value).toBe(projectA.projectId);
    await view.onClose();
  });

  it("keeps the selector available to escape a malformed selected project", async () => {
    const projectA = { projectId: "project-a", root: "Projects/A", name: "A", format: "markdown-v4" as const };
    const projectB = { projectId: "project-b", root: "Projects/B", name: "B" };
    const repository = {
      discover: vi.fn().mockResolvedValue([projectA, projectB]),
      load: vi.fn(async (project: typeof projectA) => project.projectId === projectA.projectId
        ? { kind: "markdown-v4", descriptor: projectA, state: { kind: "invalid", code: "wire_contract_invalid" }, loadedAt: 1 }
        : { kind: "migration_required", descriptor: projectB, diagnostic: { code: "migration_required", message: "B 可打开" } }),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never);

    await view.onOpen();
    expect(view.contentEl.textContent).toContain("wire_contract_invalid");
    const picker = view.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')!;
    picker.value = projectB.projectId;
    picker.dispatchEvent(new Event("change"));
    await settle();

    expect(view.contentEl.textContent).toContain("B 可打开");
    expect(view.contentEl.querySelector('[aria-label="选择项目"]')).not.toBeNull();
    await view.onClose();
  });

  it("keeps event pages cached for the same generation and resets them for a new generation", async () => {
    const project = { projectId: "project-p", root: "Projects/SessionReviewer", name: "SessionReviewer", format: "markdown-v4" as const };
    const first = projectSnapshot(project.projectId, "SessionReviewer");
    const second = structuredClone(first);
    if (second.state.kind !== "public_valid") throw new Error("expected public-valid snapshot");
    second.state.index.generation_id = "generation-2";
    second.state.index.sessions[0].last_seen_generation_id = "generation-2";
    second.state.index.sessions[0].last_successful_generation_id = "generation-2";
    const repository = {
      discover: vi.fn().mockResolvedValue([project]),
      load: vi.fn().mockResolvedValueOnce(first).mockResolvedValueOnce(first).mockResolvedValueOnce(second),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const runner = {
      status: vi.fn().mockResolvedValue({}),
      getSessionEvents: vi.fn().mockImplementation((request: { expectedGenerationId: string }) => Promise.resolve(eventPage({ generation_id: request.expectedGenerationId, session_id: "session-sessionreviewer" })))
    };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, runner as unknown as CliRunner);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });

    await view.onOpen();
    await settle();
    view.contentEl.querySelector<HTMLButtonElement>('[data-action="refresh-v4-status"]')?.click();
    await settle();
    expect(runner.getSessionEvents).toHaveBeenCalledTimes(1);

    view.contentEl.querySelector<HTMLButtonElement>('[data-action="refresh-v4-status"]')?.click();
    await settle();
    expect(runner.getSessionEvents).toHaveBeenCalledTimes(2);
    expect(runner.getSessionEvents).toHaveBeenLastCalledWith(expect.objectContaining({ expectedGenerationId: "generation-2" }));
    await view.onClose();
  });
});
