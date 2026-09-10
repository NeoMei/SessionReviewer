//go:build windows

package sessionlaunch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyReportsWindowsUnsupportedWithoutPersistingConfiguration(t *testing.T) {
	dataRoot := t.TempDir()
	executable := filepath.Join(t.TempDir(), "claude.exe")
	if err := os.WriteFile(executable, []byte("fixture must not execute"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Verify(context.Background(), dataRoot, "claude", executable)
	if err == nil || !strings.Contains(err.Error(), "native Session launch is unsupported on Windows") || got != (Configuration{}) {
		t.Fatalf("configuration=%+v err=%v", got, err)
	}
	entries, err := os.ReadDir(dataRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unsupported verification wrote state: entries=%v err=%v", entries, err)
	}
}
