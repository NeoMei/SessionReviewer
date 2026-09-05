package migrationv4

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/baselinehash"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

// Returning a publishable result for unauthenticated prose would let a caller
// invent a migration baseline instead of proving the source format and ledger.
func TestMarkdownMigrationRejectsUnauthenticatedSource(t *testing.T) {
	_, err := BuildMarkdownPreview(MarkdownMigrationInput{Source: Input{
		Review: []byte("只有问题文本，没有接受账本"),
	}})
	if err == nil {
		t.Fatal("invented migration baseline")
	}
}

func TestOldV4HistoryDuplicateFieldsRequireAuthenticatedAdjudication(t *testing.T) {
	tests := []struct {
		name, reviewTitle, historyTitle, baseline, patch, want string
		wantConflict                                           bool
	}{
		{name: "equal normalized once", reviewTitle: "same", historyTitle: "same", want: "same"},
		{name: "review side proved human", reviewTitle: "human", historyTitle: "generated", baseline: "generated", patch: "human", want: "human"},
		{name: "history side proved human", reviewTitle: "generated", historyTitle: "human", baseline: "generated", patch: "human", want: "human"},
		{name: "different without proof", reviewTitle: "json", historyTitle: "markdown", wantConflict: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			review, history, ledger, index := oldV4TimelineSource(t, test.reviewTitle, test.historyTitle, test.baseline, test.patch)
			result, err := BuildMarkdownPreview(MarkdownMigrationInput{Source: Input{
				Review: review, History: history, Ledger: ledger, SourceSessionIndex: index, SessionIndex: index,
				TargetPreimages: map[string]Preimage{}, TargetVaultPreimages: map[string]Preimage{},
			}})
			if test.wantConflict {
				if err == nil || !strings.Contains(err.Error(), "markdown_migration_conflict") {
					t.Fatalf("unproved duplicate accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Accepted.Review.Timeline[0].Title; got != test.want {
				t.Fatalf("title = %q, want %q", got, test.want)
			}
		})
	}
}

func TestOldV4HistoryStructureMismatchConflicts(t *testing.T) {
	review, history, ledger, index := oldV4TimelineSource(t, "same", "same", "", "")
	history = bytes.Replace(history, []byte("### 事件类别\nmilestone"), []byte("### 事件类别\nrelease"), 1)
	ledger = rehashOldV4Ledger(t, review, history, ledger)
	_, err := BuildMarkdownPreview(MarkdownMigrationInput{Source: Input{Review: review, History: history, Ledger: ledger, SourceSessionIndex: index, SessionIndex: index, TargetPreimages: map[string]Preimage{}, TargetVaultPreimages: map[string]Preimage{}}})
	if err == nil || !strings.Contains(err.Error(), "markdown_migration_conflict") {
		t.Fatalf("history structure mismatch accepted: %v", err)
	}
}

func TestLegacyReconstructionRejectsUnverifiedAuthenticatedFields(t *testing.T) {
	review, history, ledger, index := migrationFixture(t)
	accepted, err := reviewv2.LoadV3Bytes(review, history, ledger)
	if err != nil {
		t.Fatal(err)
	}
	base := reviewv4.Presentation{
		SchemaVersion: 4, MinimumReaderVersion: "0.4.0", MinimumWriterVersion: "0.4.0",
		ProjectID: accepted.State.Machine.ProjectID, GenerationID: accepted.State.Machine.GenerationID,
		ProjectViewDigest: "sha256:" + accepted.State.Machine.ProjectViewDigest,
		CurrentState:      reviewv4.CurrentState{}, Timeline: []reviewv4.Timeline{}, Decisions: []reviewv4.Decision{}, Risks: []reviewv4.Risk{}, OpenLoops: []reviewv4.OpenLoop{},
		ProblemRootIDs: []string{}, ProblemNodes: []reviewv4.ProblemNode{}, ChainDependencies: []reviewv4.ChainDependency{}, HumanPatches: []reviewv4.Patch{}, OrphanPatches: []reviewv4.Patch{}, GeneratedBaselines: []reviewv4.Baseline{},
	}
	cases := []struct {
		name   string
		mutate func(*reviewv4.Presentation)
	}{
		{"project view mismatch", func(p *reviewv4.Presentation) { p.ProjectViewDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"chain dependency is caller asserted", func(p *reviewv4.Presentation) {
			p.ChainDependencies = []reviewv4.ChainDependency{{Provider: "codex", SessionID: "session-proof", SessionViewDigest: "sha256:" + strings.Repeat("a", 64), DependencyDigest: "sha256:" + strings.Repeat("b", 64), TurnUnitIDs: []string{}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base
			tc.mutate(&candidate)
			_, err := BuildMarkdownPreview(MarkdownMigrationInput{Source: Input{Review: review, History: history, Ledger: ledger, SessionIndex: index, TargetPreimages: map[string]Preimage{}, TargetVaultPreimages: map[string]Preimage{}}, Reconstructed: &candidate})
			if err == nil {
				t.Fatal("unverified reconstruction accepted")
			}
		})
	}
}

func oldV4TimelineSource(t *testing.T, reviewTitle, historyTitle, baseline, patch string) ([]byte, []byte, []byte, []byte) {
	t.Helper()
	review := compatibilityArtifact(t, "v4", ReviewRelativePath)
	history := compatibilityArtifact(t, "v4", HistoryRelativePath)
	ledger := compatibilityArtifact(t, "v4", LedgerRelativePath)
	index := compatibilityArtifact(t, "v4", SessionIndexRelativePath)
	accepted, err := reviewv4.LoadProjection(review, history, ledger, index)
	if err != nil {
		t.Fatal(err)
	}
	event := reviewv4.Timeline{ID: "milestone-auth", GenerationID: accepted.Review.GenerationID, OccurredAt: "2026-09-05T00:00:00Z", Kind: "milestone", Title: reviewTitle, Summary: "summary", DecisionIDs: []string{}, ClosedLoop: reviewv4.NeutralClosedLoop()}
	accepted.Review.Timeline = []reviewv4.Timeline{event}
	if patch != "" {
		hash := baselinehash.SHA256(event.ID, "title", "scalar", baseline, nil)
		baseValue, patchValue := baseline, patch
		accepted.Review.GeneratedBaselines = []reviewv4.Baseline{{GenerationID: accepted.Review.GenerationID, EntityID: event.ID, Field: "title", Kind: "scalar", Value: &baseValue, GeneratedHash: hash}}
		accepted.Review.HumanPatches = []reviewv4.Patch{{EntityID: event.ID, Field: "title", Operation: "set", Value: &patchValue, BaseGeneratedHash: hash}}
	}
	review, err = strictjson.Encode(accepted.Review)
	if err != nil {
		t.Fatal(err)
	}
	history, err = reviewv2.RenderHistory(accepted.Review.ProjectID, accepted.Review.Revision, []reviewv2.Event{{
		ID: event.ID, OccurredAt: event.OccurredAt, Kind: event.Kind, Title: historyTitle,
		Meaning: "legacy meaning", Summary: event.Summary, Why: "legacy why", Next: "legacy next",
		Changes: []string{"legacy change"}, Results: []string{"legacy result"}, DecisionIDs: []string{},
	}})
	if err != nil {
		t.Fatal(err)
	}
	accepted.Ledger.HumanPatches = accepted.Review.HumanPatches
	accepted.Ledger.GeneratedBaselines = accepted.Review.GeneratedBaselines
	ledger = rehashOldV4Ledger(t, review, history, mustRenderLedger(t, accepted.Ledger))
	if _, err := reviewv4.LoadProjection(review, history, ledger, index); err != nil {
		t.Fatal(err)
	}
	return review, history, ledger, index
}

func mustRenderLedger(t *testing.T, ledger reviewv4.MachineLedger) []byte {
	t.Helper()
	body, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func rehashOldV4Ledger(t *testing.T, review, history, ledger []byte) []byte {
	t.Helper()
	value, err := reviewv4.DecodeLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	value.ReviewSHA256, value.HistorySHA256 = bareDigest(review), bareDigest(history)
	value.SyncHashes.ReviewSHA256, value.SyncHashes.HistorySHA256 = value.ReviewSHA256, value.HistorySHA256
	body, err := reviewv4.RenderLedger(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// Removing the explicit legacy evidence gate would turn the old converter's
// empty problem/chain arrays into a false claim of completed reconstruction.
func TestMarkdownV3MigrationPreviewIsInformativeButBlockedWithoutVerifiedReconstruction(t *testing.T) {
	review, history, ledger, index := migrationFixture(t)
	result, err := BuildMarkdownPreview(MarkdownMigrationInput{Source: Input{
		Review: review, History: history, Ledger: ledger, SessionIndex: index,
		TargetPreimages: map[string]Preimage{}, TargetVaultPreimages: map[string]Preimage{},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Preview.SourceFormat != "review-markdown-v3" || result.Preview.TargetFormat != "review-markdown-v1" {
		t.Fatalf("format route = %q -> %q", result.Preview.SourceFormat, result.Preview.TargetFormat)
	}
	if !reflect.DeepEqual(result.Preview.BlockingReasons, []string{"verified_conversation_chain_required", "verified_user_request_classification_required"}) {
		t.Fatalf("blocking reasons = %#v", result.Preview.BlockingReasons)
	}
	if result.Preview.TargetHashes != (ArtifactHashes{}) || len(result.Review)+len(result.History)+len(result.Ledger)+len(result.SessionIndex) != 0 {
		t.Fatalf("blocked preview carried publishable artifacts: %+v", result.Preview.TargetHashes)
	}
	if result.Preview.SourceHashes.SessionIndex != AbsentPreimageSHA256 {
		t.Fatalf("legacy v3 target index was misreported as source evidence: %q", result.Preview.SourceHashes.SessionIndex)
	}
	if result.Preview.PreviewDigest == "" || MigrationPreviewDigest(result.Preview) != result.Preview.PreviewDigest {
		t.Fatal("blocked preview is not deterministic or digest-bound")
	}
	if err := validateBlockedPreview(result.Preview); err != nil {
		t.Fatalf("blocked preview validation: %v", err)
	}
	if err := validatePreview(result.Preview); err == nil {
		t.Fatal("blocked preview passed publishable Result validation")
	}
}

// Re-decoding old v4 through a legacy projection would lose typed fields.
// This test requires the authenticated old presentation to survive exactly and
// the existing history shell to be reused byte-for-byte by the Markdown renderer.
func TestOldV4JSONMigrationPreservesTypedPresentationAndHistoryShell(t *testing.T) {
	review := compatibilityArtifact(t, "v4", ReviewRelativePath)
	history := compatibilityArtifact(t, "v4", HistoryRelativePath)
	ledger := compatibilityArtifact(t, "v4", LedgerRelativePath)
	index := compatibilityArtifact(t, "v4", SessionIndexRelativePath)
	source, err := reviewv4.LoadProjection(review, history, ledger, index)
	if err != nil {
		t.Fatal(err)
	}
	result, err := BuildMarkdownPreview(MarkdownMigrationInput{Source: Input{
		Review: review, History: history, Ledger: ledger, SourceSessionIndex: index,
		SessionIndex: index, TargetPreimages: map[string]Preimage{}, TargetVaultPreimages: map[string]Preimage{},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Preview.SourceFormat != "review-json-v4" || result.Preview.TargetFormat != "review-markdown-v1" || len(result.Preview.BlockingReasons) != 0 {
		t.Fatalf("preview = %+v", result.Preview)
	}
	if !reflect.DeepEqual(result.Accepted.Review, source.Review) {
		t.Fatalf("typed source changed:\n got %+v\nwant %+v", result.Accepted.Review, source.Review)
	}
	if !bytes.Contains(result.History, []byte("> 按时间逆序排列。")) {
		t.Fatalf("old history shell was not preserved: %q", result.History)
	}
	if !bytes.Contains(result.History, []byte("升级前历史原文存档（非权威，不作为当前字段或结构依据）")) {
		t.Fatalf("old history archive was not marked non-authoritative: %q", result.History)
	}
	if got := result.Preview.PreservedCustomHashes[HistoryRelativePath]; got == "" {
		t.Fatalf("history preservation hash missing: %#v", result.Preview.PreservedCustomHashes)
	}
	if _, err := reviewv4.LoadProjection(result.Review, result.History, result.Ledger, result.SessionIndex); err != nil {
		t.Fatalf("Markdown target is not a valid projection: %v", err)
	}
}

func TestHistoricalPreservationUsesSafeFenceAndKeepsExactSourceBytes(t *testing.T) {
	source := []byte("# 旧权威标题\n\n```go\nfmt.Println(\"old\")\n```\n````\n")
	got := appendHistoricalPreservation([]byte("# 当前历史\n"), source)
	if !bytes.Contains(got, source) || !bytes.Contains(got, []byte("非权威，不作为当前字段或结构依据")) {
		t.Fatalf("archive=%q", got)
	}
	if !bytes.Contains(got, []byte("`````text\n")) || !bytes.Contains(got, []byte("\n`````\n")) {
		t.Fatalf("archive did not choose a fence longer than source runs: %q", got)
	}
}

// Omitting any new routing field from the digest would allow an old JSON
// preview to authorize Markdown output.
func TestMarkdownMigrationPreviewDigestAuthenticatesNewFieldsWithoutChangingOldEncoding(t *testing.T) {
	legacy := MigrationPreview{SchemaVersion: 1, SourceVersion: 3, TargetVersion: 4}
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"source_format", "target_format", "preserved_custom_hashes", "blocking_reasons", "vault_preimage_hashes"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("legacy canonical JSON unexpectedly contains %q: %s", forbidden, body)
		}
	}
	if got, want := MigrationPreviewDigest(legacy), "sha256:fd7fedda62cca5e4be358a4eeb85525fd272c93429a3a07db195d91c2c8f994f"; got != want {
		t.Fatalf("legacy preview digest = %q, want frozen pre-Markdown digest %q", got, want)
	}
	modern := legacy
	modern.SourceFormat, modern.TargetFormat = "review-json-v4", "review-markdown-v1"
	modern.PreservedCustomHashes = map[string]string{ReviewRelativePath: "sha256:" + strings.Repeat("1", 64)}
	digest := MigrationPreviewDigest(modern)
	modern.TargetFormat = "review-json-v4"
	if digest == MigrationPreviewDigest(modern) {
		t.Fatal("target format was omitted from preview digest")
	}
}

// Deriving the target generation from wall time or the final self-referential
// index would make confirmations non-repeatable. The successor must use the
// authenticated source manifest and a source-generation seed index.
func TestMarkdownMigrationBindingSuccessorIsDeterministicAndLeavesSourceImmutable(t *testing.T) {
	projectView := memory.ProjectView{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: "project-migration", Generation: 1,
		StartedAt: "2026-09-05T00:00:00Z", EndedAt: "2026-09-05T00:00:00Z", SourceSessions: 0,
		TerminalCounts: memory.TerminalCounts{}, SessionViewDependencies: []memory.SessionViewDependency{}, ObservationRevisionIDs: []string{},
		ProbeStateDigest: "sha256:" + strings.Repeat("1", 64), LiveState: memory.StateSnapshot{}, WitnessedState: []memory.DerivedRecord{}, DerivedRecords: []memory.DerivedRecord{},
		AggregationCoverage: memory.ProjectAggregationCoverage{}, AssociatedUsage: []memory.AssociatedUsage{}, DependencyDigest: "sha256:" + strings.Repeat("2", 64), ReducerVersion: "v1",
	}
	var err error
	projectView.Digest, err = memory.ProjectViewDigest(projectView)
	if err != nil {
		t.Fatal(err)
	}
	source := memory.GenerationManifest{
		SchemaVersion: memory.MemorySchemaVersion, GenerationID: "generation-source", ProjectID: projectView.ProjectID, CreatedAt: projectView.EndedAt,
		SourceRecordDigests: []string{}, SessionViews: []memory.SessionViewDependency{}, SessionLineages: []memory.SessionLineageDependency{},
		ProbeStateDigest: projectView.ProbeStateDigest, ProbeCheck: memory.ProbeCheck{SchemaVersion: memory.MemorySchemaVersion, CheckedAt: projectView.EndedAt, StateDigest: projectView.ProbeStateDigest, Available: true, Diagnostics: []memory.Diagnostic{}},
		ProjectViewDigest: projectView.Digest,
	}
	sourceBefore := source
	manifestDigest, err := memory.Digest(source)
	if err != nil {
		t.Fatal(err)
	}
	input := BindingSuccessorInput{SourceManifest: source, SourceManifestDigest: manifestDigest, ProjectView: projectView, SessionViews: map[sessionindex.SessionKey]*memory.SessionView{}}
	first, err := BuildBindingSuccessor(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildBindingSuccessor(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(source, sourceBefore) {
		t.Fatalf("successor drifted or mutated source:\nfirst=%+v\nsecond=%+v\nsource=%+v", first, second, source)
	}
	if !strings.HasPrefix(first.Manifest.GenerationID, "migration-") || first.Index.GenerationID != first.Manifest.GenerationID || first.Manifest.SessionIndexDigest != first.Index.Digest {
		t.Fatalf("successor binding = %+v index=%+v", first.Manifest, first.Index)
	}
	if first.SourceManifestDigest != manifestDigest || first.TargetManifestDigest == "" || first.SeedIndexDigest == "" || first.Manifest.GenerationID == source.GenerationID {
		t.Fatalf("successor proof = %+v", first)
	}
}

// The old public index is journal-authenticated bytes, not private source
// history. It may authorize a successor only when every row and fact is
// reproducible from the authenticated manifest dependencies.
func TestMarkdownMigrationBindingSuccessorRejectsUnauthenticatedPublicIndexEntry(t *testing.T) {
	projectView := memory.ProjectView{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: "project-migration", Generation: 1,
		StartedAt: "2026-09-05T00:00:00Z", EndedAt: "2026-09-05T00:00:00Z", SourceSessions: 0,
		TerminalCounts: memory.TerminalCounts{}, SessionViewDependencies: []memory.SessionViewDependency{}, ObservationRevisionIDs: []string{},
		ProbeStateDigest: "sha256:" + strings.Repeat("1", 64), LiveState: memory.StateSnapshot{}, WitnessedState: []memory.DerivedRecord{}, DerivedRecords: []memory.DerivedRecord{},
		AggregationCoverage: memory.ProjectAggregationCoverage{}, AssociatedUsage: []memory.AssociatedUsage{}, DependencyDigest: "sha256:" + strings.Repeat("2", 64), ReducerVersion: "v1",
	}
	var err error
	projectView.Digest, err = memory.ProjectViewDigest(projectView)
	if err != nil {
		t.Fatal(err)
	}
	source := memory.GenerationManifest{
		SchemaVersion: memory.MemorySchemaVersion, GenerationID: "generation-source", ProjectID: projectView.ProjectID, CreatedAt: projectView.EndedAt,
		SourceRecordDigests: []string{}, SessionViews: []memory.SessionViewDependency{}, SessionLineages: []memory.SessionLineageDependency{},
		ProbeStateDigest: projectView.ProbeStateDigest, ProbeCheck: memory.ProbeCheck{SchemaVersion: memory.MemorySchemaVersion, CheckedAt: projectView.EndedAt, StateDigest: projectView.ProbeStateDigest, Available: true, Diagnostics: []memory.Diagnostic{}},
		ProjectViewDigest: projectView.Digest,
	}
	manifestDigest, err := memory.Digest(source)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := sessionindex.Build(sessionindex.BuildInput{ProjectView: projectView, Manifest: source, SessionViews: map[sessionindex.SessionKey]*memory.SessionView{}, GeneratedAt: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	exact := BindingSuccessorInput{SourceManifest: source, SourceManifestDigest: manifestDigest, ProjectView: projectView, SessionViews: map[sessionindex.SessionKey]*memory.SessionView{}, SourceIndex: &seed}
	if _, err := BuildBindingSuccessor(exact); err != nil {
		t.Fatalf("exact reconstructed public index rejected: %v", err)
	}
	extra := seed
	extra.Sessions = []sessionindex.Entry{{
		Provider: "codex", SessionID: "unauthenticated-extra", ProcessingState: sessionindex.ProcessingUnprocessed,
		StateReasonCodes: []string{}, SourceAvailability: "unavailable", Coverage: sessionindex.Coverage{},
	}}
	extra.Coverage = sessionindex.IndexCoverage{Total: 1, Unprocessed: 1, SourceUnavailable: 1}
	body, err := sessionindex.Render(extra)
	if err != nil {
		t.Fatal(err)
	}
	extra, err = sessionindex.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	exact.SourceIndex = &extra
	if _, err := BuildBindingSuccessor(exact); err == nil || !strings.Contains(err.Error(), "cannot be reconstructed") {
		t.Fatalf("unauthenticated public entry was accepted: %v", err)
	}
}
