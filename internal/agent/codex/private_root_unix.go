//go:build !windows

package codex

import (
	"errors"
	"os"
	"syscall"
)

func prepareOwnedPrivateDirectory(path string) error {
	return os.Chmod(path, 0o700)
}

func protectPrivateDirectory(_ string, _ *os.File) error { return nil }

func validatePrivateDirectory(_ string, _ *os.File, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("private working directory is not owner-only")
	}
	return nil
}
