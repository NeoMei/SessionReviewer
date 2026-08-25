package ledger

import (
	"bytes"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/accounting"
)

func TestRenderPlansAllNavigationArtifactsAndRepeatIsNoop(t *testing.T) {
	root := ledgerFixture(t)
	state, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Render(state, completeChanges())
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(plan.Files))
	for _, file := range plan.Files {
		paths = append(paths, file.RelativePath)
	}
	for _, want := range []string{
		"docs/session-review/project-overview.md",
		"docs/session-review/diagrams/project-evolution.md",
		"docs/session-review/decisions/00-目录说明.md",
		"docs/session-review/open-loops/00-目录说明.md",
		"docs/session-review/sessions/00-目录说明.md",
		"docs/session-review/decisions/decision-1.md",
		"docs/session-review/open-loops/loop-1.md",
		"docs/session-review/sessions/session-s1.md",
	} {
		if !slices.Contains(paths, want) {
			t.Fatalf("missing %s in %v", want, paths)
		}
	}
	if _, err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	next, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := Render(next, ChangeSet{})
	if err != nil {
		t.Fatal(err)
	}
	if len(repeat.Files) != 0 {
		t.Fatalf("repeat planned files=%v", repeat.Files)
	}
}

func TestLoadAndSnapshotIgnoreOnlyExactStandaloneDerivedPaths(t *testing.T) {
	root := ledgerFixture(t)
	for _, relative := range []string{
		"decisions/00-目录说明.md",
		"open-loops/00-目录说明.md",
		"sessions/00-目录说明.md",
	} {
		writeLedgerFile(t, root, relative, []byte("# generated\n"), 0o644)
	}
	if _, err := Load(root); err != nil {
		t.Fatalf("generated indexes broke ledger load: %v", err)
	}
	for _, relative := range []string{
		"docs/session-review/decisions/00-目录说明.md",
		"docs/session-review/open-loops/00-目录说明.md",
		"docs/session-review/sessions/00-目录说明.md",
	} {
		if IsSnapshotPath(relative) || IsCollectionSnapshotPath(relative) {
			t.Fatalf("derived index entered source snapshot: %s", relative)
		}
	}
	if !IsSnapshotPath("docs/session-review/decisions/00-目录说明-copy.md") {
		t.Fatal("lookalike semantic path was incorrectly excluded")
	}
}

func TestRenderRecoveryMermaidIsFiveNodeBoundedDeterministicAndRuneSafe(t *testing.T) {
	state := navigationState(t)
	first, err := RenderRecoveryMermaid(state)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderRecoveryMermaid(state)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("same state rendered different Mermaid bytes")
	}
	for _, want := range []string{
		"flowchart LR",
		"goal[\"项目目标",
		"decisions[\"关键决策汇总",
		"milestones[\"最近已验证里程碑",
		"current[\"当前状态",
		"next[\"下一步",
		"goal --> decisions --> milestones --> current --> next",
		"另有 1 项",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("missing %q:\n%s", want, first)
		}
	}
	if got := strings.Count(first, "[\""); got != 5 {
		t.Fatalf("node count=%d\n%s", got, first)
	}
	if strings.Contains(first, "未验证事件") || !strings.Contains(first, "验证三个") {
		t.Fatalf("verified milestone selection is wrong:\n%s", first)
	}
	if !utf8.ValidString(first) || len([]rune(first)) > recoveryMermaidRuneBudget {
		t.Fatal("Mermaid is invalid or unbounded")
	}
}

func TestRenderDerivedArtifactsCreatesReadableHomepageIndexesAndQuickSections(t *testing.T) {
	artifacts, err := RenderDerivedArtifacts(navigationState(t))
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string]DerivedArtifact, len(artifacts))
	for _, artifact := range artifacts {
		if _, exists := byPath[artifact.RelativePath]; exists {
			t.Fatalf("duplicate artifact path %s", artifact.RelativePath)
		}
		byPath[artifact.RelativePath] = artifact
	}
	for _, relative := range []string{
		"docs/session-review/project-overview.md",
		"docs/session-review/diagrams/project-evolution.md",
		"docs/session-review/decisions/00-目录说明.md",
		"docs/session-review/open-loops/00-目录说明.md",
		"docs/session-review/sessions/00-目录说明.md",
		"docs/session-review/decisions/decision-a.md",
		"docs/session-review/open-loops/loop-a.md",
		"docs/session-review/sessions/session-a.md",
	} {
		if _, exists := byPath[relative]; !exists {
			t.Fatalf("missing artifact %s", relative)
		}
	}

	overview := byPath["docs/session-review/project-overview.md"].Data
	for _, want := range []string{
		"## 项目导航",
		GeneratedMarkerPrefix,
		"```mermaid",
		"项目总览",
		"总耗时",
		"1,000",
		"$0.250000000",
		"decisions/00-目录说明.md",
	} {
		if !bytes.Contains(overview, []byte(want)) {
			t.Fatalf("overview missing %q:\n%s", want, overview)
		}
	}
	if bytes.Index(overview, []byte("## 项目导航")) > bytes.Index(overview, []byte("## User notes")) {
		t.Fatalf("generated homepage section was not prepended:\n%s", overview)
	}

	decisionIndex := byPath["docs/session-review/decisions/00-目录说明.md"].Data
	for _, want := range []string{"此文件由 SessionReviewer 生成", "[采用三方同步]", "已接受", "为什么采用"} {
		if !bytes.Contains(decisionIndex, []byte(want)) {
			t.Fatalf("decision index missing %q:\n%s", want, decisionIndex)
		}
	}

	for _, relative := range []string{
		"docs/session-review/decisions/decision-a.md",
		"docs/session-review/open-loops/loop-a.md",
		"docs/session-review/sessions/session-a.md",
	} {
		body := byPath[relative].Data
		if !bytes.Contains(body, []byte("## 快速理解")) || !bytes.Contains(body, []byte(GeneratedMarkerPrefix)) {
			t.Fatalf("quick section missing from %s:\n%s", relative, body)
		}
	}
}

func TestRenderDerivedArtifactsIndexesLinkToRenamedDocuments(t *testing.T) {
	state := navigationState(t)
	decision := state.documents.decisions["decision-a"]
	decision.RelativePath = "docs/session-review/decisions/renamed.md"
	state.documents.decisions["decision-a"] = decision

	artifacts, err := RenderDerivedArtifacts(state)
	if err != nil {
		t.Fatal(err)
	}
	index, found := derivedArtifactBytes(artifacts, "docs/session-review/decisions/00-目录说明.md")
	if !found {
		t.Fatal("decision index is missing")
	}
	if !bytes.Contains(index, []byte("(<renamed.md>)")) {
		t.Fatalf("renamed document is not linked by its real path:\n%s", index)
	}
	if bytes.Contains(index, []byte("(<decision-a.md>)")) {
		t.Fatalf("index links to the stable ID instead of the real path:\n%s", index)
	}
}

func TestMarkdownNavigationTextRendersHostileMarkupLiterally(t *testing.T) {
	got := markdownNavigationText("<script>alert(1)</script> & `code` *bold* _emphasis_ [link]")
	for _, forbidden := range []string{"<script>", "`code`", "*bold*", "_emphasis_", "[link]"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("generated Markdown retained active markup %q in %q", forbidden, got)
		}
	}
	for _, want := range []string{"&lt;script&gt;", "&amp;", "\\`code\\`", "\\*bold\\*", "\\_emphasis\\_", "\\[link\\]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated Markdown did not preserve %q literally: %q", want, got)
		}
	}
}

func navigationState(t *testing.T) State {
	t.Helper()
	const projectID = "project-1111111111111111"
	state := State{
		ProjectID: projectID,
		CurrentState: CurrentState{
			ProjectID:    projectID,
			Goal:         strings.Repeat("让项目脉络清晰可恢复", 20),
			LastVerified: "三方同步已验证",
			Blockers:     []string{"等待真实 Vault 验收"},
			OpenRisks:    []string{"跨平台路径"},
			NextAction:   "运行真实 Obsidian 验收",
		},
		Timeline: []TimelineEvent{
			{ID: "event-0", OccurredAt: "2026-08-20T00:00:00Z", Class: Verified, Title: "验证零"},
			{ID: "event-1", OccurredAt: "2026-08-21T00:00:00Z", Class: Verified, Title: "验证一", DecisionIDs: []string{"decision-a"}},
			{ID: "event-2", OccurredAt: "2026-08-22T00:00:00Z", Class: Verified, Title: "验证二", DecisionIDs: []string{"decision-b"}},
			{ID: "event-x", OccurredAt: "2026-08-22T12:00:00Z", Class: Inference, Title: "未验证事件"},
			{ID: "event-3", OccurredAt: "2026-08-23T00:00:00Z", Class: Verified, Title: "验证三个", DecisionIDs: []string{"decision-c"}},
		},
		Decisions: map[string]Decision{
			"decision-a": {ID: "decision-a", ProjectID: projectID, Title: "采用三方同步", Status: "accepted", Tags: []string{"sync"}, Rationale: "为什么采用：避免覆盖人工编辑。"},
			"decision-b": {ID: "decision-b", ProjectID: projectID, Title: "使用稳定 ID", Status: "accepted"},
			"decision-c": {ID: "decision-c", ProjectID: projectID, Title: "固定首页", Status: "accepted"},
			"decision-d": {ID: "decision-d", ProjectID: projectID, Title: "保留未知章节", Status: "proposed"},
		},
		OpenLoops: map[string]OpenLoop{
			"loop-a": {ID: "loop-a", ProjectID: projectID, Title: "真实 Vault 验收", Status: "open", Question: "是否能双端一致？", Blocker: "等待本机验证", NextExperiment: "运行 sync"},
		},
		Sessions: map[string]SessionReport{
			"session-a": {
				ID: "session-a", ProjectID: projectID, SessionID: "source-session-a", InitialGoal: "解决项目遗忘",
				Phases:       []SessionPhase{{Title: "实现导航", Summary: "补齐首页和目录索引。"}},
				Verification: []string{"全量测试通过"},
				Accounting: &accounting.SessionAccounting{
					StartedAt: "2026-08-24T00:00:00Z", EndedAt: "2026-08-24T00:01:00Z", DurationMS: 60_000,
					TotalTokens: 1_000, TotalCostUSD: 0.25,
					Models: []accounting.ModelAccounting{{ModelUsage: accounting.ModelUsage{Model: "gpt-test", TokenUsage: accounting.TokenUsage{TotalTokens: 1_000}}, CostUSD: 0.25}},
				},
			},
		},
		documents: stateDocuments{
			decisions: make(map[string]loadedDocument),
			openLoops: make(map[string]loadedDocument),
			sessions:  make(map[string]loadedDocument),
		},
	}
	overview := navigationLoadedDocument(t, "docs/session-review/project-overview.md", []byte("---\nid: project-overview\nentity_type: project_overview\nproject_id: "+projectID+"\nrevision: 2\n---\n\n# Project\n\n## User notes\n\nKeep this.\n"))
	state.documents.overview = &overview
	for id := range state.Decisions {
		state.documents.decisions[id] = navigationLoadedDocument(t, "docs/session-review/decisions/"+id+".md", decisionDocument(id, projectID))
	}
	state.documents.openLoops["loop-a"] = navigationLoadedDocument(t, "docs/session-review/open-loops/loop-a.md", openLoopDocument("loop-a", projectID))
	state.documents.sessions["session-a"] = navigationLoadedDocument(t, "docs/session-review/sessions/session-a.md", sessionDocument("session-a", "source-session-a", projectID))
	return state
}

func navigationLoadedDocument(t *testing.T, relative string, body []byte) loadedDocument {
	t.Helper()
	doc, err := ParseDocument(body)
	if err != nil {
		t.Fatalf("parse %s: %v", relative, err)
	}
	return loadedDocument{Document: doc, RelativePath: relative, Original: bytes.Clone(body), Perm: fs.FileMode(0o644)}
}
