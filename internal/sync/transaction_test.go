package sync

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
)

func TestTransactionJournalRoundTripsEveryKindAndStage(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store := TransactionStore{Root: root}
	kinds := []TransactionKind{TxnEntitySync, TxnConflictNote, TxnResolution}
	stages := []TransactionStage{TxnPlanned, TxnProjectWritten, TxnVaultWritten, TxnBaseCommitted}
	for _, kind := range kinds {
		for index, stage := range stages {
			t.Run(string(kind)+"/"+string(stage), func(t *testing.T) {
				want := validTransaction(kind, stage, "decision-1")
				want.UpdatedAt = want.UpdatedAt.Add(time.Duration(index) * time.Second)
				if err := store.Save(want); err != nil {
					t.Fatal(err)
				}
				got, found, err := store.Load("decision-1")
				if err != nil || !found || !reflect.DeepEqual(got, want) {
					t.Fatalf("got=%+v found=%v err=%v", got, found, err)
				}
			})
		}
		if err := store.Remove("decision-1"); err != nil {
			t.Fatal(err)
		}
		if _, found, err := store.Load("decision-1"); err != nil || found {
			t.Fatalf("found after remove=%v err=%v", found, err)
		}
	}
	info, err := os.Stat(filepath.Join(rootPath, transactionDirectoryName))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode=%o", info.Mode().Perm())
	}
}

func TestDerivedTransactionJournalIsContentFreeAndStageChecked(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store := TransactionStore{Root: root}
	txn := Transaction{
		Version: 1, Kind: TxnDerivedPublish, EntityID: derivedTransactionID,
		DesiredHash: strings.Repeat("a", 64), ExpectedBaseHash: strings.Repeat("b", 64),
		Stage: TxnPlanned, UpdatedAt: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
	}
	if err := store.Save(txn); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(rootPath, transactionRecordPath(txn.Kind, txn.EntityID)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("project-overview")) || bytes.Contains(body, []byte("快速理解")) {
		t.Fatalf("journal contains derived content: %s", body)
	}
	bad := txn
	bad.EntityID = "other"
	if err := store.Save(bad); err == nil {
		t.Fatal("invalid derived transaction identity was accepted")
	}
	bad = txn
	bad.Stage = TxnVaultWritten
	if err := store.Save(bad); err == nil {
		t.Fatal("derived transaction skipped a stage")
	}
	bad = txn
	bad.ExpectedProjectHash = strings.Repeat("c", 64)
	if err := store.Save(bad); err == nil {
		t.Fatal("derived transaction accepted a target preimage hash")
	}
}

func TestTransactionStoreAdvancesSequentiallyAndListsDeterministically(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store := TransactionStore{Root: root}
	for _, id := range []string{"decision-z", "decision-a"} {
		txn := validTransaction(TxnEntitySync, TxnPlanned, id)
		if err := store.Save(txn); err != nil {
			t.Fatal(err)
		}
		if id == "decision-z" {
			txn.Stage = TxnProjectWritten
			txn.UpdatedAt = txn.UpdatedAt.Add(time.Second)
			if err := store.Save(txn); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].EntityID != "decision-a" || got[1].EntityID != "decision-z" || got[1].Stage != TxnProjectWritten {
		t.Fatalf("transactions=%+v", got)
	}
	got[0].EntityID = "mutated"
	again, err := store.List()
	if err != nil || again[0].EntityID != "decision-a" {
		t.Fatalf("defensive list=%+v err=%v", again, err)
	}
}

func TestTransactionStoreAllowsEmptyExpectedBaseForFirstSync(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	txn := validTransaction(TxnEntitySync, TxnPlanned, "decision-new")
	txn.ExpectedBaseHash = ""
	store := TransactionStore{Root: root}
	if err := store.Save(txn); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Load(txn.EntityID)
	if err != nil || !found || !reflect.DeepEqual(got, txn) {
		t.Fatalf("got=%+v found=%v err=%v", got, found, err)
	}
	txn.Stage = TxnProjectWritten
	txn.UpdatedAt = txn.UpdatedAt.Add(time.Second)
	if err := store.Save(txn); err != nil {
		t.Fatalf("advance first-sync journal with empty base: %v", err)
	}
}

func TestTransactionStoreRequiresPlannedAsFirstPersistedStage(t *testing.T) {
	for _, stage := range []TransactionStage{TxnProjectWritten, TxnVaultWritten, TxnBaseCommitted} {
		t.Run(string(stage), func(t *testing.T) {
			rootPath := t.TempDir()
			root, err := os.OpenRoot(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			txn := validTransaction(TxnEntitySync, stage, "decision-1")
			if err := (TransactionStore{Root: root}).Save(txn); err == nil {
				t.Fatal("accepted non-planned first transaction stage")
			}
			if _, err := os.Stat(filepath.Join(rootPath, transactionDirectoryName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("transaction directory created: %v", err)
			}
		})
	}
}

func TestTransactionStoreRestrictsEmptyExpectedBaseToEntityFirstSync(t *testing.T) {
	for _, kind := range []TransactionKind{TxnConflictNote, TxnResolution} {
		t.Run(string(kind), func(t *testing.T) {
			rootPath := t.TempDir()
			root, err := os.OpenRoot(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			txn := validTransaction(kind, TxnPlanned, "decision-1")
			txn.ExpectedBaseHash = ""
			if err := (TransactionStore{Root: root}).Save(txn); err == nil {
				t.Fatal("accepted empty expected base for non-entity transaction")
			}
			if _, err := os.Stat(filepath.Join(rootPath, transactionDirectoryName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("transaction directory created: %v", err)
			}
		})
	}
}

func TestTransactionStoreRejectsStageSkipAndRegression(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store := TransactionStore{Root: root}
	txn := validTransaction(TxnEntitySync, TxnPlanned, "decision-1")
	if err := store.Save(txn); err != nil {
		t.Fatal(err)
	}
	skipped := txn
	skipped.Stage = TxnVaultWritten
	skipped.UpdatedAt = skipped.UpdatedAt.Add(time.Second)
	if err := store.Save(skipped); err == nil {
		t.Fatal("accepted skipped transaction stage")
	}
	changedPreimage := txn
	changedPreimage.Stage = TxnProjectWritten
	changedPreimage.ExpectedProjectHash = hash("different preimage")
	changedPreimage.UpdatedAt = changedPreimage.UpdatedAt.Add(time.Second)
	if err := store.Save(changedPreimage); err == nil {
		t.Fatal("accepted changed transaction preimage hash")
	}
	txn.Stage = TxnProjectWritten
	txn.UpdatedAt = txn.UpdatedAt.Add(time.Second)
	if err := store.Save(txn); err != nil {
		t.Fatal(err)
	}
	regressed := txn
	regressed.Stage = TxnPlanned
	regressed.UpdatedAt = regressed.UpdatedAt.Add(time.Second)
	if err := store.Save(regressed); err == nil {
		t.Fatal("accepted regressed transaction stage")
	}
}

func TestTransactionStoreRejectsInvalidSchemaBeforeWriting(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Transaction)
	}{
		{name: "version", mutate: func(txn *Transaction) { txn.Version = 2 }},
		{name: "kind", mutate: func(txn *Transaction) { txn.Kind = TransactionKind("other") }},
		{name: "stage", mutate: func(txn *Transaction) { txn.Stage = TransactionStage("other") }},
		{name: "entity", mutate: func(txn *Transaction) { txn.EntityID = "../CANARY" }},
		{name: "desired hash", mutate: func(txn *Transaction) { txn.DesiredHash = "CANARY" }},
		{name: "base hash", mutate: func(txn *Transaction) { txn.ExpectedBaseHash = "CANARY" }},
		{name: "project target hash", mutate: func(txn *Transaction) { txn.ExpectedProjectHash = "CANARY" }},
		{name: "target hash owner", mutate: func(txn *Transaction) { txn.Kind, txn.ExpectedVaultHash = TxnResolution, hash("vault") }},
		{name: "local time", mutate: func(txn *Transaction) { txn.UpdatedAt = txn.UpdatedAt.In(time.FixedZone("local", 0)) }},
		{name: "path key", mutate: func(txn *Transaction) { txn.FromPathKey, txn.ToPathKey = "../CANARY", "decisions/d2.md" }},
		{name: "unpaired path key", mutate: func(txn *Transaction) { txn.FromPathKey = "decisions/d1.md" }},
		{name: "identical path keys", mutate: func(txn *Transaction) { txn.FromPathKey, txn.ToPathKey = "decisions/d1.md", "decisions/d1.md" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rootPath := t.TempDir()
			root, err := os.OpenRoot(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			txn := validTransaction(TxnEntitySync, TxnPlanned, "decision-1")
			test.mutate(&txn)
			if err := (TransactionStore{Root: root}).Save(txn); err == nil || strings.Contains(err.Error(), "CANARY") {
				t.Fatalf("error=%v", err)
			}
			if _, err := os.Stat(filepath.Join(rootPath, transactionDirectoryName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("transaction directory created: %v", err)
			}
		})
	}
}

func TestTransactionJournalIsContentFreePrivateAndNamedByKindAndEntity(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	txn := validTransaction(TxnResolution, TxnPlanned, "decision-1")
	txn.FromPathKey = "decisions/old.md"
	txn.ToPathKey = "decisions/new.md"
	store := TransactionStore{Root: root}
	if err := store.Save(txn); err != nil {
		t.Fatal(err)
	}
	name := transactionRecordName(txn.Kind, txn.EntityID)
	journalPath := filepath.Join(rootPath, transactionDirectoryName, name)
	content, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"CANARY-CONTENT", `"content"`, `"raw_path"`, `"title"`, `"error"`, `"bytes"`} {
		if bytes.Contains(content, []byte(forbidden)) {
			t.Fatalf("journal contains %q: %s", forbidden, content)
		}
	}
	info, err := os.Lstat(journalPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("journal info=%v err=%v", info, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode=%o", info.Mode().Perm())
	}
}

func TestTransactionStoreTreatsCorruptDuplicateAndWrongFilenameAsFatal(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string, Transaction)
	}{
		{name: "corrupt", setup: func(t *testing.T, directory string, txn Transaction) {
			writeTransactionFixture(t, directory, transactionRecordName(txn.Kind, txn.EntityID), []byte(`{"version":1,"CANARY-CONTENT":`))
		}},
		{name: "unknown field", setup: func(t *testing.T, directory string, txn Transaction) {
			content := encodeTransactionFixture(t, txn)
			content = bytes.Replace(content, []byte(`"version": 1`), []byte(`"version": 1, "content": "CANARY-CONTENT"`), 1)
			writeTransactionFixture(t, directory, transactionRecordName(txn.Kind, txn.EntityID), content)
		}},
		{name: "duplicate key", setup: func(t *testing.T, directory string, txn Transaction) {
			content := encodeTransactionFixture(t, txn)
			content = bytes.Replace(content, []byte(`"version": 1`), []byte(`"version": 1, "version": 1`), 1)
			writeTransactionFixture(t, directory, transactionRecordName(txn.Kind, txn.EntityID), content)
		}},
		{name: "wrong filename", setup: func(t *testing.T, directory string, txn Transaction) {
			writeTransactionFixture(t, directory, strings.Repeat("0", 64)+".json", encodeTransactionFixture(t, txn))
		}},
		{name: "duplicate entity", setup: func(t *testing.T, directory string, txn Transaction) {
			writeTransactionFixture(t, directory, transactionRecordName(txn.Kind, txn.EntityID), encodeTransactionFixture(t, txn))
			other := txn
			other.Kind = TxnResolution
			writeTransactionFixture(t, directory, transactionRecordName(other.Kind, other.EntityID), encodeTransactionFixture(t, other))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rootPath := t.TempDir()
			directory := filepath.Join(rootPath, transactionDirectoryName)
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			txn := validTransaction(TxnEntitySync, TxnPlanned, "decision-1")
			test.setup(t, directory, txn)
			before := snapshotDirectory(t, directory)
			root, err := os.OpenRoot(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			store := TransactionStore{Root: root}
			if _, _, err := store.Load(txn.EntityID); err == nil || strings.Contains(err.Error(), "CANARY-CONTENT") {
				t.Fatalf("load error=%v", err)
			}
			if err := store.Save(txn); err == nil || strings.Contains(err.Error(), "CANARY-CONTENT") {
				t.Fatalf("save error=%v", err)
			}
			after := snapshotDirectory(t, directory)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("corrupt journal overwritten: before=%v after=%v", before, after)
			}
		})
	}
}

func TestTransactionStoreRejectsMismatchedRecoveryBackup(t *testing.T) {
	rootPath := t.TempDir()
	directory := filepath.Join(rootPath, transactionDirectoryName)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	primary := validTransaction(TxnEntitySync, TxnProjectWritten, "decision-1")
	name := transactionRecordName(primary.Kind, primary.EntityID)
	writeTransactionFixture(t, directory, name, encodeTransactionFixture(t, primary))
	backup := validTransaction(TxnResolution, TxnPlanned, "decision-2")
	writeTransactionFixture(t, directory, atomicfile.BackupPath(name), encodeTransactionFixture(t, backup))
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, _, err := (TransactionStore{Root: root}).Load(primary.EntityID); err == nil {
		t.Fatal("accepted recovery backup for another transaction")
	}
}

func TestTransactionStoreRejectsRedirectsInsecureModesAndReadTampering(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		rootPath, outside := t.TempDir(), t.TempDir()
		if err := os.Symlink(outside, filepath.Join(rootPath, transactionDirectoryName)); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		if _, err := (TransactionStore{Root: root}).List(); err == nil {
			t.Fatal("accepted redirected transaction directory")
		}
	})
	t.Run("mode", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("portable mode bits do not represent Windows ACLs")
		}
		rootPath := t.TempDir()
		directory := filepath.Join(rootPath, transactionDirectoryName)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		txn := validTransaction(TxnEntitySync, TxnPlanned, "decision-1")
		path := filepath.Join(directory, transactionRecordName(txn.Kind, txn.EntityID))
		if err := os.WriteFile(path, encodeTransactionFixture(t, txn), 0o644); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		if _, _, err := (TransactionStore{Root: root}).Load(txn.EntityID); err == nil {
			t.Fatal("accepted public transaction journal")
		}
	})
	t.Run("tamper", func(t *testing.T) {
		rootPath := t.TempDir()
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		store := TransactionStore{Root: root}
		txn := validTransaction(TxnEntitySync, TxnPlanned, "decision-1")
		if err := store.Save(txn); err != nil {
			t.Fatal(err)
		}
		journalPath := filepath.Join(rootPath, transactionDirectoryName, transactionRecordName(txn.Kind, txn.EntityID))
		_, _, err = store.loadWithReadHook(txn.EntityID, func() error {
			return os.WriteFile(journalPath, append(encodeTransactionFixture(t, txn), ' '), 0o600)
		})
		if err == nil {
			t.Fatal("accepted journal changed during stable read")
		}
	})
}

func validTransaction(kind TransactionKind, stage TransactionStage, entityID string) Transaction {
	return Transaction{
		Version:          1,
		Kind:             kind,
		EntityID:         entityID,
		DesiredHash:      hash("accepted"),
		ExpectedBaseHash: hash("base"),
		Stage:            stage,
		UpdatedAt:        fixedTime,
	}
}

func encodeTransactionFixture(t *testing.T, txn Transaction) []byte {
	t.Helper()
	content, err := json.MarshalIndent(txn, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(content, '\n')
}

func writeTransactionFixture(t *testing.T, directory, name string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func snapshotDirectory(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, entry.Name()+"="+string(content))
	}
	sort.Strings(result)
	return result
}
