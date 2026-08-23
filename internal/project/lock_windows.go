//go:build windows

package project

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const projectLockfileFailImmediately = 0x1
const projectLockfileExclusiveLock = 0x2

var projectKernel32 = syscall.NewLazyDLL("kernel32.dll")
var projectProcLockFileEx = projectKernel32.NewProc("LockFileEx")
var projectProcUnlockFileEx = projectKernel32.NewProc("UnlockFileEx")

func isProjectLockRedirect(info fs.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// Windows mode bits are not an ACL representation. Chmod is still applied by
// the caller, while ACL privacy remains outside this portable contract.
func privateProjectLockMode(fs.FileInfo) bool { return true }

func tryProjectPlatformLock(file *os.File) (bool, error) {
	var overlapped syscall.Overlapped
	result, _, callErr := projectProcLockFileEx.Call(file.Fd(), projectLockfileExclusiveLock|projectLockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	runtime.KeepAlive(file)
	if result != 0 {
		return true, nil
	}
	if errors.Is(callErr, syscall.Errno(33)) {
		return false, nil
	}
	return false, callErr
}

func unlockProjectPlatformLock(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := projectProcUnlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	runtime.KeepAlive(file)
	if result == 0 {
		return callErr
	}
	return nil
}
