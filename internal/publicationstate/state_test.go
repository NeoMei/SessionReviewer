package publicationstate

import (
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
