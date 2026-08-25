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

func TestProjectRenderLoadLegacyStatePreservesCompatibilitySemantics(t *testing.T) {
	legacy := legacyFixtureState(t)
	legacy.CurrentState.UncommittedChanges = []string{"internal/reviewv2/project.go", "docs/note.md"}
	legacy.CurrentState.FirstInspection = "inspect source slices before projection"
	legacy.CurrentState.LastUpdated = "2026-08-25T09:11:12Z"
	decision := legacy.Decisions["decision-local-cli"]
	decision.ReevaluateWhen = "the local trust boundary changes"
	decision.Supersedes = []string{}
	legacy.Decisions[decision.ID] = decision
	loop := legacy.OpenLoops["risk-install"]
	delete(legacy.OpenLoops, loop.ID)
	loop.ID = "risk-current-blocker-custom"
	legacy.OpenLoops[loop.ID] = loop
	legacy.Timeline[0].OpenLoopIDs = []string{loop.ID}
	report := legacy.Sessions["session-report-1"]
	report.OpenLoopsCreated = []string{loop.ID}
	legacy.Sessions[report.ID] = report

	state, err := ProjectLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	plan, err := Render(root, state)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range plan.Files {
		path := filepath.Join(root, filepath.FromSlash(file.RelativePath))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.Data, file.Perm); err != nil {
			t.Fatal(err)
		}
	}
	accepted, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(accepted.Legacy, legacy) {
		t.Fatalf("legacy compatibility round trip changed state:\nwant=%+v\ngot=%+v", legacy, accepted.Legacy)
	}
	if _, exists := accepted.Legacy.OpenLoops[loop.ID]; !exists {
		t.Fatalf("legitimate open loop with generated-looking ID was consumed: %+v", accepted.Legacy.OpenLoops)
	}
}

func TestProjectLegacyRejectsMalformedIdentityAndReferenceGraphBeforeProjection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ledger.State)
	}{
		{"map key identity", func(state *ledger.State) {
			value := state.Decisions["decision-local-cli"]
			delete(state.Decisions, value.ID)
			state.Decisions["decision-wrong-key"] = value
		}},
		{"entity project", func(state *ledger.State) {
			value := state.OpenLoops["risk-install"]
			value.ProjectID = "project-fedcba9876543210"
			state.OpenLoops[value.ID] = value
		}},
		{"global identity", func(state *ledger.State) { state.Timeline[0].ID = "decision-local-cli" }},
		{"decision supersedes", func(state *ledger.State) {
			value := state.Decisions["decision-local-cli"]
			value.Supersedes = []string{"decision-missing"}
			state.Decisions[value.ID] = value
		}},
		{"timeline open loop", func(state *ledger.State) { state.Timeline[0].OpenLoopIDs = []string{"risk-missing"} }},
		{"session decision", func(state *ledger.State) {
			value := state.Sessions["session-report-1"]
			value.DecisionsAdded = []string{"decision-missing"}
			state.Sessions[value.ID] = value
		}},
		{"session chain", func(state *ledger.State) {
			value := state.Sessions["session-report-1"]
			value.PreviousSessionID = "session-missing"
			state.Sessions[value.ID] = value
		}},
		{"evidence closure", func(state *ledger.State) { state.CurrentState.Evidence[0].SessionID = "session-missing" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			legacy := legacyFixtureState(t)
			test.mutate(&legacy)
			if _, err := ProjectLegacy(legacy); err == nil {
				t.Fatalf("malformed legacy graph %q was projected", test.name)
			}
		})
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

func TestApplyChangeSetPatchesLosslessAcceptedDocuments(t *testing.T) {
	root, _ := writeV2Fixture(t)
	reviewPath := filepath.Join(root, filepath.FromSlash(ReviewRelativePath))
	historyPath := filepath.Join(root, filepath.FromSlash(HistoryRelativePath))
	reviewBody, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	historyBody, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	reviewBody = bytes.Replace(reviewBody, []byte("## 项目目标\n"), []byte("<!-- keep-review-comment -->\n## 自定义说明\n  保留原始格式  \n\n## 项目目标\n"), 1)
	reviewBody = bytes.Replace(reviewBody, []byte("<!-- /session-reviewer:decision -->"), []byte("#### 自定义决策子节\n  原样保留  \n<!-- /session-reviewer:decision -->"), 1)
	historyBody = bytes.Replace(historyBody, []byte("# 项目历史\n\n"), []byte("# 项目历史\n\n<!-- keep-history-comment -->\n自定义历史说明\n\n"), 1)
	historyBody = bytes.Replace(historyBody, []byte("<!-- /session-reviewer:event -->"), []byte("### 自定义事件子节\n  原样保留 event  \n<!-- /session-reviewer:event -->"), 1)
	writeAcceptedDocumentsWithUpdatedHashes(t, root, reviewBody, historyBody)

	accepted, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	incoming := accepted.Legacy.CurrentState
	incoming.Revision++
	incoming.Goal = "accept changes without rewriting human prose"
	plan, err := ApplyChangeSet(accepted, ledger.ChangeSet{Current: &incoming})
	if err != nil {
		t.Fatal(err)
	}
	nextReview := plannedData(t, plan, ReviewRelativePath)
	nextHistory := plannedData(t, plan, HistoryRelativePath)
	for _, sentinel := range [][]byte{
		[]byte("<!-- keep-review-comment -->\n## 自定义说明\n  保留原始格式  \n"),
		[]byte("#### 自定义决策子节\n  原样保留  \n"),
	} {
		if !bytes.Contains(nextReview, sentinel) {
			t.Fatalf("review lost accepted bytes %q:\n%s", sentinel, nextReview)
		}
	}
	for _, sentinel := range [][]byte{
		[]byte("<!-- keep-history-comment -->\n自定义历史说明\n"),
		[]byte("### 自定义事件子节\n  原样保留 event  \n"),
	} {
		if !bytes.Contains(nextHistory, sentinel) {
			t.Fatalf("history lost accepted bytes %q:\n%s", sentinel, nextHistory)
		}
	}
	parsedReview, err := ParseReview(nextReview)
	if err != nil {
		t.Fatal(err)
	}
	parsedHistory, err := ParseHistory(nextHistory)
	if err != nil {
		t.Fatal(err)
	}
	if parsedReview.Model.Goal != incoming.Goal || parsedReview.Model.Revision != accepted.State.Review.Revision+1 || parsedHistory.Revision != parsedReview.Model.Revision {
		t.Fatalf("lossless patch has wrong semantics: review=%+v history_revision=%d", parsedReview.Model, parsedHistory.Revision)
	}
}

func TestApplyChangeSetOverlaysUnchangedHumanStatusAndCurrentRiskFields(t *testing.T) {
	root, _ := writeV2Fixture(t)
	reviewPath := filepath.Join(root, filepath.FromSlash(ReviewRelativePath))
	reviewBody, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	document, err := ParseReview(reviewBody)
	if err != nil {
		t.Fatal(err)
	}
	var currentRisk Risk
	for _, risk := range document.Model.Risks {
		if strings.HasPrefix(risk.ID, "risk-current-blocker-") {
			currentRisk = risk
			break
		}
	}
	if currentRisk.ID == "" {
		t.Fatal("fixture has no projected current blocker")
	}
	for _, edit := range []EditUnit{
		{Document: "review", UnitID: "project-overview", Field: "status", Value: "paused"},
		{Document: "review", UnitID: currentRisk.ID, Field: "risk.status", Value: "acknowledged"},
		{Document: "review", UnitID: currentRisk.ID, Field: "risk.detail", Value: "human-authored current-risk detail"},
	} {
		edit.ExpectedSHA256 = sha256Hex(reviewBody)
		reviewBody, err = PatchReviewUnit(reviewBody, edit)
		if err != nil {
			t.Fatal(err)
		}
	}
	historyBody, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(HistoryRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	writeAcceptedDocumentsWithUpdatedHashes(t, root, reviewBody, historyBody)
	accepted, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	incoming := accepted.Legacy.CurrentState
	incoming.Revision++
	incoming.Goal = "only the goal changed"
	plan, err := ApplyChangeSet(accepted, ledger.ChangeSet{Current: &incoming})
	if err != nil {
		t.Fatal(err)
	}
	next, err := ParseReview(plannedData(t, plan, ReviewRelativePath))
	if err != nil {
		t.Fatal(err)
	}
	if next.Model.Status != "paused" {
		t.Fatalf("human status was overwritten: %q", next.Model.Status)
	}
	risk := riskByID(next.Model.Risks)[currentRisk.ID]
	if risk.Status != "acknowledged" || risk.Detail != "human-authored current-risk detail" {
		t.Fatalf("unchanged current risk fields were overwritten: %+v", risk)
	}
}

func TestApplyChangeSetKeepsCurrentRiskIdentityAndHumanFieldsWhenTitleChanges(t *testing.T) {
	root, _ := writeV2Fixture(t)
	accepted, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	provenance := accepted.State.Machine.LegacyCompatibility.CurrentRisks[0]
	previous := riskByID(accepted.State.Review.Risks)[provenance.RiskID]
	incoming := accepted.Legacy.CurrentState
	incoming.Revision++
	incoming.Blockers = append([]string(nil), incoming.Blockers...)
	incoming.Blockers[0] = "release approval is now documented"
	plan, err := ApplyChangeSet(accepted, ledger.ChangeSet{Current: &incoming})
	if err != nil {
		t.Fatal(err)
	}
	next, err := ParseReview(plannedData(t, plan, ReviewRelativePath))
	if err != nil {
		t.Fatal(err)
	}
	updated, exists := riskByID(next.Model.Risks)[provenance.RiskID]
	if !exists || updated.Title != incoming.Blockers[0] || updated.Status != previous.Status {
		t.Fatalf("current-risk identity/overlay changed unexpectedly: before=%+v after=%+v", previous, updated)
	}
}

func TestApplyChangeSetInsertsNewDecisionWithoutCanonicalizingAcceptedProse(t *testing.T) {
	root, _ := writeV2Fixture(t)
	accepted, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	reviewPath := filepath.Join(root, filepath.FromSlash(ReviewRelativePath))
	reviewBody, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	reviewBody = bytes.Replace(reviewBody, []byte("## 项目目标\n"), []byte("<!-- retain-around-insertion -->\n## 项目目标\n"), 1)
	historyBody, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(HistoryRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	writeAcceptedDocumentsWithUpdatedHashes(t, root, reviewBody, historyBody)
	accepted, err = Load(root)
	if err != nil {
		t.Fatal(err)
	}
	incoming := ledger.Decision{
		ID: "decision-new-contract", ProjectID: accepted.Legacy.ProjectID,
		Title: "retain source spans", Status: "accepted", Revision: 1,
		Tags: []string{}, Supersedes: []string{}, SourceSessions: []string{}, Evidence: []ledger.EvidenceRef{},
		Rationale: "human prose is accepted state", Consequences: "updates patch only controlled fields",
		Alternatives: []string{}, RejectedPaths: []string{},
	}
	plan, err := ApplyChangeSet(accepted, ledger.ChangeSet{Decisions: []ledger.Decision{incoming}})
	if err != nil {
		t.Fatal(err)
	}
	nextBody := plannedData(t, plan, ReviewRelativePath)
	if !bytes.Contains(nextBody, []byte("<!-- retain-around-insertion -->")) {
		t.Fatalf("insertion canonicalized accepted prose:\n%s", nextBody)
	}
	next, err := ParseReview(nextBody)
	if err != nil {
		t.Fatal(err)
	}
	if got := decisionByID(next.Model.Decisions)[incoming.ID]; got.Title != incoming.Title {
		t.Fatalf("new decision not inserted: %+v", got)
	}
}

func writeAcceptedDocumentsWithUpdatedHashes(t *testing.T, root string, reviewBody, historyBody []byte) {
	t.Helper()
	if _, err := ParseReview(reviewBody); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseHistory(historyBody); err != nil {
		t.Fatal(err)
	}
	machinePath := filepath.Join(root, filepath.FromSlash(MachineLedgerRelativePath))
	machineBody, err := os.ReadFile(machinePath)
	if err != nil {
		t.Fatal(err)
	}
	machine, err := ParseMachineLedger(machineBody)
	if err != nil {
		t.Fatal(err)
	}
	machine.ReviewSHA256 = sha256Hex(reviewBody)
	machine.HistorySHA256 = sha256Hex(historyBody)
	machineBody, err = RenderMachineLedger(machine)
	if err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string][]byte{
		filepath.Join(root, filepath.FromSlash(ReviewRelativePath)):        reviewBody,
		filepath.Join(root, filepath.FromSlash(HistoryRelativePath)):       historyBody,
		filepath.Join(root, filepath.FromSlash(MachineLedgerRelativePath)): machineBody,
	} {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func plannedData(t *testing.T, plan ledger.WritePlan, relative string) []byte {
	t.Helper()
	for _, file := range plan.Files {
		if file.RelativePath == relative {
			return file.Data
		}
	}
	t.Fatalf("plan omits %s", relative)
	return nil
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
