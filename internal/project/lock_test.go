package project

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProjectLockSerializesProcessesAndSurvivesOwnerCrash(t *testing.T) {
	directory := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestProjectLockSubprocessHelper$")
	command.Env = append(os.Environ(),
		"SESSION_REVIEWER_PROJECT_LOCK_HELPER=hold",
		"SESSION_REVIEWER_PROJECT_LOCK_DIR="+directory,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() && scanner.Text() == "READY" {
			ready <- nil
			return
		}
		ready <- fmt.Errorf("helper readiness: %q: %w", scanner.Text(), scanner.Err())
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not acquire project lock")
	}

	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := AcquireProjectLock(root, "sync.lock", 0); !errors.Is(err, ErrProjectLocked) {
		t.Fatalf("held lock error=%v", err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed helper exited successfully")
	}
	stopped = true

	lock, err := AcquireProjectLock(root, "sync.lock", time.Second)
	if err != nil {
		t.Fatalf("acquire after owner crash: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("second release: %v", err)
	}
	info, err := os.Lstat(filepath.Join(directory, "sync.lock"))
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("persistent lock info=%v err=%v", info, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode=%o", info.Mode().Perm())
	}
}

func TestProjectLockSubprocessHelper(t *testing.T) {
	if os.Getenv("SESSION_REVIEWER_PROJECT_LOCK_HELPER") != "hold" {
		return
	}
	root, err := os.OpenRoot(os.Getenv("SESSION_REVIEWER_PROJECT_LOCK_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lock, err := AcquireProjectLock(root, "sync.lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	fmt.Println("READY")
	select {}
}

func TestProjectLockRejectsInvalidOrRedirectedFiles(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := AcquireProjectLock(nil, "sync.lock", time.Second); err == nil {
		t.Fatal("accepted nil root")
	}
	for _, name := range []string{"", ".", "../sync.lock", "/sync.lock"} {
		if _, err := AcquireProjectLock(root, name, time.Second); err == nil {
			t.Fatalf("accepted lock name %q", name)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root.Name(), "redirect.lock")
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := AcquireProjectLock(root, "redirect.lock", time.Second); err == nil || !strings.Contains(err.Error(), "redirected") {
		t.Fatalf("redirect error=%v", err)
	}
}

func TestProjectLockNilReleaseIsIdempotent(t *testing.T) {
	var lock *ProjectLock
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectLockNamespaceReplacementCannotBypassHeldLock(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	first, err := AcquireProjectLock(root, "sync.lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(directory, "sync.lock")
	displaced := filepath.Join(directory, "displaced.lock")
	if err := os.Rename(original, displaced); err != nil {
		if runtime.GOOS != "windows" || (!errors.Is(err, os.ErrPermission) && !errors.Is(err, syscall.Errno(32))) {
			_ = first.Release()
			t.Fatalf("replace held lock namespace: %v", err)
		}
		// A Windows handle that denies FILE_SHARE_DELETE prevents the
		// replacement itself. It must still serialize competing owners and
		// become acquirable after release.
		assertProjectLockHeldThenReleased(t, root, first)
		return
	}
	if err := os.WriteFile(original, nil, 0o600); err != nil {
		_ = first.Release()
		t.Fatal(err)
	}
	second, err := AcquireProjectLock(root, "sync.lock", 0)
	if second != nil {
		_ = second.Release()
	}
	if !errors.Is(err, ErrProjectLocked) {
		_ = first.Release()
		t.Fatalf("replacement bypassed held lock: lock=%v err=%v", second, err)
	}
	// The namespace replacement is still reported by the original owner, but
	// releasing it must also release the stable cross-process anchor.
	if err := first.Release(); err == nil {
		t.Fatal("owner did not report replaced lock identity")
	}
	third, err := AcquireProjectLock(root, "sync.lock", time.Second)
	if err != nil {
		t.Fatalf("stable anchor survived owner release: %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatal(err)
	}
}

func assertProjectLockHeldThenReleased(t *testing.T, root *os.Root, first *ProjectLock) {
	t.Helper()
	second, err := AcquireProjectLock(root, "sync.lock", 0)
	if second != nil {
		_ = second.Release()
	}
	if !errors.Is(err, ErrProjectLocked) {
		_ = first.Release()
		t.Fatalf("held lock bypassed: lock=%v err=%v", second, err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release held lock: %v", err)
	}
	third, err := AcquireProjectLock(root, "sync.lock", time.Second)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatal(err)
	}
}
