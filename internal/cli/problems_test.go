package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/neomei/SessionReviewer/internal/problemmap"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

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
