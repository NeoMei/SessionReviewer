//go:build windows

package project

import (
	"errors"
	"fmt"
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
var projectProcReOpenFile = projectKernel32.NewProc("ReOpenFile")
var projectProcGetFinalPathNameByHandleW = projectKernel32.NewProc("GetFinalPathNameByHandleW")

const (
	projectFileAttributeNormal    = 0x00000080
	projectFileFlagBackupSemantic = 0x02000000
)

type projectNamespaceAnchor struct {
	parent *os.File
}

// Windows does not use LockFileEx on a directory. Open a second handle to the
// identity resolved from the rooted parent and deny FILE_SHARE_DELETE, so the
// directory namespace cannot be replaced while the separately stabilized file
// handle is protected by LockFileEx. ReOpenFile is not used for directories:
// it can reject directory handles returned by os.Root with access denied.
func openProjectNamespaceAnchor(parent *os.Root, _ string) (*projectNamespaceAnchor, error) {
	file, err := parent.Open(".")
	if err != nil {
		return nil, errors.New("cannot pin project lock parent")
	}
	defer file.Close()
	expected, err := file.Stat()
	if err != nil || expected == nil || !expected.IsDir() || isProjectLockRedirect(expected) {
		return nil, errors.New("cannot inspect project lock parent")
	}
	resolved, err := finalProjectPath(file)
	if err != nil {
		return nil, err
	}
	encoded, err := syscall.UTF16PtrFromString(resolved)
	if err != nil {
		return nil, errors.New("cannot encode project lock parent")
	}
	handle, err := syscall.CreateFile(
		encoded,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil,
		syscall.OPEN_EXISTING,
		projectFileFlagBackupSemantic,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("cannot stabilize project lock parent: %w", err)
	}
	stable := os.NewFile(uintptr(handle), resolved)
	if stable == nil {
		_ = syscall.CloseHandle(handle)
		return nil, errors.New("cannot adopt stable project lock parent")
	}
	opened, openErr := stable.Stat()
	after, afterErr := parent.Stat(".")
	if openErr != nil || afterErr != nil || !os.SameFile(expected, opened) || !os.SameFile(expected, after) {
		_ = stable.Close()
		return nil, errors.New("project lock parent changed while stabilizing")
	}
	return &projectNamespaceAnchor{parent: stable}, nil
}

func finalProjectPath(file *os.File) (string, error) {
	buffer := make([]uint16, 512)
	for {
		length, _, callErr := projectProcGetFinalPathNameByHandleW.Call(
			file.Fd(),
			uintptr(unsafe.Pointer(&buffer[0])),
			uintptr(len(buffer)),
			0,
		)
		runtime.KeepAlive(file)
		if length == 0 {
			return "", fmt.Errorf("cannot resolve project lock parent: %w", callErr)
		}
		if length < uintptr(len(buffer)) {
			return syscall.UTF16ToString(buffer[:length]), nil
		}
		buffer = make([]uint16, int(length)+1)
		if len(buffer) > 32768 {
			return "", errors.New("resolved project lock parent path is too long")
		}
	}
}

func tryProjectNamespaceAnchor(*projectNamespaceAnchor) (bool, error) {
	return true, nil
}

func releaseProjectNamespaceAnchor(anchor *projectNamespaceAnchor) error {
	if anchor == nil || anchor.parent == nil {
		return nil
	}
	file := anchor.parent
	anchor.parent = nil
	return file.Close()
}

func closeProjectNamespaceAnchor(anchor *projectNamespaceAnchor) error {
	return releaseProjectNamespaceAnchor(anchor)
}

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

func stabilizeProjectLockFile(file *os.File) (*os.File, error) {
	return reopenProjectHandleWithoutDelete(file, syscall.GENERIC_READ|syscall.GENERIC_WRITE, projectFileAttributeNormal)
}

func reopenProjectHandleWithoutDelete(file *os.File, access, flags uint32) (*os.File, error) {
	result, _, callErr := projectProcReOpenFile.Call(
		file.Fd(),
		uintptr(access),
		uintptr(syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE),
		uintptr(flags),
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
		return nil, errors.New("cannot adopt stable project lock handle")
	}
	if err := file.Close(); err != nil {
		_ = stable.Close()
		return nil, err
	}
	return stable, nil
}

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
