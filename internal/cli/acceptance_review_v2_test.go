package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

type acceptanceManifest struct {
	Version int `json:"version"`
	Steps   []struct {
		ID       int    `json:"id"`
		Name     string `json:"name"`
		Expected string `json:"expected"`
	} `json:"steps"`
}

type acceptanceBackupManifest struct {
	Files []struct {
		SHA256          string `json:"sha256"`
		ObjectRelative  string `json:"object_relative"`
		ArchiveRelative string `json:"archive_relative"`
	} `json:"files"`
}

func TestReviewV2CoreAcceptanceReplay(t *testing.T) {
	manifestBody, err := os.ReadFile(filepath.Join("..", "..", "testdata", "acceptance", "review-v2-core-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var replay acceptanceManifest
	if err := json.Unmarshal(manifestBody, &replay); err != nil || replay.Version != 1 || len(replay.Steps) != 8 {
		t.Fatalf("acceptance manifest=%+v err=%v", replay, err)
	}
	for index, step := range replay.Steps {
		if step.ID != index+1 || step.Name == "" || step.Expected == "" {
			t.Fatalf("invalid acceptance step %+v", step)
		}
	}

	fixture := newCLILegacySyncFixture(t)
	backupRoot := t.TempDir()
	for label, source := range map[string]string{"project": fixture.project, "vault": fixture.vault, "data": fixture.data} {
		destination := filepath.Join(backupRoot, label)
		if err := copyCLITreeForTest(source, destination); err != nil {
			t.Fatal(err)
		}
		if got, want := snapshotCLITree(t, destination), snapshotCLITree(t, source); got != want {
			t.Fatalf("step 1 %s backup differs", label)
		}
	}

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
	verifyAcceptanceBackupManifest(t, fixture.project)
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

func replayAcceptanceConflictAction(t *testing.T, action string) {
	t.Helper()
	fixture := newCLILegacySyncFixture(t)
	runAcceptanceCLI(t, "sync", "--project-id", fixture.projectID, "--data-dir", fixture.data)
	backupRoot := t.TempDir()
	for label, source := range map[string]string{"project": fixture.project, "vault": fixture.vault, "data": fixture.data} {
		if err := copyCLITreeForTest(source, filepath.Join(backupRoot, label)); err != nil {
			t.Fatal(err)
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

func verifyAcceptanceBackupManifest(t *testing.T, project string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(project, "docs", "session-review", ".session-reviewer", "backups", "*", "manifest.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("step 5 backup manifests=%d err=%v", len(matches), err)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var manifest acceptanceBackupManifest
	if err := json.Unmarshal(body, &manifest); err != nil || len(manifest.Files) == 0 {
		t.Fatalf("step 5 manifest=%+v err=%v", manifest, err)
	}
	root := filepath.Dir(matches[0])
	for _, file := range manifest.Files {
		want := strings.TrimPrefix(file.SHA256, "sha256:")
		for _, relative := range []string{file.ObjectRelative, file.ArchiveRelative} {
			candidate, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(candidate)
			if hex.EncodeToString(digest[:]) != want {
				t.Fatalf("step 5 digest mismatch for logical backup entry %s", filepath.ToSlash(relative))
			}
		}
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
