package publication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
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

func TestPublishFourFilePlanVerifiesSessionIndexBeforePointerCommit(t *testing.T) {
	projectID := "project-four-file"
	dataRoot, projectRoot, vaultRoot, mapping, manifest, plan := setupPublishEnv(t, projectID)
	// A v4 review is JSON, not a syncdoc Markdown document. If the generic
	// four-file path accidentally delegates to the legacy sync engine this
	// publication fails before the Vault files can be verified.
	plan.Files[0].Desired = []byte("{\"schema_version\":4}\n")
	indexBody := []byte("{\"schema_version\":1}\n")
	plan.Files = append(plan.Files, presentation.FilePlan{
		Relative: "docs/session-review/.session-reviewer/session-index.json", Desired: indexBody, Mode: 0o600,
	})
	result, err := Publish(context.Background(), Options{
		ProjectID: projectID, PreparedGeneration: manifest.GenerationID, Plan: plan,
		Mapping: mapping, DataRoot: dataRoot, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ProjectFiles) != 4 || len(result.VaultFiles) != 4 {
		t.Fatalf("verified files = %d/%d", len(result.ProjectFiles), len(result.VaultFiles))
	}
	for _, target := range []string{
		filepath.Join(projectRoot, filepath.FromSlash("docs/session-review/.session-reviewer/session-index.json")),
		filepath.Join(vaultRoot, filepath.FromSlash(mapping.VaultReviewPath), ".session-reviewer", "session-index.json"),
	} {
		got, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(got, indexBody) {
			t.Fatalf("session index at %s = %q, %v", target, got, err)
		}
	}
	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if published, _, err := store.LoadPublished(); err != nil || published != manifest.GenerationID {
		t.Fatalf("published pointer = %q, %v", published, err)
	}
}

// Moving the published-generation commit above complete destination
// verification makes this test expose a new generation while the public atom
// has already been rolled back to its old preimages.
func TestPublishFourFilePlanCommitsPointerLast(t *testing.T) {
	projectID := "project-pointer-last"
	dataRoot, projectRoot, vaultRoot, mapping, manifest, plan := setupPublishEnv(t, projectID)
	for index := range plan.Files {
		plan.Files[index].Desired = []byte(fmt.Sprintf("v4-target-%d\n", index))
	}
	plan.Files = append(plan.Files, presentation.FilePlan{
		Relative: "docs/session-review/.session-reviewer/session-index.json",
		Desired:  []byte("v4-target-index\n"), Mode: 0o600,
	})
	stop := errors.New("stop before published pointer")
	opts := Options{
		ProjectID: projectID, PreparedGeneration: manifest.GenerationID, Plan: plan,
		Mapping: mapping, DataRoot: dataRoot, Now: time.Now,
		checkpoint: func(stage publishCheckpoint, side, relative string) error {
			if stage == checkpointBeforePointerCommit {
				return stop
			}
			return nil
		},
	}
	if _, err := Publish(context.Background(), opts); !errors.Is(err, stop) {
		t.Fatalf("Publish error = %v, want checkpoint error", err)
	}
	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.LoadPublished(); !errors.Is(err, memorystore.ErrNoPublishedGeneration) {
		t.Fatalf("published pointer advanced before final checkpoint: %v", err)
	}
	for _, file := range plan.Files {
		for _, target := range []string{
			filepath.Join(projectRoot, filepath.FromSlash(file.Relative)),
			filepath.Join(vaultRoot, filepath.FromSlash(vaultRelativePath(mapping.VaultReviewPath, file.Relative))),
		} {
			if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rolled-back target %s remains: %v", target, err)
			}
		}
	}
}

func TestPublishFourFilePlanRecoversGenericIntentAfterRestart(t *testing.T) {
	projectID := "project-four-file-recovery"
	dataRoot, projectRoot, vaultRoot, mapping, manifest, plan := setupPublishEnv(t, projectID)
	plan.Files = append(plan.Files, presentation.FilePlan{
		Relative: sessionIndexRelativePath, Desired: []byte("v4-index-target\n"), Mode: 0o600,
	})
	for index := range plan.Files {
		old := []byte(fmt.Sprintf("old-four-file-%d\n", index))
		plan.Files[index].ExpectedExists = true
		plan.Files[index].Expected = old
		plan.Files[index].Desired = []byte(fmt.Sprintf("new-four-file-%d\n", index))
		for _, target := range []string{
			filepath.Join(projectRoot, filepath.FromSlash(plan.Files[index].Relative)),
			filepath.Join(vaultRoot, filepath.FromSlash(vaultRelativePath(mapping.VaultReviewPath, plan.Files[index].Relative))),
		} {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, old, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, _, err := store.LoadPrepared()
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := OpenJournal(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	destinations := make([]Destination, 0, len(plan.Files)*2)
	for _, file := range plan.Files {
		preimageSHA := sha256Hex(file.Expected)
		if err := j.PutPreimage(preimageSHA, file.Expected); err != nil {
			j.Close()
			t.Fatal(err)
		}
		destinations = append(destinations,
			Destination{Side: "project", Relative: file.Relative, PreimageSHA256: preimageSHA, DesiredSHA256: sha256Hex(file.Desired), PreimageExists: true},
			Destination{Side: "vault", Relative: vaultRelativePath(mapping.VaultReviewPath, file.Relative), PreimageSHA256: preimageSHA, DesiredSHA256: sha256Hex(file.Desired), PreimageExists: true},
		)
	}
	sort.Slice(destinations, func(i, j int) bool {
		if destinations[i].Side != destinations[j].Side {
			return destinations[i].Side < destinations[j].Side
		}
		return destinations[i].Relative < destinations[j].Relative
	})
	intent := Intent{
		Version: 1, ProjectID: projectID, GenerationID: manifest.GenerationID,
		ManifestDigest: prepared.ManifestDigest, ProjectViewDigest: prepared.ProjectViewDigest,
		Stage: StagePrepared, CreatedAt: time.Now().UTC(), Destinations: destinations,
	}
	if err := j.Create(intent); err != nil {
		j.Close()
		t.Fatal(err)
	}
	// Model a process exit after all Project writes and two Vault writes. The
	// next Publish invocation must recover from the generic destination list;
	// no legacy fixed three-file journal participates.
	for _, file := range plan.Files {
		if err := os.WriteFile(filepath.Join(projectRoot, filepath.FromSlash(file.Relative)), file.Desired, file.Mode); err != nil {
			j.Close()
			t.Fatal(err)
		}
	}
	if err := j.Advance(StagePrepared, StageProjectWritten); err != nil {
		j.Close()
		t.Fatal(err)
	}
	for _, file := range plan.Files[:2] {
		if err := os.WriteFile(filepath.Join(vaultRoot, filepath.FromSlash(vaultRelativePath(mapping.VaultReviewPath, file.Relative))), file.Desired, file.Mode); err != nil {
			j.Close()
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := Publish(context.Background(), Options{
		ProjectID: projectID, PreparedGeneration: manifest.GenerationID, Plan: plan,
		Mapping: mapping, DataRoot: dataRoot, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Recovered || len(result.ProjectFiles) != 4 || len(result.VaultFiles) != 4 {
		t.Fatalf("recovery result = %+v", result)
	}
	for _, file := range plan.Files {
		for _, target := range []string{
			filepath.Join(projectRoot, filepath.FromSlash(file.Relative)),
			filepath.Join(vaultRoot, filepath.FromSlash(vaultRelativePath(mapping.VaultReviewPath, file.Relative))),
		} {
			body, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(body, file.Desired) {
				t.Fatalf("recovered target %s = %q, %v", target, body, err)
			}
		}
	}
	store, err = memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	published, publishedManifest, err := store.LoadPublished()
	if err != nil || published != manifest.GenerationID || publishedManifest.GenerationID != manifest.GenerationID {
		t.Fatalf("published recovery = generation %q manifest %+v err %v", published, publishedManifest, err)
	}
	repeated, err := Publish(context.Background(), Options{
		ProjectID: projectID, PreparedGeneration: manifest.GenerationID, Plan: plan,
		Mapping: mapping, DataRoot: dataRoot, Now: time.Now,
	})
	if err != nil {
		t.Fatalf("repeat v4 publication: %v", err)
	}
	if fmt.Sprint(repeated.ProjectFiles) != fmt.Sprint(result.ProjectFiles) || fmt.Sprint(repeated.VaultFiles) != fmt.Sprint(result.VaultFiles) {
		t.Fatalf("repeat v4 publication changed verified hashes: first=%+v/%+v second=%+v/%+v", result.ProjectFiles, result.VaultFiles, repeated.ProjectFiles, repeated.VaultFiles)
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

// Treating a non-converged sync report as success hides the actionable sync
// failure behind a later Vault hash mismatch.
func TestPublishReportsNonConvergedSyncBeforeFinalHashVerification(t *testing.T) {
	projectID := "project-sync-blocked"
	dataRoot, _, vaultRoot, mapping, manifest, plan := setupPublishEnv(t, projectID)
	vaultReview := filepath.Join(vaultRoot, filepath.FromSlash(mapping.VaultReviewPath))
	if err := os.MkdirAll(vaultReview, 0o700); err != nil {
		t.Fatal(err)
	}
	vaultOverview := filepath.Join(vaultReview, "项目回顾.md")
	malformed := []byte("# user-owned malformed review\n")
	if err := os.WriteFile(vaultOverview, malformed, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Publish(context.Background(), Options{
		ProjectID:          projectID,
		PreparedGeneration: manifest.GenerationID,
		Plan:               plan,
		Mapping:            mapping,
		DataRoot:           dataRoot,
		Now:                time.Now,
	})
	if err == nil || !strings.Contains(err.Error(), "sync to vault preflight did not converge") {
		t.Fatalf("Publish() error = %v", err)
	}
	if strings.Contains(err.Error(), "verify published files") {
		t.Fatalf("Publish() hid sync failure behind final hash verification: %v", err)
	}
	if !strings.Contains(err.Error(), "project-overview:malformed_source") {
		t.Fatalf("Publish() omitted the actionable sync entity error: %v", err)
	}
	body, readErr := os.ReadFile(vaultOverview)
	if readErr != nil || !bytes.Equal(body, malformed) {
		t.Fatalf("Vault preimage changed: body=%q err=%v", body, readErr)
	}
	baseFiles, globErr := filepath.Glob(filepath.Join(dataRoot, "projects", projectID, "merge-bases", "*.json"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(baseFiles) != 0 {
		t.Fatalf("failed publication committed merge-base state: %v", baseFiles)
	}
	journal, openErr := OpenJournal(dataRoot, projectID)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer journal.Close()
	intent, loadErr := journal.Load()
	if loadErr != nil || intent.Stage != StageCommitted {
		t.Fatalf("rollback journal stage=%q err=%v", intent.Stage, loadErr)
	}
}

func TestRepairRolledBackBasesRequiresJournalProofAndIdenticalHumanCopies(t *testing.T) {
	projectID := "project-rollback-base"
	dataRoot, projectRoot, vaultRoot, mapping, manifest, plan := setupPublishEnv(t, projectID)
	opts := Options{ProjectID: projectID, PreparedGeneration: manifest.GenerationID, Plan: plan, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}
	if _, err := Publish(context.Background(), opts); err != nil {
		t.Fatalf("initial Publish: %v", err)
	}

	var history []byte
	for _, file := range plan.Files {
		if file.Relative == reviewv2.HistoryRelativePath {
			history = bytes.Clone(file.Desired)
		}
	}
	if len(history) == 0 {
		t.Fatal("history fixture is missing")
	}
	failedDesired := []byte("failed publication desired base\n")
	failedHash := sha256Hex(failedDesired)
	syncRoot, err := os.OpenRoot(filepath.Join(dataRoot, "projects", projectID))
	if err != nil {
		t.Fatal(err)
	}
	defer syncRoot.Close()
	baseStore := syncengine.BaseStore{Root: syncRoot}
	base, found, err := baseStore.Load("project-history")
	if err != nil || !found {
		t.Fatalf("load initial base: found=%t err=%v", found, err)
	}
	failedBase := syncengine.BaseRecord{
		Version: 1, EntityID: base.EntityID, RelativePath: base.RelativePath,
		ContentHash: failedHash, ProjectHash: failedHash, VaultHash: failedHash,
		Content: failedDesired, SyncedAt: time.Now().UTC(),
	}
	if err := baseStore.Commit(base.ContentHash, failedBase); err != nil {
		t.Fatalf("simulate partially committed base: %v", err)
	}

	journal, err := OpenJournal(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	preimageHash := sha256Hex(history)
	if err := journal.PutPreimage(preimageHash, history); err != nil {
		t.Fatal(err)
	}
	intent := Intent{
		Version: 1, ProjectID: projectID, GenerationID: "generation-rollback-proof",
		ManifestDigest: prefixedDigest("rollback-manifest"), ProjectViewDigest: prefixedDigest("rollback-view"),
		Stage: StagePrepared, CreatedAt: time.Now().UTC(),
		Destinations: []Destination{
			{Side: "project", Relative: reviewv2.HistoryRelativePath, PreimageSHA256: preimageHash, DesiredSHA256: failedHash, PreimageExists: true},
			{Side: "vault", Relative: vaultRelativePath(mapping.VaultReviewPath, reviewv2.HistoryRelativePath), PreimageSHA256: preimageHash, DesiredSHA256: failedHash, PreimageExists: true},
		},
	}
	if err := journal.Create(intent); err != nil {
		t.Fatal(err)
	}
	for _, transition := range [][2]Stage{
		{StagePrepared, StageProjectWritten},
		{StageProjectWritten, StageVaultSynced},
		{StageVaultSynced, StageVerified},
		{StageVerified, StageCommitted},
	} {
		if err := journal.Advance(transition[0], transition[1]); err != nil {
			t.Fatal(err)
		}
	}
	projectDir, err := pathguard.Open(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer projectDir.Close()
	vaultDir, err := pathguard.Open(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer vaultDir.Close()
	if err := repairRetainedRollbackEvidence(journal, opts, projectDir, vaultDir, time.Now); err != nil {
		t.Fatalf("repairRetainedRollbackEvidence: %v", err)
	}
	repaired, found, err := baseStore.Load("project-history")
	if err != nil || !found || repaired.ContentHash != preimageHash || !bytes.Equal(repaired.Content, history) {
		t.Fatalf("repaired base mismatch: found=%t hash=%q err=%v", found, repaired.ContentHash, err)
	}
}
