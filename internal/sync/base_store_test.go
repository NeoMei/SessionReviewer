package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
)

var fixedTime = time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
var t0 = fixedTime

func TestBaseStoreCommitUsesCASAndRecoversAtomicBackupReadOnly(t *testing.T) {
	data := t.TempDir()
	root, err := os.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store := BaseStore{Root: root}
	first := validBaseRecord("decision-1", "decisions/decision-1.md", "one")
	if err := store.Commit("", first); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit("stale", first); !errors.Is(err, ErrStaleBase) {
		t.Fatalf("err=%v", err)
	}
	primary := filepath.Join(data, baseRecordPath("decision-1"))
	backup := atomicfile.BackupPath(primary)
	if err := os.Rename(primary, backup); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Load("decision-1")
	if err != nil || !found || !reflect.DeepEqual(got, first) {
		t.Fatalf("got=%+v found=%v err=%v", got, found, err)
	}
	if _, err := os.Stat(primary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read repaired primary: %v", err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup removed: %v", err)
	}
}

func TestBaseStoreRejectsInvalidSelfCertification(t *testing.T) {
	data := t.TempDir()
	root, err := os.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store := BaseStore{Root: root}

	tests := map[string]func(*BaseRecord){
		"version":              func(record *BaseRecord) { record.Version = 2 },
		"entity id":            func(record *BaseRecord) { record.EntityID = "" },
		"unsafe entity id":     func(record *BaseRecord) { record.EntityID = "../decision" },
		"absolute path":        func(record *BaseRecord) { record.RelativePath = "/decision.md" },
		"parent path":          func(record *BaseRecord) { record.RelativePath = "decisions/../decision.md" },
		"UNC path":             func(record *BaseRecord) { record.RelativePath = `\\server\share\decision.md` },
		"device path":          func(record *BaseRecord) { record.RelativePath = `\\?\C:\decision.md` },
		"non Markdown path":    func(record *BaseRecord) { record.RelativePath = "decisions/decision.txt" },
		"content hash":         func(record *BaseRecord) { record.ContentHash = hash("other") },
		"uppercase hash":       func(record *BaseRecord) { record.ContentHash = strings.ToUpper(record.ContentHash) },
		"empty project hash":   func(record *BaseRecord) { record.ProjectHash = "" },
		"wrong project hash":   func(record *BaseRecord) { record.ProjectHash = hash("other") },
		"empty vault hash":     func(record *BaseRecord) { record.VaultHash = "" },
		"wrong vault hash":     func(record *BaseRecord) { record.VaultHash = hash("other") },
		"zero synchronized at": func(record *BaseRecord) { record.SyncedAt = time.Time{} },
		"invalid UTF-8": func(record *BaseRecord) {
			record.Content = []byte{0xff}
			record.ContentHash = hashBytes(record.Content)
			record.ProjectHash = record.ContentHash
			record.VaultHash = record.ContentHash
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			record := validBaseRecord("decision-1", "decisions/decision-1.md", "one")
			mutate(&record)
			if err := store.Commit("", record); err == nil {
				t.Fatalf("record accepted: %+v", record)
			}
		})
	}
}

func TestBaseStoreRejectsMismatchedStoredIdentityAndTrailingJSON(t *testing.T) {
	for _, name := range []string{"mismatched id", "trailing JSON", "unknown field"} {
		t.Run(name, func(t *testing.T) {
			data := t.TempDir()
			mergeDir := filepath.Join(data, "merge-bases")
			if err := os.Mkdir(mergeDir, 0o700); err != nil {
				t.Fatal(err)
			}
			record := validBaseRecord("decision-1", "decisions/decision-1.md", "one")
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "mismatched id":
				record.EntityID = "decision-2"
				encoded, _ = json.Marshal(record)
			case "trailing JSON":
				encoded = append(encoded, []byte("\n{}\n")...)
			case "unknown field":
				encoded[len(encoded)-1] = ','
				encoded = append(encoded, []byte(`"extra":true}`)...)
			}
			if err := os.WriteFile(filepath.Join(data, baseRecordPath("decision-1")), encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(data)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if _, _, err := (BaseStore{Root: root}).Load("decision-1"); err == nil {
				t.Fatal("corrupt record accepted")
			}
		})
	}
}

func TestBaseStoreRejectsOversizeSymlinkAndNoncanonicalStateName(t *testing.T) {
	t.Run("oversize", func(t *testing.T) {
		data := t.TempDir()
		if err := os.Mkdir(filepath.Join(data, "merge-bases"), 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(data, baseRecordPath("decision-1"))
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate((8 << 20) + 1); err != nil {
			t.Fatal(err)
		}
		file.Close()
		assertBaseLoadFails(t, data, "decision-1")
	})
	t.Run("symlink", func(t *testing.T) {
		data := t.TempDir()
		if err := os.Mkdir(filepath.Join(data, "merge-bases"), 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.json")
		encoded, _ := json.Marshal(validBaseRecord("decision-1", "decisions/decision-1.md", "one"))
		if err := os.WriteFile(outside, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(data, baseRecordPath("decision-1"))); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		assertBaseLoadFails(t, data, "decision-1")
	})
	t.Run("noncanonical case", func(t *testing.T) {
		data := t.TempDir()
		root, err := os.OpenRoot(data)
		if err != nil {
			t.Fatal(err)
		}
		store := BaseStore{Root: root}
		if err := store.Commit("", validBaseRecord("decision-1", "decisions/decision-1.md", "one")); err != nil {
			t.Fatal(err)
		}
		root.Close()
		canonical := filepath.Join(data, baseRecordPath("decision-1"))
		upper := filepath.Join(filepath.Dir(canonical), strings.ToUpper(filepath.Base(canonical)))
		if canonical == upper {
			t.Fatal("digest unexpectedly lacks letters")
		}
		if err := os.Rename(canonical, upper); err != nil {
			t.Fatal(err)
		}
		assertBaseLoadFails(t, data, "decision-1")
	})
}

func TestBaseStoreUsesPrivateModesAndDeterministicList(t *testing.T) {
	data := t.TempDir()
	root, err := os.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store := BaseStore{Root: root}
	for _, record := range []BaseRecord{
		validBaseRecord("decision-z", "decisions/z.md", "z"),
		validBaseRecord("decision-a", "decisions/a.md", "a"),
		validBaseRecord("decision-m", "decisions/m.md", "m"),
	} {
		if err := store.Commit("", record); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(got))
	for index, record := range got {
		ids[index] = record.EntityID
	}
	want := []string{"decision-a", "decision-m", "decision-z"}
	if !reflect.DeepEqual(ids, want) || !sort.StringsAreSorted(ids) {
		t.Fatalf("ids=%v", ids)
	}
	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(filepath.Join(data, "merge-bases"))
		if err != nil || dirInfo.Mode().Perm() != 0o700 {
			t.Fatalf("directory mode=%v err=%v", dirInfo.Mode().Perm(), err)
		}
		for _, record := range got {
			info, err := os.Stat(filepath.Join(data, baseRecordPath(record.EntityID)))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("record=%s mode=%v err=%v", record.EntityID, info.Mode().Perm(), err)
			}
		}
	}
}

func TestBaseStoreLoadListAndCommitFailClosedOnUnrelatedInvalidState(t *testing.T) {
	for _, name := range []string{"invalid filename", "corrupt canonical record"} {
		t.Run(name, func(t *testing.T) {
			data := t.TempDir()
			root, err := os.OpenRoot(data)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			store := BaseStore{Root: root}
			first := validBaseRecord("decision-1", "decisions/decision-1.md", "one")
			if err := store.Commit("", first); err != nil {
				t.Fatal(err)
			}
			invalidName := strings.ToUpper(baseRecordName("decision-2"))
			invalidContent, _ := json.Marshal(validBaseRecord("decision-2", "decisions/decision-2.md", "two"))
			if name == "corrupt canonical record" {
				invalidName = baseRecordName("decision-2")
				invalidContent = []byte("corrupt unrelated state")
			}
			if err := root.WriteFile(filepath.ToSlash(filepath.Join(baseDirectoryName, invalidName)), invalidContent, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.Load(first.EntityID); err == nil {
				t.Fatal("Load ignored unrelated invalid state")
			}
			if _, err := store.List(); err == nil {
				t.Fatal("List ignored unrelated invalid state")
			}
			if err := store.Commit(first.ContentHash, validBaseRecord(first.EntityID, first.RelativePath, "next")); err == nil {
				t.Fatal("Commit ignored unrelated invalid state")
			}
		})
	}
}

func TestBaseStoreListAcceptsBackupOnlyRecord(t *testing.T) {
	data := t.TempDir()
	root, err := os.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store := BaseStore{Root: root}
	record := validBaseRecord("decision-1", "decisions/decision-1.md", "one")
	if err := store.Commit("", record); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(data, baseRecordPath(record.EntityID))
	if err := os.Rename(primary, atomicfile.BackupPath(primary)); err != nil {
		t.Fatal(err)
	}
	got, err := store.List()
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], record) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestBaseStoreConcurrentCASAllowsOneWinner(t *testing.T) {
	data := t.TempDir()
	root, err := os.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store := BaseStore{Root: root}
	first := validBaseRecord("decision-1", "decisions/decision-1.md", "one")
	if err := store.Commit("", first); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, content := range []string{"two", "three"} {
		content := content
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- store.Commit(first.ContentHash, validBaseRecord("decision-1", "decisions/decision-1.md", content))
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	var success, stale int
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrStaleBase):
			stale++
		default:
			t.Fatalf("unexpected err=%v", err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("success=%d stale=%d", success, stale)
	}
}

func TestBaseStoreCASIsCrossProcess(t *testing.T) {
	if os.Getenv("SESSION_REVIEWER_BASE_HELPER") == "1" {
		baseStoreProcessHelper()
		return
	}
	data := t.TempDir()
	root, err := os.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	store := BaseStore{Root: root}
	first := validBaseRecord("decision-1", "decisions/decision-1.md", "one")
	if err := store.Commit("", first); err != nil {
		t.Fatal(err)
	}
	root.Close()

	commands := make([]*exec.Cmd, 0, 2)
	for _, content := range []string{"two", "three"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBaseStoreCASIsCrossProcess$")
		cmd.Env = append(os.Environ(),
			"SESSION_REVIEWER_BASE_HELPER=1",
			"SESSION_REVIEWER_BASE_ROOT="+data,
			"SESSION_REVIEWER_BASE_EXPECTED="+first.ContentHash,
			"SESSION_REVIEWER_BASE_CONTENT="+content,
		)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	if err := os.WriteFile(filepath.Join(data, "base-helper-start"), []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	exitCodes := make([]int, 0, 2)
	for _, cmd := range commands {
		err := cmd.Wait()
		if err == nil {
			exitCodes = append(exitCodes, 0)
			continue
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatal(err)
		}
		exitCodes = append(exitCodes, exitErr.ExitCode())
	}
	sort.Ints(exitCodes)
	if !reflect.DeepEqual(exitCodes, []int{0, 3}) {
		t.Fatalf("exit codes=%v", exitCodes)
	}
}

func TestBaseStoreCorruptPrimaryUsesValidBackupWithoutRepair(t *testing.T) {
	data := t.TempDir()
	root, err := os.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store := BaseStore{Root: root}
	record := validBaseRecord("decision-1", "decisions/decision-1.md", "one")
	if err := store.Commit("", record); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(data, baseRecordPath(record.EntityID))
	backup := atomicfile.BackupPath(primary)
	valid, err := os.ReadFile(primary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("corrupt-primary-canary")
	if err := os.WriteFile(primary, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Load(record.EntityID)
	if err != nil || !found || !reflect.DeepEqual(got, record) {
		t.Fatalf("got=%+v found=%v err=%v", got, found, err)
	}
	if after, _ := os.ReadFile(primary); !reflect.DeepEqual(after, corrupt) {
		t.Fatalf("primary repaired: %q", after)
	}
	if after, _ := os.ReadFile(backup); !reflect.DeepEqual(after, valid) {
		t.Fatal("backup changed during read-only recovery")
	}
}

func TestBaseStoreReadRejectsStateReplacementAfterContentRead(t *testing.T) {
	data := t.TempDir()
	root, err := os.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store := BaseStore{Root: root}
	record := validBaseRecord("decision-1", "decisions/decision-1.md", "one")
	if err := store.Commit("", record); err != nil {
		t.Fatal(err)
	}
	bases, err := root.OpenRoot(baseDirectoryName)
	if err != nil {
		t.Fatal(err)
	}
	defer bases.Close()
	name := baseRecordName(record.EntityID)
	before, err := bases.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readBaseRecordWithHook(bases, name, record.EntityID, before, func() error {
		if err := bases.Rename(name, "moved.json"); err != nil {
			return err
		}
		encoded, err := json.Marshal(validBaseRecord(record.EntityID, record.RelativePath, "replacement"))
		if err != nil {
			return err
		}
		return bases.WriteFile(name, encoded, 0o600)
	})
	if err == nil {
		t.Fatal("state namespace replacement after read was accepted")
	}
}

func TestBaseStoreRevalidatesPinnedDirectoryNamespace(t *testing.T) {
	data := t.TempDir()
	root, err := os.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.Mkdir(baseDirectoryName, 0o700); err != nil {
		t.Fatal(err)
	}
	bases, err := root.OpenRoot(baseDirectoryName)
	if err != nil {
		t.Fatal(err)
	}
	defer bases.Close()
	if err := root.Rename(baseDirectoryName, "moved-bases"); err != nil {
		t.Fatal(err)
	}
	if err := root.Mkdir(baseDirectoryName, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyBaseDirectoryIdentity(root, bases); err == nil {
		t.Fatal("replacement merge-base directory accepted")
	}
}

func TestBaseStoreRejectsWhenPrimaryAndBackupAreCorrupt(t *testing.T) {
	data := t.TempDir()
	if err := os.Mkdir(filepath.Join(data, "merge-bases"), 0o700); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(data, baseRecordPath("decision-1"))
	if err := os.WriteFile(primary, []byte("bad primary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(atomicfile.BackupPath(primary), []byte("bad backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertBaseLoadFails(t, data, "decision-1")
}

func TestBaseRecordPathUsesEntityIDDigest(t *testing.T) {
	want := filepath.ToSlash(filepath.Join("merge-bases", hash("decision-1")+".json"))
	if got := baseRecordPath("decision-1"); got != want {
		t.Fatalf("path=%q want=%q", got, want)
	}
}

func validBaseRecord(entityID, relativePath, content string) BaseRecord {
	digest := hash(content)
	return BaseRecord{
		Version:      1,
		EntityID:     entityID,
		RelativePath: relativePath,
		ContentHash:  digest,
		ProjectHash:  digest,
		VaultHash:    digest,
		Content:      []byte(content),
		SyncedAt:     fixedTime,
	}
}

func hash(value string) string {
	return hashBytes([]byte(value))
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func assertBaseLoadFails(t *testing.T, data, entityID string) {
	t.Helper()
	root, err := os.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, _, err := (BaseStore{Root: root}).Load(entityID); err == nil {
		t.Fatal("Load accepted unsafe or corrupt state")
	}
}

func baseStoreProcessHelper() {
	data := os.Getenv("SESSION_REVIEWER_BASE_ROOT")
	expected := os.Getenv("SESSION_REVIEWER_BASE_EXPECTED")
	content := os.Getenv("SESSION_REVIEWER_BASE_CONTENT")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(data, "base-helper-start")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			os.Exit(5)
		}
		time.Sleep(time.Millisecond)
	}
	root, err := os.OpenRoot(data)
	if err != nil {
		os.Exit(4)
	}
	defer root.Close()
	err = (BaseStore{Root: root}).Commit(expected, validBaseRecord("decision-1", "decisions/decision-1.md", content))
	if err == nil {
		os.Exit(0)
	}
	if errors.Is(err, ErrStaleBase) {
		os.Exit(3)
	}
	os.Exit(4)
}
