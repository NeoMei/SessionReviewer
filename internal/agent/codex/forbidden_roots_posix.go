//go:build linux || darwin

package codex

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func canonicalPathEqual(first, second string) bool {
	return filepath.Clean(first) == filepath.Clean(second)
}

func validateForbiddenRootPlatform(_ string, _ *os.File) error { return nil }

func physicalDirectoriesOverlap(first, second *os.File) (bool, error) {
	firstContainsSecond, err := physicalDirectoryContains(first, second)
	if err != nil || firstContainsSecond {
		return firstContainsSecond, err
	}
	return physicalDirectoryContains(second, first)
}

func physicalDirectoryContains(ancestor, descendant *os.File) (bool, error) {
	if ancestor == nil || descendant == nil {
		return false, errors.New("directory identity is unavailable")
	}
	ancestorInfo, err := ancestor.Stat()
	if err != nil {
		return false, err
	}
	fd, err := unix.Dup(int(descendant.Fd()))
	if err != nil {
		return false, err
	}
	current := os.NewFile(uintptr(fd), "forbidden-root-walk")
	if current == nil {
		_ = unix.Close(fd)
		return false, errors.New("could not anchor directory ancestry walk")
	}
	defer func() { _ = current.Close() }()
	for depth := 0; depth < 4096; depth++ {
		currentInfo, statErr := current.Stat()
		if statErr != nil {
			return false, statErr
		}
		if os.SameFile(ancestorInfo, currentInfo) {
			return true, nil
		}
		parentFD, openErr := unix.Openat(int(current.Fd()), "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return false, openErr
		}
		parent := os.NewFile(uintptr(parentFD), "forbidden-root-parent")
		if parent == nil {
			_ = unix.Close(parentFD)
			return false, errors.New("could not open directory parent")
		}
		parentInfo, parentErr := parent.Stat()
		if parentErr != nil {
			_ = parent.Close()
			return false, parentErr
		}
		if os.SameFile(currentInfo, parentInfo) {
			_ = parent.Close()
			return false, nil
		}
		_ = current.Close()
		current = parent
	}
	return false, errors.New("directory ancestry exceeded reviewed bound")
}
