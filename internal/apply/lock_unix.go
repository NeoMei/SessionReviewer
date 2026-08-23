//go:build !windows

package apply

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

func isApplyRedirect(info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
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

func unlockApplyPlatformLock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
