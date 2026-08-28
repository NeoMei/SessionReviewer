package reviewjob

import (
	"errors"
	"fmt"
	"os"
	"time"
)

const storeLockPollInterval = 10 * time.Millisecond

type storeFileLock struct {
	root *os.Root
	name string
	file *os.File
	info os.FileInfo
}

// acquireStoreFileLock locks only the stable private lock file below the
// already pinned locks directory. It deliberately does not lock directory
// ancestors, so long-lived worker leases can coexist with short CAS writes.
func acquireStoreFileLock(root *os.Root, name string, timeout time.Duration) (*storeFileLock, error) {
	if root == nil || name != "store.lock" || timeout < 0 {
		return nil, errors.New("invalid review job store lock request")
	}
	deadline := time.Now().Add(timeout)
	for {
		file, info, err := openStableStoreLockFile(root, name)
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
			return &storeFileLock{root: root, name: name, file: file, info: info}, nil
		}
		_ = file.Close()
		if !time.Now().Before(deadline) {
			return nil, errors.New("review job store remains locked by a live owner")
		}
		time.Sleep(storeLockPollInterval)
	}
}

func openStableStoreLockFile(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	for {
		before, found, err := regularPrivateEntry(root, name)
		if err != nil {
			return nil, nil, err
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
			return nil, nil, errors.New("cannot open review job store lock")
		}
		file, err = stabilizeStoreLockFile(file)
		if err != nil {
			return nil, nil, err
		}
		opened, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return nil, nil, errors.New("cannot inspect review job store lock")
		}
		after, afterFound, err := regularPrivateEntry(root, name)
		if err != nil || !afterFound || !os.SameFile(opened, after) || (found && !os.SameFile(before, opened)) {
			_ = file.Close()
			if err != nil {
				return nil, nil, err
			}
			continue
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, nil, errors.New("cannot protect review job store lock")
		}
		opened, err = file.Stat()
		after, inspectErr := root.Lstat(name)
		if err != nil || inspectErr != nil || !sameStoreLockEntry(opened, after) {
			_ = file.Close()
			return nil, nil, errors.New("cannot verify review job store lock")
		}
		return file, opened, nil
	}
}

func sameStoreLockEntry(first, second os.FileInfo) bool {
	return first != nil && second != nil && os.SameFile(first, second) && first.Mode().IsRegular() && second.Mode().IsRegular() &&
		!isRedirect(first) && !isRedirect(second) && privateMode(first, 0o600) && privateMode(second, 0o600)
}

func (lock *storeFileLock) release() error {
	if lock == nil || lock.file == nil {
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
