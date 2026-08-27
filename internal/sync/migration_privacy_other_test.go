//go:build !windows

package sync

import (
	"os"
	"testing"
)

func hardenMigrationTestPath(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o600)
	if info.IsDir() {
		mode = 0o700
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
