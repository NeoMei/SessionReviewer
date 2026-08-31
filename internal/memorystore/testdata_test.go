package memorystore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/neomei/SessionReviewer/internal/memory"
)

const (
	testProjectID = "project-1"
	testStartedAt = "2026-08-31T10:00:00Z"
	testEndedAt   = "2026-08-31T10:01:00Z"
)

type storedFixture struct {
	observation memory.ObservationRevision
	session     memory.SessionView
	probe       memory.ProjectProbeState
	project     memory.ProjectView
	manifest    memory.GenerationManifest
}

func buildStoredFixture(t *testing.T, store *Store, generationID string) storedFixture {
	t.Helper()

	observation := memory.ObservationRevision{
		SchemaVersion: memory.MemorySchemaVersion,
		Key: memory.ObservationKey{
			Provider:       "codex",
			SessionID:      "session-1",
			SourceIdentity: "source-1",
			Sequence:       1,
			ProjectID:      testProjectID,
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
		ProjectID:          testProjectID,
		Provider:           "codex",
		SessionID:          "session-1",
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

	probe := memory.ProjectProbeState{
		SchemaVersion:           memory.MemorySchemaVersion,
		ProjectID:               testProjectID,
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
		ProjectID:      testProjectID,
		Generation:     1,
		StartedAt:      testStartedAt,
		EndedAt:        testEndedAt,
		SourceSessions: 1,
		TerminalCounts: memory.TerminalCounts{Indexed: 1},
		SessionViewDependencies: []memory.SessionViewDependency{{
			Provider: "codex", SessionID: "session-1", Digest: session.Digest,
		}},
		ObservationRevisionIDs: []string{observation.RevisionID},
		ProbeStateDigest:       probe.Digest,
		LiveState:              memory.StateSnapshot{Branch: "main", Head: probe.Head},
		WitnessedState:         []memory.DerivedRecord{},
		DerivedRecords:         []memory.DerivedRecord{},
		AssociatedUsage:        []memory.AssociatedUsage{},
		DependencyDigest:       prefixedDigest("project-dependency"),
		ReducerVersion:         "v1",
	}
	project.Digest, err = memory.ProjectViewDigest(project)
	if err != nil {
		t.Fatalf("digest project view: %v", err)
	}
	if _, err := store.PutProjectView(project); err != nil {
		t.Fatalf("put project view: %v", err)
	}

	manifest := memory.GenerationManifest{
		SchemaVersion:           memory.MemorySchemaVersion,
		GenerationID:            generationID,
		ProjectID:               testProjectID,
		CreatedAt:               testEndedAt,
		SourceRecordDigests:     []string{session.SourceRecordDigest},
		ObservationChunkDigests: []string{chunkDigest},
		SessionViews: []memory.SessionViewDependency{{
			Provider: "codex", SessionID: "session-1", Digest: session.Digest,
		}},
		ProbeStateDigest:  probe.Digest,
		ProbeCheck:        memory.ProbeCheck{SchemaVersion: memory.MemorySchemaVersion, CheckedAt: testEndedAt, StateDigest: probe.Digest, Available: true, Diagnostics: []memory.Diagnostic{}},
		ProjectViewDigest: project.Digest,
		ActiveRevisions: map[string]string{
			observationKeyDigest(t, observation.Key): observation.RevisionID,
		},
		SupersededRevisions: map[string]string{},
		WithdrawnRevisions:  map[string]string{},
	}
	if err := memory.ValidateGenerationManifest(manifest); err != nil {
		t.Fatalf("fixture manifest is invalid: %v", err)
	}
	return storedFixture{observation: observation, session: session, probe: probe, project: project, manifest: manifest}
}

func observationKeyDigest(t *testing.T, key memory.ObservationKey) string {
	t.Helper()
	digest, err := memory.Digest(key)
	if err != nil {
		t.Fatalf("digest observation key: %v", err)
	}
	return digest
}

func prefixedDigest(seed string) string {
	return "sha256:" + hexDigest(seed)
}

func hexDigest(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func digestLeaf(digest, suffix string) string {
	return digest[len("sha256:"):] + suffix
}

func writeCanonicalJSONForTest(t *testing.T, path string, value any, mode os.FileMode) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test JSON: %v", err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatalf("write test JSON: %v", err)
	}
}
