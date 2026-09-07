import { execFile as nodeExecFile } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { ScanStatus } from "../contracts/review-v3";
import type { SessionEventPageV1 } from "../contracts/review-v4";
import type { ConversationPageV1 } from "../contracts/conversation-page";
import { parseConversationPageV1 } from "../data/conversation-page";
import { parseSessionEventPageV1 } from "../data/contracts-v4";

const PROJECT_ID = /^project-[a-z0-9][a-z0-9._-]{0,127}$/;
const CONFLICT_ID = /^conflict-[a-z0-9][a-z0-9._-]{0,191}$/;
const SCAN_JOB_ID = /^[a-z0-9][a-z0-9._-]{0,127}$/;
const SCAN_GENERATION_ID = /^(?:generation|scan)-[a-z0-9][a-z0-9._-]{0,127}$/;
const SCAN_ERROR_CODE = /^[a-z][a-z0-9_]{0,127}$/;
const SCAN_COMMAND_FAILED = "SessionReviewer scan command failed";
const SESSION_EVENTS_FAILED = "无法读取扫描 Session；请刷新项目后重试，并确认 CLI 已更新。";
const INSPECT_ID = /^[a-z0-9][a-z0-9._-]{0,127}$/;
const DIGEST = /^sha256:[0-9a-f]{64}$/;
const SCAN_STATES = ["queued", "running", "completed", "completed_with_issues", "failed"] as const;
const SCAN_PHASES = ["discovering", "extracting", "reducing", "rendering", "syncing"] as const;
const SCAN_STATUS_FIELDS = new Set([
  "schema_version", "job_id", "project_id", "state", "phase", "session_count", "indexed_count",
  "issue_count", "generation_id", "error_code", "error_message",
]);

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
  reviewSchemaVersion: 3;
}

export interface SessionEventRequest {
  projectId: string;
  provider: string;
  sessionId: string;
  expectedGenerationId: string;
  expectedSessionViewDigest: string;
  limit: number;
  cursor?: string;
  anchor?: number;
}

export interface ConversationRequest {
  projectId: string;
  provider: string;
  sessionId: string;
  expectedGenerationId: string;
  expectedSessionViewDigest: string;
  limit: number;
  turnUnitId?: string;
  cursor?: string;
  messageCursor?: string;
}

export type ConversationErrorCode = "source_unavailable" | "generation_mismatch" | "stale_cursor" | "unsupported_provider" | "conversation_failed";

export class ConversationQueryError extends Error {
  constructor(readonly code: ConversationErrorCode, message: string) {
    super(message);
    this.name = "ConversationQueryError";
  }
}

export interface SyncOperation {
  entity_id: string;
  kind: string;
  target?: string;
  relative_path?: string;
  before_hash?: string;
  after_hash?: string;
}

export interface SyncStatus {
  project_id: string;
  in_sync: number;
  conflicted: number;
  malformed: number;
  queued: number;
  blocked: number;
  open_conflicts: string[];
  pending: SyncOperation[];
  derived_state: string;
  derived_files: number;
  migration: string;
  machine_state: string;
  last_successful_sync: string;
  pending_operations: SyncOperation[];
  hidden_conflict_ids: string[];
}

export class SyncStatusError extends Error {
  constructor(readonly code: "cli_unavailable" | "sync_status_failed") {
    super(code === "cli_unavailable" ? "SessionReviewer CLI is unavailable" : "SessionReviewer sync status failed");
  }
}

export class CliRunner {
  constructor(
    readonly executable: string,
    private readonly execFile: ExecFileLike = nodeExecFile
  ) {
    if (!absoluteExecutable(executable)) throw new Error("CLI executable path must be absolute");
  }

  async verifyExecutable(): Promise<VerifiedExecutable> {
    const { stdout } = await this.run(["version", "--json"]);
    const value = parseJson(stdout) as Record<string, unknown>;
    if (typeof value.version !== "string" || !/^\d+\.\d+\.\d+$/.test(value.version)) throw new Error("CLI version is not semantic");
    if (value.review_schema_version !== 3) throw new Error("CLI review schema version is incompatible");
    return { version: value.version, reviewSchemaVersion: 3 };
  }

  async startScan(projectId: string): Promise<ScanStatus> {
    validateProject(projectId);
    return parseScanStatus(await this.runJSON(["scan", "start", "--project-id", projectId, "--json"]), projectId);
  }

  async getScanStatus(projectId: string): Promise<ScanStatus> {
    validateProject(projectId);
    return parseScanStatus(await this.runJSON(["scan", "status", "--project-id", projectId, "--json"]), projectId);
  }

  async getSessionEvents(request: SessionEventRequest): Promise<SessionEventPageV1> {
    validateSessionEventRequest(request);
    const args = [
      "inspect", "session-events",
      "--project-id", request.projectId,
      "--provider", request.provider,
      "--session-id", request.sessionId,
      "--expected-generation-id", request.expectedGenerationId,
      "--limit", String(request.limit),
      "--json"
    ];
    if (request.cursor !== undefined) args.push("--cursor", request.cursor);
    else if (request.anchor !== undefined) args.push("--anchor", String(request.anchor));
    try {
      const page = parseSessionEventPageV1((await this.run(args)).stdout);
      if (page.project_id !== request.projectId || page.provider !== request.provider || page.session_id !== request.sessionId ||
          page.generation_id !== request.expectedGenerationId || page.session_view_digest !== request.expectedSessionViewDigest) {
        throw new Error("Session event page binding mismatch");
      }
      return page;
    } catch {
      throw new Error(SESSION_EVENTS_FAILED);
    }
  }

  async getConversation(request: ConversationRequest): Promise<ConversationPageV1> {
    validateConversationRequest(request);
    const args = [
      "inspect", "conversation-chain",
      "--project-id", request.projectId,
      "--provider", request.provider,
      "--session-id", request.sessionId,
      "--expected-generation-id", request.expectedGenerationId,
      "--limit", String(request.limit),
      "--json"
    ];
    if (request.turnUnitId !== undefined) args.push("--turn-unit-id", request.turnUnitId);
    if (request.cursor !== undefined) args.push("--cursor", request.cursor);
    if (request.messageCursor !== undefined) args.push("--message-cursor", request.messageCursor);
    try {
      const page = parseConversationPageV1((await this.run(args)).stdout);
      if (page.project_id !== request.projectId || page.provider !== request.provider || page.session_id !== request.sessionId ||
          page.generation_id !== request.expectedGenerationId || page.session_view_digest !== request.expectedSessionViewDigest ||
          page.mode !== (request.turnUnitId === undefined ? "turn_index" : "turn_messages") ||
          page.turn_unit_id !== (request.turnUnitId ?? null)) {
        throw new ConversationQueryError("generation_mismatch", conversationErrorMessage("generation_mismatch"));
      }
      return page;
    } catch (error) {
      if (error instanceof ConversationQueryError) throw error;
      const code = conversationErrorCode(error);
      throw new ConversationQueryError(code, conversationErrorMessage(code));
    }
  }

  async syncProject(projectId: string): Promise<string> {
    validateProject(projectId);
    return (await this.run(["sync", "--project-id", projectId], 120_000)).stdout;
  }

  async status(projectId: string): Promise<SyncStatus> {
    validateProject(projectId);
    try {
      const { stdout } = await this.run(["sync", "status", "--json", "--project-id", projectId]);
      return parseSyncStatus(parseJson(stdout), projectId);
    } catch (error) {
      const code = (error as { code?: unknown } | null)?.code;
      throw new SyncStatusError(code === "ENOENT" || code === "EACCES" || code === "ENOEXEC" ? "cli_unavailable" : "sync_status_failed");
    }
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

  private async run(args: readonly string[], timeout = 10_000): Promise<{ stdout: string; stderr: string }> {
    if (!allowedArgs(args)) throw new Error("command is not allowed");
    return new Promise((resolve, reject) => {
      this.execFile(this.executable, args, { shell: false, windowsHide: true, timeout, maxBuffer: 1 << 20, encoding: "utf8" }, (error, stdout, stderr) => {
        if (error) reject(Object.assign(new Error(`SessionReviewer CLI failed: ${stderr.trim() || error.message}`), { code: (error as { code?: unknown }).code, stdout: stdout || (error as { stdout?: unknown }).stdout }));
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
      if (payload === undefined || payload === null) throw new Error(SCAN_COMMAND_FAILED);
    }
    if (typeof payload !== "object" || payload === null || Array.isArray(payload)) throw new Error(SCAN_COMMAND_FAILED);
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
  if (args.length === 5 && args[0] === "scan" && args[1] === "start" && args[2] === "--project-id" && args[4] === "--json") return PROJECT_ID.test(args[3] ?? "");
  if (args.length === 5 && args[0] === "scan" && args[1] === "status" && args[2] === "--project-id" && args[4] === "--json") return PROJECT_ID.test(args[3] ?? "");
  if ((args.length === 13 || args.length === 15) && args[0] === "inspect" && args[1] === "session-events" &&
      args[2] === "--project-id" && PROJECT_ID.test(args[3] ?? "") && args[4] === "--provider" && INSPECT_ID.test(args[5] ?? "") &&
      args[6] === "--session-id" && INSPECT_ID.test(args[7] ?? "") && args[8] === "--expected-generation-id" && INSPECT_ID.test(args[9] ?? "") &&
      args[10] === "--limit" && validInspectLimit(args[11]) && args[12] === "--json") {
    if (args.length === 13) return true;
    if (args[13] === "--cursor") return boundedCursor(args[14]);
    return args[13] === "--anchor" && validAnchor(args[14]);
  }
  if (args.length >= 13 && args.length <= 17 && args[0] === "inspect" && args[1] === "conversation-chain" &&
      args[2] === "--project-id" && PROJECT_ID.test(args[3] ?? "") && args[4] === "--provider" && INSPECT_ID.test(args[5] ?? "") &&
      args[6] === "--session-id" && INSPECT_ID.test(args[7] ?? "") && args[8] === "--expected-generation-id" && INSPECT_ID.test(args[9] ?? "") &&
      args[10] === "--limit" && validConversationLimit(args[11]) && args[12] === "--json") {
    const rest = args.slice(13);
    if (rest.length === 0) return true;
    if (rest.length === 2) {
      return (rest[0] === "--cursor" && boundedCursor(rest[1])) ||
        (rest[0] === "--turn-unit-id" && INSPECT_ID.test(rest[1] ?? ""));
    }
    if (rest.length === 4 && rest[0] === "--turn-unit-id" && INSPECT_ID.test(rest[1] ?? "")) {
      return rest[2] === "--message-cursor" && boundedCursor(rest[3]);
    }
    return false;
  }
  return false;
}

function validateSessionEventRequest(request: SessionEventRequest): void {
  validateProject(request.projectId);
  if (!INSPECT_ID.test(request.provider)) throw new Error("invalid provider");
  if (!INSPECT_ID.test(request.sessionId)) throw new Error("invalid Session ID");
  if (!INSPECT_ID.test(request.expectedGenerationId)) throw new Error("invalid generation ID");
  if (!DIGEST.test(request.expectedSessionViewDigest)) throw new Error("invalid Session view digest");
  if (!Number.isSafeInteger(request.limit) || request.limit < 1 || request.limit > 100) throw new Error("invalid event page limit");
  if (request.cursor !== undefined && request.anchor !== undefined) throw new Error("cursor and anchor are mutually exclusive");
  if (request.cursor !== undefined && !boundedCursor(request.cursor)) throw new Error("invalid cursor");
  if (request.anchor !== undefined && (!Number.isSafeInteger(request.anchor) || request.anchor < 1)) throw new Error("invalid anchor");
}

function validateConversationRequest(request: ConversationRequest): void {
  validateProject(request.projectId);
  if (!INSPECT_ID.test(request.provider)) throw new Error("invalid provider");
  if (!INSPECT_ID.test(request.sessionId)) throw new Error("invalid Session ID");
  if (!INSPECT_ID.test(request.expectedGenerationId)) throw new Error("invalid generation ID");
  if (!DIGEST.test(request.expectedSessionViewDigest)) throw new Error("invalid Session view digest");
  if (!Number.isSafeInteger(request.limit) || request.limit < 1 || request.limit > 64) throw new Error("invalid conversation page limit");
  if (request.turnUnitId !== undefined && !INSPECT_ID.test(request.turnUnitId)) throw new Error("invalid turn unit ID");
  if (request.cursor !== undefined && request.turnUnitId !== undefined) throw new Error("index cursor cannot select a turn");
  if (request.messageCursor !== undefined && request.turnUnitId === undefined) throw new Error("message cursor requires a turn");
  if (request.cursor !== undefined && !boundedCursor(request.cursor)) throw new Error("invalid cursor");
  if (request.messageCursor !== undefined && !boundedCursor(request.messageCursor)) throw new Error("invalid message cursor");
}

function validConversationLimit(value: string | undefined): boolean {
  return Boolean(value && /^[1-9][0-9]?$/.test(value) && Number(value) <= 64);
}

function conversationErrorCode(error: unknown): ConversationErrorCode {
  const stdout = (error as { stdout?: unknown } | null)?.stdout;
  if (typeof stdout === "string" && Buffer.byteLength(stdout, "utf8") <= 4096) {
    try {
      const parsed = JSON.parse(stdout) as { error?: { code?: unknown } };
      const code = parsed?.error?.code;
      if (code === "source_unavailable" || code === "generation_mismatch" || code === "stale_cursor" || code === "unsupported_provider") return code;
    } catch { /* The generic bounded error below is intentional. */ }
  }
  return "conversation_failed";
}

function conversationErrorMessage(code: ConversationErrorCode): string {
  return {
    source_unavailable: "问答来源暂不可用；现有执行事实仍可阅读。",
    generation_mismatch: "项目已更新；请刷新后重新读取问答。",
    stale_cursor: "问答分页已失效；请从首页重新读取。",
    unsupported_provider: "当前来源暂不支持问答读取。",
    conversation_failed: "无法读取问答记录；请刷新项目后重试，并确认 CLI 已更新。"
  }[code];
}

function validInspectLimit(value: string | undefined): boolean {
  if (!value || !/^[1-9][0-9]{0,2}$/.test(value)) return false;
  const parsed = Number(value);
  return parsed >= 1 && parsed <= 100;
}

function validAnchor(value: string | undefined): boolean {
  return Boolean(value && /^[1-9][0-9]*$/.test(value) && Number.isSafeInteger(Number(value)));
}

function boundedCursor(value: string | undefined): boolean {
  return typeof value === "string" && value.length > 0 && !value.startsWith("--") && Buffer.byteLength(value, "utf8") <= 4096 && !value.includes("\0");
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

function parseSyncStatus(value: unknown, projectId: string): SyncStatus {
  const fail = (): never => { throw new SyncStatusError("sync_status_failed"); };
  if (typeof value !== "object" || value === null || Array.isArray(value)) return fail();
  const status = value as Record<string, unknown>;
  if (status.project_id !== projectId) return fail();
  for (const key of ["in_sync", "conflicted", "malformed", "queued", "blocked", "derived_files"]) {
    if (scanCount(status[key]) === undefined) return fail();
  }
  for (const key of ["open_conflicts", "hidden_conflict_ids"]) {
    const values = status[key];
    if (!Array.isArray(values) || values.length > 65_536 || values.some((item: unknown) => !syncText(item))) return fail();
  }
  for (const key of ["pending", "pending_operations"]) {
    const operations = status[key];
    if (!Array.isArray(operations) || operations.length > 65_536 || operations.some((item: unknown) => !syncOperation(item))) return fail();
  }
  // Legacy status can leave publisher states empty when that publisher does
  // not apply. Keep that existing wire shape readable.
  if (!syncText(status.derived_state) || !["", "current", "pending", "deferred", "failed"].includes(status.derived_state) ||
      !syncText(status.machine_state) || !["", "current", "pending", "blocked"].includes(status.machine_state) ||
      !syncText(status.migration) || !["current", "required"].includes(status.migration) || !syncText(status.last_successful_sync)) return fail();
  return status as unknown as SyncStatus;
}

function syncText(value: unknown): value is string {
  return typeof value === "string" && value.length <= 4096 && !value.includes("\0");
}

function syncOperation(value: unknown): value is SyncOperation {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const operation = value as Record<string, unknown>;
  return syncText(operation.entity_id) && operation.entity_id.length > 0 && syncText(operation.kind) &&
    ["add_project", "add_vault", "update_project", "update_vault", "archive", "restore", "rename", "conflict", "queue", "establish_base"].includes(operation.kind) &&
    ["target", "relative_path", "before_hash", "after_hash"].every((key) => operation[key] === undefined || syncText(operation[key]));
}

function parseScanStatus(value: Record<string, unknown>, expectedProjectId: string): ScanStatus {
  if (Object.keys(value).some((key) => !SCAN_STATUS_FIELDS.has(key))) throw new Error(SCAN_COMMAND_FAILED);
  if (value.schema_version !== 1) throw new Error(SCAN_COMMAND_FAILED);
  const projectId = typeof value.project_id === "string" ? value.project_id : "";
  const jobId = typeof value.job_id === "string" ? value.job_id : "";
  const stateStr = typeof value.state === "string" ? value.state : "";
  const phaseStr = typeof value.phase === "string" ? value.phase : "";
  const sessionCount = scanCount(value.session_count);
  const indexedCount = scanCount(value.indexed_count);
  const issueCount = scanCount(value.issue_count);
  const generationId = scanOptionalIdentity(value.generation_id, SCAN_GENERATION_ID);
  const errorCode = scanOptionalIdentity(value.error_code, SCAN_ERROR_CODE);
  const errorMessage = scanOptionalText(value.error_message, 4096);
  if (projectId !== expectedProjectId || !SCAN_JOB_ID.test(jobId) || !SCAN_STATES.includes(stateStr as ScanStatus["state"]) ||
      !SCAN_PHASES.includes(phaseStr as ScanStatus["phase"]) || sessionCount === undefined || indexedCount === undefined ||
      issueCount === undefined || indexedCount > sessionCount || issueCount > sessionCount || generationId === null ||
      errorCode === null || errorMessage === null) {
    throw new Error(SCAN_COMMAND_FAILED);
  }
  const status: ScanStatus = {
    schema_version: 1,
    job_id: jobId,
    project_id: projectId,
    state: stateStr as ScanStatus["state"],
    phase: phaseStr as ScanStatus["phase"],
    session_count: sessionCount,
    indexed_count: indexedCount,
    issue_count: issueCount,
    generation_id: generationId,
    error_code: errorCode,
    error_message: errorMessage,
  };
  return status;
}

function scanCount(value: unknown): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : undefined;
}

function scanOptionalText(value: unknown, maximum: number): string | undefined | null {
  if (value === undefined) return undefined;
  return typeof value === "string" && value.length <= maximum && !value.includes("\0") ? value : null;
}

function scanOptionalIdentity(value: unknown, pattern: RegExp): string | undefined | null {
  if (value === undefined) return undefined;
  return typeof value === "string" && pattern.test(value) ? value : null;
}
