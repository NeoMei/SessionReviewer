package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/problemmap"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func TestProblemsRootCLIAtomicallyPublishesAndReadsBack(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	accepted, err := readProblemProjection(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	store, err := problemmap.OpenStore(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	refs := []reviewv4.SourceTurnRef{}
	if len(accepted.Review.ProblemNodes) > 0 {
		refs = accepted.Review.ProblemNodes[0].SourceTurnRefs
	}
	candidate := completeCLIProblemCandidate(fixture.projectID, refs)
	if err := store.CompareAndSwap(candidate, 0); err != nil {
		t.Fatal(err)
	}
	review := readCLIProblemFile(t, fixture.project, reviewv2.ReviewRelativePath)
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

func completeCLIProblemCandidate(projectID string, refs []reviewv4.SourceTurnRef) problemmap.Candidate {
	now := time.Date(2026, 9, 9, 1, 2, 3, 0, time.UTC).Format(time.RFC3339)
	grounds := []problemmap.Ground{{RuleID: "no-signal", RuleVersion: "rules-v1", MatchedFactRefs: []string{}, Explanation: "继续待归类。"}}
	dependencies := []string{"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if len(refs) == 0 {
		grounds = []problemmap.Ground{{RuleID: "human-created", RuleVersion: "v1", MatchedFactRefs: []string{}, Explanation: "由用户明确创建。"}}
		dependencies = []string{}
	}
	return problemmap.Candidate{CandidateID: "candidate-root", ProjectID: projectID, Question: "How do we create the first root?", SourceTurnRefs: refs, RecommendedRelation: problemmap.RelationKeepPending, AlternateTargetIDs: []string{}, RelatedNodeIDs: []string{}, Grounds: grounds, Confidence: problemmap.ConfidenceLow, Status: problemmap.CandidatePending, DependencyDigests: dependencies, AnalysisMode: problemmap.AnalysisDeterministic, Revision: 1, CreatedAt: now, UpdatedAt: now}
}

func readCLIProblemFile(t *testing.T, root, relative string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return body
}
