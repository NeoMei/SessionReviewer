//go:build !windows

package cli

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

func privateReviewHandshakeFlag() string { return "handshake-fd" }

func detachedReviewHandshakeValue(*os.File) (string, error) { return "3", nil }

func configureDetachedReviewCommand(command *exec.Cmd, handshake *os.File) (func() error, error) {
	if command == nil || handshake == nil {
		return nil, errors.New("detached command and handshake are required")
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	command.Stdin = null
	command.Stdout = null
	command.Stderr = null
	command.ExtraFiles = []*os.File{handshake}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return null.Close, nil
}

func inheritedReviewHandshake(value string) (*os.File, error) {
	descriptor, err := strconv.ParseUint(value, 10, 32)
	if err != nil || descriptor != 3 {
		return nil, errors.New("invalid inherited review handshake descriptor")
	}
	file := os.NewFile(uintptr(descriptor), "review-handshake")
	if file == nil {
		return nil, errors.New("inherited review handshake is unavailable")
	}
	return file, nil
}
