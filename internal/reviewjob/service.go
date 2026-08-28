package reviewjob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/agent"
	"github.com/neomei/SessionReviewer/internal/apply"
	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/config"
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

// Prepared binds one bounded packet to the exact accepted context against
// which its proposal must be generated and validated.
type Prepared struct {
	Packet   evidence.Packet
	Accepted reviewv2.Accepted
}

// PrepareRequest exposes only the frozen session boundary and a private output
// path. Implementations normally wrap prepare.Run and reviewv2.Load.
type PrepareRequest struct {
	JobID          string
	ProjectID      string
	SessionID      string
	SessionIndex   int
	AcceptedCursor evidence.CursorBoundary
	UpperBoundary  evidence.CursorBoundary
	EvidencePath   string
	ProjectRoot    string
	DataDir        string
}

type PrepareFunc func(context.Context, PrepareRequest) (Prepared, error)

// ApplyRequest contains a proposal that has already received trusted source
// accounting and passed proposal.Validate. Paths name authenticated private
// worker files and never enter Job or public status.
type ApplyRequest struct {
	JobID        string
	ProjectRoot  string
	DataDir      string
	EvidencePath string
	ProposalPath string
	Packet       evidence.Packet
	Proposal     proposal.Proposal
	Changes      ledger.ChangeSet
}

type ApplyFunc func(context.Context, ApplyRequest) (apply.Result, error)
type SyncFunc func(context.Context, syncproject.Options) (syncengine.Report, error)

// VerifiedAgentAdapter is deliberately a post-verification control-plane
// seam. Run never calls Verify, accepts a caller-constructed Capability field,
// or manufactures a capability from a version string. A production provider
// can reach generation only through a wrapper that owns successful verification.
type VerifiedAgentAdapter interface {
	VerifiedCapability() agent.Capability
	GenerateProposal(context.Context, agent.Request) (agent.Result, error)
	Cancel(context.Context) error
}

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
	Agent   VerifiedAgentAdapter
	Apply   ApplyFunc
	Sync    SyncFunc
	Pricing PricingResolver
}

type worker struct {
	options  RunOptions
	job      Job
	revision int
	roots    *workerRoots
	work     *jobWork
}

type workerRoots struct {
	project *pathguard.Directory
	vault   *pathguard.Directory
	data    *pathguard.Directory
}

func (roots *workerRoots) close() error {
	if roots == nil {
		return nil
	}
	return errors.Join(roots.project.Close(), roots.vault.Close(), roots.data.Close())
}

type jobWork struct {
	layout       *storeLayout
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
	return errors.Join(closeRoot(work.inputs), closeRoot(work.agent), closeRoot(work.jobRoot), work.layout.finish())
}

func closeRoot(root *os.Root) error {
	if root == nil {
		return nil
	}
	return root.Close()
}

// Run consumes one already frozen durable job. It holds Task 3's project and
// global leases until a terminal state is durable and then releases them in
// LeaseSet order.
func Run(ctx context.Context, options RunOptions) (retErr error) {
	if ctx == nil {
		return errors.New("review worker context is required")
	}
	if err := validateRunOptions(options); err != nil {
		return err
	}
	job, revision, found, err := options.Store.Load(options.JobID)
	if err != nil {
		return err
	}
	if !found {
		return os.ErrNotExist
	}
	if job.State != Queued && job.State != Retrying {
		return errors.New("review job is not ready for one-shot execution")
	}
	if job.Phase != Preflight {
		return errors.New("review job does not begin at preflight")
	}

	leases, err := options.Store.AcquireLeases(job.ProjectID, job.ID, options.LeaseTimeout)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, leases.Release()) }()

	runner := &worker{options: options, job: job, revision: revision}
	if err := runner.start(); err != nil {
		return err
	}
	roots, err := authenticateWorkerRoots(options, runner.job)
	if err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	runner.roots = roots
	defer func() { retErr = errors.Join(retErr, roots.close()) }()

	if len(runner.job.FrozenSessions) == 0 {
		if err := runner.runSync(ctx); err != nil {
			return err
		}
		return runner.complete()
	}
	if options.Prepare == nil || options.Agent == nil || options.Apply == nil {
		return runner.fail(AgentUnconfigured, errors.New("pending review job lacks a worker dependency"))
	}
	capability := options.Agent.VerifiedCapability()
	if err := validateVerifiedCapability(capability, runner.job); err != nil {
		return runner.fail(AgentIncompatible, err)
	}
	work, err := openJobWork(options.Store, runner.job.ID)
	if err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	runner.work = work
	defer func() { retErr = errors.Join(retErr, work.close()) }()

	for runner.job.SessionIndex < len(runner.job.FrozenSessions) {
		if err := ctx.Err(); err != nil {
			return runner.fail(AgentCancelled, err)
		}
		if err := runner.runPacket(ctx); err != nil {
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
	return nil
}

func (runner *worker) start() error {
	started := runner.timestamp()
	return runner.update(func(job *Job) error {
		if job.State != Queued && job.State != Retrying {
			return ErrStaleRevision
		}
		job.State = Running
		job.Phase = Preflight
		if job.StartedAt.IsZero() {
			job.StartedAt = started
		}
		job.UpdatedAt = started
		job.CompletedAt = time.Time{}
		job.Owner = Owner{ID: runner.options.OwnerID, AcquiredAt: started}
		job.Error = SafeError{}
		job.PrivateError = ""
		job.SyncOnlyAvailable = false
		return nil
	})
}

func authenticateWorkerRoots(options RunOptions, job Job) (*workerRoots, error) {
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
	storeRoot, err := pathguard.Open(options.Store.Root)
	if err != nil {
		_ = project.Close()
		_ = vault.Close()
		_ = data.Close()
		return nil, err
	}
	sameData := os.SameFile(data.Info(), storeRoot.Info())
	storeCloseErr := storeRoot.Close()
	if storeCloseErr != nil || !sameData {
		_ = project.Close()
		_ = vault.Close()
		_ = data.Close()
		return nil, errors.New("worker Store root and data root differ")
	}
	if err := authenticateConfiguredMapping(data, project, vault, job.ProjectID); err != nil {
		_ = project.Close()
		_ = vault.Close()
		_ = data.Close()
		return nil, err
	}
	if rootsOverlap(project, data) || rootsOverlap(vault, data) {
		_ = project.Close()
		_ = vault.Close()
		_ = data.Close()
		return nil, errors.New("worker private data root must be physically disjoint from Project and Vault")
	}
	return &workerRoots{project: project, vault: vault, data: data}, nil
}

func authenticateConfiguredMapping(data, project, vault *pathguard.Directory, projectID string) error {
	cfg, err := config.LoadRoot(data.Root, "config.toml")
	if err != nil {
		return fmt.Errorf("load worker project mapping: %w", err)
	}
	mapping, found := cfg.ProjectByID(projectID)
	if !found || mapping.VaultRoot == "" || mapping.VaultReviewPath == "" || mapping.VaultCaseMode == "" {
		return errors.New("worker project has no complete sync mapping")
	}
	configuredProject, err := pathguard.Open(mapping.Root)
	if err != nil {
		return fmt.Errorf("open configured worker Project root: %w", err)
	}
	defer configuredProject.Close()
	configuredVault, err := pathguard.Open(mapping.VaultRoot)
	if err != nil {
		return fmt.Errorf("open configured worker Vault root: %w", err)
	}
	defer configuredVault.Close()
	if !os.SameFile(project.Info(), configuredProject.Info()) || !os.SameFile(vault.Info(), configuredVault.Info()) {
		return errors.New("worker Project or Vault root does not match its configured mapping")
	}
	return nil
}

func rootsOverlap(first, second *pathguard.Directory) bool {
	return first.ContainsIdentity(second.Info()) || second.ContainsIdentity(first.Info())
}

func validateVerifiedCapability(capability agent.Capability, job Job) error {
	if capability.Provider != job.Agent.Kind || capability.Version != job.Agent.Version ||
		!capability.ProposalOnly || !capability.NoTools || !capability.ReadOnly ||
		capability.Containment != agent.ContainmentRestrictedReadOnly ||
		!capability.StructuredOutput || !capability.NativeCancellation ||
		capability.ModelProvenance != agent.ModelProvenanceUnavailable {
		return errors.New("verified Agent capability does not satisfy the proposal-only no-tools contract")
	}
	return nil
}

func openJobWork(store Store, jobID string) (_ *jobWork, retErr error) {
	layout, err := store.openLayout(false)
	if err != nil {
		return nil, err
	}
	if layout == nil || layout.missing {
		if layout != nil {
			_ = layout.close()
		}
		return nil, os.ErrNotExist
	}
	work := &jobWork{layout: layout}
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
	if err := runner.setPhase(Preparing); err != nil {
		return err
	}
	prepared, err := runner.options.Prepare(ctx, PrepareRequest{
		JobID: runner.job.ID, ProjectID: runner.job.ProjectID,
		SessionID: frozen.SessionID, SessionIndex: runner.job.SessionIndex,
		AcceptedCursor: runner.job.CurrentPacket, UpperBoundary: frozen.Upper,
		EvidencePath: runner.work.packetPath,
		ProjectRoot:  runner.roots.project.Path, DataDir: runner.roots.data.Path,
	})
	if err != nil {
		return runner.fail(ProposalRejected, err)
	}
	packet := prepared.Packet
	if err := validatePrepared(packet, prepared.Accepted, runner.job, frozen); err != nil {
		return runner.fail(ProposalRejected, err)
	}
	packetBody, err := json.Marshal(packet)
	if err != nil || len(packetBody) > maxPrivatePacketBytes {
		return runner.fail(ProposalRejected, errors.New("prepared packet cannot be encoded within its private bound"))
	}
	packetDigest, err := evidence.Digest(packet)
	if err != nil {
		return runner.fail(ProposalRejected, err)
	}
	if err := writePrivatePayload(runner.work.inputs, packetWorkName, packetBody, maxPrivatePacketBytes, packetDigest); err != nil {
		return runner.fail(ProposalRejected, err)
	}
	if err := runner.update(func(job *Job) error {
		job.Phase = Reviewing
		job.PacketDigest = packetDigest
		job.UpdatedAt = runner.timestamp()
		return nil
	}); err != nil {
		return err
	}

	bundle, err := reviewprompt.Build(reviewprompt.Input{
		Packet: packet, Accepted: prepared.Accepted.State, OutputSchema: reviewprompt.FinalProposalSchema(),
	})
	if err != nil {
		return runner.fail(ProposalRejected, err)
	}
	if err := rejectRequestPathLeak(bundle, runner.roots, runner.work); err != nil {
		return runner.fail(ProposalRejected, err)
	}
	result, err := runner.options.Agent.GenerateProposal(ctx, agent.Request{
		Prompt:           bundle.Prompt,
		OutputSchema:     bundle.OutputSchema,
		WorkingDirectory: runner.work.agentPath,
		ForbiddenRoots: []agent.ForbiddenRoot{
			{Kind: agent.ForbiddenRootProject, CanonicalPath: runner.roots.project.Path},
			{Kind: agent.ForbiddenRootVault, CanonicalPath: runner.roots.vault.Path},
		},
		Deadline: runner.timestamp().Add(runner.options.AgentTimeout),
	})
	if err != nil {
		return runner.fail(mapAgentError(err), err)
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
	proposalBody, err := json.Marshal(draft)
	if err != nil || len(proposalBody) > maxPrivateProposalBytes {
		return runner.fail(ProposalRejected, errors.New("final proposal cannot be encoded within its private bound"))
	}
	resultDigest := digestPrivate(proposalBody)
	if err := writePrivatePayload(runner.work.inputs, proposalWorkName, proposalBody, maxPrivateProposalBytes, resultDigest); err != nil {
		return runner.fail(ProposalRejected, err)
	}
	if err := runner.update(func(job *Job) error {
		job.ResultDigest = resultDigest
		job.UpdatedAt = runner.timestamp()
		return nil
	}); err != nil {
		return err
	}
	if err := runner.setPhase(Applying); err != nil {
		return err
	}
	applyResult, err := runner.options.Apply(context.WithoutCancel(ctx), ApplyRequest{
		JobID: runner.job.ID, ProjectRoot: runner.roots.project.Path, DataDir: runner.roots.data.Path,
		EvidencePath: runner.work.packetPath, ProposalPath: runner.work.proposalPath,
		Packet: packet, Proposal: draft, Changes: changes,
	})
	if err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	if err := validateApplyResult(applyResult, packet); err != nil {
		return runner.fail(ApplyRecovery, err)
	}
	if err := runner.update(func(job *Job) error {
		if job.AcceptedPackets == maxSafeInteger {
			return errors.New("accepted packet count is exhausted")
		}
		job.AcceptedPackets++
		job.CurrentPacket = packet.NextCursor
		job.UpdatedAt = runner.timestamp()
		return nil
	}); err != nil {
		return err
	}
	if err := runner.runSync(ctx); err != nil {
		return err
	}
	if err := removePrivatePayloads(runner.work.inputs); err != nil {
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
		job.UpdatedAt = runner.timestamp()
		return nil
	}); err != nil {
		return err
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

func rejectRequestPathLeak(bundle reviewprompt.Bundle, roots *workerRoots, work *jobWork) error {
	for _, path := range []string{roots.project.Path, roots.vault.Path, roots.data.Path, work.inputsPath, work.agentPath, work.packetPath, work.proposalPath} {
		if path != "" && (bytes.Contains(bundle.Prompt, []byte(path)) || bytes.Contains(bundle.OutputSchema, []byte(path))) {
			return errors.New("Agent request bytes contain a host-owned path")
		}
	}
	return nil
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
	if err := runner.setPhase(Syncing); err != nil {
		return err
	}
	report, err := runner.options.Sync(context.WithoutCancel(ctx), syncproject.Options{
		ProjectID: runner.job.ProjectID, CWD: runner.roots.project.Path,
		DataDir: runner.roots.data.Path, GOOS: runner.options.GOOS,
		Now: runner.options.Now, Trigger: syncengine.TriggerCLI,
	})
	if err != nil {
		return runner.fail(SyncPartial, err)
	}
	if report.ProjectID != runner.job.ProjectID {
		return runner.fail(SyncPartial, errors.New("sync report belongs to another project"))
	}
	if len(report.Conflicts) != 0 {
		return runner.fail(SyncConflict, errors.New("sync reported a conflict"))
	}
	if len(report.Issues) != 0 || len(report.Errors) != 0 || report.QueueDepth != 0 ||
		report.Migration.Required || report.Derived.State != syncengine.DerivedCurrent ||
		report.Machine.State != syncengine.MachineCurrent {
		return runner.fail(SyncPartial, errors.New("sync reported partial or blocked work"))
	}
	return nil
}

func (runner *worker) complete() error {
	if runner.job.SessionIndex != len(runner.job.FrozenSessions) ||
		runner.job.AcceptedSessions != len(runner.job.FrozenSessions) ||
		runner.job.CurrentPacket != (evidence.CursorBoundary{}) {
		return runner.fail(ApplyRecovery, errors.New("review worker cannot complete with unfinished frozen progress"))
	}
	completed := runner.timestamp()
	return runner.update(func(job *Job) error {
		job.State = Completed
		job.Phase = ""
		job.UpdatedAt = completed
		job.CompletedAt = completed
		job.Owner = Owner{}
		job.Error = SafeError{}
		job.PrivateError = ""
		job.SyncOnlyAvailable = false
		return nil
	})
}

func (runner *worker) setPhase(phase Phase) error {
	return runner.update(func(job *Job) error {
		if job.State != Running {
			return errors.New("review worker phase transition requires a running job")
		}
		job.Phase = phase
		job.UpdatedAt = runner.timestamp()
		return nil
	})
}

func (runner *worker) update(mutate func(*Job) error) error {
	job, revision, err := runner.options.Store.Update(runner.job.ID, runner.revision, mutate)
	if err != nil {
		return err
	}
	runner.job, runner.revision = job, revision
	return nil
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
	if runner.work != nil {
		cause = errors.Join(cause, removePrivatePayloads(runner.work.inputs))
	}
	completed := runner.timestamp()
	persistErr := runner.update(func(job *Job) error {
		job.State = Failed
		job.Phase = ""
		job.UpdatedAt = completed
		job.CompletedAt = completed
		job.Owner = Owner{}
		job.Error = SafeError{Code: code}
		job.PrivateError = boundedPrivateError(cause)
		job.SyncOnlyAvailable = job.AcceptedPackets != 0
		return nil
	})
	return errors.Join(cause, persistErr)
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

func digestPrivate(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func writePrivatePayload(root *os.Root, name string, body []byte, limit int64, expectedDigest string) error {
	if root == nil || len(body) == 0 || int64(len(body)) > limit || !utf8.Valid(body) || digestPrivate(body) != expectedDigest {
		return errors.New("private worker payload is invalid, oversized, or unauthenticated")
	}
	if err := atomicfile.WriteRootFile(root, name, body, 0o600); err != nil {
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
	return nil
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

func removePrivatePayloads(root *os.Root) error {
	if root == nil {
		return nil
	}
	var result error
	for _, name := range []string{packetWorkName, proposalWorkName} {
		_, found, err := regularPrivateEntry(root, name)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if !found {
			continue
		}
		if err := atomicfile.RemoveRoot(root, name); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}
