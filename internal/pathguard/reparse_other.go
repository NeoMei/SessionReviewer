//go:build !windows

package pathguard

import (
	"io/fs"
	"os"
)

func isRedirect(info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
