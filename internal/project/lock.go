package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const projectLockPollInterval = 10 * time.Millisecond

var ErrProjectLocked = errors.New("project transaction remains locked by a live owner")

// ProjectLock owns an advisory operating-system lock on a persistent lock
// file. Release is safe to call on nil and more than once.
type ProjectLock struct {
	mu     sync.Mutex
	parent *os.Root
	name   string
	file   *os.File
	info   os.FileInfo
}

// AcquireProjectLock acquires an exclusive advisory lock below root. A zero
// timeout makes exactly one non-blocking attempt; a negative timeout is invalid.
func AcquireProjectLock(root *os.Root, name string, timeout time.Duration) (*ProjectLock, error) {
	if root == nil {
		return nil, errors.New("project lock root is required")
	}
	if timeout < 0 {
		return nil, errors.New("project lock timeout must not be negative")
	}
	parent, leaf, err := openProjectLockParent(root, name)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		file, info, err := openStableProjectLockFile(parent, leaf)
		if err != nil {
			_ = parent.Close()
			return nil, err
		}
		locked, err := tryProjectPlatformLock(file)
		if err != nil {
			_ = file.Close()
			_ = parent.Close()
			return nil, fmt.Errorf("acquire project advisory lock: %w", err)
		}
		if locked {
			after, inspectErr := parent.Lstat(leaf)
			opened, statErr := file.Stat()
			if inspectErr != nil || statErr != nil || !sameProjectLockEntry(info, after) || !sameProjectLockEntry(info, opened) {
				return nil, errors.Join(errors.New("project lock identity changed after acquisition"), unlockProjectPlatformLock(file), file.Close(), parent.Close())
			}
			return &ProjectLock{parent: parent, name: leaf, file: file, info: info}, nil
		}
		_ = file.Close()
		if !time.Now().Before(deadline) {
			_ = parent.Close()
			return nil, ErrProjectLocked
		}
		time.Sleep(projectLockPollInterval)
	}
}

func (lock *ProjectLock) Release() error {
	if lock == nil {
		return nil
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.file == nil {
		return nil
	}
	after, err := lock.parent.Lstat(lock.name)
	var identityErr error
	if err != nil || !sameProjectLockEntry(lock.info, after) {
		identityErr = errors.New("project lock identity changed before release")
	}
	file := lock.file
	parent := lock.parent
	lock.file = nil
	lock.parent = nil
	return errors.Join(identityErr, unlockProjectPlatformLock(file), file.Close(), parent.Close())
}

func openProjectLockParent(root *os.Root, name string) (*os.Root, string, error) {
	if name == "" || filepath.IsAbs(name) || filepath.VolumeName(name) != "" || strings.Contains(name, `\`) {
		return nil, "", errors.New("invalid project lock path")
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.ToSlash(clean) != name {
		return nil, "", errors.New("invalid project lock path")
	}
	components := strings.Split(clean, string(filepath.Separator))
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, "", errors.New("cannot pin project lock root")
	}
	for _, component := range components[:len(components)-1] {
		before, err := current.Lstat(component)
		if err != nil || before == nil || !before.IsDir() || isProjectLockRedirect(before) {
			_ = current.Close()
			return nil, "", errors.New("project lock parent is redirected or not a directory")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			_ = current.Close()
			return nil, "", errors.New("cannot open project lock parent")
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(before, opened) {
			_ = next.Close()
			_ = current.Close()
			return nil, "", errors.New("project lock parent changed while opening")
		}
		_ = current.Close()
		current = next
	}
	return current, components[len(components)-1], nil
}

func openStableProjectLockFile(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	for {
		before, err := root.Lstat(name)
		found := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, errors.New("cannot inspect project lock")
		}
		if found && (isProjectLockRedirect(before) || !before.Mode().IsRegular()) {
			return nil, nil, errors.New("project lock is redirected or not regular")
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
			return nil, nil, errors.New("cannot open project lock")
		}
		opened, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return nil, nil, errors.New("cannot inspect open project lock")
		}
		after, err := root.Lstat(name)
		if err != nil || !os.SameFile(opened, after) || isProjectLockRedirect(after) || !after.Mode().IsRegular() || (found && !os.SameFile(before, opened)) {
			_ = file.Close()
			if err != nil {
				return nil, nil, errors.New("project lock changed while opening")
			}
			continue
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, nil, errors.New("cannot protect project lock")
		}
		opened, err = file.Stat()
		after, inspectErr := root.Lstat(name)
		if err != nil || inspectErr != nil || !sameProjectLockEntry(opened, after) {
			_ = file.Close()
			return nil, nil, errors.New("cannot verify project lock permissions or identity")
		}
		return file, opened, nil
	}
}

func sameProjectLockEntry(first, second os.FileInfo) bool {
	return first != nil && second != nil && os.SameFile(first, second) && first.Mode().IsRegular() && second.Mode().IsRegular() &&
		!isProjectLockRedirect(first) && !isProjectLockRedirect(second) && privateProjectLockMode(first) && privateProjectLockMode(second)
}

// Compatibility for pre-export package-local initialization helpers.
type initLock = ProjectLock

func acquireInitLock(root *os.Root, name string, timeout time.Duration) (*initLock, error) {
	return AcquireProjectLock(root, name, timeout)
}

func (lock *ProjectLock) release() error { return lock.Release() }

func openStableInitLockFile(root *os.Root, name string) (*os.File, error) {
	file, _, err := openStableProjectLockFile(root, name)
	return file, err
}
