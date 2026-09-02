package scanjob

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/neomei/SessionReviewer/internal/contextupdate"
	"github.com/neomei/SessionReviewer/internal/scan"
)

var (
	ErrJobAlreadyRunning = errors.New("a scan job is already running for this project")
	ErrNoActiveJob       = errors.New("no scan job found for project")
)

type StartOptions struct {
	ProjectID    string
	SessionsRoot string
	DataRoot     string
	Now          func() time.Time
	WorkerRunner func(jobID, dataRoot, projectID, sessionsRoot string) (int, error)
}

func Start(ctx context.Context, opts StartOptions) (PublicStatus, error) {
	if opts.ProjectID == "" {
		return PublicStatus{}, errors.New("project ID is required")
	}
	if !filepath.IsAbs(opts.DataRoot) || filepath.Clean(opts.DataRoot) != opts.DataRoot {
		return PublicStatus{}, errors.New("data root must be an absolute clean path")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	store, err := OpenStore(opts.DataRoot, opts.ProjectID)
	if err != nil {
		return PublicStatus{}, err
	}
	defer store.Close()

	active, err := store.LoadActiveJob()
	if err == nil {
		if active.State == StateQueued || active.State == StateRunning {
			if isProcessAlive(active.PID) {
				return toPublicStatus(active), ErrJobAlreadyRunning
			}
			active.State = StateFailed
			active.ErrorCode = "worker_crashed"
			active.ErrorMessage = "worker process terminated unexpectedly"
			active.UpdatedAt = now().UTC()
			_ = store.SaveJob(active)
		}
	}

	jobID := fmt.Sprintf("scan-%d", now().UTC().UnixNano())
	record := JobRecord{
		SchemaVersion: 1,
		JobID:         jobID,
		ProjectID:     opts.ProjectID,
		SessionsRoot:  opts.SessionsRoot,
		State:         StateQueued,
		Phase:         PhaseDiscovering,
		CreatedAt:     now().UTC(),
		UpdatedAt:     now().UTC(),
	}
	if err := store.SaveJob(record); err != nil {
		return PublicStatus{}, err
	}

	runner := opts.WorkerRunner
	if runner == nil {
		runner = defaultWorkerRunner
	}
	pid, err := runner(jobID, opts.DataRoot, opts.ProjectID, opts.SessionsRoot)
	if err != nil {
		record.State = StateFailed
		record.ErrorCode = "worker_spawn_failed"
		record.ErrorMessage = err.Error()
		record.UpdatedAt = now().UTC()
		_ = store.SaveJob(record)
		return toPublicStatus(record), fmt.Errorf("spawn worker: %w", err)
	}

	record.PID = pid
	record.State = StateRunning
	record.UpdatedAt = now().UTC()
	_ = store.SaveJob(record)
	return toPublicStatus(record), nil
}

func Status(ctx context.Context, dataRoot, projectID string) (PublicStatus, error) {
	if projectID == "" {
		return PublicStatus{}, errors.New("project ID is required")
	}
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return PublicStatus{}, errors.New("data root must be an absolute clean path")
	}
	store, err := OpenStore(dataRoot, projectID)
	if err != nil {
		return PublicStatus{}, err
	}
	defer store.Close()

	active, err := store.LoadActiveJob()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PublicStatus{SchemaVersion: 1, ProjectID: projectID, State: ""}, ErrNoActiveJob
		}
		return PublicStatus{}, err
	}
	if (active.State == StateQueued || active.State == StateRunning) && !isProcessAlive(active.PID) {
		active.State = StateFailed
		active.ErrorCode = "worker_crashed"
		active.ErrorMessage = "worker process is no longer running"
		active.UpdatedAt = time.Now().UTC()
		_ = store.SaveJob(active)
	}
	return toPublicStatus(active), nil
}

func RunWorker(ctx context.Context, dataRoot, projectID, jobID string) error {
	if projectID == "" || jobID == "" {
		return errors.New("project ID and job ID are required")
	}
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return errors.New("data root must be an absolute clean path")
	}
	store, err := OpenStore(dataRoot, projectID)
	if err != nil {
		return err
	}
	defer store.Close()

	record, err := store.LoadJob(jobID)
	if err != nil {
		return fmt.Errorf("load job: %w", err)
	}

	record.State = StateRunning
	record.PID = os.Getpid()
	record.UpdatedAt = time.Now().UTC()
	_ = store.SaveJob(record)

	phaseObserver := func(phase string) {
		record.Phase = Phase(phase)
		record.UpdatedAt = time.Now().UTC()
		_ = store.SaveJob(record)
	}

	cuOpts := contextupdate.Options{
		ProjectID:     projectID,
		SessionsRoot:  record.SessionsRoot,
		DataRoot:      dataRoot,
		Now:           time.Now,
		PhaseObserver: phaseObserver,
	}

	res, err := contextupdate.Run(ctx, cuOpts)
	if err != nil {
		record.State = StateFailed
		record.ErrorCode = "scan_failed"
		record.ErrorMessage = err.Error()
		record.UpdatedAt = time.Now().UTC()
		_ = store.SaveJob(record)
		return err
	}

	if res.State == scan.Completed {
		record.State = StateCompleted
	} else if res.State == scan.CompletedWithIssues {
		record.State = StateCompletedWithIssues
	} else {
		record.State = StateFailed
	}
	record.GenerationID = res.GenerationID
	record.SessionCount = res.SourceSessions
	record.IndexedCount = res.IndexedSessions
	record.IssueCount = res.IssueSessions
	record.UpdatedAt = time.Now().UTC()
	return store.SaveJob(record)
}

func toPublicStatus(j JobRecord) PublicStatus {
	return PublicStatus{
		SchemaVersion: 1,
		JobID:         j.JobID,
		ProjectID:     j.ProjectID,
		State:         string(j.State),
		Phase:         string(j.Phase),
		SessionCount:  j.SessionCount,
		IndexedCount:  j.IndexedCount,
		IssueCount:    j.IssueCount,
		GenerationID:  j.GenerationID,
		ErrorCode:     j.ErrorCode,
	}
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

func defaultWorkerRunner(jobID, dataRoot, projectID, sessionsRoot string) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(executable, "scan", "worker", "--job-id", jobID, "--data-dir", dataRoot, "--project-id", projectID)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}
