package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/platform"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

type acceptanceManifest struct {
	Version int    `json:"version"`
	Fixture string `json:"fixture"`
	Steps   []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"steps"`
}

type acceptanceBackupManifest struct {
	Files []struct {
		RelativePath    string `json:"relative_path"`
		SHA256          string `json:"sha256"`
		Size            int64  `json:"size"`
		Mode            uint32 `json:"mode"`
		ObjectRelative  string `json:"object_relative"`
		ArchiveRelative string `json:"archive_relative"`
	} `json:"files"`
}

type acceptanceInventoryFile struct {
	body []byte
	hash string
	size int64
	mode uint32
}

type acceptanceInventory map[string]acceptanceInventoryFile

func TestReviewV2CoreAcceptanceReplay(t *testing.T) {
	manifestBody, err := os.ReadFile(filepath.Join("..", "..", "testdata", "acceptance", "review-v2-core-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var replay acceptanceManifest
	wantSteps := []string{"backup", "migration-dry-run", "migration-sync", "visible-boundary", "backup-manifest", "repeat-convergence", "different-units", "same-unit-conflict"}
	if err := json.Unmarshal(manifestBody, &replay); err != nil || replay.Version != 1 || replay.Fixture != "generated-temporary-legacy-project-vault-data" || len(replay.Steps) != len(wantSteps) {
		t.Fatalf("acceptance manifest=%+v err=%v", replay, err)
	}
	for index, step := range replay.Steps {
		if step.ID != index+1 || step.Name != wantSteps[index] {
			t.Fatalf("invalid acceptance step %+v", step)
		}
	}

	fixture := newCLILegacySyncFixture(t)
	backupRoot := t.TempDir()
	backupPaths := make(map[string]string, 3)
	for label, source := range map[string]string{"project": fixture.project, "vault": fixture.vault, "data": fixture.data} {
		destination := filepath.Join(backupRoot, label)
		backupPaths[label] = destination
		if err := copyCLITreeForTest(source, destination); err != nil {
			t.Fatal(err)
		}
		if got, want := snapshotCLITree(t, destination), snapshotCLITree(t, source); got != want {
			t.Fatalf("step 1 %s backup differs", label)
		}
	}
	preMigrationInventory := captureAcceptanceInventory(t, backupPaths["project"], "docs/session-review")

	projectBefore := acceptanceTreeSnapshot(t, fixture.project)
	vaultBefore := acceptanceTreeSnapshot(t, fixture.vault)
	dataBefore := acceptanceTreeSnapshot(t, fixture.data)
	out := runAcceptanceCLI(t, "sync", "--dry-run", "--project-id", fixture.projectID, "--data-dir", fixture.data)
	if !strings.Contains(out, "migration=required") || !strings.Contains(out, "machine=pending files=1") {
		t.Fatalf("step 2 dry-run output=%q", out)
	}
	if acceptanceTreeSnapshot(t, fixture.project) != projectBefore || acceptanceTreeSnapshot(t, fixture.vault) != vaultBefore || acceptanceTreeSnapshot(t, fixture.data) != dataBefore {
		t.Fatal("step 2 dry-run changed Project, Vault, or data")
	}

	out = runAcceptanceCLI(t, "sync", "--project-id", fixture.projectID, "--data-dir", fixture.data)
	if !strings.Contains(out, "migration=current") || !strings.Contains(out, "machine=current files=1") {
		t.Fatalf("step 3 sync output=%q", out)
	}
	projectReviewRoot := filepath.Join(fixture.project, "docs", "session-review")
	vaultReviewRoot := filepath.Join(fixture.vault, "Projects", "Legacy--269b8cab", "Session Review")
	for label, root := range map[string]string{"project": projectReviewRoot, "vault": vaultReviewRoot} {
		if visible := visibleAcceptanceEntries(t, root); strings.Join(visible, ",") != "项目历史.md,项目回顾.md" {
			t.Fatalf("step 4 %s visible entries=%v", label, visible)
		}
	}
	verifyAcceptanceBackupManifest(t, fixture.project, preMigrationInventory)
	if _, err := os.Stat(filepath.Join(vaultReviewRoot, ".session-reviewer", "backups")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("step 5 Vault contains migration backup: %v", err)
	}

	status := acceptanceStatus(t, fixture)
	if status.Migration != "current" || status.MachineState != syncengine.MachineCurrent || status.Conflicted != 0 || status.Malformed != 0 || status.Blocked != 0 || len(status.PendingOperations) != 0 || len(status.HiddenConflictIDs) != 0 {
		t.Fatalf("step 6 status=%+v", status)
	}
	converged := acceptanceTreeSnapshot(t, fixture.project) + acceptanceTreeSnapshot(t, fixture.vault) + acceptanceTreeSnapshot(t, fixture.data)
	out = runAcceptanceCLI(t, "sync", "--dry-run", "--project-id", fixture.projectID, "--data-dir", fixture.data)
	if !strings.Contains(out, "operations: 0") {
		t.Fatalf("step 6 repeat dry-run=%q", out)
	}
	out = runAcceptanceCLI(t, "sync", "--project-id", fixture.projectID, "--data-dir", fixture.data)
	if !strings.Contains(out, "operations: 0") {
		t.Fatalf("step 6 repeat sync=%q", out)
	}
	afterRepeat := acceptanceTreeSnapshot(t, fixture.project) + acceptanceTreeSnapshot(t, fixture.vault) + acceptanceTreeSnapshot(t, fixture.data)
	if afterRepeat != converged {
		t.Fatal("step 6 status/dry-run/repeat changed bytes or modification times")
	}
	assertAcceptancePeerBytes(t, projectReviewRoot, vaultReviewRoot)

	projectReview := filepath.Join(projectReviewRoot, "项目回顾.md")
	vaultReview := filepath.Join(vaultReviewRoot, "项目回顾.md")
	replaceAcceptanceText(t, projectReview, "Migrate a realistic accepted legacy ledger", "Project goal replay edit")
	replaceAcceptanceText(t, vaultReview, "preview migration", "Vault next-action replay edit")
	runAcceptanceCLI(t, "sync", "--project-id", fixture.projectID, "--data-dir", fixture.data)
	for _, path := range []string{projectReview, vaultReview} {
		body, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(body, []byte("Project goal replay edit")) || !bytes.Contains(body, []byte("Vault next-action replay edit")) {
			t.Fatalf("step 7 did not converge at %s: err=%v", filepath.Base(path), err)
		}
	}

	for _, action := range []string{"accept_project", "accept_obsidian", "manual_merge"} {
		t.Run(action, func(t *testing.T) { replayAcceptanceConflictAction(t, action) })
	}
	t.Log("sanitized acceptance manifest: 8/8 steps passed; 3/3 isolated conflict actions passed")
}

func TestAcceptanceBackupManifestRejectsSelfAttestation(t *testing.T) {
	fixture := newCLILegacySyncFixture(t)
	backupProject := filepath.Join(t.TempDir(), "project")
	if err := copyCLITreeForTest(fixture.project, backupProject); err != nil {
		t.Fatal(err)
	}
	inventory := captureAcceptanceInventory(t, backupProject, "docs/session-review")
	runAcceptanceCLI(t, "sync", "--project-id", fixture.projectID, "--data-dir", fixture.data)
	matches, err := filepath.Glob(filepath.Join(fixture.project, "docs", "session-review", ".session-reviewer", "backups", "*", "manifest.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("manifest matches=%v err=%v", matches, err)
	}
	original, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var baseline acceptanceBackupManifest
	if err := json.Unmarshal(original, &baseline); err != nil || len(baseline.Files) < 2 {
		t.Fatalf("baseline=%+v err=%v", baseline, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*acceptanceBackupManifest)
	}{
		{name: "omitted source", mutate: func(value *acceptanceBackupManifest) { value.Files = value.Files[1:] }},
		{name: "duplicate source", mutate: func(value *acceptanceBackupManifest) { value.Files[1].RelativePath = value.Files[0].RelativePath }},
		{name: "unsafe backup path", mutate: func(value *acceptanceBackupManifest) { value.Files[0].ObjectRelative = "../escape" }},
		{name: "snapshot hash mismatch", mutate: func(value *acceptanceBackupManifest) { value.Files[0].SHA256 = "sha256:" + strings.Repeat("0", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var candidate acceptanceBackupManifest
			if err := json.Unmarshal(original, &candidate); err != nil {
				t.Fatal(err)
			}
			test.mutate(&candidate)
			body, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(matches[0], body, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := validateAcceptanceBackupManifest(fixture.project, inventory); err == nil {
				t.Fatal("self-attesting migration manifest was accepted")
			}
		})
	}
}

func replayAcceptanceConflictAction(t *testing.T, action string) {
	t.Helper()
	fixture := newCLILegacySyncFixture(t)
	runAcceptanceCLI(t, "sync", "--project-id", fixture.projectID, "--data-dir", fixture.data)
	backupRoot := t.TempDir()
	backupPaths := make(map[string]string, 3)
	before := make(map[string]string, 3)
	for label, source := range map[string]string{"project": fixture.project, "vault": fixture.vault, "data": fixture.data} {
		before[label] = snapshotCLITree(t, source)
		backupPaths[label] = filepath.Join(backupRoot, label)
		if err := copyCLITreeForTest(source, backupPaths[label]); err != nil {
			t.Fatal(err)
		}
		if got := snapshotCLITree(t, backupPaths[label]); got != before[label] {
			t.Fatalf("step 8 %s backup differs before resolution", label)
		}
	}
	projectReviewRoot := filepath.Join(fixture.project, "docs", "session-review")
	vaultReviewRoot := filepath.Join(fixture.vault, "Projects", "Legacy--269b8cab", "Session Review")
	projectReview := filepath.Join(projectReviewRoot, "项目回顾.md")
	vaultReview := filepath.Join(vaultReviewRoot, "项目回顾.md")
	replaceAcceptanceText(t, projectReview, "Migrate a realistic accepted legacy ledger", "Project conflict replay")
	replaceAcceptanceText(t, vaultReview, "Migrate a realistic accepted legacy ledger", "Vault conflict replay")
	out := runAcceptanceCLI(t, "sync", "--project-id", fixture.projectID, "--data-dir", fixture.data)
	if !strings.Contains(out, "conflicts: 1") {
		t.Fatalf("step 8 conflict output=%q", out)
	}
	status := acceptanceStatus(t, fixture)
	if len(status.HiddenConflictIDs) != 1 {
		t.Fatalf("step 8 conflict status=%+v", status)
	}
	args := []string{"sync", "resolve", "--conflict", status.HiddenConflictIDs[0], "--action", action, "--project-id", fixture.projectID, "--data-dir", fixture.data}
	want := "Project conflict replay"
	if action == "accept_obsidian" {
		want = "Vault conflict replay"
	}
	if action == "manual_merge" {
		manualBody, err := os.ReadFile(projectReview)
		if err != nil {
			t.Fatal(err)
		}
		manualBody = bytes.Replace(manualBody, []byte("Project conflict replay"), []byte("Manual conflict replay"), 1)
		manualPath := filepath.Join(t.TempDir(), "manual.md")
		if err := os.WriteFile(manualPath, manualBody, 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--file", manualPath)
		want = "Manual conflict replay"
	}
	runAcceptanceCLI(t, args...)
	projectBody, projectErr := os.ReadFile(projectReview)
	vaultBody, vaultErr := os.ReadFile(vaultReview)
	if projectErr != nil || vaultErr != nil || !bytes.Equal(projectBody, vaultBody) || !bytes.Contains(projectBody, []byte(want)) {
		t.Fatalf("step 8 %s did not converge: projectErr=%v vaultErr=%v", action, projectErr, vaultErr)
	}
	status = acceptanceStatus(t, fixture)
	if status.Conflicted != 0 || len(status.HiddenConflictIDs) != 0 || len(status.PendingOperations) != 0 {
		t.Fatalf("step 8 %s final status=%+v", action, status)
	}
	restoreAcceptanceActionBackup(t, fixture, backupPaths, before)
}

func runAcceptanceCLI(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := Run(args, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("acceptance CLI args=%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func acceptanceStatus(t *testing.T, fixture cliSyncFixture) syncengine.Status {
	t.Helper()
	out := runAcceptanceCLI(t, "sync", "status", "--json", "--project-id", fixture.projectID, "--data-dir", fixture.data)
	var status syncengine.Status
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func visibleAcceptanceEntries(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var visible []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			visible = append(visible, entry.Name())
		}
	}
	sort.Strings(visible)
	return visible
}

func verifyAcceptanceBackupManifest(t *testing.T, project string, inventory acceptanceInventory) {
	t.Helper()
	if err := validateAcceptanceBackupManifest(project, inventory); err != nil {
		t.Fatal(err)
	}
}

func validateAcceptanceBackupManifest(project string, inventory acceptanceInventory) error {
	matches, err := filepath.Glob(filepath.Join(project, "docs", "session-review", ".session-reviewer", "backups", "*", "manifest.json"))
	if err != nil || len(matches) != 1 {
		return fmt.Errorf("step 5 backup manifests=%d err=%v", len(matches), err)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		return err
	}
	var manifest acceptanceBackupManifest
	if err := json.Unmarshal(body, &manifest); err != nil || len(manifest.Files) != len(inventory) {
		return fmt.Errorf("step 5 manifest file count=%d want=%d err=%v", len(manifest.Files), len(inventory), err)
	}
	root := filepath.Dir(matches[0])
	seenSource := make(map[string]struct{}, len(manifest.Files))
	seenBackup := make(map[string]struct{}, len(manifest.Files)*2)
	for _, file := range manifest.Files {
		if !safeAcceptanceRelative(file.RelativePath) || !strings.HasPrefix(file.RelativePath, "docs/session-review/") {
			return fmt.Errorf("step 5 unsafe source path %q", file.RelativePath)
		}
		sourceKey, _ := platform.PathKey("windows", platform.CaseInsensitive, file.RelativePath)
		if _, duplicate := seenSource[sourceKey]; duplicate {
			return fmt.Errorf("step 5 duplicate source path %q", file.RelativePath)
		}
		seenSource[sourceKey] = struct{}{}
		expected, found := inventory[file.RelativePath]
		if !found {
			return fmt.Errorf("step 5 manifest source is absent from pre-migration snapshot")
		}
		want := "sha256:" + expected.hash
		if file.SHA256 != want || file.Size != expected.size || file.Mode != expected.mode {
			return fmt.Errorf("step 5 manifest metadata differs from pre-migration snapshot for %q", file.RelativePath)
		}
		for _, relative := range []string{file.ObjectRelative, file.ArchiveRelative} {
			if !safeAcceptanceRelative(relative) {
				return fmt.Errorf("step 5 unsafe backup path %q", relative)
			}
			backupKey, _ := platform.PathKey("windows", platform.CaseInsensitive, relative)
			if _, duplicate := seenBackup[backupKey]; duplicate {
				return fmt.Errorf("step 5 duplicate backup path %q", relative)
			}
			seenBackup[backupKey] = struct{}{}
			candidate, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
			if err != nil {
				return err
			}
			digest := sha256.Sum256(candidate)
			if !bytes.Equal(candidate, expected.body) || hex.EncodeToString(digest[:]) != expected.hash {
				return fmt.Errorf("step 5 backup bytes differ from pre-migration snapshot for %q", file.RelativePath)
			}
		}
	}
	if len(seenSource) != len(inventory) {
		return errors.New("step 5 manifest omitted a pre-migration source")
	}
	return nil
}

func captureAcceptanceInventory(t *testing.T, projectRoot, relativeRoot string) acceptanceInventory {
	t.Helper()
	inventory := acceptanceInventory{}
	portablePaths := map[string]string{}
	absoluteRoot := filepath.Join(projectRoot, filepath.FromSlash(relativeRoot))
	err := filepath.Walk(absoluteRoot, func(current string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			relative, err := filepath.Rel(projectRoot, current)
			if err != nil {
				return err
			}
			if filepath.ToSlash(relative) == "docs/session-review/.session-reviewer" {
				return filepath.SkipDir
			}
			return nil
		}
		if atomicfile.IsRootDirectoryLockName(info.Name()) {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("pre-migration inventory contains a non-regular file")
		}
		relative, err := filepath.Rel(projectRoot, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !safeAcceptanceRelative(relative) || !strings.HasPrefix(relative, "docs/session-review/") {
			return fmt.Errorf("unsafe pre-migration inventory path")
		}
		if _, duplicate := inventory[relative]; duplicate {
			return fmt.Errorf("duplicate pre-migration inventory path")
		}
		portableKey, _ := platform.PathKey("windows", platform.CaseInsensitive, relative)
		if previous, duplicate := portablePaths[portableKey]; duplicate && previous != relative {
			return fmt.Errorf("pre-migration inventory contains a portable path collision")
		}
		portablePaths[portableKey] = relative
		body, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(body)
		inventory[relative] = acceptanceInventoryFile{
			body: body, hash: hex.EncodeToString(digest[:]), size: info.Size(), mode: uint32(info.Mode().Perm()),
		}
		return nil
	})
	if err != nil || len(inventory) == 0 {
		t.Fatalf("capture pre-migration inventory count=%d err=%v", len(inventory), err)
	}
	return inventory
}

func safeAcceptanceRelative(relative string) bool {
	_, portableErr := platform.PathKey("windows", platform.CaseInsensitive, relative)
	return portableErr == nil && relative != "" && relative != "." && !strings.Contains(relative, `\`) &&
		!strings.HasPrefix(relative, "/") && path.Clean(relative) == relative &&
		!strings.HasPrefix(relative, "../")
}

func restoreAcceptanceActionBackup(t *testing.T, fixture cliSyncFixture, backups, before map[string]string) {
	t.Helper()
	current := map[string]string{"project": fixture.project, "vault": fixture.vault, "data": fixture.data}
	retained := t.TempDir()
	for _, label := range []string{"project", "vault", "data"} {
		if got := snapshotCLITree(t, backups[label]); got != before[label] {
			t.Fatalf("step 8 %s backup changed before restore", label)
		}
		if err := os.Rename(current[label], filepath.Join(retained, label+"-resolved")); err != nil {
			t.Fatal(err)
		}
		if err := copyCLITreeForTest(backups[label], current[label]); err != nil {
			t.Fatal(err)
		}
		if got := snapshotCLITree(t, current[label]); got != before[label] {
			t.Fatalf("step 8 %s restore differs byte-for-byte from backup", label)
		}
		if got := snapshotCLITree(t, backups[label]); got != before[label] {
			t.Fatalf("step 8 %s backup changed after restore", label)
		}
	}
	status := acceptanceStatus(t, fixture)
	if status.Conflicted != 0 || len(status.PendingOperations) != 0 || len(status.HiddenConflictIDs) != 0 {
		t.Fatalf("step 8 restored backup status=%+v", status)
	}
}

func acceptanceTreeSnapshot(t *testing.T, root string) string {
	t.Helper()
	var snapshot strings.Builder
	err := filepath.Walk(root, func(current string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		fmt.Fprintf(&snapshot, "%s|%s|%d", filepath.ToSlash(relative), info.Mode(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(current)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(body)
			fmt.Fprintf(&snapshot, "|%x", digest)
		}
		snapshot.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.String()
}

func assertAcceptancePeerBytes(t *testing.T, projectRoot, vaultRoot string) {
	t.Helper()
	for _, relative := range []string{"项目回顾.md", "项目历史.md", ".session-reviewer/ledger.json"} {
		projectBody, projectErr := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(relative)))
		vaultBody, vaultErr := os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(relative)))
		if projectErr != nil || vaultErr != nil || !bytes.Equal(projectBody, vaultBody) {
			t.Fatalf("step 6 peer bytes differ for %s: projectErr=%v vaultErr=%v", relative, projectErr, vaultErr)
		}
	}
}

func replaceAcceptanceText(t *testing.T, filename, old, replacement string) {
	t.Helper()
	body, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	next := bytes.Replace(body, []byte(old), []byte(replacement), 1)
	if bytes.Equal(next, body) {
		t.Fatalf("acceptance fixture text %q not found in %s", old, filepath.Base(filename))
	}
	if err := os.WriteFile(filename, next, 0o644); err != nil {
		t.Fatal(err)
	}
}
