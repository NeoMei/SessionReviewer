package scanjob

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

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
		WorkerRunner: func(_, _, _, _ string) (int, error) { return 0, nil },
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
		WorkerRunner: func(_, _, _, _ string) (int, error) { return 0, nil },
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
		WorkerRunner: func(_, _, _, _ string) (int, error) { return 0, nil },
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
