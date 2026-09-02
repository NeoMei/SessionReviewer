package buildinfo

import (
	"strings"
	"testing"
)

func TestValidateReleaseRequiresExactMetadata(t *testing.T) {
	good := Info{Version: "0.1.0", Commit: strings.Repeat("a", 40), BuiltAt: "2026-08-25T00:00:00Z", GoVersion: "go1.26", ReviewSchemaVersion: 3}
	if err := ValidateRelease(good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Info{
		{Version: "dev", Commit: good.Commit, BuiltAt: good.BuiltAt, GoVersion: good.GoVersion, ReviewSchemaVersion: 3},
		{Version: good.Version, Commit: "short", BuiltAt: good.BuiltAt, GoVersion: good.GoVersion, ReviewSchemaVersion: 3},
		{Version: good.Version, Commit: good.Commit, BuiltAt: "today", GoVersion: good.GoVersion, ReviewSchemaVersion: 3},
		{Version: good.Version, Commit: good.Commit, BuiltAt: good.BuiltAt, GoVersion: good.GoVersion, ReviewSchemaVersion: 2},
	} {
		if ValidateRelease(bad) == nil {
			t.Fatalf("accepted %#v", bad)
		}
	}
}
