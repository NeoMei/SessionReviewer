package pricing

import (
	"math"
	"os"
	"testing"
)

func TestParseFrozenValidFixture(t *testing.T) {
	b, e := os.ReadFile("../../testdata/contracts/v4/pricing-snapshot-v1.valid.json")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Parse(b); e != nil {
		t.Fatal(e)
	}
}

func TestValidateSnapshotRejectsIncompleteMarkedComplete(t *testing.T) {
	u := "https://example.test"
	s := Snapshot{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", SnapshotID: "s", ProjectID: "p", Provider: "codex", SessionID: "x", UsageRecordDigest: "sha256:" + ones, BillingHost: "h", BilledModelID: "m", BillingMode: "standard", BillingRuleVersion: "r", PricedAt: "now", CreatedAt: "now", Status: PriceCurrent, SourceKind: "official", SourceURL: &u, Rates: Rates{}, BillableQuantities: Quantities{}, LineCostsUSD: LineCosts{}, KnownSubtotalUSD: 0, PricingComplete: true}
	if err := ValidateSnapshot(s); err == nil {
		t.Fatal("accepted incomplete snapshot")
	}
}

func TestValidateSnapshotCompletenessAndFreePrice(t *testing.T) {
	zero := 0.0
	ones := func() *float64 { v := 1.0; return &v }
	tests := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{name: "nil rate", mutate: func(s *Snapshot) { s.Rates.Output = nil }},
		{name: "nil line cost", mutate: func(s *Snapshot) { s.LineCostsUSD.Output = nil }},
		{name: "nil total", mutate: func(s *Snapshot) { s.TotalCostUSD = nil }},
		{name: "missing dimension", mutate: func(s *Snapshot) { s.MissingBillingDimensions = []string{"output"} }},
		{name: "incomplete route evidence", mutate: func(s *Snapshot) { s.SourceURL = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := completeSnapshot()
			tc.mutate(&s)
			if err := ValidateSnapshot(s); err == nil {
				t.Fatal("accepted invalid complete snapshot")
			}
		})
	}
	free := completeSnapshot()
	free.Rates = Rates{&zero, &zero, &zero, &zero, &zero}
	free.LineCostsUSD = LineCosts{&zero, &zero, &zero, &zero, &zero}
	free.KnownSubtotalUSD = 0
	free.TotalCostUSD = &zero
	if err := ValidateSnapshot(free); err != nil {
		t.Fatalf("numeric zero must be a known free price: %v", err)
	}
	unknown := free
	unknown.Rates.Input = nil
	if err := ValidateSnapshot(unknown); err == nil {
		t.Fatal("null rate was treated as free")
	}
	bad := completeSnapshot()
	bad.TotalCostUSD = ones()
	bad.KnownSubtotalUSD = math.Inf(1)
	if err := ValidateSnapshot(bad); err == nil {
		t.Fatal("accepted non-finite subtotal")
	}
}

func TestParseAndRenderPricingFixtureParity(t *testing.T) {
	valid, err := os.ReadFile("../../testdata/contracts/v4/pricing-snapshot-v1.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(valid)
	if err != nil {
		t.Fatal(err)
	}
	one, err := Render(got)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Render(got)
	if err != nil || string(one) != string(two) {
		t.Fatalf("non-deterministic render: %v", err)
	}
	invalid, err := os.ReadFile("../../testdata/contracts/v4/pricing-snapshot-v1.invalid.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(invalid); err == nil {
		t.Fatal("accepted frozen invalid fixture")
	}
}

func TestPricingSupplementFixtureParityAndNullMeansUnknown(t *testing.T) {
	valid, err := os.ReadFile("../../testdata/contracts/v4/pricing-supplement-v1.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	supplement, err := ParseSupplement(valid)
	if err != nil {
		t.Fatal(err)
	}
	if supplement.Rates.Input == nil || *supplement.Rates.Input != 0 || supplement.Rates.CachedInput != nil {
		t.Fatalf("free and unknown rates collapsed: %+v", supplement.Rates)
	}
	if _, err := RenderSupplement(supplement); err != nil {
		t.Fatal(err)
	}
	invalid, err := os.ReadFile("../../testdata/contracts/v4/pricing-supplement-v1.invalid.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSupplement(invalid); err == nil {
		t.Fatal("accepted frozen invalid supplement fixture")
	}
}

func completeSnapshot() Snapshot {
	v := 1.0
	five := 5.0
	u := "https://example.test/pricing"
	return Snapshot{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", SnapshotID: "snapshot-1", ProjectID: "project-p", Provider: "codex", SessionID: "session-1", UsageRecordDigest: "sha256:" + ones, BillingHost: "api.example.test", BilledModelID: "model-1", BillingMode: "standard", BillingRuleVersion: "rules-v1", PricedAt: "2026-09-04T00:00:00Z", CreatedAt: "2026-09-04T00:00:00Z", Status: PriceCurrent, SourceKind: "official", SourceURL: &u, RetrievedAt: strptr("2026-09-04T00:00:00Z"), Rates: Rates{&v, &v, &v, &v, &v}, BillableQuantities: Quantities{1000000, 1000000, 1000000, 1000000, 1000000}, LineCostsUSD: LineCosts{&v, &v, &v, &v, &v}, MissingBillingDimensions: []string{}, KnownSubtotalUSD: 5, TotalCostUSD: &five, PricingComplete: true, AuditReason: "Exact route."}
}

func strptr(value string) *string { return &value }

const ones = "1111111111111111111111111111111111111111111111111111111111111111"
