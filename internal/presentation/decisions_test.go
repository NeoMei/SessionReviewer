package presentation

import (
	"os"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

func TestRenderDecisionOperationBuildsThreeFileCASAndPreservesIndex(t *testing.T) {
	ledgerBody := readDecisionFixture(t, "../../testdata/contracts/v4/markdown/ledger.json")
	ledger, err := reviewv4.DecodeLedger(ledgerBody)
	if err != nil {
		t.Fatal(err)
	}
	pair := reviewv4.MarkdownPair{Review: readDecisionFixture(t, "../../testdata/contracts/v4/markdown/review.md"), History: readDecisionFixture(t, "../../testdata/contracts/v4/markdown/history.md")}
	draft, err := reviewv4.ParseMarkdownDraft(pair, ledger)
	if err != nil {
		t.Fatal(err)
	}
	next := draft.Presentation
	next.Revision++
	next.Decisions = append(next.Decisions, reviewv4.Decision{ID: "decision-new", Kind: "agreement", OccurredAt: "2026-09-09", Title: "Keep the index immutable", Rationale: "It is the machine guard", Impact: "Human publication writes three files", Status: reviewv4.DecisionActive, ReevaluateWhen: "Index contract changes", Supersedes: []string{}, MilestoneIDs: []string{}, SessionRefs: []reviewv4.SessionRef{}, Provenance: "human_created", Revision: 1})
	indexBody := readDecisionFixture(t, "../../testdata/contracts/v4/markdown/index.json")
	index, err := sessionindex.Parse(indexBody)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := RenderDecisionOperation(DecisionOperationInput{Presentation: next, Ledger: ledger, Index: index, Pending: pair, ExpectedFiles: map[string][]byte{reviewv2.ReviewRelativePath: pair.Review, reviewv2.HistoryRelativePath: pair.History, reviewv2.MachineLedgerRelativePath: ledgerBody, SessionIndexRelativePath: indexBody}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 3 {
		t.Fatalf("files=%d", len(plan.Files))
	}
	for _, file := range plan.Files {
		if file.Relative == SessionIndexRelativePath {
			t.Fatal("human decision operation rewrites index")
		}
		if !file.ExpectedExists {
			t.Fatal("decision write lacks preimage")
		}
	}
	if !strings.Contains(string(plan.Files[0].Desired), "Keep the index immutable") {
		t.Fatal("new decision absent")
	}
}

func readDecisionFixture(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
