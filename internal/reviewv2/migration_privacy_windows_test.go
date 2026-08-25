//go:build windows

package reviewv2

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func setPermissiveMigrationDACL(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString("D:(A;;FA;;;WD)")
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

func TestWindowsMigrationPrivateACLIsProtectedAndRejectsBroadening(t *testing.T) {
	fixture := newLegacyMigrationFixture(t)
	setPermissiveMigrationDACL(t, fixture.project)
	setPermissiveMigrationDACL(t, fixture.data)
	plan, err := PlanMigration(fixture.project, fixture.projectInfo, fixture.data, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop")
	err = applyMigrationWithHook(plan, func(stage Stage) error {
		if stage == StageBackupComplete {
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
		filepath.Join(fixture.project, filepath.FromSlash(migrationReviewRoot+"/.session-reviewer")),
		filepath.Join(fixture.project, filepath.FromSlash(plan.BackupRoot)),
	}
	for _, path := range privatePaths {
		if !privateMigrationPath(path, 0) {
			t.Fatalf("private ACL missing: %s", path)
		}
	}
	setPermissiveMigrationDACL(t, privatePaths[0])
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
