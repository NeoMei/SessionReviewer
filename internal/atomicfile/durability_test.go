package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

func TestEnsureRootDirRetryResyncsExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	firstSyncErr := errors.New("injected first directory sync failure")
	err = ensureRootDirWithOps(root, "projects", 0o700, directoryDurabilityOps{
		syncParent: func(*os.Root, string) error { return firstSyncErr },
	})
	if !errors.Is(err, firstSyncErr) {
		t.Fatalf("first error=%v want=%v", err, firstSyncErr)
	}

	secondSyncErr := errors.New("injected retry directory sync failure")
	calls := 0
	err = ensureRootDirWithOps(root, "projects", 0o700, directoryDurabilityOps{
		syncParent: func(_ *os.Root, name string) error {
			calls++
			if name != "projects" {
				t.Fatalf("sync name=%q", name)
			}
			return secondSyncErr
		},
	})
	if !errors.Is(err, secondSyncErr) || calls != 1 {
		t.Fatalf("retry error=%v calls=%d", err, calls)
	}
}

func TestEnsureRootDirCreatedDistinguishesOwnedCreationFromExisting(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	created, err := EnsureRootDirCreated(root, "created", 0o700)
	if err != nil || !created {
		t.Fatalf("new directory created=%v err=%v", created, err)
	}
	created, err = EnsureRootDirCreated(root, "created", 0o700)
	if err != nil || created {
		t.Fatalf("existing directory created=%v err=%v", created, err)
	}
	if err := os.Mkdir(filepath.Join(rootPath, "existing"), 0o755); err != nil {
		t.Fatal(err)
	}
	created, err = EnsureRootDirCreated(root, "existing", 0o700)
	if err != nil || created {
		t.Fatalf("preexisting directory created=%v err=%v", created, err)
	}
}

func TestEnsureRootDirPreparedDoesNotMutateFinalWindowReplacement(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	replacement := filepath.Join(rootPath, "private")
	quarantine := filepath.Join(rootPath, "created-away")
	created, err := EnsureRootDirPrepared(root, "private", 0o700, func() error {
		if err := os.Rename(replacement, quarantine); err != nil {
			return err
		}
		if err := os.Mkdir(replacement, 0o755); err != nil {
			return err
		}
		if err := os.Chmod(replacement, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(replacement, "user.txt"), []byte("user-owned"), 0o644)
	}, func(file *os.File) error {
		return file.Chmod(0o700)
	})
	if !created || !errors.Is(err, ErrRootDirectoryIdentityChanged) {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if _, statErr := os.Lstat(replacement); !errors.Is(statErr, os.ErrNotExist) {
		entries, _ := os.ReadDir(rootPath)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("replacement remained at public leaf: operation_err=%v stat_err=%v entries=%q", err, statErr, names)
	}
	preserved := findRootDirectoryQuarantine(t, rootPath)
	info, statErr := os.Stat(preserved)
	if statErr != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o755) {
		t.Fatalf("replacement was silently hardened: mode=%v err=%v", info, statErr)
	}
	body, readErr := os.ReadFile(filepath.Join(preserved, "user.txt"))
	if readErr != nil || string(body) != "user-owned" {
		t.Fatalf("replacement bytes changed: body=%q err=%v", body, readErr)
	}
	if info, statErr := os.Stat(quarantine); statErr != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		t.Fatalf("created handle was not hardened in isolation: mode=%v err=%v", info, statErr)
	}
}

func findRootDirectoryQuarantine(t *testing.T, rootPath string) string {
	t.Helper()
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	found := ""
	for _, entry := range entries {
		if !IsRootDirectoryQuarantineName(entry.Name()) {
			continue
		}
		if found != "" {
			t.Fatalf("multiple directory quarantines: %q and %q", found, entry.Name())
		}
		found = filepath.Join(rootPath, entry.Name())
	}
	if found == "" {
		t.Fatal("directory replacement was not preserved in quarantine")
	}
	return found
}

func TestEnsureRootDirPreparedErrExistWinnerNeverRunsPrepare(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	prepareCalls := 0
	created, err := ensureRootDirPreparedWithOps(root, "winner", 0o700, nil, func(*os.File) error {
		prepareCalls++
		return nil
	}, directoryDurabilityOps{
		createDirectory: func(parent *os.Root, name string, perm os.FileMode) (*os.File, error) {
			if err := parent.Mkdir(name, perm); err != nil {
				return nil, err
			}
			return nil, os.ErrExist
		},
		syncParent: func(*os.Root, string) error { return nil },
	})
	if err != nil || created || prepareCalls != 0 {
		t.Fatalf("created=%v prepareCalls=%d err=%v", created, prepareCalls, err)
	}
}

func TestSyncRootPublicationRetriesExistingPublishedFile(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	publicationErr := errors.New("injected publication sync failure")
	ops := defaultDurabilityOps()
	ops.syncPublication = func(*os.Root, string) error { return publicationErr }
	if err := writeRootAtParentWithOps(root, "state.json", []byte("new"), 0o600, ops); !errors.Is(err, publicationErr) {
		t.Fatalf("write error=%v want=%v", err, publicationErr)
	}

	var calls []string
	retryErr := errors.New("injected retry sync failure")
	err = syncRootPublicationWithSync(root, "state.json", func(_ *os.Root, name string) error {
		calls = append(calls, name)
		return retryErr
	})
	if !errors.Is(err, retryErr) || !reflect.DeepEqual(calls, []string{"state.json"}) {
		t.Fatalf("retry error=%v calls=%v", err, calls)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "state.json")); err != nil || string(got) != "new" {
		t.Fatalf("published content=%q err=%v", got, err)
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
