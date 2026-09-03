package scanjob

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/project"
)

func saveTestJob(t *testing.T, dataRoot, projectID string, job JobRecord) {
	t.Helper()
	store, err := OpenStore(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveJob(job); err != nil {
		t.Fatal(err)
	}
}

func deadTestPID() int { return os.Getpid() + 1_000_000 }

func TestScanJobStartAndStatusLifecycle(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "project-scan-test"

	// Status when no job exists
	_, err := Status(context.Background(), dataRoot, projectID)
	if !errors.Is(err, ErrNoActiveJob) {
		t.Fatalf("expected ErrNoActiveJob, got %v", err)
	}

	runner := func(jobID, dataRoot, projectID, sessionsRoot string) (int, error) {
		return os.Getpid(), nil // Use current pid as alive process
	}

	status, err := Start(context.Background(), StartOptions{
		ProjectID:    projectID,
		DataRoot:     dataRoot,
		WorkerRunner: runner,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if status.ProjectID != projectID || status.State != string(StateRunning) {
		t.Fatalf("unexpected start status: %+v", status)
	}

	// Start while job is running fails
	_, err = Start(context.Background(), StartOptions{
		ProjectID:    projectID,
		DataRoot:     dataRoot,
		WorkerRunner: runner,
	})
	if !errors.Is(err, ErrJobAlreadyRunning) {
		t.Fatalf("expected ErrJobAlreadyRunning, got %v", err)
	}

	// Status returns running status
	curStatus, err := Status(context.Background(), dataRoot, projectID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if curStatus.JobID != status.JobID || curStatus.State != string(StateRunning) {
		t.Fatalf("unexpected status: %+v", curStatus)
	}
}

func TestStatusPreservesWorkerErrorWhenProcessDies(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "project-status-preserve-error"

	status, err := Start(context.Background(), StartOptions{
		ProjectID:    projectID,
		DataRoot:     dataRoot,
		WorkerRunner: func(_, _, _, _ string) (int, error) { return deadTestPID(), nil },
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	store, err := OpenStore(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.LoadJob(status.JobID)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	job.State = StateFailed
	job.ErrorCode = "scan_failed"
	job.ErrorMessage = "no sessions found under root"
	if err := store.SaveJob(job); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()

	cur, err := Status(context.Background(), dataRoot, projectID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if cur.State != string(StateFailed) || cur.ErrorCode != "scan_failed" || cur.ErrorMessage != "no sessions found under root" {
		t.Fatalf("preserved error overwritten: %+v", cur)
	}
	if !filepath.IsAbs(dataRoot) {
		t.Fatal("dataRoot should be absolute")
	}
}

func TestStartPreservesWorkerErrorWhenProcessDies(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "project-preserve-error"

	status, err := Start(context.Background(), StartOptions{
		ProjectID:    projectID,
		DataRoot:     dataRoot,
		WorkerRunner: func(_, _, _, _ string) (int, error) { return deadTestPID(), nil },
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	store, err := OpenStore(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.LoadJob(status.JobID)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	job.State = StateFailed
	job.ErrorCode = "project_not_found"
	job.ErrorMessage = "resolve project identity: project association requires explicit confirmation"
	if err := store.SaveJob(job); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()

	_, err = Start(context.Background(), StartOptions{
		ProjectID:    projectID,
		DataRoot:     dataRoot,
		WorkerRunner: func(_, _, _, _ string) (int, error) { return deadTestPID(), nil },
	})
	if err != nil {
		t.Fatalf("second Start should succeed: %v", err)
	}

	store, err = OpenStore(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	original, err := store.LoadJob(status.JobID)
	store.Close()
	if err != nil {
		t.Fatalf("load original job: %v", err)
	}
	if original.State != StateFailed || original.ErrorCode != "project_not_found" || original.ErrorMessage == "" {
		t.Fatalf("preserved error not kept in original record: %+v", original)
	}

	cur, err := Status(context.Background(), dataRoot, projectID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if cur.State != string(StateFailed) || cur.ErrorCode != "worker_crashed" {
		t.Fatalf("expected new job to report worker crash: %+v", cur)
	}
	if !filepath.IsAbs(dataRoot) {
		t.Fatal("dataRoot should be absolute")
	}
}

func TestStatusKeepsRecentlyQueuedJobPendingWhileWorkerLaunches(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "project-launch-pending"
	now := time.Now().UTC()
	saveTestJob(t, dataRoot, projectID, JobRecord{
		SchemaVersion: 1,
		JobID:         "scan-launch-pending",
		ProjectID:     projectID,
		State:         StateQueued,
		Phase:         PhaseDiscovering,
		CreatedAt:     now,
		UpdatedAt:     now,
	})

	status, err := Status(context.Background(), dataRoot, projectID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != string(StateQueued) {
		t.Fatalf("recent queued job was misclassified: %+v", status)
	}
}

func TestStatusDoesNotKeepFutureDatedQueuedJobPendingForever(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "project-future-launch"
	future := time.Now().UTC().Add(24 * time.Hour)
	saveTestJob(t, dataRoot, projectID, JobRecord{
		SchemaVersion: 1,
		JobID:         "scan-future-launch",
		ProjectID:     projectID,
		State:         StateQueued,
		Phase:         PhaseDiscovering,
		CreatedAt:     future,
		UpdatedAt:     future,
	})

	status, err := Status(context.Background(), dataRoot, projectID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != string(StateFailed) || status.ErrorCode != "worker_crashed" {
		t.Fatalf("future-dated queued job remained active: %+v", status)
	}
}

func TestStartDoesNotSpawnSecondWorkerDuringLaunchWindow(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "project-launch-exclusive"
	now := time.Now().UTC()
	saveTestJob(t, dataRoot, projectID, JobRecord{
		SchemaVersion: 1,
		JobID:         "scan-launch-exclusive",
		ProjectID:     projectID,
		State:         StateQueued,
		Phase:         PhaseDiscovering,
		CreatedAt:     now,
		UpdatedAt:     now,
	})

	spawned := false
	status, err := Start(context.Background(), StartOptions{
		ProjectID: projectID,
		DataRoot:  dataRoot,
		Now:       func() time.Time { return now },
		WorkerRunner: func(_, _, _, _ string) (int, error) {
			spawned = true
			return os.Getpid(), nil
		},
	})
	if !errors.Is(err, ErrJobAlreadyRunning) {
		t.Fatalf("expected ErrJobAlreadyRunning, got status=%+v err=%v", status, err)
	}
	if spawned {
		t.Fatal("a second worker was spawned while the first job was still launching")
	}
}

func TestRunWorkerRejectsUncommittedQueuedLaunch(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "project-worker-queued"
	now := time.Now().UTC()
	saveTestJob(t, dataRoot, projectID, JobRecord{
		SchemaVersion: 1,
		JobID:         "scan-worker-queued",
		ProjectID:     projectID,
		State:         StateQueued,
		Phase:         PhaseDiscovering,
		CreatedAt:     now,
		UpdatedAt:     now,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err := RunWorker(ctx, dataRoot, projectID, "scan-worker-queued")
	if err == nil || !strings.Contains(err.Error(), "authorize scan worker launch") {
		t.Fatalf("expected launch authorization error, got %v", err)
	}

	store, openErr := OpenStore(dataRoot, projectID)
	if openErr != nil {
		t.Fatal(openErr)
	}
	job, loadErr := store.LoadJob("scan-worker-queued")
	store.Close()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if job.State != StateQueued || job.PID != 0 {
		t.Fatalf("unauthorized worker mutated job: %+v", job)
	}
}

func TestRunWorkerRejectsPIDMismatchWithoutMutatingJob(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "project-worker-pid"
	now := time.Now().UTC()
	saveTestJob(t, dataRoot, projectID, JobRecord{
		SchemaVersion: 1,
		JobID:         "scan-worker-pid",
		ProjectID:     projectID,
		State:         StateRunning,
		Phase:         PhaseDiscovering,
		PID:           os.Getpid() + 100000,
		CreatedAt:     now,
		UpdatedAt:     now,
	})

	err := RunWorker(context.Background(), dataRoot, projectID, "scan-worker-pid")
	if err == nil || !strings.Contains(err.Error(), "authorize scan worker launch") {
		t.Fatalf("expected launch authorization error, got %v", err)
	}

	store, openErr := OpenStore(dataRoot, projectID)
	if openErr != nil {
		t.Fatal(openErr)
	}
	job, loadErr := store.LoadJob("scan-worker-pid")
	store.Close()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if job.State != StateRunning || job.PID == os.Getpid() {
		t.Fatalf("PID-mismatched worker mutated job: %+v", job)
	}
}

func TestStartHonorsCrossProcessControlLock(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "project-control-lock"
	store, err := OpenStore(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lock, err := project.AcquireProjectLock(store.jobs.Root, "control.lock", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	spawned := false
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err = Start(ctx, StartOptions{
		ProjectID: projectID,
		DataRoot:  dataRoot,
		WorkerRunner: func(_, _, _, _ string) (int, error) {
			spawned = true
			return os.Getpid(), nil
		},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected lock wait deadline, got %v", err)
	}
	if spawned {
		t.Fatal("worker spawned without owning the scan control lock")
	}
}

func TestStartCanceledContextDoesNotPersistOrSpawn(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "project-canceled-start"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	spawned := false
	_, err := Start(ctx, StartOptions{
		ProjectID: projectID,
		DataRoot:  dataRoot,
		WorkerRunner: func(_, _, _, _ string) (int, error) {
			spawned = true
			return os.Getpid(), nil
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if spawned {
		t.Fatal("worker spawned after context cancellation")
	}
	if _, statErr := os.Stat(filepath.Join(dataRoot, "projects", projectID, "jobs", activeJobLeaf)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canceled start persisted an active job: %v", statErr)
	}
}

func TestStartRejectsSuccessfulRunnerWithoutPID(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "project-invalid-worker-pid"
	status, err := Start(context.Background(), StartOptions{
		ProjectID: projectID,
		DataRoot:  dataRoot,
		WorkerRunner: func(_, _, _, _ string) (int, error) {
			return 0, nil
		},
	})
	if err == nil {
		t.Fatalf("invalid worker PID reported success: %+v", status)
	}
	if status.State != string(StateFailed) || status.ErrorCode != "worker_spawn_failed" {
		t.Fatalf("invalid worker PID was not terminalized: %+v err=%v", status, err)
	}
}
