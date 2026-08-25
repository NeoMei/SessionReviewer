//go:build windows

package atomicfile

import (
	"io/fs"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func createRootDirectoryFile(parent *os.Root, name string, _ fs.FileMode, _ rootDirectoryCreationHooks) (*rootDirectoryCreation, error) {
	parentFile, err := parent.Open(".")
	if err != nil {
		return nil, err
	}
	defer parentFile.Close()
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attrs := &windows.OBJECT_ATTRIBUTES{Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: windows.Handle(parentFile.Fd()), ObjectName: objectName}
	var handle windows.Handle
	err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ|windows.WRITE_DAC|windows.SYNCHRONIZE, attrs, &windows.IO_STATUS_BLOCK{}, nil, windows.FILE_ATTRIBUTE_DIRECTORY, windows.FILE_SHARE_DELETE|windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.FILE_CREATE, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err == windows.STATUS_OBJECT_NAME_COLLISION {
		return nil, fs.ErrExist
	}
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), filepath.Join(parent.Name(), name))
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, os.ErrInvalid
	}
	return &rootDirectoryCreation{file: file, published: true}, nil
}
