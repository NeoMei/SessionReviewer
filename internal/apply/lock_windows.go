//go:build windows

package apply

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

func isApplyRedirect(info fs.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

const applyLockfileFailImmediately = 0x1
const applyLockfileExclusiveLock = 0x2

var applyKernel32 = syscall.NewLazyDLL("kernel32.dll")
var applyProcLockFileEx = applyKernel32.NewProc("LockFileEx")
var applyProcUnlockFileEx = applyKernel32.NewProc("UnlockFileEx")

func tryApplyPlatformLock(file *os.File) (bool, error) {
	var overlapped syscall.Overlapped
	result, _, callErr := applyProcLockFileEx.Call(file.Fd(), applyLockfileExclusiveLock|applyLockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	runtime.KeepAlive(file)
	if result != 0 {
		return true, nil
	}
	if errors.Is(callErr, syscall.Errno(33)) {
		return false, nil
	}
	return false, callErr
}

func unlockApplyPlatformLock(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := applyProcUnlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	runtime.KeepAlive(file)
	if result == 0 {
		return callErr
	}
	return nil
}
