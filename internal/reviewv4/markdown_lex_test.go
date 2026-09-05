package reviewv4

import (
	"bytes"
	"strings"
	"testing"
)

func TestMarkdownLexKeepsValueBytes(t *testing.T) {
	src := []byte("<!-- session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\r\n" +
		"  中文\r\n\r\n" +
		"<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\r\n")
	spans, err := ScanMarkdownBlocks(src)
	if err != nil || len(spans) != 1 {
		t.Fatalf("spans=%v err=%v", spans, err)
	}
	s := spans[0]
	if string(src[s.ValueStart:s.ValueEnd]) != "  中文\r\n" {
		t.Fatal("lost whitespace")
	}
}

func TestMarkdownLexIgnoresCodeAndContainerMarkers(t *testing.T) {
	marker := `<!-- session-reviewer:v4-field entity="project-overview" name="goal" -->`
	close := `<!-- /session-reviewer:v4-field entity="project-overview" name="goal" -->`
	tests := []struct{ name, body string }{
		{"backtick fence", "```markdown\n" + marker + "\n```\n"},
		{"tilde fence", "~~~~\n" + marker + "\n~~~~\n"},
		{"indented code", "    " + marker + "\n"},
		{"list container", "- " + marker + "\n"},
		{"quote container", "> " + marker + "\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spans, err := ScanMarkdownBlocks([]byte(tc.body))
			if err != nil || len(spans) != 0 {
				t.Fatalf("spans=%v err=%v", spans, err)
			}
		})
	}
	spans, err := ScanMarkdownBlocks([]byte(marker + "\n\n" + close + "\n"))
	if err != nil || len(spans) != 1 || spans[0].ValueStart != spans[0].ValueEnd {
		t.Fatalf("empty field spans=%v err=%v", spans, err)
	}
}

func TestMarkdownLexRejectsMalformedBlocks(t *testing.T) {
	openGoal := `<!-- session-reviewer:v4-field entity="project-overview" name="goal" -->`
	closeGoal := `<!-- /session-reviewer:v4-field entity="project-overview" name="goal" -->`
	openStage := `<!-- session-reviewer:v4-field entity="project-overview" name="stage" -->`
	closeStage := `<!-- /session-reviewer:v4-field entity="project-overview" name="stage" -->`
	tests := []struct {
		name, body, code string
	}{
		{"duplicate", openGoal + "\na\n" + closeGoal + "\n" + openGoal + "\nb\n" + closeGoal, MarkdownFieldDuplicate},
		{"nested", openGoal + "\n" + openStage + "\n" + closeStage + "\n" + closeGoal, MarkdownFormatInvalid},
		{"truncated", openGoal + "\nvalue\n", MarkdownFormatInvalid},
		{"mismatched", openGoal + "\nvalue\n" + closeStage, MarkdownFormatInvalid},
		{"unknown field", `<!-- session-reviewer:v4-field entity="project-overview" name="unknown" -->`, MarkdownFormatInvalid},
		{"foreign entity", `<!-- session-reviewer:v4-field entity="foreign:id" name="title" -->`, MarkdownFormatInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ScanMarkdownBlocks([]byte(tc.body))
			if got := MarkdownCodeOf(err); got != tc.code {
				t.Fatalf("code=%q want=%q err=%v", got, tc.code, err)
			}
		})
	}
}

func TestMarkdownLexGeneratedRegionsAndLimits(t *testing.T) {
	generated := "<!-- session-reviewer:v4-generated entity=\"project-overview\" name=\"problem-tree\" -->\n- node\n<!-- /session-reviewer:v4-generated entity=\"project-overview\" name=\"problem-tree\" -->\n"
	spans, err := ScanMarkdownBlocks([]byte(generated))
	if err != nil || len(spans) != 1 || !spans[0].Generated {
		t.Fatalf("spans=%v err=%v", spans, err)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"bare CR", []byte("bad\rline")},
		{"invalid utf8", []byte{0xff}},
		{"NUL", []byte{'a', 0}},
		{"over document limit", bytes.Repeat([]byte{'x'}, maxMarkdownDocumentBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ScanMarkdownBlocks(tc.raw); MarkdownCodeOf(err) != MarkdownFormatInvalid {
				t.Fatalf("err=%v", err)
			}
		})
	}
	tooLong := "<!-- session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\n" + strings.Repeat("x", maxMarkdownFieldBytes+1) + "\n<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\n"
	if _, err := ScanMarkdownBlocks([]byte(tooLong)); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("overlong field err=%v", err)
	}
	atLimit := strings.Replace(tooLong, strings.Repeat("x", maxMarkdownFieldBytes+1), strings.Repeat("x", maxMarkdownFieldBytes), 1)
	if _, err := ScanMarkdownBlocks([]byte(atLimit)); err != nil {
		t.Fatalf("field at byte limit rejected: %v", err)
	}
	problem := func(size int) string {
		return "<!-- session-reviewer:v4-field entity=\"problem:p\" name=\"question\" -->\n" + strings.Repeat("x", size) + "\n<!-- /session-reviewer:v4-field entity=\"problem:p\" name=\"question\" -->\n"
	}
	if _, err := ScanMarkdownBlocks([]byte(problem(4096))); err != nil {
		t.Fatalf("problem question at byte limit rejected: %v", err)
	}
	if _, err := ScanMarkdownBlocks([]byte(problem(4097))); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("overlong problem question err=%v", err)
	}
}

func FuzzMarkdownBlocks(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		[]byte("plain markdown\n"),
		[]byte("<!-- session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\nvalue\n<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\n"),
		[]byte("```\n<!-- session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\n```\n"),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		spans, err := ScanMarkdownBlocks(raw)
		if err != nil {
			return
		}
		previous := 0
		for _, span := range spans {
			if span.Start < previous || span.Start > span.ValueStart || span.ValueStart > span.ValueEnd || span.ValueEnd > span.End || span.End > len(raw) {
				t.Fatalf("invalid span %+v for %d bytes", span, len(raw))
			}
			previous = span.End
		}
	})
}
