package cursor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const validHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestStoreCommitAndReload(t *testing.T) {
	store := Store{Root: t.TempDir()}
	next := Cursor{
		SessionID: "s1",
		LastLine:  42,
		LastHash:  strings.Repeat("a", 64),
		UpdatedAt: time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC),
	}
	if err := store.Commit("s1", Cursor{}, next); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load("s1")
	if err != nil || got != next {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestStoreRejectsStaleCommit(t *testing.T) {
	store := Store{Root: t.TempDir()}
	current := Cursor{SessionID: "s1", LastLine: 10, LastHash: strings.Repeat("a", 64)}
	if err := store.Commit("s1", Cursor{}, current); err != nil {
		t.Fatal(err)
	}
	err := store.Commit("s1", Cursor{}, Cursor{SessionID: "s1", LastLine: 20})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("err=%v", err)
	}
}

func TestStoreRejectsUnsafeSessionID(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if _, err := store.Load("../escape"); err == nil {
		t.Fatal("expected invalid id error")
	}
}

func TestStoreRejectsDecreasingCursor(t *testing.T) {
	store := Store{Root: t.TempDir()}
	current := Cursor{SessionID: "s1", LastLine: 10, LastHash: strings.Repeat("a", 64)}
	if err := store.Commit("s1", Cursor{}, current); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit("s1", current, Cursor{SessionID: "s1", LastLine: 9, LastHash: strings.Repeat("b", 64)}); err == nil {
		t.Fatal("expected decreasing cursor error")
	}
}

func TestStoreReportsCorruptJSON(t *testing.T) {
	store := Store{Root: t.TempDir()}
	path := filepath.Join(store.Root, "cursors", "s1.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("s1"); err == nil {
		t.Fatal("expected JSON error")
	}
}

func TestStoreMissingCursorIsDistinctFromInvalidState(t *testing.T) {
	store := Store{Root: t.TempDir()}
	got, err := store.Load("missing")
	if err != nil || got != (Cursor{}) {
		t.Fatalf("missing cursor: got=%+v err=%v", got, err)
	}

	path := filepath.Join(store.Root, "cursors", "mismatched.json")
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"session_id":"other","last_line":1,"last_hash":"`+validHash+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("mismatched"); err == nil {
		t.Fatal("mismatched on-disk session must not look missing")
	}
}

func TestStoreRejectsTrailingJSONWithoutLeakingContents(t *testing.T) {
	store := Store{Root: t.TempDir()}
	path := filepath.Join(store.Root, "cursors", "s1.json")
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	secret := "SECRET-CONTENT-MUST-NOT-LEAK"
	if err := os.WriteFile(path, []byte(`{"session_id":"s1"}`+secret), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load("s1")
	if err == nil {
		t.Fatal("expected trailing JSON error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked cursor contents: %v", err)
	}
}

func TestStoreRejectsUnsafeSessionIDs(t *testing.T) {
	store := Store{Root: t.TempDir()}
	for _, id := range []string{"", ".", "..", "../x", "a/b", `a\b`, `C:relative`, `C:\absolute`, `\\server\share`, "/absolute", "white space"} {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			if _, err := store.Load(id); err == nil {
				t.Fatalf("Load(%q) accepted an unsafe session ID", id)
			}
			if err := store.Commit(id, Cursor{}, Cursor{SessionID: id}); err == nil {
				t.Fatalf("Commit(%q) accepted an unsafe session ID", id)
			}
		})
	}
}

func TestStoreRejectsWindowsReservedDeviceSessionIDs(t *testing.T) {
	store := Store{Root: t.TempDir()}
	for _, id := range []string{
		"CON", "con.txt", "CON.", "PRN", "AUX.json", "nul", "NUL...",
		"COM1", "com9.log", "LPT1", "lpt9.txt",
	} {
		t.Run(id, func(t *testing.T) {
			if _, err := store.Load(id); err == nil {
				t.Fatalf("Load(%q) accepted a Windows device alias", id)
			}
			if err := store.Commit(id, Cursor{}, Cursor{SessionID: id}); err == nil {
				t.Fatalf("Commit(%q) accepted a Windows device alias", id)
			}
		})
	}
}

func TestStoreValidatesNextCursor(t *testing.T) {
	store := Store{Root: t.TempDir()}
	cases := []struct {
		name string
		next Cursor
	}{
		{name: "mismatched session", next: Cursor{SessionID: "other", LastLine: 1, LastHash: validHash}},
		{name: "negative line", next: Cursor{SessionID: "s1", LastLine: -1}},
		{name: "missing hash", next: Cursor{SessionID: "s1", LastLine: 1}},
		{name: "short hash", next: Cursor{SessionID: "s1", LastLine: 1, LastHash: "abc"}},
		{name: "non-hex hash", next: Cursor{SessionID: "s1", LastLine: 1, LastHash: strings.Repeat("z", 64)}},
		{name: "hash at zero line", next: Cursor{SessionID: "s1", LastHash: validHash}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := store.Commit("s1", Cursor{}, test.next); err == nil {
				t.Fatalf("accepted invalid cursor: %+v", test.next)
			}
		})
	}
}

func TestStoreRejectsInvalidExpectedCursor(t *testing.T) {
	store := Store{Root: t.TempDir()}
	expected := Cursor{SessionID: "other", LastLine: 1, LastHash: validHash}
	next := Cursor{SessionID: "s1", LastLine: 2, LastHash: strings.Repeat("b", 64)}
	if err := store.Commit("s1", expected, next); err == nil || errors.Is(err, ErrStale) {
		t.Fatalf("invalid expected cursor must be rejected distinctly, err=%v", err)
	}
}

func TestStoreRejectsTimestampRollback(t *testing.T) {
	store := Store{Root: t.TempDir()}
	current := Cursor{
		SessionID: "s1",
		LastLine:  1,
		LastHash:  validHash,
		UpdatedAt: time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC),
	}
	if err := store.Commit("s1", Cursor{}, current); err != nil {
		t.Fatal(err)
	}
	next := Cursor{
		SessionID: "s1",
		LastLine:  2,
		LastHash:  strings.Repeat("b", 64),
		UpdatedAt: current.UpdatedAt.Add(-time.Second),
	}
	if err := store.Commit("s1", current, next); err == nil {
		t.Fatal("accepted a timestamp rollback")
	}
}

func TestStoreRejectsHashChangeAtSameLine(t *testing.T) {
	store := Store{Root: t.TempDir()}
	current := Cursor{SessionID: "s1", LastLine: 1, LastHash: validHash}
	if err := store.Commit("s1", Cursor{}, current); err != nil {
		t.Fatal(err)
	}
	next := Cursor{SessionID: "s1", LastLine: 1, LastHash: strings.Repeat("b", 64)}
	if err := store.Commit("s1", current, next); err == nil {
		t.Fatal("accepted a different hash for the same line")
	}
}

func TestStoreValidatesLoadedCursorInvariants(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "negative line", body: `{"session_id":"s1","last_line":-1}`},
		{name: "missing hash", body: `{"session_id":"s1","last_line":1}`},
		{name: "hash at zero line", body: `{"session_id":"s1","last_line":0,"last_hash":"` + validHash + `"}`},
		{name: "unknown field", body: `{"session_id":"s1","last_line":1,"last_hash":"` + validHash + `","secret":"do-not-accept"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := Store{Root: t.TempDir()}
			path := filepath.Join(store.Root, "cursors", "s1.json")
			if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load("s1"); err == nil {
				t.Fatal("accepted invalid on-disk cursor")
			}
		})
	}
}

func TestStoreValidatesRoot(t *testing.T) {
	base := t.TempDir()
	fileRoot := filepath.Join(base, "file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"", filepath.Join(base, "missing"), fileRoot} {
		if _, err := (Store{Root: root}).Load("s1"); err == nil {
			t.Fatalf("accepted invalid root %q", root)
		}
	}
}

func TestStoreRejectsSymlinkRootAndCursorPaths(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedRoot := filepath.Join(base, "linked")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := (Store{Root: linkedRoot}).Load("s1"); err == nil {
		t.Fatal("accepted symlink root")
	}

	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	cursorDir := filepath.Join(realRoot, "cursors")
	if err := os.Symlink(outside, cursorDir); err != nil {
		t.Fatal(err)
	}
	if err := (Store{Root: realRoot}).Commit("s1", Cursor{}, Cursor{SessionID: "s1"}); err == nil {
		t.Fatal("accepted symlink cursor directory")
	}
}

func TestStoreRejectsSymlinkCursorFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "cursors")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.json")
	if err := os.WriteFile(outside, []byte(`{"session_id":"s1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "s1.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store := Store{Root: root}
	if _, err := store.Load("s1"); err == nil {
		t.Fatal("accepted symlink cursor file")
	}
	if err := store.Commit("s1", Cursor{}, Cursor{SessionID: "s1"}); err == nil {
		t.Fatal("accepted symlink cursor file during commit")
	}
}

func TestProtectCursorDirectoryCannotBeRedirectedByRootReplacement(t *testing.T) {
	base := t.TempDir()
	live := filepath.Join(base, "live")
	moved := filepath.Join(base, "moved")
	outside := filepath.Join(base, "outside")
	for _, root := range []string{live, outside} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "cursors"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, "cursors"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root, err := (Store{Root: live}).open("s1", false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.close()
	if err := os.Rename(live, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, live); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := protectCursorDirectory(root.cursors); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(moved, "cursors"):   0o700,
		filepath.Join(outside, "cursors"): 0o755,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode=%#o want=%#o", path, got, want)
		}
	}
}

func TestStoreRejectsCaseCollidingSessionID(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if err := store.Commit("Session", Cursor{}, Cursor{SessionID: "Session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("session"); err == nil {
		t.Fatal("accepted a case-colliding session ID")
	}
}

func TestStoreSerializesConcurrentCaseCollisions(t *testing.T) {
	store := Store{Root: t.TempDir()}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{"Session", "session"} {
		go func(id string) {
			<-start
			results <- store.Commit(id, Cursor{}, Cursor{SessionID: id})
		}(id)
	}
	close(start)

	succeeded, rejected := 0, 0
	for range 2 {
		if err := <-results; err == nil {
			succeeded++
		} else {
			rejected++
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("succeeded=%d rejected=%d, want 1/1", succeeded, rejected)
	}
}

func TestStoreUsesPrivateModes(t *testing.T) {
	store := Store{Root: t.TempDir()}
	next := Cursor{SessionID: "s1", LastLine: 1, LastHash: validHash}
	if err := store.Commit("s1", Cursor{}, next); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(store.Root, "cursors"):            0o700,
		filepath.Join(store.Root, "cursors", "s1.json"): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode=%#o want=%#o", path, got, want)
		}
	}
}

func TestStoreFailedCommitLeavesPreviousBytesIntact(t *testing.T) {
	store := Store{Root: t.TempDir()}
	current := Cursor{SessionID: "s1", LastLine: 1, LastHash: validHash}
	if err := store.Commit("s1", Cursor{}, current); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Root, "cursors", "s1.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	invalid := Cursor{SessionID: "s1", LastLine: 2, LastHash: "not-a-hash"}
	if err := store.Commit("s1", current, invalid); err == nil {
		t.Fatal("expected commit failure")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("failed commit changed bytes: before=%q after=%q", before, after)
	}
}

func TestStoreReusesUnlockedCrashLockFile(t *testing.T) {
	store := Store{Root: t.TempDir()}
	dir := filepath.Join(store.Root, "cursors")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, ".s1.lock")
	lockBytes := []byte("pid=999999\ncreated=2000-01-01T00:00:00Z\n")
	if err := os.WriteFile(lockPath, lockBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}

	if err := store.Commit("s1", Cursor{}, Cursor{SessionID: "s1"}); err != nil {
		t.Fatalf("unlocked crash lock file wedged the session: %v", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("persistent advisory lock file missing: %v", err)
	}
}

func TestStoreRecoversAfterLockOwnerProcessCrash(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if err := os.Mkdir(filepath.Join(store.Root, "cursors"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoreCrashLockHelper$")
	cmd.Env = append(os.Environ(),
		"SESSION_REVIEWER_CURSOR_CRASH_HELPER=1",
		"SESSION_REVIEWER_CURSOR_ROOT="+store.Root,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash helper failed: %v\n%s", err, output)
	}
	if err := store.Commit("s1", Cursor{}, Cursor{SessionID: "s1"}); err != nil {
		t.Fatalf("crashed owner wedged the session: %v", err)
	}
}

func TestStoreCrashLockHelper(t *testing.T) {
	if os.Getenv("SESSION_REVIEWER_CURSOR_CRASH_HELPER") != "1" {
		return
	}
	root, err := os.OpenRoot(filepath.Join(os.Getenv("SESSION_REVIEWER_CURSOR_ROOT"), "cursors"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireCursorLock(root, ".s1.lock"); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestStoreLoadWaitsForTransactionAndRecoversBackup(t *testing.T) {
	store := Store{Root: t.TempDir()}
	current := Cursor{SessionID: "s1", LastLine: 1, LastHash: validHash}
	if err := store.Commit("s1", Cursor{}, current); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(store.Root, "cursors")
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lock, err := acquireCursorLock(root, ".s1.lock")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s1.json")
	backup := path + ".session-reviewer-backup"
	if err := os.Rename(path, backup); err != nil {
		_ = lock.release()
		t.Fatal(err)
	}

	type loadResult struct {
		cursor Cursor
		err    error
	}
	loaded := make(chan loadResult, 1)
	go func() {
		cursor, err := store.Load("s1")
		loaded <- loadResult{cursor: cursor, err: err}
	}()
	select {
	case result := <-loaded:
		_ = lock.release()
		t.Fatalf("Load escaped the transaction lock: got=%+v err=%v", result.cursor, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := lock.release(); err != nil {
		t.Fatal(err)
	}
	result := <-loaded
	if result.err != nil || result.cursor != current {
		t.Fatalf("recovered cursor=%+v err=%v", result.cursor, result.err)
	}
	if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup remains after recovery: %v", err)
	}
}

func TestStoreReconcilesInterruptedReplacementStates(t *testing.T) {
	current := Cursor{SessionID: "s1", LastLine: 1, LastHash: validHash}
	next := Cursor{SessionID: "s1", LastLine: 2, LastHash: strings.Repeat("b", 64)}
	encode := func(t *testing.T, cursor Cursor) []byte {
		t.Helper()
		b, err := json.Marshal(cursor)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, test := range []struct {
		name        string
		destination []byte
		backup      []byte
		temporary   []byte
		want        Cursor
		wantError   bool
	}{
		{name: "backup only", backup: encode(t, current), want: current},
		{name: "valid backup wins over uncommitted temp", backup: encode(t, current), temporary: encode(t, next), want: current},
		{name: "valid destination wins over stale corrupt backup", destination: encode(t, next), backup: []byte("corrupt"), want: next},
		{name: "valid backup replaces corrupt destination", destination: []byte("corrupt"), backup: encode(t, current), want: current},
		{name: "corrupt backup with valid temp is not guessed", backup: []byte("corrupt"), temporary: encode(t, next), wantError: true},
		{name: "corrupt destination and backup", destination: []byte("corrupt-new"), backup: []byte("corrupt-old"), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := Store{Root: t.TempDir()}
			dir := filepath.Join(store.Root, "cursors")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "s1.json")
			for name, body := range map[string][]byte{
				path:                              test.destination,
				path + ".session-reviewer-backup": test.backup,
				filepath.Join(dir, ".session-reviewer-test-temp"): test.temporary,
			} {
				if body != nil {
					if err := os.WriteFile(name, body, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := store.Load("s1")
			if test.wantError {
				if err == nil {
					t.Fatalf("got=%+v, expected corruption error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("got=%+v err=%v want=%+v", got, err, test.want)
			}
			if _, err := os.Stat(path + ".session-reviewer-backup"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("backup remains after reconciliation: %v", err)
			}
		})
	}
}

func TestStoreSerializesSimultaneousSameExpectedWriters(t *testing.T) {
	store := Store{Root: t.TempDir()}
	current := Cursor{SessionID: "s1", LastLine: 1, LastHash: validHash}
	if err := store.Commit("s1", Cursor{}, current); err != nil {
		t.Fatal(err)
	}

	const writers = 32
	start := make(chan struct{})
	results := make(chan error, writers)
	var ready sync.WaitGroup
	ready.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			ready.Done()
			<-start
			next := Cursor{SessionID: "s1", LastLine: i + 2, LastHash: fmt.Sprintf("%064x", i+1)}
			results <- store.Commit("s1", current, next)
		}(i)
	}
	ready.Wait()
	close(start)

	succeeded, stale := 0, 0
	for range writers {
		switch err := <-results; {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrStale):
			stale++
		default:
			t.Fatalf("unexpected commit error: %v", err)
		}
	}
	if succeeded != 1 || stale != writers-1 {
		t.Fatalf("succeeded=%d stale=%d, want 1/%d", succeeded, stale, writers-1)
	}
}

func TestStoreSerializesCrossProcessWriters(t *testing.T) {
	store := Store{Root: t.TempDir()}
	current := Cursor{SessionID: "s1", LastLine: 1, LastHash: validHash}
	if err := store.Commit("s1", Cursor{}, current); err != nil {
		t.Fatal(err)
	}

	barrier := filepath.Join(t.TempDir(), "start")
	resultsDir := t.TempDir()
	commands := make([]*exec.Cmd, 2)
	outputs := make([]bytes.Buffer, 2)
	for i := range commands {
		resultPath := filepath.Join(resultsDir, strconv.Itoa(i))
		cmd := exec.Command(os.Args[0], "-test.run=^TestStoreCrossProcessHelper$")
		cmd.Env = append(os.Environ(),
			"SESSION_REVIEWER_CURSOR_HELPER=1",
			"SESSION_REVIEWER_CURSOR_ROOT="+store.Root,
			"SESSION_REVIEWER_CURSOR_BARRIER="+barrier,
			"SESSION_REVIEWER_CURSOR_RESULT="+resultPath,
			"SESSION_REVIEWER_CURSOR_LINE="+strconv.Itoa(i+2),
		)
		cmd.Stdout = &outputs[i]
		cmd.Stderr = &outputs[i]
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands[i] = cmd
	}
	if err := os.WriteFile(barrier, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper failed: %v\n%v", err, outputs)
		}
	}

	succeeded, stale := 0, 0
	for i := range commands {
		result, err := os.ReadFile(filepath.Join(resultsDir, strconv.Itoa(i)))
		if err != nil {
			t.Fatal(err)
		}
		switch string(result) {
		case "success":
			succeeded++
		case "stale":
			stale++
		default:
			t.Fatalf("unexpected helper result %q", result)
		}
	}
	if succeeded != 1 || stale != 1 {
		t.Fatalf("succeeded=%d stale=%d, want 1/1", succeeded, stale)
	}
}

func TestStoreCrossProcessHelper(t *testing.T) {
	if os.Getenv("SESSION_REVIEWER_CURSOR_HELPER") != "1" {
		return
	}
	barrier := os.Getenv("SESSION_REVIEWER_CURSOR_BARRIER")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(barrier); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for cross-process barrier")
		}
		time.Sleep(time.Millisecond)
	}
	line, err := strconv.Atoi(os.Getenv("SESSION_REVIEWER_CURSOR_LINE"))
	if err != nil {
		t.Fatal(err)
	}
	current := Cursor{SessionID: "s1", LastLine: 1, LastHash: validHash}
	next := Cursor{SessionID: "s1", LastLine: line, LastHash: fmt.Sprintf("%064x", line)}
	err = (Store{Root: os.Getenv("SESSION_REVIEWER_CURSOR_ROOT")}).Commit("s1", current, next)
	result := "success"
	if errors.Is(err, ErrStale) {
		result = "stale"
	} else if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("SESSION_REVIEWER_CURSOR_RESULT"), []byte(result), 0o600); err != nil {
		t.Fatal(err)
	}
}
