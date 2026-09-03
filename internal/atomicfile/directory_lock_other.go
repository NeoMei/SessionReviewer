//go:build !windows

package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockRootDirectoryParent serializes cooperative creators with an empty,
// private lock file opened relative to the physical pinned parent. A separate
// lock inode avoids self-deadlock with higher-level locks that anchor the parent
// directory itself. Flock remains advisory: a same-UID process that deliberately
// ignores this contract remains inside the operating-system permission domain.
func lockRootDirectoryParent(parent *os.Root) (func() error, error) {
	var file *os.File
	for {
		before, inspectErr := parent.Lstat(rootDirectoryLockName)
		found := inspectErr == nil
		if inspectErr != nil && !errors.Is(inspectErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect directory creation lock: %w", inspectErr)
		}
		if found && !validRootDirectoryLock(before) {
			if repairRootDirectoryLock(parent, before) {
				continue
			}
			return nil, errors.New("directory creation lock is redirected or unsafe")
		}
		flags := os.O_RDWR
		if !found {
			flags |= os.O_CREATE | os.O_EXCL
		}
		created, openErr := parent.OpenFile(rootDirectoryLockName, flags, 0o600)
		if errors.Is(openErr, os.ErrExist) || errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return nil, fmt.Errorf("open directory creation lock: %w", openErr)
		}
		opened, statErr := created.Stat()
		after, afterErr := parent.Lstat(rootDirectoryLockName)
		if statErr != nil || afterErr != nil || !validRootDirectoryLock(opened) || !os.SameFile(opened, after) || (found && !os.SameFile(before, opened)) {
			_ = created.Close()
			return nil, errors.New("directory creation lock identity changed while opening")
		}
		if !found {
			if err := created.Chmod(0o600); err != nil {
				_ = created.Close()
				return nil, fmt.Errorf("secure directory creation lock: %w", err)
			}
			if err := created.Sync(); err != nil {
				_ = created.Close()
				return nil, fmt.Errorf("sync directory creation lock: %w", err)
			}
			if err := syncRootDirectoryEntry(parent, rootDirectoryLockName); err != nil {
				_ = created.Close()
				return nil, fmt.Errorf("sync directory creation lock publication: %w", err)
			}
		}
		file = created
		break
	}
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			if err != nil {
				return nil, errors.Join(err, file.Close())
			}
			break
		}
	}
	afterLock, err := parent.Lstat(rootDirectoryLockName)
	opened, statErr := file.Stat()
	if err != nil || statErr != nil || !validRootDirectoryLock(opened) || !os.SameFile(opened, afterLock) {
		return nil, errors.Join(errors.New("directory creation lock identity changed after acquisition"), syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
	}
	return func() error {
		return errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
	}, nil
}

func validRootDirectoryLock(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && !isAtomicRedirect(info) && info.Mode().Perm() == 0o600 && info.Size() == 0
}

func repairRootDirectoryLock(parent *os.Root, expected os.FileInfo) bool {
	if expected == nil || !expected.Mode().IsRegular() || isAtomicRedirect(expected) || expected.Size() != 0 {
		return false
	}
	mode := expected.Mode()
	if mode != mode.Perm() || mode.Perm()&0o600 != 0o600 || mode.Perm()&0o022 != 0 {
		return false
	}
	file, err := parent.OpenFile(rootDirectoryLockName, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(expected, opened) || opened.Size() != 0 ||
		!opened.Mode().IsRegular() || isAtomicRedirect(opened) {
		_ = file.Close()
		return false
	}
	chmodErr := file.Chmod(0o600)
	updated, updatedErr := file.Stat()
	closeErr := file.Close()
	named, namedErr := parent.Lstat(rootDirectoryLockName)
	return chmodErr == nil && updatedErr == nil && closeErr == nil && namedErr == nil &&
		os.SameFile(expected, updated) && os.SameFile(updated, named) &&
		validRootDirectoryLock(updated) && validRootDirectoryLock(named)
}

// ValidateRootDirectoryLock verifies the unique POSIX operational artifact
// without repairing or following it.
func ValidateRootDirectoryLock(parent *os.Root, name string) error {
	if parent == nil || !IsRootDirectoryLockName(name) {
		return errors.New("directory creation lock name is invalid")
	}
	info, err := parent.Lstat(name)
	if err != nil || !validRootDirectoryLock(info) {
		return errors.New("directory creation lock is redirected or unsafe")
	}
	return nil
}
