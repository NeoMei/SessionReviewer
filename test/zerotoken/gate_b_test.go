package zerotoken

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/contextupdate"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

type gateBFixture struct {
	root, dataRoot, projectRoot, vaultRoot, sessionsRoot, projectID, gitExecutable string
	mapping                                                                        config.ProjectMapping
	base                                                                           time.Time
	scanNow                                                                        time.Time
}

func newGateBFixture(t *testing.T, persistentRoot string) gateBFixture {
	t.Helper()
	root := persistentRoot
	if root == "" {
		root = t.TempDir()
	} else {
		if err := preparePersistentGateBRoot(root); err != nil {
			t.Fatal(err)
		}
	}
	dataRoot := filepath.Join(root, "data")
	projectRoot := filepath.Join(root, "Project")
	vaultRoot := filepath.Join(root, "Vault")
	sessionsRoot := filepath.Join(root, "sessions")
	for _, directory := range []string{dataRoot, projectRoot, vaultRoot, sessionsRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatalf("create Gate B fixture directory %s: %v", directory, err)
		}
	}
	projectID := "project-gate-b-test"
	_ = os.WriteFile(filepath.Join(projectRoot, "VERSION"), []byte("v1.0.0\n"), 0o600)
	_ = os.WriteFile(filepath.Join(projectRoot, "project-fixture.md"), []byte("# fixture\n"), 0o600)
	gitExecutable := initializeGateRepository(t, projectRoot)

	mapping := config.ProjectMapping{
		ID:              projectID,
		Root:            projectRoot,
		VaultRoot:       vaultRoot,
		VaultReviewPath: "Projects/" + projectID + "/Session Review",
		VaultCaseMode:   platform.CaseSensitive,
	}
	cfg := config.Config{
		Version:  1,
		Projects: []config.ProjectMapping{mapping},
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	base := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	scanNow := base.Add(10 * time.Minute)
	for i := 1; i <= 154; i++ {
		started := base.Add(time.Duration(i) * time.Second)
		sessionID := fmt.Sprintf("session-%03d", i)
		sessionLines := []string{
			`{"timestamp":"` + started.Format(time.RFC3339) + `","type":"session_meta","payload":{"id":"` + sessionID + `","cwd":"` + filepath.ToSlash(projectRoot) + `","source":"codex"}}`,
			`{"timestamp":"` + started.Add(time.Second).Format(time.RFC3339) + `","type":"turn_context","payload":{"cwd":"` + filepath.ToSlash(projectRoot) + `","model":"gpt-5"}}`,
			`{"timestamp":"` + started.Add(2*time.Second).Format(time.RFC3339) + `","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15},"total_token_usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}}`,
		}
		if err := os.WriteFile(filepath.Join(sessionsRoot, sessionID+".jsonl"), []byte(strings.Join(sessionLines, "\n")+"\n"), 0o600); err != nil {
			t.Fatalf("write session %s: %v", sessionID, err)
		}
	}
	return gateBFixture{root: root, dataRoot: dataRoot, projectRoot: projectRoot, vaultRoot: vaultRoot, sessionsRoot: sessionsRoot, projectID: projectID, gitExecutable: gitExecutable, mapping: mapping, base: base, scanNow: scanNow}
}

func preparePersistentGateBRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fmt.Errorf("persistent Gate B fixture root must be absolute and clean: %q", root)
	}
	for candidate := root; ; candidate = filepath.Dir(candidate) {
		info, err := os.Lstat(candidate)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("persistent Gate B fixture path has a symlink ancestor: %s", candidate)
		}
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("inspect persistent Gate B fixture ancestor %s: %w", candidate, err)
		}
		if filepath.Dir(candidate) == candidate {
			break
		}
	}
	info, err := os.Lstat(root)
	switch {
	case os.IsNotExist(err):
		if err := os.Mkdir(root, 0o700); err != nil {
			return fmt.Errorf("create persistent Gate B fixture root: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("inspect persistent Gate B fixture root: %w", err)
	case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("persistent Gate B fixture root is not a real directory: %s", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read persistent Gate B fixture root: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("persistent Gate B fixture root must be empty: %s", root)
	}
	return nil
}

func TestGateBEndToEndPublicationAndIdempotence(t *testing.T) {
	runGateBEndToEnd(t)
}

func runGateBEndToEnd(t *testing.T) {
	t.Helper()
	fixture := newGateBFixture(t, "")
	dataRoot, projectRoot, vaultRoot, sessionsRoot, projectID := fixture.dataRoot, fixture.projectRoot, fixture.vaultRoot, fixture.sessionsRoot, fixture.projectID
	mapping, base, scanNow := fixture.mapping, fixture.base, fixture.scanNow
	processRecorder := newGateGitRecorder(t, fixture.gitExecutable, projectRoot)

	phases := []string{}
	cuOpts := contextupdate.Options{
		ProjectID:    projectID,
		SessionsRoot: sessionsRoot,
		DataRoot:     dataRoot,
		RunGit:       processRecorder.run,
		Now:          func() time.Time { return scanNow },
		PhaseObserver: func(phase string) error {
			phases = append(phases, phase)
			return nil
		},
	}

	result, err := contextupdate.Run(context.Background(), cuOpts)
	if err != nil {
		t.Fatalf("contextupdate.Run: %v", err)
	}
	if result.GenerationID == "" {
		t.Fatal("expected non-empty GenerationID")
	}
	if result.ReviewRunTokens != 0 {
		t.Fatalf("expected 0 review run tokens, got %d", result.ReviewRunTokens)
	}

	// Verify files in Project and Vault match
	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	pubID, _, err := store.LoadPublished()
	if err != nil || pubID != result.GenerationID {
		t.Fatalf("published pointer mismatch: pubID=%s resID=%s", pubID, result.GenerationID)
	}

	pReview, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath)))
	if err != nil {
		t.Fatalf("read project review: %v", err)
	}
	vReview, err := os.ReadFile(filepath.Join(vaultRoot, "Projects", projectID, "Session Review", "项目回顾.md"))
	if err != nil {
		t.Fatalf("read vault review: %v", err)
	}
	if string(pReview) != string(vReview) {
		t.Fatal("project and vault review contents differ")
	}
	pHistory, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(reviewv2.HistoryRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	pLedger, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(reviewv2.MachineLedgerRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	pIndex, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(presentation.SessionIndexRelativePath)))
	if err != nil {
		t.Fatalf("new scan did not publish session index: %v", err)
	}
	acceptedV4, err := reviewv4.LoadProjection(pReview, pHistory, pLedger, pIndex)
	if err != nil || acceptedV4.Ledger.DocumentProjection == nil || len(acceptedV4.SessionIndex.Sessions) != 154 {
		t.Fatalf("new scan did not publish accepted v4 Markdown: projection=%+v err=%v", acceptedV4, err)
	}

	// Second run reflects newly created projection files in git status
	result2, err := contextupdate.Run(context.Background(), cuOpts)
	if err != nil {
		t.Fatalf("second contextupdate.Run: %v", err)
	}
	// Third run with stable git status and unchanged inputs is completely idempotent
	result3, err := contextupdate.Run(context.Background(), cuOpts)
	if err != nil {
		t.Fatalf("third contextupdate.Run: %v", err)
	}
	if result3.GenerationID != result2.GenerationID {
		t.Fatalf("generation changed on unchanged third run: got=%s want=%s", result3.GenerationID, result2.GenerationID)
	}
	seededMilestone := seedGateBV4Timeline(t, dataRoot, projectID, mapping, scanNow)

	// Distinct Project/Vault human edits are merged while a genuinely new
	// source session advances the machine index in the same scan publication.
	projectReviewPath := filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	vaultReviewPath := filepath.Join(vaultRoot, "Projects", projectID, "Session Review", "项目回顾.md")
	vaultHistoryPath := filepath.Join(vaultRoot, "Projects", projectID, "Session Review", "项目历史.md")
	editV4Field(t, projectReviewPath, reviewv2.ReviewRelativePath, reviewv4.FieldKey{Entity: "project-overview", Name: "status"}, "人工确认状态")
	editV4Field(t, vaultReviewPath, reviewv2.ReviewRelativePath, reviewv4.FieldKey{Entity: "project-overview", Name: "goal"}, "Vault 人工目标")
	editV4Field(t, vaultHistoryPath, reviewv2.HistoryRelativePath, reviewv4.FieldKey{Entity: "milestone:" + seededMilestone.ID, Name: "conclusion"}, "人工确认的历史结论")
	session2 := []string{
		`{"timestamp":"` + base.Add(5*time.Minute).Format(time.RFC3339) + `","type":"session_meta","payload":{"id":"session-155","cwd":"` + filepath.ToSlash(projectRoot) + `","source":"codex"}}`,
		`{"timestamp":"` + base.Add(5*time.Minute+time.Second).Format(time.RFC3339) + `","type":"turn_context","payload":{"cwd":"` + filepath.ToSlash(projectRoot) + `","model":"gpt-5"}}`,
	}
	if err := os.WriteFile(filepath.Join(sessionsRoot, "session-155.jsonl"), []byte(strings.Join(session2, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result4, err := contextupdate.Run(context.Background(), cuOpts)
	if err != nil {
		t.Fatalf("human-edit contextupdate.Run: %v", err)
	}
	if result4.GenerationID == result3.GenerationID {
		t.Fatal("new source session did not advance the scan generation")
	}
	projectionPaths := gateBProjectionPaths(projectRoot, vaultRoot, projectID)
	beforeSync := readGateBFiles(t, projectionPaths)
	publishes := 0
	syncOptions := syncproject.Options{ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS, Now: func() time.Time { return scanNow }, Trigger: syncengine.TriggerPeriodic}
	syncOptions.RecoverMarkdown = func(ctx context.Context, owner *publicationlock.Owner) error {
		return publication.RecoverMarkdownLocked(ctx, publication.Options{ProjectID: projectID, Mapping: mapping, DataRoot: dataRoot, Now: func() time.Time { return scanNow }}, owner)
	}
	syncOptions.PublishMarkdown = func(ctx context.Context, plan syncproject.MarkdownSyncPlan, owner *publicationlock.Owner) error {
		publishes++
		_, err := publication.PublishMarkdownEditLocked(ctx, publication.Options{ProjectID: projectID, Mapping: mapping, DataRoot: dataRoot, Now: func() time.Time { return scanNow }}, plan, owner)
		return err
	}
	syncReport, err := syncproject.RunMarkdown(context.Background(), syncOptions)
	if err != nil || publishes != 0 || len(syncReport.Operations) != 0 {
		t.Fatalf("post-scan Markdown sync was not a no-op: publishes=%d operations=%+v err=%v", publishes, syncReport.Operations, err)
	}
	if afterSync := readGateBFiles(t, projectionPaths); !reflect.DeepEqual(afterSync, beforeSync) {
		t.Fatal("post-scan Markdown sync changed Project/Vault projection bytes")
	}
	for side, root := range map[string]string{
		"project": filepath.Join(projectRoot, "docs", "session-review"),
		"vault":   filepath.Join(vaultRoot, "Projects", projectID, "Session Review"),
	} {
		accepted := loadGateBV4(t, root)
		milestone := gateBMilestone(t, accepted, seededMilestone.ID)
		if accepted.Review.CurrentState.Status != "人工确认状态" || accepted.Review.CurrentState.Goal != "Vault 人工目标" || len(accepted.SessionIndex.Sessions) != 155 || milestone.ClosedLoop.Conclusion.Kind != reviewv4.ConclusionHumanConfirmed || milestone.ClosedLoop.Conclusion.Text != "人工确认的历史结论" || !reflect.DeepEqual(milestone.ClosedLoop.Verification, seededMilestone.ClosedLoop.Verification) || !reflect.DeepEqual(milestone.ClosedLoop.SourceTurnRefs, seededMilestone.ClosedLoop.SourceTurnRefs) {
			t.Fatalf("%s merged scan lost human edits or source facts: review=%+v sessions=%d", side, accepted.Review.CurrentState, len(accepted.SessionIndex.Sessions))
		}
	}
	ledgerBody, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(reviewv2.MachineLedgerRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	machine, err := reviewv4.DecodeLedger(ledgerBody)
	if err != nil {
		t.Fatal(err)
	}
	foundHumanStatus := false
	var statusBaseline reviewv4.Baseline
	for _, patch := range machine.HumanPatches {
		if patch.EntityID == "project-overview" && patch.Field == "status" && patch.Operation == "set" && patch.Value != nil && *patch.Value == "人工确认状态" {
			foundHumanStatus = true
		}
	}
	for _, baseline := range machine.GeneratedBaselines {
		if baseline.EntityID == "project-overview" && baseline.Field == "status" {
			statusBaseline = baseline
		}
	}
	if !foundHumanStatus {
		t.Fatalf("human status patch was not persisted: %+v", machine.HumanPatches)
	}
	if statusBaseline.Value == nil {
		t.Fatalf("human status baseline was not persisted: %+v", machine.GeneratedBaselines)
	}

	// Restore the generated value, which removes the active patch but retains
	// the authenticated baseline. A later real source session must carry that
	// unpatched baseline before the field can be edited again.
	editV4Field(t, projectReviewPath, reviewv2.ReviewRelativePath, reviewv4.FieldKey{Entity: "project-overview", Name: "status"}, *statusBaseline.Value)
	publishes = 0
	if _, err := syncproject.RunMarkdown(context.Background(), syncOptions); err != nil || publishes != 1 {
		t.Fatalf("restoring generated status was not accepted: publishes=%d err=%v", publishes, err)
	}
	restored := loadGateBV4(t, filepath.Join(projectRoot, "docs", "session-review"))
	for _, patch := range restored.Review.HumanPatches {
		if patch.EntityID == "project-overview" && patch.Field == "status" {
			t.Fatalf("restored status retained an active patch: %+v", restored.Review.HumanPatches)
		}
	}
	restoredBaseline := reviewv4.Baseline{}
	for _, baseline := range restored.Review.GeneratedBaselines {
		if baseline.EntityID == "project-overview" && baseline.Field == "status" {
			restoredBaseline = baseline
		}
	}
	if restoredBaseline.Value == nil || *restoredBaseline.Value != *statusBaseline.Value || restoredBaseline.GeneratedHash != statusBaseline.GeneratedHash {
		t.Fatalf("restore changed or removed the original status baseline: got=%+v want=%+v", restoredBaseline, statusBaseline)
	}

	session3 := []string{
		`{"timestamp":"` + base.Add(6*time.Minute).Format(time.RFC3339) + `","type":"session_meta","payload":{"id":"session-156","cwd":"` + filepath.ToSlash(projectRoot) + `","source":"codex"}}`,
		`{"timestamp":"` + base.Add(6*time.Minute+time.Second).Format(time.RFC3339) + `","type":"turn_context","payload":{"cwd":"` + filepath.ToSlash(projectRoot) + `","model":"gpt-5"}}`,
	}
	if err := os.WriteFile(filepath.Join(sessionsRoot, "session-156.jsonl"), []byte(strings.Join(session3, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result5, err := contextupdate.Run(context.Background(), cuOpts)
	if err != nil {
		t.Fatalf("new generation after restoring generated status: %v", err)
	}
	if result5.GenerationID == result4.GenerationID {
		t.Fatal("new source session after restore did not advance the scan generation")
	}
	carried := loadGateBV4(t, filepath.Join(projectRoot, "docs", "session-review"))
	for _, baseline := range carried.Review.GeneratedBaselines {
		if baseline.EntityID == "project-overview" && baseline.Field == "status" {
			if baseline.GenerationID != result5.GenerationID || baseline.Value == nil || *baseline.Value != *statusBaseline.Value || baseline.GeneratedHash != statusBaseline.GeneratedHash {
				t.Fatalf("restored status baseline did not carry exactly: got=%+v original=%+v generation=%s", baseline, statusBaseline, result5.GenerationID)
			}
		}
	}

	editV4Field(t, projectReviewPath, reviewv2.ReviewRelativePath, reviewv4.FieldKey{Entity: "project-overview", Name: "status"}, "第二次人工状态")
	publishes = 0
	if _, err := syncproject.RunMarkdown(context.Background(), syncOptions); err != nil || publishes != 1 {
		t.Fatalf("second edit after restore and generation advance was not accepted: publishes=%d err=%v", publishes, err)
	}
	if reopened := loadGateBV4(t, filepath.Join(projectRoot, "docs", "session-review")); reopened.Review.CurrentState.Status != "第二次人工状态" {
		t.Fatalf("second edit missing after reopen: %q", reopened.Review.CurrentState.Status)
	}
	if _, err := contextupdate.Run(context.Background(), cuOpts); err != nil {
		t.Fatalf("post-human-edit idempotence run: %v", err)
	}

	// A later wall clock with byte-identical source/project facts reuses the
	// immutable generation and must not rewrite the public Project/Vault projection.
	beforePrepared, _, err := store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	beforePublished, _, err := store.LoadPublished()
	if err != nil {
		t.Fatal(err)
	}
	publicPaths := []string{
		filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath)),
		filepath.Join(projectRoot, filepath.FromSlash(reviewv2.HistoryRelativePath)),
		filepath.Join(projectRoot, filepath.FromSlash(reviewv2.MachineLedgerRelativePath)),
		filepath.Join(projectRoot, filepath.FromSlash(presentation.SessionIndexRelativePath)),
		filepath.Join(vaultRoot, "Projects", projectID, "Session Review", "项目回顾.md"),
		filepath.Join(vaultRoot, "Projects", projectID, "Session Review", "项目历史.md"),
		filepath.Join(vaultRoot, "Projects", projectID, "Session Review", ".session-reviewer", "ledger.json"),
		filepath.Join(vaultRoot, "Projects", projectID, "Session Review", ".session-reviewer", "session-index.json"),
	}
	beforePublic := make(map[string][]byte, len(publicPaths))
	beforeModTime := make(map[string]time.Time, len(publicPaths))
	for _, publicPath := range publicPaths {
		beforePublic[publicPath], err = os.ReadFile(publicPath)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(publicPath)
		if err != nil {
			t.Fatal(err)
		}
		beforeModTime[publicPath] = info.ModTime()
	}
	scanNow = scanNow.Add(time.Minute)
	auditOnly, err := contextupdate.Run(context.Background(), cuOpts)
	if err != nil {
		t.Fatalf("audit-only contextupdate.Run: %v", err)
	}
	afterPrepared, _, err := store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	afterPublished, _, err := store.LoadPublished()
	if err != nil {
		t.Fatal(err)
	}
	if afterPrepared.GenerationID != beforePrepared.GenerationID || afterPublished != beforePublished || auditOnly.GenerationID != beforePublished {
		t.Fatalf("audit-only generation leaked into public projection: before prepared=%s published=%s; after prepared=%s published=%s result=%s",
			beforePrepared.GenerationID, beforePublished, afterPrepared.GenerationID, afterPublished, auditOnly.GenerationID)
	}
	for _, publicPath := range publicPaths {
		afterBody, err := os.ReadFile(publicPath)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(publicPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(afterBody, beforePublic[publicPath]) || !info.ModTime().Equal(beforeModTime[publicPath]) {
			t.Fatalf("audit-only scan rewrote %s", publicPath)
		}
	}
}

func seedGateBV4Timeline(t *testing.T, dataRoot, projectID string, mapping config.ProjectMapping, now time.Time) reviewv4.Timeline {
	return seedGateBV4Acceptance(t, dataRoot, projectID, mapping, now, 1).milestone
}

type gateBV4Seed struct {
	milestone reviewv4.Timeline
	decision  reviewv4.Decision
}

func seedGateBV4Acceptance(t *testing.T, dataRoot, projectID string, mapping config.ProjectMapping, now time.Time, milestoneCount int) gateBV4Seed {
	t.Helper()
	if milestoneCount < 1 {
		t.Fatal("Gate B fixture requires at least one accepted milestone")
	}
	owner, err := publicationlock.Acquire(dataRoot, projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	read, err := syncproject.ReadMarkdownForScan(context.Background(), syncproject.Options{ProjectID: projectID, CWD: mapping.Root, DataDir: dataRoot, GOOS: runtime.GOOS, Now: func() time.Time { return now }, Trigger: syncengine.TriggerPeriodic}, owner)
	if releaseErr := owner.Release(); err != nil || releaseErr != nil {
		t.Fatalf("read accepted Markdown for seed: err=%v release=%v", err, releaseErr)
	}
	next := read.OldAccepted.Review
	closed := reviewv4.NeutralClosedLoop()
	closed.Verification.State = "present"
	closed.Verification.Text = "seed verification stays exact"
	closed.Verification.MissingReason = nil
	milestone := reviewv4.Timeline{ID: "gate-b-seeded", GenerationID: next.GenerationID, OccurredAt: "2026-09-05T00:00:00Z", Kind: "milestone", Title: "Seeded accepted history", Summary: "Trusted typed seed for the human-edit chain.", DecisionIDs: []string{}, ClosedLoop: closed}
	next.Timeline = append(next.Timeline, milestone)
	seed := gateBV4Seed{milestone: milestone}
	for index := 2; index <= milestoneCount; index++ {
		next.Timeline = append(next.Timeline, reviewv4.Timeline{
			ID: fmt.Sprintf("gate-b-seeded-%02d", index), GenerationID: next.GenerationID,
			OccurredAt: fmt.Sprintf("2026-09-05T00:%02d:00Z", index-1), Kind: "milestone",
			Title: fmt.Sprintf("Seeded accepted history %02d", index), Summary: "Trusted typed acceptance fixture; no automatic promotion is claimed.",
			DecisionIDs: []string{}, ClosedLoop: reviewv4.NeutralClosedLoop(),
		})
	}
	if milestoneCount > 1 {
		decision := reviewv4.Decision{
			ID: "gate-b-decision", Kind: "decision", OccurredAt: "2026-09-05T00:00:00Z",
			Title: "Seeded accepted decision", Rationale: "Typed fixture rationale", Impact: "Fixture-only impact",
			Status: reviewv4.DecisionActive, ReevaluateWhen: "When fixture requirements change",
			Supersedes: []string{}, MilestoneIDs: []string{milestone.ID}, SessionRefs: []reviewv4.SessionRef{},
			Provenance: "human_created", Pinned: true, Revision: 1,
		}
		next.Decisions = append(next.Decisions, decision)
		next.Timeline[len(next.Timeline)-milestoneCount].DecisionIDs = []string{decision.ID}
		problem := reviewv4.ProblemNode{
			ID: "gate-b-problem", Question: "Does the accepted problem graph survive a Markdown edit?",
			RelatedNodeIDs: []string{}, WorkflowState: "not_started", AnswerState: "no_answer",
			CompletionCriterion: "The graph is byte-for-byte equivalent after scan and sync.", CurrentConclusion: "",
			SourceTurnRefs: []reviewv4.SourceTurnRef{}, Provenance: "human_created", FirstProposedAt: "2026-09-05T00:00:00Z",
			SiblingOrder: 0, Revision: 1,
		}
		next.ProblemMapRevision = 1
		next.ProblemNodes = append(next.ProblemNodes, problem)
		next.ProblemRootIDs = append(next.ProblemRootIDs, problem.ID)
		seed.decision = decision
	}
	next.Revision++
	pair, err := reviewv4.RenderMarkdown(next, read.OldAccepted.Ledger, &read.AcceptedPair)
	if err != nil {
		t.Fatal(err)
	}
	ledger := read.OldAccepted.Ledger
	ledger.AcceptedRevision = next.Revision
	ledger.DocumentProjection.PresentationBase = next
	ledger.ReviewSHA256, ledger.HistorySHA256 = gateBHash(pair.Review), gateBHash(pair.History)
	ledger.SyncHashes.ReviewSHA256, ledger.SyncHashes.HistorySHA256 = ledger.ReviewSHA256, ledger.HistorySHA256
	ledgerBody, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath, presentation.SessionIndexRelativePath}
	desired := map[string][]byte{reviewv2.ReviewRelativePath: pair.Review, reviewv2.HistoryRelativePath: pair.History, reviewv2.MachineLedgerRelativePath: ledgerBody, presentation.SessionIndexRelativePath: read.ProjectExpected[presentation.SessionIndexRelativePath]}
	files := make([]presentation.FilePlan, 0, len(paths))
	for _, relative := range paths {
		files = append(files, presentation.FilePlan{Relative: relative, Expected: read.ProjectExpected[relative], ExpectedExists: true, Desired: desired[relative], Mode: 0o600})
	}
	plan := syncproject.MarkdownSyncPlan{Plan: presentation.RenderPlan{ProjectID: projectID, GenerationID: next.GenerationID, ProjectViewDigest: next.ProjectViewDigest, Files: files}, Index: desired[presentation.SessionIndexRelativePath], ExpectedGenerationID: next.GenerationID, ExpectedIndexDigest: read.OldAccepted.SessionIndex.Digest, ProjectExpected: read.ProjectExpected, VaultExpected: read.VaultExpected, ExpectedReceiptRevision: read.ExpectedReceiptRevision, ExpectedBaseDigest: read.ExpectedBaseDigest}
	if _, err := publication.PublishMarkdownScan(context.Background(), publication.Options{ProjectID: projectID, Mapping: mapping, DataRoot: dataRoot, Now: func() time.Time { return now }}, plan); err != nil {
		t.Fatalf("seed accepted typed history: %v", err)
	}
	return seed
}

func gateBMilestone(t *testing.T, accepted reviewv4.Accepted, id string) reviewv4.Timeline {
	t.Helper()
	for _, milestone := range accepted.Review.Timeline {
		if milestone.ID == id {
			return milestone
		}
	}
	t.Fatalf("milestone %q missing", id)
	return reviewv4.Timeline{}
}

func gateBHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func gateBProjectionPaths(projectRoot, vaultRoot, projectID string) []string {
	paths := make([]string, 0, 8)
	for _, relative := range []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath, presentation.SessionIndexRelativePath} {
		paths = append(paths, filepath.Join(projectRoot, filepath.FromSlash(relative)))
		paths = append(paths, filepath.Join(vaultRoot, "Projects", projectID, "Session Review", filepath.FromSlash(strings.TrimPrefix(relative, "docs/session-review/"))))
	}
	return paths
}

func readGateBFiles(t *testing.T, paths []string) map[string]string {
	t.Helper()
	result := make(map[string]string, len(paths))
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		result[path] = string(body)
	}
	return result
}

func TestGateBLegacyV3ProjectionStaysOnTheLegacyScanPath(t *testing.T) {
	dataRoot, projectRoot, vaultRoot, sessionsRoot := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	projectID := "project-gate-b-legacy"
	if err := os.WriteFile(filepath.Join(projectRoot, "VERSION"), []byte("v1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "project-fixture.md"), []byte("# fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	initializeGateRepository(t, projectRoot)
	mapping := config.ProjectMapping{ID: projectID, Root: projectRoot, VaultRoot: vaultRoot, VaultReviewPath: "Projects/" + projectID + "/Session Review", VaultCaseMode: platform.CaseSensitive}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{mapping}}); err != nil {
		t.Fatal(err)
	}
	seedGateBLegacyV3(t, projectRoot, mapping)

	started := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	lines := []string{
		`{"timestamp":"` + started.Format(time.RFC3339) + `","type":"session_meta","payload":{"id":"legacy-session","cwd":"` + filepath.ToSlash(projectRoot) + `","source":"codex"}}`,
		`{"timestamp":"` + started.Add(time.Second).Format(time.RFC3339) + `","type":"turn_context","payload":{"cwd":"` + filepath.ToSlash(projectRoot) + `","model":"gpt-5"}}`,
	}
	if err := os.WriteFile(filepath.Join(sessionsRoot, "legacy-session.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := contextupdate.Run(context.Background(), contextupdate.Options{ProjectID: projectID, SessionsRoot: sessionsRoot, DataRoot: dataRoot, Now: func() time.Time { return started.Add(time.Minute) }}); err != nil {
		t.Fatalf("legacy v3 contextupdate.Run: %v", err)
	}
	read := func(relative string) []byte {
		body, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	accepted, err := reviewv2.LoadV3Bytes(read(reviewv2.ReviewRelativePath), read(reviewv2.HistoryRelativePath), read(reviewv2.MachineLedgerRelativePath))
	if err != nil || accepted.State.Review.ProjectID != projectID || len(accepted.State.Machine.Sessions) != 1 {
		t.Fatalf("legacy v3 ordinary scan did not remain valid: accepted=%+v err=%v", accepted, err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, filepath.FromSlash(presentation.SessionIndexRelativePath))); !os.IsNotExist(err) {
		t.Fatalf("legacy v3 scan silently published a v4 index: %v", err)
	}
}

func seedGateBLegacyV3(t *testing.T, projectRoot string, mapping config.ProjectMapping) {
	t.Helper()
	view := memory.ProjectView{
		SchemaVersion: 1, ProjectID: mapping.ID, Generation: 1,
		StartedAt: "2026-08-30T00:00:00Z", EndedAt: "2026-08-30T00:00:01Z",
		SessionViewDependencies: []memory.SessionViewDependency{}, ObservationRevisionIDs: []string{},
		ProbeStateDigest: "sha256:" + strings.Repeat("1", 64), DependencyDigest: "sha256:" + strings.Repeat("2", 64),
		WitnessedState: []memory.DerivedRecord{}, DerivedRecords: []memory.DerivedRecord{}, AssociatedUsage: []memory.AssociatedUsage{}, ReducerVersion: "project-view-v1",
	}
	var err error
	view.Digest, err = memory.ProjectViewDigest(view)
	if err != nil {
		t.Fatal(err)
	}
	input := presentation.ProjectInput{
		ProjectView: view, GenerationID: "generation-legacy-seed", Revision: 1,
		Legacy: reviewv2.LegacyPresentation{Review: reviewv2.Review{Name: "Legacy", Goal: "legacy goal", Stage: "legacy", Status: "active", NextAction: "scan"}, Events: []reviewv2.Event{}, Compatibility: reviewv2.LegacyCompatibility{}},
	}
	output, err := presentation.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := presentation.Render(input, output)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range plan.Files {
		full := filepath.Join(projectRoot, filepath.FromSlash(file.Relative))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, file.Desired, file.Mode); err != nil {
			t.Fatal(err)
		}
	}
}

func editV4Field(t *testing.T, fullPath, relative string, key reviewv4.FieldKey, value string) {
	t.Helper()
	body, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatal(err)
	}
	document, err := reviewv4.ParseMarkdownDocument(filepath.Base(relative), body)
	if err != nil {
		t.Fatal(err)
	}
	next, err := document.ReplaceFields(map[reviewv4.FieldKey]string{key: value})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, next, 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadGateBV4(t *testing.T, root string) reviewv4.Accepted {
	t.Helper()
	read := func(relative string) []byte {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	accepted, err := reviewv4.LoadProjection(read("项目回顾.md"), read("项目历史.md"), read(".session-reviewer/ledger.json"), read(".session-reviewer/session-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}
