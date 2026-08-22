//go:build windows

package prepare

import (
	"io/fs"
	"os"
	"syscall"
)

func isSymlinkOrReparse(info fs.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
