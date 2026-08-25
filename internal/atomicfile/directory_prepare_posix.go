//go:build !windows

package atomicfile

import (
	"io/fs"
	"os"
)

func setExactCreatedRootDirectoryMode(file *os.File, perm fs.FileMode) error {
	return file.Chmod(perm.Perm())
}

func syncCreatedRootDirectory(file *os.File) error {
	return file.Sync()
}
