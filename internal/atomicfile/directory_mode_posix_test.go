//go:build !windows

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestEnsureRootDirPreparedRestoresExactCreatedModeAfterUmask(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	tests := []struct {
		name  string
		umask int
		mode  os.FileMode
	}{
		{name: "legacy-0751-umask-0027", umask: 0o027, mode: 0o751},
		{name: "legacy-0755-umask-0077", umask: 0o077, mode: 0o755},
		{name: "private-0700-umask-0077", umask: 0o077, mode: 0o700},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			withProcessUmask(test.umask, func() {
				created, err := EnsureRootDirPrepared(root, test.name, test.mode, nil, nil)
				if err != nil || !created {
					t.Fatalf("created=%v err=%v", created, err)
				}
			})
			info, err := os.Stat(filepath.Join(rootPath, test.name))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != test.mode {
				t.Fatalf("mode=%v want=%v err=%v", info.Mode().Perm(), test.mode, err)
			}
		})
	}
}

func TestEnsureRootDirPreparedNeverChmodsExistingDirectory(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, "existing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(rootPath, "existing"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	prepareCalls := 0
	withProcessUmask(0o077, func() {
		created, err := EnsureRootDirPrepared(root, "existing", 0o700, nil, func(*os.File) error {
			prepareCalls++
			return nil
		})
		if err != nil || created {
			t.Fatalf("created=%v err=%v", created, err)
		}
	})
	info, err := os.Stat(filepath.Join(rootPath, "existing"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 || prepareCalls != 0 {
		t.Fatalf("mode=%v prepareCalls=%d err=%v", info.Mode().Perm(), prepareCalls, err)
	}
}

func TestEnsureRootDirPreparedNeverChmodsErrExistWinner(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	prepareCalls := 0
	created, err := ensureRootDirPreparedWithOps(root, "winner", 0o700, nil, func(*os.File) error {
		prepareCalls++
		return nil
	}, directoryDurabilityOps{
		createDirectory: func(parent *os.Root, name string, _ os.FileMode) (*os.File, error) {
			if err := parent.Mkdir(name, 0o700); err != nil {
				return nil, err
			}
			winner, err := parent.Open(name)
			if err != nil {
				return nil, err
			}
			if err := winner.Chmod(0o755); err != nil {
				winner.Close()
				return nil, err
			}
			if err := winner.Close(); err != nil {
				return nil, err
			}
			return nil, os.ErrExist
		},
		syncParent: func(*os.Root, string) error { return nil },
	})
	if err != nil || created || prepareCalls != 0 {
		t.Fatalf("created=%v prepareCalls=%d err=%v", created, prepareCalls, err)
	}
	info, err := os.Stat(filepath.Join(rootPath, "winner"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("ErrExist winner mode=%v want=0755", info.Mode().Perm())
	}
}

func TestEnsureRootDirPreparedChmodsCreatedHandleNotReplacement(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	replacement := filepath.Join(rootPath, "legacy")
	createdAway := filepath.Join(rootPath, "created-away")

	var created bool
	withProcessUmask(0o077, func() {
		created, err = EnsureRootDirPrepared(root, "legacy", 0o751, func() error {
			if err := os.Rename(replacement, createdAway); err != nil {
				return err
			}
			if err := os.Mkdir(replacement, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(replacement, "user.txt"), []byte("replacement"), 0o644)
		}, nil)
	})
	if !created || !errors.Is(err, ErrRootDirectoryIdentityChanged) {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if info, statErr := os.Stat(createdAway); statErr != nil {
		t.Fatal(statErr)
	} else if info.Mode().Perm() != 0o751 {
		t.Fatalf("created handle mode=%v want=0751 err=%v", info.Mode().Perm(), statErr)
	}
	if info, statErr := os.Stat(replacement); statErr != nil {
		t.Fatal(statErr)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("replacement mode changed: mode=%v err=%v", info.Mode().Perm(), statErr)
	}
	body, readErr := os.ReadFile(filepath.Join(replacement, "user.txt"))
	if readErr != nil || string(body) != "replacement" {
		t.Fatalf("replacement bytes changed: body=%q err=%v", body, readErr)
	}
}

func TestEnsureRootDirPreparedDoesNotAdoptStagingReplacementBeforeOpen(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	var stagingName string
	createdAway := filepath.Join(rootPath, "created-away")
	created, err := ensureRootDirPreparedWithOps(root, "legacy", 0o751, nil, nil, directoryDurabilityOps{
		afterStagingIdentity: func(parent *os.Root, temporary, final string) error {
			if final != "legacy" || temporary == final || !IsRootDirectoryTemporaryName(temporary) {
				return errors.New("invalid staging publication names")
			}
			stagingName = temporary
			if err := os.Rename(filepath.Join(rootPath, temporary), createdAway); err != nil {
				return err
			}
			if err := parent.Mkdir(temporary, 0o700); err != nil {
				return err
			}
			if err := os.Chmod(filepath.Join(rootPath, temporary), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(rootPath, temporary, "user.txt"), []byte("replacement"), 0o644)
		},
		syncParent: func(*os.Root, string) error { return nil },
	})
	if created || !errors.Is(err, ErrRootDirectoryIdentityChanged) {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if stagingName == "" {
		t.Fatal("staging identity hook did not run")
	}
	if _, statErr := os.Lstat(filepath.Join(rootPath, "legacy")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("final directory was unexpectedly published: %v", statErr)
	}
	if info, statErr := os.Stat(filepath.Join(rootPath, stagingName)); statErr != nil {
		t.Fatal(statErr)
	} else if info.Mode().Perm() != 0o755 {
		t.Fatalf("staging replacement was chmodded: mode=%v", info.Mode().Perm())
	}
	body, readErr := os.ReadFile(filepath.Join(rootPath, stagingName, "user.txt"))
	if readErr != nil || string(body) != "replacement" {
		t.Fatalf("staging replacement bytes changed: body=%q err=%v", body, readErr)
	}
	if info, statErr := os.Stat(createdAway); statErr != nil {
		t.Fatal(statErr)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("unopened original staging mode=%v want=0700", info.Mode().Perm())
	}
}

func TestEnsureRootDirPreparedFinalCollisionCleansOnlyOwnedStaging(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	prepareCalls := 0
	var stagingName string
	created, err := ensureRootDirPreparedWithOps(root, "legacy", 0o751, nil, func(*os.File) error {
		prepareCalls++
		return nil
	}, directoryDurabilityOps{
		beforeDirectoryPublish: func(parent *os.Root, temporary, final string) error {
			stagingName = temporary
			staging, err := parent.Open(temporary)
			if err != nil {
				return err
			}
			info, statErr := staging.Stat()
			closeErr := staging.Close()
			if statErr != nil || closeErr != nil || info.Mode().Perm() != 0o751 {
				return errors.New("staging directory was not prepared before publication")
			}
			if err := parent.Mkdir(final, 0o700); err != nil {
				return err
			}
			if err := os.Chmod(filepath.Join(rootPath, final), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(rootPath, final, "user.txt"), []byte("winner"), 0o644)
		},
		syncParent: syncRootDirectoryEntry,
	})
	if err != nil || created || prepareCalls != 0 {
		t.Fatalf("created=%v prepareCalls=%d err=%v", created, prepareCalls, err)
	}
	if info, statErr := os.Stat(filepath.Join(rootPath, "legacy")); statErr != nil {
		t.Fatal(statErr)
	} else if info.Mode().Perm() != 0o755 {
		t.Fatalf("final winner was chmodded: mode=%v", info.Mode().Perm())
	}
	body, readErr := os.ReadFile(filepath.Join(rootPath, "legacy", "user.txt"))
	if readErr != nil || string(body) != "winner" {
		t.Fatalf("final winner bytes changed: body=%q err=%v", body, readErr)
	}
	if _, statErr := os.Lstat(filepath.Join(rootPath, stagingName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("owned staging was not cleaned after collision: %v", statErr)
	}
}

func TestEnsureRootDirPreparedDoesNotPublishStagingReplacement(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	var stagingName string
	createdAway := filepath.Join(rootPath, "created-away")
	created, err := ensureRootDirPreparedWithOps(root, "legacy", 0o751, nil, nil, directoryDurabilityOps{
		beforeDirectoryPublish: func(parent *os.Root, temporary, _ string) error {
			stagingName = temporary
			if err := os.Rename(filepath.Join(rootPath, temporary), createdAway); err != nil {
				return err
			}
			if err := parent.Mkdir(temporary, 0o700); err != nil {
				return err
			}
			if err := os.Chmod(filepath.Join(rootPath, temporary), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(rootPath, temporary, "user.txt"), []byte("replacement"), 0o644)
		},
		syncParent: func(*os.Root, string) error { return nil },
	})
	if created || !errors.Is(err, ErrRootDirectoryIdentityChanged) {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if _, statErr := os.Lstat(filepath.Join(rootPath, "legacy")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("staging replacement was published: %v", statErr)
	}
	if info, statErr := os.Stat(filepath.Join(rootPath, stagingName)); statErr != nil {
		t.Fatal(statErr)
	} else if info.Mode().Perm() != 0o755 {
		t.Fatalf("staging replacement was chmodded: mode=%v", info.Mode().Perm())
	}
	body, readErr := os.ReadFile(filepath.Join(rootPath, stagingName, "user.txt"))
	if readErr != nil || string(body) != "replacement" {
		t.Fatalf("staging replacement bytes changed: body=%q err=%v", body, readErr)
	}
	if info, statErr := os.Stat(createdAway); statErr != nil {
		t.Fatal(statErr)
	} else if info.Mode().Perm() != 0o751 {
		t.Fatalf("owned staging handle mode=%v want=0751", info.Mode().Perm())
	}
}

func withProcessUmask(mask int, run func()) {
	old := syscall.Umask(mask)
	defer syscall.Umask(old)
	run()
}
