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

func TestReplaceWindowsFileUsesSingleRelativeRename(t *testing.T) {
	called := 0
	ops := windowsFileOps{
		rename: func(gotTemporary, gotDestination string) error {
			called++
			if gotTemporary != "nested/state.tmp" || gotDestination != "nested/state.json" {
				t.Fatalf("rename(%q, %q)", gotTemporary, gotDestination)
			}
			return nil
		},
	}

	if err := replaceWindowsFile("nested/state.tmp", "nested/state.json", ops); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("rename calls=%d want=1", called)
	}
}

func TestReplaceWindowsFileRenameFailurePreservesDestination(t *testing.T) {
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
		rename: func(string, string) error {
			return replaceErr
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

func TestReplaceWindowsFileAnchoredRenameIgnoresReplacedNamespace(t *testing.T) {
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
				t.Skipf("renaming an open root is unavailable: %v", err)
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

func TestReplaceWindowsFileAnchoredRenameRejectsReparseSubstitution(t *testing.T) {
	base := t.TempDir()
	rootPath := filepath.Join(base, "root")
	sub := filepath.Join(rootPath, "sub")
	moved := filepath.Join(rootPath, "moved")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{sub, outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(sub, "state.json"):     "old",
		filepath.Join(sub, "state.tmp"):      "new",
		filepath.Join(outside, "state.json"): "attacker-old",
		filepath.Join(outside, "state.tmp"):  "attacker-new",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	ops := windowsFileOps{
		rename: func(temporary, destination string) error {
			if err := os.Rename(sub, moved); err != nil {
				return err
			}
			if err := os.Symlink(outside, sub); err != nil {
				t.Skipf("directory symlinks are unavailable: %v", err)
			}
			return root.Rename(temporary, destination)
		},
	}
	err = replaceWindowsFile(filepath.Join("sub", "state.tmp"), filepath.Join("sub", "state.json"), ops)
	if err == nil {
		t.Fatal("expected rooted rename to reject substituted reparse path")
	}
	for path, want := range map[string]string{
		filepath.Join(moved, "state.json"):   "old",
		filepath.Join(moved, "state.tmp"):    "new",
		filepath.Join(outside, "state.json"): "attacker-old",
		filepath.Join(outside, "state.tmp"):  "attacker-new",
	} {
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != want {
			t.Fatalf("%s content=%q err=%v want=%q", path, got, readErr, want)
		}
	}
}
