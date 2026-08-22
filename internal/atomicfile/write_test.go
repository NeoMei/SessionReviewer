package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesFileAndLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "new" {
		t.Fatalf("content=%q err=%v", b, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("entries=%v", entries)
	}
}

func TestWriteRootCannotBeRedirectedByDirectoryPathReplacement(t *testing.T) {
	base := t.TempDir()
	live := filepath.Join(base, "live")
	moved := filepath.Join(base, "moved")
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(live, "state.json"), filepath.Join(outside, "state.json")} {
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(live)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(live, moved); err != nil {
		t.Skipf("renaming an open root is unavailable: %v", err)
	}
	if err := os.Symlink(outside, live); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := WriteRoot(root, "state.json", []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(moved, "state.json"):   "new",
		filepath.Join(outside, "state.json"): "old",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s content=%q err=%v want=%q", path, got, err, want)
		}
	}
}

func TestBackupPathMatchesReplacementProtocol(t *testing.T) {
	if got, want := BackupPath("state.json"), "state.json.session-reviewer-backup"; got != want {
		t.Fatalf("BackupPath()=%q want=%q", got, want)
	}
}

func TestReplaceWindowsFileSelectsReplaceForExistingDestination(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "state.json")
	temporary := filepath.Join(dir, "state.tmp")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporary, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	replaced := false
	ops := windowsFileOps{
		stat: os.Stat,
		replaceExisting: func(gotDestination, gotTemporary string) error {
			replaced = true
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
	if !replaced {
		t.Fatal("replaceExisting was not selected")
	}
}

func TestReplaceWindowsFileSelectsMoveForAbsentDestination(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "state.json")
	temporary := filepath.Join(dir, "state.tmp")
	if err := os.WriteFile(temporary, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	moved := false
	ops := windowsFileOps{
		stat: os.Stat,
		replaceExisting: func(string, string) error {
			return errors.New("replaceExisting must not be used")
		},
		moveNew: func(gotTemporary, gotDestination string) error {
			moved = true
			if gotTemporary != temporary || gotDestination != destination {
				t.Fatalf("moveNew(%q, %q)", gotTemporary, gotDestination)
			}
			return nil
		},
	}

	if err := replaceWindowsFile(temporary, destination, ops); err != nil {
		t.Fatal(err)
	}
	if !moved {
		t.Fatal("moveNew was not selected")
	}
}

func TestReplaceWindowsFileFailurePreservesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "state.json")
	temporary := filepath.Join(dir, "state.tmp")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporary, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	replaceErr := errors.New("sharing violation")
	ops := windowsFileOps{
		stat: os.Stat,
		replaceExisting: func(string, string) error {
			return replaceErr
		},
		moveNew: func(string, string) error {
			return errors.New("moveNew must not be used")
		},
	}

	err := replaceWindowsFile(temporary, destination, ops)
	if !errors.Is(err, replaceErr) {
		t.Fatalf("error=%v", err)
	}
	got, readErr := os.ReadFile(destination)
	if readErr != nil || string(got) != "old" {
		t.Fatalf("destination=%q err=%v", got, readErr)
	}
}
