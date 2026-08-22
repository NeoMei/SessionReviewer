//go:build !windows

package prepare

import (
	"io/fs"
	"os"
)

func isSymlinkOrReparse(info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
