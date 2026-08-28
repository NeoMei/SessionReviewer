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
	// os/exec performs the child chdir before it remaps ExtraFiles to fd 3+.
	// Resolve the directory through the anchor's actual inherited descriptor;
	// ExtraFiles keeps that descriptor alive across exec after chdir succeeds.
	inheritedFD := anchor.Fd()
	command.ExtraFiles = append(command.ExtraFiles, anchor)
	path := fmt.Sprintf("/proc/self/fd/%d", inheritedFD)
	command.Dir = path
	return path, nil
}

func recheckVisiblePrivateDirectory(_ string, _ os.FileInfo) error {
	// Linux chdir resolves the inherited directory descriptor in the child.
	return nil
}
