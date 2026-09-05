package reviewv4

import (
	"bytes"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func validMarkdownDocument(body string) []byte {
	return []byte("---\n" +
		"id: review-project-p\n" +
		"entity_type: project-review\n" +
		"project_id: project-p\n" +
		"schema_version: 4\n" +
		"document_format: review-markdown-v1\n" +
		"revision: 1\n" +
		"generation_id: generation-1\n" +
		"minimum_reader_version: 0.4.1\n" +
		"minimum_writer_version: 0.4.1\n" +
		"custom_owner: preserved\n" +
		"---\n" + body)
}

func TestMarkdownDocumentPreservesAndDefensivelyCopiesBytesAndFields(t *testing.T) {
	raw := validMarkdownDocument("# Review\n<!-- session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\n  中文\nline  \n\n<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\ncustom")
	document, err := ParseMarkdownDocument("项目回顾.md", raw)
	if err != nil {
		t.Fatal(err)
	}
	got := document.Bytes()
	if !bytes.Equal(got, raw) {
		t.Fatal("unmodified bytes changed")
	}
	got[0] = 'x'
	if bytes.Equal(got, document.Bytes()) {
		t.Fatal("Bytes returned shared storage")
	}
	fields := document.Fields()
	key := FieldKey{Entity: "project-overview", Name: "goal"}
	if fields[key] != "  中文\nline  \n" {
		t.Fatalf("field=%q", fields[key])
	}
	fields[key] = "mutated"
	if document.Fields()[key] == "mutated" {
		t.Fatal("Fields returned shared storage")
	}
}

func TestMarkdownDocumentReplaceFieldsPreservesShellAndStructure(t *testing.T) {
	raw := validMarkdownDocument("before\r\n<!-- session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\r\nold\r\n<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\r\nafter\r\n")
	document, err := ParseMarkdownDocument("项目回顾.md", raw)
	if err != nil {
		t.Fatal(err)
	}
	key := FieldKey{Entity: "project-overview", Name: "goal"}
	result, err := document.ReplaceFields(map[FieldKey]string{key: "  new\r\nline\r\n"})
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(raw, []byte("old"), []byte("  new\r\nline\r\n"), 1)
	if !bytes.Equal(result, want) {
		t.Fatalf("replacement changed shell\ngot: %q\nwant:%q", result, want)
	}
	result[0] = 'x'
	if document.Bytes()[0] == 'x' {
		t.Fatal("replacement shares document storage")
	}
	if _, err := document.ReplaceFields(map[FieldKey]string{{Entity: "project-overview", Name: "stage"}: "x"}); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("missing replacement key err=%v", err)
	}
	if _, err := document.ReplaceFields(map[FieldKey]string{key: strings.Repeat("x", maxMarkdownFieldBytes+1)}); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("overlong replacement err=%v", err)
	}
	if _, err := document.ReplaceFields(map[FieldKey]string{key: "x\n<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\n"}); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("structural replacement err=%v", err)
	}
	for _, value := range []string{
		"x\n<!-- session-reviewer:v4-field entity=\"project-overview\" name=\"stage\" -->\n",
		"```markdown\n",
		"bad\rline",
		"bad\x00line",
		string([]byte{0xff}),
	} {
		if _, err := document.ReplaceFields(map[FieldKey]string{key: value}); MarkdownCodeOf(err) != MarkdownFormatInvalid {
			t.Fatalf("unsafe replacement %q err=%v", value, err)
		}
	}
	unchanged, err := document.ReplaceFields(nil)
	if err != nil || !bytes.Equal(unchanged, raw) {
		t.Fatalf("nil replacement changed bytes: err=%v", err)
	}
}

func TestMarkdownDocumentRejectsGeneratedReplacement(t *testing.T) {
	raw := validMarkdownDocument("<!-- session-reviewer:v4-generated entity=\"project-overview\" name=\"problem-tree\" -->\nold\n<!-- /session-reviewer:v4-generated entity=\"project-overview\" name=\"problem-tree\" -->\n")
	document, err := ParseMarkdownDocument("项目回顾.md", raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = document.ReplaceFields(map[FieldKey]string{{Entity: "project-overview", Name: "problem-tree"}: "new"})
	if MarkdownCodeOf(err) != MarkdownGeneratedRegionModified {
		t.Fatalf("err=%v", err)
	}
}

func TestMarkdownDocumentRejectsInvalidFrontmatter(t *testing.T) {
	tests := []struct{ name, old, replacement string }{
		{"duplicate key", "id: review-project-p\n", "id: one\nid: two\n"},
		{"alias", "id: review-project-p\n", "id: &id review-project-p\ncopy: *id\n"},
		{"merge", "id: review-project-p\n", "id: review-project-p\nbase: &base {x: y}\ncopy: {<<: *base}\n"},
		{"unknown reserved", "custom_owner: preserved\n", "custom_owner: preserved\nsession_reviewer_future: true\n"},
		{"wrong format", "document_format: review-markdown-v1\n", "document_format: review-json-v4\n"},
		{"wrong reader", "minimum_reader_version: 0.4.1\n", "minimum_reader_version: 0.4.0\n"},
		{"missing id", "id: review-project-p\n", "custom_replacement: value\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := bytes.Replace(validMarkdownDocument(""), []byte(tc.old), []byte(tc.replacement), 1)
			if _, err := ParseMarkdownDocument("项目回顾.md", raw); MarkdownCodeOf(err) != MarkdownFormatInvalid {
				t.Fatalf("err=%v", err)
			}
		})
	}
	deep := validMarkdownDocument("x")
	insert := "extra: " + strings.Repeat("[", maxMarkdownYAMLDepth+1) + "x" + strings.Repeat("]", maxMarkdownYAMLDepth+1) + "\n"
	deep = bytes.Replace(deep, []byte("custom_owner: preserved\n"), []byte(insert), 1)
	if _, err := ParseMarkdownDocument("项目回顾.md", deep); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("deep YAML err=%v", err)
	}
	multiple := bytes.Replace(validMarkdownDocument("x"), []byte("custom_owner: preserved\n"), []byte("--- # second document\nother: value\n"), 1)
	if _, err := ParseMarkdownDocument("项目回顾.md", multiple); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("multiple YAML documents err=%v", err)
	}
	oversize := bytes.Replace(validMarkdownDocument("x"), []byte("custom_owner: preserved"), []byte("custom_owner: "+strings.Repeat("x", maxMarkdownFrontmatterBytes)), 1)
	if _, err := ParseMarkdownDocument("项目回顾.md", oversize); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("oversize frontmatter err=%v", err)
	}
	nodes := validMarkdownDocument("x")
	var many strings.Builder
	many.WriteString("extra:\n")
	for index := 0; index < maxMarkdownYAMLNodes; index++ {
		many.WriteString("  - x\n")
	}
	nodes = bytes.Replace(nodes, []byte("custom_owner: preserved\n"), []byte(many.String()), 1)
	if _, err := ParseMarkdownDocument("项目回顾.md", nodes); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("excessive YAML nodes err=%v", err)
	}
}

func TestMarkdownDocumentRejectsWrongDocumentOwnershipAndUnsafePath(t *testing.T) {
	historyField := "<!-- session-reviewer:v4-field entity=\"milestone:m\" name=\"title\" -->\ntitle\n<!-- /session-reviewer:v4-field entity=\"milestone:m\" name=\"title\" -->\n"
	if _, err := ParseMarkdownDocument("项目回顾.md", validMarkdownDocument(historyField)); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("history field in review err=%v", err)
	}
	for _, relative := range []string{"", "/absolute.md", "../escape.md", "nested/../escape.md", `nested\escape.md`} {
		if _, err := ParseMarkdownDocument(relative, validMarkdownDocument("body")); MarkdownCodeOf(err) != MarkdownFormatInvalid {
			t.Fatalf("unsafe relative %q err=%v", relative, err)
		}
	}
}

func TestMarkdownDocumentSharedCorpusParserOutcomes(t *testing.T) {
	var corpus markdownCorpus
	if err := strictjson.Decode(mustRead(t, "../../testdata/contracts/v4/markdown/cases.json"), &corpus); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range corpus.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			review, reviewErr := ParseMarkdownDocument(testCase.Review, mustRead(t, "../../testdata/contracts/v4/markdown/"+testCase.Review))
			history, historyErr := ParseMarkdownDocument(testCase.History, mustRead(t, "../../testdata/contracts/v4/markdown/"+testCase.History))
			gotCode := MarkdownCodeOf(reviewErr)
			if gotCode == "" {
				gotCode = MarkdownCodeOf(historyErr)
			}
			wantCode := ""
			if testCase.ExpectedMarkdownCode != nil {
				wantCode = *testCase.ExpectedMarkdownCode
			}
			if gotCode != wantCode {
				t.Fatalf("Markdown rejection=%q want=%q reviewErr=%v historyErr=%v", gotCode, wantCode, reviewErr, historyErr)
			}
			if gotCode != "" {
				return
			}
			fields := review.Fields()
			for key, value := range history.Fields() {
				fields[key] = value
			}
			for encoded, want := range testCase.ExpectedFields {
				entity, name, ok := strings.Cut(encoded, "/")
				if !ok {
					t.Fatalf("invalid expected field key %q", encoded)
				}
				if got, exists := fields[FieldKey{Entity: entity, Name: name}]; !exists || got != want {
					t.Fatalf("field %s=%q exists=%v want=%q", encoded, got, exists, want)
				}
			}
		})
	}
}

func TestMarkdownDocumentContainerFixtureHasPinnedBindings(t *testing.T) {
	review := mustRead(t, "../../testdata/contracts/v4/markdown/review-container-markers.md")
	ledger, err := DecodeLedger(mustRead(t, "../../testdata/contracts/v4/markdown/ledger-container-markers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sha256Hex(review), "0ca020feae93b66c232d57f175c191c8e49974c04ce85482a61e97fd06b1ce9c"; got != want || ledger.ReviewSHA256 != want {
		t.Fatalf("review hash=%s ledger=%s want=%s", got, ledger.ReviewSHA256, want)
	}
	if got, want := CanonicalLedgerSHA256(ledger), "377cd1d9939da273b7ebbdeb90f2d95a1ed7a0a8420bd64fa7626fe0b5888a6c"; got != want || ledger.SyncHashes.LedgerSHA256 != want {
		t.Fatalf("ledger hash=%s embedded=%s want=%s", got, ledger.SyncHashes.LedgerSHA256, want)
	}
}
