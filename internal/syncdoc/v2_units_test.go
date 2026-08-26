package syncdoc

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

func TestV2CRLFSemanticUnitsAndRebuildUseOneCoordinateSpace(t *testing.T) {
	source := bytes.ReplaceAll(v2FixtureBytes(t, "项目历史.valid.md"), []byte("\n"), []byte("\r\n"))
	document, err := Parse("项目历史.md", source)
	if err != nil {
		t.Fatal(err)
	}
	key := UnitKey{Kind: UnitSection, Name: "session-reviewer/event/timeline-trust-chain"}
	units := document.SemanticUnits()
	marker, ok := units[key]
	if !ok || !marker.Present || !bytes.HasPrefix(marker.Value, []byte("<!-- session-reviewer:event")) {
		t.Fatalf("CRLF marker unit sliced wrong bytes: ok=%v value=%q", ok, marker.Value)
	}
	marker.Value = bytes.Replace(marker.Value, []byte("信任链与 dry-run 边界修复"), []byte("CRLF 标题"), 1)
	units[key] = marker
	rebuilt, err := document.WithSemanticUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := rebuilt.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rendered, []byte("CRLF 标题")) || !bytes.Contains(rendered, []byte("timeline-in-fence")) {
		t.Fatalf("CRLF rebuild lost edited or fenced content:\n%s", rendered)
	}
	if _, err := Parse("项目历史.md", rendered); err != nil {
		t.Fatalf("CRLF rebuild did not reparse: %v", err)
	}
}

func TestV2OldMergeAnchorTextIsAlwaysOrdinaryUserContent(t *testing.T) {
	const oldAnchor = "<!-- sr-v2-unit:event:timeline-trust-chain -->"
	base := v2FixtureBytes(t, "项目历史.valid.md")
	placements := map[string]struct{ needle, replacement string }{
		"ordinary-top-level": {"<!-- session-reviewer:event id=\"timeline-trust-chain\" -->", "## 用户顶层\n" + oldAnchor + "\n\n<!-- session-reviewer:event id=\"timeline-trust-chain\" -->"},
		"marker-body":        {"修复 receipt 信任边界。", "修复 receipt 信任边界。\n" + oldAnchor},
		"fenced-code":        {"  A[信任] --> B[收敛]", "  A[信任] --> B[收敛]\n  " + oldAnchor},
		"trailing-content":   {"<!-- /session-reviewer:event -->", "<!-- /session-reviewer:event -->\n\n## 用户尾部\n" + oldAnchor},
	}
	for name, placement := range placements {
		t.Run(name, func(t *testing.T) {
			source := bytes.Replace(base, []byte(placement.needle), []byte(placement.replacement), 1)
			if bytes.Equal(source, base) {
				t.Fatal("test placement did not change fixture")
			}
			assertV2AnchorRoundTrip(t, source, oldAnchor)
		})
	}

	t.Run("inter-block-gap", func(t *testing.T) {
		second := `
<!-- session-reviewer:event id="timeline-release" -->
## 2026-08-24 · 发布
### 事件类别
里程碑
### 节点意义
发布。
### 摘要
发布。
### 为什么会走到这里
需要验证。
### 发生了什么
- 验证
### 结果与验证
- 通过
### 留下的问题或下一步
继续。
<!-- /session-reviewer:event -->
`
		source := append(bytes.TrimSuffix(base, []byte("\n")), []byte("\n\n"+oldAnchor+"\n"+second)...)
		assertV2AnchorRoundTrip(t, source, oldAnchor)
	})
}

func TestV2ComputedLegacyPhysicalAnchorCannotCollideAcrossDocuments(t *testing.T) {
	source := v2FixtureBytes(t, "项目历史.valid.md")
	digest := sha256.Sum256(source)
	seed := append(bytes.Clone(digest[:]), []byte("session-reviewer/event/timeline-trust-chain")...)
	seed = append(seed, []byte(strconv.Itoa(0)+":"+strconv.Itoa(0))...)
	anchorDigest := sha256.Sum256(seed)
	computedAnchor := fmt.Sprintf("<!-- sr-v2-anchor:%x -->", anchorDigest)
	editedSource := bytes.Replace(source, []byte("<!-- session-reviewer:event"), []byte("## 用户可见 anchor\n"+computedAnchor+"\n\n<!-- session-reviewer:event"), 1)
	base, err := Parse("项目历史.md", source)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := Parse("项目历史.md", editedSource)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := base.WithSemanticUnits(edited.SemanticUnits())
	if err != nil {
		t.Fatalf("valid user anchor collided with base shell state: %v", err)
	}
	rendered, err := rebuilt.Render()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(rendered, []byte(computedAnchor)) != 1 {
		t.Fatalf("user anchor was not preserved exactly once:\n%s", rendered)
	}
}

func assertV2AnchorRoundTrip(t *testing.T, source []byte, anchor string) {
	t.Helper()
	document, err := Parse("项目历史.md", source)
	if err != nil {
		t.Fatal(err)
	}
	key := UnitKey{Kind: UnitSection, Name: "session-reviewer/event/timeline-trust-chain"}
	units := document.SemanticUnits()
	if unit, ok := units[key]; !ok || !unit.Present {
		t.Fatalf("semantic extraction silently lost marker: %+v", units)
	}
	rebuilt, err := document.WithSemanticUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := rebuilt.Render()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := bytes.Count(rendered, []byte(anchor)), bytes.Count(source, []byte(anchor)); got != want {
		t.Fatalf("user anchor count=%d want=%d\n%s", got, want, rendered)
	}
	if _, err := Parse("项目历史.md", rendered); err != nil {
		t.Fatalf("rebuilt document did not reparse: %v", err)
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
