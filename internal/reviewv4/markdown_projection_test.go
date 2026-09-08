package reviewv4

import (
	"bytes"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

func TestParseMarkdownDraftProjectionPreservesPendingDraftIdentity(t *testing.T) {
	review, history, ledger, index := markdownProjectionFixtures(t)
	document, err := ParseMarkdownDocument("项目回顾.md", review)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := document.ReplaceFields(map[FieldKey]string{
		{Entity: "project-overview", Name: "goal"}: "human pending goal",
	})
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := ParseMarkdownDraftProjection(edited, history, ledger, index)
	if err != nil || parsed.Draft.Presentation.CurrentState.Goal != "human pending goal" {
		t.Fatalf("pending draft identity: %v", err)
	}
	if parsed.Ledger.ProjectID != "project-p" || parsed.Draft.Presentation.ProjectID != "project-p" || parsed.SessionIndex.ProjectID != "project-p" {
		t.Fatalf("project identity drifted: ledger=%q draft=%q index=%q", parsed.Ledger.ProjectID, parsed.Draft.Presentation.ProjectID, parsed.SessionIndex.ProjectID)
	}
	if parsed.Ledger.GenerationID != "generation-1" || parsed.Draft.Presentation.GenerationID != "generation-1" || parsed.SessionIndex.GenerationID != "generation-1" {
		t.Fatalf("generation identity drifted: ledger=%q draft=%q index=%q", parsed.Ledger.GenerationID, parsed.Draft.Presentation.GenerationID, parsed.SessionIndex.GenerationID)
	}
	if _, err := LoadProjection(edited, history, ledger, index); err == nil {
		t.Fatal("draft was misrepresented as an accepted publication")
	}
}

func TestParseMarkdownDraftProjectionRejectsUnauthenticatedInputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, review, history, ledger, index []byte) ([]byte, []byte, []byte, []byte)
	}{
		{name: "missing index", mutate: func(_ *testing.T, review, history, ledger, _ []byte) ([]byte, []byte, []byte, []byte) {
			return review, history, ledger, nil
		}},
		{name: "wrong index project", mutate: mutateMarkdownProjectionIndex(func(value *sessionindex.Document) {
			value.ProjectID = "project-other"
		})},
		{name: "wrong index generation", mutate: mutateMarkdownProjectionIndex(func(value *sessionindex.Document) {
			value.GenerationID = "generation-other"
		})},
		{name: "wrong index project view", mutate: mutateMarkdownProjectionIndex(func(value *sessionindex.Document) {
			value.ProjectViewDigest = "sha256:" + strings.Repeat("9", 64)
		})},
		{name: "wrong index digest", mutate: mutateMarkdownProjectionIndex(func(value *sessionindex.Document) {
			value.GeneratedAt = "2026-09-05T00:00:00Z"
		})},
		{name: "invalid ledger self digest", mutate: func(_ *testing.T, review, history, ledger, index []byte) ([]byte, []byte, []byte, []byte) {
			ledger = bytes.Replace(ledger, []byte(`"ledger_sha256": "7f88cfc54caf84e0e58224916815530ed6ed18f156e57f1c9ea7102bd67888c5"`), []byte(`"ledger_sha256": "`+strings.Repeat("9", 64)+`"`), 1)
			return review, history, ledger, index
		}},
		{name: "absent document projection", mutate: func(t *testing.T, review, history, ledger, index []byte) ([]byte, []byte, []byte, []byte) {
			value, err := DecodeLedger(ledger)
			if err != nil {
				t.Fatal(err)
			}
			value.DocumentProjection = nil
			value.MinimumReaderVersion = "0.4.0"
			value.MinimumWriterVersion = "0.4.0"
			ledger, err = RenderLedger(value)
			if err != nil {
				t.Fatal(err)
			}
			return review, history, ledger, index
		}},
		{name: "changed reserved frontmatter", mutate: func(_ *testing.T, review, history, ledger, index []byte) ([]byte, []byte, []byte, []byte) {
			review = bytes.Replace(review, []byte("generation_id: generation-1"), []byte("generation_id: generation-other"), 1)
			return review, history, ledger, index
		}},
		{name: "generated region edit", mutate: func(_ *testing.T, review, history, ledger, index []byte) ([]byte, []byte, []byte, []byte) {
			review = bytes.Replace(review, []byte("- [正式问题]"), []byte("- [篡改的问题]"), 1)
			return review, history, ledger, index
		}},
		{name: "malformed UTF-8", mutate: func(_ *testing.T, review, history, ledger, index []byte) ([]byte, []byte, []byte, []byte) {
			review = append(bytes.Clone(review), 0xff)
			return review, history, ledger, index
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			review, history, ledger, index := markdownProjectionFixtures(t)
			if _, err := ParseMarkdownDraftProjection(review, history, ledger, index); err != nil {
				t.Fatalf("positive control failed: %v", err)
			}
			review, history, ledger, index = test.mutate(t, review, history, ledger, index)
			if _, err := ParseMarkdownDraftProjection(review, history, ledger, index); err == nil {
				t.Fatal("accepted unauthenticated Markdown projection")
			}
		})
	}
}

func markdownProjectionFixtures(t *testing.T) (review, history, ledger, index []byte) {
	t.Helper()
	return mustRead(t, "../../testdata/contracts/v4/markdown/review.md"),
		mustRead(t, "../../testdata/contracts/v4/markdown/history.md"),
		mustRead(t, "../../testdata/contracts/v4/markdown/ledger.json"),
		mustRead(t, "../../testdata/contracts/v4/markdown/index.json")
}

func mutateMarkdownProjectionIndex(mutate func(*sessionindex.Document)) func(*testing.T, []byte, []byte, []byte, []byte) ([]byte, []byte, []byte, []byte) {
	return func(t *testing.T, review, history, ledger, index []byte) ([]byte, []byte, []byte, []byte) {
		value, err := sessionindex.Parse(index)
		if err != nil {
			t.Fatal(err)
		}
		mutate(&value)
		index, err = sessionindex.Render(value)
		if err != nil {
			t.Fatalf("mutated index is not independently valid: %v", err)
		}
		return review, history, ledger, index
	}
}
