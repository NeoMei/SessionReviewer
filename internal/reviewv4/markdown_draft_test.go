package reviewv4

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/neomei/SessionReviewer/internal/baselinehash"
)

func TestMarkdownConclusionEditPreservesVerification(t *testing.T) {
	p := minimumPresentation()
	p.Timeline = []Timeline{{ID: "m1", GenerationID: p.GenerationID,
		OccurredAt: "2026-09-05", Kind: "milestone", Title: "进展",
		DecisionIDs: []string{}, ClosedLoop: NeutralClosedLoop()}}
	before := p.Timeline[0].ClosedLoop.Verification
	next, err := ApplyMarkdownEdits(p, []FieldEdit{{
		Key: FieldKey{Entity: "milestone:m1", Name: "conclusion"}, After: "人工确认结论",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if next.Timeline[0].ClosedLoop.Conclusion.Kind != ConclusionHumanConfirmed {
		t.Fatal("missing human provenance")
	}
	if !reflect.DeepEqual(before, next.Timeline[0].ClosedLoop.Verification) {
		t.Fatal("verification changed")
	}
}

func TestMarkdownDraftTreatsOnlyLineEndingChangesAsUnchanged(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	pair, err := RenderMarkdown(ledger.DocumentProjection.PresentationBase, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	pair.Review = bytes.Replace(pair.Review, []byte("运行聚焦测试。\n\n```sh\ngo test ./internal/reviewv4\n```"), []byte("运行聚焦测试。\r\n\r\n```sh\r\ngo test ./internal/reviewv4\r\n```"), 1)
	draft, err := ParseMarkdownDraft(pair, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Edits) != 0 || draft.Presentation.Timeline[0].ClosedLoop.Conclusion.Kind != ConclusionHumanConfirmed || draft.Presentation.Revision != ledger.AcceptedRevision+1 {
		t.Fatalf("line endings promoted a source or edit: %+v", draft.Edits)
	}
}

func TestMarkdownDraftAdvancesRevisionForCustomOnlyChange(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	pair := MarkdownPair{Review: mustRead(t, "../../testdata/contracts/v4/markdown/review.md"), History: mustRead(t, "../../testdata/contracts/v4/markdown/history.md")}
	pair.Review = append(pair.Review, []byte("\n人工自定义附注。\n")...)
	draft, err := ParseMarkdownDraft(pair, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Edits) != 0 || draft.Presentation.Revision != ledger.AcceptedRevision+1 || len(draft.Presentation.HumanPatches) != 0 {
		t.Fatalf("custom-only draft revision/patch mismatch: %+v", draft)
	}
}

func TestMarkdownDraftRejectsShellOnlyChangeAtMaximumRevision(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	base := clonePresentation(ledger.DocumentProjection.PresentationBase)
	base.Revision = int(maxWireInteger)
	ledger.AcceptedRevision = base.Revision
	ledger.DocumentProjection.PresentationBase = base
	pair, err := RenderMarkdown(base, bindMarkdownPair(t, ledger, MarkdownPair{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	ledger = bindMarkdownPair(t, ledger, pair)
	pair.Review = append(pair.Review, []byte("\n人工自定义附注。\n")...)
	if _, err := ParseMarkdownDraft(pair, ledger); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("maximum-revision shell-only edit err=%v", err)
	}
}

func TestMarkdownDraftRejectsGeneratedRegionModification(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	pair, err := RenderMarkdown(ledger.DocumentProjection.PresentationBase, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	pair.Review = bytes.Replace(pair.Review, []byte("- [正式问题]"), []byte("- [篡改的问题]"), 1)
	if _, err := ParseMarkdownDraft(pair, ledger); MarkdownCodeOf(err) != MarkdownGeneratedRegionModified {
		t.Fatalf("generated edit err=%v", err)
	}
}

func TestApplyMarkdownEditsAdvancesRevisionsOnceAndRecordsPatch(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	base := ledger.DocumentProjection.PresentationBase
	next, err := ApplyMarkdownEdits(base, []FieldEdit{
		{Key: FieldKey{Entity: "project-overview", Name: "goal"}, Before: base.CurrentState.Goal, After: "人工目标"},
		{Key: FieldKey{Entity: "decision:decision:alpha", Name: "rationale"}, Before: base.Decisions[0].Rationale, After: "人工理由"},
		{Key: FieldKey{Entity: "problem:problem:alpha", Name: "current_conclusion"}, Before: base.ProblemNodes[0].CurrentConclusion, After: "人工答案"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Revision != base.Revision+1 || next.Decisions[0].Revision != base.Decisions[0].Revision+1 || next.ProblemNodes[0].Revision != base.ProblemNodes[0].Revision+1 {
		t.Fatalf("wrong revisions: presentation=%d decision=%d problem=%d", next.Revision, next.Decisions[0].Revision, next.ProblemNodes[0].Revision)
	}
	if len(next.HumanPatches) != 3 || len(next.GeneratedBaselines) != 3 {
		t.Fatalf("patch ledger incomplete: patches=%+v baselines=%+v", next.HumanPatches, next.GeneratedBaselines)
	}
	foundGoalHash := false
	for _, baseline := range next.GeneratedBaselines {
		if baseline.EntityID == "project-overview" && baseline.Field == "goal" {
			foundGoalHash = baseline.Kind == "scalar" && baseline.GeneratedHash == "f984fbcc29897988109ca37bc8f7bcb2c98e518b23537c24cd372b419dd079d7"
		}
	}
	if !foundGoalHash {
		t.Fatal("goal baseline did not use the pinned shared scalar hash contract")
	}
	if !reflect.DeepEqual(base.HumanPatches, ledger.DocumentProjection.PresentationBase.HumanPatches) || base.Revision != 1 {
		t.Fatal("input baseline was mutated")
	}
	unchanged, err := ApplyMarkdownEdits(next, []FieldEdit{{Key: FieldKey{Entity: "project-overview", Name: "goal"}, Before: "人工目标", After: "人工目标"}})
	if err != nil || unchanged.Revision != next.Revision || len(unchanged.HumanPatches) != len(next.HumanPatches) {
		t.Fatalf("no-op drifted revision or patches: err=%v", err)
	}
}

func TestApplyMarkdownEditsRejectsAmbiguousLegacyBareEntityIdentity(t *testing.T) {
	p := minimumPresentation()
	p.Decisions = []Decision{{
		ID: "shared", Kind: "decision", OccurredAt: "2026-09-05", Title: "human title", Status: DecisionActive,
		Supersedes: []string{}, MilestoneIDs: []string{}, SessionRefs: []SessionRef{}, Provenance: "human_created", Revision: 1,
	}}
	p.Timeline = []Timeline{{ID: "shared", GenerationID: p.GenerationID, OccurredAt: "2026-09-05", Kind: "milestone", Title: "milestone title", DecisionIDs: []string{}, ClosedLoop: NeutralClosedLoop()}}
	generated := "generated title"
	hash := baselinehash.SHA256("shared", "title", "scalar", generated, nil)
	p.GeneratedBaselines = []Baseline{{GenerationID: p.GenerationID, EntityID: "shared", Field: "title", Kind: "scalar", Value: &generated, GeneratedHash: hash}}
	human := "human title"
	p.HumanPatches = []Patch{{EntityID: "shared", Field: "title", Operation: "set", Value: &human, BaseGeneratedHash: hash}}

	if err := CarryMarkdownGeneratedBaselines(&p, "generation-next"); err == nil {
		t.Fatal("carry accepted an ambiguous bare entity identity")
	}
	_, err := ApplyMarkdownEdits(p, []FieldEdit{{Key: FieldKey{Entity: "decision:shared", Name: "title"}, Before: human, After: "edited"}})
	if MarkdownCodeOf(err) != MarkdownBaselineMissing {
		t.Fatalf("ambiguous bare entity identity was accepted: %v", err)
	}
}

func TestApplyMarkdownEditsRejectsBareAndQualifiedSemanticBaselineDuplicate(t *testing.T) {
	p := minimumPresentation()
	p.Decisions = []Decision{{
		ID: "decision-1", Kind: "decision", OccurredAt: "2026-09-05", Title: "generated title", Status: DecisionActive,
		Supersedes: []string{}, MilestoneIDs: []string{}, SessionRefs: []SessionRef{}, Provenance: "human_created", Revision: 1,
	}}
	value := p.Decisions[0].Title
	bareHash := baselinehash.SHA256("decision-1", "title", "scalar", value, nil)
	qualifiedHash := baselinehash.SHA256("decision:decision-1", "title", "scalar", value, nil)
	p.GeneratedBaselines = []Baseline{
		{GenerationID: p.GenerationID, EntityID: "decision-1", Field: "title", Kind: "scalar", Value: &value, GeneratedHash: bareHash},
		{GenerationID: p.GenerationID, EntityID: "decision:decision-1", Field: "title", Kind: "scalar", Value: &value, GeneratedHash: qualifiedHash},
	}

	if err := CarryMarkdownGeneratedBaselines(&p, "generation-next"); err == nil {
		t.Fatal("carry accepted a semantic bare/qualified duplicate")
	}
	_, err := ApplyMarkdownEdits(p, []FieldEdit{{Key: FieldKey{Entity: "decision:decision-1", Name: "title"}, Before: value, After: "edited"}})
	if MarkdownCodeOf(err) != MarkdownBaselineMissing {
		t.Fatalf("semantic bare/qualified duplicate was accepted: %v", err)
	}
}

func TestApplyMarkdownEditsRejectsStoredIdentityWithQualifiedAndLegacyInterpretations(t *testing.T) {
	p := minimumPresentation()
	p.Decisions = []Decision{{
		ID: "risk:shared", Kind: "decision", OccurredAt: "2026-09-05", Title: "decision title", Status: DecisionActive,
		Supersedes: []string{}, MilestoneIDs: []string{}, SessionRefs: []SessionRef{}, Provenance: "human_created", Revision: 1,
	}}
	p.Risks = []Risk{{ID: "shared", Title: "risk title", Detail: "risk detail", Status: "open"}}
	generated := "generated title"
	hash := baselinehash.SHA256("risk:shared", "title", "scalar", generated, nil)
	p.GeneratedBaselines = []Baseline{{GenerationID: p.GenerationID, EntityID: "risk:shared", Field: "title", Kind: "scalar", Value: &generated, GeneratedHash: hash}}

	if err := CarryMarkdownGeneratedBaselines(&p, "generation-next"); err == nil {
		t.Fatal("carry accepted a stored identity with qualified and legacy interpretations")
	}
	_, err := ApplyMarkdownEdits(p, []FieldEdit{{Key: FieldKey{Entity: "decision:risk:shared", Name: "title"}, Before: "decision title", After: "edited"}})
	if MarkdownCodeOf(err) != MarkdownBaselineMissing {
		t.Fatalf("stored identity ambiguity was accepted: %v", err)
	}
}

func TestApplyMarkdownEditsRejectsMixedStoredBaselineAndPatchIdentities(t *testing.T) {
	for _, test := range []struct {
		name, baselineEntity, patchEntity string
	}{
		{"bare baseline qualified patch", "decision-1", "decision:decision-1"},
		{"qualified baseline bare patch", "decision:decision-1", "decision-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := minimumPresentation()
			p.Decisions = []Decision{{
				ID: "decision-1", Kind: "decision", OccurredAt: "2026-09-05", Title: "human title", Status: DecisionActive,
				Supersedes: []string{}, MilestoneIDs: []string{}, SessionRefs: []SessionRef{}, Provenance: "human_created", Revision: 1,
			}}
			generated := "generated title"
			hash := baselinehash.SHA256(test.baselineEntity, "title", "scalar", generated, nil)
			p.GeneratedBaselines = []Baseline{{GenerationID: p.GenerationID, EntityID: test.baselineEntity, Field: "title", Kind: "scalar", Value: &generated, GeneratedHash: hash}}
			human := p.Decisions[0].Title
			p.HumanPatches = []Patch{{EntityID: test.patchEntity, Field: "title", Operation: "set", Value: &human, BaseGeneratedHash: hash}}

			if err := CarryMarkdownGeneratedBaselines(&p, "generation-next"); err == nil {
				t.Fatal("carry accepted mixed stored baseline/patch identities")
			}
			_, err := ApplyMarkdownEdits(p, []FieldEdit{{Key: FieldKey{Entity: "decision:decision-1", Name: "title"}, Before: human, After: "edited"}})
			if MarkdownCodeOf(err) != MarkdownBaselineMissing {
				t.Fatalf("Markdown edit accepted and healed mixed stored identities: %v", err)
			}
		})
	}
}

func TestApplyMarkdownEditsCoversEntireCatalogAndRejectsUntrustedChanges(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	base := ledger.DocumentProjection.PresentationBase
	edits := make([]FieldEdit, 0, 24)
	for _, block := range []MarkdownDocument{
		mustMarkdownDocument(t, markdownReviewRelative, mustRead(t, "../../testdata/contracts/v4/markdown/review.md")),
		mustMarkdownDocument(t, markdownHistoryRelative, mustRead(t, "../../testdata/contracts/v4/markdown/history.md")),
	} {
		for key, value := range block.Fields() {
			edits = append(edits, FieldEdit{Key: key, Before: value, After: value + "·人工"})
		}
	}
	next, err := ApplyMarkdownEdits(base, edits)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.HumanPatches) != 24 || len(next.GeneratedBaselines) != 24 || next.Revision != base.Revision+1 || next.Decisions[0].Revision != base.Decisions[0].Revision+1 || next.ProblemNodes[0].Revision != base.ProblemNodes[0].Revision+1 {
		t.Fatalf("catalog application incomplete: revision=%d patches=%d baselines=%d", next.Revision, len(next.HumanPatches), len(next.GeneratedBaselines))
	}
	for _, edit := range edits {
		if got, ok := markdownPresentationField(next, edit.Key); !ok || got != edit.After {
			t.Fatalf("field %+v=%q exists=%v want=%q", edit.Key, got, ok, edit.After)
		}
	}

	conclusion := FieldKey{Entity: "milestone:milestone:alpha", Name: "conclusion"}
	if _, err := ApplyMarkdownEdits(base, []FieldEdit{{Key: conclusion, Before: base.Timeline[0].ClosedLoop.Conclusion.Text, After: ""}}); MarkdownCodeOf(err) != MarkdownStructureEditRequiresCommand {
		t.Fatalf("cleared conclusion err=%v", err)
	}
	impact := FieldKey{Entity: "milestone:milestone:alpha", Name: "impact_and_follow_up"}
	if _, err := ApplyMarkdownEdits(base, []FieldEdit{{Key: impact, Before: base.Timeline[0].ClosedLoop.ImpactAndFollowUp.Text, After: ""}}); MarkdownCodeOf(err) != MarkdownStructureEditRequiresCommand {
		t.Fatalf("cleared follow-up err=%v", err)
	}
	if _, err := ApplyMarkdownEdits(base, []FieldEdit{{Key: conclusion, Before: base.Timeline[0].ClosedLoop.Conclusion.Text, After: "   "}}); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("whitespace conclusion err=%v", err)
	}
	goal := FieldKey{Entity: "project-overview", Name: "goal"}
	for name, want := range map[string]struct {
		edits []FieldEdit
		code  string
	}{
		"stale":     {[]FieldEdit{{Key: goal, Before: "stale", After: "new"}}, MarkdownFieldConflict},
		"duplicate": {[]FieldEdit{{Key: goal, Before: base.CurrentState.Goal, After: "one"}, {Key: goal, Before: base.CurrentState.Goal, After: "two"}}, MarkdownFieldDuplicate},
		"foreign":   {[]FieldEdit{{Key: FieldKey{Entity: "risk:foreign", Name: "title"}, Before: "", After: "new"}}, MarkdownStructureEditRequiresCommand},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ApplyMarkdownEdits(base, want.edits); MarkdownCodeOf(err) != want.code {
				t.Fatalf("err=%v want=%s", err, want.code)
			}
		})
	}
}

func mustMarkdownDocument(t *testing.T, relative string, raw []byte) MarkdownDocument {
	t.Helper()
	document, err := ParseMarkdownDocument(relative, raw)
	if err != nil {
		t.Fatal(err)
	}
	return document
}
