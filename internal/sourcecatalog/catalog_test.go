package sourcecatalog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/source"
)

func TestApplyBatchIsAllOrNoneAndPreservesAssociations(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	first := sourceRecord("s1", []string{"project-a"}, 10)
	firstDigest, err := catalog.UpsertSource(first)
	if err != nil {
		t.Fatal(err)
	}
	second := sourceRecord("s2", []string{"project-b"}, 20)
	secondDigest, err := catalog.UpsertSource(second)
	if err != nil {
		t.Fatal(err)
	}

	appended := first
	appended.ProjectIDs = []string{"project-c"}
	appended.FrozenBoundary.Location.JSONL = &memory.JSONLSourceLocation{Line: 11, ByteOffset: 160}
	appended.FrozenBoundary.SourceHash = strings.Repeat("b", 64)
	broken := second
	broken.FrozenBoundary.SourceHash = strings.Repeat("c", 64)
	_, err = catalog.ApplyBatch([]BatchMutation{
		{Relation: source.BoundaryAppend, ExpectedDigest: firstDigest, Desired: appended},
		{Relation: source.BoundaryReplacement, ExpectedDigest: "sha256:" + strings.Repeat("0", 64), Desired: broken},
	})
	if !errors.Is(err, ErrCASConflict) {
		t.Fatalf("batch error=%v want CAS conflict", err)
	}
	gotFirst, _, _ := catalog.GetSource("codex", "s1")
	gotSecond, _, _ := catalog.GetSource("codex", "s2")
	if !reflect.DeepEqual(gotFirst, first) || !reflect.DeepEqual(gotSecond, second) {
		t.Fatalf("failed batch partially published first=%+v second=%+v", gotFirst, gotSecond)
	}

	results, err := catalog.ApplyBatch([]BatchMutation{
		{Relation: source.BoundaryAppend, ExpectedDigest: firstDigest, Desired: appended},
		{Relation: source.BoundaryUnchanged, ExpectedDigest: secondDigest, Desired: second},
	})
	if err != nil || len(results) != 2 {
		t.Fatalf("apply batch results=%+v err=%v", results, err)
	}
	gotFirst, _, _ = catalog.GetSource("codex", "s1")
	if fmt.Sprint(gotFirst.ProjectIDs) != fmt.Sprint([]string{"project-a", "project-c"}) {
		t.Fatalf("append removed association: %v", gotFirst.ProjectIDs)
	}
}

func TestApplyBatchExactDesiredIsIdempotentAfterExpectedBecameStale(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	record := sourceRecord("s1", []string{"project-a"}, 10)
	result, err := catalog.ApplyBatch([]BatchMutation{{Relation: source.BoundaryInitial, Desired: record}})
	if err != nil || len(result) != 1 {
		t.Fatalf("initial batch result=%+v err=%v", result, err)
	}
	result, err = catalog.ApplyBatch([]BatchMutation{{Relation: source.BoundaryInitial, ExpectedDigest: "sha256:" + strings.Repeat("f", 64), Desired: record}})
	if err != nil || len(result) != 1 || result[0].Digest == "" {
		t.Fatalf("idempotent batch result=%+v err=%v", result, err)
	}
}

func TestApplyBatchUnchangedCannotRewriteTimeOrUsage(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	record := sourceRecord("s1", []string{"project-a"}, 10)
	expected, err := catalog.UpsertSource(record)
	if err != nil {
		t.Fatal(err)
	}
	changed := record
	changed.EndedAt = "2026-08-31T10:00:02Z"
	changed.Usage.EndedAt = changed.EndedAt
	changed.Usage.DurationMS = 2000
	if _, err := catalog.ApplyBatch([]BatchMutation{{Relation: source.BoundaryUnchanged, ExpectedDigest: expected, Desired: changed}}); err == nil {
		t.Fatal("unchanged relation rewrote time and usage")
	}
	got, _, _ := catalog.GetSource("codex", "s1")
	if !reflect.DeepEqual(got, record) {
		t.Fatalf("rejected unchanged mutation changed record: %+v", got)
	}
}

func TestApplyBatchConcurrentStaleCallersHaveOneWholeBatchWinner(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	original := sourceRecord("s1", []string{"project-a"}, 10)
	expected, err := catalog.UpsertSource(original)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, hash := range []string{"b", "c"} {
		desired := original
		desired.FrozenBoundary.Location.JSONL = &memory.JSONLSourceLocation{Line: 11, ByteOffset: 160}
		desired.FrozenBoundary.SourceHash = strings.Repeat(hash, 64)
		go func(value memory.SourceRecord) {
			_, err := catalog.ApplyBatch([]BatchMutation{{Relation: source.BoundaryAppend, ExpectedDigest: expected, Desired: value}})
			results <- err
		}(desired)
	}
	winners, stale := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			winners++
		case errors.Is(err, ErrCASConflict):
			stale++
		default:
			t.Fatalf("unexpected batch result: %v", err)
		}
	}
	if winners != 1 || stale != 1 {
		t.Fatalf("winners=%d stale=%d", winners, stale)
	}
}

func TestWithBatchSnapshotRejectsChangedCatalogBeforeCommit(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	original := sourceRecord("s1", []string{"project-a"}, 10)
	results, err := catalog.ApplyBatch([]BatchMutation{{Relation: source.BoundaryInitial, Desired: original}})
	if err != nil {
		t.Fatal(err)
	}
	appended := original
	appended.FrozenBoundary.Location.JSONL = &memory.JSONLSourceLocation{Line: 11, ByteOffset: 160}
	appended.FrozenBoundary.SourceHash = strings.Repeat("b", 64)
	if _, err := catalog.ApplyBatch([]BatchMutation{{Relation: source.BoundaryAppend, ExpectedDigest: results[0].Digest, Desired: appended}}); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := catalog.WithBatchSnapshot(results, func() error { called = true; return nil }); !errors.Is(err, ErrCASConflict) || called {
		t.Fatalf("stale snapshot err=%v callback=%v", err, called)
	}
}

func TestApplyBatchRecoversEveryProcessCrashCheckpointToFullBatch(t *testing.T) {
	for checkpoint := 1; checkpoint <= 4; checkpoint++ {
		t.Run(strconv.Itoa(checkpoint), func(t *testing.T) {
			dataRoot := t.TempDir()
			catalog := openCatalog(t, dataRoot)
			for _, id := range []string{"s1", "s2"} {
				if _, err := catalog.UpsertSource(sourceRecord(id, []string{"project-a"}, 10)); err != nil {
					t.Fatal(err)
				}
			}
			if err := catalog.Close(); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestApplyBatchCrashHelper$")
			command.Env = append(os.Environ(), "SR_CATALOG_CRASH_ROOT="+dataRoot, "SR_CATALOG_CRASH_POINT="+strconv.Itoa(checkpoint))
			if err := command.Run(); err == nil {
				t.Fatal("crash helper exited successfully")
			}

			reopened, err := Open(dataRoot)
			if err != nil {
				t.Fatalf("reopen after crash: %v", err)
			}
			defer reopened.Close()
			for _, id := range []string{"s1", "s2"} {
				record, found, err := reopened.GetSource("codex", id)
				if err != nil || !found || record.FrozenBoundary.Location.JSONL.Line != 11 {
					t.Fatalf("source %s did not converge to full batch record=%+v found=%v err=%v", id, record, found, err)
				}
			}
			if _, err := os.Stat(filepath.Join(dataRoot, "source-catalog", batchJournalLeaf)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("batch journal remained: %v", err)
			}
		})
	}
}

func TestOpenRecoversBoundedBatchJournalLargerThanSingleRecordLimit(t *testing.T) {
	dataRoot := t.TempDir()
	catalog := openCatalog(t, dataRoot)
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	projects := make([]string, 4096)
	for index := range projects {
		projects[index] = fmt.Sprintf("p%04d-%s", index, strings.Repeat("x", 108))
	}
	journal := batchJournal{Version: 1}
	for index := 0; index < 10; index++ {
		record := sourceRecord(fmt.Sprintf("large-%d", index), projects, 10)
		digest, err := memory.Digest(record)
		if err != nil {
			t.Fatal(err)
		}
		journal.Entries = append(journal.Entries, batchJournalEntry{Leaf: sourceLeaf(record.Provider, record.SessionID), Relation: source.BoundaryInitial, Desired: record, Digest: digest})
	}
	body, err := marshalCanonical(journal)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= maxCatalogRecord {
		t.Fatalf("fixture journal=%d did not exceed record limit", len(body))
	}
	if err := os.WriteFile(filepath.Join(dataRoot, "source-catalog", batchJournalLeaf), body, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dataRoot)
	if err != nil {
		t.Fatalf("recover large bounded journal: %v", err)
	}
	defer reopened.Close()
	records, err := reopened.ListCandidates()
	if err != nil || len(records) != 10 {
		t.Fatalf("recovered records=%d err=%v", len(records), err)
	}
}

func TestOpenRejectsSemanticallyInvalidJournalBeforeMutatingAnyRecord(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*batchJournalEntry)
	}{
		{name: "association-shrink", mutate: func(entry *batchJournalEntry) { entry.Desired.ProjectIDs = []string{"project-a"} }},
		{name: "wrong-relation", mutate: func(entry *batchJournalEntry) { entry.Relation = source.BoundaryUnchanged }},
		{name: "missing-predecessor-body", mutate: func(entry *batchJournalEntry) { entry.Old = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataRoot := t.TempDir()
			catalog := openCatalog(t, dataRoot)
			old1 := sourceRecord("s1", []string{"project-a"}, 10)
			old2 := sourceRecord("s2", []string{"project-a", "project-b"}, 10)
			for _, record := range []memory.SourceRecord{old1, old2} {
				if _, err := catalog.UpsertSource(record); err != nil {
					t.Fatal(err)
				}
			}
			if err := catalog.Close(); err != nil {
				t.Fatal(err)
			}
			entries := make([]batchJournalEntry, 0, 2)
			for _, old := range []memory.SourceRecord{old1, old2} {
				oldDigest, err := memory.Digest(old)
				if err != nil {
					t.Fatal(err)
				}
				desired := old
				desired.FrozenBoundary.Location.JSONL = &memory.JSONLSourceLocation{Line: 10, ByteOffset: 160}
				desired.FrozenBoundary.SourceHash = strings.Repeat("b", 64)
				desiredDigest, err := memory.Digest(desired)
				if err != nil {
					t.Fatal(err)
				}
				predecessor := old
				entries = append(entries, batchJournalEntry{Leaf: sourceLeaf(old.Provider, old.SessionID), Relation: source.BoundaryAppend, Old: &predecessor, OldDigest: oldDigest, Desired: desired, Digest: desiredDigest})
			}
			test.mutate(&entries[1])
			entries[1].Digest, _ = memory.Digest(entries[1].Desired)
			journalBody, err := marshalCanonical(batchJournal{Version: 1, Entries: entries})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dataRoot, "source-catalog", batchJournalLeaf), journalBody, 0o600); err != nil {
				t.Fatal(err)
			}
			firstPath := filepath.Join(dataRoot, "source-catalog", sourceLeaf(old1.Provider, old1.SessionID))
			before, err := os.ReadFile(firstPath)
			if err != nil {
				t.Fatal(err)
			}
			if recovered, err := Open(dataRoot); err == nil {
				_ = recovered.Close()
				t.Fatal("semantically invalid journal recovered")
			}
			after, err := os.ReadFile(firstPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("invalid later journal entry mutated earlier source")
			}
		})
	}
}

func TestOpenRejectsJournalWhoseRequiredPredecessorIsMissing(t *testing.T) {
	dataRoot := t.TempDir()
	catalog := openCatalog(t, dataRoot)
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	old := sourceRecord("missing-old", []string{"project-a"}, 10)
	oldDigest, _ := memory.Digest(old)
	desired := old
	desired.FrozenBoundary.Location.JSONL = &memory.JSONLSourceLocation{Line: 10, ByteOffset: 160}
	desired.FrozenBoundary.SourceHash = strings.Repeat("b", 64)
	desiredDigest, _ := memory.Digest(desired)
	journal := batchJournal{Version: 1, Entries: []batchJournalEntry{{Leaf: sourceLeaf(old.Provider, old.SessionID), Relation: source.BoundaryAppend, Old: &old, OldDigest: oldDigest, Desired: desired, Digest: desiredDigest}}}
	body, err := marshalCanonical(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataRoot, "source-catalog", batchJournalLeaf), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if recovered, err := Open(dataRoot); err == nil {
		_ = recovered.Close()
		t.Fatal("missing required predecessor was accepted")
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "source-catalog", sourceLeaf(old.Provider, old.SessionID))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery invented missing predecessor state: %v", err)
	}
}

func TestApplyBatchCrashHelper(t *testing.T) {
	dataRoot := os.Getenv("SR_CATALOG_CRASH_ROOT")
	if dataRoot == "" {
		t.Skip("subprocess helper")
	}
	point, err := strconv.Atoi(os.Getenv("SR_CATALOG_CRASH_POINT"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	catalog.batchCheckpoint = func() {
		count++
		if count == point {
			os.Exit(71)
		}
	}
	mutations := make([]BatchMutation, 0, 2)
	for _, id := range []string{"s1", "s2"} {
		existing, found, err := catalog.GetSource("codex", id)
		if err != nil || !found {
			t.Fatal(err)
		}
		expected, err := memory.Digest(existing)
		if err != nil {
			t.Fatal(err)
		}
		desired := existing
		desired.FrozenBoundary.Location.JSONL = &memory.JSONLSourceLocation{Line: 11, ByteOffset: 160}
		desired.FrozenBoundary.SourceHash = strings.Repeat("d", 64)
		mutations = append(mutations, BatchMutation{Relation: source.BoundaryAppend, ExpectedDigest: expected, Desired: desired})
	}
	_, _ = catalog.ApplyBatch(mutations)
}

func TestJournalHardLinkCrashRecoversOrphanTempAndFullBatch(t *testing.T) {
	dataRoot := t.TempDir()
	catalog := openCatalog(t, dataRoot)
	for _, id := range []string{"s1", "s2"} {
		if _, err := catalog.UpsertSource(sourceRecord(id, []string{"project-a"}, 10)); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestJournalHardLinkCrashHelper$")
	command.Env = append(os.Environ(), "SR_CATALOG_LINK_CRASH_ROOT="+dataRoot)
	if err := command.Run(); err == nil {
		t.Fatal("hard-link crash helper exited successfully")
	}
	stagingPath := filepath.Join(dataRoot, "source-catalog", batchJournalStagingLeaf)
	targetPath := filepath.Join(dataRoot, "source-catalog", batchJournalLeaf)
	staging, stagingErr := os.Lstat(stagingPath)
	target, targetErr := os.Lstat(targetPath)
	if stagingErr != nil || targetErr != nil || !os.SameFile(staging, target) {
		t.Fatalf("post-link crash did not leave one proven staging alias: staging=%v/%v target=%v/%v", staging, stagingErr, target, targetErr)
	}
	reopened, err := Open(dataRoot)
	if err != nil {
		t.Fatalf("reopen hard-link crash: %v", err)
	}
	defer reopened.Close()
	records, err := reopened.ListCandidates()
	if err != nil || len(records) != 2 {
		t.Fatalf("ListCandidates records=%+v err=%v", records, err)
	}
	for _, record := range records {
		if record.FrozenBoundary.Location.JSONL.Line != 11 {
			t.Fatalf("record did not recover full batch: %+v", record)
		}
	}
	if _, err := os.Lstat(stagingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging alias remains: %v", err)
	}
}

func TestJournalHardLinkCrashHelper(t *testing.T) {
	dataRoot := os.Getenv("SR_CATALOG_LINK_CRASH_ROOT")
	if dataRoot == "" {
		t.Skip("subprocess helper")
	}
	catalog, err := Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	catalog.afterJournalLink = func() error { os.Exit(72); return nil }
	mutations := make([]BatchMutation, 0, 2)
	for _, id := range []string{"s1", "s2"} {
		old, found, err := catalog.GetSource("codex", id)
		if err != nil || !found {
			t.Fatal(err)
		}
		expected, _ := memory.Digest(old)
		desired := old
		desired.FrozenBoundary.Location.JSONL = &memory.JSONLSourceLocation{Line: 11, ByteOffset: 160}
		desired.FrozenBoundary.SourceHash = strings.Repeat("d", 64)
		mutations = append(mutations, BatchMutation{Relation: source.BoundaryAppend, ExpectedDigest: expected, Desired: desired})
	}
	_, _ = catalog.ApplyBatch(mutations)
}

func TestJournalPreLinkCrashRecoversDeterministicStagingAndFullBatch(t *testing.T) {
	dataRoot := t.TempDir()
	catalog := openCatalog(t, dataRoot)
	for _, id := range []string{"s1", "s2"} {
		if _, err := catalog.UpsertSource(sourceRecord(id, []string{"project-a"}, 10)); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestJournalPreLinkCrashHelper$")
	command.Env = append(os.Environ(), "SR_CATALOG_PRELINK_CRASH_ROOT="+dataRoot)
	if err := command.Run(); err == nil {
		t.Fatal("pre-link crash helper exited successfully")
	}
	stagingPath := filepath.Join(dataRoot, "source-catalog", batchJournalStagingLeaf)
	if _, err := os.Lstat(stagingPath); err != nil {
		t.Fatalf("deterministic staging is unavailable after crash: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dataRoot, "source-catalog", batchJournalLeaf)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-link crash unexpectedly published journal: %v", err)
	}

	reopened, err := Open(dataRoot)
	if err != nil {
		t.Fatalf("reopen pre-link crash: %v", err)
	}
	defer reopened.Close()
	for _, id := range []string{"s1", "s2"} {
		record, found, err := reopened.GetSource("codex", id)
		if err != nil || !found || record.FrozenBoundary.Location.JSONL.Line != 11 {
			t.Fatalf("source %s did not recover full batch record=%+v found=%v err=%v", id, record, found, err)
		}
	}
	if _, err := os.Lstat(stagingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered deterministic staging remains: %v", err)
	}
}

func TestJournalPreLinkCrashHelper(t *testing.T) {
	dataRoot := os.Getenv("SR_CATALOG_PRELINK_CRASH_ROOT")
	if dataRoot == "" {
		t.Skip("subprocess helper")
	}
	catalog, err := Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	catalog.beforeJournalLink = func() error { os.Exit(73); return nil }
	_, _ = catalog.ApplyBatch(appendMutations(t, catalog, "s1", "s2"))
}

func TestOpenRejectsValidLookingDifferentInodeJournalTemporary(t *testing.T) {
	dataRoot := t.TempDir()
	catalog := openCatalog(t, dataRoot)
	if _, err := catalog.UpsertSource(sourceRecord("s1", []string{"project-a"}, 10)); err != nil {
		t.Fatal(err)
	}
	body := appendJournalBody(t, catalog, "s1")
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dataRoot, "source-catalog")
	targetPath := filepath.Join(root, batchJournalLeaf)
	temporaryPath := filepath.Join(root, ".session-reviewer-"+strings.Repeat("a", 32))
	if err := os.WriteFile(targetPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporaryPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(dataRoot); err == nil {
		_ = reopened.Close()
		t.Fatal("different-inode journal temporary was accepted")
	}
	if _, err := os.Lstat(temporaryPath); err != nil {
		t.Fatalf("unowned journal temporary was mutated: %v", err)
	}
}

func TestJournalOrphanRevalidationRejectsEntrySwapBeforeUnlink(t *testing.T) {
	dataRoot := t.TempDir()
	catalog := openCatalog(t, dataRoot)
	if _, err := catalog.UpsertSource(sourceRecord("s1", []string{"project-a"}, 10)); err != nil {
		t.Fatal(err)
	}
	body := appendJournalBody(t, catalog, "s1")
	targetPath := filepath.Join(dataRoot, "source-catalog", batchJournalLeaf)
	temporaryName := ".session-reviewer-" + strings.Repeat("b", 32)
	temporaryPath := filepath.Join(dataRoot, "source-catalog", temporaryName)
	if err := os.WriteFile(targetPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(targetPath, temporaryPath); err != nil {
		t.Fatal(err)
	}
	catalog.beforeOrphanUnlink = func() {
		if err := os.Remove(temporaryPath); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(temporaryPath, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := catalog.ListCandidates(); err == nil {
		t.Fatal("same-content fresh-inode entry swap was accepted")
	}
	if _, err := os.Lstat(temporaryPath); err != nil {
		t.Fatalf("swapped temporary was removed: %v", err)
	}
}

func appendMutations(t *testing.T, catalog *Catalog, ids ...string) []BatchMutation {
	t.Helper()
	mutations := make([]BatchMutation, 0, len(ids))
	for _, id := range ids {
		old, found, err := catalog.GetSource("codex", id)
		if err != nil || !found {
			t.Fatalf("source %s baseline found=%v err=%v", id, found, err)
		}
		expected, err := memory.Digest(old)
		if err != nil {
			t.Fatal(err)
		}
		desired := old
		desired.FrozenBoundary.Location.JSONL = &memory.JSONLSourceLocation{Line: 11, ByteOffset: 160}
		desired.FrozenBoundary.SourceHash = strings.Repeat("d", 64)
		mutations = append(mutations, BatchMutation{Relation: source.BoundaryAppend, ExpectedDigest: expected, Desired: desired})
	}
	return mutations
}

func appendJournalBody(t *testing.T, catalog *Catalog, ids ...string) []byte {
	t.Helper()
	mutations := appendMutations(t, catalog, ids...)
	entries, _, err := catalog.planBatch(mutations)
	if err != nil {
		t.Fatal(err)
	}
	body, err := marshalCanonical(batchJournal{Version: 1, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestOpenFailsClosedOnMaliciousCatalogTemp(t *testing.T) {
	for _, kind := range []string{"arbitrary", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "symlink" && runtime.GOOS == "windows" {
				t.Skip("symlink setup requires privileges")
			}
			dataRoot := t.TempDir()
			catalog := openCatalog(t, dataRoot)
			if err := catalog.Close(); err != nil {
				t.Fatal(err)
			}
			name := ".session-reviewer-" + strings.Repeat("a", 32)
			path := filepath.Join(dataRoot, "source-catalog", name)
			var err error
			if kind == "symlink" {
				err = os.Symlink(filepath.Join(dataRoot, "outside"), path)
			} else {
				err = os.WriteFile(path, []byte("not canonical catalog state"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(dataRoot); err == nil {
				_ = reopened.Close()
				t.Fatalf("malicious %s temp accepted", kind)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("malicious temp was removed instead of failing closed: %v", err)
			}
		})
	}
}

func TestAtomicTempNameAcceptsOnlyWriterFormat(t *testing.T) {
	if !isAtomicTempName(".session-reviewer-" + strings.Repeat("a", 32)) {
		t.Fatal("writer temporary name rejected")
	}
	for _, name := range []string{
		".session-reviewer-" + strings.Repeat("A", 32),
		".session-reviewer-" + strings.Repeat("g", 32),
		".session-reviewer-" + strings.Repeat("a", 31),
		"other-" + strings.Repeat("a", 32),
	} {
		if isAtomicTempName(name) {
			t.Fatalf("non-writer temporary name accepted: %q", name)
		}
	}
}

func TestCatalogStoresSharedSessionUsageOnce(t *testing.T) {
	dataRoot := t.TempDir()
	catalog := openCatalog(t, dataRoot)
	first := sourceRecord("s1", []string{"project-b"}, 573135757)
	if _, err := catalog.UpsertSource(first); err != nil {
		t.Fatal(err)
	}
	second := sourceRecord("s1", []string{"project-a", "project-b", "project-a"}, 573135757)
	_, err := catalog.UpsertSource(second)
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
		usageDigest, digestErr := memory.Digest(second.Usage)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		want := memory.AssociatedUsage{Provider: "codex", SessionID: "s1", UsageRecordDigest: usageDigest, Shared: true}
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
