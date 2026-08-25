//go:build !windows

package reviewv2

import (
	"os"
	"path/filepath"
	"strings"
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

func TestMigrationInventoryRejectsDirectoryRecoveryResidue(t *testing.T) {
	for _, prefix := range []string{".session-reviewer-directory-", ".session-reviewer-directory-quarantine-"} {
		t.Run(prefix, func(t *testing.T) {
			rootPath := t.TempDir()
			reviewRoot := filepath.Join(rootPath, filepath.FromSlash(migrationReviewRoot))
			if err := os.MkdirAll(reviewRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			residue := prefix + strings.Repeat("a", 32)
			if err := os.Mkdir(filepath.Join(reviewRoot, residue), 0o700); err != nil {
				t.Fatal(err)
			}
			project, err := pathguard.Open(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer project.Close()

			entries, err := scanMigrationInventory(project, migrationReviewRoot, migrationReviewRoot, false)
			if err == nil || !strings.Contains(err.Error(), "incomplete machine directory recovery") || len(entries) != 0 {
				t.Fatalf("entries=%v err=%v", entries, err)
			}
			if _, statErr := os.Stat(filepath.Join(reviewRoot, residue)); statErr != nil {
				t.Fatal(statErr)
			}
		})
	}
}

func TestMigrationInventoryValidatesPersistentDirectoryLock(t *testing.T) {
	tests := []struct {
		name     string
		lockName string
		body     []byte
		mode     os.FileMode
		wantErr  string
	}{
		{name: "empty-private", lockName: ".session-reviewer-directory.lock", mode: 0o600},
		{name: "uppercase-canonical-alias", lockName: ".SESSION-REVIEWER-DIRECTORY.LOCK", mode: 0o600, wantErr: "extra"},
		{name: "nonempty-private", lockName: ".session-reviewer-directory.lock", body: []byte("user-owned"), mode: 0o600, wantErr: "unsafe"},
		{name: "empty-public", lockName: ".session-reviewer-directory.lock", mode: 0o644, wantErr: "unsafe"},
		{name: "extra-alias", lockName: ".session-reviewer-directory.lock.extra", mode: 0o600, wantErr: "extra"},
		{name: "uppercase-extra-alias", lockName: ".SESSION-REVIEWER-DIRECTORY.LOCK.EXTRA", mode: 0o600, wantErr: "extra"},
		{name: "nfc-case-extra-alias", lockName: ".SESSION-REVIEWER-DIRECTORY.LOCK.e\u0301xtra", mode: 0o600, wantErr: "extra"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rootPath := t.TempDir()
			reviewRoot := filepath.Join(rootPath, filepath.FromSlash(migrationReviewRoot))
			if err := os.MkdirAll(reviewRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(reviewRoot, test.lockName)
			if err := os.WriteFile(lockPath, test.body, test.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(lockPath, test.mode); err != nil {
				t.Fatal(err)
			}
			project, err := pathguard.Open(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer project.Close()

			entries, err := scanMigrationInventory(project, migrationReviewRoot, migrationReviewRoot, false)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) || len(entries) != 0 {
					t.Fatalf("entries=%v err=%v", entries, err)
				}
			} else if err != nil || len(entries) != 0 {
				t.Fatalf("entries=%v err=%v", entries, err)
			}
			body, readErr := os.ReadFile(lockPath)
			if readErr != nil || string(body) != string(test.body) {
				t.Fatalf("lock body=%q err=%v", body, readErr)
			}
			info, statErr := os.Stat(lockPath)
			if statErr != nil || info.Mode().Perm() != test.mode {
				t.Fatalf("lock mode=%v err=%v", info.Mode().Perm(), statErr)
			}
		})
	}
}

func TestMigrationInventoryRejectsDuplicatePortableDirectoryLocks(t *testing.T) {
	rootPath := t.TempDir()
	reviewRoot := filepath.Join(rootPath, filepath.FromSlash(migrationReviewRoot))
	if err := os.MkdirAll(reviewRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".session-reviewer-directory.lock", ".SESSION-REVIEWER-DIRECTORY.LOCK"} {
		lockPath := filepath.Join(reviewRoot, name)
		if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(lockPath, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	physical, err := os.ReadDir(reviewRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(physical) != 2 {
		t.Skip("filesystem collapses portable-equivalent lock names")
	}
	project, err := pathguard.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer project.Close()

	entries, err := scanMigrationInventory(project, migrationReviewRoot, migrationReviewRoot, false)
	if err == nil || !strings.Contains(err.Error(), "extra directory lock") || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

func withMigrationUmask(mask int, run func()) {
	old := syscall.Umask(mask)
	defer syscall.Umask(old)
	run()
}
