package reviewjob

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

const maxLeaseOwnerBytes = 16 << 10

const interruptedLaunchGrace = 5 * time.Second

var ErrAgentBusy = errors.New(string(AgentBusy))

var currentProcessStartToken = newProcessStartToken()

type leaseOwner struct {
	SchemaVersion     int       `json:"schema_version"`
	JobID             string    `json:"job_id"`
	PID               int       `json:"pid"`
	ProcessStartToken string    `json:"process_start_token"`
	AcquiredAt        time.Time `json:"acquired_at"`
}

// RecoveryDisposition makes every no-op reason explicit so callers can
// distinguish a live/recent launch from an ownerless interrupted execution.
type RecoveryDisposition string

const (
	RecoveryNotInterrupted        RecoveryDisposition = "not_interrupted"
	RecoveryNotRecoverable        RecoveryDisposition = "not_recoverable"
	RecoveryApplyInspectionNeeded RecoveryDisposition = "apply_inspection_required"
)

// LeaseSet owns the per-project lease followed by the global Codex worker
// lease. Release always drops them in reverse acquisition order and is safe to
// call on nil, repeatedly, or concurrently.
type LeaseSet struct {
	mu      sync.Mutex
	project *storeFileLock
	global  *storeFileLock
	layout  *storeLayout
}

// AcquireLeases acquires the exact per-project leaf first and the global leaf
// second. Kernel advisory ownership is authoritative; persisted owner bytes
// are authenticated diagnostics written only after each kernel lock is held.
func (s Store) AcquireLeases(projectID, jobID string, timeout time.Duration) (_ *LeaseSet, retErr error) {
	if err := validateStoreID(projectID, "project"); err != nil {
		return nil, err
	}
	if err := validateStoreID(jobID, "job"); err != nil {
		return nil, err
	}
	if timeout < 0 {
		return nil, errors.New("review job lease timeout must not be negative")
	}
	layout, err := s.openLayout(true)
	if err != nil {
		return nil, err
	}
	return s.acquireLeasesOnLayout(layout, projectID, jobID, timeout)
}

func (s Store) acquireLeasesOnLayout(layout *storeLayout, projectID, jobID string, timeout time.Duration) (_ *LeaseSet, retErr error) {
	if layout == nil || layout.missing {
		if layout != nil {
			_ = layout.close()
		}
		return nil, os.ErrNotExist
	}
	if err := layout.verify(); err != nil {
		_ = layout.close()
		return nil, err
	}
	leases := &LeaseSet{layout: layout}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, leases.Release())
		}
	}()

	deadline := time.Now().Add(timeout)
	projectLease, err := acquirePrivateFileLock(layout.projectLocks, projectID+".lock", timeout)
	if err != nil {
		return nil, leaseAcquireError("project", err)
	}
	leases.project = projectLease

	owner := leaseOwner{
		SchemaVersion:     PublicStatusSchemaVersion,
		JobID:             jobID,
		PID:               os.Getpid(),
		ProcessStartToken: currentProcessStartToken,
		AcquiredAt:        time.Now().UTC(),
	}
	body, err := marshalCanonical(owner, maxLeaseOwnerBytes)
	if err != nil {
		return nil, err
	}
	if err := leases.project.replaceContent(body, maxLeaseOwnerBytes); err != nil {
		return nil, fmt.Errorf("persist project lease owner: %w", err)
	}

	remaining := time.Duration(0)
	if timeout > 0 && time.Now().Before(deadline) {
		remaining = time.Until(deadline)
	}
	globalLease, err := acquirePrivateFileLock(layout.locks, "global.lock", remaining)
	if err != nil {
		return nil, leaseAcquireError("global", err)
	}
	leases.global = globalLease
	if err := leases.global.replaceContent(body, maxLeaseOwnerBytes); err != nil {
		return nil, fmt.Errorf("persist global lease owner: %w", err)
	}
	if err := layout.verify(); err != nil {
		return nil, err
	}
	return leases, nil
}

// acquireJobLeases binds the optimistic job load, both worker leases, and the
// authoritative reload to one physical Store/Data layout.
func (s Store) acquireJobLeases(jobID string, timeout time.Duration) (_ Job, _ int, _ *LeaseSet, retErr error) {
	if err := validateStoreID(jobID, "job"); err != nil {
		return Job{}, 0, nil, err
	}
	if timeout < 0 {
		return Job{}, 0, nil, errors.New("review job lease timeout must not be negative")
	}
	layout, err := s.openLayout(false)
	if err != nil {
		return Job{}, 0, nil, err
	}
	if layout == nil || layout.missing {
		if layout != nil {
			_ = layout.close()
		}
		return Job{}, 0, nil, os.ErrNotExist
	}
	job, _, found, err := s.loadFromJobs(layout.jobs, jobID)
	if err != nil || !found {
		_ = layout.close()
		if err != nil {
			return Job{}, 0, nil, err
		}
		return Job{}, 0, nil, os.ErrNotExist
	}
	leases, err := s.acquireLeasesOnLayout(layout, job.ProjectID, job.ID, timeout)
	if err != nil {
		return Job{}, 0, nil, err
	}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, leases.Release())
		}
	}()
	if err := leases.verify(); err != nil {
		return Job{}, 0, nil, err
	}
	current, revision, found, err := s.loadFromJobs(leases.layout.jobs, jobID)
	if err != nil || !found {
		if err != nil {
			return Job{}, 0, nil, err
		}
		return Job{}, 0, nil, os.ErrNotExist
	}
	if current.ProjectID != job.ProjectID {
		return Job{}, 0, nil, errors.New("review job project changed while acquiring leases")
	}
	return current, revision, leases, nil
}

func (leases *LeaseSet) verify() error {
	if leases == nil {
		return errors.New("review worker leases are unavailable")
	}
	leases.mu.Lock()
	layout := leases.layout
	leases.mu.Unlock()
	if layout == nil {
		return errors.New("review worker leases are released")
	}
	return layout.verify()
}

func (leases *LeaseSet) update(store Store, jobID string, expectedRevision int, mutate func(*Job) error) (Job, int, error) {
	return leases.updateMode(store, jobID, expectedRevision, mutate, false)
}

func (leases *LeaseSet) updateTerminal(store Store, jobID string, expectedRevision int, mutate func(*Job) error) (Job, int, error) {
	return leases.updateMode(store, jobID, expectedRevision, mutate, true)
}

func (leases *LeaseSet) updateMode(store Store, jobID string, expectedRevision int, mutate func(*Job) error, allowDetached bool) (Job, int, error) {
	if err := validateStoreID(jobID, "job"); err != nil {
		return Job{}, 0, err
	}
	if expectedRevision < 1 || expectedRevision > maxSafeInteger || mutate == nil {
		return Job{}, 0, errors.New("valid expected revision and mutation are required")
	}
	leases.mu.Lock()
	layout := leases.layout
	leases.mu.Unlock()
	if layout == nil {
		return Job{}, 0, errors.New("review worker leases are released")
	}
	if allowDetached {
		return store.updatePinnedLayout(layout, jobID, expectedRevision, mutate)
	}
	return store.updateLayout(layout, jobID, expectedRevision, mutate)
}

func leaseAcquireError(kind string, err error) error {
	if errors.Is(err, errPrivateFileLocked) {
		return fmt.Errorf("%w: %s review worker lease is owned by a live process", ErrAgentBusy, kind)
	}
	return fmt.Errorf("acquire %s review worker lease: %w", kind, err)
}

func (leases *LeaseSet) Release() error {
	if leases == nil {
		return nil
	}
	leases.mu.Lock()
	defer leases.mu.Unlock()
	global, project, layout := leases.global, leases.project, leases.layout
	leases.global, leases.project, leases.layout = nil, nil, nil
	return errors.Join(global.release(), project.release(), layout.finish())
}

// RecoverInterrupted checks project kernel ownership before classifying any
// persisted active state. A recent queued/retrying launch intent receives one
// bounded parent/worker handshake grace window; every older ownerless active
// state is interrupted. It never decides whether an in-flight apply was accepted:
// E_APPLY_RECOVERY requires authoritative receipt inspection before resume.
func (s Store) RecoverInterrupted(jobID string) (_ Job, _ int, _ RecoveryDisposition, retErr error) {
	return s.RecoverInterruptedAt(jobID, time.Now().UTC())
}

// RecoverInterruptedAt is RecoverInterrupted with an explicit observation time
// so callers that own a canonical clock — for example the review CLI's
// injected reviewNow seam — evaluate the launch-intent grace window on the
// same timeline as their other persisted state transitions.
func (s Store) RecoverInterruptedAt(jobID string, observedAt time.Time) (_ Job, _ int, _ RecoveryDisposition, retErr error) {
	observedAt = observedAt.UTC().Round(0)
	if err := validateStoreID(jobID, "job"); err != nil {
		return Job{}, 0, "", err
	}
	layout, err := s.openLayout(false)
	if err != nil {
		return Job{}, 0, "", err
	}
	if layout == nil || layout.missing {
		if layout != nil {
			_ = layout.close()
		}
		return Job{}, 0, "", os.ErrNotExist
	}
	// Recovery is deliberately allowed to finish against the exact pinned Data
	// identity even if its external pathname is detached. Requiring a pathname
	// reopen here would either redirect the CAS to a replacement Store or turn a
	// safely pinned recovery into an ambiguous partial operation.
	defer func() { retErr = errors.Join(retErr, layout.close()) }()
	if err := layout.verifyPinned(); err != nil {
		return Job{}, 0, "", err
	}
	job, revision, found, err := s.loadFromJobs(layout.jobs, jobID)
	if err != nil || !found {
		if err != nil {
			return Job{}, 0, "", err
		}
		return Job{}, 0, "", os.ErrNotExist
	}
	if err := s.guardMutation(job); err != nil {
		return Job{}, 0, "", err
	}
	if err := layout.verifyPinned(); err != nil {
		return Job{}, 0, "", err
	}
	projectLease, err := acquirePrivateFileLock(layout.projectLocks, job.ProjectID+".lock", 0)
	if errors.Is(err, errPrivateFileLocked) {
		return job, revision, RecoveryNotInterrupted, nil
	}
	if err != nil {
		return Job{}, 0, "", fmt.Errorf("inspect interrupted review worker lease: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, projectLease.release()) }()
	if err := layout.verifyPinned(); err != nil {
		return Job{}, 0, "", err
	}

	// Reload after owning the project lease. Another recovery/start may have
	// completed between the optimistic read and this authoritative boundary.
	current, currentRevision, found, err := s.loadFromJobs(layout.jobs, jobID)
	if err != nil || !found {
		if err != nil {
			return Job{}, 0, "", err
		}
		return Job{}, 0, "", os.ErrNotExist
	}
	if current.ID != job.ID || current.ProjectID != job.ProjectID || current.ProjectIdentity != job.ProjectIdentity || currentRevision < revision {
		return Job{}, 0, "", errors.New("review job identity or revision changed while acquiring recovery lease")
	}
	if err := s.guardMutation(current); err != nil {
		return Job{}, 0, "", err
	}
	if err := layout.verifyPinned(); err != nil {
		return Job{}, 0, "", err
	}
	if !active(current.State) {
		return current, currentRevision, RecoveryNotRecoverable, nil
	}
	if !recoverableInterruptedState(current, observedAt) {
		if active(current.State) {
			return current, currentRevision, RecoveryNotInterrupted, nil
		}
		return current, currentRevision, RecoveryNotRecoverable, nil
	}
	completedAt := observedAt
	if completedAt.Before(current.UpdatedAt) {
		completedAt = current.UpdatedAt
	}
	updated, nextRevision, err := s.updatePinnedLayout(layout, jobID, currentRevision, func(job *Job) error {
		if !recoverableInterruptedState(*job, observedAt) {
			return ErrStaleRevision
		}
		phase := job.Phase
		job.State = Failed
		job.Phase = ""
		job.UpdatedAt = completedAt
		job.CompletedAt = completedAt
		job.Owner = Owner{}
		job.LaunchTokenDigest = ""
		job.LaunchIntentAt = time.Time{}
		job.Error = SafeError{Code: ApplyRecovery}
		job.PrivateError = "worker lease ended before a terminal state; apply receipt inspection is required"
		if phase == Applying && hasExactRetainedApplyPayloads(*job) {
			setPayloadLifecycle(job, PayloadApplyRecovery, PayloadCleanupAfterReceipt)
			job.PayloadRetainedFor = ApplyRecovery
		} else if job.PayloadState == PayloadPublishing || job.PayloadState == PayloadRetained || len(job.PayloadPublications) != 0 {
			// No apply boundary was durably entered. Keep the authenticated bytes
			// behind a terminal cleanup boundary so recovery can remove them by
			// digest and then persist cleanup-complete.
			setPayloadLifecycle(job, PayloadCleanupPending, PayloadCleanupByDigest)
			job.PayloadRetainedFor = ""
		}
		return nil
	})
	if err != nil {
		return Job{}, currentRevision, "", err
	}
	return updated, nextRevision, RecoveryApplyInspectionNeeded, nil
}

func recoverableInterruptedState(job Job, observedAt time.Time) bool {
	if !active(job.State) {
		return false
	}
	if protectedLaunchIntent(job, observedAt) {
		return false
	}
	return true
}

func protectedLaunchIntent(job Job, observedAt time.Time) bool {
	if !prefixedSHA256.MatchString(job.LaunchTokenDigest) || !canonicalTime(job.LaunchIntentAt) ||
		!observedAt.Before(job.LaunchIntentAt.Add(interruptedLaunchGrace)) {
		return false
	}
	if job.State == Queued || job.State == Retrying {
		return true
	}
	return job.State == CancelRequested && job.Attempt > 1 && job.Phase == Preflight &&
		job.Owner.ID == "" && !job.CancellationRequested.IsZero()
}

func newProcessStartToken() string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err == nil {
		return hex.EncodeToString(random)
	}
	fallback := sha256.Sum256([]byte(strconv.Itoa(os.Getpid()) + ":" + time.Now().UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(fallback[:16])
}
