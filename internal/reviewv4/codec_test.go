package reviewv4

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/pricing"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

func TestRenderFrozenValidLedgerFixture(t *testing.T) {
	b, e := os.ReadFile("../../testdata/contracts/v4/machine-ledger-v4.valid.json")
	if e != nil {
		t.Fatal(e)
	}
	l, e := DecodeLedger(b)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = RenderLedger(l); e != nil {
		t.Fatal(e)
	}
}

func TestReviewParseRejectsUnknownFields(t *testing.T) {
	if _, err := Parse([]byte(`{"unknown":true}`), []byte(`{}`), []byte(`{}`)); err == nil {
		t.Fatal("accepted unknown review fields")
	}
}

func TestFrozenInvalidReviewAndLedgerFixturesAreRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		fn   func([]byte) error
	}{
		{name: "review", path: "../../testdata/contracts/v4/review-presentation-v4.invalid.json", fn: func(b []byte) error { _, err := DecodePresentation(b); return err }},
		{name: "ledger", path: "../../testdata/contracts/v4/machine-ledger-v4.invalid.json", fn: func(b []byte) error { _, err := DecodeLedger(b); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.fn(b); err == nil {
				t.Fatal("accepted frozen invalid fixture")
			}
		})
	}
}

func TestValidatePresentationRejectsDecisionCycleAndBrokenGraph(t *testing.T) {
	p := minimumPresentation()
	p.Decisions = []Decision{minimumDecision("a", []string{"b"}), minimumDecision("b", []string{"a"})}
	if err := ValidatePresentation(p); err == nil {
		t.Fatal("accepted supersession cycle")
	}
	p.Decisions = []Decision{minimumDecision("old", nil), minimumDecision("new", []string{"old"})}
	p.Decisions[0].Status = DecisionSuperseded
	if err := ValidatePresentation(p); err != nil {
		t.Fatal(err)
	}
	p.Decisions[1].Supersedes = nil
	if err := ValidatePresentation(p); err == nil {
		t.Fatal("accepted superseded decision without successor")
	}
}

func TestLoadProjectionEnforcesAllIdentityAndDigestBindings(t *testing.T) {
	reviewFixture := mustRead(t, "../../testdata/contracts/v4/review-presentation-v4.valid.json")
	indexFixture := mustRead(t, "../../testdata/contracts/v4/session-index-v1.valid.json")
	ledgerFixture := mustRead(t, "../../testdata/contracts/v4/machine-ledger-v4.valid.json")
	tests := []struct {
		name       string
		mutateDocs func(*Presentation, *MachineLedger, *sessionindex.Document, *[]byte)
		breakBind  func(*MachineLedger)
	}{
		{name: "project ID", mutateDocs: func(_ *Presentation, ledger *MachineLedger, _ *sessionindex.Document, _ *[]byte) {
			ledger.ProjectID = "other"
			for index := range ledger.PricingSnapshots {
				ledger.PricingSnapshots[index].ProjectID = "other"
			}
		}},
		{name: "generation ID", mutateDocs: func(_ *Presentation, _ *MachineLedger, index *sessionindex.Document, _ *[]byte) {
			index.GenerationID = "other"
		}},
		{name: "project view digest", mutateDocs: func(p *Presentation, _ *MachineLedger, _ *sessionindex.Document, _ *[]byte) {
			p.ProjectViewDigest = "sha256:" + strings.Repeat("9", 64)
		}},
		{name: "review digest", breakBind: func(ledger *MachineLedger) {
			ledger.ReviewSHA256 = strings.Repeat("9", 64)
			ledger.SyncHashes.ReviewSHA256 = ledger.ReviewSHA256
		}},
		{name: "history digest", breakBind: func(ledger *MachineLedger) {
			ledger.HistorySHA256 = strings.Repeat("9", 64)
			ledger.SyncHashes.HistorySHA256 = ledger.HistorySHA256
		}},
		{name: "index digest", breakBind: func(ledger *MachineLedger) {
			ledger.SyncHashes.SessionIndexDigest = "sha256:" + strings.Repeat("9", 64)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			presentation, err := DecodePresentation(reviewFixture)
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := DecodeLedger(ledgerFixture)
			if err != nil {
				t.Fatal(err)
			}
			index, err := sessionindex.Parse(indexFixture)
			if err != nil {
				t.Fatal(err)
			}
			history := []byte("# project history\n")
			if tc.mutateDocs != nil {
				tc.mutateDocs(&presentation, &ledger, &index, &history)
			}
			reviewBytes, err := json.Marshal(presentation)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodePresentation(reviewBytes); err != nil {
				t.Fatalf("mutated review is independently invalid: %v", err)
			}
			indexBytes, err := sessionindex.Render(index)
			if err != nil {
				t.Fatalf("mutated index is independently invalid: %v", err)
			}
			validatedIndex, err := sessionindex.Parse(indexBytes)
			if err != nil {
				t.Fatal(err)
			}
			ledger.AcceptedRevision = presentation.Revision
			ledger.ReviewSHA256 = sha256hex(reviewBytes)
			ledger.HistorySHA256 = sha256hex(history)
			ledger.SyncHashes.ReviewSHA256 = ledger.ReviewSHA256
			ledger.SyncHashes.HistorySHA256 = ledger.HistorySHA256
			ledger.SyncHashes.SessionIndexDigest = validatedIndex.Digest
			if tc.breakBind != nil {
				tc.breakBind(&ledger)
			}
			ledgerBytes, err := RenderLedger(ledger)
			if err != nil {
				t.Fatalf("mutated ledger is independently invalid: %v", err)
			}
			if _, err := DecodeLedger(ledgerBytes); err != nil {
				t.Fatalf("rendered ledger is independently invalid: %v", err)
			}
			if _, err := LoadProjection(reviewBytes, history, ledgerBytes, indexBytes); err == nil {
				t.Fatal("accepted mismatched projection")
			}
		})
	}

	presentation, err := DecodePresentation(reviewFixture)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := DecodeLedger(ledgerFixture)
	if err != nil {
		t.Fatal(err)
	}
	index, err := sessionindex.Parse(indexFixture)
	if err != nil {
		t.Fatal(err)
	}
	indexBytes, err := sessionindex.Render(index)
	if err != nil {
		t.Fatal(err)
	}
	index, err = sessionindex.Parse(indexBytes)
	if err != nil {
		t.Fatal(err)
	}
	history := []byte("# project history\n")
	ledger.AcceptedRevision = presentation.Revision
	ledger.ReviewSHA256, ledger.HistorySHA256 = sha256hex(reviewFixture), sha256hex(history)
	ledger.SyncHashes.ReviewSHA256, ledger.SyncHashes.HistorySHA256 = ledger.ReviewSHA256, ledger.HistorySHA256
	ledger.SyncHashes.SessionIndexDigest = index.Digest
	ledgerBytes, err := RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := LoadProjection(reviewFixture, history, ledgerBytes, indexBytes)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.SessionIndex.ProjectID != presentation.ProjectID {
		t.Fatal("validated index missing from Accepted")
	}
	if _, err := LoadProjection(reviewFixture, history, ledgerBytes, nil); err == nil {
		t.Fatal("accepted projection without required session index")
	}
}

func TestParseAcceptsRawMarkdownHistoryAndRejectsInvalidUTF8(t *testing.T) {
	review := mustRead(t, "../../testdata/contracts/v4/review-presentation-v4.valid.json")
	ledgerBytes := mustRead(t, "../../testdata/contracts/v4/machine-ledger-v4.valid.json")
	ledger, err := DecodeLedger(ledgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	ledger.ReviewSHA256 = sha256hex(review)
	ledger.AcceptedRevision = 1
	history := []byte("# 项目历史\n\n- 保留人类可读的里程碑。\n")
	ledger.HistorySHA256 = sha256hex(history)
	ledger.SyncHashes.ReviewSHA256 = ledger.ReviewSHA256
	ledger.SyncHashes.HistorySHA256 = ledger.HistorySHA256
	ledger.SyncHashes.LedgerSHA256 = strings.Repeat("0", 64)
	ledgerBytes, _ = RenderLedger(ledger)
	if accepted, err := Parse(review, history, ledgerBytes); err != nil || !bytes.Equal(accepted.History, history) {
		t.Fatalf("raw markdown history rejected or changed: %v", err)
	}
	invalid := []byte{0xff}
	ledger.HistorySHA256 = sha256hex(invalid)
	ledger.SyncHashes.HistorySHA256 = ledger.HistorySHA256
	ledgerBytes, _ = RenderLedger(ledger)
	if _, err := Parse(review, invalid, ledgerBytes); err == nil {
		t.Fatal("accepted invalid UTF-8 history")
	}
}

func TestDecodeLedgerRejectsTamperedSelfDigest(t *testing.T) {
	fixture := mustRead(t, "../../testdata/contracts/v4/machine-ledger-v4.valid.json")
	ledger, err := DecodeLedger(fixture)
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	raw["sync_hashes"].(map[string]any)["ledger_sha256"] = strings.Repeat("9", 64)
	body, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeLedger(body); err == nil {
		t.Fatal("accepted tampered ledger self digest")
	}
}

func TestRenderLedgerPreservesExplicitEmptyOptionalArrays(t *testing.T) {
	fixture := mustRead(t, "../../testdata/contracts/v4/machine-ledger-v4.valid.json")
	var raw map[string]any
	if err := json.Unmarshal(fixture, &raw); err != nil {
		t.Fatal(err)
	}
	raw["human_patches"] = []any{map[string]any{
		"entity_id": "entity-1", "field": "field-1", "operation": "set",
		"values": []any{}, "base_generated_hash": strings.Repeat("1", 64),
	}}
	raw["generated_baselines"] = []any{map[string]any{
		"generation_id": "generation-1", "entity_id": "entity-1", "field": "field-1", "kind": "list",
		"values": []any{}, "generated_hash": strings.Repeat("2", 64),
	}}
	body, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := DecodeLedger(body)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(rendered, &got); err != nil {
		t.Fatal(err)
	}
	patch := got["human_patches"].([]any)[0].(map[string]any)
	baseline := got["generated_baselines"].([]any)[0].(map[string]any)
	for name, value := range map[string]any{"patch": patch["values"], "baseline": baseline["values"]} {
		items, present := value.([]any)
		if !present || len(items) != 0 {
			t.Fatalf("%s explicit empty values were not preserved: %#v", name, value)
		}
	}
}

func TestValidateLedgerUsesOnlyCurrentPricingForAggregateCompleteness(t *testing.T) {
	ledger := frozenLedger(t)
	historical := ledger.PricingSnapshots[0]
	current := completePricingSnapshot(t, "snapshot-current")
	zero := 0.0
	ledger.PricingSnapshots = []pricing.Snapshot{historical, current}
	ledger.CurrentPricingSnapshotIDs = []string{current.SnapshotID}
	ledger.Accounting.TotalCostUSD = &zero
	if err := ValidateLedger(ledger); err != nil {
		t.Fatalf("incomplete historical predecessor contaminated current aggregate: %v", err)
	}
}

func TestValidateLedgerRequiresNullAggregateWhenModelCostUnknown(t *testing.T) {
	ledger := frozenLedger(t)
	one := 1.0
	ledger.PricingSnapshots = []pricing.Snapshot{}
	ledger.CurrentPricingSnapshotIDs = []string{}
	ledger.Accounting.TotalTokens = 1
	ledger.Accounting.TotalCostUSD = &one
	ledger.Accounting.Models = []Model{{Model: "model-1", TotalTokens: 1, TotalCostUSD: nil}}
	if err := ValidateLedger(ledger); err == nil {
		t.Fatal("accepted non-null aggregate with unknown included model cost")
	}
}

func frozenLedger(t *testing.T) MachineLedger {
	t.Helper()
	ledger, err := DecodeLedger(mustRead(t, "../../testdata/contracts/v4/machine-ledger-v4.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

func completePricingSnapshot(t *testing.T, id string) pricing.Snapshot {
	t.Helper()
	snapshot, err := pricing.Parse(mustRead(t, "../../testdata/contracts/v4/pricing-snapshot-v1.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	zero := 0.0
	snapshot.SnapshotID = id
	snapshot.Rates = pricing.Rates{Input: &zero, CachedInput: &zero, CacheWriteInput: &zero, Output: &zero, ReasoningOutput: &zero}
	snapshot.LineCostsUSD = pricing.LineCosts{Input: &zero, CachedInput: &zero, CacheWriteInput: &zero, Output: &zero, ReasoningOutput: &zero}
	snapshot.MissingBillingDimensions = []string{}
	snapshot.KnownSubtotalUSD, snapshot.TotalCostUSD, snapshot.PricingComplete = 0, &zero, true
	if err := pricing.ValidateSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func minimumPresentation() Presentation {
	return Presentation{SchemaVersion: 4, MinimumReaderVersion: "0.4.0", MinimumWriterVersion: "0.4.0", ProjectID: "p", GenerationID: "g", ProjectViewDigest: "sha256:" + strings.Repeat("1", 64), CurrentState: CurrentState{}, Timeline: []Timeline{}, Decisions: []Decision{}, Risks: []Risk{}, OpenLoops: []OpenLoop{}, HumanPatches: []Patch{}, OrphanPatches: []Patch{}, GeneratedBaselines: []Baseline{}}
}

func minimumDecision(id string, supersedes []string) Decision {
	return Decision{ID: id, Kind: "decision", Status: DecisionActive, Supersedes: supersedes, MilestoneIDs: []string{}, SessionRefs: []SessionRef{}, Provenance: "human_created", Revision: 1}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func jsonBytes(value any) ([]byte, error) { return json.Marshal(value) }
