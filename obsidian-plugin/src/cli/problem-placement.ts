import type { ProblemMapCandidateV1 } from "../contracts/review-v4";

type ProblemCandidateV1 = ProblemMapCandidateV1["candidates"][number];

export type ProblemPlacementState = "queued" | "running" | "cancel_requested" | "completed" | "failed" | "cancelled";

export interface ProblemPlacementJob {
  schema_version: 1;
  job_id: string;
  project_id: string;
  candidate_id: string;
  state: ProblemPlacementState;
  revision: number;
  result_candidate_revision: number;
  error_code: string | null;
  can_cancel: boolean;
}

export interface ProblemPlacementActions {
  start: (candidate: ProblemCandidateV1) => Promise<ProblemPlacementJob>;
  status: (jobId: string) => Promise<ProblemPlacementJob>;
  cancel: (jobId: string, revision: number) => Promise<ProblemPlacementJob>;
  latest?: (candidate: ProblemCandidateV1) => Promise<ProblemPlacementJob | undefined>;
}

export function parseProblemPlacementJob(value: unknown, projectId: string, candidateId?: string): ProblemPlacementJob {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("invalid problem placement job");
  const row = value as Record<string, unknown>;
  const allowed = new Set(["schema_version", "job_id", "project_id", "candidate_id", "state", "revision", "result_candidate_revision", "error_code", "can_cancel"]);
  if (Object.keys(row).some((key) => !allowed.has(key)) || row.schema_version !== 1 || row.project_id !== projectId || candidateId !== undefined && row.candidate_id !== candidateId || typeof row.job_id !== "string" || !safeId(row.job_id) || typeof row.candidate_id !== "string" || !safeId(row.candidate_id) || typeof row.state !== "string" || !["queued", "running", "cancel_requested", "completed", "failed", "cancelled"].includes(row.state) || !wireInteger(row.revision, 1) || !wireInteger(row.result_candidate_revision, 0) || row.error_code !== null && typeof row.error_code !== "string" || typeof row.can_cancel !== "boolean") {
    throw new Error("invalid problem placement job");
  }
  const active = row.state === "queued" || row.state === "running";
  if (row.can_cancel !== active || (row.state === "completed") !== ((row.result_candidate_revision as number) > 0)) throw new Error("inconsistent problem placement job");
  return row as unknown as ProblemPlacementJob;
}

function safeId(value: string): boolean { return value.length > 0 && value.length <= 256 && /^[A-Za-z0-9][A-Za-z0-9._:-]*$/.test(value); }
function wireInteger(value: unknown, minimum: number): value is number { return typeof value === "number" && Number.isSafeInteger(value) && value >= minimum; }
