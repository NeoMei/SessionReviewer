//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsNativeHandleRenameReplacesExistingDestination(t *testing.T) {
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
	assertWindowsReplacementState(t, destination, "new", temporary)
}

func TestWindowsNativeHandleRenameInstallsAbsentDestination(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "state.json")
	temporary := filepath.Join(dir, "state.tmp")
	if err := os.WriteFile(temporary, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := replaceFile(temporary, destination); err != nil {
		t.Fatal(err)
	}
	assertWindowsReplacementState(t, destination, "new", temporary)
}

func TestWindowsNativeHandleRenameFailurePreservesDestination(t *testing.T) {
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

	if err := replaceFile(temporary, destination); err == nil {
		t.Fatal("expected sharing violation")
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "old" {
		t.Fatalf("destination=%q err=%v", got, err)
	}
	got, err = os.ReadFile(temporary)
	if err != nil || string(got) != "new" {
		t.Fatalf("temporary=%q err=%v", got, err)
	}
}

func TestWindowsWriteRootFailurePreservesDestinationAndCleansTemporary(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "state.json")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := WriteRoot(root, "state.json", []byte("new"), 0o600); err == nil {
		t.Fatal("expected sharing violation")
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "old" {
		t.Fatalf("destination=%q err=%v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("unexpected files after failed replacement: %v", entries)
	}
}

func TestWindowsNativeHandleRenameNeverMakesDestinationMissing(t *testing.T) {
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
		t.Fatalf("destination became unavailable during replacement: %v", err)
	default:
	}
}

func TestWindowsNativeHandleRenameSupportsUnicodeLongPath(t *testing.T) {
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

func TestWindowsRootHandleRenameIgnoresReplacedNamespace(t *testing.T) {
	base := t.TempDir()
	live := filepath.Join(base, "live")
	moved := filepath.Join(base, "moved")
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"state.json": "old", "state.tmp": "new"} {
		if err := os.WriteFile(filepath.Join(live, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(live)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	ops := windowsFileOps{
		rename: func(temporary, destination string) error {
			if err := os.Rename(live, moved); err != nil {
				t.Skipf("Windows cannot rename this open root: %v", err)
			}
			if err := os.Mkdir(live, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(live, destination), []byte("attacker"), 0o600); err != nil {
				return err
			}
			return root.Rename(temporary, destination)
		},
	}
	if err := replaceWindowsFile("state.tmp", "state.json", ops); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(moved, "state.json"): "new",
		filepath.Join(live, "state.json"):  "attacker",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s content=%q err=%v want=%q", path, got, err, want)
		}
	}
}

func assertWindowsReplacementState(t *testing.T, destination, want string, absent ...string) {
	t.Helper()
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != want {
		t.Fatalf("destination=%q err=%v want=%q", got, err, want)
	}
	for _, path := range append(absent, BackupPath(destination)) {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected replacement artifact %q: %v", path, err)
		}
	}
}
