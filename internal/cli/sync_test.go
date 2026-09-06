package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/migrationv4"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/strictjson"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

// Removing format detection or the Markdown publisher/recoverer injection
// would route an authenticated Markdown project into the legacy v2 engine.
func TestSyncCLISelectsMarkdownServiceWithRealFormatFiles(t *testing.T) {
	originalDetect, originalMarkdown := detectSyncFormat, syncMarkdownProject
	t.Cleanup(func() { detectSyncFormat, syncMarkdownProject = originalDetect, originalMarkdown })
	fixture := newCLIFormatFixture(t, "project-p", "v4/markdown")
	called := 0
	syncMarkdownProject = func(_ context.Context, options syncproject.Options) (syncengine.Report, error) {
		called++
		if options.PublishMarkdown == nil || options.RecoverMarkdown == nil {
			t.Fatal("CLI omitted real Markdown publication callbacks")
		}
		return syncengine.Report{ProjectID: fixture.projectID, DryRun: true}, nil
	}
	var stdout, stderr bytes.Buffer
	report, err := defaultSyncProject(t.Context(), syncproject.Options{ProjectID: fixture.projectID, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI, DryRun: true})
	code := 0
	if err != nil {
		code = 1
	}
	if code != 0 || called != 1 || stderr.Len() != 0 {
		t.Fatalf("code=%d markdown_calls=%d report=%+v stdout=%q stderr=%q", code, called, report, stdout.String(), stderr.String())
	}
}

// Each supported on-disk generation must reach its own service boundary. In
// particular, a present v2/v3 ledger is not malformed v4, and old JSON v4 is
// not an editable Markdown projection merely because both use .md paths.
func TestSyncCLIClassifiesEachRealProjectionFormat(t *testing.T) {
	for _, test := range []struct {
		name    string
		fixture func(*testing.T) cliSyncFixture
		want    syncproject.ProjectionFormat
	}{
		{name: "legacy", fixture: newCLILegacySyncFixture, want: syncproject.ProjectionLegacy},
		{name: "v2", fixture: newCLISyncFixture, want: syncproject.ProjectionV2},
		{name: "v3", fixture: newCLIV3FormatFixture, want: syncproject.ProjectionV3},
		{name: "old v4 JSON", fixture: newCLIOldV4Fixture, want: syncproject.ProjectionJSONV4},
		{name: "Markdown", fixture: newCLIAuthenticatedMarkdownFixture, want: syncproject.ProjectionMarkdown},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := test.fixture(t)
			format, err := syncproject.DetectFormat(t.Context(), syncproject.Options{ProjectID: fixture.projectID, DataDir: fixture.data, GOOS: runtime.GOOS, Trigger: syncengine.TriggerCLI})
			if err != nil || format != test.want {
				t.Fatalf("format=%q want=%q err=%v", format, test.want, err)
			}
		})
	}
}

// This is the actual CLI-to-service-to-publication path. Replacing it with a
// route fake would miss draft detection, receipt/Base authentication, and the
// Project/Vault transaction.
func TestSyncCLIExecutesAuthenticatedMarkdownPublication(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	reviewPath := filepath.Join(fixture.project, filepath.FromSlash(reviewv2.ReviewRelativePath))
	vaultReviewPath := filepath.Join(fixture.vault, "Projects", "Markdown", "Session Review", "项目回顾.md")
	beforeIndex, err := os.ReadFile(filepath.Join(fixture.project, "docs", "session-review", ".session-reviewer", "session-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	review, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	review = bytes.Replace(review, []byte("authenticated Markdown migration"), []byte("human CLI edit"), 1)
	if err := os.WriteFile(reviewPath, review, 0o600); err != nil {
		t.Fatal(err)
	}
	format, err := syncproject.DetectFormat(t.Context(), syncproject.Options{ProjectID: fixture.projectID, DataDir: fixture.data})
	if err != nil || format != syncproject.ProjectionMarkdown {
		t.Fatalf("edited Markdown format=%q err=%v", format, err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"sync", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	projectReview, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	vaultReview, err := os.ReadFile(vaultReviewPath)
	if err != nil || !bytes.Equal(projectReview, vaultReview) || !bytes.Contains(projectReview, []byte("human CLI edit")) {
		t.Fatalf("Project/Vault Markdown publication mismatch: err=%v", err)
	}
	afterIndex, err := os.ReadFile(filepath.Join(fixture.project, "docs", "session-review", ".session-reviewer", "session-index.json"))
	if err != nil || !bytes.Equal(beforeIndex, afterIndex) {
		t.Fatalf("human CLI edit changed index: err=%v", err)
	}
}

// --dry-run --json is a presentation choice, not permission to send an
// already-authenticated Markdown project through the legacy migration builder.
func TestSyncCLIJSONDryRunRoutesAuthenticatedMarkdownToReadOnlySync(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	before := snapshotCLITree(t, filepath.Dir(fixture.data))
	var stdout, stderr bytes.Buffer
	code := Run([]string{"sync", "--dry-run", "--json", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var report syncengine.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || !report.DryRun || report.ProjectID != fixture.projectID {
		t.Fatalf("report=%+v err=%v raw=%q", report, err, stdout.String())
	}
	if after := snapshotCLITree(t, filepath.Dir(fixture.data)); after != before {
		t.Fatal("Markdown JSON dry-run mutated fixture trees")
	}
}

func TestSyncCLIMarkdownLegacyWriteModesFailClosed(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	for _, args := range [][]string{
		{"sync", "resolve", "--conflict", "missing-conflict", "--action", "accept_project", "--project-id", fixture.projectID, "--data-dir", fixture.data},
		{"sync", "repair-machine-ledger", "--project-id", fixture.projectID, "--data-dir", fixture.data},
	} {
		before := snapshotCLITree(t, filepath.Dir(fixture.data))
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code == 0 || snapshotCLITree(t, filepath.Dir(fixture.data)) != before {
			t.Fatalf("legacy mode did not fail closed: args=%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

// This executes the public command contract all the way through the dedicated
// legacy-to-Markdown publisher. A mocked migration service would not prove the
// Project/Vault atom, accepted receipt, merge Base, or post-migration routing.
func TestSyncCLIOldV4DryRunThenConfirmPublishesAuthenticatedMarkdown(t *testing.T) {
	fixture := newCLIOldV4Fixture(t)
	fixtureRoot := filepath.Dir(fixture.data)
	before := snapshotCLITree(t, fixtureRoot)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"sync", "--dry-run", "--json", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("dry-run code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var dryRun syncproject.MigrationResult
	if err := json.Unmarshal(stdout.Bytes(), &dryRun); err != nil || dryRun.Applied || dryRun.Preview.SourceFormat != migrationv4.FormatJSONV4 || dryRun.Preview.TargetFormat != migrationv4.FormatMarkdownV1 || dryRun.Preview.PreviewDigest == "" {
		t.Fatalf("dry-run=%+v err=%v raw=%q", dryRun, err, stdout.String())
	}
	if after := snapshotCLITree(t, fixtureRoot); after != before {
		t.Fatal("old-v4 migration dry-run mutated fixture trees")
	}

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"sync", "--confirm-migration", "--expected-preview-digest", dryRun.Preview.PreviewDigest, "--json", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("confirm code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var confirmed syncproject.MigrationResult
	if err := json.Unmarshal(stdout.Bytes(), &confirmed); err != nil || !confirmed.Applied || confirmed.Preview.PreviewDigest != dryRun.Preview.PreviewDigest {
		t.Fatalf("confirmed=%+v err=%v raw=%q", confirmed, err, stdout.String())
	}
	format, err := syncproject.DetectFormat(t.Context(), syncproject.Options{ProjectID: fixture.projectID, DataDir: fixture.data})
	if err != nil || format != syncproject.ProjectionMarkdown {
		t.Fatalf("confirmed format=%q err=%v", format, err)
	}

	readAccepted := func(root, prefix string) reviewv4.Accepted {
		t.Helper()
		read := func(relative string) []byte {
			path := filepath.Join(root, filepath.FromSlash(filepath.Join(prefix, strings.TrimPrefix(relative, "docs/session-review/"))))
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			return body
		}
		accepted, loadErr := reviewv4.LoadProjection(read(migrationv4.ReviewRelativePath), read(migrationv4.HistoryRelativePath), read(migrationv4.LedgerRelativePath), read(migrationv4.SessionIndexRelativePath))
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		return accepted
	}
	projectAccepted := readAccepted(fixture.project, "docs/session-review")
	vaultAccepted := readAccepted(fixture.vault, "Projects/Markdown/Session Review")
	if projectAccepted.Ledger.DocumentProjection == nil || vaultAccepted.Ledger.DocumentProjection == nil || projectAccepted.Ledger.AcceptedRevision != vaultAccepted.Ledger.AcceptedRevision {
		t.Fatalf("confirmed projections are not the same accepted Markdown revision")
	}
	state, err := publicationstate.OpenReadOnly(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, receiptErr := state.Accepted()
	if closeErr := state.Close(); receiptErr != nil || closeErr != nil {
		t.Fatalf("accepted receipt err=%v close=%v", receiptErr, closeErr)
	}
	projectData, err := os.OpenRoot(filepath.Join(fixture.data, "projects", fixture.projectID))
	if err != nil {
		t.Fatal(err)
	}
	base, found, baseErr := (syncengine.BaseStore{Root: projectData}).Load(syncengine.MarkdownBaseEntityID)
	if closeErr := projectData.Close(); baseErr != nil || closeErr != nil || !found || receipt.BaseDigest != base.ContentHash {
		t.Fatalf("accepted Base found=%t receipt=%q base=%q err=%v close=%v", found, receipt.BaseDigest, base.ContentHash, baseErr, closeErr)
	}

	beforeNoop := snapshotCLITree(t, fixtureRoot)
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"sync", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || snapshotCLITree(t, fixtureRoot) != beforeNoop {
		t.Fatalf("post-migration no-op code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

// Removing the pre-format recovery probe would strand a migration that crashed
// after only the first JSON-to-Markdown destination: the mixed public set is
// intentionally not classifiable as either accepted format.
func TestSyncCLIEarlyMigrationCrashRequiresReadOnlyDryRunAndRecoversBeforeFormatDetection(t *testing.T) {
	fixture := newCLIOldV4Fixture(t)
	args := []string{"sync", "--dry-run", "--json", "--project-id", fixture.projectID, "--data-dir", fixture.data}
	var stdout, stderr bytes.Buffer
	if code := Run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("initial dry-run code=%d stderr=%q", code, stderr.String())
	}
	var preview syncproject.MigrationResult
	if err := json.Unmarshal(stdout.Bytes(), &preview); err != nil || preview.Preview.PreviewDigest == "" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}

	originalMigration := syncMigrationProject
	t.Cleanup(func() { syncMigrationProject = originalMigration })
	failure := errors.New("simulated early CLI migration crash")
	mapping := config.ProjectMapping{ID: fixture.projectID, Root: fixture.project, VaultRoot: fixture.vault, VaultReviewPath: "Projects/Markdown/Session Review", VaultCaseMode: platform.CaseSensitive}
	syncMigrationProject = func(ctx context.Context, options syncproject.MigrationOptions) (syncproject.MigrationResult, error) {
		options.Recover = func(ctx context.Context, mapping config.ProjectMapping, dataRoot string, owner *publicationlock.Owner) error {
			return publication.RecoverMarkdownLocked(ctx, publication.Options{ProjectID: mapping.ID, Mapping: mapping, DataRoot: dataRoot, Now: options.Now}, owner)
		}
		options.Publish = func(ctx context.Context, plan syncproject.MigrationPublication) error {
			_, err := publication.PublishMarkdownMigrationLocked(ctx, publication.Options{
				ProjectID: plan.ProjectID, Mapping: plan.Mapping, DataRoot: plan.DataRoot, Now: options.Now,
				AfterDestination: func(side, relative string) error {
					if side == "project" && relative == reviewv2.ReviewRelativePath {
						panic(failure)
					}
					return nil
				},
			}, plan)
			return err
		}
		return syncproject.RunMigration(ctx, options)
	}
	confirmArgs := []string{"sync", "--confirm-migration", "--expected-preview-digest", preview.Preview.PreviewDigest, "--json", "--project-id", fixture.projectID, "--data-dir", fixture.data}
	func() {
		defer func() {
			if recovered := recover(); !errors.Is(recovered.(error), failure) {
				t.Fatalf("migration panic=%v", recovered)
			}
		}()
		_ = Run(confirmArgs, &stdout, &stderr)
	}()
	syncMigrationProject = originalMigration
	state, err := publicationstate.OpenReadOnly(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	intent, intentErr := state.Intent()
	if closeErr := state.Close(); intentErr != nil || closeErr != nil || intent.Stage == publicationstate.StageCommitted {
		t.Fatalf("active intent=%+v intentErr=%v closeErr=%v", intent, intentErr, closeErr)
	}
	projectReview, err := os.ReadFile(filepath.Join(fixture.project, filepath.FromSlash(reviewv2.ReviewRelativePath)))
	if err != nil || !bytes.HasPrefix(projectReview, []byte("---\n")) {
		t.Fatalf("first Project destination was not Markdown: err=%v", err)
	}
	vaultReview, err := os.ReadFile(filepath.Join(fixture.vault, filepath.FromSlash(filepath.Join(mapping.VaultReviewPath, strings.TrimPrefix(reviewv2.ReviewRelativePath, "docs/session-review/")))))
	if err != nil || !bytes.HasPrefix(vaultReview, []byte("{")) {
		t.Fatalf("Vault changed before crash recovery: err=%v", err)
	}

	beforeDryRun := snapshotCLITree(t, filepath.Dir(fixture.data))
	stdout.Reset()
	stderr.Reset()
	if code := Run(args, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "migration_recovery_required") {
		t.Fatalf("interrupted dry-run code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if after := snapshotCLITree(t, filepath.Dir(fixture.data)); after != beforeDryRun {
		t.Fatal("interrupted dry-run performed recovery or another write")
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"sync", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "migration_required") {
		t.Fatalf("ordinary recovery route code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	state, err = publicationstate.OpenReadOnly(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	intent, intentErr = state.Intent()
	if closeErr := state.Close(); intentErr != nil || closeErr != nil || intent.Stage != publicationstate.StageCommitted || intent.Outcome != publicationstate.OutcomeRolledBack {
		t.Fatalf("recovered intent=%+v intentErr=%v closeErr=%v", intent, intentErr, closeErr)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(confirmArgs, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("retry confirm code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	format, err := syncproject.DetectFormat(t.Context(), syncproject.Options{ProjectID: fixture.projectID, DataDir: fixture.data})
	if err != nil || format != syncproject.ProjectionMarkdown {
		t.Fatalf("recovered format=%q err=%v", format, err)
	}
}

// A crash after the first Project destination can leave a mixed public set.
// CLI restart must authenticate the active Markdown intent and reach recovery
// before trying to classify the four current bytes as one accepted format.
func TestSyncCLIRestartRecoversInterruptedMarkdownEditBeforeFormatDetection(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	reviewPath := filepath.Join(fixture.project, filepath.FromSlash(reviewv2.ReviewRelativePath))
	review, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	review = bytes.Replace(review, []byte("authenticated Markdown migration"), []byte("recoverable CLI edit"), 1)
	if err := os.WriteFile(reviewPath, review, 0o600); err != nil {
		t.Fatal(err)
	}
	var plan syncproject.MarkdownSyncPlan
	options := syncproject.Options{ProjectID: fixture.projectID, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI,
		RecoverMarkdown: func(context.Context, *publicationlock.Owner) error { return nil },
		PublishMarkdown: func(_ context.Context, next syncproject.MarkdownSyncPlan, _ *publicationlock.Owner) error {
			plan = next
			return nil
		},
	}
	if _, err := syncproject.RunMarkdown(t.Context(), options); err != nil || len(plan.Plan.Files) == 0 {
		t.Fatalf("capture edit plan files=%d err=%v", len(plan.Plan.Files), err)
	}
	mapping := config.ProjectMapping{ID: fixture.projectID, Root: fixture.project, VaultRoot: fixture.vault, VaultReviewPath: "Projects/Markdown/Session Review", VaultCaseMode: platform.CaseSensitive}
	vaultLedgerPath := filepath.Join(fixture.vault, filepath.FromSlash(filepath.Join(mapping.VaultReviewPath, strings.TrimPrefix(reviewv2.MachineLedgerRelativePath, "docs/session-review/"))))
	vaultLedgerBefore, err := os.ReadFile(vaultLedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	var desiredLedger []byte
	for _, file := range plan.Plan.Files {
		if file.Relative == reviewv2.MachineLedgerRelativePath {
			desiredLedger = bytes.Clone(file.Desired)
		}
	}
	if len(desiredLedger) == 0 {
		t.Fatal("captured edit plan has no ledger destination")
	}
	owner, err := publicationlock.Acquire(fixture.data, fixture.projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("simulated CLI process crash")
	func() {
		defer func() {
			if recovered := recover(); !errors.Is(recovered.(error), failure) {
				t.Fatalf("publication panic=%v", recovered)
			}
		}()
		_, _ = publication.PublishMarkdownEditLocked(t.Context(), publication.Options{
			ProjectID: fixture.projectID, Mapping: mapping, DataRoot: fixture.data, Now: time.Now,
			AfterDestination: func(side, relative string) error {
				if side == "project" && relative == reviewv2.MachineLedgerRelativePath {
					panic(failure)
				}
				return nil
			},
		}, plan, owner)
	}()
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	state, err := publicationstate.OpenReadOnly(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	intent, intentErr := state.Intent()
	if closeErr := state.Close(); intentErr != nil || closeErr != nil || intent.Stage == publicationstate.StageCommitted {
		t.Fatalf("active intent stage=%q intentErr=%v closeErr=%v", intent.Stage, intentErr, closeErr)
	}
	projectLedger, err := os.ReadFile(filepath.Join(fixture.project, filepath.FromSlash(reviewv2.MachineLedgerRelativePath)))
	if err != nil || !bytes.Equal(projectLedger, desiredLedger) {
		t.Fatalf("Project ledger did not reach next bytes: err=%v", err)
	}
	vaultLedger, err := os.ReadFile(vaultLedgerPath)
	if err != nil || !bytes.Equal(vaultLedger, vaultLedgerBefore) {
		t.Fatalf("Vault ledger changed before crash recovery: err=%v", err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"sync", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("restart code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	projectReview, err := os.ReadFile(reviewPath)
	if err != nil || !bytes.Contains(projectReview, []byte("recoverable CLI edit")) {
		t.Fatalf("recovered edit missing err=%v", err)
	}
	loadProjection := func(root, prefix string) reviewv4.Accepted {
		t.Helper()
		read := func(relative string) []byte {
			path := filepath.Join(root, filepath.FromSlash(filepath.Join(prefix, strings.TrimPrefix(relative, "docs/session-review/"))))
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			return body
		}
		accepted, loadErr := reviewv4.LoadProjection(
			read(reviewv2.ReviewRelativePath), read(reviewv2.HistoryRelativePath),
			read(reviewv2.MachineLedgerRelativePath), read("docs/session-review/.session-reviewer/session-index.json"),
		)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		return accepted
	}
	projectAccepted := loadProjection(fixture.project, "docs/session-review")
	vaultAccepted := loadProjection(fixture.vault, mapping.VaultReviewPath)
	if projectAccepted.Review.GenerationID != vaultAccepted.Review.GenerationID || projectAccepted.Ledger.AcceptedRevision != vaultAccepted.Ledger.AcceptedRevision || projectAccepted.Ledger.SyncHashes != vaultAccepted.Ledger.SyncHashes {
		t.Fatalf("recovered projections differ: project=%q vault=%q", projectAccepted.Review.GenerationID, vaultAccepted.Review.GenerationID)
	}
	state, err = publicationstate.OpenReadOnly(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, receiptErr := state.Accepted()
	if closeErr := state.Close(); receiptErr != nil || closeErr != nil {
		t.Fatalf("accepted receipt err=%v close=%v", receiptErr, closeErr)
	}
	projectData, err := os.OpenRoot(filepath.Join(fixture.data, "projects", fixture.projectID))
	if err != nil {
		t.Fatal(err)
	}
	base, found, baseErr := (syncengine.BaseStore{Root: projectData}).Load(syncengine.MarkdownBaseEntityID)
	if closeErr := projectData.Close(); baseErr != nil || closeErr != nil || !found || receipt.BaseDigest != base.ContentHash {
		t.Fatalf("accepted Base found=%t receipt=%q base=%q err=%v close=%v", found, receipt.BaseDigest, base.ContentHash, baseErr, closeErr)
	}
	beforeNoop := snapshotCLITree(t, filepath.Dir(fixture.data))
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"sync", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || snapshotCLITree(t, filepath.Dir(fixture.data)) != beforeNoop {
		t.Fatalf("authenticated no-op code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

// Reconstructing the engine in the command, resolving the platform data root
// inside the service, or moving report formatting out of the CLI breaks this
// boundary test even when the underlying engine still works.
func TestSyncProjectServiceCLIDelegationPreservesFormatting(t *testing.T) {
	original := syncProject
	t.Cleanup(func() { syncProject = original })
	dataRoot := t.TempDir()
	wantReport := syncengine.Report{
		ProjectID: "project-1111111111111111",
		DryRun:    true,
		Operations: []syncengine.Operation{{
			EntityID: "project-overview", Kind: syncengine.OperationAddVault, RelativePath: "项目回顾.md",
		}},
		Derived:   syncengine.DerivedReport{State: syncengine.DerivedCurrent, Operations: []syncengine.Operation{}},
		Migration: syncengine.MigrationReport{DryRun: true, Creates: []string{}, Archives: []string{}},
		Machine:   syncengine.MachineReport{State: syncengine.MachineCurrent, Operations: []syncengine.Operation{}},
	}
	var got syncproject.Options
	syncProject = func(ctx context.Context, options syncproject.Options) (syncengine.Report, error) {
		if ctx == nil {
			t.Fatal("CLI passed a nil sync context")
		}
		got = options
		return wantReport, nil
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"sync", "--dry-run", "--project-id", wantReport.ProjectID, "--data-dir", dataRoot}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if got.ProjectID != wantReport.ProjectID || got.CWD != "" || got.DataDir != dataRoot || got.GOOS != runtime.GOOS || got.Now == nil || got.Trigger != syncengine.TriggerCLI || !got.DryRun {
		t.Fatalf("delegated options = %#v", got)
	}
	var expected bytes.Buffer
	writeSyncReport(&expected, wantReport)
	if stdout.String() != expected.String() {
		t.Fatalf("stdout=%q want=%q", stdout.String(), expected.String())
	}
}

func TestRunSyncMigrationModesUseInjectableServiceAndJSON(t *testing.T) {
	originalMigration := syncMigrationProject
	originalSync := syncProject
	originalDetect := detectSyncFormat
	originalRecover := recoverSyncBeforeFormat
	t.Cleanup(func() {
		syncMigrationProject, syncProject, detectSyncFormat, recoverSyncBeforeFormat = originalMigration, originalSync, originalDetect, originalRecover
	})
	recoverSyncBeforeFormat = func(context.Context, syncproject.Options) (bool, error) { return false, nil }
	detectSyncFormat = func(context.Context, syncproject.Options) (syncproject.ProjectionFormat, error) {
		return syncproject.ProjectionV3, nil
	}
	syncProject = func(context.Context, syncproject.Options) (syncengine.Report, error) {
		t.Fatal("explicit migration mode reached ordinary sync")
		return syncengine.Report{}, nil
	}
	dataRoot := t.TempDir()
	digest := "sha256:" + strings.Repeat("a", 64)
	calls := 0
	syncMigrationProject = func(ctx context.Context, options syncproject.MigrationOptions) (syncproject.MigrationResult, error) {
		calls++
		if ctx == nil || options.ProjectID != "project-p" || options.DataDir != dataRoot || options.GOOS != runtime.GOOS || options.Now == nil || options.Trigger != syncengine.TriggerCLI {
			t.Fatalf("migration options = %+v", options.Options)
		}
		if calls == 1 && (options.Mode != syncproject.MigrationDryRun || options.ExpectedPreviewDigest != "") {
			t.Fatalf("dry-run options = %+v", options)
		}
		if calls == 2 && (options.Mode != syncproject.MigrationConfirm || options.ExpectedPreviewDigest != digest) {
			t.Fatalf("confirm options = %+v", options)
		}
		return syncproject.MigrationResult{Preview: migrationv4.MigrationPreview{SchemaVersion: 1, PreviewDigest: digest}, Applied: calls == 2}, nil
	}
	for _, args := range [][]string{
		{"sync", "--dry-run", "--project-id", "project-p", "--data-dir", dataRoot, "--json"},
		{"sync", "--confirm-migration", "--expected-preview-digest", digest, "--project-id", "project-p", "--data-dir", dataRoot, "--json"},
	} {
		var out, errOut bytes.Buffer
		if code := Run(args, &out, &errOut); code != 0 || errOut.Len() != 0 {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, out.String(), errOut.String())
		}
		var result syncproject.MigrationResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Preview.PreviewDigest != digest {
			t.Fatalf("args=%v result=%+v err=%v", args, result, err)
		}
	}
}

func TestRunSyncMigrationStaleIsOneStableJSONObject(t *testing.T) {
	original := syncMigrationProject
	originalDetect := detectSyncFormat
	originalRecover := recoverSyncBeforeFormat
	t.Cleanup(func() {
		syncMigrationProject, detectSyncFormat, recoverSyncBeforeFormat = original, originalDetect, originalRecover
	})
	recoverSyncBeforeFormat = func(context.Context, syncproject.Options) (bool, error) { return false, nil }
	detectSyncFormat = func(context.Context, syncproject.Options) (syncproject.ProjectionFormat, error) {
		return syncproject.ProjectionV3, nil
	}
	syncMigrationProject = func(context.Context, syncproject.MigrationOptions) (syncproject.MigrationResult, error) {
		return syncproject.MigrationResult{}, syncproject.ErrMigrationPreviewStale
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "--confirm-migration", "--expected-preview-digest", digest, "--data-dir", t.TempDir(), "--json"}, &out, &errOut)
	if code != 1 || out.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var diagnostic map[string]string
	if err := json.Unmarshal(errOut.Bytes(), &diagnostic); err != nil || diagnostic["code"] != ContractCodeMigrationPreviewStale {
		t.Fatalf("diagnostic=%v err=%v raw=%q", diagnostic, err, errOut.String())
	}
}

func TestRunSyncPlainV3ReturnsMigrationRequiredWithoutDispatchingMigration(t *testing.T) {
	originalSync := syncProject
	originalMigration := syncMigrationProject
	t.Cleanup(func() { syncProject, syncMigrationProject = originalSync, originalMigration })
	calls := 0
	syncProject = func(context.Context, syncproject.Options) (syncengine.Report, error) {
		calls++
		return syncengine.Report{}, syncproject.ErrMigrationRequired
	}
	syncMigrationProject = func(context.Context, syncproject.MigrationOptions) (syncproject.MigrationResult, error) {
		t.Fatal("plain sync implicitly dispatched migration")
		return syncproject.MigrationResult{}, nil
	}
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "--data-dir", t.TempDir()}, &out, &errOut)
	if code != 1 || calls != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "migration_required") {
		t.Fatalf("code=%d calls=%d stdout=%q stderr=%q", code, calls, out.String(), errOut.String())
	}
}

// Mapping and CWD authentication belong to the extracted service. Resolving
// CWD in the CLI first would change legacy error ordering and bypass the seam.
func TestSyncProjectServiceCLILeavesCWDResolutionToService(t *testing.T) {
	original := syncProject
	t.Cleanup(func() { syncProject = original })
	dataRoot := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "missing")
	calls := 0
	var got syncproject.Options
	syncProject = func(_ context.Context, options syncproject.Options) (syncengine.Report, error) {
		calls++
		got = options
		return syncengine.Report{}, errors.New("fixture service failure")
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"sync", "--cwd", cwd, "--data-dir", dataRoot}, &stdout, &stderr)
	if code == 0 || calls != 1 || got.CWD != cwd || got.DataDir != dataRoot || stdout.Len() != 0 || !strings.Contains(stderr.String(), "E_SYNC_FAILED") {
		t.Fatalf("code=%d calls=%d options=%#v stdout=%q stderr=%q", code, calls, got, stdout.String(), stderr.String())
	}
}

func TestRunSyncDryRunAndApplyExposeEditableVaultCopy(t *testing.T) {
	fixture := newCLISyncFixture(t)
	reviewRoot := filepath.Join(fixture.vault, "Projects", "CLI--11111111", "Session Review")

	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "--dry-run", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "add_vault project-overview") {
		t.Fatalf("dry-run code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(reviewRoot); !os.IsNotExist(err) {
		t.Fatalf("dry-run created vault review root: %v", err)
	}

	out.Reset()
	errOut.Reset()
	code = Run([]string{"sync", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "add_vault project-overview") || !strings.Contains(out.String(), "derived=current files=0") {
		t.Fatalf("sync code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if body, err := os.ReadFile(filepath.Join(reviewRoot, "项目回顾.md")); err != nil || !strings.Contains(string(body), "# SessionReviewer v2") {
		t.Fatalf("vault body=%q err=%v", body, err)
	}
}

func TestSyncOutputReportsDerivedStateAndRelativePaths(t *testing.T) {
	fixture := newCLISyncFixture(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "--dry-run", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("dry-run code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, want := range []string{
		"derived=current files=0",
		"add_vault project-history 项目历史.md",
		"add_vault project-overview 项目回顾.md",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), fixture.project) || strings.Contains(out.String(), fixture.vault) {
		t.Fatalf("derived output leaked an absolute path: %q", out.String())
	}
}

func TestRunSyncStatusJSONAndResolveGrammar(t *testing.T) {
	fixture := newCLISyncFixture(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "status", "--json", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("status code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var status map[string]any
	if err := json.Unmarshal(out.Bytes(), &status); err != nil || status["project_id"] != fixture.projectID || status["derived_state"] != "current" || status["derived_files"] != float64(0) {
		t.Fatalf("status=%v err=%v", status, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.vault, "Projects", "CLI--11111111", "Session Review")); !os.IsNotExist(err) {
		t.Fatalf("status mutated the vault: %v", err)
	}

	for _, action := range []string{"", "project", "delete", "ACCEPT_PROJECT"} {
		out.Reset()
		errOut.Reset()
		args := []string{"sync", "resolve", "--conflict", "conflict-project-overview", "--action", action, "--cwd", fixture.project, "--data-dir", fixture.data}
		if code := Run(args, &out, &errOut); code != 2 || out.Len() != 0 {
			t.Fatalf("action=%q code=%d stdout=%q stderr=%q", action, code, out.String(), errOut.String())
		}
	}
}

func TestRunSyncProjectIDSelectsPinnedMappingAndRejectsCWDCombination(t *testing.T) {
	fixture := newCLISyncFixture(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "--dry-run", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "migration=current") {
		t.Fatalf("project-id code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code = Run([]string{"sync", "--dry-run", "--project-id", fixture.projectID, "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "mutually exclusive") {
		t.Fatalf("combined code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunSyncStatusJSONIncludesV2MachineMigrationAndConflictFields(t *testing.T) {
	fixture := newCLISyncFixture(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "status", "--json", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var status map[string]any
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"migration", "machine_state", "last_successful_sync", "pending_operations", "hidden_conflict_ids"} {
		if _, exists := status[key]; !exists {
			t.Fatalf("status missing %q: %v", key, status)
		}
	}
}

func TestRunSyncPlainStatusIncludesV2MigrationAndMachineState(t *testing.T) {
	fixture := newCLISyncFixture(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "status", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, want := range []string{"migration=current", "machine=pending", "pending_operations=", "hidden_conflicts="} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("plain status missing %q: %q", want, out.String())
		}
	}
}

func TestRunSyncRepairMachineLedgerHasNoArbitraryPathAndRestoresProjectBytes(t *testing.T) {
	fixture := newCLISyncFixture(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"sync", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut); code != 0 {
		t.Fatalf("initial sync code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	projectPath := filepath.Join(fixture.project, filepath.FromSlash(reviewv2.MachineLedgerRelativePath))
	vaultPath := filepath.Join(fixture.vault, "Projects", "CLI--11111111", "Session Review", ".session-reviewer", "ledger.json")
	want, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultPath, []byte("tampered machine ledger"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"sync", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut); code == 0 || !strings.Contains(out.String(), "machine_ledger_modified") {
		t.Fatalf("tamper code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code := Run([]string{"sync", "repair-machine-ledger", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "machine=current files=1") {
		t.Fatalf("repair code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	got, err := os.ReadFile(vaultPath)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("vault machine was not repaired err=%v", err)
	}
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"sync", "repair-machine-ledger", "unexpected", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut); code != 2 {
		t.Fatalf("arbitrary path code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunSyncReportsMigrationAndPublishesReviewV2(t *testing.T) {
	fixture := newCLILegacySyncFixture(t)
	projectInfo, err := os.Stat(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviewv2.PlanMigration(fixture.project, projectInfo, filepath.Join(fixture.data, "projects", fixture.projectID), time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("legacy fixture cannot be migrated: %v", err)
	}
	projectBefore := snapshotCLITree(t, fixture.project)
	dataBefore := snapshotCLITree(t, fixture.data)
	vaultBefore := snapshotCLITree(t, fixture.vault)
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "--dry-run", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "migration=required") {
		t.Fatalf("dry-run code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, want := range []string{
		"migration_create docs/session-review/.session-reviewer/ledger.json",
		"migration_create docs/session-review/项目历史.md",
		"migration_create docs/session-review/项目回顾.md",
		"migration_archive docs/session-review/current-state.md",
		"migration_archive docs/session-review/project-overview.md",
	} {
		if !strings.Contains(out.String(), want+"\n") {
			t.Fatalf("migration preview missing %q in %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), fixture.project) || strings.Contains(out.String(), fixture.vault) {
		t.Fatalf("migration preview leaked absolute path: %q", out.String())
	}
	if snapshotCLITree(t, fixture.project) != projectBefore || snapshotCLITree(t, fixture.data) != dataBefore || snapshotCLITree(t, fixture.vault) != vaultBefore {
		t.Fatal("migration dry-run wrote to project, data, or vault")
	}
	out.Reset()
	errOut.Reset()
	code = Run([]string{"sync", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "migration=current") || !strings.Contains(out.String(), "machine=current files=1") {
		t.Fatalf("sync code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if _, err := reviewv2.Load(fixture.project); err != nil {
		t.Fatalf("load migrated v2: %v", err)
	}
	backupRoot := filepath.Join(fixture.project, "docs", "session-review", ".session-reviewer", "backups")
	entries, err := os.ReadDir(backupRoot)
	directories := 0
	for _, entry := range entries {
		if entry.IsDir() {
			directories++
		}
	}
	if err != nil || directories != 1 {
		t.Fatalf("backup entries=%v err=%v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.vault, "Projects", "Legacy--269b8cab", "Session Review", ".session-reviewer", "backups")); !os.IsNotExist(err) {
		t.Fatalf("migration backup escaped to vault: %v", err)
	}
}

func TestRunSyncResolveUsesPersistedHiddenConflictIdentity(t *testing.T) {
	fixture := newCLISyncFixture(t)
	reviewRoot := filepath.Join(fixture.vault, "Projects", "CLI--11111111", "Session Review")
	var out, errOut bytes.Buffer
	if code := Run([]string{"sync", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut); code != 0 {
		t.Fatalf("initial sync code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	projectPath := filepath.Join(fixture.project, "docs", "session-review", "项目历史.md")
	vaultPath := filepath.Join(reviewRoot, "项目历史.md")
	projectBody, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	vaultBody, err := os.ReadFile(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	projectBody = bytes.Replace(projectBody, []byte("信任链与 dry-run 边界修复"), []byte("Project choice"), 1)
	vaultBody = bytes.Replace(vaultBody, []byte("信任链与 dry-run 边界修复"), []byte("Vault choice"), 1)
	if err := os.WriteFile(projectPath, projectBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultPath, vaultBody, 0o644); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errOut.Reset()
	if code := Run([]string{"sync", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "conflicts: 1") {
		t.Fatalf("conflict code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"sync", "status", "--json", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut); code != 0 {
		t.Fatalf("status code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var status struct {
		OpenConflicts []string `json:"open_conflicts"`
	}
	if err := json.Unmarshal(out.Bytes(), &status); err != nil || len(status.OpenConflicts) != 1 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	out.Reset()
	errOut.Reset()
	code := Run([]string{"sync", "resolve", "--conflict", status.OpenConflicts[0], "--action", "accept_project", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "derived=current files=0") {
		t.Fatalf("resolve code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	projectBody, err = os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	vaultBody, err = os.ReadFile(vaultPath)
	if err != nil || !bytes.Equal(projectBody, vaultBody) || !bytes.Contains(projectBody, []byte("Project choice")) || bytes.Contains(projectBody, []byte("Vault choice")) {
		t.Fatalf("resolved project=%q vault=%q err=%v", projectBody, vaultBody, err)
	}
}

func TestRunSyncReturnsFailureAndSafeEntityDiagnosticsForPartialErrors(t *testing.T) {
	fixture := newCLISyncFixture(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"sync", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut); code != 0 {
		t.Fatalf("initial code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	projectPath := filepath.Join(fixture.project, "docs", "session-review", "项目历史.md")
	if err := os.WriteFile(projectPath, []byte("malformed-secret-canary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	code := Run([]string{"sync", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code == 0 || !strings.Contains(out.String(), "entity_error project-history malformed_source") || !strings.Contains(out.String(), "derived=deferred files=0") || !strings.Contains(errOut.String(), "E_SYNC_PARTIAL") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "malformed-secret-canary") || strings.Contains(errOut.String(), "malformed-secret-canary") {
		t.Fatalf("diagnostic leaked content: stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestWriteSyncReportReportsFailedDerivedWithoutSensitiveContent(t *testing.T) {
	var out bytes.Buffer
	writeSyncReport(&out, syncengine.Report{
		ProjectID: "project-1111111111111111",
		Derived: syncengine.DerivedReport{
			State: syncengine.DerivedFailed,
			Files: 7,
			Operations: []syncengine.Operation{{
				Kind: syncengine.OperationUpdateVault, RelativePath: "decisions/00-目录说明.md",
			}},
		},
	})
	for _, want := range []string{"derived_operation update_vault decisions/00-目录说明.md", "derived=failed files=7"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), "project-1111111111111111") {
		t.Fatalf("content-free derived report exposed project identity: %q", out.String())
	}
}

func TestFailedSyncReportRequiresMaterialProgress(t *testing.T) {
	defaults := syncengine.Report{
		Derived:   syncengine.DerivedReport{State: syncengine.DerivedDeferred, Operations: []syncengine.Operation{}},
		Migration: syncengine.MigrationReport{Creates: []string{}, Archives: []string{}},
		Machine:   syncengine.MachineReport{State: syncengine.MachinePending, Operations: []syncengine.Operation{}},
	}
	if shouldWriteFailedSyncReport(defaults) {
		t.Fatal("default report would misleadingly claim current migration after an early failure")
	}
	defaults.Migration.Required = true
	defaults.Migration.Creates = []string{"docs/session-review/项目回顾.md"}
	if !shouldWriteFailedSyncReport(defaults) {
		t.Fatal("material migration progress was hidden")
	}
}

func TestRunSyncReturnsFailureForUnassociatedMalformedDocument(t *testing.T) {
	fixture := newCLISyncFixture(t)
	bad := filepath.Join(fixture.project, "docs", "session-review", "unparseable.md")
	if err := os.WriteFile(bad, []byte("private-malformed-canary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "--dry-run", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "E_SYNC_FAILED") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "private-malformed-canary") || strings.Contains(errOut.String(), "private-malformed-canary") {
		t.Fatalf("diagnostic leaked content: stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

type cliSyncFixture struct{ project, vault, data, projectID string }

func newCLIFormatFixture(t *testing.T, projectID, fixtureRelative string) cliSyncFixture {
	t.Helper()
	root := t.TempDir()
	fixture := cliSyncFixture{project: filepath.Join(root, "project"), vault: filepath.Join(root, "vault"), data: filepath.Join(root, "data"), projectID: projectID}
	reviewRoot := filepath.Join(fixture.project, "docs", "session-review")
	if err := os.MkdirAll(reviewRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fixture.vault, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{
		filepath.Join(fixture.data, "projects", projectID, "merge-bases"),
		filepath.Join(fixture.data, "projects", projectID, "queue"),
		filepath.Join(fixture.data, "projects", projectID, "transactions"),
		filepath.Join(fixture.data, "projects", projectID, "locks"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for source, target := range map[string]string{
		"review.md": "项目回顾.md", "history.md": "项目历史.md",
		"ledger.json": filepath.Join(".session-reviewer", "ledger.json"),
		"index.json":  filepath.Join(".session-reviewer", "session-index.json"),
	} {
		body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contracts", fixtureRelative, source))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(reviewRoot, target)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.Save(filepath.Join(fixture.data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: projectID, Root: fixture.project, VaultRoot: fixture.vault,
		VaultReviewPath: "Projects/Format/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func newCLIAuthenticatedMarkdownFixture(t *testing.T) cliSyncFixture {
	return newCLIV4PublicationFixture(t, true)
}

func newCLIV3FormatFixture(t *testing.T) cliSyncFixture {
	t.Helper()
	root := t.TempDir()
	fixture := cliSyncFixture{project: filepath.Join(root, "project"), vault: filepath.Join(root, "vault"), data: filepath.Join(root, "data"), projectID: "project-v3-format"}
	for _, directory := range []string{fixture.project, fixture.vault, filepath.Join(fixture.data, "projects", fixture.projectID)} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.Save(filepath.Join(fixture.data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: fixture.projectID, Root: fixture.project, VaultRoot: fixture.vault,
		VaultReviewPath: "Projects/V3/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}}); err != nil {
		t.Fatal(err)
	}
	generationID := "generation-v3-format"
	reviewModel := reviewv2.Review{
		ProjectID: fixture.projectID, GenerationID: generationID, MinimumWriterVersion: reviewv2.MinimumWriterVersion,
		Revision: 1, Name: "V3", Goal: "classify v3", Stage: "implementation", Status: "active", NextAction: "migrate", LastVerification: "2026-09-05",
		Risks: []reviewv2.Risk{}, Decisions: []reviewv2.Decision{},
	}
	review, err := reviewv2.RenderReviewV3(reviewModel)
	if err != nil {
		t.Fatal(err)
	}
	history, err := reviewv2.RenderHistoryV3(fixture.projectID, 1, generationID, []reviewv2.Event{})
	if err != nil {
		t.Fatal(err)
	}
	machine, err := reviewv2.RenderMachineLedgerV3(reviewv2.MachineLedgerV3{
		SchemaVersion: 3, MinimumWriterVersion: reviewv2.MinimumWriterVersion,
		ProjectID: fixture.projectID, GenerationID: generationID, ProjectViewDigest: strings.Repeat("d", 64), AcceptedRevision: 1,
		ReviewSHA256: testBareSHA(review), HistorySHA256: testBareSHA(history), Sessions: []ledger.SessionReport{},
		HumanPatches: []reviewv2.HumanPatchWire{}, OrphanPatches: []reviewv2.HumanPatchWire{}, GeneratedBaselines: []reviewv2.GeneratedBaselineWire{},
		LegacyCompatibility: reviewv2.LegacyCompatibility{Timeline: []ledger.TimelineEvent{}, Decisions: []ledger.Decision{}, OpenLoops: []ledger.OpenLoop{}, CurrentRisks: []reviewv2.CurrentRiskProvenance{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for relative, body := range map[string][]byte{reviewv2.ReviewRelativePath: review, reviewv2.HistoryRelativePath: history, reviewv2.MachineLedgerRelativePath: machine} {
		path := filepath.Join(fixture.project, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func newCLIOldV4Fixture(t *testing.T) cliSyncFixture {
	return newCLIV4PublicationFixture(t, false)
}

func newCLIV4PublicationFixture(t *testing.T, publishMarkdown bool, customize ...func(*reviewv4.Presentation)) cliSyncFixture {
	t.Helper()
	root := t.TempDir()
	fixture := cliSyncFixture{project: filepath.Join(root, "project"), vault: filepath.Join(root, "vault"), data: filepath.Join(root, "data"), projectID: "project-markdown-cli"}
	if err := os.MkdirAll(fixture.project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fixture.vault, 0o755); err != nil {
		t.Fatal(err)
	}
	mapping := config.ProjectMapping{ID: fixture.projectID, Root: fixture.project, VaultRoot: fixture.vault, VaultReviewPath: "Projects/Markdown/Session Review", VaultCaseMode: platform.CaseSensitive}
	if err := config.Save(filepath.Join(fixture.data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{mapping}}); err != nil {
		t.Fatal(err)
	}
	store, err := memorystore.Open(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	probe := memory.ProjectProbeState{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: fixture.projectID, CanonicalRoot: "/private/project", Branch: "main",
		Head: strings.Repeat("a", 40), DirtyPathCount: 0, RemoteIdentityHashes: []string{}, VersionFiles: []memory.ProbeFile{},
		RequiredProjectionFiles: []memory.ProbeFile{}, ProbeVersion: "v1", Diagnostics: []memory.Diagnostic{},
	}
	probe.Digest, err = memory.ProjectProbeStateDigest(probe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProbeState(probe); err != nil {
		t.Fatal(err)
	}
	projectView := memory.ProjectView{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: fixture.projectID, Generation: 1,
		StartedAt: "2026-09-05T00:00:00Z", EndedAt: "2026-09-05T00:00:00Z", SourceSessions: 0,
		TerminalCounts: memory.TerminalCounts{}, SessionViewDependencies: []memory.SessionViewDependency{}, ObservationRevisionIDs: []string{},
		ProbeStateDigest: probe.Digest, LiveState: memory.StateSnapshot{Branch: probe.Branch, Head: probe.Head}, WitnessedState: []memory.DerivedRecord{}, DerivedRecords: []memory.DerivedRecord{},
		AggregationCoverage: memory.ProjectAggregationCoverage{}, AssociatedUsage: []memory.AssociatedUsage{}, DependencyDigest: "sha256:" + strings.Repeat("2", 64), ReducerVersion: "v1",
	}
	projectView.Digest, err = memory.ProjectViewDigest(projectView)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProjectView(projectView); err != nil {
		t.Fatal(err)
	}
	manifest := memory.GenerationManifest{
		SchemaVersion: memory.MemorySchemaVersion, GenerationID: "generation-markdown-cli", ProjectID: fixture.projectID, CreatedAt: projectView.EndedAt,
		SourceRecordDigests: []string{}, SessionViews: []memory.SessionViewDependency{}, SessionLineages: []memory.SessionLineageDependency{},
		ProbeStateDigest: projectView.ProbeStateDigest, ProbeCheck: memory.ProbeCheck{SchemaVersion: memory.MemorySchemaVersion, CheckedAt: projectView.EndedAt, StateDigest: projectView.ProbeStateDigest, Available: true, Diagnostics: []memory.Diagnostic{}},
		ProjectViewDigest: projectView.Digest,
	}
	generatedAt, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	index, err := sessionindex.Build(sessionindex.BuildInput{ProjectView: projectView, Manifest: manifest, SessionViews: map[sessionindex.SessionKey]*memory.SessionView{}, GeneratedAt: generatedAt})
	if err != nil {
		t.Fatal(err)
	}
	indexDigest, err := store.PutSessionIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SessionIndexDigest = indexDigest
	prepared, err := store.PrepareGeneration(manifest)
	if err != nil {
		t.Fatal(err)
	}
	projectOutput, err := presentation.Project(presentation.ProjectInput{ProjectView: projectView, GenerationID: manifest.GenerationID, Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	legacyPlan, err := presentation.Render(presentation.ProjectInput{ProjectView: projectView, GenerationID: manifest.GenerationID, Revision: 1}, projectOutput)
	if err != nil {
		t.Fatal(err)
	}
	byRelative := make(map[string]presentation.FilePlan, len(legacyPlan.Files))
	for _, file := range legacyPlan.Files {
		byRelative[file.Relative] = file
	}
	indexBody, err := sessionindex.Render(index)
	if err != nil {
		t.Fatal(err)
	}
	oldV4, err := migrationv4.BuildPreview(migrationv4.Input{
		Review: byRelative[reviewv2.ReviewRelativePath].Desired, History: byRelative[reviewv2.HistoryRelativePath].Desired,
		Ledger: byRelative[reviewv2.MachineLedgerRelativePath].Desired, SessionIndex: indexBody,
		GenerationID: manifest.GenerationID, TargetPreimages: map[string]migrationv4.Preimage{},
	})
	if err != nil {
		t.Fatal(err)
	}
	oldAccepted, err := reviewv4.LoadProjection(oldV4.Review, oldV4.History, oldV4.Ledger, oldV4.SessionIndex)
	if err != nil {
		t.Fatal(err)
	}
	oldAccepted.Review.CurrentState.Goal = "authenticated Markdown migration"
	for _, edit := range customize {
		edit(&oldAccepted.Review)
	}
	if len(customize) != 0 {
		events := make([]reviewv2.Event, 0, len(oldAccepted.Review.Timeline))
		for _, milestone := range oldAccepted.Review.Timeline {
			events = append(events, reviewv2.Event{ID: milestone.ID, GenerationID: milestone.GenerationID, OccurredAt: milestone.OccurredAt, Kind: milestone.Kind, Title: milestone.Title, Summary: milestone.Summary, Meaning: "milestone", Why: "status fixture", Next: "verify readonly status", DecisionIDs: milestone.DecisionIDs, Changes: []string{"confirmed conclusion"}, Results: []string{"fixture accepted"}})
		}
		oldV4.History, err = reviewv2.RenderHistoryV3(fixture.projectID, oldAccepted.Review.Revision, manifest.GenerationID, events)
		if err != nil {
			t.Fatal(err)
		}
		oldAccepted.Ledger.HistorySHA256 = testBareSHA(oldV4.History)
		oldAccepted.Ledger.SyncHashes.HistorySHA256 = oldAccepted.Ledger.HistorySHA256
	}
	oldV4.Review, err = strictjson.Encode(oldAccepted.Review)
	if err != nil {
		t.Fatal(err)
	}
	oldAccepted.Ledger.ReviewSHA256 = testBareSHA(oldV4.Review)
	oldAccepted.Ledger.SyncHashes.ReviewSHA256 = oldAccepted.Ledger.ReviewSHA256
	oldV4.Ledger, err = reviewv4.RenderLedger(oldAccepted.Ledger)
	if err != nil {
		t.Fatal(err)
	}
	target := oldV4
	if publishMarkdown {
		target, err = migrationv4.BuildMarkdownPreview(migrationv4.MarkdownMigrationInput{Source: migrationv4.Input{
			Review: oldV4.Review, History: oldV4.History, Ledger: oldV4.Ledger, SourceSessionIndex: indexBody, SessionIndex: indexBody,
			TargetPreimages: map[string]migrationv4.Preimage{}, TargetVaultPreimages: map[string]migrationv4.Preimage{},
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	plan := presentation.RenderPlan{ProjectID: fixture.projectID, GenerationID: manifest.GenerationID, ProjectViewDigest: manifest.ProjectViewDigest, Files: []presentation.FilePlan{
		{Relative: reviewv2.ReviewRelativePath, Desired: target.Review, Mode: 0o600},
		{Relative: reviewv2.HistoryRelativePath, Desired: target.History, Mode: 0o600},
		{Relative: reviewv2.MachineLedgerRelativePath, Desired: target.Ledger, Mode: 0o600},
		{Relative: "docs/session-review/.session-reviewer/session-index.json", Desired: target.SessionIndex, Mode: 0o600},
	}}
	if _, err := publication.Publish(t.Context(), publication.Options{ProjectID: fixture.projectID, PreparedGeneration: prepared.GenerationID, Plan: plan, Mapping: mapping, DataRoot: fixture.data, Now: time.Now}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func testBareSHA(body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%x", sum)
}

func newCLISyncFixture(t *testing.T) cliSyncFixture {
	t.Helper()
	root := t.TempDir()
	fixture := cliSyncFixture{project: filepath.Join(root, "project"), vault: filepath.Join(root, "vault"), data: filepath.Join(root, "data"), projectID: "project-1111111111111111"}
	projectData := filepath.Join(fixture.data, "projects", fixture.projectID)
	for _, directory := range []string{
		filepath.Join(fixture.project, "docs", "session-review"), fixture.vault,
		filepath.Join(projectData, "merge-bases"), filepath.Join(projectData, "queue"),
		filepath.Join(projectData, "transactions"), filepath.Join(projectData, "locks"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(projectData, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeCLIV2Fixture(t, fixture)
	cfg := config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: fixture.projectID, Root: fixture.project, VaultRoot: fixture.vault,
		VaultReviewPath: "Projects/CLI--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}}
	if err := config.Save(filepath.Join(fixture.data, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func newCLILegacySyncFixture(t *testing.T) cliSyncFixture {
	t.Helper()
	root := t.TempDir()
	fixture := cliSyncFixture{
		project: filepath.Join(root, "project"), vault: filepath.Join(root, "vault"),
		data: filepath.Join(root, "data"), projectID: "project-269b8cab6cbf69dd",
	}
	projectData := filepath.Join(fixture.data, "projects", fixture.projectID)
	for _, directory := range []string{
		fixture.project, fixture.vault,
		filepath.Join(projectData, "merge-bases"), filepath.Join(projectData, "queue"),
		filepath.Join(projectData, "transactions"), filepath.Join(projectData, "locks"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	legacyRoot := filepath.Join(fixture.project, "docs", "session-review")
	if err := os.MkdirAll(legacyRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	overview := "---\nid: project-overview\nentity_type: project_overview\nproject_id: " + fixture.projectID + "\nrevision: 1\nsync_status: synced\n---\n\n# Legacy fixture\n"
	if err := os.WriteFile(filepath.Join(legacyRoot, "project-overview.md"), []byte(overview), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy, err := ledger.Load(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	current := ledger.CurrentState{
		ProjectID: fixture.projectID, Revision: 1, Goal: "Migrate a realistic accepted legacy ledger",
		LastVerified: "legacy fixture loaded", Branch: "fixture", NextAction: "preview migration",
		FirstInspection: "docs/session-review/project-overview.md", LastUpdated: "2026-08-26T00:00:00Z",
	}
	plan, err := ledger.Render(legacy, ledger.ChangeSet{Current: &current})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(plan); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectData, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(fixture.data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: fixture.projectID, Root: fixture.project, VaultRoot: fixture.vault,
		VaultReviewPath: "Projects/Legacy--269b8cab/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func writeCLIV2Fixture(t *testing.T, fixture cliSyncFixture) {
	t.Helper()
	root := filepath.Join(fixture.project, "docs", "session-review")
	written := make(map[string][]byte, 2)
	for _, name := range []string{"项目回顾.valid.md", "项目历史.valid.md"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "review-v2", name))
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.ReplaceAll(body, []byte("project-0123456789abcdef"), []byte(fixture.projectID))
		target := strings.TrimSuffix(name, ".valid.md") + ".md"
		if err := os.WriteFile(filepath.Join(root, target), body, 0o644); err != nil {
			t.Fatal(err)
		}
		written[target] = body
	}
	machineBody, err := os.ReadFile(filepath.Join("..", "..", "testdata", "review-v2", "ledger.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	machine, err := reviewv2.ParseMachineLedger(machineBody)
	if err != nil {
		t.Fatal(err)
	}
	machine.ProjectID = fixture.projectID
	machine.AcceptedRevision = 1
	for index := range machine.Sessions {
		machine.Sessions[index].ProjectID = fixture.projectID
	}
	machine.LegacyCompatibility.CurrentState.ProjectID = fixture.projectID
	machine.LegacyCompatibility.CurrentState.Revision = 1
	for index := range machine.LegacyCompatibility.Decisions {
		machine.LegacyCompatibility.Decisions[index].ProjectID = fixture.projectID
		machine.LegacyCompatibility.Decisions[index].Revision = 1
	}
	for index := range machine.LegacyCompatibility.OpenLoops {
		machine.LegacyCompatibility.OpenLoops[index].ProjectID = fixture.projectID
		machine.LegacyCompatibility.OpenLoops[index].Revision = 1
	}
	for index := range machine.LegacyCompatibility.Timeline {
		machine.LegacyCompatibility.Timeline[index].Revision = 1
	}
	machine.ReviewSHA256 = syncdoc.ContentHash(written["项目回顾.md"])
	machine.HistorySHA256 = syncdoc.ContentHash(written["项目历史.md"])
	machineBody, err = reviewv2.RenderMachineLedger(machine)
	if err != nil {
		t.Fatal(err)
	}
	machineRoot := filepath.Join(root, ".session-reviewer")
	if err := os.MkdirAll(machineRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machineRoot, "ledger.json"), machineBody, 0o600); err != nil {
		t.Fatal(err)
	}
}
