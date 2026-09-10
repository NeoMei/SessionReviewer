//go:build !windows

package sessionlaunch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyRunsOnlyPinnedVersionRouteAndPersistsResult(t *testing.T) {
	dataRoot, executable := t.TempDir(), filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = --version ] || exit 19\nprintf 'claude fixture 1.2.3\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := Verify(context.Background(), dataRoot, "claude", executable)
	if err != nil || got.Provider != "claude" || got.Version != "claude fixture 1.2.3" || got.Executable == "" {
		t.Fatalf("configuration=%+v err=%v", got, err)
	}
	loaded, err := LoadConfiguration(dataRoot, "claude")
	if err != nil || loaded != got {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}
