//go:build !windows

package reviewv2

import (
	"io/fs"
	"os"
)

func securePrivateMigrationPath(path string) error   { return os.Chmod(path, 0o600) }
func securePrivateMigrationDirectory(*os.File) error { return nil }
func securePrivateMigrationFile(file *os.File) error { return nil }
func secureArchiveSourceForPublication(string) error { return nil }
func secureArchiveInventoryDirectory(*os.File) error { return nil }
func migrationSourceModeOK(path string, want fs.FileMode) bool {
	return privateMigrationPath(path, want)
}
func privateMigrationPath(path string, want fs.FileMode) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().Perm() == want.Perm()
}
