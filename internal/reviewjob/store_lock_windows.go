//go:build windows

package reviewjob

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	storeLockfileFailImmediately = 0x1
	storeLockfileExclusiveLock   = 0x2
)

var (
	storeKernel32         = syscall.NewLazyDLL("kernel32.dll")
	storeProcLockFileEx   = storeKernel32.NewProc("LockFileEx")
	storeProcUnlockFileEx = storeKernel32.NewProc("UnlockFileEx")
	storeProcReOpenFile   = storeKernel32.NewProc("ReOpenFile")
)

func stabilizeStoreLockFile(file *os.File) (*os.File, error) {
	result, _, callErr := storeProcReOpenFile.Call(
		file.Fd(),
		uintptr(syscall.GENERIC_READ|syscall.GENERIC_WRITE),
		uintptr(syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE),
		0,
	)
	runtime.KeepAlive(file)
	if syscall.Handle(result) == syscall.InvalidHandle {
		_ = file.Close()
		return nil, callErr
	}
	stable := os.NewFile(result, file.Name())
	if stable == nil {
		_ = syscall.CloseHandle(syscall.Handle(result))
		_ = file.Close()
		return nil, errors.New("cannot adopt stable review job store lock handle")
	}
	if err := file.Close(); err != nil {
		_ = stable.Close()
		return nil, err
	}
	return stable, nil
}

func tryStorePlatformLock(file *os.File) (bool, error) {
	var overlapped syscall.Overlapped
	result, _, callErr := storeProcLockFileEx.Call(file.Fd(), storeLockfileExclusiveLock|storeLockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	runtime.KeepAlive(file)
	if result != 0 {
		return true, nil
	}
	if errors.Is(callErr, syscall.Errno(33)) {
		return false, nil
	}
	return false, callErr
}

func unlockStorePlatformLock(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := storeProcUnlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	runtime.KeepAlive(file)
	if result == 0 {
		return callErr
	}
	return nil
}
