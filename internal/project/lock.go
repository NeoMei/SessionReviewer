package project

import (
	"errors"
	"fmt"
	"os"
	"time"
)

const initLockPollInterval = 10 * time.Millisecond

type initLock struct {
	file *os.File
}

func acquireInitLock(root *os.Root, name string, timeout time.Duration) (*initLock, error) {
	deadline := time.Now().Add(timeout)
	for {
		file, err := openStableInitLockFile(root, name)
		if err != nil {
			return nil, err
		}
		locked, err := tryInitPlatformLock(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("acquire initialization lock: %w", err)
		}
		if locked {
			return &initLock{file: file}, nil
		}
		_ = file.Close()
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("initialization transaction remains locked by a live owner")
		}
		time.Sleep(initLockPollInterval)
	}
}

func (lock *initLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	return errors.Join(unlockInitPlatformLock(lock.file), lock.file.Close())
}

func openStableInitLockFile(root *os.Root, name string) (*os.File, error) {
	for {
		before, err := root.Lstat(name)
		found := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if found && (!before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0) {
			return nil, fmt.Errorf("initialization lock is redirected or not regular")
		}
		flags := os.O_RDWR
		if !found {
			flags |= os.O_CREATE | os.O_EXCL
		}
		file, err := root.OpenFile(name, flags, 0o600)
		if errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		opened, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		after, err := root.Lstat(name)
		if err != nil || !os.SameFile(opened, after) || (found && !os.SameFile(before, opened)) {
			_ = file.Close()
			continue
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, err
		}
		return file, nil
	}
}
