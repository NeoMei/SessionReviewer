package contextupdate

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/baselinehash"
	"github.com/neomei/SessionReviewer/internal/migrationv4"
	"github.com/neomei/SessionReviewer/internal/pricing"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func TestV4ScanMapsCanonicalIndexAndPreservesHumanStructures(t *testing.T) {
	accepted := v4AcceptedFixture(t)
	for len(accepted.Review.Timeline) < 17 {
		item := accepted.Review.Timeline[0]
		item.ID = fmt.Sprintf("milestone-extra-%d", len(accepted.Review.Timeline))
		accepted.Review.Timeline = append(accepted.Review.Timeline, item)
	}
	original := accepted.Review
	originalPricing := accepted.Ledger.PricingSnapshots
	index := accepted.SessionIndex
	seed := index.Sessions[0]
	index.Sessions = make([]sessionindex.Entry, 154)
	for i := range index.Sessions {
		entry := seed
		entry.SessionID = fmt.Sprintf("session-%03d", i)
		index.Sessions[i] = entry
	}
	index.Coverage.Total, index.Coverage.Complete = 154, 154
	index.Coverage.SourceAvailable, index.Coverage.StartedAtKnown, index.Coverage.EndedAtKnown = 154, 154, 154
	index.GenerationID = "generation-2"
	index.ProjectViewDigest = "sha256:" + strings.Repeat("2", 64)
	indexBody, err := sessionindex.Render(index)
	if err != nil {
		t.Fatal(err)
	}
	index, err = sessionindex.Parse(indexBody)
	if err != nil {
		t.Fatal(err)
	}
	nextPresentation, nextLedger, err := mapV4Scan(v4MapInput{Accepted: accepted, Index: index, Accounting: accounting.ProjectSummary{TotalDurationMS: 12, TotalTokens: 34}})
	if err != nil {
		t.Fatal(err)
	}
	if len(nextPresentation.Timeline) != 17 || !reflect.DeepEqual(nextPresentation.Decisions, original.Decisions) || !reflect.DeepEqual(nextPresentation.Risks, original.Risks) || !reflect.DeepEqual(nextPresentation.OpenLoops, original.OpenLoops) || !reflect.DeepEqual(nextPresentation.ProblemRootIDs, original.ProblemRootIDs) || !reflect.DeepEqual(nextPresentation.ProblemNodes, original.ProblemNodes) || !reflect.DeepEqual(nextPresentation.ChainDependencies, original.ChainDependencies) || !reflect.DeepEqual(nextPresentation.HumanPatches, original.HumanPatches) || !reflect.DeepEqual(nextPresentation.OrphanPatches, original.OrphanPatches) || !reflect.DeepEqual(nextPresentation.GeneratedBaselines, original.GeneratedBaselines) || !reflect.DeepEqual(nextLedger.PricingSnapshots, originalPricing) {
		t.Fatal("scan mapping dropped accepted human structure or timeline")
	}
	for i := range original.Timeline {
		got := nextPresentation.Timeline[i]
		got.GenerationID = original.Timeline[i].GenerationID
		if !reflect.DeepEqual(got, original.Timeline[i]) {
			t.Fatalf("timeline[%d] conclusion, verification, refs, or coverage changed: got=%+v want=%+v", i, got, original.Timeline[i])
		}
	}
	if nextPresentation.GenerationID != index.GenerationID || nextPresentation.ProjectViewDigest != index.ProjectViewDigest || nextPresentation.Revision != original.Revision+1 || nextLedger.SyncHashes.SessionIndexDigest != index.Digest || len(nextLedger.Sessions) != 154 || len(nextLedger.PricingSnapshots) != len(accepted.Ledger.PricingSnapshots) {
		t.Fatalf("mapped identities/facts=%+v ledger=%+v", nextPresentation, nextLedger)
	}
}

func TestV4ScanCarriesAuthenticatedHumanBaselineToNewGeneration(t *testing.T) {
	accepted := v4AcceptedFixture(t)
	edited, err := reviewv4.ApplyMarkdownEdits(accepted.Review, []reviewv4.FieldEdit{{
		Key:    reviewv4.FieldKey{Entity: "project-overview", Name: "goal"},
		Before: accepted.Review.CurrentState.Goal, After: "first human override",
	}})
	if err != nil {
		t.Fatal(err)
	}
	accepted.Review = edited
	accepted.Ledger.GenerationID = edited.GenerationID
	accepted.Ledger.AcceptedRevision = edited.Revision
	accepted.Ledger.HumanPatches = append([]reviewv4.Patch(nil), edited.HumanPatches...)
	accepted.Ledger.GeneratedBaselines = append([]reviewv4.Baseline(nil), edited.GeneratedBaselines...)
	accepted.Ledger.DocumentProjection.PresentationBase = edited
	accepted.Ledger.SyncHashes.LedgerSHA256 = reviewv4.CanonicalLedgerSHA256(accepted.Ledger)

	beforeBaseline := edited.GeneratedBaselines[0]
	beforePatch := edited.HumanPatches[0]
	index := accepted.SessionIndex
	index.GenerationID = "generation-after-scan"
	index.ProjectViewDigest = "sha256:" + strings.Repeat("8", 64)
	indexBody, err := sessionindex.Render(index)
	if err != nil {
		t.Fatal(err)
	}
	index, err = sessionindex.Parse(indexBody)
	if err != nil {
		t.Fatal(err)
	}
	next, ledger, err := mapV4Scan(v4MapInput{Accepted: accepted, Index: index})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.GeneratedBaselines) != 1 || next.GeneratedBaselines[0].GenerationID != index.GenerationID {
		t.Fatalf("baseline not carried to new generation: %+v", next.GeneratedBaselines)
	}
	carried := next.GeneratedBaselines[0]
	carried.GenerationID = beforeBaseline.GenerationID
	if !reflect.DeepEqual(carried, beforeBaseline) || !reflect.DeepEqual(next.HumanPatches[0], beforePatch) {
		t.Fatalf("carry-forward changed generated value/hash or override: baseline=%+v patch=%+v", next.GeneratedBaselines[0], next.HumanPatches[0])
	}
	if !reflect.DeepEqual(ledger.GeneratedBaselines, next.GeneratedBaselines) || !reflect.DeepEqual(ledger.DocumentProjection.PresentationBase.GeneratedBaselines, next.GeneratedBaselines) {
		t.Fatal("ledger and presentation base did not receive carried baseline")
	}
	if _, err := reviewv4.ApplyMarkdownEdits(next, []reviewv4.FieldEdit{{
		Key:    reviewv4.FieldKey{Entity: "project-overview", Name: "goal"},
		Before: "first human override", After: "second human override",
	}}); err != nil {
		t.Fatalf("same field cannot be edited after scan: %v", err)
	}
}

// Removing the active patch on restore must not strand the authenticated live
// baseline on the old generation. Otherwise the next ordinary edit is rejected
// even though the field and its original generated value still exist.
func TestV4ScanCarriesRestoredLiveBaselineSoFieldCanBeEditedAgain(t *testing.T) {
	accepted := v4AcceptedFixture(t)
	generated := accepted.Review.CurrentState.Goal
	edited, err := reviewv4.ApplyMarkdownEdits(accepted.Review, []reviewv4.FieldEdit{{
		Key: reviewv4.FieldKey{Entity: "project-overview", Name: "goal"}, Before: generated, After: "first human override",
	}})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := reviewv4.ApplyMarkdownEdits(edited, []reviewv4.FieldEdit{{
		Key: reviewv4.FieldKey{Entity: "project-overview", Name: "goal"}, Before: "first human override", After: generated,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.HumanPatches) != 0 || len(restored.GeneratedBaselines) != 1 {
		t.Fatalf("restore did not retain only the generated baseline: patches=%+v baselines=%+v", restored.HumanPatches, restored.GeneratedBaselines)
	}
	beforeBaseline := restored.GeneratedBaselines[0]
	accepted.Review = restored
	accepted.Ledger.AcceptedRevision = restored.Revision
	accepted.Ledger.HumanPatches = append([]reviewv4.Patch{}, restored.HumanPatches...)
	accepted.Ledger.GeneratedBaselines = append([]reviewv4.Baseline(nil), restored.GeneratedBaselines...)
	accepted.Ledger.DocumentProjection.PresentationBase = restored
	accepted.Ledger.SyncHashes.LedgerSHA256 = reviewv4.CanonicalLedgerSHA256(accepted.Ledger)

	index := accepted.SessionIndex
	index.GenerationID = "generation-after-restore"
	index.ProjectViewDigest = "sha256:" + strings.Repeat("7", 64)
	indexBody, err := sessionindex.Render(index)
	if err != nil {
		t.Fatal(err)
	}
	index, err = sessionindex.Parse(indexBody)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := mapV4Scan(v4MapInput{Accepted: accepted, Index: index})
	if err != nil {
		t.Fatal(err)
	}
	carried := next.GeneratedBaselines[0]
	if carried.GenerationID != index.GenerationID || carried.Value == nil || *carried.Value != *beforeBaseline.Value || carried.GeneratedHash != beforeBaseline.GeneratedHash {
		t.Fatalf("restored live baseline was not carried without changing its value/hash: got=%+v want=%+v", carried, beforeBaseline)
	}
	again, err := reviewv4.ApplyMarkdownEdits(next, []reviewv4.FieldEdit{{
		Key: reviewv4.FieldKey{Entity: "project-overview", Name: "goal"}, Before: generated, After: "second human override",
	}})
	if err != nil {
		t.Fatalf("restored field cannot be edited after a new generation: %v", err)
	}
	if len(again.HumanPatches) != 1 || again.HumanPatches[0].Value == nil || *again.HumanPatches[0].Value != "second human override" || again.HumanPatches[0].BaseGeneratedHash != beforeBaseline.GeneratedHash {
		t.Fatalf("second edit did not retain the authenticated original baseline: %+v", again)
	}
}

func TestV4ScanRejectsMalformedBaselineInsteadOfCarryingIt(t *testing.T) {
	accepted := v4AcceptedFixture(t)
	value := accepted.Review.CurrentState.Goal
	accepted.Review.GeneratedBaselines = []reviewv4.Baseline{{
		GenerationID: accepted.Review.GenerationID, EntityID: "project-overview", Field: "goal",
		Kind: "scalar", Value: &value, GeneratedHash: strings.Repeat("f", 64),
	}}
	accepted.Ledger.GeneratedBaselines = append([]reviewv4.Baseline(nil), accepted.Review.GeneratedBaselines...)
	accepted.Ledger.DocumentProjection.PresentationBase = accepted.Review
	accepted.Ledger.SyncHashes.LedgerSHA256 = reviewv4.CanonicalLedgerSHA256(accepted.Ledger)

	if _, _, err := mapV4Scan(v4MapInput{Accepted: accepted, Index: accepted.SessionIndex}); err == nil {
		t.Fatal("malformed generated baseline was accepted")
	}
}

func TestV4ScanKeepsValidHistoricalOrphanBaselineGeneration(t *testing.T) {
	accepted := v4AcceptedFixture(t)
	value := "removed generated value"
	hash := ""
	hash = baselinehash.SHA256("decision:removed", "title", "scalar", value, nil)
	accepted.Review.GeneratedBaselines = []reviewv4.Baseline{{GenerationID: "historical-generation", EntityID: "decision:removed", Field: "title", Kind: "scalar", Value: &value, GeneratedHash: hash}}
	override := "preserved human value"
	accepted.Review.OrphanPatches = []reviewv4.Patch{{EntityID: "decision:removed", Field: "title", Operation: "set", Value: &override, BaseGeneratedHash: hash}}
	accepted.Ledger.GeneratedBaselines = append([]reviewv4.Baseline(nil), accepted.Review.GeneratedBaselines...)
	accepted.Ledger.OrphanPatches = append([]reviewv4.Patch(nil), accepted.Review.OrphanPatches...)
	accepted.Ledger.DocumentProjection.PresentationBase = accepted.Review
	accepted.Ledger.SyncHashes.LedgerSHA256 = reviewv4.CanonicalLedgerSHA256(accepted.Ledger)

	next, _, err := mapV4Scan(v4MapInput{Accepted: accepted, Index: accepted.SessionIndex})
	if err != nil {
		t.Fatal(err)
	}
	if next.GeneratedBaselines[0].GenerationID != "historical-generation" || !reflect.DeepEqual(next.OrphanPatches, accepted.Review.OrphanPatches) {
		t.Fatalf("historical orphan was rewritten: baseline=%+v orphan=%+v", next.GeneratedBaselines[0], next.OrphanPatches)
	}
}

func TestV4ScanAcceptsValidHistoricalListOrphanWithoutPromotingIt(t *testing.T) {
	accepted := v4AcceptedFixture(t)
	values := []string{"historical", "ordered"}
	hash := baselinehash.SHA256("decision:removed", "tags", "list", "", values)
	accepted.Review.GeneratedBaselines = []reviewv4.Baseline{{GenerationID: "historical-generation", EntityID: "decision:removed", Field: "tags", Kind: "list", Values: &values, GeneratedHash: hash}}
	override := []string{"preserved", "human"}
	accepted.Review.OrphanPatches = []reviewv4.Patch{{EntityID: "decision:removed", Field: "tags", Operation: "set", Values: &override, BaseGeneratedHash: hash}}
	accepted.Ledger.GeneratedBaselines = append([]reviewv4.Baseline(nil), accepted.Review.GeneratedBaselines...)
	accepted.Ledger.OrphanPatches = append([]reviewv4.Patch(nil), accepted.Review.OrphanPatches...)
	accepted.Ledger.DocumentProjection.PresentationBase = accepted.Review
	accepted.Ledger.SyncHashes.LedgerSHA256 = reviewv4.CanonicalLedgerSHA256(accepted.Ledger)

	next, ledger, err := mapV4Scan(v4MapInput{Accepted: accepted, Index: accepted.SessionIndex})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.GeneratedBaselines, accepted.Review.GeneratedBaselines) || !reflect.DeepEqual(next.OrphanPatches, accepted.Review.OrphanPatches) ||
		!reflect.DeepEqual(ledger.GeneratedBaselines, accepted.Review.GeneratedBaselines) || !reflect.DeepEqual(ledger.OrphanPatches, accepted.Review.OrphanPatches) {
		t.Fatalf("historical list orphan was changed: presentation=%+v ledger=%+v", next, ledger)
	}
}

func TestV4ScanMapsAuthenticatedMigrationWithHistoricalListOrphan(t *testing.T) {
	read := func(relative string) []byte {
		t.Helper()
		body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contracts", "migration", "v4", filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	reviewBody := read(migrationv4.ReviewRelativePath)
	historyBody := read(migrationv4.HistoryRelativePath)
	ledgerBody := read(migrationv4.LedgerRelativePath)
	indexBody := read(migrationv4.SessionIndexRelativePath)
	source, err := reviewv4.LoadProjection(reviewBody, historyBody, ledgerBody, indexBody)
	if err != nil {
		t.Fatal(err)
	}
	values := []string{"historical", "ordered"}
	hash := baselinehash.SHA256("decision:removed", "tags", "list", "", values)
	source.Review.GeneratedBaselines = []reviewv4.Baseline{{GenerationID: "historical-generation", EntityID: "decision:removed", Field: "tags", Kind: "list", Values: &values, GeneratedHash: hash}}
	override := []string{"preserved", "human"}
	source.Review.OrphanPatches = []reviewv4.Patch{{EntityID: "decision:removed", Field: "tags", Operation: "set", Values: &override, BaseGeneratedHash: hash}}
	reviewBody, err = strictjson.Encode(source.Review)
	if err != nil {
		t.Fatal(err)
	}
	source.Ledger.GeneratedBaselines = append([]reviewv4.Baseline{}, source.Review.GeneratedBaselines...)
	source.Ledger.OrphanPatches = append([]reviewv4.Patch{}, source.Review.OrphanPatches...)
	reviewDigest := fmt.Sprintf("%x", sha256.Sum256(reviewBody))
	source.Ledger.ReviewSHA256, source.Ledger.SyncHashes.ReviewSHA256 = reviewDigest, reviewDigest
	ledgerBody, err = reviewv4.RenderLedger(source.Ledger)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := migrationv4.BuildMarkdownPreview(migrationv4.MarkdownMigrationInput{Source: migrationv4.Input{
		Review: reviewBody, History: historyBody, Ledger: ledgerBody, SourceSessionIndex: indexBody, SessionIndex: indexBody,
		TargetPreimages: map[string]migrationv4.Preimage{}, TargetVaultPreimages: map[string]migrationv4.Preimage{},
	}})
	if err != nil {
		t.Fatal(err)
	}
	nextIndex := migrated.Accepted.SessionIndex
	nextIndex.GenerationID = "generation-after-migration"
	nextIndex.ProjectViewDigest = "sha256:" + strings.Repeat("7", 64)
	nextIndexBody, err := sessionindex.Render(nextIndex)
	if err != nil {
		t.Fatal(err)
	}
	nextIndex, err = sessionindex.Parse(nextIndexBody)
	if err != nil {
		t.Fatal(err)
	}
	next, ledger, err := mapV4Scan(v4MapInput{Accepted: migrated.Accepted, Index: nextIndex})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.GeneratedBaselines, source.Review.GeneratedBaselines) || !reflect.DeepEqual(next.OrphanPatches, source.Review.OrphanPatches) || !reflect.DeepEqual(ledger.GeneratedBaselines, source.Review.GeneratedBaselines) {
		t.Fatalf("real scan mapper changed migrated historical list metadata: presentation=%+v ledger=%+v", next, ledger)
	}
}

func TestV4ScanRejectsInvalidHistoricalBaselineAndPatchMetadata(t *testing.T) {
	valid := func(t *testing.T) reviewv4.Accepted {
		t.Helper()
		accepted := v4AcceptedFixture(t)
		values := []string{"historical", "ordered"}
		hash := baselinehash.SHA256("decision:removed", "tags", "list", "", values)
		accepted.Review.GeneratedBaselines = []reviewv4.Baseline{{GenerationID: "historical-generation", EntityID: "decision:removed", Field: "tags", Kind: "list", Values: &values, GeneratedHash: hash}}
		override := []string{"preserved", "human"}
		accepted.Review.OrphanPatches = []reviewv4.Patch{{EntityID: "decision:removed", Field: "tags", Operation: "set", Values: &override, BaseGeneratedHash: hash}}
		return accepted
	}
	tests := []struct {
		name   string
		mutate func(*reviewv4.Presentation)
	}{
		{"duplicate baseline", func(p *reviewv4.Presentation) {
			p.GeneratedBaselines = append(p.GeneratedBaselines, p.GeneratedBaselines[0])
		}},
		{"scalar carries list", func(p *reviewv4.Presentation) { p.GeneratedBaselines[0].Kind = "scalar" }},
		{"list carries scalar", func(p *reviewv4.Presentation) { value := "wrong"; p.GeneratedBaselines[0].Value = &value }},
		{"wrong hash", func(p *reviewv4.Presentation) { p.GeneratedBaselines[0].GeneratedHash = strings.Repeat("f", 64) }},
		{"patch linkage", func(p *reviewv4.Presentation) { p.OrphanPatches[0].BaseGeneratedHash = strings.Repeat("e", 64) }},
		{"duplicate orphan patch", func(p *reviewv4.Presentation) { p.OrphanPatches = append(p.OrphanPatches, p.OrphanPatches[0]) }},
		{"stale live scalar", func(p *reviewv4.Presentation) {
			value := p.CurrentState.Goal
			hash := baselinehash.SHA256("project-overview", "goal", "scalar", value, nil)
			p.GeneratedBaselines[0] = reviewv4.Baseline{GenerationID: "stale-generation", EntityID: "project-overview", Field: "goal", Kind: "scalar", Value: &value, GeneratedHash: hash}
			p.OrphanPatches = []reviewv4.Patch{}
		}},
		{"live orphan collision", func(p *reviewv4.Presentation) {
			value := p.CurrentState.Goal
			hash := baselinehash.SHA256("project-overview", "goal", "scalar", value, nil)
			override := "orphan override"
			p.GeneratedBaselines[0] = reviewv4.Baseline{GenerationID: p.GenerationID, EntityID: "project-overview", Field: "goal", Kind: "scalar", Value: &value, GeneratedHash: hash}
			p.OrphanPatches[0] = reviewv4.Patch{EntityID: "project-overview", Field: "goal", Operation: "set", Value: &override, BaseGeneratedHash: hash}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			accepted := valid(t)
			test.mutate(&accepted.Review)
			if _, _, err := mapV4Scan(v4MapInput{Accepted: accepted, Index: accepted.SessionIndex}); err == nil {
				t.Fatal("invalid historical baseline or patch metadata was accepted")
			}
		})
	}
}

func TestV4ScanPreservesAuthenticatedCostsOnlyForExactUsageAndPricingIdentity(t *testing.T) {
	for _, fixture := range []struct {
		name          string
		completePrice bool
		projectCost   *float64
		modelCosts    []*float64
	}{
		{name: "complete", completePrice: true, projectCost: float64Pointer(2.5), modelCosts: []*float64{float64Pointer(1), float64Pointer(1.5)}},
		{name: "partial", completePrice: false, projectCost: nil, modelCosts: []*float64{float64Pointer(1), nil}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			accepted := v4AcceptedFixture(t)
			usageDigest := "sha256:" + strings.Repeat("3", 64)
			accepted.SessionIndex.Sessions[0].UsageRecordDigest = &usageDigest
			accepted.Ledger.Sessions = []reviewv4.LedgerSession{{Provider: "claude", SessionID: "session-1", ProcessingState: reviewv4.ProcessingComplete, SourceAvailability: "available", SessionViewDigest: cloneV4String(accepted.SessionIndex.Sessions[0].SessionViewDigest), UsageRecordDigest: &usageDigest}}
			accepted.Ledger.Accounting = reviewv4.Accounting{TotalDurationMS: 60_000, TotalTokens: 25, TotalCostUSD: fixture.projectCost, Models: []reviewv4.Model{{Model: "model-a", TotalTokens: 10, TotalCostUSD: fixture.modelCosts[0]}, {Model: "model-b", TotalTokens: 15, TotalCostUSD: fixture.modelCosts[1]}}}
			snapshot := pricedSnapshot(accepted.Review.ProjectID, usageDigest, fixture.completePrice)
			accepted.Ledger.PricingSnapshots = []pricing.Snapshot{snapshot}
			accepted.Ledger.CurrentPricingSnapshotIDs = []string{snapshot.SnapshotID}
			accepted.Ledger.SyncHashes.LedgerSHA256 = reviewv4.CanonicalLedgerSHA256(accepted.Ledger)
			accountingNow := accounting.ProjectSummary{TotalDurationMS: 60_000, TotalTokens: 25, TotalCostUSD: 999, Models: []accounting.ProjectModelSummary{{Model: "model-a", TotalTokens: 10, TotalCostUSD: 444}, {Model: "model-b", TotalTokens: 15, TotalCostUSD: 555}}}

			_, got, err := mapV4Scan(v4MapInput{Accepted: accepted, Index: accepted.SessionIndex, Accounting: accountingNow})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Accounting, accepted.Ledger.Accounting) || !reflect.DeepEqual(got.PricingSnapshots, accepted.Ledger.PricingSnapshots) || !reflect.DeepEqual(got.CurrentPricingSnapshotIDs, accepted.Ledger.CurrentPricingSnapshotIDs) {
				t.Fatalf("exact authenticated pricing identity was not preserved: got=%+v want=%+v", got.Accounting, accepted.Ledger.Accounting)
			}
			changedTokens := accountingNow
			changedTokens.TotalTokens = 26
			changedTokens.Models = append([]accounting.ProjectModelSummary(nil), accountingNow.Models...)
			changedTokens.Models[1].TotalTokens = 16
			_, got, err = mapV4Scan(v4MapInput{Accepted: accepted, Index: accepted.SessionIndex, Accounting: changedTokens})
			if err != nil {
				t.Fatal(err)
			}
			if got.Accounting.TotalCostUSD != nil || got.Accounting.Models[0].TotalCostUSD != nil || got.Accounting.Models[1].TotalCostUSD != nil {
				t.Fatalf("changed aggregate/model tokens retained stale costs: %+v", got.Accounting)
			}

			changed := accepted.SessionIndex
			changedDigest := "sha256:" + strings.Repeat("4", 64)
			changed.Sessions[0].UsageRecordDigest = &changedDigest
			_, got, err = mapV4Scan(v4MapInput{Accepted: accepted, Index: changed, Accounting: accountingNow})
			if err != nil {
				t.Fatal(err)
			}
			if got.Accounting.TotalCostUSD != nil || got.Accounting.Models[0].TotalCostUSD != nil || got.Accounting.Models[1].TotalCostUSD != nil {
				t.Fatalf("changed usage identity retained stale costs: %+v", got.Accounting)
			}
			if !reflect.DeepEqual(got.PricingSnapshots, accepted.Ledger.PricingSnapshots) || !reflect.DeepEqual(got.CurrentPricingSnapshotIDs, accepted.Ledger.CurrentPricingSnapshotIDs) {
				t.Fatal("changed usage identity discarded authenticated pricing history")
			}
		})
	}
}

func pricedSnapshot(projectID, usageDigest string, complete bool) pricing.Snapshot {
	one, zero, total := 1.0, 0.0, 1.0
	source := "https://example.test/pricing"
	snapshot := pricing.Snapshot{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", SnapshotID: "snapshot-priced", ProjectID: projectID, Provider: "claude", SessionID: "session-1", UsageRecordDigest: usageDigest, BillingHost: "api.example.test", BilledModelID: "model-a", BillingMode: "standard", BillingRuleVersion: "rule-1", PricedAt: "2026-09-05T00:00:00Z", CreatedAt: "2026-09-05T00:00:00Z", Status: pricing.PriceCurrent, SourceKind: "official", SourceURL: &source, Rates: pricing.Rates{Input: &one, CachedInput: &zero, CacheWriteInput: &zero, Output: &zero, ReasoningOutput: &zero}, BillableQuantities: pricing.Quantities{Input: 1_000_000}, LineCostsUSD: pricing.LineCosts{Input: &one, CachedInput: &zero, CacheWriteInput: &zero, Output: &zero, ReasoningOutput: &zero}, MissingBillingDimensions: []string{}, KnownSubtotalUSD: 1, TotalCostUSD: &total, PricingComplete: true, AuditReason: "Exact fixture."}
	if !complete {
		snapshot.PricingComplete = false
		snapshot.TotalCostUSD = nil
		snapshot.Rates.Output = nil
		snapshot.LineCostsUSD.Output = nil
		snapshot.MissingBillingDimensions = []string{"output"}
	}
	return snapshot
}

func float64Pointer(value float64) *float64 { return &value }

func v4AcceptedFixture(t *testing.T) reviewv4.Accepted {
	t.Helper()
	read := func(name string) []byte {
		body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contracts", "v4", "markdown", name))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	accepted, err := reviewv4.LoadProjection(read("review.md"), read("history.md"), read("ledger.json"), read("index.json"))
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}
