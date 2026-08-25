package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommonEntriesRequireLicenseAndNotice(t *testing.T) {
	source := t.TempDir()
	mustWriteReleaseFile(t, source, "README.md", "readme")
	mustWriteReleaseFile(t, source, "LICENSE", "license")
	mustWriteReleaseFile(t, source, "NOTICE", "notice")
	mustWriteReleaseFile(t, source, "skill/session-reviewer/SKILL.md", "skill")

	entries, err := commonEntries(source)
	if err != nil {
		t.Fatalf("commonEntries() error = %v", err)
	}
	names := make(map[string]bool, len(entries))
	for _, entry := range entries {
		names[entry.Name] = true
	}
	for _, name := range []string{"session-reviewer/README.md", "session-reviewer/LICENSE", "session-reviewer/NOTICE", "session-reviewer/skill/session-reviewer/SKILL.md"} {
		if !names[name] {
			t.Fatalf("commonEntries() missing %q: %#v", name, names)
		}
	}
}

func TestCommonEntriesRejectMissingNotice(t *testing.T) {
	source := t.TempDir()
	mustWriteReleaseFile(t, source, "README.md", "readme")
	mustWriteReleaseFile(t, source, "LICENSE", "license")
	mustWriteReleaseFile(t, source, "skill/session-reviewer/SKILL.md", "skill")

	if _, err := commonEntries(source); err == nil || err.Error() != "LICENSE and NOTICE are required" {
		t.Fatalf("commonEntries() error = %v, want LICENSE and NOTICE are required", err)
	}
}

func TestPrepareOutputDirectoryRejectsStaleArtifacts(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "session-reviewer_0.0.9_windows_amd64.zip"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareOutputDirectory(dist); err == nil {
		t.Fatal("accepted a non-empty release output directory")
	}
	if _, err := os.Stat(filepath.Join(dist, "session-reviewer_0.0.9_windows_amd64.zip")); err != nil {
		t.Fatalf("stale artifact was mutated: %v", err)
	}
}

func mustWriteReleaseFile(t *testing.T, root, name, body string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
