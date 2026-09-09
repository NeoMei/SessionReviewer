package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/neomei/SessionReviewer/internal/problemmap"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

func TestProblemCandidateConfirmationReconcilesCrashAfterAcceptedPublication(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	accepted, err := readProblemProjection(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	review := readCLIProblemFile(t, fixture.project, reviewv2.ReviewRelativePath)
	createArgs := []string{"create", "--project-id", fixture.projectID, "--expected-problem-map-revision", strconv.Itoa(accepted.Review.ProblemMapRevision), "--expected-review-sha256", testBareSHA(review), "--data-dir", fixture.data, "--json"}
	var createdOut bytes.Buffer
	if code := runProblems(createArgs, bytes.NewBufferString(`{"schema_version":1,"question":"Crash-safe root?"}`), &createdOut, &bytes.Buffer{}); code != 0 {
		t.Fatalf("create code=%d out=%s", code, createdOut.String())
	}
	var created problemResult
	if err := json.Unmarshal(createdOut.Bytes(), &created); err != nil || created.Candidate == nil {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	candidate := *created.Candidate
	args := []string{"candidate", "transition", "--project-id", fixture.projectID, "--candidate-id", candidate.CandidateID, "--expected-candidate-revision", "1", "--expected-problem-map-revision", strconv.Itoa(accepted.Review.ProblemMapRevision), "--expected-review-sha256", testBareSHA(review), "--action", "apply_root", "--data-dir", fixture.data, "--json"}

	crash := errors.New("injected crash after accepted publication")
	originalHook := problemAfterAcceptedPublication
	problemAfterAcceptedPublication = func() error { return crash }
	t.Cleanup(func() { problemAfterAcceptedPublication = originalHook })
	var failed bytes.Buffer
	if code := runProblems(args, bytes.NewReader(nil), &failed, &bytes.Buffer{}); code == 0 {
		t.Fatalf("injected crash unexpectedly succeeded: %s", failed.String())
	}
	problemAfterAcceptedPublication = nil

	publicAfterCrash, err := readProblemProjection(fixture.project)
	if err != nil || len(publicAfterCrash.Review.ProblemNodes) != len(accepted.Review.ProblemNodes)+1 {
		t.Fatalf("accepted public graph missing after crash: nodes=%d err=%v", len(publicAfterCrash.Review.ProblemNodes), err)
	}
	store, err := problemmap.OpenStore(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	privateAfterCrash, err := store.Get(candidate.CandidateID)
	if err != nil || privateAfterCrash.Status != problemmap.CandidatePending {
		t.Fatalf("crash did not preserve reproducible split state: candidate=%+v err=%v", privateAfterCrash, err)
	}
	originalLoadAgent := problemPlacementLoadAgent
	agentLoads := 0
	problemPlacementLoadAgent = func(context.Context, string) (placementAgent, error) {
		agentLoads++
		return nil, errors.New("Agent must not start for an accepted candidate publication")
	}
	t.Cleanup(func() { problemPlacementLoadAgent = originalLoadAgent })
	_, _ = runProblemPlacement(ProblemRequest{
		Command: "placement", Subcommand: "request", DataDir: fixture.data, ProjectID: fixture.projectID,
		CandidateID: candidate.CandidateID, ExpectedCandidateRevision: candidate.Revision,
		ExpectedProblemMapRevision: accepted.Review.ProblemMapRevision, ExpectedGenerationID: accepted.Review.GenerationID,
	})
	reconciledByPlacement, err := store.Get(candidate.CandidateID)
	if err != nil || reconciledByPlacement.Status != problemmap.CandidateApplied || agentLoads != 0 {
		t.Fatalf("placement did not reconcile before Agent eligibility: candidate=%+v agent_loads=%d err=%v", reconciledByPlacement, agentLoads, err)
	}

	var listed bytes.Buffer
	if code := runProblems([]string{"candidates", "list", "--project-id", fixture.projectID, "--data-dir", fixture.data, "--json"}, bytes.NewReader(nil), &listed, &bytes.Buffer{}); code != 0 {
		t.Fatalf("recovery list code=%d out=%s", code, listed.String())
	}
	reconciled, err := store.Get(candidate.CandidateID)
	if err != nil || reconciled.Status != problemmap.CandidateApplied || reconciled.Revision != privateAfterCrash.Revision+1 {
		t.Fatalf("candidate was not reconciled from accepted proof: candidate=%+v err=%v", reconciled, err)
	}

	var replay bytes.Buffer
	if code := runProblems(args, bytes.NewReader(nil), &replay, &bytes.Buffer{}); code == 0 {
		t.Fatalf("stale replay unexpectedly reapplied the graph: %s", replay.String())
	}
	publicAfterReplay, err := readProblemProjection(fixture.project)
	if err != nil || publicAfterReplay.Review.ProblemMapRevision != publicAfterCrash.Review.ProblemMapRevision || len(publicAfterReplay.Review.ProblemNodes) != len(publicAfterCrash.Review.ProblemNodes) {
		t.Fatalf("stale replay changed accepted graph: before=%+v after=%+v err=%v", publicAfterCrash.Review.ProblemNodes, publicAfterReplay.Review.ProblemNodes, err)
	}
}

func TestProblemCandidateConfirmationCanRetryAfterPreparedPublicationIsAborted(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	accepted, err := readProblemProjection(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	review := readCLIProblemFile(t, fixture.project, reviewv2.ReviewRelativePath)
	createArgs := []string{"create", "--project-id", fixture.projectID, "--expected-problem-map-revision", strconv.Itoa(accepted.Review.ProblemMapRevision), "--expected-review-sha256", testBareSHA(review), "--data-dir", fixture.data, "--json"}
	var createdOut bytes.Buffer
	if code := runProblems(createArgs, bytes.NewBufferString(`{"schema_version":1,"question":"Retryable root?"}`), &createdOut, &bytes.Buffer{}); code != 0 {
		t.Fatalf("create code=%d out=%s", code, createdOut.String())
	}
	var created problemResult
	if err := json.Unmarshal(createdOut.Bytes(), &created); err != nil || created.Candidate == nil {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	candidate := *created.Candidate
	args := []string{"candidate", "transition", "--project-id", fixture.projectID, "--candidate-id", candidate.CandidateID, "--expected-candidate-revision", "1", "--expected-problem-map-revision", strconv.Itoa(accepted.Review.ProblemMapRevision), "--expected-review-sha256", testBareSHA(review), "--action", "apply_root", "--data-dir", fixture.data, "--json"}

	originalHook := problemBeforeMarkdownPublication
	problemBeforeMarkdownPublication = func() error { return errors.New("injected failure before publication") }
	t.Cleanup(func() { problemBeforeMarkdownPublication = originalHook })
	var failed bytes.Buffer
	if code := runProblems(args, bytes.NewReader(nil), &failed, &bytes.Buffer{}); code == 0 {
		t.Fatalf("injected failure unexpectedly succeeded: %s", failed.String())
	}
	problemBeforeMarkdownPublication = nil

	publicAfterFailure, err := readProblemProjection(fixture.project)
	if err != nil || publicAfterFailure.Review.ProblemMapRevision != accepted.Review.ProblemMapRevision {
		t.Fatalf("pre-publication failure changed public graph: before=%d after=%d err=%v", accepted.Review.ProblemMapRevision, publicAfterFailure.Review.ProblemMapRevision, err)
	}

	var retried bytes.Buffer
	if code := runProblems(args, bytes.NewReader(nil), &retried, &bytes.Buffer{}); code != 0 {
		t.Fatalf("retry code=%d out=%s", code, retried.String())
	}
	var result problemResult
	if err := json.Unmarshal(retried.Bytes(), &result); err != nil || result.Candidate == nil || result.Candidate.Status != problemmap.CandidateApplied {
		t.Fatalf("retry result=%+v err=%v", result, err)
	}
	publicAfterRetry, err := readProblemProjection(fixture.project)
	if err != nil || publicAfterRetry.Review.ProblemMapRevision != accepted.Review.ProblemMapRevision+1 || len(publicAfterRetry.Review.ProblemNodes) != len(accepted.Review.ProblemNodes)+1 {
		t.Fatalf("retry did not publish exactly once: before=%+v after=%+v err=%v", accepted.Review.ProblemNodes, publicAfterRetry.Review.ProblemNodes, err)
	}
}

func TestProblemCandidateMergeCrashReconcilesBeforeLaterEditAndNeverReapplies(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	accepted, err := readProblemProjection(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	review := readCLIProblemFile(t, fixture.project, reviewv2.ReviewRelativePath)
	createTarget := []string{"create", "--project-id", fixture.projectID, "--expected-problem-map-revision", strconv.Itoa(accepted.Review.ProblemMapRevision), "--expected-review-sha256", testBareSHA(review), "--data-dir", fixture.data, "--json"}
	var createdTarget bytes.Buffer
	if code := runProblems(createTarget, bytes.NewBufferString(`{"schema_version":1,"question":"Target root?"}`), &createdTarget, &bytes.Buffer{}); code != 0 {
		t.Fatalf("create target code=%d out=%s", code, createdTarget.String())
	}
	var targetCandidate problemResult
	if err := json.Unmarshal(createdTarget.Bytes(), &targetCandidate); err != nil || targetCandidate.Candidate == nil {
		t.Fatalf("target candidate=%+v err=%v", targetCandidate, err)
	}
	applyTarget := []string{"candidate", "transition", "--project-id", fixture.projectID, "--candidate-id", targetCandidate.Candidate.CandidateID, "--expected-candidate-revision", "1", "--expected-problem-map-revision", strconv.Itoa(accepted.Review.ProblemMapRevision), "--expected-review-sha256", testBareSHA(review), "--action", "apply_root", "--data-dir", fixture.data, "--json"}
	if code := runProblems(applyTarget, bytes.NewReader(nil), &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("apply target code=%d", code)
	}
	beforeMerge, err := readProblemProjection(fixture.project)
	if err != nil || len(beforeMerge.Review.ProblemNodes) != 1 {
		t.Fatalf("target readback nodes=%d err=%v", len(beforeMerge.Review.ProblemNodes), err)
	}
	target := beforeMerge.Review.ProblemNodes[0]

	review = readCLIProblemFile(t, fixture.project, reviewv2.ReviewRelativePath)
	createMerge := []string{"create", "--project-id", fixture.projectID, "--expected-problem-map-revision", strconv.Itoa(beforeMerge.Review.ProblemMapRevision), "--expected-review-sha256", testBareSHA(review), "--data-dir", fixture.data, "--json"}
	var createdMerge bytes.Buffer
	if code := runProblems(createMerge, bytes.NewBufferString(`{"schema_version":1,"question":"Merge evidence into target?"}`), &createdMerge, &bytes.Buffer{}); code != 0 {
		t.Fatalf("create merge code=%d out=%s", code, createdMerge.String())
	}
	var mergeCandidate problemResult
	if err := json.Unmarshal(createdMerge.Bytes(), &mergeCandidate); err != nil || mergeCandidate.Candidate == nil {
		t.Fatalf("merge candidate=%+v err=%v", mergeCandidate, err)
	}
	mergeArgs := []string{"candidate", "transition", "--project-id", fixture.projectID, "--candidate-id", mergeCandidate.Candidate.CandidateID, "--expected-candidate-revision", "1", "--expected-problem-map-revision", strconv.Itoa(beforeMerge.Review.ProblemMapRevision), "--expected-review-sha256", testBareSHA(review), "--action", "merge", "--target-problem-id", target.ID, "--data-dir", fixture.data, "--json"}
	originalHook := problemAfterAcceptedPublication
	problemAfterAcceptedPublication = func() error { return errors.New("injected merge crash after accepted publication") }
	t.Cleanup(func() { problemAfterAcceptedPublication = originalHook })
	if code := runProblems(mergeArgs, bytes.NewReader(nil), &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
		t.Fatal("injected merge crash unexpectedly succeeded")
	}
	problemAfterAcceptedPublication = nil
	afterCrash, err := readProblemProjection(fixture.project)
	if err != nil || len(afterCrash.Review.ProblemNodes) != 1 || afterCrash.Review.ProblemMapRevision != beforeMerge.Review.ProblemMapRevision+1 || afterCrash.Review.ProblemNodes[0].Revision != target.Revision+1 {
		t.Fatalf("merge was not accepted exactly once before crash: before=%+v after=%+v err=%v", beforeMerge.Review.ProblemNodes, afterCrash.Review.ProblemNodes, err)
	}

	mergedTarget := afterCrash.Review.ProblemNodes[0]
	review = readCLIProblemFile(t, fixture.project, reviewv2.ReviewRelativePath)
	editArgs := []string{"edit", "--project-id", fixture.projectID, "--problem-id", target.ID, "--expected-problem-revision", strconv.Itoa(mergedTarget.Revision), "--expected-problem-map-revision", strconv.Itoa(afterCrash.Review.ProblemMapRevision), "--expected-review-sha256", testBareSHA(review), "--data-dir", fixture.data, "--json"}
	var editedOut bytes.Buffer
	if code := runProblems(editArgs, bytes.NewBufferString(`{"schema_version":1,"question":"人工保留的问题？","current_conclusion":"人工保留的结论。","completion_criterion":"人工保留的标准。"}`), &editedOut, &bytes.Buffer{}); code != 0 {
		t.Fatalf("edit after recovery code=%d out=%s", code, editedOut.String())
	}
	afterEdit, err := readProblemProjection(fixture.project)
	if err != nil || len(afterEdit.Review.ProblemNodes) != 1 || afterEdit.Review.ProblemMapRevision != afterCrash.Review.ProblemMapRevision+1 {
		t.Fatalf("later edit changed graph shape or wrong revision: after_crash=%+v after_edit=%+v err=%v", afterCrash.Review.ProblemNodes, afterEdit.Review.ProblemNodes, err)
	}
	edited := afterEdit.Review.ProblemNodes[0]
	if edited.Question != "人工保留的问题？" || edited.CurrentConclusion != "人工保留的结论。" || edited.CompletionCriterion != "人工保留的标准。" {
		t.Fatalf("later human edit was not preserved: %+v", edited)
	}

	var listed bytes.Buffer
	if code := runProblems([]string{"candidates", "list", "--project-id", fixture.projectID, "--data-dir", fixture.data, "--json"}, bytes.NewReader(nil), &listed, &bytes.Buffer{}); code != 0 {
		t.Fatalf("list after later edit code=%d out=%s", code, listed.String())
	}
	store, err := problemmap.OpenStore(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := store.Get(mergeCandidate.Candidate.CandidateID)
	if err != nil || reconciled.Status != problemmap.CandidateMerged {
		t.Fatalf("merge candidate did not reconcile: candidate=%+v err=%v", reconciled, err)
	}

	var replay bytes.Buffer
	if code := runProblems(mergeArgs, bytes.NewReader(nil), &replay, &bytes.Buffer{}); code == 0 {
		t.Fatalf("stale merge replay unexpectedly succeeded: %s", replay.String())
	}
	afterReplay, err := readProblemProjection(fixture.project)
	if err != nil || afterReplay.Review.ProblemMapRevision != afterEdit.Review.ProblemMapRevision || len(afterReplay.Review.ProblemNodes) != 1 || afterReplay.Review.ProblemNodes[0].Revision != edited.Revision || afterReplay.Review.ProblemNodes[0].Question != edited.Question || afterReplay.Review.ProblemNodes[0].CurrentConclusion != edited.CurrentConclusion || afterReplay.Review.ProblemNodes[0].CompletionCriterion != edited.CompletionCriterion {
		t.Fatalf("merge replay changed graph or human edit: before=%+v after=%+v err=%v", afterEdit.Review.ProblemNodes, afterReplay.Review.ProblemNodes, err)
	}
}

func TestProblemsRootCLIAtomicallyPublishesAndReadsBack(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	accepted, err := readProblemProjection(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	review := readCLIProblemFile(t, fixture.project, reviewv2.ReviewRelativePath)
	createArgs := []string{"create", "--project-id", fixture.projectID, "--expected-problem-map-revision", strconv.Itoa(accepted.Review.ProblemMapRevision), "--expected-review-sha256", testBareSHA(review), "--data-dir", fixture.data, "--json"}
	var createdOut bytes.Buffer
	if code := runProblems(createArgs, bytes.NewBufferString(`{"schema_version":1,"question":"How do we create the first root?"}`), &createdOut, &bytes.Buffer{}); code != 0 {
		t.Fatalf("create code=%d out=%s", code, createdOut.String())
	}
	var created problemResult
	if err := json.Unmarshal(createdOut.Bytes(), &created); err != nil || created.Candidate == nil {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	candidate := *created.Candidate
	store, err := problemmap.OpenStore(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"candidate", "transition", "--project-id", fixture.projectID, "--candidate-id", candidate.CandidateID, "--expected-candidate-revision", "1", "--expected-problem-map-revision", strconv.Itoa(accepted.Review.ProblemMapRevision), "--expected-review-sha256", testBareSHA(review), "--action", "apply_root", "--data-dir", fixture.data, "--json"}
	var out, diagnostic bytes.Buffer
	if code := runProblems(args, bytes.NewReader(nil), &out, &diagnostic); code != 0 {
		t.Fatalf("code=%d out=%s diagnostic=%s", code, out.String(), diagnostic.String())
	}
	var result problemResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	got, err := readProblemProjection(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Review.ProblemNodes) != len(accepted.Review.ProblemNodes)+1 || got.Review.ProblemMapRevision != accepted.Review.ProblemMapRevision+1 {
		t.Fatalf("readback nodes=%d revision=%d", len(got.Review.ProblemNodes), got.Review.ProblemMapRevision)
	}
	stored, err := store.Get(candidate.CandidateID)
	if err != nil || stored.Status != problemmap.CandidateApplied {
		t.Fatalf("candidate=%+v err=%v", stored, err)
	}
	projectIndex := readCLIProblemFile(t, fixture.project, "docs/session-review/.session-reviewer/session-index.json")
	vaultIndex := readCLIProblemFile(t, fixture.vault, filepath.ToSlash(filepath.Join("Projects/Markdown/Session Review", ".session-reviewer/session-index.json")))
	if !bytes.Equal(projectIndex, vaultIndex) {
		t.Fatal("index guard changed asymmetrically")
	}
}

func TestProblemsEditResolveAndReopenPublishExactHumanFields(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	accepted, err := readProblemProjection(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted.Review.ProblemNodes) == 0 {
		review := readCLIProblemFile(t, fixture.project, reviewv2.ReviewRelativePath)
		createArgs := []string{"create", "--project-id", fixture.projectID, "--expected-problem-map-revision", strconv.Itoa(accepted.Review.ProblemMapRevision), "--expected-review-sha256", testBareSHA(review), "--data-dir", fixture.data, "--json"}
		var created bytes.Buffer
		if code := runProblems(createArgs, bytes.NewBufferString(`{"schema_version":1,"question":"seed?"}`), &created, &bytes.Buffer{}); code != 0 {
			t.Fatalf("create=%s", created.String())
		}
		var candidate problemResult
		if json.Unmarshal(created.Bytes(), &candidate) != nil || candidate.Candidate == nil {
			t.Fatal("missing candidate")
		}
		applyArgs := []string{"candidate", "transition", "--project-id", fixture.projectID, "--candidate-id", candidate.Candidate.CandidateID, "--expected-candidate-revision", "1", "--expected-problem-map-revision", strconv.Itoa(accepted.Review.ProblemMapRevision), "--expected-review-sha256", testBareSHA(review), "--action", "apply_root", "--data-dir", fixture.data, "--json"}
		if code := runProblems(applyArgs, bytes.NewReader(nil), &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
			t.Fatalf("apply=%d", code)
		}
		accepted, err = readProblemProjection(fixture.project)
		if err != nil {
			t.Fatal(err)
		}
	}
	node := accepted.Review.ProblemNodes[0]
	review := readCLIProblemFile(t, fixture.project, reviewv2.ReviewRelativePath)
	common := []string{"--project-id", fixture.projectID, "--problem-id", node.ID, "--expected-problem-revision", strconv.Itoa(node.Revision), "--expected-problem-map-revision", strconv.Itoa(accepted.Review.ProblemMapRevision), "--expected-review-sha256", testBareSHA(review), "--data-dir", fixture.data, "--json"}
	var out bytes.Buffer
	if code := runProblems(append([]string{"edit"}, common...), bytes.NewBufferString(`{"schema_version":1,"question":"用户原始问题？","current_conclusion":"保留此结论。","completion_criterion":"明确标准。"}`), &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("edit code=%d out=%s", code, out.String())
	}
	var edited problemResult
	if err := json.Unmarshal(out.Bytes(), &edited); err != nil {
		t.Fatal(err)
	}
	got, err := readProblemProjection(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	updated := got.Review.ProblemNodes[0]
	if updated.Question != "用户原始问题？" || updated.CurrentConclusion != "保留此结论。" || updated.CompletionCriterion != "明确标准。" {
		t.Fatalf("updated=%+v", updated)
	}
	stateArgs := []string{"state", "--project-id", fixture.projectID, "--problem-id", updated.ID, "--expected-problem-revision", strconv.Itoa(updated.Revision), "--expected-problem-map-revision", strconv.Itoa(got.Review.ProblemMapRevision), "--expected-review-sha256", edited.ReviewSHA256, "--action", "resolve", "--data-dir", fixture.data, "--json"}
	out.Reset()
	if code := runProblems(stateArgs, bytes.NewReader(nil), &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("resolve code=%d out=%s", code, out.String())
	}
	var resolved problemResult
	if err := json.Unmarshal(out.Bytes(), &resolved); err != nil {
		t.Fatal(err)
	}
	resolvedNode := resolved.Problems[0]
	if resolvedNode.WorkflowState != "resolved" || resolvedNode.AnswerState != updated.AnswerState {
		t.Fatalf("resolved=%+v", resolvedNode)
	}
	stateArgs = []string{"state", "--project-id", fixture.projectID, "--problem-id", resolvedNode.ID, "--expected-problem-revision", strconv.Itoa(resolvedNode.Revision), "--expected-problem-map-revision", strconv.Itoa(resolved.ProblemMapRevision), "--expected-review-sha256", resolved.ReviewSHA256, "--action", "reopen", "--data-dir", fixture.data, "--json"}
	out.Reset()
	if code := runProblems(stateArgs, bytes.NewReader(nil), &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("reopen code=%d out=%s args=%v", code, out.String(), stateArgs)
	}
	var reopened problemResult
	if err := json.Unmarshal(out.Bytes(), &reopened); err != nil || reopened.Problems[0].WorkflowState != "in_progress" {
		t.Fatalf("reopened=%+v err=%v", reopened, err)
	}
}

func readCLIProblemFile(t *testing.T, root, relative string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return body
}
