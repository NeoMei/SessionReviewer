import { afterEach, describe, expect, it, vi } from "vitest";
import { Notice, WorkspaceLeaf } from "obsidian";
import type { CliRunner } from "../src/cli/runner";
import type { ScanStatus } from "../src/contracts/review-v3";
import type { ProjectDescriptor, ProjectRepository } from "../src/data/repository";
import { ProjectEvolutionView } from "../src/view/project-view";
import { defaultViewState } from "../src/view/render-shell";
import { browserModelFixture } from "./fixtures/browser";

const PROJECT_A: ProjectDescriptor = { projectId: "project-aaaaaaaaaaaaaaaa", root: "Projects/A", name: "项目甲" };

function noticeInstances(): string[] {
  return (Notice as unknown as { instances: string[] }).instances;
}

function scanStatusFixture(overrides: Partial<ScanStatus> = {}): ScanStatus {
  return {
    schema_version: 1,
    job_id: "scan-job-1",
    project_id: PROJECT_A.projectId,
    state: "completed",
    phase: "syncing",
    session_count: 5,
    indexed_count: 5,
    issue_count: 0,
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
    getScanStatus: vi.fn().mockResolvedValue(scanStatusFixture()),
    startScan: vi.fn().mockResolvedValue(scanStatusFixture({ state: "running", phase: "discovering" })),
    syncProject: vi.fn().mockResolvedValue("synced")
  };
}

type FakeRepository = ReturnType<typeof fakeRepository>;
type FakeRunner = ReturnType<typeof fakeRunner>;

async function openView(runner: FakeRunner | undefined, repository: FakeRepository): Promise<ProjectEvolutionView> {
  const view = new ProjectEvolutionView(
    new WorkspaceLeaf(),
    repository as unknown as ProjectRepository,
    undefined,
    runner as unknown as CliRunner,
    defaultViewState()
  );
  await view.onOpen();
  return view;
}

async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

afterEach(() => {
  noticeInstances().length = 0;
  vi.useRealTimers();
});

describe("scan job view", () => {
	it("shows the preserved worker cause when a scan fails", async () => {
		const repository = fakeRepository([PROJECT_A]);
		const runner = fakeRunner();
		runner.getScanStatus.mockResolvedValue(scanStatusFixture({
			state: "failed",
			error_code: "scan_failed",
			error_message: "project association requires explicit confirmation"
		}));
		const view = await openView(runner, repository);
		expect(view.contentEl.textContent).toContain("project association requires explicit confirmation");
		await view.onClose();
	});

	 it("runs a real sync for a pending human edit", async () => {
		const repository = fakeRepository([PROJECT_A]);
		repository.load.mockResolvedValue({
			kind: "pending_edit",
			model: browserModelFixture(),
			machine: {},
			diagnostic: { code: "sync_not_run", message: "等待同步" }
		});
		const runner = fakeRunner();
		const view = await openView(runner, repository);

		const action = view.contentEl.querySelector<HTMLButtonElement>('[data-status-action="sync_not_run"]');
		expect(action?.textContent).toBe("立即同步");
		action?.click();
		await settle();

		expect(runner.syncProject).toHaveBeenCalledWith(PROJECT_A.projectId);
		await view.onClose();
	});

  it("keeps one scan action in the header meta labeled 更新项目脉络", async () => {
    const repository = fakeRepository([PROJECT_A]);
    const runner = fakeRunner();
    const view = await openView(runner, repository);

    expect(runner.getScanStatus).toHaveBeenCalledWith(PROJECT_A.projectId);
    const action = view.contentEl.querySelector<HTMLButtonElement>(".sr-review-action");
    expect(action?.textContent).toBe("更新项目脉络");
    expect(view.contentEl.querySelector(".sr-review-banner")).toBeNull();
    await view.onClose();
  });

  it("starts a scan from the header action and shows the running banner with progress", async () => {
    const repository = fakeRepository([PROJECT_A]);
    const runner = fakeRunner();
    runner.getScanStatus.mockResolvedValueOnce(scanStatusFixture({ state: "completed" }))
      .mockResolvedValue(scanStatusFixture({ state: "running", phase: "extracting" }));

    const view = await openView(runner, repository);
    const action = view.contentEl.querySelector<HTMLButtonElement>(".sr-review-action")!;
    action.click();
    await settle();

    expect(runner.startScan).toHaveBeenCalledWith(PROJECT_A.projectId);
    await view.onClose();
  });

  it("surfaces scan command failures as a visible notice", async () => {
    const repository = fakeRepository([PROJECT_A]);
    const runner = fakeRunner();
    runner.startScan.mockRejectedValue(new Error("scan start rejected"));
    const view = await openView(runner, repository);

    const action = view.contentEl.querySelector<HTMLButtonElement>(".sr-review-action")!;
    action.click();
    await settle();

    expect(noticeInstances()).toContain("scan start rejected");
    await view.onClose();
  });

  it("stops polling when the view closes", async () => {
    vi.useFakeTimers();
    const repository = fakeRepository([PROJECT_A]);
    const runner = fakeRunner();
    runner.getScanStatus.mockResolvedValue(scanStatusFixture({ state: "running", phase: "reducing" }));
    const view = await openView(runner, repository);
    expect(runner.getScanStatus).toHaveBeenCalledTimes(1);

    await view.onClose();
    await vi.advanceTimersByTimeAsync(5000);
    expect(runner.getScanStatus).toHaveBeenCalledTimes(1);
  });
});
