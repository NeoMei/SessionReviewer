package presentation

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func TestRenderV4RejectsMissingBaselineForExistingDocuments(t *testing.T) {
	_, err := RenderV4(V4RenderInput{ExpectedFiles: map[string][]byte{
		"docs/session-review/项目回顾.md": []byte("保留人工原文"),
	}})
	if err == nil {
		t.Fatal("render would replace unauthenticated human content")
	}
}

func TestRenderV4PreservesAuthenticatedNoOpAndExactFourFileOrder(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contracts", "v4", "markdown", name))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	review := read("review.md")
	history := read("history.md")
	ledger := read("ledger.json")
	index := read("index.json")
	accepted, err := reviewv4.LoadProjection(review, history, ledger, index)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string][]byte{
		reviewv2.ReviewRelativePath:        review,
		reviewv2.HistoryRelativePath:       history,
		reviewv2.MachineLedgerRelativePath: ledger,
		SessionIndexRelativePath:           index,
	}
	plan, err := RenderV4(V4RenderInput{
		Presentation: accepted.Review, Ledger: accepted.Ledger, Index: accepted.SessionIndex,
		Previous: &reviewv4.MarkdownPair{Review: review, History: history}, Pending: &reviewv4.MarkdownPair{Review: review, History: history}, PreviousLedger: &accepted.Ledger, ExpectedFiles: expected,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath, SessionIndexRelativePath}
	if plan.ProjectID != accepted.Review.ProjectID || plan.GenerationID != accepted.Review.GenerationID || plan.ProjectViewDigest != accepted.Review.ProjectViewDigest || len(plan.Files) != len(wantOrder) {
		t.Fatalf("plan identity or file count: %+v", plan)
	}
	for i, relative := range wantOrder {
		file := plan.Files[i]
		if file.Relative != relative || !file.ExpectedExists || !bytes.Equal(file.Expected, expected[relative]) || !bytes.Equal(file.Desired, expected[relative]) {
			t.Fatalf("file[%d]=%+v", i, file)
		}
	}
}

func TestRenderV4AcceptsPendingShellOnceAndRejectsGeneratedTamper(t *testing.T) {
	read := func(name string) []byte {
		body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contracts", "v4", "markdown", name))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	review, history, ledgerBody, indexBody := read("review.md"), read("history.md"), read("ledger.json"), read("index.json")
	accepted, err := reviewv4.LoadProjection(review, history, ledgerBody, indexBody)
	if err != nil {
		t.Fatal(err)
	}
	pendingReview := bytes.Replace(review, []byte("# 项目回顾\n"), []byte("# 项目回顾\n\n自定义壳说明\n"), 1)
	if bytes.Equal(pendingReview, review) {
		t.Fatal("shell-only fixture did not change bytes")
	}
	pending := reviewv4.MarkdownPair{Review: pendingReview, History: history}
	draft, err := reviewv4.ParseMarkdownDraft(pending, accepted.Ledger)
	if err != nil || draft.Presentation.Revision != accepted.Review.Revision+1 {
		t.Fatalf("shell-only edit revision=%d err=%v", draft.Presentation.Revision, err)
	}
	next := draft.Presentation
	ledger := accepted.Ledger
	ledger.AcceptedRevision = next.Revision
	ledger.DocumentProjection = &reviewv4.DocumentProjection{SchemaVersion: 1, Format: "review-markdown-v1", PresentationBase: next}
	expected := map[string][]byte{reviewv2.ReviewRelativePath: review, reviewv2.HistoryRelativePath: history, reviewv2.MachineLedgerRelativePath: ledgerBody, SessionIndexRelativePath: indexBody}
	plan, err := RenderV4(V4RenderInput{Presentation: next, Ledger: ledger, Index: accepted.SessionIndex, Previous: &reviewv4.MarkdownPair{Review: review, History: history}, Pending: &pending, PreviousLedger: &accepted.Ledger, ExpectedFiles: expected})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reviewv4.LoadProjection(plan.Files[0].Desired, plan.Files[1].Desired, plan.Files[2].Desired, plan.Files[3].Desired); err != nil || got.Review.Revision != accepted.Review.Revision+1 {
		t.Fatalf("shell-only acceptance did not advance exactly once: revision=%d err=%v", got.Review.Revision, err)
	}

	tampered := reviewv4.MarkdownPair{Review: bytes.Replace(review, []byte("- [正式问题](#problem-x70726f626c656d3a616c706861)"), []byte("人工篡改生成区"), 1), History: history}
	if _, err := reviewv4.ParseMarkdownDraft(tampered, accepted.Ledger); reviewv4.MarkdownCodeOf(err) != reviewv4.MarkdownGeneratedRegionModified {
		t.Fatalf("generated edit was not rejected: %v", err)
	}
}
