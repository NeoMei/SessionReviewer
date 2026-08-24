//go:build !windows

package atomicfile

import "io/fs"

func safeAtomicWriterMode(mode fs.FileMode) bool {
	return mode == mode.Perm() && mode&0o700 == 0o600 && mode&0o133 == 0
}
