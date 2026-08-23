package sync

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type fakeWindowsReparseInfo struct{}

func (fakeWindowsReparseInfo) Name() string       { return "junction" }
func (fakeWindowsReparseInfo) Size() int64        { return 0 }
func (fakeWindowsReparseInfo) Mode() fs.FileMode  { return fs.ModeDir }
func (fakeWindowsReparseInfo) ModTime() time.Time { return time.Time{} }
func (fakeWindowsReparseInfo) IsDir() bool        { return true }
func (fakeWindowsReparseInfo) Sys() any {
	return &struct{ FileAttributes uint32 }{FileAttributes: 0x400}
}

func TestBaseLockClassifiesWindowsReparseMetadata(t *testing.T) {
	if !isStateRedirect(fakeWindowsReparseInfo{}) {
		t.Fatal("Windows reparse metadata was not classified as a redirect")
	}
}

func TestBaseLockPersistsAfterReleaseAndSerializesOwners(t *testing.T) {
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

	first, err := acquireBaseStoreLockWithTimeout(bases, 100*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireBaseStoreLockWithTimeout(bases, 30*time.Millisecond, nil); err == nil {
		t.Fatal("second live owner acquired advisory lock")
	}
	if err := first.release(); err != nil {
		t.Fatal(err)
	}
	info, err := bases.Lstat(baseLockName)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("persistent lock missing: info=%v err=%v", info, err)
	}
	second, err := acquireBaseStoreLockWithTimeout(bases, 100*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.release(); err != nil {
		t.Fatal(err)
	}
}

func TestBaseLockAbruptProcessExitReleasesKernelOwnership(t *testing.T) {
	if os.Getenv("SESSION_REVIEWER_BASE_LOCK_CRASH_HELPER") == "1" {
		data := os.Getenv("SESSION_REVIEWER_BASE_LOCK_ROOT")
		root, err := os.OpenRoot(data)
		if err != nil {
			os.Exit(4)
		}
		bases, err := root.OpenRoot(baseDirectoryName)
		if err != nil {
			os.Exit(4)
		}
		if _, err := acquireBaseStoreLockWithTimeout(bases, time.Second, nil); err != nil {
			os.Exit(4)
		}
		os.Exit(0)
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

	cmd := exec.Command(os.Args[0], "-test.run=^TestBaseLockAbruptProcessExitReleasesKernelOwnership$")
	cmd.Env = append(os.Environ(),
		"SESSION_REVIEWER_BASE_LOCK_CRASH_HELPER=1",
		"SESSION_REVIEWER_BASE_LOCK_ROOT="+data,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v: %s", err, output)
	}

	root, err = os.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := (BaseStore{Root: root}).Commit(first.ContentHash, validBaseRecord("decision-1", "decisions/decision-1.md", "two")); err != nil {
		t.Fatalf("commit after abrupt owner exit: %v", err)
	}
}

func TestBaseLockIdentityReplacementFailsClosedAndPreservesReplacement(t *testing.T) {
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

	replacement := []byte("replacement-lock")
	lock, err := acquireBaseStoreLockWithTimeout(bases, 100*time.Millisecond, func() error {
		if err := bases.Rename(baseLockName, "moved.lock"); err != nil {
			return err
		}
		return bases.WriteFile(baseLockName, replacement, 0o600)
	})
	if err == nil {
		if lock != nil {
			_ = lock.release()
		}
		t.Fatal("lock identity replacement accepted")
	}
	got, readErr := bases.ReadFile(baseLockName)
	if readErr != nil || string(got) != string(replacement) {
		t.Fatalf("replacement changed: got=%q err=%v", got, readErr)
	}
}

func TestBaseLockRejectsRedirectWithoutTouchingTarget(t *testing.T) {
	data := t.TempDir()
	if err := os.Mkdir(filepath.Join(data, baseDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(data, baseDirectoryName, baseLockName)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	root, err := os.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	bases, err := root.OpenRoot(baseDirectoryName)
	if err != nil {
		t.Fatal(err)
	}
	defer bases.Close()
	if _, err := acquireBaseStoreLockWithTimeout(bases, 30*time.Millisecond, nil); err == nil {
		t.Fatal("redirected lock accepted")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "untouched" {
		t.Fatalf("target changed: got=%q err=%v", got, err)
	}
}

func TestBaseLockTimeoutClassifiesLiveOwner(t *testing.T) {
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
	first, err := acquireBaseStoreLockWithTimeout(bases, 100*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	started := time.Now()
	_, err = acquireBaseStoreLockWithTimeout(bases, 40*time.Millisecond, nil)
	if err == nil || !errors.Is(err, ErrBaseLocked) {
		t.Fatalf("err=%v", err)
	}
	if elapsed := time.Since(started); elapsed < 30*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("timeout elapsed=%v", elapsed)
	}
}
