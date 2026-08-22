package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
)

func TestRunRequiresCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run(nil, &out, &errOut)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "Usage: session-reviewer") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"version"}, &out, &errOut)
	if code != 0 || strings.TrimSpace(out.String()) != "dev" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"unknown"}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), `unknown command "unknown"`) {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}

func TestRunInitRequiresProjectAndVault(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"init"}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "init requires --project and --vault") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunPrepareRequiresMode(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"prepare"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "requires review or checkpoint") {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}

func TestRunPrepareRequiresOutput(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"prepare", "review"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "requires --output") {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}

func TestRunCheckpointRejectsFromStart(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"prepare", "checkpoint", "--from-start", "--output", "evidence.json"}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "valid only for review") {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}

func TestRunPrepareSuccessIsSilentAndWritesEvidence(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	data := filepath.Join(root, "data")
	project := filepath.Join(root, "project")
	for _, dir := range []string{sessions, data, project} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sessionPath := filepath.Join(sessions, "rollout.jsonl")
	const canary = "CLI-EVENT-CANARY"
	body := `{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"` + filepath.ToSlash(project) + `"}}` + "\n" +
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"` + canary + `"}]}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(sessionPath, now, now); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "p1", Root: project}}}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "out", "packet.json")
	var out, errOut bytes.Buffer
	code := Run([]string{"prepare", "review", "--sessions-root", sessions, "--cwd", project, "--data-dir", data, "--output", output}, &out, &errOut)
	if code != 0 || out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatal(err)
	}
}

func TestRunPrepareFailureDoesNotPrintSessionContent(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	data := filepath.Join(root, "data")
	project := filepath.Join(root, "project")
	for _, dir := range []string{sessions, data, project} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const canary = "CLI-FAILURE-CANARY"
	body := `{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"` + filepath.ToSlash(project) + `"}}` + "\n" +
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","role":"user","content":"` + canary + `"}}` + "\n"
	path := filepath.Join(sessions, "rollout.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "p1", Root: project}}}); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run([]string{"prepare", "review", "--session", "s1", "--sessions-root", sessions, "--cwd", project, "--data-dir", data, "--output", filepath.Join(root, "packet.json")}, &out, &errOut)
	if code != 1 || out.Len() != 0 || strings.Contains(errOut.String(), canary) || strings.Contains(errOut.String(), project) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunPrepareRejectsInvalidModeAndExtraArguments(t *testing.T) {
	for _, args := range [][]string{{"prepare", "unknown"}, {"prepare", "review", "--output", "packet.json", "extra"}} {
		var out, errOut bytes.Buffer
		if code := Run(args, &out, &errOut); code != 2 || out.Len() != 0 {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, out.String(), errOut.String())
		}
	}
}
