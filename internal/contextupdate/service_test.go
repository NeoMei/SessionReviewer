package contextupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

func TestRunRecoversPartialInitialMarkdownBeforeClassifyingPublicFiles(t *testing.T) {
	for _, stopAfter := range []int{1, 2} {
		t.Run(fmt.Sprintf("project-destination-%d", stopAfter), func(t *testing.T) {
			dataRoot, projectRoot, vaultRoot, sessionsRoot := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
			projectID := fmt.Sprintf("project-initial-recovery-%d", stopAfter)
			mapping := config.ProjectMapping{ID: projectID, Root: projectRoot, VaultRoot: vaultRoot, VaultReviewPath: "Projects/Recovery/Session Review", VaultCaseMode: platform.CaseSensitive}
			if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{mapping}}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(projectRoot, "README.md"), []byte("# recovery\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}, {"add", "README.md"}, {"commit", "-m", "seed"}} {
				command := exec.Command("git", args...)
				command.Dir = projectRoot
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, output)
				}
			}
			started := time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC)
			session := strings.Join([]string{
				`{"timestamp":"` + started.Format(time.RFC3339) + `","type":"session_meta","payload":{"id":"recovery-session","cwd":"` + filepath.ToSlash(projectRoot) + `","source":"codex"}}`,
				`{"timestamp":"` + started.Add(time.Second).Format(time.RFC3339) + `","type":"turn_context","payload":{"cwd":"` + filepath.ToSlash(projectRoot) + `","model":"gpt-5"}}`,
			}, "\n") + "\n"
			if err := os.WriteFile(filepath.Join(sessionsRoot, "recovery.jsonl"), []byte(session), 0o600); err != nil {
				t.Fatal(err)
			}
			emptyJournal, err := publication.OpenJournal(dataRoot, projectID)
			if err != nil {
				t.Fatal(err)
			}
			if err := emptyJournal.Close(); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("simulated initial Markdown crash")
			written := 0
			opts := Options{ProjectID: projectID, SessionsRoot: sessionsRoot, DataRoot: dataRoot, Now: func() time.Time { return started.Add(time.Minute) }}
			opts.afterDestination = func(side, _ string) error {
				if side == "project" {
					written++
					if written == stopAfter {
						panic(failure)
					}
				}
				return nil
			}
			var firstErr error
			func() {
				defer func() {
					if recovered, ok := recover().(error); !ok || !errors.Is(recovered, failure) {
						t.Fatalf("initial publication panic=%v error=%v, want injected crash", recovered, firstErr)
					}
				}()
				_, firstErr = Run(context.Background(), opts)
			}()
			if written != stopAfter {
				t.Fatalf("Project destinations written=%d, want %d", written, stopAfter)
			}
			opts.afterDestination = nil
			result, err := Run(context.Background(), opts)
			if err != nil {
				t.Fatalf("restart did not recover partial initial publication: %v", err)
			}
			if result.GenerationID == "" {
				t.Fatal("restart recovered no published generation")
			}
			projectBodies := make(map[string][]byte, 4)
			vaultBodies := make(map[string][]byte, 4)
			for _, relative := range []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath, presentation.SessionIndexRelativePath} {
				projectBodies[relative], err = os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(relative)))
				if err != nil {
					t.Fatalf("read recovered Project file %s: %v", relative, err)
				}
				vaultRelative := filepath.ToSlash(filepath.Join(mapping.VaultReviewPath, strings.TrimPrefix(relative, "docs/session-review/")))
				vaultBodies[relative], err = os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(vaultRelative)))
				if err != nil || !bytes.Equal(projectBodies[relative], vaultBodies[relative]) {
					t.Fatalf("recovered mirror %s differs or is missing: %v", relative, err)
				}
			}
			accepted, err := reviewv4.LoadProjection(projectBodies[reviewv2.ReviewRelativePath], projectBodies[reviewv2.HistoryRelativePath], projectBodies[reviewv2.MachineLedgerRelativePath], projectBodies[presentation.SessionIndexRelativePath])
			if err != nil {
				t.Fatalf("recovered projection is invalid: %v", err)
			}
			private, err := memorystore.OpenReadOnly(dataRoot, projectID)
			if err != nil {
				t.Fatal(err)
			}
			_, manifest, loadErr := private.LoadPublished()
			closeErr := private.Close()
			if loadErr != nil || closeErr != nil {
				t.Fatalf("load recovered private publication: err=%v close=%v", loadErr, closeErr)
			}
			if err := syncproject.VerifyMarkdownBinding(accepted, manifest); err != nil {
				t.Fatalf("recovered projection lost private binding: %v", err)
			}
		})
	}
}

func TestRecoverActiveMarkdownBeforeScanFailsClosedOnCorruptIntent(t *testing.T) {
	dataRoot, projectRoot, vaultRoot := t.TempDir(), t.TempDir(), t.TempDir()
	projectID := "project-corrupt-recovery"
	journal, err := publication.OpenJournal(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	intentPath := filepath.Join(dataRoot, "publication-journal", projectID, "intent-v1.json")
	if err := os.WriteFile(intentPath, []byte("{\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mapping := config.ProjectMapping{ID: projectID, Root: projectRoot, VaultRoot: vaultRoot, VaultReviewPath: "Projects/Corrupt/Session Review", VaultCaseMode: platform.CaseSensitive}
	if err := recoverActiveMarkdownBeforeScan(context.Background(), dataRoot, projectID, mapping, time.Now); err == nil {
		t.Fatal("corrupt journal was treated as no active intent")
	}
}

func TestUnchangedPublicationKeepsAuditGenerationPrivate(t *testing.T) {
	vaultRoot := t.TempDir()
	reviewBody := []byte("review\n")
	historyBody := []byte("history\n")
	ledgerBody := []byte("ledger\n")
	current := currentProjectFiles{
		reviewBody: reviewBody, reviewFound: true,
		historyBody: historyBody, historyFound: true,
		ledgerBody: ledgerBody, ledgerFound: true,
	}
	mapping := config.ProjectMapping{VaultRoot: vaultRoot, VaultReviewPath: "Projects/Demo/Session Review"}
	for relative, body := range map[string][]byte{
		"Projects/Demo/Session Review/项目回顾.md":                       reviewBody,
		"Projects/Demo/Session Review/项目历史.md":                       historyBody,
		"Projects/Demo/Session Review/.session-reviewer/ledger.json": ledgerBody,
	} {
		full := filepath.Join(vaultRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	accepted := reviewv2.AcceptedV3{State: reviewv2.StateV3{Machine: reviewv2.MachineLedgerV3{
		GenerationID:      "scan-published",
		ProjectViewDigest: strings.Repeat("a", 64),
		ReviewSHA256:      fmt.Sprintf("%x", sha256.Sum256(reviewBody)),
		HistorySHA256:     fmt.Sprintf("%x", sha256.Sum256(historyBody)),
	}}}
	manifest := memory.GenerationManifest{
		GenerationID:      "scan-audit-only-successor",
		ProjectViewDigest: "sha256:" + strings.Repeat("a", 64),
	}

	result, unchanged, err := unchangedPublishedProjection(mapping, current, accepted, manifest, "scan-published")
	if err != nil || !unchanged || result.GenerationID != "scan-published" || len(result.ProjectFiles) != 3 || len(result.VaultFiles) != 3 {
		t.Fatalf("audit-only successor result=%+v unchanged=%t err=%v", result, unchanged, err)
	}
	for index := range result.ProjectFiles {
		if result.ProjectFiles[index].SHA256 != result.VaultFiles[index].SHA256 {
			t.Fatalf("mirror hashes differ: project=%+v vault=%+v", result.ProjectFiles, result.VaultFiles)
		}
	}

	edited := current
	edited.reviewBody = bytes.ReplaceAll(reviewBody, []byte("review"), []byte("human edit"))
	if _, unchanged, err := unchangedPublishedProjection(mapping, edited, accepted, manifest, "scan-published"); err != nil || unchanged {
		t.Fatalf("human edit was treated as an unchanged projection: unchanged=%t err=%v", unchanged, err)
	}

	changedManifest := manifest
	changedManifest.ProjectViewDigest = "sha256:" + strings.Repeat("b", 64)
	if _, unchanged, err := unchangedPublishedProjection(mapping, current, accepted, changedManifest, "scan-published"); err != nil || unchanged {
		t.Fatalf("changed project view was treated as unchanged: unchanged=%t err=%v", unchanged, err)
	}

	vaultReview := filepath.Join(vaultRoot, "Projects", "Demo", "Session Review", "项目回顾.md")
	if err := os.WriteFile(vaultReview, []byte("vault edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, unchanged, err := unchangedPublishedProjection(mapping, current, accepted, manifest, "scan-published"); err != nil || unchanged {
		t.Fatalf("vault edit was treated as unchanged: unchanged=%t err=%v", unchanged, err)
	}
}

func TestValidateCurrentProjectionProjectRejectsCrossProjectFiles(t *testing.T) {
	accepted := reviewv2.AcceptedV3{State: reviewv2.StateV3{Machine: reviewv2.MachineLedgerV3{ProjectID: "project-other"}}}
	if err := validateCurrentProjectionProject(accepted, "project-current"); err == nil || !strings.Contains(err.Error(), "project-other") {
		t.Fatalf("cross-project projection was accepted: %v", err)
	}
	accepted.State.Machine.ProjectID = "project-current"
	if err := validateCurrentProjectionProject(accepted, "project-current"); err != nil {
		t.Fatalf("matching projection was rejected: %v", err)
	}
}

func TestPublicationJournalSettledRejectsRecoverableIntent(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "project-journal-check"
	settled, err := publicationJournalSettled(dataRoot, projectID, "scan-published")
	if err != nil || !settled {
		t.Fatalf("fresh journal settled=%t err=%v", settled, err)
	}

	j, err := publication.OpenJournal(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	err = j.Create(publication.Intent{
		Version: 1, ProjectID: projectID, GenerationID: "scan-recoverable",
		ManifestDigest: digest, ProjectViewDigest: digest,
		Stage: publication.StagePrepared, CreatedAt: time.Now().UTC(),
		Destinations: []publication.Destination{{
			Side: "project", Relative: reviewv2.ReviewRelativePath,
			DesiredSHA256: strings.Repeat("b", 64),
		}},
	})
	if closeErr := j.Close(); err != nil || closeErr != nil {
		t.Fatalf("create recoverable intent: err=%v close=%v", err, closeErr)
	}

	settled, err = publicationJournalSettled(dataRoot, projectID, "scan-published")
	if err != nil || settled {
		t.Fatalf("recoverable journal settled=%t err=%v", settled, err)
	}
}

func TestBuildProjectionAccountingAuthenticatesUsageAndBuildsStableSessionChain(t *testing.T) {
	projectID := "project-accounting"
	records := []memory.SourceRecord{
		projectionSource("codex", "later", projectID, "2026-09-02T00:00:00Z", 200),
		projectionSource("codex", "earlier", projectID, "2026-09-01T00:00:00Z", 100),
	}
	snapshots := make(map[sourcecatalog.SnapshotKey]sourcecatalog.SourceSnapshot)
	associated := make([]memory.AssociatedUsage, 0, len(records))
	for _, record := range records {
		digest, err := memory.Digest(record.Usage)
		if err != nil {
			t.Fatal(err)
		}
		key := sourcecatalog.SnapshotKey{Provider: record.Provider, SessionID: record.SessionID}
		snapshots[key] = sourcecatalog.SourceSnapshot{Record: record, Found: true}
		associated = append(associated, memory.AssociatedUsage{Provider: record.Provider, SessionID: record.SessionID, UsageRecordDigest: digest})
	}
	summary, reports, err := buildProjectionAccounting(projectID, associated, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalTokens != 300 || len(reports) != 2 || reports[0].SessionID != "codex/earlier" || reports[0].NextSessionID != "codex/later" || reports[1].PreviousSessionID != "codex/earlier" {
		t.Fatalf("accounting projection=%+v reports=%+v", summary, reports)
	}
	if reports[0].Accounting == nil || reports[0].Accounting.Models[0].Pricing != (accounting.Pricing{}) {
		t.Fatalf("unknown pricing was not represented explicitly: %+v", reports[0].Accounting)
	}

	associated[0].UsageRecordDigest = "sha256:" + strings.Repeat("f", 64)
	if _, _, err := buildProjectionAccounting(projectID, associated, snapshots); err == nil {
		t.Fatal("mismatched usage digest was accepted")
	}
}

func projectionSource(provider, sessionID, projectID, startedAt string, tokens int64) memory.SourceRecord {
	started, _ := time.Parse(time.RFC3339Nano, startedAt)
	endedAt := started.Add(time.Second).Format(time.RFC3339Nano)
	return memory.SourceRecord{
		SchemaVersion: memory.MemorySchemaVersion, Provider: provider, SessionID: sessionID,
		SourceIdentity: "source-" + sessionID, StartedAt: startedAt, EndedAt: endedAt,
		FrozenBoundary: memory.FrozenBoundary{Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: 1, ByteOffset: 1}}, SourceHash: strings.Repeat("a", 64)},
		Availability:   memory.SourceAvailable,
		Usage:          accounting.SessionUsage{StartedAt: startedAt, EndedAt: endedAt, DurationMS: 1000, Models: []accounting.ModelUsage{{Model: "gpt-test", TokenUsage: accounting.TokenUsage{InputTokens: tokens, TotalTokens: tokens}}}, TotalTokens: tokens},
		ProjectIDs:     []string{projectID},
	}
}

func TestNotifyPhasePropagatesObserverFailure(t *testing.T) {
	want := errors.New("persist phase")
	err := notifyPhase(func(phase string) error {
		if phase != "rendering" {
			t.Fatalf("phase=%q", phase)
		}
		return want
	}, "rendering")
	if !errors.Is(err, want) {
		t.Fatalf("observer error was lost: %v", err)
	}
}

func TestProductionCodexDecoderRegistrationSupersedesPreviousEvidenceVersion(t *testing.T) {
	options := productionCodexAdapterOptions("/sessions", nil, nil, nil)
	if options.AdapterVersion != "codex-jsonl-v3" {
		t.Fatalf("production decoder version=%q want codex-jsonl-v3", options.AdapterVersion)
	}
	if fmt.Sprint(options.SupersedesAdapterVersions) != fmt.Sprint([]string{"codex-jsonl-v1", "codex-jsonl-v2"}) {
		t.Fatalf("production decoder predecessors=%v want [codex-jsonl-v1 codex-jsonl-v2]", options.SupersedesAdapterVersions)
	}
}

func TestProductionClaudeDecoderRegistrationUsesNativeProjectsRoot(t *testing.T) {
	options := productionClaudeAdapterOptions("/claude/projects", nil, nil, nil)
	if options.SessionsRoot != "/claude/projects" || options.AdapterVersion != "claude-jsonl-v1" || len(options.SupersedesAdapterVersions) != 0 {
		t.Fatalf("production Claude decoder options=%+v", options)
	}
}

func TestLoadCurrentProjectFilesRejectsUnreadableExistingFile(t *testing.T) {
	projectRoot := t.TempDir()
	reviewPath := filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	if err := os.MkdirAll(reviewPath, 0o700); err != nil {
		t.Fatal(err)
	}
	projectDir, err := pathguard.Open(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer projectDir.Close()

	_, err = loadCurrentProjectFiles(projectDir)
	if err == nil || !strings.Contains(err.Error(), reviewv2.ReviewRelativePath) {
		t.Fatalf("expected a path-specific read error, got %v", err)
	}
}

func TestLoadCurrentProjectFilesRejectsOldV4JSONWithUpgradeRequired(t *testing.T) {
	projectRoot := t.TempDir()
	ledgerBody, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contracts", "v4", "machine-ledger-v4.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	for relative, body := range map[string][]byte{reviewv2.ReviewRelativePath: []byte("legacy review\n"), reviewv2.HistoryRelativePath: []byte("legacy history\n"), reviewv2.MachineLedgerRelativePath: ledgerBody} {
		full := filepath.Join(projectRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	projectDir, err := pathguard.Open(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer projectDir.Close()
	if _, err := loadCurrentProjectFiles(projectDir); !errors.Is(err, syncproject.ErrMigrationRequired) {
		t.Fatalf("old v4 JSON route error=%v, want migration_required", err)
	}
}

func TestLoadCurrentProjectFilesKeepsLegacyDocumentLimit(t *testing.T) {
	projectRoot := t.TempDir()
	reviewPath := filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	if err := os.MkdirAll(filepath.Dir(reviewPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reviewPath, bytes.Repeat([]byte("x"), reviewv2.MaxDocumentBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	projectDir, err := pathguard.Open(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer projectDir.Close()
	if _, err := loadCurrentProjectFiles(projectDir); err == nil || !strings.Contains(err.Error(), reviewv2.ReviewRelativePath) || !strings.Contains(err.Error(), "read limit") {
		t.Fatalf("oversized legacy review error=%v", err)
	}
}

func TestLoadCurrentProjectFilesKeepsLegacyLedgerLimitAndWidensOnlyProvenMarkdown(t *testing.T) {
	t.Run("legacy ledger", func(t *testing.T) {
		projectRoot := t.TempDir()
		ledgerPath := filepath.Join(projectRoot, filepath.FromSlash(reviewv2.MachineLedgerRelativePath))
		if err := os.MkdirAll(filepath.Dir(ledgerPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ledgerPath, bytes.Repeat([]byte("x"), reviewv2.MaxMachineLedgerBytes+1), 0o600); err != nil {
			t.Fatal(err)
		}
		projectDir, err := pathguard.Open(projectRoot)
		if err != nil {
			t.Fatal(err)
		}
		defer projectDir.Close()
		if _, err := loadCurrentProjectFiles(projectDir); err == nil || !strings.Contains(err.Error(), fmt.Sprint(reviewv2.MaxMachineLedgerBytes)) {
			t.Fatalf("oversized legacy ledger error=%v", err)
		}
	})
	t.Run("projected Markdown", func(t *testing.T) {
		projectRoot := t.TempDir()
		ledgerBody, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contracts", "v4", "markdown", "ledger.json"))
		if err != nil {
			t.Fatal(err)
		}
		for relative, body := range map[string][]byte{reviewv2.ReviewRelativePath: bytes.Repeat([]byte("x"), reviewv2.MaxDocumentBytes+1), reviewv2.MachineLedgerRelativePath: ledgerBody} {
			full := filepath.Join(projectRoot, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		projectDir, err := pathguard.Open(projectRoot)
		if err != nil {
			t.Fatal(err)
		}
		defer projectDir.Close()
		files, err := loadCurrentProjectFiles(projectDir)
		if err != nil || len(files.reviewBody) != reviewv2.MaxDocumentBytes+1 {
			t.Fatalf("proven Markdown did not receive Markdown ceiling: bytes=%d err=%v", len(files.reviewBody), err)
		}
	})
}

func TestNextProjectionRevisionDetectsHumanBytesWithoutChurningNoOpScan(t *testing.T) {
	reviewBody := []byte("review\n")
	historyBody := []byte("history\n")
	accepted := reviewv2.AcceptedV3{State: reviewv2.StateV3{
		Review: reviewv2.Review{Revision: 7},
		Machine: reviewv2.MachineLedgerV3{
			GenerationID:  "generation-1",
			ReviewSHA256:  fmt.Sprintf("%x", sha256.Sum256(reviewBody)),
			HistorySHA256: fmt.Sprintf("%x", sha256.Sum256(historyBody)),
		},
	}}

	if got := nextProjectionRevision(accepted, reviewBody, historyBody, "generation-1", "generation-1"); got != 7 {
		t.Fatalf("unchanged scan churned revision: got %d want 7", got)
	}
	if got := nextProjectionRevision(accepted, []byte("human edit\n"), historyBody, "generation-1", "generation-1"); got != 8 {
		t.Fatalf("human edit did not advance revision: got %d want 8", got)
	}
	if got := nextProjectionRevision(accepted, reviewBody, historyBody, "generation-2", "generation-1"); got != 8 {
		t.Fatalf("new source generation did not advance revision: got %d want 8", got)
	}
}

func TestCaptureCurrentPresentationKeepsHumanStatusAboveNewGeneratedState(t *testing.T) {
	previous := presentation.NewScalarBaseline("project-overview", "status", "generated-before")
	accepted := reviewv2.AcceptedV3{State: reviewv2.StateV3{
		Review: reviewv2.Review{
			ProjectID: "project-capture", Revision: 2, Name: "Capture",
			Status: "human-decision",
		},
		Machine: reviewv2.MachineLedgerV3{
			GeneratedBaselines: []reviewv2.GeneratedBaselineWire{{
				GenerationID: "generation-old", EntityID: previous.EntityID, Field: previous.Field,
				Kind: string(previous.Kind), Value: previous.Value, GeneratedHash: previous.GeneratedHash,
			}},
			HumanPatches:  []reviewv2.HumanPatchWire{},
			OrphanPatches: []reviewv2.HumanPatchWire{},
		},
	}}

	legacy, patches, err := captureCurrentPresentation(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Review.Status != "human-decision" {
		t.Fatalf("current human model was not retained: %+v", legacy.Review)
	}
	if len(patches) != 1 || patches[0].EntityID != "project-overview" || patches[0].Field != "status" ||
		patches[0].Operation != presentation.Set || patches[0].Value != "human-decision" {
		t.Fatalf("human status was not captured as a patch: %+v", patches)
	}

	next := presentation.NewScalarBaseline("project-overview", "status", "generated-after")
	rebased, err := presentation.Rebase(patches, []presentation.Baseline{next})
	if err != nil {
		t.Fatal(err)
	}
	applied, err := presentation.Apply(rebased.Active, []presentation.Baseline{next})
	if err != nil {
		t.Fatal(err)
	}
	if got := applied["project-overview\x00status"]; !got.Present || got.Value != "human-decision" {
		t.Fatalf("new deterministic baseline overrode human authority: %+v", got)
	}
}

func TestCaptureCurrentPresentationPreservesEmptyListContract(t *testing.T) {
	previous := presentation.NewListBaseline("event-1", "changes", nil)
	accepted := reviewv2.AcceptedV3{State: reviewv2.StateV3{
		Events: []reviewv2.Event{{ID: "event-1", Changes: []string{}}},
		Machine: reviewv2.MachineLedgerV3{
			GeneratedBaselines: []reviewv2.GeneratedBaselineWire{{
				GenerationID: "generation-old", EntityID: previous.EntityID, Field: previous.Field,
				Kind: string(previous.Kind), Values: previous.Values, GeneratedHash: previous.GeneratedHash,
			}},
			HumanPatches:  []reviewv2.HumanPatchWire{},
			OrphanPatches: []reviewv2.HumanPatchWire{},
		},
	}}

	_, patches, err := captureCurrentPresentation(accepted)
	if err != nil {
		t.Fatalf("empty list contract was lost: %v", err)
	}
	if len(patches) != 0 {
		t.Fatalf("unchanged empty list created a patch: %+v", patches)
	}
}
