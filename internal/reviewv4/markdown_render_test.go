package reviewv4

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func sharedMarkdownLedger(t *testing.T) MachineLedger {
	t.Helper()
	ledger, err := DecodeLedger(mustRead(t, "../../testdata/contracts/v4/markdown/ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

func TestMarkdownRenderAndDraftRoundTripFullCatalog(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	base := ledger.DocumentProjection.PresentationBase
	pair, err := RenderMarkdown(base, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	review, err := ParseMarkdownDocument("项目回顾.md", pair.Review)
	if err != nil {
		t.Fatal(err)
	}
	history, err := ParseMarkdownDocument("项目历史.md", pair.History)
	if err != nil {
		t.Fatal(err)
	}
	fields := review.Fields()
	for key, value := range history.Fields() {
		fields[key] = value
	}
	if len(fields) != 24 {
		t.Fatalf("rendered field count = %d, want 24", len(fields))
	}
	if got := fields[FieldKey{Entity: "milestone:milestone:alpha", Name: "conclusion"}]; got != "人工字段可编辑，结构仍需显式操作。" {
		t.Fatalf("rendered conclusion = %q", got)
	}
	for _, visible := range []string{"# 项目回顾", "# 项目历史", "触发", "执行", "验证", "Coverage"} {
		if !bytes.Contains(pair.Review, []byte(visible)) && !bytes.Contains(pair.History, []byte(visible)) {
			t.Fatalf("render omitted %q", visible)
		}
	}
	ledger.ReviewSHA256 = sha256Hex(pair.Review)
	ledger.HistorySHA256 = sha256Hex(pair.History)
	ledger.SyncHashes.ReviewSHA256 = ledger.ReviewSHA256
	ledger.SyncHashes.HistorySHA256 = ledger.HistorySHA256
	ledgerBody, err := RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = DecodeLedger(ledgerBody)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := ParseMarkdownDraft(pair, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Edits) != 0 || !reflect.DeepEqual(draft.Presentation, base) {
		t.Fatalf("unchanged draft changed presentation: edits=%+v", draft.Edits)
	}
}

func TestMarkdownRenderShowsStaticLabelsWithoutDuplicatingEditableValues(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	pair, err := RenderMarkdown(ledger.DocumentProjection.PresentationBase, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{
		"### 目标", "### 阶段", "### 状态", "### 下一步", "### 上次验证",
		"### 标题", "### 理由", "### 影响", "### 重新评估条件", "### 详情",
		"### 问题", "### 下一个实验", "### 完成标准", "### 当前结论", "### 摘要", "### 结论", "### 影响与后续",
	} {
		if !bytes.Contains(pair.Review, []byte(label)) && !bytes.Contains(pair.History, []byte(label)) {
			t.Fatalf("rendered Markdown omitted visible label %q", label)
		}
	}
	for _, value := range []string{
		ledger.DocumentProjection.PresentationBase.Decisions[0].Title,
		ledger.DocumentProjection.PresentationBase.Risks[0].Title,
		ledger.DocumentProjection.PresentationBase.Timeline[0].Title,
	} {
		if count := bytes.Count(pair.Review, []byte(value)) + bytes.Count(pair.History, []byte(value)); count != 1 {
			t.Fatalf("editable title %q appears %d times, want once", value, count)
		}
	}
}

func TestMarkdownRenderPreservesAcceptedCRLFBytesWhenValuesAreUnchanged(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	base := ledger.DocumentProjection.PresentationBase
	pair, err := RenderMarkdown(base, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	pair.Review = bytes.ReplaceAll(pair.Review, []byte("\n"), []byte("\r\n"))
	pair.History = bytes.ReplaceAll(pair.History, []byte("\n"), []byte("\r\n"))
	ledger = bindMarkdownPair(t, ledger, pair)

	got, err := RenderMarkdown(base, ledger, &pair)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, pair) {
		t.Fatal("unchanged accepted CRLF document bytes were rewritten")
	}
}

func TestMarkdownRenderUsesCollisionFreeAnchorsForValidIDs(t *testing.T) {
	ledger := projectedLedger(t)
	p := ledger.DocumentProjection.PresentationBase
	p.Revision = 1
	ledger.AcceptedRevision = 1
	p.Decisions = []Decision{
		minimumDecision("a:b", []string{}),
		minimumDecision("ab", []string{}),
	}
	p.Decisions[0].Title, p.Decisions[0].Pinned = "colon", true
	p.Decisions[1].Title, p.Decisions[1].Pinned = "plain", true
	ledger.DocumentProjection.PresentationBase = p
	seed, err := RenderMarkdown(p, bindMarkdownPair(t, ledger, MarkdownPair{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	ledger = bindMarkdownPair(t, ledger, seed)
	for _, target := range []string{"decision-x613a62", "decision-x6162"} {
		if bytes.Count(seed.Review, []byte(`<a id="`+target+`"></a>`)) != 1 || bytes.Count(seed.Review, []byte(`(#`+target+`)`)) != 1 {
			t.Fatalf("anchor/link target %q is not unique and identical:\n%s", target, seed.Review)
		}
	}
	if bytes.Contains(seed.Review, []byte("%")) || bytes.Contains(seed.Review, []byte("decision-a:b")) {
		t.Fatalf("anchors are not injective:\n%s", seed.Review)
	}
	if _, err := ParseMarkdownDraft(seed, ledger); err != nil {
		t.Fatalf("canonical collision case did not round trip: %v", err)
	}
}

func TestMarkdownDraftLargeDocumentUsesIndexedBlockAndAnchorMatching(t *testing.T) {
	ledger := projectedLedger(t)
	p := ledger.DocumentProjection.PresentationBase
	p.Revision = 1
	ledger.AcceptedRevision = 1
	p.Risks = make([]Risk, 5000)
	for index := range p.Risks {
		p.Risks[index] = Risk{ID: fmt.Sprintf("risk:%04d", index), Title: "title", Status: "open", Detail: "detail"}
	}
	ledger.DocumentProjection.PresentationBase = p
	pair, err := RenderMarkdown(p, bindMarkdownPair(t, ledger, MarkdownPair{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	ledger = bindMarkdownPair(t, ledger, pair)
	if _, err := ParseMarkdownDraft(pair, ledger); err != nil {
		t.Fatal(err)
	}
	review, err := ParseMarkdownDocument(markdownReviewRelative, pair.Review)
	if err != nil {
		t.Fatal(err)
	}
	index, err := newMarkdownDocumentIndex(markdownReviewRelative, review)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.blocks) != len(review.blocks) || len(index.anchors) != len(p.Risks) {
		t.Fatalf("index coverage blocks=%d/%d anchors=%d/%d", len(index.blocks), len(review.blocks), len(index.anchors), len(p.Risks))
	}
	for _, key := range []FieldKey{{Entity: "risk:risk:0000", Name: "title"}, {Entity: "risk:risk:4999", Name: "status"}} {
		if _, ok := index.blocks[key]; !ok {
			t.Fatalf("index omitted boundary key %+v", key)
		}
	}
}

func bindMarkdownPair(t *testing.T, ledger MachineLedger, pair MarkdownPair) MachineLedger {
	t.Helper()
	ledger.ReviewSHA256 = sha256Hex(pair.Review)
	ledger.HistorySHA256 = sha256Hex(pair.History)
	ledger.SyncHashes.ReviewSHA256 = ledger.ReviewSHA256
	ledger.SyncHashes.HistorySHA256 = ledger.HistorySHA256
	body, err := RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := DecodeLedger(body)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func TestMarkdownRenderRejectsRevisionZero(t *testing.T) {
	ledger := projectedLedger(t)
	ledger.AcceptedRevision = 0
	ledger.DocumentProjection.PresentationBase.Revision = 0
	if _, err := RenderMarkdown(ledger.DocumentProjection.PresentationBase, ledger, nil); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("revision-zero render err=%v", err)
	}
}

func TestMarkdownRenderPreservesPreviousCustomBytesAndStableIDLinks(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	base := ledger.DocumentProjection.PresentationBase
	previous := MarkdownPair{Review: mustRead(t, "../../testdata/contracts/v4/markdown/review.md"), History: mustRead(t, "../../testdata/contracts/v4/markdown/history.md")}
	next, err := ApplyMarkdownEdits(base, []FieldEdit{{
		Key:    FieldKey{Entity: "decision:decision:alpha", Name: "title"},
		Before: "选择 [Markdown] 作为人工编辑面。", After: "改名后的决策",
	}})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := RenderMarkdown(next, ledger, &previous)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(pair.Review, []byte("这段自定义文本")) || !bytes.Contains(pair.Review, []byte("#decision-x6465636973696f6e3a616c706861")) {
		t.Fatalf("custom bytes or stable ID link lost:\n%s", pair.Review)
	}
	if strings.Count(string(pair.Review), "改名后的决策") != 1 {
		t.Fatal("editable title was duplicated outside its field")
	}
}

func TestMarkdownRenderAdvancesGenerationFromAuthenticatedPreviousBase(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	base := ledger.DocumentProjection.PresentationBase
	previous := MarkdownPair{Review: mustRead(t, "../../testdata/contracts/v4/markdown/review.md"), History: mustRead(t, "../../testdata/contracts/v4/markdown/history.md")}
	next := clonePresentation(base)
	next.GenerationID = "generation-2"
	next.ProjectViewDigest = "sha256:" + strings.Repeat("2", 64)
	next.Revision++
	for index := range next.Timeline {
		next.Timeline[index].GenerationID = next.GenerationID
	}
	pair, err := RenderMarkdown(next, ledger, &previous)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"review": pair.Review, "history": pair.History} {
		if !bytes.Contains(body, []byte("generation_id: generation-2")) || bytes.Contains(body, []byte("generation_id: generation-1")) || !bytes.Contains(body, []byte("revision: 2")) {
			t.Fatalf("%s frontmatter did not advance:\n%s", name, body)
		}
	}
	again, err := RenderMarkdown(next, ledger, &previous)
	if err != nil || !reflect.DeepEqual(pair, again) {
		t.Fatalf("same input drifted: err=%v", err)
	}
}

func TestMarkdownRenderAddsNewGenerationEntitiesWhilePreservingPreviousShell(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	base := ledger.DocumentProjection.PresentationBase
	previous := MarkdownPair{Review: mustRead(t, "../../testdata/contracts/v4/markdown/review.md"), History: mustRead(t, "../../testdata/contracts/v4/markdown/history.md")}
	next := clonePresentation(base)
	next.GenerationID = "generation-2"
	next.ProjectViewDigest = "sha256:" + strings.Repeat("2", 64)
	next.Revision++
	for index := range next.Timeline {
		next.Timeline[index].GenerationID = next.GenerationID
	}
	next.Risks = append(next.Risks, Risk{ID: "risk:new", Title: "新风险", Status: "open", Detail: "新扫描发现"})
	next.Timeline = append(next.Timeline, Timeline{ID: "milestone:new", GenerationID: next.GenerationID, OccurredAt: "2026-09-06", Kind: "milestone", Title: "新里程碑", Summary: "新扫描摘要", DecisionIDs: []string{}, ClosedLoop: NeutralClosedLoop()})
	pair, err := RenderMarkdown(next, ledger, &previous)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(pair.Review, []byte(`risk:risk:new`)) || !bytes.Contains(pair.Review, []byte(`<a id="risk-x7269736b3a6e6577"></a>`)) || !bytes.Contains(pair.Review, []byte("这段自定义文本")) {
		t.Fatalf("new entity or previous shell missing:\n%s", pair.Review)
	}
	if !bytes.Contains(pair.History, []byte(`milestone:milestone:new`)) || !bytes.Contains(pair.History, []byte(`<a id="milestone-x6d696c6573746f6e653a6e6577"></a>`)) || !bytes.Contains(pair.History, []byte("历史自定义附注保留。")) {
		t.Fatalf("new milestone or previous history shell missing:\n%s", pair.History)
	}
}

func TestMarkdownRenderRejectsRemovingAcceptedEntity(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	base := ledger.DocumentProjection.PresentationBase
	previous := MarkdownPair{Review: mustRead(t, "../../testdata/contracts/v4/markdown/review.md"), History: mustRead(t, "../../testdata/contracts/v4/markdown/history.md")}
	next := clonePresentation(base)
	next.Risks = []Risk{}
	next.Revision++
	if _, err := RenderMarkdown(next, ledger, &previous); MarkdownCodeOf(err) != MarkdownStructureEditRequiresCommand {
		t.Fatalf("removed entity err=%v", err)
	}
}

func TestMarkdownRenderRejectsModifiedStableAnchor(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	base := ledger.DocumentProjection.PresentationBase
	previous := MarkdownPair{Review: mustRead(t, "../../testdata/contracts/v4/markdown/review.md"), History: mustRead(t, "../../testdata/contracts/v4/markdown/history.md")}
	previous.Review = bytes.Replace(previous.Review, []byte(`<a id="decision-x6465636973696f6e3a616c706861"></a>`), []byte(`<a id="decision-forged"></a>`), 1)
	if _, err := RenderMarkdown(base, ledger, &previous); MarkdownCodeOf(err) != MarkdownStructureEditRequiresCommand {
		t.Fatalf("modified anchor err=%v", err)
	}
}

func TestMarkdownRenderRejectsUnauthenticatedPreviousCustomBytes(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	base := ledger.DocumentProjection.PresentationBase
	previous := MarkdownPair{Review: mustRead(t, "../../testdata/contracts/v4/markdown/review.md"), History: mustRead(t, "../../testdata/contracts/v4/markdown/history.md")}
	previous.Review = append(previous.Review, []byte("\n未接受的自定义内容\n")...)
	if _, err := RenderMarkdown(base, ledger, &previous); MarkdownCodeOf(err) != MarkdownBaselineMissing {
		t.Fatalf("unauthenticated previous err=%v", err)
	}
}

func TestMarkdownRenderRejectsLedgerWithStaleSelfDigest(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	ledger.ReviewSHA256 = strings.Repeat("a", 64)
	ledger.SyncHashes.ReviewSHA256 = ledger.ReviewSHA256
	if _, err := RenderMarkdown(ledger.DocumentProjection.PresentationBase, ledger, nil); MarkdownCodeOf(err) != MarkdownBaselineMissing {
		t.Fatalf("stale self digest err=%v", err)
	}
}

func TestLoadProjectionDispatchesValidatedMarkdownCombination(t *testing.T) {
	root := "../../testdata/contracts/v4/markdown/"
	accepted, err := LoadProjection(mustRead(t, root+"review.md"), mustRead(t, root+"history.md"), mustRead(t, root+"ledger.json"), mustRead(t, root+"index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Ledger.DocumentProjection == nil || !reflect.DeepEqual(accepted.Review, accepted.Ledger.DocumentProjection.PresentationBase) {
		t.Fatal("Markdown projection did not return the authenticated typed base")
	}
	if _, err := LoadProjection(mustRead(t, root+"review-duplicate-field.md"), mustRead(t, root+"history.md"), mustRead(t, root+"ledger.json"), mustRead(t, root+"index.json")); MarkdownCodeOf(err) != MarkdownFieldDuplicate {
		t.Fatalf("draft parser diagnostic lost: %v", err)
	}
	legacyLedger := frozenLedger(t)
	legacyBody, err := RenderLedger(legacyLedger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProjection(mustRead(t, root+"review.md"), mustRead(t, root+"history.md"), legacyBody, mustRead(t, root+"index.json")); err == nil {
		t.Fatal("accepted Markdown documents with a legacy JSON ledger combination")
	}
}
