//go:build windows

package atomicfile

import (
	"io/fs"
	"os"
	"syscall"
)

// Windows does not provide the Unix fsync(directory) contract. Flush the
// published destination handle, then revalidate the pinned directory identity.
// Namespace operations remain atomic/no-clobber, but this does not claim Unix
// directory-metadata crash durability.
func syncRootPublication(parent *os.Root, destination string) error {
	file, err := parent.Open(destination)
	if err != nil {
		return err
	}
	fileSyncErr := file.Sync()
	fileCloseErr := file.Close()
	if err := errorsJoin(fileSyncErr, fileCloseErr); err != nil {
		return err
	}
	return syncPinnedDirectory(parent)
}

func syncRootDirectoryEntry(parent *os.Root, _ string) error {
	return syncPinnedDirectory(parent)
}

func syncPinnedDirectory(root *os.Root) error {
	before, err := root.Stat(".")
	if err != nil || before == nil || !before.IsDir() || isAtomicRedirect(before) {
		return os.ErrInvalid
	}
	after, err := root.Stat(".")
	if err != nil || after == nil || !os.SameFile(before, after) {
		return os.ErrInvalid
	}
	return nil
}

func isAtomicRedirect(info fs.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
