package syncproject

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/pathguard"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

func TestMappingPinExposesOnlyAuthenticatedProjectLaunchIdentity(t *testing.T) {
	fixture := newMigrationServiceFixture(t)
	pin, err := PinMapping(Options{ProjectID: fixture.projectID, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	root, identity, err := pin.AuthenticatedProjectRoot()
	if err != nil || !filepath.IsAbs(root) || filepath.Clean(root) != root || !identity.Valid() {
		t.Fatalf("root=%q identity=%+v err=%v", root, identity, err)
	}
	// Windows may preserve an 8.3 spelling while EvalSymlinks expands it.
	// Authenticate the physical directory rather than requiring one spelling.
	project, err := os.Open(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	defer project.Close()
	wantInfo, err := project.Stat()
	if err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil || !os.SameFile(rootInfo, wantInfo) {
		t.Fatalf("root=%q does not identify project=%q: %v", root, fixture.project, err)
	}
	wantIdentity, err := pathguard.PhysicalFileIdentity(project)
	if err != nil || identity != wantIdentity {
		t.Fatalf("identity=%+v want=%+v err=%v", identity, wantIdentity, err)
	}
}
