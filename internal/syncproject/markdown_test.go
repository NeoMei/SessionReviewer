package syncproject

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/migrationv4"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

func TestMarkdownBindingRejectsLoosePublicIndex(t *testing.T) {
	err := VerifyMarkdownBinding(reviewv4.Accepted{}, memory.GenerationManifest{})
	if err == nil {
		t.Fatal("accepted public files without a private binding")
	}
}

func TestRunMarkdownCancelledBeforeSyncDoesNotRecoverOrPublish(t *testing.T) {
	fixture, _ := newMarkdownLockFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, err := RunMarkdown(ctx, Options{
		ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data,
		GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI,
		RecoverMarkdown: func(context.Context, *publicationlock.Owner) error { called = true; return nil },
		PublishMarkdown: func(context.Context, MarkdownSyncPlan, *publicationlock.Owner) error { called = true; return nil },
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled sync err=%v callbacks=%v", err, called)
	}
}

func TestReadMarkdownForScanKeepsOldAcceptanceSeparateFromVaultOnlyDraft(t *testing.T) {
	fixture, accepted := newMarkdownLockFixture(t)
	vaultReview := filepath.Join(fixture.vault, "Projects", "Migration", "Session Review", "项目回顾.md")
	before, err := os.ReadFile(vaultReview)
	if err != nil {
		t.Fatal(err)
	}
	document, err := reviewv4.ParseMarkdownDocument("项目回顾.md", before)
	if err != nil {
		t.Fatal(err)
	}
	after, err := document.ReplaceFields(map[reviewv4.FieldKey]string{{Entity: "project-overview", Name: "goal"}: "Vault-only goal"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultReview, after, 0o644); err != nil {
		t.Fatal(err)
	}
	owner, err := publicationlock.Acquire(fixture.data, fixture.projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	read, err := ReadMarkdownForScan(context.Background(), Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerPeriodic}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if read.OldAccepted.Review.CurrentState.Goal != accepted.Review.CurrentState.Goal || read.Pending.Presentation.CurrentState.Goal != "Vault-only goal" {
		t.Fatalf("old acceptance and pending draft were conflated: old=%q pending=%q", read.OldAccepted.Review.CurrentState.Goal, read.Pending.Presentation.CurrentState.Goal)
	}
	if bytes.Equal(read.ProjectExpected[reviewv2.ReviewRelativePath], read.VaultExpected[reviewv2.ReviewRelativePath]) || read.ExpectedReceiptRevision == "" || read.ExpectedBaseDigest == "" {
		t.Fatal("scan read lost exact divergent preimages or private receipt/Base identity")
	}
}

func TestReadMarkdownForScanDoesNotBlessAnEditAfterMerge(t *testing.T) {
	fixture, accepted := newMarkdownLockFixture(t)
	reviewPath := filepath.Join(fixture.project, filepath.FromSlash(reviewv2.ReviewRelativePath))
	before, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	var late []byte
	options := Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerPeriodic}
	options.afterMarkdownBuild = func() error {
		document, err := reviewv4.ParseMarkdownDocument("项目回顾.md", before)
		if err != nil {
			return err
		}
		late, err = document.ReplaceFields(map[reviewv4.FieldKey]string{{Entity: "project-overview", Name: "goal"}: "late edit after merge"})
		if err != nil {
			return err
		}
		return os.WriteFile(reviewPath, late, 0o644)
	}
	owner, err := publicationlock.Acquire(fixture.data, fixture.projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	read, err := ReadMarkdownForScan(context.Background(), options, owner)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, late) || !bytes.Equal(read.ProjectExpected[reviewv2.ReviewRelativePath], before) || bytes.Equal(read.ProjectExpected[reviewv2.ReviewRelativePath], late) {
		t.Fatal("post-merge edit was blessed as the publication CAS preimage")
	}
	current, err := os.ReadFile(reviewPath)
	if err != nil || !bytes.Equal(current, late) || read.Pending.Presentation.CurrentState.Goal != accepted.Review.CurrentState.Goal {
		t.Fatalf("late edit was lost or merged retroactively: current=%q pending=%q err=%v", current, read.Pending.Presentation.CurrentState.Goal, err)
	}
}

func TestMarkdownDryRunReportsPendingWritesAndNoOp(t *testing.T) {
	fixture, accepted := newMarkdownLockFixture(t)
	reviewPath := filepath.Join(fixture.project, filepath.FromSlash(reviewv2.ReviewRelativePath))
	before, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	after := bytes.Replace(before, []byte("\n"+accepted.Review.CurrentState.Goal+"\n<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->"), []byte("\ndry-run goal\n<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->"), 1)
	if bytes.Equal(before, after) {
		t.Fatal("Markdown fixture has no editable goal block")
	}
	if err := os.WriteFile(reviewPath, after, 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := RunMarkdown(context.Background(), Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Operations) != 6 {
		t.Fatalf("pending dry-run operations = %#v, want six writes", report.Operations)
	}
	repeated, err := RunMarkdown(context.Background(), Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI, DryRun: true})
	if err != nil || !reflect.DeepEqual(repeated.Operations, report.Operations) {
		t.Fatalf("dry-run operations are not deterministic: first=%#v repeated=%#v err=%v", report.Operations, repeated.Operations, err)
	}
	if err := os.WriteFile(reviewPath, before, 0o644); err != nil {
		t.Fatal(err)
	}
	noOp, err := RunMarkdown(context.Background(), Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(noOp.Operations) != 0 {
		t.Fatalf("no-op dry-run operations = %#v", noOp.Operations)
	}
}

func TestMarkdownEditWaitsForPublicationLockBeforeReadingDraft(t *testing.T) {
	fixture, accepted := newMarkdownLockFixture(t)
	held, err := publicationlock.Acquire(fixture.data, fixture.projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Release() })
	reachedContention := make(chan struct{})
	continueAcquire := make(chan struct{})
	var captured MarkdownSyncPlan
	options := Options{
		ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data,
		GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI,
		RecoverMarkdown: func(context.Context, *publicationlock.Owner) error { return nil },
		PublishMarkdown: func(_ context.Context, plan MarkdownSyncPlan, _ *publicationlock.Owner) error {
			captured = plan
			return nil
		},
	}
	options.acquireMarkdownLock = func(dataRoot, projectID string, timeout time.Duration) (*publicationlock.Owner, error) {
		contender, acquireErr := publicationlock.Acquire(dataRoot, projectID, 0)
		if contender != nil {
			_ = contender.Release()
		}
		if !errors.Is(acquireErr, project.ErrProjectLocked) {
			return nil, errors.Join(errors.New("Markdown lock seam did not observe contention"), acquireErr)
		}
		close(reachedContention)
		<-continueAcquire
		return publicationlock.Acquire(dataRoot, projectID, timeout)
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := RunMarkdown(context.Background(), options)
		done <- runErr
	}()
	select {
	case <-reachedContention:
	case runErr := <-done:
		t.Fatalf("RunMarkdown returned before lock contention: %v", runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("RunMarkdown did not reach the contended publication lock")
	}
	reviewPath := filepath.Join(fixture.project, filepath.FromSlash(reviewv2.ReviewRelativePath))
	before, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	after := bytes.Replace(before, []byte("\n"+accepted.Review.CurrentState.Goal+"\n<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->"), []byte("\nedited while waiting\n<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->"), 1)
	if bytes.Equal(before, after) {
		t.Fatal("Markdown lock fixture has no editable goal block")
	}
	if err := os.WriteFile(reviewPath, after, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	close(continueAcquire)
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("RunMarkdown error = %v", runErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunMarkdown did not finish after publication lock release")
	}
	if len(captured.Plan.Files) != 3 {
		t.Fatalf("captured Markdown plan files = %d", len(captured.Plan.Files))
	}
	byRelative := map[string][]byte{}
	for _, file := range captured.Plan.Files {
		byRelative[file.Relative] = file.Desired
		if file.Relative == reviewv2.ReviewRelativePath && !bytes.Equal(file.Expected, after) {
			t.Fatal("locked plan used the stale pre-wait Project review preimage")
		}
	}
	projection, err := reviewv4.LoadProjection(byRelative[reviewv2.ReviewRelativePath], byRelative[reviewv2.HistoryRelativePath], byRelative[reviewv2.MachineLedgerRelativePath], captured.Index)
	if err != nil || projection.Review.CurrentState.Goal != "edited while waiting" {
		t.Fatalf("locked plan did not merge waiting-period edit: goal=%q err=%v", projection.Review.CurrentState.Goal, err)
	}
}

func newMarkdownLockFixture(t *testing.T) (migrationServiceFixture, reviewv4.Accepted) {
	t.Helper()
	fixture := newMigrationServiceFixture(t)
	legacyManifest := seedMigrationPreparedGeneration(t, fixture)
	store, err := memorystore.Open(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, _, err := store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	manifest := legacyManifest
	manifest.GenerationID = "generation-markdown-lock"
	indexDocument := sessionindex.Document{
		SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: fixture.projectID,
		GenerationID: manifest.GenerationID, ProjectViewDigest: manifest.ProjectViewDigest,
		GeneratedAt: manifest.CreatedAt, SortVersion: sessionindex.SortVersion,
		Coverage: sessionindex.IndexCoverage{}, Sessions: []sessionindex.Entry{},
	}
	manifest.SessionIndexDigest, err = store.PutSessionIndex(indexDocument)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvancePrepared(prepared, manifest); err != nil {
		t.Fatal(err)
	}
	indexBody, err := store.LoadObject(memorystore.ObjectSessionIndex, manifest.SessionIndexDigest)
	if err != nil {
		t.Fatal(err)
	}
	read := func(relative string) []byte {
		body, readErr := os.ReadFile(filepath.Join(fixture.project, filepath.FromSlash(relative)))
		if readErr != nil {
			t.Fatal(readErr)
		}
		return body
	}
	migrated, err := migrationv4.BuildPreview(migrationv4.Input{
		Review: read(reviewv2.ReviewRelativePath), History: read(reviewv2.HistoryRelativePath), Ledger: read(reviewv2.MachineLedgerRelativePath),
		SessionIndex: indexBody, GenerationID: manifest.GenerationID, TargetPreimages: map[string]migrationv4.Preimage{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ledger := migrated.Accepted.Ledger
	ledger.MinimumReaderVersion, ledger.MinimumWriterVersion = "0.4.1", "0.4.1"
	ledger.DocumentProjection = &reviewv4.DocumentProjection{SchemaVersion: 1, Format: "review-markdown-v1", PresentationBase: migrated.Accepted.Review}
	seed, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = reviewv4.DecodeLedger(seed)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := reviewv4.RenderMarkdown(migrated.Accepted.Review, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	ledger.ReviewSHA256, ledger.HistorySHA256 = markdownTestHash(pair.Review), markdownTestHash(pair.History)
	ledger.SyncHashes.ReviewSHA256, ledger.SyncHashes.HistorySHA256 = ledger.ReviewSHA256, ledger.HistorySHA256
	ledgerBody, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := reviewv4.LoadProjection(pair.Review, pair.History, ledgerBody, indexBody)
	if err != nil {
		t.Fatal(err)
	}
	indexRelative := filepath.ToSlash(filepath.Join("docs/session-review", markdownIndexRelative))
	files := map[string][]byte{reviewv2.ReviewRelativePath: pair.Review, reviewv2.HistoryRelativePath: pair.History, reviewv2.MachineLedgerRelativePath: ledgerBody, indexRelative: indexBody}
	const vaultReviewPath = "Projects/Migration/Session Review"
	for relative, body := range files {
		for _, path := range []string{filepath.Join(fixture.project, filepath.FromSlash(relative)), filepath.Join(fixture.vault, filepath.FromSlash(filepath.Join(vaultReviewPath, strings.TrimPrefix(relative, "docs/session-review/"))))} {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	base, err := syncengine.NewMarkdownBaseRecord(pair.Review, pair.History, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	baseRoot, err := os.OpenRoot(filepath.Join(fixture.data, "projects", fixture.projectID))
	if err != nil {
		t.Fatal(err)
	}
	if err := (syncengine.BaseStore{Root: baseRoot}).Commit("", base); err != nil {
		t.Fatal(err)
	}
	_ = baseRoot.Close()
	manifestDigest, err := memory.Digest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	indexHash := markdownTestHash(indexBody)
	intent := publicationstate.Intent{
		Version: 2, Kind: publicationstate.KindMarkdown, ProjectID: fixture.projectID, GenerationID: manifest.GenerationID,
		ManifestDigest: manifestDigest, ProjectViewDigest: manifest.ProjectViewDigest, Stage: publicationstate.StageBaseCommitted, CreatedAt: time.Now().UTC(),
		IndexGuard:        &publicationstate.IndexGuard{Relative: indexRelative, VaultRelative: filepath.ToSlash(filepath.Join(vaultReviewPath, ".session-reviewer/session-index.json")), ProjectSHA256: indexHash, VaultSHA256: indexHash, Digest: manifest.SessionIndexDigest, GenerationID: manifest.GenerationID},
		BaseDesiredDigest: base.ContentHash, RequiresPointer: true,
	}
	pointerPreimage := ""
	intent.PointerPreimage = &pointerPreimage
	for relative, body := range files {
		intent.Destinations = append(intent.Destinations,
			publicationstate.Destination{Side: "project", Relative: relative, DesiredSHA256: markdownTestHash(body)},
			publicationstate.Destination{Side: "vault", Relative: filepath.ToSlash(filepath.Join(vaultReviewPath, strings.TrimPrefix(relative, "docs/session-review/"))), DesiredSHA256: markdownTestHash(body)},
		)
	}
	sort.Slice(intent.Destinations, func(i, j int) bool {
		return intent.Destinations[i].Side+"\x00"+intent.Destinations[i].Relative < intent.Destinations[j].Side+"\x00"+intent.Destinations[j].Relative
	})
	intent.RevisionID = publicationstate.MarkdownRevisionID(intent)
	journalPath := filepath.Join(fixture.data, "publication-journal", fixture.projectID)
	if err := os.MkdirAll(journalPath, 0o700); err != nil {
		t.Fatal(err)
	}
	journalRoot, err := os.OpenRoot(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publicationstate.WriteAccepted(journalRoot, intent); err != nil {
		t.Fatal(err)
	}
	intent.Stage, intent.Outcome = publicationstate.StageCommitted, publicationstate.OutcomeAccepted
	intentBody, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	intentBody = append(intentBody, '\n')
	if err := os.WriteFile(filepath.Join(journalPath, publicationstate.IntentLeaf), intentBody, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = journalRoot.Close()
	if err := store.CommitPublished(manifest.GenerationID, memory.PublicationProof{Version: 4, ProjectID: fixture.projectID, GenerationID: manifest.GenerationID, ManifestDigest: manifestDigest, ProjectViewDigest: manifest.ProjectViewDigest, ReviewSHA256: markdownTestHash(pair.Review), HistorySHA256: markdownTestHash(pair.History), LedgerSHA256: markdownTestHash(ledgerBody), SessionIndexSHA256: indexHash, JournalVerified: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return fixture, accepted
}

func markdownTestHash(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
