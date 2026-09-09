package problemmap

import (
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func TestDiscoverCandidatesPreservesProviderQualifiedRefsAndUsesNoAgent(t *testing.T) {
	now := time.Date(2026, 9, 9, 1, 2, 3, 0, time.UTC)
	chain := func(provider string) ChainInput {
		digest := "sha256:" + map[string]string{"codex": string(make([]byte, 0)), "claude": string(make([]byte, 0))}[provider]
		if provider == "codex" {
			digest = "sha256:" + ruleHex("a")
		} else {
			digest = "sha256:" + ruleHex("b")
		}
		view := "sha256:" + ruleHex(map[string]string{"codex": "c", "claude": "d"}[provider])
		return ChainInput{Digest: digest, Document: conversationchain.Document{ProjectID: "project-a", Provider: provider, SessionID: "same", SessionViewDigest: view, Digest: digest, TurnUnits: []conversationchain.TurnUnit{{TurnUnitID: "turn-1", UserMessage: conversationchain.Message{VisibleExcerpt: "How should this work?"}}}}}
	}
	got := DiscoverCandidates("project-a", []ChainInput{chain("codex"), chain("claude")}, nil, now)
	if len(got) != 2 {
		t.Fatalf("candidates=%+v", got)
	}
	for _, candidate := range got {
		if candidate.AnalysisMode != AnalysisDeterministic || candidate.AgentRunID != nil || candidate.RecommendedRelation != RelationKeepPending || len(candidate.SourceTurnRefs) != 1 || candidate.SourceTurnRefs[0].SessionViewDigest == "" {
			t.Fatalf("candidate=%+v", candidate)
		}
	}
}

func TestRecommendPlacementUsesOnlyExactSignals(t *testing.T) {
	now := time.Date(2026, 9, 9, 1, 2, 3, 0, time.UTC)
	node := graphProblemNode("problem:parent", nil, 0)
	node.Question = "How is publication atomic?"
	merged := RecommendPlacement(PlacementInput{ProjectID: "project-a", Question: node.Question, Graph: []reviewv4.ProblemNode{node}, Dependencies: []string{"sha256:" + ruleHex("a")}, Now: now})
	if merged.RecommendedRelation != RelationMerge || merged.RecommendedTargetID == nil || *merged.RecommendedTargetID != node.ID {
		t.Fatalf("merge=%+v", merged)
	}
	pending := RecommendPlacement(PlacementInput{ProjectID: "project-a", Question: "How is publication safe?", Graph: []reviewv4.ProblemNode{node}, Dependencies: []string{"sha256:" + ruleHex("a")}, Now: now})
	if pending.RecommendedRelation != RelationKeepPending || pending.AgentRunID != nil {
		t.Fatalf("pending=%+v", pending)
	}
}

func ruleHex(value string) string {
	out := ""
	for len(out) < 64 {
		out += value
	}
	return out[:64]
}
