package sync

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDerivedPublishRecoversAfterVaultWriteFailure(t *testing.T) {
	fixture := newEngineFixture(t)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	vaultOverview := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "project-overview.md")
	canonical := readDerivedTestFile(t, vaultOverview)
	if err := os.WriteFile(vaultOverview, bytes.Replace(canonical, []byte("### 项目总览"), []byte("### interrupted"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	engine.writer.beforeWrite = func(side Side, _ *os.Root, _ string) error {
		if side == SideVault {
			return errors.New("injected derived vault failure")
		}
		return nil
	}
	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err == nil || report.Derived.State != DerivedFailed {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	transactions, listErr := engine.transactions.List()
	if listErr != nil || len(transactions) != 1 || transactions[0].Kind != TxnDerivedPublish || transactions[0].Stage != TxnProjectWritten {
		t.Fatalf("transactions=%+v err=%v", transactions, listErr)
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
	if err != nil || recovered.Derived.State != DerivedCurrent {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	if got := readDerivedTestFile(t, vaultOverview); !bytes.Equal(got, canonical) {
		t.Fatalf("recovered bytes differ:\n%s", got)
	}
	transactions, err = restarted.transactions.List()
	if err != nil || len(transactions) != 0 {
		t.Fatalf("transactions remain=%+v err=%v", transactions, err)
	}
}

func TestDerivedPublishConvergesProjectVaultAndBaseAndThenIsNoop(t *testing.T) {
	fixture := newEngineFixture(t)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil {
		_, planErr := engine.planDerived()
		t.Fatalf("reconcile=%v plan=%v", err, planErr)
	}
	if report.Derived.State != DerivedCurrent || report.Derived.Files < 5 {
		t.Fatalf("derived report=%+v", report.Derived)
	}
	for _, relative := range []string{
		"project-overview.md",
		"decisions/00-目录说明.md",
		"open-loops/00-目录说明.md",
		"sessions/00-目录说明.md",
		"diagrams/project-evolution.md",
	} {
		project := readDerivedTestFile(t, filepath.Join(fixture.project, "docs", "session-review", filepath.FromSlash(relative)))
		vault := readDerivedTestFile(t, filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), filepath.FromSlash(relative)))
		if !bytes.Equal(project, vault) {
			t.Fatalf("derived bytes diverged for %s", relative)
		}
	}
	overview := readDerivedTestFile(t, filepath.Join(fixture.project, "docs", "session-review", "project-overview.md"))
	if !bytes.Contains(overview, []byte("## 项目导航")) || !bytes.Contains(overview, []byte("flowchart LR")) {
		t.Fatalf("homepage navigation missing:\n%s", overview)
	}
	base, found, err := engine.bases.Load("project-overview")
	if err != nil || !found || !bytes.Equal(base.Content, overview) {
		t.Fatalf("canonical base found=%t err=%v", found, err)
	}

	repeat, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || repeat.Derived.State != DerivedCurrent || len(repeat.Operations) != 0 {
		t.Fatalf("repeat report=%+v err=%v", repeat, err)
	}
}

func TestDerivedOnlyVaultEditIsRestoredWithoutRevisionIncrement(t *testing.T) {
	fixture := newEngineFixture(t)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		_, planErr := engine.planDerived()
		t.Fatalf("reconcile=%v plan=%v", err, planErr)
	}
	vaultOverview := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "project-overview.md")
	before := readDerivedTestFile(t, vaultOverview)
	edited := strings.Replace(string(before), "### 项目总览", "### 手工篡改生成区", 1)
	if err := os.WriteFile(vaultOverview, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || report.Derived.State != DerivedCurrent {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	after := readDerivedTestFile(t, vaultOverview)
	if !bytes.Equal(before, after) || bytes.Contains(after, []byte("手工篡改")) || !bytes.Contains(after, []byte("revision: 1")) {
		t.Fatalf("generated edit was not restored without revision change:\n%s", after)
	}
}

func TestDerivedPublishDoesNotOverwriteConcurrentVaultEdit(t *testing.T) {
	fixture := newEngineFixture(t)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	vaultOverview := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "project-overview.md")
	canonical := readDerivedTestFile(t, vaultOverview)
	if err := os.WriteFile(vaultOverview, bytes.Replace(canonical, []byte("### 项目总览"), []byte("### stale generated edit"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	concurrent := append(bytes.Clone(canonical), []byte("\n## Concurrent Obsidian note\n\nKeep this edit.\n")...)
	engine.writer.beforeWrite = func(side Side, parent *os.Root, leaf string) error {
		if side != SideVault || leaf != "project-overview.md" {
			return nil
		}
		file, err := parent.OpenFile(leaf, os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := file.Write(concurrent); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	}
	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err == nil || report.Derived.State != DerivedFailed {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if got := readDerivedTestFile(t, vaultOverview); !bytes.Equal(got, concurrent) {
		t.Fatalf("concurrent edit was overwritten: %q", got)
	}
	engine.writer.beforeWrite = nil
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err == nil || !strings.Contains(err.Error(), "transaction recovery failed") {
		t.Fatalf("recovery accepted a concurrent semantic edit: %v", err)
	}
	if got := readDerivedTestFile(t, vaultOverview); !bytes.Equal(got, concurrent) {
		t.Fatalf("recovery overwrote the concurrent semantic edit: %q", got)
	}
}

func TestDerivedRecoveryRejectsAcceptedLedgerManifestChange(t *testing.T) {
	fixture := newEngineFixture(t)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	vaultOverview := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "project-overview.md")
	canonical := readDerivedTestFile(t, vaultOverview)
	if err := os.WriteFile(vaultOverview, bytes.Replace(canonical, []byte("### 项目总览"), []byte("### interrupted"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	engine.writer.beforeWrite = func(side Side, _ *os.Root, _ string) error {
		if side == SideVault {
			return errors.New("stop after project publication")
		}
		return nil
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err == nil {
		t.Fatal("derived publication was not interrupted")
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	projectOverview := filepath.Join(fixture.project, "docs", "session-review", "project-overview.md")
	project := readDerivedTestFile(t, projectOverview)
	project = append(project, []byte("\n## User note\n\nchanged after the journal was written\n")...)
	if err := os.WriteFile(projectOverview, project, 0o644); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if _, err := restarted.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err == nil || !strings.Contains(err.Error(), "transaction recovery failed") {
		t.Fatalf("manifest change did not fail closed: %v", err)
	}
	transactions, err := restarted.transactions.List()
	if err != nil || len(transactions) != 1 || transactions[0].Kind != TxnDerivedPublish {
		t.Fatalf("recovery journal was not preserved: %+v err=%v", transactions, err)
	}
}

func TestDerivedPublishProjectFailureKeepsPlannedJournal(t *testing.T) {
	fixture := newEngineFixture(t)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	projectOverview := filepath.Join(fixture.project, "docs", "session-review", "project-overview.md")
	canonical := readDerivedTestFile(t, projectOverview)
	if err := os.WriteFile(projectOverview, bytes.Replace(canonical, []byte("### 项目总览"), []byte("### stale generated edit"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	engine.writer.beforeWrite = func(side Side, _ *os.Root, _ string) error {
		if side == SideProject {
			return errors.New("injected project failure")
		}
		return nil
	}
	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err == nil || report.Derived.State != DerivedFailed {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	transactions, err := engine.transactions.List()
	if err != nil || len(transactions) != 1 || transactions[0].Stage != TxnPlanned {
		t.Fatalf("transactions=%+v err=%v", transactions, err)
	}
	if got := readDerivedTestFile(t, projectOverview); !bytes.Contains(got, []byte("stale generated edit")) {
		t.Fatalf("failed project write changed the target:\n%s", got)
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
