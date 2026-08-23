//go:build !windows

package project

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

func isProjectLockRedirect(info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}

func privateProjectLockMode(info fs.FileInfo) bool {
	return info.Mode().Perm() == 0o600
}

func tryProjectPlatformLock(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

func unlockProjectPlatformLock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
