package publication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

func TestPublishCrashRecoveryRollsBackIncompleteWrite(t *testing.T) {
	projectID := "project-crash-rb"
	dataRoot, projectRoot, _, mapping, manifest, plan := setupPublishEnv(t, projectID)

	// Create an interrupted journal intent at StageProjectWritten
	j, err := OpenJournal(dataRoot, projectID)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer j.Close()

	// Write project files to simulate crash right after project write
	reviewPath := filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	if err := os.MkdirAll(filepath.Dir(reviewPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, file := range plan.Files {
		p := filepath.Join(projectRoot, filepath.FromSlash(file.Relative))
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, file.Desired, file.Mode); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}

	destinations := make([]Destination, 0, len(plan.Files)*2)
	for _, file := range plan.Files {
		destinations = append(destinations, Destination{
			Side:           "project",
			Relative:       file.Relative,
			DesiredSHA256:  sha256Hex(file.Desired),
			PreimageExists: false,
		})
		destinations = append(destinations, Destination{
			Side:           "vault",
			Relative:       vaultRelativePath(mapping.VaultReviewPath, file.Relative),
			DesiredSHA256:  sha256Hex(file.Desired),
			PreimageExists: false,
		})
	}
	sort.Slice(destinations, func(i, k int) bool {
		if destinations[i].Side != destinations[k].Side {
			return destinations[i].Side < destinations[k].Side
		}
		return destinations[i].Relative < destinations[k].Relative
	})
	intent := Intent{
		Version:           1,
		ProjectID:         projectID,
		GenerationID:      manifest.GenerationID,
		ManifestDigest:    "sha256:" + strings.Repeat("a", 64),
		ProjectViewDigest: manifest.ProjectViewDigest,
		Stage:             StageProjectWritten,
		CreatedAt:         time.Now().UTC(),
		Destinations:      destinations,
	}
	if err := j.Create(intent); err != nil && !errors.Is(err, ErrActiveIntentExists) {
		// Write raw intent file if needed
		body, _ := encodeCanonicalJSON(intent)
		_ = atomicfile.WriteRootFile(j.dir.Root, intentFileLeaf, body, 0o600)
	}

	opts := Options{
		ProjectID:          projectID,
		PreparedGeneration: manifest.GenerationID,
		Plan:               plan,
		Mapping:            mapping,
		DataRoot:           dataRoot,
		Now:                time.Now,
	}

	// Publish runs recovery first, rolls back, then re-publishes cleanly
	result, err := Publish(context.Background(), opts)
	if err != nil {
		t.Fatalf("Publish with recovery: %v", err)
	}
	if !result.Recovered {
		t.Fatal("expected Recovered=true")
	}
}

func TestPublishCrashRecoveryNeverOverwritesHumanEdit(t *testing.T) {
	projectID := "project-crash-human"
	dataRoot, projectRoot, _, mapping, manifest, plan := setupPublishEnv(t, projectID)

	j, err := OpenJournal(dataRoot, projectID)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer j.Close()

	// Write project file with human edit (different from desired and preimage)
	reviewPath := filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	if err := os.MkdirAll(filepath.Dir(reviewPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	humanEditContent := []byte("# Human Made This Edit After Crash")
	if err := os.WriteFile(reviewPath, humanEditContent, 0o600); err != nil {
		t.Fatalf("write human edit: %v", err)
	}

	destinations := make([]Destination, 0, len(plan.Files)*2)
	for _, file := range plan.Files {
		destinations = append(destinations, Destination{
			Side:           "project",
			Relative:       file.Relative,
			DesiredSHA256:  sha256Hex(file.Desired),
			PreimageExists: false,
		})
		destinations = append(destinations, Destination{
			Side:           "vault",
			Relative:       vaultRelativePath(mapping.VaultReviewPath, file.Relative),
			DesiredSHA256:  sha256Hex(file.Desired),
			PreimageExists: false,
		})
	}
	sort.Slice(destinations, func(i, k int) bool {
		if destinations[i].Side != destinations[k].Side {
			return destinations[i].Side < destinations[k].Side
		}
		return destinations[i].Relative < destinations[k].Relative
	})
	intent := Intent{
		Version:           1,
		ProjectID:         projectID,
		GenerationID:      manifest.GenerationID,
		ManifestDigest:    "sha256:" + strings.Repeat("a", 64),
		ProjectViewDigest: manifest.ProjectViewDigest,
		Stage:             StageProjectWritten,
		CreatedAt:         time.Now().UTC(),
		Destinations:      destinations,
	}
	body, _ := encodeCanonicalJSON(intent)
	_ = atomicfile.WriteRootFile(j.dir.Root, intentFileLeaf, body, 0o600)

	opts := Options{
		ProjectID:          projectID,
		PreparedGeneration: manifest.GenerationID,
		Plan:               plan,
		Mapping:            mapping,
		DataRoot:           dataRoot,
		Now:                time.Now,
	}

	// Recovery should detect human edit and fail closed without overwriting
	_, err = Publish(context.Background(), opts)
	if err == nil {
		t.Fatal("expected error due to human edit during recovery")
	}

	// Verify human edit is preserved
	content, err := os.ReadFile(reviewPath)
	if err != nil || !strings.Contains(string(content), "Human Made This Edit After Crash") {
		t.Fatalf("human edit was overwritten: %s", content)
	}
}
