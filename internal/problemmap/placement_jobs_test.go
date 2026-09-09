package problemmap

import (
	"errors"
	"os"
	"testing"
	"time"
)

const placementToken = "0123456789abcdef0123456789abcdef"

func TestPlacementJobsReserveBeforeRunCacheByDependenciesAndCancel(t *testing.T) {
	store, err := OpenPlacementJobStore(t.TempDir(), "project-a")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 1, 2, 3, 0, time.UTC)
	candidate := completeCandidate("candidate-a")
	first, cached, err := store.Reserve(candidate, 1, 2, "generation-1", placementToken, now)
	if err != nil || cached || first.State != PlacementQueued || first.Revision != 1 {
		t.Fatalf("first=%+v cached=%v err=%v", first, cached, err)
	}
	repeat, cached, err := store.Reserve(candidate, 1, 2, "generation-1", placementToken, now.Add(time.Second))
	if err != nil || !cached || repeat.JobID != first.JobID {
		t.Fatalf("repeat=%+v cached=%v err=%v", repeat, cached, err)
	}
	running, err := store.Claim(first.JobID, placementToken, now.Add(2*time.Second), time.Minute)
	if err != nil || running.State != PlacementRunning || running.LaunchTokenSHA256 != "" {
		t.Fatalf("running=%+v err=%v", running, err)
	}
	if _, err := store.Cancel(first.JobID, 1, now); !errors.Is(err, ErrCandidateRevisionConflict) {
		t.Fatalf("stale cancel=%v", err)
	}
	cancelRequested, err := store.Cancel(first.JobID, running.Revision, now.Add(3*time.Second))
	if err != nil || cancelRequested.State != PlacementCancelRequested {
		t.Fatalf("cancel requested=%+v err=%v", cancelRequested, err)
	}
	cancelled, err := store.Finish(first.JobID, cancelRequested.Revision, PlacementCompleted, 2, nil, now.Add(4*time.Second))
	if err != nil || cancelled.State != PlacementCancelled || cancelled.ErrorCode == nil {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}
	candidate.DependencyDigests = []string{"sha256:" + repeatHex("b")}
	candidate.Revision = 2
	changed, cached, err := store.Reserve(candidate, 2, 2, "generation-1", placementToken, now.Add(5*time.Second))
	if err != nil || cached || changed.JobID == first.JobID {
		t.Fatalf("changed=%+v cached=%v err=%v", changed, cached, err)
	}
}

func TestPlacementJobStatusIsProjectQualified(t *testing.T) {
	root := t.TempDir()
	a, _ := OpenPlacementJobStore(root, "project-a")
	b, _ := OpenPlacementJobStore(root, "project-b")
	candidate := completeCandidate("candidate-a")
	job, _, _ := a.Reserve(candidate, 1, 0, "generation-1", placementToken, time.Now().UTC())
	if _, err := b.Get(job.JobID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("foreign lookup err=%v", err)
	}
	status, err := a.Status(job.JobID)
	if err != nil || status.ProjectID != "project-a" || status.JobID != job.JobID || !status.CanCancel {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestPlacementJobFindsExactCompletedRetryAfterCandidateRevisionAdvances(t *testing.T) {
	root := t.TempDir()
	candidates, _ := OpenStore(root, "project-a")
	candidate := completeCandidate("candidate-a")
	if err := candidates.CompareAndSwap(candidate, 0); err != nil {
		t.Fatal(err)
	}
	jobs, _ := OpenPlacementJobStore(root, "project-a")
	job, _, err := jobs.Reserve(candidate, 1, 4, "generation-1", placementToken, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	job, _ = jobs.Claim(job.JobID, placementToken, time.Now().UTC(), time.Minute)
	result := candidate
	result.AnalysisMode, result.AgentRunID, result.Revision = AnalysisAgentRequested, &job.JobID, 2
	result.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := jobs.CompleteCandidate(job.JobID, job.Revision, candidates, result, 1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	retry, found, err := jobs.FindExact("candidate-a", 1, 4, "generation-1")
	if err != nil || !found || retry.JobID != job.JobID || retry.State != PlacementCompleted {
		t.Fatalf("retry=%+v found=%v err=%v", retry, found, err)
	}
	if _, found, err := jobs.FindExact("candidate-a", 2, 4, "generation-1"); err != nil || found {
		t.Fatalf("stale identity matched found=%v err=%v", found, err)
	}
}

func TestPlacementJobFindsActiveCandidateAfterViewReload(t *testing.T) {
	store, err := OpenPlacementJobStore(t.TempDir(), "project-a")
	if err != nil {
		t.Fatal(err)
	}
	candidate := completeCandidate("candidate-a")
	job, _, err := store.Reserve(candidate, 1, 4, "generation-1", placementToken, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	active, found, err := store.FindActiveCandidate(candidate.CandidateID)
	if err != nil || !found || active.JobID != job.JobID {
		t.Fatalf("active=%+v found=%v err=%v", active, found, err)
	}
	cancelled, err := store.Cancel(job.JobID, job.Revision, time.Now().UTC())
	if err != nil || cancelled.State != PlacementCancelled {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}
	if _, found, err := store.FindActiveCandidate(candidate.CandidateID); err != nil || found {
		t.Fatalf("terminal job remained active found=%v err=%v", found, err)
	}
}

func TestPlacementCompletionPublishesOnlyAgentCandidateAndBindsJob(t *testing.T) {
	root := t.TempDir()
	candidates, _ := OpenStore(root, "project-a")
	candidate := completeCandidate("candidate-a")
	if err := candidates.CompareAndSwap(candidate, 0); err != nil {
		t.Fatal(err)
	}
	jobs, _ := OpenPlacementJobStore(root, "project-a")
	job, _, err := jobs.Reserve(candidate, candidate.Revision, 4, "generation-1", placementToken, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	job, err = jobs.Claim(job.JobID, placementToken, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	result := candidate
	result.AnalysisMode, result.AgentRunID, result.Revision = AnalysisAgentRequested, &job.JobID, candidate.Revision+1
	result.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	completed, err := jobs.CompleteCandidate(job.JobID, job.Revision, candidates, result, candidate.Revision, time.Now().UTC())
	if err != nil || completed.State != PlacementCompleted || completed.ResultCandidateRevision != result.Revision {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	stored, err := candidates.Get(candidate.CandidateID)
	if err != nil || stored.AgentRunID == nil || *stored.AgentRunID != job.JobID {
		t.Fatalf("candidate=%+v err=%v", stored, err)
	}
}
