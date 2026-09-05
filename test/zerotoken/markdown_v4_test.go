package zerotoken

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/contextupdate"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

// TestMarkdownV4EndToEnd reuses the Gate B publication harness. Setting
// SESSION_REVIEWER_M9_FIXTURE_ROOT opts into a persistent, final-root fixture
// for the controller's later native Obsidian experiment.
func TestMarkdownV4EndToEnd(t *testing.T) {
	runMarkdownV4EndToEnd(t, os.Getenv("SESSION_REVIEWER_M9_FIXTURE_ROOT"))
}

func TestMarkdownV4ContextUpdateUsesProvidedProcessRecorder(t *testing.T) {
	fixture := newGateBFixture(t, "")
	sentinel := errors.New("M9 recorder observed Git")
	calls := 0
	_, err := contextupdate.Run(context.Background(), contextupdate.Options{
		ProjectID: fixture.projectID, SessionsRoot: fixture.sessionsRoot, DataRoot: fixture.dataRoot,
		Now: func() time.Time { return fixture.scanNow },
		RunGit: func(context.Context, string, ...string) ([]byte, error) {
			calls++
			return nil, sentinel
		},
	})
	if calls != 1 || !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "probe project state") {
		t.Fatalf("context update bypassed provided process recorder: calls=%d err=%v", calls, err)
	}
}

func TestMarkdownV4PersistentFixtureRefusesNonemptyAndSymlinkedRoots(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	nonempty := filepath.Join(parent, "nonempty")
	if err := os.Mkdir(nonempty, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nonempty, "keep"), []byte("do not overwrite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := preparePersistentGateBRoot(nonempty); err == nil || !strings.Contains(err.Error(), "must be empty") {
		t.Fatalf("nonempty persistent root was not refused: %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	realParent := filepath.Join(parent, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	linkParent := filepath.Join(parent, "link")
	if err := os.Symlink(realParent, linkParent); err != nil {
		t.Fatal(err)
	}
	rootThroughLink := filepath.Join(linkParent, "fixture")
	if err := preparePersistentGateBRoot(rootThroughLink); err == nil || !strings.Contains(err.Error(), "symlink ancestor") {
		t.Fatalf("symlinked persistent root was not refused: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(realParent, "fixture")); !os.IsNotExist(err) {
		t.Fatalf("refused symlink-root provision changed target: %v", err)
	}
}

func runMarkdownV4EndToEnd(t *testing.T, persistentRoot string) {
	t.Helper()
	fixture := newGateBFixture(t, persistentRoot)
	recorder := newGateGitRecorder(t, fixture.gitExecutable, fixture.projectRoot)
	scanNow := fixture.scanNow
	options := contextupdate.Options{
		ProjectID: fixture.projectID, SessionsRoot: fixture.sessionsRoot, DataRoot: fixture.dataRoot,
		Now: func() time.Time { return scanNow }, RunGit: recorder.run,
	}
	reviewRunTokens := 0
	runScan := func(label string) contextupdate.Result {
		t.Helper()
		result, err := contextupdate.Run(context.Background(), options)
		if err != nil {
			t.Fatalf("%s context update: %v", label, err)
		}
		reviewRunTokens += result.ReviewRunTokens
		return result
	}
	first := runScan("initial 154-session")
	if first.GenerationID == "" || first.IndexedSessions != 154 {
		t.Fatalf("initial scan did not publish 154 sessions: %+v", first)
	}
	// The projection itself changes Git status after first publication. Stabilize
	// that deterministic project fact before seeding the accepted M9 history.
	runScan("projection-status stabilization")
	seed := seedGateBV4Acceptance(t, fixture.dataRoot, fixture.projectID, fixture.mapping, scanNow, 16)
	seeded := loadGateBV4(t, filepath.Join(fixture.projectRoot, "docs", "session-review"))
	if len(seeded.Review.Timeline) < 16 || len(seeded.SessionIndex.Sessions) != 154 {
		t.Fatalf("typed fixture seed is incomplete: milestones=%d sessions=%d", len(seeded.Review.Timeline), len(seeded.SessionIndex.Sessions))
	}
	seededMilestone := gateBMilestone(t, seeded, seed.milestone.ID)
	verificationBefore := seededMilestone.ClosedLoop.Verification
	sourceRefsBefore := seededMilestone.ClosedLoop.SourceTurnRefs
	problemRevisionBefore := seeded.Review.ProblemMapRevision
	graphBefore := append([]reviewv4.ProblemNode(nil), seeded.Review.ProblemNodes...)
	rootsBefore := append([]string(nil), seeded.Review.ProblemRootIDs...)

	projectReview := filepath.Join(fixture.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	vaultReview := filepath.Join(fixture.vaultRoot, "Projects", fixture.projectID, "Session Review", "项目回顾.md")
	vaultHistory := filepath.Join(fixture.vaultRoot, "Projects", fixture.projectID, "Session Review", "项目历史.md")
	editV4Field(t, projectReview, reviewv2.ReviewRelativePath, reviewv4.FieldKey{Entity: "project-overview", Name: "goal"}, "人工目标")
	editV4Field(t, vaultReview, reviewv2.ReviewRelativePath, reviewv4.FieldKey{Entity: "decision:" + seed.decision.ID, Name: "rationale"}, "人工决策理由")
	editV4Field(t, vaultHistory, reviewv2.HistoryRelativePath, reviewv4.FieldKey{Entity: "milestone:" + seed.milestone.ID, Name: "conclusion"}, "人工历史结论")
	appendGateBCustomMarkdown(t, projectReview, "\n## M9 人工自定义区\n\nProject 侧原样保留。\n")
	appendGateBCustomMarkdown(t, vaultHistory, "\n## M9 历史自定义区\n\nVault 侧原样保留。\n")

	merged := runScan("human Markdown merge")
	if merged.IndexedSessions != 154 {
		t.Fatalf("human-edit scan truncated cumulative index: %+v", merged)
	}
	paths := gateBProjectionPaths(fixture.projectRoot, fixture.vaultRoot, fixture.projectID)
	beforeSync := readGateBFiles(t, paths)
	publishes := 0
	syncOptions := syncproject.Options{ProjectID: fixture.projectID, CWD: fixture.projectRoot, DataDir: fixture.dataRoot, GOOS: runtime.GOOS, Now: func() time.Time { return scanNow }, Trigger: syncengine.TriggerPeriodic}
	syncOptions.RecoverMarkdown = func(ctx context.Context, owner *publicationlock.Owner) error {
		return publication.RecoverMarkdownLocked(ctx, publication.Options{ProjectID: fixture.projectID, Mapping: fixture.mapping, DataRoot: fixture.dataRoot, Now: func() time.Time { return scanNow }}, owner)
	}
	syncOptions.PublishMarkdown = func(ctx context.Context, plan syncproject.MarkdownSyncPlan, owner *publicationlock.Owner) error {
		publishes++
		_, err := publication.PublishMarkdownEditLocked(ctx, publication.Options{ProjectID: fixture.projectID, Mapping: fixture.mapping, DataRoot: fixture.dataRoot, Now: func() time.Time { return scanNow }}, plan, owner)
		return err
	}
	report, err := syncproject.RunMarkdown(context.Background(), syncOptions)
	if err != nil || publishes != 0 || len(report.Operations) != 0 {
		t.Fatalf("post-scan sync was not a no-op: publishes=%d operations=%+v err=%v", publishes, report.Operations, err)
	}
	if got := readGateBFiles(t, paths); !reflect.DeepEqual(got, beforeSync) {
		t.Fatal("post-scan sync changed projection bytes")
	}

	for side, root := range map[string]string{
		"project": filepath.Join(fixture.projectRoot, "docs", "session-review"),
		"vault":   filepath.Join(fixture.vaultRoot, "Projects", fixture.projectID, "Session Review"),
	} {
		accepted := loadGateBV4(t, root)
		if len(accepted.Review.Timeline) < 16 {
			t.Fatalf("%s reopened incomplete milestone history: %d", side, len(accepted.Review.Timeline))
		}
		milestone := gateBMilestone(t, accepted, seed.milestone.ID)
		_ = gateBMilestone(t, accepted, "gate-b-seeded-16")
		decision := gateBDecision(t, accepted, seed.decision.ID)
		if accepted.Review.CurrentState.Goal != "人工目标" || decision.Rationale != "人工决策理由" || len(accepted.SessionIndex.Sessions) != 154 {
			t.Fatalf("%s lost human fields or sessions: goal=%q rationale=%q sessions=%d", side, accepted.Review.CurrentState.Goal, decision.Rationale, len(accepted.SessionIndex.Sessions))
		}
		if milestone.ClosedLoop.Conclusion.Kind != reviewv4.ConclusionHumanConfirmed || milestone.ClosedLoop.Conclusion.Text != "人工历史结论" {
			t.Fatalf("%s did not persist human-confirmed conclusion: %+v", side, milestone.ClosedLoop.Conclusion)
		}
		if !reflect.DeepEqual(milestone.ClosedLoop.Verification, verificationBefore) || !reflect.DeepEqual(milestone.ClosedLoop.SourceTurnRefs, sourceRefsBefore) {
			t.Fatalf("%s conclusion edit changed verification or source graph: verification got=%+v want=%+v refs got=%+v want=%+v", side, milestone.ClosedLoop.Verification, verificationBefore, milestone.ClosedLoop.SourceTurnRefs, sourceRefsBefore)
		}
		if accepted.Review.ProblemMapRevision != problemRevisionBefore || !reflect.DeepEqual(accepted.Review.ProblemNodes, graphBefore) || !reflect.DeepEqual(accepted.Review.ProblemRootIDs, rootsBefore) {
			t.Fatalf("%s human edit changed accepted problem graph", side)
		}
	}
	store, err := memorystore.Open(fixture.dataRoot, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	publishedID, publishedManifest, loadErr := store.LoadPublished()
	closeErr := store.Close()
	acceptedProject := loadGateBV4(t, filepath.Join(fixture.projectRoot, "docs", "session-review"))
	if loadErr != nil || closeErr != nil || publishedID != acceptedProject.Review.GenerationID || publishedManifest.ProjectViewDigest != acceptedProject.Review.ProjectViewDigest || publishedManifest.SessionIndexDigest != acceptedProject.SessionIndex.Digest {
		t.Fatalf("public projection is not bound to private acceptance: published=%s manifest=%+v load=%v close=%v", publishedID, publishedManifest, loadErr, closeErr)
	}
	projectReviewBody, err := os.ReadFile(projectReview)
	if err != nil {
		t.Fatal(err)
	}
	projectHistoryBody, err := os.ReadFile(filepath.Join(fixture.projectRoot, filepath.FromSlash(reviewv2.HistoryRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	for label, body := range map[string][]byte{"review": projectReviewBody, "history": projectHistoryBody} {
		if !bytes.Contains(body, []byte("M9 ")) || !bytes.Contains(body, []byte("原样保留")) {
			t.Fatalf("%s custom Markdown shell was not preserved", label)
		}
	}
	assertGateBProjectVaultBytesMatch(t, paths)

	// Same inputs, then a later clock, must not rewrite any accepted byte or
	// mtime. These calls also exercise the recorder on the actual M9 path.
	runScan("same-input reopen")
	beforeAudit := readGateBFileSnapshots(t, paths)
	scanNow = scanNow.Add(time.Minute)
	runScan("later-clock reopen")
	if afterAudit := readGateBFileSnapshots(t, paths); !reflect.DeepEqual(afterAudit, beforeAudit) {
		t.Fatal("later-clock reopen changed accepted projection bytes or mtimes")
	}
	recorder.mu.Lock()
	attempts, allowedCalls, rejected := recorder.attempts, len(recorder.calls), recorder.rejected
	recorder.mu.Unlock()
	agentProcesses := rejected
	if reviewRunTokens != 0 || agentProcesses != 0 || attempts == 0 || allowedCalls != attempts {
		t.Fatalf("deterministic path process accounting: review_tokens=%d attempts=%d allowed_git=%d rejected=%d", reviewRunTokens, attempts, allowedCalls, rejected)
	}
	recorder.assertApprovedCalls(t, attempts)
	if persistentRoot != "" {
		writeGateBPersistentFixtureEvidence(t, fixture)
	}
}

type gateBFileSnapshot struct {
	Body    string
	ModTime time.Time
}

func readGateBFileSnapshots(t *testing.T, paths []string) map[string]gateBFileSnapshot {
	t.Helper()
	result := make(map[string]gateBFileSnapshot, len(paths))
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		result[path] = gateBFileSnapshot{Body: string(body), ModTime: info.ModTime()}
	}
	return result
}

func appendGateBCustomMarkdown(t *testing.T, path, custom string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, []byte(custom)...)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func gateBDecision(t *testing.T, accepted reviewv4.Accepted, id string) reviewv4.Decision {
	t.Helper()
	for _, decision := range accepted.Review.Decisions {
		if decision.ID == id {
			return decision
		}
	}
	t.Fatalf("decision %q missing", id)
	return reviewv4.Decision{}
}

func assertGateBProjectVaultBytesMatch(t *testing.T, paths []string) {
	t.Helper()
	if len(paths) != 8 {
		t.Fatalf("projection path count=%d want 8", len(paths))
	}
	for index := 0; index < len(paths); index += 2 {
		projectBody, err := os.ReadFile(paths[index])
		if err != nil {
			t.Fatal(err)
		}
		vaultBody, err := os.ReadFile(paths[index+1])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(projectBody, vaultBody) {
			t.Fatalf("Project/Vault bytes differ for %s", filepath.Base(paths[index]))
		}
	}
}

func writeGateBPersistentFixtureEvidence(t *testing.T, fixture gateBFixture) {
	t.Helper()
	baselineRoot := filepath.Join(fixture.root, "baseline")
	if err := os.Mkdir(baselineRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	var lines []string
	err := filepath.WalkDir(fixture.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(fixture.root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == ".git" || relative == "baseline") {
			if path == fixture.root {
				return nil
			}
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("fixture contains non-regular file %s", relative)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+filepath.ToSlash(relative))
		target := filepath.Join(baselineRoot, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, body, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("snapshot persistent fixture: %v", err)
	}
	sort.Strings(lines)
	manifest := "fixture_root=" + fixture.root + "\nproject_root=" + fixture.projectRoot + "\nvault_root=" + fixture.vaultRoot + "\ndata_root=" + fixture.dataRoot + "\nsessions_root=" + fixture.sessionsRoot + "\n\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(fixture.root, "fixture.sha256"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}
