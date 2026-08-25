package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/platform"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

func TestRunSyncDryRunAndApplyExposeEditableVaultCopy(t *testing.T) {
	fixture := newCLISyncFixture(t)
	reviewRoot := filepath.Join(fixture.vault, "Projects", "CLI--11111111", "Session Review")

	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "--dry-run", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "add_vault project-overview") {
		t.Fatalf("dry-run code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(reviewRoot); !os.IsNotExist(err) {
		t.Fatalf("dry-run created vault review root: %v", err)
	}

	out.Reset()
	errOut.Reset()
	code = Run([]string{"sync", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "add_vault project-overview") || !strings.Contains(out.String(), "derived=current files=5") {
		t.Fatalf("sync code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if body, err := os.ReadFile(filepath.Join(reviewRoot, "project-overview.md")); err != nil || !strings.Contains(string(body), "# CLI Sync") {
		t.Fatalf("vault body=%q err=%v", body, err)
	}
}

func TestSyncOutputReportsDerivedStateAndRelativePaths(t *testing.T) {
	fixture := newCLISyncFixture(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "--dry-run", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("dry-run code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, want := range []string{
		"derived=pending files=5",
		"derived_operation add_vault decisions/00-目录说明.md",
		"derived_operation update_project project-overview.md",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), fixture.project) || strings.Contains(out.String(), fixture.vault) {
		t.Fatalf("derived output leaked an absolute path: %q", out.String())
	}
}

func TestRunSyncStatusJSONAndResolveGrammar(t *testing.T) {
	fixture := newCLISyncFixture(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "status", "--json", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("status code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var status map[string]any
	if err := json.Unmarshal(out.Bytes(), &status); err != nil || status["project_id"] != fixture.projectID || status["derived_state"] != "pending" || status["derived_files"] != float64(5) {
		t.Fatalf("status=%v err=%v", status, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.vault, "Projects", "CLI--11111111", "Session Review")); !os.IsNotExist(err) {
		t.Fatalf("status mutated the vault: %v", err)
	}

	for _, action := range []string{"", "project", "delete", "ACCEPT_PROJECT"} {
		out.Reset()
		errOut.Reset()
		args := []string{"sync", "resolve", "--conflict", "conflict-project-overview", "--action", action, "--cwd", fixture.project, "--data-dir", fixture.data}
		if code := Run(args, &out, &errOut); code != 2 || out.Len() != 0 {
			t.Fatalf("action=%q code=%d stdout=%q stderr=%q", action, code, out.String(), errOut.String())
		}
	}
}

func TestRunSyncResolveRefreshesDerivedNavigation(t *testing.T) {
	fixture := newCLISyncFixture(t)
	reviewRoot := filepath.Join(fixture.vault, "Projects", "CLI--11111111", "Session Review")
	var out, errOut bytes.Buffer
	if code := Run([]string{"sync", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut); code != 0 {
		t.Fatalf("initial sync code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	projectPath := filepath.Join(fixture.project, "docs", "session-review", "project-overview.md")
	vaultPath := filepath.Join(reviewRoot, "project-overview.md")
	projectBody, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	vaultBody, err := os.ReadFile(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, append(projectBody, []byte("\n## User note\n\nProject choice.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultPath, append(vaultBody, []byte("\n## User note\n\nVault choice.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errOut.Reset()
	code := Run([]string{"sync", "resolve", "--conflict", "conflict-project-overview", "--action", "accept_project", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "derived=current files=5") {
		t.Fatalf("resolve code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	projectBody, err = os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	vaultBody, err = os.ReadFile(vaultPath)
	if err != nil || !bytes.Equal(projectBody, vaultBody) || !bytes.Contains(projectBody, []byte("Project choice.")) || bytes.Contains(projectBody, []byte("Vault choice.")) {
		t.Fatalf("resolved project=%q vault=%q err=%v", projectBody, vaultBody, err)
	}
}

func TestRunSyncResolveReturnsFailureWhenFollowupReconcileIsPartial(t *testing.T) {
	fixture := newCLISyncFixture(t)
	reviewRoot := filepath.Join(fixture.vault, "Projects", "CLI--11111111", "Session Review")
	var out, errOut bytes.Buffer
	if code := Run([]string{"sync", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut); code != 0 {
		t.Fatalf("initial sync code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	projectPath := filepath.Join(fixture.project, "docs", "session-review", "project-overview.md")
	vaultPath := filepath.Join(reviewRoot, "project-overview.md")
	projectBody, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	vaultBody, err := os.ReadFile(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, append(projectBody, []byte("\n## User note\n\nProject choice.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultPath, append(vaultBody, []byte("\n## User note\n\nVault choice.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	secretDocument := "---\nid: decision-sensitive\nentity_type: decision\nproject_id: " + fixture.projectID + "\nrevision: 1\nsync_status: synced\n---\n\n# Sensitive\n\napi_key=sk-abcdefghijklmnopqrstuvwxyz012345\n"
	if err := os.WriteFile(filepath.Join(fixture.project, "docs", "session-review", "decision-sensitive.md"), []byte(secretDocument), 0o600); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errOut.Reset()
	code := Run([]string{"sync", "resolve", "--conflict", "conflict-project-overview", "--action", "accept_project", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code == 0 || !strings.Contains(out.String(), "entity_error decision-sensitive sensitive_content") || !strings.Contains(out.String(), "derived=deferred files=0") || !strings.Contains(errOut.String(), "E_SYNC_PARTIAL") {
		t.Fatalf("resolve code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "sk-abcdefghijklmnopqrstuvwxyz012345") || strings.Contains(errOut.String(), "sk-abcdefghijklmnopqrstuvwxyz012345") {
		t.Fatalf("diagnostic leaked sensitive content: stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestRunSyncReturnsFailureAndSafeEntityDiagnosticsForPartialErrors(t *testing.T) {
	fixture := newCLISyncFixture(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"sync", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut); code != 0 {
		t.Fatalf("initial code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	projectPath := filepath.Join(fixture.project, "docs", "session-review", "project-overview.md")
	if err := os.WriteFile(projectPath, []byte("malformed-secret-canary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	code := Run([]string{"sync", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code == 0 || !strings.Contains(out.String(), "entity_error project-overview malformed_source") || !strings.Contains(out.String(), "derived=deferred files=0") || !strings.Contains(errOut.String(), "E_SYNC_PARTIAL") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "malformed-secret-canary") || strings.Contains(errOut.String(), "malformed-secret-canary") {
		t.Fatalf("diagnostic leaked content: stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestWriteSyncReportReportsFailedDerivedWithoutSensitiveContent(t *testing.T) {
	var out bytes.Buffer
	writeSyncReport(&out, syncengine.Report{
		ProjectID: "project-1111111111111111",
		Derived: syncengine.DerivedReport{
			State: syncengine.DerivedFailed,
			Files: 7,
			Operations: []syncengine.Operation{{
				Kind: syncengine.OperationUpdateVault, RelativePath: "decisions/00-目录说明.md",
			}},
		},
	})
	for _, want := range []string{"derived_operation update_vault decisions/00-目录说明.md", "derived=failed files=7"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), "project-1111111111111111") {
		t.Fatalf("content-free derived report exposed project identity: %q", out.String())
	}
}

func TestRunSyncReturnsFailureForUnassociatedMalformedDocument(t *testing.T) {
	fixture := newCLISyncFixture(t)
	bad := filepath.Join(fixture.project, "docs", "session-review", "unparseable.md")
	if err := os.WriteFile(bad, []byte("private-malformed-canary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "--dry-run", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code == 0 || !strings.Contains(out.String(), "scan_issue malformed") || !strings.Contains(errOut.String(), "E_SYNC_PARTIAL") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "private-malformed-canary") || strings.Contains(errOut.String(), "private-malformed-canary") {
		t.Fatalf("diagnostic leaked content: stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

type cliSyncFixture struct{ project, vault, data, projectID string }

func newCLISyncFixture(t *testing.T) cliSyncFixture {
	t.Helper()
	root := t.TempDir()
	fixture := cliSyncFixture{project: filepath.Join(root, "project"), vault: filepath.Join(root, "vault"), data: filepath.Join(root, "data"), projectID: "project-1111111111111111"}
	projectData := filepath.Join(fixture.data, "projects", fixture.projectID)
	for _, directory := range []string{
		filepath.Join(fixture.project, "docs", "session-review"), fixture.vault,
		filepath.Join(projectData, "merge-bases"), filepath.Join(projectData, "queue"),
		filepath.Join(projectData, "transactions"), filepath.Join(projectData, "locks"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(projectData, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	overview := "---\nid: project-overview\nentity_type: project_overview\nproject_id: " + fixture.projectID + "\nrevision: 1\nsync_status: synced\ncreated_at: 2026-08-25T00:00:00Z\n---\n\n# CLI Sync\n"
	if err := os.WriteFile(filepath.Join(fixture.project, "docs", "session-review", "project-overview.md"), []byte(overview), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: fixture.projectID, Root: fixture.project, VaultRoot: fixture.vault,
		VaultReviewPath: "Projects/CLI--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}}
	if err := config.Save(filepath.Join(fixture.data, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	return fixture
}
