package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveByExplicitID(t *testing.T) {
	candidates := []Candidate{{ID: "s1", Path: "one"}, {ID: "s2", Path: "two"}}
	got, err := Resolve(candidates, ResolveOptions{SessionID: "s2"})
	if err != nil || got.Path != "two" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestResolveCurrentRejectsAmbiguousSameProjectSessions(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	candidates := []Candidate{
		{ID: "s1", CWD: `/work/项目`, ModTime: now.Add(-time.Minute)},
		{ID: "s2", CWD: `/work/项目`, ModTime: now.Add(-2 * time.Minute)},
	}
	_, err := Resolve(candidates, ResolveOptions{CWD: `/work/项目`, Now: now, AmbiguityWindow: 5 * time.Minute})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err=%v", err)
	}
}

func TestDiscoverReadsOnlyJSONLSessionMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "2026", "08", "22", "rollout.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"/work/项目","source":"vscode"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(root)
	wantStartedAt := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	if err != nil || len(got) != 1 || got[0].ID != "s1" || !got[0].StartedAt.Equal(wantStartedAt) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestDiscoverStopsAfterSessionMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	content := `{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"/work/项目"}}` + "\n" +
		`{"timestamp":"2026-08-22T10:01:00Z","type":"session_meta","payload":"not-an-object"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(root)
	if err != nil || len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestResolveNormalizesWindowsPaths(t *testing.T) {
	candidates := []Candidate{{ID: "s1", CWD: `C:\项目\Repo`, ModTime: time.Now()}}
	got, err := Resolve(candidates, ResolveOptions{CWD: `c:/项目/repo`, GOOS: "windows", AmbiguityWindow: time.Minute})
	if err != nil || got.ID != "s1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
