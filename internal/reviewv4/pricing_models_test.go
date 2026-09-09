package reviewv4

import (
	"github.com/neomei/SessionReviewer/internal/pricing"
	"testing"
)

func TestLedgerPricingSeparatesModelsInSameUsageRecord(t *testing.T) {
	ledger := frozenLedger(t)
	first := ledger.PricingSnapshots[0]
	second := first
	second.SnapshotID = "snapshot-model-two"
	second.BilledModelID = "model-two"
	ledger.PricingSnapshots = []pricing.Snapshot{first, second}
	ledger.CurrentPricingSnapshotIDs = []string{first.SnapshotID, second.SnapshotID}
	if err := ValidateLedger(ledger); err != nil {
		t.Fatalf("two model prices in one authenticated Session usage rejected: %v", err)
	}
	second.BilledModelID = first.BilledModelID
	ledger.PricingSnapshots[1] = second
	if err := ValidateLedger(ledger); err == nil {
		t.Fatal("same model received two effective prices")
	}
	first.Status = pricing.PriceSuperseded
	second.BilledModelID = "model-two"
	second.SupersedesSnapshotID = &first.SnapshotID
	ledger.PricingSnapshots = []pricing.Snapshot{first, second}
	ledger.CurrentPricingSnapshotIDs = []string{second.SnapshotID}
	if err := ValidateLedger(ledger); err == nil {
		t.Fatal("different model replaced original model price")
	}
}
