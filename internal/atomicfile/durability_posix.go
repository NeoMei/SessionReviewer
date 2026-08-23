//go:build !windows

package atomicfile

import (
	"io/fs"
	"os"
)

func syncRootPublication(parent *os.Root, _ string) error {
	return syncPinnedDirectory(parent)
}

func syncRootDirectoryEntry(parent *os.Root, _ string) error {
	return syncPinnedDirectory(parent)
}

func syncPinnedDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errorsJoin(syncErr, closeErr)
}

func isAtomicRedirect(info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
