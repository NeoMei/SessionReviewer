//go:build !windows

package atomicfile

import (
	"io/fs"
	"os"
)

func createRootTempFile(root *os.Root, name string, perm fs.FileMode) (*os.File, error) {
	return root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
}
