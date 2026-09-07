import { WorkspaceLeaf } from "obsidian";
import { describe, expect, it, vi } from "vitest";
import type { CliRunner, SessionEventRequest, SessionSummaryRequest } from "../src/cli/runner";
import type { SessionEventPageV1, SessionSummaryV1 } from "../src/contracts/review-v4";
import type { Snapshot } from "../src/data/repository";
import { ProjectEvolutionView } from "../src/view/project-view";
import { renderReadyView } from "../src/view/render-shell";
import { browserModelFixture } from "./fixtures/browser";
import { populatedSessionSummary } from "./fixtures/session-summary";
import { v4SnapshotFixture } from "./fixtures/v4-shell";

function click(root: ParentNode, selector: string): void {
  const element = root.querySelector<HTMLElement>(selector);
  if (!element) throw new Error(`missing ${selector}`);
  element.click();
}

function text(root: ParentNode, selector: string): string {
  return root.querySelector(selector)?.textContent?.trim() ?? "";
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function controllerSnapshot(projectId: string, name: string): Extract<Snapshot, { kind: "markdown-v4" }> {
  const result = v4SnapshotFixture(projectId, name);
  if (result.state.kind !== "public_valid") throw new Error("expected public-valid fixture");
  result.state.index.sessions[0].session_id = `session-${name.toLocaleLowerCase()}`;
  return result;
}

function eventPageFor(request: SessionEventRequest, excerpt: string): SessionEventPageV1 {
  return {
    schema_version: 1,
    minimum_reader_version: "0.4.0",
    project_id: request.projectId,
    provider: request.provider,
    session_id: request.sessionId,
    generation_id: request.expectedGenerationId,
    session_view_digest: request.expectedSessionViewDigest,
    total: 1,
    range_start: 0,
    range_end: 1,
    items: [{ kind: "message", excerpt, revision_id: "revision-1", sequence: 1, occurred_at: "2026-09-07T00:00:00Z" }],
    previous_cursor: null,
    next_cursor: null,
    first_cursor: "first-token",
    last_cursor: "last-token",
    coverage: { seen: 1, indexed: 1, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 }
  };
}

function summaryFor(request: SessionSummaryRequest, textValue: string): SessionSummaryV1 {
  const result = populatedSessionSummary({
    project_id: request.projectId,
    provider: request.provider,
    session_id: request.sessionId,
    generation_id: request.expectedGenerationId,
    session_view_digest: request.expectedSessionViewDigest
  });
  result.phase_boundaries.items[0].text = textValue;
  return result;
}

async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

describe("project evolution browser", () => {
  it("switches adjacent node detail and returns from a decision to its event", () => {
    const view = renderReadyView(browserModelFixture());
    click(view, '[data-event-id="timeline-release"]');
    expect(text(view, '[data-role="detail-title"]')).toBe("v0.1.0 公开发布");
    expect(view.querySelector('[data-event-id="timeline-release"]')?.getAttribute("aria-selected")).toBe("true");

    click(view, '[data-view="decisions"]');
    click(view, '[data-action="open-related-event"][data-event-id="timeline-trust-chain"]');
    expect(view.querySelector('[data-view="evolution"]')?.getAttribute("aria-selected")).toBe("true");
    expect(text(view, '[data-role="detail-title"]')).toBe("信任链与 dry-run 边界修复");
  });

  it("renders concise resume information and full usage provenance", () => {
    const view = renderReadyView(browserModelFixture());
    expect(text(view, '[data-role="resume-stage"]')).toContain("v2 收尾");
    expect(text(view, '[data-role="resume-next"]')).toContain("完成 Obsidian 真实交互验收");
    click(view, '[data-view="usage"]');
    expect(text(view, '[data-role="usage-total"]')).toContain("350");
    expect(view.textContent).toContain("$4 / 百万 tokens");
    expect(view.textContent).toContain("2026-08-25");
  });

  it.each([
    [229_226_000, "2 天 15 时 40 分 26 秒"],
    [2_682_123_000, "1 月 1 天 1 时 2 分 3 秒"]
  ])("renders total duration %i ms in readable calendar-sized units", (totalDurationMs, expected) => {
    const model = browserModelFixture();
    model.accounting.totalDurationMs = totalDurationMs;

    const view = renderReadyView(model);
    click(view, '[data-view="usage"]');

    expect(text(view, '[data-role="usage-total"] .sr-definition:first-child dd')).toBe(expected);
  });

  it.each([
    [573_135_757, "5.73 亿（573,135,757）"],
    [99_999_999, "99,999,999"]
  ])("marks the 亿 magnitude for an aggregate token count of %i", (totalTokens, expected) => {
    const model = browserModelFixture();
    model.accounting.totalTokens = totalTokens;

    const view = renderReadyView(model);
    click(view, '[data-view="usage"]');

    expect(text(view, '[data-role="usage-total"] .sr-definition:nth-child(2) dd')).toBe(expected);
  });

  it("groups one model into a compact horizontal usage card", () => {
    const model = browserModelFixture();
    model.accounting.models[0].totalTokens = 573_135_757;

    const view = renderReadyView(model);
    click(view, '[data-view="usage"]');

    const card = view.querySelector<HTMLElement>(".sr-model-card");
    expect(card).toBeTruthy();
    expect([...card!.querySelectorAll(".sr-model-metrics dt")].map((node) => node.textContent)).toEqual(["Tokens", "费用", "Token 占比", "费用占比"]);
    expect([...card!.querySelectorAll(".sr-model-pricing dt")].map((node) => node.textContent)).toEqual(["输入价格", "缓存输入", "输出价格"]);
    expect(text(card!, ".sr-model-metrics")).toContain("5.73 亿（573,135,757）");
    expect(text(card!, ".sr-model-meta")).toContain("价格日期 2026-08-25");
    expect(text(card!, ".sr-model-meta")).toContain("定价来源");
    const source = card!.querySelector<HTMLAnchorElement>(".sr-model-source");
    expect(source?.textContent).toBe("https://example.com/pricing");
    expect(source?.href).toBe("https://example.com/pricing");
  });

  it("shows unavailable costs without invented zero pricing", () => {
	const model = browserModelFixture();
	model.accounting.pricingComplete = false;
	const sessionAccounting = model.sessions[0]?.accounting;
	if (!sessionAccounting) throw new Error("fixture session accounting is missing");
	sessionAccounting.pricingComplete = false;
		sessionAccounting.models[0].pricing = undefined;

	const view = renderReadyView(model);
	click(view, '[data-view="usage"]');

	expect(text(view, '[data-role="usage-total"]')).toContain("费用暂不可用");
	expect(text(view, ".sr-model-metrics")).toContain("费用暂不可用");
	expect(view.querySelector(".sr-model-pricing")).toBeNull();
  });

  it("translates machine-facing states and timestamps into readable Chinese", () => {
    const model = browserModelFixture();
    model.review.status = "at_risk";
    model.review.stage = "main";
    model.review.risks[0].status = "open";
    model.review.decisions[0].status = "accepted";
    model.review.decisions[0].occurredAt = "2026-08-25T03:45:44.447Z";
    model.events[0].kind = "verified";
    model.events[0].occurredAt = "2026-08-25T04:11:09.847Z";

    const view = renderReadyView(model);
    const status = view.querySelector<HTMLElement>(".sr-status")!;
    expect(status.textContent).toBe("有风险");
    expect(status.dataset.tone).toBe("warning");
    expect(text(view, '[data-role="resume-stage"]')).toContain("主线阶段");
    expect(text(view, ".sr-node-kind")).toBe("已验证");
    expect(text(view, ".sr-node-date")).not.toContain("T");

    click(view, '[data-view="decisions"]');
    const decisionMeta = text(view, ".sr-card-meta");
    expect(decisionMeta).toContain("已采纳");
    expect(decisionMeta).not.toContain("accepted");
    expect(decisionMeta).not.toContain("T");
  });

  it("keeps each risk concise until that individual card is expanded", () => {
    const model = browserModelFixture();
    model.review.risks[0].status = "open";
    model.review.risks[0].detail = "问题：真实 Vault 尚未完成验收。下一步：重新加载插件并逐项检查。";

    const view = renderReadyView(model);
    const risk = view.querySelector<HTMLDetailsElement>("details.sr-risk")!;
    const summary = risk.querySelector<HTMLElement>("summary.sr-risk-summary")!;
    const status = risk.querySelector<HTMLElement>(".sr-risk-status")!;
    const detail = risk.querySelector<HTMLElement>(".sr-risk-detail")!;

    expect(risk).toBeTruthy();
    expect(risk.open).toBe(false);
    expect(summary.textContent).toContain("UI 验收");
    expect(summary.textContent).toContain("待处理");
    expect(summary.textContent).toContain("真实 Vault 尚未完成验收");
    expect(status.dataset.tone).toBe("warning");
    expect(detail.textContent).toContain("重新加载插件并逐项检查");

    summary.click();
    expect(risk.open).toBe(true);
  });
});

describe("review header action", () => {
  it("appends exactly one review action to the header meta without moving existing children", () => {
    const model = browserModelFixture();
    const before = renderReadyView(model);
    const metaBefore = before.querySelector(".sr-header-meta")!;
    expect([...metaBefore.children].map((node) => (node as HTMLElement).className)).toEqual(["sr-status", ""]);
    expect([...(before.querySelector(".sr-header") as HTMLElement).children].map((node) => (node as HTMLElement).className)).toEqual(["", "sr-header-meta"]);

    const after = renderReadyView(model, undefined, undefined, undefined, { label: "总结并同步", disabled: false, onStart: () => {} });
    const metaAfter = after.querySelector(".sr-header-meta")!;
    expect([...metaAfter.children].map((node) => (node as HTMLElement).className)).toEqual(["sr-status", "", "sr-review-action"]);
    expect(after.querySelectorAll(".sr-review-action")).toHaveLength(1);
  });
});

describe("ProjectEvolutionView retained summary lifecycle", () => {
  it("persists a project switch immediately and ignores old pending event and summary responses", async () => {
    const projectA = { projectId: "project-a", root: "Projects/A", name: "A", format: "markdown-v4" as const };
    const projectB = { projectId: "project-b", root: "Projects/B", name: "B", format: "markdown-v4" as const };
    const pendingEventA = deferred<SessionEventPageV1>();
    const pendingSummaryA = deferred<SessionSummaryV1>();
    const repository = {
      discover: vi.fn().mockResolvedValue([projectA, projectB]),
      load: vi.fn(async (project: typeof projectA) => controllerSnapshot(project.projectId, project.name)),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const runner = {
      status: vi.fn().mockResolvedValue({}),
      getSessionEvents: vi.fn((request: SessionEventRequest) => request.projectId === projectA.projectId
        ? pendingEventA.promise
        : Promise.resolve(eventPageFor(request, "B 项目记录"))),
      getSessionSummary: vi.fn((request: SessionSummaryRequest) => request.projectId === projectA.projectId
        ? pendingSummaryA.promise
        : Promise.resolve(summaryFor(request, "B 项目摘要")))
    };
    const saveState = vi.fn();
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, runner as unknown as CliRunner, undefined, saveState);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });

    await view.onOpen();
    click(view.contentEl, '[data-v4-tab="sessions"]');
    const picker = view.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')!;
    picker.value = projectB.projectId;
    picker.dispatchEvent(new Event("change"));
    expect(saveState).toHaveBeenCalledWith(expect.objectContaining({ projectId: projectB.projectId }));
    await settle();
    click(view.contentEl, '[data-v4-tab="sessions"]');
    await settle();
    expect(view.contentEl.textContent).toContain("B 项目记录");
    expect(view.contentEl.textContent).toContain("B 项目摘要");

    const requestA = runner.getSessionSummary.mock.calls[0][0];
    pendingEventA.resolve(eventPageFor(runner.getSessionEvents.mock.calls[0][0], "A 过期记录"));
    pendingSummaryA.resolve(summaryFor(requestA, "A 过期摘要"));
    await settle();
    expect(view.contentEl.textContent).not.toContain("A 过期记录");
    expect(view.contentEl.textContent).not.toContain("A 过期摘要");
    await view.onClose();
  });

  it("invalidates a pending summary on project refresh and close", async () => {
    const project = { projectId: "project-p", root: "Projects/P", name: "P", format: "markdown-v4" as const };
    const pendingBeforeRefresh = deferred<SessionSummaryV1>();
    const pendingAfterRefresh = deferred<SessionSummaryV1>();
    const repository = {
      discover: vi.fn().mockResolvedValue([project]),
      load: vi.fn().mockImplementation(() => Promise.resolve(controllerSnapshot(project.projectId, project.name))),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const runner = {
      status: vi.fn().mockResolvedValue({}),
      getSessionEvents: vi.fn((request: SessionEventRequest) => Promise.resolve(eventPageFor(request, "P 项目记录"))),
      getSessionSummary: vi.fn().mockReturnValueOnce(pendingBeforeRefresh.promise).mockReturnValueOnce(pendingAfterRefresh.promise)
    };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, runner as unknown as CliRunner);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });

    await view.onOpen();
    click(view.contentEl, '[data-v4-tab="sessions"]');
    click(view.contentEl, '[data-action="refresh-v4-status"]');
    await settle();
    expect(runner.getSessionSummary).toHaveBeenCalledTimes(2);
    const request = runner.getSessionSummary.mock.calls[0][0] as SessionSummaryRequest;
    pendingAfterRefresh.resolve(summaryFor(request, "刷新后摘要"));
    pendingBeforeRefresh.resolve(summaryFor(request, "刷新前过期摘要"));
    await settle();
    expect(view.contentEl.textContent).toContain("刷新后摘要");
    expect(view.contentEl.textContent).not.toContain("刷新前过期摘要");

    await view.onClose();
    expect(view.contentEl.childElementCount).toBe(0);
  });
});
