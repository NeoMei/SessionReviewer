package publication

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

// Routing old v4 through generic publication would omit the Markdown Base and
// accepted receipt. An already-bound source needs no new private generation:
// only the human-document revision advances.
func TestOldV4ExistingIndexMigrationKeepsAuthenticatedGenerationAndIndex(t *testing.T) {
	projectID := "project-old-v4-markdown"
	dataRoot, projectRoot, vaultRoot, mapping, sourceManifest, legacy := setupPublishEnvWithIndex(t, projectID, true)
	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	index, err := store.LoadObject(memorystore.ObjectSessionIndex, sourceManifest.SessionIndexDigest)
	if closeErr := store.Close(); err != nil || closeErr != nil {
		t.Fatal(errors.Join(err, closeErr))
	}
	sourceIndex := append([]byte(nil), index...)
	byRelative := make(map[string]presentation.FilePlan, len(legacy.Files))
	for _, file := range legacy.Files {
		byRelative[file.Relative] = file
	}
	oldV4, err := migrationv4.BuildPreview(migrationv4.Input{
		Review: byRelative[reviewv2.ReviewRelativePath].Desired, History: byRelative[reviewv2.HistoryRelativePath].Desired,
		Ledger: byRelative[reviewv2.MachineLedgerRelativePath].Desired, SessionIndex: index,
		GenerationID: sourceManifest.GenerationID, TargetPreimages: map[string]migrationv4.Preimage{},
	})
	if err != nil {
		t.Fatal(err)
	}
	sourcePlan := presentation.RenderPlan{ProjectID: projectID, GenerationID: sourceManifest.GenerationID, ProjectViewDigest: sourceManifest.ProjectViewDigest, Files: []presentation.FilePlan{
		{Relative: reviewv2.ReviewRelativePath, Desired: oldV4.Review, Mode: 0o644},
		{Relative: reviewv2.HistoryRelativePath, Desired: oldV4.History, Mode: 0o644},
		{Relative: reviewv2.MachineLedgerRelativePath, Desired: oldV4.Ledger, Mode: 0o600},
		{Relative: sessionIndexRelativePath, Desired: oldV4.SessionIndex, Mode: 0o600},
	}}
	if _, err := Publish(t.Context(), Options{ProjectID: projectID, PreparedGeneration: sourceManifest.GenerationID, Plan: sourcePlan, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "projects", projectID, "locks", "sync.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture unexpectedly pre-created sync.lock: %v", err)
	}
	options := syncproject.MigrationOptions{Options: syncproject.Options{ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI}, Mode: syncproject.MigrationDryRun}
	dry, err := syncproject.RunMigration(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Preview.GenerationID != sourceManifest.GenerationID || dry.Preview.SourceGenerationID != sourceManifest.GenerationID || dry.Preview.SourceManifestDigest == "" || dry.Preview.TargetManifestDigest != dry.Preview.SourceManifestDigest || dry.Preview.TargetHashes.SessionIndex != dry.Preview.SourceHashes.SessionIndex {
		t.Fatalf("preview did not preserve authenticated source binding: %+v", dry.Preview)
	}
	for _, test := range []struct {
		name string
		path string
	}{
		{name: "Project source", path: filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))},
		{name: "Vault source", path: filepath.Join(vaultRoot, filepath.FromSlash(vaultRelativePath(mapping.VaultReviewPath, reviewv2.HistoryRelativePath)))},
	} {
		t.Run("stale "+test.name, func(t *testing.T) {
			before, readErr := os.ReadFile(test.path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if err := os.WriteFile(test.path, append(append([]byte(nil), before...), '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			stale := options
			stale.Mode, stale.ExpectedPreviewDigest = syncproject.MigrationConfirm, dry.Preview.PreviewDigest
			stale.Publish = func(context.Context, syncproject.MigrationPublication) error {
				t.Fatal("tampered source reached migration publisher")
				return nil
			}
			if _, err := syncproject.RunMigration(t.Context(), stale); err == nil {
				t.Fatalf("old preview accepted changed %s bytes", test.name)
			}
			if err := os.WriteFile(test.path, before, 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
	options.Mode, options.ExpectedPreviewDigest = syncproject.MigrationConfirm, dry.Preview.PreviewDigest
	failure := errors.New("simulated migration process crash")
	options.Publish = func(ctx context.Context, plan syncproject.MigrationPublication) error {
		_, err := PublishMarkdownMigrationLocked(ctx, Options{
			ProjectID: projectID, Mapping: mapping, DataRoot: dataRoot, Now: time.Now,
			AfterDestination: func(side, relative string) error {
				if side == "project" && relative == reviewv2.MachineLedgerRelativePath {
					panic(failure)
				}
				return nil
			},
		}, plan)
		return err
	}
	func() {
		defer func() {
			if recovered := recover(); !errors.Is(recovered.(error), failure) {
				t.Fatalf("migration panic=%v", recovered)
			}
		}()
		_, _ = syncproject.RunMigration(t.Context(), options)
	}()
	state, err := publicationstate.OpenReadOnly(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	active, intentErr := state.Intent()
	if closeErr := state.Close(); intentErr != nil || closeErr != nil || active.Stage == publicationstate.StageCommitted || active.MigrationSource == nil {
		t.Fatalf("active migration intent=%+v err=%v close=%v", active, intentErr, closeErr)
	}
	options.Recover = func(ctx context.Context, mapping config.ProjectMapping, dataRoot string, owner *publicationlock.Owner) error {
		return RecoverMarkdownLocked(ctx, Options{ProjectID: projectID, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}, owner)
	}
	options.Publish = func(ctx context.Context, plan syncproject.MigrationPublication) error {
		_, err := PublishMarkdownMigrationLocked(ctx, Options{ProjectID: projectID, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}, plan)
		return err
	}
	confirmed, err := syncproject.RunMigration(t.Context(), options)
	if err != nil || !confirmed.Applied {
		t.Fatalf("confirm=%+v err=%v", confirmed, err)
	}
	read := func(relative string) []byte { return readTestFile(t, projectRoot+"/"+relative) }
	accepted, err := reviewv4.LoadProjection(read(reviewv2.ReviewRelativePath), read(reviewv2.HistoryRelativePath), read(reviewv2.MachineLedgerRelativePath), read(sessionIndexRelativePath))
	if err != nil || accepted.Review.GenerationID != dry.Preview.GenerationID {
		t.Fatalf("accepted migration generation=%q err=%v", accepted.Review.GenerationID, err)
	}
	if got := read(sessionIndexRelativePath); string(got) != string(sourceIndex) {
		t.Fatal("format migration rewrote the authenticated source index")
	}
	readVault := func(relative string) []byte {
		return readTestFile(t, filepath.Join(vaultRoot, filepath.FromSlash(vaultRelativePath(mapping.VaultReviewPath, relative))))
	}
	vaultAccepted, err := reviewv4.LoadProjection(readVault(reviewv2.ReviewRelativePath), readVault(reviewv2.HistoryRelativePath), readVault(reviewv2.MachineLedgerRelativePath), readVault(sessionIndexRelativePath))
	if err != nil || vaultAccepted.Review.GenerationID != accepted.Review.GenerationID || vaultAccepted.Ledger.SyncHashes != accepted.Ledger.SyncHashes {
		t.Fatalf("Vault projection generation=%q err=%v", vaultAccepted.Review.GenerationID, err)
	}
	env := markdownPublicationTestEnv{projectID: projectID, dataRoot: dataRoot}
	receipt := loadAcceptedReceiptForTest(t, env)
	if receipt.GenerationID != dry.Preview.GenerationID {
		t.Fatalf("receipt generation=%q", receipt.GenerationID)
	}
	if base := loadMarkdownBaseForTest(t, env); base.ContentHash != receipt.BaseDigest {
		t.Fatalf("Base=%q receipt=%q", base.ContentHash, receipt.BaseDigest)
	}
	publicationOptions := Options{ProjectID: projectID, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}
	published := 0
	report, err := syncproject.RunMarkdown(t.Context(), syncproject.Options{
		ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI,
		RecoverMarkdown: func(ctx context.Context, owner *publicationlock.Owner) error {
			return RecoverMarkdownLocked(ctx, publicationOptions, owner)
		},
		PublishMarkdown: func(ctx context.Context, plan syncproject.MarkdownSyncPlan, owner *publicationlock.Owner) error {
			published++
			_, err := PublishMarkdownEditLocked(ctx, publicationOptions, plan, owner)
			return err
		},
	})
	if err != nil || published != 0 || len(report.Operations) != 0 {
		t.Fatalf("authenticated no-op published=%d operations=%d err=%v", published, len(report.Operations), err)
	}
}

// A legacy published generation may predate the private SessionIndexDigest
// binding. Confirmation must derive a normal successor from authenticated
// dependencies without rewriting the immutable source manifest.
func TestOldV4MissingIndexBindingBuildsAndPublishesAuthenticatedSuccessor(t *testing.T) {
	projectID := "project-old-v4-unbound-index"
	dataRoot, projectRoot, vaultRoot, mapping, sourceManifest, legacy := setupPublishEnvWithIndex(t, projectID, false)
	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	projectViewBody, err := store.LoadObject(memorystore.ObjectProjectView, sourceManifest.ProjectViewDigest)
	if err != nil {
		t.Fatal(err)
	}
	var projectView memory.ProjectView
	if err := json.Unmarshal(projectViewBody, &projectView); err != nil {
		t.Fatal(err)
	}
	views := make(map[sessionindex.SessionKey]*memory.SessionView, len(sourceManifest.SessionViews))
	for _, dependency := range sourceManifest.SessionViews {
		body, loadErr := store.LoadObject(memorystore.ObjectSessionView, dependency.Digest)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		var view memory.SessionView
		if err := json.Unmarshal(body, &view); err != nil {
			t.Fatal(err)
		}
		views[sessionindex.SessionKey{Provider: dependency.Provider, SessionID: dependency.SessionID}] = &view
	}
	generatedAt, err := time.Parse(time.RFC3339Nano, sourceManifest.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	publicIndex, err := sessionindex.Build(sessionindex.BuildInput{ProjectView: projectView, Manifest: sourceManifest, SessionViews: views, GeneratedAt: generatedAt})
	if err != nil {
		t.Fatal(err)
	}
	indexBody, err := sessionindex.Render(publicIndex)
	if closeErr := store.Close(); err != nil || closeErr != nil {
		t.Fatal(errors.Join(err, closeErr))
	}
	sourceManifestPath := filepath.Join(dataRoot, "projects", projectID, "memory-v1", "generations", sourceManifest.GenerationID+".json")
	sourceManifestBefore, err := os.ReadFile(sourceManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	byRelative := make(map[string]presentation.FilePlan, len(legacy.Files))
	for _, file := range legacy.Files {
		byRelative[file.Relative] = file
	}
	oldV4, err := migrationv4.BuildPreview(migrationv4.Input{
		Review: byRelative[reviewv2.ReviewRelativePath].Desired, History: byRelative[reviewv2.HistoryRelativePath].Desired,
		Ledger: byRelative[reviewv2.MachineLedgerRelativePath].Desired, SessionIndex: indexBody,
		GenerationID: sourceManifest.GenerationID, TargetPreimages: map[string]migrationv4.Preimage{},
	})
	if err != nil {
		t.Fatal(err)
	}
	sourcePlan := presentation.RenderPlan{ProjectID: projectID, GenerationID: sourceManifest.GenerationID, ProjectViewDigest: sourceManifest.ProjectViewDigest, Files: []presentation.FilePlan{
		{Relative: reviewv2.ReviewRelativePath, Desired: oldV4.Review, Mode: 0o644},
		{Relative: reviewv2.HistoryRelativePath, Desired: oldV4.History, Mode: 0o644},
		{Relative: reviewv2.MachineLedgerRelativePath, Desired: oldV4.Ledger, Mode: 0o600},
		{Relative: sessionIndexRelativePath, Desired: oldV4.SessionIndex, Mode: 0o600},
	}}
	if _, err := Publish(t.Context(), Options{ProjectID: projectID, PreparedGeneration: sourceManifest.GenerationID, Plan: sourcePlan, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}); err != nil {
		t.Fatal(err)
	}
	options := syncproject.MigrationOptions{Options: syncproject.Options{ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI}, Mode: syncproject.MigrationDryRun}
	dry, err := syncproject.RunMigration(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Preview.GenerationID == sourceManifest.GenerationID || dry.Preview.SourceGenerationID != sourceManifest.GenerationID || dry.Preview.TargetManifestDigest == dry.Preview.SourceManifestDigest {
		t.Fatalf("missing binding did not produce a successor: %+v", dry.Preview)
	}
	options.Mode, options.ExpectedPreviewDigest = syncproject.MigrationConfirm, dry.Preview.PreviewDigest
	preBeginCrash := errors.New("simulated crash after prepared advance")
	options.Publish = func(ctx context.Context, plan syncproject.MigrationPublication) error {
		_, err := PublishMarkdownMigrationLocked(ctx, Options{
			ProjectID: projectID, Mapping: mapping, DataRoot: dataRoot, Now: time.Now,
			checkpoint: func(stage publishCheckpoint, _, _ string) error {
				if stage == checkpointBeforeIndexGuard {
					panic(preBeginCrash)
				}
				return nil
			},
		}, plan)
		return err
	}
	func() {
		defer func() {
			if recovered := recover(); !errors.Is(recovered.(error), preBeginCrash) {
				t.Fatalf("prepared advance panic=%v", recovered)
			}
		}()
		_, _ = syncproject.RunMigration(t.Context(), options)
	}()
	state, err := publicationstate.OpenReadOnly(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	intent, intentErr := state.Intent()
	if closeErr := state.Close(); intentErr != nil || closeErr != nil || intent.Version != 1 || intent.Stage != publicationstate.StageCommitted {
		t.Fatalf("pre-Begin crash replaced source journal: intent=%+v err=%v close=%v", intent, intentErr, closeErr)
	}
	options.Publish = func(ctx context.Context, plan syncproject.MigrationPublication) error {
		_, err := PublishMarkdownMigrationLocked(ctx, Options{ProjectID: projectID, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}, plan)
		return err
	}
	confirmed, err := syncproject.RunMigration(t.Context(), options)
	if err != nil || !confirmed.Applied {
		t.Fatalf("confirm=%+v err=%v", confirmed, err)
	}
	store, err = memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	publishedID, publishedManifest, loadErr := store.LoadPublished()
	if closeErr := store.Close(); loadErr != nil || closeErr != nil || publishedID != dry.Preview.GenerationID || publishedManifest.SessionIndexDigest == "" {
		t.Fatalf("published=%q manifest=%+v load=%v close=%v", publishedID, publishedManifest, loadErr, closeErr)
	}
	if sourceManifestAfter, err := os.ReadFile(sourceManifestPath); err != nil || string(sourceManifestAfter) != string(sourceManifestBefore) {
		t.Fatalf("immutable source manifest changed: err=%v", err)
	}
	readVault := func(relative string) []byte {
		return readTestFile(t, filepath.Join(vaultRoot, filepath.FromSlash(vaultRelativePath(mapping.VaultReviewPath, relative))))
	}
	accepted, err := reviewv4.LoadProjection(readVault(reviewv2.ReviewRelativePath), readVault(reviewv2.HistoryRelativePath), readVault(reviewv2.MachineLedgerRelativePath), readVault(sessionIndexRelativePath))
	if err != nil || accepted.Review.GenerationID != publishedID {
		t.Fatalf("Vault successor generation=%q err=%v", accepted.Review.GenerationID, err)
	}
}
