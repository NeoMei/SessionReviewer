//go:build !windows

package sync

import (
	"os"
)

func openReviewTargetSecurityHandle(parent *os.Root, component string) (*os.File, error) {
	if parent == nil {
		return nil, os.ErrInvalid
	}
	return parent.Open(component)
}

func protectReviewTargetDirectory(file *os.File) error {
	if file == nil {
		return os.ErrInvalid
	}
	return file.Chmod(0o700)
}

func reviewTargetDirectoryProtected(_ string, info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode().Perm() == 0o700
}
