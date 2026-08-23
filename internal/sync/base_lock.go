package sync

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
)

var ErrBaseLocked = errors.New("merge-base transaction locked")

const (
	baseLockPollInterval = 10 * time.Millisecond
	baseLockTimeout      = 2 * time.Second
)

type baseStoreLock struct {
	root *os.Root
	file *os.File
	info os.FileInfo
}

func acquireBaseStoreLock(root *os.Root) (*baseStoreLock, error) {
	return acquireBaseStoreLockWithTimeout(root, baseLockTimeout, nil)
}

func acquireBaseStoreLockWithTimeout(root *os.Root, timeout time.Duration, afterLock func() error) (*baseStoreLock, error) {
	if root == nil || timeout <= 0 {
		return nil, errors.New("merge-base lock root and positive timeout are required")
	}
	deadline := time.Now().Add(timeout)
	for {
		file, info, created, err := openStableBaseLockFile(root)
		if err != nil {
			return nil, err
		}
		if created {
			if err := atomicfile.SyncRootPublication(root, baseLockName); err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("publish persistent merge-base lock: %w", err)
			}
		}
		locked, err := tryBasePlatformLock(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("acquire merge-base advisory lock: %w", err)
		}
		if locked {
			if afterLock != nil {
				if err := afterLock(); err != nil {
					return nil, errors.Join(err, unlockBasePlatformLock(file), file.Close())
				}
			}
			after, err := root.Lstat(baseLockName)
			if err != nil || !os.SameFile(info, after) || isStateRedirect(after) || !after.Mode().IsRegular() {
				return nil, errors.Join(errors.New("merge-base lock identity changed after acquisition"), unlockBasePlatformLock(file), file.Close())
			}
			return &baseStoreLock{root: root, file: file, info: info}, nil
		}
		_ = file.Close()
		if !time.Now().Before(deadline) {
			return nil, ErrBaseLocked
		}
		time.Sleep(baseLockPollInterval)
	}
}

func openStableBaseLockFile(root *os.Root) (*os.File, os.FileInfo, bool, error) {
	for {
		before, err := root.Lstat(baseLockName)
		found := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, false, errors.New("cannot inspect merge-base lock")
		}
		if found && (isStateRedirect(before) || !before.Mode().IsRegular()) {
			return nil, nil, false, errors.New("merge-base lock is redirected or not regular")
		}
		flags := os.O_RDWR
		if !found {
			flags |= os.O_CREATE | os.O_EXCL
		}
		file, err := root.OpenFile(baseLockName, flags, 0o600)
		if errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, false, errors.New("cannot open merge-base lock")
		}
		opened, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return nil, nil, false, errors.New("cannot inspect open merge-base lock")
		}
		after, err := root.Lstat(baseLockName)
		if err != nil || !os.SameFile(opened, after) || isStateRedirect(after) || !after.Mode().IsRegular() || (found && !os.SameFile(before, opened)) {
			_ = file.Close()
			if err != nil {
				return nil, nil, false, errors.New("merge-base lock changed while opening")
			}
			continue
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, nil, false, errors.New("cannot protect merge-base lock")
		}
		return file, opened, !found, nil
	}
}

func (lock *baseStoreLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	after, err := lock.root.Lstat(baseLockName)
	identityErr := error(nil)
	if err != nil || !os.SameFile(lock.info, after) || isStateRedirect(after) || !after.Mode().IsRegular() {
		identityErr = errors.New("merge-base lock identity changed before release")
	}
	unlockErr := unlockBasePlatformLock(lock.file)
	closeErr := lock.file.Close()
	return errors.Join(identityErr, unlockErr, closeErr)
}
