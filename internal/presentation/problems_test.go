package presentation

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

func TestRenderProblemOperationBuildsThreeFileCASAndPreservesIndex(t *testing.T) {
	ledgerBody := readProblemFixture(t, "../../testdata/contracts/v4/markdown/ledger.json")
	ledger, err := reviewv4.DecodeLedger(ledgerBody)
	if err != nil {
		t.Fatal(err)
	}
	pair := reviewv4.MarkdownPair{Review: readProblemFixture(t, "../../testdata/contracts/v4/markdown/review.md"), History: readProblemFixture(t, "../../testdata/contracts/v4/markdown/history.md")}
	draft, err := reviewv4.ParseMarkdownDraft(pair, ledger)
	if err != nil {
		t.Fatal(err)
	}
	next := draft.Presentation
	next.Revision++
	next.ProblemMapRevision++
	now := time.Date(2026, 9, 9, 1, 2, 3, 0, time.UTC).Format(time.RFC3339)
	next.ProblemRootIDs = append(next.ProblemRootIDs, "problem-new")
	next.ProblemNodes = append(next.ProblemNodes, reviewv4.ProblemNode{ID: "problem-new", Question: "New?", RelatedNodeIDs: []string{}, WorkflowState: "not_started", AnswerState: "no_answer", CompletionCriterion: "", CurrentConclusion: "", SourceTurnRefs: []reviewv4.SourceTurnRef{}, Provenance: "human_created", FirstProposedAt: now, SiblingOrder: len(draft.Presentation.ProblemRootIDs), ConfirmedAt: &now, Revision: 1})
	indexBody := readProblemFixture(t, "../../testdata/contracts/v4/markdown/index.json")
	index, err := sessionindex.Parse(indexBody)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := RenderProblemOperation(ProblemOperationInput{Presentation: next, Ledger: ledger, Index: index, Pending: pair, ExpectedFiles: map[string][]byte{reviewv2.ReviewRelativePath: pair.Review, reviewv2.HistoryRelativePath: pair.History, reviewv2.MachineLedgerRelativePath: ledgerBody, SessionIndexRelativePath: indexBody}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 3 {
		t.Fatalf("files=%d", len(plan.Files))
	}
	for _, file := range plan.Files {
		if file.Relative == SessionIndexRelativePath {
			t.Fatal("human operation rewrites index")
		}
		if !file.ExpectedExists {
			t.Fatal("missing preimage")
		}
	}
	if !strings.Contains(string(plan.Files[0].Desired), "New?") {
		t.Fatal("new problem absent")
	}
}

func readProblemFixture(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
