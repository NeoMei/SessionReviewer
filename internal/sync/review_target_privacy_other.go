//go:build !windows

package sync

import (
	"os"
)

func protectReviewTargetDirectory(file *os.File) error {
	if file == nil {
		return os.ErrInvalid
	}
	return file.Chmod(0o700)
}

func reviewTargetDirectoryProtected(_ string, info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode().Perm() == 0o700
}
