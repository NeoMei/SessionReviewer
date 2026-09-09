package reviewv4

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/baselinehash"
)

func TestMarkdownMilestoneDependencyCapacityCountsDistinctSnapshots(t *testing.T) {
	overlap := make([]ChainDependency, 32769)
	for index := range overlap {
		overlap[index] = ChainDependency{Provider: "codex", SessionID: fmt.Sprintf("session-%d", index), SessionViewDigest: fmt.Sprintf("sha256:%064x", index+1), DependencyDigest: "sha256:" + strings.Repeat("d", 64), TurnUnitIDs: []string{"turn-1"}}
	}
	merged, err := mergeMilestoneDependencies(overlap, overlap)
	if err != nil || len(merged) != len(overlap) {
		t.Fatalf("overlapping dependencies consumed capacity twice: len=%d err=%v", len(merged), err)
	}
	full := make([]ChainDependency, 65536)
	for index := range full {
		full[index] = ChainDependency{Provider: "codex", SessionID: fmt.Sprintf("full-%d", index), SessionViewDigest: fmt.Sprintf("sha256:%064x", index+1), DependencyDigest: "sha256:" + strings.Repeat("e", 64), TurnUnitIDs: []string{"turn-1"}}
	}
	extra := ChainDependency{Provider: "codex", SessionID: "overflow", SessionViewDigest: "sha256:" + strings.Repeat("f", 64), DependencyDigest: "sha256:" + strings.Repeat("e", 64), TurnUnitIDs: []string{"turn-1"}}
	if got, err := mergeMilestoneDependencies(full, []ChainDependency{extra}); err == nil || got != nil {
		t.Fatalf("true dependency union overflow returned len=%d err=%v", len(got), err)
	}
}

func TestMarkdownMilestoneUpdateBoundsReturnNoPartialOutput(t *testing.T) {
	ledger, pair := milestoneUpdateLedger(t, nil)
	overflow := ScanMilestoneUpdate{ProjectID: ledger.ProjectID, GenerationID: "generation-2", ProjectViewDigest: "sha256:" + strings.Repeat("2", 64), Timeline: make([]Timeline, 65537), ChainDependencies: []ChainDependency{}}
	if got, err := RebaseMarkdownMilestones(ledger, pair, overflow); err == nil || !reflect.DeepEqual(got, Presentation{}) {
		t.Fatalf("timeline overflow returned partial presentation: err=%v", err)
	}
	if got, err := RenderMarkdownMilestoneUpdate(ledger.DocumentProjection.PresentationBase, ledger, pair, overflow); err == nil || len(got.Review) != 0 || len(got.History) != 0 {
		t.Fatalf("timeline overflow returned partial Markdown: err=%v", err)
	}

	presentation := clonePresentation(ledger.DocumentProjection.PresentationBase)
	presentation.Revision = int(maxWireInteger)
	ledger.AcceptedRevision = presentation.Revision
	ledger.DocumentProjection.PresentationBase = presentation
	ledger = bindMarkdownPair(t, ledger, MarkdownPair{})
	maxPair, err := RenderMarkdown(presentation, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	ledger = bindMarkdownPair(t, ledger, maxPair)
	update := ScanMilestoneUpdate{ProjectID: ledger.ProjectID, GenerationID: "generation-2", ProjectViewDigest: "sha256:" + strings.Repeat("2", 64), Timeline: []Timeline{}, ChainDependencies: []ChainDependency{}}
	if got, err := RebaseMarkdownMilestones(ledger, maxPair, update); err == nil || !reflect.DeepEqual(got, Presentation{}) {
		t.Fatalf("revision overflow returned partial presentation: err=%v", err)
	}
}

func TestMarkdownMilestoneUpdateAppendsGeneratedMilestoneOverPendingHumanDraft(t *testing.T) {
	ledger, accepted := milestoneUpdateLedger(t, nil)
	pending := MarkdownPair{
		Review:  bytes.ReplaceAll(accepted.Review, []byte("\n"), []byte("\r\n")),
		History: bytes.ReplaceAll(accepted.History, []byte("\n"), []byte("\r\n")),
	}
	pending.Review = bytes.Replace(pending.Review, []byte("\r\n项目目标夹具\r\n"), []byte("\r\n人工保留目标\r\n"), 1)
	pending.Review = append(pending.Review, []byte("\r\n<!-- human-comment -->\r\n## 自定义\r\n保留原字节。\r\n")...)
	ledger = bindMarkdownPair(t, ledger, accepted)

	update := milestoneUpdate("generation-2", "2", generatedMilestone("machine:new", "generation-2", "2", "生成标题", "生成摘要"))
	next, err := RebaseMarkdownMilestones(ledger, pending, update)
	if err != nil {
		t.Fatalf("rebase: %#v", err)
	}
	if next.CurrentState.Goal != "人工保留目标" || next.Revision != ledger.AcceptedRevision+1 {
		t.Fatalf("pending edit was not rebased exactly once: revision=%d state=%+v", next.Revision, next.CurrentState)
	}
	if len(next.Timeline) != len(ledger.DocumentProjection.PresentationBase.Timeline)+1 || next.Timeline[len(next.Timeline)-1].ID != "machine:new" {
		t.Fatalf("generated milestone was not appended without losing history: %+v", next.Timeline)
	}
	assertMilestoneBaselines(t, next, "machine:new", map[string]string{
		"title": "生成标题", "summary": "生成摘要", "conclusion": "生成结论", "impact_and_follow_up": "生成影响",
	})
	rendered, err := RenderMarkdownMilestoneUpdate(next, ledger, pending, update)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rendered.Review, []byte("人工保留目标")) || !bytes.Contains(rendered.Review, []byte("<!-- human-comment -->\r\n## 自定义\r\n保留原字节。\r\n")) {
		t.Fatalf("human Markdown bytes were lost:\n%s", rendered.Review)
	}
	again, err := RenderMarkdownMilestoneUpdate(next, ledger, pending, update)
	if err != nil || !reflect.DeepEqual(rendered, again) {
		t.Fatalf("repeated milestone render drifted: err=%v", err)
	}

	tampered := clonePresentation(next)
	tampered.CurrentState.Goal = "scan override"
	if got, err := RenderMarkdownMilestoneUpdate(tampered, ledger, pending, update); err == nil || len(got.Review) != 0 || len(got.History) != 0 {
		t.Fatalf("arbitrary presentation bypass returned output: err=%v", err)
	}
}

func TestMarkdownMilestoneUpdateRejectsUnauthenticatedInputsAndHumanCollision(t *testing.T) {
	ledger, accepted := milestoneUpdateLedger(t, nil)
	ledger = bindMarkdownPair(t, ledger, accepted)
	valid := milestoneUpdate("generation-2", "2", generatedMilestone("machine:new", "generation-2", "2", "title", "summary"))

	tests := []struct {
		name    string
		ledger  MachineLedger
		pending MarkdownPair
		update  ScanMilestoneUpdate
	}{
		{name: "tampered generated block", ledger: ledger, pending: MarkdownPair{Review: accepted.Review, History: bytes.Replace(accepted.History, []byte("结论来源：human_confirmed"), []byte("结论来源：machine_forged"), 1)}, update: valid},
		{name: "old ledger self hash", ledger: func() MachineLedger { bad := ledger; bad.SyncHashes.LedgerSHA256 = strings.Repeat("f", 64); return bad }(), pending: accepted, update: valid},
		{name: "wrong project", ledger: ledger, pending: accepted, update: func() ScanMilestoneUpdate { bad := valid; bad.ProjectID = "other-project"; return bad }()},
		{name: "human milestone id collision", ledger: ledger, pending: accepted, update: milestoneUpdate("generation-2", "2", generatedMilestone(ledger.DocumentProjection.PresentationBase.Timeline[0].ID, "generation-2", "2", "collision", "summary"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, err := RebaseMarkdownMilestones(test.ledger, test.pending, test.update); err == nil || !reflect.DeepEqual(got, Presentation{}) {
				t.Fatalf("unauthenticated input returned a presentation: err=%v", err)
			}
		})
	}
}

func TestMarkdownMilestoneUpdateRebasesAcceptedAndPendingGeneratedFieldEdits(t *testing.T) {
	initial := milestoneUpdate("generation-2", "2", generatedMilestone("machine:owned", "generation-2", "2", "old title", "old summary"))
	ledger, pair := milestoneUpdateLedger(t, &initial)

	acceptedEdits := map[FieldKey]string{
		{Entity: "milestone:machine:owned", Name: "title"}:      "accepted title",
		{Entity: "milestone:machine:owned", Name: "conclusion"}: "accepted human conclusion",
	}
	ledger, pair = acceptMilestoneEdits(t, ledger, pair, acceptedEdits)
	acceptedRevision := ledger.AcceptedRevision
	pendingDoc, err := ParseMarkdownDocument(markdownHistoryRelative, pair.History)
	if err != nil {
		t.Fatal(err)
	}
	pendingHistory, err := pendingDoc.ReplaceFields(map[FieldKey]string{
		{Entity: "milestone:machine:owned", Name: "summary"}:              "pending summary",
		{Entity: "milestone:machine:owned", Name: "impact_and_follow_up"}: "pending impact",
	})
	if err != nil {
		t.Fatal(err)
	}
	pendingReview := bytes.Replace(pair.Review, []byte("\n项目目标夹具\n"), []byte("\npending project goal\n"), 1)
	pending := MarkdownPair{Review: pendingReview, History: pendingHistory}
	draft, err := ParseMarkdownDraft(pending, ledger)
	if err != nil {
		t.Fatal(err)
	}
	oldConclusion := draft.Presentation.Timeline[1].ClosedLoop.Conclusion

	changed := generatedMilestone("machine:owned", "generation-3", "3", "new default title", "new default summary")
	changed.ClosedLoop.Conclusion.Text = "new default conclusion"
	changed.ClosedLoop.ImpactAndFollowUp.Text = "new default impact"
	added := generatedMilestone("machine:later", "generation-3", "3", "later title", "later summary")
	update := milestoneUpdate("generation-3", "3", changed, added)
	next, err := RebaseMarkdownMilestones(ledger, pending, update)
	if err != nil {
		if markdown, ok := err.(*MarkdownError); ok {
			t.Fatalf("rebase edited milestone: code=%s cause=%v", markdown.Code, markdown.Cause)
		}
		t.Fatal(err)
	}
	got := next.Timeline[1]
	if got.Title != "accepted title" || got.Summary != "pending summary" || got.ClosedLoop.Conclusion.Text != "accepted human conclusion" || got.ClosedLoop.ImpactAndFollowUp.Text != "pending impact" {
		t.Fatalf("human milestone fields lost: %+v", got)
	}
	if next.CurrentState.Goal != "pending project goal" || next.Timeline[len(next.Timeline)-1].ID != "machine:later" {
		t.Fatalf("later append or pending project goal was lost: goal=%q timeline=%+v", next.CurrentState.Goal, next.Timeline)
	}
	if got.ClosedLoop.Conclusion.Kind != ConclusionHumanConfirmed || !reflect.DeepEqual(got.ClosedLoop.Conclusion.SourceTurnRefs, oldConclusion.SourceTurnRefs) {
		t.Fatalf("human conclusion kind or references changed: before=%+v after=%+v", oldConclusion, got.ClosedLoop.Conclusion)
	}
	assertMilestoneBaselines(t, next, "machine:owned", map[string]string{
		"title": "new default title", "summary": "new default summary", "conclusion": "new default conclusion", "impact_and_follow_up": "new default impact",
	})
	if next.Revision != acceptedRevision+1 {
		t.Fatalf("combined pending and scan changes advanced incorrectly: accepted=%d next=%d", acceptedRevision, next.Revision)
	}
	if err := CarryMarkdownGeneratedBaselines(&next, "generation-4"); err != nil {
		t.Fatalf("rebased metadata is not internally valid: %v", err)
	}
}

func TestMarkdownMilestoneUpdateRequiresAuthenticGeneratedOwnershipMetadata(t *testing.T) {
	initial := milestoneUpdate("generation-2", "2", generatedMilestone("machine:owned", "generation-2", "2", "old title", "old summary"))
	ledger, pair := milestoneUpdateLedger(t, &initial)
	update := milestoneUpdate("generation-3", "3", generatedMilestone("machine:owned", "generation-3", "3", "new title", "new summary"))

	tests := []struct {
		name string
		edit func(*Presentation)
	}{
		{name: "malformed baseline hash", edit: func(p *Presentation) {
			p.GeneratedBaselines[0].GeneratedHash = strings.Repeat("f", 64)
		}},
		{name: "unpatched field differs from baseline", edit: func(p *Presentation) {
			for index := range p.GeneratedBaselines {
				baseline := &p.GeneratedBaselines[index]
				if baseline.EntityID == "milestone:machine:owned" && baseline.Field == "title" {
					value := "forged default"
					baseline.Value = &value
					baseline.GeneratedHash = baselinehash.SHA256(baseline.EntityID, baseline.Field, baseline.Kind, value, nil)
				}
			}
		}},
		{name: "machine-like id without machine kind", edit: func(p *Presentation) {
			p.Timeline[1].Kind = "milestone"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			presentation := clonePresentation(ledger.DocumentProjection.PresentationBase)
			test.edit(&presentation)
			badLedger := acceptMilestonePresentation(t, ledger, pair, presentation)
			if got, err := RebaseMarkdownMilestones(badLedger, pair, update); err == nil || !reflect.DeepEqual(got, Presentation{}) {
				t.Fatalf("invalid ownership metadata returned a presentation: err=%v", err)
			}
		})
	}

	acceptedLedger, acceptedPair := acceptMilestoneEdits(t, ledger, pair, map[FieldKey]string{{Entity: "milestone:machine:owned", Name: "title"}: "human title"})
	malformedPatch := clonePresentation(acceptedLedger.DocumentProjection.PresentationBase)
	malformedPatch.HumanPatches[0].BaseGeneratedHash = strings.Repeat("e", 64)
	badLedger := acceptMilestonePresentation(t, acceptedLedger, acceptedPair, malformedPatch)
	if got, err := RebaseMarkdownMilestones(badLedger, acceptedPair, update); err == nil || !reflect.DeepEqual(got, Presentation{}) {
		t.Fatalf("malformed patch binding returned a presentation: err=%v", err)
	}
}

func TestMarkdownMilestoneUpdateRejectsDependencyConflictAndKeepsAcceptedNoOp(t *testing.T) {
	initial := milestoneUpdate("generation-2", "2", generatedMilestone("machine:owned", "generation-2", "2", "title", "summary"))
	ledger, pair := milestoneUpdateLedger(t, &initial)
	base := ledger.DocumentProjection.PresentationBase
	unchanged := ScanMilestoneUpdate{ProjectID: base.ProjectID, GenerationID: base.GenerationID, ProjectViewDigest: base.ProjectViewDigest, Timeline: []Timeline{cloneTimeline(base.Timeline[1])}, ChainDependencies: cloneChainDependencies(base.ChainDependencies)}
	next, err := RebaseMarkdownMilestones(ledger, pair, unchanged)
	if err != nil {
		t.Fatal(err)
	}
	equal, err := markdownPresentationsEqual(next, base)
	if err != nil || !equal || next.Revision != base.Revision {
		t.Fatalf("unchanged accepted milestone drifted: equal=%v revision=%d/%d err=%v", equal, next.Revision, base.Revision, err)
	}
	rendered, err := RenderMarkdownMilestoneUpdate(next, ledger, pair, unchanged)
	if err != nil || !reflect.DeepEqual(rendered, pair) {
		t.Fatalf("unchanged accepted milestone bytes drifted: err=%v", err)
	}

	conflict := unchanged
	conflict.ChainDependencies = cloneChainDependencies(conflict.ChainDependencies)
	conflict.ChainDependencies[0].DependencyDigest = "sha256:" + strings.Repeat("f", 64)
	if got, err := RebaseMarkdownMilestones(ledger, pair, conflict); err == nil || !reflect.DeepEqual(got, Presentation{}) {
		t.Fatalf("same snapshot dependency conflict returned a presentation: err=%v", err)
	}

	invalidNext := clonePresentation(next)
	invalidNext.MinimumReaderVersion = "invalid"
	if got, err := RenderMarkdownMilestoneUpdate(invalidNext, ledger, pair, unchanged); err == nil || len(got.Review) != 0 || len(got.History) != 0 {
		t.Fatalf("invalid caller presentation returned Markdown: err=%v", err)
	}
}

func TestMarkdownMilestoneUpdatePreservesConfirmedConclusionAndAdvancesRestoredDefault(t *testing.T) {
	initial := milestoneUpdate("generation-2", "2", generatedMilestone("machine:owned", "generation-2", "2", "old title", "old summary"))
	ledger, pair := milestoneUpdateLedger(t, &initial)
	ledger, pair = acceptMilestoneEdits(t, ledger, pair, map[FieldKey]string{
		{Entity: "milestone:machine:owned", Name: "title"}:      "temporary human title",
		{Entity: "milestone:machine:owned", Name: "conclusion"}: "confirmed candidate conclusion",
	})
	presentation := clonePresentation(ledger.DocumentProjection.PresentationBase)
	presentation.Timeline[1].Title = "old title"
	presentation.Timeline[1].ClosedLoop.Conclusion.Kind = ConclusionAICandidateConfirmed
	for index := range presentation.HumanPatches {
		patch := &presentation.HumanPatches[index]
		if patch.EntityID == "milestone:machine:owned" && patch.Field == "title" {
			patch.Operation, patch.Value = "restore_default", nil
		}
	}
	ledger = acceptMilestonePresentation(t, ledger, pair, presentation)
	pair = mustRenderedAcceptedPair(t, ledger)
	ledger = bindMarkdownPair(t, ledger, pair)

	changed := generatedMilestone("machine:owned", "generation-3", "3", "new title", "new summary")
	next, err := RebaseMarkdownMilestones(ledger, pair, milestoneUpdate("generation-3", "3", changed))
	if err != nil {
		t.Fatal(err)
	}
	got := next.Timeline[1]
	if got.Title != "new title" {
		t.Fatalf("restore_default froze the old default: %q", got.Title)
	}
	if got.ClosedLoop.Conclusion.Kind != ConclusionAICandidateConfirmed || got.ClosedLoop.Conclusion.Text != "confirmed candidate conclusion" {
		t.Fatalf("explicit AI-confirmed conclusion was demoted: %+v", got.ClosedLoop.Conclusion)
	}
	if err := CarryMarkdownGeneratedBaselines(&next, "generation-4"); err != nil {
		t.Fatalf("restored/confirmed metadata cannot survive another generation: %v", err)
	}
}

func TestMarkdownMilestoneUpdateRestoredConclusionFollowsDefaultAcrossRescans(t *testing.T) {
	initial := milestoneUpdate("generation-2", "2", generatedMilestone("machine:owned", "generation-2", "2", "title", "old conclusion"))
	ledger, pair := milestoneUpdateLedger(t, &initial)
	ledger, pair = acceptMilestoneEdits(t, ledger, pair, map[FieldKey]string{{Entity: "milestone:machine:owned", Name: "conclusion"}: "human conclusion"})
	presentation := clonePresentation(ledger.DocumentProjection.PresentationBase)
	presentation.Timeline[1].ClosedLoop.Conclusion = cloneConclusion(initial.Timeline[0].ClosedLoop.Conclusion)
	for index := range presentation.HumanPatches {
		patch := &presentation.HumanPatches[index]
		if patch.EntityID == "milestone:machine:owned" && patch.Field == "conclusion" {
			patch.Operation, patch.Value = "restore_default", nil
		}
	}
	ledger = acceptMilestonePresentation(t, ledger, pair, presentation)
	pair = mustRenderedAcceptedPair(t, ledger)
	ledger = bindMarkdownPair(t, ledger, pair)

	firstGenerated := generatedMilestone("machine:owned", "generation-3", "3", "title", "summary")
	firstGenerated.ClosedLoop.Conclusion.Text = "new visible conclusion"
	firstUpdate := milestoneUpdate("generation-3", "3", firstGenerated)
	first, err := RebaseMarkdownMilestones(ledger, pair, firstUpdate)
	if err != nil || first.Timeline[1].ClosedLoop.Conclusion.Kind != ConclusionVisibleAnswerExcerpt || first.Timeline[1].ClosedLoop.Conclusion.Text != "new visible conclusion" {
		t.Fatalf("restored conclusion did not follow visible default: err=%v conclusion=%+v", err, first.Timeline[1].ClosedLoop.Conclusion)
	}
	firstPair, err := RenderMarkdownMilestoneUpdate(first, ledger, pair, firstUpdate)
	if err != nil {
		t.Fatal(err)
	}
	ledger = acceptMilestonePresentation(t, ledger, firstPair, first)

	secondGenerated := generatedMilestone("machine:owned", "generation-4", "4", "title", "summary")
	secondGenerated.ClosedLoop.Conclusion.Text = "later visible conclusion"
	secondUpdate := milestoneUpdate("generation-4", "4", secondGenerated)
	second, err := RebaseMarkdownMilestones(ledger, firstPair, secondUpdate)
	if err != nil {
		if markdown, ok := err.(*MarkdownError); ok {
			t.Fatalf("restored conclusion could not rescan twice: code=%s cause=%v", markdown.Code, markdown.Cause)
		}
		t.Fatal(err)
	}
	if second.Timeline[1].ClosedLoop.Conclusion.Text != "later visible conclusion" {
		t.Fatalf("restored conclusion could not rescan twice: conclusion=%+v", second.Timeline[1].ClosedLoop.Conclusion)
	}
}

func TestMarkdownMilestoneUpdateQualifiesRetainedLegacyReferencesBeforeNewSnapshot(t *testing.T) {
	oldView := "sha256:" + strings.Repeat("1", 64)
	newView := "sha256:" + strings.Repeat("2", 64)
	old := generatedMilestone("machine:old", "generation-1", "1", "old", "old")
	old.ClosedLoop = generatedClosedLoop("", "old conclusion", "old impact")
	old.ClosedLoop.SourceTurnRefs[0].SessionViewDigest = ""
	old.ClosedLoop.TriggerQuestion.SourceTurnRefs[0].SessionViewDigest = ""
	old.ClosedLoop.Conclusion.SourceTurnRefs[0].SessionViewDigest = ""
	old.ClosedLoop.Verification.SourceTurnRefs[0].SessionViewDigest = ""
	oldDep := generatedDependency("1")
	oldDep.SessionViewDigest = oldView
	ledger, pair := milestoneUpdateLedger(t, nil)
	base := clonePresentation(ledger.DocumentProjection.PresentationBase)
	base.Timeline = append(base.Timeline, old)
	base.ChainDependencies = []ChainDependency{oldDep}
	addMilestoneBaselines(&base, old)
	base.MinimumReaderVersion, base.MinimumWriterVersion = "0.4.0", "0.4.0"
	ledger = acceptMilestonePresentation(t, ledger, pair, base)
	pair = mustRenderedAcceptedPair(t, ledger)
	ledger = bindMarkdownPair(t, ledger, pair)

	fresh := generatedMilestone("machine:new", "generation-2", "2", "new", "new")
	update := milestoneUpdate("generation-2", "2", fresh)
	next, err := RebaseMarkdownMilestones(ledger, pair, update)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.ChainDependencies) != 2 || next.MinimumReaderVersion != "0.4.3" || next.MinimumWriterVersion != "0.4.3" {
		t.Fatalf("historical dependency/capability not retained: %+v", next)
	}
	for _, ref := range next.Timeline[1].ClosedLoop.SourceTurnRefs {
		if ref.SessionViewDigest != oldView {
			t.Fatalf("legacy reference was not qualified from old authenticated dependency: %+v", ref)
		}
	}
	if next.ChainDependencies[1].SessionViewDigest != newView {
		t.Fatalf("new dependency snapshot missing: %+v", next.ChainDependencies)
	}
}

func TestMarkdownMilestoneUpdateDeduplicatesQualifiedAndLegacyReferenceToSameSnapshot(t *testing.T) {
	oldView := "sha256:" + strings.Repeat("1", 64)
	old := generatedMilestone("machine:old", "generation-1", "1", "old", "old")
	old.ClosedLoop = generatedClosedLoop("", "old conclusion", "old impact")
	dependency := generatedDependency("1")
	ledger, pair := milestoneUpdateLedger(t, nil)
	presentation := clonePresentation(ledger.DocumentProjection.PresentationBase)
	presentation.Timeline = append(presentation.Timeline, old)
	presentation.ChainDependencies = []ChainDependency{dependency}
	addMilestoneBaselines(&presentation, old)
	ledger = acceptMilestonePresentation(t, ledger, pair, presentation)
	pair = mustRenderedAcceptedPair(t, ledger)
	ledger = bindMarkdownPair(t, ledger, pair)
	ledger, pair = acceptMilestoneEdits(t, ledger, pair, map[FieldKey]string{{Entity: "milestone:machine:old", Name: "conclusion"}: "human conclusion"})

	generated := generatedMilestone("machine:old", "generation-2", "1", "new", "new")
	update := ScanMilestoneUpdate{ProjectID: ledger.ProjectID, GenerationID: "generation-2", ProjectViewDigest: "sha256:" + strings.Repeat("2", 64), Timeline: []Timeline{generated}, ChainDependencies: []ChainDependency{dependency}}
	next, err := RebaseMarkdownMilestones(ledger, pair, update)
	if err != nil {
		t.Fatal(err)
	}
	loop := next.Timeline[1].ClosedLoop
	if loop.Conclusion.Text != "human conclusion" || loop.Conclusion.SourceTurnRefs[0].SessionViewDigest != "" || len(loop.SourceTurnRefs) != 1 {
		t.Fatalf("equivalent legacy/qualified reference was duplicated or rewritten: %+v oldView=%s", loop, oldView)
	}
}

func TestMarkdownMilestoneUpdateIndexesLongAcceptedHistoryOncePerRebase(t *testing.T) {
	const milestoneCount = 24
	const turnCount = 512

	dependency := generatedDependency("1")
	dependency.TurnUnitIDs = make([]string, turnCount)
	for index := range dependency.TurnUnitIDs {
		dependency.TurnUnitIDs[index] = fmt.Sprintf("turn-%d", index+1)
	}
	initialTimeline := make([]Timeline, milestoneCount)
	for index := range initialTimeline {
		initialTimeline[index] = generatedMilestone(fmt.Sprintf("machine:scale-%d", index), "generation-2", "1", "old title", "old summary")
	}
	initial := ScanMilestoneUpdate{
		ProjectID: "project-p", GenerationID: "generation-2", ProjectViewDigest: "sha256:" + strings.Repeat("1", 64),
		Timeline: initialTimeline, ChainDependencies: []ChainDependency{dependency},
	}
	ledger, pair := milestoneUpdateLedger(t, &initial)
	edits := make(map[FieldKey]string, milestoneCount)
	for index := range initialTimeline {
		edits[FieldKey{Entity: "milestone:" + initialTimeline[index].ID, Name: "conclusion"}] = fmt.Sprintf("human conclusion %d", index)
	}
	ledger, pair = acceptMilestoneEdits(t, ledger, pair, edits)

	changedTimeline := make([]Timeline, milestoneCount)
	for index := range changedTimeline {
		changedTimeline[index] = generatedMilestone(initialTimeline[index].ID, "generation-3", "1", "new title", "new summary")
	}
	update := ScanMilestoneUpdate{
		ProjectID: "project-p", GenerationID: "generation-3", ProjectViewDigest: "sha256:" + strings.Repeat("2", 64),
		Timeline: changedTimeline, ChainDependencies: []ChainDependency{dependency},
	}
	var rebaseErr error
	allocations := testing.AllocsPerRun(1, func() {
		_, rebaseErr = RebaseMarkdownMilestones(ledger, pair, update)
	})
	if rebaseErr != nil {
		t.Fatal(rebaseErr)
	}
	if allocations > 30000 {
		t.Fatalf("long accepted history rebuilt dependency indexes per reference: allocations=%.0f", allocations)
	}
}

func milestoneUpdateLedger(t *testing.T, initial *ScanMilestoneUpdate) (MachineLedger, MarkdownPair) {
	t.Helper()
	ledger := sharedMarkdownLedger(t)
	base := ledger.DocumentProjection.PresentationBase
	pair, err := RenderMarkdown(base, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	ledger = bindMarkdownPair(t, ledger, pair)
	if initial == nil {
		return ledger, pair
	}
	next, err := RebaseMarkdownMilestones(ledger, pair, *initial)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderMarkdownMilestoneUpdate(next, ledger, pair, *initial)
	if err != nil {
		t.Fatal(err)
	}
	ledger = acceptMilestonePresentation(t, ledger, rendered, next)
	return ledger, rendered
}

func acceptMilestoneEdits(t *testing.T, ledger MachineLedger, pair MarkdownPair, values map[FieldKey]string) (MachineLedger, MarkdownPair) {
	t.Helper()
	doc, err := ParseMarkdownDocument(markdownHistoryRelative, pair.History)
	if err != nil {
		t.Fatal(err)
	}
	history, err := doc.ReplaceFields(values)
	if err != nil {
		t.Fatal(err)
	}
	pending := MarkdownPair{Review: pair.Review, History: history}
	draft, err := ParseMarkdownDraft(pending, ledger)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderMarkdownDraft(draft.Presentation, ledger, pending)
	if err != nil {
		t.Fatal(err)
	}
	return acceptMilestonePresentation(t, ledger, rendered, draft.Presentation), rendered
}

func acceptMilestonePresentation(t *testing.T, ledger MachineLedger, pair MarkdownPair, next Presentation) MachineLedger {
	t.Helper()
	ledger.ProjectID, ledger.GenerationID, ledger.ProjectViewDigest = next.ProjectID, next.GenerationID, next.ProjectViewDigest
	capability := documentProjectionCapability(next)
	ledger.MinimumReaderVersion, ledger.MinimumWriterVersion = capability, capability
	ledger.AcceptedRevision = next.Revision
	ledger.HumanPatches = cloneMarkdownPatches(next.HumanPatches)
	ledger.OrphanPatches = cloneMarkdownPatches(next.OrphanPatches)
	ledger.GeneratedBaselines = cloneMarkdownBaselines(next.GeneratedBaselines)
	ledger.DocumentProjection = &DocumentProjection{SchemaVersion: 1, Format: "review-markdown-v1", PresentationBase: clonePresentation(next)}
	return bindMarkdownPair(t, ledger, pair)
}

func mustRenderedAcceptedPair(t *testing.T, ledger MachineLedger) MarkdownPair {
	t.Helper()
	pair, err := RenderMarkdown(ledger.DocumentProjection.PresentationBase, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func milestoneUpdate(generation, digestChar string, timeline ...Timeline) ScanMilestoneUpdate {
	dependencies := []ChainDependency{}
	if len(timeline) > 0 {
		dependencies = []ChainDependency{generatedDependency(digestChar)}
	}
	return ScanMilestoneUpdate{ProjectID: "project-p", GenerationID: generation, ProjectViewDigest: "sha256:" + strings.Repeat(digestChar, 64), Timeline: timeline, ChainDependencies: dependencies}
}

func generatedMilestone(id, generation, digestChar, title, summary string) Timeline {
	return Timeline{ID: id, GenerationID: generation, OccurredAt: "2026-09-09T00:00:00Z", Kind: "machine_verification", Title: title, Summary: summary, DecisionIDs: []string{}, ClosedLoop: generatedClosedLoop("sha256:"+strings.Repeat(digestChar, 64), "生成结论", "生成影响")}
}

func generatedClosedLoop(view, conclusion, impact string) ClosedLoop {
	ref := SourceTurnRef{Provider: "codex", SessionID: "session-1", TurnUnitID: "turn-1", SessionViewDigest: view}
	return ClosedLoop{
		TriggerQuestion:   ClosedLoopSegment{State: "present", Text: "question", SourceTurnRefs: []SourceTurnRef{ref}},
		Conclusion:        ClosedLoopConclusion{Kind: ConclusionVisibleAnswerExcerpt, Text: conclusion, SourceTurnRefs: []SourceTurnRef{ref}},
		Execution:         ClosedLoopSegment{State: "missing", MissingReason: milestoneStringPointer("no_execution_evidence"), SourceTurnRefs: []SourceTurnRef{}},
		Verification:      ClosedLoopSegment{State: "present", Text: "verified", SourceTurnRefs: []SourceTurnRef{ref}},
		ImpactAndFollowUp: ClosedLoopSegment{State: "present", Text: impact, SourceTurnRefs: []SourceTurnRef{}},
		SourceTurnRefs:    []SourceTurnRef{ref}, Coverage: ClosedLoopCoverage{SourceTurns: 1, CapturedTurns: 1},
	}
}

func generatedDependency(digestChar string) ChainDependency {
	return ChainDependency{Provider: "codex", SessionID: "session-1", SessionViewDigest: "sha256:" + strings.Repeat(digestChar, 64), DependencyDigest: "sha256:" + strings.Repeat("d", 64), TurnUnitIDs: []string{"turn-1"}}
}

func milestoneStringPointer(value string) *string { return &value }

func addMilestoneBaselines(p *Presentation, item Timeline) {
	for field, value := range map[string]string{
		"title": item.Title, "summary": item.Summary, "conclusion": item.ClosedLoop.Conclusion.Text, "impact_and_follow_up": item.ClosedLoop.ImpactAndFollowUp.Text,
	} {
		entity := "milestone:" + item.ID
		value := value
		p.GeneratedBaselines = append(p.GeneratedBaselines, Baseline{GenerationID: p.GenerationID, EntityID: entity, Field: field, Kind: "scalar", Value: &value, GeneratedHash: baselinehash.SHA256(entity, field, "scalar", value, nil)})
	}
}

func assertMilestoneBaselines(t *testing.T, p Presentation, id string, want map[string]string) {
	t.Helper()
	got := map[string]string{}
	for _, baseline := range p.GeneratedBaselines {
		if baseline.EntityID == "milestone:"+id && baseline.Value != nil {
			got[baseline.Field] = *baseline.Value
			if baseline.GenerationID != p.GenerationID || baseline.GeneratedHash != baselinehash.SHA256(baseline.EntityID, baseline.Field, "scalar", *baseline.Value, nil) {
				t.Fatalf("invalid generated baseline binding: %+v", baseline)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("milestone baselines=%v want=%v", got, want)
	}
}
