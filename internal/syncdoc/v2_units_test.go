package syncdoc

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV2HistoryUnitsUseStableMarkerIdentityAcrossTitleEdits(t *testing.T) {
	base := v2FixtureDocument(t, "项目历史.valid.md", "项目历史.md")
	editedSource := bytes.Replace(v2FixtureBytes(t, "项目历史.valid.md"), []byte("信任链与 dry-run 边界修复"), []byte("新的可见标题"), 1)
	edited, err := Parse("项目历史.md", editedSource)
	if err != nil {
		t.Fatal(err)
	}

	key := UnitKey{Kind: UnitSection, Name: "session-reviewer/event/timeline-trust-chain"}
	baseUnit, baseOK := base.SemanticUnits()[key]
	editedUnit, editedOK := edited.SemanticUnits()[key]
	if !baseOK || !editedOK || !baseUnit.Present || !editedUnit.Present {
		t.Fatalf("stable unit missing: base=%v edited=%v", baseOK, editedOK)
	}
	if bytes.Equal(baseUnit.Value, editedUnit.Value) || !bytes.Contains(editedUnit.Value, []byte("新的可见标题")) {
		t.Fatalf("marker value did not carry title edit: %q", editedUnit.Value)
	}
	for candidate := range edited.SemanticUnits() {
		if candidate.Kind == UnitSection && strings.Contains(candidate.Name, "新的可见标题") {
			t.Fatalf("visible title leaked into semantic identity: %+v", candidate)
		}
	}
}

func TestV2WithSemanticUnitsRebuildsMarkerInPlaceAndPreservesUnknownGaps(t *testing.T) {
	const before = "\n## 自定义顶层\nmarker 前的未知内容\n\n"
	const after = "\n## 另一个自定义顶层\nmarker 后的未知内容\n"
	source := v2FixtureBytes(t, "项目历史.valid.md")
	source = bytes.Replace(source, []byte("\n<!-- session-reviewer:event"), []byte(before+"<!-- session-reviewer:event"), 1)
	source = bytes.Replace(source, []byte("<!-- /session-reviewer:event -->\n"), []byte("<!-- /session-reviewer:event -->"+after), 1)
	base, err := Parse("项目历史.md", source)
	if err != nil {
		t.Fatal(err)
	}
	editedSource := bytes.Replace(source, []byte("信任链与 dry-run 边界修复"), []byte("替换后标题"), 1)
	edited, err := Parse("项目历史.md", editedSource)
	if err != nil {
		t.Fatal(err)
	}
	units := base.SemanticUnits()
	key := UnitKey{Kind: UnitSection, Name: "session-reviewer/event/timeline-trust-chain"}
	units[key] = edited.SemanticUnits()[key]

	rebuilt, err := base.WithSemanticUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := rebuilt.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rendered, []byte(before)) || !bytes.Contains(rendered, []byte(after)) || !bytes.Contains(rendered, []byte("替换后标题")) {
		t.Fatalf("rebuild lost marker position or unknown gaps:\n%s", rendered)
	}
	reparsed, err := Parse("项目历史.md", rendered)
	if err != nil {
		t.Fatalf("rebuilt document does not reparse: %v", err)
	}
	if !reparsed.SemanticUnits()[key].Present {
		t.Fatal("reparsed document lost stable marker unit")
	}
}

func v2FixtureDocument(t *testing.T, name, relative string) Document {
	t.Helper()
	document, err := Parse(relative, v2FixtureBytes(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func v2FixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "review-v2", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}
