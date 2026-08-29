//go:build !windows

package sync

import (
	"errors"
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

func reviewTargetSecurityHandleIdentity(file *os.File) (os.FileInfo, error) {
	if file == nil {
		return nil, errors.New("review-target security handle is not a directory")
	}
	info, err := file.Stat()
	if err != nil || info == nil || !info.IsDir() {
		return nil, errors.New("review-target security handle is not a directory")
	}
	return info, nil
}

func reviewTargetDirectoryProtected(_ string, info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode().Perm() == 0o700
}
