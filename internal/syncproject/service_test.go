package syncproject

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

func TestSyncProjectMigrationConfirmationRecomputesUnderProjectLock(t *testing.T) {
	fixture := newMigrationServiceFixture(t)
	preview := migrationv4.MigrationPreview{PreviewDigest: "sha256:" + strings.Repeat("1", 64)}
	buildCalls := 0
	publishCalls := 0
	options := MigrationOptions{
		Options: Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI},
		Mode:    MigrationDryRun,
		build: func(pin *MappingPin) (migrationv4.Result, error) {
			buildCalls++
			contender, err := publicationlock.Acquire(pin.data.Path, pin.mapping.ID, 0)
			if contender != nil {
				_ = contender.Release()
			}
			if !errors.Is(err, project.ErrProjectLocked) {
				t.Fatalf("preview recomputation ran without publication lock: %v", err)
			}
			return migrationv4.Result{Preview: preview}, nil
		},
		Publish: func(_ context.Context, publication MigrationPublication) error {
			publishCalls++
			if publication.Preview.PreviewDigest != preview.PreviewDigest {
				t.Fatalf("publication preview = %+v", publication.Preview)
			}
			if publication.PublicationLock == nil {
				t.Fatal("publisher did not receive publication lock ownership")
			}
			contender, publicationErr := publicationlock.Acquire(publication.DataRoot, publication.ProjectID, 0)
			if contender != nil {
				_ = contender.Release()
			}
			if !errors.Is(publicationErr, project.ErrProjectLocked) {
				t.Fatalf("publisher ran without publication lock: %v", publicationErr)
			}
			lock, err := project.AcquireProjectLock(publication.syncDataRoot, "locks/sync.lock", 0)
			if lock != nil {
				_ = lock.Release()
			}
			if !errors.Is(err, project.ErrProjectLocked) {
				t.Fatalf("publisher ran without project lock: %v", err)
			}
			return nil
		},
	}
	dry, err := RunMigration(t.Context(), options)
	if err != nil || dry.Applied || dry.Preview.PreviewDigest != preview.PreviewDigest || buildCalls != 1 || publishCalls != 0 {
		t.Fatalf("dry=%+v build=%d publish=%d err=%v", dry, buildCalls, publishCalls, err)
	}

	options.Mode = MigrationConfirm
	options.ExpectedPreviewDigest = preview.PreviewDigest
	confirmed, err := RunMigration(t.Context(), options)
	if err != nil || !confirmed.Applied || buildCalls != 2 || publishCalls != 1 {
		t.Fatalf("confirmed=%+v build=%d publish=%d err=%v", confirmed, buildCalls, publishCalls, err)
	}
}

func TestSyncProjectMigrationConfirmationRejectsRecomputedStaleDigest(t *testing.T) {
	fixture := newMigrationServiceFixture(t)
	want := "sha256:" + strings.Repeat("1", 64)
	options := MigrationOptions{
		Options: Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI},
		Mode:    MigrationConfirm, ExpectedPreviewDigest: want,
		build: func(*MappingPin) (migrationv4.Result, error) {
			return migrationv4.Result{Preview: migrationv4.MigrationPreview{PreviewDigest: "sha256:" + strings.Repeat("2", 64)}}, nil
		},
		Publish: func(context.Context, MigrationPublication) error {
			t.Fatal("stale preview reached publisher")
			return nil
		},
	}
	if _, err := RunMigration(t.Context(), options); !errors.Is(err, ErrMigrationPreviewStale) {
		t.Fatalf("RunMigration error = %v", err)
	}
}

func TestSyncProjectPlainV3RequiresExplicitMigration(t *testing.T) {
	fixture := newMigrationServiceFixture(t)
	for relative, body := range map[string][]byte{
		reviewv2.ReviewRelativePath:        []byte("---\nid: project-overview\nentity_type: project_review\nproject_id: project-migration\nschema_version: 3\nrevision: 1\n---\n# v3\n"),
		reviewv2.HistoryRelativePath:       []byte("---\nid: project-history\nentity_type: project_history\nproject_id: project-migration\nschema_version: 3\nrevision: 1\n---\n# history\n"),
		reviewv2.MachineLedgerRelativePath: []byte("{}\n"),
	} {
		path := filepath.Join(fixture.project, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Run(t.Context(), Options{
		ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data,
		GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI,
	})
	if !errors.Is(err, ErrMigrationRequired) {
		t.Fatalf("plain v3 sync error = %v", err)
	}
}

func TestSyncProjectBuildsBoundMigrationFromPreparedGeneration(t *testing.T) {
	fixture := newMigrationServiceFixture(t)
	manifest := seedMigrationPreparedGeneration(t, fixture)
	before := snapshotMigrationPublicFiles(t, fixture)
	dry, err := RunMigration(t.Context(), MigrationOptions{
		Options: Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI},
		Mode:    MigrationDryRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dry.Applied || dry.Preview.ProjectID != fixture.projectID || dry.Preview.GenerationID != manifest.GenerationID || dry.Preview.TargetPreimageHashes.SessionIndex != migrationv4.AbsentPreimageSHA256 {
		t.Fatalf("dry migration = %+v", dry)
	}
	if after := snapshotMigrationPublicFiles(t, fixture); !reflect.DeepEqual(before, after) {
		t.Fatalf("dry-run wrote public files: before=%v after=%v", before, after)
	}

	published := 0
	confirmed, err := RunMigration(t.Context(), MigrationOptions{
		Options: Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI},
		Mode:    MigrationConfirm, ExpectedPreviewDigest: dry.Preview.PreviewDigest,
		Publish: func(_ context.Context, publication MigrationPublication) error {
			published++
			if len(publication.Plan.Files) != 4 {
				t.Fatalf("publication files = %d", len(publication.Plan.Files))
			}
			for _, file := range publication.Plan.Files {
				if file.Relative == migrationv4.SessionIndexRelativePath {
					if file.ExpectedExists {
						t.Fatal("new session index unexpectedly had a preimage")
					}
				} else if !file.ExpectedExists || len(file.Expected) == 0 {
					t.Fatalf("source preimage missing for %s", file.Relative)
				}
			}
			return nil
		},
	})
	if err != nil || !confirmed.Applied || published != 1 || confirmed.Preview.PreviewDigest != dry.Preview.PreviewDigest {
		t.Fatalf("confirmed=%+v published=%d err=%v", confirmed, published, err)
	}
}

func TestMigrationSessionIndexPreservesUnknownTimestamps(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	entry, err := migrationIndexEntry(memory.SessionView{
		Provider: "codex", SessionID: "unknown-times", TerminalState: memory.Indexed,
		SourceAvailability: memory.SourceAvailable, UsageRecordDigest: digest,
		ObservationSummaries: []memory.ObservationSummary{}, ActiveRevisionIDs: []string{}, Diagnostics: []memory.Diagnostic{},
	}, memory.SessionViewDependency{Digest: digest}, "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	if entry.StartedAt != nil || entry.EndedAt != nil || entry.DurationMS != nil {
		t.Fatalf("migration fabricated unknown timestamps: %+v", entry)
	}
	coverage := sessionindex.IndexCoverage{Total: 1}
	addIndexCoverage(&coverage, entry)
	if coverage.StartedAtKnown != 0 || coverage.EndedAtKnown != 0 {
		t.Fatalf("migration counted unknown timestamps as known: %+v", coverage)
	}
}

type migrationServiceFixture struct {
	projectID string
	project   string
	data      string
}

func seedMigrationPreparedGeneration(t *testing.T, fixture migrationServiceFixture) memory.GenerationManifest {
	t.Helper()
	store, err := memorystore.Open(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created := "2026-09-04T08:00:00Z"
	probe := memory.ProjectProbeState{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: fixture.projectID,
		CanonicalRoot: fixture.project, Branch: "main", Head: strings.Repeat("a", 40),
		RemoteIdentityHashes: []string{}, VersionFiles: []memory.ProbeFile{}, RequiredProjectionFiles: []memory.ProbeFile{},
		ProbeVersion: "v1", Diagnostics: []memory.Diagnostic{},
	}
	probe.Digest, err = memory.ProjectProbeStateDigest(probe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProbeState(probe); err != nil {
		t.Fatal(err)
	}
	view := memory.ProjectView{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: fixture.projectID, Generation: 1,
		StartedAt: created, EndedAt: created, SourceSessions: 0, TerminalCounts: memory.TerminalCounts{},
		SessionViewDependencies: []memory.SessionViewDependency{}, ObservationRevisionIDs: []string{}, ProbeStateDigest: probe.Digest,
		LiveState: memory.StateSnapshot{Branch: "main", Head: probe.Head}, WitnessedState: []memory.DerivedRecord{}, DerivedRecords: []memory.DerivedRecord{},
		AggregationCoverage: memory.ProjectAggregationCoverage{}, AssociatedUsage: []memory.AssociatedUsage{},
		DependencyDigest: "sha256:" + strings.Repeat("b", 64), ReducerVersion: "v1",
	}
	view.Digest, err = memory.ProjectViewDigest(view)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProjectView(view); err != nil {
		t.Fatal(err)
	}
	manifest := memory.GenerationManifest{
		SchemaVersion: memory.MemorySchemaVersion, GenerationID: "generation-migration", ProjectID: fixture.projectID, CreatedAt: created,
		SourceRecordDigests: []string{}, SessionViews: []memory.SessionViewDependency{}, SessionLineages: []memory.SessionLineageDependency{},
		ProbeStateDigest:  probe.Digest,
		ProbeCheck:        memory.ProbeCheck{SchemaVersion: memory.MemorySchemaVersion, CheckedAt: created, StateDigest: probe.Digest, Available: true, Diagnostics: []memory.Diagnostic{}},
		ProjectViewDigest: view.Digest,
	}
	if _, err := store.PrepareGeneration(manifest); err != nil {
		t.Fatal(err)
	}
	reviewModel := reviewv2.Review{
		ProjectID: fixture.projectID, GenerationID: manifest.GenerationID, MinimumWriterVersion: reviewv2.MinimumWriterVersion,
		Revision: 1, Name: "Migration", Goal: "Preserve", Stage: "implementation", Status: "active", NextAction: "confirm", LastVerification: "2026-09-04",
		Risks: []reviewv2.Risk{}, Decisions: []reviewv2.Decision{{ID: "decision-1", OccurredAt: "2026-09-04", Title: "Keep", Rationale: "because", Impact: "scope", Status: "active"}},
	}
	reviewBody, err := reviewv2.RenderReviewV3(reviewModel)
	if err != nil {
		t.Fatal(err)
	}
	historyBody, err := reviewv2.RenderHistoryV3(fixture.projectID, 1, manifest.GenerationID, []reviewv2.Event{})
	if err != nil {
		t.Fatal(err)
	}
	ledgerBody, err := reviewv2.RenderMachineLedgerV3(reviewv2.MachineLedgerV3{
		SchemaVersion: 3, MinimumWriterVersion: reviewv2.MinimumWriterVersion,
		ProjectID: fixture.projectID, GenerationID: manifest.GenerationID, ProjectViewDigest: strings.TrimPrefix(view.Digest, "sha256:"),
		AcceptedRevision: 1, ReviewSHA256: fmt.Sprintf("%x", sha256.Sum256(reviewBody)), HistorySHA256: fmt.Sprintf("%x", sha256.Sum256(historyBody)),
		Sessions: []ledger.SessionReport{}, HumanPatches: []reviewv2.HumanPatchWire{}, OrphanPatches: []reviewv2.HumanPatchWire{}, GeneratedBaselines: []reviewv2.GeneratedBaselineWire{},
		LegacyCompatibility: reviewv2.LegacyCompatibility{Timeline: []ledger.TimelineEvent{}, Decisions: []ledger.Decision{}, OpenLoops: []ledger.OpenLoop{}, CurrentRisks: []reviewv2.CurrentRiskProvenance{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for relative, body := range map[string][]byte{reviewv2.ReviewRelativePath: reviewBody, reviewv2.HistoryRelativePath: historyBody, reviewv2.MachineLedgerRelativePath: ledgerBody} {
		path := filepath.Join(fixture.project, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return manifest
}

func snapshotMigrationPublicFiles(t *testing.T, fixture migrationServiceFixture) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	for _, relative := range []string{migrationv4.ReviewRelativePath, migrationv4.HistoryRelativePath, migrationv4.LedgerRelativePath, migrationv4.SessionIndexRelativePath} {
		path := filepath.Join(fixture.project, filepath.FromSlash(relative))
		body, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		result[relative] = body
	}
	return result
}

func newMigrationServiceFixture(t *testing.T) migrationServiceFixture {
	t.Helper()
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	vaultRoot := filepath.Join(root, "vault")
	dataRoot := filepath.Join(root, "data")
	for _, directory := range []string{projectRoot, vaultRoot, filepath.Join(dataRoot, "projects", "project-migration", "locks")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dataRoot, "projects", "project-migration", "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: "project-migration", Root: projectRoot, VaultRoot: vaultRoot,
		VaultReviewPath: "Projects/Migration/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}}); err != nil {
		t.Fatal(err)
	}
	return migrationServiceFixture{projectID: "project-migration", project: projectRoot, data: dataRoot}
}

// Removing configured-mapping authentication, passing the global data root to
// the engine, or reconciling with a different trigger makes this test fail.
func TestSyncProjectServiceAuthenticatesMappingAndReconciles(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	vaultRoot := filepath.Join(root, "vault")
	dataRoot := filepath.Join(root, "data")
	for _, path := range []string{projectRoot, vaultRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	initialized, err := project.Initialize(project.InitOptions{
		ProjectRoot: projectRoot,
		VaultRoot:   vaultRoot,
		DataDir:     dataRoot,
		GOOS:        runtime.GOOS,
		Now:         func() time.Time { return now },
		Random:      bytes.NewReader(bytes.Repeat([]byte{0x11}, 8)),
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := Run(t.Context(), Options{
		ProjectID: initialized.ProjectID,
		CWD:       projectRoot,
		DataDir:   dataRoot,
		GOOS:      runtime.GOOS,
		Now:       func() time.Time { return now },
		Trigger:   syncengine.TriggerCLI,
		DryRun:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectID != initialized.ProjectID || !report.DryRun || len(report.Operations) == 0 {
		t.Fatalf("Run() report = %#v", report)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "Projects")); !os.IsNotExist(err) {
		t.Fatalf("dry-run mutated the Vault: %v", err)
	}

	if _, err := Run(t.Context(), Options{
		ProjectID: initialized.ProjectID,
		CWD:       filepath.Join(root, "other"),
		DataDir:   dataRoot,
		GOOS:      runtime.GOOS,
		Now:       func() time.Time { return now },
		Trigger:   syncengine.TriggerCLI,
		DryRun:    true,
	}); err == nil {
		t.Fatal("Run() accepted a CWD that does not authenticate the configured project")
	}
}

func TestSyncProjectServiceReconcilesProjectNestedInsideVault(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	projectRoot := filepath.Join(vaultRoot, "project")
	dataRoot := filepath.Join(root, "data")
	projectID := "project-1111111111111111"
	for _, path := range []string{projectRoot, dataRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	legacy := ledger.State{
		ProjectID: projectID,
		CurrentState: ledger.CurrentState{
			ProjectID: projectID, Revision: 1, Goal: "Nested project sync", Branch: "main",
			NextAction: "Sync", LastVerified: "2026-08-29T08:00:00Z", LastUpdated: "2026-08-29T08:00:00Z",
		},
		Decisions: map[string]ledger.Decision{}, OpenLoops: map[string]ledger.OpenLoop{}, Sessions: map[string]ledger.SessionReport{},
	}
	state, err := reviewv2.ProjectLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reviewv2.Render(projectRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(plan); err != nil {
		t.Fatal(err)
	}
	syncData := filepath.Join(dataRoot, "projects", projectID)
	for _, name := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := os.MkdirAll(filepath.Join(syncData, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(syncData, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{
		Version: 1,
		Projects: []config.ProjectMapping{{
			ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
			VaultReviewPath: "Projects/Nested--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	report, err := Run(t.Context(), Options{
		ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot,
		GOOS: runtime.GOOS, Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) },
		Trigger: syncengine.TriggerCLI,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectID != projectID || report.DryRun || len(report.Conflicts) != 0 || len(report.Errors) != 0 {
		t.Fatalf("Run() report = %#v", report)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "Projects", "Nested--11111111", "Session Review", "项目回顾.md")); err != nil {
		t.Fatalf("real nested sync did not publish into Vault: %v", err)
	}
}

// Removing the exact review-target capability from the MappingPin -> Engine
// handoff lets NewEngine independently reopen an ordinary replacement at the
// configured path and publish trusted Project bytes into it.
func TestSyncProjectRunNeverAdoptsReviewTargetAtPinToEngineHandoff(t *testing.T) {
	for _, targetInitiallyExists := range []bool{false, true} {
		name := "missing target with racing creator"
		if targetInitiallyExists {
			name = "existing target replaced"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			projectRoot := filepath.Join(root, "project")
			vaultRoot := filepath.Join(root, "vault")
			dataRoot := filepath.Join(root, "data")
			for _, directory := range []string{projectRoot, vaultRoot} {
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
			initialized, err := project.Initialize(project.InitOptions{
				ProjectRoot: projectRoot, VaultRoot: vaultRoot, DataDir: dataRoot,
				GOOS: runtime.GOOS, Now: func() time.Time { return now },
				Random: bytes.NewReader(bytes.Repeat([]byte{0x61}, 8)),
			})
			if err != nil {
				t.Fatal(err)
			}
			baseOptions := Options{
				ProjectID: initialized.ProjectID, CWD: projectRoot, DataDir: dataRoot,
				GOOS: runtime.GOOS, Now: func() time.Time { return now }, Trigger: syncengine.TriggerCLI,
			}
			if targetInitiallyExists {
				if _, err := Run(t.Context(), baseOptions); err != nil {
					t.Fatal(err)
				}
			}
			pin, err := PinMapping(baseOptions)
			if err != nil {
				t.Fatal(err)
			}
			defer pin.Close()

			target := filepath.Join(vaultRoot, filepath.FromSlash(pin.mapping.VaultReviewPath))
			detached := filepath.Join(root, "detached-authority")
			options := baseOptions
			options.Pin = pin
			options.beforeEngine = func() error {
				if targetInitiallyExists {
					if err := os.Rename(target, detached); err != nil {
						return err
					}
				}
				return os.MkdirAll(target, 0o700)
			}
			if _, err := Run(t.Context(), options); err == nil {
				t.Fatal("Run() accepted a replaced review-target namespace")
			}
			entries, err := os.ReadDir(target)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("ordinary replacement received trusted writes: %v", entries)
			}
			if targetInitiallyExists {
				if _, err := os.Stat(filepath.Join(detached, "项目回顾.md")); err != nil {
					t.Fatalf("pinned detached authority was lost: %v", err)
				}
			}
		})
	}
}

func TestPinMappingRejectsReviewTargetContainingOrEqualProject(t *testing.T) {
	for _, test := range []struct {
		name            string
		projectRelative string
	}{
		{name: "target contains project", projectRelative: "project"},
		{name: "target equals project"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			vaultRoot := filepath.Join(root, "vault")
			dataRoot := filepath.Join(root, "data")
			projectID := "project-1111111111111111"
			reviewPath := "Projects/Unsafe--11111111/Session Review"
			target := filepath.Join(vaultRoot, filepath.FromSlash(reviewPath))
			projectRoot := target
			if test.projectRelative != "" {
				projectRoot = filepath.Join(target, test.projectRelative)
			}
			for _, directory := range []string{projectRoot, filepath.Join(dataRoot, "projects", projectID)} {
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{
				Version: 1,
				Projects: []config.ProjectMapping{{
					ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
					VaultReviewPath: reviewPath, VaultCaseMode: platform.CaseSensitive,
				}},
			}); err != nil {
				t.Fatal(err)
			}

			pin, err := PinMapping(Options{
				ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot,
				GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI,
			})
			if pin != nil {
				_ = pin.Close()
				t.Fatal("PinMapping returned a pin for an overlapping review target")
			}
			if err == nil {
				t.Fatal("PinMapping accepted an overlapping review target")
			}
		})
	}
}

func TestPinMappingRecheckRejectsReviewTargetRedirectedToContainProject(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	dataRoot := filepath.Join(root, "data")
	projectID := "project-1111111111111111"
	projectRoot := filepath.Join(vaultRoot, "Session Review", "project")
	reviewPath := "Projects/Alias--11111111/Session Review"
	for _, directory := range []string{
		projectRoot,
		filepath.Join(vaultRoot, "Projects"),
		filepath.Join(dataRoot, "projects", projectID),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{
		Version: 1,
		Projects: []config.ProjectMapping{{
			ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
			VaultReviewPath: reviewPath, VaultCaseMode: platform.CaseSensitive,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	options := Options{
		ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot,
		GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI,
	}
	pin, err := PinMapping(options)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	alias := filepath.Join(vaultRoot, "Projects", "Alias--11111111")
	if err := os.Symlink(vaultRoot, alias); err != nil {
		t.Skipf("symlink/reparse-point creation is unavailable: %v", err)
	}
	if err := pin.Recheck(options); err == nil {
		t.Fatal("MappingPin accepted a review-target redirect to an ancestor of Project")
	}
}

func TestSyncProjectRejectsReviewTargetInsideNestedProjectWithoutWrites(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	projectRoot := filepath.Join(vaultRoot, "Projects", "Nested--11111111")
	dataRoot := filepath.Join(root, "data")
	projectID := "project-1111111111111111"
	for _, directory := range []string{projectRoot, dataRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	legacy := ledger.State{
		ProjectID: projectID,
		CurrentState: ledger.CurrentState{
			ProjectID: projectID, Revision: 1, Goal: "Reject nested target", Branch: "main", NextAction: "Do not write",
			LastVerified: "2026-08-29T08:00:00Z", LastUpdated: "2026-08-29T08:00:00Z",
		},
		Decisions: map[string]ledger.Decision{}, OpenLoops: map[string]ledger.OpenLoop{}, Sessions: map[string]ledger.SessionReport{},
	}
	state, err := reviewv2.ProjectLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reviewv2.Render(projectRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(plan); err != nil {
		t.Fatal(err)
	}
	syncData := filepath.Join(dataRoot, "projects", projectID)
	for _, name := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := os.MkdirAll(filepath.Join(syncData, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(syncData, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{
		Version: 1,
		Projects: []config.ProjectMapping{{
			ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
			VaultReviewPath: "Projects/Nested--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(projectRoot, "Session Review")
	if _, err := Run(t.Context(), Options{
		ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS,
		Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) }, Trigger: syncengine.TriggerCLI,
	}); err == nil {
		t.Fatal("Run() accepted a Vault review target inside the authoritative Project")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("unsafe target received writes: %v", err)
	}
	redirectName := "Redirect--11111111"
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(projectRoot, filepath.Join(vaultRoot, "Projects", redirectName)); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{
		Version: 1,
		Projects: []config.ProjectMapping{{
			ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
			VaultReviewPath: "Projects/" + redirectName + "/Session Review", VaultCaseMode: platform.CaseSensitive,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(t.Context(), Options{
		ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS,
		Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) }, Trigger: syncengine.TriggerCLI,
	}); err == nil {
		t.Fatal("Run() followed a redirect in the configured Vault review target")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("redirected target received writes: %v", err)
	}
}

// Changing the service contract to silently resolve a relative machine-data
// root would let worker and CLI callers disagree about the protected root.
func TestSyncProjectServiceRequiresExplicitAbsoluteDataDir(t *testing.T) {
	if _, err := Run(t.Context(), Options{
		ProjectID: "project-1111111111111111",
		DataDir:   "relative-data",
		GOOS:      runtime.GOOS,
		Now:       time.Now,
		Trigger:   syncengine.TriggerCLI,
	}); err == nil {
		t.Fatal("Run() accepted a relative data directory")
	}
}

// Extraction must preserve the old CLI lookup error when the data directory
// has not been initialized; opening the data root eagerly changes that public
// diagnostic into a lower-level filesystem error.
func TestSyncProjectServicePreservesMissingConfigLookupError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-data")
	_, err := Run(t.Context(), Options{
		ProjectID: "project-1111111111111111",
		DataDir:   missing,
		GOOS:      runtime.GOOS,
		Now:       time.Now,
		Trigger:   syncengine.TriggerCLI,
	})
	if err == nil || err.Error() != "configured project ID was not found" || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Run() error = %v, want configured mapping lookup diagnostic", err)
	}
}

func TestPinnedSyncRejectsConfigOrVaultReplacementWithoutDecoyWrites(t *testing.T) {
	for _, target := range []string{"config", "vault"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			projectRoot := filepath.Join(root, "project")
			vaultRoot := filepath.Join(root, "vault")
			dataRoot := filepath.Join(root, "data")
			for _, path := range []string{projectRoot, vaultRoot} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
			initialized, err := project.Initialize(project.InitOptions{
				ProjectRoot: projectRoot, VaultRoot: vaultRoot, DataDir: dataRoot,
				GOOS: runtime.GOOS, Now: func() time.Time { return now },
				Random: bytes.NewReader(bytes.Repeat([]byte{0x22}, 8)),
			})
			if err != nil {
				t.Fatal(err)
			}
			options := Options{
				ProjectID: initialized.ProjectID, CWD: projectRoot, DataDir: dataRoot,
				GOOS: runtime.GOOS, Now: func() time.Time { return now }, Trigger: syncengine.TriggerCLI,
			}
			pin, err := PinMapping(options)
			if err != nil {
				t.Fatal(err)
			}
			defer pin.Close()
			if target == "config" {
				fragments, err := os.ReadDir(filepath.Join(dataRoot, "projects.d"))
				if err != nil || len(fragments) != 1 {
					t.Fatalf("project fragments=%v err=%v", fragments, err)
				}
				configPath := filepath.Join(dataRoot, "projects.d", fragments[0].Name())
				body, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(configPath, configPath+".pinned"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(configPath, body, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(vaultRoot, vaultRoot+".pinned"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(vaultRoot, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			options.Pin = pin
			if _, err := Run(t.Context(), options); err == nil {
				t.Fatalf("Run() accepted %s replacement after mapping pin", target)
			}
			entries, err := os.ReadDir(vaultRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("replacement Vault received writes: entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestPinMappingParsesOneCapturedConfigSnapshotAcrossABA(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	vaultRoot := filepath.Join(root, "vault-original")
	decoyVault := filepath.Join(root, "vault-decoy")
	dataRoot := filepath.Join(root, "data")
	projectID := "project-1111111111111111"
	for _, directory := range []string{projectRoot, vaultRoot, decoyVault, dataRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	legacy := ledger.State{
		ProjectID: projectID,
		CurrentState: ledger.CurrentState{
			ProjectID: projectID, Revision: 1, Goal: "Captured config", Branch: "main", NextAction: "Sync",
			LastVerified: "2026-08-29T08:00:00Z", LastUpdated: "2026-08-29T08:00:00Z",
		},
		Decisions: map[string]ledger.Decision{}, OpenLoops: map[string]ledger.OpenLoop{}, Sessions: map[string]ledger.SessionReport{},
	}
	state, err := reviewv2.ProjectLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reviewv2.Render(projectRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(plan); err != nil {
		t.Fatal(err)
	}
	syncData := filepath.Join(dataRoot, "projects", projectID)
	for _, name := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := os.MkdirAll(filepath.Join(syncData, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(syncData, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	original := config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
		VaultReviewPath: "Projects/Original--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}}
	decoy := original
	decoy.Projects = []config.ProjectMapping{{
		ID: projectID, Root: projectRoot, VaultRoot: decoyVault,
		VaultReviewPath: "Projects/Decoy--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}
	configPath := filepath.Join(dataRoot, "config.toml")
	if err := config.Save(configPath, original); err != nil {
		t.Fatal(err)
	}
	originalBody, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	decoyPath := filepath.Join(root, "decoy.toml")
	if err := config.Save(decoyPath, decoy); err != nil {
		t.Fatal(err)
	}
	decoyBody, err := os.ReadFile(decoyPath)
	if err != nil {
		t.Fatal(err)
	}
	options := Options{
		ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS,
		Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) }, Trigger: syncengine.TriggerCLI,
	}
	seenCapture, seenParse := false, false
	options.pinCheckpoint = func(stage pinCheckpointStage) error {
		switch stage {
		case pinAfterCapture:
			seenCapture = true
			return os.WriteFile(configPath, decoyBody, 0o600)
		case pinAfterParse:
			seenParse = true
			return os.WriteFile(configPath, originalBody, 0o600)
		default:
			return nil
		}
	}
	pin, err := PinMapping(options)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	if !seenCapture || !seenParse || pin.mapping.VaultRoot != vaultRoot || pin.mapping.VaultReviewPath != "Projects/Original--11111111/Session Review" {
		t.Fatalf("pin mapping=%#v capture=%v parse=%v", pin.mapping, seenCapture, seenParse)
	}
	options.Pin = pin
	options.pinCheckpoint = nil
	if _, err := Run(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "Projects", "Original--11111111", "Session Review", "项目回顾.md")); err != nil {
		t.Fatalf("captured mapping target was not written: %v", err)
	}
	entries, err := os.ReadDir(decoyVault)
	if err != nil || len(entries) != 0 {
		t.Fatalf("decoy Vault received writes: entries=%v err=%v", entries, err)
	}
}

func TestPinMappingNeverUsesDecoyConfigAcrossEverySnapshotCheckpoint(t *testing.T) {
	stages := []pinCheckpointStage{
		pinAfterCapture, pinAfterParse, pinAfterMapping, pinBeforeVaultOpen, pinAfterVaultOpen, pinBeforeFinalVerify,
	}
	for index, mutateAt := range stages {
		t.Run(string(mutateAt), func(t *testing.T) {
			root := t.TempDir()
			projectRoot := filepath.Join(root, "project")
			vaultRoot := filepath.Join(root, "vault-original")
			decoyVault := filepath.Join(root, "vault-decoy")
			dataRoot := filepath.Join(root, "data")
			projectID := "project-1111111111111111"
			for _, directory := range []string{projectRoot, vaultRoot, decoyVault, filepath.Join(dataRoot, "projects", projectID)} {
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			original := config.Config{Version: 1, Projects: []config.ProjectMapping{{
				ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
				VaultReviewPath: "Projects/Original--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
			}}}
			decoy := config.Config{Version: 1, Projects: []config.ProjectMapping{{
				ID: projectID, Root: projectRoot, VaultRoot: decoyVault,
				VaultReviewPath: "Projects/Decoy--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
			}}}
			configPath := filepath.Join(dataRoot, "config.toml")
			if err := config.Save(configPath, original); err != nil {
				t.Fatal(err)
			}
			originalBody, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			decoyPath := filepath.Join(root, "decoy.toml")
			if err := config.Save(decoyPath, decoy); err != nil {
				t.Fatal(err)
			}
			decoyBody, err := os.ReadFile(decoyPath)
			if err != nil {
				t.Fatal(err)
			}
			options := Options{ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI}
			restored := false
			options.pinCheckpoint = func(stage pinCheckpointStage) error {
				if stage == mutateAt {
					return os.WriteFile(configPath, decoyBody, 0o600)
				}
				if index+1 < len(stages) && stage == stages[index+1] {
					restored = true
					return os.WriteFile(configPath, originalBody, 0o600)
				}
				return nil
			}
			pin, err := PinMapping(options)
			if index == len(stages)-1 {
				if err == nil {
					_ = pin.Close()
					t.Fatal("PinMapping accepted an unrestored namespace mutation")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer pin.Close()
				if !restored || pin.mapping.VaultRoot != vaultRoot || pin.mapping.VaultReviewPath != "Projects/Original--11111111/Session Review" {
					t.Fatalf("pin mapping=%#v restored=%v", pin.mapping, restored)
				}
			}
			entries, err := os.ReadDir(decoyVault)
			if err != nil || len(entries) != 0 {
				t.Fatalf("decoy Vault received writes: entries=%v err=%v", entries, err)
			}
		})
	}
}
