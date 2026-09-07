import { WorkspaceLeaf } from "obsidian";
import { describe, expect, it, vi } from "vitest";
import type { CliRunner } from "../src/cli/runner";
import type { Snapshot } from "../src/data/repository";
import { renderMarkdownV4View } from "../src/view/presentation";
import { ProjectEvolutionView } from "../src/view/project-view";
import { defaultViewState } from "../src/view/render-shell";
import type { V4ViewState } from "../src/state/v4-view-state";
import { v4SnapshotFixture } from "./fixtures/v4-shell";

const labels = ["项目演进", "问题脉络", "决策与约定", "全部 Sessions", "用量"];

function click(root: ParentNode, tab: V4ViewState["view"]): void {
  root.querySelector<HTMLButtonElement>(`[data-v4-tab="${tab}"]`)?.click();
}

async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

describe("v4 five-tab shell", () => {
  it("defaults to evolution and mounts every real data-backed panel only when selected", () => {
    const scan = vi.fn();
    const root = renderMarkdownV4View(v4SnapshotFixture(), vi.fn(), { loadSessionEvents: scan });

    expect([...root.querySelectorAll('[role="tab"]')].map((node) => node.textContent)).toEqual(labels);
    expect(root.querySelector('[data-v4-panel="evolution"]')).not.toBeNull();
    expect(root.querySelector('[aria-label="扫描 Session"]')).toBeNull();
    expect(scan).not.toHaveBeenCalled();

    click(root, "sessions");
    expect(root.querySelector('[aria-label="扫描 Session"]')).not.toBeNull();
    click(root, "evolution");
    expect(root.querySelector('[aria-label="扫描 Session"]')).toBeNull();
    click(root, "problems");
    expect(root.textContent).toContain("如何确保同一字段只有一个权威位置？");
    expect(root.querySelector('[aria-label="正式问题树"]')).not.toBeNull();
    expect(root.querySelector('[aria-label="当前问题与子问题"]')).not.toBeNull();
    expect(root.querySelector('[aria-label="问题证据与问答来源"]')).not.toBeNull();
    click(root, "decisions");
    expect(root.textContent).toContain("选择 [Markdown] 作为人工编辑面。");
    click(root, "usage");
    expect(root.textContent).toContain("model-1");
    expect(root.textContent).toContain("api.example.test");
    expect(root.textContent).toContain("输入 Token10");
    expect(root.textContent).toContain("缓存输入 Token0");
    expect(root.textContent).toContain("缓存写入 Token0");
    expect(root.textContent).toContain("输出 Token5");
    expect(root.textContent).toContain("推理输出 Token0");
    expect(root.textContent).toContain("每百万 Token$1");
    expect(root.textContent).toContain("官方价格页");
    expect(scan).toHaveBeenCalledTimes(1);
  });

  it("shows one compact trust disclosure with visible snapshot and scan-coverage status", () => {
    const root = renderMarkdownV4View(v4SnapshotFixture(), vi.fn());

    expect(root.querySelector(".sr-v4-header")?.textContent).toContain("待私有验证 · 只读");
    expect(root.querySelector(".sr-v4-header")?.textContent).toContain("Sessions 1 · 完整 1 · 部分 0 · 异常 0 · 未处理 0 · 来源不可用 0");
    expect(root.querySelectorAll("details.sr-v4-trust-details")).toHaveLength(1);
    expect(root.textContent?.match(/公开文件校验不等于私有接受证明/g)).toHaveLength(1);
  });

  it("marks long accepted header fields as bounded previews and exposes their full text by keyboard", () => {
    const snapshot = v4SnapshotFixture();
    if (snapshot.state.kind !== "public_valid") throw new Error("expected fixture");
    const longGoal = `长目标 ${"恢复可读且可验证的项目上下文。".repeat(12)}`;
    const longNext = `长下一步 ${"先完成测试再进行真实环境验收。".repeat(12)}`;
    snapshot.state.value.presentation.current_state.goal = longGoal;
    snapshot.state.value.presentation.current_state.next_action = longNext;
    const root = renderMarkdownV4View(snapshot, vi.fn());
    document.body.append(root);

    const goal = root.querySelector<HTMLDetailsElement>('[data-v4-state-field="goal"]');
    const next = root.querySelector<HTMLDetailsElement>('[data-v4-state-field="next_action"]');
    expect(goal).not.toBeNull();
    expect(next).not.toBeNull();
    expect(goal!.querySelector("summary")?.textContent).toContain("展开全文");
    expect(next!.querySelector("summary")?.textContent).toContain("展开全文");
    const nextSummary = next!.querySelector<HTMLElement>("summary")!;
    nextSummary.focus();
    nextSummary.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(next!.open).toBe(true);
    expect(next!.querySelector(".sr-v4-state-full")?.textContent).toBe(longNext);
    expect(document.activeElement).toBe(nextSummary);
    root.remove();
  });

  it("supports roving focus and selection with ArrowLeft, ArrowRight, Home and End", () => {
    const root = renderMarkdownV4View(v4SnapshotFixture(), vi.fn());
    document.body.append(root);
    const first = root.querySelector<HTMLButtonElement>('[data-v4-tab="evolution"]')!;
    first.focus();
    first.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    expect(document.activeElement).toBe(root.querySelector('[data-v4-tab="problems"]'));
    expect(root.querySelector('[data-v4-tab="problems"]')?.getAttribute("aria-selected")).toBe("true");
    (document.activeElement as HTMLElement).dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true }));
    expect(document.activeElement).toBe(root.querySelector('[data-v4-tab="usage"]'));
    (document.activeElement as HTMLElement).dispatchEvent(new KeyboardEvent("keydown", { key: "Home", bubbles: true }));
    expect(document.activeElement).toBe(root.querySelector('[data-v4-tab="evolution"]'));
    (document.activeElement as HTMLElement).dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }));
    expect(document.activeElement).toBe(root.querySelector('[data-v4-tab="usage"]'));
    root.remove();
  });

  it("reaches the first and last milestone through a bounded full-history window", () => {
    const snapshot = v4SnapshotFixture();
    if (snapshot.state.kind !== "public_valid") throw new Error("expected fixture");
    const template = snapshot.state.value.presentation.timeline[0];
    snapshot.state.value.presentation.timeline = Array.from({ length: 18 }, (_value, index) => ({
      ...structuredClone(template), id: `milestone:${String(index + 1).padStart(2, "0")}`, title: `里程碑 ${index + 1}`,
      occurred_at: `2026-09-${String(index + 1).padStart(2, "0")}T00:00:00Z`
    }));
    const root = renderMarkdownV4View(snapshot, vi.fn());

    expect(root.querySelectorAll("[data-v4-milestone-id]").length).toBeLessThan(18);
    root.querySelector<HTMLButtonElement>('[data-v4-history-mode="full"]')!.click();
    expect(root.textContent).toContain("里程碑 1");
    expect(root.querySelector(".sr-v4-timeline-rail")?.textContent).not.toContain("里程碑 18");
    root.querySelector<HTMLButtonElement>('[data-v4-history-page="last"]')!.click();
    expect(root.textContent).toContain("里程碑 18");
    expect(root.querySelectorAll("[data-v4-milestone-id]").length).toBeLessThan(18);
    root.querySelector<HTMLButtonElement>('[data-v4-milestone-id="milestone:18"]')!.click();
    expect(root.querySelector(".sr-v4-milestone-detail h2")?.textContent).toBe("里程碑 18");
  });

  it("keeps the full-history page and selected milestone focus after choosing a paged item", () => {
    const snapshot = v4SnapshotFixture();
    if (snapshot.state.kind !== "public_valid") throw new Error("expected fixture");
    const template = snapshot.state.value.presentation.timeline[0];
    snapshot.state.value.presentation.timeline = Array.from({ length: 21 }, (_value, index) => ({
      ...structuredClone(template), id: `milestone:${String(index + 1).padStart(2, "0")}`, title: `里程碑 ${index + 1}`,
      occurred_at: `2026-09-${String(index + 1).padStart(2, "0")}T00:00:00Z`
    }));
    const root = renderMarkdownV4View(snapshot, vi.fn());
    document.body.append(root);

    root.querySelector<HTMLButtonElement>('[data-v4-history-mode="full"]')!.click();
    const first = root.querySelector<HTMLButtonElement>('[data-v4-milestone-id="milestone:01"]')!;
    first.focus();
    first.click();

    expect(root.querySelector('[data-v4-history-mode="recent"]')).not.toBeNull();
    expect(root.querySelector('[data-v4-history-page="first"]')).not.toBeNull();
    expect(root.querySelector('[data-v4-milestone-id="milestone:01"]')).not.toBeNull();
    expect(root.querySelector('.sr-v4-milestone-detail h2')?.textContent).toBe("里程碑 1");
    expect(document.activeElement).toBe(root.querySelector('[data-v4-milestone-id="milestone:01"]'));

    root.querySelector<HTMLButtonElement>('[data-v4-history-page="last"]')!.click();
    const last = root.querySelector<HTMLButtonElement>('[data-v4-milestone-id="milestone:21"]')!;
    last.focus();
    last.click();
    expect(root.querySelector('[data-v4-history-mode="recent"]')).not.toBeNull();
    expect(root.querySelector('[data-v4-milestone-id="milestone:21"]')).not.toBeNull();
    expect(root.querySelector('.sr-v4-milestone-detail h2')?.textContent).toBe("里程碑 21");
    expect(document.activeElement).toBe(root.querySelector('[data-v4-milestone-id="milestone:21"]'));
    root.remove();
  });

  it("selects a deep formal problem while keeping tree, path, direct children and evidence columns", () => {
    const snapshot = v4SnapshotFixture();
    if (snapshot.state.kind !== "public_valid") throw new Error("expected fixture");
    const template = snapshot.state.value.presentation.problem_nodes[0];
    snapshot.state.value.presentation.problem_nodes = [
      { ...structuredClone(template), id: "problem:root", question: "根问题？", primary_parent_id: null },
      { ...structuredClone(template), id: "problem:child", question: "子问题？", primary_parent_id: "problem:root" },
      { ...structuredClone(template), id: "problem:deep", question: "深层问题？", primary_parent_id: "problem:child", source_turn_refs: [{ provider: "codex", session_id: "session-deep", turn_unit_id: "turn-3" }] }
    ];
    snapshot.state.value.presentation.problem_root_ids = ["problem:root"];
    const root = renderMarkdownV4View(snapshot, vi.fn());
    click(root, "problems");
    root.querySelector<HTMLButtonElement>('[data-v4-problem-id="problem:deep"]')!.click();

    expect(root.querySelector('[aria-label="正式问题树"]')?.textContent).toContain("深层问题？");
    expect(root.querySelector('[aria-label="当前问题与子问题"]')?.textContent).toContain("根问题？ › 子问题？ › 深层问题？");
    expect(root.querySelector('[aria-label="问题证据与问答来源"]')?.textContent).toContain("codex / session-deep # turn-3");
  });

  it("keeps pending and stale valid presentations read-only and invalid snapshots fail closed", () => {
    const valid = v4SnapshotFixture();
    if (valid.state.kind !== "public_valid") throw new Error("expected public-valid fixture");
    const pending: Extract<Snapshot, { kind: "markdown-v4" }> = {
      ...valid,
      state: { kind: "pending_edit", value: valid.state.value, ledger: valid.state.ledger }
    };
    const stale: Extract<Snapshot, { kind: "markdown-v4-stale" }> = {
      kind: "markdown-v4-stale", lastValid: { kind: "markdown-v4", descriptor: valid.descriptor, state: valid.state, loadedAt: valid.loadedAt },
      state: { kind: "invalid", code: "wire_contract_invalid" },
      diagnostic: { code: "stale_snapshot", message: "stale" }
    };
    const invalid: Extract<Snapshot, { kind: "markdown-v4" }> = {
      ...valid, state: { kind: "invalid", code: "wire_contract_invalid" }
    };

    expect(renderMarkdownV4View(pending, vi.fn()).textContent).toContain("有未同步修改 · 只读");
    expect(renderMarkdownV4View(stale, vi.fn()).textContent).toContain("已过期 · 只读");
    const failed = renderMarkdownV4View(invalid, vi.fn());
    expect(failed.textContent).toContain("当前快照不可验证 · 只读");
    expect(failed.querySelector('[role="tab"]')).toBeNull();
  });
});

describe("v4 project state and lifecycle", () => {
  it("mounts all five panels through ProjectEvolutionView and restores the saved tab on reload", async () => {
    const project = { projectId: "project-p", root: "Projects/P", name: "P", format: "markdown-v4" as const };
    const repository = {
      discover: vi.fn().mockResolvedValue([project]),
      load: vi.fn().mockResolvedValue(v4SnapshotFixture()),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    let persisted: Record<string, V4ViewState> = {};
    const saveV4 = vi.fn((next: V4ViewState) => { persisted = { ...persisted, [next.projectId]: structuredClone(next) }; });
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, undefined, defaultViewState(), undefined, persisted, saveV4);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });

    await view.onOpen();
    expect([...view.contentEl.querySelectorAll('[role="tab"]')].map((node) => node.textContent)).toEqual(labels);
    for (const tab of ["problems", "decisions", "sessions", "usage", "evolution", "usage"] as const) {
      click(view.contentEl, tab);
      expect(view.contentEl.querySelector(`[data-v4-panel="${tab}"]`)).not.toBeNull();
    }
    await view.onClose();

    const reloaded = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, undefined, defaultViewState(), undefined, persisted, saveV4);
    Object.assign(reloaded, { app: { workspace: { openLinkText: vi.fn() } } });
    await reloaded.onOpen();
    expect(reloaded.contentEl.querySelector('[data-v4-tab="usage"]')?.getAttribute("aria-selected")).toBe("true");
    expect(reloaded.contentEl.querySelector('[data-v4-panel="usage"]')).not.toBeNull();
    await reloaded.onClose();
  });

  it("restores each project's tab without sharing milestone or problem selection", async () => {
    const projectA = { projectId: "project-a", root: "Projects/A", name: "A", format: "markdown-v4" as const };
    const projectB = { projectId: "project-b", root: "Projects/B", name: "B", format: "markdown-v4" as const };
    const repository = {
      discover: vi.fn().mockResolvedValue([projectA, projectB]),
      load: vi.fn(async (project: typeof projectA) => v4SnapshotFixture(project.projectId, project.name)),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const states = {
      "project-a": { projectId: "project-a", view: "decisions", selectedMilestoneId: "milestone-a", selectedProblemId: "problem-a" },
      "project-b": { projectId: "project-b", view: "usage", selectedMilestoneId: "milestone-b", selectedProblemId: "problem-b" }
    } satisfies Record<string, V4ViewState>;
    const saveV4 = vi.fn();
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, undefined, defaultViewState(), undefined, states, saveV4);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });

    await view.onOpen();
    expect(view.contentEl.querySelector('[data-v4-tab="decisions"]')?.getAttribute("aria-selected")).toBe("true");
    const picker = view.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')!;
    picker.value = projectB.projectId;
    picker.dispatchEvent(new Event("change"));
    await settle();
    expect(view.contentEl.querySelector('[data-v4-tab="usage"]')?.getAttribute("aria-selected")).toBe("true");
    click(view.contentEl, "problems");
    expect(saveV4).toHaveBeenLastCalledWith({ projectId: "project-b", view: "problems", selectedMilestoneId: null, selectedProblemId: "problem:alpha" });
    await view.onClose();
  });

  it("disposes the Sessions panel so a late response cannot update a detached tab", async () => {
    let resolve!: (value: never) => void;
    const pending = new Promise<never>((done) => { resolve = done; });
    const root = renderMarkdownV4View(v4SnapshotFixture(), vi.fn(), { loadSessionEvents: () => pending });
    click(root, "sessions");
    const sessions = root.querySelector('[data-v4-panel="sessions"]')!;
    click(root, "evolution");
    resolve({ items: [{ excerpt: "late detached response" }] } as never);
    await settle();

    expect(sessions.isConnected).toBe(false);
    expect(root.textContent).not.toContain("late detached response");
  });

  it("does not remount or leak the Sessions reader when its active tab is clicked again", () => {
    const loadSessionEvents = vi.fn(() => new Promise<never>(() => {}));
    const root = renderMarkdownV4View(v4SnapshotFixture(), vi.fn(), { loadSessionEvents });
    click(root, "sessions");
    const firstRecords = root.scanRecords;

    click(root, "sessions");

    expect(root.scanRecords).toBe(firstRecords);
    expect(loadSessionEvents).toHaveBeenCalledTimes(1);
    root.dispose?.();
  });

  it("does not start scan or model work while switching v4 tabs", async () => {
    const project = { projectId: "project-p", root: "Projects/P", name: "P", format: "markdown-v4" as const };
    const repository = { discover: vi.fn().mockResolvedValue([project]), load: vi.fn().mockResolvedValue(v4SnapshotFixture()), watch: vi.fn().mockReturnValue(vi.fn()) };
    const runner = { status: vi.fn().mockResolvedValue({}), startScan: vi.fn(), getSessionEvents: vi.fn(), getConversation: vi.fn() };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, runner as unknown as CliRunner);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });
    await view.onOpen();
    click(view.contentEl, "problems");
    click(view.contentEl, "decisions");
    click(view.contentEl, "usage");
    expect(runner.startScan).not.toHaveBeenCalled();
    expect(runner.getSessionEvents).not.toHaveBeenCalled();
    expect(runner.getConversation).not.toHaveBeenCalled();
    await view.onClose();
  });
});
