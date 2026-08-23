//go:build windows

package sync

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	baseLockfileFailImmediately = 0x00000001
	baseLockfileExclusiveLock   = 0x00000002
	baseErrorLockViolation      = syscall.Errno(33)
)

var (
	baseKernel32         = syscall.NewLazyDLL("kernel32.dll")
	baseProcLockFileEx   = baseKernel32.NewProc("LockFileEx")
	baseProcUnlockFileEx = baseKernel32.NewProc("UnlockFileEx")
)

func tryBasePlatformLock(file *os.File) (bool, error) {
	var overlapped syscall.Overlapped
	result, _, callErr := baseProcLockFileEx.Call(
		file.Fd(),
		baseLockfileExclusiveLock|baseLockfileFailImmediately,
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	runtime.KeepAlive(file)
	if result != 0 {
		return true, nil
	}
	if errors.Is(callErr, baseErrorLockViolation) {
		return false, nil
	}
	return false, callErr
}

func unlockBasePlatformLock(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := baseProcUnlockFileEx.Call(
		file.Fd(),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	runtime.KeepAlive(file)
	if result == 0 {
		return callErr
	}
	return nil
}
