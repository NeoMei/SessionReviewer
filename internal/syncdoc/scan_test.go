package syncdoc

import (
	"bytes"
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

func TestScanPathCollisionIncludesMalformedParticipantsBeforeParsing(t *testing.T) {
	canary := "MALFORMED-COLLISION-CANARY"
	malformedPath := "decisions/Café.md"
	validPath := "decisions/Cafe\u0301.md"
	got := BuildInventory([]SourceDocument{
		{RelativePath: malformedPath, Content: []byte(canary)},
		{RelativePath: validPath, Content: entity("decision-2", "project-1", "valid")},
	}, "darwin", platform.CaseInsensitive)
	if len(got.ByID) != 0 || len(got.Issues) != 3 {
		t.Fatalf("ByID=%+v issues=%+v", got.ByID, got.Issues)
	}
	want := map[string]map[IssueKind]int{
		malformedPath: {IssueMalformed: 1, IssuePathCollision: 1},
		validPath:     {IssuePathCollision: 1},
	}
	for _, issue := range got.Issues {
		if issue.Err == nil || strings.Contains(issue.Err.Error(), canary) {
			t.Fatalf("unsafe issue=%+v", issue)
		}
		want[issue.RelativePath][issue.Kind]--
	}
	for relative, kinds := range want {
		for kind, remaining := range kinds {
			if remaining != 0 {
				t.Fatalf("relative=%q kind=%q remaining=%d issues=%+v", relative, kind, remaining, got.Issues)
			}
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
		"diagrams/project-evolution.md":                                []byte("derived diagram without entity frontmatter"),
		"decisions/00-目录说明.md":                                         []byte("generated index without entity frontmatter"),
		"open-loops/00-目录说明.md":                                        []byte("generated index without entity frontmatter"),
		"sessions/00-目录说明.md":                                          []byte("generated index without entity frontmatter"),
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
	notesPath := filepath.Join(rootPath, "Sync-Conflicts-Notes", "keep.md")
	if err := os.MkdirAll(filepath.Dir(notesPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notesPath, entity("decision-notes", "project-1", "notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := pathguard.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	got := Scan(directory, ".", "darwin", platform.CaseSensitive)
	if len(got.Issues) != 0 || len(got.ByID) != 2 || got.ByID["decision-keep"].RelativePath != "decisions/keep.md" || got.ByID["decision-notes"].RelativePath != "Sync-Conflicts-Notes/keep.md" {
		t.Fatalf("inventory=%+v", got)
	}
}

func TestScanPrunesCaseFoldedConflictDirectoryWithoutOvermatching(t *testing.T) {
	for _, excluded := range []string{"SYNC-CONFLICTS", "Sync-Conflicts"} {
		t.Run(excluded, func(t *testing.T) {
			rootPath := t.TempDir()
			keep := filepath.Join(rootPath, "Sync-Conflicts-Notes", "keep.md")
			if err := os.MkdirAll(filepath.Dir(keep), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(keep, entity("decision-notes", "project-1", "notes"), 0o600); err != nil {
				t.Fatal(err)
			}
			excludedRoot := filepath.Join(rootPath, excluded)
			if err := os.Mkdir(excludedRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			oversize, err := os.OpenFile(filepath.Join(excludedRoot, "oversize.md"), os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := oversize.Truncate(MaxDocumentBytes + 1); err != nil {
				t.Fatal(err)
			}
			oversize.Close()
			if err := os.WriteFile(filepath.Join(excludedRoot, "malformed.md"), []byte("malformed"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(t.TempDir(), "outside.md"), filepath.Join(excludedRoot, "redirect.md")); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			directory, err := pathguard.Open(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			got := Scan(directory, ".", "windows", platform.CaseInsensitive)
			if len(got.Issues) != 0 || len(got.ByID) != 1 || got.ByID["decision-notes"].RelativePath != "Sync-Conflicts-Notes/keep.md" {
				t.Fatalf("inventory=%+v", got)
			}
		})
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

func TestScanRejectsAggregateFileAndByteBudgetWithoutPartialInventory(t *testing.T) {
	rootPath := t.TempDir()
	docs := filepath.Join(rootPath, "decisions")
	if err := os.MkdirAll(docs, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"a.md": entity("decision-a", "project-1", "a"),
		"b.md": entity("decision-b", "project-1", "b"),
	} {
		if err := os.WriteFile(filepath.Join(docs, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	directory, err := pathguard.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	for _, limits := range []struct {
		files int
		bytes int
	}{{files: 1, bytes: 1 << 20}, {files: 10, bytes: 1}} {
		got := scanWithLimits(directory, "decisions", "darwin", platform.CaseSensitive, limits.files, limits.bytes)
		if len(got.ByID) != 0 || len(got.Issues) != 1 || got.Issues[0].RelativePath != "decisions" || got.Issues[0].Kind != IssueMalformed {
			t.Fatalf("limits=%+v inventory=%+v", limits, got)
		}
	}
}

func TestScanIsolatesUnreadableMarkdownAndKeepsUnrelatedEntities(t *testing.T) {
	rootPath := t.TempDir()
	docs := filepath.Join(rootPath, "decisions")
	if err := os.MkdirAll(docs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, "good.md"), entity("decision-good", "project-1", "good"), 0o600); err != nil {
		t.Fatal(err)
	}
	oversized, err := os.OpenFile(filepath.Join(docs, "oversized.md"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := oversized.Truncate(MaxDocumentBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := oversized.Close(); err != nil {
		t.Fatal(err)
	}
	directory, err := pathguard.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	got := Scan(directory, "decisions", "darwin", platform.CaseSensitive)
	if len(got.ByID) != 1 || got.ByID["decision-good"].RelativePath != "decisions/good.md" {
		t.Fatalf("unrelated entity lost: %+v", got)
	}
	if len(got.Issues) != 1 || got.Issues[0].Kind != IssueMalformed || got.Issues[0].RelativePath != "decisions/oversized.md" {
		t.Fatalf("isolated issue=%+v", got.Issues)
	}
}

func TestV2ScanIncludesOnlyStableDocumentsAndRejectsMixedInventory(t *testing.T) {
	review := v2FixtureBytes(t, "项目回顾.valid.md")
	history := v2FixtureBytes(t, "项目历史.valid.md")
	legacy := entity("decision-legacy", "project-0123456789abcdef", "legacy")

	valid := BuildInventory([]SourceDocument{
		{RelativePath: "项目历史.md", Content: history},
		{RelativePath: "项目回顾.md", Content: review},
	}, "darwin", platform.CaseSensitive)
	if len(valid.Issues) != 0 || len(valid.ByID) != 2 ||
		valid.ByID["project-overview"].RelativePath != "项目回顾.md" ||
		valid.ByID["project-history"].RelativePath != "项目历史.md" {
		t.Fatalf("v2 inventory=%+v", valid)
	}

	for name, sources := range map[string][]SourceDocument{
		"mixed-legacy": {
			{RelativePath: "项目回顾.md", Content: review},
			{RelativePath: "项目历史.md", Content: history},
			{RelativePath: "decisions/legacy.md", Content: legacy},
		},
		"mixed-malformed-v2": {
			{RelativePath: "项目回顾.md", Content: bytes.Replace(review, []byte("<!-- /session-reviewer:risk -->"), []byte("<!-- broken-risk-close -->"), 1)},
			{RelativePath: "decisions/legacy.md", Content: legacy},
		},
		"wrong-review-filename": {
			{RelativePath: "回顾-改名.md", Content: review},
			{RelativePath: "项目历史.md", Content: history},
		},
		"missing-history": {
			{RelativePath: "项目回顾.md", Content: review},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := BuildInventory(sources, "darwin", platform.CaseSensitive)
			if len(got.ByID) != 0 || len(got.Issues) == 0 {
				t.Fatalf("inventory=%+v", got)
			}
			for _, issue := range got.Issues {
				if issue.Kind != IssueMalformed {
					t.Fatalf("issue=%+v", issue)
				}
			}
		})
	}
}

func TestV2ScanPrunesEntireMachineSubtreeBeforeUnsafeEntries(t *testing.T) {
	rootPath := t.TempDir()
	for relative, content := range map[string][]byte{
		"项目回顾.md": v2FixtureBytes(t, "项目回顾.valid.md"),
		"项目历史.md": v2FixtureBytes(t, "项目历史.valid.md"),
	} {
		if err := os.WriteFile(filepath.Join(rootPath, relative), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	machine := filepath.Join(rootPath, ".session-reviewer")
	if err := os.Mkdir(machine, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine, "malformed.md"), []byte("not frontmatter"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside.md"), filepath.Join(machine, "redirect.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	directory, err := pathguard.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	got := Scan(directory, ".", "darwin", platform.CaseSensitive)
	if len(got.Issues) != 0 || len(got.ByID) != 2 {
		t.Fatalf("inventory=%+v", got)
	}
}
