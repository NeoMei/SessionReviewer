package syncdoc

import (
	"bytes"
	"testing"
)

func TestV4BlockCommentOwnershipComposition(t *testing.T) {
	_, fixture, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	const bindings = "id: review-project-p\nentity_type: project-review\nproject_id: project-p\nschema_version: 4\ndocument_format: review-markdown-v1\nrevision: 1\ngeneration_id: generation-1\nminimum_reader_version: 0.4.1\nminimum_writer_version: 0.4.1\n"
	cases := []struct{ name, before, selected, want string }{
		{"leading and next head", "custom_owner: one\n# keep B\ncustom_keep: two\n", "custom_owner: changed\n# keep B\ncustom_keep: two\n", "custom_owner: changed\n# keep B\ncustom_keep: two\n"},
		{"blank separator", "custom_owner: one\n\n# keep B\ncustom_keep: two\n\n", "custom_owner: changed\n\n# keep B\ncustom_keep: two\n\n", "custom_owner: changed\n\n# keep B\ncustom_keep: two\n\n"},
		{"delete head owned entry", "# owner head\ncustom_owner: one # owner line\n\n# keep B\ncustom_keep: two\n", "\n# keep B\ncustom_keep: two\n", "\n# keep B\ncustom_keep: two\n"},
		{"adjacent change delete add", "# owner head\ncustom_owner: one\n# delete A\ncustom_a: first\n# delete B\ncustom_b: second\n\n# keep C\ncustom_keep: two\n", "# new owner head\ncustom_owner: changed\n\n# keep C\ncustom_keep: two\n# new team\ncustom_team: codec\n", "# new owner head\ncustom_owner: changed\n\n# keep C\ncustom_keep: two\n# new team\ncustom_team: codec\n"},
		{"foot belongs to deleted entry", "custom_owner: one\n# owner foot\n\ncustom_keep: two\n", "\ncustom_keep: two\n", "\ncustom_keep: two\n"},
		{"quoted punctuation and head", "custom_owner: {label: \"# keep B\", plain: value#hash}\n# keep B\ncustom_keep: two\n", "custom_owner: {label: \"# changed\", plain: value#hash}\n# keep B\ncustom_keep: two\n", "custom_owner: {label: \"# changed\", plain: value#hash}\n# keep B\ncustom_keep: two\n"},
		{"literal clip separator", "custom_owner: |\n  # scalar content\n\n# keep B\ncustom_keep: two\n", "custom_owner: |\n  # changed content\n\n# keep B\ncustom_keep: two\n", "custom_owner: |\n  # changed content\n\n# keep B\ncustom_keep: two\n"},
		{"folded strip separator", "custom_owner: >-\n  # scalar content\n\n# keep B\ncustom_keep: two\n", "custom_owner: >-\n  # changed content\n\n# keep B\ncustom_keep: two\n", "custom_owner: >-\n  # changed content\n\n# keep B\ncustom_keep: two\n"},
		{"literal keep owned blanks", "custom_owner: |+\n  # scalar content\n\n# keep B\ncustom_keep: two\n", "custom_owner: |+\n  # changed content\n\n# keep B\ncustom_keep: two\n", "custom_owner: |+\n  # changed content\n\n# keep B\ncustom_keep: two\n"},
		{"unchanged head presentation", " # owner head  \ncustom_owner: one\n# keep B\ncustom_keep: two\n", " # owner head  \ncustom_owner: changed\n# keep B\ncustom_keep: two\n", " # owner head  \ncustom_owner: changed\n# keep B\ncustom_keep: two\n"},
	}
	for _, tc := range cases {
		for _, newline := range []string{"\n", "\r\n"} {
			t.Run(tc.name+map[string]string{"\n": "/LF", "\r\n": "/CRLF"}[newline], func(t *testing.T) {
				front := func(s string) []byte {
					return bytes.ReplaceAll([]byte("# source leading comment\n\n"+bindings+s), []byte("\n"), []byte(newline))
				}
				base := bytes.ReplaceAll(fixture, []byte("\n"), []byte(newline))
				before := replaceV4TestFrontmatter(t, base, front(tc.before))
				selected := replaceV4TestFrontmatter(t, base, front(tc.selected))
				want := replaceV4TestFrontmatter(t, base, front(tc.want))
				sourceDoc, err := ParseV4("项目回顾.md", before, ledger)
				if err != nil {
					t.Fatal(err)
				}
				selection, err := ParseV4("项目回顾.md", selected, ledger)
				if err != nil {
					t.Fatal(err)
				}
				edited, err := sourceDoc.WithSemanticUnits(selection.SemanticUnits())
				if err != nil {
					t.Fatal(err)
				}
				got, err := edited.Render()
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("lossless block composition\ngot: %q\nwant: %q", got[:bytes.Index(got, []byte("# 项目回顾"))], want[:bytes.Index(want, []byte("# 项目回顾"))])
				}
				reparsed, err := ParseV4("项目回顾.md", got, ledger)
				if err != nil {
					t.Fatal(err)
				}
				if !reparsed.SemanticEqual(selection) {
					t.Fatal("selected semantic units changed")
				}
				noop, err := reparsed.WithSemanticUnits(reparsed.SemanticUnits())
				if err != nil {
					t.Fatal(err)
				}
				again, err := noop.Render()
				if err != nil || !bytes.Equal(again, got) {
					t.Fatalf("unstable no-op: %v", err)
				}
			})
		}
	}
}
