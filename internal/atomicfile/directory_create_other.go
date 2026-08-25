//go:build !windows

package atomicfile

import (
	"io/fs"
	"os"
)

func createRootDirectoryFile(parent *os.Root, name string, perm fs.FileMode) (*os.File, error) {
	if err := parent.Mkdir(name, perm); err != nil {
		return nil, err
	}
	file, err := parent.Open(name)
	if err != nil {
		return nil, err
	}
	return file, nil
}
