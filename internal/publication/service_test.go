package publication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

func hexDigest(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func prefixedDigest(seed string) string {
	return "sha256:" + hexDigest(seed)
}

func observationKeyDigest(t *testing.T, key memory.ObservationKey) string {
	t.Helper()
	digest, err := memory.Digest(key)
	if err != nil {
		t.Fatalf("digest observation key: %v", err)
	}
	return digest
}

func setupPublishEnv(t *testing.T, projectID string) (string, string, string, config.ProjectMapping, memory.GenerationManifest, presentation.RenderPlan) {
	t.Helper()
	dataRoot := t.TempDir()
	projectRoot := t.TempDir()
	vaultRoot := t.TempDir()

	mapping := config.ProjectMapping{
		ID:              projectID,
		Root:            projectRoot,
		VaultRoot:       vaultRoot,
		VaultReviewPath: "Projects/" + projectID + "/Session Review",
		VaultCaseMode:   platform.CaseSensitive,
	}

	// Initialize config in dataRoot
	cfg := config.Config{
		Version:  1,
		Projects: []config.ProjectMapping{mapping},
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	testStartedAt := "2026-08-31T10:00:00Z"
	testEndedAt := "2026-08-31T10:01:00Z"
	genID := "generation-400000000000001"

	observation := memory.ObservationRevision{
		SchemaVersion: memory.MemorySchemaVersion,
		Key: memory.ObservationKey{
			Provider:       "codex",
			SessionID:      "session-1",
			SourceIdentity: "source-1",
			Sequence:       1,
			ProjectID:      projectID,
			Kind:           "test",
			Subject:        "go test",
		},
		Ref: memory.SourceRef{
			Provider:       "codex",
			SessionID:      "session-1",
			SourceIdentity: "source-1",
			Location: memory.SourceLocation{
				Kind:  memory.SourceLocationJSONL,
				JSONL: &memory.JSONLSourceLocation{Line: 12, ByteOffset: 340},
			},
			SourceHash: hexDigest("source"),
		},
		Timestamp:      testEndedAt,
		Operation:      "run",
		Object:         "focused tests",
		Outcome:        "passed",
		Fields:         map[string]string{"passed": "1", "failed": "0"},
		AdapterID:      "codex-jsonl",
		AdapterVersion: "v1",
	}
	observation.RevisionID = memory.ObservationRevisionID(observation)
	chunkDigest, err := store.PutObservationChunk([]memory.ObservationRevision{observation})
	if err != nil {
		t.Fatalf("put observation chunk: %v", err)
	}

	session := memory.SessionView{
		SchemaVersion:      memory.MemorySchemaVersion,
		ProjectID:          projectID,
		Provider:           "codex",
		SessionID:          "session-1",
		SourceIdentity:     "source-1",
		SourceRecordDigest: prefixedDigest("source-record"),
		UsageRecordDigest:  prefixedDigest("source-record"),
		StartedAt:          testStartedAt,
		EndedAt:            testEndedAt,
		TerminalState:      memory.Indexed,
		SourceAvailability: memory.SourceAvailable,
		ActiveRevisionIDs:  []string{observation.RevisionID},
		ObservationSummaries: []memory.ObservationSummary{{
			RevisionID: observation.RevisionID, Sequence: observation.Key.Sequence,
			Kind: observation.Key.Kind, Subject: observation.Key.Subject, OccurredAt: observation.Timestamp,
			Operation: observation.Operation, Object: observation.Object, Outcome: observation.Outcome,
			Fields: map[string]string{"passed": "1", "failed": "0"},
		}},
		ObservationChunkDigests: []string{chunkDigest},
		DerivedRecords:          []memory.DerivedRecord{},
		Diagnostics:             []memory.Diagnostic{},
		DependencyDigest:        prefixedDigest("session-dependency"),
		MaterializerVersion:     "v1",
	}
	session.Digest, err = memory.SessionViewDigest(session)
	if err != nil {
		t.Fatalf("digest session view: %v", err)
	}
	if _, err := store.PutSessionView(session); err != nil {
		t.Fatalf("put session view: %v", err)
	}

	lineage := memory.SessionLineage{
		SchemaVersion:       memory.MemorySchemaVersion,
		ProjectID:           projectID,
		Provider:            session.Provider,
		SessionID:           session.SessionID,
		SourceIdentity:      session.SourceIdentity,
		ActiveRevisions:     map[string]string{observationKeyDigest(t, observation.Key): observation.RevisionID},
		SupersededRevisions: map[string]string{},
		WithdrawnRevisions:  map[string]string{},
	}
	lineage.Digest, err = memory.SessionLineageDigest(lineage)
	if err != nil {
		t.Fatalf("digest SessionLineage: %v", err)
	}
	if _, err := store.PutSessionLineage(lineage); err != nil {
		t.Fatalf("put SessionLineage: %v", err)
	}

	probe := memory.ProjectProbeState{
		SchemaVersion:           memory.MemorySchemaVersion,
		ProjectID:               projectID,
		CanonicalRoot:           "/private/project",
		Branch:                  "main",
		Head:                    "0123456789abcdef0123456789abcdef01234567",
		DirtyPathCount:          0,
		RemoteIdentityHashes:    []string{prefixedDigest("remote")},
		VersionFiles:            []memory.ProbeFile{},
		RequiredProjectionFiles: []memory.ProbeFile{},
		ProbeVersion:            "v1",
		Diagnostics:             []memory.Diagnostic{},
	}
	probe.Digest, err = memory.ProjectProbeStateDigest(probe)
	if err != nil {
		t.Fatalf("digest probe state: %v", err)
	}
	if _, err := store.PutProbeState(probe); err != nil {
		t.Fatalf("put probe state: %v", err)
	}

	project := memory.ProjectView{
		SchemaVersion:  memory.MemorySchemaVersion,
		ProjectID:      projectID,
		Generation:     1,
		StartedAt:      testStartedAt,
		EndedAt:        testEndedAt,
		SourceSessions: 1,
		TerminalCounts: memory.TerminalCounts{Indexed: 1},
		SessionViewDependencies: []memory.SessionViewDependency{{
			Provider: "codex", SessionID: "session-1", Digest: session.Digest,
		}},
		ObservationRevisionIDs: []string{},
		ProbeStateDigest:       probe.Digest,
		LiveState:              memory.StateSnapshot{Branch: "main", Head: probe.Head},
		WitnessedState:         []memory.DerivedRecord{},
		DerivedRecords:         []memory.DerivedRecord{},
		AggregationCoverage: memory.ProjectAggregationCoverage{
			ObservationSummariesSeen:  1,
			EventReferences:           memory.AggregationChannelCoverage{Seen: 1, Dropped: 1, Truncated: true},
			SelectedEvidenceRevisions: memory.AggregationChannelCoverage{},
		},
		AssociatedUsage:  []memory.AssociatedUsage{},
		DependencyDigest: prefixedDigest("project-dependency"),
		ReducerVersion:   "v1",
	}
	project.Digest, err = memory.ProjectViewDigest(project)
	if err != nil {
		t.Fatalf("digest project view: %v", err)
	}
	if _, err := store.PutProjectView(project); err != nil {
		t.Fatalf("put project view: %v", err)
	}

	manifest := memory.GenerationManifest{
		SchemaVersion:       memory.MemorySchemaVersion,
		GenerationID:        genID,
		ProjectID:           projectID,
		CreatedAt:           testEndedAt,
		SourceRecordDigests: []string{session.SourceRecordDigest},
		SessionViews: []memory.SessionViewDependency{{
			Provider: "codex", SessionID: "session-1", Digest: session.Digest,
		}},
		SessionLineages: []memory.SessionLineageDependency{{
			Provider: "codex", SessionID: "session-1", Digest: lineage.Digest,
		}},
		ProbeStateDigest:  probe.Digest,
		ProbeCheck:        memory.ProbeCheck{SchemaVersion: memory.MemorySchemaVersion, CheckedAt: testEndedAt, StateDigest: probe.Digest, Available: true, Diagnostics: []memory.Diagnostic{}},
		ProjectViewDigest: project.Digest,
	}
	if err := memory.ValidateGenerationManifest(manifest); err != nil {
		t.Fatalf("fixture manifest is invalid: %v", err)
	}
	if _, err := store.PrepareGeneration(manifest); err != nil {
		t.Fatalf("PrepareGeneration: %v", err)
	}

	// Create ProjectView output and RenderPlan
	pOutput, err := presentation.Project(presentation.ProjectInput{
		ProjectView:  project,
		GenerationID: genID,
		Revision:     1,
	})
	if err != nil {
		t.Fatalf("presentation.Project: %v", err)
	}
	plan, err := presentation.Render(presentation.ProjectInput{
		ProjectView:  project,
		GenerationID: genID,
		Revision:     1,
	}, pOutput)
	if err != nil {
		t.Fatalf("presentation.Render: %v", err)
	}

	return dataRoot, projectRoot, vaultRoot, mapping, manifest, plan
}

func TestPublishCleanRunSucceeds(t *testing.T) {
	projectID := "project-clean"
	dataRoot, projectRoot, vaultRoot, mapping, manifest, plan := setupPublishEnv(t, projectID)

	opts := Options{
		ProjectID:          projectID,
		PreparedGeneration: manifest.GenerationID,
		Plan:               plan,
		Mapping:            mapping,
		DataRoot:           dataRoot,
		Now:                time.Now,
	}

	result, err := Publish(context.Background(), opts)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if result.GenerationID != manifest.GenerationID {
		t.Fatalf("expected generation %s, got %s", manifest.GenerationID, result.GenerationID)
	}
	if len(result.ProjectFiles) != 3 || len(result.VaultFiles) != 3 {
		t.Fatalf("expected 3 project and 3 vault files, got %d and %d", len(result.ProjectFiles), len(result.VaultFiles))
	}

	// Verify store published pointer
	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	pubID, pubManifest, err := store.LoadPublished()
	if err != nil || pubID != manifest.GenerationID || pubManifest.GenerationID != manifest.GenerationID {
		t.Fatalf("LoadPublished mismatch: pubID=%s pubManifest=%+v err=%v", pubID, pubManifest, err)
	}

	// Verify files exist in Project and Vault
	reviewPath := filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	if _, err := os.Stat(reviewPath); err != nil {
		t.Fatalf("project review file missing: %v", err)
	}
	vaultReviewPath := filepath.Join(vaultRoot, "Projects", projectID, "Session Review", "项目回顾.md")
	if _, err := os.Stat(vaultReviewPath); err != nil {
		t.Fatalf("vault review file missing: %v", err)
	}

	// Second publish is idempotent with 0 writes
	result2, err := Publish(context.Background(), opts)
	if err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	if result2.GenerationID != manifest.GenerationID {
		t.Fatalf("second publish generation mismatch")
	}
}

func TestPublishPreimageMismatchFailsClosed(t *testing.T) {
	projectID := "project-conflict"
	dataRoot, projectRoot, _, mapping, manifest, plan := setupPublishEnv(t, projectID)

	// Tamper with project file before publish (simulate conflicting existing file)
	reviewPath := filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	if err := os.MkdirAll(filepath.Dir(reviewPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(reviewPath, []byte("# Unexpected Existing Content"), 0o600); err != nil {
		t.Fatalf("write unexpected: %v", err)
	}

	opts := Options{
		ProjectID:          projectID,
		PreparedGeneration: manifest.GenerationID,
		Plan:               plan,
		Mapping:            mapping,
		DataRoot:           dataRoot,
		Now:                time.Now,
	}

	_, err := Publish(context.Background(), opts)
	if !errors.Is(err, ErrPublicationConflict) {
		t.Fatalf("expected ErrPublicationConflict, got %v", err)
	}

	// Verify the unexpected file was not overwritten
	content, err := os.ReadFile(reviewPath)
	if err != nil || !strings.Contains(string(content), "Unexpected Existing Content") {
		t.Fatalf("conflicting file was overwritten or lost: %s", content)
	}
}
