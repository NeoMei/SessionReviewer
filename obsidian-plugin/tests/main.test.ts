import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { WorkspaceLeaf } from "obsidian";
import { describe, expect, it, vi } from "vitest";
import { discoverRuntime, type RuntimeDiscoveryOptions } from "../src/cli/discovery";
import SessionReviewerPlugin from "../src/main";
import type { ProjectEvolutionView } from "../src/view/project-view";

const fixtureRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../../testdata/contracts/v4/markdown");
const fixture = (name: string): string => readFileSync(resolve(fixtureRoot, name), "utf8");

describe("plugin lifecycle", () => {
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
    expect(saveData).toHaveBeenLastCalledWith({ viewState: expect.anything() as never });
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

    Object.assign(plugin, {
      loadData: vi.fn().mockResolvedValue({ cliPath: "/stale/session-reviewer" }),
      saveData: vi.fn(),
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
