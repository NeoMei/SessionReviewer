package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWriteRootDurabilityOrder(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	var calls []string
	ops := durabilityOps{
		syncTemporary: func(file *os.File) error {
			calls = append(calls, "temporary-sync")
			return file.Sync()
		},
		publish: func(parent *os.Root, temporary, destination string) error {
			calls = append(calls, "publish")
			return parent.Rename(temporary, destination)
		},
		syncPublication: func(*os.Root, string) error {
			calls = append(calls, "publication-sync")
			return nil
		},
	}
	if err := writeRootAtParentWithOps(root, "state.json", []byte("new"), 0o600, ops); err != nil {
		t.Fatal(err)
	}
	if want := []string{"temporary-sync", "publish", "publication-sync"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

func TestWriteRootPublicationSyncFailureReturnsAfterPublication(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	syncErr := errors.New("injected publication sync failure")
	ops := defaultDurabilityOps()
	ops.syncPublication = func(*os.Root, string) error { return syncErr }
	err = writeRootAtParentWithOps(root, "state.json", []byte("new"), 0o600, ops)
	if !errors.Is(err, syncErr) {
		t.Fatalf("error=%v want=%v", err, syncErr)
	}
	got, readErr := os.ReadFile(filepath.Join(dir, "state.json"))
	if readErr != nil || string(got) != "new" {
		t.Fatalf("published content=%q err=%v", got, readErr)
	}
}

func TestEnsureRootDirSyncFailureReturnsAfterCreation(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	syncErr := errors.New("injected directory sync failure")
	err = ensureRootDirWithOps(root, "projects", 0o700, directoryDurabilityOps{
		syncParent: func(*os.Root, string) error { return syncErr },
	})
	if !errors.Is(err, syncErr) {
		t.Fatalf("error=%v want=%v", err, syncErr)
	}
	info, statErr := os.Stat(filepath.Join(dir, "projects"))
	if statErr != nil || !info.IsDir() {
		t.Fatalf("created directory info=%v err=%v", info, statErr)
	}
}

func TestEnsureRootDirRejectsRedirectedParentAndTraversal(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "redirect")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	for _, path := range []string{"redirect/child", "../escape", "/absolute"} {
		t.Run(path, func(t *testing.T) {
			if err := EnsureRootDir(root, path, 0o700); err == nil {
				t.Fatalf("EnsureRootDir(%q) succeeded", path)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(outside, "child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside child err=%v", err)
	}
}

func TestRenameRootSyncFailureReturnsAfterRename(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "before"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	syncErr := errors.New("injected rename sync failure")
	err = renameRootWithSync(root, "before", "after", func(*os.Root, string) error { return syncErr })
	if !errors.Is(err, syncErr) {
		t.Fatalf("error=%v want=%v", err, syncErr)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "after")); err != nil || string(got) != "data" {
		t.Fatalf("renamed content=%q err=%v", got, err)
	}
}

func TestRemoveRootSyncFailureReturnsAfterRemoval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stale")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	syncErr := errors.New("injected remove sync failure")
	err = removeRootWithSync(root, "stale", func(*os.Root, string) error { return syncErr })
	if !errors.Is(err, syncErr) {
		t.Fatalf("error=%v want=%v", err, syncErr)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed path err=%v", err)
	}
}
