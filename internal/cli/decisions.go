package cli

import (
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
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/agent"
	"github.com/neomei/SessionReviewer/internal/annotation"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/decisions"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/reviewjob"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

const decisionsHelp = `Read and change human-confirmed decisions and private candidates.

Usage:
  session-reviewer decisions create --project-id ID --expected-review-sha256 SHA [--data-dir PATH] --json
  session-reviewer decisions edit --project-id ID --decision-id ID --expected-decision-revision N --expected-review-sha256 SHA [--data-dir PATH] --json
  session-reviewer decisions candidates list --project-id ID [--status STATUS] [--data-dir PATH] --json
  session-reviewer decisions candidate transition ... [--data-dir PATH] --json
  session-reviewer decisions extract [status|cancel] ... [--data-dir PATH] --json
`

type decisionResult struct {
	SchemaVersion     int                         `json:"schema_version"`
	ProjectID         string                      `json:"project_id"`
	ReviewSHA256      string                      `json:"review_sha256,omitempty"`
	Decisions         []reviewv4.Decision         `json:"decisions,omitempty"`
	Candidates        []annotation.Annotation     `json:"candidates,omitempty"`
	Candidate         *annotation.Annotation      `json:"candidate,omitempty"`
	CandidateEvidence []decisionCandidateEvidence `json:"candidate_evidence,omitempty"`
}

type decisionCandidateEvidence struct {
	CandidateID  string                            `json:"candidate_id"`
	EvidenceRefs []decisions.ExtractionEvidenceRef `json:"evidence_refs"`
	ErrorCode    string                            `json:"error_code"`
}

func runDecisions(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && isHelpToken(args[0]) {
		fmt.Fprint(stdout, decisionsHelp)
		return 0
	}
	if len(args) > 0 && args[0] == "worker" {
		return runDecisionWorker(args[1:])
	}
	request, err := ParseDecisionContract(args)
	if err != nil {
		return writeDecisionError(stdout, err)
	}
	if request.Command == "create" || request.Command == "edit" {
		body, readErr := io.ReadAll(io.LimitReader(stdin, decisions.MaxInputBytes+1))
		if readErr != nil {
			return writeDecisionError(stdout, readErr)
		}
		input, parseErr := decisions.ParseDecisionInput(body)
		if parseErr != nil {
			return writeDecisionError(stdout, parseErr)
		}
		result, applyErr := applyDecisionRequest(context.Background(), request, input)
		if applyErr != nil {
			return writeDecisionError(stdout, applyErr)
		}
		return writeDecisionJSON(stdout, stderr, result)
	}
	if request.Command == "candidate" {
		body, readErr := io.ReadAll(io.LimitReader(stdin, decisions.MaxInputBytes+1))
		if readErr != nil {
			return writeDecisionError(stdout, readErr)
		}
		var input *decisions.DecisionInput
		if len(body) != 0 {
			parsed, parseErr := decisions.ParseDecisionInput(body)
			if parseErr != nil {
				return writeDecisionError(stdout, parseErr)
			}
			input = &parsed
		}
		result, transitionErr := transitionDecisionCandidate(context.Background(), request, input)
		if transitionErr != nil {
			return writeDecisionError(stdout, transitionErr)
		}
		return writeDecisionJSON(stdout, stderr, result)
	}
	if request.Command == "candidates" {
		dataRoot := resolveDataDir(request.DataDir)
		store, openErr := decisions.OpenStore(dataRoot, request.ProjectID)
		if openErr != nil {
			return writeDecisionError(stdout, openErr)
		}
		values, listErr := store.List(annotation.CandidateStatus(request.Status))
		if listErr != nil {
			return writeDecisionError(stdout, listErr)
		}
		evidence := decisionCandidateEvidenceResults(context.Background(), dataRoot, request.ProjectID, values)
		return writeDecisionJSON(stdout, stderr, decisionResult{SchemaVersion: 1, ProjectID: request.ProjectID, Candidates: values, CandidateEvidence: evidence})
	}
	if request.Command == "extract" {
		return runDecisionExtraction(request, stdout, stderr)
	}
	return writeDecisionError(stdout, errors.New("decision command is not implemented"))
}

func runDecisionExtraction(request DecisionRequest, stdout, stderr io.Writer) int {
	dataRoot := resolveDataDir(request.DataDir)
	store, err := decisions.OpenExtractionJobStore(dataRoot)
	if err != nil {
		return writeDecisionError(stdout, err)
	}
	if request.Subcommand == "status" {
		var job any
		if request.JobID != "" {
			job, err = store.LoadReconciled(request.JobID, time.Now())
		} else {
			job, err = store.LatestReconciled(request.ProjectID, time.Now())
		}
		if err != nil {
			return writeDecisionError(stdout, err)
		}
		return writeDecisionJSON(stdout, stderr, job)
	}
	if request.Subcommand == "cancel" {
		job, err := store.Cancel(request.JobID, request.ExpectedRevision, time.Now())
		if err != nil {
			return writeDecisionError(stdout, err)
		}
		return writeDecisionJSON(stdout, stderr, job)
	}
	if _, err := decisions.LoadAgentConfiguration(dataRoot); err != nil {
		return writeDecisionError(stdout, ContractError{Code: "agent_unconfigured", Message: "decision extraction requires a configured proposal-only Agent"})
	}
	memoryStore, err := memorystore.OpenReadOnly(dataRoot, request.ProjectID)
	if err != nil {
		return writeDecisionError(stdout, err)
	}
	defer memoryStore.Close()
	generationID, manifest, err := memoryStore.LoadPublished()
	if err != nil || generationID != request.ExpectedGenerationID {
		return writeDecisionError(stdout, errors.Join(ContractError{Code: ContractCodeGenerationMismatch, Message: "published generation changed"}, err))
	}
	candidateStore, err := decisions.OpenStore(dataRoot, request.ProjectID)
	if err != nil {
		return writeDecisionError(stdout, err)
	}
	if err := store.ReconcileProject(request.ProjectID, time.Now()); err != nil {
		return writeDecisionError(stdout, err)
	}
	record, err := candidateStore.Load()
	if err != nil {
		return writeDecisionError(stdout, err)
	}
	watermark := decisions.SuccessfulExtractionDependencies(record)
	newDigests := newDecisionExtractionDigests(manifest, watermark)
	job, err := decisions.StartExtraction(decisions.StartExtractionOptions{DataRoot: dataRoot, ProjectID: request.ProjectID, GenerationID: generationID, NewDependencyDigests: newDigests, Launch: func(job decisions.ExtractionJob) (int, error) { return launchDecisionExtractionWorker(job, dataRoot) }})
	if err != nil {
		return writeDecisionError(stdout, err)
	}
	return writeDecisionJSON(stdout, stderr, job)
}

func newDecisionExtractionDigests(manifest memory.GenerationManifest, watermark map[string]bool) []string {
	newDigests := []string{}
	for _, dependency := range manifest.ConversationChains {
		if !watermark[dependency.SessionViewDigest] {
			newDigests = append(newDigests, dependency.SessionViewDigest)
		}
	}
	sort.Strings(newDigests)
	if len(newDigests) > decisions.MaxExtractionDependencies {
		newDigests = newDigests[:decisions.MaxExtractionDependencies]
	}
	return newDigests
}

func runDecisionWorker(args []string) int {
	flags, err := parseContractFlags(args, map[string]bool{"job-id": true, "project-id": true, "data-dir": true, "json": true})
	if err != nil || requireFlags(flags, "job-id", "project-id", "data-dir") != nil || validateInspectDataDir(flags.values["data-dir"]) != nil {
		return 2
	}
	if err := executeDecisionWorker(context.Background(), flags.values["data-dir"], flags.values["project-id"], flags.values["job-id"]); err != nil {
		return 1
	}
	return 0
}

func executeDecisionWorker(ctx context.Context, dataRoot, projectID, jobID string) error {
	jobStore, err := decisions.OpenExtractionJobStore(dataRoot)
	if err != nil {
		return err
	}
	var job decisions.ExtractionJob
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		job, err = jobStore.Load(jobID)
		if err == nil && job.State == decisions.ExtractionRunning && job.PID == os.Getpid() {
			break
		}
		if err == nil && job.State == decisions.ExtractionQueued {
			job, err = jobStore.AuthorizeWorker(jobID, os.Getpid(), job.Revision, time.Now())
			if err == nil {
				break
			}
		}
	}
	if err != nil || job.State != decisions.ExtractionRunning || job.PID != os.Getpid() || job.ProjectID != projectID {
		return errors.New("decision extraction worker is not authorized")
	}
	configuration, err := decisions.LoadAgentConfiguration(dataRoot)
	if err != nil {
		return failDecisionWorker(jobStore, job, "agent_unconfigured")
	}
	handle, err := reviewjob.VerifyAgent(ctx, configuration.Provider, configuration.Executable)
	if err != nil {
		return failDecisionWorker(jobStore, job, "agent_unconfigured")
	}
	memoryStore, err := memorystore.OpenReadOnly(dataRoot, projectID)
	if err != nil {
		return failDecisionWorker(jobStore, job, "source_unavailable")
	}
	defer memoryStore.Close()
	generationID, manifest, err := memoryStore.LoadPublishedContext(ctx)
	if err != nil {
		return failDecisionWorker(jobStore, job, "source_unavailable")
	}
	dependencies, err := currentDecisionExtractionDependencies(job, manifest)
	if err != nil {
		return failDecisionWorker(jobStore, job, "generation_changed")
	}
	chains := map[string]conversationchain.Document{}
	for _, dependency := range dependencies {
		body, loadErr := memoryStore.LoadObjectContext(ctx, memorystore.ObjectConversationChain, dependency.Digest)
		if loadErr != nil {
			return failDecisionWorker(jobStore, job, "source_unavailable")
		}
		chain, parseErr := conversationchain.Parse(body)
		if parseErr != nil {
			return failDecisionWorker(jobStore, job, "source_invalid")
		}
		chains[dependency.SessionViewDigest] = chain
	}
	batches, schema, err := decisions.BuildExtractionBatches(projectID, dependencies, chains)
	if err != nil {
		return failDecisionWorker(jobStore, job, "proposal_rejected")
	}
	_, mapping, _, err := resolveSyncMapping("", projectID, dataRoot)
	if err != nil {
		return failDecisionWorker(jobStore, job, "mapping_changed")
	}
	workRoot, err := os.MkdirTemp(filepath.Join(dataRoot, "decision-extraction-jobs"), ".work-")
	if err != nil {
		return failDecisionWorker(jobStore, job, "worker_failed")
	}
	defer os.RemoveAll(workRoot)
	candidateAt := time.Now()
	candidates := []annotation.Annotation{}
	candidatesByID := map[string]annotation.Annotation{}
	for _, batch := range batches {
		deadline := time.Now().Add(5 * time.Minute)
		result, runErr := reviewjob.GenerateProposal(ctx, handle, agent.Request{Prompt: batch.Prompt, OutputSchema: schema, WorkingDirectory: workRoot, ForbiddenRoots: []agent.ForbiddenRoot{{Kind: agent.ForbiddenRootProject, CanonicalPath: mapping.Root}, {Kind: agent.ForbiddenRootVault, CanonicalPath: mapping.VaultRoot}}, Deadline: deadline, ProposalContract: agent.ProposalContractGenericJSON})
		if runErr != nil {
			return failDecisionWorker(jobStore, job, "agent_failed")
		}
		parsed, parseErr := decisions.ParseExtractionProposal(result.Proposal, projectID, generationID, job.JobID, batch.Dependencies, batch.Evidence, candidateAt)
		if parseErr != nil {
			return failDecisionWorker(jobStore, job, "proposal_rejected")
		}
		for _, candidate := range parsed {
			if existing, ok := candidatesByID[candidate.ID]; ok {
				merged, mergeErr := mergeDecisionCandidateEvidence(existing, candidate)
				if mergeErr != nil {
					return failDecisionWorker(jobStore, job, "proposal_rejected")
				}
				candidatesByID[candidate.ID] = merged
				for index := range candidates {
					if candidates[index].ID == candidate.ID {
						candidates[index] = merged
						break
					}
				}
				continue
			}
			candidatesByID[candidate.ID] = candidate
			candidates = append(candidates, candidate)
			if len(candidates) > 128 {
				return failDecisionWorker(jobStore, job, "proposal_rejected")
			}
		}
	}
	candidateStore, err := decisions.OpenStore(dataRoot, projectID)
	if err != nil {
		return failDecisionWorker(jobStore, job, "store_failed")
	}
	run := annotation.Run{RunID: job.JobID, ProjectID: projectID, Status: "completed", ExtractorVersion: decisions.ExtractorVersion, PromptSchemaVersion: decisions.PromptSchemaVersion, DependencyDigests: append([]string(nil), job.DependencyDigests...), CreatedAt: job.CreatedAt, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	_, err = jobStore.CompleteExtraction(job.JobID, job.Revision, candidateStore, run, candidates, time.Now())
	if errors.Is(err, decisions.ErrExtractionJobRevisionConflict) {
		return errors.New("decision extraction was cancelled before candidate commit")
	}
	if errors.Is(err, decisions.ErrExtractionJobProjectionPending) {
		return nil
	}
	if err != nil {
		return failDecisionWorker(jobStore, job, "store_failed")
	}
	return nil
}

func currentDecisionExtractionDependencies(job decisions.ExtractionJob, manifest memory.GenerationManifest) ([]memory.ConversationChainDependency, error) {
	requested := map[string]bool{}
	for _, digest := range job.DependencyDigests {
		requested[digest] = true
	}
	result := make([]memory.ConversationChainDependency, 0, len(requested))
	for _, dependency := range manifest.ConversationChains {
		if requested[dependency.SessionViewDigest] {
			result = append(result, dependency)
			delete(requested, dependency.SessionViewDigest)
		}
	}
	if len(requested) != 0 {
		return nil, errors.New("decision extraction dependency changed")
	}
	return result, nil
}

func mergeDecisionCandidateEvidence(left, right annotation.Annotation) (annotation.Annotation, error) {
	leftBase, rightBase := left, right
	leftBase.Dependencies, rightBase.Dependencies = nil, nil
	if !reflect.DeepEqual(leftBase, rightBase) {
		return annotation.Annotation{}, errors.New("candidate changed across prompt batches")
	}
	seen := map[string]bool{}
	merged := left
	merged.Dependencies = append([]annotation.Dependency{}, left.Dependencies...)
	for _, dependency := range merged.Dependencies {
		seen[dependency.Kind+"\x00"+dependency.RevisionID+"\x00"+dependency.Digest] = true
	}
	for _, dependency := range right.Dependencies {
		key := dependency.Kind + "\x00" + dependency.RevisionID + "\x00" + dependency.Digest
		if !seen[key] {
			seen[key] = true
			merged.Dependencies = append(merged.Dependencies, dependency)
		}
	}
	if len(merged.Dependencies) > 256 {
		return annotation.Annotation{}, errors.New("candidate evidence exceeds bound")
	}
	return merged, nil
}

func failDecisionWorker(store *decisions.ExtractionJobStore, job decisions.ExtractionJob, code string) error {
	prior := job.Revision
	job.State, job.PID, job.ErrorCode, job.Revision, job.UpdatedAt = decisions.ExtractionFailed, 0, code, prior+1, time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.CompareAndSwap(job, prior); err != nil {
		return err
	}
	return errors.New(code)
}

func applyDecisionRequest(ctx context.Context, request DecisionRequest, input decisions.DecisionInput) (decisionResult, error) {
	dataRoot := resolveDataDir(request.DataDir)
	_, mapping, _, err := resolveSyncMapping("", request.ProjectID, dataRoot)
	if err != nil {
		return decisionResult{}, err
	}
	owner, err := publicationlock.Acquire(dataRoot, request.ProjectID, 10*time.Second)
	if err != nil {
		return decisionResult{}, err
	}
	defer owner.Release()
	pubOpts := publication.Options{ProjectID: request.ProjectID, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}
	if err := publication.RecoverMarkdownLocked(ctx, pubOpts, owner); err != nil {
		return decisionResult{}, err
	}
	read, err := syncproject.ReadMarkdownForScan(ctx, syncproject.Options{ProjectID: request.ProjectID, CWD: mapping.Root, DataDir: dataRoot, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI}, owner)
	if err != nil {
		return decisionResult{}, err
	}
	if decisionBareSHA(read.ProjectExpected[reviewv2.ReviewRelativePath]) != request.ExpectedReviewSHA256 {
		return decisionResult{}, ContractError{Code: ContractCodeReviewPreimageConflict, Message: "review preimage changed"}
	}
	next := read.Pending.Presentation
	if request.Command == "create" {
		id := nextDecisionID(next.Decisions)
		next, err = decisions.CreateDecision(next, id, input)
	} else {
		next, err = decisions.EditDecision(next, request.DecisionID, request.ExpectedDecisionRevision, input)
	}
	if err != nil {
		return decisionResult{}, err
	}
	index, err := sessionindex.Parse(read.ProjectExpected[presentation.SessionIndexRelativePath])
	if err != nil {
		return decisionResult{}, err
	}
	plan, err := presentation.RenderDecisionOperation(presentation.DecisionOperationInput{Presentation: next, Ledger: read.OldAccepted.Ledger, Index: index, Pending: read.Pending.Documents, ExpectedFiles: read.ProjectExpected})
	if err != nil {
		return decisionResult{}, err
	}
	edit := syncproject.MarkdownSyncPlan{Plan: plan, Index: read.ProjectExpected[presentation.SessionIndexRelativePath], ExpectedGenerationID: read.ExpectedGenerationID, ExpectedIndexDigest: read.ExpectedIndexDigest, VaultExpected: read.VaultExpected, ExpectedReceiptRevision: read.ExpectedReceiptRevision, ExpectedBaseDigest: read.ExpectedBaseDigest}
	if _, err := publication.PublishMarkdownEditLocked(ctx, pubOpts, edit, owner); err != nil {
		return decisionResult{}, err
	}
	return decisionResult{SchemaVersion: 1, ProjectID: request.ProjectID, ReviewSHA256: decisionBareSHA(plan.Files[0].Desired), Decisions: next.Decisions}, nil
}

func transitionDecisionCandidate(ctx context.Context, request DecisionRequest, edited *decisions.DecisionInput) (decisionResult, error) {
	dataRoot := resolveDataDir(request.DataDir)
	_, mapping, _, err := resolveSyncMapping("", request.ProjectID, dataRoot)
	if err != nil {
		return decisionResult{}, err
	}
	owner, err := publicationlock.Acquire(dataRoot, request.ProjectID, 10*time.Second)
	if err != nil {
		return decisionResult{}, err
	}
	defer owner.Release()
	pubOpts := publication.Options{ProjectID: request.ProjectID, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}
	if err := publication.RecoverMarkdownLocked(ctx, pubOpts, owner); err != nil {
		return decisionResult{}, err
	}
	read, err := syncproject.ReadMarkdownForScan(ctx, syncproject.Options{ProjectID: request.ProjectID, CWD: mapping.Root, DataDir: dataRoot, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI}, owner)
	if err != nil {
		return decisionResult{}, err
	}
	if decisionBareSHA(read.ProjectExpected[reviewv2.ReviewRelativePath]) != request.ExpectedReviewSHA256 {
		return decisionResult{}, ContractError{Code: ContractCodeReviewPreimageConflict, Message: "review preimage changed"}
	}
	store, err := decisions.OpenStore(dataRoot, request.ProjectID)
	if err != nil {
		return decisionResult{}, err
	}
	candidate, err := store.Get(request.CandidateID)
	if err != nil {
		return decisionResult{}, err
	}
	if candidate.Revision != request.ExpectedRevision {
		return decisionResult{}, decisions.ErrCandidateRevisionConflict
	}
	index, err := sessionindex.Parse(read.ProjectExpected[presentation.SessionIndexRelativePath])
	if err != nil {
		return decisionResult{}, err
	}
	if err := validateDecisionCandidateFresh(index, candidate); err != nil {
		if candidate.Status == annotation.CandidatePending || candidate.Status == annotation.CandidateIgnored {
			_, _ = store.Transition(candidate.ID, candidate.Revision, "stale", "", time.Now())
		}
		return decisionResult{}, err
	}
	chains, err := loadDecisionCandidateChains(ctx, dataRoot, request.ProjectID, []annotation.Annotation{candidate})
	if err != nil {
		return decisionResult{}, err
	}
	evidenceRefs, err := resolveDecisionCandidateEvidence(candidate, chains)
	if err != nil {
		if candidate.Status == annotation.CandidatePending || candidate.Status == annotation.CandidateIgnored {
			_, _ = store.Transition(candidate.ID, candidate.Revision, "stale", "", time.Now())
		}
		return decisionResult{}, err
	}
	if request.Action != "confirm" {
		updated, err := store.Transition(candidate.ID, candidate.Revision, request.Action, "", time.Now())
		if err != nil {
			return decisionResult{}, err
		}
		return decisionResult{SchemaVersion: 1, ProjectID: request.ProjectID, ReviewSHA256: request.ExpectedReviewSHA256, Candidate: &updated, CandidateEvidence: []decisionCandidateEvidence{{CandidateID: updated.ID, EvidenceRefs: evidenceRefs}}}, nil
	}
	if candidate.Status != annotation.CandidatePending || candidate.EntityID == nil {
		return decisionResult{}, errors.New("only a pending decision candidate can be confirmed")
	}
	input := edited
	if input == nil {
		parsed, parseErr := decisions.ParseDecisionInput([]byte(candidate.Text))
		if parseErr != nil {
			return decisionResult{}, parseErr
		}
		input = &parsed
	}
	if err := validateDecisionCandidate(index, candidate, *input); err != nil {
		return decisionResult{}, err
	}
	formalID := *candidate.EntityID
	next := read.Pending.Presentation
	alreadyPublished := false
	for _, value := range next.Decisions {
		if value.ID == formalID {
			alreadyPublished = decisionMatchesCandidate(value, *input)
			if !alreadyPublished {
				return decisionResult{}, errors.New("candidate entity ID already belongs to another decision")
			}
			break
		}
	}
	resultSHA := request.ExpectedReviewSHA256
	if !alreadyPublished {
		next, err = decisions.ConfirmCandidateDecision(next, formalID, *input)
		if err != nil {
			return decisionResult{}, err
		}
		plan, renderErr := presentation.RenderDecisionOperation(presentation.DecisionOperationInput{Presentation: next, Ledger: read.OldAccepted.Ledger, Index: index, Pending: read.Pending.Documents, ExpectedFiles: read.ProjectExpected})
		if renderErr != nil {
			return decisionResult{}, renderErr
		}
		edit := syncproject.MarkdownSyncPlan{Plan: plan, Index: read.ProjectExpected[presentation.SessionIndexRelativePath], ExpectedGenerationID: read.ExpectedGenerationID, ExpectedIndexDigest: read.ExpectedIndexDigest, VaultExpected: read.VaultExpected, ExpectedReceiptRevision: read.ExpectedReceiptRevision, ExpectedBaseDigest: read.ExpectedBaseDigest}
		if _, publishErr := publication.PublishMarkdownEditLocked(ctx, pubOpts, edit, owner); publishErr != nil {
			return decisionResult{}, publishErr
		}
		resultSHA = decisionBareSHA(plan.Files[0].Desired)
	}
	updated, err := store.Transition(candidate.ID, candidate.Revision, "confirm", formalID, time.Now())
	if err != nil {
		return decisionResult{}, err
	}
	return decisionResult{SchemaVersion: 1, ProjectID: request.ProjectID, ReviewSHA256: resultSHA, Decisions: next.Decisions, Candidate: &updated, CandidateEvidence: []decisionCandidateEvidence{{CandidateID: updated.ID, EvidenceRefs: evidenceRefs}}}, nil
}

func validateDecisionCandidateFresh(index sessionindex.Document, candidate annotation.Annotation) error {
	active := map[string]sessionindex.Entry{}
	for _, entry := range index.Sessions {
		if entry.SessionViewDigest != nil {
			active[*entry.SessionViewDigest] = entry
		}
	}
	seenView := false
	for _, dependency := range candidate.Dependencies {
		if dependency.Kind == "session_view" {
			seenView = true
			if _, ok := active[dependency.Digest]; !ok || dependency.RevisionID != "view-"+strings.TrimPrefix(dependency.Digest, "sha256:")[:16] {
				return errors.New("candidate SessionView dependency is stale")
			}
		}
	}
	if !seenView {
		return errors.New("candidate has no SessionView dependency")
	}
	return nil
}

func decisionCandidateEvidenceResults(ctx context.Context, dataRoot, projectID string, candidates []annotation.Annotation) []decisionCandidateEvidence {
	result := make([]decisionCandidateEvidence, 0, len(candidates))
	chains, err := loadDecisionCandidateChains(ctx, dataRoot, projectID, candidates)
	for _, candidate := range candidates {
		entry := decisionCandidateEvidence{CandidateID: candidate.ID, EvidenceRefs: []decisions.ExtractionEvidenceRef{}}
		if err == nil {
			entry.EvidenceRefs, err = resolveDecisionCandidateEvidence(candidate, chains)
		}
		if err != nil {
			entry.ErrorCode = "candidate_stale"
			err = nil
		}
		result = append(result, entry)
	}
	return result
}

func loadDecisionCandidateChains(ctx context.Context, dataRoot, projectID string, candidates []annotation.Annotation) (map[string]conversationchain.Document, error) {
	store, err := memorystore.OpenReadOnly(dataRoot, projectID)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	_, manifest, err := store.LoadPublishedContext(ctx)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, candidate := range candidates {
		for _, dependency := range candidate.Dependencies {
			if dependency.Kind == "session_view" || dependency.Kind == "source_turn" {
				wanted[dependency.Digest] = true
			}
		}
	}
	chains := make(map[string]conversationchain.Document, len(wanted))
	for _, dependency := range manifest.ConversationChains {
		if !wanted[dependency.SessionViewDigest] {
			continue
		}
		body, loadErr := store.LoadObjectContext(ctx, memorystore.ObjectConversationChain, dependency.Digest)
		if loadErr != nil {
			return nil, loadErr
		}
		chain, parseErr := conversationchain.Parse(body)
		if parseErr != nil || chain.Provider != dependency.Provider || chain.SessionID != dependency.SessionID || chain.SessionViewDigest != dependency.SessionViewDigest {
			return nil, errors.Join(errors.New("candidate conversation chain is not authenticated"), parseErr)
		}
		chains[dependency.SessionViewDigest] = chain
	}
	return chains, nil
}

func resolveDecisionCandidateEvidence(candidate annotation.Annotation, chains map[string]conversationchain.Document) ([]decisions.ExtractionEvidenceRef, error) {
	views := map[string]bool{}
	for _, dependency := range candidate.Dependencies {
		if dependency.Kind == "session_view" {
			views[dependency.Digest] = true
		}
	}
	result := []decisions.ExtractionEvidenceRef{}
	for _, dependency := range candidate.Dependencies {
		if dependency.Kind != "source_turn" {
			continue
		}
		chain, ok := chains[dependency.Digest]
		if !ok || !views[dependency.Digest] {
			return nil, errors.New("candidate source-turn dependency is stale")
		}
		found := false
		for _, turn := range chain.TurnUnits {
			if turn.UserMessage.RevisionID == dependency.RevisionID {
				result = append(result, decisions.ExtractionEvidenceRef{Provider: chain.Provider, SessionID: chain.SessionID, SessionViewDigest: chain.SessionViewDigest, TurnUnitID: turn.TurnUnitID, RevisionID: dependency.RevisionID})
				found = true
			}
			for _, answer := range turn.AssistantMessages {
				if answer.RevisionID == dependency.RevisionID {
					result = append(result, decisions.ExtractionEvidenceRef{Provider: chain.Provider, SessionID: chain.SessionID, SessionViewDigest: chain.SessionViewDigest, TurnUnitID: turn.TurnUnitID, RevisionID: dependency.RevisionID})
					found = true
				}
			}
		}
		if !found {
			return nil, errors.New("candidate source-turn revision is absent from its authenticated chain")
		}
	}
	if len(result) == 0 {
		return nil, errors.New("candidate has no source-turn evidence")
	}
	return result, nil
}

func validateDecisionCandidate(index sessionindex.Document, candidate annotation.Annotation, input decisions.DecisionInput) error {
	if err := validateDecisionCandidateFresh(index, candidate); err != nil {
		return err
	}
	wantKind := strings.TrimSuffix(candidate.AnnotationKind, "_candidate")
	if wantKind != input.Kind || wantKind != "decision" && wantKind != "agreement" {
		return errors.New("candidate kind does not match the confirmed decision")
	}
	activeByDigest := map[string]sessionindex.Entry{}
	for _, entry := range index.Sessions {
		if entry.SessionViewDigest != nil {
			activeByDigest[*entry.SessionViewDigest] = entry
		}
	}
	bound := map[string]bool{}
	for _, dependency := range candidate.Dependencies {
		if entry, ok := activeByDigest[dependency.Digest]; ok {
			bound[entry.Provider+"\x00"+entry.SessionID] = true
		}
	}
	if len(input.SessionRefs) == 0 {
		return errors.New("confirmed candidate has no Session reference")
	}
	for _, ref := range input.SessionRefs {
		if !bound[ref.Provider+"\x00"+ref.SessionID] {
			return errors.New("confirmed candidate cites a Session outside its authenticated dependencies")
		}
	}
	return nil
}

func decisionMatchesCandidate(value reviewv4.Decision, input decisions.DecisionInput) bool {
	if value.Provenance != "ai_candidate_confirmed" {
		return false
	}
	want := decisions.DecisionInput{SchemaVersion: 1, Kind: value.Kind, OccurredAt: value.OccurredAt, Title: value.Title, Rationale: value.Rationale, Impact: value.Impact, Status: value.Status, ReevaluateWhen: value.ReevaluateWhen, Supersedes: value.Supersedes, MilestoneIDs: value.MilestoneIDs, SessionRefs: value.SessionRefs, Pinned: value.Pinned}
	left, _ := json.Marshal(want)
	right, _ := json.Marshal(input)
	return string(left) == string(right)
}

func nextDecisionID(values []reviewv4.Decision) string {
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	sum := sha256.Sum256([]byte(stamp))
	id := "decision-" + hex.EncodeToString(sum[:8])
	used := map[string]bool{}
	for _, value := range values {
		used[value.ID] = true
	}
	for used[id] {
		sum = sha256.Sum256(sum[:])
		id = "decision-" + hex.EncodeToString(sum[:8])
	}
	return id
}

func decisionBareSHA(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func writeDecisionError(output io.Writer, err error) int {
	code := "decision_operation_failed"
	var contract ContractError
	if errors.As(err, &contract) {
		code = contract.Code
	} else if errors.Is(err, decisions.ErrDecisionRevisionConflict) {
		code = "decision_revision_conflict"
	} else if errors.Is(err, decisions.ErrCandidateRevisionConflict) {
		code = ContractCodeCandidateRevisionConflict
	}
	_ = json.NewEncoder(output).Encode(map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
	return 1
}

func writeDecisionJSON(output, diagnostic io.Writer, value any) int {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintln(diagnostic, "decision output failed")
		return 1
	}
	return 0
}
