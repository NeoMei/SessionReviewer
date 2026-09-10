package decisions

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/annotation"
)

func TestCandidateStorePersistsCASLifecycleAndPreservesOtherKinds(t *testing.T) {
	dataRoot := privateDecisionTempDir(t)
	store, err := OpenStore(dataRoot, "project-p")
	if err != nil {
		t.Fatal(err)
	}
	record := candidateStoreRecord("project-p")
	if err := store.ReplaceAbsent(record); err != nil {
		t.Fatal(err)
	}
	beforeMilestone := record.Annotations[1]
	ignored, err := store.Transition("candidate-1", 1, "ignore", "", time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC))
	if err != nil || ignored.Status != annotation.CandidateIgnored || ignored.Revision != 2 {
		t.Fatalf("ignored=%+v err=%v", ignored, err)
	}
	restored, err := store.Transition("candidate-1", 2, "restore", "", time.Date(2026, 9, 9, 1, 1, 0, 0, time.UTC))
	if err != nil || restored.Status != annotation.CandidatePending || restored.Revision != 3 {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
	confirmed, err := store.Transition("candidate-1", 3, "confirm", "decision-1", time.Date(2026, 9, 9, 1, 2, 0, 0, time.UTC))
	if err != nil || confirmed.Status != annotation.CandidateConfirmed || confirmed.Revision != 4 || confirmed.ConfirmedEntityID == nil || *confirmed.ConfirmedEntityID != "decision-1" {
		t.Fatalf("confirmed=%+v err=%v", confirmed, err)
	}
	if _, err := store.Transition("candidate-1", 4, "ignore", "", time.Now()); !errors.Is(err, ErrCandidateTerminal) {
		t.Fatalf("terminal transition err=%v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Annotations[1].Text != beforeMilestone.Text || loaded.Annotations[1].Revision != beforeMilestone.Revision || loaded.Annotations[1].Status != beforeMilestone.Status {
		t.Fatalf("unrelated milestone candidate changed: before=%+v after=%+v", beforeMilestone, loaded.Annotations[1])
	}
	info, err := os.Stat(filepath.Join(dataRoot, "projects", "project-p", "annotations", "head.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Windows FileMode does not describe access-control permissions.
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("private store mode=%v err=%v", info.Mode(), err)
	}
}

func TestCandidatePublicationDigestBindsImmutableFieldsAcrossConfirmation(t *testing.T) {
	entity, field := "decision-1", "decision"
	candidate := annotation.Annotation{
		ID: "candidate-1", ProjectID: "project-p", AnnotationKind: "decision_candidate", EntityID: &entity, Field: &field,
		Status: annotation.CandidatePending, Text: `{"title":"Keep proof"}`, GenerationID: "generation-1", SchemaVersion: 1,
		AnalysisProfile: "decision-extractor-v1", AgentRunID: "run-1",
		Dependencies: []annotation.Dependency{{Kind: "session_view", RevisionID: "view-1", Digest: "sha256:" + strings.Repeat("1", 64)}},
		Revision:     3, CreatedAt: "2026-09-09T00:00:00Z",
	}
	pending, err := CandidatePublicationDigest(candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Status, candidate.Revision, candidate.ConfirmedEntityID = annotation.CandidateConfirmed, 4, &entity
	confirmed, err := CandidatePublicationDigest(candidate)
	if err != nil || confirmed != pending {
		t.Fatalf("confirmed digest=%q pending=%q err=%v", confirmed, pending, err)
	}
	candidate.Dependencies[0].RevisionID = "different-proof"
	changed, err := CandidatePublicationDigest(candidate)
	if err != nil || changed == pending {
		t.Fatalf("immutable dependency was not bound: changed=%q pending=%q err=%v", changed, pending, err)
	}
}

func TestCandidateStoreFiltersDecisionKindsAndRejectsStaleRevision(t *testing.T) {
	store, err := OpenStore(privateDecisionTempDir(t), "project-p")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAbsent(candidateStoreRecord("project-p")); err != nil {
		t.Fatal(err)
	}
	values, err := store.List(annotation.CandidatePending)
	if err != nil || len(values) != 1 || values[0].ID != "candidate-1" {
		t.Fatalf("values=%+v err=%v", values, err)
	}
	if _, err := store.Transition("candidate-1", 2, "ignore", "", time.Now()); !errors.Is(err, ErrCandidateRevisionConflict) {
		t.Fatalf("stale transition err=%v", err)
	}
}

func TestCandidateStoreNotDecisionAndRestoreStateTable(t *testing.T) {
	tests := []struct {
		start  annotation.CandidateStatus
		action string
		want   annotation.CandidateStatus
		ok     bool
	}{{annotation.CandidatePending, "not_decision", annotation.CandidateNotDecision, true}, {annotation.CandidatePending, "restore", "", false}, {annotation.CandidateIgnored, "restore", annotation.CandidatePending, true}, {annotation.CandidateIgnored, "not_decision", "", false}, {annotation.CandidateNotDecision, "restore", "", false}, {annotation.CandidateStale, "restore", "", false}}
	for _, test := range tests {
		t.Run(string(test.start)+"_"+test.action, func(t *testing.T) {
			store, _ := OpenStore(privateDecisionTempDir(t), "project-p")
			record := candidateStoreRecord("project-p")
			if err := store.ReplaceAbsent(record); err != nil {
				t.Fatal(err)
			}
			revision := 1
			if test.start != annotation.CandidatePending {
				seedAction := map[annotation.CandidateStatus]string{annotation.CandidateIgnored: "ignore", annotation.CandidateNotDecision: "not_decision", annotation.CandidateStale: "stale"}[test.start]
				if _, err := store.Transition("candidate-1", revision, seedAction, "", time.Now()); err != nil {
					t.Fatal(err)
				}
				revision++
			}
			got, err := store.Transition("candidate-1", revision, test.action, "", time.Now())
			if test.ok && (err != nil || got.Status != test.want) {
				t.Fatalf("got=%+v err=%v", got, err)
			}
			if !test.ok && err == nil {
				t.Fatalf("invalid transition succeeded: %+v", got)
			}
		})
	}
}

func TestCandidateStoreCommitsCompletedExtractionAndWatermarkAtomically(t *testing.T) {
	store, err := OpenStore(privateDecisionTempDir(t), "project-p")
	if err != nil {
		t.Fatal(err)
	}
	record := candidateStoreRecord("project-p")
	if err := store.ReplaceAbsent(record); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("2", 64)
	run := annotation.Run{RunID: "run-2", ProjectID: "project-p", Status: "completed", ExtractorVersion: ExtractorVersion, PromptSchemaVersion: PromptSchemaVersion, DependencyDigests: []string{digest}, CreatedAt: "2026-09-09T01:00:00Z", UpdatedAt: "2026-09-09T01:00:01Z"}
	entity, field := "decision-2", "decision"
	candidate := annotation.Annotation{ID: "candidate-2", ProjectID: "project-p", AnnotationKind: "decision_candidate", EntityID: &entity, Field: &field, Status: annotation.CandidatePending, Text: record.Annotations[0].Text, GenerationID: "generation-2", SchemaVersion: 1, AnalysisProfile: ExtractorVersion, AgentRunID: run.RunID, Dependencies: []annotation.Dependency{{Kind: "session_view", RevisionID: "view-" + strings.Repeat("2", 16), Digest: digest}}, Revision: 1, CreatedAt: run.UpdatedAt}
	if err := store.CommitExtraction(run, []annotation.Annotation{candidate}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || len(loaded.ExtractionRuns) != 2 || len(loaded.Annotations) != 3 || loaded.Annotations[1].Text != record.Annotations[1].Text {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	watermark := SuccessfulExtractionDependencies(loaded)
	if !watermark[record.ExtractionRuns[0].DependencyDigests[0]] || !watermark[digest] {
		t.Fatalf("watermark=%v", watermark)
	}
	if err := store.CommitExtraction(run, []annotation.Annotation{candidate}); err != nil {
		t.Fatalf("idempotent completion err=%v", err)
	}
}

func candidateStoreRecord(projectID string) annotation.StoreRecord {
	entity, field, milestone, prompt := "decision-proposed", "decision", "milestone-1", "milestone-conclusion-v1"
	digest := "sha256:" + strings.Repeat("1", 64)
	return annotation.StoreRecord{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: projectID, Annotations: []annotation.Annotation{
		{ID: "candidate-1", ProjectID: projectID, AnnotationKind: "decision_candidate", EntityID: &entity, Field: &field, Status: annotation.CandidatePending, Text: `{"schema_version":1,"kind":"decision","occurred_at":"2026-09-09","title":"Candidate","rationale":"Reason","impact":"Impact","status":"active","reevaluate_when":"Later","supersedes":[],"milestone_ids":[],"session_refs":[],"pinned":false}`, GenerationID: "generation-1", SchemaVersion: 1, AnalysisProfile: "decision-extract-v1", AgentRunID: "run-1", Dependencies: []annotation.Dependency{{Kind: "session_view", RevisionID: "view-1", Digest: digest}}, Revision: 1, CreatedAt: "2026-09-09T00:00:00Z"},
		{ID: "summary-1", ProjectID: projectID, AnnotationKind: "milestone_conclusion_candidate", Status: annotation.CandidatePending, Text: "Keep me exact", GenerationID: "generation-1", SchemaVersion: 1, AnalysisProfile: "summary-v1", AgentRunID: "run-1", Dependencies: []annotation.Dependency{{Kind: "source_turn", RevisionID: "turn-1", Digest: digest}}, Revision: 1, CreatedAt: "2026-09-09T00:00:00Z", TargetMilestoneID: &milestone, PromptSchemaVersion: &prompt},
	}, ExtractionRuns: []annotation.Run{{RunID: "run-1", ProjectID: projectID, Status: "completed", ExtractorVersion: "decision-extract-v1", PromptSchemaVersion: "decision-candidate-v1", DependencyDigests: []string{digest}, CreatedAt: "2026-09-09T00:00:00Z", UpdatedAt: "2026-09-09T00:00:01Z"}}}
}

func privateDecisionTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
