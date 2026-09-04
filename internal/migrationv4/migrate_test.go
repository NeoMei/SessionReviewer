package migrationv4

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

type compatibilityFixture struct {
	Case                 string   `json:"case"`
	SourceVersion        int      `json:"source_version"`
	TargetVersion        int      `json:"target_version"`
	ReviewVersion        int      `json:"review_version"`
	HistoryVersion       int      `json:"history_version"`
	LedgerVersion        int      `json:"ledger_version"`
	IndexVersion         int      `json:"index_version"`
	ExpectedReader       string   `json:"expected_reader"`
	ExpectedRoute        string   `json:"expected_route"`
	OrdinarySyncError    string   `json:"ordinary_sync_error"`
	ConfirmationRequired bool     `json:"confirmation_required"`
	Present              []string `json:"present"`
	Missing              string   `json:"missing"`
	Expected             string   `json:"expected"`
}

func TestCompatibilityMatrixFixturesExerciseRealReaders(t *testing.T) {
	fixtures := loadCompatibilityFixtures(t)
	if got := fixtures["v2"]; got.SourceVersion != 2 || got.ExpectedReader != "reviewv2" || got.ExpectedRoute != "migrationv3" {
		t.Fatalf("unexpected v2 fixture: %+v", got)
	}
	for _, path := range []string{"../../testdata/review-v2/项目回顾.valid.md", "../../testdata/review-v2/项目历史.valid.md", "../../testdata/review-v2/ledger.valid.json"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		switch filepath.Base(path) {
		case "项目回顾.valid.md":
			if _, err := reviewv2.ParseReview(body); err != nil {
				t.Fatalf("v2 review fixture is no longer readable: %v", err)
			}
		case "项目历史.valid.md":
			if _, err := reviewv2.ParseHistory(body); err != nil {
				t.Fatalf("v2 history fixture is no longer readable: %v", err)
			}
		default:
			if _, err := reviewv2.ParseMachineLedger(body); err != nil {
				t.Fatalf("v2 ledger fixture is no longer readable: %v", err)
			}
		}
	}

	review, history, machine, index := migrationFixture(t)
	v3 := fixtures["v3"]
	if v3.SourceVersion != 3 || v3.TargetVersion != 4 || v3.OrdinarySyncError != "migration_required" || !v3.ConfirmationRequired {
		t.Fatalf("unexpected v3 fixture: %+v", v3)
	}
	if _, err := reviewv2.LoadV3Bytes(review, history, machine); err != nil {
		t.Fatalf("v3 strict read failed: %v", err)
	}
	result, err := MigrateAcceptedV3Result(review, history, machine, index)
	if err != nil {
		t.Fatal(err)
	}
	v4 := fixtures["v4"]
	if v4.ReviewVersion != 4 || v4.LedgerVersion != 4 || v4.IndexVersion != 1 || v4.ExpectedReader != "reviewv4.LoadProjection" {
		t.Fatalf("unexpected v4 fixture: %+v", v4)
	}
	if _, err := reviewv4.LoadProjection(result.Review, result.History, result.Ledger, result.SessionIndex); err != nil {
		t.Fatalf("v4 direct open failed: %v", err)
	}

	newer := fixtures["newer"]
	newerReview := bytes.Replace(review, []byte("schema_version: 3"), []byte(fmt.Sprintf("schema_version: %d", newer.SourceVersion)), 1)
	if newer.Expected != "reject" || newer.SourceVersion <= 4 {
		t.Fatalf("unexpected newer fixture: %+v", newer)
	}
	if _, err := PreviewMigration(newerReview, history, machine); err == nil {
		t.Fatal("newer fixture was accepted")
	}

	partial := fixtures["partial"]
	if partial.Expected != "reject" || partial.Missing != "session_index" || len(partial.Present) != 3 {
		t.Fatalf("unexpected partial fixture: %+v", partial)
	}
	if _, err := MigrateAcceptedV3(review, history, machine, nil); err == nil {
		t.Fatal("partial fixture was accepted")
	}

	mixed := fixtures["mixed"]
	mixedLedger := bytes.Replace(machine, []byte(`"schema_version": 3`), []byte(fmt.Sprintf(`"schema_version": %d`, mixed.LedgerVersion)), 1)
	if mixed.Expected != "reject" || mixed.ReviewVersion != 3 || mixed.HistoryVersion != 3 || mixed.LedgerVersion != 4 || mixed.IndexVersion != 1 {
		t.Fatalf("unexpected mixed fixture: %+v", mixed)
	}
	if _, err := PreviewMigration(review, history, mixedLedger); err == nil {
		t.Fatal("mixed fixture was accepted")
	}
}

func loadCompatibilityFixtures(t *testing.T) map[string]compatibilityFixture {
	t.Helper()
	result := make(map[string]compatibilityFixture)
	paths, err := filepath.Glob("../../testdata/contracts/migration/*.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fixture compatibilityFixture
		if err := json.Unmarshal(body, &fixture); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if fixture.Case == "" || result[fixture.Case].Case != "" {
			t.Fatalf("%s: missing or duplicate case %q", path, fixture.Case)
		}
		result[fixture.Case] = fixture
	}
	if len(result) != 6 {
		t.Fatalf("compatibility fixture count = %d, want 6", len(result))
	}
	return result
}

func TestMigrateAcceptedV3PreservesDecisionWithoutInventingFields(t *testing.T) {
	review, history, machine, index := migrationFixture(t)
	result, err := MigrateAcceptedV3Result(review, history, machine, index)
	if err != nil {
		t.Fatal(err)
	}
	decision := result.Accepted.Review.Decisions[0]
	if decision.ID != "decision-1" || decision.Title != "Keep v3" || decision.Rationale != "because" || decision.Impact != "scope" ||
		decision.Kind != "decision" || decision.Status != reviewv4.DecisionActive || decision.Provenance != "migrated" || decision.Pinned || decision.Revision != 1 ||
		decision.ReevaluateWhen != "" || len(decision.Supersedes) != 0 || len(decision.MilestoneIDs) != 0 || len(decision.SessionRefs) != 0 {
		t.Fatalf("migration lost or invented decision data: %+v", decision)
	}
	if _, err := reviewv4.LoadProjection(result.Review, result.History, result.Ledger, result.SessionIndex); err != nil {
		t.Fatalf("migrated four-file projection is not mutually bound: %v", err)
	}
}

func TestMigrateAcceptedV3PreservesLegacyUsageAsUnverifiedPricingEvidence(t *testing.T) {
	review, history, machine, indexBody := migrationFixture(t)
	source, err := reviewv2.LoadV3Bytes(review, history, machine)
	if err != nil {
		t.Fatal(err)
	}
	account := &accounting.SessionAccounting{
		StartedAt: "2026-09-04T00:00:00Z", EndedAt: "2026-09-04T00:01:00Z", DurationMS: 60_000,
		Models: []accounting.ModelAccounting{{
			ModelUsage: accounting.ModelUsage{Model: "legacy-model", TokenUsage: accounting.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}},
			Pricing:    accounting.Pricing{Currency: "USD", InputPerMillion: 1, OutputPerMillion: 2, Source: "https://example.test/legacy-price", AsOf: "2026-09-04"},
			CostUSD:    0.00002,
		}},
		TotalTokens: 15, TotalCostUSD: 0.00002,
	}
	source.State.Machine.Sessions = []ledger.SessionReport{{ID: "report-legacy", ProjectID: source.State.Machine.ProjectID, SessionID: "session-legacy", Accounting: account}}
	source.State.Machine.Accounting = accounting.ProjectSummary{
		TotalDurationMS: 60_000, TotalTokens: 15, TotalCostUSD: 0.00002,
		Models: []accounting.ProjectModelSummary{{Model: "legacy-model", TotalTokens: 15, TotalCostUSD: 0.00002, TokenSharePct: 100, CostSharePct: 100}},
	}
	index, err := sessionindex.Parse(indexBody)
	if err != nil {
		t.Fatal(err)
	}
	duration, records := uint64(60_000), uint64(1)
	terminal := "indexed"
	sessionDigest, usageDigest := "sha256:"+strings.Repeat("2", 64), "sha256:"+strings.Repeat("3", 64)
	lastGeneration := index.GenerationID
	index.Sessions = []sessionindex.Entry{{
		Provider: "codex", SessionID: "session-legacy", ProcessingState: sessionindex.ProcessingComplete,
		StateReasonCodes: []string{}, SourceAvailability: "available", SourceTerminalState: &terminal,
		StartedAt: account.StartedAt, EndedAt: account.EndedAt, DurationMS: &duration, RecordCount: &records,
		Coverage: sessionindex.Coverage{}, FactCounts: sessionindex.FactCounts{}, SessionViewDigest: &sessionDigest, UsageRecordDigest: &usageDigest,
		LastSeenGenerationID: &lastGeneration, LastSuccessfulGenerationID: &lastGeneration,
	}}
	index.Coverage = sessionindex.IndexCoverage{Total: 1, Complete: 1, SourceAvailable: 1, StartedAtKnown: 1, EndedAtKnown: 1, UsageKnown: 1}
	indexBody, err = sessionindex.Render(index)
	if err != nil {
		t.Fatal(err)
	}
	result, err := migrate(source, history, indexBody, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accepted.Ledger.PricingSnapshots) != 1 {
		t.Fatalf("pricing snapshots = %+v", result.Accepted.Ledger.PricingSnapshots)
	}
	snapshot := result.Accepted.Ledger.PricingSnapshots[0]
	if snapshot.Status != "legacy_unverified" || snapshot.PricingComplete || snapshot.TotalCostUSD != nil || snapshot.SourceKind != "unresolved" || snapshot.SourceURL != nil || snapshot.BillableQuantities.Input != 10 || snapshot.BillableQuantities.Output != 5 {
		t.Fatalf("legacy pricing evidence was upgraded or lost: %+v", snapshot)
	}
	if len(result.Accepted.Ledger.CurrentPricingSnapshotIDs) != 0 || result.Accepted.Ledger.Accounting.TotalCostUSD != nil || result.Accepted.Ledger.Accounting.Models[0].TotalCostUSD != nil {
		t.Fatalf("unverified legacy pricing was claimed current or complete: %+v", result.Accepted.Ledger.Accounting)
	}
}

func TestMigrationPreviewBindsEveryFreshnessInputAndIsDeterministic(t *testing.T) {
	review, history, machine, index := migrationFixture(t)
	input := Input{
		Review: review, History: history, Ledger: machine, SessionIndex: index,
		SessionViewDependencyDigests: []string{"sha256:" + strings.Repeat("2", 64)},
		TargetPreimages: map[string]Preimage{
			ReviewRelativePath: {Exists: true, Bytes: review},
		},
	}
	first, err := BuildPreview(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildPreview(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Review, second.Review) || !bytes.Equal(first.History, second.History) || !bytes.Equal(first.Ledger, second.Ledger) || !bytes.Equal(first.SessionIndex, second.SessionIndex) || first.Preview.PreviewDigest != second.Preview.PreviewDigest {
		t.Fatal("repeated migration was not byte stable")
	}
	if first.Preview.SchemaVersion != 1 || first.Preview.SourceVersion != 3 || first.Preview.TargetVersion != 4 || !first.Preview.RequiresSessionIndex {
		t.Fatalf("unexpected preview contract: %+v", first.Preview)
	}
	if got := first.Preview.TargetPreimageHashes.SessionIndex; got != AbsentPreimageSHA256 {
		t.Fatalf("missing target preimage = %q", got)
	}
	mutations := []func(*Input){
		func(in *Input) { in.Review = append(append([]byte(nil), in.Review...), '\n') },
		func(in *Input) { in.SessionViewDependencyDigests = []string{"sha256:" + strings.Repeat("3", 64)} },
		func(in *Input) {
			in.TargetPreimages[ReviewRelativePath] = Preimage{Exists: true, Bytes: []byte("changed")}
		},
	}
	for i, mutate := range mutations {
		changed := cloneInput(input)
		mutate(&changed)
		got, err := BuildPreview(changed)
		if err != nil {
			t.Fatal(err)
		}
		if got.Preview.PreviewDigest == first.Preview.PreviewDigest {
			t.Fatalf("freshness mutation %d did not change preview digest", i)
		}
	}
}

func TestPreviewMigrationRejectsMixedOrNewerSources(t *testing.T) {
	review, history, machine, _ := migrationFixture(t)
	if _, err := PreviewMigration(review, history, machine); err != nil {
		t.Fatalf("v3 preview rejected: %v", err)
	}
	newer := bytes.Replace(review, []byte("schema_version: 3"), []byte("schema_version: 4"), 1)
	if _, err := PreviewMigration(newer, history, machine); err == nil {
		t.Fatal("mixed/newer source was accepted")
	}
	if _, err := PreviewMigration(review, nil, machine); err == nil {
		t.Fatal("partial source was accepted")
	}
	for name, source := range map[string][3][]byte{
		"missing review":  {nil, history, machine},
		"missing history": {review, nil, machine},
		"missing ledger":  {review, history, nil},
		"newer history":   {review, bytes.Replace(history, []byte("schema_version: 3"), []byte("schema_version: 4"), 1), machine},
		"newer ledger":    {review, history, bytes.Replace(machine, []byte(`"schema_version": 3`), []byte(`"schema_version": 4`), 1)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PreviewMigration(source[0], source[1], source[2]); err == nil {
				t.Fatal("incompatible source was accepted")
			}
		})
	}
}

func TestMigrationPreviewRejectsStaleDigestAndInvalidBindings(t *testing.T) {
	review, history, machine, index := migrationFixture(t)
	result, err := BuildPreview(Input{Review: review, History: history, Ledger: machine, SessionIndex: index})
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*MigrationPreview){
		func(preview *MigrationPreview) { preview.SourceHashes.Review = "sha256:" + strings.Repeat("f", 64) },
		func(preview *MigrationPreview) { preview.GenerationID = "generation-other" },
		func(preview *MigrationPreview) { preview.TargetPreimageHashes.SessionIndex = "invalid" },
		func(preview *MigrationPreview) { preview.PreviewDigest = "sha256:" + strings.Repeat("0", 64) },
	}
	for index, mutate := range mutations {
		preview := result.Preview
		mutate(&preview)
		if err := validatePreview(preview); err == nil {
			t.Fatalf("invalid/stale preview mutation %d was accepted", index)
		}
	}
}

func TestMigrateAcceptedV3RejectsMixedProjectGenerationAndUnmappableStatus(t *testing.T) {
	review, history, machine, index := migrationFixture(t)
	parsedIndex, err := sessionindex.Parse(index)
	if err != nil {
		t.Fatal(err)
	}
	parsedIndex.ProjectID = "project-other"
	wrongProject, err := sessionindex.Render(parsedIndex)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateAcceptedV3(review, history, machine, wrongProject); err == nil {
		t.Fatal("mixed project index was accepted")
	}
	parsedIndex.ProjectID = "project-migration"
	parsedIndex.GenerationID = "generation-other"
	wrongGeneration, err := sessionindex.Render(parsedIndex)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateAcceptedV3(review, history, machine, wrongGeneration); err == nil {
		t.Fatal("mixed generation index was accepted")
	}

	badStatus := bytes.Replace(review, []byte("#### \u72b6\u6001\nactive"), []byte("#### \u72b6\u6001\nmaybe"), 1)
	badMachine := rebindV3ReviewHash(t, machine, badStatus)
	if _, err := MigrateAcceptedV3(badStatus, history, badMachine, index); err == nil {
		t.Fatal("unmappable decision status was accepted")
	}
}

func rebindV3ReviewHash(t *testing.T, machine, review []byte) []byte {
	t.Helper()
	value, err := reviewv2.ParseMachineLedgerV3(machine)
	if err != nil {
		t.Fatal(err)
	}
	value.ReviewSHA256 = bareHash(review)
	body, err := reviewv2.RenderMachineLedgerV3(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func cloneInput(input Input) Input {
	result := input
	result.Review = append([]byte(nil), input.Review...)
	result.History = append([]byte(nil), input.History...)
	result.Ledger = append([]byte(nil), input.Ledger...)
	result.SessionIndex = append([]byte(nil), input.SessionIndex...)
	result.SessionViewDependencyDigests = append([]string(nil), input.SessionViewDependencyDigests...)
	result.TargetPreimages = make(map[string]Preimage, len(input.TargetPreimages))
	for key, value := range input.TargetPreimages {
		value.Bytes = append([]byte(nil), value.Bytes...)
		result.TargetPreimages[key] = value
	}
	return result
}

func migrationFixture(t *testing.T) ([]byte, []byte, []byte, []byte) {
	t.Helper()
	projectID := "project-migration"
	generationID := "generation-migration"
	reviewModel := reviewv2.Review{
		ProjectID: projectID, GenerationID: generationID, MinimumWriterVersion: reviewv2.MinimumWriterVersion,
		Revision: 3, Name: "Migration", Goal: "Preserve", Stage: "implementation", Status: "active", NextAction: "verify", LastVerification: "2026-09-04",
		Risks: []reviewv2.Risk{}, Decisions: []reviewv2.Decision{{ID: "decision-1", OccurredAt: "2026-09-04", Title: "Keep v3", Rationale: "because", Impact: "scope", Status: "active"}},
	}
	events := []reviewv2.Event{{ID: "event-1", GenerationID: generationID, OccurredAt: "2026-09-04", Kind: "verification", Title: "Verified", Meaning: "meaning", Summary: "summary", Why: "why", Next: "next", Changes: []string{"change"}, Results: []string{"passed"}, DecisionIDs: []string{"decision-1"}}}
	reviewBody, err := reviewv2.RenderReviewV3(reviewModel)
	if err != nil {
		t.Fatal(err)
	}
	historyBody, err := reviewv2.RenderHistoryV3(projectID, reviewModel.Revision, generationID, events)
	if err != nil {
		t.Fatal(err)
	}
	machineBody, err := reviewv2.RenderMachineLedgerV3(reviewv2.MachineLedgerV3{
		SchemaVersion: 3, MinimumWriterVersion: reviewv2.MinimumWriterVersion, ProjectID: projectID, GenerationID: generationID,
		ProjectViewDigest: strings.Repeat("1", 64), AcceptedRevision: reviewModel.Revision, ReviewSHA256: bareHash(reviewBody), HistorySHA256: bareHash(historyBody),
		Accounting: accounting.ProjectSummary{Models: []accounting.ProjectModelSummary{}}, Sessions: []ledger.SessionReport{}, HumanPatches: []reviewv2.HumanPatchWire{}, OrphanPatches: []reviewv2.HumanPatchWire{}, GeneratedBaselines: []reviewv2.GeneratedBaselineWire{},
		LegacyCompatibility: reviewv2.LegacyCompatibility{Timeline: []ledger.TimelineEvent{}, Decisions: []ledger.Decision{}, OpenLoops: []ledger.OpenLoop{}, CurrentRisks: []reviewv2.CurrentRiskProvenance{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	indexBody, err := sessionindex.Render(sessionindex.Document{
		SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: projectID, GenerationID: generationID,
		ProjectViewDigest: "sha256:" + strings.Repeat("1", 64), GeneratedAt: "2026-09-04T00:00:00Z", SortVersion: sessionindex.SortVersion,
		Sessions: []sessionindex.Entry{}, Coverage: sessionindex.IndexCoverage{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return reviewBody, historyBody, machineBody, indexBody
}

func bareHash(body []byte) string { return fmt.Sprintf("%x", sha256.Sum256(body)) }
