import type { AgentAnnotationEntryV1 } from "../contracts/review-v4";
export interface DecisionEvidenceRef {
  provider: string;
  session_id: string;
  session_view_digest: string;
  turn_unit_id: string;
  revision_id: string;
}
export interface DecisionCandidateEvidence {
  candidate_id: string;
  evidence_refs: DecisionEvidenceRef[];
  error_code: "" | "candidate_stale";
}
export interface DecisionCandidatePage {
  candidates: AgentAnnotationEntryV1[];
  evidence: DecisionCandidateEvidence[];
}
