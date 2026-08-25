package reviewv2

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

func TestMigrationDryRunWritesNothingAndCrashRecoveryConverges(t *testing.T) {
	for _, failAfter := range []Stage{StagePlanned, StageBackupComplete, StageV2Written, StageLegacyMoved, StageCommitted} {
		t.Run(string(failAfter), func(t *testing.T) {
			fixture := newLegacyMigrationFixture(t)
			before := fixture.snapshot()
			plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, fixture.snapshot()) {
				t.Fatal("planning mutated filesystem")
			}
			err = applyMigrationWithHook(plan, func(stage Stage) error {
				if stage == failAfter {
					return errors.New("injected crash")
				}
				return nil
			})
			if err == nil {
				t.Fatal("injected crash was ignored")
			}
			if err := RecoverMigration(fixture.project, fixture.projectInfo, fixture.data); err != nil {
				t.Fatal(err)
			}
			assertV2OnlyVisible(t, fixture.project)
			assertBackupManifestComplete(t, fixture.project)
		})
	}
}

func TestMigrationRejectsChangesAfterPlanningBeforeAnyMigrationWrite(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture legacyMigrationFixture, plan MigrationPlan)
	}{
		{"legacy content edit", func(t *testing.T, fixture legacyMigrationFixture, _ MigrationPlan) {
			file := filepath.Join(fixture.project, "docs", "session-review", "current-state.md")
			body, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, append(body, []byte("\nuser edit after preview\n")...), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"unexpected visible file", func(t *testing.T, fixture legacyMigrationFixture, _ MigrationPlan) {
			if err := os.WriteFile(filepath.Join(fixture.project, "docs", "session-review", "added-after-preview.txt"), []byte("new"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"v2 target collision", func(t *testing.T, fixture legacyMigrationFixture, _ MigrationPlan) {
			if err := os.WriteFile(filepath.Join(fixture.project, filepath.FromSlash(ReviewRelativePath)), []byte("user-created v2 target"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"backup collision", func(t *testing.T, fixture legacyMigrationFixture, plan MigrationPlan) {
			if err := os.MkdirAll(filepath.Join(fixture.project, filepath.FromSlash(plan.BackupRoot)), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLegacyMigrationFixture(t)
			plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, fixture, plan)
			before := fixture.snapshot()
			if _, err := ApplyMigration(plan); err == nil {
				t.Fatal("stale migration plan was applied")
			}
			if !reflect.DeepEqual(before, fixture.snapshot()) {
				t.Fatal("rejected stale plan performed migration writes")
			}
		})
	}
}

func TestRecoverMigrationNeverOverwritesUserEditAfterInterruption(t *testing.T) {
	tests := []struct {
		stage Stage
		path  string
	}{
		{StageBackupComplete, "docs/session-review/current-state.md"},
		{StageV2Written, ReviewRelativePath},
		{StageLegacyMoved, ReviewRelativePath},
	}
	for _, test := range tests {
		t.Run(string(test.stage), func(t *testing.T) {
			fixture := newLegacyMigrationFixture(t)
			plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			stop := errors.New("injected stop")
			if err := applyMigrationWithHook(plan, func(stage Stage) error {
				if stage == test.stage {
					return stop
				}
				return nil
			}); !errors.Is(err, stop) {
				t.Fatalf("injected error=%v", err)
			}
			full := filepath.Join(fixture.project, filepath.FromSlash(test.path))
			body, err := os.ReadFile(full)
			if err != nil {
				t.Fatal(err)
			}
			edited := append(append([]byte(nil), body...), []byte("\nuser edit during interrupted migration\n")...)
			if err := os.WriteFile(full, edited, 0o644); err != nil {
				t.Fatal(err)
			}
			err = RecoverMigration(fixture.project, fixture.projectInfo, fixture.data)
			if !errors.Is(err, ErrStaleMigration) {
				t.Fatalf("recovery error=%v", err)
			}
			after, readErr := os.ReadFile(full)
			if readErr != nil || !bytes.Equal(after, edited) {
				t.Fatalf("user edit overwritten: got=%q err=%v", after, readErr)
			}
		})
	}
}

func TestMigrationV2PublicationNeverReplacesConcurrentUserFile(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	userBytes := []byte("user-published-between-check-and-publish")
	injected := false
	err = applyMigrationWithHooks(plan, migrationHooks{
		beforeV2Publish: func(relative string) error {
			if injected || relative != ReviewRelativePath {
				return nil
			}
			injected = true
			return os.WriteFile(filepath.Join(fixture.project, filepath.FromSlash(relative)), userBytes, 0o644)
		},
	})
	if !errors.Is(err, ErrStaleMigration) {
		t.Fatalf("publication race error=%v", err)
	}
	got, readErr := os.ReadFile(filepath.Join(fixture.project, filepath.FromSlash(ReviewRelativePath)))
	if readErr != nil || !bytes.Equal(got, userBytes) {
		t.Fatalf("concurrent user file overwritten: got=%q err=%v", got, readErr)
	}
}

func TestMigrationArchiveNeverReplacesConcurrentDestination(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	source := "docs/session-review/current-state.md"
	destination := plan.BackupRoot + "/archive/current-state.md"
	original, err := os.ReadFile(filepath.Join(fixture.project, filepath.FromSlash(source)))
	if err != nil {
		t.Fatal(err)
	}
	userBytes := []byte("user-owned-archive-destination")
	err = applyMigrationWithHooks(plan, migrationHooks{
		beforeArchivePublish: func(gotSource, gotDestination string) error {
			if gotSource != source || gotDestination != destination {
				return nil
			}
			return os.WriteFile(filepath.Join(fixture.project, filepath.FromSlash(destination)), userBytes, 0o600)
		},
	})
	if !errors.Is(err, ErrStaleMigration) {
		t.Fatalf("archive collision error=%v", err)
	}
	gotDestination, destinationErr := os.ReadFile(filepath.Join(fixture.project, filepath.FromSlash(destination)))
	gotSource, sourceErr := os.ReadFile(filepath.Join(fixture.project, filepath.FromSlash(source)))
	if destinationErr != nil || !bytes.Equal(gotDestination, userBytes) {
		t.Fatalf("archive destination overwritten: got=%q err=%v", gotDestination, destinationErr)
	}
	if sourceErr != nil || !bytes.Equal(gotSource, original) {
		t.Fatalf("archive source lost after collision: got=%q err=%v", gotSource, sourceErr)
	}
}

func TestMigrationArchiveRollsBackWhenSourceReplacedAfterPublish(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	source := "docs/session-review/current-state.md"
	destination := plan.BackupRoot + "/archive/current-state.md"
	userBytes := []byte("user-replaced-source-after-archive-publish")
	injected := false
	err = applyMigrationWithHooks(plan, migrationHooks{
		afterArchivePublish: func(gotSource, gotDestination string) error {
			if injected || gotSource != source || gotDestination != destination {
				return nil
			}
			injected = true
			temporary := filepath.Join(fixture.project, "replacement.tmp")
			if err := os.WriteFile(temporary, userBytes, 0o644); err != nil {
				return err
			}
			return os.Rename(temporary, filepath.Join(fixture.project, filepath.FromSlash(source)))
		},
	})
	if !errors.Is(err, ErrStaleMigration) {
		t.Fatalf("source replacement error=%v", err)
	}
	gotSource, sourceErr := os.ReadFile(filepath.Join(fixture.project, filepath.FromSlash(source)))
	if sourceErr != nil || !bytes.Equal(gotSource, userBytes) {
		t.Fatalf("replacement source overwritten: got=%q err=%v", gotSource, sourceErr)
	}
	if _, destinationErr := os.Lstat(filepath.Join(fixture.project, filepath.FromSlash(destination))); !errors.Is(destinationErr, os.ErrNotExist) {
		t.Fatalf("archive rollback did not remove owned destination: %v", destinationErr)
	}
}

func TestMigrationArchiveRetirementNeverDeletesFinalWindowSourceReplacement(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	source := "docs/session-review/current-state.md"
	userBytes := []byte("user-replaced-source-in-final-retirement-window")
	injected := false
	quarantine := ""
	err = applyMigrationWithHooks(plan, migrationHooks{
		beforeArchiveRetire: func(gotSource, gotQuarantine string) error {
			if injected || gotSource != source {
				return nil
			}
			injected = true
			quarantine = gotQuarantine
			temporary := filepath.Join(fixture.project, "retirement-replacement.tmp")
			if err := os.WriteFile(temporary, userBytes, 0o644); err != nil {
				return err
			}
			return os.Rename(temporary, filepath.Join(fixture.project, filepath.FromSlash(source)))
		},
	})
	if !errors.Is(err, ErrStaleMigration) {
		t.Fatalf("retirement race error=%v", err)
	}
	if got, readErr := os.ReadFile(filepath.Join(fixture.project, filepath.FromSlash(source))); readErr == nil && bytes.Equal(got, userBytes) {
		return
	}
	if got, readErr := os.ReadFile(filepath.Join(fixture.project, filepath.FromSlash(quarantine))); readErr == nil && bytes.Equal(got, userBytes) {
		return
	}
	t.Fatal("final-window source replacement was neither retained at source nor recoverable quarantine")
}

func TestMigrationArchiveRollbackNeverDeletesFinalWindowDestinationReplacement(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	source := "docs/session-review/current-state.md"
	destination := plan.BackupRoot + "/archive/current-state.md"
	userBytes := []byte("user-replaced-destination-in-final-rollback-window")
	quarantine := ""
	err = applyMigrationWithHooks(plan, migrationHooks{
		afterArchivePublish: func(gotSource, _ string) error {
			if gotSource != source {
				return nil
			}
			return errors.New("force rollback")
		},
		beforeArchiveRollbackRetire: func(_, gotDestination string) error {
			// The second hook argument is the deterministic quarantine path.
			quarantine = gotDestination
			if quarantine == "" {
				return nil
			}
			temporary := filepath.Join(fixture.project, "rollback-replacement.tmp")
			if err := os.WriteFile(temporary, userBytes, 0o600); err != nil {
				return err
			}
			return os.Rename(temporary, filepath.Join(fixture.project, filepath.FromSlash(destination)))
		},
	})
	if err == nil {
		t.Fatal("rollback race unexpectedly succeeded")
	}
	if got, readErr := os.ReadFile(filepath.Join(fixture.project, filepath.FromSlash(destination))); readErr == nil && bytes.Equal(got, userBytes) {
		return
	}
	if got, readErr := os.ReadFile(filepath.Join(fixture.project, filepath.FromSlash(quarantine))); readErr == nil && bytes.Equal(got, userBytes) {
		return
	}
	t.Fatal("final-window destination replacement was neither retained nor recoverable quarantine")
}

func TestMigrationRecoveryConvergesAfterPartialArchiveDirectoryCreation(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop after archive directory creation")
	created := false
	err = applyMigrationWithHooks(plan, migrationHooks{
		afterArchiveDirectory: func(string) error {
			if created {
				return nil
			}
			created = true
			return stop
		},
	})
	if !errors.Is(err, stop) || !created {
		t.Fatalf("partial archive interruption=%v created=%v", err, created)
	}
	if err := RecoverMigration(fixture.project, fixture.projectInfo, fixture.data); err != nil {
		t.Fatal(err)
	}
	assertV2OnlyVisible(t, fixture.project)
}

func TestArchiveInventoryDirectorySecuresOnlyProvenNewDirectoryBeforeValidation(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, "archive"), 0o700); err != nil {
		t.Fatal(err)
	}
	project, err := pathguard.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer project.Close()
	secured := false
	err = ensureArchiveInventoryDirectoryWithPrivacy(project, "archive/new", 0o751, nil,
		func(*os.File) error { secured = true; return nil },
		func(string, os.FileMode) bool { return secured },
	)
	if err != nil || !secured {
		t.Fatalf("new archive directory was not secured before validation: secured=%v err=%v", secured, err)
	}
	if info, statErr := os.Stat(filepath.Join(rootPath, "archive", "new")); statErr != nil || info.Mode().Perm() != 0o751 {
		t.Fatalf("non-Windows archive mode changed: mode=%v err=%v", info, statErr)
	}
	if err := os.Mkdir(filepath.Join(rootPath, "archive", "existing"), 0o755); err != nil {
		t.Fatal(err)
	}
	secureCalls := 0
	err = ensureArchiveInventoryDirectoryWithPrivacy(project, "archive/existing", 0o755, nil,
		func(*os.File) error { secureCalls++; return nil },
		func(string, os.FileMode) bool { return false },
	)
	if err == nil || secureCalls != 0 {
		t.Fatalf("existing unsafe directory repaired: secureCalls=%d err=%v", secureCalls, err)
	}
	if info, statErr := os.Stat(filepath.Join(rootPath, "archive", "existing")); statErr != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("existing mode was changed: mode=%v err=%v", info, statErr)
	}
}

func TestArchiveInventoryDirectoryReplacementBeforeSecureIsStaleAndUntouched(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, "archive"), 0o700); err != nil {
		t.Fatal(err)
	}
	project, err := pathguard.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer project.Close()
	replacement := filepath.Join(rootPath, "archive", "nested")
	away := filepath.Join(rootPath, "archive", "created-away")
	err = ensureArchiveInventoryDirectoryWithPrivacy(project, "archive/nested", 0o700, func() error {
		if err := os.Rename(replacement, away); err != nil {
			return err
		}
		if err := os.Mkdir(replacement, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(replacement, "user.txt"), []byte("replacement"), 0o644)
	}, func(file *os.File) error { return file.Chmod(0o700) }, func(string, os.FileMode) bool { return true })
	if !errors.Is(err, ErrStaleMigration) {
		t.Fatalf("replacement race error=%v", err)
	}
	info, statErr := os.Stat(replacement)
	if statErr != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("replacement was repaired: mode=%v err=%v", info, statErr)
	}
	body, readErr := os.ReadFile(filepath.Join(replacement, "user.txt"))
	if readErr != nil || string(body) != "replacement" {
		t.Fatalf("replacement bytes changed: body=%q err=%v", body, readErr)
	}
}

func TestMigrationRecoveryConvergesAfterCrashFollowingArchiveLink(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	crashed := false
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				crashed = true
			}
		}()
		_ = applyMigrationWithHooks(plan, migrationHooks{
			afterArchivePublish: func(string, string) error {
				panic("simulated process death after no-replace link")
			},
		})
	}()
	if !crashed {
		t.Fatal("archive link crash was not injected")
	}
	if err := RecoverMigration(fixture.project, fixture.projectInfo, fixture.data); err != nil {
		t.Fatal(err)
	}
	assertV2OnlyVisible(t, fixture.project)
}

func TestMigrationRecoveryConvergesAfterCrashFollowingArchiveRetirement(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	crashed := false
	func() {
		defer func() { crashed = recover() != nil }()
		_ = applyMigrationWithHooks(plan, migrationHooks{
			afterArchiveRetire: func(string, string) error { panic("simulated process death after durable quarantine rename") },
		})
	}()
	if !crashed {
		t.Fatal("archive retirement crash was not injected")
	}
	if err := RecoverMigration(fixture.project, fixture.projectInfo, fixture.data); err != nil {
		t.Fatal(err)
	}
	assertV2OnlyVisible(t, fixture.project)
	assertBackupManifestComplete(t, fixture.project)
}

func TestMigrationAuthenticatesV2ModesAtEveryRecoveryStage(t *testing.T) {
	for _, stage := range []Stage{StageV2Written, StageLegacyMoved, StageCommitted} {
		for _, target := range []string{ReviewRelativePath, HistoryRelativePath, MachineLedgerRelativePath} {
			t.Run(string(stage)+"/"+filepath.Base(target), func(t *testing.T) {
				fixture := newLegacyMigrationFixture(t)
				plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
				if err != nil {
					t.Fatal(err)
				}
				stop := errors.New("stop after stage")
				if err := applyMigrationWithHook(plan, func(current Stage) error {
					if current == stage {
						return stop
					}
					return nil
				}); !errors.Is(err, stop) {
					t.Fatalf("stage interruption=%v", err)
				}
				expected := os.FileMode(0o644)
				mutated := os.FileMode(0o600)
				if target == MachineLedgerRelativePath {
					expected, mutated = 0o600, 0o644
				}
				full := filepath.Join(fixture.project, filepath.FromSlash(target))
				before, err := os.Stat(full)
				if err != nil || before.Mode().Perm() != expected {
					t.Fatalf("initial mode=%v want=%v err=%v", before, expected, err)
				}
				if err := os.Chmod(full, mutated); err != nil {
					t.Fatal(err)
				}
				err = RecoverMigration(fixture.project, fixture.projectInfo, fixture.data)
				if !errors.Is(err, ErrStaleMigration) {
					t.Fatalf("mode mutation recovery=%v", err)
				}
				after, statErr := os.Stat(full)
				if statErr != nil || after.Mode().Perm() != mutated {
					t.Fatalf("recovery changed user mode=%v err=%v", after, statErr)
				}
			})
		}
	}
}

func TestRecoveryRejectsNonPrivateMachineDirectoriesWithoutRepair(t *testing.T) {
	tests := []struct {
		stage    Stage
		rootKind string
		relative func(MigrationPlan) string
	}{
		{StageBackupComplete, "data", func(MigrationPlan) string { return migrationJournalDir }},
		{StageBackupComplete, "project", func(MigrationPlan) string { return "docs/session-review/.session-reviewer" }},
		{StageBackupComplete, "project", func(MigrationPlan) string { return migrationBackupRoot }},
		{StageBackupComplete, "project", func(plan MigrationPlan) string { return plan.BackupRoot }},
		{StageBackupComplete, "project", func(plan MigrationPlan) string { return plan.BackupRoot + "/objects" }},
		{StageLegacyMoved, "project", func(plan MigrationPlan) string { return plan.BackupRoot + "/archive" }},
	}
	for _, test := range tests {
		t.Run(test.rootKind+"/"+filepath.Base(test.relative(MigrationPlan{BackupRoot: "backup"})), func(t *testing.T) {
			fixture := newLegacyMigrationFixture(t)
			plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			stop := errors.New("stop after stage")
			if err := applyMigrationWithHook(plan, func(current Stage) error {
				if current == test.stage {
					return stop
				}
				return nil
			}); !errors.Is(err, stop) {
				t.Fatalf("stage interruption=%v", err)
			}
			root := fixture.project
			if test.rootKind == "data" {
				root = fixture.data
			}
			full := filepath.Join(root, filepath.FromSlash(test.relative(plan)))
			if err := os.Chmod(full, 0o755); err != nil {
				t.Fatal(err)
			}
			before := fixture.snapshot()
			err = RecoverMigration(fixture.project, fixture.projectInfo, fixture.data)
			if err == nil {
				t.Fatal("non-private machine directory was accepted")
			}
			if after := fixture.snapshot(); !reflect.DeepEqual(before, after) {
				t.Fatal("recovery wrote after directory mode became unsafe")
			}
			info, statErr := os.Stat(full)
			if statErr != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("recovery silently repaired mode=%v err=%v", info, statErr)
			}
		})
	}
}

func TestRecoveryRejectsUnexpectedBackupNamespaceEntries(t *testing.T) {
	tests := []struct {
		stage    Stage
		relative func(MigrationPlan) string
	}{
		{StageBackupComplete, func(plan MigrationPlan) string { return plan.BackupRoot + "/unexpected.bin" }},
		{StageBackupComplete, func(plan MigrationPlan) string { return plan.BackupRoot + "/objects/extra" }},
		{StageLegacyMoved, func(plan MigrationPlan) string { return plan.BackupRoot + "/archive/unexpected.md" }},
	}
	for index, test := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			fixture := newLegacyMigrationFixture(t)
			plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			stop := errors.New("stop after stage")
			if err := applyMigrationWithHook(plan, func(current Stage) error {
				if current == test.stage {
					return stop
				}
				return nil
			}); !errors.Is(err, stop) {
				t.Fatalf("stage interruption=%v", err)
			}
			extra := filepath.Join(fixture.project, filepath.FromSlash(test.relative(plan)))
			if err := os.MkdirAll(filepath.Dir(extra), 0o700); err != nil {
				t.Fatal(err)
			}
			userBytes := []byte("unexpected-user-backup-entry")
			if err := os.WriteFile(extra, userBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			err = RecoverMigration(fixture.project, fixture.projectInfo, fixture.data)
			if err == nil {
				t.Fatal("unexpected backup namespace entry was accepted")
			}
			got, readErr := os.ReadFile(extra)
			if readErr != nil || !bytes.Equal(got, userBytes) {
				t.Fatalf("unexpected entry changed: got=%q err=%v", got, readErr)
			}
		})
	}
}

func TestMigrationRejectsMixedStateWithoutMatchingJournal(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	projected, err := ProjectLegacy(mustLoadLegacyForMigrationTest(t, fixture.project))
	if err != nil {
		t.Fatal(err)
	}
	writePlan, err := Render(fixture.project, projected)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range writePlan.Files {
		full := filepath.Join(fixture.project, filepath.FromSlash(file.RelativePath))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, file.Data, file.Perm); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now); err == nil || !strings.Contains(err.Error(), "no matching migration journal") {
		t.Fatalf("plan mixed error=%v", err)
	}
	if err := RecoverMigration(fixture.project, fixture.projectInfo, fixture.data); err == nil || !strings.Contains(err.Error(), "no matching migration journal") {
		t.Fatalf("recover mixed error=%v", err)
	}
}

func TestMigrationRejectsUnsafePortableInventoryAndBudgets(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, fixture legacyMigrationFixture)
	}{
		{"case collision", func(t *testing.T, fixture legacyMigrationFixture) {
			directory := filepath.Join(fixture.project, "docs", "session-review", "portable")
			if err := os.MkdirAll(directory, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"Case.bin", "case.bin"} {
				if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"NFC collision", func(t *testing.T, fixture legacyMigrationFixture) {
			directory := filepath.Join(fixture.project, "docs", "session-review", "portable")
			if err := os.MkdirAll(directory, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"Caf\u00e9.bin", "Cafe\u0301.bin"} {
				if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"symlink", func(t *testing.T, fixture legacyMigrationFixture) {
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(fixture.project, "docs", "session-review", "redirect.bin")); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
		}},
		{"64 MiB", func(t *testing.T, fixture legacyMigrationFixture) {
			large := filepath.Join(fixture.project, "docs", "session-review", "large.bin")
			file, err := os.OpenFile(large, os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(maxMigrationBytes + 1); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLegacyMigrationFixture(t)
			test.setup(t, fixture)
			if test.name == "case collision" || test.name == "NFC collision" {
				entries, err := os.ReadDir(filepath.Join(fixture.project, "docs", "session-review", "portable"))
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) < 2 {
					t.Skip("test filesystem collapses the two portable-collision spellings")
				}
			}
			if _, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now); err == nil {
				t.Fatal("unsafe or oversized migration inventory was accepted")
			}
		})
	}
}

func TestMigrationPortableInventoryKeyCollapsesCaseAndNFC(t *testing.T) {
	for _, pair := range [][2]string{
		{"docs/session-review/portable/Case.bin", "docs/session-review/portable/case.bin"},
		{"docs/session-review/portable/Caf\u00e9.bin", "docs/session-review/portable/Cafe\u0301.bin"},
	} {
		first, err := migrationPortableInventoryKey(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		second, err := migrationPortableInventoryKey(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if first != second {
			t.Fatalf("portable keys differ: %q=%q %q=%q", pair[0], first, pair[1], second)
		}
	}
}

func TestMigrationRejectsProjectRootReplacement(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	moved := fixture.project + ".moved"
	if err := os.Rename(fixture.project, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.project, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyMigration(plan); err == nil || !strings.Contains(err.Error(), "expected project root") {
		t.Fatalf("replacement error=%v", err)
	}
}

func TestRecoverMigrationRejectsPublicBackupObjectBeforeV2Writes(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop after backup")
	if err := applyMigrationWithHook(plan, func(stage Stage) error {
		if stage == StageBackupComplete {
			return stop
		}
		return nil
	}); !errors.Is(err, stop) {
		t.Fatalf("injected error=%v", err)
	}
	objects, err := filepath.Glob(filepath.Join(fixture.project, filepath.FromSlash(plan.BackupRoot), "objects", "*"))
	if err != nil || len(objects) == 0 {
		t.Fatalf("backup objects=%v err=%v", objects, err)
	}
	if err := os.Chmod(objects[0], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RecoverMigration(fixture.project, fixture.projectInfo, fixture.data); err == nil || !strings.Contains(err.Error(), "backup object") {
		t.Fatalf("public backup recovery error=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.project, filepath.FromSlash(ReviewRelativePath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery wrote v2 before rejecting public backup: %v", err)
	}
}

func TestMigrationDoesNotTightenExistingHumanDirectoryModes(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	for _, relative := range []string{"docs", "docs/session-review"} {
		if err := os.Chmod(filepath.Join(fixture.project, filepath.FromSlash(relative)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyMigration(plan); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"docs", "docs/session-review"} {
		info, err := os.Stat(filepath.Join(fixture.project, filepath.FromSlash(relative)))
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("directory %s mode=%o err=%v", relative, info.Mode().Perm(), err)
		}
	}
	for _, relative := range []string{"docs/session-review/.session-reviewer", "docs/session-review/.session-reviewer/backups"} {
		info, err := os.Stat(filepath.Join(fixture.project, filepath.FromSlash(relative)))
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("private directory %s mode=%o err=%v", relative, info.Mode().Perm(), err)
		}
	}
}

func TestRecoverMigrationConvergesPartialV2WriteAndPartialLegacyArchive(t *testing.T) {
	t.Run("partial v2 write", func(t *testing.T) {
		fixture := newLegacyMigrationFixture(t)
		plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
		if err != nil {
			t.Fatal(err)
		}
		stop := errors.New("stop after backup")
		if err := applyMigrationWithHook(plan, func(stage Stage) error {
			if stage == StageBackupComplete {
				return stop
			}
			return nil
		}); !errors.Is(err, stop) {
			t.Fatalf("injected error=%v", err)
		}
		file := plan.Writes[0]
		full := filepath.Join(fixture.project, filepath.FromSlash(file.RelativePath))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, file.Data, file.Perm); err != nil {
			t.Fatal(err)
		}
		if err := RecoverMigration(fixture.project, fixture.projectInfo, fixture.data); err != nil {
			t.Fatal(err)
		}
		assertV2OnlyVisible(t, fixture.project)
	})

	t.Run("partial legacy archive", func(t *testing.T) {
		fixture := newLegacyMigrationFixture(t)
		plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
		if err != nil {
			t.Fatal(err)
		}
		stop := errors.New("stop after v2")
		if err := applyMigrationWithHook(plan, func(stage Stage) error {
			if stage == StageV2Written {
				return stop
			}
			return nil
		}); !errors.Is(err, stop) {
			t.Fatalf("injected error=%v", err)
		}
		archive := filepath.Join(fixture.project, filepath.FromSlash(plan.BackupRoot), "archive")
		if err := os.MkdirAll(archive, 0o700); err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(fixture.project, "docs", "session-review", "project-overview.md")
		if err := os.Rename(source, filepath.Join(archive, "project-overview.md")); err != nil {
			t.Fatal(err)
		}
		if err := RecoverMigration(fixture.project, fixture.projectInfo, fixture.data); err != nil {
			t.Fatal(err)
		}
		assertV2OnlyVisible(t, fixture.project)
		assertBackupManifestComplete(t, fixture.project)
	})
}

func mustLoadLegacyForMigrationTest(t *testing.T, root string) ledger.State {
	t.Helper()
	state, err := ledger.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

type legacyMigrationFixture struct {
	project     string
	data        string
	projectInfo os.FileInfo
	now         time.Time
}

func newLegacyMigrationFixture(t *testing.T) legacyMigrationFixture {
	t.Helper()
	project := t.TempDir()
	data := t.TempDir()
	writeLegacyOverview(t, project)
	legacy, err := ledger.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	current := ledger.CurrentState{
		ProjectID:       legacy.ProjectID,
		Revision:        1,
		Goal:            "migrate the accepted legacy review",
		LastVerified:    "legacy fixture loaded",
		Branch:          "codex/session-reviewer-v2",
		Blockers:        []string{},
		OpenRisks:       []string{},
		NextAction:      "preview migration",
		FirstInspection: "docs/session-review/project-overview.md",
		LastUpdated:     "2026-08-25T12:00:00Z",
		SourceSessions:  []string{},
		Evidence:        []ledger.EvidenceRef{},
	}
	writePlan, err := ledger.Render(legacy, ledger.ChangeSet{Current: &current})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(writePlan); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(project)
	if err != nil {
		t.Fatal(err)
	}
	return legacyMigrationFixture{
		project:     project,
		data:        data,
		projectInfo: info,
		now:         time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
	}
}

func (fixture legacyMigrationFixture) snapshot() []string {
	var values []string
	for _, root := range []string{fixture.project, fixture.data} {
		_ = filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relative, _ := filepath.Rel(root, name)
			if entry.IsDir() {
				values = append(values, root+":"+filepath.ToSlash(relative)+"/")
				return nil
			}
			body, readErr := os.ReadFile(name)
			if readErr != nil {
				return readErr
			}
			values = append(values, root+":"+filepath.ToSlash(relative)+":"+string(body))
			return nil
		})
	}
	sort.Strings(values)
	return values
}

func assertV2OnlyVisible(t *testing.T, project string) {
	t.Helper()
	if version, err := DetectVersion(project); err != nil || version != VersionV2 {
		t.Fatalf("version=%q err=%v", version, err)
	}
	for _, relative := range []string{ReviewRelativePath, HistoryRelativePath, MachineLedgerRelativePath} {
		if info, err := os.Lstat(filepath.Join(project, filepath.FromSlash(relative))); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("v2 target %s info=%v err=%v", relative, info, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(project, "docs", "session-review", "project-overview.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy overview remains: %v", err)
	}
}

func assertBackupManifestComplete(t *testing.T, project string) {
	t.Helper()
	pattern := filepath.Join(project, "docs", "session-review", ".session-reviewer", "backups", "*", "manifest.json")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) != 1 {
		t.Fatalf("backup manifests=%v err=%v", matches, err)
	}
	if info, err := os.Stat(matches[0]); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatalf("manifest info=%v err=%v", info, err)
	}
}
