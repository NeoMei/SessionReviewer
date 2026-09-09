package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/neomei/SessionReviewer/internal/annotation"
	"github.com/neomei/SessionReviewer/internal/decisions"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
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
	SchemaVersion int                     `json:"schema_version"`
	ProjectID     string                  `json:"project_id"`
	ReviewSHA256  string                  `json:"review_sha256,omitempty"`
	Decisions     []reviewv4.Decision     `json:"decisions,omitempty"`
	Candidates    []annotation.Annotation `json:"candidates,omitempty"`
	Candidate     *annotation.Annotation  `json:"candidate,omitempty"`
}

func runDecisions(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && isHelpToken(args[0]) {
		fmt.Fprint(stdout, decisionsHelp)
		return 0
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
		store, openErr := decisions.OpenStore(resolveDataDir(request.DataDir), request.ProjectID)
		if openErr != nil {
			return writeDecisionError(stdout, openErr)
		}
		values, listErr := store.List(annotation.CandidateStatus(request.Status))
		if listErr != nil {
			return writeDecisionError(stdout, listErr)
		}
		return writeDecisionJSON(stdout, stderr, decisionResult{SchemaVersion: 1, ProjectID: request.ProjectID, Candidates: values})
	}
	return writeDecisionError(stdout, ContractError{Code: "agent_unconfigured", Message: "decision extraction requires a configured proposal-only Agent"})
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
	if err := validateDecisionCandidate(index, candidate); err != nil {
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
		return decisionResult{SchemaVersion: 1, ProjectID: request.ProjectID, ReviewSHA256: request.ExpectedReviewSHA256, Candidate: &updated}, nil
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
	return decisionResult{SchemaVersion: 1, ProjectID: request.ProjectID, ReviewSHA256: resultSHA, Decisions: next.Decisions, Candidate: &updated}, nil
}

func validateDecisionCandidate(index sessionindex.Document, candidate annotation.Annotation) error {
	if candidate.GenerationID != index.GenerationID {
		return errors.New("candidate generation is stale")
	}
	active := map[string]bool{}
	for _, entry := range index.Sessions {
		if entry.SessionViewDigest != nil {
			active[*entry.SessionViewDigest] = true
		}
	}
	seenView := false
	for _, dependency := range candidate.Dependencies {
		if dependency.Kind == "session_view" {
			seenView = true
			if !active[dependency.Digest] {
				return errors.New("candidate SessionView dependency is stale")
			}
		}
	}
	if !seenView {
		return errors.New("candidate has no SessionView dependency")
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
