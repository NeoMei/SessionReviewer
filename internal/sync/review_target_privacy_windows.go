//go:build windows

package sync

import (
	"errors"
	"os"

	"github.com/neomei/SessionReviewer/internal/winsecurity"
	"golang.org/x/sys/windows"
)

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
	return windows.SetSecurityInfo(
		windows.Handle(file.Fd()), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil,
	)
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
