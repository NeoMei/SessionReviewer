package presentation

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

func TestProjectHumanPrecedenceSuppressOrphanAndUnknownBytes(t *testing.T) {
	input := ProjectInput{
		ProjectView:  projectViewFixture(),
		GenerationID: "generation-400000000000001",
		Revision:     7,
		Legacy:       legacyPresentationFixture(),
		ActivePatches: []Patch{
			{EntityID: "project-overview", Field: "goal", Operation: Set, Value: "人工目标", BaseGeneratedHash: strings64("1")},
			{EntityID: "project-overview", Field: "status", Operation: Set, Value: "人工状态", BaseGeneratedHash: strings64("2")},
			{EntityID: "project-overview", Field: "next_action", Operation: Suppress, BaseGeneratedHash: strings64("3")},
			{EntityID: "decision-demo", Field: "title", Operation: Set, Value: "人工决策", BaseGeneratedHash: strings64("a")},
			{EntityID: "risk-demo", Field: "visibility", Operation: Suppress, BaseGeneratedHash: strings64("b")},
		},
		OrphanPatches: []Patch{{
			EntityID: "decision-gone", Field: "title", Operation: Set, Value: "未附着人工决策",
			BaseGeneratedHash: strings64("4"),
		}},
		UnknownBlocks: map[string][]byte{"custom": []byte("<!-- 自定义保留块 -->")},
		Accounting:    accounting.ProjectSummary{TotalTokens: 1234, Models: []accounting.ProjectModelSummary{{Model: "gpt-test", TotalTokens: 1234, TokenSharePct: 100}}},
	}
	output, err := Project(input)
	if err != nil {
		t.Fatal(err)
	}
	if output.Review.Goal != "人工目标" || output.Review.Status != "人工状态" || output.Review.NextAction != "" {
		t.Fatalf("human precedence failed: %+v", output.Review)
	}
	if len(output.OrphanPatches) != 1 || output.OrphanPatches[0].EntityID != "decision-gone" {
		t.Fatalf("orphan patch was attached or lost: %+v", output.OrphanPatches)
	}
	if output.Review.Decisions[0].Title != "人工决策" || len(output.Review.Risks) != 0 {
		t.Fatalf("human decision/risk precedence failed: %+v", output.Review)
	}
	if !bytes.Equal(output.UnknownBlocks["custom"], input.UnknownBlocks["custom"]) {
		t.Fatal("unknown custom block bytes changed")
	}
	if !strings.Contains(output.RecentProgress, "完成零 token 扫描") || strings.Contains(output.RecentProgress, "event-ref-a") {
		t.Fatalf("selected deterministic progress missing: %q", output.RecentProgress)
	}
	if !strings.Contains(output.Usage, "2 个 Session") || !strings.Contains(output.Usage, "1 个跨项目共享") || !strings.Contains(output.Usage, "gpt-test · 1,234 tokens") || strings.Contains(output.Usage, "session-demo") {
		t.Fatalf("concise aggregate usage is missing or leaks session references: %q", output.Usage)
	}
}

func TestProjectBuildsBoundedHumanReadableHistoryFromRequests(t *testing.T) {
	view := projectViewFixture()
	view.DerivedRecords = nil
	for index := 0; index < 25; index++ {
		view.DerivedRecords = append(view.DerivedRecords, memory.DerivedRecord{
			ID: "event-request-" + strconv.Itoa(index), Kind: "event_ref", OccurredAt: "2026-09-01T01:00:00Z",
			Fields: map[string]string{"operation": "user_request", "excerpt": "处理项目请求 " + strconv.Itoa(index)},
		})
	}
	view.DerivedRecords = append(view.DerivedRecords, memory.DerivedRecord{
		ID: "event-system", Kind: "event_ref", OccurredAt: "2026-09-02T01:00:00Z",
		Fields: map[string]string{"operation": "user_request", "excerpt": "# AGENTS.md instructions\ninternal"},
	})
	view.DerivedRecords[0].Fields["excerpt"] = "修复 `SessionReviewer` 的 **扫描** [问题](https://example.test)"
	events := projectHistoryEvents(view, nil, nil, nil)
	if len(events) != 20 {
		t.Fatalf("history event count=%d want 20", len(events))
	}
	for _, event := range events {
		if event.ID == "event-system" || event.Kind != "user_request" || event.Meaning == "" || event.Why == "" || len(event.Changes) == 0 || len(event.Results) == 0 || event.Next == "" {
			t.Fatalf("invalid projected history event: %+v", event)
		}
	}
	if _, err := reviewv2.RenderHistory("project-demo", 1, events); err != nil {
		t.Fatalf("projected history is not renderable: %v", err)
	}
}

func TestProjectHistoryRetainsHumanEditedGeneratedEventBeyondRecentWindow(t *testing.T) {
	view := projectViewFixture()
	view.DerivedRecords = nil
	for index := 0; index < 21; index++ {
		view.DerivedRecords = append(view.DerivedRecords, memory.DerivedRecord{
			ID: "event-request-" + strconv.Itoa(index), Kind: "event_ref", OccurredAt: "2026-09-01T01:00:00Z",
			Fields: map[string]string{"operation": "user_request", "excerpt": "请求 " + strconv.Itoa(index)},
		})
	}
	protected := reviewv2.Event{ID: "event-request-9", OccurredAt: "2026-09-01T01:00:00Z", Kind: "user_request", Title: "人工标题"}
	events := projectHistoryEvents(view, []reviewv2.Event{protected}, nil, []string{protected.ID})
	if len(events) != 21 {
		t.Fatalf("history event count=%d want 21", len(events))
	}
	found := false
	for _, event := range events {
		if event.ID == protected.ID && event.Title == protected.Title {
			found = true
		}
	}
	if !found {
		t.Fatalf("human-edited generated event was pruned: %+v", events)
	}
}

func TestRenderV3ProducesVerifiedThreeFileCASPlan(t *testing.T) {
	input := ProjectInput{
		ProjectView:   projectViewFixture(),
		GenerationID:  "generation-400000000000002",
		Revision:      8,
		Legacy:        legacyPresentationFixture(),
		UnknownBlocks: map[string][]byte{"custom": []byte("<!-- 自定义保留块 -->")},
	}
	output, err := Project(input)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := Render(input, output)
	if err != nil {
		t.Fatal(err)
	}
	input.ExpectedFiles = map[string][]byte{
		reviewv2.ReviewRelativePath: plannedBytes(t, initial, reviewv2.ReviewRelativePath),
	}
	plan, err := Render(input, output)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ProjectID != input.ProjectView.ProjectID || plan.GenerationID != input.GenerationID ||
		plan.ProjectViewDigest != input.ProjectView.Digest || len(plan.Files) != 3 {
		t.Fatalf("plan identity=%+v", plan)
	}
	var review, history, ledger FilePlan
	for _, file := range plan.Files {
		switch file.Relative {
		case reviewv2.ReviewRelativePath:
			review = file
		case reviewv2.HistoryRelativePath:
			history = file
		case reviewv2.MachineLedgerRelativePath:
			ledger = file
		}
	}
	if review.Desired == nil || history.Desired == nil || ledger.Desired == nil ||
		review.Mode != 0o644 || history.Mode != 0o644 || ledger.Mode != 0o600 {
		t.Fatalf("file plans review=%+v history=%+v ledger=%+v", review, history, ledger)
	}
	if !review.ExpectedExists || !bytes.Equal(review.Expected, input.ExpectedFiles[reviewv2.ReviewRelativePath]) ||
		history.ExpectedExists || ledger.ExpectedExists {
		t.Fatalf("preimages review=%+v history=%+v ledger=%+v", review, history, ledger)
	}
	if !bytes.Contains(review.Desired, []byte("<!-- 自定义保留块 -->")) || strings.Contains(string(review.Desired), "Agent") {
		t.Fatal("review desired bytes did not preserve custom content without an Agent-only section")
	}
	for _, identity := range []string{GeneratedSectionRecentProgress, GeneratedSectionModelUsage, GeneratedSectionCustomContent} {
		open := "<!-- presentation:section id=\"" + identity + "\" -->"
		close := "<!-- /presentation:section id=\"" + identity + "\" -->"
		if !bytes.Contains(review.Desired, []byte(open)) || !bytes.Contains(review.Desired, []byte(close)) {
			t.Fatalf("generated section %s lacks stable identity markers", identity)
		}
	}
	if len(review.Desired) > reviewv2.MaxDocumentBytes || len(history.Desired) > reviewv2.MaxDocumentBytes {
		t.Fatal("rendered human document exceeds the four MiB contract")
	}
	accepted, err := reviewv2.LoadV3Bytes(review.Desired, history.Desired, ledger.Desired)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.State.Review.ProjectID != input.ProjectView.ProjectID || accepted.State.Machine.GenerationID != input.GenerationID {
		t.Fatalf("parsed projection identity=%+v", accepted.State.Machine)
	}
	if len(accepted.State.Machine.GeneratedBaselines) != len(output.Baselines) || len(accepted.State.Machine.OrphanPatches) != len(output.OrphanPatches) {
		t.Fatalf("ledger baselines/orphans did not preserve projection sets: %+v", accepted.State.Machine)
	}
	for index, baseline := range output.Baselines {
		if accepted.State.Machine.GeneratedBaselines[index].GeneratedHash != baseline.GeneratedHash {
			t.Fatalf("baseline %d hash changed across public wire: got=%s gotwire=%+v want=%s wantvalue=%+v", index,
				accepted.State.Machine.GeneratedBaselines[index].GeneratedHash, accepted.State.Machine.GeneratedBaselines[index],
				baseline.GeneratedHash, baseline)
		}
	}
	again, err := Render(input, output)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan, again) {
		t.Fatal("render plan is not deterministic")
	}
}

func TestRenderV3RoundTripDoesNotAbsorbGeneratedMarkersIntoHumanFields(t *testing.T) {
	input := ProjectInput{
		ProjectView:  projectViewFixture(),
		GenerationID: "generation-400000000000003",
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
	accepted, err := reviewv2.LoadV3Bytes(first.Files[0].Desired, first.Files[1].Desired, first.Files[2].Desired)
	if err != nil {
		t.Fatal(err)
	}
	secondInput := input
	secondInput.Legacy.Review = accepted.State.Review
	secondInput.Legacy.Events = accepted.State.Events
	secondOutput, err := Project(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(secondInput, secondOutput)
	if err != nil {
		t.Fatal(err)
	}
	for index := range first.Files {
		if !bytes.Equal(first.Files[index].Desired, second.Files[index].Desired) {
			t.Fatalf("round trip changed %s", first.Files[index].Relative)
		}
	}
}
