//go:build !windows

package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

type projectNamespaceAnchor struct {
	file *os.File
}

func openProjectNamespaceAnchor(parent *os.Root, _ string) (*projectNamespaceAnchor, error) {
	file, err := parent.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open pinned project lock parent: %w", err)
	}
	return &projectNamespaceAnchor{file: file}, nil
}

func tryProjectNamespaceAnchor(anchor *projectNamespaceAnchor) (bool, error) {
	return tryProjectPlatformLock(anchor.file)
}

func releaseProjectNamespaceAnchor(anchor *projectNamespaceAnchor) error {
	if anchor == nil || anchor.file == nil {
		return nil
	}
	file := anchor.file
	anchor.file = nil
	return errors.Join(unlockProjectPlatformLock(file), file.Close())
}

func closeProjectNamespaceAnchor(anchor *projectNamespaceAnchor) error {
	if anchor == nil || anchor.file == nil {
		return nil
	}
	file := anchor.file
	anchor.file = nil
	return file.Close()
}

func isProjectLockRedirect(info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}

func privateProjectLockMode(info fs.FileInfo) bool {
	return info.Mode().Perm() == 0o600
}

func stabilizeProjectLockFile(file *os.File) (*os.File, error) {
	return file, nil
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
