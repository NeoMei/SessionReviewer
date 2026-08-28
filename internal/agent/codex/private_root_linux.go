//go:build linux

package codex

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

func configureCommandDirectory(command *exec.Cmd, anchor *os.File, _ string) (string, error) {
	if anchor == nil {
		return "", errors.New("private directory anchor is closed")
	}
	childFD := 3 + len(command.ExtraFiles)
	command.ExtraFiles = append(command.ExtraFiles, anchor)
	path := fmt.Sprintf("/proc/self/fd/%d", childFD)
	command.Dir = path
	return path, nil
}

func recheckVisiblePrivateDirectory(_ string, _ os.FileInfo) error {
	// Linux chdir resolves the inherited directory descriptor in the child.
	return nil
}
