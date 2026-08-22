//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const errorSharingViolation = syscall.Errno(32)

func TestWindowsExistingDestinationUsesNativeReplace(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "state.json")
	temporary := filepath.Join(dir, "state.tmp")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporary, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	called := false
	ops := windowsFileOps{
		stat: os.Stat,
		replaceExisting: func(gotDestination, gotTemporary string) error {
			called = true
			if gotDestination != destination || gotTemporary != temporary {
				t.Fatalf("replaceExisting(%q, %q)", gotDestination, gotTemporary)
			}
			return nil
		},
		moveNew: func(string, string) error {
			return errors.New("moveNew must not be used")
		},
	}

	if err := replaceWindowsFile(temporary, destination, ops); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("ReplaceFileW adapter was not selected")
	}
}

func TestWindowsNativeReplaceFailurePreservesDestination(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "state.json")
	temporary := filepath.Join(dir, "state.tmp")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporary, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := windowsFileOps{
		stat: os.Stat,
		replaceExisting: func(string, string) error {
			return errorSharingViolation
		},
		moveNew: func(string, string) error {
			return errors.New("moveNew must not be used")
		},
	}

	if err := replaceWindowsFile(temporary, destination, ops); !errors.Is(err, errorSharingViolation) {
		t.Fatalf("error=%v", err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "old" {
		t.Fatalf("destination=%q err=%v", got, err)
	}
}

func TestWindowsNativeReplaceRemovesTemporaryWithoutBackup(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "state.json")
	temporary := filepath.Join(dir, "state.tmp")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporary, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := replaceFile(temporary, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "new" {
		t.Fatalf("destination=%q err=%v", got, err)
	}
	for _, path := range []string{temporary, BackupPath(destination)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected replacement artifact %q: %v", path, err)
		}
	}
}

func TestWindowsNativeMoveInstallsAbsentDestination(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "state.json")
	temporary := filepath.Join(dir, "state.tmp")
	if err := os.WriteFile(temporary, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := replaceFile(temporary, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "new" {
		t.Fatalf("destination=%q err=%v", got, err)
	}
	if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary remains: %v", err)
	}
}

func TestWindowsNativeReplaceFailureLeavesOldDestinationReadable(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "state.json")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := replaceFile(filepath.Join(dir, "missing-temporary"), destination)
	if err == nil {
		t.Fatal("expected replacement error")
	}
	got, readErr := os.ReadFile(destination)
	if readErr != nil || string(got) != "old" {
		t.Fatalf("destination=%q err=%v replacement error=%v", got, readErr, err)
	}
	if _, statErr := os.Stat(BackupPath(destination)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("backup remains after failure: %v", statErr)
	}
}

func TestWindowsNativeSharingViolationPreservesDestination(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "state.json")
	temporary := filepath.Join(dir, "state.tmp")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporary, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()

	err = replaceFile(temporary, destination)
	if !errors.Is(err, errorSharingViolation) {
		t.Fatalf("error=%v want sharing violation", err)
	}
	got, readErr := os.ReadFile(destination)
	if readErr != nil || string(got) != "old" {
		t.Fatalf("destination=%q err=%v replacement error=%v", got, readErr, err)
	}
}

func TestWindowsNativeReplaceNeverMakesDestinationMissing(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "state.json")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	readErr := make(chan error, 1)
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := os.Stat(destination); err != nil {
				select {
				case readErr <- err:
				default:
				}
				return
			}
		}
	}()

	for i := range 50 {
		temporary := filepath.Join(dir, "state.tmp")
		if err := os.WriteFile(temporary, []byte{byte(i)}, 0o600); err != nil {
			close(stop)
			t.Fatal(err)
		}
		if err := replaceFile(temporary, destination); err != nil {
			close(stop)
			t.Fatal(err)
		}
	}
	close(stop)
	<-done
	select {
	case err := <-readErr:
		t.Fatalf("destination became unreadable during replacement: %v", err)
	default:
	}
}

func TestWindowsNativeReplaceSupportsUnicodeLongPath(t *testing.T) {
	dir := t.TempDir()
	segment := strings.Repeat("long-segment-", 3)
	for len(filepath.Join(dir, "状态-世界.json")) < 300 {
		dir = filepath.Join(dir, segment)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "状态-世界.json")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Write(destination, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "new" {
		t.Fatalf("destination=%q err=%v", got, err)
	}
}

func TestWindowsRootReplacementFollowsOpenRootAfterRename(t *testing.T) {
	base := t.TempDir()
	live := filepath.Join(base, "live")
	moved := filepath.Join(base, "moved")
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "state.json"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(live)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(live, moved); err != nil {
		t.Skipf("Windows cannot rename this open root: %v", err)
	}
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "state.json"), []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteRoot(root, "state.json", []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(moved, "state.json"): "new",
		filepath.Join(live, "state.json"):  "unrelated",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s destination=%q err=%v want=%q", path, got, err, want)
		}
	}
}
