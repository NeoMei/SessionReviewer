//go:build !windows

package reviewv2

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/neomei/SessionReviewer/internal/pathguard"
)

func TestArchiveInventoryDirectoryRestoresExactLegacyModeAfterUmask(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, "archive"), 0o700); err != nil {
		t.Fatal(err)
	}
	project, err := pathguard.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer project.Close()

	tests := []struct {
		name  string
		umask int
		mode  os.FileMode
	}{
		{name: "mode-0751-umask-0027", umask: 0o027, mode: 0o751},
		{name: "mode-0755-umask-0077", umask: 0o077, mode: 0o755},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			withMigrationUmask(test.umask, func() {
				if err := ensureArchiveInventoryDirectory(project, "archive/"+test.name, test.mode); err != nil {
					t.Fatal(err)
				}
			})
			info, err := os.Stat(filepath.Join(rootPath, "archive", test.name))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != test.mode {
				t.Fatalf("mode=%v want=%v", info.Mode().Perm(), test.mode)
			}
		})
	}
}

func withMigrationUmask(mask int, run func()) {
	old := syscall.Umask(mask)
	defer syscall.Umask(old)
	run()
}
