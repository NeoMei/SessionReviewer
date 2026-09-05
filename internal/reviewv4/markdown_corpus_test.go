package reviewv4

import (
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func TestMarkdownCorpusMatchesTypedDraftParser(t *testing.T) {
	var corpus markdownCorpus
	if err := strictjson.Decode(mustRead(t, "../../testdata/contracts/v4/markdown/cases.json"), &corpus); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range corpus.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			ledger, err := DecodeLedger(mustRead(t, "../../testdata/contracts/v4/markdown/"+testCase.Ledger))
			if got := strictjson.CodeOf(err); got != testCase.ExpectedCode {
				t.Fatalf("ledger code=%q want=%q err=%v", got, testCase.ExpectedCode, err)
			}
			if err != nil {
				return
			}
			draft, err := ParseMarkdownDraft(MarkdownPair{
				Review:  mustRead(t, "../../testdata/contracts/v4/markdown/"+testCase.Review),
				History: mustRead(t, "../../testdata/contracts/v4/markdown/"+testCase.History),
			}, ledger)
			wantCode := ""
			if testCase.ExpectedMarkdownCode != nil {
				wantCode = *testCase.ExpectedMarkdownCode
			}
			if got := MarkdownCodeOf(err); got != wantCode {
				t.Fatalf("markdown code=%q want=%q err=%v", got, wantCode, err)
			}
			if err != nil {
				return
			}
			for encoded, want := range testCase.ExpectedFields {
				entity, name, ok := strings.Cut(encoded, "/")
				if !ok {
					t.Fatalf("invalid field key %q", encoded)
				}
				got, exists := markdownPresentationField(draft.Presentation, FieldKey{Entity: entity, Name: name})
				if !exists || got != want {
					t.Fatalf("field %s=%q exists=%v want=%q", encoded, got, exists, want)
				}
			}
		})
	}
}

func TestMarkdownCorpusExistingPatchCanBeUpdated(t *testing.T) {
	ledger, err := DecodeLedger(mustRead(t, "../../testdata/contracts/v4/markdown/ledger-existing-patch.json"))
	if err != nil {
		t.Fatal(err)
	}
	draft, err := ParseMarkdownDraft(MarkdownPair{
		Review:  mustRead(t, "../../testdata/contracts/v4/markdown/review-existing-patch-draft.md"),
		History: mustRead(t, "../../testdata/contracts/v4/markdown/history.md"),
	}, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if got := draft.Presentation.CurrentState.Goal; got != "再次人工编辑目标" {
		t.Fatalf("goal=%q", got)
	}
	if len(draft.Presentation.HumanPatches) != 1 || draft.Presentation.HumanPatches[0].Value == nil || *draft.Presentation.HumanPatches[0].Value != "再次人工编辑目标" {
		t.Fatalf("patch=%+v", draft.Presentation.HumanPatches)
	}
}

func TestMarkdownCorpusGoEscapedBaselineCanBeUpdated(t *testing.T) {
	ledger, err := DecodeLedger(mustRead(t, "../../testdata/contracts/v4/markdown/ledger-special-baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	const wantHash = "112173e9eb315eaffc9ce7b55ca9fc1a768be05486a426a2365c1354301476bd"
	if len(ledger.GeneratedBaselines) != 1 || ledger.GeneratedBaselines[0].GeneratedHash != wantHash {
		t.Fatalf("baseline=%+v", ledger.GeneratedBaselines)
	}
	draft, err := ParseMarkdownDraft(MarkdownPair{
		Review:  mustRead(t, "../../testdata/contracts/v4/markdown/review-special-baseline-draft.md"),
		History: mustRead(t, "../../testdata/contracts/v4/markdown/history.md"),
	}, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if got := draft.Presentation.CurrentState.Goal; got != "再次特殊字符覆盖" {
		t.Fatalf("goal=%q", got)
	}
	if len(draft.Presentation.HumanPatches) != 1 || draft.Presentation.HumanPatches[0].BaseGeneratedHash != wantHash {
		t.Fatalf("patch=%+v", draft.Presentation.HumanPatches)
	}
}

func TestMarkdownCorpusMalformedPatchStateIsFieldLocal(t *testing.T) {
	tests := []struct{ ledger, review string }{
		{"ledger-duplicate-baseline.json", "review.md"},
		{"ledger-invalid-baseline-hash.json", "review.md"},
		{"ledger-baseline-generation-mismatch.json", "review.md"},
		{"ledger-baseline-kind-mismatch.json", "review.md"},
		{"ledger-baseline-value-missing.json", "review.md"},
		{"ledger-baseline-values-present.json", "review.md"},
		{"ledger-duplicate-human-patch.json", "review-existing-patch.md"},
		{"ledger-orphan-patch-collision.json", "review-existing-patch.md"},
	}
	for _, test := range tests {
		t.Run(test.ledger, func(t *testing.T) {
			ledger, err := DecodeLedger(mustRead(t, "../../testdata/contracts/v4/markdown/"+test.ledger))
			if err != nil {
				t.Fatal(err)
			}
			draft, err := ParseMarkdownDraft(MarkdownPair{
				Review:  mustRead(t, "../../testdata/contracts/v4/markdown/"+test.review),
				History: mustRead(t, "../../testdata/contracts/v4/markdown/history.md"),
			}, ledger)
			if err != nil {
				t.Fatal(err)
			}
			if len(draft.Edits) != 0 {
				t.Fatalf("edits=%+v", draft.Edits)
			}
		})
	}
}
