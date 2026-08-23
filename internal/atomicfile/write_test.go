package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestWriteRootFileRejectsNonLeafPaths(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, leaf := range []string{"", ".", "..", "../state", "nested/state", `nested\state`} {
		if err := WriteRootFile(root, leaf, []byte("unsafe"), 0o600); err == nil {
			t.Fatalf("accepted non-leaf path %q", leaf)
		}
	}
}

func TestWriteRootFileCheckedRejectsInvalidLeafBeforeCheckpointOrTemp(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	checks := 0
	for _, leaf := range []string{"", ".", "..", "../state", "nested/state", `nested\state`, `C:state`} {
		if err := WriteRootFileChecked(root, leaf, []byte("CANARY-CONTENT"), 0o600, func() error {
			checks++
			return nil
		}); err == nil {
			t.Fatalf("accepted invalid leaf %q", leaf)
		}
	}
	if checks != 0 {
		t.Fatalf("checkpoint ran before leaf validation: %d", checks)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid leaf created entries=%v err=%v", entries, err)
	}
}

func TestWriteRootFileCheckedRemovesArtifactsAtEveryFailedCheckpoint(t *testing.T) {
	for _, failedCheck := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("checkpoint-%d", failedCheck), func(t *testing.T) {
			directory := t.TempDir()
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			checkpointErr := errors.New("namespace changed")
			checks := 0
			err = WriteRootFileChecked(root, "state.json", []byte("CANARY-CONTENT"), 0o600, func() error {
				checks++
				if checks == failedCheck {
					return checkpointErr
				}
				return nil
			})
			if !errors.Is(err, checkpointErr) || checks != failedCheck {
				t.Fatalf("error=%v checks=%d", err, checks)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed checkpoint left entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestWriteRootFileCheckedDoesNotDeleteReplacedPublication(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	checks := 0
	checkpointErr := errors.New("namespace changed")
	err = WriteRootFileChecked(root, "state.json", []byte("ours"), 0o600, func() error {
		checks++
		if checks != 3 {
			return nil
		}
		if err := os.Rename(filepath.Join(directory, "state.json"), filepath.Join(directory, "ours-moved")); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(directory, "state.json"), []byte("other"), 0o600); err != nil {
			return err
		}
		return checkpointErr
	})
	if !errors.Is(err, checkpointErr) {
		t.Fatalf("error=%v", err)
	}
	if got, err := os.ReadFile(filepath.Join(directory, "state.json")); err != nil || string(got) != "other" {
		t.Fatalf("replacement content=%q err=%v", got, err)
	}
}

func TestWriteRootFileCheckedDoesNotDeleteReplacedTemporary(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	checks := 0
	checkpointErr := errors.New("namespace changed")
	var replacement string
	err = WriteRootFileChecked(root, "state.json", []byte("ours"), 0o600, func() error {
		checks++
		if checks != 2 {
			return nil
		}
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 1 {
			return fmt.Errorf("temporary entries=%v: %w", entries, err)
		}
		replacement = filepath.Join(directory, entries[0].Name())
		if err := os.Rename(replacement, filepath.Join(directory, "ours-moved")); err != nil {
			return err
		}
		if err := os.WriteFile(replacement, []byte("other"), 0o600); err != nil {
			return err
		}
		return checkpointErr
	})
	if !errors.Is(err, checkpointErr) {
		t.Fatalf("error=%v", err)
	}
	if got, err := os.ReadFile(replacement); err != nil || string(got) != "other" {
		t.Fatalf("replacement temporary content=%q err=%v", got, err)
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

func TestWriteRootPinsImmediateParentAcrossOrdinaryDirectorySubstitution(t *testing.T) {
	base := t.TempDir()
	rootPath := filepath.Join(base, "root")
	parentPath := filepath.Join(rootPath, "nested")
	movedParentPath := filepath.Join(rootPath, "moved")
	if err := os.MkdirAll(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentPath, "state.json"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	err = writeRootWithParentOpener(root, filepath.Join("nested", "state.json"), []byte("new"), 0o600,
		func(path string) (*os.Root, error) {
			if path != "nested" {
				t.Fatalf("open parent path=%q want=nested", path)
			}
			parent, err := root.OpenRoot(path)
			if err != nil {
				return nil, err
			}
			if err := os.Rename(parentPath, movedParentPath); err != nil {
				parent.Close()
				t.Skipf("renaming an open parent is unavailable: %v", err)
			}
			if err := os.Mkdir(parentPath, 0o700); err != nil {
				parent.Close()
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(parentPath, "state.json"), []byte("attacker"), 0o600); err != nil {
				parent.Close()
				return nil, err
			}
			return parent, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(movedParentPath, "state.json"): "new",
		filepath.Join(parentPath, "state.json"):      "attacker",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s content=%q err=%v want=%q", path, got, err, want)
		}
	}
	for _, dir := range []string{movedParentPath, parentPath} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "state.json" {
			t.Fatalf("unexpected files in %s: %v", dir, entries)
		}
	}
}

func TestWriteRootSupportsLongNestedRelativePath(t *testing.T) {
	base := t.TempDir()
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	segments := make([]string, 0, 16)
	for len(filepath.Join(append(segments, "状态-世界.json")...)) < 320 {
		segments = append(segments, "nested-"+strings.Repeat("x", 24))
	}
	parent := filepath.Join(segments...)
	if err := os.MkdirAll(filepath.Join(base, parent), 0o700); err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join(parent, "状态-世界.json")
	if err := WriteRoot(root, relative, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(base, relative))
	if err != nil || string(got) != "new" {
		t.Fatalf("destination=%q err=%v", got, err)
	}
}

func TestWriteRootFailureCleansTemporaryFromPinnedImmediateParent(t *testing.T) {
	base := t.TempDir()
	rootPath := filepath.Join(base, "root")
	parentPath := filepath.Join(rootPath, "nested")
	movedParentPath := filepath.Join(rootPath, "moved")
	if err := os.MkdirAll(filepath.Join(parentPath, "target"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	err = writeRootWithParentOpener(root, filepath.Join("nested", "target"), []byte("new"), 0o600,
		func(path string) (*os.Root, error) {
			parent, err := root.OpenRoot(path)
			if err != nil {
				return nil, err
			}
			if err := os.Rename(parentPath, movedParentPath); err != nil {
				parent.Close()
				t.Skipf("renaming an open parent is unavailable: %v", err)
			}
			if err := os.Mkdir(parentPath, 0o700); err != nil {
				parent.Close()
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(parentPath, "attacker"), []byte("unchanged"), 0o600); err != nil {
				parent.Close()
				return nil, err
			}
			return parent, nil
		})
	if err == nil {
		t.Fatal("expected replacing a directory with a file to fail")
	}
	entries, err := os.ReadDir(movedParentPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "target" || !entries[0].IsDir() {
		t.Fatalf("unexpected files in retained parent after failure: %v", entries)
	}
	entries, err = os.ReadDir(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "attacker" {
		t.Fatalf("replacement parent changed during cleanup: %v", entries)
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
		destinationExists: func(gotDestination string) (bool, error) {
			if gotDestination != "nested/state.json" {
				t.Fatalf("destinationExists(%q)", gotDestination)
			}
			return true, nil
		},
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
		destinationExists: func(string) (bool, error) { return true, nil },
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
		destinationExists: func(string) (bool, error) { return true, nil },
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
		destinationExists: func(string) (bool, error) { return true, nil },
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

func TestReplaceWindowsFileAbsentDestinationUsesNoClobberLink(t *testing.T) {
	linked := false
	removed := false
	ops := windowsFileOps{
		destinationExists: func(gotDestination string) (bool, error) {
			if gotDestination != "state.json" {
				t.Fatalf("destinationExists(%q)", gotDestination)
			}
			return false, nil
		},
		rename: func(string, string) error {
			t.Fatal("absent destination must not use replacing rename")
			return nil
		},
		link: func(gotTemporary, gotDestination string) error {
			linked = true
			if gotTemporary != "state.tmp" || gotDestination != "state.json" {
				t.Fatalf("link(%q, %q)", gotTemporary, gotDestination)
			}
			return nil
		},
		remove: func(gotTemporary string) error {
			removed = true
			if gotTemporary != "state.tmp" {
				t.Fatalf("remove(%q)", gotTemporary)
			}
			return nil
		},
	}

	if err := replaceWindowsFile("state.tmp", "state.json", ops); err != nil {
		t.Fatal(err)
	}
	if !linked || !removed {
		t.Fatalf("linked=%v removed=%v", linked, removed)
	}
}

func TestReplaceWindowsFileAbsentRacingCreatorIsNotClobbered(t *testing.T) {
	creatorContent := "creator"
	linkErr := os.ErrExist
	ops := windowsFileOps{
		destinationExists: func(string) (bool, error) { return false, nil },
		rename: func(string, string) error {
			t.Fatal("racing creator must not be replaced")
			return nil
		},
		link: func(string, string) error {
			if creatorContent != "creator" {
				t.Fatalf("creator content changed before link: %q", creatorContent)
			}
			return linkErr
		},
		remove: func(string) error {
			t.Fatal("failed link must leave temporary cleanup to the caller")
			return nil
		},
	}

	err := replaceWindowsFile("state.tmp", "state.json", ops)
	if !errors.Is(err, linkErr) {
		t.Fatalf("error=%v", err)
	}
	if creatorContent != "creator" {
		t.Fatalf("racing creator was clobbered: %q", creatorContent)
	}
}

func TestReplaceWindowsFileAbsentUnsupportedHardLinkFailsSafely(t *testing.T) {
	unsupported := errors.New("hard links unsupported")
	renameCalled := false
	ops := windowsFileOps{
		destinationExists: func(string) (bool, error) { return false, nil },
		rename: func(string, string) error {
			renameCalled = true
			return nil
		},
		link: func(string, string) error { return unsupported },
		remove: func(string) error {
			t.Fatal("failed link must not remove the temporary")
			return nil
		},
	}

	err := replaceWindowsFile("state.tmp", "state.json", ops)
	if !errors.Is(err, unsupported) {
		t.Fatalf("error=%v", err)
	}
	if renameCalled {
		t.Fatal("unsafe replacing fallback was used")
	}
}

func TestReplaceWindowsFileTreatsDanglingLinkAsExistingEntry(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "state.json")
	temporary := filepath.Join(dir, "state.tmp")
	if err := os.Symlink("missing-target", destination); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(temporary, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := replaceWindowsFile("state.tmp", "state.json", windowsRootFileOps(root)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "new" {
		t.Fatalf("destination=%q err=%v", got, err)
	}
}
