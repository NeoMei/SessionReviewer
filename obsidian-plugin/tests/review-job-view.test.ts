import { afterEach, describe, expect, it, vi } from "vitest";
import { WorkspaceLeaf } from "obsidian";
import type { CliRunner, ReviewStatus } from "../src/cli/runner";
import type { ProjectDescriptor, ProjectRepository } from "../src/data/repository";
import { ProjectEvolutionView } from "../src/view/project-view";
import { defaultViewState } from "../src/view/render-shell";
import { browserModelFixture } from "./fixtures/browser";

const PROJECT_A: ProjectDescriptor = { projectId: "project-aaaaaaaaaaaaaaaa", root: "Projects/A", name: "项目甲" };
const PROJECT_B: ProjectDescriptor = { projectId: "project-bbbbbbbbbbbbbbbb", root: "Projects/B", name: "项目乙" };
const AGENT = "/bin/codex";

function reviewStatusFixture(overrides: Partial<ReviewStatus> = {}): ReviewStatus {
  return {
    schemaVersion: 1,
    projectId: PROJECT_A.projectId,
    state: "idle",
    attempt: 0,
    sessionIndex: 0,
    sessionCount: 0,
    acceptedPackets: 0,
    acceptedSessions: 0,
    canRetry: false,
    canCancel: false,
    canSyncOnly: false,
    ...overrides
  };
}

function fakeRepository(projects: ProjectDescriptor[]) {
  return {
    discover: vi.fn().mockResolvedValue(projects),
    load: vi.fn().mockImplementation(async () => ({ kind: "ready", model: browserModelFixture(), machine: {}, loadedAt: 1 })),
    watch: vi.fn().mockReturnValue(vi.fn()),
    ignoreSelfWrite: vi.fn(),
    loadConflict: vi.fn()
  };
}

function fakeRunner() {
  return {
    status: vi.fn().mockResolvedValue({}),
    reviewStatus: vi.fn().mockResolvedValue(reviewStatusFixture()),
    startReview: vi.fn(),
    cancelReview: vi.fn(),
    retryReview: vi.fn(),
    syncProject: vi.fn().mockResolvedValue("synced")
  };
}

type FakeRepository = ReturnType<typeof fakeRepository>;
type FakeRunner = ReturnType<typeof fakeRunner>;

async function openView(runner: FakeRunner, repository: FakeRepository, agentExecutable = AGENT): Promise<ProjectEvolutionView> {
  const view = new ProjectEvolutionView(
    new WorkspaceLeaf(),
    repository as unknown as ProjectRepository,
    undefined,
    runner as unknown as CliRunner,
    defaultViewState(),
    undefined,
    agentExecutable
  );
  await view.onOpen();
  document.body.append(view.contentEl);
  return view;
}

async function settle(): Promise<void> {
  await new Promise<void>((resolve) => setTimeout(resolve, 0));
}

afterEach(() => {
  vi.useRealTimers();
  document.body.replaceChildren();
});

describe("review job view", () => {
  it("keeps one review action in the header meta and no banner while idle", async () => {
    const repository = fakeRepository([PROJECT_A]);
    const runner = fakeRunner();
    const view = await openView(runner, repository);

    expect(runner.reviewStatus).toHaveBeenCalledTimes(1);
    expect(runner.reviewStatus).toHaveBeenCalledWith(PROJECT_A.projectId);
    expect(repository.load).toHaveBeenCalledTimes(1);
    const meta = view.contentEl.querySelector(".sr-header-meta")!;
    expect([...meta.children].map((node) => (node as HTMLElement).className)).toEqual(["sr-status", "", "sr-review-action"]);
    const action = view.contentEl.querySelector<HTMLButtonElement>(".sr-review-action")!;
    expect(action.textContent).toBe("总结并同步");
    expect(action.disabled).toBe(false);
    expect(view.contentEl.querySelector(".sr-review-banner")).toBeNull();
  });

  it("starts a review from the header action and shows the running banner with progress", async () => {
    const repository = fakeRepository([PROJECT_A]);
    const runner = fakeRunner();
    const running = reviewStatusFixture({ state: "running", jobId: "job-1", phase: "reviewing", attempt: 1, sessionIndex: 2, sessionCount: 12, canCancel: true });
    runner.reviewStatus.mockResolvedValueOnce(reviewStatusFixture()).mockResolvedValue(running);
    runner.startReview.mockResolvedValue(running);
    const view = await openView(runner, repository);

    view.contentEl.querySelector<HTMLButtonElement>(".sr-review-action")!.click();
    await settle();

    expect(runner.startReview).toHaveBeenCalledWith(PROJECT_A.projectId, AGENT);
    const banner = view.contentEl.querySelector<HTMLElement>(".sr-review-banner")!;
    expect(banner.dataset.reviewState).toBe("running");
    expect(banner.textContent).toContain("正在总结");
    expect(banner.textContent).toContain("2 / 12 会话");
    expect(banner.querySelector<HTMLButtonElement>('[data-review-action="cancel"]')).toBeTruthy();
    const action = view.contentEl.querySelector<HTMLButtonElement>(".sr-review-action")!;
    expect(action.textContent).toBe("正在总结");
    expect(action.disabled).toBe(true);
    await view.onClose();
  });

  it("cancels a running job from the banner", async () => {
    const repository = fakeRepository([PROJECT_A]);
    const runner = fakeRunner();
    const running = reviewStatusFixture({ state: "running", jobId: "job-1", phase: "reviewing", attempt: 1, canCancel: true });
    runner.reviewStatus.mockResolvedValue(running);
    runner.cancelReview.mockResolvedValue(reviewStatusFixture({ state: "cancel_requested", jobId: "job-1", phase: "reviewing", attempt: 1 }));
    const view = await openView(runner, repository);

    view.contentEl.querySelector<HTMLButtonElement>('[data-review-action="cancel"]')!.click();
    await settle();

    expect(runner.cancelReview).toHaveBeenCalledWith("job-1");
    expect(view.contentEl.querySelector<HTMLElement>(".sr-review-banner")!.dataset.reviewState).toBe("cancel_requested");
    await view.onClose();
  });

  it("polls with backoff and reloads repository data once when the job completes", async () => {
    vi.useFakeTimers();
    const repository = fakeRepository([PROJECT_A]);
    const runner = fakeRunner();
    const running = reviewStatusFixture({ state: "running", jobId: "job-1", phase: "reviewing", attempt: 1, sessionIndex: 5, sessionCount: 12, canCancel: true });
    const completed = reviewStatusFixture({ state: "completed", jobId: "job-1", attempt: 1, acceptedSessions: 12, reviewUsage: { totalTokens: 1234, pricingComplete: true } });
    runner.reviewStatus.mockResolvedValueOnce(running).mockResolvedValueOnce(running).mockResolvedValue(completed);
    const view = await openView(runner, repository);

    expect(runner.reviewStatus).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(999);
    expect(runner.reviewStatus).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(runner.reviewStatus).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1999);
    expect(runner.reviewStatus).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(runner.reviewStatus).toHaveBeenCalledTimes(3);

    const banner = view.contentEl.querySelector<HTMLElement>(".sr-review-banner")!;
    expect(banner.dataset.reviewState).toBe("completed");
    expect(banner.textContent).toContain("总结完成");
    expect(repository.load).toHaveBeenCalledTimes(2);
    expect(view.contentEl.querySelector(".sr-sr-only")?.textContent).toContain("自动总结完成。");

    await vi.advanceTimersByTimeAsync(10_000);
    expect(runner.reviewStatus).toHaveBeenCalledTimes(3);
    expect(repository.load).toHaveBeenCalledTimes(2);
    await view.onClose();
  });

  it("maps failure codes to fixed messages and offers retry plus sync-only", async () => {
    const repository = fakeRepository([PROJECT_A]);
    const runner = fakeRunner();
    runner.reviewStatus.mockResolvedValue(reviewStatusFixture({
      state: "failed",
      jobId: "job-1",
      attempt: 2,
      errorCode: "E_SYNC_CONFLICT",
      canRetry: true,
      canSyncOnly: true,
      retryExpectedAttempt: 2,
      retryExpectedRevision: 5
    }));
    runner.retryReview.mockResolvedValue(reviewStatusFixture({ state: "retrying", jobId: "job-1", attempt: 3, phase: "scanning", canCancel: true }));
    const view = await openView(runner, repository);

    const banner = view.contentEl.querySelector<HTMLElement>(".sr-review-banner")!;
    expect(banner.dataset.reviewState).toBe("failed");
    expect(banner.textContent).toContain("已总结，但同步存在冲突，请先处理冲突。");
    expect(banner.textContent).not.toContain("E_SYNC_CONFLICT");

    banner.querySelector<HTMLButtonElement>('[data-review-action="sync-only"]')!.click();
    await settle();
    expect(runner.syncProject).toHaveBeenCalledWith(PROJECT_A.projectId);

    view.contentEl.querySelector<HTMLButtonElement>('[data-review-action="retry"]')!.click();
    await settle();
    expect(runner.retryReview).toHaveBeenCalledWith("job-1", AGENT, 2, 5);
    expect(view.contentEl.querySelector<HTMLElement>(".sr-review-banner")!.dataset.reviewState).toBe("retrying");
    await view.onClose();
  });

  it("suppresses duplicate start clicks while the start request is in flight", async () => {
    const repository = fakeRepository([PROJECT_A]);
    const runner = fakeRunner();
    let resolveStart!: (value: ReviewStatus) => void;
    runner.startReview.mockReturnValue(new Promise<ReviewStatus>((resolve) => { resolveStart = resolve; }));
    const view = await openView(runner, repository);

    view.contentEl.querySelector<HTMLButtonElement>(".sr-review-action")!.click();
    expect(view.contentEl.querySelector<HTMLButtonElement>(".sr-review-action")!.disabled).toBe(true);
    view.contentEl.querySelector<HTMLButtonElement>(".sr-review-action")!.click();
    expect(runner.startReview).toHaveBeenCalledTimes(1);

    resolveStart(reviewStatusFixture({ state: "running", jobId: "job-1", phase: "reviewing", attempt: 1, canCancel: true }));
    await settle();
    expect(view.contentEl.querySelector<HTMLElement>(".sr-review-banner")!.dataset.reviewState).toBe("running");
    await view.onClose();
  });

  it("explains that Codex must be configured when starting without an agent path", async () => {
    const repository = fakeRepository([PROJECT_A]);
    const runner = fakeRunner();
    const view = await openView(runner, repository, "");

    view.contentEl.querySelector<HTMLButtonElement>(".sr-review-action")!.click();
    await settle();

    expect(runner.startReview).not.toHaveBeenCalled();
    expect(view.contentEl.querySelector(".sr-sr-only")?.textContent).toContain("尚未配置 Codex，请先在设置中验证。");
  });

  it("stops polling when the view closes", async () => {
    vi.useFakeTimers();
    const repository = fakeRepository([PROJECT_A]);
    const runner = fakeRunner();
    runner.reviewStatus.mockResolvedValue(reviewStatusFixture({ state: "running", jobId: "job-1", phase: "reviewing", attempt: 1, canCancel: true }));
    const view = await openView(runner, repository);
    expect(runner.reviewStatus).toHaveBeenCalledTimes(1);

    await view.onClose();
    await vi.advanceTimersByTimeAsync(10_000);

    expect(runner.reviewStatus).toHaveBeenCalledTimes(1);
  });

  it("stops polling for the previous project when switching projects", async () => {
    vi.useFakeTimers();
    const repository = fakeRepository([PROJECT_A, PROJECT_B]);
    const runner = fakeRunner();
    runner.reviewStatus.mockImplementation((projectId: string) => Promise.resolve(
      projectId === PROJECT_B.projectId
        ? reviewStatusFixture({ projectId: PROJECT_B.projectId })
        : reviewStatusFixture({ state: "running", jobId: "job-1", phase: "reviewing", attempt: 1, canCancel: true })
    ));
    const view = await openView(runner, repository);

    const select = view.contentEl.querySelector<HTMLSelectElement>(".sr-project-picker select")!;
    expect(select.value).toBe(PROJECT_A.projectId);
    select.value = PROJECT_B.projectId;
    select.dispatchEvent(new Event("change"));
    await vi.advanceTimersByTimeAsync(10_000);

    const projectACalls = runner.reviewStatus.mock.calls.filter((call) => call[0] === PROJECT_A.projectId);
    expect(projectACalls).toHaveLength(1);
    expect(view.contentEl.querySelector(".sr-review-banner")).toBeNull();
    await view.onClose();
  });
});
