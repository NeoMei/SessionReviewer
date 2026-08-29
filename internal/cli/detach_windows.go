//go:build windows

package cli

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

func privateReviewHandshakeFlag() string { return "handshake-handle" }

func detachedReviewHandshakeValue(handshake *os.File) (string, error) {
	if handshake == nil || handshake.Fd() == 0 {
		return "", errors.New("review handshake handle is unavailable")
	}
	return strconv.FormatUint(uint64(handshake.Fd()), 10), nil
}

func configureDetachedReviewCommand(command *exec.Cmd, handshake *os.File) (func() error, error) {
	if command == nil || handshake == nil {
		return nil, errors.New("detached command and handshake are required")
	}
	handle := windows.Handle(handshake.Fd())
	policy, err := detachedReviewInheritancePolicy(uintptr(handle))
	if err != nil {
		return nil, err
	}
	if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		return nil, err
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		_ = windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, 0)
		return nil, err
	}
	command.Stdin = null
	command.Stdout = null
	command.Stderr = null
	command.SysProcAttr = windowsDetachedReviewSysProcAttr(policy)
	return func() error {
		return errors.Join(
			windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, 0),
			null.Close(),
		)
	}, nil
}

func windowsDetachedReviewSysProcAttr(policy detachedReviewInheritance) *syscall.SysProcAttr {
	handles := make([]syscall.Handle, len(policy.additionalHandles))
	for index, handle := range policy.additionalHandles {
		handles[index] = syscall.Handle(handle)
	}
	return &syscall.SysProcAttr{
		CreationFlags:              windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
		NoInheritHandles:           policy.noInheritHandles,
		AdditionalInheritedHandles: handles,
	}
}

func inheritedReviewHandshake(value string) (*os.File, error) {
	handle, err := strconv.ParseUint(value, 10, 64)
	if err != nil || handle == 0 {
		return nil, errors.New("invalid inherited review handshake handle")
	}
	file := os.NewFile(uintptr(handle), "review-handshake")
	if file == nil {
		return nil, errors.New("inherited review handshake is unavailable")
	}
	return file, nil
}
