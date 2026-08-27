//go:build !windows

package config

import (
	"io/fs"
	"os"
)

func secureProjectFragmentsDirectory(*os.File) error { return nil }
func secureProjectFragmentFile(*os.File) error       { return nil }

func privateProjectFragmentsPath(_ string, info fs.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode().Perm() == 0o700
}

func privateProjectFragmentPath(_ string, info fs.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm() == 0o600
}
