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
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func TestFreshInitScanRepeatLifecyclePublishesCurrentMarkdown(t *testing.T) {
	projectRoot, vaultRoot, dataRoot, sessionsRoot := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = projectRoot
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v %s", err, output)
		}
	}
	const sessionID = "66666666-6666-4666-8666-666666666666"
	var source bytes.Buffer
	for _, record := range []map[string]any{
		{"timestamp": "2026-09-09T00:00:00Z", "type": "session_meta", "payload": map[string]any{"id": sessionID, "cwd": projectRoot}},
		{"timestamp": "2026-09-09T00:00:01Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Does first-use retain this answer?"}}}},
		{"timestamp": "2026-09-09T00:00:02Z", "type": "response_item", "payload": map[string]any{"type": "function_call", "call_id": "verify-first-use", "name": "exec_command", "arguments": `{"cmd":"go test ./internal/cli"}`}},
		{"timestamp": "2026-09-09T00:00:03Z", "type": "response_item", "payload": map[string]any{"type": "function_call_output", "call_id": "verify-first-use", "output": `{"exit_code":0,"output":"PASS"}`}},
		{"timestamp": "2026-09-09T00:00:04Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "First-use retained answer."}}}},
	} {
		if err := json.NewEncoder(&source).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sessionsRoot, "rollout-2026-09-09T00-00-00-"+sessionID+".jsonl"), source.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	initOut := runCurrentInitCLI(t, []string{"init", "--project", projectRoot, "--vault", vaultRoot, "--data-dir", dataRoot, "--write"})
	if !strings.Contains(initOut, "action: create\n") {
		t.Fatalf("fresh init output:\n%s", initOut)
	}
	cfg, err := config.Load(filepath.Join(dataRoot, "config.toml"))
	if err != nil || len(cfg.Projects) != 1 {
		t.Fatalf("fresh mapping=%+v err=%v", cfg.Projects, err)
	}
	mapping := cfg.Projects[0]
	beforeFirstScan := snapshotFreshInitPrivateState(t, mapping, dataRoot)
	repeatBeforeScan := runCurrentInitCLI(t, []string{"init", "--project", projectRoot, "--vault", vaultRoot, "--data-dir", dataRoot, "--write"})
	if !strings.Contains(repeatBeforeScan, "action: reuse\n") {
		t.Fatalf("repeat init before scan did not reuse:\n%s", repeatBeforeScan)
	}
	if afterRepeat := snapshotFreshInitPrivateState(t, mapping, dataRoot); !equalCurrentInitCLIBytes(beforeFirstScan, afterRepeat) {
		t.Fatal("repeat init before first scan changed bootstrap mapping or private state")
	}
	runCurrentInitCLI(t, []string{"scan", "--project-id", mapping.ID, "--sessions-root", sessionsRoot, "--data-dir", dataRoot, "--json"})

	load := func(root string, vault bool) reviewv4.Accepted {
		t.Helper()
		read := func(relative string) []byte {
			if vault {
				relative = strings.TrimPrefix(relative, "docs/session-review/")
			}
			return readCurrentInitCLIFile(t, filepath.Join(root, filepath.FromSlash(relative)))
		}
		accepted, err := reviewv4.LoadProjection(read(reviewv2.ReviewRelativePath), read(reviewv2.HistoryRelativePath), read(reviewv2.MachineLedgerRelativePath), read("docs/session-review/.session-reviewer/session-index.json"))
		if err != nil {
			t.Fatal(err)
		}
		return accepted
	}
	accepted := load(projectRoot, false)
	if len(accepted.SessionIndex.Sessions) != 1 || len(accepted.Review.Timeline) == 0 || !strings.Contains(accepted.Review.Timeline[0].ClosedLoop.Conclusion.Text, "First-use retained answer") {
		t.Fatalf("first-use projection is not readable: index=%+v timeline=%+v", accepted.SessionIndex.Sessions, accepted.Review.Timeline)
	}

	reviewPath := filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	document, err := reviewv4.ParseMarkdownDocument("项目回顾.md", readCurrentInitCLIFile(t, reviewPath))
	if err != nil {
		t.Fatal(err)
	}
	edited, err := document.ReplaceFields(map[reviewv4.FieldKey]string{{Entity: "project-overview", Name: "goal"}: "Fresh lifecycle human goal"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reviewPath, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeInit := snapshotCurrentInitCLIPublication(t, mapping, dataRoot)
	runCurrentInitCLI(t, []string{"init", "--project", projectRoot, "--vault", vaultRoot, "--data-dir", dataRoot, "--write"})
	if afterInit := snapshotCurrentInitCLIPublication(t, mapping, dataRoot); !equalCurrentInitCLIBytes(beforeInit, afterInit) {
		t.Fatal("repeat init changed current Project/Vault/config bytes")
	}
	runCurrentInitCLI(t, []string{"scan", "--project-id", mapping.ID, "--sessions-root", sessionsRoot, "--data-dir", dataRoot, "--json"})
	for _, target := range []struct {
		root  string
		vault bool
	}{{root: projectRoot}, {root: filepath.Join(vaultRoot, filepath.FromSlash(mapping.VaultReviewPath)), vault: true}} {
		if got := load(target.root, target.vault).Review.CurrentState.Goal; got != "Fresh lifecycle human goal" {
			t.Fatalf("human goal at %s=%q", target.root, got)
		}
	}
}

func snapshotFreshInitPrivateState(t *testing.T, mapping config.ProjectMapping, dataRoot string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	fragment := filepath.Join(dataRoot, config.ProjectFragmentsDir, mapping.ID+".toml")
	result[fragment] = readCurrentInitCLIFile(t, fragment)
	for _, relative := range []string{"locks/sync.lock"} {
		path := filepath.Join(dataRoot, "projects", mapping.ID, filepath.FromSlash(relative))
		result[path] = readCurrentInitCLIFile(t, path)
	}
	return result
}

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
	if body, err := os.ReadFile(configPath); err == nil {
		result[configPath] = body
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	fragmentPath := filepath.Join(dataRoot, config.ProjectFragmentsDir, mapping.ID+".toml")
	if body, err := os.ReadFile(fragmentPath); err == nil {
		result[fragmentPath] = body
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
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
