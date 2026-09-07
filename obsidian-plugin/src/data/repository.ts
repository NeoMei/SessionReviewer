import type { BrowserModel, MachineLedger } from "../contracts/review-v3";
import { sha256Text } from "./hash";
import { parseLedger } from "./ledger";
import { parseHistory, parseReview } from "./markdown";
import { parseReviewPresentationV4 } from "./contracts-v4";
import { readMarkdownIdentityV4 } from "./markdown-v4";
import { loadMarkdownSnapshot, vaultReadFailure, type MarkdownDocumentPath, type MarkdownSnapshot } from "./repository-v4";
import type { VaultPort } from "./vault-port";
import type { ConflictCandidate } from "../view/conflict-modal";

export interface ProjectDescriptor {
  projectId: string;
  root: string;
  name: string;
  format?: "markdown-v4" | "legacy-v4-json";
}

export interface Diagnostic {
  code:
    | "review_parse_failed"
    | "history_parse_failed"
    | "machine_ledger_modified"
    | "stale_snapshot"
    | "sync_not_run"
    | "migration_required"
    | "content_conflict"
    | "sync_status_failed"
    | "markdown_sync_pending"
    | "cli_unavailable";
  message: string;
}

export interface SnapshotReady {
  kind: "ready";
  model: BrowserModel;
  machine: MachineLedger;
  loadedAt: number;
}

export interface MarkdownSnapshotReady {
  kind: "markdown-v4";
  descriptor: ProjectDescriptor;
  state: Extract<MarkdownSnapshot, { kind: "public_valid" }>;
  loadedAt: number;
}

export type LastValidSnapshot = SnapshotReady | MarkdownSnapshotReady;

export type Snapshot =
  | SnapshotReady
  | { kind: "pending_edit"; model: BrowserModel; machine: MachineLedger; diagnostic: Diagnostic }
  | { kind: "migration_required"; descriptor: ProjectDescriptor; diagnostic: Diagnostic }
  | { kind: "stale"; lastValid: SnapshotReady; diagnostic: Diagnostic }
  | { kind: "markdown-v4"; descriptor: ProjectDescriptor; state: MarkdownSnapshot; loadedAt: number }
  | { kind: "markdown-v4-stale"; lastValid: MarkdownSnapshotReady; state: Extract<MarkdownSnapshot, { kind: "invalid" | "unverified" | "read_failed" }>; diagnostic: Diagnostic }
  | { kind: "empty"; diagnostic?: Diagnostic };

interface PendingWrite {
  path: string;
  sha256: string;
}

export class ProjectRepository {
  private pendingWrite?: PendingWrite;

  constructor(private readonly vault: VaultPort) {}

  async discover(): Promise<ProjectDescriptor[]> {
    const descriptors: ProjectDescriptor[] = [];
    const markdownFiles = this.vault.getMarkdownFiles();
    const markdownPaths = new Set(markdownFiles.map((file) => file.path));
    for (const file of markdownFiles) {
      if (file.basename !== "项目回顾") continue;
      const root = parent(file.path);
      const historyPath = `${root}/项目历史.md`;
      if (!markdownPaths.has(historyPath)) continue;
      let source: string;
      try {
        source = await this.vault.read(file.path);
      } catch {
        // Inventory can outlive a file or its permissions. Isolate this candidate.
        continue;
      }
      try {
        const identity = readMarkdownIdentityV4(source);
        if (identity.document !== "review") continue;
        descriptors.push({ projectId: identity.projectId, root, name: projectFolderName(root), format: "markdown-v4" });
        continue;
      } catch {
        // Continue to legacy formats only when the source is not a known v4 Markdown document.
      }
      if (/^---\r?\n[\s\S]*?^schema_version:\s*4\s*$[\s\S]*?^document_format:\s*review-markdown-v1\s*$/m.test(source)) {
        const projectId = /^project_id:\s*([^\s]+)\s*$/m.exec(source)?.[1];
        if (projectId && validProjectId(projectId)) descriptors.push({ projectId, root, name: projectFolderName(root), format: "markdown-v4" });
        continue;
      }
      try {
        const legacy = parseReviewPresentationV4(source);
        descriptors.push({ projectId: legacy.project_id, root, name: projectFolderName(root), format: "legacy-v4-json" });
        continue;
      } catch {
        // Try v3 below.
      }
      try {
        const review = parseReview(source);
        if (!validProjectId(review.projectId)) continue;
        descriptors.push({ projectId: review.projectId, root, name: review.name });
      } catch {
        // Discovery never upgrades malformed candidates into selectable projects.
      }
    }
    return descriptors.sort((left, right) => left.name.localeCompare(right.name, "zh-CN") || left.projectId.localeCompare(right.projectId));
  }

  async load(project: ProjectDescriptor, previous?: LastValidSnapshot): Promise<Snapshot> {
    if (project.format === "legacy-v4-json") {
      return { kind: "migration_required", descriptor: project, diagnostic: { code: "migration_required", message: "此项目是旧 v4 JSON，需要先运行显式迁移预览。" } };
    }
    if (project.format === "markdown-v4") return this.loadV4(project, previous);
    const reviewPath = `${project.root}/项目回顾.md`;
    const historyPath = `${project.root}/项目历史.md`;
    const ledgerPath = `${project.root}/.session-reviewer/ledger.json`;
    let reviewText: string;
    let historyText: string;
    try {
      reviewText = await this.vault.read(reviewPath);
      parseReview(reviewText);
    } catch (error) {
      return invalid(previous?.kind === "ready" ? previous : undefined, "review_parse_failed", `项目回顾无法解析：${message(error)}`);
    }
    try {
      historyText = await this.vault.read(historyPath);
      parseHistory(historyText);
    } catch (error) {
      return invalid(previous?.kind === "ready" ? previous : undefined, "history_parse_failed", `项目历史无法解析：${message(error)}`);
    }
    const review = parseReview(reviewText);
    const history = parseHistory(historyText);
    let machine: MachineLedger;
    try {
      machine = parseLedger(await this.vault.read(ledgerPath));
    } catch (error) {
      return invalid(previous?.kind === "ready" ? previous : undefined, "machine_ledger_modified", `机器账本无法验证：${message(error)}`);
    }
    if (review.projectId !== project.projectId || history.projectId !== project.projectId || machine.projectId !== project.projectId) {
      return invalid(previous?.kind === "ready" ? previous : undefined, "stale_snapshot", "三份文件的 project_id 不一致。");
    }
    if (review.revision !== history.revision) return invalid(previous?.kind === "ready" ? previous : undefined, "stale_snapshot", "项目回顾与项目历史的 revision 不一致。");
    const decisions = new Set(review.decisions.map((decision) => decision.id));
    for (const event of history.events) {
      for (const id of event.decisionIds) if (!decisions.has(id)) return invalid(previous?.kind === "ready" ? previous : undefined, "stale_snapshot", `历史节点 ${event.id} 引用了缺失的决策 ${id}。`);
    }
    const reviewSha256 = sha256Text(reviewText);
    const historySha256 = sha256Text(historyText);
    const model: BrowserModel = {
      review,
      events: history.events,
      accounting: machine.accounting,
      sessions: machine.sessions,
      ...(machine.lastSuccessfulSync === undefined ? {} : { lastSuccessfulSync: machine.lastSuccessfulSync }),
      source: { reviewPath, historyPath, ledgerPath, reviewText, historyText, reviewSha256, historySha256 },
      historyFields: history.fields
    };
    if (machine.reviewSha256 !== reviewSha256 || machine.historySha256 !== historySha256 || machine.acceptedRevision !== review.revision) {
      return { kind: "pending_edit", model, machine, diagnostic: { code: "sync_not_run", message: "人类可读文档已更新，等待同步到代码目录。" } };
    }
    return { kind: "ready", model, machine, loadedAt: Date.now() };
  }

  private async loadV4(project: ProjectDescriptor, previous?: LastValidSnapshot): Promise<Snapshot> {
    const documents = {
      review: "项目回顾.md", history: "项目历史.md",
      ledger: ".session-reviewer/ledger.json", index: ".session-reviewer/session-index.json"
    } as const satisfies Record<string, MarkdownDocumentPath>;
    let state: MarkdownSnapshot;
    try {
      state = loadMarkdownSnapshot({
        review: await readV4Document(this.vault, project.root, documents.review),
        history: await readV4Document(this.vault, project.root, documents.history),
        ledger: await readV4Document(this.vault, project.root, documents.ledger),
        index: await readV4Document(this.vault, project.root, documents.index)
      });
    } catch (error) {
      if (!(error instanceof VaultDocumentReadError)) throw error;
      state = vaultReadFailure(error.document, error.cause);
    }
    if ((state.kind === "invalid" || state.kind === "unverified" || state.kind === "read_failed") && previous?.kind === "markdown-v4" && previous.descriptor.projectId === project.projectId && previous.descriptor.root === project.root) {
      return { kind: "markdown-v4-stale", lastValid: previous, state, diagnostic: { code: "stale_snapshot", message: "当前四文件快照无法验证，正在显示此项目上一次公开校验快照（已过期、只读）。" } };
    }
    return { kind: "markdown-v4", descriptor: project, state, loadedAt: Date.now() };
  }

  watch(project: ProjectDescriptor, refresh: () => void): () => void {
    const targets = new Set([
      `${project.root}/项目回顾.md`,
      `${project.root}/项目历史.md`,
      `${project.root}/.session-reviewer/ledger.json`,
      `${project.root}/.session-reviewer/session-index.json`
    ]);
    let timer: number | undefined;
    let closed = false;
    const dispose = this.vault.onChange((path) => {
      const normalizedPath = normalize(path);
      if (closed || !targets.has(normalizedPath)) return;
      if (this.pendingWrite?.path === normalizedPath) {
        void this.vault.read(normalizedPath).then((body) => {
          if (this.pendingWrite?.path === normalizedPath && this.pendingWrite.sha256 === sha256Text(body)) this.pendingWrite = undefined;
          else schedule();
        }).catch(schedule);
        return;
      }
      schedule();
    });
    const schedule = (): void => {
      if (timer !== undefined) window.clearTimeout(timer);
      timer = window.setTimeout(() => { timer = undefined; if (!closed) refresh(); }, 150);
    };
    return () => {
      closed = true;
      if (timer !== undefined) window.clearTimeout(timer);
      dispose();
    };
  }

  ignoreSelfWrite(path: string, sha256: string): void {
    this.pendingWrite = { path: normalize(path), sha256 };
  }

  async loadConflict(project: ProjectDescriptor, conflictId: string): Promise<ConflictCandidate> {
    if (!/^conflict-[a-z0-9][a-z0-9._-]{0,191}$/.test(conflictId)) throw new Error("invalid conflict ID");
    const body = await this.vault.read(`${project.root}/.session-reviewer/conflicts/${conflictId}.json`);
    if (Buffer.byteLength(body, "utf8") > 16 << 20) throw new Error("conflict record exceeds size limit");
    const value = JSON.parse(body) as Record<string, unknown>;
    if (value.version !== 1 || value.id !== conflictId || value.project_id !== project.projectId || value.resolution_status !== "open") throw new Error("conflict record identity is invalid");
    const base = requiredConflictText(value, "base");
    const projectText = requiredConflictText(value, "project");
    const obsidian = requiredConflictText(value, "vault");
    if (value.base_hash !== sha256Text(base) || value.project_hash !== sha256Text(projectText) || value.vault_hash !== sha256Text(obsidian)) throw new Error("conflict candidate hashes do not match");
    const entityId = value.entity_id;
    const unit = typeof entityId === "string" || typeof entityId === "number" ? String(entityId) : "未知语义单元";
    return { id: conflictId, unit, base, project: projectText, obsidian };
  }
}

class VaultDocumentReadError extends Error {
  constructor(readonly document: MarkdownDocumentPath, readonly cause: unknown) {
    super("Vault document read failed");
  }
}

async function readV4Document(vault: VaultPort, root: string, document: MarkdownDocumentPath): Promise<string> {
  try {
    return await vault.read(`${root}/${document}`);
  } catch (error) {
    throw new VaultDocumentReadError(document, error);
  }
}

function invalid(previous: SnapshotReady | undefined, code: Diagnostic["code"], messageText: string): Snapshot {
  const diagnostic = { code, message: messageText };
  return previous ? { kind: "stale", lastValid: previous, diagnostic } : { kind: "empty", diagnostic };
}

function parent(path: string): string {
  const normalized = normalize(path);
  const index = normalized.lastIndexOf("/");
  return index < 0 ? "" : normalized.slice(0, index);
}

function projectFolderName(root: string): string {
  const normalized = normalize(root);
  const folders = normalized.split("/").filter(Boolean);
  const leaf = folders.at(-1) ?? normalized;
  if (leaf !== "Session Review" || folders.length < 2) return leaf;
  return folders.at(-2)!.replace(/--[0-9a-f]{8}$/, "");
}

function normalize(path: string): string {
  return path.replaceAll("\\", "/").replace(/^\/+|\/+$/g, "");
}

function validProjectId(value: string): boolean {
  return /^project-[a-z0-9][a-z0-9._-]{0,127}$/.test(value);
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function requiredConflictText(value: Record<string, unknown>, key: string): string {
  const candidate = value[key];
  if (typeof candidate !== "string") throw new Error(`conflict ${key} candidate is invalid`);
  return candidate;
}
