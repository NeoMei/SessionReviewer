//go:build windows

package reviewjob

import (
	"errors"
	"os"
	"testing"
	"time"
)

// This native contract is also cross-compiled on non-Windows hosts. Task 13
// runs it on Windows to prove that the stabilized handle's DELETE access and
// FILE_SHARE_DELETE policy permit authenticated cleanup before unlock.
func TestWindowsOwnedBootstrapLockCanBeUnlinkedWhileHeld(t *testing.T) {
	rootPath := newStoreRoot(t)
	layout, err := (Store{Root: rootPath}).openLayout(true)
	if err != nil {
		t.Fatal(err)
	}
	defer layout.close()
	lock, err := acquireStoreFileLock(layout.locks, storeLockName, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !lock.created {
		t.Fatal("fixture did not publish the bootstrap lock")
	}
	if err := lock.unlinkOwnedWhileHeld(); err != nil {
		_ = lock.release()
		t.Fatal(err)
	}
	if _, err := layout.locks.Lstat(storeLockName); !errors.Is(err, os.ErrNotExist) {
		_ = lock.release()
		t.Fatalf("owned lock remained in the Windows namespace: %v", err)
	}
	if err := lock.release(); err != nil {
		t.Fatal(err)
	}
}
