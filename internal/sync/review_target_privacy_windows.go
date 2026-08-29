//go:build windows

package sync

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/neomei/SessionReviewer/internal/winsecurity"
	"golang.org/x/sys/windows"
)

func openReviewTargetSecurityHandle(parent *os.Root, component string) (*os.File, error) {
	if parent == nil || component == "" || component == "." || component == ".." || strings.ContainsAny(component, `/\\`) {
		return nil, os.ErrInvalid
	}
	parentFile, err := parent.Open(".")
	if err != nil {
		return nil, err
	}
	defer parentFile.Close()
	objectName, err := windows.NewNTUnicodeString(component)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(parentFile.Fd()),
		ObjectName:    objectName,
	}
	var handle windows.Handle
	err = windows.NtCreateFile(
		&handle,
		windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		attributes,
		&windows.IO_STATUS_BLOCK{},
		nil,
		windows.FILE_ATTRIBUTE_DIRECTORY,
		windows.FILE_SHARE_DELETE|windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), filepath.Join(parentFile.Name(), component))
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, os.ErrInvalid
	}
	return file, nil
}

func reviewTargetPrivateSDDL() (string, *windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return "", nil, errors.New("current Windows user SID is unavailable")
	}
	sid := user.User.Sid
	sddl := "O:" + sid.String() + "D:P(A;OICI;FA;;;" + sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	return sddl, sid, nil
}

func protectReviewTargetDirectory(file *os.File) error {
	if file == nil {
		return os.ErrInvalid
	}
	before, err := reviewTargetSecurityHandleIdentity(file)
	if err != nil {
		return err
	}
	sddl, owner, err := reviewTargetPrivateSDDL()
	if err != nil {
		return err
	}
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return errors.New("protected review-target DACL is unavailable")
	}
	if err := windows.SetSecurityInfo(
		windows.Handle(file.Fd()), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil,
	); err != nil {
		return err
	}
	persistedDescriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || persistedDescriptor == nil || !winsecurity.ProtectedDACLMatches(persistedDescriptor, sddl) {
		return errors.New("protected review-target DACL did not persist")
	}
	gotOwner, _, err := persistedDescriptor.Owner()
	if err != nil || gotOwner == nil || !gotOwner.Equals(owner) {
		return errors.New("protected review-target owner changed")
	}
	after, err := reviewTargetSecurityHandleIdentity(file)
	if err != nil || !os.SameFile(before, after) {
		return errors.New("protected review-target handle identity changed")
	}
	return nil
}

func reviewTargetSecurityHandleIdentity(file *os.File) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil || info == nil || !info.IsDir() {
		return nil, errors.New("review-target security handle is not a directory")
	}
	var native windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &native); err != nil {
		return nil, err
	}
	if native.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, errors.New("review-target security handle is a reparse point")
	}
	return info, nil
}

func reviewTargetDirectoryProtected(path string, info os.FileInfo) bool {
	if info == nil || !info.IsDir() {
		return false
	}
	sddl, expectedOwner, err := reviewTargetPrivateSDDL()
	if err != nil {
		return false
	}
	descriptor, err := windows.GetNamedSecurityInfo(
		path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil || !winsecurity.ProtectedDACLMatches(descriptor, sddl) {
		return false
	}
	owner, _, err := descriptor.Owner()
	return err == nil && owner != nil && owner.Equals(expectedOwner)
}
