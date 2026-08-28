//go:build !windows && !darwin && !linux

package codex

import (
	"errors"
	"os"
	"os/exec"
)

func configureCommandDirectory(command *exec.Cmd, anchor *os.File, physicalPath string) (string, error) {
	if anchor == nil {
		return "", errors.New("private directory anchor is closed")
	}
	command.Dir = physicalPath
	return physicalPath, nil
}

func recheckVisiblePrivateDirectory(path string, expected os.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, current) {
		return errPrivateRootIdentityChanged
	}
	return nil
}
