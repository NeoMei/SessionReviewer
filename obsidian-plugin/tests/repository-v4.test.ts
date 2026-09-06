import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WorkspaceLeaf } from "obsidian";
import { ProjectRepository, type MarkdownSnapshotReady } from "../src/data/repository";
import type { VaultFile, VaultPort } from "../src/data/vault-port";
import { ProjectEvolutionView } from "../src/view/project-view";
import { CliRunner } from "../src/cli/runner";
import { syncStatusFixture } from "./fixtures/sync-status";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../testdata/contracts/v4/markdown");
const fixture = (name: string): string => readFileSync(resolve(root, name), "utf8");

class V4Vault implements VaultPort {
  readonly files = new Map<string, string>();
  readonly process = vi.fn(async (_path: string, _transform: (current: string) => string): Promise<void> => {});
  private readonly listeners = new Set<(path: string) => void>();
  getMarkdownFiles(): VaultFile[] { return [...this.files.keys()].filter((path) => path.endsWith(".md")).map((path) => ({ path, basename: path.split("/").at(-1)!.replace(/\.md$/, "") })); }
  getFrontmatter(): Record<string, unknown> | undefined { return undefined; }
  async read(path: string): Promise<string> { const value = this.files.get(path); if (value === undefined) throw Object.assign(new Error("missing file"), { code: "ENOENT" }); return value; }
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

afterEach(() => { vi.useRealTimers(); });

describe("v4 bounded Vault read diagnostics", () => {
  const documents = [
    "项目回顾.md",
    "项目历史.md",
    ".session-reviewer/ledger.json",
    ".session-reviewer/session-index.json"
  ] as const;

  it.each(documents)("identifies a missing %s without treating it as a Markdown baseline failure", async (document) => {
    const { vault, root: projectRoot } = configuredVault();
    const repository = new ProjectRepository(vault);
    const project = (await repository.discover())[0];
    vault.files.delete(`${projectRoot}/${document}`);

    const snapshot = await repository.load(project);

    expect(snapshot.kind).toBe("markdown-v4");
    if (snapshot.kind !== "markdown-v4") throw new Error("expected v4 snapshot");
    expect(snapshot.state).toEqual({ kind: "read_failed", document, category: "missing" });
  });

  it.each([
    ["EACCES", "permission-denied"],
    ["EPERM", "permission-denied"],
    ["EBUSY", "read-failed"],
    [undefined, "read-failed"]
  ] as const)("maps Vault read code %s to the bounded %s category", async (code, category) => {
    const { vault, root: projectRoot } = configuredVault();
    const repository = new ProjectRepository(vault);
    const project = (await repository.discover())[0];
    const originalRead = vault.read.bind(vault);
    vi.spyOn(vault, "read").mockImplementation(async (path) => {
      if (path === `${projectRoot}/.session-reviewer/ledger.json`) {
        throw Object.assign(new Error("hostile"), code === undefined ? {} : { code });
      }
      return originalRead(path);
    });

    const snapshot = await repository.load(project);

    expect(snapshot.kind).toBe("markdown-v4");
    if (snapshot.kind !== "markdown-v4") throw new Error("expected v4 snapshot");
    expect(snapshot.state).toEqual({ kind: "read_failed", document: ".session-reviewer/ledger.json", category });
  });

  it("never exposes hostile exception messages, absolute paths, arbitrary codes, or document contents", async () => {
    const { vault, root: projectRoot } = configuredVault();
    const repository = new ProjectRepository(vault);
    const project = (await repository.discover())[0];
    const secret = "DO-NOT-EXPOSE-CONTENT";
    const hostilePath = "/Users/private/Secret Vault/.session-reviewer/ledger.json";
    const originalRead = vault.read.bind(vault);
    vi.spyOn(vault, "read").mockImplementation(async (path) => {
      if (path === `${projectRoot}/.session-reviewer/ledger.json`) {
        throw Object.assign(new Error(`cannot read ${hostilePath}: ${secret}`), { code: "TOP_SECRET_ARBITRARY_CODE", body: secret });
      }
      return originalRead(path);
    });

    const snapshot = await repository.load(project);

    const serialized = JSON.stringify(snapshot);
    expect(serialized).not.toContain(hostilePath);
    expect(serialized).not.toContain(secret);
    expect(serialized).not.toContain("TOP_SECRET_ARBITRARY_CODE");
    expect(serialized).toContain('"category":"read-failed"');
  });

  it.each([
    {
      label: "throwing code getter",
      failure: () => Object.defineProperty(new Error("Vault read failed"), "code", { get: () => { throw new Error("getter secret"); } })
    },
    {
      label: "throwing Proxy has trap",
      failure: () => new Proxy(new Error("Vault read failed"), { has: () => { throw new Error("has secret"); } })
    },
    {
      label: "throwing Proxy get trap",
      failure: () => new Proxy(Object.assign(new Error("Vault read failed"), { code: "EACCES" }), { get: () => { throw new Error("get secret"); } })
    }
  ])("defaults a $label to the bounded read-failed category", async ({ failure }) => {
    const { vault, root: projectRoot } = configuredVault();
    const repository = new ProjectRepository(vault);
    const project = (await repository.discover())[0];
    const originalRead = vault.read.bind(vault);
    vi.spyOn(vault, "read").mockImplementation(async (path) => {
      if (path === `${projectRoot}/.session-reviewer/ledger.json`) throw failure();
      return originalRead(path);
    });

    await expect(repository.load(project)).resolves.toMatchObject({
      kind: "markdown-v4",
      state: { kind: "read_failed", document: ".session-reviewer/ledger.json", category: "read-failed" }
    });
  });

  it("shows a bounded first-load read diagnostic and remains read-only", async () => {
    const { vault, root: projectRoot } = configuredVault();
    const repository = new ProjectRepository(vault);
    const project = (await repository.discover())[0];
    vault.files.delete(`${projectRoot}/项目历史.md`);
    const snapshot = await repository.load(project);
    if (snapshot.kind !== "markdown-v4") throw new Error("expected v4 snapshot");
    const { renderMarkdownV4View } = await import("../src/view/presentation");

    const view = renderMarkdownV4View(snapshot, () => {});

    expect(view.textContent).toContain("项目历史.md");
    expect(view.textContent).toContain("文件缺失");
    expect(view.textContent).toContain("只读");
    expect(view.textContent).toContain("公开文件校验不等于私有接受证明");
    expect(view.querySelector("[data-action='edit-v4']")).toBeNull();
  });

  it("preserves a same-project public snapshot and shows the bounded read diagnostic as stale", async () => {
    const { vault, root: projectRoot } = configuredVault();
    const repository = new ProjectRepository(vault);
    const project = (await repository.discover())[0];
    const first = await repository.load(project);
    if (first.kind !== "markdown-v4" || first.state.kind !== "public_valid") throw new Error("expected public-valid v4");
    vault.files.delete(`${projectRoot}/.session-reviewer/session-index.json`);

    const stale = await repository.load(project, first as MarkdownSnapshotReady);

    expect(stale.kind).toBe("markdown-v4-stale");
    if (stale.kind !== "markdown-v4-stale") throw new Error("expected stale v4");
    const { renderMarkdownV4View } = await import("../src/view/presentation");
    const view = renderMarkdownV4View(stale, () => {});
    expect(view.textContent).toContain("项目目标夹具");
    expect(view.textContent).toContain(".session-reviewer/session-index.json");
    expect(view.textContent).toContain("文件缺失");
    expect(view.textContent).toContain("已过期 · 只读");
  });

  it("renders an accurate stale read banner through the full project view", async () => {
    vi.useFakeTimers();
    const { vault, root: projectRoot } = configuredVault();
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), new ProjectRepository(vault));
    await view.onOpen();
    vault.files.delete(`${projectRoot}/.session-reviewer/session-index.json`);
    vault.write(`${projectRoot}/项目回顾.md`, fixture("review.md"));

    await vi.advanceTimersByTimeAsync(151);

    expect(view.contentEl.textContent).toContain("正在显示上次可信内容");
    expect(view.contentEl.textContent).toContain("当前文件无法读取或验证");
    expect(view.contentEl.textContent).toContain(".session-reviewer/session-index.json：文件缺失");
    expect(view.contentEl.textContent).toContain("已过期 · 只读");
    expect(view.contentEl.textContent).not.toContain("当前文件身份、引用或 revision 不一致");
    await view.onClose();
  });

  it("keeps parser validation failures distinct from Vault read failures", async () => {
    const { vault, root: projectRoot } = configuredVault();
    vault.files.set(`${projectRoot}/.session-reviewer/session-index.json`, "{}");
    const repository = new ProjectRepository(vault);
    const project = (await repository.discover())[0];

    const snapshot = await repository.load(project);

    expect(snapshot.kind).toBe("markdown-v4");
    if (snapshot.kind !== "markdown-v4") throw new Error("expected v4 snapshot");
    expect(snapshot.state).toEqual({ kind: "invalid", code: "wire_shape_invalid" });
  });
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

describe("v4 live refresh lifecycle", () => {
  it("discards an older failed status after a newer clean refresh", async () => {
    vi.useFakeTimers();
    const { vault, root: projectRoot } = configuredVault();
    const repository = new ProjectRepository(vault);
    let calls = 0;
    let finishOld!: () => void;
    const runner = new CliRunner("/bin/session-reviewer", (_file, args, _options, callback) => {
      if (args[0] !== "sync") return callback(new Error("no scan"), "", "");
      calls++;
      if (calls === 2) { finishOld = () => callback(Object.assign(new Error("transient"), { code: 1 }), "", ""); return; }
      callback(null, JSON.stringify(syncStatusFixture("project-p")), "");
    });
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository, undefined, runner);
    await view.onOpen();
    vault.write(`${projectRoot}/项目回顾.md`, fixture("review.md"));
    await vi.advanceTimersByTimeAsync(151);
    vault.write(`${projectRoot}/项目历史.md`, fixture("history.md"));
    await vi.advanceTimersByTimeAsync(151);
    finishOld();
    await vi.advanceTimersByTimeAsync(0);
    expect(view.contentEl.textContent).not.toContain("同步状态验证失败");
    expect(view.contentEl.textContent).toContain("待私有验证");
    await view.onClose();
  });

  it.each(["newer snapshot", "project switch", "close"])("discards a delayed old snapshot after %s", async (event) => {
    vi.useFakeTimers();
    const { vault, root: projectRoot } = configuredVault();
    const repository = new ProjectRepository(vault);
    const project = (await repository.discover())[0];
    if (event === "project switch") vi.spyOn(repository, "discover").mockResolvedValue([project, { ...project, projectId: "project-q", root: "Projects/Q", name: "Q" }]);
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository);
    await view.onOpen();
    vault.files.set(`${projectRoot}/项目回顾.md`, fixture("review.md").replace("项目目标夹具", "old delayed goal"));
    const oldSnapshot = await repository.load(project);
    const old = deferred<typeof oldSnapshot>();
    vi.spyOn(repository, "load").mockImplementationOnce(() => old.promise);
    vault.write(`${projectRoot}/项目回顾.md`, fixture("review.md").replace("项目目标夹具", "newer goal"));
    await vi.advanceTimersByTimeAsync(151);
    if (event === "newer snapshot") {
      vault.write(`${projectRoot}/项目历史.md`, fixture("history.md"));
      await vi.advanceTimersByTimeAsync(151);
    } else if (event === "project switch") {
      const picker = view.contentEl.querySelector<HTMLSelectElement>("select")!;
      picker.value = "project-q";
      picker.dispatchEvent(new Event("change"));
      await vi.advanceTimersByTimeAsync(0);
    } else await view.onClose();
    old.resolve(oldSnapshot);
    await vi.advanceTimersByTimeAsync(0);
    expect(view.contentEl.textContent).not.toContain("old delayed goal");
    if (event === "newer snapshot") expect(view.contentEl.textContent).toContain("newer goal");
    if (event === "project switch") expect(view.contentEl.querySelector<HTMLSelectElement>("select")!.value).toBe("project-q");
    if (event === "close") expect(view.contentEl.childElementCount).toBe(0);
    await view.onClose();
  });

  it("settles a transient private refusal without another public event and keeps retry bounded", async () => {
    vi.useFakeTimers();
    const { vault } = configuredVault();
    const commands: string[] = [];
    let failing = true;
    const runner = new CliRunner("/bin/session-reviewer", (_file, args, _options, callback) => {
      commands.push(args.join(" "));
      if (failing) callback(Object.assign(new Error("publication still settling"), { code: 1 }), "", "");
      else callback(null, JSON.stringify(syncStatusFixture("project-p")), "");
    });
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), new ProjectRepository(vault), undefined, runner);
    await view.onOpen();
    expect(view.contentEl.textContent).toContain("同步状态验证失败");
    failing = false;
    await vi.advanceTimersByTimeAsync(6000);
    expect(view.contentEl.textContent).not.toContain("同步状态验证失败");
    expect(view.contentEl.textContent).toContain("待私有验证");
    const refresh = view.contentEl.querySelector<HTMLButtonElement>("[data-action='refresh-v4-status']");
    expect(refresh).not.toBeNull();
    failing = true;
    refresh!.click();
    await vi.advanceTimersByTimeAsync(20000);
    expect(view.contentEl.textContent).toContain("同步状态验证失败");
    const settledCalls = commands.length;
    await vi.advanceTimersByTimeAsync(20000);
    expect(commands).toHaveLength(settledCalls);
    failing = false;
    view.contentEl.querySelector<HTMLButtonElement>("[data-action='refresh-v4-status']")!.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(view.contentEl.textContent).not.toContain("同步状态验证失败");
    expect(view.contentEl.textContent).toContain("待私有验证");
    expect(commands).toEqual(commands.map(() => "sync status --json --project-id project-p"));
    expect(view.contentEl.querySelector("[data-resolution-action], [data-status-action], [data-action='edit-v4']")).toBeNull();
    expect(vault.process).not.toHaveBeenCalled();
    await view.onClose();
  });

  it("cancels settling retries when the view closes", async () => {
    vi.useFakeTimers();
    const { vault } = configuredVault();
    const commands: string[] = [];
    const runner = new CliRunner("/bin/session-reviewer", (_file, args, _options, callback) => {
      commands.push(args.join(" "));
      callback(Object.assign(new Error("persistent failure"), { code: 1 }), "", "");
    });
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), new ProjectRepository(vault), undefined, runner);
    await view.onOpen();
    expect(view.contentEl.textContent).toContain("同步状态验证失败");
    await view.onClose();
    const closedCalls = commands.length;
    await vi.advanceTimersByTimeAsync(20000);
    expect(commands).toHaveLength(closedCalls);
    expect(view.contentEl.childElementCount).toBe(0);
  });
});

describe("v4 project repository", () => {
  it.each([
    { label: "clean", patch: {}, code: undefined, text: "待私有验证" },
    { label: "pending Project edit", patch: { in_sync: 0, machine_state: "pending", derived_state: "pending" }, code: undefined, text: "等待同步" },
    { label: "semantic conflict", patch: { in_sync: 0, conflicted: 1, open_conflicts: ["section:session-reviewer/v4/project-overview/goal"], machine_state: "blocked", derived_state: "deferred" }, code: undefined, text: "两边修改了同一内容" },
    { label: "legacy IDs on v4", patch: { hidden_conflict_ids: ["conflict-old"], conflicted: 1 }, code: undefined, text: "两边修改了同一内容" },
    { label: "blocked status", patch: { machine_state: "blocked" }, code: undefined, text: "同步状态验证失败" },
    { label: "command failed", patch: {}, code: 1, text: "同步状态验证失败" },
    { label: "runtime disappeared", patch: {}, code: "ENOENT", text: "CLI 不可用" }
  ])("interprets $label without promoting public validity or enabling legacy actions", async ({ patch, code, text }) => {
    const { vault } = configuredVault();
    const repository = new ProjectRepository(vault);
    const runner = new CliRunner("/bin/session-reviewer", (_file, args, _options, callback) => {
      if (args[0] !== "sync") return callback(new Error("scan not available"), "", "");
      callback(code === undefined ? null : Object.assign(new Error("/private/secret"), { code }), JSON.stringify(syncStatusFixture("project-p", patch)), "/private/secret");
    });
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository, undefined, runner);
    await view.onOpen();
    expect(view.contentEl.textContent).toContain(text);
    expect(view.contentEl.textContent).toContain("待私有验证");
    expect(view.contentEl.textContent).toContain("只读");
    expect(view.contentEl.textContent).not.toContain("/private/secret");
    if (code !== "ENOENT") expect(view.contentEl.textContent).not.toContain("CLI 不可用");
    expect(view.contentEl.querySelector("[data-resolution-action], [data-status-action], [data-action='edit-v4']")).toBeNull();
    expect(view.contentEl.querySelectorAll("button:not([data-action='refresh-v4-status'])")).toHaveLength(2);
    const snapshot = await repository.load((await repository.discover())[0]);
    expect(snapshot.kind === "markdown-v4" && snapshot.state.kind).toBe("public_valid");
    expect(vault.process).not.toHaveBeenCalled();
    await view.onClose();
  });

  it("retains a local pending draft when status validation fails", async () => {
    const { vault, root: projectRoot } = configuredVault();
    vault.files.set(`${projectRoot}/项目回顾.md`, fixture("review.md").replace("项目目标夹具", "pending draft stays visible"));
    const repository = new ProjectRepository(vault);
    const runner = new CliRunner("/bin/session-reviewer", (_file, _args, _options, callback) => callback(Object.assign(new Error("sensitive"), { code: 1 }), "", "sensitive"));
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository, undefined, runner);
    await view.onOpen();
    expect(view.contentEl.textContent).toContain("pending draft stays visible");
    expect(view.contentEl.textContent).toContain("未同步修改");
    expect(view.contentEl.textContent).toContain("同步状态验证失败");
    expect(view.contentEl.textContent).not.toContain("CLI 不可用");
    expect(vault.process).not.toHaveBeenCalled();
    await view.onClose();
  });
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
    expect(view.contentEl.querySelectorAll("button:not([data-action='refresh-v4-status'])")).toHaveLength(2);
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
