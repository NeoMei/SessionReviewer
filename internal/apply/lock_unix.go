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
	projectRoot *os.File
	projectData *os.File
}

func acquireApplyPlatformLock(projectRoot, _ *os.Root, _, _ string, timeout time.Duration) (*applyPlatformLock, error) {
	file, err := projectRoot.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open pinned project root for apply lock: %w", err)
	}
	if err := acquireApplyFileLock(file, timeout); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire project-root apply lock: %w", err)
	}
	return &applyPlatformLock{projectRoot: file}, nil
}

func attachApplyProjectDataLock(lock *applyPlatformLock, root *os.Root, timeout time.Duration) error {
	if lock == nil || lock.projectRoot == nil || root == nil {
		return errors.New("project apply lock and project data root are required")
	}
	file, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open pinned project data root for apply lock: %w", err)
	}
	if err := acquireApplyFileLock(file, timeout); err != nil {
		_ = file.Close()
		return fmt.Errorf("acquire project-data apply lock: %w", err)
	}
	lock.projectData = file
	return nil
}

func acquireApplyFileLock(file *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		locked, err := tryApplyPlatformLock(file)
		if err != nil {
			return err
		}
		if locked {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errors.New("project apply transaction remains locked by a live owner")
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
	if lock == nil {
		return nil
	}
	var err error
	if lock.projectData != nil {
		err = errors.Join(err, syscall.Flock(int(lock.projectData.Fd()), syscall.LOCK_UN), lock.projectData.Close())
	}
	if lock.projectRoot != nil {
		err = errors.Join(err, syscall.Flock(int(lock.projectRoot.Fd()), syscall.LOCK_UN), lock.projectRoot.Close())
	}
	return err
}
