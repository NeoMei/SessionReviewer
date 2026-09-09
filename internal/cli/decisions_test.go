package cli

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/annotation"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

func TestDecisionsCreateAndEditPublishOnlyHumanDecisionFields(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	before, err := readProblemProjection(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	indexBefore := readCLIProblemFile(t, fixture.project, "docs/session-review/.session-reviewer/session-index.json")
	review := readCLIProblemFile(t, fixture.project, reviewv2.ReviewRelativePath)
	body := `{"schema_version":1,"kind":"agreement","occurred_at":"2026-09-09","title":"Keep exact preimages","rationale":"Avoid stale writes","impact":"Decision publication","status":"active","reevaluate_when":"The contract changes","supersedes":[],"milestone_ids":[],"session_refs":[],"pinned":true}`
	args := []string{"create", "--project-id", fixture.projectID, "--expected-review-sha256", testBareSHA(review), "--data-dir", fixture.data, "--json"}
	var output bytes.Buffer
	if code := runDecisions(args, strings.NewReader(body), &output, &bytes.Buffer{}); code != 0 {
		t.Fatalf("create code=%d output=%s", code, output.String())
	}
	var created decisionResult
	if err := json.Unmarshal(output.Bytes(), &created); err != nil || len(created.Decisions) != len(before.Review.Decisions)+1 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	decision := created.Decisions[len(created.Decisions)-1]
	body = strings.Replace(body, "Keep exact preimages", "Keep exact review and entity preimages", 1)
	args = []string{"edit", "--project-id", fixture.projectID, "--decision-id", decision.ID, "--expected-decision-revision", strconv.Itoa(decision.Revision), "--expected-review-sha256", created.ReviewSHA256, "--data-dir", fixture.data, "--json"}
	output.Reset()
	if code := runDecisions(args, strings.NewReader(body), &output, &bytes.Buffer{}); code != 0 {
		t.Fatalf("edit code=%d output=%s", code, output.String())
	}
	var edited decisionResult
	if err := json.Unmarshal(output.Bytes(), &edited); err != nil {
		t.Fatal(err)
	}
	got := edited.Decisions[len(edited.Decisions)-1]
	if got.Title != "Keep exact review and entity preimages" || got.Revision != decision.Revision+1 || got.Provenance != "human_created" {
		t.Fatalf("edited decision=%+v", got)
	}
	after, err := readProblemProjection(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	if after.Review.CurrentState != before.Review.CurrentState || !bytes.Equal(indexBefore, readCLIProblemFile(t, fixture.project, "docs/session-review/.session-reviewer/session-index.json")) {
		t.Fatal("decision operation changed a non-decision field or the index guard")
	}
}

func TestDecisionCandidateDependenciesRequireCurrentSessionView(t *testing.T) {
	active := "sha256:" + strings.Repeat("1", 64)
	index := sessionindex.Document{GenerationID: "generation-1", Sessions: []sessionindex.Entry{{SessionViewDigest: &active}}}
	candidate := annotation.Annotation{GenerationID: "generation-1", Dependencies: []annotation.Dependency{{Kind: "session_view", RevisionID: "view-1", Digest: active}}}
	if err := validateDecisionCandidate(index, candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Dependencies[0].Digest = "sha256:" + strings.Repeat("2", 64)
	if err := validateDecisionCandidate(index, candidate); err == nil {
		t.Fatal("inactive candidate dependency accepted")
	}
}

func TestDecisionsEditRejectsStaleReviewAndEntityRevisions(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	review := readCLIProblemFile(t, fixture.project, reviewv2.ReviewRelativePath)
	body := `{"schema_version":1,"kind":"decision","occurred_at":"2026-09-09","title":"One","rationale":"R","impact":"I","status":"active","reevaluate_when":"","supersedes":[],"milestone_ids":[],"session_refs":[],"pinned":false}`
	var output bytes.Buffer
	if code := runDecisions([]string{"create", "--project-id", fixture.projectID, "--expected-review-sha256", testBareSHA(review), "--data-dir", fixture.data, "--json"}, strings.NewReader(body), &output, &bytes.Buffer{}); code != 0 {
		t.Fatalf("create=%s", output.String())
	}
	var created decisionResult
	if err := json.Unmarshal(output.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	decision := created.Decisions[len(created.Decisions)-1]
	base := []string{"edit", "--project-id", fixture.projectID, "--decision-id", decision.ID, "--expected-decision-revision", "1", "--expected-review-sha256", created.ReviewSHA256, "--data-dir", fixture.data, "--json"}
	base[6] = "2"
	output.Reset()
	if code := runDecisions(base, strings.NewReader(body), &output, &bytes.Buffer{}); code == 0 || !strings.Contains(output.String(), "decision_revision_conflict") {
		t.Fatalf("stale entity code=%d output=%s", code, output.String())
	}
	base[6] = "1"
	base[8] = strings.Repeat("0", 64)
	output.Reset()
	if code := runDecisions(base, strings.NewReader(body), &output, &bytes.Buffer{}); code == 0 || !strings.Contains(output.String(), ContractCodeReviewPreimageConflict) {
		t.Fatalf("stale review code=%d output=%s", code, output.String())
	}
}
