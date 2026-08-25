//go:build windows

package atomicfile

import (
	"io/fs"
	"os"
)

func setExactCreatedRootDirectoryMode(*os.File, fs.FileMode) error {
	return nil
}

// Windows directory privacy and durability are provided by the handle-bound
// protected DACL preparation and namespace identity checks, respectively.
func syncCreatedRootDirectory(*os.File) error {
	return nil
}
