package contextupdate

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

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
