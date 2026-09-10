package opencode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/pathguard"
	_ "modernc.org/sqlite"
)

// Removing WAL replay loses the row; replaying uncommitted frames exposes the
// second row. The writer is a synthetic fixture, never the user's OpenCode DB.
func TestSnapshotCommittedWALAndUncommittedTailAreReadOnly(t *testing.T) {
	path, writer := snapshotWALFixture(t)
	beforeWAL, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec("PRAGMA cache_size=1; BEGIN; INSERT INTO records VALUES (2, zeroblob(1048576))"); err != nil {
		t.Fatal(err)
	}
	afterWAL, err := os.Stat(path + "-wal")
	if err != nil || afterWAL.Size() <= beforeWAL.Size() {
		t.Fatalf("fixture did not spill uncommitted WAL frames: %v", err)
	}
	// Windows enforces SQLite's byte-range locks on the live SHM file, so a
	// whole-directory hash cannot read it during this write transaction. Keep
	// the live-writer read below and check every source byte on a detached copy
	// of the actual DB/WAL (including the uncommitted tail), with a SHM sentinel.
	copyPath := filepath.Join(t.TempDir(), "opencode.db")
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(copyPath+suffix, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(copyPath+"-shm", []byte("SHM must not be read or changed"), 0600); err != nil {
		t.Fatal(err)
	}
	check := func(db *sql.DB) error {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM records").Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("snapshot exposed %d rows, want only committed row", count)
		}
		if _, err := db.Exec("INSERT INTO records VALUES (3, 'write')"); err == nil {
			t.Error("snapshot allowed write")
		}
		return nil
	}
	if err := withSnapshot(context.Background(), path, check); err != nil {
		t.Fatal(err)
	}
	// The live read must leave the captured main DB and WAL unchanged too.
	for _, suffix := range []string{"", "-wal"} {
		before, err := os.ReadFile(copyPath + suffix)
		if err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(path + suffix)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("snapshot changed live source %q: %v", suffix, err)
		}
	}
	before := snapshotDirectoryHashes(t, filepath.Dir(copyPath))
	if err := withSnapshot(context.Background(), copyPath, check); err != nil {
		t.Fatal(err)
	}
	if after := snapshotDirectoryHashes(t, filepath.Dir(copyPath)); !reflect.DeepEqual(before, after) {
		t.Fatal("snapshot changed source contents or directory entries")
	}
}

func TestSnapshotPartialWALTailDoesNotCommit(t *testing.T) {
	path, _ := snapshotWALFixture(t)
	f, err := os.OpenFile(path+"-wal", os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write([]byte{0, 0, 0, 2, 0, 0, 0, 2, 1})
	if closeErr := f.Close(); err != nil || closeErr != nil {
		t.Fatal(errors.Join(err, closeErr))
	}
	if err := withSnapshot(context.Background(), path, func(db *sql.DB) error {
		var text string
		if err := db.QueryRow("SELECT body FROM records WHERE id=1").Scan(&text); err != nil {
			return err
		}
		if text != "committed" {
			t.Fatalf("got body %q", text)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRejectsInvalidInputsBeforeCallback(t *testing.T) {
	for _, kind := range []string{"missing", "relative", "unclean", "symlink", "parent_symlink", "wal_symlink", "journal_symlink", "directory", "header", "page_size", "hot_journal", "wal_checksum", "wal_salt", "wal_header", "wal_header_checksum", "oversized", "oversized_wal", "cancelled", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			path, _ := snapshotWALFixture(t)
			ctx := context.Background()
			switch kind {
			case "missing":
				path += ".missing"
			case "relative":
				path = "opencode.db"
			case "unclean":
				path = filepath.Dir(path) + "/./opencode.db"
			case "symlink":
				link := path + ".link"
				if err := os.Symlink(path, link); err != nil {
					t.Skip(err)
				}
				path = link
			case "parent_symlink":
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(filepath.Dir(path), link); err != nil {
					t.Skip(err)
				}
				path = filepath.Join(link, filepath.Base(path))
			case "wal_symlink", "journal_symlink":
				suffix := map[string]string{"wal_symlink": "-wal", "journal_symlink": "-journal"}[kind]
				_ = os.Remove(path + suffix)
				if err := os.Symlink(path, path+suffix); err != nil {
					t.Skip(err)
				}
			case "directory":
				path = filepath.Dir(path)
			case "header", "page_size":
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "header" {
					data[0] = 0
				} else {
					data[16], data[17] = 0, 7
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "hot_journal":
				if err := os.WriteFile(path+"-journal", []byte{0xd9, 0xd5, 5, 0xf9, 0x20, 0xa1, 0x63, 0xd7}, 0600); err != nil {
					t.Fatal(err)
				}
			case "wal_checksum", "wal_salt", "wal_header", "wal_header_checksum":
				data, err := os.ReadFile(path + "-wal")
				if err != nil {
					t.Fatal(err)
				}
				offset := map[string]int{"wal_checksum": 56, "wal_salt": 40, "wal_header": 4, "wal_header_checksum": 24}[kind]
				data[offset] ^= 1
				if err := os.WriteFile(path+"-wal", data, 0600); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.Truncate(path, (512<<20)+1); err != nil {
					t.Fatal(err)
				}
			case "oversized_wal":
				if err := os.Truncate(path+"-wal", 512<<20); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "deadline":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Unix(0, 0))
				defer cancel()
			}
			called := false
			err := withSnapshot(ctx, path, func(*sql.DB) error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("invalid %s accepted (called=%v, err=%v)", kind, called, err)
			}
			if kind == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if kind == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost deadline: %v", err)
			}
		})
	}
}

func TestSnapshotPlainDatabaseAndCallbackError(t *testing.T) {
	path, writer := snapshotWALFixture(t)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	before := snapshotDirectoryHashes(t, filepath.Dir(path))
	want := errors.New("callback error")
	if err := withSnapshot(context.Background(), path, func(db *sql.DB) error {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM records").Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("count = %d", count)
		}
		return want
	}); !errors.Is(err, want) {
		t.Fatalf("callback error = %v", err)
	}
	if after := snapshotDirectoryHashes(t, filepath.Dir(path)); !reflect.DeepEqual(before, after) {
		t.Fatal("snapshot created a sidecar or changed source contents")
	}
}

func TestSnapshotGrowthBudgetIncludesCapturedSourceBuffers(t *testing.T) {
	path, _ := snapshotWALFixture(t)
	main, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wal, err := os.ReadFile(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	// The SQLite fixture is a 4096-byte base growing to an 8192-byte DB.
	// Model the rest of the aggregate as a large captured WAL file without
	// allocating half a GiB merely to exercise the admission boundary.
	if len(main) != 4096 {
		t.Fatalf("unexpected fixture base size %d", len(main))
	}
	for _, limit := range []int64{maxSnapshotBytes, 3072 << 20} {
		if _, err := reconstructSQLite(context.Background(), main, wal, limit-8191, limit); err == nil {
			t.Fatal("growth allocation ignored still-live captured source buffers")
		}
		result, err := reconstructSQLite(context.Background(), main, wal, limit-8192, limit)
		if err != nil || len(result) != 8192 {
			t.Fatalf("exact configured growth budget rejected: length=%d err=%v", len(result), err)
		}
	}
}

func TestSnapshotIgnoresChangingReaderSHM(t *testing.T) {
	path, writer := snapshotWALFixture(t)
	// A real SQLite read transaction pins a WAL read mark. Copy the persistent
	// fixture files, then model a concurrently changing read-mark sidecar there.
	// The copy keeps test-only SHM writes away from SQLite's active mapping.
	tx, err := writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM records").Scan(&count); err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(copyPath+suffix, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	shm, err := os.OpenFile(copyPath+"-shm", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer shm.Close()
	// Its size must not consume the DB/WAL capture budget either.
	if err := shm.Truncate(512 << 20); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		value := byte(0)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = shm.WriteAt([]byte{value}, 116)
			value++
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	for range 3 {
		if err := withSnapshot(context.Background(), copyPath, func(db *sql.DB) error {
			var got int
			if err := db.QueryRow("SELECT count(*) FROM records").Scan(&got); err != nil {
				return err
			}
			if got != 1 {
				t.Fatalf("got %d records with changing reader SHM", got)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapshotRejectsSameSaltCorruptionAfterValidCommit(t *testing.T) {
	path, writer := snapshotWALFixture(t)
	if _, err := writer.Exec("INSERT INTO records VALUES (2, 'later')"); err != nil {
		t.Fatal(err)
	}
	wal, err := os.ReadFile(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	pageSize := int(binary.BigEndian.Uint32(wal[8:12]))
	lastFrame := len(wal) - (pageSize + 24)
	wal[lastFrame+24] ^= 1 // Corrupt the later commit, retaining the earlier one.
	if err := os.WriteFile(path+"-wal", wal, 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := withSnapshot(context.Background(), path, func(db *sql.DB) error {
		called = true
		return nil
	}); err == nil || called {
		t.Fatalf("same-salt corrupt later frame exposed stale committed data: called=%v err=%v", called, err)
	}
}

func TestSnapshotSQLiteWALResetRetainsStaleSuffix(t *testing.T) {
	path, writer := snapshotWALFixture(t)
	for range 8 {
		if _, err := writer.Exec("UPDATE records SET body=body||'x'"); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	var busy, log, checkpointed int
	if err := writer.QueryRow("PRAGMA wal_checkpoint(RESTART)").Scan(&busy, &log, &checkpointed); err != nil || busy != 0 || log != checkpointed {
		t.Fatalf("checkpoint fixture: %d/%d/%d %v", busy, log, checkpointed, err)
	}
	if _, err := writer.Exec("UPDATE records SET body='latest after reset'"); err != nil {
		t.Fatal(err)
	}
	wal, err := os.ReadFile(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	frameSize := 24 + int(binary.BigEndian.Uint32(wal[8:12]))
	if len(wal) != len(before) || len(wal) <= 32+frameSize || bytes.Equal(wal[16:24], wal[32+frameSize+8:32+frameSize+16]) {
		t.Fatal("SQLite fixture did not retain a stale salt-mismatched suffix")
	}
	sourceBefore := snapshotDirectoryHashes(t, filepath.Dir(path))
	if err := withSnapshot(context.Background(), path, func(db *sql.DB) error {
		var body string
		if err := db.QueryRow("SELECT body FROM records WHERE id=1").Scan(&body); err != nil {
			return err
		}
		if body != "latest after reset" {
			t.Fatalf("body = %q", body)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if after := snapshotDirectoryHashes(t, filepath.Dir(path)); !reflect.DeepEqual(sourceBefore, after) {
		t.Fatal("snapshot changed source after WAL reuse")
	}
}

func TestSnapshotCaptureVerificationRejectsObservedChanges(t *testing.T) {
	for _, mutation := range []string{"append", "replace", "symlink", "new_sidecar", "remove"} {
		t.Run(mutation, func(t *testing.T) {
			path, writer := snapshotWALFixture(t)
			// This test mutates the captured namespace, not an active SQLite
			// writer. Release SQLite's Windows delete-sharing restriction before
			// capture; keep the pathguard capture handle open during mutation.
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			dir, err := pathguard.Open(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			defer dir.Close()
			file, info, err := dir.OpenRegular(filepath.Base(path))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			files := []capturedSQLiteFile{{name: filepath.Base(path), file: file, info: info}, {name: filepath.Base(path) + "-journal"}}
			if err := verifySQLiteFiles(context.Background(), dir, files); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "append":
				err = os.Truncate(path, info.Size()+4096)
			case "replace":
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				err = os.Rename(path, path+".previous")
				if err == nil {
					err = os.WriteFile(path, data, 0600)
				}
			case "symlink":
				err = os.Rename(path, path+".previous")
				if err == nil {
					err = os.Symlink(path+".previous", path)
				}
			case "new_sidecar":
				err = os.WriteFile(path+"-journal", nil, 0600)
			case "remove":
				err = os.Remove(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := verifySQLiteFiles(context.Background(), dir, files); err == nil {
				t.Fatalf("accepted observed %s during capture", mutation)
			}
		})
	}
}

func snapshotWALFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE records(id INTEGER PRIMARY KEY, body TEXT); INSERT INTO records VALUES (1, 'committed')"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path + "-wal")
	if err != nil || len(data) < 32 || binary.BigEndian.Uint32(data[:4])&^uint32(1) != 0x377f0682 {
		t.Fatalf("WAL fixture invalid: %v", err)
	}
	return path, db
}

func snapshotDirectoryHashes(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string][32]byte)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[entry.Name()] = sha256.Sum256(data)
	}
	return out
}
