package reviewv2

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
)

func TestMigrationJournalIsContentFreeAndRejectsBackupCollision(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	projectKey := plan.projectKey
	journalName := filepath.Base(migrationJournalRelative(projectKey))
	directory := filepath.Join(fixture.data, migrationJournalDir)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	collision := filepath.Join(directory, atomicfile.BackupPath(journalName))
	if err := os.WriteFile(collision, []byte("untrusted recovery alias"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(collision)
	if err != nil {
		t.Fatal(err)
	}
	err = applyMigrationWithHook(plan, func(stage Stage) error {
		if stage == StageBackupComplete {
			return errors.New("stop after journal publication")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "journal") {
		t.Fatalf("backup collision err=%v", err)
	}
	after, readErr := os.ReadFile(collision)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("journal collision was overwritten: before=%q after=%q err=%v", before, after, readErr)
	}
	if _, statErr := os.Lstat(filepath.Join(directory, journalName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("primary journal was written beside collision: %v", statErr)
	}
}

func TestMigrationJournalRejectsRecoveryInventoryBeyondLedgerBudgets(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := journalFromMigrationPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	emptyHash := hashPrefixed(nil)
	tooMany := journal
	tooMany.VisibleInventory = make([]migrationJournalEntry, maxMigrationFiles+1)
	for index := range tooMany.VisibleInventory {
		tooMany.VisibleInventory[index] = migrationJournalEntry{
			RelativePath: fmt.Sprintf("docs/session-review/extra/file-%04d.bin", index),
			Kind:         "file", SHA256: emptyHash, Mode: 0o644,
		}
	}
	if err := validateMigrationJournal(tooMany); err == nil {
		t.Fatal("journal accepted more than 4096 recovery files")
	}
	tooLarge := journal
	tooLarge.VisibleInventory = append([]migrationJournalEntry(nil), journal.VisibleInventory...)
	tooLarge.VisibleInventory = append(tooLarge.VisibleInventory, migrationJournalEntry{
		RelativePath: "docs/session-review/extra/too-large.bin", Kind: "file",
		SHA256: emptyHash, Size: maxMigrationBytes + 1, Mode: 0o644,
	})
	if err := validateMigrationJournal(tooLarge); err == nil {
		t.Fatal("journal accepted recovery bytes beyond 64 MiB")
	}
}

func TestMigrationJournalContainsHashesAndPathsButNoDocumentContent(t *testing.T) {
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
		t.Fatalf("apply error=%v", err)
	}
	projectKey := plan.projectKey
	body, err := os.ReadFile(filepath.Join(fixture.data, filepath.FromSlash(migrationJournalRelative(projectKey))))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"migrate the accepted legacy review", "legacy fixture loaded", "preview migration"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("journal contains legacy content %q: %s", forbidden, body)
		}
	}
	for _, required := range []string{"manifest_sha256", "visible_inventory", "sha256:"} {
		if !strings.Contains(string(body), required) {
			t.Fatalf("journal lacks %q: %s", required, body)
		}
	}
}
