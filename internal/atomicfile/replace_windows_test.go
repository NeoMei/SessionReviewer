//go:build windows

package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsReplaceRemovesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".session-reviewer-backup"); !os.IsNotExist(err) {
		t.Fatalf("backup remains: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("entries=%v", entries)
	}
}

func TestWindowsReplaceRestoresDestinationWhenReplacementFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := replaceFile(filepath.Join(dir, "missing-temporary"), path)
	if err == nil {
		t.Fatal("expected replacement error")
	}
	b, readErr := os.ReadFile(path)
	if readErr != nil || string(b) != "old" {
		t.Fatalf("content=%q err=%v", b, readErr)
	}
	if _, statErr := os.Stat(path + ".session-reviewer-backup"); !os.IsNotExist(statErr) {
		t.Fatalf("backup remains after rollback: %v", statErr)
	}
}
