package scanjob

import (
	"context"
	"errors"
	"os"
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
