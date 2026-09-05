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
