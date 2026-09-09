package problemmap

import (
	"errors"
	"sort"
	"time"

	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

const (
	AgentProposalVersion = "problem-placement-proposal-v1"
	MaxAgentPromptBytes  = 128 << 10
	MaxAgentOutputBytes  = 64 << 10
)

var agentPlacementSchema = []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,"required":["schema_version","contract","recommended_relation","recommended_target_id","alternate_target_ids","related_node_ids","grounds","confidence"],"properties":{"schema_version":{"const":1},"contract":{"const":"problem-placement-proposal-v1"},"recommended_relation":{"enum":["child","sibling","merge","keep_pending"]},"recommended_target_id":{"type":["string","null"]},"alternate_target_ids":{"type":"array","maxItems":2,"items":{"type":"string"}},"related_node_ids":{"type":"array","maxItems":2,"items":{"type":"string"}},"grounds":{"type":"array","minItems":1,"maxItems":8,"items":{"type":"object","additionalProperties":false,"required":["explanation","matched_problem_ids"],"properties":{"explanation":{"type":"string","minLength":1,"maxLength":4096},"matched_problem_ids":{"type":"array","maxItems":8,"items":{"type":"string"}}}}},"confidence":{"enum":["high","medium","low"]}}}`)

type placementPrompt struct {
	SchemaVersion int                      `json:"schema_version" required:"true"`
	Contract      string                   `json:"contract" required:"true"`
	ProjectID     string                   `json:"project_id" required:"true"`
	CandidateID   string                   `json:"candidate_id" required:"true"`
	Question      string                   `json:"question" required:"true"`
	SourceTurns   []reviewv4.SourceTurnRef `json:"source_turn_refs" required:"true"`
	Problems      []placementPromptNode    `json:"problems" required:"true"`
}

type placementPromptNode struct {
	ID              string  `json:"id" required:"true"`
	Question        string  `json:"question" required:"true"`
	PrimaryParentID *string `json:"primary_parent_id" required:"true" nullable:"true"`
	WorkflowState   string  `json:"workflow_state" required:"true"`
	SiblingOrder    int     `json:"sibling_order" required:"true"`
	Revision        int     `json:"revision" required:"true"`
}

type placementProposal struct {
	SchemaVersion       int               `json:"schema_version" required:"true"`
	Contract            string            `json:"contract" required:"true"`
	RecommendedRelation Relation          `json:"recommended_relation" required:"true"`
	RecommendedTargetID *string           `json:"recommended_target_id" required:"true" nullable:"true"`
	AlternateTargetIDs  []string          `json:"alternate_target_ids" required:"true"`
	RelatedNodeIDs      []string          `json:"related_node_ids" required:"true"`
	Grounds             []placementGround `json:"grounds" required:"true"`
	Confidence          Confidence        `json:"confidence" required:"true"`
}

type placementGround struct {
	Explanation       string   `json:"explanation" required:"true"`
	MatchedProblemIDs []string `json:"matched_problem_ids" required:"true"`
}

func BuildAgentPlacementPrompt(candidate Candidate, graph []reviewv4.ProblemNode) ([]byte, []byte, error) {
	if candidate.Question == "" || len(candidate.Question) > 4096 || candidate.Status != CandidatePending && candidate.Status != CandidateKeptPending {
		return nil, nil, errors.New("placement candidate is not eligible for Agent analysis")
	}
	nodes := make([]placementPromptNode, len(graph))
	for index, node := range graph {
		nodes[index] = placementPromptNode{ID: node.ID, Question: node.Question, PrimaryParentID: node.PrimaryParentID, WorkflowState: node.WorkflowState, SiblingOrder: node.SiblingOrder, Revision: node.Revision}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	prompt, err := strictjson.Encode(placementPrompt{SchemaVersion: 1, Contract: "problem-placement-input-v1", ProjectID: candidate.ProjectID, CandidateID: candidate.CandidateID, Question: candidate.Question, SourceTurns: cloneNonNilSlice(candidate.SourceTurnRefs), Problems: nodes})
	if err != nil || len(prompt) > MaxAgentPromptBytes {
		return nil, nil, errors.Join(errors.New("placement prompt exceeds its bound"), err)
	}
	return prompt, append([]byte(nil), agentPlacementSchema...), nil
}

func ParseAgentPlacementProposal(body []byte, candidate Candidate, graph []reviewv4.ProblemNode, runID string, now time.Time) (Candidate, error) {
	if len(body) == 0 || len(body) > MaxAgentOutputBytes || !validID(runID) || now.IsZero() {
		return Candidate{}, errors.New("placement proposal identity is invalid")
	}
	var proposal placementProposal
	if err := strictjson.Decode(body, &proposal); err != nil {
		return Candidate{}, err
	}
	if proposal.SchemaVersion != 1 || proposal.Contract != AgentProposalVersion || proposal.AlternateTargetIDs == nil || proposal.RelatedNodeIDs == nil || proposal.Grounds == nil || len(proposal.Grounds) == 0 || len(proposal.Grounds) > 8 || len(proposal.AlternateTargetIDs) > 2 || len(proposal.RelatedNodeIDs) > 2 {
		return Candidate{}, errors.New("placement proposal shape is invalid")
	}
	allowed := make(map[string]bool, len(graph))
	for _, node := range graph {
		allowed[node.ID] = true
	}
	validateIDs := func(values []string) bool {
		seen := map[string]bool{}
		for _, id := range values {
			if !allowed[id] || seen[id] {
				return false
			}
			seen[id] = true
		}
		return true
	}
	if !validateIDs(proposal.AlternateTargetIDs) || !validateIDs(proposal.RelatedNodeIDs) {
		return Candidate{}, errors.New("placement proposal cites a foreign or duplicate problem")
	}
	switch proposal.RecommendedRelation {
	case RelationChild, RelationSibling, RelationMerge:
		if proposal.RecommendedTargetID == nil || !allowed[*proposal.RecommendedTargetID] {
			return Candidate{}, errors.New("placement proposal target is invalid")
		}
	case RelationKeepPending:
		if proposal.RecommendedTargetID != nil {
			return Candidate{}, errors.New("pending placement proposal has a target")
		}
	default:
		return Candidate{}, errors.New("placement proposal relation is invalid")
	}
	if proposal.Confidence != ConfidenceHigh && proposal.Confidence != ConfidenceMedium && proposal.Confidence != ConfidenceLow {
		return Candidate{}, errors.New("placement proposal confidence is invalid")
	}
	grounds := make([]Ground, len(proposal.Grounds))
	for index, ground := range proposal.Grounds {
		if ground.Explanation == "" || len(ground.Explanation) > 4096 || !validateIDs(ground.MatchedProblemIDs) {
			return Candidate{}, errors.New("placement proposal ground is invalid")
		}
		grounds[index] = Ground{RuleID: "agent-proposal", RuleVersion: AgentProposalVersion, MatchedFactRefs: cloneNonNilSlice(ground.MatchedProblemIDs), Explanation: ground.Explanation}
	}
	result := candidate
	result.RecommendedRelation, result.RecommendedTargetID = proposal.RecommendedRelation, proposal.RecommendedTargetID
	result.AlternateTargetIDs, result.RelatedNodeIDs, result.Grounds, result.Confidence = cloneNonNilSlice(proposal.AlternateTargetIDs), cloneNonNilSlice(proposal.RelatedNodeIDs), grounds, proposal.Confidence
	result.AnalysisMode, result.AgentRunID, result.Revision, result.UpdatedAt = AnalysisAgentRequested, &runID, candidate.Revision+1, now.UTC().Round(0).Format(time.RFC3339Nano)
	return result, nil
}
