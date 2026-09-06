package syncdoc

import (
	"bytes"
	"gopkg.in/yaml.v3"
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
		{"remove owned head and line", "# owner head\ncustom_owner: one # owner line\n# keep B\ncustom_keep: two\n", "custom_owner: changed\n# keep B\ncustom_keep: two\n", "custom_owner: changed\n# keep B\ncustom_keep: two\n"},
		{"change owned foot", "custom_owner: one\n# old foot\n\ncustom_keep: two\n", "custom_owner: changed\n# new foot\n\ncustom_keep: two\n", "custom_owner: changed\n# new foot\n\ncustom_keep: two\n"},
		{"remove owned foot", "custom_owner: one\n# old foot\n\ncustom_keep: two\n", "custom_owner: changed\n\ncustom_keep: two\n", "custom_owner: changed\n\ncustom_keep: two\n"},
		{"addition before final trivia", "custom_owner: one\n\n", "custom_owner: changed\n# new team\ncustom_team: codec\n\n", "custom_owner: changed\n# new team\ncustom_team: codec\n\n"},
		{"delete keep scalar owned blanks", "custom_owner: |+\n  content\n\n# keep B\ncustom_keep: two\n", "# keep B\ncustom_keep: two\n", "# keep B\ncustom_keep: two\n"},
		{"folded keep owned blanks", "custom_owner: >+\n  content\n\n# keep B\ncustom_keep: two\n", "custom_owner: >+\n  changed\n\n# keep B\ncustom_keep: two\n", "custom_owner: >+\n  changed\n\n# keep B\ncustom_keep: two\n"},
		{"tag and adjacent changes", "# head A\ncustom_owner: !!str one\n\n# head B\ncustom_keep: two\n", "# head A\ncustom_owner: !!str changed\n\n# head B\ncustom_keep: changed\n", "# head A\ncustom_owner: !!str changed\n\n# head B\ncustom_keep: changed\n"},
		{"unchanged foot presentation", "custom_owner: one\n # owner foot  \n\ncustom_keep: two\n", "custom_owner: changed\n # owner foot  \n\ncustom_keep: two\n", "custom_owner: changed\n # owner foot  \n\ncustom_keep: two\n"},
		{"two nested folded values", "custom_owner:\n  first: >+ # first header\n    one\n\n  # second head\n  second: !!str >+\n    two\n\n# keep B\ncustom_keep: three\n", "custom_owner:\n  first: >+ # first header\n    changed\n\n  # second head\n  second: !!str >+\n    changed\n\n# keep B\ncustom_keep: three\n", "custom_owner:\n  first: >+ # first header\n    changed\n\n  # second head\n  second: !!str >+\n    changed\n\n# keep B\ncustom_keep: three\n"},
	}
	for _, tc := range cases {
		for _, indent := range []string{"", "  "} {
			for _, newline := range []string{"\n", "\r\n"} {
				t.Run(tc.name+map[string]string{"\n": "/LF", "\r\n": "/CRLF"}[newline]+"/indent="+string(rune('0'+len(indent))), func(t *testing.T) {
					front := func(s string) []byte {
						lines := bytes.SplitAfter([]byte("# source leading comment\n\n"+bindings+s), []byte("\n"))
						for i, line := range lines {
							if len(bytes.TrimSpace(line)) != 0 {
								lines[i] = append([]byte(indent), line...)
							}
						}
						return bytes.ReplaceAll(bytes.Join(lines, nil), []byte("\n"), []byte(newline))
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
}

func TestV4BlockScalarSemanticUnitRoundTrip(t *testing.T) {
	_, fixture, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	for _, tc := range []struct {
		name, source, value string
		nested              bool
	}{
		{"folded keep", ">+\n  changed\n\n", "changed\n\n", false},
		{"tagged folded keep comment", "!!str >+ # scalar header\n  changed\n\n", "changed\n\n", false},
		{"nested folded keep", "\n  label: >+ # nested header\n    changed\n\n", "changed\n\n", true},
		{"nested folded keep extra", "\n  label: >+\n    changed\n\n\n", "changed\n\n\n", true},
		{"folded clip", ">\n  changed\n", "changed\n", false},
		{"literal keep", "|+\n  changed\n\n", "changed\n\n", false},
		{"folded empty keep", ">+\n\n\n", "\n\n", false},
		{"literal empty keep", "|+\n\n\n", "\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := bytes.Replace(fixture, []byte("custom_owner: 保留\n"), []byte("custom_owner: "+tc.source+"# next head\ncustom_keep: two\n"), 1)
			doc, err := ParseV4("项目回顾.md", raw, ledger)
			if err != nil {
				t.Fatal(err)
			}
			key := UnitKey{Kind: UnitFrontmatter, Name: "custom_owner"}
			unit := doc.SemanticUnits()[key]
			node, err := decodeUnitValue(unit.Value)
			if err != nil {
				t.Fatal(err)
			}
			value := node
			if tc.nested {
				value = node.Content[1]
			}
			if value.Value != tc.value {
				t.Fatalf("semantic unit scalar=%q want=%q", value.Value, tc.value)
			}
			if value.Style&yaml.FoldedStyle == 0 && tc.name != "literal keep" && tc.name != "literal empty keep" {
				t.Fatal("folded style lost")
			}
			encoded, err := encodeV4FrontmatterUnit(key, unit)
			if err != nil {
				t.Fatal(err)
			}
			mapping, err := decodeFrontmatter(encoded)
			if err != nil {
				t.Fatal(err)
			}
			value = mapping.Content[1]
			if tc.nested {
				value = value.Content[1]
			}
			if value.Value != tc.value {
				t.Fatalf("rendered scalar=%q want=%q", value.Value, tc.value)
			}
			original := encodeNode(node)
			for round := 0; round < 3; round++ {
				body, err := encodeV4YAMLNode(node)
				if err != nil {
					t.Fatal(err)
				}
				next, err := decodeUnitValue(body)
				if err != nil {
					t.Fatal(err)
				}
				if !v4YAMLTypedEqual(node, next) || !v4YAMLCommentsEqual(node, next) {
					t.Fatal("repeated scalar roundtrip changed typed value or comments")
				}
				if !bytes.Equal(encodeNode(node), original) {
					t.Fatal("encoding mutated input nodes")
				}
				node = next
			}
			unchanged, err := doc.Render()
			if err != nil || !bytes.Equal(unchanged, raw) {
				t.Fatalf("raw no-op changed: %v", err)
			}
		})
	}
}
