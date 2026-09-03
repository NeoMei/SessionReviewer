package contextupdate

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

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
