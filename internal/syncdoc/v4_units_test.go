package syncdoc

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func TestV4UnitsExposeOnlyHumanFieldsAndShell(t *testing.T) {
	document, raw, _ := v4FixtureDocument(t, "项目回顾.md", "review.md")
	units := document.SemanticUnits()
	goal := UnitKey{Kind: UnitSection, Name: "session-reviewer/v4/project-overview/goal"}
	if got := string(units[goal].Value); got != "项目目标夹具" {
		t.Fatalf("goal=%q", got)
	}
	if _, found := units[UnitKey{Kind: UnitFrontmatter, Name: "custom_owner"}]; !found {
		t.Fatal("custom frontmatter was not exposed as a shell unit")
	}
	for _, name := range []string{"id", "entity_type", "project_id", "schema_version", "document_format", "revision", "generation_id", "minimum_reader_version", "minimum_writer_version"} {
		if units[UnitKey{Kind: UnitFrontmatter, Name: name}].Present {
			t.Fatalf("machine frontmatter %q became writable", name)
		}
	}
	rendered, err := document.Render()
	if err != nil || !bytes.Equal(rendered, raw) {
		t.Fatalf("unchanged v4 bytes changed: err=%v", err)
	}
}

func TestV4ShellReplacementChangesOnlySelectedField(t *testing.T) {
	_, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	crlf := bytes.ReplaceAll(raw, []byte("\n"), []byte("\r\n"))
	document, err := ParseV4("项目回顾.md", crlf, ledger)
	if err != nil {
		t.Fatal(err)
	}
	key := UnitKey{Kind: UnitSection, Name: "session-reviewer/v4/project-overview/goal"}
	units := document.SemanticUnits()
	unit := units[key]
	unit.Value = []byte("新的\r\n目标")
	units[key] = unit
	edited, err := document.WithSemanticUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := edited.Render()
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(crlf, []byte("项目目标夹具"), []byte("新的\r\n目标"), 1)
	if !bytes.Equal(rendered, want) {
		t.Fatal("field replacement rewrote the physical shell")
	}
	if _, err := ParseV4("项目回顾.md", rendered, ledger); err != nil {
		t.Fatalf("rendered field draft cannot be reparsed: %v", err)
	}
}

func TestV4ShellParagraphReorderingPreservesMixedPhysicalBytes(t *testing.T) {
	_, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	raw = bytes.Replace(raw, []byte("这段自定义文本和 [链接](https://example.test/custom) 必须原样保留。"), []byte("第一段。\n\n第二段。"), 1)
	mixed := bytes.Replace(raw, []byte("# 项目回顾\n\n这段自定义文本"), []byte("# 项目回顾\r\n\r\n这段自定义文本"), 1)
	document, err := ParseV4("项目回顾.md", mixed, ledger)
	if err != nil {
		t.Fatal(err)
	}
	units := document.SemanticUnits()
	found := false
	for key, unit := range units {
		if key.Kind != UnitSection || !bytes.Contains(unit.Value, []byte("第一段。")) {
			continue
		}
		unit.Value = bytes.Replace(unit.Value, []byte("第一段。\n\n第二段。"), []byte("第二段。\n\n第一段。"), 1)
		units[key] = unit
		found = true
		break
	}
	if !found {
		t.Fatal("custom shell unit not found")
	}
	edited, err := document.WithSemanticUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := edited.Render()
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(mixed, []byte("第一段。\n\n第二段。"), []byte("第二段。\n\n第一段。"), 1)
	if !bytes.Equal(rendered, want) {
		t.Fatal("custom paragraph edit rewrote unrelated mixed line endings")
	}
}

func TestV4ShellCustomFrontmatterCanBeAddedAndRemovedWithoutRewritingMachineBindings(t *testing.T) {
	document, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	units := document.SemanticUnits()
	delete(units, UnitKey{Kind: UnitFrontmatter, Name: "custom_owner"})
	units[UnitKey{Kind: UnitFrontmatter, Name: "custom_team"}] = Unit{Present: true, Value: []byte("codec\n")}

	edited, err := document.WithSemanticUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := edited.Render()
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(raw, []byte("custom_owner: 保留\n"), nil, 1)
	want = bytes.Replace(want, []byte("---\n# 项目回顾"), []byte("custom_team: codec\n---\n# 项目回顾"), 1)
	if !bytes.Equal(rendered, want) {
		t.Fatal("custom frontmatter edit rewrote unrelated document bytes")
	}
	if _, err := ParseV4("项目回顾.md", rendered, ledger); err != nil {
		t.Fatalf("custom frontmatter result cannot be reparsed: %v", err)
	}
}

func TestV4UnitsCannotAddOrDeleteFormalFields(t *testing.T) {
	document, _, _ := v4FixtureDocument(t, "项目回顾.md", "review.md")
	units := document.SemanticUnits()
	delete(units, UnitKey{Kind: UnitSection, Name: "session-reviewer/v4/project-overview/goal"})
	if _, err := document.WithSemanticUnits(units); err == nil {
		t.Fatal("deleted formal field was accepted")
	}
	units = document.SemanticUnits()
	units[UnitKey{Kind: UnitSection, Name: "session-reviewer/v4/project-overview/unknown"}] = Unit{Present: true, Value: []byte("x")}
	if _, err := document.WithSemanticUnits(units); err == nil {
		t.Fatal("added formal field was accepted")
	}
}

func TestV4ShellSupportsFiveMiBWithoutRelaxingLegacyLimit(t *testing.T) {
	_, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	custom := append([]byte("\n"+strings.Repeat("x", 5<<20)+"\n"), raw...)
	openingEnd := bytes.IndexByte(raw, '\n') + 1
	closing := bytes.Index(raw[openingEnd:], []byte("---\n"))
	if closing < 0 {
		t.Fatal("fixture frontmatter has no close")
	}
	insert := openingEnd + closing + len("---\n")
	custom = append(bytes.Clone(raw[:insert]), append(custom[:(5<<20)+2], raw[insert:]...)...)
	document, err := ParseV4("项目回顾.md", custom, ledger)
	if err != nil {
		t.Fatalf("v4 5 MiB document rejected: %v", err)
	}
	if rendered, err := document.Render(); err != nil || !bytes.Equal(rendered, custom) {
		t.Fatalf("large v4 round trip changed bytes: err=%v", err)
	}
	if _, err := Parse("项目回顾.md", custom); err == nil {
		t.Fatal("legacy parser accepted a document over 4 MiB")
	}
}

func TestV4SensitiveScanMasksOnlyAuthenticatedMarkerIdentities(t *testing.T) {
	_, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	fake := []byte("\n```markdown\n<!-- session-reviewer:v4-field entity=\"risk:/Users/private\" name=\"title\" -->\n```\n")
	raw = append(bytes.Clone(raw), fake...)
	document, err := ParseV4("项目回顾.md", raw, ledger)
	if err != nil {
		t.Fatal(err)
	}
	source, err := document.SensitiveScanSource()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(source, []byte("risk:/Users/private")) {
		t.Fatal("fenced marker-looking human content was masked")
	}
	if bytes.Contains(source, []byte(`entity="project-overview"`)) || !bytes.Contains(source, []byte(`entity="validated-marker"`)) {
		t.Fatal("authenticated marker identities were not masked")
	}
}

func TestV4PlaceholderAssemblyPreflightsInjectedLimit(t *testing.T) {
	placeholder := []byte("<!-- sr-v4-block:0 -->\n")
	block := v4BlockState{
		block:               reviewv4.MarkdownBlock{Key: reviewv4.FieldKey{Entity: "project-overview", Name: "goal"}, ValueStart: 2, ValueEnd: 5},
		unitKey:             UnitKey{Kind: UnitSection, Name: "session-reviewer/v4/project-overview/goal"},
		semanticPlaceholder: placeholder,
		original:            []byte("[old]"),
	}
	fields := UnitSet{block.unitKey: {Present: true, Value: []byte(strings.Repeat("x", 32))}}
	if result, err := replaceV4PlaceholdersBounded(placeholder, []v4BlockState{block}, fields, 16); err == nil || result != nil {
		t.Fatalf("over-limit block assembly result=%q err=%v", result, err)
	}
}

func TestV4SemanticUnitPreflightUsesInjectedLimit(t *testing.T) {
	units := UnitSet{
		{Kind: UnitPreamble}:                 {Present: true, Value: []byte("12345678")},
		{Kind: UnitSection, Name: "heading"}: {Present: true, Value: []byte("12345678"), HeadingPresentation: []byte("# heading\n")},
	}
	if err := preflightV4UnitSet(units, 16); err == nil {
		t.Fatal("aggregate-over-limit semantic units were accepted")
	}
}

func v4FixtureDocument(t *testing.T, relative, fixture string) (Document, []byte, reviewv4.MachineLedger) {
	t.Helper()
	ledgerBody, err := os.ReadFile("../../testdata/contracts/v4/markdown/ledger.json")
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := reviewv4.DecodeLedger(ledgerBody)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../testdata/contracts/v4/markdown/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	document, err := ParseV4(relative, raw, ledger)
	if err != nil {
		t.Fatal(err)
	}
	return document, raw, ledger
}
