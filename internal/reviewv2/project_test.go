package reviewv2

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/ledger"
)

func TestProjectLegacyProducesTwoDocumentsAndMachineLedger(t *testing.T) {
	legacy := legacyFixtureState(t)
	state, err := ProjectLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Events) != len(legacy.Timeline) {
		t.Fatalf("events=%d", len(state.Events))
	}
	if state.Review.Decisions[0].ID == "" || state.Review.NextAction != legacy.CurrentState.NextAction {
		t.Fatalf("review=%+v", state.Review)
	}
	if state.Machine.Accounting.TotalTokens == 0 || state.Machine.Accounting.TotalCostUSD == 0 {
		t.Fatalf("accounting=%+v", state.Machine.Accounting)
	}
	plan, err := Render("", state)
	if err != nil {
		t.Fatal(err)
	}
	if got := plannedPaths(plan); !reflect.DeepEqual(got, []string{
		HistoryRelativePath, MachineLedgerRelativePath, ReviewRelativePath,
	}) {
		t.Fatalf("paths=%v", got)
	}
}

func TestProjectLegacyAssignsStableDistinctIDsToRepeatedCurrentRisks(t *testing.T) {
	legacy := legacyFixtureState(t)
	legacy.CurrentState.Blockers = make([]string, 20)
	for index := range legacy.CurrentState.Blockers {
		legacy.CurrentState.Blockers[index] = "same repeated blocker"
	}
	state, err := ProjectLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]struct{}, len(state.Review.Risks))
	for _, risk := range state.Review.Risks {
		if _, duplicate := seen[risk.ID]; duplicate {
			t.Fatalf("duplicate generated risk ID %q", risk.ID)
		}
		seen[risk.ID] = struct{}{}
	}
}

func TestApplyChangeSetReturnsThreeFilePlanWithExactPreimages(t *testing.T) {
	root, _ := writeV2Fixture(t)
	accepted, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	incoming := accepted.Legacy.Decisions["decision-local-cli"]
	incoming.Revision++
	incoming.Title = "keep all processing on the trusted local boundary"
	plan, err := ApplyChangeSet(accepted, ledger.ChangeSet{Decisions: []ledger.Decision{incoming}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ProjectRoot != root || !reflect.DeepEqual(plannedPaths(plan), []string{HistoryRelativePath, MachineLedgerRelativePath, ReviewRelativePath}) {
		t.Fatalf("plan=%+v", plan)
	}
	for _, file := range plan.Files {
		path := filepath.Join(root, filepath.FromSlash(file.RelativePath))
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !file.ExpectedExists || !bytes.Equal(file.ExpectedData, before) || file.ExpectedPerm != info.Mode().Perm() || file.Perm != info.Mode().Perm() {
			t.Fatalf("imprecise preimage for %s: %+v", file.RelativePath, file)
		}
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ApplyExpected(plan, rootInfo); err != nil {
		t.Fatal(err)
	}
	next, err := LoadExpected(root, rootInfo)
	if err != nil {
		t.Fatal(err)
	}
	if next.State.Review.Revision != accepted.State.Review.Revision+1 || next.State.Review.Decisions[0].Title != incoming.Title {
		t.Fatalf("next=%+v", next.State.Review)
	}
	if !reflect.DeepEqual(next.State.Events, accepted.State.Events) {
		t.Fatalf("unrelated decision change rewrote accepted event fields:\nbefore=%+v\nafter=%+v", accepted.State.Events, next.State.Events)
	}
}

func TestApplyChangeSetPlanRejectsPostRenderEditBeforeAnyWrite(t *testing.T) {
	root, _ := writeV2Fixture(t)
	accepted, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	incoming := accepted.Legacy.Decisions["decision-local-cli"]
	incoming.Revision++
	incoming.Title = "new accepted title"
	plan, err := ApplyChangeSet(accepted, ledger.ChangeSet{Decisions: []ledger.Decision{incoming}})
	if err != nil {
		t.Fatal(err)
	}
	machinePath := filepath.Join(root, filepath.FromSlash(MachineLedgerRelativePath))
	machineBefore, err := os.ReadFile(machinePath)
	if err != nil {
		t.Fatal(err)
	}
	reviewPath := filepath.Join(root, filepath.FromSlash(ReviewRelativePath))
	if err := os.WriteFile(reviewPath, []byte("concurrent edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ApplyExpected(plan, rootInfo); err == nil {
		t.Fatal("post-render edit was accepted")
	}
	machineAfter, err := os.ReadFile(machinePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(machineBefore, machineAfter) {
		t.Fatal("apply wrote a file before rejecting stale preimage")
	}
}

func legacyFixtureState(t *testing.T) ledger.State {
	t.Helper()
	const projectID = "project-0123456789abcdef"
	evidence := ledger.EvidenceRef{
		EvidenceID: "evidence-trust-chain",
		SessionID:  "session-source-1",
		JSONLLine:  12,
		SourceHash: strings.Repeat("a", 64),
		Summary:    "focused tests and repeat dry-run passed",
	}
	pricing := accounting.Pricing{
		Currency:                  "USD",
		InputPerMillion:           4,
		CachedInputPerMillion:     .4,
		CacheWriteInputPerMillion: 5,
		OutputPerMillion:          20,
		Source:                    "https://platform.openai.com/pricing",
		AsOf:                      "2026-08-25",
	}
	usage := accounting.TokenUsage{
		InputTokens:           1_000,
		CachedInputTokens:     400,
		CacheWriteInputTokens: 100,
		OutputTokens:          200,
		ReasoningOutputTokens: 50,
		TotalTokens:           1_200,
	}
	model := accounting.ModelAccounting{
		ModelUsage: accounting.ModelUsage{Model: "gpt-5.6-sol", TokenUsage: usage},
		Pricing:    pricing,
		CostUSD:    .00666,
	}
	sessionAccounting := &accounting.SessionAccounting{
		StartedAt:    "2026-08-25T09:00:00Z",
		EndedAt:      "2026-08-25T09:10:00Z",
		DurationMS:   600_000,
		Models:       []accounting.ModelAccounting{model},
		TotalTokens:  usage.TotalTokens,
		TotalCostUSD: model.CostUSD,
	}
	return ledger.State{
		ProjectID: projectID,
		CurrentState: ledger.CurrentState{
			ProjectID:       projectID,
			Revision:        2,
			Goal:            "make long-session recovery trustworthy",
			LastVerified:    "focused tests and repeat dry-run passed",
			Branch:          "codex/session-reviewer-v2",
			Blockers:        []string{"release approval is pending"},
			OpenRisks:       []string{"Windows still needs a final smoke test"},
			NextAction:      "finish migration implementation",
			FirstInspection: "read task brief",
			LastUpdated:     "2026-08-25T09:10:00Z",
			SourceSessions:  []string{"session-source-1"},
			Evidence:        []ledger.EvidenceRef{evidence},
		},
		Timeline: []ledger.TimelineEvent{{
			ID:          "timeline-trust-chain",
			OccurredAt:  "2026-08-25T09:10:00Z",
			Revision:    1,
			Class:       ledger.Verified,
			Title:       "trust chain verified",
			Summary:     "root-pinned reads and repeat dry-run converged",
			Evidence:    []ledger.EvidenceRef{evidence},
			DecisionIDs: []string{"decision-local-cli"},
			OpenLoopIDs: []string{"risk-install"},
		}},
		Decisions: map[string]ledger.Decision{
			"decision-local-cli": {
				ID:             "decision-local-cli",
				ProjectID:      projectID,
				Title:          "keep processing local",
				Status:         "accepted",
				Revision:       1,
				Tags:           []string{"privacy"},
				SourceSessions: []string{"session-source-1"},
				Evidence:       []ledger.EvidenceRef{evidence},
				Context:        "session transcripts are local",
				Rationale:      "avoid uploading private session data",
				Consequences:   "the CLI owns parsing and validation",
				Alternatives:   []string{"hosted processing"},
				RejectedPaths:  []string{"upload raw transcripts"},
			},
		},
		OpenLoops: map[string]ledger.OpenLoop{
			"risk-install": {
				ID:                  "risk-install",
				ProjectID:           projectID,
				Title:               "verify installer permissions",
				Status:              "open",
				Revision:            1,
				Tags:                []string{"release"},
				SourceSessions:      []string{"session-source-1"},
				Evidence:            []ledger.EvidenceRef{evidence},
				Question:            "does installation preserve permissions?",
				Attempts:            []string{"local package test"},
				Blocker:             "native Windows runner pending",
				NextExperiment:      "run Windows installation smoke",
				CompletionCriterion: "both platform smokes pass",
			},
		},
		Sessions: map[string]ledger.SessionReport{
			"session-report-1": {
				ID:               "session-report-1",
				ProjectID:        projectID,
				SessionID:        "session-source-1",
				Revision:         1,
				InitialGoal:      "harden accepted state",
				Phases:           []ledger.SessionPhase{{Title: "verification", Summary: "checked trust chain", Evidence: []ledger.EvidenceRef{evidence}}},
				Verification:     []string{"go test ./..."},
				DecisionsAdded:   []string{"decision-local-cli"},
				OpenLoopsCreated: []string{"risk-install"},
				Evidence:         []ledger.EvidenceRef{evidence},
				Accounting:       sessionAccounting,
			},
		},
	}
}

func plannedPaths(plan ledger.WritePlan) []string {
	paths := make([]string, len(plan.Files))
	for index, file := range plan.Files {
		paths[index] = file.RelativePath
	}
	return paths
}
