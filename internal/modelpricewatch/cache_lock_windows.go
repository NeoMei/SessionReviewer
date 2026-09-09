//go:build windows

package modelpricewatch

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

const cacheLockfileExclusiveLock = 0x00000002
const cacheLockfileFailImmediately = 0x00000001

var cacheKernel32 = syscall.NewLazyDLL("kernel32.dll")
var cacheProcLockFileEx = cacheKernel32.NewProc("LockFileEx")
var cacheProcUnlockFileEx = cacheKernel32.NewProc("UnlockFileEx")

func tryCachePlatformLock(file *os.File) (bool, error) {
	var overlapped syscall.Overlapped
	result, _, callErr := cacheProcLockFileEx.Call(file.Fd(), cacheLockfileExclusiveLock|cacheLockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result != 0 {
		return true, nil
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno == 33 {
		return false, nil
	}
	return false, callErr
}
func unlockCachePlatformLock(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := cacheProcUnlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return errors.New(callErr.Error())
	}
	return nil
}
