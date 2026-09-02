package publication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testIntent(projectID, genID string) Intent {
	h1 := sha256.Sum256([]byte("desired1"))
	h2 := sha256.Sum256([]byte("desired2"))
	p1 := sha256.Sum256([]byte("preimage1"))
	return Intent{
		Version:           1,
		ProjectID:         projectID,
		GenerationID:      genID,
		ManifestDigest:    "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ProjectViewDigest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Stage:             StagePrepared,
		CreatedAt:         time.Now().UTC().Truncate(time.Second),
		Destinations: []Destination{
			{
				Side:           "project",
				Relative:       "docs/session-review/项目回顾.md",
				PreimageSHA256: hex.EncodeToString(p1[:]),
				DesiredSHA256:  hex.EncodeToString(h1[:]),
				PreimageExists: true,
			},
			{
				Side:          "vault",
				Relative:      "projects/test/项目回顾.md",
				DesiredSHA256: hex.EncodeToString(h2[:]),
			},
		},
	}
}

func TestJournalLifecycleAndStageTransitions(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "proj-123"
	j, err := OpenJournal(dataRoot, projectID)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer j.Close()

	// No active intent initially
	_, err = j.Load()
	if !errors.Is(err, ErrNoActiveIntent) {
		t.Fatalf("expected ErrNoActiveIntent, got %v", err)
	}

	intent := testIntent(projectID, "gen-001")
	if err := j.Create(intent); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Creating again over active intent fails
	if err := j.Create(intent); !errors.Is(err, ErrActiveIntentExists) {
		t.Fatalf("expected ErrActiveIntentExists, got %v", err)
	}

	// Load and verify
	loaded, err := j.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.GenerationID != intent.GenerationID || loaded.Stage != StagePrepared {
		t.Fatalf("unexpected loaded intent: %+v", loaded)
	}

	// Valid transitions: prepared -> project_written -> vault_synced -> verified -> committed
	if err := j.Advance(StagePrepared, StageProjectWritten); err != nil {
		t.Fatalf("Advance to project_written: %v", err)
	}
	// Idempotent duplicate advance succeeds
	if err := j.Advance(StagePrepared, StageProjectWritten); err != nil {
		t.Fatalf("duplicate Advance should succeed idempotently: %v", err)
	}
	// Stale expected stage fails
	if err := j.Advance(StagePrepared, StageVaultSynced); !errors.Is(err, ErrStageMismatch) {
		t.Fatalf("expected ErrStageMismatch, got %v", err)
	}
	if err := j.Advance(StageProjectWritten, StageVaultSynced); err != nil {
		t.Fatalf("Advance to vault_synced: %v", err)
	}
	if err := j.Advance(StageVaultSynced, StageVerified); err != nil {
		t.Fatalf("Advance to verified: %v", err)
	}
	if err := j.Advance(StageVerified, StageCommitted); err != nil {
		t.Fatalf("Advance to committed: %v", err)
	}

	// Now in StageCommitted, creating new intent for gen-002 succeeds
	intent2 := testIntent(projectID, "gen-002")
	if err := j.Create(intent2); err != nil {
		t.Fatalf("Create after commit: %v", err)
	}
}

func TestJournalPreimagePayloads(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "proj-preimage"
	j, err := OpenJournal(dataRoot, projectID)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer j.Close()

	data := []byte("# Original Human Review Content")
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	// Put with wrong hash fails
	if err := j.PutPreimage("0000000000000000000000000000000000000000000000000000000000000000", data); !errors.Is(err, ErrPreimageMismatch) {
		t.Fatalf("expected ErrPreimageMismatch, got %v", err)
	}

	// Put with correct hash
	if err := j.PutPreimage(hash, data); err != nil {
		t.Fatalf("PutPreimage: %v", err)
	}
	// Idempotent put
	if err := j.PutPreimage(hash, data); err != nil {
		t.Fatalf("idempotent PutPreimage: %v", err)
	}

	// Load preimage
	loaded, err := j.LoadPreimage(hash)
	if err != nil {
		t.Fatalf("LoadPreimage: %v", err)
	}
	if !bytes.Equal(loaded, data) {
		t.Fatalf("loaded preimage does not match")
	}
}

func TestJournalStrictValidation(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "proj-val"
	j, err := OpenJournal(dataRoot, projectID)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer j.Close()

	// Unsorted destinations
	intent := testIntent(projectID, "gen-001")
	intent.Destinations = []Destination{
		{Side: "vault", Relative: "a.md", DesiredSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{Side: "project", Relative: "b.md", DesiredSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	}
	if err := j.Create(intent); err == nil {
		t.Fatal("expected error for unsorted destinations")
	}

	// Wrong project ID
	intent = testIntent("other-proj", "gen-001")
	if err := j.Create(intent); err == nil {
		t.Fatal("expected error for wrong project ID")
	}

	// Unknown fields or corrupt intent file rejection
	intentPath := filepath.Join(dataRoot, "publication-journal", projectID, "intent-v1.json")
	corruptJSON := []byte(`{"version":1,"project_id":"proj-val","extra_unknown_field":"bad"}`)
	if err := os.WriteFile(intentPath, corruptJSON, 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	if _, err := j.Load(); err == nil {
		t.Fatal("expected error decoding intent with unknown fields")
	}
}

func TestJournalRecover(t *testing.T) {
	dataRoot := t.TempDir()
	projectID := "proj-rec"
	j, err := OpenJournal(dataRoot, projectID)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer j.Close()

	intent := testIntent(projectID, "gen-rec-1")
	if err := j.Create(intent); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := j.Advance(StagePrepared, StageProjectWritten); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	recovered := false
	handler := RecoveryHandlerFunc(func(ctx context.Context, intent Intent, j *Journal) error {
		recovered = true
		if intent.Stage != StageProjectWritten {
			t.Fatalf("expected StageProjectWritten, got %s", intent.Stage)
		}
		return j.Advance(StageProjectWritten, StageRollbackRequired)
	})

	if err := j.Recover(context.Background(), handler); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if !recovered {
		t.Fatal("handler was not called")
	}
	loaded, err := j.Load()
	if err != nil || loaded.Stage != StageRollbackRequired {
		t.Fatalf("expected StageRollbackRequired, got stage=%v err=%v", loaded.Stage, err)
	}
}
