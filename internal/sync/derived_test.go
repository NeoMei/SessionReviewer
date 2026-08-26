package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

func TestMachineLedgerTamperBlocksNormalSyncAndExplicitRepairRestoresCanonicalBytes(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	options := fixture.options()
	options.Now = func() time.Time { return now }
	engine, err := NewEngine(options)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil || report.Machine.State != MachineCurrent {
		t.Fatalf("initial report=%+v err=%v", report, err)
	}
	projectPath := filepath.Join(fixture.project, filepath.FromSlash(reviewv2.MachineLedgerRelativePath))
	vaultPath := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), ".session-reviewer", "ledger.json")
	projectBefore := readDerivedTestFile(t, projectPath)
	machineBefore, err := reviewv2.ParseMachineLedger(projectBefore)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	noop, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(noop.Operations) != 0 || len(noop.Machine.Operations) != 0 {
		t.Fatalf("noop=%+v err=%v", noop, err)
	}
	if got := readDerivedTestFile(t, projectPath); !bytes.Equal(got, projectBefore) {
		t.Fatal("no-op real sync churned canonical machine bytes")
	}
	const canary = "VAULT-MACHINE-TAMPER-CANDIDATE"
	if err := os.WriteFile(vaultPath, []byte(canary), 0o600); err != nil {
		t.Fatal(err)
	}

	blocked, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || blocked.Machine.State != MachineBlocked || len(blocked.Errors) != 1 || blocked.Errors[0].Code != "machine_ledger_modified" {
		t.Fatalf("blocked=%+v err=%v", blocked, err)
	}
	if got := readDerivedTestFile(t, vaultPath); string(got) != canary {
		t.Fatalf("normal sync overwrote tampered vault ledger: %q", got)
	}
	if got := readDerivedTestFile(t, projectPath); !bytes.Equal(got, projectBefore) {
		t.Fatal("blocked normal sync churned project ledger")
	}

	now = now.Add(time.Hour)
	repaired, err := engine.RepairMachineLedger(context.Background())
	if err != nil || repaired.State != MachineCurrent {
		t.Fatalf("repair=%+v err=%v", repaired, err)
	}
	projectAfter := readDerivedTestFile(t, projectPath)
	vaultAfter := readDerivedTestFile(t, vaultPath)
	if !bytes.Equal(projectAfter, vaultAfter) || !bytes.Equal(projectAfter, projectBefore) {
		t.Fatal("repair did not copy the unchanged Project canonical bytes to Vault")
	}
	parsed, err := reviewv2.ParseMachineLedger(projectAfter)
	if err != nil || parsed.LastSuccessfulSync != machineBefore.LastSuccessfulSync {
		t.Fatalf("machine=%+v err=%v", parsed, err)
	}
	visible := fmt.Sprintf("%+v %v", blocked, err)
	if strings.Contains(visible, canary) || strings.Contains(visible, fixture.project) || strings.Contains(visible, fixture.vault) {
		t.Fatalf("public diagnostics leaked candidate or absolute root: %q", visible)
	}
}

func TestMachineLedgerPublicationRecoversExactBytesAfterVaultCrash(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	engine.writer.beforeWrite = func(side Side, _ *os.Root, leaf string) error {
		if side == SideVault && leaf == "ledger.json" {
			return errors.New("injected machine vault crash")
		}
		return nil
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err == nil {
		t.Fatal("machine publication crash was ignored")
	}
	transactions, err := engine.transactions.List()
	if err != nil || len(transactions) != 1 || transactions[0].Kind != TxnMachinePublish || transactions[0].Stage != TxnProjectWritten {
		t.Fatalf("transactions=%+v err=%v", transactions, err)
	}
	projectPath := filepath.Join(fixture.project, filepath.FromSlash(reviewv2.MachineLedgerRelativePath))
	interruptedBytes := readDerivedTestFile(t, projectPath)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	recovered, err := restarted.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || recovered.Machine.State != MachineCurrent {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	vaultPath := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), ".session-reviewer", "ledger.json")
	if vaultBytes := readDerivedTestFile(t, vaultPath); !bytes.Equal(vaultBytes, interruptedBytes) {
		t.Fatal("recovery regenerated different machine bytes")
	}
	if transactions, err := restarted.transactions.List(); err != nil || len(transactions) != 0 {
		t.Fatalf("transaction remained after recovery: %+v err=%v", transactions, err)
	}
	if _, err := reviewv2.Load(fixture.project); err != nil {
		t.Fatalf("recovered project boundary invalid: %v", err)
	}
}

func TestMachineLedgerReplansAfterProjectWriteCrashAndRecoveredHumanCommit(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{
		filepath.Join(fixture.project, "docs", "session-review", "项目历史.md"),
		filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目历史.md"),
	} {
		body := readDerivedTestFile(t, filename)
		body = bytes.Replace(body, []byte("信任链与 dry-run 边界修复"), []byte("recovered human commit"), 1)
		if err := os.WriteFile(filename, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	engine.writer.beforeWrite = func(side Side, _ *os.Root, leaf string) error {
		if side == SideProject && leaf == "ledger.json" {
			return errors.New("injected machine project crash")
		}
		return nil
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err == nil {
		t.Fatal("machine Project crash was ignored")
	}
	transactions, err := engine.transactions.List()
	if err != nil || len(transactions) != 1 || transactions[0].Kind != TxnMachinePublish || transactions[0].Stage != TxnPlanned {
		t.Fatalf("transactions=%+v err=%v", transactions, err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	recovered, err := restarted.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || recovered.Machine.State != MachineCurrent || len(recovered.Machine.Operations) != 2 {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	if _, err := reviewv2.Load(fixture.project); err != nil {
		t.Fatalf("recovered machine ledger stayed stale: %v", err)
	}
}

func TestRepairMachineLedgerCASNeverOverwritesConcurrentVaultReplacement(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	options := fixture.options()
	engine, err := NewEngine(options)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	vaultPath := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), ".session-reviewer", "ledger.json")
	if err := os.WriteFile(vaultPath, []byte("tampered before repair"), 0o600); err != nil {
		t.Fatal(err)
	}
	concurrent := []byte("concurrent replacement must survive")
	engine.writer.beforeWrite = func(side Side, parent *os.Root, leaf string) error {
		if side != SideVault || leaf != "ledger.json" {
			return nil
		}
		file, err := parent.OpenFile(leaf, os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if _, err := file.Write(concurrent); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	}
	if _, err := engine.RepairMachineLedger(context.Background()); err == nil {
		t.Fatal("repair overwrote a concurrent replacement")
	}
	if got := readDerivedTestFile(t, vaultPath); !bytes.Equal(got, concurrent) {
		t.Fatalf("concurrent replacement changed: %q", got)
	}
	engine.writer.beforeWrite = nil
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err == nil || !strings.Contains(err.Error(), "transaction recovery failed") {
		t.Fatalf("recovery accepted concurrent machine replacement: %v", err)
	}
	if got := readDerivedTestFile(t, vaultPath); !bytes.Equal(got, concurrent) {
		t.Fatalf("recovery overwrote concurrent replacement: %q", got)
	}
}

func TestV2ReconcileNeverPublishesLegacyNavigationArtifacts(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || report.Derived.State != DerivedCurrent || report.Derived.Files != 0 || len(report.Derived.Operations) != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	for _, root := range []string{
		filepath.Join(fixture.project, "docs", "session-review"),
		filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath)),
	} {
		for _, relative := range []string{"decisions/00-目录说明.md", "open-loops/00-目录说明.md", "sessions/00-目录说明.md", "diagrams/project-evolution.md"} {
			if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative))); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("legacy navigation artifact exists: %s err=%v", relative, err)
			}
		}
	}
}

func TestDerivedPlanningRejectsRedirectedCollectionBeforeWriting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not a portable unprivileged Windows fixture")
	}
	fixture := newEngineFixture(t)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	outside := t.TempDir()
	canary := filepath.Join(outside, "canary.md")
	if err := os.WriteFile(canary, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(fixture.project, "docs", "session-review", "decisions")); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.planDerived(); err == nil {
		t.Fatal("redirected derived collection was accepted")
	}
	if got := readDerivedTestFile(t, canary); string(got) != "outside\n" {
		t.Fatalf("outside target changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(outside, "00-目录说明.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("derived index escaped through redirect: %v", err)
	}
}

func readDerivedTestFile(t *testing.T, filename string) []byte {
	t.Helper()
	body, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
