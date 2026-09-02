package zerotoken

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/contextupdate"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

func TestGateBEndToEndPublicationAndIdempotence(t *testing.T) {
	dataRoot := t.TempDir()
	projectRoot := t.TempDir()
	vaultRoot := t.TempDir()
	sessionsRoot := t.TempDir()
	projectID := "project-gate-b-test"
	_ = os.WriteFile(filepath.Join(projectRoot, "VERSION"), []byte("v1.0.0\n"), 0o600)
	_ = os.WriteFile(filepath.Join(projectRoot, "project-fixture.md"), []byte("# fixture\n"), 0o600)
	initializeGateRepository(t, projectRoot)

	mapping := config.ProjectMapping{
		ID:              projectID,
		Root:            projectRoot,
		VaultRoot:       vaultRoot,
		VaultReviewPath: "Projects/" + projectID + "/Session Review",
		VaultCaseMode:   platform.CaseSensitive,
	}
	cfg := config.Config{
		Version:  1,
		Projects: []config.ProjectMapping{mapping},
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	base := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	sessionLines := []string{
		`{"timestamp":"` + base.Format(time.RFC3339) + `","type":"session_meta","payload":{"id":"session-1","cwd":"` + filepath.ToSlash(projectRoot) + `","source":"codex"}}`,
		`{"timestamp":"` + base.Add(time.Second).Format(time.RFC3339) + `","type":"turn_context","payload":{"cwd":"` + filepath.ToSlash(projectRoot) + `","model":"gpt-5"}}`,
		`{"timestamp":"` + base.Add(2*time.Second).Format(time.RFC3339) + `","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15},"total_token_usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}}`,
	}
	if err := os.WriteFile(filepath.Join(sessionsRoot, "session-1.jsonl"), []byte(strings.Join(sessionLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}

	phases := []string{}
	cuOpts := contextupdate.Options{
		ProjectID:    projectID,
		SessionsRoot: sessionsRoot,
		DataRoot:     dataRoot,
		Now:          func() time.Time { return base.Add(10 * time.Minute) },
		PhaseObserver: func(phase string) {
			phases = append(phases, phase)
		},
	}

	result, err := contextupdate.Run(context.Background(), cuOpts)
	if err != nil {
		t.Fatalf("contextupdate.Run: %v", err)
	}
	if result.GenerationID == "" {
		t.Fatal("expected non-empty GenerationID")
	}
	if result.ReviewRunTokens != 0 {
		t.Fatalf("expected 0 review run tokens, got %d", result.ReviewRunTokens)
	}

	// Verify files in Project and Vault match
	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	pubID, _, err := store.LoadPublished()
	if err != nil || pubID != result.GenerationID {
		t.Fatalf("published pointer mismatch: pubID=%s resID=%s", pubID, result.GenerationID)
	}

	pReview, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath)))
	if err != nil {
		t.Fatalf("read project review: %v", err)
	}
	vReview, err := os.ReadFile(filepath.Join(vaultRoot, "Projects", projectID, "Session Review", "项目回顾.md"))
	if err != nil {
		t.Fatalf("read vault review: %v", err)
	}
	if string(pReview) != string(vReview) {
		t.Fatal("project and vault review contents differ")
	}

	// Second run reflects newly created projection files in git status
	result2, err := contextupdate.Run(context.Background(), cuOpts)
	if err != nil {
		t.Fatalf("second contextupdate.Run: %v", err)
	}
	// Third run with stable git status and unchanged inputs is completely idempotent
	result3, err := contextupdate.Run(context.Background(), cuOpts)
	if err != nil {
		t.Fatalf("third contextupdate.Run: %v", err)
	}
	if result3.GenerationID != result2.GenerationID {
		t.Fatalf("generation changed on unchanged third run: got=%s want=%s", result3.GenerationID, result2.GenerationID)
	}
}
