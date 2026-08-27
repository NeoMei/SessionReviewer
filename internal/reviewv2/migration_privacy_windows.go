//go:build windows

package reviewv2

import (
	"io/fs"
	"os"

	"github.com/neomei/SessionReviewer/internal/winsecurity"
	"golang.org/x/sys/windows"
)

func privateMigrationSDDL(inherit bool) (string, error) {
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

func securePrivateMigrationPath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return setPrivateMigrationDACL(path, info.IsDir())
}
func securePrivateMigrationDirectory(file *os.File) error {
	return setPrivateMigrationDACLHandle(file, true)
}
func securePrivateMigrationFile(file *os.File) error {
	return setPrivateMigrationDACL(file.Name(), false)
}
func secureArchiveSourceForPublication(path string) error {
	return setPrivateMigrationDACL(path, false)
}
func secureArchiveInventoryDirectory(file *os.File) error {
	return setPrivateMigrationDACLHandle(file, true)
}
func migrationSourceModeOK(string, fs.FileMode) bool { return true }

func setPrivateMigrationDACL(path string, inherit bool) error {
	sddl, err := privateMigrationSDDL(inherit)
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
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func setPrivateMigrationDACLHandle(file *os.File, inherit bool) error {
	sddl, err := privateMigrationSDDL(inherit)
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

func privateMigrationPath(path string, _ fs.FileMode) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	want, err := privateMigrationSDDL(info.IsDir())
	if err != nil {
		return false
	}
	got, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	return winsecurity.ProtectedDACLMatches(got, want)
}
