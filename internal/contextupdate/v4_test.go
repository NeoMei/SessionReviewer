package contextupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/pricing"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
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
