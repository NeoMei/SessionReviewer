//go:build !linux && !darwin && !windows

package codex

import (
	"os"
	"path/filepath"
)

func canonicalPathEqual(first, second string) bool {
	return filepath.Clean(first) == filepath.Clean(second)
}

func validateForbiddenRootPlatform(_ string, _ *os.File) error { return nil }

func physicalDirectoriesOverlap(first, second *os.File) (bool, error) {
	firstInfo, err := first.Stat()
	if err != nil {
		return false, err
	}
	secondInfo, err := second.Stat()
	if err != nil {
		return false, err
	}
	return os.SameFile(firstInfo, secondInfo), nil
}
