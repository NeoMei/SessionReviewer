package project

import (
	"fmt"
	"os"
	"time"
)

const initLockPollInterval = 10 * time.Millisecond

type initLock struct {
	path string
	file *os.File
}

func acquireInitLock(path string, timeout time.Duration) (*initLock, error) {
	deadline := time.Now().Add(timeout)
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, err := fmt.Fprintf(file, "pid=%d\n", os.Getpid()); err != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, err
			}
			if err := file.Sync(); err != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, err
			}
			return &initLock{path: path, file: file}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("initialization lock %q remains held; refusing to remove an unknown or stale lock", path)
		}
		time.Sleep(initLockPollInterval)
	}
}

func (lock *initLock) release() error {
	owned, err := lock.file.Stat()
	if err != nil {
		_ = lock.file.Close()
		return err
	}
	current, err := os.Lstat(lock.path)
	if err != nil {
		_ = lock.file.Close()
		return err
	}
	if !os.SameFile(owned, current) {
		_ = lock.file.Close()
		return fmt.Errorf("initialization lock ownership changed; refusing to remove %q", lock.path)
	}
	if err := lock.file.Close(); err != nil {
		return err
	}
	return os.Remove(lock.path)
}
