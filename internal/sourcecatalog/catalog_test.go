package sourcecatalog

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
)

func TestCatalogStoresSharedSessionUsageOnce(t *testing.T) {
	dataRoot := t.TempDir()
	catalog := openCatalog(t, dataRoot)
	first := sourceRecord("s1", []string{"project-b"}, 573135757)
	if _, err := catalog.UpsertSource(first); err != nil {
		t.Fatal(err)
	}
	second := sourceRecord("s1", []string{"project-a", "project-b", "project-a"}, 573135757)
	digest, err := catalog.UpsertSource(second)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := catalog.GetSource("codex", "s1")
	if err != nil || !found || !reflect.DeepEqual(got.ProjectIDs, []string{"project-a", "project-b"}) {
		t.Fatalf("record=%+v found=%v err=%v", got, found, err)
	}
	if count := catalogJSONCount(t, dataRoot); count != 1 {
		t.Fatalf("rows=%d", count)
	}
	for _, projectID := range []string{"project-a", "project-b"} {
		usage, err := catalog.AssociatedUsage(projectID)
		if err != nil || len(usage) != 1 {
			t.Fatalf("project=%s usage=%+v err=%v", projectID, usage, err)
		}
		want := memory.AssociatedUsage{Provider: "codex", SessionID: "s1", UsageRecordDigest: digest, Shared: true}
		if usage[0] != want {
			t.Fatalf("project=%s usage=%+v want=%+v", projectID, usage[0], want)
		}
	}
}

func TestCatalogIsContentFreePrivateRootedAndSorted(t *testing.T) {
	dataRoot := t.TempDir()
	catalog := openCatalog(t, dataRoot)
	for _, record := range []memory.SourceRecord{
		sourceRecord("s2", []string{"project-b", "project-a"}, 20),
		sourceRecord("s1", []string{"project-a"}, 10),
	} {
		if _, err := catalog.UpsertSource(record); err != nil {
			t.Fatal(err)
		}
	}
	records, err := catalog.ListCandidates()
	if err != nil || len(records) != 2 || records[0].SessionID != "s1" || records[1].SessionID != "s2" {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	filtered, err := catalog.ListCandidates("project-b")
	if err != nil || len(filtered) != 1 || filtered[0].SessionID != "s2" {
		t.Fatalf("filtered=%+v err=%v", filtered, err)
	}
	entries, err := os.ReadDir(filepath.Join(dataRoot, "source-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		path := filepath.Join(dataRoot, "source-catalog", entry.Name())
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" {
			wantMode := os.FileMode(0o600)
			if entry.IsDir() {
				wantMode = 0o700
			}
			if info.Mode().Perm() != wantMode {
				t.Fatalf("%s mode=%#o want=%#o", path, info.Mode().Perm(), wantMode)
			}
		}
		if strings.HasSuffix(entry.Name(), ".json") {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"message", "tool_output", "raw_tool_output", "transcript"} {
				if bytes.Contains(bytes.ToLower(body), []byte(forbidden)) {
					t.Fatalf("catalog JSON contains %q: %s", forbidden, body)
				}
			}
		}
	}
}

func TestCatalogConflictingSourceIdentityDoesNotMutate(t *testing.T) {
	dataRoot := t.TempDir()
	catalog := openCatalog(t, dataRoot)
	record := sourceRecord("s1", []string{"project-a"}, 10)
	if _, err := catalog.UpsertSource(record); err != nil {
		t.Fatal(err)
	}
	before := catalogJSONSnapshot(t, dataRoot)
	conflict := sourceRecord("s1", []string{"project-b"}, 10)
	conflict.SourceIdentity = "different-source"
	if _, err := catalog.UpsertSource(conflict); !errors.Is(err, projectidentity.ErrAssociationRequired) {
		t.Fatalf("error=%v, want association required", err)
	}
	after := catalogJSONSnapshot(t, dataRoot)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("catalog mutated on conflict: before=%q after=%q", before, after)
	}
}

func TestCatalogReplaceSourceCASAllowsAuthenticatedTruncationAndInteriorMutation(t *testing.T) {
	dataRoot := t.TempDir()
	catalog := openCatalog(t, dataRoot)
	original := sourceRecord("s1", []string{"project-a"}, 10)
	originalDigest, err := catalog.UpsertSource(original)
	if err != nil {
		t.Fatal(err)
	}

	truncated := sourceRecord("s1", []string{"project-a"}, 5)
	truncated.FrozenBoundary.Location.JSONL = &memory.JSONLSourceLocation{Line: 5, ByteOffset: 64}
	truncated.FrozenBoundary.SourceHash = strings.Repeat("b", 64)
	truncated.EndedAt = "2026-08-31T10:00:00.5Z"
	truncated.Usage.EndedAt = truncated.EndedAt
	truncated.Usage.DurationMS = 500
	replacementDigest, err := catalog.ReplaceSource(originalDigest, truncated)
	if err != nil || replacementDigest == originalDigest {
		t.Fatalf("replace truncation digest=%q err=%v", replacementDigest, err)
	}
	got, found, err := catalog.GetSource("codex", "s1")
	if err != nil || !found || !reflect.DeepEqual(got, truncated) {
		t.Fatalf("truncation replacement=%+v found=%v err=%v", got, found, err)
	}
	appended := truncated
	appended.FrozenBoundary.Location.JSONL = &memory.JSONLSourceLocation{Line: 6, ByteOffset: 96}
	appended.FrozenBoundary.SourceHash = strings.Repeat("d", 64)
	if _, err := catalog.ReplaceSource(replacementDigest, appended); !errors.Is(err, projectidentity.ErrAssociationRequired) {
		t.Fatalf("replacement API accepted monotonic append: %v", err)
	}

	interior := truncated
	interior.FrozenBoundary.SourceHash = strings.Repeat("c", 64)
	if _, err := catalog.ReplaceSource(originalDigest, interior); !errors.Is(err, ErrCASConflict) {
		t.Fatalf("stale replacement error=%v want ErrCASConflict", err)
	}
	still, _, err := catalog.GetSource("codex", "s1")
	if err != nil || !reflect.DeepEqual(still, truncated) {
		t.Fatalf("stale replacement mutated catalog: %+v err=%v", still, err)
	}
}

func TestCatalogReplaceSourceConcurrentStaleCallersHaveOneWinner(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	original := sourceRecord("s1", []string{"project-a"}, 10)
	expected, err := catalog.UpsertSource(original)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, hash := range []string{"b", "c"} {
		replacement := original
		replacement.FrozenBoundary.SourceHash = strings.Repeat(hash, 64)
		go func() {
			_, err := catalog.ReplaceSource(expected, replacement)
			results <- err
		}()
	}
	winners, stale := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrCASConflict):
			stale++
		default:
			t.Fatalf("unexpected concurrent replacement error: %v", err)
		}
	}
	if winners != 1 || stale != 1 {
		t.Fatalf("replacement winners=%d stale=%d", winners, stale)
	}
}

func TestCatalogRejectsRedirectedNamespaceWithoutWritingOutside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ordinary symlink setup requires Windows privileges; pathguard has Windows reparse tests")
	}
	dataRoot := t.TempDir()
	out := t.TempDir()
	if err := os.Symlink(out, filepath.Join(dataRoot, "source-catalog")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dataRoot); err == nil {
		t.Fatal("redirected catalog namespace was accepted")
	}
	entries, err := os.ReadDir(out)
	if err != nil || len(entries) != 0 {
		t.Fatalf("redirect target mutated: entries=%v err=%v", entries, err)
	}
}

func TestCatalogReadersWaitForConcurrentAtomicPublication(t *testing.T) {
	dataRoot := t.TempDir()
	writer := openCatalog(t, dataRoot)
	reader := openCatalog(t, dataRoot)
	publicationReady := make(chan struct{})
	releasePublication := make(chan struct{})
	var publishOnce sync.Once
	writer.beforePublish = func() error {
		publishOnce.Do(func() { close(publicationReady) })
		<-releasePublication
		return nil
	}
	readerAttempts := make(chan struct{}, 3)
	reader.beforeAdvisoryLock = func() { readerAttempts <- struct{}{} }

	writeResult := make(chan error, 1)
	go func() {
		_, err := writer.UpsertSource(sourceRecord("s1", []string{"project-a"}, 10))
		writeResult <- err
	}()
	receiveSignal(t, publicationReady, "writer publication checkpoint")
	if !catalogHasAtomicTemporary(t, dataRoot) {
		t.Fatal("publication checkpoint did not expose the intended atomic temporary window")
	}

	type readResult struct {
		name string
		err  error
	}
	results := make(chan readResult, 3)
	go func() {
		records, err := reader.ListCandidates()
		if err == nil && (len(records) != 1 || records[0].SessionID != "s1") {
			err = errors.New("enumeration did not return canonical published record")
		}
		results <- readResult{name: "list", err: err}
	}()
	go func() {
		record, found, err := reader.GetSource("codex", "s1")
		if err == nil && (!found || record.SessionID != "s1") {
			err = errors.New("lookup did not return canonical published record")
		}
		results <- readResult{name: "get", err: err}
	}()
	go func() {
		usage, err := reader.AssociatedUsage("project-a")
		if err == nil && (len(usage) != 1 || usage[0].SessionID != "s1") {
			err = errors.New("usage did not return canonical published record")
		}
		results <- readResult{name: "usage", err: err}
	}()
	for range 3 {
		receiveSignal(t, readerAttempts, "reader advisory-lock attempt")
	}
	select {
	case result := <-results:
		t.Fatalf("reader %s completed before publication release: %v", result.name, result.err)
	default:
	}

	close(releasePublication)
	if err := receiveResult(t, writeResult, "catalog upsert"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		result := receiveResult(t, results, "catalog reader")
		if result.err != nil {
			t.Fatalf("reader %s: %v", result.name, result.err)
		}
	}
}

func openCatalog(t *testing.T, dataRoot string) *Catalog {
	t.Helper()
	catalog, err := Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	return catalog
}

func sourceRecord(sessionID string, projectIDs []string, total int64) memory.SourceRecord {
	return memory.SourceRecord{
		SchemaVersion:  memory.MemorySchemaVersion,
		Provider:       "codex",
		SessionID:      sessionID,
		SourceIdentity: "source-" + sessionID,
		StartedAt:      "2026-08-31T10:00:00Z",
		EndedAt:        "2026-08-31T10:00:01Z",
		FrozenBoundary: memory.FrozenBoundary{
			Location:   memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: 9, ByteOffset: 128}},
			SourceHash: strings.Repeat("a", 64),
		},
		Availability: memory.SourceAvailable,
		Usage: accounting.SessionUsage{
			StartedAt:  "2026-08-31T10:00:00Z",
			EndedAt:    "2026-08-31T10:00:01Z",
			DurationMS: 1000,
			Models: []accounting.ModelUsage{{Model: "gpt-5", TokenUsage: accounting.TokenUsage{
				InputTokens: total, TotalTokens: total,
			}}},
			TotalTokens: total,
		},
		ProjectIDs: append([]string(nil), projectIDs...),
	}
}

func catalogJSONCount(t *testing.T, dataRoot string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataRoot, "source-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count
}

func catalogJSONSnapshot(t *testing.T, dataRoot string) [][]byte {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataRoot, "source-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	var result [][]byte
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dataRoot, "source-catalog", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, body)
	}
	sort.Slice(result, func(i, j int) bool { return bytes.Compare(result[i], result[j]) < 0 })
	return result
}

func catalogHasAtomicTemporary(t *testing.T, dataRoot string) bool {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataRoot, "source-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".session-reviewer-") {
			return true
		}
	}
	return false
}

func receiveSignal(t *testing.T, channel <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func receiveResult[T any](t *testing.T, channel <-chan T, name string) T {
	t.Helper()
	select {
	case result := <-channel:
		return result
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
		var zero T
		return zero
	}
}
