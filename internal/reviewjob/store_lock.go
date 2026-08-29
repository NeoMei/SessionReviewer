package reviewjob

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const storeLockPollInterval = 10 * time.Millisecond

type storeFileLock struct {
	mu   sync.Mutex
	root *os.Root
	name string
	file *os.File
	info os.FileInfo
	// created is true only when this acquisition published the stable leaf.
	// Guarded Store bootstrap uses it to roll back an otherwise empty layout
	// without ever removing a pre-existing or concurrently replaced lock.
	created bool
}

// acquireStoreFileLock locks only the stable private lock file below the
// already pinned locks directory. It deliberately does not lock directory
// ancestors, so long-lived worker leases can coexist with short CAS writes.
func acquireStoreFileLock(root *os.Root, name string, timeout time.Duration) (*storeFileLock, error) {
	return acquireStoreFileLockChecked(root, name, timeout, nil)
}

func acquireStoreFileLockChecked(root *os.Root, name string, timeout time.Duration, checkpoint func() error) (*storeFileLock, error) {
	if root == nil || name != "store.lock" || timeout < 0 {
		return nil, errors.New("invalid review job store lock request")
	}
	return acquirePrivateFileLockChecked(root, name, timeout, checkpoint)
}

var errPrivateFileLocked = errors.New("private advisory lock remains held by a live owner")

// acquirePrivateFileLock owns only one already-rooted stable leaf. It does not
// lock directory ancestors, so independently named long-lived worker leases
// can coexist with the short store CAS lock.
func acquirePrivateFileLock(root *os.Root, name string, timeout time.Duration) (*storeFileLock, error) {
	return acquirePrivateFileLockChecked(root, name, timeout, nil)
}

func acquirePrivateFileLockChecked(root *os.Root, name string, timeout time.Duration, checkpoint func() error) (*storeFileLock, error) {
	if root == nil || !validPrivateLockLeaf(name) || timeout < 0 {
		return nil, errors.New("invalid private advisory lock request")
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := rejectCaseCollision(root, name); err != nil {
			return nil, err
		}
		file, info, created, err := openStableStoreLockFileChecked(root, name, checkpoint)
		if err != nil {
			return nil, err
		}
		locked, err := tryStorePlatformLock(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("acquire review job store lock: %w", err)
		}
		if locked {
			after, inspectErr := root.Lstat(name)
			opened, statErr := file.Stat()
			if inspectErr != nil || statErr != nil || !sameStoreLockEntry(info, after) || !sameStoreLockEntry(info, opened) {
				return nil, errors.Join(errors.New("review job store lock identity changed after acquisition"), unlockStorePlatformLock(file), file.Close())
			}
			if err := rejectCaseCollision(root, name); err != nil {
				return nil, errors.Join(err, unlockStorePlatformLock(file), file.Close())
			}
			return &storeFileLock{root: root, name: name, file: file, info: info, created: created}, nil
		}
		_ = file.Close()
		if !time.Now().Before(deadline) {
			return nil, errPrivateFileLocked
		}
		time.Sleep(storeLockPollInterval)
	}
}

func validPrivateLockLeaf(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`) && len(name) <= 255
}

func openStableStoreLockFile(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	file, info, _, err := openStableStoreLockFileChecked(root, name, nil)
	return file, info, err
}

func openStableStoreLockFileChecked(root *os.Root, name string, checkpoint func() error) (*os.File, os.FileInfo, bool, error) {
	for {
		before, found, err := regularPrivateEntry(root, name)
		if err != nil {
			return nil, nil, false, err
		}
		flags := os.O_RDWR
		if !found {
			if checkpoint != nil {
				if err := checkpoint(); err != nil {
					return nil, nil, false, err
				}
			}
			flags |= os.O_CREATE | os.O_EXCL
		}
		file, err := root.OpenFile(name, flags, 0o600)
		if errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, false, errors.New("cannot open review job store lock")
		}
		created := !found
		createdInfo, inspectErr := file.Stat()
		if inspectErr != nil {
			_ = file.Close()
			return nil, nil, false, errors.New("cannot inspect opened review job store lock")
		}
		file, err = stabilizeStoreLockFile(file)
		if err != nil {
			if created {
				_ = removeOwnedPrivateLock(root, name, createdInfo)
			}
			return nil, nil, false, err
		}
		opened, err := file.Stat()
		if err != nil {
			_ = file.Close()
			if created {
				_ = removeOwnedPrivateLock(root, name, createdInfo)
			}
			return nil, nil, false, errors.New("cannot inspect review job store lock")
		}
		after, afterFound, err := regularPrivateEntry(root, name)
		if err != nil || !afterFound || !os.SameFile(opened, after) || (found && !os.SameFile(before, opened)) {
			_ = file.Close()
			if err != nil {
				if created {
					_ = removeOwnedPrivateLock(root, name, createdInfo)
				}
				return nil, nil, false, err
			}
			continue
		}
		if created {
			if err := file.Chmod(0o600); err != nil {
				_ = file.Close()
				_ = removeOwnedPrivateLock(root, name, opened)
				return nil, nil, false, errors.New("cannot protect review job store lock")
			}
		}
		opened, err = file.Stat()
		after, inspectErr = root.Lstat(name)
		if err != nil || inspectErr != nil || !sameStoreLockEntry(opened, after) {
			_ = file.Close()
			if created {
				_ = removeOwnedPrivateLock(root, name, createdInfo)
			}
			return nil, nil, false, errors.New("cannot verify review job store lock")
		}
		return file, opened, created, nil
	}
}

func removeOwnedPrivateLock(root *os.Root, name string, expected os.FileInfo) error {
	current, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || expected == nil || current == nil || !os.SameFile(expected, current) ||
		!expected.Mode().IsRegular() || !current.Mode().IsRegular() || isRedirect(expected) || isRedirect(current) || current.Size() != 0 {
		return errors.New("owned private lock changed before rollback")
	}
	return root.Remove(name)
}

func sameStoreLockEntry(first, second os.FileInfo) bool {
	return first != nil && second != nil && os.SameFile(first, second) && first.Mode().IsRegular() && second.Mode().IsRegular() &&
		!isRedirect(first) && !isRedirect(second) && privateMode(first, 0o600) && privateMode(second, 0o600)
}

func (lock *storeFileLock) release() error {
	if lock == nil {
		return nil
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.file == nil {
		return nil
	}
	after, err := lock.root.Lstat(lock.name)
	var identityErr error
	if err != nil || !sameStoreLockEntry(lock.info, after) {
		identityErr = errors.New("review job store lock identity changed before release")
	}
	file := lock.file
	lock.file = nil
	return errors.Join(identityErr, unlockStorePlatformLock(file), file.Close())
}

func (lock *storeFileLock) replaceContent(body []byte, maxBytes int) error {
	if lock == nil || len(body) == 0 || len(body) > maxBytes {
		return errors.New("invalid private advisory lock metadata")
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.file == nil {
		return errors.New("private advisory lock is not held")
	}
	if err := rejectCaseCollision(lock.root, lock.name); err != nil {
		return err
	}
	before, err := lock.root.Lstat(lock.name)
	opened, statErr := lock.file.Stat()
	if err != nil || statErr != nil || !sameStoreLockEntry(lock.info, before) || !sameStoreLockEntry(lock.info, opened) {
		return errors.New("private advisory lock identity changed before metadata write")
	}
	if err := lock.file.Truncate(0); err != nil {
		return errors.New("cannot truncate private advisory lock metadata")
	}
	if _, err := lock.file.Seek(0, io.SeekStart); err != nil {
		return errors.New("cannot seek private advisory lock metadata")
	}
	if _, err := lock.file.Write(body); err != nil {
		return errors.New("cannot write private advisory lock metadata")
	}
	if err := lock.file.Sync(); err != nil {
		return errors.New("cannot persist private advisory lock metadata")
	}
	written := make([]byte, len(body)+1)
	count, readErr := lock.file.ReadAt(written, 0)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return errors.New("cannot verify private advisory lock metadata")
	}
	after, inspectErr := lock.root.Lstat(lock.name)
	caseErr := rejectCaseCollision(lock.root, lock.name)
	if inspectErr != nil || caseErr != nil || !sameStoreLockEntry(lock.info, after) || count != len(body) || !bytes.Equal(written[:count], body) {
		return errors.New("private advisory lock metadata failed verification")
	}
	return nil
}
