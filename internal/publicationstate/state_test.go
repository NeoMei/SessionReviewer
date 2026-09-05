package publicationstate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadOnlyReaderReturnsValidatedIntent(t *testing.T) {
	dataRoot := t.TempDir()
	if err := os.Chmod(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	projectID := "project-read-intent"
	journalRoot := filepath.Join(dataRoot, "publication-journal", projectID)
	if err := os.MkdirAll(journalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	intent := Intent{
		Version: 1, ProjectID: projectID, GenerationID: "generation-1",
		ManifestDigest: digest, ProjectViewDigest: digest, Stage: StagePrepared,
		CreatedAt:    time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC),
		Destinations: []Destination{{Side: "project", Relative: "docs/session-review/project-review.json", DesiredSHA256: strings.Repeat("b", 64)}},
	}
	body, err := canonical(intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(journalRoot, IntentLeaf), body, 0o600); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenReadOnly(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := reader.Intent()
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != projectID || got.Stage != StagePrepared || got.GenerationID != "generation-1" {
		t.Fatalf("intent=%+v", got)
	}
}

func TestMigrationSourceOmissionPreservesLegacyIntentReceiptAndRevisionCanonicalBytes(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	hash := strings.Repeat("b", 64)
	destination := []Destination{{Side: "project", Relative: "docs/session-review/项目回顾.md", DesiredSHA256: hash}}
	guard := &IndexGuard{Relative: "docs/session-review/.session-reviewer/session-index.json", VaultRelative: "Projects/P/Session Review/.session-reviewer/session-index.json", ProjectSHA256: hash, VaultSHA256: hash, Digest: digest, GenerationID: "generation-1"}
	pointer := "generation-0"
	v1 := Intent{Version: 1, ProjectID: "project-p", GenerationID: "generation-1", ManifestDigest: digest, ProjectViewDigest: digest, Stage: StageCommitted, CreatedAt: time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC), Destinations: destination}
	v2 := Intent{Version: 2, Kind: KindMarkdown, ProjectID: "project-p", GenerationID: "generation-1", ManifestDigest: digest, ProjectViewDigest: digest, Stage: StagePrepared, CreatedAt: v1.CreatedAt, Destinations: destination, IndexGuard: guard, BaseDesiredDigest: hash, RequiresPointer: true, PointerPreimage: &pointer}
	type legacyIntent struct {
		Version             int           `json:"version"`
		Kind                Kind          `json:"kind,omitempty"`
		ProjectID           string        `json:"project_id"`
		GenerationID        string        `json:"generation_id"`
		ManifestDigest      string        `json:"manifest_digest"`
		ProjectViewDigest   string        `json:"project_view_digest"`
		RevisionID          string        `json:"revision_id,omitempty"`
		Stage               Stage         `json:"stage"`
		Outcome             Outcome       `json:"outcome,omitempty"`
		CreatedAt           time.Time     `json:"created_at"`
		Destinations        []Destination `json:"destinations"`
		IndexGuard          *IndexGuard   `json:"index_guard,omitempty"`
		BasePreimageDigest  string        `json:"base_preimage_digest,omitempty"`
		BaseReviewPreimage  string        `json:"base_review_preimage_sha256,omitempty"`
		BaseHistoryPreimage string        `json:"base_history_preimage_sha256,omitempty"`
		BaseDesiredDigest   string        `json:"base_desired_digest,omitempty"`
		RequiresPointer     bool          `json:"requires_pointer,omitempty"`
		PointerPreimage     *string       `json:"pointer_preimage_generation_id,omitempty"`
	}
	for name, current := range map[string]Intent{"v1": v1, "v2": v2} {
		got, err := canonical(current)
		if err != nil {
			t.Fatal(err)
		}
		want, err := canonical(legacyIntent{Version: current.Version, Kind: current.Kind, ProjectID: current.ProjectID, GenerationID: current.GenerationID, ManifestDigest: current.ManifestDigest, ProjectViewDigest: current.ProjectViewDigest, RevisionID: current.RevisionID, Stage: current.Stage, Outcome: current.Outcome, CreatedAt: current.CreatedAt, Destinations: current.Destinations, IndexGuard: current.IndexGuard, BasePreimageDigest: current.BasePreimageDigest, BaseReviewPreimage: current.BaseReviewPreimage, BaseHistoryPreimage: current.BaseHistoryPreimage, BaseDesiredDigest: current.BaseDesiredDigest, RequiresPointer: current.RequiresPointer, PointerPreimage: current.PointerPreimage})
		if err != nil || string(got) != string(want) {
			t.Fatalf("%s canonical changed: err=%v\ngot=%s\nwant=%s", name, err, got, want)
		}
	}
	type legacyRevision struct {
		ProjectID, GenerationID, ManifestDigest, ProjectViewDigest string
		Destinations                                               []Destination
		IndexGuard                                                 *IndexGuard
		BaseDesiredDigest                                          string
		RequiresPointer                                            bool
		PointerPreimage                                            *string `json:"PointerPreimage,omitempty"`
	}
	revisionBody, err := canonical(legacyRevision{ProjectID: v2.ProjectID, GenerationID: v2.GenerationID, ManifestDigest: v2.ManifestDigest, ProjectViewDigest: v2.ProjectViewDigest, Destinations: v2.Destinations, IndexGuard: v2.IndexGuard, BaseDesiredDigest: v2.BaseDesiredDigest, RequiresPointer: v2.RequiresPointer, PointerPreimage: v2.PointerPreimage})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(revisionBody)
	if got, want := MarkdownRevisionID(v2), "sha256:"+hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("legacy v2 revision changed: got=%q want=%q", got, want)
	}
	receipt := AcceptedReceipt{Version: 1, ProjectID: v2.ProjectID, GenerationID: v2.GenerationID, ManifestDigest: v2.ManifestDigest, ProjectViewDigest: v2.ProjectViewDigest, RevisionID: MarkdownRevisionID(v2), Destinations: v2.Destinations, IndexGuard: v2.IndexGuard, BaseDigest: v2.BaseDesiredDigest, RequiresPointer: true, PointerPreimage: v2.PointerPreimage}
	type legacyReceipt struct {
		Version           int           `json:"version"`
		ProjectID         string        `json:"project_id"`
		GenerationID      string        `json:"generation_id"`
		ManifestDigest    string        `json:"manifest_digest"`
		ProjectViewDigest string        `json:"project_view_digest"`
		RevisionID        string        `json:"revision_id"`
		Destinations      []Destination `json:"destinations"`
		IndexGuard        *IndexGuard   `json:"index_guard,omitempty"`
		BaseDigest        string        `json:"base_digest"`
		RequiresPointer   bool          `json:"requires_pointer,omitempty"`
		PointerPreimage   *string       `json:"pointer_preimage_generation_id,omitempty"`
	}
	gotReceipt, err := canonical(receipt)
	if err != nil {
		t.Fatal(err)
	}
	wantReceipt, err := canonical(legacyReceipt{Version: receipt.Version, ProjectID: receipt.ProjectID, GenerationID: receipt.GenerationID, ManifestDigest: receipt.ManifestDigest, ProjectViewDigest: receipt.ProjectViewDigest, RevisionID: receipt.RevisionID, Destinations: receipt.Destinations, IndexGuard: receipt.IndexGuard, BaseDigest: receipt.BaseDigest, RequiresPointer: receipt.RequiresPointer, PointerPreimage: receipt.PointerPreimage})
	if err != nil || string(gotReceipt) != string(wantReceipt) {
		t.Fatalf("legacy receipt canonical changed: err=%v\ngot=%s\nwant=%s", err, gotReceipt, wantReceipt)
	}
}

// Recomputing RevisionID is not permission to smuggle a structurally invalid
// migration proof into the accepted receipt. Readers must enforce the same
// typed proof boundary as the source intent.
func TestValidateReceiptRejectsMalformedMigrationSourceWithMatchingRevision(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	hash := strings.Repeat("b", 64)
	pointer := "generation-source"
	proof := &MigrationSourceProof{
		GenerationID: "INVALID-SOURCE", ManifestDigest: digest, ProjectViewDigest: digest,
		IndexDigest: digest, JournalDigest: digest,
		Destinations: []Destination{{Side: "project", Relative: "docs/session-review/old.json", DesiredSHA256: hash}},
	}
	receipt := AcceptedReceipt{
		Version: 1, ProjectID: "project-p", GenerationID: "generation-target",
		ManifestDigest: digest, ProjectViewDigest: digest,
		Destinations: []Destination{{Side: "project", Relative: "docs/session-review/项目回顾.md", DesiredSHA256: hash}},
		IndexGuard: &IndexGuard{
			Relative: "docs/session-review/.session-reviewer/session-index.json", VaultRelative: "Projects/P/Session Review/.session-reviewer/session-index.json",
			ProjectSHA256: hash, VaultSHA256: hash, Digest: digest, GenerationID: "generation-target",
		},
		BaseDigest: hash, RequiresPointer: true, PointerPreimage: &pointer, MigrationSource: proof,
	}
	receipt.RevisionID = MarkdownRevisionID(Intent{
		ProjectID: receipt.ProjectID, GenerationID: receipt.GenerationID,
		ManifestDigest: receipt.ManifestDigest, ProjectViewDigest: receipt.ProjectViewDigest,
		Destinations: receipt.Destinations, IndexGuard: receipt.IndexGuard,
		BaseDesiredDigest: receipt.BaseDigest, RequiresPointer: receipt.RequiresPointer,
		PointerPreimage: receipt.PointerPreimage, MigrationSource: receipt.MigrationSource,
	})
	if err := ValidateReceipt(receipt, receipt.ProjectID); err == nil {
		t.Fatal("accepted receipt trusted malformed migration source proof")
	}
}
