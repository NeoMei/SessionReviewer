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
	"runtime"
	"sort"
	"time"

	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/problemmap"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

const problemsHelp = `Read and change the human-confirmed problem graph.

Usage:
  session-reviewer problems candidates list --project-id ID [--status STATUS] --json
  session-reviewer problems candidate transition ... --json
  session-reviewer problems move ... --json
  session-reviewer problems reorder ... --json
  session-reviewer problems placement request|status|cancel ... --json
`

type problemResult struct {
	SchemaVersion      int                     `json:"schema_version"`
	ProjectID          string                  `json:"project_id"`
	ProblemMapRevision int                     `json:"problem_map_revision"`
	ReviewSHA256       string                  `json:"review_sha256"`
	Candidate          *problemmap.Candidate   `json:"candidate,omitempty"`
	Problems           []reviewv4.ProblemNode  `json:"problems"`
	Candidates         []problemmap.Candidate  `json:"candidates,omitempty"`
	MovePreview        *problemmap.MovePreview `json:"move_preview,omitempty"`
}

func runProblems(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 2 && args[0] == "placement" && args[1] == "worker" {
		return runPrivateProblemPlacementWorker(args[1:])
	}
	if len(args) == 1 && isHelpToken(args[0]) {
		fmt.Fprint(stdout, problemsHelp)
		return 0
	}
	request, err := ParseProblemContract(args)
	if len(args) > 0 && (args[0] == "reorder" || args[0] == "create" || args[0] == "edit") {
		body, readErr := io.ReadAll(io.LimitReader(stdin, MaxDecisionInputBytes+1))
		if readErr != nil {
			return writeProblemError(stdout, readErr)
		}
		var current []string
		if args[0] == "reorder" {
			base, parseErr := parseProblemReorder(args[1:])
			if parseErr != nil {
				return writeProblemError(stdout, parseErr)
			}
			state, loadErr := loadProblemState(base.ProjectID, base.DataDir)
			if loadErr != nil {
				return writeProblemError(stdout, loadErr)
			}
			current = directProblemChildren(state.accepted.Review.ProblemNodes, base.ParentID)
		}
		request, err = ParseProblemContractWithInput(args, body, current)
	}
	if err != nil {
		return writeProblemError(stdout, err)
	}
	if request.Command == "placement" {
		status, placementErr := runProblemPlacement(request)
		if request.Subcommand == "status" && request.CandidateID != "" && errors.Is(placementErr, os.ErrNotExist) {
			return writeProblemJSON(stdout, stderr, nil)
		}
		if placementErr != nil {
			return writeProblemError(stdout, placementErr)
		}
		return writeProblemJSON(stdout, stderr, status)
	}
	if request.Command == "candidates" {
		dataRoot := resolveDataDir(request.DataDir)
		state, loadErr := loadProblemState(request.ProjectID, request.DataDir)
		if loadErr != nil {
			return writeProblemError(stdout, loadErr)
		}
		store, openErr := problemmap.OpenStore(dataRoot, request.ProjectID)
		if openErr != nil {
			return writeProblemError(stdout, openErr)
		}
		values, listErr := store.List(problemmap.CandidateStatus(request.Status))
		if listErr != nil {
			return writeProblemError(stdout, listErr)
		}
		return writeProblemJSON(stdout, stderr, problemResult{SchemaVersion: 1, ProjectID: request.ProjectID, ProblemMapRevision: state.accepted.Review.ProblemMapRevision, ReviewSHA256: state.accepted.Ledger.ReviewSHA256, Problems: state.accepted.Review.ProblemNodes, Candidates: values})
	}
	result, applyErr := applyProblemRequest(context.Background(), request)
	if applyErr != nil {
		return writeProblemError(stdout, applyErr)
	}
	return writeProblemJSON(stdout, stderr, result)
}

type problemState struct {
	dataRoot    string
	mappingRoot string
	accepted    reviewv4.Accepted
}

func loadProblemState(projectID, requestedDataRoot string) (problemState, error) {
	dataRoot := resolveDataDir(requestedDataRoot)
	if dataRoot == "" {
		return problemState{}, errors.New("SessionReviewer data directory is unavailable")
	}
	root, mapping, _, err := resolveSyncMapping("", projectID, dataRoot)
	if err != nil {
		return problemState{}, err
	}
	_ = root
	read, err := readProblemProjection(mapping.Root)
	if err != nil {
		return problemState{}, err
	}
	return problemState{dataRoot: dataRoot, mappingRoot: mapping.Root, accepted: read}, nil
}

func readProblemProjection(root string) (reviewv4.Accepted, error) {
	paths := []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath, presentation.SessionIndexRelativePath}
	bodies := make([][]byte, len(paths))
	for index, relative := range paths {
		body, err := osReadFile(problemJoin(root, relative))
		if err != nil {
			return reviewv4.Accepted{}, err
		}
		bodies[index] = body
	}
	return reviewv4.LoadProjection(bodies[0], bodies[1], bodies[2], bodies[3])
}

var osReadFile = func(path string) ([]byte, error) { return os.ReadFile(path) }
var problemJoin = filepath.Join

func applyProblemRequest(ctx context.Context, request ProblemRequest) (problemResult, error) {
	dataRoot := resolveDataDir(request.DataDir)
	root, mapping, _, err := resolveSyncMapping("", request.ProjectID, dataRoot)
	if err != nil {
		return problemResult{}, err
	}
	_ = root
	owner, err := publicationlock.Acquire(dataRoot, request.ProjectID, 10*time.Second)
	if err != nil {
		return problemResult{}, err
	}
	defer owner.Release()
	pubOpts := publication.Options{ProjectID: request.ProjectID, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}
	if err := publication.RecoverMarkdownLocked(ctx, pubOpts, owner); err != nil {
		return problemResult{}, err
	}
	read, err := syncproject.ReadMarkdownForScan(ctx, syncproject.Options{ProjectID: request.ProjectID, CWD: mapping.Root, DataDir: dataRoot, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI}, owner)
	if err != nil {
		return problemResult{}, err
	}
	p := read.Pending.Presentation
	if request.ExpectedProblemMapRevision != p.ProblemMapRevision {
		return problemResult{}, ContractError{Code: ContractCodeGenerationMismatch, Message: "problem map revision changed"}
	}
	if problemBareSHA(read.ProjectExpected[reviewv2.ReviewRelativePath]) != request.ExpectedReviewSHA256 {
		return problemResult{}, ContractError{Code: ContractCodeReviewPreimageConflict, Message: "review preimage changed"}
	}
	graph := problemmap.Graph{ProjectID: p.ProjectID, Revision: p.ProblemMapRevision, Nodes: p.ProblemNodes}
	var candidate *problemmap.Candidate
	var preview *problemmap.MovePreview
	if request.Command == "create" {
		store, openErr := problemmap.OpenStore(dataRoot, request.ProjectID)
		if openErr != nil {
			return problemResult{}, openErr
		}
		value := problemmap.NewHumanCandidate(request.ProjectID, request.Question, time.Now())
		if prior, getErr := store.Get(value.CandidateID); getErr == nil {
			value = prior
		} else if !errors.Is(getErr, os.ErrNotExist) {
			return problemResult{}, getErr
		} else if err := store.CompareAndSwap(value, 0); err != nil {
			return problemResult{}, err
		}
		return problemResult{SchemaVersion: 1, ProjectID: p.ProjectID, ProblemMapRevision: p.ProblemMapRevision, ReviewSHA256: request.ExpectedReviewSHA256, Problems: p.ProblemNodes, Candidate: &value}, nil
	}
	if request.Command == "candidate" {
		store, openErr := problemmap.OpenStore(dataRoot, request.ProjectID)
		if openErr != nil {
			return problemResult{}, openErr
		}
		value, getErr := store.Get(request.CandidateID)
		if getErr != nil {
			return problemResult{}, getErr
		}
		if value.Revision != request.ExpectedCandidateRevision {
			return problemResult{}, problemmap.ErrCandidateRevisionConflict
		}
		switch request.Action {
		case "apply_root", "apply_child", "apply_sibling", "merge":
			action := map[string]problemmap.ApplyAction{"apply_root": problemmap.ApplyRoot, "apply_child": problemmap.ApplyChild, "apply_sibling": problemmap.ApplySibling, "merge": problemmap.ApplyMerge}[request.Action]
			graph, err = problemmap.ApplyCandidate(graph, value, action, request.TargetProblemID, time.Now())
		case "keep_pending":
			value.Status = problemmap.CandidateKeptPending
		case "dismiss":
			value.Status = problemmap.CandidateDismissed
		case "restore":
			value.Status = problemmap.CandidatePending
		}
		if err != nil {
			return problemResult{}, err
		}
		if request.Action == "keep_pending" || request.Action == "dismiss" || request.Action == "restore" {
			value.Revision++
			value.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			if err := store.CompareAndSwap(value, request.ExpectedCandidateRevision); err != nil {
				return problemResult{}, err
			}
			candidate = &value
			return problemResult{SchemaVersion: 1, ProjectID: p.ProjectID, ProblemMapRevision: p.ProblemMapRevision, ReviewSHA256: request.ExpectedReviewSHA256, Problems: p.ProblemNodes, Candidate: candidate}, nil
		}
		if request.Action == "merge" {
			value.Status = problemmap.CandidateMerged
		} else {
			value.Status = problemmap.CandidateApplied
		}
		value.Revision++
		value.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		candidate = &value
	} else if request.Command == "move" {
		value, previewErr := problemmap.PreviewMove(graph.Nodes, request.ProblemID, request.NewParentID)
		if previewErr != nil {
			return problemResult{}, previewErr
		}
		preview = &value
		graph, err = problemmap.Move(graph, request.ProblemID, request.NewParentID, request.ExpectedProblemRevision)
	} else if request.Command == "reorder" {
		graph, err = problemmap.Reorder(graph, request.ParentID, request.OrderedChildIDs)
	} else if request.Command == "edit" {
		graph, err = problemmap.EditHumanFields(graph, request.ProblemID, request.ExpectedProblemRevision, problemmap.HumanFields{Question: request.Question, CurrentConclusion: request.CurrentConclusion, CompletionCriterion: request.CompletionCriterion})
	} else if request.Command == "state" {
		state := map[string]string{"resolve": "resolved", "reopen": "in_progress"}[request.Action]
		graph, err = problemmap.SetWorkflowState(graph, request.ProblemID, request.ExpectedProblemRevision, state)
	}
	if err != nil {
		return problemResult{}, err
	}
	p.Revision++
	p.ProblemMapRevision = graph.Revision
	p.ProblemNodes = graph.Nodes
	p.ProblemRootIDs = problemRootIDs(graph.Nodes)
	index, err := sessionindex.Parse(read.ProjectExpected[presentation.SessionIndexRelativePath])
	if err != nil {
		return problemResult{}, err
	}
	plan, err := presentation.RenderProblemOperation(presentation.ProblemOperationInput{Presentation: p, Ledger: read.OldAccepted.Ledger, Index: index, Pending: read.Pending.Documents, ExpectedFiles: read.ProjectExpected})
	if err != nil {
		return problemResult{}, err
	}
	edit := syncproject.MarkdownSyncPlan{Plan: plan, Index: read.ProjectExpected[presentation.SessionIndexRelativePath], ExpectedGenerationID: read.ExpectedGenerationID, ExpectedIndexDigest: read.ExpectedIndexDigest, VaultExpected: read.VaultExpected, ExpectedReceiptRevision: read.ExpectedReceiptRevision, ExpectedBaseDigest: read.ExpectedBaseDigest}
	if _, err := publication.PublishMarkdownEditLocked(ctx, pubOpts, edit, owner); err != nil {
		return problemResult{}, err
	}
	if candidate != nil {
		store, _ := problemmap.OpenStore(dataRoot, request.ProjectID)
		if err := store.CompareAndSwap(*candidate, request.ExpectedCandidateRevision); err != nil {
			return problemResult{}, err
		}
	}
	return problemResult{SchemaVersion: 1, ProjectID: p.ProjectID, ProblemMapRevision: p.ProblemMapRevision, ReviewSHA256: problemBareSHA(plan.Files[0].Desired), Problems: p.ProblemNodes, Candidate: candidate, MovePreview: preview}, nil
}

func problemRootIDs(nodes []reviewv4.ProblemNode) []string {
	values := []reviewv4.ProblemNode{}
	for _, node := range nodes {
		if node.PrimaryParentID == nil {
			values = append(values, node)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].SiblingOrder != values[j].SiblingOrder {
			return values[i].SiblingOrder < values[j].SiblingOrder
		}
		return values[i].ID < values[j].ID
	})
	result := make([]string, len(values))
	for i := range values {
		result[i] = values[i].ID
	}
	return result
}
func directProblemChildren(nodes []reviewv4.ProblemNode, parent string) []string {
	result := []string{}
	for _, node := range nodes {
		match := parent == "root" && node.PrimaryParentID == nil || node.PrimaryParentID != nil && *node.PrimaryParentID == parent
		if match {
			result = append(result, node.ID)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
func problemBareSHA(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
func writeProblemError(output io.Writer, err error) int {
	code := "problem_operation_failed"
	var contract ContractError
	if errors.As(err, &contract) {
		code = contract.Code
	} else if errors.Is(err, problemmap.ErrCandidateRevisionConflict) {
		code = ContractCodeCandidateRevisionConflict
	}
	_ = json.NewEncoder(output).Encode(map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
	return 1
}
func writeProblemJSON(output, diagnostic io.Writer, value any) int {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintln(diagnostic, "problem output failed")
		return 1
	}
	return 0
}
