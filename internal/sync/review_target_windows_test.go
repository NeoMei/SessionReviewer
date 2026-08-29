//go:build windows

package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
)

// This is a Task 13 native gate. Cross-compilation in Task 9 proves the test
// and implementation build, while a Windows runner proves the protected DACL
// and directory file identity for both one- and multi-component creation.
func TestWindowsReviewTargetMissingCreationUsesProtectedDACLAndStableIdentity(t *testing.T) {
	for _, reviewPath := range []string{"Review", "Projects/Review"} {
		t.Run(strings.ReplaceAll(reviewPath, "/", "_"), func(t *testing.T) {
			root := t.TempDir()
			projectPath := filepath.Join(root, "project")
			vaultPath := filepath.Join(root, "vault")
			dataPath := filepath.Join(root, "data")
			for _, path := range []string{projectPath, vaultPath, dataPath} {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			project, err := pathguard.Open(projectPath)
			if err != nil {
				t.Fatal(err)
			}
			defer project.Close()
			vault, err := pathguard.Open(vaultPath)
			if err != nil {
				t.Fatal(err)
			}
			defer vault.Close()
			data, err := pathguard.Open(dataPath)
			if err != nil {
				t.Fatal(err)
			}
			defer data.Close()
			pin, err := PinReviewTarget(reviewPath, platform.CaseInsensitive, project, vault, data)
			if err != nil {
				t.Fatal(err)
			}
			defer pin.Close()
			target, ready, err := pin.directory(true)
			if err != nil || !ready {
				t.Fatalf("create target: ready=%v err=%v", ready, err)
			}

			current := vaultPath
			for _, component := range strings.Split(reviewPath, "/") {
				current = filepath.Join(current, component)
				info, err := os.Lstat(current)
				if err != nil || !info.IsDir() || !reviewTargetDirectoryProtected(current, info) {
					t.Fatalf("created component lacks protected private DACL: path=%s info=%v err=%v", current, info, err)
				}
				opened, err := pathguard.Open(current)
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(info, opened.Info()) {
					_ = opened.Close()
					t.Fatal("created component path and handle identities differ")
				}
				_ = opened.Close()
			}
			if info, err := os.Stat(current); err != nil || !os.SameFile(info, target.Info()) {
				t.Fatalf("returned target identity differs from created leaf: info=%v err=%v", info, err)
			}
		})
	}
}
