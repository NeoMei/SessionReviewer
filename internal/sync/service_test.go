package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
)

func TestEngineRecoversInterruptedTwoSideWriteFromContentFreeJournal(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	vaultHistory := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目历史.md")
	body, err := os.ReadFile(vaultHistory)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(body), "信任链与 dry-run 边界修复", "interrupted edit", 1)
	if err := os.WriteFile(vaultHistory, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	engine.writer.beforeWrite = func(side Side, _ *os.Root, _ string) error {
		if side == SideVault {
			return errors.New("injected vault write failure")
		}
		return nil
	}
	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(report.Errors) != 1 {
		t.Fatalf("interrupted report=%+v err=%v", report, err)
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
	if err != nil || len(recovered.Errors) != 0 || len(recovered.Conflicts) != 0 {
		t.Fatalf("recovered report=%+v err=%v", recovered, err)
	}
	projectBody, _ := os.ReadFile(filepath.Join(fixture.project, "docs", "session-review", "项目历史.md"))
	vaultBody, _ := os.ReadFile(vaultHistory)
	if !strings.Contains(string(projectBody), "interrupted edit") || !strings.Contains(string(projectBody), "revision: 2") || string(projectBody) != string(vaultBody) {
		t.Fatalf("project=%s\nvault=%s", projectBody, vaultBody)
	}
}

func TestReconcileDryRunPlansMigrationAndRealSyncConvergesV2(t *testing.T) {
	fixture := newEngineFixture(t)
	completeLegacyFixtureForMigration(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	beforeProject := snapshotFixtureTree(t, fixture.project)
	beforeData := snapshotFixtureTree(t, fixture.data)
	beforeVault := snapshotFixtureTree(t, fixture.vault)
	dry, err := engine.Reconcile(context.Background(), ReconcileRequest{DryRun: true, Trigger: TriggerCLI})
	if err != nil {
		_, planErr := reviewv2.PlanMigration(fixture.project, engine.project.Info(), fixture.data, fixture.options().Now())
		t.Fatalf("reconcile=%v direct_plan=%v", err, planErr)
	}
	if !dry.Migration.Required || !dry.Migration.DryRun || len(dry.Migration.Creates) != 3 || len(dry.Migration.Archives) == 0 {
		t.Fatalf("migration dry-run report=%+v", dry.Migration)
	}
	if after := snapshotFixtureTree(t, fixture.project); !reflect.DeepEqual(beforeProject, after) {
		t.Fatalf("migration dry-run mutated project\nbefore=%v\nafter=%v", beforeProject, after)
	}
	if !reflect.DeepEqual(beforeData, snapshotFixtureTree(t, fixture.data)) || !reflect.DeepEqual(beforeVault, snapshotFixtureTree(t, fixture.vault)) {
		t.Fatal("migration dry-run mutated sync data or Vault")
	}

	real, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if real.Migration.Required || real.Migration.DryRun || real.Machine.State != MachineCurrent {
		t.Fatalf("real report=%+v", real)
	}
	projectMachine := readDerivedTestFile(t, filepath.Join(fixture.project, filepath.FromSlash(reviewv2.MachineLedgerRelativePath)))
	vaultMachine := readDerivedTestFile(t, filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), ".session-reviewer", "ledger.json"))
	if !bytes.Equal(projectMachine, vaultMachine) {
		t.Fatal("project and vault machine ledgers diverged")
	}

	repeat, err := engine.Reconcile(context.Background(), ReconcileRequest{DryRun: true, Trigger: TriggerCLI})
	if err != nil || len(repeat.Operations) != 0 || len(repeat.Machine.Operations) != 0 {
		t.Fatalf("repeat=%+v err=%v", repeat, err)
	}
}

func TestReconcileMigrationRetiresMirroredLegacyVaultBeforeCompactSync(t *testing.T) {
	fixture := newEngineFixture(t)
	completeLegacyFixtureForMigration(t, fixture)
	copyLegacyReviewToVault(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Conflicts) != 0 || len(report.Errors) != 0 || report.Machine.State != MachineCurrent {
		t.Fatalf("migration replayed legacy Vault state: %+v", report)
	}
	projectInventory := syncdoc.Scan(engine.project, "docs/session-review", "darwin", platform.CaseSensitive)
	vaultInventory, _, err := engine.scanVault()
	if err != nil {
		t.Fatal(err)
	}
	if !compactV2Inventory(projectInventory, "docs/session-review") || !compactV2Inventory(vaultInventory, engine.options.VaultReviewPath) {
		t.Fatalf("project=%+v vault=%+v", projectInventory, vaultInventory)
	}
	for _, root := range []string{
		filepath.Join(fixture.project, "docs", "session-review"),
		filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath)),
	} {
		if _, err := os.Stat(filepath.Join(root, "project-overview.md")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy overview survived compact migration at %s: %v", root, err)
		}
	}
}

func TestReconcileMigrationRejectsDivergedLegacyVaultBeforeAnyWrite(t *testing.T) {
	fixture := newEngineFixture(t)
	completeLegacyFixtureForMigration(t, fixture)
	copyLegacyReviewToVault(t, fixture)
	vaultOverview := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "project-overview.md")
	body, err := os.ReadFile(vaultOverview)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultOverview, append(body, []byte("\nVault-only edit\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	beforeProject := snapshotFixtureTree(t, fixture.project)
	beforeVault := snapshotFixtureTree(t, fixture.vault)
	beforeData := snapshotFixtureTree(t, fixture.data)

	for _, request := range []ReconcileRequest{
		{DryRun: true, Trigger: TriggerCLI},
		{Trigger: TriggerCLI},
	} {
		if _, err := engine.Reconcile(context.Background(), request); err == nil {
			t.Fatalf("diverged legacy Vault accepted for request %+v", request)
		}
		if !reflect.DeepEqual(beforeProject, snapshotFixtureTree(t, fixture.project)) ||
			!reflect.DeepEqual(beforeVault, snapshotFixtureTree(t, fixture.vault)) ||
			!reflect.DeepEqual(beforeData, snapshotFixtureTree(t, fixture.data)) {
			t.Fatalf("failed migration preflight mutated state for request %+v", request)
		}
	}
}

func TestReconcileMigrationAcceptsProjectChangeWhenLegacyVaultStillMatchesBase(t *testing.T) {
	fixture := newEngineFixture(t)
	completeLegacyFixtureForMigration(t, fixture)
	copyLegacyReviewToVault(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	overviewRelative := "project-overview.md"
	projectOverview := filepath.Join(fixture.project, "docs", "session-review", overviewRelative)
	baseBody, err := os.ReadFile(projectOverview)
	if err != nil {
		t.Fatal(err)
	}
	hash := syncdoc.ContentHash(baseBody)
	if err := engine.bases.Commit("", BaseRecord{
		Version: 1, EntityID: "project-overview", RelativePath: overviewRelative,
		ContentHash: hash, ProjectHash: hash, VaultHash: hash, Content: baseBody, SyncedAt: fixture.options().Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectOverview, append(baseBody, []byte("\nProject accepted update\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	projectSessionIndex := filepath.Join(fixture.project, "docs", "session-review", "sessions", "00-目录说明.md")
	indexBody, err := os.ReadFile(projectSessionIndex)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectSessionIndex, append(indexBody, []byte("\n- regenerated entry\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil {
		t.Fatalf("unchanged legacy Vault blocked accepted Project migration: %v", err)
	}
	if len(report.Conflicts) != 0 || len(report.Errors) != 0 || report.Machine.State != MachineCurrent {
		t.Fatalf("report=%+v", report)
	}
}

func TestReconcileFailsClosedOnEveryLegacyEntityTransactionStageBeforeNewMigration(t *testing.T) {
	for _, stage := range []TransactionStage{TxnPlanned, TxnProjectWritten, TxnVaultWritten, TxnBaseCommitted} {
		t.Run(string(stage), func(t *testing.T) {
			fixture := newEngineFixture(t)
			completeLegacyFixtureForMigration(t, fixture)
			engine, err := NewEngine(fixture.options())
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			stageLegacyOverviewTransaction(t, fixture, engine, stage)
			beforeProject := snapshotFixtureTree(t, fixture.project)
			beforeData := snapshotFixtureTree(t, fixture.data)
			beforeVault := snapshotFixtureTree(t, fixture.vault)

			for attempt := 0; attempt < 2; attempt++ {
				if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err == nil || !strings.Contains(err.Error(), "legacy sync transaction blocks migration") {
					t.Fatalf("attempt=%d err=%v", attempt, err)
				}
				if version, err := reviewv2.DetectVersionExpected(fixture.project, engine.project.Info()); err != nil || version != reviewv2.VersionLegacy {
					t.Fatalf("attempt=%d version=%s err=%v", attempt, version, err)
				}
				transactions, err := engine.transactions.List()
				if err != nil || len(transactions) != 1 || transactions[0].Stage != stage {
					t.Fatalf("attempt=%d transactions=%+v err=%v", attempt, transactions, err)
				}
				if !reflect.DeepEqual(beforeProject, snapshotFixtureTree(t, fixture.project)) ||
					!reflect.DeepEqual(beforeData, snapshotFixtureTree(t, fixture.data)) ||
					!reflect.DeepEqual(beforeVault, snapshotFixtureTree(t, fixture.vault)) {
					t.Fatalf("attempt=%d mutated state before fail-closed return", attempt)
				}
			}
		})
	}
}

func TestReconcileNeverReplaysLegacyEntityBytesAfterMigrationJournalRecovery(t *testing.T) {
	fixture := newEngineFixture(t)
	completeLegacyFixtureForMigration(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	stageLegacyOverviewTransaction(t, fixture, engine, TxnVaultWritten)
	_, _ = stageRecoverableMigrationJournal(t, fixture, engine)

	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err == nil || !strings.Contains(err.Error(), "legacy sync transaction cannot be recovered into review v2") {
		t.Fatalf("err=%v", err)
	}
	if version, err := reviewv2.DetectVersionExpected(fixture.project, engine.project.Info()); err != nil || version != reviewv2.VersionV2 {
		t.Fatalf("version=%s err=%v", version, err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.project, "docs", "session-review", "project-overview.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy bytes were replayed into migrated Project: %v", err)
	}
	beforeRetry := snapshotFixtureTree(t, fixture.project)
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err == nil || !strings.Contains(err.Error(), "legacy sync transaction cannot be recovered into review v2") {
		t.Fatalf("retry err=%v", err)
	}
	if !reflect.DeepEqual(beforeRetry, snapshotFixtureTree(t, fixture.project)) {
		t.Fatal("retry mutated Project after fail-closed recovery")
	}
}

func TestV2RecoveryAuthenticationRejectsNestedCompactBasenames(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for _, tc := range []struct {
		entityID, canonical, nested string
	}{
		{entityID: "project-overview", canonical: "项目回顾.md", nested: "nested/项目回顾.md"},
		{entityID: "project-history", canonical: "项目历史.md", nested: "archive/项目历史.md"},
	} {
		body := readDerivedTestFile(t, filepath.Join(fixture.project, "docs", "session-review", tc.canonical))
		if !engine.validCompactV2RecoveryBody(tc.entityID, tc.canonical, body) {
			t.Fatalf("canonical %s was rejected", tc.canonical)
		}
		if engine.validCompactV2RecoveryBody(tc.entityID, tc.nested, body) {
			t.Fatalf("nested same-basename path %s was accepted", tc.nested)
		}
	}
}

func stageLegacyOverviewTransaction(t *testing.T, fixture engineFixture, engine *Engine, stage TransactionStage) {
	t.Helper()
	relative := "project-overview.md"
	projectPath := filepath.Join(fixture.project, "docs", "session-review", relative)
	body := readDerivedTestFile(t, projectPath)
	hash := syncdoc.ContentHash(body)
	transaction := Transaction{
		Version: 1, Kind: TxnEntitySync, EntityID: "project-overview", DesiredHash: hash,
		ExpectedProjectHash: hash, Stage: TxnPlanned, UpdatedAt: fixture.options().Now(),
	}
	if err := engine.transactions.Save(transaction); err != nil {
		t.Fatal(err)
	}
	if stage == TxnPlanned {
		return
	}
	transaction.Stage = TxnProjectWritten
	if err := engine.transactions.Save(transaction); err != nil {
		t.Fatal(err)
	}
	if stage == TxnProjectWritten {
		return
	}
	if _, err := engine.bindReviewTarget(true); err != nil {
		t.Fatal(err)
	}
	if err := engine.writer.Write(context.Background(), SideVault, relative, body, 0o644); err != nil {
		t.Fatal(err)
	}
	transaction.Stage = TxnVaultWritten
	if err := engine.transactions.Save(transaction); err != nil {
		t.Fatal(err)
	}
	if stage == TxnVaultWritten {
		return
	}
	if err := engine.bases.Commit("", BaseRecord{
		Version: 1, EntityID: "project-overview", RelativePath: relative,
		ContentHash: hash, ProjectHash: hash, VaultHash: hash, Content: body, SyncedAt: fixture.options().Now(),
	}); err != nil {
		t.Fatal(err)
	}
	transaction.Stage = TxnBaseCommitted
	if err := engine.transactions.Save(transaction); err != nil {
		t.Fatal(err)
	}
}

func completeLegacyFixtureForMigration(t *testing.T, fixture engineFixture) {
	t.Helper()
	legacy, err := ledger.Load(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	current := ledger.CurrentState{
		ProjectID: legacy.ProjectID, Revision: 1, Goal: "migrate sync fixture", LastVerified: "legacy accepted",
		Branch: "codex/session-reviewer-v2", Blockers: []string{}, OpenRisks: []string{}, NextAction: "sync",
		FirstInspection: "docs/session-review/project-overview.md", LastUpdated: "2026-08-25T00:00:00Z",
		SourceSessions: []string{}, Evidence: []ledger.EvidenceRef{},
	}
	plan, err := ledger.Render(legacy, ledger.ChangeSet{Current: &current})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(plan); err != nil {
		t.Fatal(err)
	}
}

func copyLegacyReviewToVault(t *testing.T, fixture engineFixture) {
	t.Helper()
	projectReview := filepath.Join(fixture.project, "docs", "session-review")
	vaultReview := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath))
	err := filepath.Walk(projectReview, func(source string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(projectReview, source)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(vaultReview, relative)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		body, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, body, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReconcileBackgroundTriggersOnlyReportRequiredMigration(t *testing.T) {
	for _, trigger := range []Trigger{TriggerWatcher, TriggerPeriodic, TriggerQueue} {
		t.Run(string(trigger), func(t *testing.T) {
			fixture := newEngineFixture(t)
			completeLegacyFixtureForMigration(t, fixture)
			engine, err := NewEngine(fixture.options())
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			beforeProject := snapshotFixtureTree(t, fixture.project)
			beforeData := snapshotFixtureTree(t, fixture.data)
			beforeVault := snapshotFixtureTree(t, fixture.vault)

			report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: trigger})
			if err != nil {
				t.Fatal(err)
			}
			if !report.Migration.Required || report.Migration.DryRun || report.Machine.State != MachinePending {
				t.Fatalf("report=%+v", report)
			}
			if !reflect.DeepEqual(beforeProject, snapshotFixtureTree(t, fixture.project)) ||
				!reflect.DeepEqual(beforeData, snapshotFixtureTree(t, fixture.data)) ||
				!reflect.DeepEqual(beforeVault, snapshotFixtureTree(t, fixture.vault)) {
				t.Fatal("background migration report mutated state")
			}
		})
	}
}

func TestReconcileBackgroundTriggersDoNotRecoverInterruptedMigration(t *testing.T) {
	for _, trigger := range []Trigger{TriggerWatcher, TriggerPeriodic, TriggerQueue} {
		t.Run(string(trigger), func(t *testing.T) {
			fixture := newEngineFixture(t)
			completeLegacyFixtureForMigration(t, fixture)
			engine, err := NewEngine(fixture.options())
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			_, _ = stageRecoverableMigrationJournal(t, fixture, engine)
			beforeProject := snapshotFixtureTree(t, fixture.project)
			beforeData := snapshotFixtureTree(t, fixture.data)
			beforeVault := snapshotFixtureTree(t, fixture.vault)

			report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: trigger})
			if err != nil {
				t.Fatal(err)
			}
			if !report.Migration.Required || report.Machine.State != MachinePending {
				t.Fatalf("report=%+v", report)
			}
			if !reflect.DeepEqual(beforeProject, snapshotFixtureTree(t, fixture.project)) ||
				!reflect.DeepEqual(beforeData, snapshotFixtureTree(t, fixture.data)) ||
				!reflect.DeepEqual(beforeVault, snapshotFixtureTree(t, fixture.vault)) {
				t.Fatal("background trigger recovered an interrupted migration")
			}
		})
	}
}

func TestReconcileBackgroundTriggersDoNotFinalizeCommittedMigrationJournal(t *testing.T) {
	for _, trigger := range []Trigger{TriggerWatcher, TriggerPeriodic, TriggerQueue} {
		t.Run(string(trigger), func(t *testing.T) {
			fixture := newEngineFixture(t)
			completeLegacyFixtureForMigration(t, fixture)
			engine, err := NewEngine(fixture.options())
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			journalPath, journalBody := stageRecoverableMigrationJournal(t, fixture, engine)
			if err := reviewv2.RecoverMigration(fixture.project, engine.project.Info(), fixture.data); err != nil {
				t.Fatal(err)
			}
			var journal map[string]any
			if err := json.Unmarshal(journalBody, &journal); err != nil {
				t.Fatal(err)
			}
			journal["stage"] = "committed"
			committed, err := json.MarshalIndent(journal, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			committed = append(committed, '\n')
			if err := os.WriteFile(journalPath, committed, 0o600); err != nil {
				t.Fatal(err)
			}
			hardenMigrationTestPath(t, journalPath)
			pending, err := reviewv2.MigrationPending(fixture.project, engine.project.Info(), fixture.data)
			if err != nil || !pending {
				t.Fatalf("late journal pending=%v err=%v", pending, err)
			}
			beforeProject := snapshotFixtureTree(t, fixture.project)
			beforeData := snapshotFixtureTree(t, fixture.data)
			beforeVault := snapshotFixtureTree(t, fixture.vault)

			report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: trigger})
			if err != nil || !report.Migration.Required || report.Machine.State != MachinePending {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			if !reflect.DeepEqual(beforeProject, snapshotFixtureTree(t, fixture.project)) ||
				!reflect.DeepEqual(beforeData, snapshotFixtureTree(t, fixture.data)) ||
				!reflect.DeepEqual(beforeVault, snapshotFixtureTree(t, fixture.vault)) {
				t.Fatal("background trigger finalized a committed migration journal")
			}
		})
	}
}

func stageRecoverableMigrationJournal(t *testing.T, fixture engineFixture, engine *Engine) (string, []byte) {
	t.Helper()
	machineDirectory := filepath.Join(fixture.project, "docs", "session-review", ".session-reviewer")
	if err := os.Mkdir(machineDirectory, 0o500); err != nil {
		t.Fatal(err)
	}
	plan, err := reviewv2.PlanMigration(fixture.project, engine.project.Info(), fixture.data, fixture.options().Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviewv2.ApplyMigration(plan); err == nil {
		t.Fatal("migration unexpectedly passed the staged private-directory failure")
	}
	entries, err := os.ReadDir(filepath.Join(fixture.data, "migrations"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("migration journal entries=%v err=%v", entries, err)
	}
	journalPath := filepath.Join(fixture.data, "migrations", entries[0].Name())
	journalBody := readDerivedTestFile(t, journalPath)
	if err := os.Chmod(machineDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	hardenMigrationTestPath(t, machineDirectory)
	return journalPath, journalBody
}

func TestSelectedEntityReconcileDoesNotCanonizeUnselectedProjectEdit(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	machinePath := filepath.Join(fixture.project, filepath.FromSlash(reviewv2.MachineLedgerRelativePath))
	machineBefore := readDerivedTestFile(t, machinePath)
	overviewPath := filepath.Join(fixture.project, "docs", "session-review", "项目回顾.md")
	overview := readDerivedTestFile(t, overviewPath)
	edited := bytes.Replace(overview, []byte("SessionReviewer v2"), []byte("unselected Project edit"), 1)
	if bytes.Equal(overview, edited) {
		t.Fatal("overview fixture marker was not found")
	}
	if err := os.WriteFile(overviewPath, edited, 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := engine.Reconcile(context.Background(), ReconcileRequest{
		Trigger: TriggerCLI, EntityIDs: []string{"project-history"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := readDerivedTestFile(t, machinePath); !bytes.Equal(got, machineBefore) {
		t.Fatal("selected reconcile canonized an unselected Project edit")
	}
	if report.Machine.State != MachinePending || len(report.Machine.Operations) != 0 {
		t.Fatalf("machine report=%+v", report.Machine)
	}
}

func TestV2EngineResolveUsesCompactFinalizationForEveryAction(t *testing.T) {
	for _, tc := range []struct {
		name, title string
		action      ResolutionAction
	}{
		{name: "project", title: "project 标题", action: AcceptProject},
		{name: "vault", title: "vault 标题", action: AcceptObsidian},
		{name: "manual", title: "manual 标题", action: ManualMerge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newEngineFixture(t)
			writeV2EngineFixture(t, fixture)
			engine, err := NewEngine(fixture.options())
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			if report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil || len(report.Errors) != 0 {
				t.Fatalf("initial report=%+v err=%v", report, err)
			}
			projectPath := filepath.Join(fixture.project, "docs", "session-review", "项目历史.md")
			vaultPath := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目历史.md")
			projectBody, _ := os.ReadFile(projectPath)
			vaultBody, _ := os.ReadFile(vaultPath)
			projectBody = bytes.Replace(projectBody, []byte("信任链与 dry-run 边界修复"), []byte("project 标题"), 1)
			vaultBody = bytes.Replace(vaultBody, []byte("信任链与 dry-run 边界修复"), []byte("vault 标题"), 1)
			if err := os.WriteFile(projectPath, projectBody, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(vaultPath, vaultBody, 0o644); err != nil {
				t.Fatal(err)
			}
			conflicted, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
			if err != nil || len(conflicted.Conflicts) != 1 {
				t.Fatalf("conflicted=%+v err=%v", conflicted, err)
			}
			resolution := Resolution{ConflictID: conflicted.Conflicts[0], Action: tc.action}
			if tc.action == ManualMerge {
				manual := bytes.Replace(renderDocument(t, v2HistoryWithTwoEvents(t)), []byte("project-0123456789abcdef"), []byte(fixture.projectID), 1)
				manual = bytes.Replace(manual, []byte("信任链与 dry-run 边界修复"), []byte("manual 标题"), 1)
				manualPath := filepath.Join(fixture.root, "manual.md")
				if err := os.WriteFile(manualPath, manual, 0o600); err != nil {
					t.Fatal(err)
				}
				resolution.ManualFile = manualPath
			}
			resolved, err := engine.Resolve(context.Background(), resolution)
			if err != nil || len(resolved.Conflicts) != 0 {
				t.Fatalf("resolved=%+v err=%v", resolved, err)
			}
			accepted, _ := os.ReadFile(projectPath)
			if !bytes.Contains(accepted, []byte(tc.title)) {
				t.Fatalf("wrong accepted content:\n%s", accepted)
			}
			assertV2VisibleFrontmatter(t, accepted, 2)
			for _, forbidden := range []string{"sync_status:", "sync_hash:", "base_hash:", "project_hash:", "vault_hash:"} {
				if bytes.Contains(accepted, []byte(forbidden)) {
					t.Fatalf("leaked %q:\n%s", forbidden, accepted)
				}
			}
			if _, err := reviewv2.Load(fixture.project); err != nil {
				t.Fatalf("resolved project did not retain a valid machine boundary: %v", err)
			}
			projectMachine := readDerivedTestFile(t, filepath.Join(fixture.project, filepath.FromSlash(reviewv2.MachineLedgerRelativePath)))
			vaultMachine := readDerivedTestFile(t, filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), ".session-reviewer", "ledger.json"))
			if !bytes.Equal(projectMachine, vaultMachine) {
				t.Fatal("resolved machine ledger bytes diverged")
			}
			var resolvedRecordBytes []byte
			for _, filename := range []string{
				filepath.Join(fixture.project, "docs", "session-review", ".session-reviewer", "conflicts", resolution.ConflictID+".json"),
				filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), ".session-reviewer", "conflicts", resolution.ConflictID+".json"),
			} {
				body := readDerivedTestFile(t, filename)
				if resolvedRecordBytes != nil && !bytes.Equal(resolvedRecordBytes, body) {
					t.Fatal("resolved hidden conflict records diverged")
				}
				resolvedRecordBytes = body
			}
			resolvedRecord, err := ParseConflictRecord(resolvedRecordBytes)
			if err != nil || resolvedRecord.ResolutionStatus != ResolutionResolved || resolvedRecord.ResolutionAction != tc.action {
				t.Fatalf("resolved hidden record=%+v err=%v", resolvedRecord, err)
			}
			followup, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
			if err != nil || len(followup.Operations) != 0 || len(followup.Conflicts) != 0 {
				t.Fatalf("resolution did not converge: report=%+v err=%v", followup, err)
			}
		})
	}
}

func TestV2EngineResolvesMultipleHiddenConflictsWithoutAcceptingPeerImplicitly(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	type pair struct{ project, vault string }
	paths := map[string]pair{
		"project-history": {
			project: filepath.Join(fixture.project, "docs", "session-review", "项目历史.md"),
			vault:   filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目历史.md"),
		},
		"project-overview": {
			project: filepath.Join(fixture.project, "docs", "session-review", "项目回顾.md"),
			vault:   filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目回顾.md"),
		},
	}
	for id, target := range paths {
		projectBody := readDerivedTestFile(t, target.project)
		vaultBody := readDerivedTestFile(t, target.vault)
		from := []byte("信任链与 dry-run 边界修复")
		if id == "project-overview" {
			from = []byte("Skill + 本地 CLI")
		}
		if err := os.WriteFile(target.project, bytes.Replace(projectBody, from, []byte(id+" project choice"), 1), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target.vault, bytes.Replace(vaultBody, from, []byte(id+" vault choice"), 1), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	conflicted, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(conflicted.Conflicts) != 2 {
		t.Fatalf("conflicted=%+v err=%v", conflicted, err)
	}
	byEntity := make(map[string]string, 2)
	for _, id := range conflicted.Conflicts {
		for entity := range paths {
			if strings.Contains(id, entity) {
				byEntity[entity] = id
			}
		}
	}
	first, err := engine.Resolve(context.Background(), Resolution{ConflictID: byEntity["project-history"], Action: AcceptProject})
	if err != nil || !reflect.DeepEqual(first.Conflicts, []string{byEntity["project-overview"]}) || first.Machine.State != MachinePending {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if got := readDerivedTestFile(t, paths["project-overview"].vault); !bytes.Contains(got, []byte("project-overview vault choice")) {
		t.Fatal("first resolution implicitly accepted the Project peer conflict")
	}
	second, err := engine.Resolve(context.Background(), Resolution{ConflictID: byEntity["project-overview"], Action: AcceptObsidian})
	if err != nil || len(second.Conflicts) != 0 || second.Machine.State != MachineCurrent {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if _, err := reviewv2.Load(fixture.project); err != nil {
		t.Fatalf("two-step resolution did not restore a valid compact review: %v", err)
	}
}

func TestHiddenConflictRecordsAreMirroredJSONAndExcludedFromPublicSurfaces(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil || len(report.Errors) != 0 {
		t.Fatalf("initial report=%+v err=%v", report, err)
	}
	projectHistory := filepath.Join(fixture.project, "docs", "session-review", "项目历史.md")
	vaultHistory := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目历史.md")
	projectBody := readDerivedTestFile(t, projectHistory)
	vaultBody := readDerivedTestFile(t, vaultHistory)
	const projectCandidate = "PROJECT-CONFLICT-CANDIDATE"
	const vaultCandidate = "VAULT-CONFLICT-CANDIDATE"
	projectBody = bytes.Replace(projectBody, []byte("信任链与 dry-run 边界修复"), []byte(projectCandidate), 1)
	vaultBody = bytes.Replace(vaultBody, []byte("信任链与 dry-run 边界修复"), []byte(vaultCandidate), 1)
	if err := os.WriteFile(projectHistory, projectBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultHistory, vaultBody, 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(report.Conflicts) != 1 || len(report.Errors) != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	conflictID := report.Conflicts[0]
	projectRecord := filepath.Join(fixture.project, "docs", "session-review", ".session-reviewer", "conflicts", conflictID+".json")
	vaultRecord := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), ".session-reviewer", "conflicts", conflictID+".json")
	projectJSON := readDerivedTestFile(t, projectRecord)
	vaultJSON := readDerivedTestFile(t, vaultRecord)
	if !bytes.Equal(projectJSON, vaultJSON) || !json.Valid(projectJSON) {
		t.Fatal("hidden conflict record is not mirrored bounded JSON")
	}
	if !bytes.Contains(projectJSON, []byte(projectCandidate)) || !bytes.Contains(projectJSON, []byte(vaultCandidate)) {
		t.Fatal("hidden record omitted conflict candidates")
	}
	for _, visible := range []string{
		filepath.Join(fixture.project, "docs", "session-review", "sync-conflicts"),
		filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "sync-conflicts"),
	} {
		if _, err := os.Lstat(visible); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("visible conflict path exists: %s err=%v", visible, err)
		}
	}
	public := fmt.Sprintf("%+v", report)
	if strings.Contains(public, projectCandidate) || strings.Contains(public, vaultCandidate) || strings.Contains(public, fixture.project) || strings.Contains(public, fixture.vault) {
		t.Fatalf("public conflict report leaked candidate or absolute root: %q", public)
	}
	if !ignoredEventPath(".session-reviewer/conflicts/" + conflictID + ".json") {
		t.Fatal("hidden conflict event was not ignored")
	}
	inventory := syncdoc.Scan(engine.project, "docs/session-review", "darwin", platform.CaseSensitive)
	if len(inventory.Issues) != 0 || len(inventory.ByID) != 2 {
		t.Fatalf("hidden record entered ordinary inventory: %+v", inventory)
	}
	engine.options.Now = func() time.Time { return time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC) }
	status, err := engine.Status(context.Background())
	if err != nil || !reflect.DeepEqual(status.OpenConflicts, []string{conflictID}) {
		t.Fatalf("status conflict identity churned: status=%+v err=%v", status, err)
	}
	repeated, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || !reflect.DeepEqual(repeated.Conflicts, []string{conflictID}) || len(repeated.Errors) != 0 {
		t.Fatalf("repeat conflict identity churned: report=%+v err=%v", repeated, err)
	}
	entries, err := os.ReadDir(filepath.Dir(projectRecord))
	if err != nil || len(entries) != 1 {
		t.Fatalf("repeat conflict created another hidden record: entries=%v err=%v", entries, err)
	}
}

func TestHiddenConflictTransactionRecoversExactMirroredJSONAfterCrash(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	projectHistory := filepath.Join(fixture.project, "docs", "session-review", "项目历史.md")
	vaultHistory := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目历史.md")
	projectBody := bytes.Replace(readDerivedTestFile(t, projectHistory), []byte("信任链与 dry-run 边界修复"), []byte("PROJECT-HIDDEN-CRASH-CANDIDATE"), 1)
	vaultBody := bytes.Replace(readDerivedTestFile(t, vaultHistory), []byte("信任链与 dry-run 边界修复"), []byte("VAULT-HIDDEN-CRASH-CANDIDATE"), 1)
	if err := os.WriteFile(projectHistory, projectBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultHistory, vaultBody, 0o644); err != nil {
		t.Fatal(err)
	}
	engine.writer.beforeWrite = func(side Side, _ *os.Root, leaf string) error {
		if side == SideVault && strings.HasPrefix(leaf, "conflict-") && strings.HasSuffix(leaf, ".json") {
			return errors.New("injected hidden conflict vault crash")
		}
		return nil
	}
	interrupted, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(interrupted.Conflicts) != 1 || len(interrupted.Errors) != 1 || interrupted.Errors[0].Code != "conflict_record_failed" {
		t.Fatalf("interrupted=%+v err=%v", interrupted, err)
	}
	conflictID := interrupted.Conflicts[0]
	projectRecord := filepath.Join(fixture.project, "docs", "session-review", ".session-reviewer", "conflicts", conflictID+".json")
	interruptedJSON := readDerivedTestFile(t, projectRecord)
	transactions, err := engine.transactions.List()
	if err != nil || len(transactions) != 1 || transactions[0].Kind != TxnConflictRecord || transactions[0].Stage != TxnProjectWritten {
		t.Fatalf("transactions=%+v err=%v", transactions, err)
	}
	transactionFiles, err := os.ReadDir(filepath.Join(fixture.data, "transactions"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range transactionFiles {
		body, err := os.ReadFile(filepath.Join(fixture.data, "transactions", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(body, []byte("HIDDEN-CRASH-CANDIDATE")) {
			t.Fatal("transaction journal persisted conflict candidates")
		}
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
	if err != nil || len(recovered.Conflicts) != 1 || len(recovered.Errors) != 0 {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	vaultRecord := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), ".session-reviewer", "conflicts", conflictID+".json")
	if got := readDerivedTestFile(t, vaultRecord); !bytes.Equal(got, interruptedJSON) {
		t.Fatal("hidden conflict recovery regenerated different bytes")
	}
}

func TestHiddenConflictResolutionRecoversMirroredResolvedJSONAfterCrash(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	projectHistory := filepath.Join(fixture.project, "docs", "session-review", "项目历史.md")
	vaultHistory := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目历史.md")
	projectBody := bytes.Replace(readDerivedTestFile(t, projectHistory), []byte("信任链与 dry-run 边界修复"), []byte("resolution crash project"), 1)
	vaultBody := bytes.Replace(readDerivedTestFile(t, vaultHistory), []byte("信任链与 dry-run 边界修复"), []byte("resolution crash vault"), 1)
	if err := os.WriteFile(projectHistory, projectBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultHistory, vaultBody, 0o644); err != nil {
		t.Fatal(err)
	}
	conflicted, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(conflicted.Conflicts) != 1 {
		t.Fatalf("conflicted=%+v err=%v", conflicted, err)
	}
	conflictID := conflicted.Conflicts[0]
	engine.writer.beforeWrite = func(side Side, _ *os.Root, leaf string) error {
		if side == SideVault && leaf == conflictID+".json" {
			return errors.New("injected resolved conflict Vault crash")
		}
		return nil
	}
	if _, err := engine.Resolve(context.Background(), Resolution{ConflictID: conflictID, Action: AcceptProject}); err == nil {
		t.Fatal("resolved conflict publication crash was ignored")
	}
	transactions, err := engine.transactions.List()
	if err != nil || len(transactions) != 1 || transactions[0].Kind != TxnConflictResolve || transactions[0].Stage != TxnProjectWritten {
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
	if report, err := restarted.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil || len(report.Errors) != 0 {
		t.Fatalf("recovered=%+v err=%v", report, err)
	}
	var mirrored []byte
	for _, filename := range []string{
		filepath.Join(fixture.project, "docs", "session-review", ".session-reviewer", "conflicts", conflictID+".json"),
		filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), ".session-reviewer", "conflicts", conflictID+".json"),
	} {
		body := readDerivedTestFile(t, filename)
		if mirrored != nil && !bytes.Equal(mirrored, body) {
			t.Fatal("recovered resolved conflict JSON diverged")
		}
		mirrored = body
	}
	record, err := ParseConflictRecord(mirrored)
	if err != nil || record.ResolutionStatus != ResolutionResolved || record.ResolutionAction != AcceptProject {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

func TestResolvePersistsDurableClosureIntentBeforeAcceptedEntityWrite(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	projectHistory := filepath.Join(fixture.project, "docs", "session-review", "项目历史.md")
	vaultHistory := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目历史.md")
	projectBody := bytes.Replace(readDerivedTestFile(t, projectHistory), []byte("信任链与 dry-run 边界修复"), []byte("intent project"), 1)
	vaultBody := bytes.Replace(readDerivedTestFile(t, vaultHistory), []byte("信任链与 dry-run 边界修复"), []byte("intent vault"), 1)
	if err := os.WriteFile(projectHistory, projectBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultHistory, vaultBody, 0o644); err != nil {
		t.Fatal(err)
	}
	conflicted, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(conflicted.Conflicts) != 1 {
		t.Fatalf("conflicted=%+v err=%v", conflicted, err)
	}
	conflictID := conflicted.Conflicts[0]
	intentObserved := false
	engine.writer.beforeWrite = func(side Side, _ *os.Root, leaf string) error {
		if side != SideProject || leaf != "项目历史.md" {
			return nil
		}
		txn, found, loadErr := engine.transactions.Load(conflictID)
		if loadErr == nil && found && txn.Kind == TxnConflictResolve && txn.Stage == TxnPlanned {
			intentObserved = true
		}
		return errors.New("stop before accepted entity write")
	}
	if _, err := engine.Resolve(context.Background(), Resolution{ConflictID: conflictID, Action: AcceptProject}); err == nil {
		t.Fatal("injected accepted entity failure was ignored")
	}
	if !intentObserved {
		t.Fatal("conflict closure intent was not durable before accepted entity write")
	}
	engine.writer.beforeWrite = nil
	recovered, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(recovered.Conflicts) != 1 || recovered.Conflicts[0] != conflictID {
		t.Fatalf("pre-commit intent recovery=%+v err=%v", recovered, err)
	}
	transactions, err := engine.transactions.List()
	if err != nil || len(transactions) != 0 {
		t.Fatalf("aborted resolution transactions=%+v err=%v", transactions, err)
	}
}

func TestResolveRecoversMigrationBeforeConflictOrSyncTransactionWork(t *testing.T) {
	for _, stage := range []reviewv2.Stage{reviewv2.StagePlanned, reviewv2.StageV2Written, reviewv2.StageCommitted} {
		t.Run(string(stage), func(t *testing.T) {
			fixture := newEngineFixture(t)
			completeLegacyFixtureForMigration(t, fixture)
			engine, err := NewEngine(fixture.options())
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			stageMigrationJournalForResolve(t, fixture, engine, stage)
			if stage == reviewv2.StageV2Written {
				if version, err := reviewv2.DetectVersionExpected(fixture.project, engine.project.Info()); err != nil || version != reviewv2.VersionMixed {
					t.Fatalf("precondition version=%s err=%v", version, err)
				}
			}

			resolution := Resolution{ConflictID: "conflict-project-overview-0123456789ab", Action: AcceptProject}
			if _, err := engine.Resolve(context.Background(), resolution); !errors.Is(err, ErrStaleConflict) {
				t.Fatalf("resolve err=%v", err)
			}
			if pending, err := reviewv2.MigrationPending(fixture.project, engine.project.Info(), fixture.data); err != nil || pending {
				t.Fatalf("pending=%v err=%v", pending, err)
			}
			if version, err := reviewv2.DetectVersionExpected(fixture.project, engine.project.Info()); err != nil || version != reviewv2.VersionV2 {
				t.Fatalf("version=%s err=%v", version, err)
			}

			beforeProject := snapshotFixtureTree(t, fixture.project)
			beforeData := snapshotFixtureTree(t, fixture.data)
			beforeVault := snapshotFixtureTree(t, fixture.vault)
			if _, err := engine.Resolve(context.Background(), resolution); !errors.Is(err, ErrStaleConflict) {
				t.Fatalf("retry err=%v", err)
			}
			if !reflect.DeepEqual(beforeProject, snapshotFixtureTree(t, fixture.project)) ||
				!reflect.DeepEqual(beforeData, snapshotFixtureTree(t, fixture.data)) ||
				!reflect.DeepEqual(beforeVault, snapshotFixtureTree(t, fixture.vault)) {
				t.Fatal("retry after migration recovery mutated filesystem metadata")
			}
		})
	}
}

func stageMigrationJournalForResolve(t *testing.T, fixture engineFixture, engine *Engine, stage reviewv2.Stage) {
	t.Helper()
	journalPath, journalBody := stageRecoverableMigrationJournal(t, fixture, engine)
	if stage == reviewv2.StagePlanned {
		return
	}
	if err := reviewv2.RecoverMigration(fixture.project, engine.project.Info(), fixture.data); err != nil {
		t.Fatal(err)
	}
	var journal map[string]any
	if err := json.Unmarshal(journalBody, &journal); err != nil {
		t.Fatal(err)
	}
	if stage == reviewv2.StageV2Written {
		backupRelative, ok := journal["backup_relative"].(string)
		if !ok {
			t.Fatal("migration journal backup path is missing")
		}
		archive := filepath.Join(fixture.project, filepath.FromSlash(backupRelative), "archive")
		entries, err := os.ReadDir(archive)
		if err != nil {
			t.Fatal(err)
		}
		active := filepath.Join(fixture.project, "docs", "session-review")
		for _, entry := range entries {
			if err := os.Rename(filepath.Join(archive, entry.Name()), filepath.Join(active, entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Remove(archive); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(fixture.project, filepath.FromSlash(backupRelative), "quarantine")); err != nil {
			t.Fatal(err)
		}
	}
	body := bytes.Replace(journalBody, []byte(`"stage": "planned"`), []byte(`"stage": "`+string(stage)+`"`), 1)
	if bytes.Equal(body, journalBody) {
		t.Fatal("migration journal stage marker was not replaced")
	}
	if err := os.Remove(atomicfile.BackupPath(journalPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	hardenMigrationTestPath(t, journalPath)
}

func TestResolveRejectsStaleHiddenConflictIdentityAndLiveHashes(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	projectHistory := filepath.Join(fixture.project, "docs", "session-review", "项目历史.md")
	vaultHistory := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目历史.md")
	projectBody := bytes.Replace(readDerivedTestFile(t, projectHistory), []byte("信任链与 dry-run 边界修复"), []byte("project stale candidate"), 1)
	vaultBody := bytes.Replace(readDerivedTestFile(t, vaultHistory), []byte("信任链与 dry-run 边界修复"), []byte("vault stale candidate"), 1)
	if err := os.WriteFile(projectHistory, projectBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultHistory, vaultBody, 0o644); err != nil {
		t.Fatal(err)
	}
	conflicted, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(conflicted.Conflicts) != 1 {
		t.Fatalf("conflicted=%+v err=%v", conflicted, err)
	}
	conflictID := conflicted.Conflicts[0]
	projectBody = bytes.Replace(projectBody, []byte("project stale candidate"), []byte("later live edit"), 1)
	if err := os.WriteFile(projectHistory, projectBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Resolve(context.Background(), Resolution{ConflictID: conflictID, Action: AcceptProject}); !errors.Is(err, ErrStaleConflict) {
		t.Fatalf("stale live hash err=%v", err)
	}
	if _, err := engine.Resolve(context.Background(), Resolution{ConflictID: "conflict-20260825t000000z-project-history-000000000000", Action: AcceptProject}); !errors.Is(err, ErrStaleConflict) {
		t.Fatalf("stale identity err=%v", err)
	}
	if got := readDerivedTestFile(t, vaultHistory); !bytes.Contains(got, []byte("vault stale candidate")) {
		t.Fatal("stale resolution changed live candidate")
	}
}

func TestV2EngineNormalizesCRLFInventoryAndThenConverges(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	directory := filepath.Join(fixture.project, "docs", "session-review")
	for _, name := range []string{"项目回顾.md", "项目历史.md"} {
		filename := filepath.Join(directory, name)
		body, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, bytes.ReplaceAll(body, []byte("\n"), []byte("\r\n")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	first, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(first.Errors) != 0 || len(first.Conflicts) != 0 {
		t.Fatalf("CRLF first sync report=%+v err=%v", first, err)
	}
	for _, operation := range first.Operations {
		if operation.Kind == OperationConflict || operation.RelativePath == "invalid_hash" {
			t.Fatalf("CRLF inventory rejected: %+v", first)
		}
	}
	projectHistory := filepath.Join(directory, "项目历史.md")
	projectBody, _ := os.ReadFile(projectHistory)
	if bytes.Contains(projectBody, []byte("\r")) {
		t.Fatal("accepted project side was not normalized")
	}
	projectBody = bytes.Replace(projectBody, []byte("信任链与 dry-run 边界修复"), []byte("CRLF 同步编辑"), 1)
	projectBody = bytes.ReplaceAll(projectBody, []byte("\n"), []byte("\r\n"))
	if err := os.WriteFile(projectHistory, projectBody, 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(second.Errors) != 0 || len(second.Conflicts) != 0 {
		t.Fatalf("CRLF edited sync report=%+v err=%v", second, err)
	}
	third, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(third.Operations) != 0 || len(third.Conflicts) != 0 {
		t.Fatalf("CRLF edited sync did not converge: report=%+v err=%v", third, err)
	}
}

func TestEngineRoundTripsProjectAndObsidianEditsAndThenBecomesNoop(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	first, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Operations) != 2 || first.Operations[0].Kind != OperationAddVault || first.Operations[1].Kind != OperationAddVault || first.Machine.State != MachineCurrent {
		t.Fatalf("first report=%+v", first)
	}
	vaultHistory := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目历史.md")
	body, err := os.ReadFile(vaultHistory)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(body), "信任链与 dry-run 边界修复", "edited in Obsidian", 1)
	if err := os.WriteFile(vaultHistory, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	second, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Operations) < 2 || second.Machine.State != MachineCurrent {
		t.Fatalf("second report=%+v", second)
	}
	projectBody, err := os.ReadFile(filepath.Join(fixture.project, "docs", "session-review", "项目历史.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(projectBody), "edited in Obsidian") || !strings.Contains(string(projectBody), "revision: 2") || !bytes.Equal(projectBody, readDerivedTestFile(t, vaultHistory)) {
		t.Fatalf("project body=%s", projectBody)
	}
	if _, err := reviewv2.Load(fixture.project); err != nil {
		t.Fatalf("round trip broke compact review: %v", err)
	}

	third, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(third.Operations) != 0 {
		t.Fatalf("third report=%+v err=%v", third, err)
	}
}

func TestEnginePublishesReceiptTrustedProjectProvenanceWithoutWeakeningHumanValidation(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}

	projectPath := filepath.Join(fixture.project, "docs", "session-review", "项目回顾.md")
	projectBody, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	trustedBody := []byte(strings.Replace(string(projectBody), "revision: 1", "revision: 2", 1))
	if err := os.WriteFile(projectPath, trustedBody, 0o644); err != nil {
		t.Fatal(err)
	}
	engine.trustAppliedTransition = func(relative string, preimageExists bool, preimageHash, targetHash string) (bool, error) {
		if relative != "docs/session-review/项目回顾.md" || !preimageExists || preimageHash != syncdoc.ContentHash(projectBody) || targetHash != syncdoc.ContentHash(trustedBody) {
			t.Fatalf("unexpected trust query: relative=%q exists=%t preimage=%q target=%q", relative, preimageExists, preimageHash, targetHash)
		}
		return true, nil
	}
	dryRun, err := engine.Reconcile(context.Background(), ReconcileRequest{DryRun: true, Trigger: TriggerCLI})
	if err != nil || len(dryRun.Conflicts) != 0 || len(dryRun.Errors) != 0 || !hasOperation(dryRun.Operations, "project-overview", OperationUpdateVault) || dryRun.Machine.State != MachinePending {
		t.Fatalf("trusted dry-run=%+v err=%v", dryRun, err)
	}

	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(report.Conflicts) != 0 || len(report.Errors) != 0 || !hasOperation(report.Operations, "project-overview", OperationUpdateVault) || report.Machine.State != MachineCurrent {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	vaultBody, err := os.ReadFile(filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目回顾.md"))
	if err != nil || !bytes.Equal(vaultBody, trustedBody) {
		t.Fatalf("vault=%q err=%v", vaultBody, err)
	}
	repeat, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(repeat.Operations) != 0 || len(repeat.Conflicts) != 0 {
		t.Fatalf("repeat=%+v err=%v", repeat, err)
	}
}

func TestEngineDoesNotPublishTrustedProjectProvenanceOverConcurrentVaultEdit(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}

	projectPath := filepath.Join(fixture.project, "docs", "session-review", "项目回顾.md")
	vaultPath := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目回顾.md")
	projectBody, _ := os.ReadFile(projectPath)
	vaultBody, _ := os.ReadFile(vaultPath)
	if err := os.WriteFile(projectPath, []byte(strings.Replace(string(projectBody), "revision: 1", "revision: 2", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	concurrentVault := []byte(strings.Replace(string(vaultBody), "SessionReviewer v2", "concurrent vault edit", 1))
	if err := os.WriteFile(vaultPath, concurrentVault, 0o644); err != nil {
		t.Fatal(err)
	}
	engine.trustAppliedTransition = func(string, bool, string, string) (bool, error) {
		t.Fatal("receipt trust must not be consulted when Vault diverged from the merge base")
		return false, nil
	}

	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(report.Conflicts) != 1 || !strings.HasPrefix(report.Conflicts[0], "conflict-project-overview-") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	after, err := os.ReadFile(vaultPath)
	if err != nil || !bytes.Equal(after, concurrentVault) {
		t.Fatalf("vault overwritten: %q err=%v", after, err)
	}
}

func hasOperation(operations []Operation, entityID string, kind OperationKind) bool {
	for _, operation := range operations {
		if operation.EntityID == entityID && operation.Kind == kind {
			return true
		}
	}
	return false
}

func TestEngineDryRunPlansInitialVaultCopyWithoutFilesystemChanges(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	before := snapshotFixtureTree(t, fixture.root)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	report, err := engine.Reconcile(context.Background(), ReconcileRequest{DryRun: true, Trigger: TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Operations) != 2 || len(report.Machine.Operations) != 2 || report.Machine.Operations[1].Kind != OperationAddVault {
		t.Fatalf("report=%+v", report)
	}
	if after := snapshotFixtureTree(t, fixture.root); !reflect.DeepEqual(before, after) {
		t.Fatalf("dry-run changed tree: before=%v after=%v", before, after)
	}
}

func TestV2DryRunPlansTheSameEntityAlignmentAndMachineWritesAsRealSync(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	vaultHistory := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目历史.md")
	body := readDerivedTestFile(t, vaultHistory)
	body = bytes.Replace(body, []byte("信任链与 dry-run 边界修复"), []byte("dry-run exact write plan"), 1)
	if err := os.WriteFile(vaultHistory, body, 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotFixtureTree(t, fixture.root)
	dry, err := engine.Reconcile(context.Background(), ReconcileRequest{DryRun: true, Trigger: TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if after := snapshotFixtureTree(t, fixture.root); !reflect.DeepEqual(before, after) {
		t.Fatal("v2 dry-run mutated the fixture")
	}
	real, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dry.Operations, real.Operations) || !reflect.DeepEqual(dry.Machine.Operations, real.Machine.Operations) {
		t.Fatalf("dry-run plan differs from real writes\ndry=%+v\nreal=%+v", dry, real)
	}
}

func TestFirstSyncOfIdenticalV2CopiesEstablishesResolvableMergeBases(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	vaultReview := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath))
	if err := os.CopyFS(vaultReview, os.DirFS(filepath.Join(fixture.project, "docs", "session-review"))); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	options := fixture.options()
	options.Now = func() time.Time { return now }
	engine, err := NewEngine(options)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	status, err := engine.Status(context.Background())
	if err != nil || len(status.PendingOperations) != 4 || status.MachineState != MachinePending {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	now = now.Add(time.Minute)
	dry, err := engine.Reconcile(context.Background(), ReconcileRequest{DryRun: true, Trigger: TriggerCLI})
	if err != nil || len(dry.Operations) != 2 || dry.Machine.State != MachinePending {
		t.Fatalf("dry=%+v err=%v", dry, err)
	}
	for _, operation := range dry.Operations {
		if operation.Kind != OperationEstablishBase {
			t.Fatalf("dry operation=%+v", operation)
		}
	}
	if bases, listErr := engine.bases.List(); listErr != nil || len(bases) != 0 {
		t.Fatalf("dry-run established bases=%+v err=%v", bases, listErr)
	}
	dryPlan := append(append([]Operation{}, dry.Operations...), dry.Machine.Operations...)
	if !reflect.DeepEqual(status.PendingOperations, dryPlan) {
		t.Fatalf("status plan differs from later dry-run\nstatus=%+v\ndry=%+v", status.PendingOperations, dryPlan)
	}
	for _, operation := range dry.Machine.Operations {
		if operation.AfterHash != "" {
			t.Fatalf("public machine operation claimed commit-time hash: %+v", operation)
		}
	}
	now = now.Add(time.Minute)
	commitTime := now
	first, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(first.Operations) != 2 || len(first.Conflicts) != 0 || first.Machine.State != MachineCurrent {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if !reflect.DeepEqual(dry.Operations, first.Operations) || !reflect.DeepEqual(dry.Machine.Operations, first.Machine.Operations) {
		t.Fatalf("identical-copy dry-run differs from real plan\ndry=%+v\nreal=%+v", dry, first)
	}
	machinePath := filepath.Join(fixture.project, filepath.FromSlash(reviewv2.MachineLedgerRelativePath))
	machineBody := readDerivedTestFile(t, machinePath)
	machine, err := reviewv2.ParseMachineLedger(machineBody)
	if err != nil || machine.LastSuccessfulSync != commitTime.Format(time.RFC3339Nano) {
		t.Fatalf("machine last_successful_sync=%q want=%q err=%v", machine.LastSuccessfulSync, commitTime.Format(time.RFC3339Nano), err)
	}
	now = now.Add(time.Hour)
	repeat, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(repeat.Operations) != 0 || len(repeat.Machine.Operations) != 0 {
		t.Fatalf("repeat=%+v err=%v", repeat, err)
	}
	if after := readDerivedTestFile(t, machinePath); !bytes.Equal(after, machineBody) {
		t.Fatal("repeat sync churned commit-time machine ledger bytes")
	}
	bases, err := engine.bases.List()
	if err != nil || len(bases) != 2 {
		t.Fatalf("identical first sync bases=%+v err=%v", bases, err)
	}

	projectPath := filepath.Join(fixture.project, "docs", "session-review", "项目回顾.md")
	vaultPath := filepath.Join(vaultReview, "项目回顾.md")
	for _, edit := range []struct {
		path, replacement string
	}{{projectPath, "Project candidate"}, {vaultPath, "Vault candidate"}} {
		body, err := os.ReadFile(edit.path)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.Replace(body, []byte("Skill + 本地 CLI"), []byte(edit.replacement), 1)
		if !bytes.Contains(body, []byte(edit.replacement)) {
			t.Fatalf("fixture title was not replaced in %s", edit.path)
		}
		if err := os.WriteFile(edit.path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	conflicted, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(conflicted.Conflicts) != 1 {
		t.Fatalf("conflicted=%+v err=%v", conflicted, err)
	}
	resolved, err := engine.Resolve(context.Background(), Resolution{ConflictID: conflicted.Conflicts[0], Action: AcceptProject})
	if err != nil || len(resolved.Conflicts) != 0 {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	projectBody, projectErr := os.ReadFile(projectPath)
	vaultBody, vaultErr := os.ReadFile(vaultPath)
	if projectErr != nil || vaultErr != nil || !bytes.Equal(projectBody, vaultBody) || !bytes.Contains(projectBody, []byte("Project candidate")) {
		t.Fatalf("projectErr=%v vaultErr=%v equal=%v", projectErr, vaultErr, bytes.Equal(projectBody, vaultBody))
	}
}

func TestNewEngineRejectsProjectRootThatChangedAfterMappingResolution(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	expected, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	options := fixture.options()
	options.ProjectRootExpected = expected
	engine, err := NewEngine(options)
	if engine != nil {
		_ = engine.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("engine=%v err=%v", engine, err)
	}
}

func TestEngineDoesNotOverwriteMalformedSynchronizedPath(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(fixture.project, "docs", "session-review", "项目历史.md")
	vaultPath := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目历史.md")
	vaultBefore := readDerivedTestFile(t, vaultPath)
	malformed := []byte("malformed-project-canary\n")
	if err := os.WriteFile(projectPath, malformed, 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Issues) == 0 || len(report.Errors) == 0 || report.Machine.State != MachineCurrent {
		t.Fatalf("report=%+v", report)
	}
	after, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, malformed) {
		t.Fatalf("malformed source was overwritten: %q", after)
	}
	if got := readDerivedTestFile(t, vaultPath); !bytes.Equal(got, vaultBefore) {
		t.Fatal("machine gate allowed malformed Project content to overwrite Vault")
	}
}

func TestStatusReportsMalformedCompactReviewAsBlocked(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(fixture.project, "docs", "session-review", "项目历史.md")
	if err := os.WriteFile(projectPath, []byte("malformed-project-canary\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := engine.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Blocked == 0 {
		t.Fatalf("malformed entity was not visible: %+v", status)
	}
}

func TestRootScanIssueBlocksEveryEntity(t *testing.T) {
	issues := []syncdoc.ScanIssue{{Kind: syncdoc.IssueMalformed, RelativePath: "docs/session-review", Err: errors.New("tree scan failed")}}
	if !scanIssuesBlockEntity("decision-one", BaseRecord{}, false, issues, nil, scanIssuePaths(issues), nil, "Projects/Test/Session Review") {
		t.Fatal("a root-level scan failure did not block the entity")
	}
}

func TestEngineDoesNotOverwriteConcurrentSemanticEditDuringWriteOrRecovery(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(fixture.project, "docs", "session-review", "项目历史.md")
	vaultPath := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目历史.md")
	base := readDerivedTestFile(t, projectPath)
	vaultEdit := bytes.Replace(base, []byte("信任链与 dry-run 边界修复"), []byte("Vault note accepted candidate"), 1)
	concurrentProject := bytes.Replace(base, []byte("发布验证"), []byte("Concurrent Project note"), 1)
	if err := os.WriteFile(vaultPath, vaultEdit, 0o644); err != nil {
		t.Fatal(err)
	}
	engine.writer.beforeWrite = func(side Side, parent *os.Root, leaf string) error {
		if side != SideProject || leaf != "项目历史.md" {
			return nil
		}
		file, err := parent.OpenFile(leaf, os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := file.Write(concurrentProject); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	}
	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(report.Errors) != 1 || report.Errors[0].EntityID != "project-history" || report.Errors[0].Code != "write_failed" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if got := readDerivedTestFile(t, projectPath); !bytes.Equal(got, concurrentProject) {
		t.Fatalf("concurrent Project edit was overwritten: %q", got)
	}
	engine.writer.beforeWrite = nil
	recovered, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(recovered.Errors) != 0 || len(recovered.Conflicts) != 0 {
		t.Fatalf("safe rescan did not merge the concurrent edits: report=%+v err=%v", recovered, err)
	}
	got := readDerivedTestFile(t, projectPath)
	if !bytes.Contains(got, []byte("Concurrent Project note")) || !bytes.Contains(got, []byte("Vault note accepted candidate")) || !bytes.Contains(got, []byte("revision: 2")) {
		t.Fatalf("safe rescan lost a concurrent semantic edit: %q", got)
	}
}

func TestEngineRecoveryRejectsSemanticEditAfterFirstSideWasWritten(t *testing.T) {
	fixture := newEngineFixture(t)
	writeV2EngineFixture(t, fixture)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	vaultPath := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目历史.md")
	base := readDerivedTestFile(t, vaultPath)
	vaultEdit := bytes.Replace(base, []byte("信任链与 dry-run 边界修复"), []byte("Original accepted candidate"), 1)
	if err := os.WriteFile(vaultPath, vaultEdit, 0o644); err != nil {
		t.Fatal(err)
	}
	engine.writer.beforeWrite = func(side Side, _ *os.Root, _ string) error {
		if side == SideVault {
			return errors.New("stop after Project write")
		}
		return nil
	}
	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(report.Errors) != 1 || report.Errors[0].Code != "write_failed" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	concurrent := bytes.Replace(vaultEdit, []byte("发布验证"), []byte("Later Vault note do not overwrite"), 1)
	if err := os.WriteFile(vaultPath, concurrent, 0o644); err != nil {
		t.Fatal(err)
	}
	engine.writer.beforeWrite = nil
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err == nil || !strings.Contains(err.Error(), "transaction recovery failed") {
		t.Fatalf("recovery accepted a changed Vault preimage: %v", err)
	}
	if got := readDerivedTestFile(t, vaultPath); !bytes.Equal(got, concurrent) {
		t.Fatalf("recovery overwrote the later Vault edit: %q", got)
	}
}

type engineFixture struct {
	root, project, vault, data, vaultReviewPath, projectID string
}

func newEngineFixture(t *testing.T) engineFixture {
	t.Helper()
	root := t.TempDir()
	fixture := engineFixture{
		root: root, project: filepath.Join(root, "project"), vault: filepath.Join(root, "vault"),
		data: filepath.Join(root, "data"), vaultReviewPath: "Projects/SessionReviewer--11111111/Session Review",
		projectID: "project-1111111111111111",
	}
	for _, path := range []string{
		filepath.Join(fixture.project, "docs", "session-review"), fixture.vault,
		filepath.Join(fixture.data, "merge-bases"), filepath.Join(fixture.data, "queue"),
		filepath.Join(fixture.data, "transactions"), filepath.Join(fixture.data, "locks"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fixture.data, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	overview := "---\nid: project-overview\nentity_type: project_overview\nproject_id: " + fixture.projectID + "\nrevision: 1\nsync_status: synced\ncreated_at: 2026-08-24T00:00:00Z\nnote: base\n---\n\n# SessionReviewer\n"
	if err := os.WriteFile(filepath.Join(fixture.project, "docs", "session-review", "project-overview.md"), []byte(overview), 0o644); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture engineFixture) options() Options {
	return Options{
		ProjectRoot: fixture.project, VaultRoot: fixture.vault, VaultReviewPath: fixture.vaultReviewPath,
		DataRoot: fixture.data, ProjectID: fixture.projectID, GOOS: "darwin",
		VaultCaseMode: platform.CaseSensitive, Retry: DefaultRetryPolicy(), Now: func() time.Time {
			return time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
		},
	}
}

func TestNewEngineRejectsUnsafeRootContainmentDirections(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, *engineFixture)
		message string
	}{
		{
			name: "vault below project",
			mutate: func(t *testing.T, fixture *engineFixture) {
				t.Helper()
				nested := filepath.Join(fixture.project, "vault")
				if err := os.Rename(fixture.vault, nested); err != nil {
					t.Fatal(err)
				}
				fixture.vault = nested
			},
			message: "vault root must not be nested in project root",
		},
		{
			name: "data below project",
			mutate: func(t *testing.T, fixture *engineFixture) {
				t.Helper()
				nested := filepath.Join(fixture.project, "sync-data")
				if err := os.Rename(fixture.data, nested); err != nil {
					t.Fatal(err)
				}
				fixture.data = nested
			},
			message: "sync data root must be disjoint from project and vault roots",
		},
		{
			name: "data above project and vault",
			mutate: func(t *testing.T, fixture *engineFixture) {
				t.Helper()
				fixture.data = fixture.root
			},
			message: "sync data root must be disjoint from project and vault roots",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEngineFixture(t)
			test.mutate(t, &fixture)
			engine, err := NewEngine(fixture.options())
			if engine != nil {
				_ = engine.Close()
				t.Fatal("NewEngine returned an engine for unsafe roots")
			}
			if err == nil || err.Error() != test.message {
				t.Fatalf("NewEngine error=%v want %q", err, test.message)
			}
		})
	}
	t.Run("replacement after exclusive creation", func(t *testing.T) {
		fixture := newEngineFixture(t)
		target := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath))
		parent := filepath.Dir(target)
		if err := os.MkdirAll(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		projectRoot, err := pathguard.Open(fixture.project)
		if err != nil {
			t.Fatal(err)
		}
		defer projectRoot.Close()
		vaultRoot, err := pathguard.Open(fixture.vault)
		if err != nil {
			t.Fatal(err)
		}
		defer vaultRoot.Close()
		pin, err := PinReviewTarget(fixture.vaultReviewPath, platform.CaseSensitive, projectRoot, vaultRoot)
		if err != nil {
			t.Fatal(err)
		}
		defer pin.Close()
		const detachedName = "detached-created-leaf"
		pin.state.afterCreateIdentity = func(root *os.Root, component string) error {
			if err := root.Rename(component, detachedName); err != nil {
				return err
			}
			return root.Mkdir(component, 0o700)
		}

		if _, _, err := pin.directory(true); err == nil {
			t.Fatal("creation capability adopted the post-create replacement")
		}
		for _, directory := range []string{target, filepath.Join(parent, detachedName)} {
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("creation race target received trusted writes at %s: %v", directory, entries)
			}
		}
	})
}

// Removing either containment direction from the Vault review-target guard
// lets scanVault and Vault writes treat the authoritative Project as editable
// Vault content.
func TestNewEngineRejectsVaultReviewTargetContainingOrEqualProject(t *testing.T) {
	for _, test := range []struct {
		name            string
		projectRelative string
	}{
		{name: "target contains project", projectRelative: "project"},
		{name: "target equals project"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEngineFixture(t)
			target := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath))
			projectRoot := target
			if test.projectRelative != "" {
				projectRoot = filepath.Join(target, test.projectRelative)
			}
			if err := os.MkdirAll(filepath.Dir(projectRoot), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(fixture.project, projectRoot); err != nil {
				t.Fatal(err)
			}
			fixture.project = projectRoot

			engine, err := NewEngine(fixture.options())
			if engine != nil {
				_ = engine.Close()
				t.Fatal("NewEngine returned an engine for an overlapping Vault review target")
			}
			if err == nil || err.Error() != "vault review target must be disjoint from the authoritative Project" {
				t.Fatalf("NewEngine error=%v", err)
			}
		})
	}
}

func TestNewEngineRejectsCaseAliasReviewTargetContainingProject(t *testing.T) {
	fixture := newEngineFixture(t)
	fixture.vaultReviewPath = "Projects/CaseAlias--11111111/Session Review"
	physicalTarget := filepath.Join(fixture.vault, "projects", "casealias--11111111", "session review")
	projectRoot := filepath.Join(physicalTarget, "project")
	if err := os.MkdirAll(filepath.Dir(projectRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.project, projectRoot); err != nil {
		t.Fatal(err)
	}
	fixture.project = projectRoot
	options := fixture.options()
	options.VaultCaseMode = platform.CaseInsensitive

	engine, err := NewEngine(options)
	if engine != nil {
		_ = engine.Close()
		t.Fatal("NewEngine accepted a case-alias review target containing the Project")
	}
	if err == nil || err.Error() != "vault review target must be disjoint from the authoritative Project" {
		t.Fatalf("NewEngine error=%v", err)
	}
}

func TestNewEngineRejectsSymlinkOrReparseReviewTargetAliasContainingProject(t *testing.T) {
	fixture := newEngineFixture(t)
	realParent := filepath.Join(fixture.vault, "real-review-parent")
	projectRoot := filepath.Join(realParent, "Session Review", "project")
	if err := os.MkdirAll(filepath.Dir(projectRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.project, projectRoot); err != nil {
		t.Fatal(err)
	}
	fixture.project = projectRoot
	aliasParent := filepath.Join(fixture.vault, "Projects")
	if err := os.MkdirAll(aliasParent, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(aliasParent, "Alias--11111111")
	if err := os.Symlink(realParent, alias); err != nil {
		t.Skipf("symlink/reparse-point creation is unavailable: %v", err)
	}
	fixture.vaultReviewPath = "Projects/Alias--11111111/Session Review"

	engine, err := NewEngine(fixture.options())
	if engine != nil {
		_ = engine.Close()
		t.Fatal("NewEngine followed a review-target symlink/reparse alias containing the Project")
	}
	if err == nil {
		t.Fatal("NewEngine accepted a redirected Vault review target")
	}
}

func TestReviewTargetPinMissingLeafRejectsRacingClaims(t *testing.T) {
	tests := []struct {
		name     string
		caseMode platform.CaseMode
		claim    func(string, string) (string, error)
	}{
		{
			name:     "ordinary directory",
			caseMode: platform.CaseSensitive,
			claim: func(target, _ string) (string, error) {
				return target, os.Mkdir(target, 0o700)
			},
		},
		{
			name:     "symlink or reparse point",
			caseMode: platform.CaseSensitive,
			claim: func(target, outside string) (string, error) {
				if err := os.Mkdir(outside, 0o700); err != nil {
					return outside, err
				}
				return outside, os.Symlink(outside, target)
			},
		},
		{
			name:     "case alias",
			caseMode: platform.CaseInsensitive,
			claim: func(target, _ string) (string, error) {
				alias := filepath.Join(filepath.Dir(target), strings.ToLower(filepath.Base(target)))
				return alias, os.Mkdir(alias, 0o700)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEngineFixture(t)
			target := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath))
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			projectRoot, err := pathguard.Open(fixture.project)
			if err != nil {
				t.Fatal(err)
			}
			defer projectRoot.Close()
			vaultRoot, err := pathguard.Open(fixture.vault)
			if err != nil {
				t.Fatal(err)
			}
			defer vaultRoot.Close()
			pin, err := PinReviewTarget(fixture.vaultReviewPath, test.caseMode, projectRoot, vaultRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer pin.Close()

			claimRoot, err := test.claim(target, filepath.Join(fixture.root, "outside"))
			if err != nil {
				if test.name == "symlink or reparse point" {
					t.Skipf("symlink/reparse-point creation is unavailable: %v", err)
				}
				t.Fatal(err)
			}
			if _, _, err := pin.directory(true); err == nil {
				t.Fatal("missing-target capability adopted a racing namespace claim")
			}
			if err := pin.Recheck(projectRoot, vaultRoot); err == nil {
				t.Fatal("missing-target capability recheck accepted a racing namespace claim")
			}
			entries, err := os.ReadDir(claimRoot)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("racing claim received trusted writes: %v", entries)
			}
			if err := pin.Close(); err != nil {
				t.Fatal(err)
			}
			if err := pin.Close(); err != nil {
				t.Fatalf("second Close() was not idempotent: %v", err)
			}
		})
	}
}

func TestReviewTargetPinSharedLifetimeRejectsDetachedAuthorityUntilLastClose(t *testing.T) {
	fixture := newEngineFixture(t)
	target := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath))
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	projectRoot, err := pathguard.Open(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	defer projectRoot.Close()
	vaultRoot, err := pathguard.Open(fixture.vault)
	if err != nil {
		t.Fatal(err)
	}
	defer vaultRoot.Close()
	pin, err := PinReviewTarget(fixture.vaultReviewPath, platform.CaseSensitive, projectRoot, vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	const cloneCount = 8
	clones := make([]*ReviewTargetPin, 0, cloneCount)
	for range cloneCount {
		clone, err := pin.cloneFor(fixture.vaultReviewPath, platform.CaseSensitive, projectRoot, vaultRoot)
		if err != nil {
			t.Fatal(err)
		}
		clones = append(clones, clone)
	}
	if err := pin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pin.Close(); err != nil {
		t.Fatalf("owner Close() was not idempotent: %v", err)
	}
	detached := filepath.Join(fixture.root, "detached-review-target")
	if err := os.Rename(target, detached); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := clones[0].directory(false); err == nil {
		t.Fatal("retained clone remained usable after namespace detachment")
	}
	if err := clones[0].Recheck(projectRoot, vaultRoot); err == nil {
		t.Fatal("namespace alarm accepted the ordinary replacement")
	}

	errorsFound := make(chan error, cloneCount)
	var wait stdsync.WaitGroup
	for _, clone := range clones {
		clone := clone
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, err := clone.directory(false)
			if err == nil {
				err = errors.New("detached target unexpectedly remained usable")
			} else {
				err = nil
			}
			if closeErr := clone.Close(); err == nil {
				err = closeErr
			}
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, clone := range clones {
		if err := clone.Close(); err != nil {
			t.Fatalf("clone Close() was not idempotent: %v", err)
		}
	}
	if _, _, err := clones[0].directory(false); err == nil {
		t.Fatal("closed capability remained usable")
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatalf("ordinary replacement received trusted writes: entries=%v err=%v", entries, err)
	}
	if entries, err := os.ReadDir(detached); err != nil || len(entries) != 0 {
		t.Fatalf("detached authority received a rooted operation: entries=%v err=%v", entries, err)
	}
}

// A shallow copy is not a new capability owner. If each value carries its own
// sync.Once, closing the original and its shallow copy decrements the shared
// handle twice and invalidates a separately retained live clone.
func TestReviewTargetPinShallowCopiesShareOneOwnerToken(t *testing.T) {
	fixture := newEngineFixture(t)
	target := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath))
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	projectRoot, err := pathguard.Open(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	defer projectRoot.Close()
	vaultRoot, err := pathguard.Open(fixture.vault)
	if err != nil {
		t.Fatal(err)
	}
	defer vaultRoot.Close()
	pin, err := PinReviewTarget(fixture.vaultReviewPath, platform.CaseSensitive, projectRoot, vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	shallow := *pin
	live, err := pin.cloneFor(fixture.vaultReviewPath, platform.CaseSensitive, projectRoot, vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()

	if err := pin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := shallow.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pin.directory(false); err == nil {
		t.Fatal("closed owner remained usable")
	}
	if _, err := shallow.cloneFor(fixture.vaultReviewPath, platform.CaseSensitive, projectRoot, vaultRoot); err == nil {
		t.Fatal("closed shallow-copied owner remained cloneable")
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if directory, ready, err := live.directory(false); err != nil || !ready || !os.SameFile(directory.Info(), targetInfo) {
		t.Fatalf("independent live owner lost the retained target: ready=%v err=%v", ready, err)
	}
}

// A retained handle is never authority to write after its configured
// namespace detaches, including when the directory is relocated underneath an
// authoritative Project or private Data root.
func TestReviewTargetPinRejectsRelocationUnderProjectOrDataBeforeUse(t *testing.T) {
	for _, destination := range []string{"project", "data"} {
		t.Run(destination, func(t *testing.T) {
			fixture := newEngineFixture(t)
			target := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath))
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatal(err)
			}
			projectRoot, err := pathguard.Open(fixture.project)
			if err != nil {
				t.Fatal(err)
			}
			defer projectRoot.Close()
			vaultRoot, err := pathguard.Open(fixture.vault)
			if err != nil {
				t.Fatal(err)
			}
			defer vaultRoot.Close()
			pin, err := PinReviewTarget(fixture.vaultReviewPath, platform.CaseSensitive, projectRoot, vaultRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer pin.Close()
			destinationRoot := fixture.project
			if destination == "data" {
				destinationRoot = fixture.data
			}
			relocated := filepath.Join(destinationRoot, "relocated-review-target")
			if err := os.Rename(target, relocated); err != nil {
				t.Fatal(err)
			}

			if _, _, err := pin.directory(true); err == nil {
				t.Fatal("detached review-target capability remained usable")
			}
			if entries, err := os.ReadDir(relocated); err != nil || len(entries) != 0 {
				t.Fatalf("relocated target changed before failure: entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestReviewTargetPinRechecksRelocationBetweenMissingComponentCreates(t *testing.T) {
	fixture := newEngineFixture(t)
	projectRoot, err := pathguard.Open(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	defer projectRoot.Close()
	vaultRoot, err := pathguard.Open(fixture.vault)
	if err != nil {
		t.Fatal(err)
	}
	defer vaultRoot.Close()
	dataRoot, err := pathguard.Open(fixture.data)
	if err != nil {
		t.Fatal(err)
	}
	defer dataRoot.Close()
	pin, err := PinReviewTarget("Missing/Leaf", platform.CaseSensitive, projectRoot, vaultRoot, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	relocated := filepath.Join(fixture.project, "relocated-missing-target")
	pin.state.afterCreatePinned = func(created *pathguard.Directory) error {
		pin.state.afterCreatePinned = nil
		if err := os.Rename(filepath.Join(fixture.vault, "Missing"), relocated); err != nil {
			return err
		}
		return os.Mkdir(filepath.Join(fixture.vault, "Missing"), 0o700)
	}
	if _, _, err := pin.directory(true); err == nil {
		t.Fatal("relocated intermediate target remained authorized for leaf creation")
	}
	if _, err := os.Stat(filepath.Join(relocated, "Leaf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("leaf was created through relocated Project authority: %v", err)
	}
}

// The pre-create alias scan is not enough: a racer can add a folded/NFC
// equivalent after the exact Mkdir. The post-create and later recheck scans
// must bind the sole equivalent entry to the authenticated inode.
func TestReviewTargetPinRejectsEquivalentAliasCreatedAfterExactTarget(t *testing.T) {
	fixture := newEngineFixture(t)
	projectRoot, err := pathguard.Open(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	defer projectRoot.Close()
	vaultRoot, err := pathguard.Open(fixture.vault)
	if err != nil {
		t.Fatal(err)
	}
	defer vaultRoot.Close()
	pin, err := PinReviewTarget(fixture.vaultReviewPath, platform.CaseInsensitive, projectRoot, vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	createdAlias := false
	pin.state.afterCreateIdentity = func(root *os.Root, component string) error {
		if createdAlias {
			return nil
		}
		createdAlias = true
		if err := root.Mkdir(strings.ToLower(component), 0o700); err != nil {
			if errors.Is(err, os.ErrExist) {
				t.Skip("filesystem natively rejects folded aliases")
			}
			return err
		}
		return nil
	}
	if _, _, err := pin.directory(true); err == nil {
		t.Fatal("post-create folded alias race was accepted")
	}

	fixture = newEngineFixture(t)
	projectRoot2, err := pathguard.Open(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	defer projectRoot2.Close()
	vaultRoot2, err := pathguard.Open(fixture.vault)
	if err != nil {
		t.Fatal(err)
	}
	defer vaultRoot2.Close()
	pin2, err := PinReviewTarget(fixture.vaultReviewPath, platform.CaseInsensitive, projectRoot2, vaultRoot2)
	if err != nil {
		t.Fatal(err)
	}
	defer pin2.Close()
	if _, ready, err := pin2.directory(true); err != nil || !ready {
		t.Fatalf("create exact target: ready=%v err=%v", ready, err)
	}
	target := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath))
	alias := filepath.Join(filepath.Dir(target), strings.ToLower(filepath.Base(target)))
	if err := os.Mkdir(alias, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			t.Skip("filesystem natively rejects folded aliases")
		}
		t.Fatal(err)
	}
	if err := pin2.Recheck(projectRoot2, vaultRoot2); err == nil {
		t.Fatal("recheck accepted a second folded/NFC-equivalent target")
	}
}

// This pure namespace snapshot test is intentionally filesystem-independent:
// it exercises the post-create race decision even on default macOS volumes
// that cannot physically contain both folded spellings.
func TestReviewTargetEquivalentAliasRaceSnapshotRejectsSecondFoldedEntry(t *testing.T) {
	root := t.TempDir()
	exactPath := filepath.Join(root, "exact")
	aliasPath := filepath.Join(root, "alias")
	if err := os.Mkdir(exactPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(aliasPath, 0o700); err != nil {
		t.Fatal(err)
	}
	exact, err := os.Lstat(exactPath)
	if err != nil {
		t.Fatal(err)
	}
	alias, err := os.Lstat(aliasPath)
	if err != nil {
		t.Fatal(err)
	}
	candidates := []reviewTargetEquivalentCandidate{
		{name: "Review", info: exact},
		{name: "review", info: alias},
	}
	if err := validateReviewTargetEquivalentCandidates("Review", exact, candidates); err == nil {
		t.Fatal("post-create folded alias snapshot was accepted")
	}
	if err := validateReviewTargetEquivalentCandidates("Review", nil, candidates); err == nil {
		t.Fatal("pre-create folded alias snapshot was accepted")
	}
}

func TestMutatingEntrypointsAuthenticateReviewTargetBeforeMigrationOrRecovery(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Engine) error
	}{
		{
			name: "machine ledger repair",
			run: func(engine *Engine) error {
				_, err := engine.RepairMachineLedger(t.Context())
				return err
			},
		},
		{
			name: "conflict resolution",
			run: func(engine *Engine) error {
				_, err := engine.Resolve(t.Context(), Resolution{
					ConflictID: "conflict-project-overview-0123456789ab",
					Action:     AcceptProject,
				})
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEngineFixture(t)
			engine, err := NewEngine(fixture.options())
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()

			aliasParent := filepath.Join(fixture.vault, "Projects")
			if err := os.MkdirAll(aliasParent, 0o700); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(aliasParent, "SessionReviewer--11111111")
			if err := os.Symlink(fixture.project, alias); err != nil {
				t.Skipf("symlink/reparse-point creation is unavailable: %v", err)
			}

			if err := test.run(engine); err == nil || !strings.Contains(err.Error(), "vault review target identity changed") {
				t.Fatalf("entrypoint error=%v, want review-target identity failure before recovery", err)
			}
		})
	}
}

// Reopening VaultReviewPath after a potentially long sync-lock wait lets an
// ordinary replacement become trusted state. Each mutating entrypoint must
// continue only through the exact target capability retained by NewEngine.
func TestMutatingEntrypointsUsePinnedReviewTargetAcrossLockWait(t *testing.T) {
	t.Run("reconcile", func(t *testing.T) {
		fixture := newEngineFixture(t)
		writeV2EngineFixture(t, fixture)
		engine, err := NewEngine(fixture.options())
		if err != nil {
			t.Fatal(err)
		}
		defer engine.Close()
		if _, err := engine.Reconcile(t.Context(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
			t.Fatal(err)
		}
		projectPath := filepath.Join(fixture.project, "docs", "session-review", "项目回顾.md")
		projectBody := readDerivedTestFile(t, projectPath)
		projectBody = bytes.Replace(projectBody, []byte("Skill + 本地 CLI"), []byte("Pinned reconcile authority"), 1)
		if !bytes.Contains(projectBody, []byte("Pinned reconcile authority")) {
			t.Fatal("reconcile fixture edit did not apply")
		}
		if err := os.WriteFile(projectPath, projectBody, 0o644); err != nil {
			t.Fatal(err)
		}

		detached := replaceReviewTargetWhileEntrypointWaits(t, fixture, engine, func() error {
			_, err := engine.Reconcile(t.Context(), ReconcileRequest{Trigger: TriggerCLI})
			return err
		})
		if got := readDerivedTestFile(t, filepath.Join(detached, "项目回顾.md")); bytes.Contains(got, []byte("Pinned reconcile authority")) {
			t.Fatalf("reconcile mutated a detached target: %q", got)
		}
	})

	t.Run("machine ledger repair", func(t *testing.T) {
		fixture := newEngineFixture(t)
		writeV2EngineFixture(t, fixture)
		engine, err := NewEngine(fixture.options())
		if err != nil {
			t.Fatal(err)
		}
		defer engine.Close()
		if _, err := engine.Reconcile(t.Context(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
			t.Fatal(err)
		}
		projectLedger := readDerivedTestFile(t, filepath.Join(fixture.project, filepath.FromSlash(reviewv2.MachineLedgerRelativePath)))
		targetLedger := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), ".session-reviewer", "ledger.json")
		if err := os.WriteFile(targetLedger, []byte("repair through pinned target\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		detached := replaceReviewTargetWhileEntrypointWaits(t, fixture, engine, func() error {
			_, err := engine.RepairMachineLedger(t.Context())
			return err
		})
		if got := readDerivedTestFile(t, filepath.Join(detached, ".session-reviewer", "ledger.json")); bytes.Equal(got, projectLedger) || !bytes.Equal(got, []byte("repair through pinned target\n")) {
			t.Fatal("repair mutated the detached target")
		}
	})

	t.Run("conflict resolution", func(t *testing.T) {
		fixture := newEngineFixture(t)
		writeV2EngineFixture(t, fixture)
		engine, err := NewEngine(fixture.options())
		if err != nil {
			t.Fatal(err)
		}
		defer engine.Close()
		if _, err := engine.Reconcile(t.Context(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
			t.Fatal(err)
		}
		projectPath := filepath.Join(fixture.project, "docs", "session-review", "项目回顾.md")
		targetPath := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "项目回顾.md")
		base := readDerivedTestFile(t, projectPath)
		projectBody := bytes.Replace(base, []byte("Skill + 本地 CLI"), []byte("Pinned resolution project"), 1)
		targetBody := bytes.Replace(base, []byte("Skill + 本地 CLI"), []byte("Pinned resolution vault"), 1)
		if err := os.WriteFile(projectPath, projectBody, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(targetPath, targetBody, 0o644); err != nil {
			t.Fatal(err)
		}
		conflicted, err := engine.Reconcile(t.Context(), ReconcileRequest{Trigger: TriggerCLI})
		if err != nil || len(conflicted.Conflicts) != 1 {
			t.Fatalf("conflicted=%+v err=%v", conflicted, err)
		}

		detached := replaceReviewTargetWhileEntrypointWaits(t, fixture, engine, func() error {
			_, err := engine.Resolve(t.Context(), Resolution{ConflictID: conflicted.Conflicts[0], Action: AcceptProject})
			return err
		})
		if got := readDerivedTestFile(t, filepath.Join(detached, "项目回顾.md")); bytes.Contains(got, []byte("Pinned resolution project")) || !bytes.Contains(got, []byte("Pinned resolution vault")) {
			t.Fatalf("resolve mutated the detached target: %q", got)
		}
	})

	t.Run("transaction recovery", func(t *testing.T) {
		fixture := newEngineFixture(t)
		writeV2EngineFixture(t, fixture)
		engine, err := NewEngine(fixture.options())
		if err != nil {
			t.Fatal(err)
		}
		defer engine.Close()
		if _, err := engine.Reconcile(t.Context(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
			t.Fatal(err)
		}
		projectPath := filepath.Join(fixture.project, "docs", "session-review", "项目历史.md")
		projectBody := readDerivedTestFile(t, projectPath)
		projectBody = bytes.Replace(projectBody, []byte("信任链与 dry-run 边界修复"), []byte("Pinned recovery authority"), 1)
		if !bytes.Contains(projectBody, []byte("Pinned recovery authority")) {
			t.Fatal("recovery fixture edit did not apply")
		}
		if err := os.WriteFile(projectPath, projectBody, 0o644); err != nil {
			t.Fatal(err)
		}
		engine.writer.beforeWrite = func(side Side, _ *os.Root, _ string) error {
			if side == SideVault {
				return errors.New("leave transaction for recovery")
			}
			return nil
		}
		if report, err := engine.Reconcile(t.Context(), ReconcileRequest{Trigger: TriggerCLI}); err != nil || len(report.Errors) == 0 {
			t.Fatalf("failed to stage recovery transaction: report=%+v err=%v", report, err)
		}
		engine.writer.beforeWrite = nil

		detached := replaceReviewTargetWhileEntrypointWaits(t, fixture, engine, func() error {
			_, err := engine.Reconcile(t.Context(), ReconcileRequest{Trigger: TriggerCLI})
			return err
		})
		if got := readDerivedTestFile(t, filepath.Join(detached, "项目历史.md")); bytes.Contains(got, []byte("Pinned recovery authority")) {
			t.Fatalf("transaction recovery mutated the detached target: %q", got)
		}
	})

	t.Run("legacy migration", func(t *testing.T) {
		fixture := newEngineFixture(t)
		completeLegacyFixtureForMigration(t, fixture)
		copyLegacyReviewToVault(t, fixture)
		engine, err := NewEngine(fixture.options())
		if err != nil {
			t.Fatal(err)
		}
		defer engine.Close()

		detached := replaceReviewTargetWhileEntrypointWaits(t, fixture, engine, func() error {
			_, err := engine.Reconcile(t.Context(), ReconcileRequest{Trigger: TriggerCLI})
			return err
		})
		if _, err := os.Stat(filepath.Join(detached, "project-overview.md")); err != nil {
			t.Fatalf("legacy file changed after target detachment: %v", err)
		}
		if _, err := os.Stat(filepath.Join(detached, "项目回顾.md")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("migration published through a detached target: %v", err)
		}
	})
}

func replaceReviewTargetWhileEntrypointWaits(t *testing.T, fixture engineFixture, engine *Engine, run func() error) string {
	t.Helper()
	blocker, err := project.AcquireProjectLock(engine.data.Root, "locks/sync.lock", 0)
	if err != nil {
		t.Fatal(err)
	}
	reached := make(chan struct{})
	var once stdsync.Once
	engine.beforeLock = func() error {
		once.Do(func() { close(reached) })
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		_ = blocker.Release()
		t.Fatal("entrypoint did not reach the held sync lock")
	}
	target := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath))
	detached := filepath.Join(fixture.root, "detached-"+strings.ReplaceAll(t.Name(), "/", "-"))
	if err := os.Rename(target, detached); err != nil {
		_ = blocker.Release()
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		_ = blocker.Release()
		t.Fatal(err)
	}
	if err := blocker.Release(); err != nil {
		t.Fatal(err)
	}
	err = <-done
	engine.beforeLock = nil
	if err == nil || !strings.Contains(err.Error(), "vault review target identity changed") {
		t.Fatalf("entrypoint error=%v, want pre-mutation namespace identity failure", err)
	}
	entries, readErr := os.ReadDir(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("ordinary replacement received trusted writes: %v", entries)
	}
	return detached
}

func writeFixtureDecision(t *testing.T, fixture engineFixture, relative string) {
	t.Helper()
	body := "---\nid: decision-sync\nentity_type: decision\nproject_id: " + fixture.projectID + "\nrevision: 1\nsync_status: synced\ntitle: Sync decision\nstatus: accepted\ntags: [sync]\nsupersedes: []\nsource_sessions: [session-1]\nevidence:\n  - evidence_id: evidence-1\n    session_id: session-1\n    jsonl_line: 1\n    source_hash: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n    summary: accepted\n---\n\n# Sync decision\n\n## Alternatives\n\n## Rejected paths\n"
	filename := filepath.Join(fixture.project, "docs", "session-review", filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeV2EngineFixture(t *testing.T, fixture engineFixture) {
	t.Helper()
	directory := filepath.Join(fixture.project, "docs", "session-review")
	if err := os.Remove(filepath.Join(directory, "project-overview.md")); err != nil {
		t.Fatal(err)
	}
	written := make(map[string][]byte, 2)
	for name, document := range map[string]syncdoc.Document{
		"项目回顾.md": v2ReviewWithTwoDecisions(t),
		"项目历史.md": v2HistoryWithTwoEvents(t),
	} {
		body := renderDocument(t, document)
		body = bytes.Replace(body, []byte("project-0123456789abcdef"), []byte(fixture.projectID), 1)
		if err := os.WriteFile(filepath.Join(directory, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
		written[name] = body
	}
	machineFixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "review-v2", "ledger.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	machine, err := reviewv2.ParseMachineLedger(machineFixture)
	if err != nil {
		t.Fatal(err)
	}
	machine.ProjectID = fixture.projectID
	machine.AcceptedRevision = 1
	for index := range machine.Sessions {
		machine.Sessions[index].ProjectID = fixture.projectID
	}
	machine.LegacyCompatibility.CurrentState.ProjectID = fixture.projectID
	machine.LegacyCompatibility.CurrentState.Revision = 1
	decisionTemplate := machine.LegacyCompatibility.Decisions[0]
	decisionTemplate.ProjectID = fixture.projectID
	decisionTemplate.Revision = 1
	localDecision := decisionTemplate
	localDecision.ID = "decision-local-cli"
	compatibilityDecision := decisionTemplate
	compatibilityDecision.ID = "decision-compatibility"
	machine.LegacyCompatibility.Decisions = []ledger.Decision{localDecision, compatibilityDecision}
	riskTemplate := machine.LegacyCompatibility.OpenLoops[0]
	riskTemplate.ID = "risk-installer-permission"
	riskTemplate.ProjectID = fixture.projectID
	riskTemplate.Revision = 1
	machine.LegacyCompatibility.OpenLoops = []ledger.OpenLoop{riskTemplate}
	eventTemplate := machine.LegacyCompatibility.Timeline[0]
	eventTemplate.Revision = 1
	eventTemplate.DecisionIDs = []string{"decision-local-cli"}
	trustEvent := eventTemplate
	trustEvent.ID = "timeline-trust-chain"
	releaseEvent := eventTemplate
	releaseEvent.ID = "timeline-release"
	releaseEvent.DecisionIDs = []string{}
	machine.LegacyCompatibility.Timeline = []ledger.TimelineEvent{trustEvent, releaseEvent}
	decisionEvidence := machine.Evidence["decision-a"]
	delete(machine.Evidence, "decision-a")
	machine.Evidence["decision-local-cli"] = decisionEvidence
	machine.ReviewSHA256 = syncdoc.ContentHash(written["项目回顾.md"])
	machine.HistorySHA256 = syncdoc.ContentHash(written["项目历史.md"])
	machineBody, err := reviewv2.RenderMachineLedger(machine)
	if err != nil {
		t.Fatal(err)
	}
	machineDir := filepath.Join(directory, ".session-reviewer")
	if err := os.MkdirAll(machineDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machineDir, "ledger.json"), machineBody, 0o600); err != nil {
		t.Fatal(err)
	}
}

func snapshotFixtureTree(t *testing.T, root string) []string {
	t.Helper()
	var result []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result = append(result, filepath.ToSlash(relative)+"|"+info.Mode().String()+"|"+info.ModTime().UTC().Format(time.RFC3339Nano))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
