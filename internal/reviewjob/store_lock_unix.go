//go:build !windows

package reviewjob

import (
	"errors"
	"os"
	"syscall"
)

func stabilizeStoreLockFile(file *os.File) (*os.File, error) { return file, nil }

func tryStorePlatformLock(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

func unlockStorePlatformLock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
