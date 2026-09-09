package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/annotation"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/decisions"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
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
	index := sessionindex.Document{GenerationID: "generation-1", Sessions: []sessionindex.Entry{{Provider: "codex", SessionID: "session-1", SessionViewDigest: &active}}}
	candidate := annotation.Annotation{AnnotationKind: "decision_candidate", GenerationID: "generation-1", Dependencies: []annotation.Dependency{{Kind: "session_view", RevisionID: "view-" + strings.Repeat("1", 16), Digest: active}}}
	input := decisions.DecisionInput{Kind: "decision", SessionRefs: []reviewv4.SessionRef{{Provider: "codex", SessionID: "session-1"}}}
	if err := validateDecisionCandidate(index, candidate, input); err != nil {
		t.Fatal(err)
	}
	candidate.Dependencies[0].Digest = "sha256:" + strings.Repeat("2", 64)
	if err := validateDecisionCandidate(index, candidate, input); err == nil {
		t.Fatal("inactive candidate dependency accepted")
	}
	candidate.Dependencies[0].Digest = active
	candidate.GenerationID = "generation-previous"
	if err := validateDecisionCandidate(index, candidate, input); err != nil {
		t.Fatalf("unchanged authenticated SessionView was rejected after ordinary rescan: %v", err)
	}
	input.SessionRefs[0].SessionID = "invented"
	if err := validateDecisionCandidate(index, candidate, input); err == nil {
		t.Fatal("unbound Session reference accepted")
	}
	input.SessionRefs[0].SessionID = "session-1"
	input.Kind = "agreement"
	if err := validateDecisionCandidate(index, candidate, input); err == nil {
		t.Fatal("candidate kind mismatch accepted")
	}
}

func TestResolveDecisionCandidateEvidenceReturnsExactAuthenticatedCoordinates(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	candidate := annotation.Annotation{ID: "candidate-1", Dependencies: []annotation.Dependency{
		{Kind: "session_view", RevisionID: "view-" + strings.Repeat("1", 16), Digest: digest},
		{Kind: "source_turn", RevisionID: "revision-answer", Digest: digest},
	}}
	chain := conversationchain.Document{Provider: "codex", SessionID: "session-1", SessionViewDigest: digest, TurnUnits: []conversationchain.TurnUnit{{TurnUnitID: "turn-7", UserMessage: conversationchain.Message{RevisionID: "revision-question"}, AssistantMessages: []conversationchain.Message{{RevisionID: "revision-answer"}}}}}
	refs, err := resolveDecisionCandidateEvidence(candidate, map[string]conversationchain.Document{digest: chain})
	if err != nil || len(refs) != 1 || refs[0].Provider != "codex" || refs[0].SessionID != "session-1" || refs[0].TurnUnitID != "turn-7" || refs[0].RevisionID != "revision-answer" {
		t.Fatalf("refs=%+v err=%v", refs, err)
	}
	candidate.Dependencies[1].RevisionID = "invented"
	if _, err := resolveDecisionCandidateEvidence(candidate, map[string]conversationchain.Document{digest: chain}); err == nil {
		t.Fatal("invented source-turn revision was accepted")
	}
}

func TestNewDecisionExtractionDigestsPagesDeterministicallyWithoutAdvancingUnprocessedViews(t *testing.T) {
	manifest := memory.GenerationManifest{ConversationChains: make([]memory.ConversationChainDependency, decisions.MaxExtractionDependencies+2)}
	for index := range manifest.ConversationChains {
		manifest.ConversationChains[index].SessionViewDigest = fmt.Sprintf("sha256:%064x", decisions.MaxExtractionDependencies+1-index)
	}
	first := newDecisionExtractionDigests(manifest, map[string]bool{})
	if len(first) != decisions.MaxExtractionDependencies || first[0] != fmt.Sprintf("sha256:%064x", 0) || first[len(first)-1] != fmt.Sprintf("sha256:%064x", decisions.MaxExtractionDependencies-1) {
		t.Fatalf("first page count=%d bounds=%q..%q", len(first), first[0], first[len(first)-1])
	}
	watermark := map[string]bool{}
	for _, digest := range first {
		watermark[digest] = true
	}
	second := newDecisionExtractionDigests(manifest, watermark)
	if len(second) != 2 || second[0] != fmt.Sprintf("sha256:%064x", decisions.MaxExtractionDependencies) || second[1] != fmt.Sprintf("sha256:%064x", decisions.MaxExtractionDependencies+1) {
		t.Fatalf("second page=%v", second)
	}
}

func TestCurrentDecisionExtractionDependenciesAllowNewGenerationWithSameViews(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	job := decisions.ExtractionJob{GenerationID: "generation-before-rescan", DependencyDigests: []string{digest}}
	manifest := memory.GenerationManifest{GenerationID: "generation-after-rescan", ConversationChains: []memory.ConversationChainDependency{{Provider: "codex", SessionID: "session-1", SessionViewDigest: digest, Digest: "sha256:" + strings.Repeat("2", 64)}}}
	dependencies, err := currentDecisionExtractionDependencies(job, manifest)
	if err != nil || len(dependencies) != 1 || dependencies[0].SessionViewDigest != digest {
		t.Fatalf("dependencies=%+v err=%v", dependencies, err)
	}
	job.DependencyDigests[0] = "sha256:" + strings.Repeat("3", 64)
	if _, err := currentDecisionExtractionDependencies(job, manifest); err == nil {
		t.Fatal("changed SessionView dependency was accepted")
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
