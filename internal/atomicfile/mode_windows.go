//go:build windows

package atomicfile

import "io/fs"

func safeAtomicWriterMode(mode fs.FileMode) bool {
	// Go exposes writable Windows files as 0666 regardless of the POSIX mode
	// passed to Chmod. Type and reparse-point checks are performed separately.
	return mode == mode.Perm() && mode.Perm()&0o200 != 0
}
