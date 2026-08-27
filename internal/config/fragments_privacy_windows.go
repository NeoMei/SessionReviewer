//go:build windows

package config

import (
	"io/fs"
	"os"

	"github.com/neomei/SessionReviewer/internal/winsecurity"
	"golang.org/x/sys/windows"
)

func secureProjectFragmentsDirectory(file *os.File) error {
	return setPrivateConfigDACLHandle(file, true)
}
func secureProjectFragmentFile(file *os.File) error { return setPrivateConfigDACLHandle(file, false) }

func privateConfigSDDL(inherit bool) (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	flags := ""
	if inherit {
		flags = "OICI"
	}
	return "D:P(A;" + flags + ";FA;;;" + user.User.Sid.String() + ")(A;" + flags + ";FA;;;SY)", nil
}

func setPrivateConfigDACLHandle(file *os.File, inherit bool) error {
	sddl, err := privateConfigSDDL(inherit)
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
	return info != nil && info.IsDir() && privateConfigPath(path, true)
}

func privateProjectFragmentPath(path string, info fs.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && privateConfigPath(path, false)
}

func privateConfigPath(path string, inherit bool) bool {
	got, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	sddl, err := privateConfigSDDL(inherit)
	if err != nil {
		return false
	}
	return winsecurity.ProtectedDACLMatches(got, sddl)
}
