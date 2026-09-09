package problemmap

import (
	"errors"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func TestStorePersistsListsAndCompareAndSwapsCandidateRevisions(t *testing.T) {
	store, err := OpenStore(t.TempDir(), "project-a")
	if err != nil {
		t.Fatal(err)
	}
	candidate := completeCandidate("candidate-a")
	if err := store.CompareAndSwap(candidate, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwap(candidate, 0); !errors.Is(err, ErrCandidateRevisionConflict) {
		t.Fatalf("stale create err=%v", err)
	}
	got, err := store.List(CandidatePending)
	if err != nil || len(got) != 1 || got[0].CandidateID != candidate.CandidateID {
		t.Fatalf("list=%+v err=%v", got, err)
	}
	candidate.Status, candidate.Revision = CandidateDismissed, 2
	if err := store.CompareAndSwap(candidate, 1); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get("candidate-a"); err != nil || got.Status != CandidateDismissed || got.Revision != 2 {
		t.Fatalf("get=%+v err=%v", got, err)
	}
	if got, err := OpenStore(t.TempDir(), "project-b"); err != nil || got.ProjectID() != "project-b" {
		t.Fatalf("second=%v err=%v", got, err)
	}
}

func TestAnalysisIdentityNormalizesQuestionAndDependencies(t *testing.T) {
	one := AnalysisIdentity("project-a", "  How   now?  ", "rules-v1", []string{"sha256:" + repeatHex("b"), "sha256:" + repeatHex("a")})
	two := AnalysisIdentity("project-a", "how now?", "rules-v1", []string{"sha256:" + repeatHex("a"), "sha256:" + repeatHex("b")})
	if one == "" || one != two {
		t.Fatalf("identity mismatch %q %q", one, two)
	}
}

func completeCandidate(id string) Candidate {
	now := time.Date(2026, 9, 9, 1, 2, 3, 0, time.UTC).Format(time.RFC3339)
	return Candidate{CandidateID: id, ProjectID: "project-a", Question: "How?", SourceTurnRefs: []reviewv4.SourceTurnRef{{Provider: "codex", SessionID: "s1", TurnUnitID: "t1"}}, RecommendedRelation: RelationKeepPending, RecommendedTargetID: nil, AlternateTargetIDs: []string{}, RelatedNodeIDs: []string{}, Grounds: []Ground{{RuleID: "no-signal", RuleVersion: "rules-v1", MatchedFactRefs: []string{}, Explanation: "没有可复核的层级信号，继续待归类。"}}, Confidence: ConfidenceLow, Status: CandidatePending, DependencyDigests: []string{"sha256:" + repeatHex("a")}, AnalysisMode: AnalysisDeterministic, AgentRunID: nil, Revision: 1, CreatedAt: now, UpdatedAt: now}
}

func repeatHex(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result[:64]
}
