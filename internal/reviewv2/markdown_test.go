package reviewv2

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestTwoDocumentRoundTripPreservesUnknownContentAndStableIDs(t *testing.T) {
	reviewSource := mustFixture(t, "../../testdata/review-v2/项目回顾.valid.md")
	reviewDoc, err := ParseReview(reviewSource)
	if err != nil {
		t.Fatal(err)
	}
	if reviewDoc.Model.Decisions[0].ID != "decision-local-cli" {
		t.Fatalf("decision=%+v", reviewDoc.Model.Decisions[0])
	}
	reviewOut, err := reviewDoc.Render()
	if err != nil || !bytes.Equal(reviewSource, reviewOut) {
		t.Fatalf("review round trip err=%v", err)
	}

	historySource := mustFixture(t, "../../testdata/review-v2/项目历史.valid.md")
	historyDoc, err := ParseHistory(historySource)
	if err != nil {
		t.Fatal(err)
	}
	if historyDoc.Events[0].ID != "timeline-trust-chain" {
		t.Fatalf("events=%+v", historyDoc.Events)
	}
	historyOut, err := historyDoc.Render()
	if err != nil || !bytes.Equal(historySource, historyOut) {
		t.Fatalf("history round trip err=%v", err)
	}
}

func TestMarkerScannerRejectsHostileStructuresAndIgnoresFencedMarkers(t *testing.T) {
	valid := mustFixture(t, "../../testdata/review-v2/项目历史.valid.md")
	document, err := ParseHistory(valid)
	if err != nil || len(document.Events) != 1 {
		t.Fatalf("fenced fake marker changed event scan: events=%d err=%v", len(document.Events), err)
	}

	tests := []struct {
		name   string
		source []byte
		want   string
	}{
		{
			name:   "duplicate identity",
			source: mustFixture(t, "../../testdata/review-v2/项目历史.invalid-duplicate-event.md"),
			want:   "duplicate marker identity",
		},
		{
			name:   "missing close",
			source: bytes.Replace(valid, []byte("<!-- /session-reviewer:event -->"), nil, 1),
			want:   "missing its closing marker",
		},
		{
			name:   "nested marker",
			source: bytes.Replace(valid, []byte("### 摘要\n"), []byte("<!-- session-reviewer:event id=\"timeline-nested\" -->\n### 摘要\n"), 1),
			want:   "nested event marker",
		},
		{
			name:   "upper-case ID",
			source: bytes.Replace(valid, []byte("timeline-trust-chain"), []byte("Timeline-trust-chain"), 1),
			want:   "stable lower-case",
		},
		{
			name:   "mismatched close",
			source: bytes.Replace(valid, []byte("/session-reviewer:event"), []byte("/session-reviewer:decision"), 1),
			want:   "does not match",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseHistory(test.source); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("hostile source accepted or misclassified: %v", err)
			}
		})
	}

	tooLarge := append(bytes.Clone(valid), bytes.Repeat([]byte("x"), MaxDocumentBytes-len(valid)+1)...)
	if _, err := ParseHistory(tooLarge); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized history accepted: %v", err)
	}

	var excessive strings.Builder
	excessive.WriteString("---\nid: project-history\nentity_type: project_history\nproject_id: project-many-blocks\nschema_version: 2\nrevision: 1\n---\n# 项目历史\n")
	for index := 0; index <= maxMarkerBlocks; index++ {
		fmt.Fprintf(&excessive, "<!-- session-reviewer:event id=\"event-%d\" -->\n<!-- /session-reviewer:event -->\n", index)
	}
	if _, err := ParseHistory([]byte(excessive.String())); err == nil || !strings.Contains(err.Error(), "more than 20000") {
		t.Fatalf("marker block limit not enforced: %v", err)
	}
}

func TestTwoDocumentCRLFInputNormalizesWithoutLosingUnicodeOrUnknownSlices(t *testing.T) {
	for _, fixture := range []struct {
		name  string
		parse func([]byte) ([]byte, error)
	}{
		{
			name: "review",
			parse: func(source []byte) ([]byte, error) {
				document, err := ParseReview(source)
				if err != nil {
					return nil, err
				}
				return document.Render()
			},
		},
		{
			name: "history",
			parse: func(source []byte) ([]byte, error) {
				document, err := ParseHistory(source)
				if err != nil {
					return nil, err
				}
				return document.Render()
			},
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			lf := mustFixture(t, "../../testdata/review-v2/项目"+map[string]string{"review": "回顾", "history": "历史"}[fixture.name]+".valid.md")
			crlf := bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n"))
			out, err := fixture.parse(crlf)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(out, []byte("\r")) || !bytes.Equal(out, lf) {
				t.Fatalf("CRLF did not normalize to the lossless LF form")
			}
		})
	}
}

func TestPatchReviewUnitUsesFieldAllowlistAndStaleHashProtection(t *testing.T) {
	source := mustFixture(t, "../../testdata/review-v2/项目回顾.valid.md")
	hash := markdownSHA256(source)

	goal, err := PatchReviewUnit(source, EditUnit{
		Document: ReviewRelativePath, UnitID: "project-overview", Field: "goal",
		Value: "保留 [括号]、\"引号\" 与中文路径。", ExpectedSHA256: hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseReview(goal)
	if err != nil || parsed.Model.Goal != "保留 [括号]、\"引号\" 与中文路径。" {
		t.Fatalf("goal patch failed: goal=%q err=%v", parsed.Model.Goal, err)
	}
	for _, preserved := range [][]byte{[]byte("custom_theme:"), []byte("自定义备注"), []byte("decision-in-fence"), []byte("```mermaid")} {
		if !bytes.Contains(goal, preserved) {
			t.Fatalf("goal patch lost unknown source slice %q", preserved)
		}
	}

	decision, err := PatchReviewUnit(source, EditUnit{
		Document: "review", UnitID: "decision-local-cli", Field: "decision.title",
		Value: "本地 CLI [无上传]", ExpectedSHA256: hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	decisionDoc, err := ParseReview(decision)
	if err != nil || decisionDoc.Model.Decisions[0].ID != "decision-local-cli" || decisionDoc.Model.Decisions[0].Title != "本地 CLI [无上传]" {
		t.Fatalf("decision title patch changed stable identity: %+v err=%v", decisionDoc.Model.Decisions, err)
	}

	if out, err := PatchReviewUnit(source, EditUnit{
		Document: "review", UnitID: "project-overview", Field: "status", Value: "stale", ExpectedSHA256: strings.Repeat("0", 64),
	}); err == nil || out != nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale patch mutated or was accepted: out=%q err=%v", out, err)
	}
	for _, forbidden := range []string{"schema_version", "project_id", "revision", "hashes", "evidence", "usage", "price", "sync_status", "decision.status"} {
		if out, err := PatchReviewUnit(source, EditUnit{
			Document: "review", UnitID: "project-overview", Field: forbidden, Value: "2", ExpectedSHA256: hash,
		}); err == nil || out != nil || !strings.Contains(err.Error(), "not editable") {
			t.Fatalf("forbidden field %q accepted: out=%q err=%v", forbidden, out, err)
		}
	}
}

func TestPatchHistoryUnitPreservesEventIdentityAndRejectsCrossDocumentEdits(t *testing.T) {
	source := mustFixture(t, "../../testdata/review-v2/项目历史.valid.md")
	hash := markdownSHA256(source)
	out, err := PatchHistoryUnit(source, EditUnit{
		Document: HistoryRelativePath, UnitID: "timeline-trust-chain", Field: "event.title",
		Value: "信任链 [v2] 与 \"dry-run\"", ExpectedSHA256: hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := ParseHistory(out)
	if err != nil || len(document.Events) != 1 || document.Events[0].ID != "timeline-trust-chain" || document.Events[0].Title != "信任链 [v2] 与 \"dry-run\"" {
		t.Fatalf("event title patch failed: events=%+v err=%v", document.Events, err)
	}
	if !bytes.Contains(out, []byte("### 自定义细节")) || !bytes.Contains(out, []byte("timeline-in-fence")) {
		t.Fatal("event patch lost unknown or fenced source content")
	}

	lists, err := PatchHistoryUnit(source, EditUnit{
		Document: "history", UnitID: "timeline-trust-chain", Field: "event.changes",
		Value: "保留 receipt\n- 验证 `/Vault/项目/[v2]`", ExpectedSHA256: hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	listDoc, err := ParseHistory(lists)
	if err != nil || len(listDoc.Events[0].Changes) != 2 || listDoc.Events[0].Changes[0] != "保留 receipt" {
		t.Fatalf("event list patch failed: changes=%v err=%v", listDoc.Events[0].Changes, err)
	}

	if out, err := PatchHistoryUnit(source, EditUnit{
		Document: "review", UnitID: "timeline-trust-chain", Field: "event.next", Value: "x", ExpectedSHA256: hash,
	}); err == nil || out != nil || !strings.Contains(err.Error(), "document") {
		t.Fatalf("cross-document patch accepted: out=%q err=%v", out, err)
	}
	if out, err := PatchHistoryUnit(source, EditUnit{
		Document: "history", UnitID: "timeline-trust-chain", Field: "event.next",
		Value: "ok\n<!-- session-reviewer:event id=\"event-injected\" -->", ExpectedSHA256: hash,
	}); err == nil || out != nil {
		t.Fatalf("marker injection patch accepted: out=%q err=%v", out, err)
	}
}

func TestPatchAllowlistFieldsEachRoundTripThroughTheirSemanticUnit(t *testing.T) {
	reviewSource := mustFixture(t, "../../testdata/review-v2/项目回顾.valid.md")
	reviewCases := []struct {
		unit  string
		field string
		value string
	}{
		{"project-overview", "goal", "新目标"},
		{"project-overview", "stage", "新阶段"},
		{"project-overview", "status", "新状态"},
		{"project-overview", "next_action", "新下一步"},
		{"risk-installer-permission", "risk.title", "新风险标题"},
		{"risk-installer-permission", "risk.status", "已缓解"},
		{"risk-installer-permission", "risk.detail", "新详情"},
		{"decision-local-cli", "decision.title", "新决策标题"},
		{"decision-local-cli", "decision.rationale", "新原因"},
		{"decision-local-cli", "decision.impact", "新影响"},
	}
	for _, test := range reviewCases {
		t.Run(test.field, func(t *testing.T) {
			out, err := PatchReviewUnit(reviewSource, EditUnit{
				Document: "review", UnitID: test.unit, Field: test.field, Value: test.value,
				ExpectedSHA256: markdownSHA256(reviewSource),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseReview(out); err != nil {
				t.Fatalf("patched review cannot reparse: %v", err)
			}
		})
	}

	historySource := mustFixture(t, "../../testdata/review-v2/项目历史.valid.md")
	historyCases := []struct {
		field string
		value string
	}{
		{"event.title", "新事件标题"},
		{"event.meaning", "新意义"},
		{"event.summary", "新摘要"},
		{"event.why", "新原因"},
		{"event.changes", "change one\nchange two"},
		{"event.results", "result one\nresult two"},
		{"event.next", "新下一步"},
	}
	for _, test := range historyCases {
		t.Run(test.field, func(t *testing.T) {
			out, err := PatchHistoryUnit(historySource, EditUnit{
				Document: "history", UnitID: "timeline-trust-chain", Field: test.field, Value: test.value,
				ExpectedSHA256: markdownSHA256(historySource),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseHistory(out); err != nil {
				t.Fatalf("patched history cannot reparse: %v", err)
			}
		})
	}
}

func TestTwoDocumentCanonicalRenderReparses(t *testing.T) {
	review := Review{
		ProjectID: "project-canonical-render", Revision: 2, Name: "项目 [v2]", Goal: "目标", Stage: "阶段", Status: "进行中",
		NextAction: "下一步", LastVerification: "go test ./...",
		Risks:     []Risk{{ID: "risk-one", Title: "风险", Status: "开放", Detail: "详情"}},
		Decisions: []Decision{{ID: "decision-one", OccurredAt: "2026-08-25", Title: "决策", Rationale: "原因", Impact: "影响", Status: "已采用"}},
	}
	reviewBytes, err := RenderReview(review)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := ParseReview(reviewBytes); err != nil || parsed.Model.ProjectID != review.ProjectID || parsed.Model.Decisions[0].ID != "decision-one" {
		t.Fatalf("canonical review did not reparse: model=%+v err=%v", parsed.Model, err)
	}

	events := []Event{{
		ID: "event-one", OccurredAt: "2026-08-25", Kind: "里程碑", Title: "事件", Meaning: "意义", Summary: "摘要", Why: "原因",
		Changes: []string{"change [1]"}, Results: []string{"result \"ok\""}, DecisionIDs: []string{"decision-one"}, Next: "next",
	}}
	historyBytes, err := RenderHistory(review.ProjectID, review.Revision, events)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := ParseHistory(historyBytes); err != nil || parsed.ProjectID != review.ProjectID || parsed.Events[0].ID != "event-one" {
		t.Fatalf("canonical history did not reparse: events=%+v err=%v", parsed.Events, err)
	}
}

func TestMarkerScannerRejectsIndentedReservedMarkerInsteadOfDroppingIdentity(t *testing.T) {
	source := mustFixture(t, "../../testdata/review-v2/项目历史.valid.md")
	source = bytes.Replace(source, []byte("<!-- session-reviewer:event id=\"timeline-trust-chain\" -->"), []byte(" <!-- session-reviewer:event id=\"timeline-trust-chain\" -->"), 1)
	source = bytes.Replace(source, []byte("<!-- /session-reviewer:event -->"), []byte(" <!-- /session-reviewer:event -->"), 1)
	if _, err := ParseHistory(source); err == nil || !strings.Contains(err.Error(), "exact marker line") {
		t.Fatalf("indented reserved marker silently lost its identity: %v", err)
	}
}

func TestMarkerScannerRejectsReviewBlocksOutsideTheirVisibleContainers(t *testing.T) {
	source := mustFixture(t, "../../testdata/review-v2/项目回顾.valid.md")
	tests := []struct {
		name string
		old  string
		new  string
	}{
		{"risk outside risk section", "## 风险与待办", "## 自定义风险"},
		{"decision outside decision section", "## 关键决策", "## 自定义决策"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			misplaced := bytes.Replace(source, []byte(test.old), []byte(test.new), 1)
			if _, err := ParseReview(misplaced); err == nil || !strings.Contains(err.Error(), "outside") {
				t.Fatalf("misplaced stable block accepted: %v", err)
			}
		})
	}
}

func TestPatchHistoryUnitCanInsertMissingOptionalSummary(t *testing.T) {
	source := mustFixture(t, "../../testdata/review-v2/项目历史.valid.md")
	summary := []byte("### 摘要\n修复 receipt 信任边界。\n")
	source = bytes.Replace(source, summary, nil, 1)
	if parsed, err := ParseHistory(source); err != nil || parsed.Events[0].Summary != "" {
		t.Fatalf("history without optional summary is not valid: summary=%q err=%v", parsed.Events[0].Summary, err)
	}
	patched, err := PatchHistoryUnit(source, EditUnit{
		Document: "history", UnitID: "timeline-trust-chain", Field: "event.summary", Value: "按需插入的摘要。",
		ExpectedSHA256: markdownSHA256(source),
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseHistory(patched)
	if err != nil || parsed.Events[0].Summary != "按需插入的摘要。" || parsed.Events[0].ID != "timeline-trust-chain" {
		t.Fatalf("inserted summary did not preserve event semantics: event=%+v err=%v", parsed.Events[0], err)
	}
	if !bytes.Contains(patched, []byte("### 自定义细节")) {
		t.Fatal("summary insertion dropped an unknown subsection")
	}
}

func TestTwoDocumentCanonicalRenderAcceptsExplicitEmptyCollections(t *testing.T) {
	review := Review{
		ProjectID: "project-empty-review", Revision: 1, Name: "空项目", Goal: "目标", Stage: "起步", Status: "新建",
		NextAction: "首次回顾", LastVerification: "尚未验证", Risks: []Risk{}, Decisions: []Decision{},
	}
	if _, err := RenderReview(review); err != nil {
		t.Fatalf("explicit empty review collections rejected: %v", err)
	}
	if _, err := RenderHistory(review.ProjectID, review.Revision, []Event{}); err != nil {
		t.Fatalf("explicit empty history collection rejected: %v", err)
	}
	event := Event{
		ID: "event-empty-references", OccurredAt: "2026-08-25", Kind: "验证", Title: "空关联决策", Meaning: "意义", Summary: "",
		Why: "原因", Changes: []string{"change"}, Results: []string{"result"}, DecisionIDs: []string{}, Next: "next",
	}
	if _, err := RenderHistory(review.ProjectID, review.Revision, []Event{event}); err != nil {
		t.Fatalf("explicit empty event references rejected: %v", err)
	}
}

func markdownSHA256(source []byte) string {
	digest := sha256.Sum256(source)
	return fmt.Sprintf("%x", digest)
}
