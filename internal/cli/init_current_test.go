package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func TestCurrentMarkdownInitReusesRealScanPublication(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "accepted"
		if pending {
			name = "pending draft"
		}
		t.Run(name, func(t *testing.T) {
			projectRoot, vaultRoot, dataRoot, sessionsRoot := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
			for _, args := range [][]string{{"init"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
				command := exec.Command("git", args...)
				command.Dir = projectRoot
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("fixture git: %v %s", err, output)
				}
			}
			mapping := config.ProjectMapping{
				ID: "project-controller-native", Root: projectRoot, VaultRoot: vaultRoot,
				VaultReviewPath: "Projects/Controller/Session Review", VaultCaseMode: platform.CaseSensitive,
			}
			if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{mapping}}); err != nil {
				t.Fatal(err)
			}
			const sessionID = "77777777-7777-4777-8777-777777777777"
			var source bytes.Buffer
			for _, record := range []map[string]any{
				{"timestamp": "2026-09-08T00:00:00Z", "type": "session_meta", "payload": map[string]any{"id": sessionID, "cwd": projectRoot}},
				{"timestamp": "2026-09-08T00:00:01Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Can I read the retained answer?"}}}},
				{"timestamp": "2026-09-08T00:00:02Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "CLI integration retained answer."}}}},
			} {
				if err := json.NewEncoder(&source).Encode(record); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(sessionsRoot, "rollout-2026-09-08T00-00-00-"+sessionID+".jsonl"), source.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			runCurrentInitCLI(t, []string{"scan", "--project-id", mapping.ID, "--sessions-root", sessionsRoot, "--data-dir", dataRoot, "--json"})

			projectReviewPath := filepath.Join(projectRoot, "docs/session-review/项目回顾.md")
			if pending {
				document, err := reviewv4.ParseMarkdownDocument("项目回顾.md", readCurrentInitCLIFile(t, projectReviewPath))
				if err != nil {
					t.Fatal(err)
				}
				body, err := document.ReplaceFields(map[reviewv4.FieldKey]string{{Entity: "project-overview", Name: "goal"}: "Controller pending human goal"})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(projectReviewPath, body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotCurrentInitCLIPublication(t, mapping, dataRoot)
			stdout := runCurrentInitCLI(t, []string{"init", "--project", projectRoot, "--vault", vaultRoot, "--data-dir", dataRoot, "--write"})
			if !strings.Contains(stdout, "action: reuse\n") || !strings.Contains(stdout, "project_id: "+mapping.ID+"\n") || !strings.Contains(stdout, "written: true\n") {
				t.Fatalf("init did not report current reuse:\n%s", stdout)
			}
			if after := snapshotCurrentInitCLIPublication(t, mapping, dataRoot); !equalCurrentInitCLIBytes(before, after) {
				t.Fatal("init modified a real scan publication, Vault copy, or config")
			}

			if pending {
				runCurrentInitCLI(t, []string{"scan", "--project-id", mapping.ID, "--sessions-root", sessionsRoot, "--data-dir", dataRoot, "--json"})
				for _, root := range []string{filepath.Join(projectRoot, "docs/session-review"), filepath.Join(vaultRoot, mapping.VaultReviewPath)} {
					read := func(relative string) []byte { return readCurrentInitCLIFile(t, filepath.Join(root, relative)) }
					accepted, err := reviewv4.LoadProjection(read("项目回顾.md"), read("项目历史.md"), read(".session-reviewer/ledger.json"), read(".session-reviewer/session-index.json"))
					if err != nil || accepted.Review.CurrentState.Goal != "Controller pending human goal" {
						t.Fatalf("rescan lost human goal at %s: goal=%q err=%v", root, accepted.Review.CurrentState.Goal, err)
					}
				}
			}
		})
	}
}

func runCurrentInitCLI(t *testing.T, args []string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := Run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("%v code=%d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func snapshotCurrentInitCLIPublication(t *testing.T, mapping config.ProjectMapping, dataRoot string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	for _, root := range []string{filepath.Join(mapping.Root, "docs/session-review"), filepath.Join(mapping.VaultRoot, mapping.VaultReviewPath)} {
		for _, relative := range []string{"项目回顾.md", "项目历史.md", ".session-reviewer/ledger.json", ".session-reviewer/session-index.json"} {
			path := filepath.Join(root, relative)
			result[path] = readCurrentInitCLIFile(t, path)
		}
	}
	configPath := filepath.Join(dataRoot, "config.toml")
	result[configPath] = readCurrentInitCLIFile(t, configPath)
	return result
}

func readCurrentInitCLIFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func equalCurrentInitCLIBytes(first, second map[string][]byte) bool {
	if len(first) != len(second) {
		return false
	}
	for path, body := range first {
		if !bytes.Equal(body, second[path]) {
			return false
		}
	}
	return true
}
