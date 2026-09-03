package scanjob

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validStoredJob(projectID string) JobRecord {
	now := time.Now().UTC()
	return JobRecord{
		SchemaVersion: 1,
		JobID:         "scan-valid",
		ProjectID:     projectID,
		State:         StateRunning,
		Phase:         PhaseDiscovering,
		PID:           os.Getpid(),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

func TestStoreRejectsJobForAnotherProject(t *testing.T) {
	store, err := OpenStore(t.TempDir(), "project-one")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job := validStoredJob("project-two")
	if err := store.SaveJob(job); err == nil {
		t.Fatal("cross-project job was accepted")
	}
}

func TestStatusRejectsInvalidPersistedJobState(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "project-corrupt-state"
	store, err := OpenStore(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	job := validStoredJob(projectID)
	if err := store.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	activePath := filepath.Join(dataRoot, "projects", projectID, "jobs", activeJobLeaf)
	body, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), `"state": "running"`, `"state": "teleporting"`, 1))
	if err := os.WriteFile(activePath, body, jobFileMode); err != nil {
		t.Fatal(err)
	}

	if _, err := Status(context.Background(), dataRoot, projectID); err == nil {
		t.Fatal("invalid persisted state was exposed as public status")
	}
}

func TestStoreRejectsInvalidCountsAndTimeOrder(t *testing.T) {
	store, err := OpenStore(t.TempDir(), "project-invalid-scalars")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	negative := validStoredJob("project-invalid-scalars")
	negative.SessionCount = -1
	if err := store.SaveJob(negative); err == nil {
		t.Fatal("negative count was accepted")
	}
	reversed := validStoredJob("project-invalid-scalars")
	reversed.UpdatedAt = reversed.CreatedAt.Add(-time.Second)
	if err := store.SaveJob(reversed); err == nil {
		t.Fatal("reversed timestamps were accepted")
	}
}

func TestStoreRejectsInvalidOrOversizedWorkerErrors(t *testing.T) {
	store, err := OpenStore(t.TempDir(), "project-invalid-errors")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	job := validStoredJob("project-invalid-errors")
	job.State = StateFailed
	job.PID = 0
	job.ErrorCode = "BAD CODE"
	job.ErrorMessage = "failure"
	if err := store.SaveJob(job); err == nil {
		t.Fatal("unsafe error code was accepted")
	}
	job.ErrorCode = "scan_failed"
	job.ErrorMessage = strings.Repeat("x", maxErrorMessageBytes+1)
	if err := store.SaveJob(job); err == nil {
		t.Fatal("oversized error message was accepted")
	}
	job.ErrorMessage = ""
	if err := store.SaveJob(job); err == nil {
		t.Fatal("failed job without an error message was accepted")
	}
}
