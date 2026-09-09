package contextupdate

import (
	"context"
	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/pricing"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestScanPricingMissingRouteIsPendingAndStable(t *testing.T) {
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	requests := []pricing.ResolutionRequest{}
	for _, model := range []string{"model-a", "model-b"} {
		requests = append(requests, pricing.ResolutionRequest{ProjectID: "p", Provider: "codex", SessionID: "s", UsageRecordDigest: "sha256:" + strings.Repeat("a", 64), Route: pricing.BillingRoute{ModelID: model}, Usage: accounting.ModelUsage{Model: model, TokenUsage: accounting.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}, PricedAt: now})
	}
	ledger := reviewv4.MachineLedger{ProjectID: "p", PricingSnapshots: []pricing.Snapshot{}, CurrentPricingSnapshotIDs: []string{}, Accounting: reviewv4.Accounting{TotalTokens: 30, Models: []reviewv4.Model{{Model: "model-a", TotalTokens: 15}, {Model: "model-b", TotalTokens: 15}}}}
	if err := applyScanPricing(context.Background(), &ledger, requests, now); err != nil {
		t.Fatal(err)
	}
	if len(ledger.PricingSnapshots) != 2 || len(ledger.CurrentPricingSnapshotIDs) != 2 || ledger.Accounting.TotalCostUSD != nil {
		t.Fatalf("missing per-model pending state: %+v", ledger)
	}
	for _, p := range ledger.PricingSnapshots {
		if p.Status != pricing.PricePending || p.BillingHost != "unknown" || p.TotalCostUSD != nil {
			t.Fatalf("invented price or route: %+v", p)
		}
	}
	before := append([]pricing.Snapshot(nil), ledger.PricingSnapshots...)
	if err := applyScanPricing(context.Background(), &ledger, requests, now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, ledger.PricingSnapshots) {
		t.Fatal("unchanged rescan rewrote immutable prices")
	}
	requests[0].UsageRecordDigest = "sha256:" + strings.Repeat("b", 64)
	if err := applyScanPricing(context.Background(), &ledger, requests, now); err != nil {
		t.Fatal(err)
	}
	if len(ledger.PricingSnapshots) != 3 || len(ledger.CurrentPricingSnapshotIDs) != 2 {
		t.Fatal("changed usage did not retain old snapshot and replace current selection")
	}
}

func TestScanPricingUsesSessionEndTime(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	index := sessionindex.Document{Sessions: []sessionindex.Entry{{Provider: "codex", SessionID: "s", UsageRecordDigest: &digest}}}
	reports := []ledger.SessionReport{{SessionID: "codex/s", Accounting: &accounting.SessionAccounting{StartedAt: "2026-09-08T23:59:00Z", EndedAt: "2026-09-09T00:01:00Z", Models: []accounting.ModelAccounting{{ModelUsage: accounting.ModelUsage{Model: "model-a"}}}}}}
	requests, err := scanPricingRequests("p", index, reports)
	if err != nil || len(requests) != 1 || requests[0].PricedAt.Format(time.RFC3339) != "2026-09-09T00:01:00Z" {
		t.Fatalf("wrong price-effective time: %+v %v", requests, err)
	}
}
