//go:build !windows

package atomicfile

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
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
	if _, statErr := os.Lstat(replacement); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("replacement remained at public leaf: %v", statErr)
	}
	preserved := findRootDirectoryQuarantine(t, rootPath)
	if info, statErr := os.Stat(preserved); statErr != nil {
		t.Fatal(statErr)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("replacement mode changed: mode=%v err=%v", info.Mode().Perm(), statErr)
	}
	body, readErr := os.ReadFile(filepath.Join(preserved, "user.txt"))
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

func TestEnsureRootDirPreparedNeverRemovesEmptyStagingReplacement(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	stagingName := ""
	created, err := ensureRootDirPreparedWithOps(root, "legacy", 0o751, nil, nil, directoryDurabilityOps{
		afterStagingIdentity: func(parent *os.Root, temporary, _ string) error {
			stagingName = temporary
			if err := os.Rename(filepath.Join(rootPath, temporary), filepath.Join(rootPath, "owned-away")); err != nil {
				return err
			}
			if err := parent.Mkdir(temporary, 0o700); err != nil {
				return err
			}
			return os.Chmod(filepath.Join(rootPath, temporary), 0o755)
		},
		syncParent: syncRootDirectoryEntry,
	})
	if created || !errors.Is(err, ErrRootDirectoryIdentityChanged) {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if info, statErr := os.Stat(filepath.Join(rootPath, stagingName)); statErr != nil {
		t.Fatal(statErr)
	} else if !info.IsDir() || info.Mode().Perm() != 0o755 {
		t.Fatalf("empty staging replacement changed: mode=%v", info.Mode())
	}
}

func TestEnsureRootDirPreparedFinalCollisionRetainsOwnedStagingAndWinner(t *testing.T) {
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
	if created || !errors.Is(err, ErrRootDirectoryPublicationCollision) || prepareCalls != 0 {
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
	if info, statErr := os.Stat(filepath.Join(rootPath, stagingName)); statErr != nil {
		t.Fatal(statErr)
	} else if info.Mode().Perm() != 0o751 {
		t.Fatalf("collision staging mode=%v want=0751", info.Mode().Perm())
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

func TestEnsureRootDirPreparedQuarantinesFinalWindowSourceReplacement(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	var stagingName string
	ownedAway := filepath.Join(rootPath, "owned-away")
	created, err := ensureRootDirPreparedWithOps(root, "legacy", 0o751, nil, nil, directoryDurabilityOps{
		afterStagingPublicationCheck: func(parent *os.Root, temporary, _ string) error {
			stagingName = temporary
			if err := os.Rename(filepath.Join(rootPath, temporary), ownedAway); err != nil {
				return err
			}
			if err := parent.Mkdir(temporary, 0o700); err != nil {
				return err
			}
			if err := os.Chmod(filepath.Join(rootPath, temporary), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(rootPath, temporary, "user.txt"), []byte("final-window replacement"), 0o644)
		},
		syncParent: syncRootDirectoryEntry,
	})
	if created || !errors.Is(err, ErrRootDirectoryIdentityChanged) {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if stagingName == "" {
		t.Fatal("final source replacement hook did not run")
	}
	if _, statErr := os.Lstat(filepath.Join(rootPath, "legacy")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unexpected final entry after quarantine: %v", statErr)
	}
	if info, statErr := os.Stat(ownedAway); statErr != nil {
		t.Fatal(statErr)
	} else if info.Mode().Perm() != 0o751 {
		t.Fatalf("owned staging mode=%v want=0751", info.Mode().Perm())
	}
	quarantine := findRootDirectoryQuarantine(t, rootPath)
	body, readErr := os.ReadFile(filepath.Join(quarantine, "user.txt"))
	if readErr != nil || string(body) != "final-window replacement" {
		t.Fatalf("quarantine bytes=%q err=%v", body, readErr)
	}
	if info, statErr := os.Stat(quarantine); statErr != nil {
		t.Fatal(statErr)
	} else if info.Mode().Perm() != 0o755 {
		t.Fatalf("quarantine replacement mode=%v want=0755", info.Mode().Perm())
	}
}

func TestEnsureRootDirPreparedSerializesCooperativeParentCreators(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := ensureRootDirPreparedWithOps(root, "first", 0o700, nil, nil, directoryDurabilityOps{
			afterStagingIdentity: func(*os.Root, string, string) error {
				close(firstEntered)
				<-releaseFirst
				return nil
			},
			syncParent: syncRootDirectoryEntry,
		})
		firstDone <- err
	}()
	<-firstEntered

	secondStarted := make(chan struct{})
	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		_, err := ensureRootDirPreparedWithOps(root, "second", 0o700, nil, nil, directoryDurabilityOps{
			afterStagingIdentity: func(*os.Root, string, string) error {
				close(secondEntered)
				return nil
			},
			syncParent: syncRootDirectoryEntry,
		})
		secondDone <- err
	}()
	<-secondStarted
	select {
	case <-secondEntered:
		close(releaseFirst)
		t.Fatal("second cooperative creator entered while parent lock was held")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("second cooperative creator did not acquire released parent lock")
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRootDirPreparedSerializesCrossProcessCreator(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	release, err := lockRootDirectoryParent(root)
	if err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestRootDirectoryCrossProcessHelper$")
	command.Env = append(os.Environ(), "SESSION_REVIEWER_DIRECTORY_LOCK_HELPER="+rootPath)
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = release()
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		_ = release()
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "started" {
		_ = release()
		_ = command.Wait()
		t.Fatalf("helper did not start: %q err=%v", scanner.Text(), scanner.Err())
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	var early error
	finishedEarly := false
	select {
	case early = <-done:
		finishedEarly = true
	case <-time.After(100 * time.Millisecond):
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if finishedEarly {
		t.Fatalf("cross-process creator exited while parent lock was held: %v", early)
	}
	if !finishedEarly {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cross-process creator did not acquire released parent lock")
		}
	}
}

func TestEnsureRootDirPreparedDoesNotDeadlockUnderHigherLevelFlock(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	anchor, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer anchor.Close()
	if err := syscall.Flock(int(anchor.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := EnsureRootDirCreated(root, "child", 0o700)
		done <- err
	}()
	select {
	case err := <-done:
		if unlockErr := syscall.Flock(int(anchor.Fd()), syscall.LOCK_UN); err != nil || unlockErr != nil {
			t.Fatalf("create err=%v unlock err=%v", err, unlockErr)
		}
	case <-time.After(500 * time.Millisecond):
		_ = syscall.Flock(int(anchor.Fd()), syscall.LOCK_UN)
		<-done
		t.Fatal("atomic directory lock self-deadlocked under higher-level namespace flock")
	}
}

func TestEnsureRootDirPreparedRejectsUnsafePersistentLockWithoutRepair(t *testing.T) {
	rootPath := t.TempDir()
	lockPath := filepath.Join(rootPath, rootDirectoryLockName)
	if err := os.WriteFile(lockPath, []byte("user-owned"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockPath, 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	created, err := EnsureRootDirCreated(root, "child", 0o700)
	if err == nil || created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	body, readErr := os.ReadFile(lockPath)
	if readErr != nil || string(body) != "user-owned" {
		t.Fatalf("lock body=%q err=%v", body, readErr)
	}
	info, statErr := os.Stat(lockPath)
	if statErr != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("lock mode=%v err=%v", info.Mode().Perm(), statErr)
	}
	if _, statErr := os.Stat(filepath.Join(rootPath, "child")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target unexpectedly created: %v", statErr)
	}
}

func TestRootDirectoryCrossProcessHelper(t *testing.T) {
	rootPath := os.Getenv("SESSION_REVIEWER_DIRECTORY_LOCK_HELPER")
	if rootPath == "" {
		t.Skip("helper process only")
	}
	fmt.Fprintln(os.Stdout, "started")
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := EnsureRootDirCreated(root, "child", 0o700); err != nil {
		t.Fatal(err)
	}
}

func withProcessUmask(mask int, run func()) {
	old := syscall.Umask(mask)
	defer syscall.Umask(old)
	run()
}
