package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/neomei/SessionReviewer/internal/agent"
	"github.com/neomei/SessionReviewer/internal/agent/codex"
	"github.com/neomei/SessionReviewer/internal/decisions"
	"github.com/neomei/SessionReviewer/internal/problemmap"
)

type placementAgent interface {
	GenerateProposal(context.Context, agent.Request) (agent.Result, error)
	Cancel(context.Context) error
}

type problemPlacementLaunchRequest struct {
	Binary, DataRoot, ProjectID, JobID, Token string
}

var (
	problemPlacementNow        = func() time.Time { return time.Now().UTC().Round(0) }
	problemPlacementExecutable = currentReviewExecutable
	problemPlacementLaunch     = launchDetachedProblemPlacementWorker
	problemPlacementLoadAgent  = loadConfiguredProblemPlacementAgent
)

func runProblemPlacement(request ProblemRequest) (problemmap.PlacementStatus, error) {
	dataRoot := resolveDataDir(request.DataDir)
	if request.Subcommand == "status" || request.Subcommand == "cancel" {
		store, err := problemmap.OpenPlacementJobStore(dataRoot, request.ProjectID)
		if err != nil {
			return problemmap.PlacementStatus{}, err
		}
		candidates, err := problemmap.OpenStore(dataRoot, request.ProjectID)
		if err != nil {
			return problemmap.PlacementStatus{}, err
		}
		if request.Subcommand == "status" && request.JobID == "" {
			job, found, findErr := store.FindActiveCandidate(request.CandidateID)
			if findErr != nil {
				return problemmap.PlacementStatus{}, findErr
			}
			if !found {
				return problemmap.PlacementStatus{}, os.ErrNotExist
			}
			return store.Status(job.JobID)
		}
		_, _, _ = store.RecoverCompleted(request.JobID, candidates, problemPlacementNow())
		if request.Subcommand == "cancel" {
			if _, err := store.Cancel(request.JobID, request.ExpectedRevision, problemPlacementNow()); err != nil {
				return problemmap.PlacementStatus{}, err
			}
		}
		return store.Status(request.JobID)
	}
	if err := reconcileProblemPublications(context.Background(), dataRoot, request.ProjectID); err != nil {
		return problemmap.PlacementStatus{}, err
	}
	state, err := loadProblemState(request.ProjectID, request.DataDir)
	if err != nil {
		return problemmap.PlacementStatus{}, err
	}
	if state.accepted.Review.GenerationID != request.ExpectedGenerationID || state.accepted.Review.ProblemMapRevision != request.ExpectedProblemMapRevision {
		return problemmap.PlacementStatus{}, errors.New("placement request is stale")
	}
	candidates, err := problemmap.OpenStore(dataRoot, request.ProjectID)
	if err != nil {
		return problemmap.PlacementStatus{}, err
	}
	jobs, err := problemmap.OpenPlacementJobStore(dataRoot, request.ProjectID)
	if err != nil {
		return problemmap.PlacementStatus{}, err
	}
	if prior, found, findErr := jobs.FindExact(request.CandidateID, request.ExpectedCandidateRevision, request.ExpectedProblemMapRevision, request.ExpectedGenerationID); findErr != nil {
		return problemmap.PlacementStatus{}, findErr
	} else if found {
		_, _, _ = jobs.RecoverCompleted(prior.JobID, candidates, problemPlacementNow())
		return jobs.Status(prior.JobID)
	}
	candidate, err := candidates.Get(request.CandidateID)
	if err != nil {
		return problemmap.PlacementStatus{}, err
	}
	if candidate.Revision != request.ExpectedCandidateRevision || candidate.Status != problemmap.CandidatePending && candidate.Status != problemmap.CandidateKeptPending {
		return problemmap.PlacementStatus{}, problemmap.ErrCandidateRevisionConflict
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	_, err = problemPlacementLoadAgent(ctx, dataRoot)
	cancel()
	if err != nil {
		return problemmap.PlacementStatus{}, err
	}
	token, err := newProblemPlacementToken()
	if err != nil {
		return problemmap.PlacementStatus{}, err
	}
	job, cached, err := jobs.Reserve(candidate, request.ExpectedCandidateRevision, request.ExpectedProblemMapRevision, request.ExpectedGenerationID, token, problemPlacementNow())
	if err != nil {
		return problemmap.PlacementStatus{}, err
	}
	if !cached {
		binary, execErr := problemPlacementExecutable()
		if execErr == nil {
			execErr = problemPlacementLaunch(problemPlacementLaunchRequest{Binary: binary, DataRoot: dataRoot, ProjectID: request.ProjectID, JobID: job.JobID, Token: token})
		}
		if execErr != nil {
			code := "E_AGENT_UNCONFIGURED"
			_, _ = jobs.FailQueued(job.JobID, job.Revision, &code, problemPlacementNow())
			return problemmap.PlacementStatus{}, execErr
		}
	}
	return jobs.Status(job.JobID)
}

func loadConfiguredProblemPlacementAgent(ctx context.Context, dataRoot string) (placementAgent, error) {
	configuration, err := decisions.LoadAgentConfiguration(dataRoot)
	if err != nil {
		return nil, agent.NewError(agent.CodeUnconfigured, err)
	}
	adapter := codex.New()
	capability, err := adapter.Verify(ctx, configuration.Executable)
	if err != nil {
		return nil, err
	}
	if capability.Provider != configuration.Provider || capability.Version != configuration.Version || !capability.ProposalOnly || !capability.ReadOnly || !capability.StructuredOutput || !capability.NativeCancellation || capability.Containment != agent.ContainmentRestrictedReadOnly {
		return nil, agent.NewError(agent.CodeIncompatible, errors.New("configured Agent capability changed"))
	}
	return adapter, nil
}

func executeProblemPlacement(ctx context.Context, dataRoot, projectID, jobID, token string, runner placementAgent, owned func() error) error {
	jobs, err := problemmap.OpenPlacementJobStore(dataRoot, projectID)
	if err != nil {
		return err
	}
	job, err := jobs.Claim(jobID, token, problemPlacementNow(), 6*time.Minute)
	if err != nil {
		return err
	}
	if owned != nil {
		if err := owned(); err != nil {
			return finishProblemPlacementFailure(jobs, job, err)
		}
	}
	_, mapping, _, err := resolveSyncMapping("", projectID, dataRoot)
	if err != nil {
		return finishProblemPlacementFailure(jobs, job, err)
	}
	accepted, err := readProblemProjection(mapping.Root)
	if err != nil || accepted.Review.GenerationID != job.ExpectedGenerationID || accepted.Review.ProblemMapRevision != job.ExpectedProblemMapRevision {
		return finishProblemPlacementFailure(jobs, job, errors.Join(errors.New("placement inputs changed before execution"), err))
	}
	candidates, err := problemmap.OpenStore(dataRoot, projectID)
	if err != nil {
		return finishProblemPlacementFailure(jobs, job, err)
	}
	candidate, err := candidates.Get(job.CandidateID)
	if err != nil || candidate.Revision != job.ExpectedCandidateRevision {
		return finishProblemPlacementFailure(jobs, job, errors.Join(problemmap.ErrCandidateRevisionConflict, err))
	}
	prompt, schema, err := problemmap.BuildAgentPlacementPrompt(candidate, accepted.Review.ProblemNodes)
	if err != nil {
		return finishProblemPlacementFailure(jobs, job, err)
	}
	workParent := filepath.Join(dataRoot, "agent-work")
	if err := os.MkdirAll(workParent, 0o700); err != nil {
		return finishProblemPlacementFailure(jobs, job, err)
	}
	workRoot, err := os.MkdirTemp(workParent, "problem-placement-")
	if err != nil {
		return finishProblemPlacementFailure(jobs, job, err)
	}
	defer os.RemoveAll(workRoot)
	projectRoot, err := filepath.EvalSymlinks(mapping.Root)
	if err != nil {
		return finishProblemPlacementFailure(jobs, job, err)
	}
	vaultRoot, err := filepath.EvalSymlinks(mapping.VaultRoot)
	if err != nil {
		return finishProblemPlacementFailure(jobs, job, err)
	}
	monitorDone := make(chan struct{})
	go monitorProblemPlacementCancellation(ctx, jobs, jobID, runner, monitorDone)
	result, runErr := runner.GenerateProposal(ctx, agent.Request{Prompt: prompt, OutputSchema: schema, WorkingDirectory: workRoot, ForbiddenRoots: []agent.ForbiddenRoot{{Kind: agent.ForbiddenRootProject, CanonicalPath: projectRoot}, {Kind: agent.ForbiddenRootVault, CanonicalPath: vaultRoot}}, Deadline: problemPlacementNow().Add(5 * time.Minute), ProposalContract: agent.ProposalContractGenericJSON})
	close(monitorDone)
	latest, getErr := jobs.Get(jobID)
	if getErr != nil {
		return getErr
	}
	if latest.State == problemmap.PlacementCancelRequested {
		_, finishErr := jobs.Finish(jobID, latest.Revision, problemmap.PlacementCancelled, 0, nil, problemPlacementNow())
		return finishErr
	}
	if runErr != nil {
		return finishProblemPlacementFailure(jobs, latest, runErr)
	}
	proposal, err := problemmap.ParseAgentPlacementProposal(result.Proposal, candidate, accepted.Review.ProblemNodes, jobID, problemPlacementNow())
	if err != nil {
		return finishProblemPlacementFailure(jobs, latest, err)
	}
	fresh, err := loadProblemState(projectID, dataRoot)
	if err != nil || fresh.accepted.Review.GenerationID != job.ExpectedGenerationID || fresh.accepted.Review.ProblemMapRevision != job.ExpectedProblemMapRevision {
		return finishProblemPlacementFailure(jobs, latest, errors.Join(errors.New("placement inputs changed during execution"), err))
	}
	latestCandidate, err := candidates.Get(candidate.CandidateID)
	if err != nil || latestCandidate.Revision != candidate.Revision {
		return finishProblemPlacementFailure(jobs, latest, errors.Join(problemmap.ErrCandidateRevisionConflict, err))
	}
	_, err = jobs.CompleteCandidate(jobID, latest.Revision, candidates, proposal, candidate.Revision, problemPlacementNow())
	return err
}

func monitorProblemPlacementCancellation(ctx context.Context, jobs *problemmap.PlacementJobStore, jobID string, runner placementAgent, done <-chan struct{}) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = runner.Cancel(context.Background())
			return
		case <-done:
			return
		case <-ticker.C:
			job, err := jobs.Get(jobID)
			if err == nil && job.State == problemmap.PlacementCancelRequested {
				_ = runner.Cancel(context.Background())
				return
			}
		}
	}
}

func finishProblemPlacementFailure(jobs *problemmap.PlacementJobStore, job problemmap.PlacementJob, cause error) error {
	code := "E_PROPOSAL_REJECTED"
	if agentCode, ok := agent.CodeOf(cause); ok {
		code = string(agentCode)
	}
	latest, err := jobs.Get(job.JobID)
	if err != nil {
		return errors.Join(cause, err)
	}
	if latest.State == problemmap.PlacementCancelRequested {
		_, err = jobs.Finish(job.JobID, latest.Revision, problemmap.PlacementCancelled, 0, nil, problemPlacementNow())
	} else {
		_, err = jobs.Finish(job.JobID, latest.Revision, problemmap.PlacementFailed, 0, &code, problemPlacementNow())
	}
	return errors.Join(cause, err)
}

func newProblemPlacementToken() (string, error) {
	body := make([]byte, 32)
	if _, err := rand.Read(body); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func launchDetachedProblemPlacementWorker(request problemPlacementLaunchRequest) error {
	if !filepath.IsAbs(request.Binary) || !filepath.IsAbs(request.DataRoot) || !safeReviewID(request.ProjectID) || !safeReviewID(request.JobID) || len(request.Token) < 32 {
		return errors.New("detached placement launch request is invalid")
	}
	parent, child, err := os.Pipe()
	if err != nil {
		return err
	}
	defer parent.Close()
	handshakeValue, err := detachedReviewHandshakeValue(child)
	if err != nil {
		_ = child.Close()
		return err
	}
	args := []string{"problems", "placement", "worker", "--project-id", request.ProjectID, "--job-id", request.JobID, "--launch-token", request.Token, "--data-dir", request.DataRoot, "--" + privateReviewHandshakeFlag(), handshakeValue}
	command := exec.Command(request.Binary, args...)
	cleanup, err := configureDetachedReviewCommand(command, child)
	if err != nil {
		_ = child.Close()
		return err
	}
	if err := command.Start(); err != nil {
		_ = child.Close()
		_ = cleanup()
		return err
	}
	_ = child.Close()
	_ = cleanup()
	response, readErr := readDetachedReviewHandshake(parent, 10*time.Second)
	if readErr != nil || response != 1 {
		terminateDetachedReviewProcess(command)
		if readErr != nil {
			return readErr
		}
		return errors.New("detached placement worker rejected launch")
	}
	go func() { _ = command.Wait() }()
	return nil
}

func runPrivateProblemPlacementWorker(args []string) int {
	if len(args) != 11 || args[0] != "worker" || args[1] != "--project-id" || !safeReviewID(args[2]) || args[3] != "--job-id" || !safeReviewID(args[4]) || args[5] != "--launch-token" || len(args[6]) < 32 || args[7] != "--data-dir" || !filepath.IsAbs(args[8]) || args[9] != "--"+privateReviewHandshakeFlag() {
		return 2
	}
	handshake, err := inheritedReviewHandshake(args[10])
	if err != nil {
		return 2
	}
	defer handshake.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	runner, err := problemPlacementLoadAgent(ctx, args[8])
	cancel()
	if err != nil {
		_, _ = handshake.Write([]byte{2})
		return 1
	}
	err = executeProblemPlacement(context.Background(), args[8], args[2], args[4], args[6], runner, func() error {
		_, writeErr := handshake.Write([]byte{1})
		if writeErr == nil {
			writeErr = handshake.Close()
		}
		return writeErr
	})
	if err != nil {
		return 1
	}
	return 0
}
