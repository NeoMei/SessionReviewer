package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/cursor"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/session"
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

func TestRunInitPreviewsWithoutWritingUntilWriteFlag(t *testing.T) {
	projectRoot := t.TempDir()
	vaultRoot := t.TempDir()
	dataRoot := filepath.Join(t.TempDir(), "data")
	args := []string{"init", "--project", projectRoot, "--vault", vaultRoot, "--data-dir", dataRoot}
	var out, errOut bytes.Buffer
	if code := Run(args, &out, &errOut); code != 0 || !strings.Contains(out.String(), "action: create") {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "docs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview wrote docs: %v", err)
	}
	out.Reset()
	errOut.Reset()
	if code := Run(append(args, "--write"), &out, &errOut); code != 0 || !strings.Contains(out.String(), "written: true") {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
}

func TestRunInitFailureDoesNotLeakUnpreviewedPaths(t *testing.T) {
	projectRoot := t.TempDir()
	vaultRoot := filepath.Join(t.TempDir(), "customer-secret-vault")
	ownerRoot := filepath.Join(t.TempDir(), "customer-secret-owner")
	dataRoot := t.TempDir()
	for _, path := range []string{vaultRoot, ownerRoot, filepath.Join(projectRoot, "docs", "session-review")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const projectID = "project-1111111111111111"
	if err := os.WriteFile(filepath.Join(projectRoot, "docs", "session-review", "project-overview.md"), []byte("---\nproject_id: "+projectID+"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: projectID, Root: ownerRoot, VaultRoot: vaultRoot,
	}}}); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"init", "--project", projectRoot, "--vault", vaultRoot, "--data-dir", dataRoot, "--write"}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "E_INIT_IDENTITY_CONFLICT") || !strings.Contains(errOut.String(), "recovery:") || strings.Contains(errOut.String(), ownerRoot) || strings.Contains(errOut.String(), vaultRoot) {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
}

func TestWriteInitDiagnosticClassifiesActionableFailuresWithoutDisclosure(t *testing.T) {
	const canary = "CUSTOMER-PRIVATE-PATH"
	tests := []struct {
		name     string
		err      error
		code     string
		hintPart string
	}{
		{name: "invalid root", err: fmt.Errorf("%s: %w", canary, project.ErrInvalidInitializationRoot), code: "E_INIT_ROOT_INVALID", hintPart: "check --project, --vault, and --data-dir"},
		{name: "nested roots", err: fmt.Errorf("%s: %w", canary, project.ErrNestedInitializationRoots), code: "E_INIT_ROOTS_NESTED", hintPart: "choose separate roots"},
		{name: "corrupt config", err: fmt.Errorf("%s: %w", canary, project.ErrCorruptInitializationConfig), code: "E_INIT_CONFIG_CORRUPT", hintPart: "restore config.toml"},
		{name: "identity conflict", err: fmt.Errorf("%s: %w", canary, project.ErrConflictingInitializationIdentity), code: "E_INIT_IDENTITY_CONFLICT", hintPart: "use the mapped --vault"},
		{name: "state changed", err: fmt.Errorf("%s: %w", canary, project.ErrInitializationStateChanged), code: "E_INIT_STATE_CHANGED", hintPart: "rerun init preview"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := writeInitDiagnostic(&out, test.err); code != 1 {
				t.Fatalf("code=%d", code)
			}
			got := out.String()
			if !strings.Contains(got, test.code) || !strings.Contains(got, test.hintPart) || strings.Contains(got, canary) {
				t.Fatalf("diagnostic=%q", got)
			}
		})
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

func TestRunPrepareCursorDriftPrintsOnlySafeRecoveryGuidance(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	data := filepath.Join(root, "data")
	project := filepath.Join(root, "secret-project-path")
	for _, dir := range []string{sessions, data, project} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(sessions, "rollout.jsonl")
	body := `{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"` + filepath.ToSlash(project) + `"}}` + "\n" +
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"SECRET-CONTENT"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var hash string
	if _, err := session.Stream(path, session.DecodeOptions{FromLine: 2}, func(record session.Record) error {
		if record.Line == 2 {
			hash = record.SourceHash
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "p1", Root: project}}}); err != nil {
		t.Fatal(err)
	}
	projectData := filepath.Join(data, "projects", "p1")
	if err := os.MkdirAll(projectData, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (cursor.Store{Root: projectData}).Commit("s1", cursor.Cursor{}, cursor.Cursor{SessionID: "s1", LastLine: 2, LastHash: hash, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	body = strings.Replace(body, "SECRET-CONTENT", "CHANGED-SECRET-CONTENT", 1)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run([]string{"prepare", "checkpoint", "--session", "s1", "--sessions-root", sessions, "--cwd", project, "--data-dir", data, "--output", filepath.Join(root, "packet.json")}, &out, &errOut)
	if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "prepare review --from-start") || strings.Contains(errOut.String(), "SECRET") || strings.Contains(errOut.String(), project) {
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
