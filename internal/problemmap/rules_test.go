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

func TestDiscoverCandidatesRejectsPureHostWrappersAndShortControlReplies(t *testing.T) {
	digest := "sha256:" + ruleHex("a")
	view := "sha256:" + ruleHex("b")
	questions := []string{
		"<turn_aborted>\nThe user interrupted the previous turn intentionally. Any active tool executions were cancelled.\n</turn_aborted>",
		"<subagent_notification>{\"agent_id\":\"x\",\"status\":\"completed\"}</subagent_notification>",
		"确认", "符合", "可以", "1", "二", "好的继续", "继续", "允许", "发布", "补吧",
		"确认，2 和 3 交换一下位置", "是，但也要考虑尽量用无 token 消耗的模式实现必要的整理步骤", "github 已经发布了吗？为什么本地还是 0.3.5",
	}
	turns := make([]conversationchain.TurnUnit, len(questions))
	for index, question := range questions {
		turns[index] = conversationchain.TurnUnit{TurnUnitID: "turn-" + string(rune('a'+index)), UserMessage: conversationchain.Message{VisibleExcerpt: question}}
	}
	chain := ChainInput{Digest: digest, Document: conversationchain.Document{ProjectID: "project-a", Provider: "codex", SessionID: "session-a", SessionViewDigest: view, Digest: digest, TurnUnits: turns}}
	got := DiscoverCandidates("project-a", []ChainInput{chain}, nil, time.Date(2026, 9, 9, 1, 2, 3, 0, time.UTC))
	if len(got) != 3 {
		t.Fatalf("eligible candidates=%d values=%+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, candidate := range got {
		seen[candidate.Question] = true
	}
	for _, wanted := range questions[len(questions)-3:] {
		if !seen[wanted] {
			t.Fatalf("substantive mixed request was removed: %q", wanted)
		}
	}
}

func ruleHex(value string) string {
	out := ""
	for len(out) < 64 {
		out += value
	}
	return out[:64]
}
