package problemmap

import "github.com/neomei/SessionReviewer/internal/reviewv4"

const MaxWireInteger int64 = 1<<53 - 1

type Relation string

const (
	RelationChild       Relation = "child"
	RelationSibling     Relation = "sibling"
	RelationMerge       Relation = "merge"
	RelationKeepPending Relation = "keep_pending"
)

type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

type CandidateStatus string

const (
	CandidatePending     CandidateStatus = "pending"
	CandidateApplied     CandidateStatus = "applied"
	CandidateMerged      CandidateStatus = "merged"
	CandidateKeptPending CandidateStatus = "kept_pending"
	CandidateStale       CandidateStatus = "stale"
	CandidateDismissed   CandidateStatus = "dismissed"
)

type AnalysisMode string

const (
	AnalysisDeterministic  AnalysisMode = "deterministic"
	AnalysisAgentRequested AnalysisMode = "agent_requested"
)

type Ground struct {
	RuleID          string   `json:"rule_id" required:"true"`
	RuleVersion     string   `json:"rule_version" required:"true"`
	MatchedFactRefs []string `json:"matched_fact_refs" required:"true"`
	Explanation     string   `json:"explanation" required:"true"`
}

type Candidate struct {
	CandidateID         string                   `json:"candidate_id" required:"true"`
	ProjectID           string                   `json:"project_id" required:"true"`
	Question            string                   `json:"question" required:"true"`
	SourceTurnRefs      []reviewv4.SourceTurnRef `json:"source_turn_refs" required:"true"`
	RecommendedRelation Relation                 `json:"recommended_relation" required:"true"`
	RecommendedTargetID *string                  `json:"recommended_target_id" required:"true" nullable:"true"`
	AlternateTargetIDs  []string                 `json:"alternate_target_ids" required:"true"`
	RelatedNodeIDs      []string                 `json:"related_node_ids" required:"true"`
	Grounds             []Ground                 `json:"grounds" required:"true"`
	Confidence          Confidence               `json:"confidence" required:"true"`
	Status              CandidateStatus          `json:"status" required:"true"`
	DependencyDigests   []string                 `json:"dependency_digests" required:"true"`
	AnalysisMode        AnalysisMode             `json:"analysis_mode" required:"true"`
	AgentRunID          *string                  `json:"agent_run_id" required:"true" nullable:"true"`
	Revision            int                      `json:"revision" required:"true"`
	CreatedAt           string                   `json:"created_at" required:"true"`
	UpdatedAt           string                   `json:"updated_at" required:"true"`
}

type CandidateStore struct {
	SchemaVersion        int         `json:"schema_version" required:"true"`
	MinimumReaderVersion string      `json:"minimum_reader_version" required:"true"`
	Digest               string      `json:"digest" required:"true"`
	ProjectID            string      `json:"project_id" required:"true"`
	Candidates           []Candidate `json:"candidates" required:"true"`
}

type MovePreview struct {
	ProblemID       string   `json:"problem_id" required:"true"`
	OldPath         []string `json:"old_path" required:"true"`
	NewPath         []string `json:"new_path" required:"true"`
	AffectedNodeIDs []string `json:"affected_node_ids" required:"true"`
}
