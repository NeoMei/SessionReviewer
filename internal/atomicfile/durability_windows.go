//go:build windows

package atomicfile

import (
	"io/fs"
	"os"
	"syscall"
)

// Windows does not provide the Unix fsync(directory) contract through Go's
// portable API. Flush the published destination handle, then attempt to flush
// the pinned directory handle. Any unsupported or failed flush is returned;
// native-Windows crash testing is required before claiming Unix equivalence.
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
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errorsJoin(syncErr, closeErr)
}

func isAtomicRedirect(info fs.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
