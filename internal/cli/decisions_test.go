package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/annotation"
	"github.com/neomei/SessionReviewer/internal/candidatepublication"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/decisions"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
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

func TestDecisionCandidatesListEncodesEmptyEvidenceArrayForStaleCandidate(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	store, err := decisions.OpenStore(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 3, 30, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("9", 64)
	run := annotation.Run{
		RunID: "decision-extract-stale", ProjectID: fixture.projectID, Status: "completed",
		ExtractorVersion: decisions.ExtractorVersion, PromptSchemaVersion: decisions.PromptSchemaVersion,
		DependencyDigests: []string{digest}, CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Add(time.Second).Format(time.RFC3339Nano),
	}
	entity, field := "decision-stale", "decision"
	candidate := annotation.Annotation{
		ID: "candidate-stale", ProjectID: fixture.projectID, AnnotationKind: "decision_candidate", EntityID: &entity, Field: &field,
		Status: annotation.CandidatePending, Text: `{}`, GenerationID: "generation-before-rescan", SchemaVersion: 1,
		AnalysisProfile: decisions.ExtractorVersion, AgentRunID: run.RunID,
		Dependencies: []annotation.Dependency{
			{Kind: "session_view", RevisionID: "view-" + strings.Repeat("9", 16), Digest: digest},
			{Kind: "source_turn", RevisionID: "revision-stale", Digest: digest},
		},
		Revision: 1, CreatedAt: now.Format(time.RFC3339Nano),
	}
	if err := store.CommitExtraction(run, []annotation.Annotation{candidate}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if code := runDecisions([]string{"candidates", "list", "--project-id", fixture.projectID, "--data-dir", fixture.data, "--json"}, strings.NewReader(""), &output, &bytes.Buffer{}); code != 0 {
		t.Fatalf("candidate list code=%d output=%s", code, output.String())
	}
	var response struct {
		CandidateEvidence []struct {
			EvidenceRefs json.RawMessage `json:"evidence_refs"`
			ErrorCode    string          `json:"error_code"`
		} `json:"candidate_evidence"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.CandidateEvidence) != 1 || string(response.CandidateEvidence[0].EvidenceRefs) != "[]" || response.CandidateEvidence[0].ErrorCode != "candidate_stale" {
		t.Fatalf("candidate_evidence=%s", output.String())
	}
}

func TestDecisionCandidateConfirmationRecoversPublishedResultWithoutOverwritingLaterHumanEdit(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	candidate, input := seedDecisionPublicationCandidate(t, fixture)
	beforeReview := readCLIProblemFile(t, fixture.project, reviewv2.ReviewRelativePath)
	preparedAt := time.Date(2026, 9, 9, 4, 0, 0, 0, time.UTC)

	_, mapping, _, err := resolveSyncMapping("", fixture.projectID, fixture.data)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := publicationlock.Acquire(fixture.data, fixture.projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pubOpts := publication.Options{ProjectID: fixture.projectID, Mapping: mapping, DataRoot: fixture.data, Now: func() time.Time { return preparedAt }}
	if err := publication.RecoverMarkdownLocked(context.Background(), pubOpts, owner); err != nil {
		_ = owner.Release()
		t.Fatal(err)
	}
	read, err := syncproject.ReadMarkdownForScan(context.Background(), syncproject.Options{ProjectID: fixture.projectID, CWD: mapping.Root, DataDir: fixture.data, GOOS: runtime.GOOS, Now: func() time.Time { return preparedAt }, Trigger: syncengine.TriggerCLI}, owner)
	if err != nil {
		_ = owner.Release()
		t.Fatal(err)
	}
	index, err := sessionindex.Parse(read.ProjectExpected[presentation.SessionIndexRelativePath])
	if err != nil {
		_ = owner.Release()
		t.Fatal(err)
	}
	next, err := decisions.ConfirmCandidateDecision(read.Pending.Presentation, *candidate.EntityID, input)
	if err != nil {
		_ = owner.Release()
		t.Fatal(err)
	}
	plan, err := presentation.RenderDecisionOperation(presentation.DecisionOperationInput{Presentation: next, Ledger: read.OldAccepted.Ledger, Index: index, Pending: read.Pending.Documents, ExpectedFiles: read.ProjectExpected})
	if err != nil {
		_ = owner.Release()
		t.Fatal(err)
	}
	fingerprint, err := publication.ExpectedMarkdownResultFingerprint(plan, mapping, read.ProjectExpected[presentation.SessionIndexRelativePath])
	if err != nil {
		_ = owner.Release()
		t.Fatal(err)
	}
	candidateDigest, err := decisions.CandidatePublicationDigest(candidate)
	if err != nil {
		_ = owner.Release()
		t.Fatal(err)
	}
	intentStore, err := candidatepublication.OpenStore(fixture.data, fixture.projectID, decisionPublicationNamespace)
	if err != nil {
		_ = owner.Release()
		t.Fatal(err)
	}
	intent, err := candidatepublication.NewIntent(candidatepublication.IntentInput{
		ProjectID: fixture.projectID, Namespace: decisionPublicationNamespace, CandidateID: candidate.ID,
		ExpectedCandidateRevision: candidate.Revision, CandidateDigest: candidateDigest, Action: "confirm",
		EntityID: *candidate.EntityID, ResultFingerprint: fingerprint, TerminalStatus: "confirmed", PreparedAt: preparedAt,
	})
	if err != nil {
		_ = owner.Release()
		t.Fatal(err)
	}
	if _, err := intentStore.Prepare(intent); err != nil {
		_ = owner.Release()
		t.Fatal(err)
	}
	edit := syncproject.MarkdownSyncPlan{Plan: plan, Index: read.ProjectExpected[presentation.SessionIndexRelativePath], ExpectedGenerationID: read.ExpectedGenerationID, ExpectedIndexDigest: read.ExpectedIndexDigest, VaultExpected: read.VaultExpected, ExpectedReceiptRevision: read.ExpectedReceiptRevision, ExpectedBaseDigest: read.ExpectedBaseDigest}
	if _, err := publication.PublishMarkdownEditLocked(context.Background(), pubOpts, edit, owner); err != nil {
		_ = owner.Release()
		t.Fatal(err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	if stillPending, err := decisions.OpenStore(fixture.data, fixture.projectID); err != nil {
		t.Fatal(err)
	} else if value, err := stillPending.Get(candidate.ID); err != nil || value.Status != annotation.CandidatePending {
		t.Fatalf("fault fixture crossed candidate CAS: candidate=%+v err=%v", value, err)
	}

	acceptedAfterCrash, err := readProblemProjection(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	formal := acceptedAfterCrash.Review.Decisions[len(acceptedAfterCrash.Review.Decisions)-1]
	humanEdit := input
	humanEdit.Title = "Human edit after candidate CAS gap"
	humanBody, err := json.Marshal(humanEdit)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if code := runDecisions([]string{"edit", "--project-id", fixture.projectID, "--decision-id", formal.ID, "--expected-decision-revision", strconv.Itoa(formal.Revision), "--expected-review-sha256", testBareSHA(readCLIProblemFile(t, fixture.project, reviewv2.ReviewRelativePath)), "--data-dir", fixture.data, "--json"}, bytes.NewReader(humanBody), &output, &bytes.Buffer{}); code != 0 {
		t.Fatalf("later human edit code=%d output=%s", code, output.String())
	}

	output.Reset()
	if code := runDecisions([]string{"candidates", "list", "--project-id", fixture.projectID, "--data-dir", fixture.data, "--json"}, strings.NewReader(""), &output, &bytes.Buffer{}); code != 0 {
		t.Fatalf("recovery list code=%d output=%s", code, output.String())
	}
	confirmed, err := decisions.OpenStore(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	confirmedCandidate, err := confirmed.Get(candidate.ID)
	if err != nil || confirmedCandidate.Status != annotation.CandidateConfirmed || confirmedCandidate.Revision != candidate.Revision+1 {
		t.Fatalf("candidate did not converge from receipt: candidate=%+v err=%v", confirmedCandidate, err)
	}
	storedIntent, err := intentStore.Get(intent.OperationID)
	if err != nil || storedIntent.State != candidatepublication.StateCompleted {
		t.Fatalf("intent did not converge: intent=%+v err=%v", storedIntent, err)
	}

	retryInput := input
	retryInput.Title = "This stale retry must not replay"
	retryBody, err := json.Marshal(retryInput)
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := runDecisions([]string{"candidate", "transition", "--project-id", fixture.projectID, "--candidate-id", candidate.ID, "--expected-revision", strconv.Itoa(candidate.Revision), "--action", "confirm", "--expected-review-sha256", testBareSHA(beforeReview), "--data-dir", fixture.data, "--json"}, bytes.NewReader(retryBody), &output, &bytes.Buffer{}); code != 0 {
		t.Fatalf("terminal retry code=%d output=%s", code, output.String())
	}
	after, err := readProblemProjection(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Review.Decisions) != 1 || after.Review.Decisions[0].Title != humanEdit.Title || after.Review.Decisions[0].Revision != formal.Revision+1 {
		t.Fatalf("retry replayed old input or duplicated decision: %+v", after.Review.Decisions)
	}
}

func TestDecisionCandidateRecoveryAbortsUnacceptedIntentAndAllowsFreshRetry(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	candidate, _ := seedDecisionPublicationCandidate(t, fixture)
	digest, err := decisions.CandidatePublicationDigest(candidate)
	if err != nil {
		t.Fatal(err)
	}
	intentStore, err := candidatepublication.OpenStore(fixture.data, fixture.projectID, decisionPublicationNamespace)
	if err != nil {
		t.Fatal(err)
	}
	firstAt := time.Date(2026, 9, 9, 5, 0, 0, 0, time.UTC)
	makeIntent := func(at time.Time) candidatepublication.Intent {
		intent, err := candidatepublication.NewIntent(candidatepublication.IntentInput{
			ProjectID: fixture.projectID, Namespace: decisionPublicationNamespace, CandidateID: candidate.ID,
			ExpectedCandidateRevision: candidate.Revision, CandidateDigest: digest, Action: "confirm", EntityID: *candidate.EntityID,
			ResultFingerprint: "sha256:" + strings.Repeat("f", 64), TerminalStatus: "confirmed", PreparedAt: at,
		})
		if err != nil {
			t.Fatal(err)
		}
		return intent
	}
	first := makeIntent(firstAt)
	if _, err := intentStore.Prepare(first); err != nil {
		t.Fatal(err)
	}
	_, mapping, _, err := resolveSyncMapping("", fixture.projectID, fixture.data)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := publicationlock.Acquire(fixture.data, fixture.projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pubOpts := publication.Options{ProjectID: fixture.projectID, Mapping: mapping, DataRoot: fixture.data, Now: func() time.Time { return firstAt }}
	recoverErr := recoverDecisionCandidatePublicationsLocked(context.Background(), pubOpts, owner, firstAt.Add(time.Second))
	releaseErr := owner.Release()
	if err := errors.Join(recoverErr, releaseErr); err != nil {
		t.Fatal(err)
	}
	aborted, err := intentStore.Get(first.OperationID)
	if err != nil || aborted.State != candidatepublication.StateAborted {
		t.Fatalf("unaccepted intent=%+v err=%v", aborted, err)
	}
	retry := makeIntent(firstAt.Add(2 * time.Second))
	if retry.OperationID == first.OperationID {
		t.Fatal("fresh retry reused the aborted operation identity")
	}
	if _, err := intentStore.Prepare(retry); err != nil {
		t.Fatalf("fresh retry after abort: %v", err)
	}
}

func seedDecisionPublicationCandidate(t *testing.T, fixture cliSyncFixture) (annotation.Annotation, decisions.DecisionInput) {
	t.Helper()
	entity, field := "decision-proposed", "decision"
	digest := "sha256:" + strings.Repeat("1", 64)
	input := decisions.DecisionInput{SchemaVersion: 1, Kind: "decision", OccurredAt: "2026-09-09", Title: "Candidate", Rationale: "Reason", Impact: "Impact", Status: reviewv4.DecisionActive, ReevaluateWhen: "Later", Supersedes: []string{}, MilestoneIDs: []string{}, SessionRefs: []reviewv4.SessionRef{}, Pinned: false}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	candidate := annotation.Annotation{
		ID: "candidate-1", ProjectID: fixture.projectID, AnnotationKind: "decision_candidate", EntityID: &entity, Field: &field,
		Status: annotation.CandidatePending, Text: string(body), GenerationID: "generation-markdown-cli", SchemaVersion: 1,
		AnalysisProfile: decisions.ExtractorVersion, AgentRunID: "run-1",
		Dependencies: []annotation.Dependency{{Kind: "session_view", RevisionID: "view-1111111111111111", Digest: digest}, {Kind: "source_turn", RevisionID: "revision-answer", Digest: digest}},
		Revision:     1, CreatedAt: "2026-09-09T00:00:00Z",
	}
	run := annotation.Run{RunID: "run-1", ProjectID: fixture.projectID, Status: "completed", ExtractorVersion: decisions.ExtractorVersion, PromptSchemaVersion: decisions.PromptSchemaVersion, DependencyDigests: []string{digest}, CreatedAt: "2026-09-09T00:00:00Z", UpdatedAt: "2026-09-09T00:00:01Z"}
	store, err := decisions.OpenStore(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitExtraction(run, []annotation.Annotation{candidate}); err != nil {
		t.Fatal(err)
	}
	return candidate, input
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

func TestDecisionExtractionStartReconcilesPriorCandidateCommitBeforeWatermarkPaging(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	executable := filepath.Join(fixture.data, "configured-codex")
	if err := os.WriteFile(executable, []byte("controlled fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	configuration, err := decisions.MeasureAgentConfiguration("codex", "fixture", executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := decisions.SaveAgentConfiguration(fixture.data, configuration); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("9", 64)
	jobStore, err := decisions.OpenExtractionJobStore(fixture.data)
	if err != nil {
		t.Fatal(err)
	}
	job := decisions.ExtractionJob{
		SchemaVersion: 1, JobID: decisions.ExtractionIdentity(fixture.projectID, []string{digest}), ProjectID: fixture.projectID,
		GenerationID: "generation-markdown-cli", State: decisions.ExtractionQueued, Revision: 1, DependencyDigests: []string{digest},
		CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano),
	}
	if err := jobStore.Create(job); err != nil {
		t.Fatal(err)
	}
	job, err = jobStore.AuthorizeWorker(job.JobID, 999999, job.Revision, now)
	if err != nil {
		t.Fatal(err)
	}
	candidateStore, err := decisions.OpenStore(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	run := annotation.Run{
		RunID: job.JobID, ProjectID: fixture.projectID, Status: "completed", ExtractorVersion: decisions.ExtractorVersion,
		PromptSchemaVersion: decisions.PromptSchemaVersion, DependencyDigests: []string{digest},
		CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Add(time.Second).Format(time.RFC3339Nano),
	}
	if err := candidateStore.CommitExtraction(run, []annotation.Annotation{}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	code := runDecisions([]string{"extract", "--project-id", fixture.projectID, "--expected-generation-id", "generation-markdown-cli", "--data-dir", fixture.data, "--json"}, strings.NewReader(""), &output, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("extract start code=%d output=%s", code, output.String())
	}
	reconciled, err := jobStore.Load(job.JobID)
	if err != nil || reconciled.State != decisions.ExtractionCompleted || reconciled.Revision != job.Revision+1 {
		t.Fatalf("prior projection was not reconciled before start: job=%+v err=%v", reconciled, err)
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
