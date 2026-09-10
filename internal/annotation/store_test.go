package annotation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func TestCandidateStorePersistsImmutableRevisionsAndCASHead(t *testing.T) {
	dataRoot := privateStoreTempDir(t)
	store, err := OpenStore(dataRoot, "project-p")
	if err != nil {
		t.Fatal(err)
	}
	firstRecord := candidateStoreFixture("project-p")
	first, err := store.CompareAndSwap(context.Background(), 0, "", firstRecord)
	if err != nil || first.Revision != 1 || first.Digest == "" || !reflect.DeepEqual(first.Record, firstRecord) {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	firstPath := filepath.Join(dataRoot, "projects", "project-p", "annotations", "revisions", strings.TrimPrefix(first.Digest, "sha256:")+".json")
	firstBytes, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompareAndSwap(context.Background(), 0, "", firstRecord); !errors.Is(err, ErrCandidateRevisionConflict) {
		t.Fatalf("stale CAS err=%v", err)
	}
	nextRecord := cloneStoreFixture(firstRecord)
	nextRecord.Annotations[0].Status = CandidateIgnored
	nextRecord.Annotations[0].Revision++
	second, err := store.CompareAndSwap(context.Background(), first.Revision, first.Digest, nextRecord)
	if err != nil || second.Revision != 2 || second.Digest == first.Digest {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	after, err := os.ReadFile(firstPath)
	if err != nil || string(after) != string(firstBytes) {
		t.Fatalf("immutable first revision changed err=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenStoreReadOnly(dataRoot, "project-p")
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	loaded, err := readOnly.Load(context.Background())
	if err != nil || !reflect.DeepEqual(loaded, second) {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if _, err := readOnly.CompareAndSwap(context.Background(), second.Revision, second.Digest, nextRecord); !errors.Is(err, ErrStoreReadOnly) {
		t.Fatalf("read-only CAS err=%v", err)
	}
}

func TestCandidateStoreIdenticalCASIsNoOp(t *testing.T) {
	store, err := OpenStore(privateStoreTempDir(t), "project-p")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.CompareAndSwap(context.Background(), 0, "", candidateStoreFixture("project-p"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CompareAndSwap(context.Background(), first.Revision, first.Digest, first.Record)
	if err != nil || second.Revision != first.Revision || second.Digest != first.Digest {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestCandidateStoreCanonicalEquivalentCASIsNoOp(t *testing.T) {
	store, err := OpenStore(privateStoreTempDir(t), "project-p")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := candidateStoreFixture("project-p")
	record.Annotations[0].Dependencies = nil
	record.ExtractionRuns[0].DependencyDigests = nil
	first, err := store.CompareAndSwap(context.Background(), 0, "", record)
	if err != nil {
		t.Fatal(err)
	}
	equivalent := cloneStoreFixture(first.Record)
	equivalent.Annotations[0].Dependencies = nil
	equivalent.ExtractionRuns[0].DependencyDigests = nil
	second, err := store.CompareAndSwap(context.Background(), first.Revision, first.Digest, equivalent)
	if err != nil || !reflect.DeepEqual(second, first) {
		t.Fatalf("canonical no-op second=%+v first=%+v err=%v", second, first, err)
	}
}

func TestCandidateStoreReturnsCanonicalPersistedRecord(t *testing.T) {
	store, err := OpenStore(privateStoreTempDir(t), "project-p")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := StoreRecord{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: "project-p"}
	written, err := store.CompareAndSwap(context.Background(), 0, "", record)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if written.Record.Annotations == nil || written.Record.ExtractionRuns == nil || !reflect.DeepEqual(written, loaded) {
		t.Fatalf("CAS returned non-canonical state written=%+v loaded=%+v", written, loaded)
	}
}

func TestCandidateStoreConcurrentCASHasOneWinner(t *testing.T) {
	dataRoot := privateStoreTempDir(t)
	initialStore, err := OpenStore(dataRoot, "project-p")
	if err != nil {
		t.Fatal(err)
	}
	first, err := initialStore.CompareAndSwap(context.Background(), 0, "", candidateStoreFixture("project-p"))
	if err != nil {
		t.Fatal(err)
	}
	_ = initialStore.Close()
	left, _ := OpenStore(dataRoot, "project-p")
	right, _ := OpenStore(dataRoot, "project-p")
	defer left.Close()
	defer right.Close()
	values := []StoreRecord{cloneStoreFixture(first.Record), cloneStoreFixture(first.Record)}
	values[0].Annotations[0].Status, values[0].Annotations[0].Revision = CandidateIgnored, 2
	values[1].Annotations[0].Status, values[1].Annotations[0].Revision = CandidateStale, 2
	var wait sync.WaitGroup
	errs := make([]error, 2)
	wait.Add(2)
	for index := range values {
		go func(index int) {
			defer wait.Done()
			_, errs[index] = []*FileStore{left, right}[index].CompareAndSwap(context.Background(), first.Revision, first.Digest, values[index])
		}(index)
	}
	wait.Wait()
	winners := 0
	conflicts := 0
	for _, err := range errs {
		if err == nil {
			winners++
		} else if errors.Is(err, ErrCandidateRevisionConflict) {
			conflicts++
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("errs=%v", errs)
	}
}

func TestCandidateStoreRejectsImmutableCandidateMutation(t *testing.T) {
	store, err := OpenStore(privateStoreTempDir(t), "project-p")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.CompareAndSwap(context.Background(), 0, "", candidateStoreFixture("project-p"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Annotation){
		func(value *Annotation) { value.Text = "rewritten evidence" },
		func(value *Annotation) { value.Dependencies[0].Digest = testStoreDigest("9") },
		func(value *Annotation) { value.Revision += 2 },
	} {
		next := cloneStoreFixture(first.Record)
		mutate(&next.Annotations[0])
		if _, err := store.CompareAndSwap(context.Background(), first.Revision, first.Digest, next); err == nil {
			t.Fatal("immutable candidate mutation was accepted")
		}
	}
}

func TestCandidateStoreLifecycleTable(t *testing.T) {
	tests := []struct {
		name   string
		before CandidateStatus
		after  CandidateStatus
		valid  bool
	}{
		{"pending confirmed", CandidatePending, CandidateConfirmed, true},
		{"pending ignored", CandidatePending, CandidateIgnored, true},
		{"pending not decision", CandidatePending, CandidateNotDecision, true},
		{"pending stale", CandidatePending, CandidateStale, true},
		{"ignored pending", CandidateIgnored, CandidatePending, true},
		{"ignored stale", CandidateIgnored, CandidateStale, true},
		{"confirmed ignored", CandidateConfirmed, CandidateIgnored, false},
		{"stale pending", CandidateStale, CandidatePending, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenStore(privateStoreTempDir(t), "project-p")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			record := candidateStoreFixture("project-p")
			if test.before != CandidatePending {
				record.Annotations[0].Status = test.before
				record.Annotations[0].Revision = 2
				if test.before == CandidateConfirmed {
					confirmed := "decision-confirmed"
					record.Annotations[0].ConfirmedEntityID = &confirmed
				}
			}
			// Seed through the canonical first revision, then use the real CAS
			// transition for every non-pending starting state.
			seed := candidateStoreFixture("project-p")
			first, err := store.CompareAndSwap(context.Background(), 0, "", seed)
			if err != nil {
				t.Fatal(err)
			}
			current := first
			if test.before != CandidatePending {
				current, err = store.CompareAndSwap(context.Background(), first.Revision, first.Digest, record)
				if err != nil {
					t.Fatal(err)
				}
			}
			next := cloneStoreFixture(current.Record)
			next.Annotations[0].Status = test.after
			next.Annotations[0].Revision++
			if test.after == CandidateConfirmed {
				confirmed := "decision-confirmed"
				next.Annotations[0].ConfirmedEntityID = &confirmed
			} else {
				next.Annotations[0].ConfirmedEntityID = nil
			}
			_, err = store.CompareAndSwap(context.Background(), current.Revision, current.Digest, next)
			if test.valid && err != nil {
				t.Fatalf("valid transition rejected: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid transition accepted")
			}
		})
	}
}

func TestCandidateStoreRejectsCorruptHeadWithoutRepair(t *testing.T) {
	dataRoot := privateStoreTempDir(t)
	store, err := OpenStore(dataRoot, "project-p")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CompareAndSwap(context.Background(), 0, "", candidateStoreFixture("project-p")); err != nil {
		t.Fatal(err)
	}
	headPath := filepath.Join(dataRoot, "projects", "project-p", "annotations", "head.json")
	corrupt := []byte(`{"schema_version":1,"project_id":"project-p","revision":1,"digest":"` + testStoreDigest("1") + `","unknown":true}`)
	if err := os.WriteFile(headPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err == nil {
		t.Fatal("corrupt head was accepted")
	}
	after, err := os.ReadFile(headPath)
	if err != nil || !reflect.DeepEqual(after, corrupt) {
		t.Fatalf("corrupt head was repaired err=%v", err)
	}
}

func TestCandidateStoreCancellationBeforeHeadLeavesOldRevision(t *testing.T) {
	store, err := OpenStore(privateStoreTempDir(t), "project-p")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.CompareAndSwap(context.Background(), 0, "", candidateStoreFixture("project-p"))
	if err != nil {
		t.Fatal(err)
	}
	next := cloneStoreFixture(first.Record)
	next.Annotations[0].Status, next.Annotations[0].Revision = CandidateIgnored, 2
	ctx, cancel := context.WithCancel(context.Background())
	store.testHooks.afterImmutableWrite = func() error { cancel(); return nil }
	if _, err := store.CompareAndSwap(ctx, first.Revision, first.Digest, next); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
	store.testHooks.afterImmutableWrite = nil
	loaded, err := store.Load(context.Background())
	if err != nil || loaded.Revision != first.Revision || loaded.Digest != first.Digest {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestCandidateStoreCancellationAtFinalHeadCheckpointLeavesOldRevision(t *testing.T) {
	store, err := OpenStore(privateStoreTempDir(t), "project-p")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.CompareAndSwap(context.Background(), 0, "", candidateStoreFixture("project-p"))
	if err != nil {
		t.Fatal(err)
	}
	next := cloneStoreFixture(first.Record)
	next.Annotations[0].Status, next.Annotations[0].Revision = CandidateIgnored, 2
	ctx, cancel := context.WithCancel(context.Background())
	store.testHooks.beforeHeadWrite = func() error { cancel(); return nil }
	if _, err := store.CompareAndSwap(ctx, first.Revision, first.Digest, next); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
	store.testHooks.beforeHeadWrite = nil
	loaded, err := store.Load(context.Background())
	if err != nil || loaded.Revision != first.Revision || loaded.Digest != first.Digest {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestCandidateStorePreservesRunsAndRejectsCandidatesFromFailedRuns(t *testing.T) {
	store, err := OpenStore(privateStoreTempDir(t), "project-p")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.CompareAndSwap(context.Background(), 0, "", candidateStoreFixture("project-p"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*StoreRecord){
		func(value *StoreRecord) { value.ExtractionRuns = nil },
		func(value *StoreRecord) { value.ExtractionRuns[0].DependencyDigests[0] = testStoreDigest("8") },
		func(value *StoreRecord) {
			value.ExtractionRuns[0].Status = "failed"
			value.ExtractionRuns[0].UpdatedAt = "2026-09-09T00:02:00Z"
		},
	} {
		next := cloneStoreFixture(first.Record)
		mutate(&next)
		if _, err := store.CompareAndSwap(context.Background(), first.Revision, first.Digest, next); err == nil {
			t.Fatal("invalid extraction run transition was accepted")
		}
	}
}

func TestCandidateStoreRejectsExtractionRunTimestampRegression(t *testing.T) {
	store, err := OpenStore(privateStoreTempDir(t), "project-p")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := candidateStoreFixture("project-p")
	record.Annotations = nil
	record.ExtractionRuns[0].Status = "running"
	first, err := store.CompareAndSwap(context.Background(), 0, "", record)
	if err != nil {
		t.Fatal(err)
	}
	next := cloneStoreFixture(first.Record)
	next.ExtractionRuns[0].Status = "completed"
	next.ExtractionRuns[0].UpdatedAt = next.ExtractionRuns[0].CreatedAt
	if _, err := store.CompareAndSwap(context.Background(), first.Revision, first.Digest, next); err == nil {
		t.Fatal("extraction run timestamp regression was accepted")
	}
}

func TestCandidateStoreLoadRejectsSemanticallyInvalidCanonicalRecord(t *testing.T) {
	dataRoot := privateStoreTempDir(t)
	store, err := OpenStore(dataRoot, "project-p")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := candidateStoreFixture("project-p")
	record.ExtractionRuns[0].Status = "running"
	body, err := Render(record)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	revisionsPath := filepath.Join(dataRoot, "projects", "project-p", "annotations", "revisions")
	if err := os.WriteFile(filepath.Join(revisionsPath, strings.TrimPrefix(digest, "sha256:")+".json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	head := []byte(`{"schema_version":1,"project_id":"project-p","revision":1,"digest":"` + digest + `"}` + "\n")
	if err := os.WriteFile(filepath.Join(dataRoot, "projects", "project-p", "annotations", "head.json"), head, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err == nil {
		t.Fatal("semantically invalid candidate record was accepted")
	}
}

func TestCandidateStoreReadOnlyMissingDoesNotMutateFilesystem(t *testing.T) {
	dataRoot := privateStoreTempDir(t)
	before, err := snapshotStoreTree(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStoreReadOnly(dataRoot, "project-p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("load err=%v", err)
	}
	_ = store.Close()
	after, err := snapshotStoreTree(dataRoot)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("read-only open mutated tree before=%v after=%v err=%v", before, after, err)
	}
}

func TestCandidateStoreReadOnlyLoadDoesNotCreateLockOrChangeFiles(t *testing.T) {
	dataRoot := privateStoreTempDir(t)
	writer, err := OpenStore(dataRoot, "project-p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompareAndSwap(context.Background(), 0, "", candidateStoreFixture("project-p")); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	lockPath := filepath.Join(dataRoot, "projects", "project-p", "annotations", "store.lock")
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	before, err := snapshotStoreTree(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := OpenStoreReadOnly(dataRoot, "project-p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	after, err := snapshotStoreTree(dataRoot)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("read-only load mutated tree before=%v after=%v err=%v", before, after, err)
	}
	if _, err := os.Lstat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only load created lock: %v", err)
	}
}

func TestCandidateStoreRejectsRedirectedNamespace(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink setup is platform-specific")
	}
	dataRoot := privateStoreTempDir(t)
	external := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataRoot, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(dataRoot, "projects", "project-p")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dataRoot, "project-p"); err == nil {
		t.Fatal("redirected project namespace accepted")
	}
}

func TestCandidateStoreRejectsNonPrivateExistingParentWithoutRepair(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix permission contract")
	}
	dataRoot := t.TempDir()
	projectPath := filepath.Join(dataRoot, "projects", "project-p")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dataRoot, "project-p"); err == nil {
		t.Fatal("non-private existing project namespace accepted")
	}
	info, err := os.Stat(projectPath)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("existing parent was silently repaired mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestCandidateStoreRejectsNonPrivateDataRootWithoutRepair(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix permission contract")
	}
	dataRoot := t.TempDir()
	if err := os.Chmod(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dataRoot, "project-p"); err == nil {
		t.Fatal("non-private data root was accepted")
	}
	info, err := os.Stat(dataRoot)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("data root was silently repaired mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestCandidateStoreReadOnlyRejectsNonPrivateAncestorWithoutRepair(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix permission contract")
	}
	dataRoot := privateStoreTempDir(t)
	writer, err := OpenStore(dataRoot, "project-p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompareAndSwap(context.Background(), 0, "", candidateStoreFixture("project-p")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(dataRoot, "projects", "project-p")
	if err := os.Chmod(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStoreReadOnly(dataRoot, "project-p"); err == nil {
		t.Fatal("read-only store accepted a non-private project ancestor")
	}
	info, err := os.Stat(projectPath)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("read-only open repaired ancestor mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestCandidateStoreFailureAfterImmutableWriteLeavesOldHeadAuthoritative(t *testing.T) {
	store, err := OpenStore(privateStoreTempDir(t), "project-p")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.CompareAndSwap(context.Background(), 0, "", candidateStoreFixture("project-p"))
	if err != nil {
		t.Fatal(err)
	}
	next := cloneStoreFixture(first.Record)
	next.Annotations[0].Status, next.Annotations[0].Revision = CandidateIgnored, 2
	store.testHooks.afterImmutableWrite = func() error { return errors.New("injected stop") }
	if _, err := store.CompareAndSwap(context.Background(), first.Revision, first.Digest, next); err == nil {
		t.Fatal("injected failure was ignored")
	}
	store.testHooks.afterImmutableWrite = nil
	loaded, err := store.Load(context.Background())
	if err != nil || loaded.Revision != first.Revision || loaded.Digest != first.Digest {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestCandidateStoreRejectsNamespaceReplacementAfterOpen(t *testing.T) {
	dataRoot := privateStoreTempDir(t)
	store, err := OpenStore(dataRoot, "project-p")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.CompareAndSwap(context.Background(), 0, "", candidateStoreFixture("project-p"))
	if err != nil {
		t.Fatal(err)
	}
	next := cloneStoreFixture(first.Record)
	next.Annotations[0].Status, next.Annotations[0].Revision = CandidateIgnored, 2
	annotationPath := filepath.Join(dataRoot, "projects", "project-p", "annotations")
	headPath := filepath.Join(annotationPath, "head.json")
	oldHead, err := os.ReadFile(headPath)
	if err != nil {
		t.Fatal(err)
	}
	var renameErr error
	replacementCreated := false
	store.testHooks.afterImmutableWrite = func() error {
		renameErr = os.Rename(annotationPath, annotationPath+"-replaced")
		if renameErr != nil {
			return renameErr
		}
		if err := os.Mkdir(annotationPath, 0o700); err != nil {
			return err
		}
		replacementCreated = true
		return nil
	}
	if _, err := store.CompareAndSwap(context.Background(), first.Revision, first.Digest, next); err == nil {
		t.Fatal("namespace replacement after open was reported as a successful CAS")
	}
	if renameErr != nil {
		// Windows can prevent renaming an open pinned directory. That rejection
		// must leave the original head authoritative, with no replacement path.
		const windowsSharingViolation = syscall.Errno(32)
		if runtime.GOOS != "windows" || (!errors.Is(renameErr, os.ErrPermission) && !errors.Is(renameErr, windowsSharingViolation)) {
			t.Fatalf("unexpected namespace replacement failure: %v", renameErr)
		}
		if _, err := os.Stat(annotationPath + "-replaced"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("blocked rename created replacement path: %v", err)
		}
		currentHead, err := os.ReadFile(headPath)
		if err != nil || string(currentHead) != string(oldHead) {
			t.Fatalf("blocked namespace replacement changed head: %v", err)
		}
		store.testHooks.afterImmutableWrite = nil
		loaded, err := store.Load(context.Background())
		if err != nil || loaded.Revision != first.Revision || loaded.Digest != first.Digest {
			t.Fatalf("original head lost authority: loaded=%+v err=%v", loaded, err)
		}
		return
	}
	if !replacementCreated {
		t.Fatal("replacement namespace was not created")
	}
	if _, err := os.Stat(filepath.Join(annotationPath, "head.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement namespace was mutated: %v", err)
	}
}

func TestCandidateStoreRejectsOversizedCanonicalRecordBeforePublishingHead(t *testing.T) {
	dataRoot := privateStoreTempDir(t)
	store, err := OpenStore(dataRoot, "project-p")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := candidateStoreFixture("project-p")
	template := record.Annotations[0]
	template.Text = strings.Repeat("x", 4096)
	record.Annotations = make([]Annotation, 16_000)
	for index := range record.Annotations {
		candidate := template
		candidate.ID = fmt.Sprintf("candidate-%05d", index)
		record.Annotations[index] = candidate
	}
	if _, err := store.CompareAndSwap(context.Background(), 0, "", record); err == nil {
		t.Fatal("oversized canonical record was published")
	}
	headPath := filepath.Join(dataRoot, "projects", "project-p", "annotations", "head.json")
	if _, err := os.Lstat(headPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized record changed head: %v", err)
	}
}

func candidateStoreFixture(projectID string) StoreRecord {
	run := Run{RunID: "run-1", ProjectID: projectID, Status: "completed", ExtractorVersion: "extract-v1", PromptSchemaVersion: "prompt-v1", DependencyDigests: []string{testStoreDigest("1")}, CreatedAt: "2026-09-09T00:00:00Z", UpdatedAt: "2026-09-09T00:01:00Z"}
	entity, field := "decision-1", "decision"
	candidate := Annotation{ID: "candidate-1", ProjectID: projectID, AnnotationKind: "decision_candidate", EntityID: &entity, Field: &field, Status: CandidatePending, Text: "bounded proposal", GenerationID: "generation-1", SchemaVersion: 1, AnalysisProfile: "extract-v1", AgentRunID: run.RunID, Dependencies: []Dependency{{Kind: "session_view", RevisionID: "view-1", Digest: testStoreDigest("1")}}, Revision: 1, CreatedAt: "2026-09-09T00:01:00Z"}
	return StoreRecord{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: projectID, Annotations: []Annotation{candidate}, ExtractionRuns: []Run{run}}
}

func cloneStoreFixture(value StoreRecord) StoreRecord {
	body, err := Render(value)
	if err != nil {
		panic(err)
	}
	parsed, err := Parse(body)
	if err != nil {
		panic(err)
	}
	return parsed
}

func testStoreDigest(seed string) string { return "sha256:" + strings.Repeat(seed, 64) }

func privateStoreTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func snapshotStoreTree(root string) ([]string, error) {
	values := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		values = append(values, relative)
		return nil
	})
	return values, err
}
