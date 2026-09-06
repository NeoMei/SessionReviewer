package publication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/migrationv4"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

func TestMarkdownEditPublishesThreeFilesAndNoOpDoesNotRepublish(t *testing.T) {
	env := setupMarkdownPublication(t, "project-markdown-edit")
	beforeIndex := readTestFile(t, filepath.Join(env.projectRoot, filepath.FromSlash(sessionIndexRelativePath)))
	projectReview := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	review := readTestFile(t, projectReview)
	accepted := loadMarkdownProjectionForTest(t, env)
	if len(accepted.Review.Timeline) == 0 {
		t.Fatal("fixture has no milestone conclusion")
	}
	milestone := accepted.Review.Timeline[0]
	review = replaceMarkdownFieldForTest(t, review, "project-overview", "goal", accepted.Review.CurrentState.Goal, "human goal")
	if err := os.WriteFile(projectReview, review, 0o644); err != nil {
		t.Fatal(err)
	}
	projectHistory := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.HistoryRelativePath))
	history := replaceMarkdownFieldForTest(t, readTestFile(t, projectHistory), "milestone:"+milestone.ID, "conclusion", milestone.ClosedLoop.Conclusion.Text, "human conclusion")
	if err := os.WriteFile(projectHistory, history, 0o644); err != nil {
		t.Fatal(err)
	}

	publishes := 0
	options := env.syncOptions(false)
	options.RecoverMarkdown = func(ctx context.Context, owner *publicationlock.Owner) error {
		return RecoverMarkdownLocked(ctx, env.publishOptions(), owner)
	}
	options.PublishMarkdown = func(ctx context.Context, edit syncproject.MarkdownSyncPlan, owner *publicationlock.Owner) error {
		publishes++
		_, err := PublishMarkdownEditLocked(ctx, env.publishOptions(), edit, owner)
		return err
	}
	if _, err := syncproject.RunMarkdown(context.Background(), options); err != nil {
		t.Fatalf("RunMarkdown: %v", err)
	}
	if publishes != 1 {
		t.Fatalf("publisher calls = %d, want 1", publishes)
	}
	if _, err := syncproject.RunMarkdown(context.Background(), options); err != nil {
		t.Fatalf("no-op RunMarkdown: %v", err)
	}
	if publishes != 1 {
		t.Fatalf("no-op republished: calls = %d", publishes)
	}
	if got := readTestFile(t, filepath.Join(env.projectRoot, filepath.FromSlash(sessionIndexRelativePath))); !bytes.Equal(got, beforeIndex) {
		t.Fatal("human edit changed the session index")
	}
	if got := loadMarkdownProjectionForTest(t, env); got.Review.GenerationID != env.manifest.GenerationID || got.Review.CurrentState.Goal != "human goal" || got.Review.Revision != accepted.Review.Revision+1 || got.Review.Timeline[0].ClosedLoop.Conclusion.Text != "human conclusion" || got.Review.Timeline[0].ClosedLoop.Conclusion.Kind != reviewv4.ConclusionHumanConfirmed || !reflect.DeepEqual(got.Review.Timeline[0].ClosedLoop.Verification, milestone.ClosedLoop.Verification) {
		t.Fatalf("accepted edit = generation %q revision %d goal %q conclusion=%+v verification=%+v", got.Review.GenerationID, got.Review.Revision, got.Review.CurrentState.Goal, got.Review.Timeline[0].ClosedLoop.Conclusion, got.Review.Timeline[0].ClosedLoop.Verification)
	}
}

func TestMarkdownVaultFlowCustomEditStatusSyncReopenAndRejectInvalid(t *testing.T) {
	env := setupFlowMarkdownPublication(t, "project-markdown-flow-custom")
	projectReviewPath := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	vaultReviewPath := filepath.Join(env.vaultRoot, filepath.FromSlash(vaultRelativePath(env.mapping.VaultReviewPath, reviewv2.ReviewRelativePath)))
	projectIndexPath := filepath.Join(env.projectRoot, filepath.FromSlash(sessionIndexRelativePath))
	vaultIndexPath := filepath.Join(env.vaultRoot, filepath.FromSlash(vaultRelativePath(env.mapping.VaultReviewPath, sessionIndexRelativePath)))
	beforeProjectReview := readTestFile(t, projectReviewPath)
	beforeIndex := map[string][]byte{
		projectIndexPath: readTestFile(t, projectIndexPath),
		vaultIndexPath:   readTestFile(t, vaultIndexPath),
	}
	beforeIndexModTime := make(map[string]time.Time, len(beforeIndex))
	for path := range beforeIndex {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		beforeIndexModTime[path] = info.ModTime()
	}
	vaultEdit := bytes.Replace(readTestFile(t, vaultReviewPath), []byte("custom_owner: '保留'"), []byte("custom_owner: '修改'"), 1)
	if bytes.Equal(vaultEdit, readTestFile(t, vaultReviewPath)) {
		t.Fatal("flow custom fixture was not edited")
	}
	if err := os.WriteFile(vaultReviewPath, vaultEdit, 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := syncproject.StatusMarkdown(t.Context(), env.syncOptions(false))
	if err != nil {
		t.Fatalf("authenticated flow status: %v", err)
	}
	if len(status.Pending) != 6 || status.InSync != 0 || status.Conflicted != 0 || status.Malformed != 0 {
		t.Fatalf("flow custom status = %+v", status)
	}

	publishes := 0
	options := env.syncOptions(false)
	options.RecoverMarkdown = func(ctx context.Context, owner *publicationlock.Owner) error {
		return RecoverMarkdownLocked(ctx, env.publishOptions(), owner)
	}
	options.PublishMarkdown = func(ctx context.Context, edit syncproject.MarkdownSyncPlan, owner *publicationlock.Owner) error {
		publishes++
		_, publishErr := PublishMarkdownEditLocked(ctx, env.publishOptions(), edit, owner)
		return publishErr
	}
	if _, err := syncproject.RunMarkdown(context.Background(), options); err != nil {
		t.Fatalf("authenticated flow sync: %v", err)
	}
	if publishes != 1 {
		t.Fatalf("flow custom publisher calls = %d, want 1", publishes)
	}
	expectedReview := bytes.Replace(vaultEdit, []byte("revision: !!int +1,"), []byte("revision: !!int 2,"), 1)
	for _, path := range []string{projectReviewPath, vaultReviewPath} {
		if got := readTestFile(t, path); !bytes.Equal(got, expectedReview) {
			t.Fatalf("flow custom publication changed unrelated bytes in %s\ngot:\n%s\nwant:\n%s", path, got, expectedReview)
		}
	}
	if !bytes.Contains(readTestFile(t, projectReviewPath), []byte("[\u94fe\u63a5](https://example.test/custom)")) {
		t.Fatal("flow custom publication changed unrelated Markdown link")
	}
	for path, before := range beforeIndex {
		if got := readTestFile(t, path); !bytes.Equal(got, before) {
			t.Fatalf("human flow edit changed index bytes: %s", path)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(beforeIndexModTime[path]) {
			t.Fatalf("human flow edit changed index mtime: %s", path)
		}
	}
	accepted := loadMarkdownProjectionForTest(t, env)
	if accepted.Review.GenerationID != env.manifest.GenerationID || accepted.Review.Revision != 2 {
		t.Fatalf("flow edit changed generation or wrong revision: generation=%q revision=%d", accepted.Review.GenerationID, accepted.Review.Revision)
	}
	if status, err := syncproject.StatusMarkdown(t.Context(), env.syncOptions(false)); err != nil || status.InSync != 1 {
		t.Fatalf("flow status after sync: %+v err=%v", status, err)
	}
	if _, err := syncproject.RunMarkdown(context.Background(), options); err != nil {
		t.Fatalf("flow no-op reopen: %v", err)
	}
	if publishes != 1 {
		t.Fatalf("flow no-op republished: calls=%d", publishes)
	}

	beforeInvalidProject := readTestFile(t, projectReviewPath)
	beforeInvalidReceipt := loadAcceptedReceiptForTest(t, env)
	beforeInvalidBase := loadMarkdownBaseForTest(t, env)
	invalidVault := bytes.Replace(readTestFile(t, vaultReviewPath), []byte("custom_owner: '修改'"), []byte("custom_owner: [invalid"), 1)
	if bytes.Equal(invalidVault, readTestFile(t, vaultReviewPath)) {
		t.Fatal("flow invalid fixture was not edited")
	}
	if err := os.WriteFile(vaultReviewPath, invalidVault, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := syncproject.StatusMarkdown(t.Context(), env.syncOptions(false)); err == nil {
		t.Fatal("invalid flow edit produced authenticated status")
	}
	if _, err := syncproject.RunMarkdown(context.Background(), options); err == nil {
		t.Fatal("invalid flow edit was synchronized")
	}
	if publishes != 1 || !bytes.Equal(readTestFile(t, projectReviewPath), beforeInvalidProject) ||
		!reflect.DeepEqual(loadAcceptedReceiptForTest(t, env), beforeInvalidReceipt) ||
		!reflect.DeepEqual(loadMarkdownBaseForTest(t, env), beforeInvalidBase) {
		t.Fatal("invalid flow edit changed accepted state")
	}
	if !bytes.Equal(readTestFile(t, vaultReviewPath), invalidVault) {
		t.Fatal("invalid flow edit was overwritten")
	}
	if bytes.Equal(beforeProjectReview, readTestFile(t, projectReviewPath)) {
		t.Fatal("valid flow custom edit did not reach Project")
	}
}

func TestMarkdownVaultFlowGroupedDeletionWithPlainQuoteAndTrailingComma(t *testing.T) {
	env := setupFlowMarkdownPublication(t, "project-markdown-flow-group-delete")
	projectReviewPath := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	vaultReviewPath := filepath.Join(env.vaultRoot, filepath.FromSlash(vaultRelativePath(env.mapping.VaultReviewPath, reviewv2.ReviewRelativePath)))
	projectIndexPath := filepath.Join(env.projectRoot, filepath.FromSlash(sessionIndexRelativePath))
	vaultIndexPath := filepath.Join(env.vaultRoot, filepath.FromSlash(vaultRelativePath(env.mapping.VaultReviewPath, sessionIndexRelativePath)))
	beforeIndex := map[string][]byte{
		projectIndexPath: readTestFile(t, projectIndexPath),
		vaultIndexPath:   readTestFile(t, vaultIndexPath),
	}
	beforeIndexModTime := make(map[string]time.Time, len(beforeIndex))
	for path := range beforeIndex {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		beforeIndexModTime[path] = info.ModTime()
	}
	originalVault := readTestFile(t, vaultReviewPath)
	vaultEdit := bytes.Replace(originalVault, []byte(", custom_a: one, custom_b: two,"), nil, 1)
	if bytes.Equal(vaultEdit, originalVault) {
		t.Fatal("flow grouped-deletion fixture was not edited")
	}
	if err := os.WriteFile(vaultReviewPath, vaultEdit, 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := syncproject.StatusMarkdown(t.Context(), env.syncOptions(false))
	if err != nil {
		t.Fatalf("authenticated grouped-deletion status: %v", err)
	}
	if len(status.Pending) != 6 || status.InSync != 0 || status.Conflicted != 0 || status.Malformed != 0 {
		t.Fatalf("grouped-deletion status = %+v", status)
	}

	publishes := 0
	options := env.syncOptions(false)
	options.RecoverMarkdown = func(ctx context.Context, owner *publicationlock.Owner) error {
		return RecoverMarkdownLocked(ctx, env.publishOptions(), owner)
	}
	options.PublishMarkdown = func(ctx context.Context, edit syncproject.MarkdownSyncPlan, owner *publicationlock.Owner) error {
		publishes++
		_, publishErr := PublishMarkdownEditLocked(ctx, env.publishOptions(), edit, owner)
		return publishErr
	}
	if _, err := syncproject.RunMarkdown(context.Background(), options); err != nil {
		t.Fatalf("authenticated grouped-deletion sync: %v", err)
	}
	if publishes != 1 {
		t.Fatalf("grouped-deletion publisher calls = %d, want 1", publishes)
	}
	expectedReview := bytes.Replace(vaultEdit, []byte("revision: !!int +1,"), []byte("revision: !!int 2,"), 1)
	for _, path := range []string{projectReviewPath, vaultReviewPath} {
		if got := readTestFile(t, path); !bytes.Equal(got, expectedReview) {
			t.Fatalf("grouped-deletion publication changed unrelated bytes in %s\ngot:\n%s\nwant:\n%s", path, got, expectedReview)
		}
	}
	if !bytes.Contains(expectedReview, []byte("custom_note: team's,")) {
		t.Fatal("grouped-deletion publication lost plain-scalar apostrophe or trailing comma")
	}
	for path, before := range beforeIndex {
		if got := readTestFile(t, path); !bytes.Equal(got, before) {
			t.Fatalf("grouped flow deletion changed index bytes: %s", path)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(beforeIndexModTime[path]) {
			t.Fatalf("grouped flow deletion changed index mtime: %s", path)
		}
	}
	accepted := loadMarkdownProjectionForTest(t, env)
	if accepted.Review.GenerationID != env.manifest.GenerationID || accepted.Review.Revision != 2 {
		t.Fatalf("grouped flow deletion changed generation or wrong revision: generation=%q revision=%d", accepted.Review.GenerationID, accepted.Review.Revision)
	}
	if status, err := syncproject.StatusMarkdown(t.Context(), env.syncOptions(false)); err != nil || status.InSync != 1 {
		t.Fatalf("grouped-deletion status after sync: %+v err=%v", status, err)
	}
	if _, err := syncproject.RunMarkdown(context.Background(), options); err != nil {
		t.Fatalf("grouped-deletion no-op reopen: %v", err)
	}
	if publishes != 1 {
		t.Fatalf("grouped-deletion no-op republished: calls=%d", publishes)
	}
}

func TestMarkdownScanNoOpStillChecksReceiptBaseAndVaultPreimages(t *testing.T) {
	env := setupMarkdownPublication(t, "project-markdown-scan-cas")
	paths := []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath, sessionIndexRelativePath}
	files := make([]presentation.FilePlan, 0, len(paths))
	vaultExpected := make(map[string][]byte, len(paths))
	var index []byte
	for _, relative := range paths {
		projectBody := readTestFile(t, filepath.Join(env.projectRoot, filepath.FromSlash(relative)))
		vaultBody := readTestFile(t, filepath.Join(env.vaultRoot, filepath.FromSlash(vaultRelativePath(env.mapping.VaultReviewPath, relative))))
		files = append(files, presentation.FilePlan{Relative: relative, Expected: projectBody, ExpectedExists: true, Desired: projectBody, Mode: 0o600})
		vaultExpected[relative] = vaultBody
		if relative == sessionIndexRelativePath {
			index = projectBody
		}
	}
	receipt := loadAcceptedReceiptForTest(t, env)
	base := loadMarkdownBaseForTest(t, env)
	scan := syncproject.MarkdownSyncPlan{
		Plan:  presentation.RenderPlan{ProjectID: env.projectID, GenerationID: env.manifest.GenerationID, ProjectViewDigest: env.manifest.ProjectViewDigest, Files: files},
		Index: index, ExpectedGenerationID: env.manifest.GenerationID, ExpectedIndexDigest: env.manifest.SessionIndexDigest,
		VaultExpected: vaultExpected, ExpectedReceiptRevision: receipt.RevisionID, ExpectedBaseDigest: base.ContentHash,
	}

	wrongReceipt := scan
	wrongReceipt.ExpectedReceiptRevision = "sha256:" + strings.Repeat("f", 64)
	if _, err := PublishMarkdownScan(context.Background(), env.publishOptions(), wrongReceipt); err == nil || !strings.Contains(err.Error(), "receipt changed") {
		t.Fatalf("same-generation no-op ignored receipt preimage: %v", err)
	}
	wrongBase := scan
	wrongBase.ExpectedBaseDigest = strings.Repeat("f", 64)
	if _, err := PublishMarkdownScan(context.Background(), env.publishOptions(), wrongBase); err == nil || !strings.Contains(err.Error(), "receipt changed") {
		t.Fatalf("same-generation no-op ignored Base preimage: %v", err)
	}
	projectReview := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	originalProject := readTestFile(t, projectReview)
	lateProject := append(bytes.Clone(originalProject), []byte("\nlate project edit\n")...)
	if err := os.WriteFile(projectReview, lateProject, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishMarkdownScan(context.Background(), env.publishOptions(), scan); !errors.Is(err, ErrPublicationConflict) {
		t.Fatalf("late Project edit was not rejected by scan CAS: %v", err)
	}
	if got := readTestFile(t, projectReview); !bytes.Equal(got, lateProject) {
		t.Fatal("late Project edit was overwritten after scan CAS conflict")
	}
	if err := os.WriteFile(projectReview, originalProject, 0o644); err != nil {
		t.Fatal(err)
	}
	vaultReview := filepath.Join(env.vaultRoot, filepath.FromSlash(vaultRelativePath(env.mapping.VaultReviewPath, reviewv2.ReviewRelativePath)))
	if err := os.WriteFile(vaultReview, append(readTestFile(t, vaultReview), []byte("\nlate vault edit\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishMarkdownScan(context.Background(), env.publishOptions(), scan); !errors.Is(err, ErrPublicationConflict) {
		t.Fatalf("late Vault edit was not rejected by scan CAS: %v", err)
	}
}

func TestInitialMarkdownScanRejectsAcceptedHistoryEvenWhenPublicFilesAreGone(t *testing.T) {
	env := setupMarkdownPublication(t, "project-markdown-scan-history")
	paths := []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath, sessionIndexRelativePath}
	files := make([]presentation.FilePlan, 0, len(paths))
	vaultExpected := make(map[string][]byte, len(paths))
	for _, relative := range paths {
		body := readTestFile(t, filepath.Join(env.projectRoot, filepath.FromSlash(relative)))
		files = append(files, presentation.FilePlan{Relative: relative, Desired: body, Mode: 0o600})
		vaultExpected[relative] = nil
		if err := os.Remove(filepath.Join(env.projectRoot, filepath.FromSlash(relative))); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(env.vaultRoot, filepath.FromSlash(vaultRelativePath(env.mapping.VaultReviewPath, relative)))); err != nil {
			t.Fatal(err)
		}
	}
	index := files[3].Desired
	scan := syncproject.MarkdownSyncPlan{Plan: presentation.RenderPlan{ProjectID: env.projectID, GenerationID: env.manifest.GenerationID, ProjectViewDigest: env.manifest.ProjectViewDigest, Files: files}, Index: index, ExpectedGenerationID: env.manifest.GenerationID, ExpectedIndexDigest: env.manifest.SessionIndexDigest, VaultExpected: vaultExpected}
	if _, err := PublishMarkdownScan(context.Background(), env.publishOptions(), scan); err == nil || !strings.Contains(err.Error(), "accepted publication history") {
		t.Fatalf("missing public files reset an accepted project: %v", err)
	}
}

func TestMarkdownRecoveryBeforeReceiptRollsBackBaseAndPreservesPriorAcceptance(t *testing.T) {
	env := setupMarkdownPublication(t, "project-markdown-before-receipt")
	prior := loadAcceptedReceiptForTest(t, env)
	priorBase := loadMarkdownBaseForTest(t, env)
	projectReview := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	accepted := loadMarkdownProjectionForTest(t, env)
	review := replaceMarkdownFieldForTest(t, readTestFile(t, projectReview), "project-overview", "goal", accepted.Review.CurrentState.Goal, "pending goal")
	if err := os.WriteFile(projectReview, review, 0o644); err != nil {
		t.Fatal(err)
	}
	edit := captureMarkdownPlanForTest(t, env)
	failure := errors.New("fail before accepted receipt")
	opts := env.publishOptions()
	opts.checkpoint = func(stage publishCheckpoint, _, _ string) error {
		if stage == checkpointBeforeReceiptCommit {
			return failure
		}
		return nil
	}
	owner, err := publicationlock.Acquire(env.dataRoot, env.projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PublishMarkdownEditLocked(context.Background(), opts, edit, owner); !errors.Is(err, failure) {
		t.Fatalf("PublishMarkdownEditLocked error = %v", err)
	}
	if err := RecoverMarkdownLocked(context.Background(), env.publishOptions(), owner); err != nil {
		t.Fatalf("RecoverMarkdownLocked: %v", err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	if got := loadAcceptedReceiptForTest(t, env); got.RevisionID != prior.RevisionID {
		t.Fatalf("prior receipt was replaced: %s -> %s", prior.RevisionID, got.RevisionID)
	}
	if got := loadMarkdownBaseForTest(t, env); got.ContentHash != priorBase.ContentHash {
		t.Fatalf("Base advanced across failed acceptance: %s -> %s", priorBase.ContentHash, got.ContentHash)
	}
	if got := readTestFile(t, projectReview); !bytes.Equal(got, review) {
		t.Fatal("failed publication did not preserve the pending human Project edit")
	}
}

func TestMarkdownCancellationBeforeReceiptPreservesPriorAcceptance(t *testing.T) {
	env := setupMarkdownPublication(t, "project-markdown-cancel")
	prior := loadAcceptedReceiptForTest(t, env)
	priorBase := loadMarkdownBaseForTest(t, env)
	accepted := loadMarkdownProjectionForTest(t, env)
	projectReview := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	review := replaceMarkdownFieldForTest(t, readTestFile(t, projectReview), "project-overview", "goal", accepted.Review.CurrentState.Goal, "cancelled goal")
	if err := os.WriteFile(projectReview, review, 0o644); err != nil {
		t.Fatal(err)
	}
	edit := captureMarkdownPlanForTest(t, env)
	ctx, cancel := context.WithCancel(context.Background())
	opts := env.publishOptions()
	opts.checkpoint = func(stage publishCheckpoint, _, _ string) error {
		if stage == checkpointBeforeReceiptCommit {
			cancel()
		}
		return nil
	}
	owner, err := publicationlock.Acquire(env.dataRoot, env.projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = PublishMarkdownEditLocked(ctx, opts, edit, owner)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("publication error=%v, want cancellation", err)
	}
	if err := RecoverMarkdownLocked(context.Background(), env.publishOptions(), owner); err != nil {
		t.Fatal(err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	if got := loadAcceptedReceiptForTest(t, env); got.RevisionID != prior.RevisionID {
		t.Fatal("cancellation advanced receipt")
	}
	if got := loadMarkdownBaseForTest(t, env); got.ContentHash != priorBase.ContentHash {
		t.Fatal("cancellation advanced Base")
	}
}

func TestMarkdownCancellationAfterReceiptReturnsCancellationAndKeepsAcceptedCommit(t *testing.T) {
	env := setupMarkdownPublication(t, "project-markdown-postcommit")
	prior := loadAcceptedReceiptForTest(t, env)
	priorBase := loadMarkdownBaseForTest(t, env)
	accepted := loadMarkdownProjectionForTest(t, env)
	path := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	if err := os.WriteFile(path, replaceMarkdownFieldForTest(t, readTestFile(t, path), "project-overview", "goal", accepted.Review.CurrentState.Goal, "committed goal"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := captureMarkdownPlanForTest(t, env)
	ctx, cancel := context.WithCancel(context.Background())
	opts := env.publishOptions()
	opts.checkpoint = func(stage publishCheckpoint, _, _ string) error {
		if stage == checkpointAfterReceiptCommit {
			cancel()
		}
		return nil
	}
	owner, err := publicationlock.Acquire(env.dataRoot, env.projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = PublishMarkdownEditLocked(ctx, opts, edit, owner)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("post-commit error=%v", err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	if got := loadAcceptedReceiptForTest(t, env); got.RevisionID == prior.RevisionID {
		t.Fatal("durable accepted receipt was rolled back")
	}
	if got := loadMarkdownBaseForTest(t, env); got.ContentHash == priorBase.ContentHash {
		t.Fatal("accepted Base did not advance")
	}
}

func TestMarkdownRecoveryAfterReceiptNeverRollsBackAcceptedRevision(t *testing.T) {
	env := setupMarkdownPublication(t, "project-markdown-after-receipt")
	accepted := loadMarkdownProjectionForTest(t, env)
	projectReview := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	review := replaceMarkdownFieldForTest(t, readTestFile(t, projectReview), "project-overview", "goal", accepted.Review.CurrentState.Goal, "accepted goal")
	if err := os.WriteFile(projectReview, review, 0o644); err != nil {
		t.Fatal(err)
	}
	edit := captureMarkdownPlanForTest(t, env)
	failure := errors.New("fail after accepted receipt")
	opts := env.publishOptions()
	opts.checkpoint = func(stage publishCheckpoint, _, _ string) error {
		if stage == checkpointAfterReceiptCommit {
			return failure
		}
		return nil
	}
	owner, err := publicationlock.Acquire(env.dataRoot, env.projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PublishMarkdownEditLocked(context.Background(), opts, edit, owner); !errors.Is(err, failure) {
		t.Fatalf("PublishMarkdownEditLocked error = %v", err)
	}
	committed := loadAcceptedReceiptForTest(t, env)
	if err := RecoverMarkdownLocked(context.Background(), env.publishOptions(), owner); err != nil {
		t.Fatalf("RecoverMarkdownLocked: %v", err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	if got := loadAcceptedReceiptForTest(t, env); got.RevisionID != committed.RevisionID {
		t.Fatalf("accepted receipt changed during terminal recovery: %s -> %s", committed.RevisionID, got.RevisionID)
	}
	if got := loadMarkdownProjectionForTest(t, env); got.Review.CurrentState.Goal != "accepted goal" {
		t.Fatalf("accepted revision was rolled back: goal=%q", got.Review.CurrentState.Goal)
	}
}

func TestMarkdownDryRunLeavesProjectVaultAndPrivateTreesUnchanged(t *testing.T) {
	env := setupMarkdownPublication(t, "project-markdown-dry-run")
	accepted := loadMarkdownProjectionForTest(t, env)
	reviewPath := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	updated := replaceMarkdownFieldForTest(t, readTestFile(t, reviewPath), "project-overview", "goal", accepted.Review.CurrentState.Goal, "dry goal")
	if err := os.WriteFile(reviewPath, updated, 0o644); err != nil {
		t.Fatal(err)
	}
	before := []string{snapshotMarkdownTree(t, env.projectRoot), snapshotMarkdownTree(t, env.vaultRoot), snapshotMarkdownTree(t, env.dataRoot)}
	opts := env.syncOptions(true)
	called := false
	opts.RecoverMarkdown = func(context.Context, *publicationlock.Owner) error { called = true; return nil }
	opts.PublishMarkdown = func(context.Context, syncproject.MarkdownSyncPlan, *publicationlock.Owner) error {
		called = true
		return nil
	}
	report, err := syncproject.RunMarkdown(context.Background(), opts)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if len(report.Operations) != 6 {
		t.Fatalf("dry-run operations = %#v, want six Project/Vault writes", report.Operations)
	}
	wantPaths := []struct {
		target syncengine.Side
		path   string
		entity string
		kind   syncengine.OperationKind
		before []byte
	}{
		{syncengine.SideProject, "项目回顾.md", "project-overview", syncengine.OperationUpdateProject, updated},
		{syncengine.SideVault, "项目回顾.md", "project-overview", syncengine.OperationUpdateVault, readTestFile(t, filepath.Join(env.vaultRoot, filepath.FromSlash(vaultRelativePath(env.mapping.VaultReviewPath, reviewv2.ReviewRelativePath))))},
		{syncengine.SideProject, "项目历史.md", "project-history", syncengine.OperationUpdateProject, readTestFile(t, filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.HistoryRelativePath)))},
		{syncengine.SideVault, "项目历史.md", "project-history", syncengine.OperationUpdateVault, readTestFile(t, filepath.Join(env.vaultRoot, filepath.FromSlash(vaultRelativePath(env.mapping.VaultReviewPath, reviewv2.HistoryRelativePath))))},
		{syncengine.SideProject, ".session-reviewer/ledger.json", "machine-ledger", syncengine.OperationUpdateProject, readTestFile(t, filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.MachineLedgerRelativePath)))},
		{syncengine.SideVault, ".session-reviewer/ledger.json", "machine-ledger", syncengine.OperationUpdateVault, readTestFile(t, filepath.Join(env.vaultRoot, filepath.FromSlash(vaultRelativePath(env.mapping.VaultReviewPath, reviewv2.MachineLedgerRelativePath))))},
	}
	for index, want := range wantPaths {
		operation := report.Operations[index]
		sum := sha256.Sum256(want.before)
		if operation.Target != want.target || operation.RelativePath != want.path || operation.EntityID != want.entity || operation.Kind != want.kind || operation.BeforeHash != hex.EncodeToString(sum[:]) || len(operation.AfterHash) != 64 || operation.BeforeHash == operation.AfterHash {
			t.Fatalf("dry-run operation[%d] = %#v, want target=%q path=%q and changed hashes", index, operation, want.target, want.path)
		}
	}
	after := []string{snapshotMarkdownTree(t, env.projectRoot), snapshotMarkdownTree(t, env.vaultRoot), snapshotMarkdownTree(t, env.dataRoot)}
	if called || !reflect.DeepEqual(before, after) {
		t.Fatalf("dry-run mutated state or invoked callbacks: called=%t changed=%t", called, !reflect.DeepEqual(before, after))
	}
}

func TestMarkdownSameGenerationFourFileRecoveryRequiresExactReceipt(t *testing.T) {
	for _, point := range []publishCheckpoint{checkpointBeforePointerCommit, checkpointAfterPointerCommit} {
		t.Run(string(point), func(t *testing.T) {
			env := setupMarkdownPublication(t, "project-same-generation-"+strings.ReplaceAll(string(point), "_", "-"))
			priorReceipt := loadAcceptedReceiptForTest(t, env)
			priorBase := loadMarkdownBaseForTest(t, env)
			plan := env.plan
			for index := range plan.Files {
				plan.Files[index].Expected = bytes.Clone(plan.Files[index].Desired)
				plan.Files[index].ExpectedExists = true
			}
			failure := errors.New("simulated same-generation pointer crash")
			opts := env.publishOptions()
			opts.Plan = plan
			opts.checkpoint = func(stage publishCheckpoint, _, _ string) error {
				if stage == point {
					panic(failure)
				}
				return nil
			}
			func() {
				defer func() {
					recovered, ok := recover().(error)
					if !ok || !errors.Is(recovered, failure) {
						t.Fatalf("publication panic = %v", recovered)
					}
				}()
				_, _ = Publish(context.Background(), opts)
			}()
			owner, err := publicationlock.Acquire(env.dataRoot, env.projectID, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := RecoverMarkdownLocked(context.Background(), env.publishOptions(), owner); err != nil {
				_ = owner.Release()
				t.Fatalf("RecoverMarkdownLocked: %v", err)
			}
			if err := owner.Release(); err != nil {
				t.Fatal(err)
			}
			if got := loadAcceptedReceiptForTest(t, env); got.RevisionID != priorReceipt.RevisionID {
				t.Fatalf("same-generation pointer equality replaced exact accepted receipt: %s -> %s", priorReceipt.RevisionID, got.RevisionID)
			}
			if got := loadMarkdownBaseForTest(t, env); got.ContentHash != priorBase.ContentHash {
				t.Fatalf("same-generation pointer equality advanced Base: %s -> %s", priorBase.ContentHash, got.ContentHash)
			}
		})
	}
}

func TestMarkdownNewGenerationBaseCommittedWithoutReceiptRecoversForward(t *testing.T) {
	projectID := "project-new-generation-mixed"
	dataRoot, _, _, mapping, manifest, legacy := setupPublishEnvWithIndex(t, projectID, true)
	plan := validMarkdownPublicationPlanForTest(t, dataRoot, manifest, legacy)
	failure := errors.New("simulated crash before first accepted receipt")
	opts := Options{
		ProjectID: projectID, PreparedGeneration: manifest.GenerationID, Plan: plan,
		Mapping: mapping, DataRoot: dataRoot, Now: time.Now,
		checkpoint: func(stage publishCheckpoint, _, _ string) error {
			if stage == checkpointBeforeReceiptCommit {
				panic(failure)
			}
			return nil
		},
	}
	func() {
		defer func() {
			recovered, ok := recover().(error)
			if !ok || !errors.Is(recovered, failure) {
				t.Fatalf("publication panic = %v", recovered)
			}
		}()
		_, _ = Publish(context.Background(), opts)
	}()
	owner, err := publicationlock.Acquire(dataRoot, projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecoverMarkdownLocked(context.Background(), Options{ProjectID: projectID, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}, owner); err != nil {
		_ = owner.Release()
		t.Fatalf("RecoverMarkdownLocked: %v", err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	published, _, loadErr := store.LoadPublished()
	closeErr := store.Close()
	if loadErr != nil || closeErr != nil || published != manifest.GenerationID {
		t.Fatalf("published pointer changed without an authorized rollback: generation=%q load=%v close=%v", published, loadErr, closeErr)
	}
	state, err := publicationstate.OpenReadOnly(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, acceptedErr := state.Accepted()
	if closeErr := state.Close(); acceptedErr != nil || closeErr != nil || receipt.GenerationID != manifest.GenerationID {
		t.Fatalf("durable pointer did not recover acceptance: receipt=%+v accepted=%v close=%v", receipt, acceptedErr, closeErr)
	}
}

func TestMarkdownNewGenerationCancellationConvergesThroughNormalRecovery(t *testing.T) {
	for _, point := range []publishCheckpoint{checkpointBeforePointerCommit, checkpointAfterPointerCommit, checkpointBeforeReceiptCommit, checkpointAfterReceiptCommit} {
		for _, reopen := range []string{"sync", "publisher"} {
			t.Run(string(point)+"/"+reopen, func(t *testing.T) {
				env := setupMarkdownPublication(t, "project-generation-cancel")
				scan := nextMarkdownGenerationForTest(t, env)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				opts := env.publishOptions()
				opts.checkpoint = func(stage publishCheckpoint, _, _ string) error {
					if stage == point {
						cancel()
					}
					return nil
				}
				if _, err := PublishMarkdownScan(ctx, opts, scan); !errors.Is(err, context.Canceled) {
					t.Fatalf("expected cancellation, got %v", err)
				}
				wantGeneration := scan.ExpectedGenerationID
				if point == checkpointBeforePointerCommit {
					wantGeneration = env.manifest.GenerationID
				}
				store, err := memorystore.OpenReadOnly(env.dataRoot, env.projectID)
				if err != nil {
					t.Fatal(err)
				}
				published, _, err := store.LoadPublished()
				if closeErr := store.Close(); err != nil || closeErr != nil || published != wantGeneration {
					t.Fatalf("actual pointer boundary: got=%s want=%s err=%v close=%v", published, wantGeneration, err, closeErr)
				}
				j, err := OpenJournal(env.dataRoot, env.projectID)
				if err != nil {
					t.Fatal(err)
				}
				intent, err := j.Load()
				if closeErr := j.Close(); err != nil || closeErr != nil {
					t.Fatal(errors.Join(err, closeErr))
				}
				if (point == checkpointAfterPointerCommit || point == checkpointBeforeReceiptCommit) && intent.Stage == StageCommitted {
					t.Fatal("post-pointer cancellation terminalized inconsistent rollback")
				}
				if reopen == "publisher" && point != checkpointBeforePointerCommit && point != checkpointAfterReceiptCommit {
					reopenOpts := env.publishOptions()
					reopenOpts.PreparedGeneration, reopenOpts.Plan = scan.ExpectedGenerationID, scan.Plan
					if _, err := Publish(t.Context(), reopenOpts); err != nil {
						t.Fatalf("publisher reopen: %v", err)
					}
				} else {
					owner, err := publicationlock.Acquire(env.dataRoot, env.projectID, time.Second)
					if err != nil {
						t.Fatal(err)
					}
					err = RecoverMarkdownLocked(t.Context(), env.publishOptions(), owner)
					if releaseErr := owner.Release(); err != nil || releaseErr != nil {
						t.Fatal(errors.Join(err, releaseErr))
					}
				}
				for _, file := range scan.Plan.Files {
					want := file.Desired
					if point == checkpointBeforePointerCommit {
						want = file.Expected
					}
					for _, path := range []string{filepath.Join(env.projectRoot, filepath.FromSlash(file.Relative)), filepath.Join(env.vaultRoot, filepath.FromSlash(vaultRelativePath(env.mapping.VaultReviewPath, file.Relative)))} {
						if !bytes.Equal(readTestFile(t, path), want) {
							t.Fatalf("recovery public bytes mismatch: %s", path)
						}
					}
				}
				receipt, base := loadAcceptedReceiptForTest(t, env), loadMarkdownBaseForTest(t, env)
				if receipt.GenerationID != wantGeneration || receipt.BaseDigest != base.ContentHash || loadMarkdownProjectionForTest(t, env).Review.GenerationID != wantGeneration {
					t.Fatal("receipt/Base/public generation diverged")
				}
				if status, err := syncproject.StatusMarkdown(t.Context(), env.syncOptions(false)); err != nil || status.InSync != 1 {
					t.Fatalf("normal status after recovery: %+v err=%v", status, err)
				}
			})
		}
	}
}

func nextMarkdownGenerationForTest(t *testing.T, env markdownPublicationTestEnv) syncproject.MarkdownSyncPlan {
	t.Helper()
	store, err := memorystore.Open(env.dataRoot, env.projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prepared, manifest, err := store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	manifest.GenerationID = "generation-next"
	manifest.SessionIndexDigest = ""
	body, err := store.LoadObject(memorystore.ObjectProjectView, manifest.ProjectViewDigest)
	if err != nil {
		t.Fatal(err)
	}
	var view memory.ProjectView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	views := make(map[sessionindex.SessionKey]*memory.SessionView)
	for _, dependency := range manifest.SessionViews {
		body, err := store.LoadObject(memorystore.ObjectSessionView, dependency.Digest)
		if err != nil {
			t.Fatal(err)
		}
		var session memory.SessionView
		if err := json.Unmarshal(body, &session); err != nil {
			t.Fatal(err)
		}
		views[sessionindex.SessionKey{Provider: dependency.Provider, SessionID: dependency.SessionID}] = &session
	}
	generatedAt, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	index, err := sessionindex.Build(sessionindex.BuildInput{ProjectView: view, Manifest: manifest, SessionViews: views, GeneratedAt: generatedAt})
	if err != nil {
		t.Fatal(err)
	}
	manifest.SessionIndexDigest, err = store.PutSessionIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvancePrepared(prepared, manifest); err != nil {
		t.Fatal(err)
	}
	input := presentation.ProjectInput{ProjectView: view, GenerationID: manifest.GenerationID, Revision: 1}
	output, err := presentation.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := presentation.Render(input, output)
	if err != nil {
		t.Fatal(err)
	}
	plan := validMarkdownPublicationPlanForTest(t, env.dataRoot, manifest, legacy)
	vaultExpected := make(map[string][]byte)
	for i := range plan.Files {
		file := &plan.Files[i]
		file.Expected, file.ExpectedExists = readTestFile(t, filepath.Join(env.projectRoot, filepath.FromSlash(file.Relative))), true
		vaultExpected[file.Relative] = readTestFile(t, filepath.Join(env.vaultRoot, filepath.FromSlash(vaultRelativePath(env.mapping.VaultReviewPath, file.Relative))))
	}
	indexBody, err := sessionindex.Render(index)
	if err != nil {
		t.Fatal(err)
	}
	return syncproject.MarkdownSyncPlan{Plan: plan, Index: indexBody, ExpectedGenerationID: manifest.GenerationID, ExpectedIndexDigest: index.Digest, VaultExpected: vaultExpected, ExpectedReceiptRevision: loadAcceptedReceiptForTest(t, env).RevisionID, ExpectedBaseDigest: loadMarkdownBaseForTest(t, env).ContentHash}
}

func TestMarkdownEditRejectsVaultPreimageAndIndexChanges(t *testing.T) {
	for index, relative := range []string{reviewv2.ReviewRelativePath, sessionIndexRelativePath} {
		t.Run(filepath.Base(relative), func(t *testing.T) {
			env := setupMarkdownPublication(t, fmt.Sprintf("project-markdown-preimage-%d", index))
			accepted := loadMarkdownProjectionForTest(t, env)
			reviewPath := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
			updated := replaceMarkdownFieldForTest(t, readTestFile(t, reviewPath), "project-overview", "goal", accepted.Review.CurrentState.Goal, "human goal")
			if err := os.WriteFile(reviewPath, updated, 0o644); err != nil {
				t.Fatal(err)
			}
			edit := captureMarkdownPlanForTest(t, env)
			vaultPath := filepath.Join(env.vaultRoot, filepath.FromSlash(vaultRelativePath(env.mapping.VaultReviewPath, relative)))
			before := readTestFile(t, vaultPath)
			if err := os.WriteFile(vaultPath, append(bytes.Clone(before), '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			owner, err := publicationlock.Acquire(env.dataRoot, env.projectID, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_, publishErr := PublishMarkdownEditLocked(context.Background(), env.publishOptions(), edit, owner)
			_ = owner.Release()
			if publishErr == nil {
				t.Fatal("changed Vault/index preimage was published")
			}
			if got := readTestFile(t, vaultPath); !bytes.Equal(got, append(bytes.Clone(before), '\n')) {
				t.Fatal("conflicting Vault/index edit was overwritten")
			}
		})
	}
}

func TestMarkdownBindingRejectsPubliclyRehashedLedger(t *testing.T) {
	env := setupMarkdownPublication(t, "project-markdown-rehashed")
	reviewPath := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	accepted := loadMarkdownProjectionForTest(t, env)
	pair := reviewv4.MarkdownPair{Review: readTestFile(t, reviewPath), History: readTestFile(t, filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.HistoryRelativePath)))}
	pair.Review = replaceMarkdownFieldForTest(t, pair.Review, "project-overview", "goal", accepted.Review.CurrentState.Goal, "publicly resigned")
	draft, err := reviewv4.ParseMarkdownDraft(pair, accepted.Ledger)
	if err != nil {
		t.Fatal(err)
	}
	resigned, err := reviewv4.RenderMarkdownDraft(draft.Presentation, accepted.Ledger, pair)
	if err != nil {
		t.Fatal(err)
	}
	ledger := accepted.Ledger
	ledger.AcceptedRevision = draft.Presentation.Revision
	ledger.DocumentProjection.PresentationBase = draft.Presentation
	ledger.HumanPatches = draft.Presentation.HumanPatches
	ledger.OrphanPatches = draft.Presentation.OrphanPatches
	ledger.GeneratedBaselines = draft.Presentation.GeneratedBaselines
	ledger.ReviewSHA256, ledger.HistorySHA256 = sha256Hex(resigned.Review), sha256Hex(resigned.History)
	ledger.SyncHashes.ReviewSHA256, ledger.SyncHashes.HistorySHA256 = ledger.ReviewSHA256, ledger.HistorySHA256
	ledgerBody, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviewv4.LoadProjection(resigned.Review, resigned.History, ledgerBody, readTestFile(t, filepath.Join(env.projectRoot, filepath.FromSlash(sessionIndexRelativePath)))); err != nil {
		t.Fatalf("publicly re-signed projection is not publicly valid: %v", err)
	}
	for _, file := range []struct {
		relative string
		body     []byte
		mode     os.FileMode
	}{{reviewv2.ReviewRelativePath, resigned.Review, 0o644}, {reviewv2.HistoryRelativePath, resigned.History, 0o644}, {reviewv2.MachineLedgerRelativePath, ledgerBody, 0o600}} {
		for _, target := range []string{filepath.Join(env.projectRoot, filepath.FromSlash(file.relative)), filepath.Join(env.vaultRoot, filepath.FromSlash(vaultRelativePath(env.mapping.VaultReviewPath, file.relative)))} {
			if err := os.WriteFile(target, file.body, file.mode); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := syncproject.RunMarkdown(context.Background(), env.syncOptions(true)); err == nil {
		t.Fatal("publicly rehashed ledger was trusted without the private receipt")
	}
}

func TestMarkdownInitialRetryRejectsMissingPrivateReceipt(t *testing.T) {
	env := setupMarkdownPublication(t, "project-markdown-missing-receipt")
	receipt := filepath.Join(env.dataRoot, "publication-journal", env.projectID, publicationstate.AcceptedReceiptLeaf)
	if err := os.Remove(receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(context.Background(), Options{ProjectID: env.projectID, PreparedGeneration: env.manifest.GenerationID, Plan: env.plan, Mapping: env.mapping, DataRoot: env.dataRoot, Now: time.Now}); err == nil {
		t.Fatal("published-generation pointer bypassed the missing private Markdown receipt")
	}
}

func TestMarkdownEditRejectsMissingLedgerWithoutChangingAcceptance(t *testing.T) {
	for _, side := range []string{"project", "vault"} {
		t.Run(side, func(t *testing.T) {
			env := setupMarkdownPublication(t, "project-markdown-missing-ledger-"+side)
			priorReceipt := loadAcceptedReceiptForTest(t, env)
			priorBase := loadMarkdownBaseForTest(t, env)
			root := env.projectRoot
			relative := reviewv2.MachineLedgerRelativePath
			if side == "vault" {
				root = env.vaultRoot
				relative = vaultRelativePath(env.mapping.VaultReviewPath, relative)
			}
			ledgerPath := filepath.Join(root, filepath.FromSlash(relative))
			if err := os.Remove(ledgerPath); err != nil {
				t.Fatal(err)
			}
			if _, err := syncproject.RunMarkdown(context.Background(), env.syncOptions(true)); err == nil {
				t.Fatal("missing machine ledger was accepted")
			}
			if _, err := os.Stat(ledgerPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed Markdown read recreated the missing ledger")
			}
			if got := loadAcceptedReceiptForTest(t, env); got.RevisionID != priorReceipt.RevisionID {
				t.Fatal("missing ledger advanced the accepted receipt")
			}
			if got := loadMarkdownBaseForTest(t, env); got.ContentHash != priorBase.ContentHash {
				t.Fatal("missing ledger advanced the common Base")
			}
		})
	}
}

func TestMarkdownBindingRejectsSingleSidedRehashedMachineLedger(t *testing.T) {
	env := setupMarkdownPublication(t, "project-markdown-single-rehashed-ledger")
	accepted := loadMarkdownProjectionForTest(t, env)
	ledger := accepted.Ledger
	ledger.Accounting.TotalDurationMS++
	ledgerBody, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ledgerBody, readTestFile(t, filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.MachineLedgerRelativePath)))) {
		t.Fatal("single-sided attack fixture did not change the machine ledger")
	}
	projectLedger := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.MachineLedgerRelativePath))
	if err := os.WriteFile(projectLedger, ledgerBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := syncproject.RunMarkdown(context.Background(), env.syncOptions(true)); err == nil {
		t.Fatal("single-sided self-rehashed machine ledger was accepted")
	}
}

func TestMarkdownEditFailureCheckpointMatrix(t *testing.T) {
	type failurePoint struct {
		name, side, relative string
		stage                publishCheckpoint
		accepted             bool
	}
	points := []failurePoint{
		{"before-initial-guard", "initial", "", checkpointBeforeIndexGuard, false},
		{"after-initial-guard", "initial", "", checkpointAfterIndexGuard, false},
		{"project-review", "project", reviewv2.ReviewRelativePath, checkpointAfterDestination, false},
		{"project-history", "project", reviewv2.HistoryRelativePath, checkpointAfterDestination, false},
		{"project-ledger", "project", reviewv2.MachineLedgerRelativePath, checkpointAfterDestination, false},
		{"before-vault-sync", "", "", checkpointBeforeVaultSync, false},
		{"vault-review", "vault", reviewv2.ReviewRelativePath, checkpointAfterDestination, false},
		{"vault-history", "vault", reviewv2.HistoryRelativePath, checkpointAfterDestination, false},
		{"vault-ledger", "vault", reviewv2.MachineLedgerRelativePath, checkpointAfterDestination, false},
		{"after-vault-sync", "", "", checkpointAfterVaultSync, false},
		{"after-public-validation", "", "", checkpointAfterPublicValidate, false},
		{"before-pointer", "", "", checkpointBeforePointerCommit, false},
		{"after-pointer", "", "", checkpointAfterPointerCommit, false},
		{"before-final-guard", "final", "", checkpointBeforeIndexGuard, false},
		{"after-final-guard", "final", "", checkpointAfterIndexGuard, false},
		{"before-receipt", "", "", checkpointBeforeReceiptCommit, false},
		{"after-receipt", "", "", checkpointAfterReceiptCommit, true},
	}
	for index, point := range points {
		t.Run(point.name, func(t *testing.T) {
			env := setupMarkdownPublication(t, fmt.Sprintf("project-markdown-failure-%02d", index))
			priorReceipt := loadAcceptedReceiptForTest(t, env)
			priorBase := loadMarkdownBaseForTest(t, env)
			priorIndex := readTestFile(t, filepath.Join(env.projectRoot, filepath.FromSlash(sessionIndexRelativePath)))
			accepted := loadMarkdownProjectionForTest(t, env)
			reviewPath := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
			pending := replaceMarkdownFieldForTest(t, readTestFile(t, reviewPath), "project-overview", "goal", accepted.Review.CurrentState.Goal, "human goal")
			if err := os.WriteFile(reviewPath, pending, 0o644); err != nil {
				t.Fatal(err)
			}
			edit := captureMarkdownPlanForTest(t, env)
			failure := errors.New("injected publication crash")
			opts := env.publishOptions()
			opts.checkpoint = func(stage publishCheckpoint, side, relative string) error {
				wantRelative := point.relative
				if point.side == "vault" {
					wantRelative = vaultRelativePath(env.mapping.VaultReviewPath, point.relative)
				}
				if stage == point.stage && side == point.side && relative == wantRelative {
					panic(failure)
				}
				return nil
			}
			owner, err := publicationlock.Acquire(env.dataRoot, env.projectID, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			panicked := false
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						panicked = errors.Is(recovered.(error), failure)
					}
				}()
				_, _ = PublishMarkdownEditLocked(context.Background(), opts, edit, owner)
			}()
			if !panicked {
				t.Fatal("publication did not reach injected crash checkpoint")
			}
			if err := RecoverMarkdownLocked(context.Background(), env.publishOptions(), owner); err != nil {
				t.Fatalf("recovery: %v", err)
			}
			if err := owner.Release(); err != nil {
				t.Fatal(err)
			}
			gotReceipt := loadAcceptedReceiptForTest(t, env)
			gotBase := loadMarkdownBaseForTest(t, env)
			if point.accepted {
				got := loadMarkdownProjectionForTest(t, env)
				if gotReceipt.RevisionID == priorReceipt.RevisionID || gotBase.ContentHash == priorBase.ContentHash || got.Review.CurrentState.Goal != "human goal" {
					t.Fatal("post-receipt failure rolled back the accepted revision")
				}
			} else if gotReceipt.RevisionID != priorReceipt.RevisionID || gotBase.ContentHash != priorBase.ContentHash || !bytes.Equal(readTestFile(t, reviewPath), pending) {
				t.Fatal("pre-receipt failure advanced acceptance or lost the pending draft")
			}
			gotIndex := readTestFile(t, filepath.Join(env.projectRoot, filepath.FromSlash(sessionIndexRelativePath)))
			parsedIndex, err := sessionindex.Parse(gotIndex)
			if err != nil || !bytes.Equal(gotIndex, priorIndex) || parsedIndex.GenerationID != env.manifest.GenerationID {
				t.Fatal("failure changed index bytes or generation")
			}
		})
	}
}

func TestMarkdownRecoveryPreservesNewProjectEditAndRestoresBase(t *testing.T) {
	env := setupMarkdownPublication(t, "project-markdown-recovery-new-edit")
	priorReceipt := loadAcceptedReceiptForTest(t, env)
	priorBase := loadMarkdownBaseForTest(t, env)
	accepted := loadMarkdownProjectionForTest(t, env)
	reviewPath := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	pending := replaceMarkdownFieldForTest(t, readTestFile(t, reviewPath), "project-overview", "goal", accepted.Review.CurrentState.Goal, "pending goal")
	if err := os.WriteFile(reviewPath, pending, 0o644); err != nil {
		t.Fatal(err)
	}
	edit := captureMarkdownPlanForTest(t, env)
	failure := errors.New("crash before receipt")
	opts := env.publishOptions()
	opts.checkpoint = func(stage publishCheckpoint, _, _ string) error {
		if stage == checkpointBeforeReceiptCommit {
			panic(failure)
		}
		return nil
	}
	owner, err := publicationlock.Acquire(env.dataRoot, env.projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recovered := recover(); !errors.Is(recovered.(error), failure) {
				t.Fatalf("publication panic = %v", recovered)
			}
		}()
		_, _ = PublishMarkdownEditLocked(context.Background(), opts, edit, owner)
	}()
	newer := replaceMarkdownFieldForTest(t, readTestFile(t, reviewPath), "project-overview", "goal", "pending goal", "newer recovery edit")
	if err := os.WriteFile(reviewPath, newer, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RecoverMarkdownLocked(context.Background(), env.publishOptions(), owner); err == nil {
		t.Fatal("recovery accepted a Project edit made after the interrupted publication")
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, reviewPath); !bytes.Equal(got, newer) {
		t.Fatal("recovery overwrote the newer Project edit")
	}
	if got := loadAcceptedReceiptForTest(t, env); got.RevisionID != priorReceipt.RevisionID {
		t.Fatal("recovery advanced the accepted receipt")
	}
	if got := loadMarkdownBaseForTest(t, env); got.ContentHash != priorBase.ContentHash {
		t.Fatal("recovery left the failed revision in the common Base")
	}
}

func TestMarkdownRecoveryDoesNotOverwriteConflictingBase(t *testing.T) {
	env := setupMarkdownPublication(t, "project-markdown-basecas")
	priorReceipt := loadAcceptedReceiptForTest(t, env)
	accepted := loadMarkdownProjectionForTest(t, env)
	reviewPath := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	pending := replaceMarkdownFieldForTest(t, readTestFile(t, reviewPath), "project-overview", "goal", accepted.Review.CurrentState.Goal, "pending goal")
	if err := os.WriteFile(reviewPath, pending, 0o644); err != nil {
		t.Fatal(err)
	}
	edit := captureMarkdownPlanForTest(t, env)
	failure := errors.New("crash before receipt")
	opts := env.publishOptions()
	opts.checkpoint = func(stage publishCheckpoint, _, _ string) error {
		if stage == checkpointBeforeReceiptCommit {
			panic(failure)
		}
		return nil
	}
	owner, err := publicationlock.Acquire(env.dataRoot, env.projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() { _ = recover() }()
		_, _ = PublishMarkdownEditLocked(context.Background(), opts, edit, owner)
	}()
	root, err := os.OpenRoot(filepath.Join(env.dataRoot, "projects", env.projectID))
	if err != nil {
		t.Fatal(err)
	}
	store := syncengine.BaseStore{Root: root}
	failedBase := loadMarkdownBaseForTest(t, env)
	currentPair := acceptedMarkdownPairForTest(t, env, failedBase)
	foreignReview := replaceMarkdownFieldForTest(t, currentPair.Review, "project-overview", "goal", "pending goal", "foreign base edit")
	foreign, err := syncengine.NewMarkdownBaseRecord(foreignReview, currentPair.History, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(failedBase.ContentHash, foreign); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RecoverMarkdownLocked(context.Background(), env.publishOptions(), owner); err == nil {
		t.Fatal("recovery silently accepted a conflicting common Base")
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	if got := loadMarkdownBaseForTest(t, env); got.ContentHash != foreign.ContentHash {
		t.Fatal("recovery overwrote a conflicting common Base")
	}
	if got := loadAcceptedReceiptForTest(t, env); got.RevisionID != priorReceipt.RevisionID {
		t.Fatal("Base conflict advanced the accepted receipt")
	}
}

func TestMarkdownEditRejectsIndexChangeAtFinalGuard(t *testing.T) {
	env := setupMarkdownPublication(t, "project-markdown-final-guard")
	accepted := loadMarkdownProjectionForTest(t, env)
	reviewPath := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	pending := replaceMarkdownFieldForTest(t, readTestFile(t, reviewPath), "project-overview", "goal", accepted.Review.CurrentState.Goal, "human goal")
	if err := os.WriteFile(reviewPath, pending, 0o644); err != nil {
		t.Fatal(err)
	}
	edit := captureMarkdownPlanForTest(t, env)
	indexPath := filepath.Join(env.projectRoot, filepath.FromSlash(sessionIndexRelativePath))
	opts := env.publishOptions()
	opts.checkpoint = func(stage publishCheckpoint, _, _ string) error {
		if stage == checkpointAfterPointerCommit {
			return os.WriteFile(indexPath, append(readTestFile(t, indexPath), '\n'), 0o600)
		}
		return nil
	}
	owner, err := publicationlock.Acquire(env.dataRoot, env.projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, publishErr := PublishMarkdownEditLocked(context.Background(), opts, edit, owner)
	_ = owner.Release()
	if publishErr == nil {
		t.Fatal("final index guard accepted changed bytes")
	}
}

func TestMarkdownReceiptDistinguishesAcceptedFromSameGenerationRollback(t *testing.T) {
	data := t.TempDir()
	projectID := "project-markdown-receipt"
	j, err := OpenJournal(data, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	accepted := markdownIntentForTest(projectID, "a")
	if err := j.Create(accepted); err != nil {
		t.Fatal(err)
	}
	for _, step := range [][2]Stage{{StagePrepared, StageProjectWritten}, {StageProjectWritten, StageVaultSynced}, {StageVaultSynced, StageVerified}, {StageVerified, StageBaseCommitted}} {
		if err := j.Advance(step[0], step[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.CommitMarkdownAccepted(); err != nil {
		t.Fatal(err)
	}
	receipt, err := j.LoadAcceptedMarkdown()
	if err != nil || receipt.RevisionID != accepted.RevisionID {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}

	failed := markdownIntentForTest(projectID, "b")
	if err := j.Create(failed); err != nil {
		t.Fatal(err)
	}
	if err := j.Advance(StagePrepared, StageRollbackRequired); err != nil {
		t.Fatal(err)
	}
	if err := j.CommitMarkdownRolledBack(); err != nil {
		t.Fatal(err)
	}
	current, err := j.Load()
	if err != nil || current.Stage != StageCommitted || current.Outcome != OutcomeRolledBack {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	retained, err := j.LoadAcceptedMarkdown()
	if err != nil || retained.RevisionID != accepted.RevisionID {
		t.Fatalf("prior accepted receipt was lost: receipt=%+v err=%v", retained, err)
	}
}

func TestMarkdownIntentRejectsIncompleteBaseRollbackPayload(t *testing.T) {
	intent := markdownIntentForTest("project-markdown-base-payload", "a")
	intent.BaseHistoryPreimage = ""
	if err := publicationstate.ValidateIntent(intent, intent.ProjectID); err == nil {
		t.Fatal("accepted an intent that cannot restore its prior Base pair")
	}
}

func TestMarkdownIntentAuthenticatesObservedPointerPreimage(t *testing.T) {
	intent := markdownIntentForTest("project-markdown-pointer-wire", "a")
	intent.RequiresPointer = true
	intent.RevisionID = MarkdownRevisionID(intent)
	if err := publicationstate.ValidateIntent(intent, intent.ProjectID); err == nil {
		t.Fatal("four-file intent accepted an unknown pointer preimage")
	}
	observedAbsent := ""
	intent.PointerPreimage = &observedAbsent
	intent.RevisionID = MarkdownRevisionID(intent)
	if err := publicationstate.ValidateIntent(intent, intent.ProjectID); err != nil {
		t.Fatalf("four-file intent rejected an observed absent pointer: %v", err)
	}
	observedSameGeneration := intent.GenerationID
	intent.PointerPreimage = &observedSameGeneration
	if err := publicationstate.ValidateIntent(intent, intent.ProjectID); err == nil {
		t.Fatal("pointer preimage changed without invalidating the revision digest")
	}
	intent = markdownIntentForTest("project-markdown-pointer-wire", "b")
	intent.PointerPreimage = &observedAbsent
	intent.RevisionID = MarkdownRevisionID(intent)
	if err := publicationstate.ValidateIntent(intent, intent.ProjectID); err == nil {
		t.Fatal("three-file intent accepted an inapplicable pointer preimage")
	}
}

func markdownIntentForTest(projectID, seed string) Intent {
	hash := strings.Repeat(seed, 64)
	intent := Intent{
		Version: 2, Kind: KindMarkdown, ProjectID: projectID,
		GenerationID: "generation-markdown", ManifestDigest: "sha256:" + strings.Repeat("c", 64),
		ProjectViewDigest: "sha256:" + strings.Repeat("d", 64), Stage: StagePrepared,
		CreatedAt: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC),
		Destinations: []Destination{
			{Side: "project", Relative: "docs/session-review/.session-reviewer/ledger.json", DesiredSHA256: hash},
			{Side: "project", Relative: "docs/session-review/项目历史.md", DesiredSHA256: hash},
			{Side: "project", Relative: "docs/session-review/项目回顾.md", DesiredSHA256: hash},
			{Side: "vault", Relative: "Session Review/.session-reviewer/ledger.json", DesiredSHA256: hash},
			{Side: "vault", Relative: "Session Review/项目历史.md", DesiredSHA256: hash},
			{Side: "vault", Relative: "Session Review/项目回顾.md", DesiredSHA256: hash},
		},
		IndexGuard:         &IndexGuard{Relative: sessionIndexRelativePath, VaultRelative: "Session Review/.session-reviewer/session-index.json", ProjectSHA256: hash, VaultSHA256: hash, Digest: "sha256:" + hash, GenerationID: "generation-markdown"},
		BasePreimageDigest: strings.Repeat("e", 64), BaseDesiredDigest: strings.Repeat("f", 64),
		BaseReviewPreimage: strings.Repeat("1", 64), BaseHistoryPreimage: strings.Repeat("2", 64),
	}
	intent.RevisionID = MarkdownRevisionID(intent)
	return intent
}

type markdownPublicationTestEnv struct {
	projectID, dataRoot, projectRoot, vaultRoot string
	mapping                                     config.ProjectMapping
	manifest                                    memory.GenerationManifest
	plan                                        presentation.RenderPlan
}

func setupMarkdownPublication(t *testing.T, projectID string) markdownPublicationTestEnv {
	t.Helper()
	dataRoot, projectRoot, vaultRoot, mapping, manifest, legacy := setupPublishEnvWithIndex(t, projectID, true)
	plan := validMarkdownPublicationPlanForTest(t, dataRoot, manifest, legacy)
	if _, err := Publish(context.Background(), Options{ProjectID: projectID, PreparedGeneration: manifest.GenerationID, Plan: plan, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}); err != nil {
		t.Fatalf("initial Markdown Publish: %v", err)
	}
	return markdownPublicationTestEnv{projectID: projectID, dataRoot: dataRoot, projectRoot: projectRoot, vaultRoot: vaultRoot, mapping: mapping, manifest: manifest, plan: plan}
}

func setupFlowMarkdownPublication(t *testing.T, projectID string) markdownPublicationTestEnv {
	t.Helper()
	dataRoot, projectRoot, vaultRoot, mapping, manifest, legacy := setupPublishEnvWithIndex(t, projectID, true)
	plan := validMarkdownPublicationPlanForTest(t, dataRoot, manifest, legacy)
	byRelative := make(map[string]*presentation.FilePlan, len(plan.Files))
	for index := range plan.Files {
		byRelative[plan.Files[index].Relative] = &plan.Files[index]
	}
	for _, document := range []struct {
		relative, id, entity string
	}{
		{reviewv2.ReviewRelativePath, "review-" + projectID, "project-review"},
		{reviewv2.HistoryRelativePath, "history-" + projectID, "project-history"},
	} {
		file := byRelative[document.relative]
		if file == nil {
			t.Fatalf("missing flow fixture file %s", document.relative)
		}
		flow := fmt.Sprintf("{id: %s, entity_type: %s, project_id: %s, custom_owner: '保留', schema_version: 4, document_format: review-markdown-v1, revision: !!int +1, generation_id: %q, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_note: team's, custom_a: one, custom_b: two,}\r\n", document.id, document.entity, projectID, manifest.GenerationID)
		file.Desired = replaceMarkdownFrontmatterForTest(t, file.Desired, []byte(flow))
		if document.relative == reviewv2.ReviewRelativePath {
			file.Desired = bytes.Replace(file.Desired, []byte("---\n# 项目回顾"), []byte("---\n自定义段落和 [链接](https://example.test/custom) 必须保留。\n\n```yaml\ncustom: code-block\n```\n\n# 项目回顾"), 1)
		}
	}
	ledgerFile := byRelative[reviewv2.MachineLedgerRelativePath]
	if ledgerFile == nil {
		t.Fatal("missing flow fixture ledger")
	}
	ledger, err := reviewv4.DecodeLedger(ledgerFile.Desired)
	if err != nil {
		t.Fatal(err)
	}
	ledger.ReviewSHA256 = sha256Hex(byRelative[reviewv2.ReviewRelativePath].Desired)
	ledger.HistorySHA256 = sha256Hex(byRelative[reviewv2.HistoryRelativePath].Desired)
	ledger.SyncHashes.ReviewSHA256 = ledger.ReviewSHA256
	ledger.SyncHashes.HistorySHA256 = ledger.HistorySHA256
	ledgerFile.Desired, err = reviewv4.RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(context.Background(), Options{ProjectID: projectID, PreparedGeneration: manifest.GenerationID, Plan: plan, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}); err != nil {
		t.Fatalf("initial flow Markdown Publish: %v", err)
	}
	return markdownPublicationTestEnv{projectID: projectID, dataRoot: dataRoot, projectRoot: projectRoot, vaultRoot: vaultRoot, mapping: mapping, manifest: manifest, plan: plan}
}

func replaceMarkdownFrontmatterForTest(t *testing.T, raw, replacement []byte) []byte {
	t.Helper()
	firstEnd := bytes.IndexByte(raw, '\n') + 1
	if firstEnd == 0 {
		t.Fatal("missing opening frontmatter line")
	}
	closingOffset := bytes.Index(raw[firstEnd:], []byte("---\n"))
	if closingOffset < 0 {
		t.Fatal("missing closing frontmatter line")
	}
	closingStart := firstEnd + closingOffset
	result := make([]byte, 0, len(raw)-(closingStart-firstEnd)+len(replacement))
	result = append(result, raw[:firstEnd]...)
	result = append(result, replacement...)
	result = append(result, raw[closingStart:]...)
	return result
}

func validMarkdownPublicationPlanForTest(t *testing.T, dataRoot string, manifest memory.GenerationManifest, legacy presentation.RenderPlan) presentation.RenderPlan {
	t.Helper()
	store, err := memorystore.Open(dataRoot, manifest.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	index, err := store.LoadObject(memorystore.ObjectSessionIndex, manifest.SessionIndexDigest)
	if closeErr := store.Close(); err != nil || closeErr != nil {
		t.Fatal(errors.Join(err, closeErr))
	}
	byPath := make(map[string]presentation.FilePlan, len(legacy.Files))
	for _, file := range legacy.Files {
		byPath[file.Relative] = file
	}
	migrated, err := migrationv4.BuildPreview(migrationv4.Input{
		Review: byPath[reviewv2.ReviewRelativePath].Desired, History: byPath[reviewv2.HistoryRelativePath].Desired,
		Ledger: byPath[reviewv2.MachineLedgerRelativePath].Desired, SessionIndex: index,
		GenerationID: manifest.GenerationID, TargetPreimages: map[string]migrationv4.Preimage{},
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := reviewv4.LoadProjection(migrated.Review, migrated.History, migrated.Ledger, migrated.SessionIndex)
	if err != nil {
		t.Fatal(err)
	}
	accepted.Review.Timeline = append(accepted.Review.Timeline, reviewv4.Timeline{
		ID: "milestone-test", GenerationID: manifest.GenerationID, OccurredAt: manifest.CreatedAt,
		Kind: "milestone", Title: "Test milestone", Summary: "Test summary", DecisionIDs: []string{}, ClosedLoop: reviewv4.NeutralClosedLoop(),
	})
	ledger := accepted.Ledger
	ledger.MinimumReaderVersion, ledger.MinimumWriterVersion = "0.4.1", "0.4.1"
	ledger.DocumentProjection = &reviewv4.DocumentProjection{SchemaVersion: 1, Format: "review-markdown-v1", PresentationBase: accepted.Review}
	seedLedger, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		t.Fatalf("bind initial Markdown ledger: %v", err)
	}
	ledger, err = reviewv4.DecodeLedger(seedLedger)
	if err != nil {
		t.Fatalf("decode bound initial Markdown ledger: %v", err)
	}
	pair, err := reviewv4.RenderMarkdown(accepted.Review, ledger, nil)
	if err != nil {
		t.Fatalf("render initial Markdown pair: %v", err)
	}
	ledger.ReviewSHA256, ledger.HistorySHA256 = sha256Hex(pair.Review), sha256Hex(pair.History)
	ledger.SyncHashes.ReviewSHA256, ledger.SyncHashes.HistorySHA256 = ledger.ReviewSHA256, ledger.HistorySHA256
	ledgerBody, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		t.Fatalf("render initial Markdown ledger: %v", err)
	}
	if _, err := reviewv4.LoadProjection(pair.Review, pair.History, ledgerBody, index); err != nil {
		t.Fatalf("load initial Markdown projection: %v", err)
	}
	return presentation.RenderPlan{ProjectID: manifest.ProjectID, GenerationID: manifest.GenerationID, ProjectViewDigest: manifest.ProjectViewDigest, Files: []presentation.FilePlan{
		{Relative: reviewv2.ReviewRelativePath, Desired: pair.Review, Mode: 0o644},
		{Relative: reviewv2.HistoryRelativePath, Desired: pair.History, Mode: 0o644},
		{Relative: reviewv2.MachineLedgerRelativePath, Desired: ledgerBody, Mode: 0o600},
		{Relative: sessionIndexRelativePath, Desired: index, Mode: 0o600},
	}}
}

func (env markdownPublicationTestEnv) publishOptions() Options {
	return Options{ProjectID: env.projectID, PreparedGeneration: env.manifest.GenerationID, Mapping: env.mapping, DataRoot: env.dataRoot, Now: time.Now}
}

func (env markdownPublicationTestEnv) syncOptions(dry bool) syncproject.Options {
	return syncproject.Options{ProjectID: env.projectID, CWD: env.projectRoot, DataDir: env.dataRoot, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI, DryRun: dry}
}

func captureMarkdownPlanForTest(t *testing.T, env markdownPublicationTestEnv) syncproject.MarkdownSyncPlan {
	t.Helper()
	var captured syncproject.MarkdownSyncPlan
	opts := env.syncOptions(false)
	opts.RecoverMarkdown = func(context.Context, *publicationlock.Owner) error { return nil }
	opts.PublishMarkdown = func(_ context.Context, plan syncproject.MarkdownSyncPlan, _ *publicationlock.Owner) error {
		captured = plan
		return nil
	}
	if _, err := syncproject.RunMarkdown(context.Background(), opts); err != nil {
		t.Fatalf("capture Markdown plan: %v", err)
	}
	if len(captured.Plan.Files) != 3 {
		t.Fatalf("captured Markdown file count = %d", len(captured.Plan.Files))
	}
	return captured
}

func loadMarkdownProjectionForTest(t *testing.T, env markdownPublicationTestEnv) reviewv4.Accepted {
	t.Helper()
	read := func(relative string) []byte {
		return readTestFile(t, filepath.Join(env.projectRoot, filepath.FromSlash(relative)))
	}
	accepted, err := reviewv4.LoadProjection(read(reviewv2.ReviewRelativePath), read(reviewv2.HistoryRelativePath), read(reviewv2.MachineLedgerRelativePath), read(sessionIndexRelativePath))
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

func loadAcceptedReceiptForTest(t *testing.T, env markdownPublicationTestEnv) AcceptedMarkdownReceipt {
	t.Helper()
	j, err := OpenJournal(env.dataRoot, env.projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	receipt, err := j.LoadAcceptedMarkdown()
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func loadMarkdownBaseForTest(t *testing.T, env markdownPublicationTestEnv) syncengine.BaseRecord {
	t.Helper()
	root, err := os.OpenRoot(filepath.Join(env.dataRoot, "projects", env.projectID))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	record, found, err := (syncengine.BaseStore{Root: root}).Load(syncengine.MarkdownBaseEntityID)
	if err != nil || !found {
		t.Fatalf("load Markdown Base: found=%t err=%v", found, err)
	}
	return record
}

func acceptedMarkdownPairForTest(t *testing.T, _ markdownPublicationTestEnv, record syncengine.BaseRecord) reviewv4.MarkdownPair {
	t.Helper()
	review, history, err := syncengine.MarkdownBaseDocuments(record)
	if err != nil {
		t.Fatal(err)
	}
	return reviewv4.MarkdownPair{Review: review, History: history}
}

func replaceMarkdownFieldForTest(t *testing.T, body []byte, entity, name, before, after string) []byte {
	t.Helper()
	marker := []byte("<!-- session-reviewer:v4-field entity=\"" + entity + "\" name=\"" + name + "\" -->\n" + before + "\n<!-- /session-reviewer:v4-field entity=\"" + entity + "\" name=\"" + name + "\" -->")
	replacement := []byte("<!-- session-reviewer:v4-field entity=\"" + entity + "\" name=\"" + name + "\" -->\n" + after + "\n<!-- /session-reviewer:v4-field entity=\"" + entity + "\" name=\"" + name + "\" -->")
	updated := bytes.Replace(body, marker, replacement, 1)
	if bytes.Equal(updated, body) {
		t.Fatalf("field %s/%s was not found", entity, name)
	}
	return updated
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func snapshotMarkdownTree(t *testing.T, root string) string {
	t.Helper()
	var rows []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		row := filepath.ToSlash(relative) + "|" + info.Mode().String()
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			row += "|" + sha256Hex(body)
		}
		rows = append(rows, row)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(rows)
	return strings.Join(rows, "\n")
}
