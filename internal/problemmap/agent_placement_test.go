package problemmap

import (
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func TestAgentPlacementPromptAndProposalAreBoundedToExistingGraph(t *testing.T) {
	candidate := completeCandidate("candidate-a")
	node := graphProblemNode("problem-a", nil, 0)
	prompt, schema, err := BuildAgentPlacementPrompt(candidate, []reviewv4.ProblemNode{node})
	if err != nil || !strings.Contains(string(prompt), candidate.Question) || strings.Contains(string(prompt), "current_conclusion") || !strings.Contains(string(schema), AgentProposalVersion) {
		t.Fatalf("prompt=%s schema=%s err=%v", prompt, schema, err)
	}
	body := []byte(`{"schema_version":1,"contract":"problem-placement-proposal-v1","recommended_relation":"child","recommended_target_id":"problem-a","alternate_target_ids":[],"related_node_ids":[],"grounds":[{"explanation":"The candidate explicitly refines the existing question.","matched_problem_ids":["problem-a"]}],"confidence":"medium"}`)
	got, err := ParseAgentPlacementProposal(body, candidate, []reviewv4.ProblemNode{node}, "problem-job-a", time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC))
	if err != nil || got.AnalysisMode != AnalysisAgentRequested || got.AgentRunID == nil || got.Revision != candidate.Revision+1 || got.RecommendedTargetID == nil || *got.RecommendedTargetID != node.ID {
		t.Fatalf("candidate=%+v err=%v", got, err)
	}
	foreign := strings.Replace(string(body), "problem-a", "problem-foreign", 1)
	if _, err := ParseAgentPlacementProposal([]byte(foreign), candidate, []reviewv4.ProblemNode{node}, "problem-job-a", time.Now()); err == nil {
		t.Fatal("foreign target accepted")
	}
	extra := strings.Replace(string(body), `"confidence":"medium"`, `"confidence":"medium","prompt":"hidden"`, 1)
	if _, err := ParseAgentPlacementProposal([]byte(extra), candidate, []reviewv4.ProblemNode{node}, "problem-job-a", time.Now()); err == nil {
		t.Fatal("extra output accepted")
	}
}
