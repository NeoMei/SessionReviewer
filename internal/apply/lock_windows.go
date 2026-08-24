//go:build windows

package apply

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

func isApplyRedirect(info fs.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

const (
	applyWaitObject0   = 0
	applyWaitAbandoned = 0x80
	applyWaitTimeout   = 0x102
)

var applyKernel32 = syscall.NewLazyDLL("kernel32.dll")
var applyProcCreateMutexW = applyKernel32.NewProc("CreateMutexW")
var applyProcWaitForSingleObject = applyKernel32.NewProc("WaitForSingleObject")
var applyProcReleaseMutex = applyKernel32.NewProc("ReleaseMutex")
var applyProcCloseHandle = applyKernel32.NewProc("CloseHandle")
var applyProcGetFinalPathNameByHandleW = applyKernel32.NewProc("GetFinalPathNameByHandleW")

type applyPlatformLock struct {
	handle       syscall.Handle
	threadPinned bool
}

func acquireApplyPlatformLock(_, dataRoot *os.Root, fallbackPath, projectID string, timeout time.Duration) (*applyPlatformLock, error) {
	identity, err := canonicalApplyDataIdentity(dataRoot, fallbackPath)
	if err != nil {
		return nil, err
	}
	name := windowsApplyMutexName(identity, projectID)
	encoded, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, fmt.Errorf("encode apply mutex name: %w", err)
	}
	handle, _, callErr := applyProcCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(encoded)))
	if handle == 0 {
		return nil, fmt.Errorf("create apply mutex: %w", callErr)
	}
	// A Windows mutex is owned by the acquiring OS thread. Keep this goroutine
	// on that thread until release so the Go scheduler cannot migrate it and
	// cause ReleaseMutex to fail with ERROR_NOT_OWNER.
	runtime.LockOSThread()
	waitMillis := uintptr(timeout / time.Millisecond)
	result, _, waitErr := applyProcWaitForSingleObject.Call(handle, waitMillis)
	if result == applyWaitObject0 || result == applyWaitAbandoned {
		return &applyPlatformLock{handle: syscall.Handle(handle), threadPinned: true}, nil
	}
	_, _, _ = applyProcCloseHandle.Call(handle)
	runtime.UnlockOSThread()
	if result == applyWaitTimeout {
		return nil, errors.New("project apply transaction remains locked by a live owner")
	}
	return nil, fmt.Errorf("wait for apply mutex: %w", waitErr)
}

func attachApplyProjectDataLock(lock *applyPlatformLock, root *os.Root, _ time.Duration) error {
	if lock == nil || lock.handle == 0 || root == nil {
		return errors.New("project apply mutex and project data root are required")
	}
	return nil
}

func canonicalApplyDataIdentity(root *os.Root, fallbackPath string) (string, error) {
	file, err := root.Open(".")
	if err != nil {
		return "", fmt.Errorf("open pinned data root for apply mutex identity: %w", err)
	}
	defer file.Close()
	buffer := make([]uint16, 512)
	for {
		length, _, callErr := applyProcGetFinalPathNameByHandleW.Call(file.Fd(), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0)
		runtime.KeepAlive(file)
		if length == 0 {
			return "", fmt.Errorf("resolve apply mutex data identity: %w", callErr)
		}
		if length < uintptr(len(buffer)) {
			resolved := syscall.UTF16ToString(buffer[:length])
			if strings.HasPrefix(resolved, `\\?\UNC\`) {
				resolved = `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`)
			} else {
				resolved = strings.TrimPrefix(resolved, `\\?\`)
			}
			return strings.ToLower(filepath.Clean(resolved)), nil
		}
		buffer = make([]uint16, int(length)+1)
		if len(buffer) > 32768 {
			return "", fmt.Errorf("resolved apply mutex data identity is too long: %q", filepath.Base(fallbackPath))
		}
	}
}

func releaseApplyPlatformLock(lock *applyPlatformLock) error {
	if lock == nil || lock.handle == 0 {
		return nil
	}
	defer func() {
		if lock.threadPinned {
			lock.threadPinned = false
			runtime.UnlockOSThread()
		}
	}()
	released, _, releaseErr := applyProcReleaseMutex.Call(uintptr(lock.handle))
	closed, _, closeErr := applyProcCloseHandle.Call(uintptr(lock.handle))
	lock.handle = 0
	if released == 0 || closed == 0 {
		return errors.Join(releaseErr, closeErr)
	}
	return nil
}
