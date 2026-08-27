import { execFile as nodeExecFile } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

const PROJECT_ID = /^project-[a-z0-9][a-z0-9._-]{0,127}$/;
const CONFLICT_ID = /^conflict-[a-z0-9][a-z0-9._-]{0,191}$/;

interface ExecOptions {
  shell: false;
  windowsHide: true;
  timeout: number;
  maxBuffer: number;
  encoding: "utf8";
}

type ExecCallback = (error: Error | null, stdout: string, stderr: string) => void;
export type ExecFileLike = (file: string, args: readonly string[], options: ExecOptions, callback: ExecCallback) => unknown;

export interface VerifiedExecutable {
  version: string;
  reviewSchemaVersion: 2;
}

export type AllowedAction =
  | { kind: "status"; projectId: string }
  | { kind: "migrationDryRun"; projectId: string }
  | { kind: "resolve"; projectId: string; conflictId: string; action: "accept_project" | "accept_obsidian" }
  | { kind: "manualMerge"; projectId: string; conflictId: string; file: string }
  | { kind: "repairMachineLedger"; projectId: string };

export class CliRunner {
  constructor(
    readonly executable: string,
    private readonly execFile: ExecFileLike = nodeExecFile as unknown as ExecFileLike
  ) {
    if (!absoluteExecutable(executable)) throw new Error("CLI executable path must be absolute");
  }

  async verifyExecutable(): Promise<VerifiedExecutable> {
    const { stdout } = await this.run(["version", "--json"]);
    const value = parseJson(stdout) as Record<string, unknown>;
    if (typeof value.version !== "string" || !/^\d+\.\d+\.\d+$/.test(value.version)) throw new Error("CLI version is not semantic");
    if (value.review_schema_version !== 2) throw new Error("CLI review schema version is incompatible");
    return { version: value.version, reviewSchemaVersion: 2 };
  }

  async status(projectId: string): Promise<Record<string, unknown>> {
    validateProject(projectId);
    const { stdout } = await this.run(["sync", "status", "--json", "--project-id", projectId]);
    return parseJson(stdout) as Record<string, unknown>;
  }

  async migrationDryRun(projectId: string): Promise<string> {
    validateProject(projectId);
    return (await this.run(["sync", "--dry-run", "--project-id", projectId])).stdout;
  }

  async resolve(projectId: string, conflictId: string, action: "accept_project" | "accept_obsidian"): Promise<string> {
    validateProject(projectId);
    validateConflict(conflictId);
    await this.requireLiveConflict(projectId, conflictId);
    return (await this.run(["sync", "resolve", "--conflict", conflictId, "--action", action, "--project-id", projectId])).stdout;
  }

  async repairMachineLedger(projectId: string): Promise<string> {
    validateProject(projectId);
    return (await this.run(["sync", "repair-machine-ledger", "--project-id", projectId])).stdout;
  }

  async manualMerge(projectId: string, conflictId: string, content: string): Promise<string> {
    validateProject(projectId);
    validateConflict(conflictId);
    await this.requireLiveConflict(projectId, conflictId);
    const directory = await mkdtemp(join(tmpdir(), "session-reviewer-merge-"));
    const file = join(directory, "manual-merge.md");
    try {
      await writeFile(file, content, { encoding: "utf8", mode: 0o600, flag: "wx" });
      return (await this.run(["sync", "resolve", "--conflict", conflictId, "--action", "manual_merge", "--file", file, "--project-id", projectId])).stdout;
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  }

  async run(args: readonly string[]): Promise<{ stdout: string; stderr: string }> {
    if (!allowedArgs(args)) throw new Error("command is not allowed");
    return new Promise((resolve, reject) => {
      this.execFile(this.executable, args, { shell: false, windowsHide: true, timeout: 10_000, maxBuffer: 1 << 20, encoding: "utf8" }, (error, stdout, stderr) => {
        if (error) reject(new Error(`SessionReviewer CLI failed: ${stderr.trim() || error.message}`));
        else resolve({ stdout, stderr });
      });
    });
  }

  private async requireLiveConflict(projectId: string, conflictId: string): Promise<void> {
    const status = await this.status(projectId);
    const ids = status.hidden_conflict_ids ?? status.open_conflicts;
    if (Array.isArray(ids) && !ids.includes(conflictId)) throw new Error("stale conflict: refresh before resolving");
  }
}

function allowedArgs(args: readonly string[]): boolean {
  if (args.length === 2 && args[0] === "version" && args[1] === "--json") return true;
  if (args.length === 5 && args[0] === "sync" && args[1] === "status" && args[2] === "--json" && args[3] === "--project-id") return PROJECT_ID.test(args[4] ?? "");
  if (args.length === 4 && args[0] === "sync" && args[1] === "--dry-run" && args[2] === "--project-id") return PROJECT_ID.test(args[3] ?? "");
  if (args.length === 4 && args[0] === "sync" && args[1] === "repair-machine-ledger" && args[2] === "--project-id") return PROJECT_ID.test(args[3] ?? "");
  if (args.length === 8 && args[0] === "sync" && args[1] === "resolve" && args[2] === "--conflict" && CONFLICT_ID.test(args[3] ?? "") && args[4] === "--action" && (args[5] === "accept_project" || args[5] === "accept_obsidian") && args[6] === "--project-id") return PROJECT_ID.test(args[7] ?? "");
  if (args.length === 10 && args[0] === "sync" && args[1] === "resolve" && args[2] === "--conflict" && CONFLICT_ID.test(args[3] ?? "") && args[4] === "--action" && args[5] === "manual_merge" && args[6] === "--file" && args[8] === "--project-id") return Boolean(args[7]) && PROJECT_ID.test(args[9] ?? "");
  return false;
}

function validateProject(value: string): void {
  if (!PROJECT_ID.test(value)) throw new Error("invalid project ID");
}

function validateConflict(value: string): void {
  if (!CONFLICT_ID.test(value)) throw new Error("invalid conflict ID");
}

function absoluteExecutable(value: string): boolean {
  return value.startsWith("/") || /^[A-Za-z]:[\\/]/.test(value) || value.startsWith("\\\\");
}

function parseJson(source: string): unknown {
  try { return JSON.parse(source); } catch { throw new Error("CLI returned malformed JSON"); }
}
