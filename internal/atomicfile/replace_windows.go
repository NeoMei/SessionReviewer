//go:build windows

package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const moveFileWriteThrough = 0x00000008

var (
	kernel32                      = syscall.NewLazyDLL("kernel32.dll")
	replaceFileWProc              = kernel32.NewProc("ReplaceFileW")
	moveFileExWProc               = kernel32.NewProc("MoveFileExW")
	getFinalPathNameByHandleWProc = kernel32.NewProc("GetFinalPathNameByHandleW")
)

func nativeReplace(destination, temporary string) error {
	destination, err := windowsAPIPath(destination)
	if err != nil {
		return err
	}
	temporary, err = windowsAPIPath(temporary)
	if err != nil {
		return err
	}
	destinationPtr, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	temporaryPtr, err := syscall.UTF16PtrFromString(temporary)
	if err != nil {
		return err
	}
	ok, _, callErr := replaceFileWProc.Call(
		uintptr(unsafe.Pointer(destinationPtr)),
		uintptr(unsafe.Pointer(temporaryPtr)),
		0,
		0,
		0,
		0,
	)
	if ok == 0 {
		return failedWindowsCall(callErr)
	}
	return nil
}

func nativeMoveNew(temporary, destination string) error {
	temporary, err := windowsAPIPath(temporary)
	if err != nil {
		return err
	}
	destination, err = windowsAPIPath(destination)
	if err != nil {
		return err
	}
	temporaryPtr, err := syscall.UTF16PtrFromString(temporary)
	if err != nil {
		return err
	}
	destinationPtr, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	ok, _, callErr := moveFileExWProc.Call(
		uintptr(unsafe.Pointer(temporaryPtr)),
		uintptr(unsafe.Pointer(destinationPtr)),
		moveFileWriteThrough,
	)
	if ok == 0 {
		return failedWindowsCall(callErr)
	}
	return nil
}

func replaceFile(temporary, destination string) error {
	return replaceWindowsFile(temporary, destination, windowsFileOps{
		stat:            os.Stat,
		replaceExisting: nativeReplace,
		moveNew:         nativeMoveNew,
	})
}

func replaceRootFile(root *os.Root, temporary, destination string) error {
	return replaceWindowsFile(temporary, destination, windowsFileOps{
		stat: root.Stat,
		replaceExisting: func(destination, temporary string) error {
			temporaryPath, destinationPath, err := rootReplacementPaths(root, temporary, destination)
			if err != nil {
				return err
			}
			return nativeReplace(destinationPath, temporaryPath)
		},
		moveNew: func(temporary, destination string) error {
			temporaryPath, destinationPath, err := rootReplacementPaths(root, temporary, destination)
			if err != nil {
				return err
			}
			return nativeMoveNew(temporaryPath, destinationPath)
		},
	})
}

func rootReplacementPaths(root *os.Root, temporary, destination string) (string, string, error) {
	if filepath.Clean(filepath.Dir(temporary)) != filepath.Clean(filepath.Dir(destination)) {
		return "", "", fmt.Errorf("temporary and destination must share a directory")
	}
	temporaryPath, err := rootFinalPath(root, temporary)
	if err != nil {
		return "", "", fmt.Errorf("resolve temporary in root: %w", err)
	}
	destinationPath := filepath.Join(filepath.Dir(temporaryPath), filepath.Base(destination))
	return temporaryPath, destinationPath, nil
}

func rootFinalPath(root *os.Root, name string) (string, error) {
	file, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()

	connection, err := file.SyscallConn()
	if err != nil {
		return "", err
	}
	var path string
	var pathErr error
	if err := connection.Control(func(handle uintptr) {
		path, pathErr = finalPathFromHandle(syscall.Handle(handle))
	}); err != nil {
		return "", err
	}
	if pathErr != nil {
		return "", pathErr
	}
	return path, nil
}

func finalPathFromHandle(handle syscall.Handle) (string, error) {
	buffer := make([]uint16, 512)
	for {
		length, _, callErr := getFinalPathNameByHandleWProc.Call(
			uintptr(handle),
			uintptr(unsafe.Pointer(&buffer[0])),
			uintptr(len(buffer)),
			0,
		)
		if length == 0 {
			return "", failedWindowsCall(callErr)
		}
		if length < uintptr(len(buffer)) {
			return syscall.UTF16ToString(buffer[:length]), nil
		}
		buffer = make([]uint16, length)
	}
}

func windowsAPIPath(path string) (string, error) {
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\??\`) || strings.HasPrefix(path, `\\.\`) {
		return path, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(absolute, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(absolute, `\\`), nil
	}
	return `\\?\` + absolute, nil
}

func failedWindowsCall(callErr error) error {
	if errno, ok := callErr.(syscall.Errno); ok && errno != 0 {
		return errno
	}
	return syscall.EINVAL
}
