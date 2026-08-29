package reviewjob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/proposal"
)

const maxRecoveryTransitionRetries = 64

type cancellationWatchResult struct {
	job      Job
	revision int
	changed  bool
	err      error
}

// RetryRequest binds one user intent to the exact failed job revision it was
// rendered from. RequestID makes transport retries idempotent without treating
// every later click on an active attempt as the same request.
type RetryRequest struct {
	JobID             string
	ExpectedAttempt   int
	ExpectedRevision  int
	RequestID         string
	At                time.Time
	LaunchTokenDigest string
	LaunchIntentAt    time.Time
}

// RequestRetry performs the sole failed-to-next-attempt transition. Concurrent
// clicks converge on one incremented attempt; a durable cancellation request is
// preserved, and accepted progress, accounting, frozen boundaries, and
// authenticated payload state are never rewritten.
func RequestRetry(store Store, request RetryRequest) (Job, int, error) {
	if !validID(request.JobID) || !validID(request.RequestID) || request.ExpectedAttempt <= 0 ||
		request.ExpectedAttempt >= maxSafeInteger || request.ExpectedRevision <= 0 || request.ExpectedRevision > maxSafeInteger || request.At.IsZero() {
		return Job{}, 0, errors.New("retry request is invalid")
	}
	if (request.LaunchTokenDigest == "") != request.LaunchIntentAt.IsZero() ||
		(request.LaunchTokenDigest != "" && !prefixedSHA256.MatchString(request.LaunchTokenDigest)) {
		return Job{}, 0, errors.New("retry launch intent is invalid")
	}
	at := request.At.UTC()
	launchAt := request.LaunchIntentAt.UTC()
	for range maxRecoveryTransitionRetries {
		job, revision, found, err := store.Load(request.JobID)
		if err != nil || !found {
			if err != nil {
				return Job{}, 0, err
			}
			return Job{}, 0, os.ErrNotExist
		}
		if job.RetryRequestID == request.RequestID {
			if job.RetryAttempt != request.ExpectedAttempt || job.RetryRevision != request.ExpectedRevision {
				return Job{}, revision, errors.New("retry request ID was reused with different expectations")
			}
			return job, revision, nil
		}
		if job.Attempt != request.ExpectedAttempt || revision != request.ExpectedRevision {
			return Job{}, revision, ErrStaleRevision
		}
		if job.State != Failed {
			return Job{}, revision, errors.New("review job is not retryable")
		}
		if job.Attempt == maxSafeInteger {
			return Job{}, revision, errors.New("review job attempt count is exhausted")
		}
		updatedAt := at
		if updatedAt.Before(job.UpdatedAt) {
			updatedAt = job.UpdatedAt
		}
		next, nextRevision, err := store.Update(request.JobID, revision, func(next *Job) error {
			if next.State != Failed || next.Attempt != request.ExpectedAttempt || next.RetryRequestID == request.RequestID {
				return ErrStaleRevision
			}
			next.State = Retrying
			if !next.CancellationRequested.IsZero() {
				next.State = CancelRequested
			}
			next.Phase = Preflight
			next.Attempt++
			next.RetryRequestID = request.RequestID
			next.RetryAttempt = request.ExpectedAttempt
			next.RetryRevision = request.ExpectedRevision
			next.LaunchTokenDigest = request.LaunchTokenDigest
			next.LaunchIntentAt = launchAt
			next.UpdatedAt = updatedAt
			next.CompletedAt = time.Time{}
			next.Owner = Owner{}
			next.Error = SafeError{}
			next.PrivateError = ""
			return nil
		})
		if errors.Is(err, ErrStaleRevision) {
			continue
		}
		return next, nextRevision, err
	}
	return Job{}, 0, errors.New("retry transition did not converge")
}

// RequestCancel persists a cancellation request without weakening commit
// windows. A queued job with no accepted work can terminalize immediately;
// live workers observe cancel_requested through their pinned Store layout.
func RequestCancel(store Store, jobID string, at time.Time) (Job, int, error) {
	if at.IsZero() {
		return Job{}, 0, errors.New("cancellation time is required")
	}
	at = at.UTC()
	for range maxRecoveryTransitionRetries {
		job, revision, found, err := store.Load(jobID)
		if err != nil || !found {
			if err != nil {
				return Job{}, 0, err
			}
			return Job{}, 0, os.ErrNotExist
		}
		if job.State == CancelRequested || job.State == Cancelled {
			return job, revision, nil
		}
		if !active(job.State) {
			return Job{}, revision, errors.New("review job is not cancellable")
		}
		updatedAt := at
		if updatedAt.Before(job.UpdatedAt) {
			updatedAt = job.UpdatedAt
		}
		next, nextRevision, err := store.Update(jobID, revision, func(next *Job) error {
			if !active(next.State) || next.State == CancelRequested {
				return ErrStaleRevision
			}
			next.CancellationRequested = updatedAt
			next.UpdatedAt = updatedAt
			if next.State == Queued && !next.AcceptedSyncPending && next.PayloadState == "" {
				next.State = Cancelled
				next.Phase = ""
				next.CompletedAt = updatedAt
				next.Owner = Owner{}
				next.Error = SafeError{Code: AgentCancelled}
				next.PrivateError = "review cancelled before worker start"
				next.LaunchTokenDigest = ""
				next.LaunchIntentAt = time.Time{}
				return nil
			}
			next.State = CancelRequested
			return nil
		})
		if errors.Is(err, ErrStaleRevision) {
			continue
		}
		return next, nextRevision, err
	}
	return Job{}, 0, errors.New("cancellation transition did not converge")
}

// recoverRetryState completes every durable boundary from the failed attempt
// before Prepare can observe the accepted cursor. Exact retained bytes are
// reused only for receipt resolution; all other authenticated payloads are
// cleaned as stale before new evidence is prepared.
func (runner *worker) recoverRetryState(ctx context.Context) error {
	if err := runner.verifyMutationRoots(); err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	accepted := runner.job.AcceptedSyncPending
	var recoveredPacket *evidence.Packet
	if runner.job.PayloadState == PayloadApplyRecovery {
		packet, draft, err := runner.loadRetainedApplyPayload()
		if err != nil {
			return runner.fail(ApplyRecovery, err)
		}
		recoveredPacket = &packet
		if !runner.job.AcceptedSyncPending {
			if runner.options.Apply == nil {
				return runner.fail(ApplyRecovery, errors.New("apply recovery service is unavailable"))
			}
			if err := runner.verifyMutationRoots(); err != nil {
				return runner.fail(ApplyRecovery, err)
			}
			if err := runner.setPhase(Applying); err != nil {
				return runner.fail(ApplyRecovery, err)
			}
			stopCancellation := runner.watchCommitCancellation(ctx)
			result, applyErr := runner.options.Apply(context.WithoutCancel(ctx), ApplyRequest{
				JobID: runner.job.ID, ProjectRoot: runner.roots.project.Path, DataDir: runner.roots.data.Path,
				ProjectIdentity: runner.roots.projectIdentity, DataIdentity: runner.roots.dataIdentity,
				EvidencePath: runner.work.packetPath, ProposalPath: runner.work.proposalPath,
				Packet: packet, Proposal: draft, Changes: ledger.ChangeSet{},
			})
			cancelErr := stopCancellation()
			if applyErr != nil || cancelErr != nil {
				return runner.fail(ApplyRecovery, errors.Join(applyErr, cancelErr))
			}
			if err := runner.refreshConcurrentCancellation(); err != nil {
				return runner.fail(ApplyRecovery, err)
			}
			if err := runner.verifyMutationRoots(); err != nil {
				return runner.fail(ApplyRecovery, err)
			}
			if err := validateApplyResult(result, packet); err != nil {
				return runner.fail(ApplyRecovery, err)
			}
			if err := runner.persistAcceptedApply(packet); err != nil {
				return runner.fail(ApplyRecovery, err)
			}
		}
		accepted = true
	}
	if runner.job.AcceptedSyncPending {
		accepted = true
		if err := runner.runSync(ctx); err != nil {
			return err
		}
	}
	if err := runner.finishRecoveredPayload(accepted, recoveredPacket); err != nil {
		return err
	}
	if requested, err := runner.observeCancellation(ctx); err != nil {
		return runner.fail(ApplyRecovery, err)
	} else if requested {
		return runner.finishCancelled(errors.New("review cancellation requested"))
	}
	return nil
}

// clearStaleAgentWork removes leftover Agent scratch entries from an attempt
// that ended before its own cleanup could run. The retry path reopens the job
// work directory, which requires an empty Agent root, and a SIGKILLed worker
// leaves that state behind. Only the Agent scratch directory is removed;
// authenticated payload bytes keep their digest-verified cleanup boundary in
// recoverRetryState.
func clearStaleAgentWork(leases *LeaseSet, jobID string) error {
	if leases == nil || leases.layout == nil || leases.layout.missing {
		return os.ErrNotExist
	}
	if err := leases.verify(); err != nil {
		return err
	}
	jobRoot, found, err := openPrivateDirectory(leases.layout.work, jobID, false)
	if err != nil || !found || jobRoot == nil {
		return errors.Join(os.ErrNotExist, err)
	}
	defer jobRoot.Close()
	agentRoot, err := ensurePrivateDirectory(jobRoot, "agent")
	if err != nil {
		return err
	}
	defer agentRoot.Close()
	entries, err := readBoundedEntries(agentRoot, maxWorkEntries)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := agentRoot.RemoveAll(entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func (runner *worker) loadRetainedApplyPayload() (evidence.Packet, proposal.Proposal, error) {
	if runner.work == nil || runner.job.PayloadState != PayloadApplyRecovery ||
		runner.job.PacketDigest == "" || runner.job.ResultDigest == "" {
		return evidence.Packet{}, proposal.Proposal{}, errors.New("apply recovery payload is unavailable")
	}
	packetInfo, packetFound, packetErr := regularPrivateEntry(runner.work.inputs, packetWorkName)
	proposalInfo, proposalFound, proposalErr := regularPrivateEntry(runner.work.inputs, proposalWorkName)
	if packetErr != nil || proposalErr != nil || !packetFound || !proposalFound {
		return evidence.Packet{}, proposal.Proposal{}, errors.New("apply recovery payload is missing or unsafe")
	}
	packetBody, packetErr := readStablePrivatePayload(runner.work.inputs, packetWorkName, packetInfo, maxPrivatePacketBytes)
	proposalBody, proposalErr := readStablePrivatePayload(runner.work.inputs, proposalWorkName, proposalInfo, maxPrivateProposalBytes)
	if packetErr != nil || proposalErr != nil || digestPrivate(packetBody) != runner.job.PacketDigest || digestPrivate(proposalBody) != runner.job.ResultDigest {
		return evidence.Packet{}, proposal.Proposal{}, errors.New("apply recovery payload digest changed")
	}
	var packet evidence.Packet
	decodeErr := json.Unmarshal(packetBody, &packet)
	canonicalPacket, encodeErr := json.Marshal(packet)
	if decodeErr != nil || encodeErr != nil || !bytes.Equal(packetBody, canonicalPacket) {
		return evidence.Packet{}, proposal.Proposal{}, errors.New("apply recovery packet is noncanonical")
	}
	draft, err := proposal.Decode(bytes.NewReader(proposalBody))
	if err != nil {
		return evidence.Packet{}, proposal.Proposal{}, errors.New("apply recovery proposal is invalid")
	}
	canonicalProposal, err := json.Marshal(draft)
	if err != nil || !bytes.Equal(proposalBody, canonicalProposal) {
		return evidence.Packet{}, proposal.Proposal{}, errors.New("apply recovery proposal is noncanonical")
	}
	if err := validateRecoveryPacket(packet, runner.job); err != nil {
		return evidence.Packet{}, proposal.Proposal{}, err
	}
	return packet, draft, nil
}

func validateRecoveryPacket(packet evidence.Packet, job Job) error {
	if job.SessionIndex < 0 || job.SessionIndex >= len(job.FrozenSessions) {
		return errors.New("apply recovery has no frozen session")
	}
	frozen := job.FrozenSessions[job.SessionIndex]
	if packet.ProjectID != job.ProjectID || packet.SessionID != frozen.SessionID ||
		(packet.ExpectedCursor != job.CurrentPacket && packet.NextCursor != job.CurrentPacket) ||
		packet.NextCursor.Line > frozen.Upper.Line || (!packet.HasMore && packet.NextCursor != frozen.Upper) {
		return errors.New("apply recovery packet does not match durable progress")
	}
	return nil
}

func (runner *worker) persistAcceptedApply(packet evidence.Packet) error {
	for range 2 {
		err := runner.update(func(job *Job) error {
			if job.State != Running && job.State != CancelRequested {
				return ErrStaleRevision
			}
			if job.CurrentPacket == packet.NextCursor && job.AcceptedSyncPending {
				return nil
			}
			if job.CurrentPacket != packet.ExpectedCursor || job.AcceptedPackets == maxSafeInteger {
				return errors.New("apply recovery cursor changed before acceptance")
			}
			job.AcceptedPackets++
			job.CurrentPacket = packet.NextCursor
			job.AcceptedSyncPending = true
			job.UpdatedAt = runner.timestamp()
			return nil
		})
		if !errors.Is(err, ErrStaleRevision) {
			return err
		}
		if err := runner.refreshConcurrentCancellation(); err != nil {
			return err
		}
		if runner.job.State != CancelRequested {
			return ErrStaleRevision
		}
	}
	return ErrStaleRevision
}

func (runner *worker) finishRecoveredPayload(accepted bool, packet *evidence.Packet) error {
	if runner.work == nil {
		return nil
	}
	if runner.job.PayloadState == PayloadCleanupComplete || runner.job.PayloadState == "" {
		if accepted {
			return runner.advanceRecoveredAcceptedPacket(packet)
		}
		return nil
	}
	if err := runner.verifyMutationRoots(); err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	if runner.job.PayloadState != PayloadCleanupPending {
		if err := runner.update(func(job *Job) error {
			setPayloadLifecycle(job, PayloadCleanupPending, PayloadCleanupByDigest)
			job.PayloadRetainedFor = ""
			job.UpdatedAt = runner.timestamp()
			return nil
		}); err != nil {
			return runner.fail(ApplyRecovery, err)
		}
	}
	if err := runner.verifyMutationRoots(); err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	if err := cleanupPrivatePayloads(runner.work.inputs, runner.job); err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	if err := runner.update(func(job *Job) error {
		setPayloadLifecycle(job, PayloadCleanupComplete, PayloadCleanupByDigest)
		job.PayloadRetainedFor = ""
		job.UpdatedAt = runner.timestamp()
		return nil
	}); err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	if accepted {
		return runner.advanceRecoveredAcceptedPacket(packet)
	}
	return nil
}

func (runner *worker) advanceRecoveredAcceptedPacket(packet *evidence.Packet) error {
	return runner.update(func(job *Job) error {
		if job.AcceptedSyncPending || job.SessionIndex >= len(job.FrozenSessions) {
			return errors.New("accepted recovery cannot advance before mandatory sync")
		}
		terminal := job.CurrentPacket == job.FrozenSessions[job.SessionIndex].Upper
		if packet != nil {
			if job.CurrentPacket != packet.NextCursor {
				return errors.New("accepted recovery packet cursor changed")
			}
			terminal = !packet.HasMore
		}
		if terminal {
			if job.SessionIndex == maxSafeInteger || job.AcceptedSessions == maxSafeInteger {
				return errors.New("accepted recovery progress is exhausted")
			}
			job.SessionIndex++
			job.AcceptedSessions++
			job.CurrentPacket = evidence.CursorBoundary{}
		}
		job.UpdatedAt = runner.timestamp()
		return nil
	})
}

func cancellationTransitionOnly(before, after Job) bool {
	if after.State != CancelRequested || before.ID != after.ID || before.ProjectID != after.ProjectID {
		return false
	}
	want := before
	want.State = CancelRequested
	want.CancellationRequested = after.CancellationRequested
	want.UpdatedAt = after.UpdatedAt
	return reflect.DeepEqual(want, after)
}

func (runner *worker) cancelAgent() error {
	if runner.options.Agent == nil {
		return nil
	}
	runner.agentCancelOnce.Do(func() {
		timeout := runner.options.AgentTimeout
		if timeout <= 0 {
			timeout = time.Second
		}
		cancelContext, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		runner.agentCancelErr = runner.options.Agent.cancel(cancelContext)
	})
	return runner.agentCancelErr
}

// watchAgentCancellation bridges both the local context and a separately
// persisted cancel request into the provider's native cancellation method.
// The worker goroutine alone adopts durable state after Generate returns.
func (runner *worker) watchAgentCancellation(ctx context.Context) func() error {
	done := make(chan struct{})
	finished := make(chan error, 1)
	var once sync.Once
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				finished <- runner.cancelAgent()
				return
			case <-ticker.C:
				job, _, err := runner.loadPinnedJob()
				if err != nil {
					finished <- err
					return
				}
				if job.State == CancelRequested {
					finished <- runner.cancelAgent()
					return
				}
			case <-done:
				finished <- nil
				return
			}
		}
	}()
	return func() error {
		once.Do(func() { close(done) })
		watchErr := <-finished
		if ctx.Err() != nil {
			watchErr = errors.Join(watchErr, runner.cancelAgent())
		}
		return watchErr
	}
}

// watchCommitCancellation durably records cancellation while an Apply or Sync
// call is in its non-interruptible commit window. It never mutates runner
// memory from the watcher goroutine.
func (runner *worker) watchCommitCancellation(ctx context.Context) func() error {
	done := make(chan struct{})
	finished := make(chan cancellationWatchResult, 1)
	baseline, expectedRevision := runner.job, runner.revision
	var once sync.Once
	go func() {
		select {
		case <-ctx.Done():
			at := runner.options.Now().UTC()
			if at.IsZero() || at.Before(baseline.UpdatedAt) {
				at = baseline.UpdatedAt
			}
			job, revision, err := runner.leases.update(runner.options.Store, baseline.ID, expectedRevision, func(job *Job) error {
				if !reflect.DeepEqual(*job, baseline) || job.State != Running {
					return ErrStaleRevision
				}
				job.State = CancelRequested
				job.CancellationRequested = at
				job.UpdatedAt = at
				return nil
			})
			finished <- cancellationWatchResult{job: job, revision: revision, changed: err == nil, err: err}
		case <-done:
			finished <- cancellationWatchResult{}
		}
	}()
	return func() error {
		once.Do(func() { close(done) })
		result := <-finished
		if result.changed {
			runner.job, runner.revision = result.job, result.revision
		}
		if result.err != nil && !errors.Is(result.err, ErrStaleRevision) {
			return result.err
		}
		if err := runner.refreshConcurrentCancellation(); err != nil {
			return err
		}
		if ctx.Err() != nil && runner.job.State != CancelRequested {
			_, err := runner.persistCancellationRequest()
			return err
		}
		return nil
	}
}

func (runner *worker) loadPinnedJob() (Job, int, error) {
	if runner.leases == nil {
		return Job{}, 0, errors.New("review worker leases are unavailable")
	}
	runner.leases.mu.Lock()
	layout := runner.leases.layout
	if layout == nil {
		runner.leases.mu.Unlock()
		return Job{}, 0, errors.New("review worker leases are released")
	}
	if err := layout.verify(); err != nil {
		runner.leases.mu.Unlock()
		return Job{}, 0, err
	}
	job, revision, found, err := runner.options.Store.loadFromJobs(layout.jobs, runner.job.ID)
	if err == nil {
		err = layout.verify()
	}
	runner.leases.mu.Unlock()
	if err != nil {
		return Job{}, 0, err
	}
	if !found {
		return Job{}, 0, os.ErrNotExist
	}
	return job, revision, nil
}

func (runner *worker) refreshConcurrentCancellation() error {
	current, revision, err := runner.loadPinnedJob()
	if err != nil {
		return err
	}
	if revision == runner.revision {
		if !reflect.DeepEqual(current, runner.job) {
			return errors.New("review job bytes changed without a revision")
		}
		return nil
	}
	if revision != runner.revision+1 || !cancellationTransitionOnly(runner.job, current) {
		return ErrStaleRevision
	}
	runner.job, runner.revision = current, revision
	return nil
}

func (runner *worker) persistCancellationRequest() (bool, error) {
	if runner.job.State == CancelRequested {
		return true, nil
	}
	if runner.job.State != Running {
		return false, errors.New("review cancellation requires a running job")
	}
	at := runner.timestamp()
	if err := runner.update(func(job *Job) error {
		if job.State != Running {
			return ErrStaleRevision
		}
		job.State = CancelRequested
		job.CancellationRequested = at
		job.UpdatedAt = at
		return nil
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (runner *worker) observeCancellation(ctx context.Context) (bool, error) {
	if err := runner.refreshConcurrentCancellation(); err != nil {
		return false, err
	}
	if runner.job.State == CancelRequested {
		if runner.job.Phase != Applying && runner.job.Phase != Syncing {
			_ = runner.cancelAgent()
		}
		return true, nil
	}
	if ctx.Err() == nil {
		return false, nil
	}
	_ = runner.cancelAgent()
	return runner.persistCancellationRequest()
}

func (runner *worker) finishCancelled(cause error) error {
	cause = errors.Join(cause, runner.agentCancelErr)
	if runner.job.AcceptedSyncPending || runner.job.PayloadState == PayloadApplyRecovery {
		return runner.fail(ApplyRecovery, errors.Join(cause, errors.New("review cancellation cannot discard an unresolved commit")))
	}
	if runner.work != nil && runner.job.PayloadState != "" && runner.job.PayloadState != PayloadCleanupComplete {
		if err := runner.verifyMutationRoots(); err != nil {
			return runner.fail(ApplyRecovery, errors.Join(cause, err))
		}
		if runner.job.PayloadState != PayloadCleanupPending {
			if err := runner.update(func(job *Job) error {
				setPayloadLifecycle(job, PayloadCleanupPending, PayloadCleanupByDigest)
				job.PayloadRetainedFor = ""
				job.UpdatedAt = runner.timestamp()
				return nil
			}); err != nil {
				return errors.Join(cause, err)
			}
		}
		if err := cleanupPrivatePayloads(runner.work.inputs, runner.job); err != nil {
			return errors.Join(cause, err)
		}
		if err := runner.update(func(job *Job) error {
			setPayloadLifecycle(job, PayloadCleanupComplete, PayloadCleanupByDigest)
			job.PayloadRetainedFor = ""
			job.UpdatedAt = runner.timestamp()
			return nil
		}); err != nil {
			return errors.Join(cause, err)
		}
	}
	completed := runner.timestamp()
	terminalErr := runner.updateTerminal(func(job *Job) error {
		if job.State != Running && job.State != CancelRequested {
			return ErrStaleRevision
		}
		job.State = Cancelled
		job.Phase = ""
		job.UpdatedAt = completed
		job.CompletedAt = completed
		if job.CancellationRequested.IsZero() {
			job.CancellationRequested = completed
		}
		job.Owner = Owner{}
		job.Error = SafeError{Code: AgentCancelled}
		job.PrivateError = boundedPrivateError(cause)
		return nil
	})
	return errors.Join(cause, terminalErr)
}
