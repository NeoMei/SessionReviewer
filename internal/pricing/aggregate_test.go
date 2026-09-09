package pricing

import (
	"strings"
	"testing"
)

func TestAggregateSelectsOnlyLatestValidLeavesAndPreservesUnknownTotal(t *testing.T) {
	first := completeSnapshot()
	first.SnapshotID = "first"
	first.Status = PriceSuperseded
	second := completeSnapshot()
	second.SnapshotID = "second"
	second.SupersedesSnapshotID = strptr("first")
	unknown := completeSnapshot()
	unknown.SnapshotID = "unknown"
	unknown.SessionID = "session-2"
	unknown.UsageRecordDigest = "sha256:" + strings.Repeat("b", 64)
	unknown.Status = PricePending
	unknown.SourceKind = "unresolved"
	unknown.SourceURL = nil
	unknown.Rates = Rates{}
	unknown.LineCostsUSD = LineCosts{}
	unknown.MissingBillingDimensions = []string{"input", "cached_input", "cache_write_input", "output", "reasoning_output"}
	unknown.KnownSubtotalUSD = 0
	unknown.TotalCostUSD = nil
	unknown.PricingComplete = false
	got, err := Aggregate([]Snapshot{first, second, unknown})
	if err != nil {
		t.Fatal(err)
	}
	if got.KnownSubtotalUSD != 5 || got.TotalCostUSD != nil || got.PricingComplete || len(got.CurrentSnapshotIDs) != 2 {
		t.Fatalf("got=%#v", got)
	}
}

func TestAggregateRejectsForkedSupersessionAndIdentityChanges(t *testing.T) {
	root := completeSnapshot()
	root.SnapshotID = "root"
	root.Status = PriceSuperseded
	a := completeSnapshot()
	a.SnapshotID = "a"
	a.SupersedesSnapshotID = strptr("root")
	b := completeSnapshot()
	b.SnapshotID = "b"
	b.SupersedesSnapshotID = strptr("root")
	if _, err := Aggregate([]Snapshot{root, a, b}); err == nil {
		t.Fatal("accepted fork")
	}
	b.SupersedesSnapshotID = strptr("a")
	b.SessionID = "other"
	a.Status = PriceSuperseded
	if _, err := Aggregate([]Snapshot{root, a, b}); err == nil {
		t.Fatal("accepted identity change")
	}
}
