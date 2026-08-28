package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

// Reconstructing the engine in the command, resolving the platform data root
// inside the service, or moving report formatting out of the CLI breaks this
// boundary test even when the underlying engine still works.
func TestSyncProjectServiceCLIDelegationPreservesFormatting(t *testing.T) {
	original := syncProject
	t.Cleanup(func() { syncProject = original })
	dataRoot := t.TempDir()
	wantReport := syncengine.Report{
		ProjectID: "project-1111111111111111",
		DryRun:    true,
		Operations: []syncengine.Operation{{
			EntityID: "project-overview", Kind: syncengine.OperationAddVault, RelativePath: "项目回顾.md",
		}},
		Derived:   syncengine.DerivedReport{State: syncengine.DerivedCurrent, Operations: []syncengine.Operation{}},
		Migration: syncengine.MigrationReport{DryRun: true, Creates: []string{}, Archives: []string{}},
		Machine:   syncengine.MachineReport{State: syncengine.MachineCurrent, Operations: []syncengine.Operation{}},
	}
	var got syncproject.Options
	syncProject = func(ctx context.Context, options syncproject.Options) (syncengine.Report, error) {
		if ctx == nil {
			t.Fatal("CLI passed a nil sync context")
		}
		got = options
		return wantReport, nil
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"sync", "--dry-run", "--project-id", wantReport.ProjectID, "--data-dir", dataRoot}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if got.ProjectID != wantReport.ProjectID || got.CWD != "" || got.DataDir != dataRoot || got.GOOS != runtime.GOOS || got.Now == nil || got.Trigger != syncengine.TriggerCLI || !got.DryRun {
		t.Fatalf("delegated options = %#v", got)
	}
	var expected bytes.Buffer
	writeSyncReport(&expected, wantReport)
	if stdout.String() != expected.String() {
		t.Fatalf("stdout=%q want=%q", stdout.String(), expected.String())
	}
}

// Mapping and CWD authentication belong to the extracted service. Resolving
// CWD in the CLI first would change legacy error ordering and bypass the seam.
func TestSyncProjectServiceCLILeavesCWDResolutionToService(t *testing.T) {
	original := syncProject
	t.Cleanup(func() { syncProject = original })
	dataRoot := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "missing")
	calls := 0
	var got syncproject.Options
	syncProject = func(_ context.Context, options syncproject.Options) (syncengine.Report, error) {
		calls++
		got = options
		return syncengine.Report{}, errors.New("fixture service failure")
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"sync", "--cwd", cwd, "--data-dir", dataRoot}, &stdout, &stderr)
	if code == 0 || calls != 1 || got.CWD != cwd || got.DataDir != dataRoot || stdout.Len() != 0 || !strings.Contains(stderr.String(), "E_SYNC_FAILED") {
		t.Fatalf("code=%d calls=%d options=%#v stdout=%q stderr=%q", code, calls, got, stdout.String(), stderr.String())
	}
}

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
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "add_vault project-overview") || !strings.Contains(out.String(), "derived=current files=0") {
		t.Fatalf("sync code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if body, err := os.ReadFile(filepath.Join(reviewRoot, "项目回顾.md")); err != nil || !strings.Contains(string(body), "# SessionReviewer v2") {
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
		"derived=current files=0",
		"add_vault project-history 项目历史.md",
		"add_vault project-overview 项目回顾.md",
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
	if err := json.Unmarshal(out.Bytes(), &status); err != nil || status["project_id"] != fixture.projectID || status["derived_state"] != "current" || status["derived_files"] != float64(0) {
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

func TestRunSyncProjectIDSelectsPinnedMappingAndRejectsCWDCombination(t *testing.T) {
	fixture := newCLISyncFixture(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "--dry-run", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "migration=current") {
		t.Fatalf("project-id code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code = Run([]string{"sync", "--dry-run", "--project-id", fixture.projectID, "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "mutually exclusive") {
		t.Fatalf("combined code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunSyncStatusJSONIncludesV2MachineMigrationAndConflictFields(t *testing.T) {
	fixture := newCLISyncFixture(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "status", "--json", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var status map[string]any
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"migration", "machine_state", "last_successful_sync", "pending_operations", "hidden_conflict_ids"} {
		if _, exists := status[key]; !exists {
			t.Fatalf("status missing %q: %v", key, status)
		}
	}
}

func TestRunSyncPlainStatusIncludesV2MigrationAndMachineState(t *testing.T) {
	fixture := newCLISyncFixture(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "status", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, want := range []string{"migration=current", "machine=pending", "pending_operations=", "hidden_conflicts="} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("plain status missing %q: %q", want, out.String())
		}
	}
}

func TestRunSyncRepairMachineLedgerHasNoArbitraryPathAndRestoresProjectBytes(t *testing.T) {
	fixture := newCLISyncFixture(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"sync", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut); code != 0 {
		t.Fatalf("initial sync code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	projectPath := filepath.Join(fixture.project, filepath.FromSlash(reviewv2.MachineLedgerRelativePath))
	vaultPath := filepath.Join(fixture.vault, "Projects", "CLI--11111111", "Session Review", ".session-reviewer", "ledger.json")
	want, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultPath, []byte("tampered machine ledger"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"sync", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut); code == 0 || !strings.Contains(out.String(), "machine_ledger_modified") {
		t.Fatalf("tamper code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code := Run([]string{"sync", "repair-machine-ledger", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "machine=current files=1") {
		t.Fatalf("repair code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	got, err := os.ReadFile(vaultPath)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("vault machine was not repaired err=%v", err)
	}
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"sync", "repair-machine-ledger", "unexpected", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut); code != 2 {
		t.Fatalf("arbitrary path code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunSyncReportsMigrationAndPublishesReviewV2(t *testing.T) {
	fixture := newCLILegacySyncFixture(t)
	projectInfo, err := os.Stat(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviewv2.PlanMigration(fixture.project, projectInfo, filepath.Join(fixture.data, "projects", fixture.projectID), time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("legacy fixture cannot be migrated: %v", err)
	}
	projectBefore := snapshotCLITree(t, fixture.project)
	dataBefore := snapshotCLITree(t, fixture.data)
	vaultBefore := snapshotCLITree(t, fixture.vault)
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "--dry-run", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "migration=required") {
		t.Fatalf("dry-run code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, want := range []string{
		"migration_create docs/session-review/.session-reviewer/ledger.json",
		"migration_create docs/session-review/项目历史.md",
		"migration_create docs/session-review/项目回顾.md",
		"migration_archive docs/session-review/current-state.md",
		"migration_archive docs/session-review/project-overview.md",
	} {
		if !strings.Contains(out.String(), want+"\n") {
			t.Fatalf("migration preview missing %q in %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), fixture.project) || strings.Contains(out.String(), fixture.vault) {
		t.Fatalf("migration preview leaked absolute path: %q", out.String())
	}
	if snapshotCLITree(t, fixture.project) != projectBefore || snapshotCLITree(t, fixture.data) != dataBefore || snapshotCLITree(t, fixture.vault) != vaultBefore {
		t.Fatal("migration dry-run wrote to project, data, or vault")
	}
	out.Reset()
	errOut.Reset()
	code = Run([]string{"sync", "--project-id", fixture.projectID, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "migration=current") || !strings.Contains(out.String(), "machine=current files=1") {
		t.Fatalf("sync code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if _, err := reviewv2.Load(fixture.project); err != nil {
		t.Fatalf("load migrated v2: %v", err)
	}
	backupRoot := filepath.Join(fixture.project, "docs", "session-review", ".session-reviewer", "backups")
	entries, err := os.ReadDir(backupRoot)
	directories := 0
	for _, entry := range entries {
		if entry.IsDir() {
			directories++
		}
	}
	if err != nil || directories != 1 {
		t.Fatalf("backup entries=%v err=%v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.vault, "Projects", "Legacy--269b8cab", "Session Review", ".session-reviewer", "backups")); !os.IsNotExist(err) {
		t.Fatalf("migration backup escaped to vault: %v", err)
	}
}

func TestRunSyncResolveUsesPersistedHiddenConflictIdentity(t *testing.T) {
	fixture := newCLISyncFixture(t)
	reviewRoot := filepath.Join(fixture.vault, "Projects", "CLI--11111111", "Session Review")
	var out, errOut bytes.Buffer
	if code := Run([]string{"sync", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut); code != 0 {
		t.Fatalf("initial sync code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	projectPath := filepath.Join(fixture.project, "docs", "session-review", "项目历史.md")
	vaultPath := filepath.Join(reviewRoot, "项目历史.md")
	projectBody, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	vaultBody, err := os.ReadFile(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	projectBody = bytes.Replace(projectBody, []byte("信任链与 dry-run 边界修复"), []byte("Project choice"), 1)
	vaultBody = bytes.Replace(vaultBody, []byte("信任链与 dry-run 边界修复"), []byte("Vault choice"), 1)
	if err := os.WriteFile(projectPath, projectBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultPath, vaultBody, 0o644); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errOut.Reset()
	if code := Run([]string{"sync", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "conflicts: 1") {
		t.Fatalf("conflict code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"sync", "status", "--json", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut); code != 0 {
		t.Fatalf("status code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var status struct {
		OpenConflicts []string `json:"open_conflicts"`
	}
	if err := json.Unmarshal(out.Bytes(), &status); err != nil || len(status.OpenConflicts) != 1 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	out.Reset()
	errOut.Reset()
	code := Run([]string{"sync", "resolve", "--conflict", status.OpenConflicts[0], "--action", "accept_project", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "derived=current files=0") {
		t.Fatalf("resolve code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	projectBody, err = os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	vaultBody, err = os.ReadFile(vaultPath)
	if err != nil || !bytes.Equal(projectBody, vaultBody) || !bytes.Contains(projectBody, []byte("Project choice")) || bytes.Contains(projectBody, []byte("Vault choice")) {
		t.Fatalf("resolved project=%q vault=%q err=%v", projectBody, vaultBody, err)
	}
}

func TestRunSyncReturnsFailureAndSafeEntityDiagnosticsForPartialErrors(t *testing.T) {
	fixture := newCLISyncFixture(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"sync", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut); code != 0 {
		t.Fatalf("initial code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	projectPath := filepath.Join(fixture.project, "docs", "session-review", "项目历史.md")
	if err := os.WriteFile(projectPath, []byte("malformed-secret-canary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	code := Run([]string{"sync", "--cwd", fixture.project, "--data-dir", fixture.data}, &out, &errOut)
	if code == 0 || !strings.Contains(out.String(), "entity_error project-history malformed_source") || !strings.Contains(out.String(), "derived=deferred files=0") || !strings.Contains(errOut.String(), "E_SYNC_PARTIAL") {
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

func TestFailedSyncReportRequiresMaterialProgress(t *testing.T) {
	defaults := syncengine.Report{
		Derived:   syncengine.DerivedReport{State: syncengine.DerivedDeferred, Operations: []syncengine.Operation{}},
		Migration: syncengine.MigrationReport{Creates: []string{}, Archives: []string{}},
		Machine:   syncengine.MachineReport{State: syncengine.MachinePending, Operations: []syncengine.Operation{}},
	}
	if shouldWriteFailedSyncReport(defaults) {
		t.Fatal("default report would misleadingly claim current migration after an early failure")
	}
	defaults.Migration.Required = true
	defaults.Migration.Creates = []string{"docs/session-review/项目回顾.md"}
	if !shouldWriteFailedSyncReport(defaults) {
		t.Fatal("material migration progress was hidden")
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
	if code == 0 || !strings.Contains(errOut.String(), "E_SYNC_FAILED") {
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
	writeCLIV2Fixture(t, fixture)
	cfg := config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: fixture.projectID, Root: fixture.project, VaultRoot: fixture.vault,
		VaultReviewPath: "Projects/CLI--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}}
	if err := config.Save(filepath.Join(fixture.data, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func newCLILegacySyncFixture(t *testing.T) cliSyncFixture {
	t.Helper()
	root := t.TempDir()
	fixture := cliSyncFixture{
		project: filepath.Join(root, "project"), vault: filepath.Join(root, "vault"),
		data: filepath.Join(root, "data"), projectID: "project-269b8cab6cbf69dd",
	}
	projectData := filepath.Join(fixture.data, "projects", fixture.projectID)
	for _, directory := range []string{
		fixture.project, fixture.vault,
		filepath.Join(projectData, "merge-bases"), filepath.Join(projectData, "queue"),
		filepath.Join(projectData, "transactions"), filepath.Join(projectData, "locks"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	legacyRoot := filepath.Join(fixture.project, "docs", "session-review")
	if err := os.MkdirAll(legacyRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	overview := "---\nid: project-overview\nentity_type: project_overview\nproject_id: " + fixture.projectID + "\nrevision: 1\nsync_status: synced\n---\n\n# Legacy fixture\n"
	if err := os.WriteFile(filepath.Join(legacyRoot, "project-overview.md"), []byte(overview), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy, err := ledger.Load(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	current := ledger.CurrentState{
		ProjectID: fixture.projectID, Revision: 1, Goal: "Migrate a realistic accepted legacy ledger",
		LastVerified: "legacy fixture loaded", Branch: "fixture", NextAction: "preview migration",
		FirstInspection: "docs/session-review/project-overview.md", LastUpdated: "2026-08-26T00:00:00Z",
	}
	plan, err := ledger.Render(legacy, ledger.ChangeSet{Current: &current})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(plan); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectData, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(fixture.data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: fixture.projectID, Root: fixture.project, VaultRoot: fixture.vault,
		VaultReviewPath: "Projects/Legacy--269b8cab/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func writeCLIV2Fixture(t *testing.T, fixture cliSyncFixture) {
	t.Helper()
	root := filepath.Join(fixture.project, "docs", "session-review")
	written := make(map[string][]byte, 2)
	for _, name := range []string{"项目回顾.valid.md", "项目历史.valid.md"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "review-v2", name))
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.ReplaceAll(body, []byte("project-0123456789abcdef"), []byte(fixture.projectID))
		target := strings.TrimSuffix(name, ".valid.md") + ".md"
		if err := os.WriteFile(filepath.Join(root, target), body, 0o644); err != nil {
			t.Fatal(err)
		}
		written[target] = body
	}
	machineBody, err := os.ReadFile(filepath.Join("..", "..", "testdata", "review-v2", "ledger.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	machine, err := reviewv2.ParseMachineLedger(machineBody)
	if err != nil {
		t.Fatal(err)
	}
	machine.ProjectID = fixture.projectID
	machine.AcceptedRevision = 1
	for index := range machine.Sessions {
		machine.Sessions[index].ProjectID = fixture.projectID
	}
	machine.LegacyCompatibility.CurrentState.ProjectID = fixture.projectID
	machine.LegacyCompatibility.CurrentState.Revision = 1
	for index := range machine.LegacyCompatibility.Decisions {
		machine.LegacyCompatibility.Decisions[index].ProjectID = fixture.projectID
		machine.LegacyCompatibility.Decisions[index].Revision = 1
	}
	for index := range machine.LegacyCompatibility.OpenLoops {
		machine.LegacyCompatibility.OpenLoops[index].ProjectID = fixture.projectID
		machine.LegacyCompatibility.OpenLoops[index].Revision = 1
	}
	for index := range machine.LegacyCompatibility.Timeline {
		machine.LegacyCompatibility.Timeline[index].Revision = 1
	}
	machine.ReviewSHA256 = syncdoc.ContentHash(written["项目回顾.md"])
	machine.HistorySHA256 = syncdoc.ContentHash(written["项目历史.md"])
	machineBody, err = reviewv2.RenderMachineLedger(machine)
	if err != nil {
		t.Fatal(err)
	}
	machineRoot := filepath.Join(root, ".session-reviewer")
	if err := os.MkdirAll(machineRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machineRoot, "ledger.json"), machineBody, 0o600); err != nil {
		t.Fatal(err)
	}
}
