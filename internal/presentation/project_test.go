package presentation

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

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
	if !strings.Contains(output.RecentProgress, "event-ref-a") {
		t.Fatalf("selected deterministic progress missing: %q", output.RecentProgress)
	}
	if !strings.Contains(output.Usage, "关联使用") || !strings.Contains(output.Usage, "共享使用") {
		t.Fatalf("associated/shared usage labels missing: %q", output.Usage)
	}
}

func TestRenderV3ProducesVerifiedThreeFileCASPlan(t *testing.T) {
	input := ProjectInput{
		ProjectView:  projectViewFixture(),
		GenerationID: "generation-400000000000002",
		Revision:     8,
		Legacy:       legacyPresentationFixture(),
		ExpectedFiles: map[string][]byte{
			reviewv2.ReviewRelativePath: []byte("current review preimage"),
		},
		UnknownBlocks: map[string][]byte{"custom": []byte("<!-- 自定义保留块 -->")},
	}
	output, err := Project(input)
	if err != nil {
		t.Fatal(err)
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
