package reviewv4

import (
	"bytes"
	"errors"
	"testing"
)

func TestMarkdownDecisionOperationAddsDecisionAndPreservesHumanDraft(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	pair := MarkdownPair{Review: mustRead(t, "../../testdata/contracts/v4/markdown/review.md"), History: mustRead(t, "../../testdata/contracts/v4/markdown/history.md")}
	custom := []byte("这段自定义文本和 [链接](https://example.test/custom) 必须原样保留。")
	draft, err := ParseMarkdownDraft(pair, ledger)
	if err != nil {
		t.Fatal(err)
	}
	next := draft.Presentation
	next.Revision++
	next.Decisions = append(next.Decisions, Decision{
		ID: "decision-new", Kind: "decision", OccurredAt: "2026-09-09", Title: "Use the bounded path",
		Rationale: "Keeps authority narrow", Impact: "Applies to F3", Status: DecisionActive,
		ReevaluateWhen: "The contract changes", Supersedes: []string{}, MilestoneIDs: []string{}, SessionRefs: []SessionRef{},
		Provenance: "human_created", Revision: 1,
	})
	updated, err := RenderMarkdownDecisionOperation(next, ledger, pair)
	if err != nil {
		var markdownErr *MarkdownError
		if errors.As(err, &markdownErr) {
			t.Fatalf("%v cause=%v", err, markdownErr.Cause)
		}
		t.Fatal(err)
	}
	if !bytes.Contains(updated.Review, custom) || !bytes.Contains(updated.Review, []byte("Use the bounded path")) {
		t.Fatalf("updated Markdown omitted human or decision content")
	}
}

func TestMarkdownDecisionOperationRejectsUnrelatedMutation(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	base := ledger.DocumentProjection.PresentationBase
	pair, err := RenderMarkdown(base, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	ledger.ReviewSHA256, ledger.HistorySHA256 = sha256Hex(pair.Review), sha256Hex(pair.History)
	ledger.SyncHashes.ReviewSHA256, ledger.SyncHashes.HistorySHA256 = ledger.ReviewSHA256, ledger.HistorySHA256
	next := clonePresentation(base)
	next.Revision++
	next.CurrentState.Goal = "fabricated"
	if _, err := RenderMarkdownDecisionOperation(next, ledger, pair); err == nil {
		t.Fatal("unrelated mutation accepted")
	}
}
