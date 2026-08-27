//go:build windows

package reviewv2

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"golang.org/x/sys/windows"
)

func setPermissiveMigrationDACL(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString("D:(A;OICI;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsCreatedDirectoryHandleDoesNotHardenReplacement(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	replacement := filepath.Join(rootPath, "private")
	away := filepath.Join(rootPath, "created-away")
	created, err := atomicfile.EnsureRootDirPrepared(root, "private", 0o700, func() error {
		if err := os.Rename(replacement, away); err != nil {
			return err
		}
		if err := os.Mkdir(replacement, 0o755); err != nil {
			return err
		}
		setPermissiveMigrationDACL(t, replacement)
		return os.WriteFile(filepath.Join(replacement, "user.txt"), []byte("replacement"), 0o644)
	}, securePrivateMigrationDirectory)
	if !created || !errors.Is(err, atomicfile.ErrRootDirectoryIdentityChanged) {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if _, statErr := os.Lstat(replacement); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("replacement remained at public leaf: %v", statErr)
	}
	preserved := findWindowsDirectoryQuarantine(t, rootPath)
	if privateMigrationPath(preserved, 0) {
		t.Fatal("replacement was silently hardened")
	}
	body, readErr := os.ReadFile(filepath.Join(preserved, "user.txt"))
	if readErr != nil || string(body) != "replacement" {
		t.Fatalf("replacement bytes changed: body=%q err=%v", body, readErr)
	}
	if !privateMigrationPath(away, 0) {
		got, want, control := migrationACLDiagnostic(t, away)
		t.Fatalf("identity-bound created directory was not hardened: got=%q want=%q control=%#x", got, want, control)
	}
}

func findWindowsDirectoryQuarantine(t *testing.T, rootPath string) string {
	t.Helper()
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if atomicfile.IsRootDirectoryQuarantineName(entry.Name()) {
			return filepath.Join(rootPath, entry.Name())
		}
	}
	t.Fatal("directory replacement was not preserved in quarantine")
	return ""
}

func TestWindowsMigrationPrivateACLIsProtectedAndRejectsBroadening(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	nested := filepath.Join(fixture.project, "docs", "session-review", "nested", "level-two")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	setPermissiveMigrationDACL(t, fixture.project)
	setPermissiveMigrationDACL(t, fixture.data)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop")
	err = applyMigrationWithHook(plan, func(stage Stage) error {
		if stage == StageLegacyMoved {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("interruption=%v", err)
	}
	privatePaths := []string{
		filepath.Join(fixture.data, migrationJournalDir),
		filepath.Join(fixture.data, filepath.FromSlash(migrationJournalRelative(plan.projectKey))),
		filepath.Join(fixture.project, filepath.FromSlash(ReviewRelativePath)),
		filepath.Join(fixture.project, filepath.FromSlash(HistoryRelativePath)),
		filepath.Join(fixture.project, filepath.FromSlash(MachineLedgerRelativePath)),
		filepath.Join(fixture.project, filepath.FromSlash(migrationReviewRoot+"/.session-reviewer")),
		filepath.Join(fixture.project, filepath.FromSlash(plan.BackupRoot)),
		filepath.Join(fixture.project, filepath.FromSlash(plan.BackupRoot+"/archive")),
		filepath.Join(fixture.project, filepath.FromSlash(plan.BackupRoot+"/archive/nested")),
		filepath.Join(fixture.project, filepath.FromSlash(plan.BackupRoot+"/archive/nested/level-two")),
		filepath.Join(fixture.project, filepath.FromSlash(plan.BackupRoot+"/quarantine")),
	}
	for _, path := range privatePaths {
		if !privateMigrationPath(path, 0) {
			got, want, control := migrationACLDiagnostic(t, path)
			t.Fatalf("private ACL missing: %s got=%q want=%q control=%#x", path, got, want, control)
		}
	}
	setPermissiveMigrationDACL(t, privatePaths[len(privatePaths)-2])
	before, err := os.ReadFile(privatePaths[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := RecoverMigration(fixture.project, fixture.projectInfo, fixture.data); err == nil {
		t.Fatal("broadened DACL was accepted")
	}
	after, err := os.ReadFile(privatePaths[1])
	if err != nil || string(after) != string(before) {
		t.Fatalf("recovery wrote after ACL broadening: %v", err)
	}
}

func migrationACLDiagnostic(t *testing.T, path string) (got, want string, control windows.SECURITY_DESCRIPTOR_CONTROL) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "get-error:" + err.Error(), "", 0
	}
	control, _, err = descriptor.Control()
	if err != nil {
		return descriptor.String(), "control-error:" + err.Error(), 0
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		return descriptor.String(), "stat-error:" + statErr.Error(), control
	}
	want, err = privateMigrationSDDL(info.IsDir())
	if err != nil {
		want = "want-error:" + err.Error()
	}
	return descriptor.String(), want, control
}
