//go:build windows

package project

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const initLockfileFailImmediately = 0x1
const initLockfileExclusiveLock = 0x2

var initKernel32 = syscall.NewLazyDLL("kernel32.dll")
var initProcLockFileEx = initKernel32.NewProc("LockFileEx")
var initProcUnlockFileEx = initKernel32.NewProc("UnlockFileEx")

func tryInitPlatformLock(file *os.File) (bool, error) {
	var overlapped syscall.Overlapped
	result, _, callErr := initProcLockFileEx.Call(file.Fd(), initLockfileExclusiveLock|initLockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	runtime.KeepAlive(file)
	if result != 0 {
		return true, nil
	}
	if errors.Is(callErr, syscall.Errno(33)) {
		return false, nil
	}
	return false, callErr
}

func unlockInitPlatformLock(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := initProcUnlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	runtime.KeepAlive(file)
	if result == 0 {
		return callErr
	}
	return nil
}
