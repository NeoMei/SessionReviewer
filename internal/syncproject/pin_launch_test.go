package syncproject

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"

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
	wantRoot, resolveErr := filepath.EvalSymlinks(fixture.project)
	if err != nil || resolveErr != nil || root != wantRoot || !identity.Valid() {
		t.Fatalf("root=%q identity=%+v err=%v", root, identity, err)
	}
}
