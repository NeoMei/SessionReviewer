//go:build windows

package atomicfile

import (
	"errors"
	"os"
)

func lockRootDirectoryParent(*os.Root) (func() error, error) {
	return func() error { return nil }, nil
}

// Windows directory creation is handle-atomic and does not publish a POSIX
// advisory-lock artifact. Recovery rejects one rather than accepting a
// cross-platform downgrade.
func ValidateRootDirectoryLock(*os.Root, string) error {
	return errors.New("directory creation lock artifact is unsupported on windows")
}
