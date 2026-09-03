package scanjob

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/contextupdate"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/scan"
)

var (
	ErrJobAlreadyRunning = errors.New("a scan job is already running for this project")
	ErrNoActiveJob       = errors.New("no scan job found for project")
)

const (
	workerLaunchGrace   = 5 * time.Second
	workerLaunchPollGap = 10 * time.Millisecond
	controlLockMaxWait  = 2 * time.Second
	controlLockName     = "control.lock"
)

type StartOptions struct {
	ProjectID    string
	SessionsRoot string
	DataRoot     string
	Now          func() time.Time
	WorkerRunner func(jobID, dataRoot, projectID, sessionsRoot string) (int, error)
}

func Start(ctx context.Context, opts StartOptions) (status PublicStatus, retErr error) {
	if ctx == nil {
		return PublicStatus{}, errors.New("scan start context is required")
	}
	if err := ctx.Err(); err != nil {
		return PublicStatus{}, err
	}
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
	controlLock, err := acquireControlLock(ctx, store)
	if err != nil {
		return PublicStatus{}, err
	}
	defer func() {
		retErr = errors.Join(retErr, controlLock.Release())
	}()

	active, err := store.LoadActiveJob()
	if err == nil {
		if active.State == StateQueued || active.State == StateRunning {
			if jobMayStillBeActive(active, now().UTC()) {
				return toPublicStatus(active), ErrJobAlreadyRunning
			}
			markWorkerCrashed(&active, now().UTC(), "worker process terminated unexpectedly")
			if err := store.SaveJob(active); err != nil {
				return PublicStatus{}, fmt.Errorf("persist crashed scan job: %w", err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return PublicStatus{}, fmt.Errorf("load active scan job: %w", err)
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
	if err == nil && pid <= 0 {
		err = errors.New("worker returned an invalid PID")
	}
	if err != nil {
		record.State = StateFailed
		record.ErrorCode = "worker_spawn_failed"
		record.ErrorMessage = boundedErrorMessage(err.Error(), "worker spawn failed")
		record.UpdatedAt = now().UTC()
		if saveErr := store.SaveJob(record); saveErr != nil {
			return toPublicStatus(record), errors.Join(fmt.Errorf("spawn worker: %w", err), fmt.Errorf("persist worker spawn failure: %w", saveErr))
		}
		return toPublicStatus(record), fmt.Errorf("spawn worker: %w", err)
	}

	record.PID = pid
	record.State = StateRunning
	record.UpdatedAt = now().UTC()
	if err := store.SaveJob(record); err != nil {
		return toPublicStatus(record), fmt.Errorf("persist running scan job: %w", err)
	}
	return toPublicStatus(record), nil
}

func Status(ctx context.Context, dataRoot, projectID string) (status PublicStatus, retErr error) {
	if ctx == nil {
		return PublicStatus{}, errors.New("scan status context is required")
	}
	if err := ctx.Err(); err != nil {
		return PublicStatus{}, err
	}
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
	controlLock, err := acquireControlLock(ctx, store)
	if err != nil {
		return PublicStatus{}, err
	}
	defer func() {
		retErr = errors.Join(retErr, controlLock.Release())
	}()

	active, err := store.LoadActiveJob()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PublicStatus{SchemaVersion: 1, ProjectID: projectID, State: ""}, ErrNoActiveJob
		}
		return PublicStatus{}, err
	}
	if (active.State == StateQueued || active.State == StateRunning) && !jobMayStillBeActive(active, time.Now().UTC()) {
		markWorkerCrashed(&active, time.Now().UTC(), "worker process is no longer running")
		if err := store.SaveJob(active); err != nil {
			return toPublicStatus(active), fmt.Errorf("persist crashed scan job: %w", err)
		}
	}
	return toPublicStatus(active), nil
}

func markWorkerCrashed(job *JobRecord, observedAt time.Time, message string) {
	job.State = StateFailed
	if job.ErrorCode == "" {
		job.ErrorCode = "worker_crashed"
		job.ErrorMessage = boundedErrorMessage(message, "worker process terminated")
	}
	if job.CreatedAt.After(observedAt) {
		job.CreatedAt = observedAt
	}
	job.UpdatedAt = observedAt
}

func jobMayStillBeActive(job JobRecord, observedAt time.Time) bool {
	if job.State == StateQueued && job.PID == 0 {
		age := observedAt.Sub(job.UpdatedAt)
		return age >= -workerLaunchGrace && age <= workerLaunchGrace
	}
	return isProcessAlive(job.PID)
}

func acquireControlLock(ctx context.Context, store *Store) (*project.ProjectLock, error) {
	deadline := time.NewTimer(controlLockMaxWait)
	defer deadline.Stop()
	ticker := time.NewTicker(workerLaunchPollGap)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lock, err := project.AcquireProjectLock(store.jobs.Root, controlLockName, 0)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, project.ErrProjectLocked) {
			return nil, fmt.Errorf("acquire scan control lock: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("acquire scan control lock: %w", project.ErrProjectLocked)
		case <-ticker.C:
		}
	}
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

	record, err := authorizeWorkerLaunch(ctx, store, projectID, jobID, os.Getpid())
	if err != nil {
		return fmt.Errorf("authorize scan worker launch: %w", err)
	}

	phaseObserver := func(phase string) error {
		record.Phase = Phase(phase)
		record.UpdatedAt = time.Now().UTC()
		return store.SaveJob(record)
	}
	extractionObserver := func(progress scan.Progress) error {
		record.Phase = PhaseExtracting
		record.SessionCount = progress.SourceSessions
		record.IndexedCount = progress.IndexedSessions
		record.IssueCount = progress.IssueSessions
		record.UpdatedAt = time.Now().UTC()
		return store.SaveJob(record)
	}

	cuOpts := contextupdate.Options{
		ProjectID:          projectID,
		SessionsRoot:       record.SessionsRoot,
		DataRoot:           dataRoot,
		Now:                time.Now,
		PhaseObserver:      phaseObserver,
		ExtractionObserver: extractionObserver,
	}

	res, err := contextupdate.Run(ctx, cuOpts)
	if err != nil {
		record.State = StateFailed
		record.ErrorCode = "scan_failed"
		record.ErrorMessage = boundedErrorMessage(err.Error(), "scan failed")
		record.UpdatedAt = time.Now().UTC()
		if saveErr := store.SaveJob(record); saveErr != nil {
			return errors.Join(err, fmt.Errorf("persist failed scan job: %w", saveErr))
		}
		return err
	}

	if res.State == scan.Completed {
		record.State = StateCompleted
	} else if res.State == scan.CompletedWithIssues {
		record.State = StateCompletedWithIssues
	} else {
		record.State = StateFailed
		record.ErrorCode = "scan_failed"
		record.ErrorMessage = "scan returned an invalid terminal state"
	}
	record.GenerationID = res.GenerationID
	record.SessionCount = res.SourceSessions
	record.IndexedCount = res.IndexedSessions
	record.IssueCount = res.IssueSessions
	record.UpdatedAt = time.Now().UTC()
	return store.SaveJob(record)
}

func boundedErrorMessage(message, fallback string) string {
	if message == "" {
		message = fallback
	}
	if len(message) <= maxErrorMessageBytes {
		return message
	}
	message = message[:maxErrorMessageBytes]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	if message == "" {
		return fallback
	}
	return message
}

func authorizeWorkerLaunch(ctx context.Context, store *Store, projectID, jobID string, workerPID int) (JobRecord, error) {
	timeout := time.NewTimer(workerLaunchGrace)
	defer timeout.Stop()
	ticker := time.NewTicker(workerLaunchPollGap)
	defer ticker.Stop()

	for {
		record, err := store.LoadJob(jobID)
		if err != nil {
			return JobRecord{}, fmt.Errorf("load job: %w", err)
		}
		if record.ProjectID != projectID {
			return JobRecord{}, errors.New("job project does not match worker project")
		}

		switch record.State {
		case StateQueued:
			if record.PID != 0 {
				return JobRecord{}, fmt.Errorf("queued job has unexpected worker PID %d", record.PID)
			}
		case StateRunning:
			if record.PID != workerPID {
				return JobRecord{}, fmt.Errorf("worker PID %d does not match authorized PID %d", workerPID, record.PID)
			}
			active, err := store.LoadActiveJob()
			if err != nil {
				return JobRecord{}, fmt.Errorf("load active job: %w", err)
			}
			if active.JobID != jobID || active.ProjectID != projectID || active.State != StateRunning || active.PID != workerPID {
				return JobRecord{}, errors.New("job is not the active authorized worker")
			}
			return record, nil
		default:
			return JobRecord{}, fmt.Errorf("job is not launchable in state %q", record.State)
		}

		select {
		case <-ctx.Done():
			return JobRecord{}, ctx.Err()
		case <-timeout.C:
			return JobRecord{}, errors.New("timed out waiting for parent process authorization")
		case <-ticker.C:
		}
	}
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
		ErrorMessage:  j.ErrorMessage,
	}
}
