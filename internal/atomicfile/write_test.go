package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
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
		t.Fatal(err)
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

func TestWindowsReplacementPreservesBackupWhenInstallAndRollbackFail(t *testing.T) {
	const (
		temporary   = "state.tmp"
		destination = "state.json"
		backup      = destination + ".session-reviewer-backup"
	)
	installErr := errors.New("install failed")
	rollbackErr := errors.New("rollback failed")
	files := map[string]string{temporary: "new", destination: "old"}
	renames := 0
	ops := windowsFileOps{
		stat: func(name string) (fs.FileInfo, error) {
			if _, ok := files[name]; !ok {
				return nil, os.ErrNotExist
			}
			return fakeFileInfo{name: name}, nil
		},
		rename: func(oldpath, newpath string) error {
			renames++
			switch renames {
			case 2:
				return installErr
			case 3:
				return rollbackErr
			}
			files[newpath] = files[oldpath]
			delete(files, oldpath)
			return nil
		},
		remove: func(name string) error {
			delete(files, name)
			return nil
		},
	}

	err := replaceWindowsFile(temporary, destination, ops)
	if !errors.Is(err, installErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("error does not preserve install and rollback failures: %v", err)
	}
	if got := files[backup]; got != "old" {
		t.Fatalf("recoverable backup=%q files=%v", got, files)
	}
}

func TestWindowsReplacementPreservesBothFilesWhenStaleBackupCleanupFails(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	files := map[string]string{
		"state.tmp":                          "new",
		"state.json":                         "current",
		"state.json.session-reviewer-backup": "recoverable",
	}
	ops := windowsFileOps{
		stat: func(name string) (fs.FileInfo, error) {
			if _, ok := files[name]; !ok {
				return nil, os.ErrNotExist
			}
			return fakeFileInfo{name: name}, nil
		},
		rename: func(oldpath, newpath string) error {
			files[newpath] = files[oldpath]
			delete(files, oldpath)
			return nil
		},
		remove: func(string) error { return cleanupErr },
	}

	err := replaceWindowsFile("state.tmp", "state.json", ops)
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("error=%v", err)
	}
	if files["state.json"] != "current" || files["state.json.session-reviewer-backup"] != "recoverable" {
		t.Fatalf("durable files changed after cleanup failure: %v", files)
	}
}

type fakeFileInfo struct {
	name string
}

func (f fakeFileInfo) Name() string     { return f.name }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() fs.FileMode  { return 0 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }
