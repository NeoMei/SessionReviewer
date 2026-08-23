//go:build !windows

package apply

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"time"
)

func isApplyRedirect(info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}

type applyPlatformLock struct {
	file *os.File
}

func acquireApplyPlatformLock(root *os.Root, _, _ string, timeout time.Duration) (*applyPlatformLock, error) {
	file, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open pinned data root for apply lock: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		locked, err := tryApplyPlatformLock(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("acquire data-root apply lock: %w", err)
		}
		if locked {
			return &applyPlatformLock{file: file}, nil
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, errors.New("project apply transaction remains locked by a live owner")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func tryApplyPlatformLock(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

func releaseApplyPlatformLock(lock *applyPlatformLock) error {
	if lock == nil || lock.file == nil {
		return nil
	}
	return errors.Join(syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN), lock.file.Close())
}
