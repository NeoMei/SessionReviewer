package inspect_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/neomei/SessionReviewer/internal/cli"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/inspect"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/platform"
)

func TestRetainedConversationSurvivesRemovalRescanAndRestoration(t *testing.T) {
	projectRoot, vaultRoot, dataRoot, sessionsRoot := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = projectRoot
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v %s", err, output)
		}
	}
	const projectID = "project-retained-query"
	cfg := config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: projectID, Root: projectRoot, VaultRoot: vaultRoot, VaultReviewPath: "Projects/Retained/Session Review", VaultCaseMode: platform.CaseSensitive}}}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	const sessionID = "77777777-7777-4777-8777-777777777777"
	records := []map[string]any{
		{"timestamp": "2026-09-08T00:00:00Z", "type": "session_meta", "payload": map[string]any{"id": sessionID, "cwd": projectRoot}},
		{"timestamp": "2026-09-08T00:00:01Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Can I read the retained answer?"}}}},
		{"timestamp": "2026-09-08T00:00:02Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Retained answer."}}}},
	}
	var source bytes.Buffer
	for _, record := range records {
		if err := json.NewEncoder(&source).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	sourcePath := filepath.Join(sessionsRoot, "rollout-2026-09-08T00-00-00-"+sessionID+".jsonl")
	if err := os.WriteFile(sourcePath, source.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	runScan := func() string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := cli.Run([]string{"scan", "--project-id", projectID, "--sessions-root", sessionsRoot, "--data-dir", dataRoot, "--json"}, &stdout, &stderr); code != 0 {
			t.Fatalf("scan code %d: %s %s", code, stdout.String(), stderr.String())
		}
		store, err := memorystore.OpenReadOnly(dataRoot, projectID)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		generation, _, err := store.LoadPublished()
		if err != nil {
			t.Fatal(err)
		}
		return generation
	}
	t.Setenv("SESSION_REVIEWER_SESSIONS_ROOT", sessionsRoot)
	generation := runScan()
	request := inspect.ConversationRequest{DataRoot: dataRoot, ProjectID: projectID, Provider: "codex", SessionID: sessionID, ExpectedGenerationID: generation, Limit: 20}
	index, err := inspect.LoadConversationPage(context.Background(), request)
	if err != nil || len(index.TurnUnits) != 1 {
		t.Fatalf("source index: page=%+v err=%v", index, err)
	}
	originalView := index.SessionViewDigest
	request.TurnUnitID = index.TurnUnits[0].TurnUnitID
	available, err := inspect.LoadConversationPage(context.Background(), request)
	if err != nil || available.BodyAvailability != "source_full" || len(available.Messages) != 2 || available.Messages[1].Text == nil || *available.Messages[1].Text != "Retained answer." {
		t.Fatalf("available body: page=%+v err=%v", available, err)
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}
	beforeRescan, err := inspect.LoadConversationPage(context.Background(), request)
	if err != nil || beforeRescan.BodyAvailability != "retained_excerpt" || beforeRescan.Messages[1].Text != nil {
		t.Fatalf("retained before rescan: page=%+v err=%v", beforeRescan, err)
	}
	request.ExpectedGenerationID = runScan()
	afterRescan, err := inspect.LoadConversationPage(context.Background(), request)
	if err != nil || afterRescan.BodyAvailability != "retained_excerpt" || afterRescan.SessionViewDigest == originalView || afterRescan.EvidenceSessionViewDigest == nil || *afterRescan.EvidenceSessionViewDigest != originalView || afterRescan.Messages[1].Text != nil || afterRescan.Messages[1].Phase != nil {
		t.Fatalf("retained after rescan: page=%+v err=%v", afterRescan, err)
	}
	if err := os.WriteFile(sourcePath, source.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	request.ExpectedGenerationID = runScan()
	restored, err := inspect.LoadConversationPage(context.Background(), request)
	if err != nil || restored.BodyAvailability != "source_full" || restored.SessionViewDigest != originalView || restored.Messages[1].Text == nil || *restored.Messages[1].Text != "Retained answer." {
		t.Fatalf("restored source: page=%+v err=%v", restored, err)
	}
}
