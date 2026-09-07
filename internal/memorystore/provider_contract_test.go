package memorystore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/memory"
)

func TestProviderContractMixedGenerationPublishesReloadsAndRetainsNamespacedSessions(t *testing.T) {
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	fixture := buildMixedProviderFixture(t, store, "generation-mixed-provider")
	prepared, err := store.PrepareGeneration(fixture.manifest)
	if err != nil {
		t.Fatalf("prepare mixed-provider generation: %v", err)
	}
	proof := memory.PublicationProof{
		ProjectID: testProjectID, GenerationID: fixture.manifest.GenerationID,
		ManifestDigest: prepared.ManifestDigest, ProjectViewDigest: prepared.ProjectViewDigest,
		ReviewSHA256: hexDigest("review"), HistorySHA256: hexDigest("history"), LedgerSHA256: hexDigest("ledger"),
		JournalVerified: true,
	}
	if err := store.CommitPublished(fixture.manifest.GenerationID, proof); err != nil {
		t.Fatalf("publish mixed-provider generation: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenReadOnly(dataRoot, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, err := reopened.LoadPublished()
	if err != nil {
		t.Fatalf("reload published generation: %v", err)
	}
	projectBody, err := reopened.LoadObject(ObjectProjectView, manifest.ProjectViewDigest)
	if err != nil {
		t.Fatalf("reload mixed-provider ProjectView: %v", err)
	}
	var reloadedProject memory.ProjectView
	if err := json.Unmarshal(projectBody, &reloadedProject); err != nil {
		t.Fatal(err)
	}
	if len(manifest.SessionViews) != 2 || len(manifest.SessionLineages) != 2 || len(reloadedProject.AssociatedUsage) != 2 {
		t.Fatalf("mixed-provider counts collapsed: views=%d lineages=%d usage=%d", len(manifest.SessionViews), len(manifest.SessionLineages), len(reloadedProject.AssociatedUsage))
	}

	seen := map[string]bool{}
	for _, dependency := range manifest.SessionViews {
		body, err := reopened.LoadObject(ObjectSessionView, dependency.Digest)
		if err != nil {
			t.Fatalf("load %s SessionView: %v", dependency.Provider, err)
		}
		var view memory.SessionView
		if err := json.Unmarshal(body, &view); err != nil {
			t.Fatal(err)
		}
		if view.Provider != dependency.Provider || view.SessionID != "same-native-id" {
			t.Fatalf("namespaced lookup returned wrong SessionView: dependency=%+v view=%+v", dependency, view)
		}
		seen[view.Provider+"\x00"+view.SessionID] = true
	}
	if !seen["codex\x00same-native-id"] || !seen["claude\x00same-native-id"] || len(seen) != 2 {
		t.Fatalf("provider namespace collapsed on reload: %v", seen)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	retentionStore, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = retentionStore.Close() })
	if report, err := retentionStore.ReportRetention(time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)); err != nil || report.ReachableObjects != 9 || report.CleanupCandidates != 0 {
		t.Fatalf("retention rejected valid mixed-provider snapshot: report=%+v err=%v", report, err)
	}
}

func TestProviderContractRejectsCrossProviderSubstitutionDuringPreparationAndRead(t *testing.T) {
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := buildMixedProviderFixture(t, store, "generation-cross-provider-substitution")

	forgedProject := fixture.project
	forgedProject.SessionViewDependencies = append([]memory.SessionViewDependency(nil), fixture.project.SessionViewDependencies...)
	forgedProject.SessionViewDependencies[1].Digest = forgedProject.SessionViewDependencies[0].Digest
	forgedProject.Digest, err = memory.ProjectViewDigest(forgedProject)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProjectView(forgedProject); err != nil {
		t.Fatalf("put structurally valid forged ProjectView: %v", err)
	}
	forged := fixture.manifest
	forged.SessionViews = append([]memory.SessionViewDependency(nil), fixture.manifest.SessionViews...)
	forged.SessionViews[1].Digest = forged.SessionViews[0].Digest
	forged.ProjectViewDigest = forgedProject.Digest
	if err := memory.ValidateGenerationManifest(forged); err != nil {
		t.Fatalf("forged graph must reach store identity reconciliation: %v", err)
	}
	if _, err := store.PrepareGeneration(forged); err == nil {
		t.Fatal("preparation accepted a cross-provider SessionView substitution")
	}

	manifestDigest, err := memory.Digest(forged)
	if err != nil {
		t.Fatal(err)
	}
	prepared := Prepared{GenerationID: forged.GenerationID, ManifestDigest: manifestDigest, ProjectViewDigest: forged.ProjectViewDigest}
	writeCanonicalJSONForTest(t, filepath.Join(store.memory.Path, "generations", forged.GenerationID+".json"), forged, 0o600)
	writeCanonicalJSONForTest(t, filepath.Join(store.memory.Path, "manifest.json"), prepared, 0o600)
	if _, _, err := store.LoadPrepared(); err == nil {
		t.Fatal("prepared read accepted a cross-provider SessionView substitution")
	}
}

func TestProviderContractRejectsSessionViewWhoseSummarySourceHasAnotherProvider(t *testing.T) {
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := buildMixedProviderFixture(t, store, "generation-summary-provider-mismatch")
	codexViewBody, err := store.LoadObject(ObjectSessionView, fixture.manifest.SessionViews[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	claudeViewBody, err := store.LoadObject(ObjectSessionView, fixture.manifest.SessionViews[1].Digest)
	if err != nil {
		t.Fatal(err)
	}
	var codexView, claudeView memory.SessionView
	if err := json.Unmarshal(codexViewBody, &codexView); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(claudeViewBody, &claudeView); err != nil {
		t.Fatal(err)
	}
	codexRecords, err := store.LoadObservationChunkContext(context.Background(), codexView.ObservationChunkDigests[0])
	if err != nil {
		t.Fatal(err)
	}
	claudeView.ActiveRevisionIDs = []string{codexRecords[0].RevisionID}
	claudeView.ObservationSummaries[0].RevisionID = codexRecords[0].RevisionID
	claudeView.ObservationChunkDigests = append([]string(nil), codexView.ObservationChunkDigests...)
	claudeView.Digest, err = memory.SessionViewDigest(claudeView)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSessionView(claudeView); err != nil {
		t.Fatalf("put independently valid mismatched-source SessionView: %v", err)
	}

	claudeLineageBody, err := store.LoadObject(ObjectSessionLineage, fixture.manifest.SessionLineages[1].Digest)
	if err != nil {
		t.Fatal(err)
	}
	var claudeLineage memory.SessionLineage
	if err := json.Unmarshal(claudeLineageBody, &claudeLineage); err != nil {
		t.Fatal(err)
	}
	claudeLineage.ActiveRevisions = map[string]string{observationKeyDigest(t, codexRecords[0].Key): codexRecords[0].RevisionID}
	claudeLineage.Digest, err = memory.SessionLineageDigest(claudeLineage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSessionLineage(claudeLineage); err != nil {
		t.Fatalf("put independently valid mismatched-source lineage: %v", err)
	}

	forgedProject := fixture.project
	forgedProject.SessionViewDependencies = append([]memory.SessionViewDependency(nil), fixture.project.SessionViewDependencies...)
	forgedProject.SessionViewDependencies[1].Digest = claudeView.Digest
	forgedProject.Digest, err = memory.ProjectViewDigest(forgedProject)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProjectView(forgedProject); err != nil {
		t.Fatal(err)
	}
	forged := fixture.manifest
	forged.SessionViews = append([]memory.SessionViewDependency(nil), forgedProject.SessionViewDependencies...)
	forged.SessionLineages = append([]memory.SessionLineageDependency(nil), fixture.manifest.SessionLineages...)
	forged.SessionLineages[1].Digest = claudeLineage.Digest
	forged.ProjectViewDigest = forgedProject.Digest
	if _, err := store.PrepareGeneration(forged); err == nil {
		t.Fatal("preparation accepted a SessionView summarized from another provider's source")
	}
}

type mixedProviderFixture struct {
	project  memory.ProjectView
	manifest memory.GenerationManifest
}

func buildMixedProviderFixture(t *testing.T, store *Store, generationID string) mixedProviderFixture {
	t.Helper()
	base := buildStoredFixture(t, store, generationID)
	base.session.Provider = "codex"
	base.session.SessionID = "same-native-id"
	base.observation.Key.SessionID = "same-native-id"
	base.observation.Ref.SessionID = "same-native-id"
	base.observation.RevisionID = memory.ObservationRevisionID(base.observation)
	baseChunk, err := store.PutObservationChunk([]memory.ObservationRevision{base.observation})
	if err != nil {
		t.Fatalf("put namespaced Codex observation: %v", err)
	}
	base.session.ActiveRevisionIDs = []string{base.observation.RevisionID}
	base.session.ObservationSummaries[0].RevisionID = base.observation.RevisionID
	base.session.ObservationChunkDigests = []string{baseChunk}
	base.session.Digest, err = memory.SessionViewDigest(base.session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSessionView(base.session); err != nil {
		t.Fatalf("put namespaced Codex SessionView: %v", err)
	}
	base.lineage.SessionID = "same-native-id"
	base.lineage.ActiveRevisions = map[string]string{observationKeyDigest(t, base.observation.Key): base.observation.RevisionID}
	base.lineage.Digest, err = memory.SessionLineageDigest(base.lineage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSessionLineage(base.lineage); err != nil {
		t.Fatalf("put namespaced Codex lineage: %v", err)
	}

	claudeObservation := base.observation
	claudeObservation.Key.Provider = "claude"
	claudeObservation.Key.SourceIdentity = "claude-source"
	claudeObservation.Ref.Provider = "claude"
	claudeObservation.Ref.SourceIdentity = "claude-source"
	claudeObservation.Ref.SourceHash = hexDigest("claude-source")
	claudeObservation.AdapterID = "claude-jsonl"
	claudeObservation.RevisionID = memory.ObservationRevisionID(claudeObservation)
	claudeChunk, err := store.PutObservationChunk([]memory.ObservationRevision{claudeObservation})
	if err != nil {
		t.Fatalf("put Claude observation: %v", err)
	}
	claudeSession := base.session
	claudeSession.Provider = "claude"
	claudeSession.SourceIdentity = "claude-source"
	claudeSession.SourceRecordDigest = prefixedDigest("claude-source-record")
	claudeSession.UsageRecordDigest = prefixedDigest("claude-source-record")
	claudeSession.ActiveRevisionIDs = []string{claudeObservation.RevisionID}
	claudeSession.ObservationSummaries = append([]memory.ObservationSummary(nil), base.session.ObservationSummaries...)
	claudeSession.ObservationSummaries[0].RevisionID = claudeObservation.RevisionID
	claudeSession.ObservationChunkDigests = []string{claudeChunk}
	claudeSession.DependencyDigest = prefixedDigest("claude-session-dependency")
	claudeSession.Digest, err = memory.SessionViewDigest(claudeSession)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSessionView(claudeSession); err != nil {
		t.Fatalf("put Claude SessionView: %v", err)
	}
	claudeLineage := base.lineage
	claudeLineage.Provider = "claude"
	claudeLineage.SourceIdentity = "claude-source"
	claudeLineage.ActiveRevisions = map[string]string{observationKeyDigest(t, claudeObservation.Key): claudeObservation.RevisionID}
	claudeLineage.Digest, err = memory.SessionLineageDigest(claudeLineage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSessionLineage(claudeLineage); err != nil {
		t.Fatalf("put Claude lineage: %v", err)
	}

	project := base.project
	project.SourceSessions = 2
	project.TerminalCounts.Indexed = 2
	project.SessionViewDependencies = []memory.SessionViewDependency{
		{Provider: "codex", SessionID: "same-native-id", Digest: base.session.Digest},
		{Provider: "claude", SessionID: "same-native-id", Digest: claudeSession.Digest},
	}
	project.AggregationCoverage.ObservationSummariesSeen = 2
	project.AggregationCoverage.EventReferences = memory.AggregationChannelCoverage{Seen: 2, Dropped: 2, Truncated: true}
	project.AssociatedUsage = []memory.AssociatedUsage{
		{Provider: "codex", SessionID: "same-native-id", UsageRecordDigest: base.session.UsageRecordDigest},
		{Provider: "claude", SessionID: "same-native-id", UsageRecordDigest: claudeSession.UsageRecordDigest},
	}
	project.Digest, err = memory.ProjectViewDigest(project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProjectView(project); err != nil {
		t.Fatalf("put mixed-provider ProjectView: %v", err)
	}
	manifest := base.manifest
	manifest.SourceRecordDigests = []string{base.session.SourceRecordDigest, claudeSession.SourceRecordDigest}
	manifest.SessionViews = append([]memory.SessionViewDependency(nil), project.SessionViewDependencies...)
	manifest.SessionLineages = []memory.SessionLineageDependency{
		{Provider: "codex", SessionID: "same-native-id", Digest: base.lineage.Digest},
		{Provider: "claude", SessionID: "same-native-id", Digest: claudeLineage.Digest},
	}
	manifest.ProjectViewDigest = project.Digest
	return mixedProviderFixture{project: project, manifest: manifest}
}
