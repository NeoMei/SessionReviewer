package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommonEntriesRequireLicenseAndNotice(t *testing.T) {
	source := t.TempDir()
	mustWriteReleaseFile(t, source, "README.md", "readme")
	mustWriteReleaseFile(t, source, "LICENSE", "license")
	mustWriteReleaseFile(t, source, "NOTICE", "notice")
	mustWriteReleaseFile(t, source, "schemas/review-ledger-v2.schema.json", "{\"schema_version\":2}\n")
	mustWriteReleaseFile(t, source, "skill/session-reviewer/SKILL.md", "skill")

	entries, err := commonEntries(source)
	if err != nil {
		t.Fatalf("commonEntries() error = %v", err)
	}
	names := make(map[string]bool, len(entries))
	for _, entry := range entries {
		names[entry.Name] = true
	}
	for _, name := range []string{"session-reviewer/README.md", "session-reviewer/LICENSE", "session-reviewer/NOTICE", "session-reviewer/schemas/review-ledger-v2.schema.json", "session-reviewer/skill/session-reviewer/SKILL.md"} {
		if !names[name] {
			t.Fatalf("commonEntries() missing %q: %#v", name, names)
		}
	}
}

func TestCommonEntriesPackagesAuthoritativeReviewV2SchemaBytes(t *testing.T) {
	source := t.TempDir()
	for _, name := range []string{"README.md", "LICENSE", "NOTICE"} {
		mustWriteReleaseFile(t, source, name, name)
	}
	mustWriteReleaseFile(t, source, "skill/session-reviewer/SKILL.md", "skill")
	want := []byte("{\n  \"title\": \"Review Ledger v2\"\n}\n")
	mustWriteReleaseFile(t, source, "schemas/review-ledger-v2.schema.json", string(want))
	entries, err := commonEntries(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name == "session-reviewer/schemas/review-ledger-v2.schema.json" {
			if !bytes.Equal(entry.Body, want) || entry.Mode != 0o644 {
				t.Fatalf("schema entry mode=%o body=%q", entry.Mode, entry.Body)
			}
			return
		}
	}
	t.Fatal("release archive omitted review-ledger-v2.schema.json")
}

func TestReleaseWrappersDefaultToV023AndPassExactSourceAndDist(t *testing.T) {
	checks := map[string][]string{
		"../../scripts/build-release.sh":  {"version=${1:-0.2.3}", "--source .", "--dist \"$dist\""},
		"../../scripts/build-release.ps1": {"[string]$Version = \"0.2.3\"", "--source .", "--dist $Dist"},
	}
	for name, required := range checks {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range required {
			if !strings.Contains(string(body), value) {
				t.Errorf("%s missing %q", name, value)
			}
		}
	}
}

func TestCIExecutesBothReleaseWrappersTwiceAndVerifiesArtifacts(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Count(text, "./scripts/build-release.sh 0.2.3") != 2 {
		t.Fatalf("CI must execute the shell wrapper twice:\n%s", text)
	}
	if strings.Count(text, `.\scripts\build-release.ps1 -Version 0.2.3`) != 2 {
		t.Fatalf("CI must execute the PowerShell wrapper twice:\n%s", text)
	}
	for _, required := range []string{
		"session-reviewer_0.2.3_darwin_amd64.tar.gz",
		"session-reviewer_0.2.3_darwin_arm64.tar.gz",
		"session-reviewer_0.2.3_windows_amd64.zip",
		"shasum -a 256 -c SHA256SUMS", "Get-FileHash -Algorithm SHA256",
		"session-reviewer/schemas/review-ledger-v2.schema.json",
		`session-reviewer\schemas\review-ledger-v2.schema.json`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("CI packaging gate missing %q", required)
		}
	}
}

func TestReadmeDocumentsPublicReviewV2Workflow(t *testing.T) {
	body, err := os.ReadFile("../../README.zh-CN.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, required := range []string{
		"docs/session-review/项目回顾.md", "docs/session-review/项目历史.md", "docs/session-review/.session-reviewer/ledger.json",
		"sync repair-machine-ledger", "--project-id", "migration backup", "PowerShell", "v0.2.1",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("README.md missing %q", required)
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
