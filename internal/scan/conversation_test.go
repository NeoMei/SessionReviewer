package scan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/sessionview"
	"github.com/neomei/SessionReviewer/internal/source"
)

func (adapter *fakeAdapter) ReadVisiblePrefix(_ context.Context, record memory.SourceRecord) ([]conversationchain.SourceMessage, conversationchain.VisibleCoverage, error) {
	if !adapter.visibleEnabled {
		return nil, conversationchain.VisibleCoverage{}, &source.UnsupportedCapabilityError{Provider: record.Provider}
	}
	spec := adapter.sources[record.SessionID]
	if spec == nil || spec.record.SourceIdentity != record.SourceIdentity || spec.record.FrozenBoundary.SourceHash != record.FrozenBoundary.SourceHash {
		return nil, conversationchain.VisibleCoverage{}, context.Canceled
	}
	if spec.afterVisible != nil {
		spec.afterVisible()
	}
	if spec.visibleErr != nil {
		return nil, conversationchain.VisibleCoverage{}, spec.visibleErr
	}
	if spec.visibleMessages != nil {
		return append([]conversationchain.SourceMessage(nil), spec.visibleMessages...), spec.visibleCoverage, nil
	}
	lastOrdinal := uint64(record.FrozenBoundary.Location.JSONL.Line)
	for _, revision := range spec.observations {
		if revision.Ref.Location.JSONL != nil && uint64(revision.Ref.Location.JSONL.Line) > lastOrdinal {
			lastOrdinal = uint64(revision.Ref.Location.JSONL.Line)
		}
	}
	messages := []conversationchain.SourceMessage{
		{Role: conversationchain.RoleUser, Text: "question " + record.SessionID, OccurredAt: scanStartedAt, RecordHash: scanHex("visible-user-" + record.SessionID), RecordOrdinal: 1},
		{Role: conversationchain.RoleAssistant, Phase: "final_answer", Text: "answer " + record.SessionID, OccurredAt: scanEndedAt, RecordHash: scanHex("visible-answer-" + record.SessionID), RecordOrdinal: lastOrdinal},
	}
	return messages, conversationchain.VisibleCoverage{SourceRecords: lastOrdinal, VisibleMessages: 2, CapturedMessages: 2, Complete: true}, nil
}

func scanConversationObjectNames(t *testing.T, dataRoot string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataRoot, "projects", scanTestProject, "memory-v1", "conversation-chains"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func scanStoredObjects(t *testing.T, dataRoot string) map[string]string {
	t.Helper()
	root := filepath.Join(dataRoot, "projects", scanTestProject, "memory-v1")
	result := make(map[string]string)
	for _, directory := range []string{"observations", "sessions", "session-lineages", "project-probes", "project-views", "session-indexes", "conversation-chains", "generations"} {
		entries, err := os.ReadDir(filepath.Join(root, directory))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			body, err := os.ReadFile(filepath.Join(root, directory, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			result[filepath.Join(directory, entry.Name())] = string(body)
		}
	}
	return result
}

func TestScanConversationPersistsCanonicalPrivateChainBeforePreparation(t *testing.T) {
	harness := newScanHarness(t)
	harness.adapter.visibleEnabled = true
	spec := harness.addSource(1, memory.Indexed, scanTestProject)
	spec.observations[0].Key.Kind = "verification"
	spec.observations[0].Key.Subject = "go-test"
	spec.observations[0].Operation = "verification"
	spec.observations[0].Outcome = "passed"
	spec.observations[0].Fields = map[string]string{"component": "go:all", "passed": "true", "failed": "false"}
	spec.observations[0].Excerpt = "TOOL-OUTPUT-MUST-NOT-PERSIST"
	spec.observations[0].RevisionID = memory.ObservationRevisionID(spec.observations[0])

	result, err := Run(context.Background(), harness.options)
	if err != nil || !result.Prepared {
		t.Fatalf("scan result=%+v err=%v", result, err)
	}
	_, manifest, err := harness.store.LoadPrepared()
	if err != nil || len(manifest.ConversationChains) != 1 {
		t.Fatalf("scan omitted retained chain: roots=%+v err=%v", manifest.ConversationChains, err)
	}
	body, err := harness.store.LoadObject(memorystore.ObjectConversationChain, manifest.ConversationChains[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(`"text":`)) || bytes.Contains(body, []byte("TOOL-OUTPUT-MUST-NOT-PERSIST")) {
		t.Fatalf("private source body leaked into retained representation: %s", body)
	}
	chain, err := conversationchain.Parse(body)
	if err != nil || len(chain.TurnUnits) != 1 || chain.TurnUnits[0].AnswerState != conversationchain.AnswerAnswered || len(chain.TurnUnits[0].Results) != 1 || chain.TurnUnits[0].Results[0].VerificationState != "passed" {
		t.Fatalf("retained causal evidence=%+v err=%v", chain, err)
	}
	if chain.SegmentationRuleVersion != conversationchain.CurrentSegmentationRuleVersion || chain.DependencyProofV1 == nil || chain.DependencyProofV1.RuleVersion != conversationchain.CurrentSegmentationRuleVersion {
		t.Fatalf("current segmentation version was not bound: document=%q proof=%+v", chain.SegmentationRuleVersion, chain.DependencyProofV1)
	}
	if chain.DependencyProofV1 == nil || chain.DependencyProofV1.SourceRecordDigest == "" || !strings.HasPrefix(chain.Digest, "sha256:") {
		t.Fatalf("retained dependency proof=%+v", chain.DependencyProofV1)
	}
}

func TestScanConversationPersistsMaterializationCoverageFromSourceReader(t *testing.T) {
	harness := newScanHarness(t)
	harness.adapter.visibleEnabled = true
	spec := harness.addSource(1, memory.Indexed, scanTestProject)
	spec.visibleMessages = []conversationchain.SourceMessage{
		{Role: conversationchain.RoleAssistant, Phase: "commentary", Text: "orphan", OccurredAt: scanStartedAt, RecordHash: scanHex("orphan"), RecordOrdinal: 1},
		{Role: conversationchain.RoleUser, Text: "question", OccurredAt: scanEndedAt, RecordHash: scanHex("question"), RecordOrdinal: 2},
	}
	spec.visibleCoverage = conversationchain.VisibleCoverage{SourceRecords: 4, VisibleMessages: 2, CapturedMessages: 1, OrphanMessages: 1, MalformedRecords: 1, Complete: false}
	result, err := Run(context.Background(), harness.options)
	if err != nil || !result.Prepared {
		t.Fatalf("scan result=%+v err=%v", result, err)
	}
	_, manifest, err := harness.store.LoadPrepared()
	if err != nil || len(manifest.ConversationChains) != 1 {
		t.Fatalf("coverage roots=%+v err=%v", manifest.ConversationChains, err)
	}
	body, err := harness.store.LoadObject(memorystore.ObjectConversationChain, manifest.ConversationChains[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := conversationchain.Parse(body)
	coverage := chain.MaterializationCoverageV1
	if err != nil || coverage == nil || coverage.SourceRecords != 4 || coverage.VisibleMessages != 2 || coverage.CapturedMessages != 1 || coverage.OrphanMessages != 1 || coverage.MalformedRecords != 1 || coverage.Complete || !coverage.SourceIncomplete || len(chain.TurnUnits) != 1 || chain.TurnUnits[0].AnswerState != conversationchain.AnswerNone {
		t.Fatalf("retained coverage=%+v turns=%+v err=%v", coverage, chain.TurnUnits, err)
	}
}

func TestScanConversationLifecycleRetainsHistoryAndRestoresCurrentRoot(t *testing.T) {
	harness := newScanHarness(t)
	harness.adapter.visibleEnabled = true
	spec := harness.addSource(1, memory.Indexed, scanTestProject)

	first, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatal(err)
	}
	_, firstManifest, err := harness.store.LoadPrepared()
	if err != nil || len(firstManifest.ConversationChains) != 1 {
		t.Fatalf("initial roots=%+v err=%v", firstManifest.ConversationChains, err)
	}
	firstRoot := firstManifest.ConversationChains[0]
	firstBody, err := harness.store.LoadObject(memorystore.ObjectConversationChain, firstRoot.Digest)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := Run(context.Background(), harness.options)
	if err != nil || repeat.GenerationID != first.GenerationID {
		t.Fatalf("identical replay first=%+v repeat=%+v err=%v", first, repeat, err)
	}
	_, repeatManifest, err := harness.store.LoadPrepared()
	if err != nil || !reflect.DeepEqual(repeatManifest.ConversationChains, firstManifest.ConversationChains) || len(repeatManifest.RetainedConversationChains) != 0 {
		t.Fatalf("identical replay roots=%+v retained=%+v err=%v", repeatManifest.ConversationChains, repeatManifest.RetainedConversationChains, err)
	}
	repeatBody, err := harness.store.LoadObject(memorystore.ObjectConversationChain, firstRoot.Digest)
	if err != nil || !bytes.Equal(firstBody, repeatBody) {
		t.Fatalf("identical chain bytes drifted: err=%v", err)
	}

	appended := spec.observations[0]
	appended.Key.Sequence = 3
	appended.Key.Subject = "appended-file"
	appended.Ref.Location.JSONL = &memory.JSONLSourceLocation{Line: 3, ByteOffset: 80}
	appended.Ref.SourceHash = scanHex("appended-file")
	appended.Object = "appended-file"
	appended.Fields = map[string]string{"path": "appended-file"}
	appended.RevisionID = memory.ObservationRevisionID(appended)
	spec.observations = append(spec.observations, appended)
	spec.report.BoundaryRelation = source.BoundaryAppend
	spec.boundary.Frozen.Location.JSONL = &memory.JSONLSourceLocation{Line: 4, ByteOffset: 180}
	spec.boundary.Frozen.SourceHash = scanHex("appended-boundary")
	spec.record.FrozenBoundary = spec.boundary.Frozen
	spec.visibleMessages = []conversationchain.SourceMessage{
		{Role: conversationchain.RoleUser, Text: "first question", OccurredAt: scanStartedAt, RecordHash: scanHex("visible-user-1"), RecordOrdinal: 1},
		{Role: conversationchain.RoleAssistant, Phase: "final_answer", Text: "first answer", OccurredAt: scanStartedAt, RecordHash: scanHex("visible-answer-1"), RecordOrdinal: 2},
		{Role: conversationchain.RoleUser, Text: "next question", OccurredAt: scanEndedAt, RecordHash: scanHex("visible-user-2"), RecordOrdinal: 3},
		{Role: conversationchain.RoleAssistant, Phase: "final_answer", Text: "next answer", OccurredAt: scanEndedAt, RecordHash: scanHex("visible-answer-2"), RecordOrdinal: 4},
	}
	spec.visibleCoverage = conversationchain.VisibleCoverage{SourceRecords: 4, VisibleMessages: 4, CapturedMessages: 4, Complete: true}
	second, err := Run(context.Background(), harness.options)
	if err != nil || second.GenerationID == first.GenerationID {
		t.Fatalf("append result=%+v err=%v", second, err)
	}
	_, secondManifest, err := harness.store.LoadPrepared()
	if err != nil || len(secondManifest.ConversationChains) != 1 || len(secondManifest.RetainedConversationChains) != 1 || secondManifest.RetainedConversationChains[0] != firstRoot {
		t.Fatalf("append roots current=%+v retained=%+v err=%v", secondManifest.ConversationChains, secondManifest.RetainedConversationChains, err)
	}
	secondRoot := secondManifest.ConversationChains[0]
	if secondRoot.Digest == firstRoot.Digest || secondRoot.SessionViewDigest == firstRoot.SessionViewDigest {
		t.Fatalf("append reused old root: first=%+v second=%+v", firstRoot, secondRoot)
	}
	secondBody, err := harness.store.LoadObject(memorystore.ObjectConversationChain, secondRoot.Digest)
	if err != nil {
		t.Fatal(err)
	}
	secondDocument, err := conversationchain.Parse(secondBody)
	if err != nil || len(secondDocument.TurnUnits) != 2 || secondDocument.TurnUnits[1].AnswerState != conversationchain.AnswerAnswered {
		t.Fatalf("append chain units=%+v err=%v", secondDocument.TurnUnits, err)
	}
	if _, err := harness.store.LoadObject(memorystore.ObjectConversationChain, firstRoot.Digest); err != nil {
		t.Fatalf("historical chain unreachable after append: %v", err)
	}

	delete(harness.adapter.sources, spec.record.SessionID)
	harness.adapter.issues = []source.Issue{{Provider: "codex", SessionID: spec.record.SessionID, Code: "missing_segment", TerminalState: memory.Missing}}
	missing, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatal(err)
	}
	_, missingManifest, err := harness.store.LoadPrepared()
	if err != nil || len(missingManifest.ConversationChains) != 0 || len(missingManifest.RetainedConversationChains) != 2 || !scanHasConversationRoot(missingManifest.RetainedConversationChains, firstRoot) || !scanHasConversationRoot(missingManifest.RetainedConversationChains, secondRoot) {
		t.Fatalf("missing source lost authenticated roots: current=%+v retained=%+v err=%v", missingManifest.ConversationChains, missingManifest.RetainedConversationChains, err)
	}
	if _, err := harness.store.LoadObject(memorystore.ObjectConversationChain, secondRoot.Digest); err != nil {
		t.Fatalf("newest historical chain unreachable after source loss: %v", err)
	}
	missingAgain, err := Run(context.Background(), harness.options)
	if err != nil || missingAgain.GenerationID != missing.GenerationID {
		t.Fatalf("unchanged missing replay drifted: first=%+v again=%+v err=%v", missing, missingAgain, err)
	}

	harness.adapter.sources[spec.record.SessionID] = spec
	harness.adapter.issues = nil
	spec.report.BoundaryRelation = source.BoundaryReplacement
	restored, err := Run(context.Background(), harness.options)
	if err != nil || restored.GenerationID == "" {
		t.Fatalf("restored source result=%+v err=%v", restored, err)
	}
	_, restoredManifest, err := harness.store.LoadPrepared()
	if err != nil || len(restoredManifest.ConversationChains) != 1 || restoredManifest.ConversationChains[0] != secondRoot || len(restoredManifest.RetainedConversationChains) != 1 || restoredManifest.RetainedConversationChains[0] != firstRoot {
		t.Fatalf("restored roots current=%+v retained=%+v err=%v", restoredManifest.ConversationChains, restoredManifest.RetainedConversationChains, err)
	}
}

func TestReconcileConversationChainsReplacesSameSessionViewRuleSuccessorWithoutAmbiguity(t *testing.T) {
	viewDigest := "sha256:" + strings.Repeat("a", 64)
	legacy := memory.ConversationChainDependency{Provider: "codex", SessionID: "session-1", SessionViewDigest: viewDigest, Digest: "sha256:" + strings.Repeat("b", 64)}
	current := memory.ConversationChainDependency{Provider: "codex", SessionID: "session-1", SessionViewDigest: viewDigest, Digest: "sha256:" + strings.Repeat("c", 64)}
	previous := baseline{manifest: memory.GenerationManifest{ConversationChains: []memory.ConversationChainDependency{legacy}}}
	manifest := memory.GenerationManifest{
		SessionViews:       []memory.SessionViewDependency{{Provider: "codex", SessionID: "session-1", Digest: viewDigest}},
		ConversationChains: []memory.ConversationChainDependency{current},
	}

	reconcileConversationChains(previous, &manifest)
	if !reflect.DeepEqual(manifest.ConversationChains, []memory.ConversationChainDependency{current}) || len(manifest.RetainedConversationChains) != 0 {
		t.Fatalf("same-view rule successor became ambiguous: current=%+v retained=%+v", manifest.ConversationChains, manifest.RetainedConversationChains)
	}
	if previous.manifest.ConversationChains[0] != legacy {
		t.Fatal("reconciliation mutated the immutable prior generation reference")
	}
}

func TestScanConversationSegmentationUpgradeRetainsHistoricalViewAndChainAndThenStabilizes(t *testing.T) {
	harness := newScanHarness(t)
	harness.adapter.visibleEnabled = true
	harness.addSource(1, memory.Indexed, scanTestProject)
	legacyMaterializer := sessionview.MaterializerVersion + "-" + conversationchain.NotificationSegmentationRuleVersion
	harness.options.Materialize = func(input sessionview.Input) (memory.SessionView, bool, error) {
		input.MaterializerVersion = legacyMaterializer
		return sessionview.Materialize(input)
	}

	first, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatal(err)
	}
	prepared, firstManifest, err := harness.store.LoadPrepared()
	if err != nil || len(firstManifest.SessionViews) != 1 || len(firstManifest.ConversationChains) != 1 {
		t.Fatalf("legacy baseline manifest=%+v err=%v", firstManifest, err)
	}
	firstView, firstRoot := firstManifest.SessionViews[0], firstManifest.ConversationChains[0]
	firstBody, err := harness.store.LoadObject(memorystore.ObjectConversationChain, firstRoot.Digest)
	if err != nil {
		t.Fatal(err)
	}
	firstChain, err := conversationchain.Parse(firstBody)
	if err != nil || firstChain.SegmentationRuleVersion != conversationchain.NotificationSegmentationRuleVersion {
		t.Fatalf("legacy chain version=%q err=%v", firstChain.SegmentationRuleVersion, err)
	}
	proof := memory.PublicationProof{
		Version: 4, ProjectID: scanTestProject, GenerationID: first.GenerationID,
		ManifestDigest: prepared.ManifestDigest, ProjectViewDigest: prepared.ProjectViewDigest,
		ReviewSHA256: strings.Repeat("1", 64), HistorySHA256: strings.Repeat("2", 64), LedgerSHA256: strings.Repeat("3", 64),
		SessionIndexSHA256: strings.TrimPrefix(firstManifest.SessionIndexDigest, "sha256:"), JournalVerified: true,
	}
	if err := harness.store.CommitPublished(first.GenerationID, proof); err != nil {
		t.Fatal(err)
	}

	harness.options.Materialize = sessionview.Materialize
	second, err := Run(context.Background(), harness.options)
	if err != nil || second.GenerationID == first.GenerationID {
		t.Fatalf("segmentation upgrade result=%+v err=%v", second, err)
	}
	_, secondManifest, err := harness.store.LoadPrepared()
	if err != nil || len(secondManifest.SessionViews) != 1 || len(secondManifest.ConversationChains) != 1 || !scanHasConversationRoot(secondManifest.RetainedConversationChains, firstRoot) {
		t.Fatalf("upgrade did not retain historical roots: current=%+v/%+v retained=%+v/%+v err=%v", secondManifest.SessionViews, secondManifest.ConversationChains, secondManifest.RetainedSessionViews, secondManifest.RetainedConversationChains, err)
	}
	secondViewBody, err := harness.store.LoadObject(memorystore.ObjectSessionView, secondManifest.SessionViews[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	var secondView memory.SessionView
	if err := json.Unmarshal(secondViewBody, &secondView); err != nil {
		t.Fatal(err)
	}
	if secondManifest.SessionViews[0].Digest == firstView.Digest || secondManifest.ConversationChains[0].SessionViewDigest == firstRoot.SessionViewDigest || secondView.MaterializerVersion != scanSessionViewMaterializerVersion {
		t.Fatalf("segmentation upgrade reused historical view: before=%+v after=%+v", firstView, secondManifest.SessionViews[0])
	}
	secondBody, err := harness.store.LoadObject(memorystore.ObjectConversationChain, secondManifest.ConversationChains[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	secondChain, err := conversationchain.Parse(secondBody)
	if err != nil || secondChain.SegmentationRuleVersion != conversationchain.CurrentSegmentationRuleVersion {
		t.Fatalf("current chain version=%q err=%v", secondChain.SegmentationRuleVersion, err)
	}
	retainedBody, err := harness.store.LoadObject(memorystore.ObjectConversationChain, firstRoot.Digest)
	if err != nil || !bytes.Equal(retainedBody, firstBody) {
		t.Fatalf("historical chain bytes changed: err=%v", err)
	}

	repeat, err := Run(context.Background(), harness.options)
	if err != nil || repeat.GenerationID != second.GenerationID {
		t.Fatalf("post-upgrade repeat result=%+v err=%v", repeat, err)
	}
	_, repeatManifest, err := harness.store.LoadPrepared()
	if err != nil || !reflect.DeepEqual(repeatManifest.ConversationChains, secondManifest.ConversationChains) || !reflect.DeepEqual(repeatManifest.RetainedConversationChains, secondManifest.RetainedConversationChains) {
		t.Fatalf("post-upgrade repeat drifted: before=%+v after=%+v err=%v", secondManifest, repeatManifest, err)
	}
}

func scanHasConversationRoot(values []memory.ConversationChainDependency, want memory.ConversationChainDependency) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestScanConversationSupersessionUsesActiveRevisionBodies(t *testing.T) {
	harness := newScanHarness(t)
	harness.adapter.visibleEnabled = true
	spec := harness.addSource(1, memory.Indexed, scanTestProject)
	if _, err := Run(context.Background(), harness.options); err != nil {
		t.Fatal(err)
	}
	_, firstManifest, _ := harness.store.LoadPrepared()
	firstRoot := firstManifest.ConversationChains[0]

	successor := spec.observations[0]
	successor.AdapterVersion = "v2"
	successor.Fields = map[string]string{"path": "file-1", "file_hash": scanHex("successor")}
	successor.RevisionID = memory.ObservationRevisionID(successor)
	spec.observations = []memory.ObservationRevision{successor}
	spec.report.BoundaryRelation = source.BoundaryReplacement
	spec.boundary.Frozen.SourceHash = scanHex("replacement-boundary")
	spec.record.FrozenBoundary = spec.boundary.Frozen
	if _, err := Run(context.Background(), harness.options); err != nil {
		t.Fatal(err)
	}
	_, manifest, err := harness.store.LoadPrepared()
	if err != nil || len(manifest.ConversationChains) != 1 || len(manifest.RetainedConversationChains) != 1 {
		t.Fatalf("supersession roots=%+v retained=%+v err=%v", manifest.ConversationChains, manifest.RetainedConversationChains, err)
	}
	body, err := harness.store.LoadObject(memorystore.ObjectConversationChain, manifest.ConversationChains[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	document, err := conversationchain.Parse(body)
	if err != nil || document.DependencyProofV1 == nil || !reflect.DeepEqual(document.DependencyProofV1.ActiveRevisionIDs, []string{successor.RevisionID}) || manifest.RetainedConversationChains[0] != firstRoot {
		t.Fatalf("supersession chain=%+v retained=%+v err=%v", document.DependencyProofV1, manifest.RetainedConversationChains, err)
	}
}

func TestScanConversationCancellationAfterMaterializationPersistsNoChainOrPointer(t *testing.T) {
	harness := newScanHarness(t)
	harness.adapter.visibleEnabled = true
	spec := harness.addSource(1, memory.Indexed, scanTestProject)
	ctx, cancel := context.WithCancel(context.Background())
	spec.afterVisible = cancel
	result, err := Run(ctx, harness.options)
	if !errors.Is(err, context.Canceled) || result.Prepared {
		t.Fatalf("cancelled scan result=%+v err=%v", result, err)
	}
	if names := scanConversationObjectNames(t, harness.options.DataRoot); len(names) != 0 {
		t.Fatalf("cancellation persisted chain objects: %v", names)
	}
	if _, _, err := harness.store.LoadPrepared(); !errors.Is(err, memorystore.ErrNoPreparedGeneration) {
		t.Fatalf("cancellation advanced prepared pointer: %v", err)
	}
}

func TestScanConversationUnsupportedCapabilityPersistsPartialReason(t *testing.T) {
	harness := newScanHarness(t)
	harness.adapter.visibleEnabled = false
	harness.addSource(1, memory.Indexed, scanTestProject)
	result, err := Run(context.Background(), harness.options)
	if err != nil || !result.Prepared || result.State != CompletedWithIssues {
		t.Fatalf("unsupported retained reader result=%+v err=%v", result, err)
	}
	_, manifest, err := harness.store.LoadPrepared()
	if err != nil || len(manifest.ConversationChains) != 0 {
		t.Fatalf("unsupported reader forged chain=%+v err=%v", manifest.ConversationChains, err)
	}
	view := loadScanSessionView(t, harness.store, manifest.SessionViews[0])
	if len(view.Diagnostics) != 1 || view.Diagnostics[0].Code != "visible_reader_unsupported" {
		t.Fatalf("persistent visible-reader diagnostic=%+v", view.Diagnostics)
	}
	body, err := harness.store.LoadObject(memorystore.ObjectSessionIndex, manifest.SessionIndexDigest)
	if err != nil {
		t.Fatal(err)
	}
	index, err := sessionindex.Parse(body)
	if err != nil || len(index.Sessions) != 1 || index.Sessions[0].ProcessingState != sessionindex.ProcessingPartial || index.Sessions[0].WarningCount != 1 {
		t.Fatalf("persistent unsupported index=%+v err=%v", index.Sessions, err)
	}
}

type cancelAfterConversationStore struct {
	MemoryStore
	cancel context.CancelFunc
}

func (store cancelAfterConversationStore) PutConversationChain(document conversationchain.Document) (string, error) {
	digest, err := store.MemoryStore.PutConversationChain(document)
	store.cancel()
	return digest, err
}

func TestScanConversationCancellationBeforePreparedAdvanceKeepsPriorPointer(t *testing.T) {
	harness := newScanHarness(t)
	harness.adapter.visibleEnabled = true
	spec := harness.addSource(1, memory.Indexed, scanTestProject)
	baseline, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatal(err)
	}
	before := scanConversationObjectNames(t, harness.options.DataRoot)

	spec.report.BoundaryRelation = source.BoundaryReplacement
	spec.boundary.Frozen.SourceHash = scanHex("cancel-before-advance")
	spec.record.FrozenBoundary = spec.boundary.Frozen
	ctx, cancel := context.WithCancel(context.Background())
	harness.options.Store = cancelAfterConversationStore{MemoryStore: harness.store, cancel: cancel}
	result, err := Run(ctx, harness.options)
	if !errors.Is(err, context.Canceled) || result.Prepared {
		t.Fatalf("cancelled successor result=%+v err=%v", result, err)
	}
	prepared, manifest, err := harness.store.LoadPrepared()
	if err != nil || prepared.GenerationID != baseline.GenerationID || manifest.GenerationID != baseline.GenerationID {
		t.Fatalf("cancellation advanced prepared pointer: prepared=%+v manifest=%+v err=%v", prepared, manifest, err)
	}
	after := scanConversationObjectNames(t, harness.options.DataRoot)
	if len(after) != len(before)+1 {
		t.Fatalf("expected one immutable orphan before cancelled advance: before=%v after=%v", before, after)
	}
}

func TestScanConversationCumulativeByteBudgetLeavesPublishedStateAndObjectsUnchanged(t *testing.T) {
	harness := newScanHarness(t)
	harness.adapter.visibleEnabled = true
	harness.addSource(1, memory.Indexed, scanTestProject)
	first, err := Run(context.Background(), harness.options)
	if err != nil {
		t.Fatal(err)
	}
	preparedBefore, manifestBefore, err := harness.store.LoadPrepared()
	if err != nil || len(manifestBefore.ConversationChains) != 1 {
		t.Fatalf("baseline prepared=%+v chains=%+v err=%v", preparedBefore, manifestBefore.ConversationChains, err)
	}
	firstBody, err := harness.store.LoadObject(memorystore.ObjectConversationChain, manifestBefore.ConversationChains[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	proof := memory.PublicationProof{
		Version: 4, ProjectID: scanTestProject, GenerationID: first.GenerationID,
		ManifestDigest: preparedBefore.ManifestDigest, ProjectViewDigest: preparedBefore.ProjectViewDigest,
		ReviewSHA256: strings.Repeat("1", 64), HistorySHA256: strings.Repeat("2", 64), LedgerSHA256: strings.Repeat("3", 64),
		SessionIndexSHA256: strings.TrimPrefix(manifestBefore.SessionIndexDigest, "sha256:"), JournalVerified: true,
	}
	if err := harness.store.CommitPublished(first.GenerationID, proof); err != nil {
		t.Fatal(err)
	}
	objectsBefore := scanStoredObjects(t, harness.options.DataRoot)
	catalogBefore, err := harness.catalog.ListCandidates(scanTestProject)
	if err != nil {
		t.Fatal(err)
	}

	harness.addSource(2, memory.Indexed, scanTestProject)
	harness.options.conversationChainBudgetBytes = int64(len(firstBody))
	result, err := Run(context.Background(), harness.options)
	if !errors.Is(err, ErrConversationChainBudget) || result.Prepared || result.State != Failed {
		t.Fatalf("conversation budget result=%+v err=%v", result, err)
	}
	if !strings.Contains(err.Error(), "used "+strconv.Itoa(len(firstBody))) {
		t.Fatalf("overflow occurred before the first document consumed the exact boundary: %v", err)
	}
	preparedAfter, manifestAfter, err := harness.store.LoadPrepared()
	if err != nil || preparedAfter != preparedBefore || !reflect.DeepEqual(manifestAfter, manifestBefore) {
		t.Fatalf("conversation budget changed prepared state: before=%+v/%+v after=%+v/%+v err=%v", preparedBefore, manifestBefore, preparedAfter, manifestAfter, err)
	}
	publishedAfter, publishedManifest, err := harness.store.LoadPublished()
	if err != nil || publishedAfter != first.GenerationID || !reflect.DeepEqual(publishedManifest, manifestBefore) {
		t.Fatalf("conversation budget changed published state: generation=%q manifest=%+v err=%v", publishedAfter, publishedManifest, err)
	}
	catalogAfter, err := harness.catalog.ListCandidates(scanTestProject)
	if err != nil || !reflect.DeepEqual(catalogAfter, catalogBefore) {
		t.Fatalf("conversation budget changed catalog: before=%+v after=%+v err=%v", catalogBefore, catalogAfter, err)
	}
	if objectsAfter := scanStoredObjects(t, harness.options.DataRoot); !reflect.DeepEqual(objectsAfter, objectsBefore) {
		t.Fatalf("conversation budget changed CAS objects: before=%v after=%v", objectsBefore, objectsAfter)
	}
}
