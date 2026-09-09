package decisions

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/annotation"
)

func TestExtractionJobStoreUsesRevisionCASAndStableIdentity(t *testing.T) {
	root := privateDecisionTempDir(t)
	digest := "sha256:" + strings.Repeat("1", 64)
	now := time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC)
	job := ExtractionJob{SchemaVersion: 1, JobID: ExtractionIdentity("project-p", []string{digest}), ProjectID: "project-p", GenerationID: "generation-1", State: ExtractionQueued, Revision: 1, DependencyDigests: []string{digest}, CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	store, err := OpenExtractionJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(job); err != nil {
		t.Fatal(err)
	}
	job.State, job.PID, job.Revision, job.UpdatedAt = ExtractionRunning, 42, 2, now.Add(time.Second).Format(time.RFC3339Nano)
	if err := store.CompareAndSwap(job, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwap(job, 1); !errors.Is(err, ErrExtractionJobRevisionConflict) {
		t.Fatalf("stale update err=%v", err)
	}
	loaded, err := store.Load(job.JobID)
	if err != nil || loaded.Revision != 2 || loaded.PID != 42 {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	latest, err := store.Latest("project-p")
	if err != nil || latest == nil || latest.JobID != job.JobID {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
}

func TestExtractionJobStartIsIdempotentForSameNewDependencies(t *testing.T) {
	root := privateDecisionTempDir(t)
	digest := "sha256:" + strings.Repeat("1", 64)
	launches := 0
	now := time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC)
	opts := StartExtractionOptions{DataRoot: root, ProjectID: "project-p", GenerationID: "generation-1", NewDependencyDigests: []string{digest}, Now: func() time.Time { return now }, Launch: func(ExtractionJob) (int, error) { launches++; return 42, nil }}
	first, err := StartExtraction(opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := StartExtraction(opts)
	if err != nil || first.JobID != second.JobID || launches != 1 || second.State != ExtractionRunning {
		t.Fatalf("first=%+v second=%+v launches=%d err=%v", first, second, launches, err)
	}
}

func TestExtractionJobStartRetriesFailedAndCancelledAttempts(t *testing.T) {
	for _, terminal := range []ExtractionState{ExtractionFailed, ExtractionCancelled} {
		t.Run(string(terminal), func(t *testing.T) {
			root := privateDecisionTempDir(t)
			digest := "sha256:" + strings.Repeat("1", 64)
			now := time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC)
			store, err := OpenExtractionJobStore(root)
			if err != nil {
				t.Fatal(err)
			}
			job := ExtractionJob{SchemaVersion: 1, JobID: ExtractionIdentity("project-p", []string{digest}), ProjectID: "project-p", GenerationID: "generation-old", State: terminal, Revision: 3, DependencyDigests: []string{digest}, CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
			if terminal == ExtractionFailed {
				job.ErrorCode = "agent_failed"
			}
			if err := store.Create(job); err != nil {
				t.Fatal(err)
			}
			launches := 0
			got, err := StartExtraction(StartExtractionOptions{DataRoot: root, ProjectID: "project-p", GenerationID: "generation-new", NewDependencyDigests: []string{digest}, Now: func() time.Time { return now.Add(time.Second) }, Launch: func(queued ExtractionJob) (int, error) {
				launches++
				if queued.State != ExtractionQueued || queued.Revision != 4 || queued.GenerationID != "generation-new" {
					t.Fatalf("queued=%+v", queued)
				}
				return 42, nil
			}})
			if err != nil || launches != 1 || got.State != ExtractionRunning || got.Revision != 5 || got.GenerationID != "generation-new" || got.ErrorCode != "" {
				t.Fatalf("got=%+v launches=%d err=%v", got, launches, err)
			}
		})
	}
}

func TestExtractionJobStartWithNoNewDependenciesCompletesWithoutLaunch(t *testing.T) {
	root := privateDecisionTempDir(t)
	launches := 0
	job, err := StartExtraction(StartExtractionOptions{
		DataRoot:             root,
		ProjectID:            "project-p",
		GenerationID:         "generation-1",
		NewDependencyDigests: []string{},
		Now:                  func() time.Time { return time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC) },
		Launch: func(ExtractionJob) (int, error) {
			launches++
			return 42, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.State != ExtractionCompleted || job.DependencyDigests == nil || len(job.DependencyDigests) != 0 || launches != 0 {
		t.Fatalf("job=%+v launches=%d", job, launches)
	}
}

func TestExtractionJobCompletionSerializesCandidateCommitAgainstCancel(t *testing.T) {
	root := privateDecisionTempDir(t)
	now := time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC)
	job := runningExtractionJob(t, root, now)
	store, err := OpenExtractionJobStore(root)
	if err != nil {
		t.Fatal(err)
	}

	commitEntered := make(chan struct{})
	releaseCommit := make(chan struct{})
	var completeJob ExtractionJob
	var completeErr error
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		completeJob, completeErr = store.Complete(job.JobID, job.Revision, 1, now.Add(time.Second), func() error {
			close(commitEntered)
			<-releaseCommit
			return nil
		})
	}()
	<-commitEntered

	cancelDone := make(chan error, 1)
	go func() {
		_, cancelErr := store.Cancel(job.JobID, job.Revision, now.Add(2*time.Second))
		cancelDone <- cancelErr
	}()
	select {
	case err := <-cancelDone:
		t.Fatalf("cancel escaped completion lock: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseCommit)
	wait.Wait()
	if completeErr != nil || completeJob.State != ExtractionCompleted || completeJob.CandidateCount != 1 {
		t.Fatalf("complete=%+v err=%v", completeJob, completeErr)
	}
	if err := <-cancelDone; !errors.Is(err, ErrExtractionJobRevisionConflict) {
		t.Fatalf("cancel err=%v", err)
	}
}

func TestExtractionJobCancelPreventsCandidateCommit(t *testing.T) {
	root := privateDecisionTempDir(t)
	now := time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC)
	job := runningExtractionJob(t, root, now)
	store, err := OpenExtractionJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.Cancel(job.JobID, job.Revision, now.Add(time.Second))
	if err != nil || cancelled.State != ExtractionCancelled {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}
	committed := false
	_, err = store.Complete(job.JobID, job.Revision, 1, now.Add(2*time.Second), func() error {
		committed = true
		return nil
	})
	if !errors.Is(err, ErrExtractionJobRevisionConflict) || committed {
		t.Fatalf("complete err=%v committed=%t", err, committed)
	}
}

func TestExtractionJobCompletionCommitsCandidateStoreWithoutNestedLock(t *testing.T) {
	root := privateDecisionTempDir(t)
	now := time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC)
	job := runningExtractionJob(t, root, now)
	jobStore, err := OpenExtractionJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	candidateStore, err := OpenStore(root, job.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	run := annotation.Run{RunID: job.JobID, ProjectID: job.ProjectID, Status: "completed", ExtractorVersion: ExtractorVersion, PromptSchemaVersion: PromptSchemaVersion, DependencyDigests: append([]string{}, job.DependencyDigests...), CreatedAt: job.CreatedAt, UpdatedAt: now.Add(time.Second).Format(time.RFC3339Nano)}
	done := make(chan error, 1)
	go func() {
		_, err := jobStore.CompleteExtraction(job.JobID, job.Revision, candidateStore, run, []annotation.Annotation{}, now.Add(time.Second))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("candidate commit deadlocked under extraction job lock")
	}
}

func TestExtractionJobReconcilesCommittedCandidatesAfterProjectionWriteFailure(t *testing.T) {
	root := privateDecisionTempDir(t)
	now := time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC)
	job := runningExtractionJob(t, root, now)
	jobStore, err := OpenExtractionJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	candidateStore, err := OpenStore(root, job.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	run := annotation.Run{RunID: job.JobID, ProjectID: job.ProjectID, Status: "completed", ExtractorVersion: ExtractorVersion, PromptSchemaVersion: PromptSchemaVersion, DependencyDigests: append([]string{}, job.DependencyDigests...), CreatedAt: job.CreatedAt, UpdatedAt: now.Add(time.Second).Format(time.RFC3339Nano)}
	jobStore.writeHook = func(next ExtractionJob) error {
		if next.State == ExtractionCompleted {
			return errors.New("injected job projection failure")
		}
		return nil
	}
	if _, err := jobStore.CompleteExtraction(job.JobID, job.Revision, candidateStore, run, []annotation.Annotation{}, now.Add(time.Second)); !errors.Is(err, ErrExtractionJobProjectionPending) {
		t.Fatalf("complete err=%v", err)
	}
	committed, err := candidateStore.Load()
	if err != nil || len(committed.ExtractionRuns) != 1 || !SuccessfulExtractionDependencies(committed)[job.DependencyDigests[0]] {
		t.Fatalf("candidate commit=%+v err=%v", committed, err)
	}
	restarted, err := OpenExtractionJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := restarted.LoadReconciled(job.JobID, now.Add(2*time.Second))
	if err != nil || reconciled.State != ExtractionCompleted || reconciled.Revision != job.Revision+1 || reconciled.CandidateCount != 0 {
		t.Fatalf("reconciled=%+v err=%v", reconciled, err)
	}
}

func TestExtractionJobCancelReconcilesCommittedCandidateAuthority(t *testing.T) {
	root := privateDecisionTempDir(t)
	now := time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC)
	job := runningExtractionJob(t, root, now)
	jobStore, _ := OpenExtractionJobStore(root)
	candidateStore, _ := OpenStore(root, job.ProjectID)
	run := annotation.Run{RunID: job.JobID, ProjectID: job.ProjectID, Status: "completed", ExtractorVersion: ExtractorVersion, PromptSchemaVersion: PromptSchemaVersion, DependencyDigests: append([]string{}, job.DependencyDigests...), CreatedAt: job.CreatedAt, UpdatedAt: now.Add(time.Second).Format(time.RFC3339Nano)}
	if err := candidateStore.CommitExtraction(run, []annotation.Annotation{}); err != nil {
		t.Fatal(err)
	}
	if _, err := jobStore.Cancel(job.JobID, job.Revision, now.Add(2*time.Second)); !errors.Is(err, ErrExtractionJobRevisionConflict) {
		t.Fatalf("cancel after candidate authority err=%v", err)
	}
	loaded, err := jobStore.Load(job.JobID)
	if err != nil || loaded.State != ExtractionCompleted || loaded.Revision != job.Revision+1 {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestExtractionCandidateWriteFailureDoesNotAdvanceWatermark(t *testing.T) {
	root := privateDecisionTempDir(t)
	now := time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC)
	job := runningExtractionJob(t, root, now)
	jobStore, _ := OpenExtractionJobStore(root)
	candidateStore, _ := OpenStore(root, job.ProjectID)
	run := annotation.Run{RunID: job.JobID, ProjectID: job.ProjectID, Status: "failed", ExtractorVersion: ExtractorVersion, PromptSchemaVersion: PromptSchemaVersion, DependencyDigests: append([]string{}, job.DependencyDigests...), CreatedAt: job.CreatedAt, UpdatedAt: now.Add(time.Second).Format(time.RFC3339Nano)}
	if _, err := jobStore.CompleteExtraction(job.JobID, job.Revision, candidateStore, run, []annotation.Annotation{}, now.Add(time.Second)); err == nil {
		t.Fatal("invalid candidate commit succeeded")
	}
	record, err := candidateStore.Load()
	if err != nil || len(record.ExtractionRuns) != 0 || len(SuccessfulExtractionDependencies(record)) != 0 {
		t.Fatalf("failed commit advanced watermark: record=%+v err=%v", record, err)
	}
}

func runningExtractionJob(t *testing.T, root string, now time.Time) ExtractionJob {
	t.Helper()
	digest := "sha256:" + strings.Repeat("2", 64)
	store, err := OpenExtractionJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	job := ExtractionJob{SchemaVersion: 1, JobID: ExtractionIdentity("project-p", []string{digest}), ProjectID: "project-p", GenerationID: "generation-1", State: ExtractionQueued, Revision: 1, DependencyDigests: []string{digest}, CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	if err := store.Create(job); err != nil {
		t.Fatal(err)
	}
	job, err = store.AuthorizeWorker(job.JobID, 999999, job.Revision, now)
	if err != nil {
		t.Fatal(err)
	}
	return job
}
