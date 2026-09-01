package memorystore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/memory"
)

var retentionNow = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func TestRetentionReportsExactReachableGraphAndDryRunDoesNotMutate(t *testing.T) {
	dataRoot, store, fixture := newRetentionStore(t, "generation-1")
	memoryRoot := retentionMemoryRoot(dataRoot)
	before := retentionInventory(t, memoryRoot)

	report, err := store.ReportRetention(retentionNow)
	if err != nil {
		t.Fatalf("report retention: %v", err)
	}
	wantObjects, wantBytes := reachableFixtureTotals(t, memoryRoot, fixture.manifest)
	if report != (RetentionReport{ReachableObjects: wantObjects, ReachableBytes: wantBytes}) {
		t.Fatalf("report=%+v want reachable_objects=%d reachable_bytes=%d", report, wantObjects, wantBytes)
	}
	after := retentionInventory(t, memoryRoot)
	if !equalRetentionInventory(before, after) {
		t.Fatalf("dry report changed private store metadata:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestRetentionKeepsNativeLineageAndValidatesOpaquePins(t *testing.T) {
	dataRoot, store, first := newRetentionStore(t, "generation-1")
	second := buildStoredFixture(t, store, "generation-2")
	expected, _, err := store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvancePrepared(expected, second.manifest); err != nil {
		t.Fatalf("advance prepared: %v", err)
	}

	report, err := store.ReportRetention(retentionNow)
	if err != nil {
		t.Fatalf("report native lineage: %v", err)
	}
	memoryRoot := retentionMemoryRoot(dataRoot)
	wantObjects, wantBytes := reachableFixtureTotals(t, memoryRoot, second.manifest)
	firstInfo, statErr := os.Stat(filepath.Join(memoryRoot, "generations", first.manifest.GenerationID+".json"))
	if statErr != nil {
		t.Fatal(statErr)
	}
	if report.ReachableObjects != wantObjects || report.ReachableBytes != wantBytes || report.RetainedUnreachableObjects != 1 || report.RetainedUnreachableBytes != firstInfo.Size() {
		t.Fatalf("orphan lineage accounting is wrong: report=%+v want reachable=%d/%d retained=1/%d", report, wantObjects, wantBytes, firstInfo.Size())
	}

	pinned, err := store.ReportRetention(retentionNow, first.manifest.GenerationID)
	if err != nil {
		t.Fatalf("report pinned lineage: %v", err)
	}
	if pinned.ReachableObjects != wantObjects+1 || pinned.ReachableBytes != wantBytes+firstInfo.Size() || pinned.RetainedUnreachableObjects != 0 || pinned.RetainedUnreachableBytes != 0 {
		t.Fatalf("validated pin did not promote only its graph: %+v", pinned)
	}

	for _, pins := range [][]string{{"missing-generation"}, {"../escape"}, {first.manifest.GenerationID, first.manifest.GenerationID}} {
		if _, err := store.ReportRetention(retentionNow, pins...); err == nil {
			t.Fatalf("invalid external pins accepted: %q", pins)
		}
	}
}

func TestRetentionCleanupBytesCountHardlinkedStorageOnceButEveryEntryIsUnlinked(t *testing.T) {
	dataRoot, store, _ := newRetentionStore(t, "generation-hardlinks")
	memoryRoot := retentionMemoryRoot(dataRoot)
	body := []byte("one-physical-candidate")
	cache := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", body, retentionNow.Add(-8*24*time.Hour))
	sum := sha256.Sum256(body)
	stage := filepath.Join(memoryRoot, "staging", hex.EncodeToString(sum[:])+".stage")
	if err := os.Link(cache, stage); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	report, err := store.ReportRetention(retentionNow)
	if err != nil {
		t.Fatal(err)
	}
	if report.CleanupCandidates != 2 || report.CleanupBytes != int64(len(body)) {
		t.Fatalf("hardlink accounting=%+v want entries=2 unique_bytes=%d", report, len(body))
	}
	if _, err := store.CleanupUnreachable(retentionNow); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{cache, stage} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("authorized hardlink entry remains %q: %v", filepath.Base(path), err)
		}
	}
}

func TestRetentionGraceBoundaryAndCleanupUseCanonicalRootedCandidates(t *testing.T) {
	dataRoot, store, _ := newRetentionStore(t, "generation-1")
	memoryRoot := retentionMemoryRoot(dataRoot)
	oldCache := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte("old-cache"), retentionNow.Add(-7*24*time.Hour))
	oldStage := writeRetentionCandidate(t, memoryRoot, "staging", ".stage", []byte("old-stage"), retentionNow.Add(-8*24*time.Hour))
	youngCache := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte("young-cache"), retentionNow.Add(-7*24*time.Hour+time.Nanosecond))

	report, err := store.ReportRetention(retentionNow)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := fileSize(t, oldCache) + fileSize(t, oldStage)
	if report.CleanupCandidates != 2 || report.CleanupBytes != wantBytes {
		t.Fatalf("grace report=%+v want candidates=2 bytes=%d", report, wantBytes)
	}
	if _, err := os.Stat(oldCache); err != nil {
		t.Fatalf("report mutated exact-boundary candidate: %v", err)
	}

	cleaned, err := store.CleanupUnreachable(retentionNow)
	if err != nil {
		t.Fatalf("cleanup unreachable: %v", err)
	}
	if cleaned.CleanupCandidates != 0 || cleaned.CleanupBytes != 0 {
		t.Fatalf("cleanup returned stale candidate totals: %+v", cleaned)
	}
	for _, removed := range []string{oldCache, oldStage} {
		if _, err := os.Lstat(removed); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old candidate remains %q: %v", filepath.Base(removed), err)
		}
	}
	if _, err := os.Stat(youngCache); err != nil {
		t.Fatalf("younger-than-seven-days cache was removed: %v", err)
	}
}

func TestRetentionFailsClosedOnCorruptGraphNamespaceRedirectAndPermissions(t *testing.T) {
	t.Run("corrupt generation", func(t *testing.T) {
		dataRoot, store, fixture := newRetentionStore(t, "generation-corrupt")
		candidate := writeRetentionCandidate(t, retentionMemoryRoot(dataRoot), "cache", ".cache", []byte("candidate"), retentionNow.Add(-8*24*time.Hour))
		generationPath := filepath.Join(retentionMemoryRoot(dataRoot), "generations", fixture.manifest.GenerationID+".json")
		if err := os.WriteFile(generationPath, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CleanupUnreachable(retentionNow); err == nil {
			t.Fatal("corrupt generation graph did not fail closed")
		}
		assertExists(t, candidate)
	})

	t.Run("unknown namespace", func(t *testing.T) {
		dataRoot, store, _ := newRetentionStore(t, "generation-namespace")
		candidate := writeRetentionCandidate(t, retentionMemoryRoot(dataRoot), "staging", ".stage", []byte("candidate"), retentionNow.Add(-8*24*time.Hour))
		if err := os.Mkdir(filepath.Join(retentionMemoryRoot(dataRoot), "unknown-state"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CleanupUnreachable(retentionNow); err == nil {
			t.Fatal("unknown memory namespace was accepted")
		}
		assertExists(t, candidate)
	})

	t.Run("noncanonical entry", func(t *testing.T) {
		dataRoot, store, _ := newRetentionStore(t, "generation-entry")
		candidate := writeRetentionCandidate(t, retentionMemoryRoot(dataRoot), "cache", ".cache", []byte("candidate"), retentionNow.Add(-8*24*time.Hour))
		if err := os.WriteFile(filepath.Join(retentionMemoryRoot(dataRoot), "staging", "not-canonical.tmp"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CleanupUnreachable(retentionNow); err == nil {
			t.Fatal("noncanonical staging entry was accepted")
		}
		assertExists(t, candidate)
	})

	t.Run("redirect", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("unprivileged symlink setup is unavailable; Windows reparse behavior is cross-compiled and covered by pathguard")
		}
		dataRoot, store, _ := newRetentionStore(t, "generation-redirect")
		memoryRoot := retentionMemoryRoot(dataRoot)
		outside := filepath.Join(t.TempDir(), "outside.cache")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte("outside"))
		link := filepath.Join(memoryRoot, "cache")
		if err := os.Mkdir(link, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(link, hex.EncodeToString(sum[:])+".cache")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := store.ReportRetention(retentionNow); err == nil {
			t.Fatal("redirected cache entry was accepted")
		}
		assertExists(t, outside)
	})

	t.Run("permission", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX mode contract")
		}
		dataRoot, store, _ := newRetentionStore(t, "generation-mode")
		candidate := writeRetentionCandidate(t, retentionMemoryRoot(dataRoot), "cache", ".cache", []byte("mode"), retentionNow.Add(-8*24*time.Hour))
		if err := os.Chmod(candidate, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CleanupUnreachable(retentionNow); err == nil {
			t.Fatal("public cache candidate was accepted")
		}
		assertExists(t, candidate)
	})
}

func TestRetentionRechecksIdentityMetadataAndNamespaceBeforeDelete(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, candidate, memoryRoot string)
	}{
		{
			name: "entry replacement",
			mutate: func(t *testing.T, candidate, _ string) {
				moved := candidate + ".moved"
				if err := os.Rename(candidate, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(candidate, []byte("replacement"), 0o600); err != nil {
					t.Fatal(err)
				}
				_ = os.Chtimes(candidate, retentionNow.Add(-9*24*time.Hour), retentionNow.Add(-9*24*time.Hour))
			},
		},
		{
			name: "metadata change",
			mutate: func(t *testing.T, candidate, _ string) {
				if err := os.Chtimes(candidate, retentionNow.Add(-9*24*time.Hour), retentionNow.Add(-9*24*time.Hour)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "namespace addition",
			mutate: func(t *testing.T, _, memoryRoot string) {
				writeRetentionCandidate(t, memoryRoot, "staging", ".stage", []byte("concurrent"), retentionNow.Add(-8*24*time.Hour))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataRoot, store, _ := newRetentionStore(t, "generation-toctou")
			memoryRoot := retentionMemoryRoot(dataRoot)
			candidate := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte("original"), retentionNow.Add(-8*24*time.Hour))
			var invoked atomic.Bool
			retentionDeleteCheckpoint = func() error {
				if invoked.CompareAndSwap(false, true) {
					test.mutate(t, candidate, memoryRoot)
				}
				return nil
			}
			t.Cleanup(func() { retentionDeleteCheckpoint = nil })
			if _, err := store.CleanupUnreachable(retentionNow); err == nil {
				t.Fatal("cleanup accepted a candidate changed after planning")
			}
			if !invoked.Load() {
				t.Fatal("delete revalidation checkpoint was not reached")
			}
			assertExists(t, candidate)
		})
	}
}

func TestRetentionSerializesConcurrentAdvanceAndCopiesExplicitPins(t *testing.T) {
	dataRoot, store, first := newRetentionStore(t, "generation-concurrent-a")
	second := buildStoredFixture(t, store, "generation-concurrent-b")
	expected, _, err := store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	candidate := writeRetentionCandidate(t, retentionMemoryRoot(dataRoot), "cache", ".cache", []byte("concurrent-cleanup"), retentionNow.Add(-8*24*time.Hour))
	pins := []string{first.manifest.GenerationID}
	started := make(chan struct{})
	advanced := make(chan error, 1)
	var invoked atomic.Bool
	retentionDeleteCheckpoint = func() error {
		if invoked.CompareAndSwap(false, true) {
			pins[0] = "caller-mutated-pin"
			go func() {
				close(started)
				_, err := store.AdvancePrepared(expected, second.manifest)
				advanced <- err
			}()
			<-started
		}
		return nil
	}
	t.Cleanup(func() { retentionDeleteCheckpoint = nil })
	if _, err := store.CleanupUnreachable(retentionNow, pins...); err != nil {
		t.Fatalf("cleanup with copied external pin: %v", err)
	}
	if err := <-advanced; err != nil {
		t.Fatalf("concurrent prepared advance after cleanup: %v", err)
	}
	if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("eligible cache survived serialized cleanup: %v", err)
	}
	prepared, _, err := store.LoadPrepared()
	if err != nil || prepared.GenerationID != second.manifest.GenerationID {
		t.Fatalf("concurrent advance did not converge: prepared=%+v err=%v", prepared, err)
	}
	if _, err := store.ReportRetention(retentionNow, first.manifest.GenerationID, second.manifest.GenerationID); err != nil {
		t.Fatalf("native and explicit successor pins did not reconcile: %v", err)
	}
}

func TestRetentionSerializesConcurrentInitialPrepare(t *testing.T) {
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := buildStoredFixture(t, store, "generation-concurrent-initial")
	candidate := writeRetentionCandidate(t, retentionMemoryRoot(dataRoot), "staging", ".stage", []byte("initial-prepare"), retentionNow.Add(-8*24*time.Hour))
	prepared := make(chan error, 1)
	started := make(chan struct{})
	var invoked atomic.Bool
	retentionDeleteCheckpoint = func() error {
		if invoked.CompareAndSwap(false, true) {
			go func() {
				close(started)
				_, err := store.PrepareGeneration(fixture.manifest)
				prepared <- err
			}()
			<-started
		}
		return nil
	}
	t.Cleanup(func() { retentionDeleteCheckpoint = nil })
	if _, err := store.CleanupUnreachable(retentionNow); err != nil {
		t.Fatalf("cleanup before initial prepare: %v", err)
	}
	if err := <-prepared; err != nil {
		t.Fatalf("concurrent initial prepare after cleanup: %v", err)
	}
	if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("eligible staging survived serialized cleanup: %v", err)
	}
	current, manifest, err := store.LoadPrepared()
	if err != nil || current.GenerationID != fixture.manifest.GenerationID || manifest.GenerationID != fixture.manifest.GenerationID {
		t.Fatalf("initial prepare did not converge: current=%+v manifest=%+v err=%v", current, manifest, err)
	}
}

func TestRetentionRejectsInvalidPinBeforeAnyDeletion(t *testing.T) {
	dataRoot, store, _ := newRetentionStore(t, "generation-pin-fail")
	candidate := writeRetentionCandidate(t, retentionMemoryRoot(dataRoot), "staging", ".stage", []byte("pin-fail"), retentionNow.Add(-8*24*time.Hour))
	if _, err := store.CleanupUnreachable(retentionNow, "missing-generation"); err == nil {
		t.Fatal("cleanup accepted a missing external generation pin")
	}
	assertExists(t, candidate)
}

func TestRetentionCleanupIsRestartableAfterPartialProcessExit(t *testing.T) {
	if os.Getenv("SESSION_REVIEWER_RETENTION_CRASH_HELPER") == "1" {
		retentionCleanupCrashHelper(t)
		return
	}
	dataRoot, store, _ := newRetentionStore(t, "generation-crash")
	memoryRoot := retentionMemoryRoot(dataRoot)
	first := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte("a-first"), retentionNow.Add(-8*24*time.Hour))
	second := writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte("z-second"), retentionNow.Add(-8*24*time.Hour))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestRetentionCleanupIsRestartableAfterPartialProcessExit$")
	command.Env = append(os.Environ(), "SESSION_REVIEWER_RETENTION_CRASH_HELPER=1", "SESSION_REVIEWER_RETENTION_DATA_ROOT="+dataRoot)
	if err := command.Run(); err == nil {
		t.Fatal("retention crash helper did not exit")
	}
	remaining := 0
	for _, candidate := range []string{first, second} {
		if _, err := os.Lstat(candidate); err == nil {
			remaining++
		}
	}
	if remaining != 1 {
		t.Fatalf("partial cleanup removed %d candidates, want exactly one", 2-remaining)
	}

	restarted, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatalf("reopen after cleanup crash: %v", err)
	}
	defer restarted.Close()
	if _, err := restarted.CleanupUnreachable(retentionNow); err != nil {
		t.Fatalf("resume cleanup: %v", err)
	}
	for _, candidate := range []string{first, second} {
		if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("candidate remains after restart %q: %v", filepath.Base(candidate), err)
		}
	}
}

func TestRetentionCleanupUsesOneFullSnapshotAndLinearCandidateRevalidation(t *testing.T) {
	dataRoot, store, _ := newRetentionStore(t, "generation-linear")
	memoryRoot := retentionMemoryRoot(dataRoot)
	const candidates = 64
	for index := 0; index < candidates; index++ {
		writeRetentionCandidate(t, memoryRoot, "cache", ".cache", []byte(fmt.Sprintf("linear-%03d", index)), retentionNow.Add(-8*24*time.Hour))
	}
	var snapshots, revalidations atomic.Int32
	retentionFullSnapshotCheckpoint = func() { snapshots.Add(1) }
	retentionCandidateRevalidationCheckpoint = func() { revalidations.Add(1) }
	t.Cleanup(func() {
		retentionFullSnapshotCheckpoint = nil
		retentionCandidateRevalidationCheckpoint = nil
	})
	report, err := store.CleanupUnreachableContext(context.Background(), retentionNow)
	if err != nil {
		t.Fatal(err)
	}
	if snapshots.Load() != 1 || revalidations.Load() != candidates {
		t.Fatalf("cleanup complexity snapshots=%d revalidations=%d want 1/%d", snapshots.Load(), revalidations.Load(), candidates)
	}
	if report.CleanupCandidates != 0 || report.CleanupBytes != 0 {
		t.Fatalf("completed cleanup report=%+v", report)
	}
}

func TestRetentionCleanupCancellationReturnsValidPartialReportAndCanResume(t *testing.T) {
	dataRoot, store, _ := newRetentionStore(t, "generation-cancel")
	memoryRoot := retentionMemoryRoot(dataRoot)
	paths := make([]string, 5)
	for index := range paths {
		paths[index] = writeRetentionCandidate(t, memoryRoot, "staging", ".stage", []byte(fmt.Sprintf("cancel-%d", index)), retentionNow.Add(-8*24*time.Hour))
	}
	ctx, cancel := context.WithCancel(context.Background())
	var checkpoints atomic.Int32
	retentionDeleteCheckpoint = func() error {
		if checkpoints.Add(1) == 2 {
			cancel()
		}
		return nil
	}
	t.Cleanup(func() { retentionDeleteCheckpoint = nil })
	report, err := store.CleanupUnreachableContext(ctx, retentionNow)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cleanup cancellation error=%v", err)
	}
	if report.CleanupCandidates != 4 {
		t.Fatalf("partial cleanup report=%+v want 4 remaining candidates", report)
	}
	remaining := 0
	for _, path := range paths {
		if _, statErr := os.Lstat(path); statErr == nil {
			remaining++
		} else if !errors.Is(statErr, os.ErrNotExist) {
			t.Fatal(statErr)
		}
	}
	if remaining != 4 {
		t.Fatalf("cancelled cleanup left %d candidates, want 4", remaining)
	}
	if current, reportErr := store.ReportRetention(retentionNow); reportErr != nil || current.CleanupCandidates != 4 {
		t.Fatalf("partial store is not reportable: report=%+v err=%v", current, reportErr)
	}
	retentionDeleteCheckpoint = nil
	if _, err := store.CleanupUnreachable(retentionNow); err != nil {
		t.Fatalf("resume partial cleanup: %v", err)
	}
}

func retentionCleanupCrashHelper(t *testing.T) {
	dataRoot := os.Getenv("SESSION_REVIEWER_RETENTION_DATA_ROOT")
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoints atomic.Int32
	retentionDeleteCheckpoint = func() error {
		if checkpoints.Add(1) == 2 {
			os.Exit(93)
		}
		return nil
	}
	_, _ = store.CleanupUnreachable(retentionNow)
	os.Exit(94)
}

func newRetentionStore(t *testing.T, generationID string) (string, *Store, storedFixture) {
	t.Helper()
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixture := buildStoredFixture(t, store, generationID)
	if _, err := store.PrepareGeneration(fixture.manifest); err != nil {
		t.Fatalf("prepare generation: %v", err)
	}
	return dataRoot, store, fixture
}

func retentionMemoryRoot(dataRoot string) string {
	return filepath.Join(dataRoot, "projects", testProjectID, "memory-v1")
}

func writeRetentionCandidate(t *testing.T, memoryRoot, namespace, suffix string, body []byte, modified time.Time) string {
	t.Helper()
	directory := filepath.Join(memoryRoot, namespace)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	path := filepath.Join(directory, hex.EncodeToString(sum[:])+suffix)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	return path
}

func reachableFixtureTotals(t *testing.T, memoryRoot string, manifest memory.GenerationManifest) (int, int64) {
	t.Helper()
	paths := []string{
		filepath.Join(memoryRoot, "generations", manifest.GenerationID+".json"),
		filepath.Join(memoryRoot, "observations", digestLeaf(manifest.ObservationChunkDigests[0], ".jsonl")),
		filepath.Join(memoryRoot, "sessions", digestLeaf(manifest.SessionViews[0].Digest, ".json")),
		filepath.Join(memoryRoot, "project-probes", digestLeaf(manifest.ProbeStateDigest, ".json")),
		filepath.Join(memoryRoot, "project-views", digestLeaf(manifest.ProjectViewDigest, ".json")),
	}
	var total int64
	for _, path := range paths {
		total += fileSize(t, path)
	}
	return len(paths), total
}

type retentionInventoryEntry struct {
	path       string
	size       int64
	mode       os.FileMode
	modified   time.Time
	contentSum string
}

func retentionInventory(t *testing.T, root string) []retentionInventoryEntry {
	t.Helper()
	var entries []retentionInventoryEntry
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		digest := ""
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(body)
			digest = hex.EncodeToString(sum[:])
		}
		relative, _ := filepath.Rel(root, path)
		entries = append(entries, retentionInventoryEntry{path: filepath.ToSlash(relative), size: info.Size(), mode: info.Mode(), modified: info.ModTime(), contentSum: digest})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return entries
}

func equalRetentionInventory(first, second []retentionInventoryEntry) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index].path != second[index].path || first[index].size != second[index].size || first[index].mode != second[index].mode || !first[index].modified.Equal(second[index].modified) || first[index].contentSum != second[index].contentSum {
			return false
		}
	}
	return true
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("expected path to remain %q: %v", path, err)
	}
}

func TestRetentionCandidateNamesArePortableAndContentAddressed(t *testing.T) {
	for _, body := range [][]byte{[]byte("cache"), []byte(strings.Repeat("x", 1024))} {
		sum := sha256.Sum256(body)
		name := hex.EncodeToString(sum[:]) + ".cache"
		if strings.ContainsAny(name, `/\\:`) || len(name) != 70 {
			t.Fatalf("nonportable canonical candidate name %q", name)
		}
		decoded, err := hex.DecodeString(strings.TrimSuffix(name, ".cache"))
		if err != nil || !bytes.Equal(decoded, sum[:]) {
			t.Fatalf("candidate name does not authenticate content: %q", name)
		}
	}
}
