import { execFile as nodeExecFile } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

const PROJECT_ID = /^project-[a-z0-9][a-z0-9._-]{0,127}$/;
const CONFLICT_ID = /^conflict-[a-z0-9][a-z0-9._-]{0,191}$/;
const REVIEW_JOB_ID = /^[a-z0-9][a-z0-9._-]{0,127}$/;
const REVIEW_COMMAND_FAILED = "SessionReviewer review command failed";
const REVIEW_STATES = ["idle", "queued", "running", "completed", "failed", "cancel_requested", "cancelled", "retrying"] as const;
const REVIEW_PHASES = ["preflight", "scanning", "preparing", "reviewing", "applying", "syncing"] as const;

export const ACTIVE_REVIEW_STATES: readonly ReviewState[] = ["queued", "running", "retrying", "cancel_requested"];

export type ReviewState = (typeof REVIEW_STATES)[number];
export type ReviewPhase = (typeof REVIEW_PHASES)[number];

export interface AgentVerification {
  schemaVersion: 1;
  kind: "codex";
  compatible: boolean;
  version?: string;
  errorCode?: string;
}

export interface ReviewUsage {
  totalTokens: number;
  totalCostUsd?: number;
  pricingComplete: boolean;
}

export interface ReviewStatus {
  schemaVersion: 1;
  jobId?: string;
  projectId: string;
  state: ReviewState;
  phase?: ReviewPhase;
  attempt: number;
  sessionIndex: number;
  sessionCount: number;
  acceptedPackets: number;
  acceptedSessions: number;
  errorCode?: string;
  retryExpectedAttempt?: number;
  retryExpectedRevision?: number;
  canRetry: boolean;
  canCancel: boolean;
  canSyncOnly: boolean;
  reviewUsage?: ReviewUsage;
}

export interface ReviewRunner {
  verifyAgent: (executable: string) => Promise<AgentVerification>;
  startReview: (projectId: string, agentExecutable: string) => Promise<ReviewStatus>;
  reviewStatus: (projectId: string) => Promise<ReviewStatus>;
  cancelReview: (jobId: string) => Promise<ReviewStatus>;
  retryReview: (jobId: string, agentExecutable: string, expectedAttempt: number, expectedRevision: number) => Promise<ReviewStatus>;
  syncProject: (projectId: string) => Promise<string>;
}

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

  async verifyAgent(executable: string): Promise<AgentVerification> {
    return parseAgentVerification(await this.runJSON(["review", "agent", "verify", "--executable", executable, "--json"]));
  }

  async startReview(projectId: string, agentExecutable: string): Promise<ReviewStatus> {
    validateProject(projectId);
    return parseReviewStatus(await this.runJSON(["review", "start", "--project-id", projectId, "--agent-executable", agentExecutable, "--json"]));
  }

  async reviewStatus(projectId: string): Promise<ReviewStatus> {
    validateProject(projectId);
    return parseReviewStatus(await this.runJSON(["review", "status", "--project-id", projectId, "--json"]));
  }

  async cancelReview(jobId: string): Promise<ReviewStatus> {
    validateJobId(jobId);
    return parseReviewStatus(await this.runJSON(["review", "cancel", "--job-id", jobId, "--json"]));
  }

  async retryReview(jobId: string, agentExecutable: string, expectedAttempt: number, expectedRevision: number): Promise<ReviewStatus> {
    validateJobId(jobId);
    if (!Number.isSafeInteger(expectedAttempt) || expectedAttempt < 1 || !Number.isSafeInteger(expectedRevision) || expectedRevision < 1) {
      throw new Error(REVIEW_COMMAND_FAILED);
    }
    return parseReviewStatus(await this.runJSON([
      "review", "retry", "--job-id", jobId, "--agent-executable", agentExecutable,
      "--expected-attempt", String(expectedAttempt), "--expected-revision", String(expectedRevision), "--json"
    ]));
  }

  async syncProject(projectId: string): Promise<string> {
    validateProject(projectId);
    return (await this.run(["sync", "--project-id", projectId])).stdout;
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

  private async run(args: readonly string[]): Promise<{ stdout: string; stderr: string }> {
    if (!allowedArgs(args)) throw new Error("command is not allowed");
    return new Promise((resolve, reject) => {
      this.execFile(this.executable, args, { shell: false, windowsHide: true, timeout: 10_000, maxBuffer: 1 << 20, encoding: "utf8" }, (error, stdout, stderr) => {
        if (error) reject(Object.assign(new Error(`SessionReviewer CLI failed: ${stderr.trim() || error.message}`), { stdout: stdout || (error as { stdout?: unknown }).stdout }));
        else resolve({ stdout, stderr });
      });
    });
  }

  private async runJSON(args: readonly string[]): Promise<Record<string, unknown>> {
    let payload: unknown;
    try {
      payload = parseJson((await this.run(args)).stdout);
    } catch (error) {
      const carried = error as { stdout?: unknown } | null;
      if (typeof carried?.stdout === "string") {
        try { payload = JSON.parse(carried.stdout); } catch { payload = undefined; }
      }
      if (payload === undefined || payload === null) throw new Error(REVIEW_COMMAND_FAILED);
    }
    if (typeof payload !== "object" || payload === null || Array.isArray(payload)) throw new Error(REVIEW_COMMAND_FAILED);
    return payload as Record<string, unknown>;
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
  if (args.length === 3 && args[0] === "sync" && args[1] === "--project-id") return PROJECT_ID.test(args[2] ?? "");
  if (args.length === 6 && args[0] === "review" && args[1] === "agent" && args[2] === "verify" && args[3] === "--executable" && args[5] === "--json") return absoluteExecutable(args[4] ?? "");
  if (args.length === 7 && args[0] === "review" && args[1] === "start" && args[2] === "--project-id" && args[4] === "--agent-executable" && args[6] === "--json") return PROJECT_ID.test(args[3] ?? "") && absoluteExecutable(args[5] ?? "");
  if (args.length === 5 && args[0] === "review" && args[1] === "status" && args[2] === "--project-id" && args[4] === "--json") return PROJECT_ID.test(args[3] ?? "");
  if (args.length === 5 && args[0] === "review" && args[1] === "cancel" && args[2] === "--job-id" && args[4] === "--json") return REVIEW_JOB_ID.test(args[3] ?? "");
  if (args.length === 11 && args[0] === "review" && args[1] === "retry" && args[2] === "--job-id" && args[4] === "--agent-executable" && args[6] === "--expected-attempt" && args[8] === "--expected-revision" && args[10] === "--json") {
    return REVIEW_JOB_ID.test(args[3] ?? "") && absoluteExecutable(args[5] ?? "") && /^[1-9][0-9]*$/.test(args[7] ?? "") && /^[1-9][0-9]*$/.test(args[9] ?? "");
  }
  return false;
}

function validateProject(value: string): void {
  if (!PROJECT_ID.test(value)) throw new Error("invalid project ID");
}

function validateConflict(value: string): void {
  if (!CONFLICT_ID.test(value)) throw new Error("invalid conflict ID");
}

function validateJobId(value: string): void {
  if (!REVIEW_JOB_ID.test(value)) throw new Error(REVIEW_COMMAND_FAILED);
}

function absoluteExecutable(value: string): boolean {
  return value.startsWith("/") || /^[A-Za-z]:[\\/]/.test(value) || value.startsWith("\\\\");
}

function parseJson(source: string): unknown {
  try { return JSON.parse(source); } catch { throw new Error("CLI returned malformed JSON"); }
}

function parseAgentVerification(value: Record<string, unknown>): AgentVerification {
  if (value.schema_version !== 1 || value.kind !== "codex" || typeof value.compatible !== "boolean") {
    throw new Error(REVIEW_COMMAND_FAILED);
  }
  if (value.version !== undefined && typeof value.version !== "string") throw new Error(REVIEW_COMMAND_FAILED);
  if (value.error_code !== undefined && typeof value.error_code !== "string") throw new Error(REVIEW_COMMAND_FAILED);
  return {
    schemaVersion: 1,
    kind: "codex",
    compatible: value.compatible,
    version: value.version as string | undefined,
    errorCode: value.error_code as string | undefined
  };
}

function parseReviewStatus(value: Record<string, unknown>): ReviewStatus {
  if (value.schema_version !== 1) throw new Error(REVIEW_COMMAND_FAILED);
  if (typeof value.project_id !== "string" || value.project_id.length === 0 || value.project_id.length > 129) throw new Error(REVIEW_COMMAND_FAILED);
  if (typeof value.state !== "string" || !REVIEW_STATES.includes(value.state as ReviewState)) throw new Error(REVIEW_COMMAND_FAILED);
  const idle = value.state === "idle";
  if (idle ? value.job_id !== undefined : typeof value.job_id !== "string" || !REVIEW_JOB_ID.test(value.job_id)) {
    throw new Error(REVIEW_COMMAND_FAILED);
  }
  const status: ReviewStatus = {
    schemaVersion: 1,
    projectId: value.project_id,
    state: value.state as ReviewState,
    attempt: readCount(value, "attempt", !idle),
    sessionIndex: readCount(value, "session_index"),
    sessionCount: readCount(value, "session_count"),
    acceptedPackets: readCount(value, "accepted_packets"),
    acceptedSessions: readCount(value, "accepted_sessions"),
    canRetry: readBoolean(value, "can_retry"),
    canCancel: readBoolean(value, "can_cancel"),
    canSyncOnly: readBoolean(value, "can_sync_only")
  };
  if (typeof value.job_id === "string") status.jobId = value.job_id;
  if (value.phase !== undefined) {
    if (typeof value.phase !== "string" || !REVIEW_PHASES.includes(value.phase as ReviewPhase)) throw new Error(REVIEW_COMMAND_FAILED);
    status.phase = value.phase as ReviewPhase;
  }
  if (value.error_code !== undefined) {
    if (typeof value.error_code !== "string" || value.error_code.length === 0) throw new Error(REVIEW_COMMAND_FAILED);
    status.errorCode = value.error_code;
  }
  if (value.retry_expected_attempt !== undefined) status.retryExpectedAttempt = readCount(value, "retry_expected_attempt");
  if (value.retry_expected_revision !== undefined) status.retryExpectedRevision = readCount(value, "retry_expected_revision");
  if (value.review_usage !== undefined) status.reviewUsage = parseReviewUsage(value.review_usage);
  return status;
}

function parseReviewUsage(value: unknown): ReviewUsage {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new Error(REVIEW_COMMAND_FAILED);
  const usage = value as Record<string, unknown>;
  const totalTokens = usage.total_tokens;
  if (typeof totalTokens !== "number" || !Number.isSafeInteger(totalTokens) || totalTokens < 0) throw new Error(REVIEW_COMMAND_FAILED);
  if (typeof usage.pricing_complete !== "boolean") throw new Error(REVIEW_COMMAND_FAILED);
  const parsed: ReviewUsage = { totalTokens, pricingComplete: usage.pricing_complete };
  if (usage.total_cost_usd !== undefined) {
    if (typeof usage.total_cost_usd !== "number" || !Number.isFinite(usage.total_cost_usd) || usage.total_cost_usd < 0) {
      throw new Error(REVIEW_COMMAND_FAILED);
    }
    parsed.totalCostUsd = usage.total_cost_usd;
  }
  return parsed;
}

function readCount(value: Record<string, unknown>, key: string, required = true): number {
  const count = value[key];
  if (count === undefined && !required) return 0;
  if (typeof count !== "number" || !Number.isSafeInteger(count) || count < 0) throw new Error(REVIEW_COMMAND_FAILED);
  return count;
}

function readBoolean(value: Record<string, unknown>, key: string): boolean {
  if (typeof value[key] !== "boolean") throw new Error(REVIEW_COMMAND_FAILED);
  return value[key];
}
