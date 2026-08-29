package reviewjob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/agent"
	"github.com/neomei/SessionReviewer/internal/apply"
	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/proposal"
	"github.com/neomei/SessionReviewer/internal/reviewprompt"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

const (
	maxPrivatePacketBytes   = 4 << 20
	maxPrivateProposalBytes = 4 << 20
	maxPrivateErrorBytes    = 32 << 10
	maxWorkEntries          = 32
	packetWorkName          = "packet.json"
	proposalWorkName        = "proposal.json"
)

type payloadCheckpointStage string

const (
	payloadBeforeIntentCAS   payloadCheckpointStage = "before_intent_cas"
	payloadAfterIntentCAS    payloadCheckpointStage = "after_intent_cas"
	payloadBeforeWrite       payloadCheckpointStage = "before_write"
	payloadAfterWrite        payloadCheckpointStage = "after_write"
	payloadBeforeRename      payloadCheckpointStage = "before_rename"
	payloadAfterRename       payloadCheckpointStage = "after_rename"
	payloadBeforeVerify      payloadCheckpointStage = "before_verify"
	payloadAfterVerify       payloadCheckpointStage = "after_verify"
	payloadBeforeRetainedCAS payloadCheckpointStage = "before_retained_cas"
	payloadAfterRetainedCAS  payloadCheckpointStage = "after_retained_cas"
)

type payloadCheckpointFailure struct {
	kind  PayloadKind
	stage payloadCheckpointStage
	err   error
}

func (failure *payloadCheckpointFailure) Error() string {
	return fmt.Sprintf("private %s payload checkpoint %s: %v", failure.kind, failure.stage, failure.err)
}

func (failure *payloadCheckpointFailure) Unwrap() error { return failure.err }

type prepareCheckpointStage string

const (
	prepareAfterReturn     prepareCheckpointStage = "after_return"
	prepareAfterRootCheck  prepareCheckpointStage = "after_root_check"
	prepareAfterValidation prepareCheckpointStage = "after_validation"
)

type prepareCheckpointFailure struct {
	stage prepareCheckpointStage
	err   error
}

func (failure *prepareCheckpointFailure) Error() string {
	return fmt.Sprintf("prepare checkpoint %s: %v", failure.stage, failure.err)
}

func (failure *prepareCheckpointFailure) Unwrap() error { return failure.err }

// Prepared binds one bounded packet to the exact accepted context against
// which its proposal must be generated and validated.
type Prepared struct {
	Packet      evidence.Packet
	PacketBytes []byte
	Accepted    reviewv2.Accepted
}

// PrepareRequest exposes only the frozen session boundary and authenticated
// read roots. Implementations return exact canonical bytes in memory; only the
// worker may publish them into its private WAL.
type PrepareRequest struct {
	JobID           string
	ProjectID       string
	SessionID       string
	SessionIndex    int
	AcceptedCursor  evidence.CursorBoundary
	UpperBoundary   evidence.CursorBoundary
	ProjectRoot     string
	DataDir         string
	ProjectIdentity pathguard.IdentityToken
	DataIdentity    pathguard.IdentityToken
}

type PrepareFunc func(context.Context, PrepareRequest) (Prepared, error)

// ApplyRequest contains a proposal that has already received trusted source
// accounting and passed proposal.Validate. Paths name authenticated private
// worker files and never enter Job or public status. Apply implementations
// must authenticate ProjectIdentity/DataIdentity immediately before mutation.
type ApplyRequest struct {
	JobID           string
	ProjectRoot     string
	DataDir         string
	ProjectIdentity pathguard.IdentityToken
	DataIdentity    pathguard.IdentityToken
	EvidencePath    string
	ProposalPath    string
	Packet          evidence.Packet
	Proposal        proposal.Proposal
	Changes         ledger.ChangeSet
}

type ApplyFunc func(context.Context, ApplyRequest) (apply.Result, error)
type SyncFunc func(context.Context, syncproject.Options) (syncengine.Report, error)

// RunOptions contains the trusted one-shot worker dependencies. Frozen
// sessions and all public progress are loaded from Store, never supplied here.
type RunOptions struct {
	Store        Store
	JobID        string
	OwnerID      string
	LeaseTimeout time.Duration

	ProjectRoot  string
	VaultRoot    string
	DataDir      string
	GOOS         string
	AgentTimeout time.Duration
	Now          func() time.Time

	Prepare PrepareFunc
	Agent   *AgentHandle
	Apply   ApplyFunc
	Sync    SyncFunc
	Pricing PricingResolver
	// LaunchToken is the private one-use authority persisted before a detached
	// worker is spawned. OwnershipReady runs only after both leases and durable
	// Owner state exist and the token has been consumed.
	LaunchToken    string
	OwnershipReady func() error

	beforeCleanupBoundary func() error
	afterCleanupBoundary  func() error
	prepareCheckpoint     func(prepareCheckpointStage) error
	payloadCheckpoint     func(PayloadKind, payloadCheckpointStage) error
}

type worker struct {
	options  RunOptions
	job      Job
	revision int
	leases   *LeaseSet
	roots    *workerRoots
	work     *jobWork
	retry    bool

	agentCancelOnce sync.Once
	agentCancelErr  error
}

type workerRoots struct {
	project         *pathguard.Directory
	vault           *pathguard.Directory
	data            *pathguard.Directory
	projectIdentity pathguard.IdentityToken
	vaultIdentity   pathguard.IdentityToken
	dataIdentity    pathguard.IdentityToken
	syncPin         *syncproject.MappingPin
}

func (roots *workerRoots) close() error {
	if roots == nil {
		return nil
	}
	return errors.Join(roots.syncPin.Close(), roots.project.Close(), roots.vault.Close(), roots.data.Close())
}

func (roots *workerRoots) verify() error {
	if roots == nil {
		return errors.New("worker roots are unavailable")
	}
	for _, expected := range []struct {
		name      string
		directory *pathguard.Directory
		identity  pathguard.IdentityToken
	}{
		{name: "Project", directory: roots.project, identity: roots.projectIdentity},
		{name: "Vault", directory: roots.vault, identity: roots.vaultIdentity},
		{name: "Data", directory: roots.data, identity: roots.dataIdentity},
	} {
		reopened, err := pathguard.Open(expected.directory.Path)
		if err != nil {
			return fmt.Errorf("worker %s root changed", expected.name)
		}
		identity, identityErr := reopened.PhysicalIdentity()
		same := os.SameFile(expected.directory.Info(), reopened.Info())
		closeErr := reopened.Close()
		if identityErr != nil || closeErr != nil || !same || identity != expected.identity {
			return fmt.Errorf("worker %s root changed", expected.name)
		}
	}
	return nil
}

type jobWork struct {
	jobRoot      *os.Root
	inputs       *os.Root
	agent        *os.Root
	inputsPath   string
	agentPath    string
	packetPath   string
	proposalPath string
}

func (work *jobWork) close() error {
	if work == nil {
		return nil
	}
	return errors.Join(closeRoot(work.inputs), closeRoot(work.agent), closeRoot(work.jobRoot))
}

func closeRoot(root *os.Root) error {
	if root == nil {
		return nil
	}
	return root.Close()
}

// Run consumes one already frozen durable job. It authenticates and pins every
// mutation root before publishing ownership. After ownership is durable it
// holds Task 3's project and global leases until a terminal state is durable;
// a detached pre-ownership authentication failure instead leaves the one-use
// launch authority untouched for the parent to terminalize.
func Run(ctx context.Context, options RunOptions) (retErr error) {
	if ctx == nil {
		return errors.New("review worker context is required")
	}
	if err := validateRunOptions(options); err != nil {
		return err
	}
	if err := verifyLaunchToken(options.Store, options.JobID, options.LaunchToken); err != nil {
		return err
	}
	job, revision, leases, err := options.Store.acquireJobLeases(options.JobID, options.LeaseTimeout)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, leases.Release()) }()
	retryCancellation := job.State == CancelRequested && job.Phase == Preflight && job.Owner.ID == "" && job.Attempt > 1
	if job.State != Queued && job.State != Retrying && !retryCancellation {
		return errors.New("review job is not ready for one-shot execution")
	}
	if job.Phase != Preflight {
		return errors.New("review job does not begin at preflight")
	}

	runner := &worker{options: options, job: job, revision: revision, leases: leases, retry: job.State == Retrying || retryCancellation}
	roots, err := authenticateWorkerRoots(options, runner.job, leases)
	if err != nil {
		// Detached workers still hold their one-use launch authority here. The
		// parent owns terminalization after the failure handshake, so a stale
		// Project/Vault mapping cannot consume the token or publish ownership.
		// Tokenless in-process callers have no parent launch handshake, so they
		// may persist the failure directly without first publishing an owner.
		if options.LaunchToken == "" {
			return runner.fail(ApplyRecovery, err)
		}
		return err
	}
	runner.roots = roots
	defer func() { retErr = errors.Join(retErr, roots.close()) }()
	if err := runner.start(); err != nil {
		return err
	}
	if options.OwnershipReady != nil {
		if err := options.OwnershipReady(); err != nil {
			return runner.fail(ApplyRecovery, err)
		}
	}

	cancelWithoutCommitRecovery := runner.retry && runner.job.State == CancelRequested &&
		!runner.job.AcceptedSyncPending && runner.job.PayloadState != PayloadApplyRecovery
	if cancelWithoutCommitRecovery {
		if runner.job.PayloadState == "" {
			return runner.finishCancelled(errors.New("review cancellation requested"))
		}
		if err := clearStaleAgentWork(leases, runner.job.ID); err != nil {
			return runner.fail(ApplyRecovery, err)
		}
		work, err := openJobWork(leases, runner.job.ID)
		if err != nil {
			return runner.fail(ApplyRecovery, err)
		}
		runner.work = work
		defer func() { retErr = errors.Join(retErr, work.close()) }()
		return runner.recoverRetryState(ctx)
	}
	if runner.retry {
		if options.Agent == nil {
			return runner.fail(AgentUnconfigured, errors.New("retry lacks its frozen verified Agent"))
		}
		if err := options.Agent.validateFor(runner.job); err != nil {
			return runner.fail(AgentIncompatible, err)
		}
	}
	needsWork := runner.retry && (runner.job.PayloadState != "" || runner.job.AcceptedSyncPending)
	if needsWork {
		if err := clearStaleAgentWork(leases, runner.job.ID); err != nil {
			return runner.fail(ApplyRecovery, err)
		}
		work, err := openJobWork(leases, runner.job.ID)
		if err != nil {
			return runner.fail(ApplyRecovery, err)
		}
		runner.work = work
		defer func() { retErr = errors.Join(retErr, work.close()) }()
		if err := runner.recoverRetryState(ctx); err != nil {
			return err
		}
	}
	if len(runner.job.FrozenSessions) != 0 && runner.job.SessionIndex == len(runner.job.FrozenSessions) {
		if requested, err := runner.observeCancellation(ctx); err != nil {
			return runner.fail(ApplyRecovery, err)
		} else if requested {
			return runner.finishCancelled(errors.New("review cancellation requested"))
		}
		return runner.complete()
	}
	if len(runner.job.FrozenSessions) == 0 {
		if err := runner.runSync(ctx); err != nil {
			return err
		}
		if requested, err := runner.observeCancellation(ctx); err != nil {
			return runner.fail(ApplyRecovery, err)
		} else if requested {
			return runner.finishCancelled(errors.New("review cancellation requested"))
		}
		return runner.complete()
	}
	if requested, err := runner.observeCancellation(ctx); err != nil {
		return runner.fail(ApplyRecovery, err)
	} else if requested {
		return runner.finishCancelled(errors.New("review cancellation requested"))
	}
	if options.Prepare == nil || options.Agent == nil || options.Apply == nil {
		return runner.fail(AgentUnconfigured, errors.New("pending review job lacks a worker dependency"))
	}
	if err := options.Agent.validateFor(runner.job); err != nil {
		return runner.fail(AgentIncompatible, err)
	}
	if runner.work == nil {
		work, err := openJobWork(leases, runner.job.ID)
		if err != nil {
			return runner.fail(ApplyRecovery, err)
		}
		runner.work = work
		defer func() { retErr = errors.Join(retErr, work.close()) }()
	}

	for runner.job.SessionIndex < len(runner.job.FrozenSessions) {
		if requested, err := runner.observeCancellation(ctx); err != nil {
			return runner.fail(ApplyRecovery, err)
		} else if requested {
			return runner.finishCancelled(errors.New("review cancellation requested"))
		}
		if err := runner.runPacket(ctx); err != nil {
			if refreshErr := runner.refreshConcurrentCancellation(); refreshErr == nil &&
				runner.job.State == CancelRequested && runner.job.Phase != Applying && runner.job.Phase != Syncing &&
				!runner.job.AcceptedSyncPending && runner.job.PayloadState != PayloadApplyRecovery {
				return runner.finishCancelled(err)
			}
			return err
		}
	}
	return runner.complete()
}

func validateRunOptions(options RunOptions) error {
	if strings.TrimSpace(options.Store.Root) == "" || strings.TrimSpace(options.JobID) == "" ||
		strings.TrimSpace(options.OwnerID) == "" || strings.TrimSpace(options.ProjectRoot) == "" ||
		strings.TrimSpace(options.VaultRoot) == "" || strings.TrimSpace(options.DataDir) == "" ||
		strings.TrimSpace(options.GOOS) == "" || options.Now == nil || options.Sync == nil {
		return errors.New("review worker requires store, identity, roots, clock, and sync service")
	}
	if !filepath.IsAbs(options.Store.Root) || !filepath.IsAbs(options.DataDir) || options.LeaseTimeout < 0 || options.AgentTimeout <= 0 {
		return errors.New("review worker roots, lease timeout, or Agent timeout are invalid")
	}
	if !validID(options.OwnerID) {
		return errors.New("review worker owner ID is invalid")
	}
	if options.LaunchToken != "" && !validLaunchToken(options.LaunchToken) {
		return errors.New("review worker launch token is invalid")
	}
	return nil
}

func verifyLaunchToken(store Store, jobID, token string) error {
	job, _, found, err := store.Load(jobID)
	if err != nil || !found {
		if err != nil {
			return err
		}
		return os.ErrNotExist
	}
	if job.LaunchTokenDigest == "" {
		if token != "" {
			return errors.New("review worker launch token was already consumed")
		}
		return nil
	}
	if !validLaunchToken(token) || subtle.ConstantTimeCompare([]byte(job.LaunchTokenDigest), []byte(launchTokenDigest(token))) != 1 {
		return errors.New("review worker launch token is invalid")
	}
	return nil
}

// VerifyLaunchAuthority authenticates a detached worker before it opens
// configured roots or probes the stored Agent. Run rechecks and consumes the
// same authority atomically with durable ownership after both leases exist.
func VerifyLaunchAuthority(store Store, jobID, token string) error {
	if strings.TrimSpace(store.Root) == "" || !validID(jobID) {
		return errors.New("review worker launch authority target is invalid")
	}
	return verifyLaunchToken(store, jobID, token)
}

func validLaunchToken(token string) bool {
	if len(token) < 32 || len(token) > 128 {
		return false
	}
	for _, character := range token {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func launchTokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (runner *worker) start() error {
	started := runner.timestamp()
	return runner.update(func(job *Job) error {
		cancelRequested := job.State == CancelRequested && job.Phase == Preflight && job.Owner.ID == "" && job.Attempt > 1
		if job.State != Queued && job.State != Retrying && !cancelRequested {
			return ErrStaleRevision
		}
		if !cancelRequested {
			job.State = Running
		}
		if job.LaunchTokenDigest != "" {
			if subtle.ConstantTimeCompare([]byte(job.LaunchTokenDigest), []byte(launchTokenDigest(runner.options.LaunchToken))) != 1 {
				return errors.New("review worker launch token changed before ownership")
			}
			job.LaunchTokenDigest = ""
			job.LaunchIntentAt = time.Time{}
		} else if runner.options.LaunchToken != "" {
			return errors.New("review worker launch token was already consumed")
		}
		job.Phase = Preflight
		if job.StartedAt.IsZero() {
			job.StartedAt = started
		}
		job.UpdatedAt = started
		job.CompletedAt = time.Time{}
		job.Owner = Owner{ID: runner.options.OwnerID, AcquiredAt: started}
		job.Error = SafeError{}
		job.PrivateError = ""
		return nil
	})
}

func authenticateWorkerRoots(options RunOptions, job Job, leases *LeaseSet) (*workerRoots, error) {
	project, err := pathguard.Open(options.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("open worker Project root: %w", err)
	}
	projectIdentity, err := project.PhysicalIdentity()
	if err != nil || projectIdentity != job.ProjectIdentity {
		_ = project.Close()
		return nil, errors.New("worker Project identity does not match frozen job")
	}
	vault, err := pathguard.Open(options.VaultRoot)
	if err != nil {
		_ = project.Close()
		return nil, fmt.Errorf("open worker Vault root: %w", err)
	}
	data, err := pathguard.Open(options.DataDir)
	if err != nil {
		_ = project.Close()
		_ = vault.Close()
		return nil, fmt.Errorf("open worker data root: %w", err)
	}
	if leases == nil || leases.layout == nil || leases.layout.data == nil ||
		!os.SameFile(data.Info(), leases.layout.data.Info()) || data.Path != leases.layout.data.Path {
		_ = project.Close()
		_ = vault.Close()
		_ = data.Close()
		return nil, errors.New("worker Store root and data root differ")
	}
	if rootsOverlap(project, data) || rootsOverlap(vault, data) {
		_ = project.Close()
		_ = vault.Close()
		_ = data.Close()
		return nil, errors.New("worker private data root must be physically disjoint from Project and Vault")
	}
	syncPin, err := syncproject.PinMapping(syncproject.Options{
		ProjectID: job.ProjectID, CWD: project.Path, DataDir: data.Path,
		GOOS: options.GOOS, Now: options.Now, Trigger: syncengine.TriggerCLI,
	})
	if err != nil {
		_ = project.Close()
		_ = vault.Close()
		_ = data.Close()
		return nil, fmt.Errorf("pin worker sync mapping: %w", err)
	}
	if err := syncPin.AuthenticateBinding(job.ProjectID, project.Info(), vault.Info(), data.Info()); err != nil {
		_ = syncPin.Close()
		_ = project.Close()
		_ = vault.Close()
		_ = data.Close()
		return nil, err
	}
	vaultIdentity, vaultErr := vault.PhysicalIdentity()
	dataIdentity, dataErr := data.PhysicalIdentity()
	if vaultErr != nil || dataErr != nil {
		_ = syncPin.Close()
		_ = project.Close()
		_ = vault.Close()
		_ = data.Close()
		return nil, errors.New("worker root identity is unavailable")
	}
	return &workerRoots{
		project: project, vault: vault, data: data,
		projectIdentity: job.ProjectIdentity, vaultIdentity: vaultIdentity, dataIdentity: dataIdentity,
		syncPin: syncPin,
	}, nil
}

func rootsOverlap(first, second *pathguard.Directory) bool {
	return first.ContainsIdentity(second.Info()) || second.ContainsIdentity(first.Info())
}

func openJobWork(leases *LeaseSet, jobID string) (_ *jobWork, retErr error) {
	if leases == nil || leases.layout == nil || leases.layout.missing {
		return nil, os.ErrNotExist
	}
	if err := leases.verify(); err != nil {
		return nil, err
	}
	layout := leases.layout
	work := &jobWork{}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, work.close())
		}
	}()
	jobRoot, found, err := openPrivateDirectory(layout.work, jobID, false)
	if err != nil || !found || jobRoot == nil {
		return nil, errors.Join(os.ErrNotExist, err)
	}
	work.jobRoot = jobRoot
	inputs, err := ensurePrivateDirectory(jobRoot, "inputs")
	if err != nil {
		return nil, err
	}
	work.inputs = inputs
	agentRoot, err := ensurePrivateDirectory(jobRoot, "agent")
	if err != nil {
		return nil, err
	}
	work.agent = agentRoot
	entries, err := readBoundedEntries(agentRoot, maxWorkEntries)
	if err != nil || len(entries) != 0 {
		return nil, errors.New("private Agent work directory is not empty")
	}
	base := filepath.Join(layout.data.Path, "review-jobs", "work", jobID)
	work.inputsPath = filepath.Join(base, "inputs")
	work.agentPath = filepath.Join(base, "agent")
	work.packetPath = filepath.Join(work.inputsPath, packetWorkName)
	work.proposalPath = filepath.Join(work.inputsPath, proposalWorkName)
	return work, nil
}

func (runner *worker) runPacket(ctx context.Context) error {
	frozen := runner.job.FrozenSessions[runner.job.SessionIndex]
	if requested, err := runner.observeCancellation(ctx); err != nil {
		return runner.fail(ApplyRecovery, err)
	} else if requested {
		return runner.finishCancelled(errors.New("review cancellation requested"))
	}
	if err := runner.setPreCommitPhaseOrCancel(Preparing); err != nil {
		return err
	}
	if err := runner.verifyMutationRoots(); err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	prepared, err := runner.options.Prepare(ctx, PrepareRequest{
		JobID: runner.job.ID, ProjectID: runner.job.ProjectID,
		SessionID: frozen.SessionID, SessionIndex: runner.job.SessionIndex,
		AcceptedCursor: runner.job.CurrentPacket, UpperBoundary: frozen.Upper,
		ProjectRoot: runner.roots.project.Path, DataDir: runner.roots.data.Path,
		ProjectIdentity: runner.roots.projectIdentity, DataIdentity: runner.roots.dataIdentity,
	})
	if err != nil {
		if requested, cancelErr := runner.observeCancellation(ctx); cancelErr != nil {
			return runner.fail(ApplyRecovery, errors.Join(err, cancelErr))
		} else if requested {
			return runner.finishCancelled(errors.Join(err, ctx.Err()))
		}
		return runner.fail(ProposalRejected, err)
	}
	if len(prepared.PacketBytes) == 0 || len(prepared.PacketBytes) > maxPrivatePacketBytes {
		return runner.fail(ProposalRejected, errors.New("prepared packet bytes are absent or oversized"))
	}
	packetBody := append([]byte(nil), prepared.PacketBytes...)
	packetDigest := digestPrivate(packetBody)
	if err := runner.runPrepareCheckpoint(prepareAfterReturn); err != nil {
		return err
	}
	if err := runner.verifyMutationRoots(); err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	if err := runner.runPrepareCheckpoint(prepareAfterRootCheck); err != nil {
		return err
	}
	var packet evidence.Packet
	decodeErr := json.Unmarshal(packetBody, &packet)
	canonicalPacket, encodeErr := json.Marshal(packet)
	if decodeErr != nil || encodeErr != nil || !bytes.Equal(packetBody, canonicalPacket) || !reflect.DeepEqual(packet, prepared.Packet) {
		return runner.fail(ProposalRejected, errors.New("prepared packet bytes are absent, noncanonical, oversized, or do not authenticate the packet"))
	}
	if err := validatePrepared(packet, prepared.Accepted, runner.job, frozen); err != nil {
		return runner.fail(ProposalRejected, err)
	}
	if err := runner.runPrepareCheckpoint(prepareAfterValidation); err != nil {
		return err
	}
	if err := runner.publishPacketPayload(packetBody, packetDigest); err != nil {
		if isPayloadCheckpointFailure(err) {
			return err
		}
		return runner.fail(ProposalRejected, err)
	}

	bundle, err := reviewprompt.Build(reviewprompt.Input{
		Packet: packet, Accepted: prepared.Accepted.State, OutputSchema: reviewprompt.FinalProposalSchema(),
		GOOS: runner.options.GOOS,
		ForbiddenRoots: []reviewprompt.ForbiddenRoot{
			{CanonicalPath: runner.roots.project.Path, Aliases: distinctAliases(runner.roots.project.Path, runner.options.ProjectRoot)},
			{CanonicalPath: runner.roots.vault.Path, Aliases: distinctAliases(runner.roots.vault.Path, runner.options.VaultRoot)},
			{CanonicalPath: runner.roots.data.Path, Aliases: distinctAliases(runner.roots.data.Path, runner.options.DataDir, runner.options.Store.Root)},
			{CanonicalPath: runner.work.inputsPath},
			{CanonicalPath: runner.work.agentPath},
		},
	})
	if err != nil {
		return runner.fail(ProposalRejected, err)
	}
	if requested, err := runner.observeCancellation(ctx); err != nil {
		return runner.fail(ApplyRecovery, err)
	} else if requested {
		return runner.finishCancelled(errors.New("review cancellation requested"))
	}
	if err := runner.setPreCommitPhaseOrCancel(Reviewing); err != nil {
		return err
	}
	stopAgentCancellation := runner.watchAgentCancellation(ctx)
	result, err := runner.options.Agent.generate(ctx, agent.Request{
		Prompt:           bundle.Prompt,
		OutputSchema:     bundle.OutputSchema,
		WorkingDirectory: runner.work.agentPath,
		ForbiddenRoots: []agent.ForbiddenRoot{
			{Kind: agent.ForbiddenRootProject, CanonicalPath: runner.roots.project.Path},
			{Kind: agent.ForbiddenRootVault, CanonicalPath: runner.roots.vault.Path},
		},
		Deadline: runner.timestamp().Add(runner.options.AgentTimeout),
	})
	watchErr := stopAgentCancellation()
	requested, cancelErr := runner.observeCancellation(ctx)
	if cancelErr != nil {
		return runner.fail(ApplyRecovery, errors.Join(err, watchErr, cancelErr))
	}
	if requested {
		return runner.finishCancelled(errors.Join(err, watchErr, ctx.Err(), errors.New("review cancellation requested")))
	}
	if watchErr != nil {
		return runner.fail(ApplyRecovery, watchErr)
	}
	if err != nil {
		if mapAgentError(err) == AgentCancelled {
			_ = runner.cancelAgent()
			if _, cancelErr := runner.persistCancellationRequest(); cancelErr != nil {
				return runner.fail(ApplyRecovery, errors.Join(err, cancelErr))
			}
			return runner.finishCancelled(err)
		}
		return runner.fail(mapAgentError(err), err)
	}
	if err := runner.verifyMutationRoots(); err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	// The accepted capability cannot attest provider model provenance. Treat a
	// nonempty model as a contradictory result and reject it before persisting
	// either model metadata or token usage.
	if result.Model != "" {
		return runner.fail(AgentIncompatible, errors.New("Agent returned model provenance that its verified capability cannot attest"))
	}
	reviewAccounting, err := AddReviewResult(runner.job.ReviewAccounting, result, runner.job.StartedAt, runner.options.Pricing)
	if err != nil {
		return runner.fail(ProposalRejected, err)
	}
	if err := runner.update(func(job *Job) error {
		job.ReviewAccounting = reviewAccounting
		job.UpdatedAt = runner.timestamp()
		return nil
	}); err != nil {
		return err
	}
	draft, err := proposal.Decode(bytes.NewReader(result.Proposal))
	if err != nil {
		return runner.fail(ProposalRejected, err)
	}
	if draft.SessionReport.Accounting != nil {
		return runner.fail(ProposalRejected, errors.New("Agent-authored source accounting is forbidden"))
	}
	if bundle.HostAccountingRequired != (packet.SessionUsage != nil) {
		return runner.fail(ProposalRejected, errors.New("prompt accounting boundary is inconsistent"))
	}
	if err := enrichSourceAccounting(&draft, packet.SessionUsage, runner.job.StartedAt, runner.options.Pricing); err != nil {
		return runner.fail(ProposalRejected, err)
	}
	changes, err := proposal.Validate(draft, packet, prepared.Accepted.Legacy)
	if err != nil {
		return runner.fail(ProposalRejected, err)
	}
	if requested, err := runner.observeCancellation(ctx); err != nil {
		return runner.fail(ApplyRecovery, err)
	} else if requested {
		return runner.finishCancelled(errors.New("review cancellation requested"))
	}
	proposalBody, err := json.Marshal(draft)
	if err != nil || len(proposalBody) > maxPrivateProposalBytes {
		return runner.fail(ProposalRejected, errors.New("final proposal cannot be encoded within its private bound"))
	}
	resultDigest := digestPrivate(proposalBody)
	if err := runner.publishProposalPayload(proposalBody, resultDigest); err != nil {
		if isPayloadCheckpointFailure(err) {
			return err
		}
		return runner.fail(ProposalRejected, err)
	}
	if err := runner.verifyMutationRoots(); err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	if requested, err := runner.observeCancellation(ctx); err != nil {
		return runner.fail(ApplyRecovery, err)
	} else if requested {
		return runner.finishCancelled(errors.New("review cancellation requested"))
	}
	if err := runner.setPreCommitPhaseOrCancel(Applying); err != nil {
		return err
	}
	stopCommitCancellation := runner.watchCommitCancellation(ctx)
	applyResult, err := runner.options.Apply(context.WithoutCancel(ctx), ApplyRequest{
		JobID: runner.job.ID, ProjectRoot: runner.roots.project.Path, DataDir: runner.roots.data.Path,
		ProjectIdentity: runner.roots.projectIdentity, DataIdentity: runner.roots.dataIdentity,
		EvidencePath: runner.work.packetPath, ProposalPath: runner.work.proposalPath,
		Packet: packet, Proposal: draft, Changes: changes,
	})
	cancelErr = stopCommitCancellation()
	if err != nil {
		return runner.fail(ApplyRecovery, errors.Join(err, cancelErr))
	}
	if cancelErr != nil {
		return runner.fail(ApplyRecovery, cancelErr)
	}
	if err := runner.verifyMutationRoots(); err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	if err := validateApplyResult(applyResult, packet); err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	if err := runner.persistAcceptedApply(packet); err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	if err := runner.runSync(ctx); err != nil {
		return err
	}
	if runner.options.beforeCleanupBoundary != nil {
		if err := runner.options.beforeCleanupBoundary(); err != nil {
			return err
		}
	}
	if err := runner.update(func(job *Job) error {
		setPayloadLifecycle(job, PayloadCleanupPending, PayloadCleanupByDigest)
		job.PayloadRetainedFor = ""
		job.UpdatedAt = runner.timestamp()
		return nil
	}); err != nil {
		return runner.fail(SyncPartial, err)
	}
	if runner.options.afterCleanupBoundary != nil {
		if err := runner.options.afterCleanupBoundary(); err != nil {
			return err
		}
	}
	if err := cleanupPrivatePayloads(runner.work.inputs, runner.job); err != nil {
		return runner.fail(SyncPartial, err)
	}
	if err := runner.update(func(job *Job) error {
		if !packet.HasMore {
			if job.SessionIndex == maxSafeInteger || job.AcceptedSessions == maxSafeInteger {
				return errors.New("accepted session progress is exhausted")
			}
			job.SessionIndex++
			job.AcceptedSessions++
			job.CurrentPacket = evidence.CursorBoundary{}
		}
		setPayloadLifecycle(job, PayloadCleanupComplete, PayloadCleanupByDigest)
		job.PayloadRetainedFor = ""
		job.UpdatedAt = runner.timestamp()
		return nil
	}); err != nil {
		return runner.fail(SyncPartial, err)
	}
	if requested, err := runner.observeCancellation(ctx); err != nil {
		return runner.fail(ApplyRecovery, err)
	} else if requested {
		return runner.finishCancelled(errors.New("review cancellation requested"))
	}
	return nil
}

func validatePrepared(packet evidence.Packet, accepted reviewv2.Accepted, job Job, frozen FrozenSession) error {
	if packet.ProjectID != job.ProjectID || packet.SessionID != frozen.SessionID || packet.CWD == "" {
		return errors.New("prepared packet does not belong to the frozen job session")
	}
	if err := reviewv2.Validate(accepted.State); err != nil || accepted.State.Review.ProjectID != job.ProjectID || accepted.Legacy.ProjectID != job.ProjectID {
		return errors.New("prepared accepted context is invalid or belongs to another project")
	}
	legacy, err := reviewv2.LegacyState(accepted.State)
	if err != nil || !reflect.DeepEqual(legacy, accepted.Legacy) {
		return errors.New("prepared accepted projections do not describe the same state")
	}
	if packet.FromCursor < 1 || packet.ToCursor < packet.FromCursor || packet.ExpectedCursor.Line != packet.FromCursor-1 || packet.NextCursor.Line != packet.ToCursor {
		return errors.New("prepared packet cursor envelope is invalid")
	}
	if packet.ExpectedCursor != job.CurrentPacket {
		return errors.New("successor packet does not begin at the accepted durable cursor")
	}
	if packet.NextCursor.Line > frozen.Upper.Line {
		return errors.New("prepared packet exceeds the frozen upper boundary")
	}
	if packet.HasMore {
		if packet.NextCursor.Line >= frozen.Upper.Line {
			return errors.New("packet claims a successor at or beyond the frozen upper boundary")
		}
	} else if packet.NextCursor != frozen.Upper {
		return errors.New("terminal packet does not end at the frozen upper boundary")
	}
	return nil
}

func distinctAliases(canonical string, candidates ...string) []string {
	aliases := make([]string, 0, len(candidates))
	seen := map[string]bool{canonical: true}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && !seen[candidate] {
			seen[candidate] = true
			aliases = append(aliases, candidate)
		}
	}
	return aliases
}

func enrichSourceAccounting(draft *proposal.Proposal, usage *accounting.SessionUsage, at time.Time, resolver PricingResolver) error {
	if draft == nil {
		return errors.New("proposal draft is required")
	}
	if usage == nil {
		draft.SessionReport.Accounting = nil
		return nil
	}
	if err := accounting.ValidateSessionUsage(usage); err != nil {
		return fmt.Errorf("source session usage: %w", err)
	}
	report := &accounting.SessionAccounting{
		StartedAt: usage.StartedAt, EndedAt: usage.EndedAt, DurationMS: usage.DurationMS,
		Models: make([]accounting.ModelAccounting, 0, len(usage.Models)), TotalTokens: usage.TotalTokens,
	}
	for _, model := range usage.Models {
		if resolver == nil {
			return fmt.Errorf("source model %q lacks trusted pricing", model.Model)
		}
		pricing, found := resolver.Resolve(model.Model, at)
		if !found {
			return fmt.Errorf("source model %q lacks trusted pricing", model.Model)
		}
		cost, err := accounting.PriceUsage(model.TokenUsage, pricing)
		if err != nil {
			return fmt.Errorf("source model %q pricing: %w", model.Model, err)
		}
		report.Models = append(report.Models, accounting.ModelAccounting{ModelUsage: model, Pricing: pricing, CostUSD: cost})
		report.TotalCostUSD += cost
	}
	if err := accounting.ValidateSessionAccounting(report, usage); err != nil {
		return fmt.Errorf("trusted source accounting: %w", err)
	}
	draft.SessionReport.Accounting = report
	return nil
}

func validateApplyResult(result apply.Result, packet evidence.Packet) error {
	if result.ProjectID != packet.ProjectID || result.SessionID != packet.SessionID ||
		result.FromCursor != packet.FromCursor || result.ToCursor != packet.ToCursor ||
		(!result.CursorAdvanced && !result.AlreadyApplied) {
		return errors.New("apply result does not prove the exact packet cursor was accepted")
	}
	return nil
}

func (runner *worker) runSync(ctx context.Context) error {
	mandatory := runner.job.AcceptedSyncPending
	requested, err := runner.observeCancellation(ctx)
	if err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	if requested && !mandatory {
		return runner.finishCancelled(errors.New("review cancellation requested"))
	}
	if err := runner.verifyMutationRoots(); err != nil {
		code := ApplyRecovery
		if runner.job.AcceptedSyncPending {
			code = SyncPartial
		}
		return runner.fail(code, err)
	}
	if err := runner.setPhase(Syncing); err != nil {
		if !errors.Is(err, ErrStaleRevision) {
			return runner.fail(SyncPartial, err)
		}
		if refreshErr := runner.refreshConcurrentCancellation(); refreshErr != nil {
			return runner.fail(SyncPartial, errors.Join(err, refreshErr))
		}
		if runner.job.State != CancelRequested || !mandatory {
			return runner.finishCancelled(errors.New("review cancellation requested"))
		}
		if err := runner.setPhase(Syncing); err != nil {
			return runner.fail(SyncPartial, err)
		}
	}
	stopCommitCancellation := runner.watchCommitCancellation(ctx)
	report, err := runner.options.Sync(context.WithoutCancel(ctx), syncproject.Options{
		ProjectID: runner.job.ProjectID, CWD: runner.roots.project.Path,
		DataDir: runner.roots.data.Path, GOOS: runner.options.GOOS,
		Now: runner.options.Now, Trigger: syncengine.TriggerCLI,
		Pin: runner.roots.syncPin,
		// The accepted apply just advanced the project machine ledger, so the
		// vault copy is one accepted revision behind by construction. Publish
		// it through the reviewed repair transaction instead of failing the
		// session after the ledger was already accepted.
		RepairMachineLedger: true,
	})
	cancelErr := stopCommitCancellation()
	if err != nil {
		if len(report.Conflicts) != 0 {
			return runner.fail(SyncConflict, errors.Join(err, cancelErr))
		}
		return runner.fail(SyncPartial, errors.Join(err, cancelErr))
	}
	if cancelErr != nil {
		return runner.fail(SyncPartial, cancelErr)
	}
	if err := runner.verifyMutationRoots(); err != nil {
		return runner.fail(SyncPartial, err)
	}
	if report.ProjectID != runner.job.ProjectID {
		return runner.fail(SyncPartial, errors.New("sync report belongs to another project"))
	}
	if len(report.Conflicts) != 0 {
		return runner.fail(SyncConflict, errors.New("sync reported a conflict"))
	}
	if len(report.Issues) != 0 || len(report.Errors) != 0 || report.QueueDepth != 0 ||
		report.DryRun || report.Migration.DryRun || report.Migration.Required ||
		report.Derived.State != syncengine.DerivedCurrent ||
		report.Machine.State != syncengine.MachineCurrent {
		return runner.fail(SyncPartial, errors.New("sync reported partial or blocked work"))
	}
	err = runner.clearAcceptedSyncPending()
	if err != nil {
		return runner.fail(SyncPartial, err)
	}
	return nil
}

func (runner *worker) clearAcceptedSyncPending() error {
	for range 2 {
		err := runner.update(func(job *Job) error {
			if job.State != Running && job.State != CancelRequested {
				return ErrStaleRevision
			}
			job.AcceptedSyncPending = false
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

func (runner *worker) complete() error {
	if err := runner.refreshConcurrentCancellation(); err != nil {
		return err
	}
	if runner.job.State == CancelRequested {
		return runner.finishCancelled(errors.New("review cancellation requested"))
	}
	if runner.job.SessionIndex != len(runner.job.FrozenSessions) ||
		runner.job.AcceptedSessions != len(runner.job.FrozenSessions) ||
		runner.job.CurrentPacket != (evidence.CursorBoundary{}) {
		return runner.fail(ApplyRecovery, errors.New("review worker cannot complete with unfinished frozen progress"))
	}
	completed := runner.timestamp()
	err := runner.update(func(job *Job) error {
		job.State = Completed
		job.Phase = ""
		job.UpdatedAt = completed
		job.CompletedAt = completed
		job.Owner = Owner{}
		job.Error = SafeError{}
		job.PrivateError = ""
		job.AcceptedSyncPending = false
		if job.PayloadState == PayloadPublishing || job.PayloadState == PayloadCleanupPending || job.PayloadState == PayloadRetained || job.PayloadState == PayloadApplyRecovery {
			return errors.New("review worker cannot complete with private payload cleanup pending")
		}
		return nil
	})
	if !errors.Is(err, ErrStaleRevision) {
		return err
	}
	if refreshErr := runner.refreshConcurrentCancellation(); refreshErr != nil {
		return errors.Join(err, refreshErr)
	}
	if runner.job.State == CancelRequested {
		return runner.finishCancelled(errors.New("review cancellation requested"))
	}
	return err
}

func (runner *worker) setPhase(phase Phase) error {
	return runner.update(func(job *Job) error {
		if job.State != Running && job.State != CancelRequested {
			return errors.New("review worker phase transition requires a running job")
		}
		job.Phase = phase
		job.UpdatedAt = runner.timestamp()
		return nil
	})
}

func (runner *worker) setPreCommitPhaseOrCancel(phase Phase) error {
	err := runner.setPhase(phase)
	if !errors.Is(err, ErrStaleRevision) {
		return err
	}
	if refreshErr := runner.refreshConcurrentCancellation(); refreshErr != nil {
		return errors.Join(err, refreshErr)
	}
	if runner.job.State == CancelRequested {
		return runner.finishCancelled(errors.New("review cancellation requested"))
	}
	return err
}

func (runner *worker) update(mutate func(*Job) error) error {
	job, revision, err := runner.leases.update(runner.options.Store, runner.job.ID, runner.revision, mutate)
	if err != nil {
		return err
	}
	runner.job, runner.revision = job, revision
	return nil
}

func (runner *worker) updateTerminal(mutate func(*Job) error) error {
	job, revision, err := runner.leases.updateTerminal(runner.options.Store, runner.job.ID, runner.revision, mutate)
	if err != nil {
		return err
	}
	runner.job, runner.revision = job, revision
	return nil
}

func (runner *worker) verifyMutationRoots() error {
	if err := runner.leases.verify(); err != nil {
		return err
	}
	if err := runner.roots.verify(); err != nil {
		return err
	}
	return runner.roots.syncPin.Recheck(syncproject.Options{
		ProjectID: runner.job.ProjectID, CWD: runner.roots.project.Path, DataDir: runner.roots.data.Path,
		GOOS: runner.options.GOOS, Now: runner.options.Now, Trigger: syncengine.TriggerCLI,
		Pin: runner.roots.syncPin,
	})
}

func (runner *worker) timestamp() time.Time {
	value := runner.options.Now().UTC()
	if value.IsZero() {
		value = runner.job.UpdatedAt
	}
	if value.Before(runner.job.CreatedAt) {
		value = runner.job.CreatedAt
	}
	if value.Before(runner.job.UpdatedAt) {
		value = runner.job.UpdatedAt
	}
	return value
}

func (runner *worker) fail(code ErrorCode, cause error) error {
	retainForRecovery := code == ApplyRecovery && runner.work != nil && runner.job.Phase == Applying &&
		(hasExactRetainedApplyPayloads(runner.job) || hasExactApplyRecoveryPayloads(runner.job))
	completed := runner.timestamp()
	if !retainForRecovery && runner.work != nil && runner.options.beforeCleanupBoundary != nil {
		if err := runner.options.beforeCleanupBoundary(); err != nil {
			return errors.Join(cause, err)
		}
	}
	persistErr := runner.updateTerminal(func(job *Job) error {
		job.State = Failed
		job.Phase = ""
		job.UpdatedAt = completed
		job.CompletedAt = completed
		job.Owner = Owner{}
		job.Error = SafeError{Code: code}
		job.PrivateError = boundedPrivateError(cause)
		if retainForRecovery {
			setPayloadLifecycle(job, PayloadApplyRecovery, PayloadCleanupAfterReceipt)
			job.PayloadRetainedFor = ApplyRecovery
		} else if runner.work != nil {
			setPayloadLifecycle(job, PayloadCleanupPending, PayloadCleanupByDigest)
			job.PayloadRetainedFor = ""
		}
		return nil
	})
	if persistErr != nil || retainForRecovery || runner.work == nil {
		return errors.Join(cause, persistErr)
	}
	if runner.options.afterCleanupBoundary != nil {
		if err := runner.options.afterCleanupBoundary(); err != nil {
			return errors.Join(cause, err)
		}
	}
	cleanupErr := cleanupPrivatePayloads(runner.work.inputs, runner.job)
	if cleanupErr != nil {
		return errors.Join(cause, cleanupErr)
	}
	completeErr := runner.updateTerminal(func(job *Job) error {
		if job.State != Failed || job.PayloadState != PayloadCleanupPending {
			return ErrStaleRevision
		}
		setPayloadLifecycle(job, PayloadCleanupComplete, PayloadCleanupByDigest)
		job.UpdatedAt = runner.timestamp()
		return nil
	})
	return errors.Join(cause, completeErr)
}

func boundedPrivateError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if !utf8.ValidString(value) {
		value = "review worker failed with invalid diagnostic text"
	}
	if len(value) > maxPrivateErrorBytes {
		value = value[:maxPrivateErrorBytes]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func mapAgentError(err error) ErrorCode {
	code, ok := agent.CodeOf(err)
	if !ok {
		return AgentIncompatible
	}
	switch code {
	case agent.CodeUnconfigured:
		return AgentUnconfigured
	case agent.CodeIncompatible:
		return AgentIncompatible
	case agent.CodeAuth:
		return AgentAuth
	case agent.CodeBusy:
		return AgentBusy
	case agent.CodeTimeout:
		return AgentTimeout
	case agent.CodeToolForbidden:
		return AgentToolForbidden
	case agent.CodeCancelled:
		return AgentCancelled
	default:
		return AgentIncompatible
	}
}

func (runner *worker) publishPacketPayload(body []byte, digest string) error {
	if err := runner.runPayloadCheckpoint(PayloadPacket, payloadBeforeIntentCAS); err != nil {
		return err
	}
	if err := runner.update(func(job *Job) error {
		job.PacketDigest = digest
		job.ResultDigest = ""
		job.PayloadState = PayloadPublishing
		job.PayloadRetainedFor = ""
		job.PayloadPublications = []PayloadPublication{{
			Kind: PayloadPacket, Name: packetWorkName, Digest: digest,
			State: PayloadPublishing, CleanupAuthority: PayloadCleanupNotAuthorized,
		}}
		job.UpdatedAt = runner.timestamp()
		return nil
	}); err != nil {
		return err
	}
	if err := runner.runPayloadCheckpoint(PayloadPacket, payloadAfterIntentCAS); err != nil {
		return err
	}
	if err := writePrivatePayload(runner.work.inputs, packetWorkName, body, maxPrivatePacketBytes, digest, func(stage payloadCheckpointStage) error {
		return runner.runPayloadCheckpoint(PayloadPacket, stage)
	}); err != nil {
		return err
	}
	if err := runner.runPayloadCheckpoint(PayloadPacket, payloadBeforeRetainedCAS); err != nil {
		return err
	}
	if err := runner.update(func(job *Job) error {
		if len(job.PayloadPublications) != 1 || job.PayloadPublications[0].Kind != PayloadPacket ||
			job.PayloadPublications[0].Digest != digest || job.PayloadPublications[0].State != PayloadPublishing {
			return errors.New("packet publication intent changed before retention")
		}
		job.PayloadPublications[0].State = PayloadRetained
		job.PayloadPublications[0].CleanupAuthority = PayloadCleanupNotAuthorized
		job.PayloadState = PayloadRetained
		job.Phase = Reviewing
		job.UpdatedAt = runner.timestamp()
		return nil
	}); err != nil {
		return err
	}
	return runner.runPayloadCheckpoint(PayloadPacket, payloadAfterRetainedCAS)
}

func (runner *worker) publishProposalPayload(body []byte, digest string) error {
	if err := runner.runPayloadCheckpoint(PayloadProposal, payloadBeforeIntentCAS); err != nil {
		return err
	}
	if err := runner.update(func(job *Job) error {
		if len(job.PayloadPublications) != 1 || job.PayloadPublications[0].Kind != PayloadPacket ||
			job.PayloadPublications[0].State != PayloadRetained || job.PayloadState != PayloadRetained {
			return errors.New("proposal publication requires one retained packet")
		}
		job.ResultDigest = digest
		job.PayloadPublications = append(job.PayloadPublications, PayloadPublication{
			Kind: PayloadProposal, Name: proposalWorkName, Digest: digest,
			State: PayloadPublishing, CleanupAuthority: PayloadCleanupNotAuthorized,
		})
		job.PayloadState = PayloadPublishing
		job.UpdatedAt = runner.timestamp()
		return nil
	}); err != nil {
		return err
	}
	if err := runner.runPayloadCheckpoint(PayloadProposal, payloadAfterIntentCAS); err != nil {
		return err
	}
	if err := writePrivatePayload(runner.work.inputs, proposalWorkName, body, maxPrivateProposalBytes, digest, func(stage payloadCheckpointStage) error {
		return runner.runPayloadCheckpoint(PayloadProposal, stage)
	}); err != nil {
		return err
	}
	if err := runner.runPayloadCheckpoint(PayloadProposal, payloadBeforeRetainedCAS); err != nil {
		return err
	}
	if err := runner.update(func(job *Job) error {
		if len(job.PayloadPublications) != 2 || job.PayloadPublications[1].Kind != PayloadProposal ||
			job.PayloadPublications[1].Digest != digest || job.PayloadPublications[1].State != PayloadPublishing {
			return errors.New("proposal publication intent changed before retention")
		}
		for index := range job.PayloadPublications {
			job.PayloadPublications[index].State = PayloadRetained
			job.PayloadPublications[index].CleanupAuthority = PayloadCleanupNotAuthorized
		}
		job.PayloadState = PayloadRetained
		job.UpdatedAt = runner.timestamp()
		return nil
	}); err != nil {
		return err
	}
	return runner.runPayloadCheckpoint(PayloadProposal, payloadAfterRetainedCAS)
}

func (runner *worker) runPayloadCheckpoint(kind PayloadKind, stage payloadCheckpointStage) error {
	if runner.options.payloadCheckpoint == nil {
		return nil
	}
	if err := runner.options.payloadCheckpoint(kind, stage); err != nil {
		return &payloadCheckpointFailure{kind: kind, stage: stage, err: err}
	}
	return nil
}

func (runner *worker) runPrepareCheckpoint(stage prepareCheckpointStage) error {
	if runner.options.prepareCheckpoint == nil {
		return nil
	}
	if err := runner.options.prepareCheckpoint(stage); err != nil {
		return &prepareCheckpointFailure{stage: stage, err: err}
	}
	return nil
}

func isPayloadCheckpointFailure(err error) bool {
	var failure *payloadCheckpointFailure
	return errors.As(err, &failure)
}

func digestPrivate(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func writePrivatePayload(root *os.Root, name string, body []byte, limit int64, expectedDigest string, checkpoint func(payloadCheckpointStage) error) error {
	if root == nil || len(body) == 0 || int64(len(body)) > limit || !utf8.Valid(body) || digestPrivate(body) != expectedDigest {
		return errors.New("private worker payload is invalid, oversized, or unauthenticated")
	}
	checkpointCalls := 0
	if err := atomicfile.WriteRootFileCheckedWithRollbackCheckpoint(root, name, body, 0o600, func() error {
		checkpointCalls++
		switch checkpointCalls {
		case 1:
			return checkpoint(payloadBeforeWrite)
		case 2:
			if err := checkpoint(payloadAfterWrite); err != nil {
				return err
			}
			return checkpoint(payloadBeforeRename)
		case 3:
			return checkpoint(payloadAfterRename)
		default:
			return errors.New("private payload writer exceeded checkpoint contract")
		}
	}, nil); err != nil {
		return err
	}
	if checkpointCalls != 3 {
		return errors.New("private payload writer did not reach every durable checkpoint")
	}
	if err := checkpoint(payloadBeforeVerify); err != nil {
		return err
	}
	info, found, err := regularPrivateEntry(root, name)
	if err != nil || !found {
		return errors.Join(errors.New("private worker payload was not durably published"), err)
	}
	written, err := readStablePrivatePayload(root, name, info, limit)
	if err != nil || !bytes.Equal(written, body) || digestPrivate(written) != expectedDigest {
		return errors.Join(errors.New("private worker payload failed authenticated post-write verification"), err)
	}
	return checkpoint(payloadAfterVerify)
}

func readStablePrivatePayload(root *os.Root, name string, before os.FileInfo, limit int64) ([]byte, error) {
	if before == nil || before.Size() < 1 || before.Size() > limit {
		return nil, errors.New("private worker payload exceeds its bound")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameFileMetadata(before, opened) {
		return nil, errors.New("private worker payload changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	afterHandle, statErr := file.Stat()
	afterName, nameErr := root.Lstat(name)
	if err != nil || statErr != nil || nameErr != nil || int64(len(body)) > limit ||
		!sameFileMetadata(opened, afterHandle) || !sameFileMetadata(opened, afterName) || isRedirect(afterName) {
		return nil, errors.New("private worker payload changed while reading")
	}
	return body, nil
}

func setPayloadLifecycle(job *Job, state PayloadState, authority PayloadCleanupAuthority) {
	if job == nil {
		return
	}
	job.PayloadState = state
	for index := range job.PayloadPublications {
		job.PayloadPublications[index].State = state
		job.PayloadPublications[index].CleanupAuthority = authority
	}
}

func hasExactRetainedApplyPayloads(job Job) bool {
	if job.PacketDigest == "" || job.ResultDigest == "" {
		return false
	}
	if len(job.PayloadPublications) == 0 {
		return job.PayloadState == PayloadRetained // compatibility for a pre-WAL current record
	}
	return len(job.PayloadPublications) == 2 &&
		job.PayloadPublications[0].Kind == PayloadPacket && job.PayloadPublications[0].Name == packetWorkName &&
		job.PayloadPublications[0].Digest == job.PacketDigest && job.PayloadPublications[0].State == PayloadRetained &&
		job.PayloadPublications[1].Kind == PayloadProposal && job.PayloadPublications[1].Name == proposalWorkName &&
		job.PayloadPublications[1].Digest == job.ResultDigest && job.PayloadPublications[1].State == PayloadRetained
}

func hasExactApplyRecoveryPayloads(job Job) bool {
	return job.PayloadState == PayloadApplyRecovery && job.PayloadRetainedFor == ApplyRecovery &&
		job.PacketDigest != "" && job.ResultDigest != "" && len(job.PayloadPublications) == 2 &&
		job.PayloadPublications[0].Kind == PayloadPacket && job.PayloadPublications[0].Name == packetWorkName &&
		job.PayloadPublications[0].Digest == job.PacketDigest && job.PayloadPublications[0].State == PayloadApplyRecovery &&
		job.PayloadPublications[0].CleanupAuthority == PayloadCleanupAfterReceipt &&
		job.PayloadPublications[1].Kind == PayloadProposal && job.PayloadPublications[1].Name == proposalWorkName &&
		job.PayloadPublications[1].Digest == job.ResultDigest && job.PayloadPublications[1].State == PayloadApplyRecovery &&
		job.PayloadPublications[1].CleanupAuthority == PayloadCleanupAfterReceipt
}

func cleanupPrivatePayloads(root *os.Root, job Job) error {
	if root == nil {
		return nil
	}
	if job.PayloadState != PayloadCleanupPending && job.PayloadState != PayloadCleanupComplete {
		return errors.New("private worker payload cleanup lacks durable cleanup authority")
	}
	var result error
	payloads := append([]PayloadPublication(nil), job.PayloadPublications...)
	for _, payload := range payloads {
		if payload.State != job.PayloadState || payload.CleanupAuthority != PayloadCleanupByDigest {
			return errors.New("private worker payload publication lacks durable digest cleanup authority")
		}
	}
	if len(payloads) == 0 {
		// Compatibility for jobs durably written before exact publication WAL.
		payloads = []PayloadPublication{
			{Kind: PayloadPacket, Name: packetWorkName, Digest: job.PacketDigest},
			{Kind: PayloadProposal, Name: proposalWorkName, Digest: job.ResultDigest},
		}
	}
	for _, payload := range payloads {
		if (payload.Kind != PayloadPacket || payload.Name != packetWorkName) &&
			(payload.Kind != PayloadProposal || payload.Name != proposalWorkName) {
			result = errors.Join(result, errors.New("private worker payload cleanup target is invalid"))
			continue
		}
		_, found, err := regularPrivateEntry(root, payload.Name)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if !found {
			continue
		}
		digest := strings.TrimPrefix(payload.Digest, "sha256:")
		if payload.Digest == "" || digest == payload.Digest || !lowercaseSHA256.MatchString(digest) {
			result = errors.Join(result, errors.New("private worker payload lacks an authenticated cleanup digest"))
			continue
		}
		if err := atomicfile.RemoveRootFileIfHashMatches(root, payload.Name, digest); err != nil {
			result = errors.Join(result, err)
		}
	}
	entries, err := readBoundedEntries(root, maxWorkEntries)
	if err != nil {
		return errors.Join(result, err)
	}
	parentInfo, err := root.Stat(".")
	if err != nil {
		return errors.Join(result, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !privateAtomicTempName(name) {
			continue
		}
		info, found, err := regularPrivateEntry(root, name)
		if err != nil || !found || !sameFileOwner(parentInfo, info) {
			result = errors.Join(result, errors.New("private atomic temporary file is not safely owned"), err)
			continue
		}
		digest, err := stablePrivateFileDigest(root, name, info, maxPrivateProposalBytes)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if err := atomicfile.RemoveRootFileIfHashMatches(root, name, digest); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func privateAtomicTempName(name string) bool {
	const prefix = ".session-reviewer-"
	if len(name) != len(prefix)+32 || !strings.HasPrefix(name, prefix) {
		return false
	}
	decoded, err := hex.DecodeString(name[len(prefix):])
	return err == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == name[len(prefix):]
}

func stablePrivateFileDigest(root *os.Root, name string, before os.FileInfo, limit int64) (string, error) {
	if before == nil || before.Size() < 0 || before.Size() > limit {
		return "", errors.New("private atomic temporary file exceeds cleanup bound")
	}
	file, err := root.Open(name)
	if err != nil {
		return "", err
	}
	body, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	opened, statErr := file.Stat()
	closeErr := file.Close()
	after, nameErr := root.Lstat(name)
	if readErr != nil || statErr != nil || closeErr != nil || nameErr != nil || int64(len(body)) > limit ||
		!sameFileMetadata(before, opened) || !sameFileMetadata(opened, after) || isRedirect(after) {
		return "", errors.New("private atomic temporary file changed while authenticating cleanup")
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
