package reviewv2

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
)

func TestMigrationJournalRequiresEveryRootAndNestedField(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := journalFromMigrationPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	original, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	assertMutationRejected := func(name string, mutate func(map[string]any)) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(original, &value); err != nil {
				t.Fatal(err)
			}
			mutate(value)
			body, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeMigrationJournal(body); err == nil {
				t.Fatal("journal with omitted field was accepted")
			}
		})
	}
	for _, field := range []string{"version", "project_key", "project_id", "manifest_sha256", "backup_relative", "stage", "planned_at", "project_root", "data_root", "legacy", "writes", "visible_inventory"} {
		field := field
		assertMutationRejected("root/"+field, func(value map[string]any) { delete(value, field) })
	}
	for _, rootField := range []string{"project_root", "data_root"} {
		for _, field := range []string{"canonical_path", "identity"} {
			rootField, field := rootField, field
			assertMutationRejected(rootField+"/"+field, func(value map[string]any) {
				delete(value[rootField].(map[string]any), field)
			})
		}
		for _, field := range []string{"kind", "volume", "file"} {
			rootField, field := rootField, field
			assertMutationRejected(rootField+"/identity/"+field, func(value map[string]any) {
				identity := value[rootField].(map[string]any)["identity"].(map[string]any)
				delete(identity, field)
			})
		}
	}
	for _, collection := range []string{"legacy", "writes"} {
		for _, field := range []string{"relative_path", "sha256", "size", "mode"} {
			collection, field := collection, field
			assertMutationRejected(collection+"/"+field, func(value map[string]any) {
				delete(value[collection].([]any)[0].(map[string]any), field)
			})
		}
	}
	fileIndex := 0
	for index, entry := range journal.VisibleInventory {
		if entry.Kind == "file" {
			fileIndex = index
			break
		}
	}
	for _, field := range []string{"relative_path", "kind", "sha256", "size", "mode"} {
		field := field
		assertMutationRejected("visible_inventory/"+field, func(value map[string]any) {
			delete(value["visible_inventory"].([]any)[fileIndex].(map[string]any), field)
		})
	}
}

func TestMigrationJournalRejectsSemanticTruncationAndStructuralAliases(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := journalFromMigrationPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name   string
		mutate func(*migrationJournal)
	}{
		{"empty legacy", func(value *migrationJournal) { value.Legacy = nil }},
		{"empty inventory", func(value *migrationJournal) { value.VisibleInventory = nil }},
		{"duplicate writes", func(value *migrationJournal) { value.Writes[1].RelativePath = value.Writes[0].RelativePath }},
		{"wrong write set", func(value *migrationJournal) { value.Writes[0].RelativePath = "docs/session-review/other.md" }},
		{"legacy absent from inventory", func(value *migrationJournal) { value.Legacy[0].RelativePath = "docs/session-review/missing.md" }},
		{"manifest mismatch", func(value *migrationJournal) { value.VisibleInventory[0].Mode ^= 1 }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			value := journal
			value.Legacy = append([]migrationJournalFile(nil), journal.Legacy...)
			value.Writes = append([]migrationJournalFile(nil), journal.Writes...)
			value.VisibleInventory = append([]migrationJournalEntry(nil), journal.VisibleInventory...)
			test.mutate(&value)
			if err := validateMigrationJournal(value); err == nil {
				t.Fatal("semantically truncated journal was accepted")
			}
		})
	}
	encoded, err := encodeMigrationJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"unknown":    bytes.Replace(encoded, []byte("{\n"), []byte("{\n  \"unexpected\": true,\n"), 1),
		"case alias": bytes.Replace(encoded, []byte("{\n"), []byte("{\n  \"Stage\": \"planned\",\n"), 1),
		"duplicate":  bytes.Replace(encoded, []byte("  \"stage\": \"planned\","), []byte("  \"stage\": \"planned\",\n  \"stage\": \"planned\","), 1),
		"truncated":  encoded[:len(encoded)/2],
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeMigrationJournal(body); err == nil {
				t.Fatal("corrupt or aliased journal was accepted")
			}
		})
	}
}

func TestRecoveryRejectsPhysicalRootReplacementAtEveryStage(t *testing.T) {
	for _, rootKind := range []string{"project", "data"} {
		for _, stage := range []Stage{StagePlanned, StageBackupComplete, StageV2Written, StageLegacyMoved, StageCommitted} {
			t.Run(rootKind+"/"+string(stage), func(t *testing.T) {
				fixture := newLegacyMigrationFixture(t)
				plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
				if err != nil {
					t.Fatal(err)
				}
				stop := errors.New("stop at persisted stage")
				if err := applyMigrationWithHook(plan, func(current Stage) error {
					if current == stage {
						return stop
					}
					return nil
				}); !errors.Is(err, stop) {
					t.Fatalf("stage interruption=%v", err)
				}
				target := fixture.project
				if rootKind == "data" {
					target = fixture.data
				}
				replaceDirectoryWithCopy(t, target)
				projectInfo := fixture.projectInfo
				if rootKind == "project" {
					projectInfo, err = os.Stat(fixture.project)
					if err != nil {
						t.Fatal(err)
					}
				}
				err = RecoverMigration(fixture.project, projectInfo, fixture.data)
				if !errors.Is(err, ErrStaleMigration) || !strings.Contains(err.Error(), "physical "+rootKind+" root identity") {
					t.Fatalf("replacement recovery error=%v", err)
				}
			})
		}
	}
}

func replaceDirectoryWithCopy(t *testing.T, target string) {
	t.Helper()
	retired := target + "-retired"
	if err := os.Rename(target, retired); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(retired, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(retired, current)
		if err != nil || relative == "." {
			return err
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Mkdir(destination, info.Mode().Perm())
		}
		body, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, body, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

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
