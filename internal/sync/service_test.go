package sync

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
)

func TestEngineRecoversInterruptedTwoSideWriteFromContentFreeJournal(t *testing.T) {
	fixture := newEngineFixture(t)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	vaultOverview := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "project-overview.md")
	body, err := os.ReadFile(vaultOverview)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(body), "# SessionReviewer", "# interrupted edit", 1)
	if err := os.WriteFile(vaultOverview, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	engine.writer.beforeWrite = func(side Side, _ *os.Root, _ string) error {
		if side == SideVault {
			return errors.New("injected vault write failure")
		}
		return nil
	}
	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(report.Errors) != 1 {
		t.Fatalf("interrupted report=%+v err=%v", report, err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	recovered, err := restarted.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(recovered.Errors) != 0 || len(recovered.Conflicts) != 0 {
		t.Fatalf("recovered report=%+v err=%v", recovered, err)
	}
	projectBody, _ := os.ReadFile(filepath.Join(fixture.project, "docs", "session-review", "project-overview.md"))
	vaultBody, _ := os.ReadFile(vaultOverview)
	if !strings.Contains(string(projectBody), "# interrupted edit") || !strings.Contains(string(projectBody), "revision: 2") || string(projectBody) != string(vaultBody) {
		t.Fatalf("project=%s\nvault=%s", projectBody, vaultBody)
	}
}

func TestEngineResolvesLiveUnitConflictByAcceptingProject(t *testing.T) {
	fixture := newEngineFixture(t)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(fixture.project, "docs", "session-review", "project-overview.md")
	vaultPath := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "project-overview.md")
	projectBody, _ := os.ReadFile(projectPath)
	vaultBody, _ := os.ReadFile(vaultPath)
	if err := os.WriteFile(projectPath, []byte(strings.Replace(string(projectBody), "note: base", "note: project-choice", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultPath, []byte(strings.Replace(string(vaultBody), "note: base", "note: vault-choice", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	conflicted, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(conflicted.Conflicts) != 1 || conflicted.Conflicts[0] != "conflict-project-overview" {
		t.Fatalf("conflicted=%+v err=%v", conflicted, err)
	}
	resolved, err := engine.Resolve(context.Background(), Resolution{ConflictID: "conflict-project-overview", Action: AcceptProject})
	if err != nil || len(resolved.Conflicts) != 0 || len(resolved.Operations) != 2 || resolved.Derived.State != DerivedCurrent || resolved.Derived.Files < 5 {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	projectBody, _ = os.ReadFile(projectPath)
	vaultBody, _ = os.ReadFile(vaultPath)
	if string(projectBody) != string(vaultBody) || !strings.Contains(string(projectBody), "note: project-choice") || !strings.Contains(string(projectBody), "revision: 2") {
		t.Fatalf("project=%s\nvault=%s", projectBody, vaultBody)
	}
}

func TestEngineRoundTripsProjectAndObsidianEditsAndThenBecomesNoop(t *testing.T) {
	fixture := newEngineFixture(t)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	first, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Operations) != 1 || first.Operations[0].Kind != OperationAddVault {
		t.Fatalf("first report=%+v", first)
	}
	vaultOverview := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "project-overview.md")
	body, err := os.ReadFile(vaultOverview)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(body), "# SessionReviewer", "# SessionReviewer edited in Obsidian", 1)
	if err := os.WriteFile(vaultOverview, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	second, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Operations) != 2 || second.Operations[0].Kind != OperationUpdateProject || second.Operations[1].Kind != OperationUpdateVault {
		t.Fatalf("second report=%+v", second)
	}
	projectBody, err := os.ReadFile(filepath.Join(fixture.project, "docs", "session-review", "project-overview.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(projectBody), "edited in Obsidian") || !strings.Contains(string(projectBody), "revision: 2") {
		t.Fatalf("project body=%s", projectBody)
	}

	third, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(third.Operations) != 0 {
		t.Fatalf("third report=%+v err=%v", third, err)
	}
}

func TestEnginePublishesReceiptTrustedProjectProvenanceWithoutWeakeningHumanValidation(t *testing.T) {
	fixture := newEngineFixture(t)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}

	projectPath := filepath.Join(fixture.project, "docs", "session-review", "project-overview.md")
	projectBody, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	trustedBody := []byte(strings.Replace(string(projectBody), "revision: 1", "revision: 2", 1))
	if err := os.WriteFile(projectPath, trustedBody, 0o644); err != nil {
		t.Fatal(err)
	}
	engine.trustAppliedTransition = func(relative string, preimageExists bool, preimageHash, targetHash string) (bool, error) {
		if relative != "docs/session-review/project-overview.md" || !preimageExists || preimageHash != syncdoc.ContentHash(projectBody) || targetHash != syncdoc.ContentHash(trustedBody) {
			t.Fatalf("unexpected trust query: relative=%q exists=%t preimage=%q target=%q", relative, preimageExists, preimageHash, targetHash)
		}
		return true, nil
	}
	dryRun, err := engine.Reconcile(context.Background(), ReconcileRequest{DryRun: true, Trigger: TriggerCLI})
	if err != nil || len(dryRun.Conflicts) != 0 || len(dryRun.Errors) != 0 || len(dryRun.Operations) != 1 || dryRun.Derived.State != DerivedDeferred {
		t.Fatalf("trusted dry-run=%+v err=%v", dryRun, err)
	}

	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(report.Conflicts) != 0 || len(report.Errors) != 0 || len(report.Operations) != 1 || report.Operations[0].Kind != OperationUpdateVault {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	vaultBody, err := os.ReadFile(filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "project-overview.md"))
	if err != nil || !bytes.Equal(vaultBody, trustedBody) {
		t.Fatalf("vault=%q err=%v", vaultBody, err)
	}
	repeat, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(repeat.Operations) != 0 || len(repeat.Conflicts) != 0 {
		t.Fatalf("repeat=%+v err=%v", repeat, err)
	}
}

func TestEngineDoesNotPublishTrustedProjectProvenanceOverConcurrentVaultEdit(t *testing.T) {
	fixture := newEngineFixture(t)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}

	projectPath := filepath.Join(fixture.project, "docs", "session-review", "project-overview.md")
	vaultPath := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "project-overview.md")
	projectBody, _ := os.ReadFile(projectPath)
	vaultBody, _ := os.ReadFile(vaultPath)
	if err := os.WriteFile(projectPath, []byte(strings.Replace(string(projectBody), "revision: 1", "revision: 2", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	concurrentVault := []byte(strings.Replace(string(vaultBody), "note: base", "note: concurrent-vault", 1))
	if err := os.WriteFile(vaultPath, concurrentVault, 0o644); err != nil {
		t.Fatal(err)
	}
	engine.trustAppliedTransition = func(string, bool, string, string) (bool, error) {
		t.Fatal("receipt trust must not be consulted when Vault diverged from the merge base")
		return false, nil
	}

	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(report.Conflicts) != 1 || report.Conflicts[0] != "conflict-project-overview" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	after, err := os.ReadFile(vaultPath)
	if err != nil || !bytes.Equal(after, concurrentVault) {
		t.Fatalf("vault overwritten: %q err=%v", after, err)
	}
}

func TestEngineDryRunPlansInitialVaultCopyWithoutFilesystemChanges(t *testing.T) {
	fixture := newEngineFixture(t)
	before := snapshotFixtureTree(t, fixture.root)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	report, err := engine.Reconcile(context.Background(), ReconcileRequest{DryRun: true, Trigger: TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Operations) != 1 || report.Operations[0].Kind != OperationAddVault {
		t.Fatalf("report=%+v", report)
	}
	if after := snapshotFixtureTree(t, fixture.root); !reflect.DeepEqual(before, after) {
		t.Fatalf("dry-run changed tree: before=%v after=%v", before, after)
	}
}

func TestEngineDryRunDefersDerivedPlanningUntilSemanticEditIsAccepted(t *testing.T) {
	fixture := newEngineFixture(t)
	writeFixtureDecision(t, fixture, "decisions/dry-run-edit.md")
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(fixture.project, "docs", "session-review", "decisions", "dry-run-edit.md")
	projectBody, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	projectBody = append(projectBody, []byte("\n## User note\n\nPreview this semantic edit safely.\n")...)
	if err := os.WriteFile(projectPath, projectBody, 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := engine.Reconcile(context.Background(), ReconcileRequest{DryRun: true, Trigger: TriggerCLI})
	if err != nil {
		t.Fatalf("dry-run rejected a valid semantic edit: report=%+v err=%v", report, err)
	}
	if len(report.Operations) == 0 || report.Derived.State != DerivedDeferred {
		t.Fatalf("dry-run did not defer derived publication: %+v", report)
	}
}

func TestEngineDoesNotOverwriteMalformedSynchronizedPath(t *testing.T) {
	fixture := newEngineFixture(t)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(fixture.project, "docs", "session-review", "project-overview.md")
	malformed := []byte("malformed-project-canary\n")
	if err := os.WriteFile(projectPath, malformed, 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Issues) == 0 || len(report.Errors) == 0 {
		t.Fatalf("report=%+v", report)
	}
	after, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, malformed) {
		t.Fatalf("malformed source was overwritten: %q", after)
	}
}

func TestStatusDoesNotCountMalformedBaseAsInSync(t *testing.T) {
	fixture := newEngineFixture(t)
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(fixture.project, "docs", "session-review", "project-overview.md")
	if err := os.WriteFile(projectPath, []byte("malformed-project-canary\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := engine.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Malformed == 0 || status.Blocked == 0 {
		t.Fatalf("malformed entity was not visible: %+v", status)
	}
	if status.InSync != 0 {
		t.Fatalf("malformed base counted as in sync: %+v", status)
	}
}

func TestRootScanIssueBlocksEveryEntity(t *testing.T) {
	issues := []syncdoc.ScanIssue{{Kind: syncdoc.IssueMalformed, RelativePath: "docs/session-review", Err: errors.New("tree scan failed")}}
	if !scanIssuesBlockEntity("decision-one", BaseRecord{}, false, issues, nil, scanIssuePaths(issues), nil, "Projects/Test/Session Review") {
		t.Fatal("a root-level scan failure did not block the entity")
	}
}

func TestEngineRenameRetiresTheOldPathOnBothSides(t *testing.T) {
	fixture := newEngineFixture(t)
	writeFixtureDecision(t, fixture, "decisions/old.md")
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	oldProject := filepath.Join(fixture.project, "docs", "session-review", "decisions", "old.md")
	newProject := filepath.Join(fixture.project, "docs", "session-review", "decisions", "new.md")
	if err := os.Rename(oldProject, newProject); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	oldVault := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "decisions", "old.md")
	newVault := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "decisions", "new.md")
	for _, removed := range []string{oldProject, oldVault} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("old path remains %q: %v", removed, err)
		}
	}
	for _, present := range []string{newProject, newVault} {
		if _, err := os.Stat(present); err != nil {
			t.Fatalf("new path missing %q: %v", present, err)
		}
	}
	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(report.Issues) != 0 || len(report.Conflicts) != 0 || len(report.Errors) != 0 || len(report.Operations) != 0 {
		t.Fatalf("post-rename report=%+v err=%v", report, err)
	}
}

func TestEngineDoesNotOverwriteConcurrentSemanticEditDuringWriteOrRecovery(t *testing.T) {
	fixture := newEngineFixture(t)
	writeFixtureDecision(t, fixture, "decisions/concurrent.md")
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(fixture.project, "docs", "session-review", "decisions", "concurrent.md")
	vaultPath := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "decisions", "concurrent.md")
	base := readDerivedTestFile(t, projectPath)
	vaultEdit := append(bytes.Clone(base), []byte("\n## Vault note\n\nAccepted candidate.\n")...)
	concurrentProject := append(bytes.Clone(base), []byte("\n## Concurrent Project note\n\nKeep this edit.\n")...)
	if err := os.WriteFile(vaultPath, vaultEdit, 0o644); err != nil {
		t.Fatal(err)
	}
	engine.writer.beforeWrite = func(side Side, parent *os.Root, leaf string) error {
		if side != SideProject || leaf != "concurrent.md" {
			return nil
		}
		file, err := parent.OpenFile(leaf, os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := file.Write(concurrentProject); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	}
	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(report.Errors) != 1 || report.Errors[0].EntityID != "decision-sync" || report.Errors[0].Code != "write_failed" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if got := readDerivedTestFile(t, projectPath); !bytes.Equal(got, concurrentProject) {
		t.Fatalf("concurrent Project edit was overwritten: %q", got)
	}
	engine.writer.beforeWrite = nil
	recovered, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(recovered.Errors) != 0 || len(recovered.Conflicts) != 0 {
		t.Fatalf("safe rescan did not merge the concurrent edits: report=%+v err=%v", recovered, err)
	}
	got := readDerivedTestFile(t, projectPath)
	if !bytes.Contains(got, []byte("Concurrent Project note")) || !bytes.Contains(got, []byte("Vault note")) || !bytes.Contains(got, []byte("revision: 2")) {
		t.Fatalf("safe rescan lost a concurrent semantic edit: %q", got)
	}
}

func TestEngineRecoveryRejectsSemanticEditAfterFirstSideWasWritten(t *testing.T) {
	fixture := newEngineFixture(t)
	writeFixtureDecision(t, fixture, "decisions/recovery-race.md")
	engine, err := NewEngine(fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err != nil {
		t.Fatal(err)
	}
	vaultPath := filepath.Join(fixture.vault, filepath.FromSlash(fixture.vaultReviewPath), "decisions", "recovery-race.md")
	base := readDerivedTestFile(t, vaultPath)
	vaultEdit := append(bytes.Clone(base), []byte("\n## Vault note\n\nOriginal accepted candidate.\n")...)
	if err := os.WriteFile(vaultPath, vaultEdit, 0o644); err != nil {
		t.Fatal(err)
	}
	engine.writer.beforeWrite = func(side Side, _ *os.Root, _ string) error {
		if side == SideVault {
			return errors.New("stop after Project write")
		}
		return nil
	}
	report, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI})
	if err != nil || len(report.Errors) != 1 || report.Errors[0].Code != "write_failed" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	concurrent := append(bytes.Clone(vaultEdit), []byte("\n## Later Vault note\n\nDo not overwrite.\n")...)
	if err := os.WriteFile(vaultPath, concurrent, 0o644); err != nil {
		t.Fatal(err)
	}
	engine.writer.beforeWrite = nil
	if _, err := engine.Reconcile(context.Background(), ReconcileRequest{Trigger: TriggerCLI}); err == nil || !strings.Contains(err.Error(), "transaction recovery failed") {
		t.Fatalf("recovery accepted a changed Vault preimage: %v", err)
	}
	if got := readDerivedTestFile(t, vaultPath); !bytes.Equal(got, concurrent) {
		t.Fatalf("recovery overwrote the later Vault edit: %q", got)
	}
}

type engineFixture struct {
	root, project, vault, data, vaultReviewPath, projectID string
}

func newEngineFixture(t *testing.T) engineFixture {
	t.Helper()
	root := t.TempDir()
	fixture := engineFixture{
		root: root, project: filepath.Join(root, "project"), vault: filepath.Join(root, "vault"),
		data: filepath.Join(root, "data"), vaultReviewPath: "Projects/SessionReviewer--11111111/Session Review",
		projectID: "project-1111111111111111",
	}
	for _, path := range []string{
		filepath.Join(fixture.project, "docs", "session-review"), fixture.vault,
		filepath.Join(fixture.data, "merge-bases"), filepath.Join(fixture.data, "queue"),
		filepath.Join(fixture.data, "transactions"), filepath.Join(fixture.data, "locks"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fixture.data, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	overview := "---\nid: project-overview\nentity_type: project_overview\nproject_id: " + fixture.projectID + "\nrevision: 1\nsync_status: synced\ncreated_at: 2026-08-24T00:00:00Z\nnote: base\n---\n\n# SessionReviewer\n"
	if err := os.WriteFile(filepath.Join(fixture.project, "docs", "session-review", "project-overview.md"), []byte(overview), 0o644); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture engineFixture) options() Options {
	return Options{
		ProjectRoot: fixture.project, VaultRoot: fixture.vault, VaultReviewPath: fixture.vaultReviewPath,
		DataRoot: fixture.data, ProjectID: fixture.projectID, GOOS: "darwin",
		VaultCaseMode: platform.CaseSensitive, Retry: DefaultRetryPolicy(), Now: func() time.Time {
			return time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
		},
	}
}

func writeFixtureDecision(t *testing.T, fixture engineFixture, relative string) {
	t.Helper()
	body := "---\nid: decision-sync\nentity_type: decision\nproject_id: " + fixture.projectID + "\nrevision: 1\nsync_status: synced\ntitle: Sync decision\nstatus: accepted\ntags: [sync]\nsupersedes: []\nsource_sessions: [session-1]\nevidence:\n  - evidence_id: evidence-1\n    session_id: session-1\n    jsonl_line: 1\n    source_hash: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n    summary: accepted\n---\n\n# Sync decision\n\n## Alternatives\n\n## Rejected paths\n"
	filename := filepath.Join(fixture.project, "docs", "session-review", filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func snapshotFixtureTree(t *testing.T, root string) []string {
	t.Helper()
	var result []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result = append(result, filepath.ToSlash(relative)+"|"+info.Mode().String()+"|"+info.ModTime().UTC().Format(time.RFC3339Nano))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
