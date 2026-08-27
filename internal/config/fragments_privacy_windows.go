//go:build windows

package config

import (
	"io/fs"
	"os"

	"github.com/neomei/SessionReviewer/internal/winsecurity"
	"golang.org/x/sys/windows"
)

func secureProjectFragmentsDirectory(file *os.File) error { return setPrivateConfigDACLHandle(file) }
func secureProjectFragmentFile(file *os.File) error       { return setPrivateConfigDACLHandle(file) }

func privateConfigSDDL() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return "D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)", nil
}

func setPrivateConfigDACLHandle(file *os.File) error {
	sddl, err := privateConfigSDDL()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func privateProjectFragmentsPath(path string, info fs.FileInfo) bool {
	return info != nil && info.IsDir() && privateConfigPath(path)
}

func privateProjectFragmentPath(path string, info fs.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && privateConfigPath(path)
}

func privateConfigPath(path string) bool {
	got, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	sddl, err := privateConfigSDDL()
	if err != nil {
		return false
	}
	return winsecurity.ProtectedDACLMatches(got, sddl)
}
