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

func TestWindowsReplaceRecoversStaleBackupBeforeFailedRetry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	backup := path + ".session-reviewer-backup"
	if err := os.WriteFile(backup, []byte("recoverable"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := replaceFile(filepath.Join(dir, "missing-temporary"), path)
	if err == nil {
		t.Fatal("expected replacement error")
	}
	b, readErr := os.ReadFile(path)
	if readErr != nil || string(b) != "recoverable" {
		t.Fatalf("content=%q err=%v", b, readErr)
	}
	if _, statErr := os.Stat(backup); !os.IsNotExist(statErr) {
		t.Fatalf("backup remains after successful recovery: %v", statErr)
	}
}

func TestWindowsReplaceReconcilesStaleBackupWhenDestinationExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	temporary := filepath.Join(dir, "state.tmp")
	backup := path + ".session-reviewer-backup"
	for name, content := range map[string]string{
		path:      "current",
		temporary: "new",
		backup:    "stale",
	} {
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := replaceFile(temporary, path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "new" {
		t.Fatalf("content=%q err=%v", b, err)
	}
	if _, statErr := os.Stat(backup); !os.IsNotExist(statErr) {
		t.Fatalf("backup remains after reconciliation: %v", statErr)
	}
}
