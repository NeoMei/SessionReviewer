//go:build windows

package reviewv2

import (
	"io/fs"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

func privateMigrationSDDL() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return "D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)", nil
}

func securePrivateMigrationPath(path string) error        { return setPrivateMigrationDACL(path) }
func securePrivateMigrationDirectory(path string) error   { return setPrivateMigrationDACL(path) }
func securePrivateMigrationFile(file *os.File) error      { return setPrivateMigrationDACL(file.Name()) }
func secureArchiveSourceForPublication(path string) error { return setPrivateMigrationDACL(path) }
func migrationSourceModeOK(string, fs.FileMode) bool      { return true }

func setPrivateMigrationDACL(path string) error {
	sddl, err := privateMigrationSDDL()
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

func privateMigrationPath(path string, _ fs.FileMode) bool {
	want, err := privateMigrationSDDL()
	if err != nil {
		return false
	}
	wantSD, err := windows.SecurityDescriptorFromString(want)
	if err != nil {
		return false
	}
	got, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	control, _, err := got.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return false
	}
	return canonicalMigrationDACL(got.String()) == canonicalMigrationDACL(wantSD.String())
}

func canonicalMigrationDACL(s string) string {
	if index := strings.Index(s, "D:"); index >= 0 {
		return s[index:]
	}
	return s
}
