package presentation

import (
	"bytes"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

func TestRenderCarriesSourceSessionAccountingNameAndSyncMetadata(t *testing.T) {
	sessionAccounting := &accounting.SessionAccounting{
		StartedAt: "2026-09-01T00:00:00Z", EndedAt: "2026-09-01T00:00:01Z", DurationMS: 1000,
		Models:      []accounting.ModelAccounting{{ModelUsage: accounting.ModelUsage{Model: "gpt-test", TokenUsage: accounting.TokenUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}}}},
		TotalTokens: 120,
	}
	summary, err := accounting.Aggregate([]*accounting.SessionAccounting{sessionAccounting})
	if err != nil {
		t.Fatal(err)
	}
	input := ProjectInput{
		ProjectView: projectViewFixture(), GenerationID: "generation-400000000000100", Revision: 10,
		ProjectName: "AgentWiki", Accounting: summary, LastSuccessfulSync: "2026-09-03T12:00:00Z",
		SessionReports: []ledger.SessionReport{{
			ID: "session-accounting", ProjectID: "project-demo", SessionID: "codex/session-demo", Revision: 1,
			PreviousSessionID: "", NextSessionID: "", Accounting: sessionAccounting,
		}},
	}
	output, err := Project(input)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Render(input, output)
	if err != nil {
		t.Fatal(err)
	}
	machine, err := reviewv2.ParseMachineLedgerV3(plannedBytes(t, plan, reviewv2.MachineLedgerRelativePath))
	if err != nil {
		t.Fatal(err)
	}
	if output.Review.Name != "AgentWiki" || machine.LastSuccessfulSync != input.LastSuccessfulSync || machine.Accounting.TotalTokens != 120 || len(machine.Sessions) != 1 {
		t.Fatalf("projection lost public metadata: name=%q machine=%+v", output.Review.Name, machine)
	}
}

func TestRecentProgressIsConciseAndUsesHumanReadableExcerpt(t *testing.T) {
	view := projectViewFixture()
	view.DerivedRecords = nil
	for index := 0; index < 30; index++ {
		view.DerivedRecords = append(view.DerivedRecords, memory.DerivedRecord{
			ID: "event-ref-" + strings.Repeat("a", index%2+1), Kind: "event_ref", Subject: "opaque-message-id",
			OccurredAt: "2026-09-01T01:00:00Z", Fields: map[string]string{"operation": "user_request", "excerpt": "修复扫描问题\n并重新测试"},
		})
	}
	got := recentProgress(view)
	if lines := strings.Count(got, "\n") + 1; lines != 20 {
		t.Fatalf("recent progress lines=%d want 20", lines)
	}
	if !strings.Contains(got, "修复扫描问题 并重新测试") || strings.Contains(got, "opaque-message-id") {
		t.Fatalf("recent progress is not human readable: %q", got)
	}
}

func TestRecentProgressDropsPlatformInstructionsAndExtractsAttachedRequest(t *testing.T) {
	view := projectViewFixture()
	view.DerivedRecords = []memory.DerivedRecord{
		{ID: "event-system", Kind: "event_ref", OccurredAt: "2026-09-01T02:00:00Z", Fields: map[string]string{"operation": "user_request", "excerpt": "# AGENTS.md instructions\n<INSTRUCTIONS>internal</INSTRUCTIONS>"}},
		{ID: "event-request", Kind: "event_ref", OccurredAt: "2026-09-01T01:00:00Z", Fields: map[string]string{"operation": "user_request", "excerpt": "# Files mentioned by the user:\nfoo.png\n\n## My request:\n修复真实问题"}},
	}
	got := recentProgress(view)
	if got != "- 2026-09-01T01:00:00Z · 修复真实问题" {
		t.Fatalf("unexpected human request projection: %q", got)
	}
}

func projectViewFixture() memory.ProjectView {
	observation := digest64("5")
	view := memory.ProjectView{
		SchemaVersion: 1, ProjectID: "project-demo", Generation: 1,
		StartedAt: "2026-09-01T00:00:00Z", EndedAt: "2026-09-02T00:00:00Z",
		SourceSessions: 2, TerminalCounts: memory.TerminalCounts{Indexed: 2},
		SessionViewDependencies: []memory.SessionViewDependency{
			{Provider: "codex", SessionID: "session-demo", Digest: digest64("6")},
			{Provider: "codex", SessionID: "session-other", Digest: digest64("a")},
		},
		ObservationRevisionIDs: []string{observation},
		ProbeStateDigest:       digest64("7"), LiveState: memory.StateSnapshot{Branch: "main", Head: strings40("a"), DirtyPathCount: 2},
		WitnessedState: []memory.DerivedRecord{{
			ID: "witness-branch", Kind: "witnessed_state", Subject: "branch", OccurredAt: "2026-09-01T00:00:00Z",
			DependencyRevisionIDs: []string{observation}, RuleID: "newest", RuleVersion: "v1", Fields: map[string]string{"value": "main"},
		}},
		AggregationCoverage: memory.ProjectAggregationCoverage{
			ObservationSummariesSeen:  1,
			WitnessedKeys:             memory.AggregationChannelCoverage{Seen: 1, Emitted: 1},
			EventReferences:           memory.AggregationChannelCoverage{Seen: 1, Emitted: 1},
			SelectedEvidenceRevisions: memory.AggregationChannelCoverage{Seen: 2, Emitted: 1, Collapsed: 1},
		},
		DerivedRecords: []memory.DerivedRecord{{
			ID: "event-ref-a", Kind: "event_ref", Subject: "完成零 token 扫描", OccurredAt: "2026-09-01T01:00:00Z",
			DependencyRevisionIDs: []string{observation}, RuleID: "event", RuleVersion: "v1",
			Fields: map[string]string{"provider": "codex", "session_id": "session-demo", "sequence": "1", "fact_kind": "verification"},
		}},
		AssociatedUsage: []memory.AssociatedUsage{
			{Provider: "codex", SessionID: "session-demo", UsageRecordDigest: digest64("8"), Shared: true},
			{Provider: "codex", SessionID: "session-other", UsageRecordDigest: digest64("b"), Shared: false},
		},
		DependencyDigest: digest64("9"), ReducerVersion: "project-view-v1",
	}
	digest, err := memory.ProjectViewDigest(view)
	if err != nil {
		panic(err)
	}
	view.Digest = digest
	return view
}

func legacyPresentationFixture() reviewv2.LegacyPresentation {
	return reviewv2.LegacyPresentation{
		Review: reviewv2.Review{
			Name: "Demo", Goal: "自动目标", Stage: "main", Status: "自动状态", NextAction: "自动下一步",
			Risks:     []reviewv2.Risk{{ID: "risk-demo", Title: "自动风险", Status: "open", Detail: "自动风险详情"}},
			Decisions: []reviewv2.Decision{{ID: "decision-demo", Title: "自动决策", Rationale: "自动原因", Impact: "自动影响", Status: "accepted"}},
		},
		Events: []reviewv2.Event{{
			ID: "event-demo", OccurredAt: "2026-09-01T02:00:00Z", Kind: "verification", Title: "自动历史",
			Meaning: "自动意义", Summary: "自动摘要", Why: "自动原因", Changes: []string{"自动变更"},
			Results: []string{"自动结果"}, Next: "自动下一步",
		}},
		Compatibility: reviewv2.LegacyCompatibility{
			Timeline: []ledger.TimelineEvent{}, Decisions: []ledger.Decision{},
			OpenLoops: []ledger.OpenLoop{}, CurrentRisks: []reviewv2.CurrentRiskProvenance{},
		},
	}
}

func TestRenderPreservesUnknownHumanMarkdownWhileRefreshingGeneratedSections(t *testing.T) {
	input := ProjectInput{
		ProjectView:  projectViewFixture(),
		GenerationID: "generation-400000000000099",
		Revision:     9,
		Legacy:       legacyPresentationFixture(),
	}
	output, err := Project(input)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Render(input, output)
	if err != nil {
		t.Fatal(err)
	}
	review := plannedBytes(t, first, reviewv2.ReviewRelativePath)
	history := plannedBytes(t, first, reviewv2.HistoryRelativePath)
	ledger := plannedBytes(t, first, reviewv2.MachineLedgerRelativePath)

	const customFrontmatter = "human_plugin:\n  keep: 'exact'\n"
	review = bytes.Replace(review, []byte("---\n# "), []byte(customFrontmatter+"---\n# "), 1)
	const reviewCustom = "\n## 人工补充\n\n原样保留 `custom`。\n"
	review = append(review, []byte(reviewCustom)...)
	const historyCustom = "\n## 人工历史备注\n\n不要丢掉。\n"
	history = append(history, []byte(historyCustom)...)
	review = bytes.Replace(review, []byte("2 个 Session"), []byte("被手工篡改的生成统计"), 1)

	input.ExpectedFiles = map[string][]byte{
		reviewv2.ReviewRelativePath:        review,
		reviewv2.HistoryRelativePath:       history,
		reviewv2.MachineLedgerRelativePath: ledger,
	}
	second, err := Render(input, output)
	if err != nil {
		t.Fatal(err)
	}
	nextReview := plannedBytes(t, second, reviewv2.ReviewRelativePath)
	nextHistory := plannedBytes(t, second, reviewv2.HistoryRelativePath)
	for _, want := range [][]byte{[]byte(customFrontmatter), []byte(reviewCustom), []byte("2 个 Session")} {
		if !bytes.Contains(nextReview, want) {
			t.Fatalf("review lost preserved or regenerated bytes %q:\n%s", want, nextReview)
		}
	}
	if bytes.Contains(nextReview, []byte("被手工篡改的生成统计")) {
		t.Fatalf("machine-owned generated section accepted a human override:\n%s", nextReview)
	}
	if !bytes.Contains(nextHistory, []byte(historyCustom)) {
		t.Fatalf("history lost custom section:\n%s", nextHistory)
	}
	if _, err := reviewv2.LoadV3Bytes(nextReview, nextHistory, plannedBytes(t, second, reviewv2.MachineLedgerRelativePath)); err != nil {
		t.Fatalf("preserved projection does not reparse: %v", err)
	}
}

func TestRenderMigratesGeneratedProjectIDTitleWithoutDuplicatingRoot(t *testing.T) {
	legacy := legacyPresentationFixture()
	legacy.Review.Name = "project-demo"
	firstInput := ProjectInput{
		ProjectView: projectViewFixture(), GenerationID: "generation-400000000000101", Revision: 10,
		Legacy: legacy, UnknownBlocks: map[string][]byte{"custom": []byte("人工体")},
	}
	firstOutput, err := Project(firstInput)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Render(firstInput, firstOutput)
	if err != nil {
		t.Fatal(err)
	}
	firstReview := plannedBytes(t, first, reviewv2.ReviewRelativePath)
	historyStart := bytes.Index(firstReview, []byte("## 项目历史\n"))
	generatedStart := bytes.Index(firstReview[historyStart:], []byte("## 近期进展\n")) + historyStart
	if historyStart < 0 || generatedStart < historyStart {
		t.Fatalf("fixture is missing review history or generated sections:\n%s", firstReview)
	}
	// Reproduce the legacy ordering produced by the old presentation merge:
	// generated sections appeared before the project-history link.
	review := append([]byte(nil), firstReview[:historyStart]...)
	review = append(review, firstReview[generatedStart:]...)
	review = append(review, firstReview[historyStart:generatedStart]...)
	review = append(review, []byte("\n## 人工补充\n\n必须保留。\n")...)

	secondInput := firstInput
	secondInput.ProjectName = "AgentWiki"
	// Legacy versions could strand a generated marker in the preceding
	// machine field; loading that document surfaced it as LastVerification.
	secondInput.Legacy.Review.LastVerification = generatedSectionOpen(GeneratedSectionRecentProgress)
	secondInput.ExpectedFiles = map[string][]byte{
		reviewv2.ReviewRelativePath:        review,
		reviewv2.HistoryRelativePath:       plannedBytes(t, first, reviewv2.HistoryRelativePath),
		reviewv2.MachineLedgerRelativePath: plannedBytes(t, first, reviewv2.MachineLedgerRelativePath),
	}
	secondOutput, err := Project(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(secondInput, secondOutput)
	if err != nil {
		t.Fatal(err)
	}
	nextReview := plannedBytes(t, second, reviewv2.ReviewRelativePath)
	if bytes.Count(nextReview, []byte("\n# ")) != 1 || !bytes.Contains(nextReview, []byte("\n# AgentWiki\n")) {
		t.Fatalf("generated title migration produced invalid roots:\n%s", nextReview)
	}
	if !bytes.Contains(nextReview, []byte("## 人工补充\n\n必须保留。")) {
		t.Fatalf("generated title migration lost human section:\n%s", nextReview)
	}
	for _, marker := range [][]byte{
		[]byte(generatedSectionOpen(GeneratedSectionRecentProgress)),
		[]byte(generatedSectionClose(GeneratedSectionRecentProgress)),
		[]byte(generatedSectionOpen(GeneratedSectionModelUsage)),
		[]byte(generatedSectionClose(GeneratedSectionModelUsage)),
	} {
		if count := bytes.Count(nextReview, marker); count != 1 {
			t.Fatalf("generated marker %q count=%d want 1:\n%s", marker, count, nextReview)
		}
	}
	for _, identity := range []string{GeneratedSectionRecentProgress, GeneratedSectionModelUsage} {
		open := bytes.Index(nextReview, []byte(generatedSectionOpen(identity)))
		close := bytes.Index(nextReview, []byte(generatedSectionClose(identity)))
		if open < 0 || close <= open {
			t.Fatalf("generated section %q is not properly ordered:\n%s", identity, nextReview)
		}
	}
	customHeading := bytes.Index(nextReview, []byte("## 自定义内容\n"))
	customOpen := bytes.Index(nextReview, []byte(generatedSectionOpen(GeneratedSectionCustomContent)))
	if customOpen >= 0 && (customHeading < 0 || customOpen <= customHeading) {
		t.Fatalf("custom-content marker is outside its section:\n%s", nextReview)
	}
}

func plannedBytes(t *testing.T, plan RenderPlan, relative string) []byte {
	t.Helper()
	for _, file := range plan.Files {
		if file.Relative == relative {
			return append([]byte(nil), file.Desired...)
		}
	}
	t.Fatalf("missing plan file %s", relative)
	return nil
}

func TestCaptureCustomContentPreservesPayloadAndRejectsBrokenMarkers(t *testing.T) {
	payload := []byte("## 用户章节\n\n保留空格  \n")
	source := append([]byte(generatedSectionOpen(GeneratedSectionCustomContent)+"\n## 自定义内容\n"), payload...)
	source = append(source, []byte("\n"+generatedSectionClose(GeneratedSectionCustomContent)+"\n")...)
	captured, err := CaptureCustomContent(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(captured["preserved"], payload) {
		t.Fatalf("captured payload changed: got=%q want=%q", captured["preserved"], payload)
	}
	if _, err := CaptureCustomContent(bytes.TrimSuffix(source, []byte(generatedSectionClose(GeneratedSectionCustomContent)+"\n"))); err == nil {
		t.Fatal("unclosed custom-content section was accepted")
	}
	current := append([]byte("## 自定义内容\n"+generatedSectionOpen(GeneratedSectionCustomContent)+"\n"), payload...)
	current = append(current, []byte("\n"+generatedSectionClose(GeneratedSectionCustomContent)+"\n")...)
	captured, err = CaptureCustomContent(current)
	if err != nil || !bytes.Equal(captured["preserved"], payload) {
		t.Fatalf("current custom-content format was not captured: captured=%q err=%v", captured["preserved"], err)
	}
}
