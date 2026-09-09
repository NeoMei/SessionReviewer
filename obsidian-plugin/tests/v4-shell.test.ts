import { WorkspaceLeaf } from "obsidian";
import { describe, expect, it, vi } from "vitest";
import type { CliRunner } from "../src/cli/runner";
import type { Snapshot } from "../src/data/repository";
import { renderMarkdownV4View } from "../src/view/presentation";
import { ProjectEvolutionView } from "../src/view/project-view";
import { defaultViewState } from "../src/view/render-shell";
import { normalizeV4ViewState, type V4ViewState } from "../src/state/v4-view-state";
import { v4SnapshotFixture } from "./fixtures/v4-shell";
import { selectedConversationPage, visibleMessage, visibleTurn } from "./fixtures/conversation";

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

  it("loads one snapshot-qualified milestone answer only after keyboard activation", () => {
    const snapshot = v4SnapshotFixture();
    if (snapshot.state.kind !== "public_valid") throw new Error("expected fixture");
    const presentation = snapshot.state.value.presentation;
    const milestone = presentation.timeline[0];
    const view = `sha256:${"2".repeat(64)}`;
    milestone.closed_loop.conclusion.source_turn_refs = [{ provider: "claude", session_id: "session-1", turn_unit_id: "turn-accepted", session_view_digest: view }];
    presentation.chain_dependencies = [{ provider: "claude", session_id: "session-1", session_view_digest: view, dependency_digest: `sha256:${"3".repeat(64)}`, turn_unit_ids: ["turn-accepted"] }];
    const loadConversation = vi.fn(() => new Promise<never>(() => {}));
    const root = renderMarkdownV4View(snapshot, vi.fn(), { loadConversation });

    expect(loadConversation).not.toHaveBeenCalled();
    click(root, "usage");
    click(root, "evolution");
    expect(loadConversation).not.toHaveBeenCalled();
    const expand = root.querySelector<HTMLButtonElement>('[data-action="expand-milestone-answer"]')!;
    expand.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));

    expect(loadConversation).toHaveBeenCalledTimes(1);
    expect(loadConversation).toHaveBeenCalledWith({
      projectId: "project-p",
      provider: "claude",
      sessionId: "session-1",
      expectedGenerationId: "generation-1",
      expectedSessionViewDigest: view,
      sessionViewDigest: view,
      limit: 20,
      turnUnitId: "turn-accepted"
    });
    expect(root.querySelector('[data-action="expand-milestone-answer"]')?.getAttribute("aria-expanded")).toBe("true");
  });

  it("never mounts the answer reader without CLI or a bound index", () => {
    const setup = (withIndex: boolean, cliUnavailable: boolean) => {
      const snapshot = v4SnapshotFixture();
      if (snapshot.state.kind !== "public_valid") throw new Error("expected fixture");
      const presentation = snapshot.state.value.presentation;
      presentation.timeline[0].closed_loop.conclusion.source_turn_refs = [{ provider: "claude", session_id: "session-1", turn_unit_id: "turn-accepted", session_view_digest: `sha256:${"2".repeat(64)}` }];
      presentation.chain_dependencies = [{ provider: "claude", session_id: "session-1", session_view_digest: `sha256:${"2".repeat(64)}`, dependency_digest: `sha256:${"3".repeat(64)}`, turn_unit_ids: ["turn-accepted"] }];
      if (!withIndex) delete (snapshot.state as unknown as { index?: unknown }).index;
      const loadConversation = vi.fn(() => new Promise<never>(() => {}));
      return { root: renderMarkdownV4View(snapshot, vi.fn(), { loadConversation, cliUnavailable }), loadConversation };
    };

    for (const value of [setup(true, true), setup(false, false)]) {
      click(value.root, "usage");
      click(value.root, "evolution");
      const action = value.root.querySelector<HTMLButtonElement>('[data-action="expand-milestone-answer"]')!;
      expect(action.disabled).toBe(true);
      action.click();
      expect(value.loadConversation).not.toHaveBeenCalled();
    }
  });

  it("preserves expanded answers across rail-only redraws and disposes them on milestone, tab and shell replacement boundaries", () => {
    const snapshot = v4SnapshotFixture();
    if (snapshot.state.kind !== "public_valid") throw new Error("expected fixture");
    const presentation = snapshot.state.value.presentation;
    const seed = presentation.timeline[0];
    const view = `sha256:${"2".repeat(64)}`;
    presentation.chain_dependencies = [{ provider: "claude", session_id: "session-1", session_view_digest: view, dependency_digest: `sha256:${"3".repeat(64)}`, turn_unit_ids: ["turn-accepted"] }];
    presentation.timeline = ["older", "newer"].map((id, index) => {
      const item = structuredClone(seed);
      item.id = id;
      item.title = id;
      item.occurred_at = `2026-09-0${index + 1}T00:00:00Z`;
      item.closed_loop.conclusion.source_turn_refs = [{ provider: "claude", session_id: "session-1", turn_unit_id: "turn-accepted", session_view_digest: view }];
      return item;
    });
    const loadConversation = vi.fn(() => new Promise<never>(() => {}));
    const root = renderMarkdownV4View(snapshot, vi.fn(), { loadConversation });
    const expand = () => root.querySelector<HTMLButtonElement>('[data-action="expand-milestone-answer"]')!.click();

    expand();
    root.querySelector<HTMLButtonElement>('[data-v4-milestone-id="older"]')!.click();
    expect(root.querySelector(".sr-conversation")).toBeNull();
    expand();
    root.querySelector<HTMLButtonElement>('[data-v4-history-mode="full"]')!.click();
    expect(root.querySelector(".sr-conversation")).not.toBeNull();
    click(root, "usage");
    expect(root.querySelector(".sr-conversation")).toBeNull();
    click(root, "evolution");
    expand();
    root.dispose?.();
    expect(root.querySelector(".sr-conversation")).toBeNull();
    expect(loadConversation).toHaveBeenCalledTimes(3);
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

  it("uses the same latest actual milestone on first render and after leaving and re-entering evolution", () => {
    const snapshot = v4SnapshotFixture();
    if (snapshot.state.kind !== "public_valid") throw new Error("expected fixture");
    const seed = snapshot.state.value.presentation.timeline[0];
    snapshot.state.value.presentation.timeline = [
      { ...structuredClone(seed), id: "later-utc", occurred_at: "2026-09-08T01:00:00Z", title: "Later actual instant" },
      { ...structuredClone(seed), id: "earlier-offset", occurred_at: "2026-09-08T08:00:00+08:00", title: "Earlier actual instant" }
    ];
    const saveStatePatch = vi.fn();
    const root = renderMarkdownV4View(snapshot, vi.fn(), { saveStatePatch });

    expect(root.querySelector(".sr-v4-milestone-detail h2")?.textContent).toBe("Later actual instant");
    click(root, "usage");
    click(root, "evolution");
    expect(root.querySelector(".sr-v4-milestone-detail h2")?.textContent).toBe("Later actual instant");
    expect(saveStatePatch).toHaveBeenLastCalledWith(expect.objectContaining({ selectedMilestoneId: "later-utc" }));
  });

  it("preserves an explicitly selected older milestone across history mode changes and unrelated tabs", () => {
    const snapshot = v4SnapshotFixture();
    if (snapshot.state.kind !== "public_valid") throw new Error("expected fixture");
    const seed = snapshot.state.value.presentation.timeline[0];
    snapshot.state.value.presentation.timeline = [
      { ...structuredClone(seed), id: "older", occurred_at: "2026-09-07T00:00:00Z", title: "Explicit older" },
      { ...structuredClone(seed), id: "newer", occurred_at: "2026-09-08T00:00:00Z", title: "Default newer" }
    ];
    const initialState = { projectId: snapshot.descriptor.projectId, view: "evolution", selectedMilestoneId: "older", selectedProblemId: null };
    const root = renderMarkdownV4View(snapshot, vi.fn(), { initialState });

    expect(root.querySelector(".sr-v4-milestone-detail h2")?.textContent).toBe("Explicit older");
    root.querySelector<HTMLButtonElement>('[data-v4-history-mode="full"]')!.click();
    root.querySelector<HTMLButtonElement>('[data-v4-history-mode="recent"]')!.click();
    click(root, "decisions");
    click(root, "evolution");
    expect(root.querySelector(".sr-v4-milestone-detail h2")?.textContent).toBe("Explicit older");
    expect(root.querySelector('[data-v4-milestone-id="older"]')?.getAttribute("aria-selected")).toBe("true");
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

  it("creates and confirms an empty-graph root without optimistic mutation", async () => {
	const snapshot = v4SnapshotFixture();
	if (snapshot.state.kind !== "public_valid") throw new Error("expected fixture");
	snapshot.state.value.presentation.problem_nodes = [];
	snapshot.state.value.presentation.problem_root_ids = [];
	const createProblem = vi.fn().mockResolvedValue(undefined);
	const transitionCandidate = vi.fn().mockResolvedValue(undefined);
	const candidate = {
		candidate_id: "candidate-human", project_id: snapshot.descriptor.projectId, question: "保持用户原文？", source_turn_refs: [],
		recommended_relation: "keep_pending" as const, recommended_target_id: null, alternate_target_ids: [], related_node_ids: [],
		grounds: [{ rule_id: "human-created", rule_version: "v1", matched_fact_refs: [], explanation: "由用户明确创建，等待确认正式位置。" }],
		confidence: "low" as const, status: "pending" as const, dependency_digests: [], analysis_mode: "deterministic" as const,
		agent_run_id: null, revision: 1, created_at: "2026-09-09T00:00:00Z", updated_at: "2026-09-09T00:00:00Z"
	};
	const root = renderMarkdownV4View(snapshot, vi.fn(), { problemCandidates: [], createProblem, transitionCandidate });
	click(root, "problems");
	const input = root.querySelector<HTMLTextAreaElement>('[data-v4-new-problem]')!;
	input.value = "保持用户原文？";
	root.querySelector<HTMLButtonElement>('[data-action="create-problem-candidate"]')!.click();
	await settle();
	expect(createProblem).toHaveBeenCalledWith("保持用户原文？");
	expect(root.querySelector('[data-v4-problem-id]')).toBeNull();

	const pending = renderMarkdownV4View(snapshot, vi.fn(), { problemCandidates: [candidate], transitionCandidate });
	click(pending, "problems");
	expect(pending.textContent).toContain("由用户明确创建");
	pending.querySelector<HTMLButtonElement>('[data-action="apply-root"]')!.click();
	expect(transitionCandidate).not.toHaveBeenCalled();
	pending.querySelector<HTMLButtonElement>('[data-action="confirm-apply-root"]')!.click();
	await settle();
	expect(transitionCandidate).toHaveBeenCalledWith(candidate, "apply_root", undefined);
	expect(pending.querySelector('[data-v4-problem-id]')).toBeNull();
  });

  it("keeps manual creation available on a non-empty graph and folds applied candidates into history", async () => {
	const snapshot = v4SnapshotFixture();
	if (snapshot.state.kind !== "public_valid") throw new Error("expected fixture");
	const createProblem = vi.fn().mockResolvedValue(undefined);
	const applied = {
		candidate_id: "candidate-applied", project_id: snapshot.descriptor.projectId, question: "已确认问题？", source_turn_refs: [],
		recommended_relation: "keep_pending" as const, recommended_target_id: null, alternate_target_ids: [], related_node_ids: [],
		grounds: [{ rule_id: "human-created", rule_version: "v1", matched_fact_refs: [], explanation: "由用户明确创建。" }],
		confidence: "low" as const, status: "applied" as const, dependency_digests: [], analysis_mode: "deterministic" as const,
		agent_run_id: null, revision: 2, created_at: "2026-09-09T00:00:00Z", updated_at: "2026-09-09T00:01:00Z"
	};
	const root = renderMarkdownV4View(snapshot, vi.fn(), { createProblem, problemCandidates: [applied] });
	click(root, "problems");
	const input = root.querySelector<HTMLTextAreaElement>('[data-v4-new-problem]')!;
	input.value = "  新的子问题？  ";
	root.querySelector<HTMLButtonElement>('[data-action="create-problem-candidate"]')!.click();
	await settle();
	expect(createProblem).toHaveBeenCalledWith("  新的子问题？  ");
	const pending = root.querySelector<HTMLElement>('[aria-label="待归类问题"]')!;
	expect(pending.querySelector("h3")?.textContent).toBe("待归类问题 0");
	expect(pending.querySelectorAll(":scope > article")).toHaveLength(0);
	expect(pending.querySelector("details")?.textContent).toContain("已确认问题？");
  });

  it("opens an exact problem source and requires explicit resolve confirmation", async () => {
	const snapshot = v4SnapshotFixture();
	if (snapshot.state.kind !== "public_valid") throw new Error("expected fixture");
	const node = snapshot.state.value.presentation.problem_nodes[0];
	const view = `sha256:${"7".repeat(64)}`;
	node.source_turn_refs = [{ provider: "codex", session_id: "session-source", turn_unit_id: "turn-source", session_view_digest: view }];
	const sourceRef = (role: "user" | "assistant", ordinal: number) => ({ provider: "codex", session_id: "session-source", source_identity: "source-1", record_ordinal: ordinal, source_hash: "3".repeat(64) });
	const page = selectedConversationPage({ minimum_reader_version: "0.4.3", project_id: snapshot.descriptor.projectId, session_id: "session-source", generation_id: snapshot.state.value.presentation.generation_id, session_view_digest: view, turn_unit_id: "turn-source",
		turn_units: [visibleTurn({ turn_unit_id: "turn-source", user_message: visibleMessage("user", { source_ref: sourceRef("user", 1), visible_excerpt: "来源问题？", text: null }) })],
		messages: [visibleMessage("user", { source_ref: sourceRef("user", 1), visible_excerpt: "来源问题？", text: "来源问题？" }), visibleMessage("assistant", { source_ref: sourceRef("assistant", 2), visible_excerpt: "来源回答。", text: "来源回答。" })] });
	const loadConversation = vi.fn().mockResolvedValue(page);
	const setProblemState = vi.fn().mockResolvedValue(undefined);
	const root = renderMarkdownV4View(snapshot, vi.fn(), { loadConversation, setProblemState });
	click(root, "problems");
	root.querySelector<HTMLButtonElement>('[data-action="open-problem-source"]')!.click();
	await settle();
	expect(loadConversation).toHaveBeenCalledWith(expect.objectContaining({ projectId: snapshot.descriptor.projectId, provider: "codex", sessionId: "session-source", turnUnitId: "turn-source", expectedSessionViewDigest: view, sessionViewDigest: view }));
	expect(root.querySelector('.sr-v4-problem-source-answer')?.textContent).toContain("来源回答。");
	root.querySelector<HTMLButtonElement>('[data-action="resolve-problem"]')!.click();
	expect(setProblemState).not.toHaveBeenCalled();
	root.querySelector<HTMLButtonElement>('[data-action="confirm-resolve-problem"]')!.click();
	await settle();
	expect(setProblemState).toHaveBeenCalledWith(node, "resolve");
  });

  it("disposes a pending problem-source viewer when leaving the tab", async () => {
	const snapshot = v4SnapshotFixture(); if (snapshot.state.kind !== "public_valid") throw new Error("expected fixture"); const node = snapshot.state.value.presentation.problem_nodes[0]; const view = `sha256:${"7".repeat(64)}`;
	node.source_turn_refs = [{ provider: "codex", session_id: "session-source", turn_unit_id: "turn-source", session_view_digest: view }];
	let resolve!: (value: never) => void; const loadConversation = vi.fn(() => new Promise<never>((done) => { resolve = done; }));
	const root = renderMarkdownV4View(snapshot, vi.fn(), { loadConversation }); click(root, "problems"); root.querySelector<HTMLButtonElement>('[data-action="open-problem-source"]')!.click(); click(root, "decisions");
	resolve({} as never); await settle(); expect(root.querySelector('.sr-v4-problem-source-answer')).toBeNull();
  });

  it("disposes a pending problem-source viewer when selecting another problem", async () => {
	const snapshot = v4SnapshotFixture(); if (snapshot.state.kind !== "public_valid") throw new Error("expected fixture"); const node = snapshot.state.value.presentation.problem_nodes[0]; const view = `sha256:${"7".repeat(64)}`;
	node.source_turn_refs = [{ provider: "codex", session_id: "session-source", turn_unit_id: "turn-source", session_view_digest: view }];
	const second = structuredClone(node); second.id = "problem:second"; second.question = "另一个问题？"; second.source_turn_refs = []; second.sibling_order = 1; snapshot.state.value.presentation.problem_nodes.push(second); snapshot.state.value.presentation.problem_root_ids.push(second.id);
	let resolve!: (value: never) => void; const loadConversation = vi.fn(() => new Promise<never>((done) => { resolve = done; }));
	const root = renderMarkdownV4View(snapshot, vi.fn(), { loadConversation }); click(root, "problems"); root.querySelector<HTMLButtonElement>('[data-action="open-problem-source"]')!.click(); root.querySelector<HTMLButtonElement>('[data-v4-problem-id="problem:second"]')!.click();
	resolve({} as never); await settle(); expect(root.querySelector('.sr-v4-problem-source-answer')?.textContent).toBe(""); expect(root.textContent).toContain("另一个问题？");
  });

  it("edits, previews subtree movement, and submits complete sibling order", async () => {
	const snapshot = v4SnapshotFixture(); if (snapshot.state.kind !== "public_valid") throw new Error("expected fixture");
	const first = snapshot.state.value.presentation.problem_nodes[0];
	const second = structuredClone(first); second.id = "problem:beta"; second.question = "第二个问题？"; second.sibling_order = 1;
	snapshot.state.value.presentation.problem_nodes.push(second); snapshot.state.value.presentation.problem_root_ids.push(second.id);
	const editProblem = vi.fn().mockResolvedValue(undefined), moveProblem = vi.fn().mockResolvedValue(undefined), reorderProblems = vi.fn().mockResolvedValue(undefined);
	const root = renderMarkdownV4View(snapshot, vi.fn(), { editProblem, moveProblem, reorderProblems }); click(root, "problems");
	const question = root.querySelector<HTMLTextAreaElement>('[data-problem-edit-question]')!; question.value = "  保留空格和原文？  ";
	root.querySelector<HTMLButtonElement>('[data-action="edit-problem"]')!.click(); root.querySelector<HTMLButtonElement>('[data-action="confirm-edit-problem"]')!.click(); await settle();
	expect(editProblem).toHaveBeenCalledWith(first, expect.objectContaining({ question: "  保留空格和原文？  " }));
	root.querySelector<HTMLButtonElement>('[data-action="move-problem"]')!.click(); expect(root.querySelector('[data-problem-move-preview]')?.textContent).toContain("原路径");
	root.querySelector<HTMLButtonElement>('[data-action="confirm-move-problem"]')!.click(); await settle(); expect(moveProblem).toHaveBeenCalledWith(first, "root");
	root.querySelector<HTMLButtonElement>('[data-action="move-problem-down"]')!.click(); await settle(); expect(reorderProblems).toHaveBeenCalledWith("root", [second.id, first.id]);
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
    expect(saveV4).toHaveBeenLastCalledWith(expect.objectContaining({ projectId: "project-b", view: "problems", selectedMilestoneId: "milestone-b", selectedProblemId: "problem:alpha" }));
    await view.onClose();
  });

  it("merges cross-leaf state patches in both directions", async () => {
    const project = { projectId: "project-p", root: "Projects/P", name: "P", format: "markdown-v4" as const };
    const repository = { discover: vi.fn().mockResolvedValue([project]), load: vi.fn().mockResolvedValue(v4SnapshotFixture()), watch: vi.fn().mockReturnValue(vi.fn()) };
    let shared = normalizeV4ViewState(undefined, project.projectId);
    const save = vi.fn((next: V4ViewState) => { shared = structuredClone(next); });
    const load = () => structuredClone(shared);
    const first = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, undefined, defaultViewState(), undefined, { [project.projectId]: shared }, save, load);
    const second = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, undefined, defaultViewState(), undefined, { [project.projectId]: shared }, save, load);
    Object.assign(first, { app: { workspace: { openLinkText: vi.fn() } } });
    Object.assign(second, { app: { workspace: { openLinkText: vi.fn() } } });
    await first.onOpen();
    await second.onOpen();

    click(first.contentEl, "sessions");
    click(second.contentEl, "problems");
    const search = first.contentEl.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')!;
    search.value = "session-1";
    search.dispatchEvent(new Event("input", { bubbles: true }));
    expect(shared.view).toBe("problems");
    expect(shared.selectedProblemId).toBe("problem:alpha");
    expect(shared.sessionBrowser?.query).toBe("session-1");

    click(first.contentEl, "sessions");
    const nextSearch = first.contentEl.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')!;
    nextSearch.value = "codex";
    nextSearch.dispatchEvent(new Event("input", { bubbles: true }));
    click(second.contentEl, "usage");
    expect(shared.view).toBe("usage");
    expect(shared.sessionBrowser?.query).toBe("codex");
    await first.onClose();
    await second.onClose();
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

it("wires private search and catalog actions through the five-tab shell without eager I/O",async()=>{
 const snapshot=v4SnapshotFixture();if(snapshot.state.kind!=="public_valid")throw new Error("fixture invalid");
 let searches=0;let catalogs=0;
 const price=snapshot.state.ledger.pricing_snapshots[0];snapshot.state.ledger.accounting.models=[{model:price.billed_model_id,total_tokens:15,total_cost_usd:null}];snapshot.state.ledger.current_pricing_snapshot_ids=[price.snapshot_id];
 const root=renderMarkdownV4View(snapshot,()=>{}, {loadSessionSearch:async request=>{searches++;return {schema_version:1,project_id:request.projectId,generation_id:request.expectedGenerationId,total:0,items:[],previous_cursor:null,next_cursor:null};},pricingActions:{catalog:async()=>{catalogs++;throw new Error("offline");},acceptCatalog:async()=>{}}});document.body.append(root);
 expect(searches+catalogs).toBe(0);click(root,"sessions");root.querySelector<HTMLInputElement>('[aria-label="搜索分支、文件或错误"]')!.value="missing";root.querySelector<HTMLButtonElement>('[data-action="private-session-search"]')!.click();await settle();expect(searches).toBe(1);
 click(root,"usage");const query=root.querySelector<HTMLButtonElement>('[data-action="query-catalog"]');expect(query).not.toBeNull();query!.click();await settle();expect(catalogs).toBe(1);expect(root.textContent).toContain("价格目录暂不可用");root.dispose?.();root.remove();
});

it("navigates from a milestone only to questions with the exact referenced provider, turn and snapshot",()=>{
 const snapshot=v4SnapshotFixture();if(snapshot.state.kind!=="public_valid")throw new Error("fixture invalid");const p=snapshot.state.value.presentation;
 const ref={provider:"codex",session_id:"session-one",turn_unit_id:"turn-one",session_view_digest:"sha256:"+"a".repeat(64)};
 p.timeline=[{...p.timeline[0],id:"milestone-linked",closed_loop:{...p.timeline[0].closed_loop,source_turn_refs:[ref]}}];
 const base=p.problem_nodes[0];p.problem_nodes=[{...base,id:"problem-linked",question:"准确关联问题",primary_parent_id:null,source_turn_refs:[ref]},{...base,id:"problem-wrong",question:"其他来源同名 Session",primary_parent_id:null,source_turn_refs:[{...ref,provider:"claude"}]}];p.problem_root_ids=["problem-linked","problem-wrong"];
 const root=renderMarkdownV4View(snapshot,()=>{});document.body.append(root);
 const links=root.querySelectorAll<HTMLButtonElement>('[data-action="open-related-problem"]');expect(links).toHaveLength(1);expect(links[0].textContent).toContain("准确关联问题");links[0].click();expect(root.querySelector('[data-v4-tab="problems"]')?.getAttribute("aria-selected")).toBe("true");expect(root.querySelector('[aria-label="问题证据与问答来源"]')?.textContent).toContain("准确关联问题");root.dispose?.();root.remove();
});
