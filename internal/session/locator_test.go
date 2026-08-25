package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

func TestResolveExplicitIDIgnoresNegativeAmbiguityWindow(t *testing.T) {
	got, err := Resolve([]Candidate{{ID: "s1", Path: "one"}}, ResolveOptions{SessionID: "s1", AmbiguityWindow: -time.Second})
	if err != nil || got.ID != "s1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestResolveExplicitIDRejectsDuplicateCandidates(t *testing.T) {
	candidates := []Candidate{
		{ID: "s1", Path: "/sessions/one.jsonl"},
		{ID: "s1", Path: "/sessions/two.jsonl"},
	}
	_, err := Resolve(candidates, ResolveOptions{SessionID: "s1"})
	requireErrorContains(t, err, "duplicate", "/sessions/one.jsonl", "/sessions/two.jsonl")
}

func TestResolveExplicitIDChainsOrderedSegmentsForOneProject(t *testing.T) {
	firstStart := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	secondStart := firstStart.Add(2 * time.Hour)
	candidates := []Candidate{
		{ID: "s1", Path: "/sessions/later.jsonl", CWD: "/work/project", StartedAt: secondStart, ModTime: secondStart.Add(time.Minute)},
		{ID: "s1", Path: "/sessions/first.jsonl", CWD: "/work/project", StartedAt: firstStart, ModTime: firstStart.Add(time.Minute)},
	}

	got, err := Resolve(candidates, ResolveOptions{SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.segments) != 2 || got.segments[0].Path != "/sessions/first.jsonl" || got.segments[1].Path != "/sessions/later.jsonl" {
		t.Fatalf("segments=%+v", got.segments)
	}
	if !got.StartedAt.Equal(firstStart) || !got.ModTime.Equal(secondStart.Add(time.Minute)) {
		t.Fatalf("composite=%+v", got)
	}
}

func TestResolveExplicitIDRejectsSegmentedIdentityAcrossProjects(t *testing.T) {
	start := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	candidates := []Candidate{
		{ID: "s1", Path: "/sessions/one.jsonl", CWD: "/work/one", StartedAt: start},
		{ID: "s1", Path: "/sessions/two.jsonl", CWD: "/work/two", StartedAt: start.Add(time.Hour)},
	}

	_, err := Resolve(candidates, ResolveOptions{SessionID: "s1"})
	requireErrorContains(t, err, "duplicate session id", "different projects")
	if !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("error does not preserve session-conflict sentinel: %v", err)
	}
}

func TestResolveExplicitIDBypassesFreshness(t *testing.T) {
	candidate := Candidate{ID: "s1", Path: "one", ModTime: time.Time{}}
	got, err := Resolve([]Candidate{candidate}, ResolveOptions{SessionID: "s1"})
	if err != nil || got.Path != "one" {
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

func TestResolveCurrentAmbiguitySentinelSurvivesNestedWrapping(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	candidates := []Candidate{
		{ID: "source-canary-one", CWD: "/work/project", ModTime: now.Add(-time.Minute)},
		{ID: "source-canary-two", CWD: "/work/project", ModTime: now.Add(-2 * time.Minute)},
	}
	_, err := Resolve(candidates, ResolveOptions{CWD: "/work/project", Now: now, AmbiguityWindow: 5 * time.Minute})
	wrapped := fmt.Errorf("outer resolution context: %w", err)
	if !errors.Is(wrapped, ErrSessionAmbiguous) {
		t.Fatalf("nested error does not preserve ambiguity sentinel: %v", wrapped)
	}
	for _, detail := range []string{"ambiguous", "source-canary-one", "source-canary-two"} {
		if !strings.Contains(err.Error(), detail) {
			t.Fatalf("internal error %q missing %q", err, detail)
		}
	}
}

func TestResolveCurrentRejectsEqualModTimesAtZeroWindow(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	candidates := []Candidate{
		{ID: "s1", CWD: "/work/project", ModTime: now.Add(-time.Minute)},
		{ID: "s2", CWD: "/work/project", ModTime: now.Add(-time.Minute)},
	}
	_, err := Resolve(candidates, ResolveOptions{CWD: "/work/project", Now: now})
	requireErrorContains(t, err, "ambiguous", "s1", "s2")
}

func TestResolveCurrentRejectsNegativeAmbiguityWindow(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	_, err := Resolve(nil, ResolveOptions{CWD: "/work/project", Now: now, AmbiguityWindow: -time.Second})
	requireErrorContains(t, err, "ambiguity window", "negative")
}

func TestResolveCurrentAllowsExactPositiveAmbiguityBoundary(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	candidates := []Candidate{
		{ID: "newer", CWD: "/work/project", ModTime: now.Add(-time.Minute)},
		{ID: "older", CWD: "/work/project", ModTime: now.Add(-6 * time.Minute)},
	}
	got, err := Resolve(candidates, ResolveOptions{CWD: "/work/project", Now: now, AmbiguityWindow: 5 * time.Minute})
	if err != nil || got.ID != "newer" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestResolveCurrentRequiresNow(t *testing.T) {
	_, err := Resolve([]Candidate{{ID: "s1", CWD: "/work/project", ModTime: time.Now()}}, ResolveOptions{CWD: "/work/project"})
	requireErrorContains(t, err, "current time", "required")
}

func TestResolveCurrentAcceptsSoleMultiDayCandidate(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	candidate := Candidate{ID: "weekend", CWD: "/work/project", ModTime: now.Add(-72 * time.Hour)}
	got, err := Resolve([]Candidate{candidate}, ResolveOptions{CWD: "/work/project", Now: now})
	if err != nil || got.ID != "weekend" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestResolveCurrentRejectsFutureModTime(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	candidate := Candidate{ID: "future", CWD: "/work/project", ModTime: now.Add(5*time.Minute + time.Nanosecond)}
	_, err := Resolve([]Candidate{candidate}, ResolveOptions{CWD: "/work/project", Now: now})
	requireErrorContains(t, err, "future", "future", "modification time")
}

func TestResolveCurrentRejectsFutureStartedAt(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	candidate := Candidate{
		ID:        "future-start",
		CWD:       "/work/project",
		ModTime:   now,
		StartedAt: now.Add(5*time.Minute + time.Nanosecond),
	}
	_, err := Resolve([]Candidate{candidate}, ResolveOptions{CWD: "/work/project", Now: now})
	requireErrorContains(t, err, "future-start", "future", "start time")
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
	discovery, err := Discover(root, "")
	wantStartedAt := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	if err != nil || len(discovery.Candidates) != 1 || discovery.Candidates[0].ID != "s1" || !discovery.Candidates[0].StartedAt.Equal(wantStartedAt) {
		t.Fatalf("discovery=%+v err=%v", discovery, err)
	}
}

func TestDiscoverEnforcesTreeCandidateAndAggregateByteBudgets(t *testing.T) {
	root := t.TempDir()
	writeCandidate(t, root, "one.jsonl", "one", "/project")
	writeCandidate(t, root, "two.jsonl", "two", "/project")

	tests := []struct {
		name   string
		limits DiscoveryLimits
	}{
		{"entries", DiscoveryLimits{MaxEntries: 1, MaxCandidates: 10, MaxBytes: 1 << 20}},
		{"candidates", DiscoveryLimits{MaxEntries: 10, MaxCandidates: 1, MaxBytes: 1 << 20}},
		{"bytes", DiscoveryLimits{MaxEntries: 10, MaxCandidates: 10, MaxBytes: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DiscoverWithLimits(root, "", test.limits); !errors.Is(err, ErrDiscoveryBudget) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestResolveRejectsExcessiveSessionSegments(t *testing.T) {
	candidates := make([]Candidate, maxSessionSegments+1)
	start := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	for index := range candidates {
		candidates[index] = Candidate{ID: "segmented", Path: fmt.Sprintf("/%04d.jsonl", index), CWD: "/project", StartedAt: start.Add(time.Duration(index) * time.Second)}
	}
	if _, err := Resolve(candidates, ResolveOptions{SessionID: "segmented"}); !errors.Is(err, ErrDiscoveryBudget) {
		t.Fatalf("error=%v", err)
	}
}

func TestDiscoverExplicitIDStopsAfterUnrelatedSessionMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	content := `{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"/work/项目"}}` + "\n" +
		`{"timestamp":"2026-08-22T10:01:00Z","type":"session_meta","payload":"not-an-object"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	discovery, err := Discover(root, "wanted")
	if err != nil || len(discovery.Candidates) != 1 || discovery.Candidates[0].ID != "s1" || len(discovery.Issues) != 0 {
		t.Fatalf("discovery=%+v err=%v", discovery, err)
	}
}

func TestDiscoverRejectsMalformedJSONBeforeMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	content := "not-json\n" +
		`{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"/work/project"}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	discovery, err := Discover(root, "")
	if err != nil || len(discovery.Issues) != 1 {
		t.Fatalf("discovery=%+v err=%v", discovery, err)
	}
	requireErrorContains(t, discovery.Issues[0].Err, path, "malformed", "1")
	if discovery.Issues[0].SessionID != "s1" {
		t.Fatalf("issue=%+v", discovery.Issues[0])
	}
}

func TestDiscoverRejectsMissingSessionIDWithPathAndLine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	content := `{"timestamp":"2026-08-22T09:59:00Z","type":"event","payload":{}}` + "\n" +
		`{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"  ","cwd":"/work/project"}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	discovery, err := Discover(root, "")
	if err != nil || len(discovery.Issues) != 1 {
		t.Fatalf("discovery=%+v err=%v", discovery, err)
	}
	requireErrorContains(t, discovery.Issues[0].Err, path, "line 2", "session id")
}

func TestDiscoverPayloadDecodeErrorIncludesPathAndLine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	content := `{"timestamp":"2026-08-22T09:59:00Z","type":"event","payload":{}}` + "\n" +
		`{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":"invalid"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	discovery, err := Discover(root, "")
	if err != nil || len(discovery.Issues) != 1 {
		t.Fatalf("discovery=%+v err=%v", discovery, err)
	}
	requireErrorContains(t, discovery.Issues[0].Err, path, "line 2", "metadata")
}

func TestDiscoverRejectsSymlinkRoot(t *testing.T) {
	target := t.TempDir()
	root := filepath.Join(t.TempDir(), "sessions-link")
	if err := os.Symlink(target, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := Discover(root, "")
	requireErrorContains(t, err, root, "symlink or reparse point")
}

func TestDiscoverRejectsSymlinkFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "target.jsonl")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "rollout.jsonl")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := Discover(root, "")
	requireErrorContains(t, err, path, "symlink or reparse point")
}

func TestDiscoverExplicitIDIgnoresUnrelatedCorruptFile(t *testing.T) {
	root := t.TempDir()
	writeCandidate(t, root, "selected.jsonl", "wanted", "/project")
	if err := os.WriteFile(filepath.Join(root, "broken.jsonl"), []byte("{not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	discovery, err := Discover(root, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResolveDiscovery(discovery, ResolveOptions{SessionID: "wanted"})
	if err != nil || got.ID != "wanted" {
		t.Fatalf("got=%+v err=%v issues=%+v", got, err, discovery.Issues)
	}
}

func TestDiscoverExplicitIDRejectsSelectedCorruptCandidate(t *testing.T) {
	root := t.TempDir()
	body := `{"timestamp":"2026-08-22T00:00:00Z","type":"session_meta","payload":{"id":"wanted","cwd":"/project"}}` + "\n{broken\n"
	if err := os.WriteFile(filepath.Join(root, "selected.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	discovery, err := Discover(root, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveDiscovery(discovery, ResolveOptions{SessionID: "wanted"})
	if err == nil || !strings.Contains(err.Error(), "selected session candidate is corrupt") {
		t.Fatalf("err=%v", err)
	}
}

func TestDiscoverExplicitIDScansSelectedCandidatePastConflictingMetadata(t *testing.T) {
	root := t.TempDir()
	body := `{"timestamp":"2026-08-22T00:00:00Z","type":"session_meta","payload":{"id":"wanted","cwd":"/project"}}` + "\n" +
		`{"timestamp":"2026-08-22T00:01:00Z","type":"session_meta","payload":{"id":"other","cwd":"/project"}}` + "\n" +
		"{malformed-after-conflicting-metadata\n"
	path := filepath.Join(root, "selected.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	discovery, err := Discover(root, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Issues) != 1 || discovery.Issues[0].SessionID != "wanted" {
		t.Fatalf("discovery=%+v", discovery)
	}
	requireErrorContains(t, discovery.Issues[0].Err, path, "conflicting session id", "malformed")
	_, err = ResolveDiscovery(discovery, ResolveOptions{SessionID: "wanted"})
	requireErrorContains(t, err, "selected session candidate is corrupt")
}

func TestDiscoverExplicitIDRejectsConflictingRepeatedMetadata(t *testing.T) {
	root := t.TempDir()
	body := `{"timestamp":"2026-08-22T00:00:00Z","type":"session_meta","payload":{"id":"wanted","cwd":"/project"}}` + "\n" +
		`{"timestamp":"2026-08-22T00:01:00Z","type":"session_meta","payload":{"id":"other","cwd":"/project"}}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "selected.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	discovery, err := Discover(root, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Issues) != 1 || discovery.Issues[0].SessionID != "wanted" {
		t.Fatalf("discovery=%+v", discovery)
	}
	requireErrorContains(t, discovery.Issues[0].Err, "conflicting session id")
}

func TestDiscoverExplicitIDRejectsCorruptDuplicateCandidate(t *testing.T) {
	root := t.TempDir()
	writeCandidate(t, root, "one.jsonl", "wanted", "/project")
	body := `{"timestamp":"2026-08-22T00:00:00Z","type":"session_meta","payload":{"id":"wanted","cwd":"/project"}}` + "\n{broken\n"
	if err := os.WriteFile(filepath.Join(root, "two.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	discovery, err := Discover(root, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveDiscovery(discovery, ResolveOptions{SessionID: "wanted"})
	if err == nil || !strings.Contains(err.Error(), "duplicate session id") {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveDiscoveryCurrentRejectsAnyIssue(t *testing.T) {
	discovery := Discovery{
		Candidates: []Candidate{{ID: "healthy", CWD: "/project"}},
		Issues:     []DiscoveryIssue{{Path: "/sessions/broken.jsonl", Err: errors.New("malformed JSONL record")}},
	}
	_, err := ResolveDiscovery(discovery, ResolveOptions{CWD: "/project", Now: time.Now()})
	requireErrorContains(t, err, "current-session discovery", "corrupt candidates", "select a session explicitly")
}

func writeCandidate(t *testing.T, root, name, id, cwd string) {
	t.Helper()
	body := `{"timestamp":"2026-08-22T00:00:00Z","type":"session_meta","payload":{"id":"` + id + `","cwd":"` + cwd + `"}}` + "\n"
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveNormalizesWindowsPaths(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	candidates := []Candidate{{ID: "s1", CWD: `C:\项目\Repo`, ModTime: now}}
	got, err := Resolve(candidates, ResolveOptions{CWD: `c:/项目/repo`, GOOS: "windows", Now: now, AmbiguityWindow: time.Minute})
	if err != nil || got.ID != "s1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func requireErrorContains(t *testing.T, err error, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q", parts)
	}
	for _, part := range parts {
		if !strings.Contains(err.Error(), part) && !strings.Contains(err.Error(), strconv.Quote(part)) {
			t.Fatalf("error %q does not contain %q", err, part)
		}
	}
}
