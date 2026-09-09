export interface DecisionExtractionJob {
  schema_version: 1;
  job_id: string;
  project_id: string;
  generation_id: string;
  state: "queued" | "running" | "completed" | "failed" | "cancelled";
  revision: number;
  pid: number;
  dependency_digests: string[];
  candidate_count: number;
  error_code: string;
  created_at: string;
  updated_at: string;
}
export interface DecisionExtractionActions {
  start: () => Promise<DecisionExtractionJob>;
  status: (id: string) => Promise<DecisionExtractionJob>;
  cancel: (id: string, revision: number) => Promise<DecisionExtractionJob>;
  currentGenerationId?: string;
  initialJob?: DecisionExtractionJob;
  latest?: () => Promise<DecisionExtractionJob | undefined>;
  configuration?: (executable?: string) => Promise<DecisionAgentConfiguration>;
}

export interface DecisionAgentConfiguration {
  schema_version: number;
  kind: string;
  compatible: boolean;
  version?: string;
  executable?: string;
  error_code?: string;
}

export function validateDecisionJob(value: unknown, projectId?: string, jobId?: string): DecisionExtractionJob {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("invalid extraction job");
  const row = value as Record<string, unknown>;
  if (
    Object.keys(row).sort().join(",") !== "candidate_count,created_at,dependency_digests,error_code,generation_id,job_id,pid,project_id,revision,schema_version,state,updated_at" ||
    row.schema_version !== 1 ||
    typeof row.job_id !== "string" || !/^decision-extract-[a-z0-9._-]+$/.test(row.job_id) ||
    typeof row.project_id !== "string" || typeof row.generation_id !== "string" ||
    !["queued", "running", "completed", "failed", "cancelled"].includes(String(row.state)) ||
    !Number.isSafeInteger(row.revision) || Number(row.revision) < 1 ||
    !Number.isSafeInteger(row.pid) || Number(row.pid) < 0 ||
    !Number.isSafeInteger(row.candidate_count) || Number(row.candidate_count) < 0 ||
    typeof row.error_code !== "string" ||
    typeof row.created_at !== "string" || typeof row.updated_at !== "string" ||
    !Array.isArray(row.dependency_digests) || !row.dependency_digests.every(d => typeof d === "string" && /^sha256:[0-9a-f]{64}$/.test(d))
  ) throw new Error("invalid extraction job");
  if ((projectId && row.project_id !== projectId) || (jobId && row.job_id !== jobId)) throw new Error("extraction identity mismatch");
  return value as DecisionExtractionJob;
}
