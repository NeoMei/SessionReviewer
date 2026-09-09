package reviewv4

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestMarkdownProblemOperationAddsRootOverPendingHumanDraft(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	pair := MarkdownPair{Review: mustRead(t, "../../testdata/contracts/v4/markdown/review.md"), History: mustRead(t, "../../testdata/contracts/v4/markdown/history.md")}
	custom := []byte("这段自定义文本和 [链接](https://example.test/custom) 必须原样保留。")
	draft, err := ParseMarkdownDraft(pair, ledger)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 1, 2, 3, 0, time.UTC).Format(time.RFC3339)
	next := draft.Presentation
	next.ProblemMapRevision++
	next.Revision++
	next.ProblemRootIDs = append(next.ProblemRootIDs, "problem-new")
	next.ProblemNodes = append(next.ProblemNodes, ProblemNode{ID: "problem-new", Question: "First root?", PrimaryParentID: nil, RelatedNodeIDs: []string{}, WorkflowState: "not_started", AnswerState: "no_answer", CompletionCriterion: "", CurrentConclusion: "", SourceTurnRefs: []SourceTurnRef{}, Provenance: "human_created", FirstProposedAt: now, SiblingOrder: len(draft.Presentation.ProblemRootIDs), ConfirmedAt: &now, Revision: 1})
	updated, err := RenderMarkdownProblemOperation(next, ledger, pair)
	if err != nil {
		var markdownErr *MarkdownError
		if errors.As(err, &markdownErr) {
			t.Fatalf("%v cause=%v", err, markdownErr.Cause)
		}
		t.Fatal(err)
	}
	if !bytes.Contains(updated.Review, custom) || !bytes.Contains(updated.Review, []byte("First root?")) {
		t.Fatalf("updated review omitted human or root content:\n%s", updated.Review)
	}
}

func TestMarkdownProblemOperationRejectsUnrelatedMutation(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	base := ledger.DocumentProjection.PresentationBase
	pair, err := RenderMarkdown(base, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	ledger.ReviewSHA256, ledger.HistorySHA256 = sha256Hex(pair.Review), sha256Hex(pair.History)
	ledger.SyncHashes.ReviewSHA256, ledger.SyncHashes.HistorySHA256 = ledger.ReviewSHA256, ledger.HistorySHA256
	next := base
	next.Revision++
	next.CurrentState.Goal = "fabricated"
	if _, err := RenderMarkdownProblemOperation(next, ledger, pair); err == nil {
		t.Fatal("unrelated mutation accepted")
	}
}
