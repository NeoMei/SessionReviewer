//go:build darwin

package codex

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func configureCommandDirectory(command *exec.Cmd, anchor *os.File, _ string) (string, error) {
	if anchor == nil {
		return "", errors.New("private directory anchor is closed")
	}
	var buffer [4096]byte
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, anchor.Fd(), uintptr(unix.F_GETPATH), uintptr(unsafe.Pointer(&buffer[0])))
	if errno != 0 {
		return "", errno
	}
	path := unix.ByteSliceToString(buffer[:])
	if path == "" {
		return "", errors.New("private directory anchor has no physical path")
	}
	command.Dir = path
	return path, nil
}

func recheckVisiblePrivateDirectory(path string, expected os.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, current) {
		return errPrivateRootIdentityChanged
	}
	return nil
}
