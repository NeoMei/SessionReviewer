package reviewv4

import (
	"strings"
	"testing"
)

func TestMarkdownBoundedWriterRejectsBeforeCrossingLimit(t *testing.T) {
	writer := newMarkdownBoundedWriter(8)
	if _, err := writer.WriteString("1234"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString("56789"); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("overflow err=%v", err)
	}
	if got := writer.Len(); got != 4 {
		t.Fatalf("writer grew to %d bytes after rejecting overflow", got)
	}
}

func TestRenderFreshMarkdownHonorsInjectedLimit(t *testing.T) {
	p := minimumPresentation()
	p.Revision = 1
	p.CurrentState.Goal = strings.Repeat("x", 512)
	if pair, err := renderFreshMarkdownLimit(p, 256); MarkdownCodeOf(err) != MarkdownFormatInvalid || pair.Review != nil || pair.History != nil {
		t.Fatalf("bounded render lengths=(%d,%d) err=%v", len(pair.Review), len(pair.History), err)
	}
}

func TestReplaceMarkdownBlocksHonorsInjectedLimitBeforeAssembly(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	pair, err := RenderMarkdown(ledger.DocumentProjection.PresentationBase, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	document, err := ParseMarkdownDocument(markdownReviewRelative, pair.Review)
	if err != nil {
		t.Fatal(err)
	}
	replacements := make(map[FieldKey]string, len(document.blocks))
	for _, block := range document.blocks {
		replacements[block.Key] = markdownBlockValue(document, block)
	}
	replacements[FieldKey{Entity: "project-overview", Name: "goal"}] = strings.Repeat("x", 1024)
	if body, err := replaceMarkdownBlocksLimit(document, replacements, len(pair.Review)+8); MarkdownCodeOf(err) != MarkdownFormatInvalid || body != nil {
		t.Fatalf("bounded replacement len=%d err=%v", len(body), err)
	}
}
