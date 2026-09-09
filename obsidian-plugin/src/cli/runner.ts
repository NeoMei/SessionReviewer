import { parseProblemPlacementJob, type ProblemPlacementJob } from "./problem-placement";
import { validateDecisionJob, type DecisionExtractionJob, type DecisionAgentConfiguration } from "./decision-jobs";
import { parseDecisionCandidates } from "../data/contracts-v4";
import type { AgentAnnotationEntryV1 } from "../contracts/review-v4";
import type { DecisionCandidateAction } from "../view/decision-candidates";
import type { DecisionInput } from "../view/decision-form";
import type { DecisionV4 } from "../contracts/review-v4";
import { validateCatalog, validatePricingResult, type PricingCatalog, type PricingCatalogSelection } from "./pricing";
import type { PricingSnapshotV1, PricingSupplementV1 } from "../contracts/review-v4";
import { parsePricingSnapshotV1, parsePricingSupplementV1 } from "../data/contracts-v4";
import { validateSearchPage, type SessionSearchRequest, type SessionSearchPage } from "./session-search";
import { execFile as nodeExecFile } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { ScanStatus } from "../contracts/review-v3";
import type { ProblemMapCandidateV1, ProblemNodeV4, SessionEventPageV1, SessionSummaryV1 } from "../contracts/review-v4";
import type { ConversationPageV1 } from "../contracts/conversation-page";
import { parseConversationPageV1 } from "../data/conversation-page";
import { parseSessionEventPageV1, parseSessionInspectWireError, parseSessionSummaryV1 } from "../data/contracts-v4";
import { SessionInspectError, type SessionInspectErrorCode } from "./session-inspect-error";
export { SessionInspectError, type SessionInspectErrorCode } from "./session-inspect-error";

const PROJECT_ID = /^project-[a-z0-9][a-z0-9._-]{0,127}$/;
const CONFLICT_ID = /^conflict-[a-z0-9][a-z0-9._-]{0,191}$/;
const SCAN_JOB_ID = /^[a-z0-9][a-z0-9._-]{0,127}$/;
const SCAN_GENERATION_ID = /^(?:generation|scan)-[a-z0-9][a-z0-9._-]{0,127}$/;
const SCAN_ERROR_CODE = /^[a-z][a-z0-9_]{0,127}$/;
const SCAN_COMMAND_FAILED = "SessionReviewer scan command failed";
const SESSION_SUMMARY_FAILED = "无法读取 Session 摘要；请刷新项目后重试，并确认 CLI 已更新。";
const INSPECT_ID = /^[a-z0-9][a-z0-9._-]{0,127}$/;
const ENTITY_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/;
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

export interface SessionSummaryRequest {
  projectId: string;
  provider: string;
  sessionId: string;
  expectedGenerationId: string;
  expectedSessionViewDigest: string;
}

export class SessionSummaryQueryError extends Error {
  constructor(readonly code: "summary_failed") {
    super(SESSION_SUMMARY_FAILED);
    this.name = "SessionSummaryQueryError";
  }
}

export interface ConversationRequest {
  projectId: string;
  provider: string;
  sessionId: string;
  expectedGenerationId: string;
  expectedSessionViewDigest: string;
	sessionViewDigest?: string;
  limit: number;
  turnUnitId?: string;
  cursor?: string;
  messageCursor?: string;
}

export type ConversationErrorCode = "source_unavailable" | "retained_evidence_unavailable" | "retained_evidence_ambiguous" | "visible_reader_unsupported" | "generation_mismatch" | "stale_cursor" | "unsupported_provider" | "conversation_failed";

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

export type ProblemCandidate = ProblemMapCandidateV1["candidates"][number];
export interface ProblemOperationResponse {
  schema_version: 1;
  project_id: string;
  problem_map_revision: number;
  review_sha256: string;
  problems: ProblemNodeV4[];
  candidates?: ProblemCandidate[];
  candidate?: ProblemCandidate;
}
export interface CreateProblemRequest { projectId: string; expectedProblemMapRevision: number; expectedReviewSHA256: string; question: string }
export interface ProblemCAS { projectId: string; expectedProblemMapRevision: number; expectedReviewSHA256: string }
export interface EditProblemRequest extends ProblemCAS { problem: ProblemNodeV4; question: string; currentConclusion: string; completionCriterion: string }

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

  async startProblemPlacement(projectId: string, candidate: Pick<ProblemCandidate, "candidate_id" | "revision">, mapRevision: number, generationId: string): Promise<ProblemPlacementJob> {
    validateProject(projectId);
    return parseProblemPlacementJob(parseJson((await this.run(["problems", "placement", "request", "--project-id", projectId, "--candidate-id", candidate.candidate_id, "--expected-candidate-revision", String(candidate.revision), "--expected-problem-map-revision", String(mapRevision), "--expected-generation-id", generationId, "--json"])).stdout), projectId, candidate.candidate_id);
  }

  async latestProblemPlacement(projectId: string, candidateId: string): Promise<ProblemPlacementJob | undefined> {
    validateProject(projectId);
    const value = parseJson((await this.run(["problems", "placement", "status", "--project-id", projectId, "--candidate-id", candidateId, "--json"])).stdout);
    return value === null ? undefined : parseProblemPlacementJob(value, projectId, candidateId);
  }

  async getProblemPlacement(projectId: string, jobId: string): Promise<ProblemPlacementJob> {
    validateProject(projectId);
    const job = parseProblemPlacementJob(parseJson((await this.run(["problems", "placement", "status", "--project-id", projectId, "--job-id", jobId, "--json"])).stdout), projectId);
    if (job.job_id !== jobId) throw new Error("placement job mismatch");
    return job;
  }

  async cancelProblemPlacement(projectId: string, jobId: string, revision: number): Promise<ProblemPlacementJob> {
    validateProject(projectId);
    const job = parseProblemPlacementJob(parseJson((await this.run(["problems", "placement", "cancel", "--project-id", projectId, "--job-id", jobId, "--expected-revision", String(revision), "--json"])).stdout), projectId);
    if (job.job_id !== jobId) throw new Error("placement job mismatch");
    return job;
  }

  async startDecisionExtraction(projectId: string, generationId: string): Promise<DecisionExtractionJob> {
    validateProject(projectId);
    const job = validateDecisionJob(parseJson((await this.run(["decisions", "extract", "--project-id", projectId, "--expected-generation-id", generationId, "--json"])).stdout), projectId);
    return job;
  }

  async getDecisionExtraction(projectId: string, jobId?: string): Promise<DecisionExtractionJob | undefined> {
    validateProject(projectId);
    const value = parseJson((await this.run(["decisions", "extract", "status", jobId ? "--job-id" : "--project-id", jobId ?? projectId, "--json"])).stdout);
    if (!jobId && value === null) return undefined;
    return validateDecisionJob(value, projectId, jobId);
  }

  async cancelDecisionExtraction(projectId: string, jobId: string, revision: number): Promise<DecisionExtractionJob> {
    validateProject(projectId);
    return validateDecisionJob(parseJson((await this.run(["decisions", "extract", "cancel", "--job-id", jobId, "--expected-revision", String(revision), "--json"])).stdout), projectId, jobId);
  }

  async decisionAgentConfiguration(executable?: string): Promise<DecisionAgentConfiguration> {
    const args = executable === undefined ? ["review", "agent", "status", "--json"] : ["review", "agent", "configure", "--executable", executable, "--json"];
    let value: unknown;
    try { value = parseJson((await this.run(args)).stdout); }
    catch (error) { const stdout = (error as { stdout?: unknown }).stdout; if (typeof stdout !== "string") throw new Error("无法读取或验证 Agent 配置。"); value = parseJson(stdout); }
    if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("invalid Agent configuration");
    const row = value as Record<string, unknown>;
    if (row.schema_version !== 1 || row.kind !== "codex" || typeof row.compatible !== "boolean" || Object.keys(row).some(key => !["schema_version", "kind", "compatible", "version", "executable", "error_code"].includes(key)) || ["version", "executable", "error_code"].some(key => row[key] !== undefined && typeof row[key] !== "string")) throw new Error("invalid Agent configuration");
    return value as DecisionAgentConfiguration;
  }

  async listDecisionCandidates(projectId: string): Promise<AgentAnnotationEntryV1[]> {
    validateProject(projectId);
    try { return parseDecisionCandidates((await this.run(["decisions", "candidates", "list", "--project-id", projectId, "--json"])).stdout, projectId); }
    catch { throw new Error("无法读取决策候选；请刷新项目重试。"); }
  }

  async transitionDecisionCandidate(projectId: string, expectedReviewSHA256: string, candidate: Pick<AgentAnnotationEntryV1, "id" | "revision">, action: DecisionCandidateAction, input?: DecisionInput): Promise<void> {
    validateProject(projectId);
    if (!/^[0-9a-f]{64}$/.test(expectedReviewSHA256) || !INSPECT_ID.test(candidate.id) || !Number.isSafeInteger(candidate.revision) || candidate.revision < 1 || !["confirm", "ignore", "not_decision", "restore"].includes(action)) throw new Error("invalid candidate transition");
    const args = ["decisions", "candidate", "transition", "--project-id", projectId, "--candidate-id", candidate.id, "--expected-revision", String(candidate.revision), "--action", action, "--expected-review-sha256", expectedReviewSHA256, "--json"];
    try { const result = parseJson((await this.runWithInput(args, input ? JSON.stringify(input) : "", 30_000)).stdout) as Record<string, unknown>; if (!result || result.schema_version !== 1 || result.project_id !== projectId) throw new Error("candidate result mismatch"); }
    catch { throw new Error("保存结果未确认；请刷新项目检查。"); }
  }

  async saveDecision(projectId: string, expectedReviewSHA256: string, input: DecisionInput, prior?: Pick<DecisionV4, "id" | "revision">): Promise<void> {
    validateProject(projectId);
    if (!/^[0-9a-f]{64}$/.test(expectedReviewSHA256) || (prior && (!ENTITY_ID.test(prior.id) || !Number.isSafeInteger(prior.revision) || prior.revision < 1))) throw new Error("invalid decision identity");
    const args = ["decisions", prior ? "edit" : "create", "--project-id", projectId, "--expected-review-sha256", expectedReviewSHA256];
    if (prior) args.push("--decision-id", prior.id, "--expected-decision-revision", String(prior.revision));
    args.push("--json");
    try {
      const result = parseJson((await this.runWithInput(args, JSON.stringify(input), 30_000)).stdout) as Record<string, unknown>;
      if (!result || result.schema_version !== 1 || result.project_id !== projectId || typeof result.review_sha256 !== "string" || !/^[0-9a-f]{64}$/.test(result.review_sha256) || Object.keys(result).some(key => !["schema_version", "project_id", "review_sha256", "decisions"].includes(key))) throw new Error("decision result mismatch");
    } catch { throw new Error("保存结果未确认；请刷新项目检查。"); }
  }

  async getPricingCatalog(): Promise<PricingCatalog> {
    try { return validateCatalog(parseJson((await this.run(["pricing", "catalog", "list", "--json"], 35_000)).stdout)); }
    catch { throw new Error("无法读取价格目录；可稍后重试或人工补充价格。"); }
  }

  async supplementPricing(input: PricingSupplementV1, expectedLedgerSHA256: string): Promise<PricingSnapshotV1> {
    parsePricingSupplementV1(JSON.stringify(input));
    return this.publishPrice(["pricing", "supplement"], input, expectedLedgerSHA256);
  }

  async acceptCatalogPricing(input: PricingCatalogSelection, expectedLedgerSHA256: string): Promise<PricingSnapshotV1> {
    if (input.schema_version !== 1 || input.minimum_reader_version !== "0.4.0" || !INSPECT_ID.test(input.modelpricewatch_listing_id) || !input.billing_host.trim() || !input.billing_mode.trim()) throw new Error("invalid catalog selection");
    return this.publishPrice(["pricing", "catalog", "accept"], input, expectedLedgerSHA256);
  }

  private async publishPrice(prefix: string[], input: PricingSupplementV1 | PricingCatalogSelection, expectedLedgerSHA256: string): Promise<PricingSnapshotV1> {
    validateProject(input.project_id);
    if (!INSPECT_ID.test(input.provider) || !INSPECT_ID.test(input.session_id) || !DIGEST.test(input.usage_record_digest) || !/^[0-9a-f]{64}$/.test(expectedLedgerSHA256)) throw new Error("invalid pricing identity");
    const args = [...prefix, "--project-id", input.project_id, "--provider", input.provider, "--session-id", input.session_id, "--usage-record-digest", input.usage_record_digest, "--expected-ledger-sha256", expectedLedgerSHA256, "--json"];
    try { const result = parsePricingSnapshotV1((await this.runWithInput(args, JSON.stringify(input), 35_000)).stdout); validatePricingResult(result, input); return result; }
    catch { throw new Error("保存结果未确认；请刷新账本检查。"); }
  }

  async getSessionSearch(request: SessionSearchRequest): Promise<SessionSearchPage> {
    validateProject(request.projectId);
    if (!INSPECT_ID.test(request.expectedGenerationId) || !["branch", "file", "error"].includes(request.queryKind) || typeof request.query !== "string" || !request.query.trim() || Buffer.byteLength(request.query, "utf8") > 256 || [...request.query].some((char) => char.charCodeAt(0) < 32 || char.charCodeAt(0) === 127) || !Number.isSafeInteger(request.limit) || request.limit < 1 || request.limit > 100 || (request.cursor !== undefined && !boundedCursor(request.cursor))) throw new Error("invalid search request");
    const args = ["inspect", "session-search", "--project-id", request.projectId, "--expected-generation-id", request.expectedGenerationId, "--query-kind", request.queryKind, "--query", request.query, "--limit", String(request.limit), "--json"];
    if (request.cursor !== undefined) args.push("--cursor", request.cursor);
    try { const page = parseJson((await this.run(args)).stdout) as SessionSearchPage; validateSearchPage(page, request); return page; }
    catch { throw new Error("无法搜索 Session；请刷新项目并确认 CLI 已更新。"); }
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
          page.generation_id !== request.expectedGenerationId || page.session_view_digest !== request.expectedSessionViewDigest ||
          !sessionEventPageMatchesRequest(page, request)) {
        throw new Error("Session event page binding mismatch");
      }
      return page;
    } catch (error) {
      throw error instanceof SessionInspectError ? error : new SessionInspectError(sessionInspectErrorCode(error));
    }
  }

  async getSessionSummary(request: SessionSummaryRequest): Promise<SessionSummaryV1> {
    validateSessionSummaryRequest(request);
    const args = [
      "inspect", "session-summary",
      "--project-id", request.projectId,
      "--provider", request.provider,
      "--session-id", request.sessionId,
      "--expected-generation-id", request.expectedGenerationId,
      "--json"
    ];
    try {
      const summary = parseSessionSummaryV1((await this.run(args, 5_000)).stdout);
      if (summary.project_id !== request.projectId || summary.provider !== request.provider || summary.session_id !== request.sessionId ||
          summary.generation_id !== request.expectedGenerationId || summary.session_view_digest !== request.expectedSessionViewDigest) {
        throw new Error("Session summary binding mismatch");
      }
      return summary;
    } catch {
      throw new SessionSummaryQueryError("summary_failed");
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
	if (request.sessionViewDigest !== undefined) args.push("--session-view-digest", request.sessionViewDigest);
    try {
      const page = parseConversationPageV1((await this.run(args)).stdout);
	  const selectedViewDigest = request.sessionViewDigest ?? request.expectedSessionViewDigest;
	  const expectedReaderVersion = request.sessionViewDigest === undefined ? "0.4.0" : "0.4.3";
      if (page.project_id !== request.projectId || page.provider !== request.provider || page.session_id !== request.sessionId ||
		  page.generation_id !== request.expectedGenerationId || page.session_view_digest !== selectedViewDigest ||
		  page.minimum_reader_version !== expectedReaderVersion ||
		  (request.sessionViewDigest !== undefined && page.evidence_session_view_digest !== undefined && page.evidence_session_view_digest !== selectedViewDigest) ||
          page.mode !== (request.turnUnitId === undefined ? "turn_index" : "turn_messages") ||
          page.turn_unit_id !== (request.turnUnitId ?? null) || !conversationPageMatchesRequest(page, request)) {
        throw new ConversationQueryError("generation_mismatch", conversationErrorMessage("generation_mismatch"));
      }
      return page;
    } catch (error) {
      if (error instanceof ConversationQueryError) throw error;
      const code = conversationErrorCode(error);
      throw new ConversationQueryError(code, conversationErrorMessage(code));
    }
  }

  async listProblemCandidates(projectId: string): Promise<ProblemOperationResponse> {
    validateProject(projectId);
    return this.problemResponse(await this.run(["problems", "candidates", "list", "--project-id", projectId, "--json"]), projectId);
  }

  async createProblem(request: CreateProblemRequest): Promise<ProblemOperationResponse> {
    validateProblemCAS(request);
    if (!request.question || Buffer.byteLength(request.question, "utf8") > 65_536) throw new Error("problem question is too large");
    const args = ["problems", "create", "--project-id", request.projectId, "--expected-problem-map-revision", String(request.expectedProblemMapRevision), "--expected-review-sha256", request.expectedReviewSHA256, "--json"];
    return this.problemResponse(await this.runWithInput(args, JSON.stringify({ schema_version: 1, question: request.question })), request.projectId);
  }

  async transitionProblemCandidate(request: ProblemCAS & { candidate: ProblemCandidate; action: "apply_root" | "apply_child" | "apply_sibling" | "merge" | "keep_pending" | "dismiss" | "restore"; targetProblemId?: string }): Promise<ProblemOperationResponse> {
    validateProblemCAS(request);
    if (!ENTITY_ID.test(request.candidate.candidate_id) || !Number.isSafeInteger(request.candidate.revision) || request.candidate.revision < 1) throw new Error("invalid candidate");
    const args = ["problems", "candidate", "transition", "--project-id", request.projectId, "--candidate-id", request.candidate.candidate_id, "--expected-candidate-revision", String(request.candidate.revision), "--expected-problem-map-revision", String(request.expectedProblemMapRevision), "--expected-review-sha256", request.expectedReviewSHA256, "--action", request.action];
    if (request.targetProblemId !== undefined) { if (!ENTITY_ID.test(request.targetProblemId)) throw new Error("invalid target problem"); args.push("--target-problem-id", request.targetProblemId); }
    args.push("--json");
    return this.problemResponse(await this.run(args), request.projectId);
  }

  async setProblemState(request: ProblemCAS & { problem: ProblemNodeV4; action: "resolve" | "reopen" }): Promise<ProblemOperationResponse> {
    validateProblemCAS(request);
    const args = ["problems", "state", "--project-id", request.projectId, "--problem-id", request.problem.id, "--expected-problem-revision", String(request.problem.revision), "--expected-problem-map-revision", String(request.expectedProblemMapRevision), "--expected-review-sha256", request.expectedReviewSHA256, "--action", request.action, "--json"];
    return this.problemResponse(await this.run(args), request.projectId);
  }

  async editProblem(request: EditProblemRequest): Promise<ProblemOperationResponse> {
    validateProblemCAS(request); validateProblemNodeIdentity(request.problem);
    const input = JSON.stringify({ schema_version: 1, question: request.question, current_conclusion: request.currentConclusion, completion_criterion: request.completionCriterion });
    const args = ["problems", "edit", "--project-id", request.projectId, "--problem-id", request.problem.id, "--expected-problem-revision", String(request.problem.revision), "--expected-problem-map-revision", String(request.expectedProblemMapRevision), "--expected-review-sha256", request.expectedReviewSHA256, "--json"];
    return this.problemResponse(await this.runWithInput(args, input), request.projectId);
  }

  async moveProblem(request: ProblemCAS & { problem: ProblemNodeV4; newParentId: string }): Promise<ProblemOperationResponse> {
    validateProblemCAS(request); validateProblemNodeIdentity(request.problem);
    if (request.newParentId !== "root" && !ENTITY_ID.test(request.newParentId)) throw new Error("invalid problem parent");
    const args = ["problems", "move", "--project-id", request.projectId, "--problem-id", request.problem.id, "--new-parent-id", request.newParentId, "--expected-problem-revision", String(request.problem.revision), "--expected-problem-map-revision", String(request.expectedProblemMapRevision), "--expected-review-sha256", request.expectedReviewSHA256, "--json"];
    return this.problemResponse(await this.run(args), request.projectId);
  }

  async reorderProblemChildren(request: ProblemCAS & { parentId: string; orderedChildIds: string[] }): Promise<ProblemOperationResponse> {
    validateProblemCAS(request);
    if (request.parentId !== "root" && !ENTITY_ID.test(request.parentId)) throw new Error("invalid reorder parent");
    if (!Array.isArray(request.orderedChildIds) || request.orderedChildIds.some((id) => !ENTITY_ID.test(id)) || new Set(request.orderedChildIds).size !== request.orderedChildIds.length) throw new Error("invalid child order");
    const args = ["problems", "reorder", "--project-id", request.projectId, "--parent-id", request.parentId, "--expected-problem-map-revision", String(request.expectedProblemMapRevision), "--expected-review-sha256", request.expectedReviewSHA256, "--json"];
    return this.problemResponse(await this.runWithInput(args, JSON.stringify({ schema_version: 1, ordered_child_ids: request.orderedChildIds })), request.projectId);
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

  private async runWithInput(args: readonly string[], input: string, timeout = 10_000): Promise<{ stdout: string; stderr: string }> {
    if (!allowedArgs(args)) throw new Error("command is not allowed");
    if (Buffer.byteLength(input, "utf8") > 65_536) throw new Error("stdin payload is too large");
    return new Promise((resolve, reject) => {
      const child = this.execFile(this.executable, args, { shell: false, windowsHide: true, timeout, maxBuffer: 1 << 20, encoding: "utf8" }, (error, stdout, stderr) => {
        if (error) reject(Object.assign(new Error(`SessionReviewer CLI failed: ${stderr.trim() || error.message}`), { code: (error as { code?: unknown }).code, stdout: stdout || (error as { stdout?: unknown }).stdout }));
        else resolve({ stdout, stderr });
      }) as { stdin?: { end(value: string): void } } | undefined;
      if (!child?.stdin) { reject(new Error("CLI stdin transport is unavailable")); return; }
      child.stdin.end(input);
    });
  }

  private problemResponse(result: { stdout: string }, projectId: string): ProblemOperationResponse {
    const value = parseJson(result.stdout);
    if (typeof value !== "object" || value === null || Array.isArray(value)) throw new Error("problem response is invalid");
    const row = value as Record<string, unknown>;
    const allowed = new Set(["schema_version", "project_id", "problem_map_revision", "review_sha256", "problems", "candidates", "candidate", "move_preview"]);
	const validNode = (value: unknown): value is ProblemNodeV4 => {
		if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
		const node = value as Record<string, unknown>;
		return matchesString(ENTITY_ID, node.id ?? "") && typeof node.question === "string" && node.question.length > 0 && node.question.length <= 4096 &&
			(node.primary_parent_id === null || matchesString(ENTITY_ID, node.primary_parent_id ?? "")) && Array.isArray(node.related_node_ids) && node.related_node_ids.length <= 2 && (node.related_node_ids as unknown[]).every((id) => matchesString(ENTITY_ID, id)) &&
			["not_started", "in_progress", "paused", "resolved"].includes(String(node.workflow_state)) && ["no_answer", "answered_unverified", "execution_verified"].includes(String(node.answer_state)) &&
			typeof node.completion_criterion === "string" && typeof node.current_conclusion === "string" && Array.isArray(node.source_turn_refs) && node.source_turn_refs.every(validProblemSourceRef) &&
			Number.isSafeInteger(node.sibling_order) && Number(node.sibling_order) >= 0 && Number.isSafeInteger(node.revision) && Number(node.revision) >= 1;
	};
    if (Object.keys(row).some((key) => !allowed.has(key)) || row.schema_version !== 1 || row.project_id !== projectId || !Number.isSafeInteger(row.problem_map_revision) || Number(row.problem_map_revision) < 0 || typeof row.review_sha256 !== "string" || !/^[0-9a-f]{64}$/.test(row.review_sha256) || !Array.isArray(row.problems) || !row.problems.every(validNode) || (row.candidates !== undefined && !Array.isArray(row.candidates))) throw new Error("problem response binding mismatch");
	if (row.candidates !== undefined && !(row.candidates as unknown[]).every((candidate) => validCandidate(candidate, projectId))) throw new Error("problem response binding mismatch");
	if (row.candidate !== undefined && !validCandidate(row.candidate, projectId)) throw new Error("problem response binding mismatch");
    return row as unknown as ProblemOperationResponse;
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
  if (args[0] === "problems" && args[1] === "placement" && args[3] === "--project-id" && PROJECT_ID.test(args[4] ?? "")) {
    if (args[2] === "request") return args.length === 14 && args[5] === "--candidate-id" && INSPECT_ID.test(args[6]) && args[7] === "--expected-candidate-revision" && validPositive(args[8]) && args[9] === "--expected-problem-map-revision" && validNonnegative(args[10]) && args[11] === "--expected-generation-id" && SCAN_GENERATION_ID.test(args[12]) && args[13] === "--json";
    if (args[2] === "status" && args[5] === "--candidate-id" && INSPECT_ID.test(args[6] ?? "") && args.length === 8 && args[7] === "--json") return true;
    if (args[5] !== "--job-id" || !INSPECT_ID.test(args[6] ?? "")) return false;
    return args[2] === "status" && args.length === 8 && args[7] === "--json" || args[2] === "cancel" && args.length === 10 && args[7] === "--expected-revision" && validPositive(args[8]) && args[9] === "--json";
  }
  if (args[0] === "review" && args[1] === "agent") return args.length === 4 && args[2] === "status" && args[3] === "--json" || args.length === 6 && args[2] === "configure" && args[3] === "--executable" && absoluteExecutable(args[4]) && args[5] === "--json";
  if (args[0] === "decisions" && args[1] === "extract") {
    if (args.length === 7 && args[2] === "--project-id" && PROJECT_ID.test(args[3]) && args[4] === "--expected-generation-id" && SCAN_GENERATION_ID.test(args[5]) && args[6] === "--json") return true;
    if (args.length === 6 && args[2] === "status" && args[5] === "--json") return args[3] === "--job-id" && INSPECT_ID.test(args[4]) || args[3] === "--project-id" && PROJECT_ID.test(args[4]);
    return args.length === 8 && args[2] === "cancel" && args[3] === "--job-id" && INSPECT_ID.test(args[4]) && args[5] === "--expected-revision" && validPositive(args[6]) && args[7] === "--json";
  }
  if (args.length === 6 && args[0] === "decisions" && args[1] === "candidates" && args[2] === "list" && args[3] === "--project-id" && PROJECT_ID.test(args[4]) && args[5] === "--json") return true;
  if (args.length === 14 && args[0] === "decisions" && args[1] === "candidate" && args[2] === "transition" && args[3] === "--project-id" && PROJECT_ID.test(args[4]) && args[5] === "--candidate-id" && INSPECT_ID.test(args[6]) && args[7] === "--expected-revision" && validPositive(args[8]) && args[9] === "--action" && ["confirm", "ignore", "not_decision", "restore"].includes(args[10]) && args[11] === "--expected-review-sha256" && /^[0-9a-f]{64}$/.test(args[12]) && args[13] === "--json") return true;
  if (args[0] === "decisions" && ["create", "edit"].includes(args[1]) && args[2] === "--project-id" && PROJECT_ID.test(args[3]) && args[4] === "--expected-review-sha256" && /^[0-9a-f]{64}$/.test(args[5])) {
    return args[1] === "create" ? args.length === 7 && args[6] === "--json" : args.length === 11 && args[6] === "--decision-id" && ENTITY_ID.test(args[7]) && args[8] === "--expected-decision-revision" && validPositive(args[9]) && args[10] === "--json";
  }
  if (args.join("\0") === "pricing\0catalog\0list\0--json") return true;
  if (args[0] === "pricing") {
    const offset = args[1] === "supplement" ? 2 : args[1] === "catalog" && args[2] === "accept" ? 3 : 0;
    const rest = args.slice(offset);
    return offset > 0 && rest.length === 11 && rest[0] === "--project-id" && PROJECT_ID.test(rest[1]) && rest[2] === "--provider" && INSPECT_ID.test(rest[3]) && rest[4] === "--session-id" && INSPECT_ID.test(rest[5]) && rest[6] === "--usage-record-digest" && DIGEST.test(rest[7]) && rest[8] === "--expected-ledger-sha256" && /^[0-9a-f]{64}$/.test(rest[9]) && rest[10] === "--json";
  }
  if ((args.length === 13 || args.length === 15) && args[0] === "inspect" && args[1] === "session-search" && args[2] === "--project-id" && PROJECT_ID.test(args[3] ?? "") && args[4] === "--expected-generation-id" && INSPECT_ID.test(args[5] ?? "") && args[6] === "--query-kind" && ["branch", "file", "error"].includes(args[7]) && args[8] === "--query" && Boolean(args[9]) && args[10] === "--limit" && /^(?:[1-9][0-9]?|100)$/.test(args[11]) && args[12] === "--json") return args.length === 13 || (args[13] === "--cursor" && boundedCursor(args[14]));
  if (args.length === 2 && args[0] === "version" && args[1] === "--json") return true;
  if (args.length === 5 && args[0] === "sync" && args[1] === "status" && args[2] === "--json" && args[3] === "--project-id") return PROJECT_ID.test(args[4] ?? "");
  if (args.length === 4 && args[0] === "sync" && args[1] === "--dry-run" && args[2] === "--project-id") return PROJECT_ID.test(args[3] ?? "");
  if (args.length === 4 && args[0] === "sync" && args[1] === "repair-machine-ledger" && args[2] === "--project-id") return PROJECT_ID.test(args[3] ?? "");
  if (args.length === 8 && args[0] === "sync" && args[1] === "resolve" && args[2] === "--conflict" && CONFLICT_ID.test(args[3] ?? "") && args[4] === "--action" && (args[5] === "accept_project" || args[5] === "accept_obsidian") && args[6] === "--project-id") return PROJECT_ID.test(args[7] ?? "");
  if (args.length === 10 && args[0] === "sync" && args[1] === "resolve" && args[2] === "--conflict" && CONFLICT_ID.test(args[3] ?? "") && args[4] === "--action" && args[5] === "manual_merge" && args[6] === "--file" && args[8] === "--project-id") return Boolean(args[7]) && PROJECT_ID.test(args[9] ?? "");
  if (args.length === 3 && args[0] === "sync" && args[1] === "--project-id") return PROJECT_ID.test(args[2] ?? "");
  if (args.length === 6 && args[0] === "problems" && args[1] === "candidates" && args[2] === "list" && args[3] === "--project-id" && args[5] === "--json") return PROJECT_ID.test(args[4] ?? "");
  if (args.length === 9 && args[0] === "problems" && args[1] === "create" && args[2] === "--project-id" && PROJECT_ID.test(args[3] ?? "") && args[4] === "--expected-problem-map-revision" && validNonnegative(args[5]) && args[6] === "--expected-review-sha256" && /^[0-9a-f]{64}$/.test(args[7] ?? "") && args[8] === "--json") return true;
  if (args.length === 15 && args[0] === "problems" && args[1] === "state" && args[2] === "--project-id" && PROJECT_ID.test(args[3] ?? "") && args[4] === "--problem-id" && ENTITY_ID.test(args[5] ?? "") && args[6] === "--expected-problem-revision" && validPositive(args[7]) && args[8] === "--expected-problem-map-revision" && validNonnegative(args[9]) && args[10] === "--expected-review-sha256" && /^[0-9a-f]{64}$/.test(args[11] ?? "") && args[12] === "--action" && (args[13] === "resolve" || args[13] === "reopen") && args[14] === "--json") return true;
  if (args.length === 13 && args[0] === "problems" && args[1] === "edit" && args[2] === "--project-id" && PROJECT_ID.test(args[3] ?? "") && args[4] === "--problem-id" && ENTITY_ID.test(args[5] ?? "") && args[6] === "--expected-problem-revision" && validPositive(args[7]) && args[8] === "--expected-problem-map-revision" && validNonnegative(args[9]) && args[10] === "--expected-review-sha256" && /^[0-9a-f]{64}$/.test(args[11] ?? "") && args[12] === "--json") return true;
  if (args.length === 15 && args[0] === "problems" && args[1] === "move" && args[2] === "--project-id" && PROJECT_ID.test(args[3] ?? "") && args[4] === "--problem-id" && ENTITY_ID.test(args[5] ?? "") && args[6] === "--new-parent-id" && (args[7] === "root" || ENTITY_ID.test(args[7] ?? "")) && args[8] === "--expected-problem-revision" && validPositive(args[9]) && args[10] === "--expected-problem-map-revision" && validNonnegative(args[11]) && args[12] === "--expected-review-sha256" && /^[0-9a-f]{64}$/.test(args[13] ?? "") && args[14] === "--json") return true;
  if (args.length === 11 && args[0] === "problems" && args[1] === "reorder" && args[2] === "--project-id" && PROJECT_ID.test(args[3] ?? "") && args[4] === "--parent-id" && (args[5] === "root" || ENTITY_ID.test(args[5] ?? "")) && args[6] === "--expected-problem-map-revision" && validNonnegative(args[7]) && args[8] === "--expected-review-sha256" && /^[0-9a-f]{64}$/.test(args[9] ?? "") && args[10] === "--json") return true;
  if ((args.length === 16 || args.length === 18) && args[0] === "problems" && args[1] === "candidate" && args[2] === "transition" && args[3] === "--project-id" && PROJECT_ID.test(args[4] ?? "") && args[5] === "--candidate-id" && ENTITY_ID.test(args[6] ?? "") && args[7] === "--expected-candidate-revision" && validPositive(args[8]) && args[9] === "--expected-problem-map-revision" && validNonnegative(args[10]) && args[11] === "--expected-review-sha256" && /^[0-9a-f]{64}$/.test(args[12] ?? "") && args[13] === "--action") {
    const action = args[14]; const target = args.length === 18 && args[15] === "--target-problem-id" && ENTITY_ID.test(args[16] ?? "") && args[17] === "--json";
    return args.length === 16 ? ["apply_root", "keep_pending", "dismiss", "restore"].includes(action ?? "") && args[15] === "--json" : target && ["apply_child", "apply_sibling", "merge"].includes(action ?? "");
  }
  if (args.length === 5 && args[0] === "scan" && args[1] === "start" && args[2] === "--project-id" && args[4] === "--json") return PROJECT_ID.test(args[3] ?? "");
  if (args.length === 5 && args[0] === "scan" && args[1] === "status" && args[2] === "--project-id" && args[4] === "--json") return PROJECT_ID.test(args[3] ?? "");
  if (args.length === 11 && args[0] === "inspect" && args[1] === "session-summary" &&
      args[2] === "--project-id" && PROJECT_ID.test(args[3] ?? "") && args[4] === "--provider" && INSPECT_ID.test(args[5] ?? "") &&
      args[6] === "--session-id" && INSPECT_ID.test(args[7] ?? "") && args[8] === "--expected-generation-id" && INSPECT_ID.test(args[9] ?? "") &&
      args[10] === "--json") return true;
  if ((args.length === 13 || args.length === 15) && args[0] === "inspect" && args[1] === "session-events" &&
      args[2] === "--project-id" && PROJECT_ID.test(args[3] ?? "") && args[4] === "--provider" && INSPECT_ID.test(args[5] ?? "") &&
      args[6] === "--session-id" && INSPECT_ID.test(args[7] ?? "") && args[8] === "--expected-generation-id" && INSPECT_ID.test(args[9] ?? "") &&
      args[10] === "--limit" && validInspectLimit(args[11]) && args[12] === "--json") {
    if (args.length === 13) return true;
    if (args[13] === "--cursor") return boundedCursor(args[14]);
    return args[13] === "--anchor" && validAnchor(args[14]);
  }
  if (args.length >= 13 && args.length <= 19 && args[0] === "inspect" && args[1] === "conversation-chain" &&
      args[2] === "--project-id" && PROJECT_ID.test(args[3] ?? "") && args[4] === "--provider" && INSPECT_ID.test(args[5] ?? "") &&
      args[6] === "--session-id" && INSPECT_ID.test(args[7] ?? "") && args[8] === "--expected-generation-id" && INSPECT_ID.test(args[9] ?? "") &&
      args[10] === "--limit" && validConversationLimit(args[11]) && args[12] === "--json") {
	const rest = args.slice(13);
	let index = 0;
	if (rest[index] === "--turn-unit-id" && INSPECT_ID.test(rest[index + 1] ?? "")) index += 2;
	else if (rest[index] === "--cursor" && boundedCursor(rest[index + 1])) index += 2;
	if (rest[index] === "--message-cursor" && boundedCursor(rest[index + 1]) && rest[0] === "--turn-unit-id") index += 2;
	if (rest[index] === "--session-view-digest" && DIGEST.test(rest[index + 1] ?? "")) index += 2;
	return index === rest.length;
  }
  return false;
}

function validateProblemCAS(request: ProblemCAS): void { validateProject(request.projectId); if (!Number.isSafeInteger(request.expectedProblemMapRevision) || request.expectedProblemMapRevision < 0) throw new Error("invalid problem map revision"); if (!/^[0-9a-f]{64}$/.test(request.expectedReviewSHA256)) throw new Error("invalid review SHA"); }
function validateProblemNodeIdentity(problem: ProblemNodeV4): void { if (!ENTITY_ID.test(problem.id) || !Number.isSafeInteger(problem.revision) || problem.revision < 1) throw new Error("invalid problem node"); }
function validNonnegative(value: string | undefined): boolean { return Boolean(value && /^(?:0|[1-9][0-9]*)$/.test(value) && Number.isSafeInteger(Number(value))); }
function validPositive(value: string | undefined): boolean { return Boolean(value && /^[1-9][0-9]*$/.test(value) && Number.isSafeInteger(Number(value))); }
function validProblemSourceRef(value: unknown): boolean { if (typeof value !== "object" || value === null || Array.isArray(value)) return false; const ref = value as Record<string, unknown>; return matchesString(INSPECT_ID, ref.provider ?? "") && matchesString(INSPECT_ID, ref.session_id ?? "") && matchesString(INSPECT_ID, ref.turn_unit_id ?? "") && (ref.session_view_digest === undefined || matchesString(DIGEST, ref.session_view_digest)); }
function validCandidate(value: unknown, projectId: string): value is ProblemCandidate {
	if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
	const candidate = value as Record<string, unknown>;
	const stringIDs = (item: unknown, max: number): item is string[] => Array.isArray(item) && item.length <= max && item.every((id) => matchesString(ENTITY_ID, id));
	const grounds = Array.isArray(candidate.grounds) && candidate.grounds.length <= 256 && candidate.grounds.every((ground) => typeof ground === "object" && ground !== null && !Array.isArray(ground) && matchesString(INSPECT_ID, (ground as Record<string, unknown>).rule_id ?? "") && matchesString(INSPECT_ID, (ground as Record<string, unknown>).rule_version ?? "") && stringIDs((ground as Record<string, unknown>).matched_fact_refs, 256) && typeof (ground as Record<string, unknown>).explanation === "string");
	const refs = Array.isArray(candidate.source_turn_refs) && candidate.source_turn_refs.length <= 256 && candidate.source_turn_refs.every(validProblemSourceRef);
	const dependencies = Array.isArray(candidate.dependency_digests) && candidate.dependency_digests.length <= 256 && candidate.dependency_digests.every((digest) => matchesString(DIGEST, digest));
	return candidate.project_id === projectId && matchesString(ENTITY_ID, candidate.candidate_id ?? "") && typeof candidate.question === "string" && candidate.question.length > 0 && candidate.question.length <= 4096 && refs &&
		["child", "sibling", "merge", "keep_pending"].includes(String(candidate.recommended_relation)) && (candidate.recommended_target_id === null || matchesString(ENTITY_ID, candidate.recommended_target_id)) && stringIDs(candidate.alternate_target_ids, 2) && stringIDs(candidate.related_node_ids, 2) && grounds &&
		["high", "medium", "low"].includes(String(candidate.confidence)) && ["pending", "applied", "merged", "kept_pending", "stale", "dismissed"].includes(String(candidate.status)) && dependencies && ["deterministic", "agent_requested"].includes(String(candidate.analysis_mode)) && (candidate.agent_run_id === null || matchesString(INSPECT_ID, candidate.agent_run_id)) && Number.isSafeInteger(candidate.revision) && Number(candidate.revision) >= 1 && typeof candidate.created_at === "string" && typeof candidate.updated_at === "string";
}

function validateSessionSummaryRequest(request: SessionSummaryRequest): void {
  validateProject(request.projectId);
  if (!INSPECT_ID.test(request.provider)) throw new Error("invalid provider");
  if (!INSPECT_ID.test(request.sessionId)) throw new Error("invalid Session ID");
  if (!INSPECT_ID.test(request.expectedGenerationId)) throw new Error("invalid generation ID");
  if (!DIGEST.test(request.expectedSessionViewDigest)) throw new Error("invalid Session view digest");
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

function sessionEventPageMatchesRequest(page: SessionEventPageV1, request: SessionEventRequest): boolean {
  const pageLength = page.range_end - page.range_start;
  if (pageLength > request.limit || (page.range_end < page.total && pageLength !== request.limit)) return false;
  if (request.cursor === undefined && request.anchor === undefined && page.range_start !== 0) return false;
  if (request.anchor !== undefined && !(page.range_start < request.anchor && request.anchor <= page.range_end)) return false;
  return true;
}

function sessionInspectErrorCode(error: unknown): SessionInspectErrorCode {
  const stdout = (error as { stdout?: unknown } | null)?.stdout;
  if (typeof stdout !== "string" || Buffer.byteLength(stdout, "utf8") > (1 << 20)) return "unavailable";
  try {
    return parseSessionInspectWireError(stdout);
  } catch {
    return "unavailable";
  }
}

function validateConversationRequest(request: ConversationRequest): void {
  validateProject(request.projectId);
  if (!INSPECT_ID.test(request.provider)) throw new Error("invalid provider");
  if (!INSPECT_ID.test(request.sessionId)) throw new Error("invalid Session ID");
  if (!INSPECT_ID.test(request.expectedGenerationId)) throw new Error("invalid generation ID");
  if (!DIGEST.test(request.expectedSessionViewDigest)) throw new Error("invalid Session view digest");
	if (request.sessionViewDigest !== undefined && !DIGEST.test(request.sessionViewDigest)) throw new Error("invalid selected Session view digest");
  if (!Number.isSafeInteger(request.limit) || request.limit < 1 || request.limit > 64) throw new Error("invalid conversation page limit");
  if (request.turnUnitId !== undefined && !INSPECT_ID.test(request.turnUnitId)) throw new Error("invalid turn unit ID");
  if (request.cursor !== undefined && request.turnUnitId !== undefined) throw new Error("index cursor cannot select a turn");
  if (request.messageCursor !== undefined && request.turnUnitId === undefined) throw new Error("message cursor requires a turn");
  if (request.cursor !== undefined && !boundedCursor(request.cursor)) throw new Error("invalid cursor");
  if (request.messageCursor !== undefined && !boundedCursor(request.messageCursor)) throw new Error("invalid message cursor");
}

function conversationPageMatchesRequest(page: ConversationPageV1, request: ConversationRequest): boolean {
  const pageLength = page.range_end - page.range_start;
  const requestCursor = request.turnUnitId === undefined ? request.cursor : request.messageCursor;
  if (pageLength > request.limit || (requestCursor === undefined && page.range_start !== 0)) return false;
  if (page.total === 0) return true;
  return pageLength > 0 && page.first_cursor !== null && page.last_cursor !== null &&
    (page.previous_cursor === null) === (page.range_start === 0) &&
    (page.next_cursor === null) === (page.range_end === page.total);
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
      if (code === "source_unavailable" || code === "retained_evidence_unavailable" || code === "retained_evidence_ambiguous" || code === "visible_reader_unsupported" || code === "generation_mismatch" || code === "stale_cursor" || code === "unsupported_provider") return code;
    } catch { /* The generic bounded error below is intentional. */ }
  }
  return "conversation_failed";
}

function conversationErrorMessage(code: ConversationErrorCode): string {
  return {
    source_unavailable: "问答来源暂不可用；现有执行事实仍可阅读。",
    retained_evidence_unavailable: "未找到与当前 Session 一致的保留问答证据。",
    retained_evidence_ambiguous: "找到多份无法自动区分的保留问答证据。",
    visible_reader_unsupported: "当前来源的问答读取器不可用；这不表示 Session 没有 Agent 回答。",
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

function matchesString(pattern: RegExp, value: unknown): value is string { return typeof value === "string" && pattern.test(value); }
