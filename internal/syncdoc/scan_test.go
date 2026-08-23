package syncdoc

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
)

func TestScanFindsStableIDsAndIsolatesNormalizedCollisions(t *testing.T) {
	sources := []SourceDocument{
		{RelativePath: "decisions/Café.md", Content: entity("decision-1", "project-1", "one")},
		{RelativePath: "decisions/Cafe\u0301.md", Content: entity("decision-2", "project-1", "two")},
	}
	got := BuildInventory(sources, "darwin", platform.CaseInsensitive)
	if len(got.ByID) != 0 || len(got.Issues) != 2 {
		t.Fatalf("inventory=%+v", got)
	}
	for _, issue := range got.Issues {
		if issue.Kind != IssuePathCollision {
			t.Fatalf("issue=%+v", issue)
		}
	}
}

func TestScanIsolatesEveryDuplicateIdentityParticipant(t *testing.T) {
	sources := []SourceDocument{
		{RelativePath: "decisions/a.md", Content: entity("decision-1", "project-1", "one")},
		{RelativePath: "decisions/b.md", Content: entity("decision-1", "project-1", "two")},
		{RelativePath: "decisions/c.md", Content: entity("decision-1", "project-1", "three")},
		{RelativePath: "decisions/d.md", Content: entity("decision-2", "project-1", "four")},
	}
	got := BuildInventory(sources, "darwin", platform.CaseSensitive)
	if len(got.ByID) != 1 || got.ByID["decision-2"].RelativePath != "decisions/d.md" {
		t.Fatalf("ByID=%+v", got.ByID)
	}
	if len(got.Issues) != 3 {
		t.Fatalf("Issues=%+v", got.Issues)
	}
	for _, issue := range got.Issues {
		if issue.Kind != IssueDuplicateID || issue.EntityID != "decision-1" {
			t.Fatalf("issue=%+v", issue)
		}
	}
}

func TestScanMalformedIssueDoesNotLeakCandidateContent(t *testing.T) {
	canary := "CANARY-SHOULD-NOT-LEAK"
	got := BuildInventory([]SourceDocument{{RelativePath: "decisions/bad.md", Content: []byte(canary)}}, "darwin", platform.CaseSensitive)
	if len(got.ByID) != 0 || len(got.Issues) != 1 || got.Issues[0].Kind != IssueMalformed {
		t.Fatalf("inventory=%+v", got)
	}
	if got.Issues[0].Err == nil || strings.Contains(got.Issues[0].Err.Error(), canary) {
		t.Fatalf("unsafe issue=%+v", got.Issues[0])
	}
}

func TestScanRejectsUnstableEntityIdentity(t *testing.T) {
	got := BuildInventory([]SourceDocument{{
		RelativePath: "decisions/upper.md",
		Content:      entity("Decision-UPPER", "project-1", "upper"),
	}}, "darwin", platform.CaseSensitive)
	if len(got.ByID) != 0 || len(got.Issues) != 1 || got.Issues[0].Kind != IssueMalformed || got.Issues[0].EntityID != "" {
		t.Fatalf("inventory=%+v", got)
	}
}

func TestScanBuildInventoryIsDeterministicAndHashesExactContent(t *testing.T) {
	sources := []SourceDocument{
		{RelativePath: "z/z.md", Content: entity("decision-z", "project-1", "z")},
		{RelativePath: "a/a.md", Content: entity("decision-a", "project-1", "a")},
	}
	first := BuildInventory(sources, "darwin", platform.CaseSensitive)
	second := BuildInventory([]SourceDocument{sources[1], sources[0]}, "darwin", platform.CaseSensitive)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	for id, entry := range first.ByID {
		if entry.Identity.ID != id || entry.ContentHash != ContentHash(entry.Content) {
			t.Fatalf("entry=%+v", entry)
		}
		rendered, err := entry.Document.Render()
		if err != nil || !reflect.DeepEqual(rendered, entry.Content) {
			t.Fatalf("rendered mismatch for %s: err=%v", id, err)
		}
	}
}

func TestScanWalkSkipsExactExcludedEntries(t *testing.T) {
	rootPath := t.TempDir()
	files := map[string][]byte{
		"decisions/keep.md":                                            entity("decision-keep", "project-1", "keep"),
		"decisions/non-md.txt":                                         entity("decision-text", "project-1", "text"),
		"decisions/.hidden.md":                                         entity("decision-hidden", "project-1", "hidden"),
		"decisions/.hidden/inside.md":                                  entity("decision-hidden-dir", "project-1", "hidden dir"),
		".obsidian/plugin.md":                                          entity("decision-obsidian", "project-1", "obsidian"),
		"sync-conflicts/conflict.md":                                   entity("decision-conflict", "project-1", "conflict"),
		"decisions/save.md.session-reviewer-backup":                    entity("decision-backup", "project-1", "backup"),
		"decisions/.session-reviewer-0123456789abcdef0123456789abcdef": entity("decision-temp", "project-1", "temp"),
	}
	for relative, content := range files {
		full := filepath.Join(rootPath, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	directory, err := pathguard.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	got := Scan(directory, "decisions", "darwin", platform.CaseSensitive)
	if len(got.Issues) != 0 || len(got.ByID) != 1 || got.ByID["decision-keep"].RelativePath != "decisions/keep.md" {
		t.Fatalf("inventory=%+v", got)
	}

	all := Scan(directory, ".", "darwin", platform.CaseSensitive)
	if len(all.Issues) != 0 || len(all.ByID) != 1 || all.ByID["decision-keep"].RelativePath != "decisions/keep.md" {
		t.Fatalf("root inventory=%+v", all)
	}
}

func TestScanPrunesExcludedSubtreesBeforeUnsafeEntries(t *testing.T) {
	rootPath := t.TempDir()
	keepPath := filepath.Join(rootPath, "decisions", "keep.md")
	if err := os.MkdirAll(filepath.Dir(keepPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keepPath, entity("decision-keep", "project-1", "keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, excluded := range []string{".obsidian", "sync-conflicts", ".hidden", ".session-reviewer-deadbeef", "old.session-reviewer-backup"} {
		directory := filepath.Join(rootPath, excluded)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		oversize, err := os.OpenFile(filepath.Join(directory, "oversize.md"), os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := oversize.Truncate(MaxDocumentBytes + 1); err != nil {
			t.Fatal(err)
		}
		oversize.Close()
		if err := os.WriteFile(filepath.Join(directory, "malformed.md"), []byte("not frontmatter"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(t.TempDir(), "outside.md"), filepath.Join(directory, "redirect.md")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	}
	directory, err := pathguard.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	got := Scan(directory, ".", "darwin", platform.CaseSensitive)
	if len(got.Issues) != 0 || len(got.ByID) != 1 || got.ByID["decision-keep"].RelativePath != "decisions/keep.md" {
		t.Fatalf("inventory=%+v", got)
	}
}

func TestScanIssueOrderUsesSlashPaths(t *testing.T) {
	sources := []SourceDocument{
		{RelativePath: "z/bad.md", Content: []byte("bad-z")},
		{RelativePath: "a/bad.md", Content: []byte("bad-a")},
	}
	got := BuildInventory(sources, "darwin", platform.CaseSensitive)
	paths := make([]string, 0, len(got.Issues))
	for _, issue := range got.Issues {
		paths = append(paths, issue.RelativePath)
	}
	if !sort.StringsAreSorted(paths) || !reflect.DeepEqual(paths, []string{"a/bad.md", "z/bad.md"}) {
		t.Fatalf("paths=%v", paths)
	}
}

func TestScanReturnsSanitizedWalkFailure(t *testing.T) {
	directory, err := pathguard.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory.Close()
	got := Scan(directory, "docs", "darwin", platform.CaseSensitive)
	if len(got.ByID) != 0 || len(got.Issues) != 1 || got.Issues[0].Kind != IssueMalformed || got.Issues[0].Err == nil {
		t.Fatalf("inventory=%+v", got)
	}
	if errors.Is(got.Issues[0].Err, os.ErrNotExist) {
		t.Fatal("scanner should return a sanitized classification, not a raw rooted error")
	}
}
