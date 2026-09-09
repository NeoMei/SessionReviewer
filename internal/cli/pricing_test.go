package cli

import (
	"bytes"
	"encoding/json"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/pricing"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPricingCLIHelpAndStrictRefreshContract(t *testing.T) {
	var out, diag bytes.Buffer
	if code := Run([]string{"pricing", "--help"}, &out, &diag); code != 0 || !strings.Contains(out.String(), "refresh") {
		t.Fatalf("pricing entry missing: %d %s %s", code, &out, &diag)
	}
	root := t.TempDir()
	request, err := ParsePricingContract([]string{"refresh", "--data-dir", root, "--json"})
	if err != nil || request.DataDir != root || request.Command != "refresh" {
		t.Fatalf("refresh contract: %+v %v", request, err)
	}
	for _, args := range [][]string{{"refresh", "--url", "https://example.com", "--json"}, {"refresh", "--data-dir", "relative", "--json"}, {"refresh", "--json", "--json"}, {"refresh", "--force", "--json"}} {
		if _, err := ParsePricingContract(args); err == nil {
			t.Fatalf("unsafe refresh argv accepted: %v", args)
		}
	}
}

func TestPricingSupplementPublishesCalculatedCostAndSurvivesRescan(t *testing.T) {
	projectRoot, vaultRoot, dataRoot, sessionsRoot := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = projectRoot
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v %s", err, output)
		}
	}
	const sessionID = "77777777-7777-4777-8777-777777777777"
	var source bytes.Buffer
	records := []map[string]any{
		{"timestamp": "2026-09-09T00:00:00Z", "type": "session_meta", "payload": map[string]any{"id": sessionID, "cwd": projectRoot}},
		{"timestamp": "2026-09-09T00:00:01Z", "type": "event_msg", "payload": map[string]any{"type": "token_count", "info": map[string]any{"last_token_usage": map[string]any{"input_tokens": 100, "cached_input_tokens": 20, "output_tokens": 10, "total_tokens": 110}}}},
	}
	for _, r := range records {
		if err := json.NewEncoder(&source).Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sessionsRoot, "rollout-2026-09-09T00-00-00-"+sessionID+".jsonl"), source.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	runCurrentInitCLI(t, []string{"init", "--project", projectRoot, "--vault", vaultRoot, "--data-dir", dataRoot, "--write"})
	cfg, err := config.Load(filepath.Join(dataRoot, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	id := cfg.Projects[0].ID
	scanArgs := []string{"scan", "--project-id", id, "--sessions-root", sessionsRoot, "--data-dir", dataRoot, "--json"}
	runCurrentInitCLI(t, scanArgs)
	load := func() reviewv4.MachineLedger {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(projectRoot, reviewv2.MachineLedgerRelativePath))
		if err != nil {
			t.Fatal(err)
		}
		l, err := reviewv4.DecodeLedger(b)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	before := load()
	if len(before.PricingSnapshots) != 1 {
		t.Fatalf("no pending snapshot: %+v", before.PricingSnapshots)
	}
	prior := before.PricingSnapshots[0]
	one, two, zero := 1.0, 2.0, 0.0
	supplement := pricing.Supplement{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: id, Provider: "codex", SessionID: sessionID, UsageRecordDigest: prior.UsageRecordDigest, BillingHost: "api.example.test", BilledModelID: prior.BilledModelID, BillingMode: "standard", BillingRuleVersion: prior.BillingRuleVersion, EffectiveFrom: "2026-09-01T00:00:00Z", Rates: pricing.Rates{Input: &one, CachedInput: &zero, CacheWriteInput: &zero, Output: &two, ReasoningOutput: &zero}, SourceURL: "https://example.test/prices", AuditReason: "Human confirmed exact billing route and public rates", SupersedesSnapshotID: &prior.SnapshotID}
	body, err := pricing.RenderSupplement(supplement)
	if err != nil {
		t.Fatal(err)
	}
	ledgerBytes, _ := os.ReadFile(filepath.Join(projectRoot, reviewv2.MachineLedgerRelativePath))
	args := []string{"supplement", "--project-id", id, "--provider", "codex", "--session-id", sessionID, "--usage-record-digest", prior.UsageRecordDigest, "--expected-ledger-sha256", testBareSHA(ledgerBytes), "--data-dir", dataRoot, "--json"}
	var out, diag bytes.Buffer
	if code := runPricing(args, bytes.NewReader(body), &out, &diag); code != 0 {
		t.Fatalf("supplement failed %d %s %s", code, &out, &diag)
	}
	after := load()
	if len(after.PricingSnapshots) != 2 || after.Accounting.TotalCostUSD == nil || math.Abs(*after.Accounting.TotalCostUSD-0.0001) > 1e-12 {
		t.Fatalf("not calculated from captured quantities: %+v", after.Accounting)
	}
	stable, _ := os.ReadFile(filepath.Join(projectRoot, reviewv2.MachineLedgerRelativePath))
	out.Reset()
	if code := runPricing(args, bytes.NewReader(body), &out, &diag); code == 0 {
		t.Fatal("stale supplement accepted")
	}
	check, _ := os.ReadFile(filepath.Join(projectRoot, reviewv2.MachineLedgerRelativePath))
	if !bytes.Equal(stable, check) {
		t.Fatal("stale supplement changed ledger")
	}
	runCurrentInitCLI(t, scanArgs)
	rescanned := load()
	if rescanned.Accounting.TotalCostUSD == nil || *rescanned.Accounting.TotalCostUSD != *after.Accounting.TotalCostUSD || len(rescanned.PricingSnapshots) != 2 {
		t.Fatal("rescan rewrote or lost supplement")
	}
	vaultLedger, err := os.ReadFile(filepath.Join(vaultRoot, cfg.Projects[0].VaultReviewPath, ".session-reviewer/ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	projectLedger, _ := os.ReadFile(filepath.Join(projectRoot, reviewv2.MachineLedgerRelativePath))
	if !bytes.Equal(vaultLedger, projectLedger) {
		t.Fatal("Vault price readback differs")
	}
}

func TestPricingAccountingNeedsEveryExpectedUsageIdentity(t *testing.T) {
	cost := 1.0
	ledger := reviewv4.MachineLedger{Accounting: reviewv4.Accounting{Models: []reviewv4.Model{{Model: "model-a"}}}, PricingSnapshots: []pricing.Snapshot{{SnapshotID: "one", Provider: "codex", SessionID: "s1", UsageRecordDigest: "d1", BilledModelID: "model-a", TotalCostUSD: &cost, PricingComplete: true}}, CurrentPricingSnapshotIDs: []string{"one"}}
	wanted := map[string]string{pricingUsageKey("codex", "s1", "d1", "model-a"): "model-a", pricingUsageKey("codex", "s2", "d2", "model-a"): "model-a"}
	if err := recomputePricingAccounting(&ledger, wanted); err != nil {
		t.Fatal(err)
	}
	if ledger.Accounting.TotalCostUSD != nil || ledger.Accounting.Models[0].TotalCostUSD != nil {
		t.Fatal("unpriced Session was silently omitted from total")
	}
}
