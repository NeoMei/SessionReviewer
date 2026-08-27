//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileRenameInfoEx struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [windows.MAX_LONG_PATH]uint16
}

func renameRootNoReplace(oldParent *os.Root, oldName string, newParent *os.Root, newName string) error {
	oldDir, err := oldParent.Open(".")
	if err != nil {
		return err
	}
	defer oldDir.Close()
	newDir, err := newParent.Open(".")
	if err != nil {
		return err
	}
	defer newDir.Close()
	objectName, err := windows.NewNTUnicodeString(oldName)
	if err != nil {
		return err
	}
	attrs := &windows.OBJECT_ATTRIBUTES{Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: windows.Handle(oldDir.Fd()), ObjectName: objectName}
	var source windows.Handle
	err = windows.NtCreateFile(&source, windows.SYNCHRONIZE|windows.DELETE, attrs, &windows.IO_STATUS_BLOCK{}, nil, 0, windows.FILE_SHARE_DELETE|windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.FILE_OPEN, windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(source)
	name, err := windows.UTF16FromString(newName)
	if err != nil {
		return err
	}
	info := fileRenameInfoEx{RootDirectory: windows.Handle(newDir.Fd())}
	if len(name) > len(info.FileName) {
		return windows.ERROR_FILENAME_EXCED_RANGE
	}
	copy(info.FileName[:], name)
	info.FileNameLength = uint32((len(name) - 1) * 2)
	infoSize := uint32(unsafe.Offsetof(info.FileName) + uintptr(info.FileNameLength))
	err = windows.SetFileInformationByHandle(source, windows.FileRenameInfoEx, (*byte)(unsafe.Pointer(&info)), infoSize)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		// FileRenameInfoEx is unavailable on some supported Windows hosts and
		// filesystems. With zero flags this buffer is layout-compatible with
		// FILE_RENAME_INFO (ReplaceIfExists=false), preserving no-replace
		// behavior on the older information class.
		return windows.SetFileInformationByHandle(source, windows.FileRenameInfo, (*byte)(unsafe.Pointer(&info)), infoSize)
	}
	return err
}
