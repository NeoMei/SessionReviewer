import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it, vi } from "vitest";
import { WorkspaceLeaf } from "obsidian";
import { ProjectRepository, type MarkdownSnapshotReady } from "../src/data/repository";
import type { VaultFile, VaultPort } from "../src/data/vault-port";
import { ProjectEvolutionView } from "../src/view/project-view";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../testdata/contracts/v4/markdown");
const fixture = (name: string): string => readFileSync(resolve(root, name), "utf8");

class V4Vault implements VaultPort {
  readonly files = new Map<string, string>();
  readonly process = vi.fn(async (_path: string, _transform: (current: string) => string): Promise<void> => {});
  private readonly listeners = new Set<(path: string) => void>();
  getMarkdownFiles(): VaultFile[] { return [...this.files.keys()].filter((path) => path.endsWith(".md")).map((path) => ({ path, basename: path.split("/").at(-1)!.replace(/\.md$/, "") })); }
  getFrontmatter(): Record<string, unknown> | undefined { return undefined; }
  async read(path: string): Promise<string> { const value = this.files.get(path); if (value === undefined) throw new Error("missing file"); return value; }
  onChange(listener: (path: string) => void): () => void { this.listeners.add(listener); return () => this.listeners.delete(listener); }
  write(path: string, value: string): void { this.files.set(path, value); for (const listener of this.listeners) listener(path); }
}

function configuredVault(): { vault: V4Vault; root: string } {
  const projectRoot = "Projects/V4";
  const vault = new V4Vault();
  vault.files.set(`${projectRoot}/项目回顾.md`, fixture("review.md"));
  vault.files.set(`${projectRoot}/项目历史.md`, fixture("history.md"));
  vault.files.set(`${projectRoot}/.session-reviewer/ledger.json`, fixture("ledger.json"));
  vault.files.set(`${projectRoot}/.session-reviewer/session-index.json`, fixture("index.json"));
  return { vault, root: projectRoot };
}

describe("v4 project repository", () => {
  it("discovers and loads the real four-file route without any write", async () => {
    const { vault, root: projectRoot } = configuredVault();
    const repository = new ProjectRepository(vault);

    const projects = await repository.discover();
    expect(projects).toEqual([{ projectId: "project-p", root: projectRoot, name: "project-p", format: "markdown-v4" }]);
    const snapshot = await repository.load(projects[0]);

    expect(snapshot.kind).toBe("markdown-v4");
    if (snapshot.kind !== "markdown-v4") throw new Error("expected v4 snapshot");
    expect(snapshot.state.kind).toBe("public_valid");
    expect(vault.process).not.toHaveBeenCalled();
  });

  it("keeps legal field and custom-shell drafts pending after full index validation", async () => {
    const { vault, root: projectRoot } = configuredVault();
    const repository = new ProjectRepository(vault);
    const project = (await repository.discover())[0];
    vault.files.set(`${projectRoot}/项目回顾.md`, `${fixture("review.md").replace("项目目标夹具", "人工编辑目标")}\n自定义附注\n`);

    const snapshot = await repository.load(project);

    expect(snapshot.kind).toBe("markdown-v4");
    if (snapshot.kind !== "markdown-v4") throw new Error("expected v4 snapshot");
    expect(snapshot.state.kind).toBe("pending_edit");
    if (snapshot.state.kind !== "pending_edit") throw new Error("expected pending edit");
    expect(snapshot.state.value.changedFields).toContainEqual({ entity: "project-overview", name: "goal" });
  });

  it("rejects an invalid index and never falls through to v3", async () => {
    const { vault, root: projectRoot } = configuredVault();
    vault.files.set(`${projectRoot}/.session-reviewer/session-index.json`, "{}");
    const repository = new ProjectRepository(vault);
    const project = (await repository.discover())[0];

    const snapshot = await repository.load(project);

    expect(snapshot.kind).toBe("markdown-v4");
    if (snapshot.kind !== "markdown-v4") throw new Error("expected v4 snapshot");
    expect(snapshot.state).toMatchObject({ kind: "invalid", code: "wire_shape_invalid" });
    expect(vault.process).not.toHaveBeenCalled();
  });

  it("keeps a missing authenticated Markdown baseline explicitly unverified", async () => {
    const { vault, root: projectRoot } = configuredVault();
    vault.files.set(`${projectRoot}/.session-reviewer/ledger.json`, readFileSync(resolve(root, "../../../../obsidian-plugin/tests/fixtures/v4/machine-ledger-v4.valid.json"), "utf8"));
    const repository = new ProjectRepository(vault);
    const project = (await repository.discover())[0];

    const snapshot = await repository.load(project);

    expect(snapshot.kind).toBe("markdown-v4");
    if (snapshot.kind !== "markdown-v4") throw new Error("expected v4 snapshot");
    expect(snapshot.state).toEqual({ kind: "unverified", reason: "baseline_missing" });
  });

  it("retains a public-valid snapshot only for the same project and labels it stale", async () => {
    const { vault, root: projectRoot } = configuredVault();
    const repository = new ProjectRepository(vault);
    const project = (await repository.discover())[0];
    const first = await repository.load(project);
    expect(first.kind).toBe("markdown-v4");
    vault.files.set(`${projectRoot}/项目历史.md`, fixture("review-truncated-field.md"));

    const stale = await repository.load(project, first as MarkdownSnapshotReady);
    const other = await repository.load({ ...project, projectId: "project-other" }, first as MarkdownSnapshotReady);

    expect(stale.kind).toBe("markdown-v4-stale");
    if (stale.kind !== "markdown-v4-stale") throw new Error("expected stale v4");
    const { renderMarkdownV4View } = await import("../src/view/presentation");
    expect(renderMarkdownV4View(stale, () => {}).textContent).toContain("markdown_structure_edit_requires_command");
    expect(other.kind).toBe("markdown-v4");
    expect(vault.process).not.toHaveBeenCalled();
  });

  it("watches review, history, ledger, and session index", async () => {
    vi.useFakeTimers();
    const { vault, root: projectRoot } = configuredVault();
    const repository = new ProjectRepository(vault);
    const project = (await repository.discover())[0];
    const refresh = vi.fn();
    const dispose = repository.watch(project, refresh);
    for (const relative of ["项目回顾.md", "项目历史.md", ".session-reviewer/ledger.json", ".session-reviewer/session-index.json"]) {
      vault.write(`${projectRoot}/${relative}`, vault.files.get(`${projectRoot}/${relative}`)!);
      await vi.advanceTimersByTimeAsync(151);
    }
    dispose();
    vi.useRealTimers();
    expect(refresh).toHaveBeenCalledTimes(4);
  });

  it("renders a read-only pending-verification view with native Markdown open actions", async () => {
    const { vault } = configuredVault();
    const repository = new ProjectRepository(vault);
    const project = (await repository.discover())[0];
    const snapshot = await repository.load(project);
    expect(snapshot.kind).toBe("markdown-v4");
    if (snapshot.kind !== "markdown-v4" || snapshot.state.kind !== "public_valid") throw new Error("expected public-valid v4");

    const { renderMarkdownV4View } = await import("../src/view/presentation");
    const opens: string[] = [];
    const view = renderMarkdownV4View(snapshot, (path) => opens.push(path), { cliUnavailable: true });

    expect(view.textContent).toContain("待私有验证");
    expect(view.textContent).toContain("只读");
    expect(view.textContent).toContain("项目目标夹具");
    expect(view.textContent).toContain("人工字段可编辑，结构仍需显式操作。");
    expect(view.textContent).toContain("验证状态：missing");
    expect(view.textContent).toContain("验证缺失原因：not_verified");
    expect(view.textContent).toContain("CLI 不可用");
    expect(view.querySelector("[data-action='edit-v4']")).toBeNull();
    view.querySelectorAll<HTMLButtonElement>("button")[0].click();
    view.querySelectorAll<HTMLButtonElement>("button")[1].click();
    expect(opens).toEqual(["Projects/V4/项目回顾.md", "Projects/V4/项目历史.md"]);
    expect(vault.process).not.toHaveBeenCalled();
  });

  it("renders populated milestone verification text and source references", async () => {
    const { vault } = configuredVault();
    const repository = new ProjectRepository(vault);
    const project = (await repository.discover())[0];
    const snapshot = await repository.load(project);
    if (snapshot.kind !== "markdown-v4" || snapshot.state.kind !== "public_valid") throw new Error("expected public-valid v4");
    const presentation = structuredClone(snapshot.state.value.presentation);
    presentation.timeline[0].closed_loop.verification = {
      state: "present",
      text: "完整回归已通过",
      missing_reason: null,
      source_turn_refs: [{ provider: "codex", session_id: "session-1", turn_unit_id: "turn-1" }]
    };
    const populated: MarkdownSnapshotReady = { ...snapshot, state: { ...snapshot.state, value: { ...snapshot.state.value, presentation } } };

    const { renderMarkdownV4View } = await import("../src/view/presentation");
    const view = renderMarkdownV4View(populated, () => {});

    expect(view.textContent).toContain("验证文本：完整回归已通过");
    expect(view.textContent).toContain("验证引用：codex/session-1#turn-1");
  });

  it("routes a v4 repository snapshot through ProjectEvolutionView without the v3 editor", async () => {
    const { vault } = configuredVault();
    const realRepository = new ProjectRepository(vault);
    const project = (await realRepository.discover())[0];
    const snapshot = await realRepository.load(project);
    const repository = {
      discover: vi.fn().mockResolvedValue([project]),
      load: vi.fn().mockResolvedValue(snapshot),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const openLinkText = vi.fn();
    const editor = { apply: vi.fn() };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as unknown as ProjectRepository, editor as never);
    Object.assign(view, { app: { workspace: { openLinkText } } });

    await view.onOpen();
    view.contentEl.querySelector<HTMLButtonElement>("button")!.click();

    expect(view.contentEl.textContent).toContain("待私有验证");
    expect(view.contentEl.textContent).toContain("CLI 不可用");
    expect(openLinkText).toHaveBeenCalledWith("Projects/V4/项目回顾.md", "", false);
    expect(editor.apply).not.toHaveBeenCalled();
    expect(vault.process).not.toHaveBeenCalled();
  });

  it("surfaces an invalid v4 reason without invoking editor or process", async () => {
    const project = { projectId: "project-p", root: "Projects/V4", name: "project-p", format: "markdown-v4" as const };
    const repository = { discover: vi.fn().mockResolvedValue([project]), load: vi.fn().mockResolvedValue({ kind: "markdown-v4", descriptor: project, state: { kind: "invalid", code: "wire_shape_invalid" }, loadedAt: 1 }), watch: vi.fn().mockReturnValue(vi.fn()) };
    const editor = { apply: vi.fn() };
    const openLinkText = vi.fn();
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as unknown as ProjectRepository, editor as never);
    Object.assign(view, { app: { workspace: { openLinkText } } });

    await view.onOpen();

    expect(view.contentEl.textContent).toContain("wire_shape_invalid");
    expect(view.contentEl.querySelectorAll("button")).toHaveLength(2);
    view.contentEl.querySelector<HTMLButtonElement>("button")!.click();
    expect(openLinkText).toHaveBeenCalledWith("Projects/V4/项目回顾.md", "", false);
    expect(editor.apply).not.toHaveBeenCalled();
  });

  it("never passes a v4 document to the v3 editor process path", async () => {
    const { vault, root: projectRoot } = configuredVault();
    vault.files.set(`${projectRoot}/项目回顾.md`, fixture("review-duplicate-field.md"));
    const repository = new ProjectRepository(vault);
    const editor = { apply: vi.fn(async () => { await vault.process("unexpected", (value: string) => { return value; }); }) };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository, editor as never);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });

    await view.onOpen();
    for (const button of view.contentEl.querySelectorAll<HTMLButtonElement>("button")) button.click();

    expect(view.contentEl.textContent).toContain("markdown_field_duplicate");
    expect(editor.apply).not.toHaveBeenCalled();
    expect(vault.process).not.toHaveBeenCalled();
  });
});
