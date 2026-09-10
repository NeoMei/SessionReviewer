package candidatepublication

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestIntentStoreRetainsPreparedIntentAndUsesRevisionCAS(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(root, "project-p", "problems")
	if err != nil {
		t.Fatal(err)
	}
	preparedAt := time.Date(2026, 9, 9, 15, 0, 0, 0, time.UTC)
	intent, err := NewIntent(IntentInput{
		ProjectID: "project-p", Namespace: "problems", CandidateID: "candidate-1", ExpectedCandidateRevision: 4,
		CandidateDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Action:          "apply_child", EntityID: "problem-1",
		ResultFingerprint: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		TerminalStatus:    "applied", PreparedAt: preparedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Prepare(intent)
	if err != nil || created.State != StatePrepared || created.Revision != 1 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if repeated, err := store.Prepare(intent); err != nil || repeated != created {
		t.Fatalf("idempotent prepare=%+v err=%v", repeated, err)
	}

	reopened, err := OpenStore(root, "project-p", "problems")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := reopened.Prepared()
	if err != nil || len(pending) != 1 || pending[0] != created {
		t.Fatalf("prepared=%+v err=%v", pending, err)
	}
	if _, err := reopened.Transition(intent.OperationID, 2, StateCompleted, preparedAt.Add(time.Second)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale transition err=%v", err)
	}
	completed, err := reopened.Transition(intent.OperationID, 1, StateCompleted, preparedAt.Add(time.Second))
	if err != nil || completed.State != StateCompleted || completed.Revision != 2 || completed.UpdatedAt != "2026-09-09T15:00:01Z" {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	if pending, err := reopened.Prepared(); err != nil || len(pending) != 0 {
		t.Fatalf("completed intent still prepared: %+v err=%v", pending, err)
	}
	if _, err := reopened.Transition(intent.OperationID, 2, StateAborted, preparedAt.Add(2*time.Second)); !errors.Is(err, ErrTerminalIntent) {
		t.Fatalf("terminal rewrite err=%v", err)
	}
	info, err := os.Stat(filepath.Join(root, "projects", "project-p", "candidate-publication-problems.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Windows FileMode does not describe access-control permissions.
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("intent file mode=%v err=%v", info.Mode(), err)
	}
}

func TestIntentStoreRejectsConflictingIdentityAndUnsafeRoots(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(root, "project-p", "decisions")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 9, 16, 0, 0, 0, time.UTC)
	intent, err := NewIntent(IntentInput{
		ProjectID: "project-p", Namespace: "decisions", CandidateID: "candidate-1", ExpectedCandidateRevision: 1,
		CandidateDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Action:          "confirm", EntityID: "decision-1",
		ResultFingerprint: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		TerminalStatus:    "confirmed", PreparedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Prepare(intent); err != nil {
		t.Fatal(err)
	}
	conflict := intent
	conflict.EntityID = "decision-2"
	if _, err := store.Prepare(conflict); !errors.Is(err, ErrIntentConflict) {
		t.Fatalf("conflicting prepare err=%v", err)
	}
	if _, err := OpenStore(root, "project-p", "../foreign"); err == nil {
		t.Fatal("unsafe namespace accepted")
	}
	if _, err := OpenStore("relative", "project-p", "decisions"); err == nil {
		t.Fatal("relative data root accepted")
	}
}

func TestAbortedIntentDoesNotBlockFreshRetry(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(root, "project-p", "problems")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 9, 17, 0, 0, 0, time.UTC)
	input := IntentInput{
		ProjectID: "project-p", Namespace: "problems", CandidateID: "candidate-1", ExpectedCandidateRevision: 1,
		CandidateDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Action:          "apply_root", EntityID: "problem-1",
		ResultFingerprint: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		TerminalStatus:    "applied", PreparedAt: at,
	}
	first, err := NewIntent(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Prepare(first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(first.OperationID, first.Revision, StateAborted, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	input.PreparedAt = at.Add(2 * time.Second)
	retry, err := NewIntent(input)
	if err != nil {
		t.Fatal(err)
	}
	if retry.OperationID == first.OperationID {
		t.Fatal("fresh retry reused the aborted operation ID")
	}
	if _, err := store.Prepare(retry); err != nil {
		t.Fatalf("fresh retry prepare err=%v", err)
	}
}
