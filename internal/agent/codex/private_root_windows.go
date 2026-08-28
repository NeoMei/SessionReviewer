//go:build windows

package codex

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func prepareOwnedPrivateDirectory(path string) error {
	handle, err := openDirectorySecurityHandle(path)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return applyPrivateDirectoryACL(handle)
}

func protectPrivateDirectory(path string, anchor *os.File) error {
	if anchor == nil {
		return errors.New("private directory anchor is closed")
	}
	handle, err := openDirectorySecurityHandle(path)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	var openedInfo, anchorInfo windows.ByHandleFileInformation
	openedErr := windows.GetFileInformationByHandle(handle, &openedInfo)
	anchorErr := windows.GetFileInformationByHandle(windows.Handle(anchor.Fd()), &anchorInfo)
	if openedErr != nil || anchorErr != nil ||
		openedInfo.VolumeSerialNumber != anchorInfo.VolumeSerialNumber ||
		openedInfo.FileIndexHigh != anchorInfo.FileIndexHigh ||
		openedInfo.FileIndexLow != anchorInfo.FileIndexLow {
		return errPrivateRootIdentityChanged
	}
	return applyPrivateDirectoryACL(handle)
}

func openDirectorySecurityHandle(path string) (windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(
		pointer,
		windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}

func applyPrivateDirectoryACL(handle windows.Handle) error {
	owner, dacl, err := privateDirectoryACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner,
		nil,
		dacl,
		nil,
	)
}

func privateDirectoryACL() (*windows.SID, *windows.ACL, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return nil, nil, errors.New("current Windows user SID is unavailable")
	}
	owner := user.User.Sid
	sddl := fmt.Sprintf("O:%sD:P(A;OICI;FA;;;%s)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", owner.String(), owner.String())
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, nil, err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return nil, nil, errors.New("could not build a protected private DACL")
	}
	return owner, dacl, nil
}

func validatePrivateDirectory(path string, anchor *os.File, info os.FileInfo) error {
	if anchor == nil || !info.IsDir() {
		return errors.New("private working root is not a directory")
	}
	if err := validateNoReparseComponents(path); err != nil {
		return err
	}
	handle := windows.Handle(anchor.Fd())
	var fileInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &fileInfo); err != nil {
		return err
	}
	if fileInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("private working root is a reparse point")
	}
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil {
		return errors.New("private working root DACL is unavailable")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("private working root DACL is not protected")
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return errors.New("private working root owner is unavailable")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !owner.Equals(user.User.Sid) {
		return errors.New("private working root has an unexpected owner")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return errors.New("private working root has a permissive DACL")
	}
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil || ace == nil {
			return errors.New("private working root DACL is malformed")
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if !sid.Equals(owner) && !sid.IsWellKnown(windows.WinLocalSystemSid) && !sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
				return errors.New("private working root grants access to an untrusted SID")
			}
		default:
			return errors.New("private working root has an unreviewed DACL entry")
		}
	}
	return nil
}

func configureCommandDirectory(command *exec.Cmd, anchor *os.File, physicalPath string) (string, error) {
	if anchor == nil {
		return "", errors.New("private directory anchor is closed")
	}
	command.Dir = physicalPath
	return physicalPath, nil
}

func recheckVisiblePrivateDirectory(path string, expected os.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, current) {
		return errPrivateRootIdentityChanged
	}
	return validateNoReparseComponents(path)
}

func validateNoReparseComponents(path string) error {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	if volume == "" {
		return errors.New("private Windows working root has no volume")
	}
	current := volume + string(os.PathSeparator)
	remainder := strings.TrimPrefix(clean[len(volume):], string(os.PathSeparator))
	for _, component := range strings.Split(remainder, string(os.PathSeparator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		pointer, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attributes, err := windows.GetFileAttributes(pointer)
		if err != nil {
			return err
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return errors.New("private Windows working root traverses a reparse point")
		}
	}
	return nil
}
