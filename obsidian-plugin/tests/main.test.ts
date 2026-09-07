import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { WorkspaceLeaf } from "obsidian";
import { afterEach, describe, expect, it, vi } from "vitest";
import { discoverRuntime, type RuntimeDiscoveryOptions } from "../src/cli/discovery";
import SessionReviewerPlugin from "../src/main";
import type { ProjectEvolutionView } from "../src/view/project-view";
import { ProjectRepository } from "../src/data/repository";
import { v4SnapshotFixture } from "./fixtures/v4-shell";

const fixtureRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../../testdata/contracts/v4/markdown");
const fixture = (name: string): string => readFileSync(resolve(fixtureRoot, name), "utf8");

afterEach(() => { vi.restoreAllMocks(); });

describe("plugin lifecycle", () => {
  it("merges alternating saves from two leaves and serializes persistence before reload", async () => {
    const projects = [
      { projectId: "project-a", root: "Projects/A", name: "A", format: "markdown-v4" as const },
      { projectId: "project-b", root: "Projects/B", name: "B", format: "markdown-v4" as const }
    ];
    vi.spyOn(ProjectRepository.prototype, "discover").mockResolvedValue(projects);
    vi.spyOn(ProjectRepository.prototype, "load").mockImplementation(async (project) => v4SnapshotFixture(project.projectId, project.name));
    vi.spyOn(ProjectRepository.prototype, "watch").mockReturnValue(vi.fn());
    let stored: unknown = null;
    let inFlight = 0;
    let maxInFlight = 0;
    const saveData = vi.fn(async (value: unknown) => {
      inFlight += 1;
      maxInFlight = Math.max(maxInFlight, inFlight);
      await new Promise((resolve) => setTimeout(resolve, 1));
      stored = structuredClone(value);
      inFlight -= 1;
    });
    let createView: ((leaf: WorkspaceLeaf) => ProjectEvolutionView) | undefined;
    const plugin = new SessionReviewerPlugin({ workspace: {} } as never, {} as never);
    Object.assign(plugin, {
      loadData: vi.fn().mockResolvedValue(null), saveData,
      runtimeResolver: vi.fn().mockResolvedValue(undefined),
      registerView: vi.fn((_type: string, creator: (leaf: WorkspaceLeaf) => ProjectEvolutionView) => { createView = creator; }),
      addRibbonIcon: vi.fn(), addCommand: vi.fn()
    });
    await plugin.onload();
    const leafA = createView!(new WorkspaceLeaf());
    const leafB = createView!(new WorkspaceLeaf());
    const app = { workspace: { openLinkText: vi.fn() } };
    Object.assign(leafA, { app });
    Object.assign(leafB, { app });
    await leafA.onOpen();
    await leafB.onOpen();
    const pickerB = leafB.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')!;
    pickerB.value = "project-b";
    pickerB.dispatchEvent(new Event("change"));
    await new Promise((resolve) => setTimeout(resolve, 0));
    leafB.contentEl.querySelector<HTMLButtonElement>('[data-v4-tab="usage"]')!.click();
    const pickerA = leafA.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')!;
    pickerA.value = "project-b";
    pickerA.dispatchEvent(new Event("change"));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(leafA.contentEl.querySelector('[data-v4-tab="usage"]')?.getAttribute("aria-selected")).toBe("true");
    pickerA.value = "project-a";
    pickerA.dispatchEvent(new Event("change"));
    await new Promise((resolve) => setTimeout(resolve, 0));
    leafA.contentEl.querySelector<HTMLButtonElement>('[data-v4-tab="decisions"]')!.click();
    await new Promise((resolve) => setTimeout(resolve, 20));
    await leafA.onClose();
    await leafB.onClose();

    expect(maxInFlight).toBe(1);
    expect(stored).toMatchObject({
      v4ViewStates: {
        "project-a": { projectId: "project-a", view: "decisions" },
        "project-b": { projectId: "project-b", view: "usage" }
      }
    });

    let createReloaded: ((leaf: WorkspaceLeaf) => ProjectEvolutionView) | undefined;
    const reloaded = new SessionReviewerPlugin({ workspace: {} } as never, {} as never);
    Object.assign(reloaded, {
      loadData: vi.fn().mockResolvedValue(stored), saveData: vi.fn(),
      runtimeResolver: vi.fn().mockResolvedValue(undefined),
      registerView: vi.fn((_type: string, creator: (leaf: WorkspaceLeaf) => ProjectEvolutionView) => { createReloaded = creator; }),
      addRibbonIcon: vi.fn(), addCommand: vi.fn()
    });
    await reloaded.onload();
    const restoredA = createReloaded!(new WorkspaceLeaf());
    Object.assign(restoredA, { app });
    await restoredA.onOpen();
    expect(restoredA.contentEl.querySelector('[data-v4-tab="decisions"]')?.getAttribute("aria-selected")).toBe("true");
    const restoredPicker = restoredA.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')!;
    restoredPicker.value = "project-b";
    restoredPicker.dispatchEvent(new Event("change"));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(restoredA.contentEl.querySelector('[data-v4-tab="usage"]')?.getAttribute("aria-selected")).toBe("true");
    await restoredA.onClose();
  });

  it("auto-discovers the runtime, removes the settings tab, and clears legacy path fields", async () => {
    const plugin = new SessionReviewerPlugin({} as never, {} as never);
    const addSettingTab = vi.fn();
    const saveData = vi.fn().mockResolvedValue(undefined);
    const loadData = vi.fn().mockResolvedValue({ cliPath: "/bin/sr", codexPath: "/bin/codex", viewState: { fullHistory: true } });
    const runtimeResolver = vi.fn().mockResolvedValue({
      runner: { executable: "/bin/sr" }
    });

    Object.assign(plugin, {
      app: { workspace: { getLeaf: vi.fn(), revealLeaf: vi.fn() } },
      addSettingTab,
      saveData,
      loadData,
      registerView: vi.fn(),
      addRibbonIcon: vi.fn(),
      addCommand: vi.fn(),
      runtimeResolver
    });

    await plugin.onload();

    expect(runtimeResolver).toHaveBeenCalledWith({ legacyCliPath: "/bin/sr" });
    expect(addSettingTab).not.toHaveBeenCalled();
    expect(saveData).toHaveBeenLastCalledWith({ viewState: expect.anything() as never, v4ViewStates: {} });
  });

  it("cold-starts without a runtime and still opens public-valid Markdown natively in a read-only view", async () => {
    const projectRoot = "Projects/V4";
    const files = new Map<string, string>([
      [`${projectRoot}/项目回顾.md`, fixture("review.md")],
      [`${projectRoot}/项目历史.md`, fixture("history.md")],
      [`${projectRoot}/.session-reviewer/ledger.json`, fixture("ledger.json")],
      [`${projectRoot}/.session-reviewer/session-index.json`, fixture("index.json")]
    ]);
    const process = vi.fn();
    const openLinkText = vi.fn();
    const eventRefs = Array.from({ length: 4 }, (_value, index) => ({ index }));
    let nextEventRef = 0;
    const workspace = { getLeaf: vi.fn(), revealLeaf: vi.fn(), openLinkText };
    const vault = {
      getMarkdownFiles: () => [...files.keys()].filter((path) => path.endsWith(".md")).map((path) => ({
        path,
        basename: path.split("/").at(-1)!.replace(/\.md$/, "")
      })),
      getAbstractFileByPath: vi.fn(),
      adapter: {
        read: async (path: string) => {
          const value = files.get(path);
          if (value === undefined) throw Object.assign(new Error("missing file"), { code: "ENOENT" });
          return value;
        }
      },
      process,
      on: vi.fn(() => eventRefs[nextEventRef++]),
      offref: vi.fn()
    };
    const app = { workspace, vault, metadataCache: { getFileCache: vi.fn() } };
    const plugin = new SessionReviewerPlugin(app as never, {} as never);
    const checked: string[] = [];
    let createView: ((leaf: WorkspaceLeaf) => ProjectEvolutionView) | undefined;
    const runtimeResolver = vi.fn(async (options: RuntimeDiscoveryOptions = {}) => discoverRuntime({
      ...options,
      home: "/isolated/home",
      platform: "darwin",
      env: { PATH: "" },
      executableExists: async (candidate) => {
        checked.push(candidate);
        return false;
      }
    }));
    const saveData = vi.fn();

    Object.assign(plugin, {
      loadData: vi.fn().mockResolvedValue({ cliPath: "/stale/session-reviewer" }),
      saveData,
      runtimeResolver,
      registerView: vi.fn((_type: string, creator: (leaf: WorkspaceLeaf) => ProjectEvolutionView) => { createView = creator; }),
      addRibbonIcon: vi.fn(),
      addCommand: vi.fn()
    });

    await plugin.onload();
    expect(createView).toBeDefined();
    const view = createView!(new WorkspaceLeaf());
    Object.assign(view, { app });
    await view.onOpen();
    try {
      expect(runtimeResolver).toHaveBeenCalledWith({ legacyCliPath: "/stale/session-reviewer" });
      expect(checked).toContain("/stale/session-reviewer");
      expect(view.contentEl.textContent).toContain("待私有验证 · 只读");
      expect(view.contentEl.textContent).toContain("公开文件校验不等于私有接受证明");
      expect(view.contentEl.textContent).toContain("CLI 不可用：只能原生阅读或编辑 Markdown，不能验证或同步");
      expect(view.contentEl.querySelector("[data-resolution-action], [data-status-action], [data-action='edit-v4'], .sr-review-action")).toBeNull();
      view.contentEl.querySelector<HTMLButtonElement>('[data-v4-tab="usage"]')!.click();
      await Promise.resolve();
      expect(saveData).toHaveBeenLastCalledWith(expect.objectContaining({
        v4ViewStates: { "project-p": { projectId: "project-p", view: "usage", selectedMilestoneId: null, selectedProblemId: null } }
      }));
      const nativeOpenActions = [...view.contentEl.querySelectorAll<HTMLButtonElement>("button")]
        .filter((button) => button.textContent?.startsWith("打开项目"));
      expect(nativeOpenActions.map((button) => button.textContent)).toEqual(["打开项目回顾", "打开项目历史"]);
      nativeOpenActions.forEach((button) => button.click());
      expect(openLinkText).toHaveBeenNthCalledWith(1, `${projectRoot}/项目回顾.md`, "", false);
      expect(openLinkText).toHaveBeenNthCalledWith(2, `${projectRoot}/项目历史.md`, "", false);
      expect(process).not.toHaveBeenCalled();
    } finally {
      await view.onClose();
    }
    expect(vault.offref).toHaveBeenCalledTimes(4);
    eventRefs.forEach((ref, index) => expect(vault.offref).toHaveBeenNthCalledWith(index + 1, ref));
  });
});
