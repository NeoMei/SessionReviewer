import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { sha256Text } from "../src/data/hash";
import { ProjectRepository, type SnapshotReady } from "../src/data/repository";
import type { VaultFile, VaultPort } from "../src/data/vault-port";

const fixture = (name: string): Promise<string> =>
  readFile(resolve(dirname(fileURLToPath(import.meta.url)), "../../testdata/review-v3", name), "utf8");

class FakeVault implements VaultPort {
  readonly files = new Map<string, string>();
  readonly frontmatter = new Map<string, Record<string, unknown>>();
  private readonly listeners = new Set<(path: string) => void>();

  getMarkdownFiles(): VaultFile[] {
    return [...this.files.keys()].filter((path) => path.endsWith(".md")).map((path) => ({ path, basename: path.split("/").at(-1)?.replace(/\.md$/, "") ?? "" }));
  }
  getFrontmatter(path: string): Record<string, unknown> | undefined { return this.frontmatter.get(path); }
  async read(path: string): Promise<string> {
    const value = this.files.get(path);
    if (value === undefined) throw new Error("missing file");
    return value;
  }
  async process(path: string, transform: (current: string) => string): Promise<void> { this.write(path, transform(await this.read(path))); }
  onChange(listener: (path: string) => void): () => void { this.listeners.add(listener); return () => this.listeners.delete(listener); }
  write(path: string, value: string): void { this.files.set(path, value); for (const listener of this.listeners) listener(path); }
}

async function configuredVault(): Promise<{ vault: FakeVault; root: string }> {
  const root = "Projects/A/Session Review";
  const review = await fixture("项目回顾.valid.md");
  const history = await fixture("项目历史.valid.md");
  const machine = JSON.parse(await fixture("ledger.valid.json")) as Record<string, unknown>;
  const replaceProject = (value: unknown): void => {
    if (Array.isArray(value)) for (const item of value) replaceProject(item);
    else if (typeof value === "object" && value !== null) for (const [key, item] of Object.entries(value)) {
      if (key === "project_id") (value as Record<string, unknown>)[key] = "project-0123456789abcdef";
      else replaceProject(item);
    }
  };
  replaceProject(machine);
  machine.accepted_revision = 1;
  machine.review_sha256 = sha256Text(review);
  machine.history_sha256 = sha256Text(history);
  const vault = new FakeVault();
  vault.write(`${root}/项目回顾.md`, review);
  vault.write(`${root}/项目历史.md`, history);
  vault.write(`${root}/.session-reviewer/ledger.json`, JSON.stringify(machine));
  vault.frontmatter.set(`${root}/项目回顾.md`, {
    entity_type: "project_review", schema_version: 2, project_id: "project-0123456789abcdef"
  });
  return { vault, root };
}

describe("project repository", () => {
  it("discovers valid project files before the metadata cache is warm", async () => {
    const { vault, root } = await configuredVault();
    vault.frontmatter.clear();

    const projects = await new ProjectRepository(vault).discover();

    expect(projects).toEqual([{ projectId: "project-0123456789abcdef", root, name: "SessionReviewer v2" }]);
  });

  it("discovers project_review files and keeps the last valid snapshot after corruption", async () => {
    const { vault, root } = await configuredVault();
    const repo = new ProjectRepository(vault);
    const projects = await repo.discover();
    expect(projects).toEqual([{ projectId: "project-0123456789abcdef", root, name: "SessionReviewer v2" }]);
    const first = await repo.load(projects[0]);
    expect(first.kind).toBe("ready");
    vault.write(`${root}/项目历史.md`, "broken");
    const second = await repo.load(projects[0], first as SnapshotReady);
    expect(second.kind).toBe("stale");
    if (second.kind !== "stale") throw new Error("expected stale snapshot");
    expect(second.lastValid).toEqual(first);
    expect(second.diagnostic.code).toBe("history_parse_failed");
  });

  it("shows valid human edits immediately but labels machine accounting pending", async () => {
    const { vault, root } = await configuredVault();
    const repo = new ProjectRepository(vault);
    const project = (await repo.discover())[0];
    vault.write(`${root}/项目回顾.md`, (await vault.read(`${root}/项目回顾.md`)).replace("正在实现无损 Markdown codec。", "页面中的新状态。"));
    const snapshot = await repo.load(project);
    expect(snapshot.kind).toBe("pending_edit");
    if (snapshot.kind !== "pending_edit") throw new Error("expected pending edit");
    expect(snapshot.model.review.status).toBe("页面中的新状态。");
    expect(snapshot.diagnostic.code).toBe("sync_not_run");
    expect(snapshot.machine.accounting.totalTokens).toBe(350);
  });
});
