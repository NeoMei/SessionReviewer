package reviewv2

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadAcceptedV2ValidatesHashesAccountingAndSnapshot(t *testing.T) {
	root, original := writeV2Fixture(t)
	accepted, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Legacy.ProjectID != original.Review.ProjectID || accepted.Legacy.CurrentState.Goal != original.Review.Goal {
		t.Fatalf("legacy=%+v", accepted.Legacy)
	}
	wantPaths := []string{HistoryRelativePath, MachineLedgerRelativePath, ReviewRelativePath}
	gotPaths := make([]string, len(accepted.Snapshot.Files))
	for index, file := range accepted.Snapshot.Files {
		gotPaths[index] = file.RelativePath
		if !strings.HasPrefix(file.SHA256, "sha256:") || file.Size < 1 {
			t.Fatalf("snapshot file=%+v", file)
		}
	}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("snapshot paths=%v", gotPaths)
	}

	machinePath := filepath.Join(root, filepath.FromSlash(MachineLedgerRelativePath))
	machineBody, err := os.ReadFile(machinePath)
	if err != nil {
		t.Fatal(err)
	}
	machine, err := ParseMachineLedger(machineBody)
	if err != nil {
		t.Fatal(err)
	}
	machine.Accounting.TotalTokens++
	tampered, err := RenderMachineLedger(machine)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(machinePath, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "accounting") {
		t.Fatalf("accounting mismatch err=%v", err)
	}

	if err := os.WriteFile(machinePath, machineBody, 0o644); err != nil {
		t.Fatal(err)
	}
	reviewPath := filepath.Join(root, filepath.FromSlash(ReviewRelativePath))
	reviewBody, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reviewPath, bytes.Replace(reviewBody, []byte(original.Review.Goal), []byte("changed without hash update"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("hash mismatch err=%v", err)
	}
}

func TestDetectVersionAndLegacyReadOnlyMigrationGate(t *testing.T) {
	empty := t.TempDir()
	if version, err := DetectVersion(empty); err != nil || version != VersionEmpty {
		t.Fatalf("empty version=%q err=%v", version, err)
	}

	legacyRoot := t.TempDir()
	writeLegacyOverview(t, legacyRoot)
	if version, err := DetectVersion(legacyRoot); err != nil || version != VersionLegacy {
		t.Fatalf("legacy version=%q err=%v", version, err)
	}
	if _, err := Load(legacyRoot); err == nil {
		t.Fatal("legacy ledger accepted by v2 write loader")
	} else {
		var migration *ErrMigrationRequired
		if !errors.As(err, &migration) || migration.ProjectRoot != legacyRoot {
			t.Fatalf("migration error=%T %v", err, err)
		}
	}
	readOnly, err := LoadAnyReadOnly(legacyRoot)
	if err != nil || readOnly.Legacy.ProjectID != "project-0123456789abcdef" {
		t.Fatalf("read-only legacy=%+v err=%v", readOnly.Legacy, err)
	}

	v2Root, _ := writeV2Fixture(t)
	if version, err := DetectVersion(v2Root); err != nil || version != VersionV2 {
		t.Fatalf("v2 version=%q err=%v", version, err)
	}
	writeLegacyOverview(t, v2Root)
	if version, err := DetectVersion(v2Root); err != nil || version != VersionMixed {
		t.Fatalf("mixed version=%q err=%v", version, err)
	}
	if _, err := Load(v2Root); err == nil || errors.As(err, new(*ErrMigrationRequired)) {
		t.Fatalf("mixed state err=%v", err)
	}

	partialLegacy := t.TempDir()
	if err := os.MkdirAll(filepath.Join(partialLegacy, "docs", "session-review", "decisions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if version, err := DetectVersion(partialLegacy); err != nil || version != VersionLegacy {
		t.Fatalf("partial legacy version=%q err=%v", version, err)
	}
}

func TestLoadExpectedAndSnapshotUsagePinProjectRootIdentity(t *testing.T) {
	root, _ := writeV2Fixture(t)
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := LoadExpected(root, rootInfo)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := SnapshotUsageExpected(root, rootInfo)
	if err != nil || !reflect.DeepEqual(usage, accepted.Snapshot) {
		t.Fatalf("usage=%+v accepted=%+v err=%v", usage, accepted.Snapshot, err)
	}
	otherInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExpected(root, otherInfo); err == nil || !strings.Contains(err.Error(), "expected project root") {
		t.Fatalf("wrong root load err=%v", err)
	}
	if _, err := SnapshotUsageExpected(root, otherInfo); err == nil || !strings.Contains(err.Error(), "expected project root") {
		t.Fatalf("wrong root snapshot err=%v", err)
	}
}

func TestLoadRejectsCrossFileMutationAfterInitialSnapshots(t *testing.T) {
	root, _ := writeV2Fixture(t)
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	reviewPath := filepath.Join(root, filepath.FromSlash(ReviewRelativePath))
	_, err = loadAcceptedWithHooks(root, rootInfo, false, loadHooks{afterFilesRead: func() error {
		body, readErr := os.ReadFile(reviewPath)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(reviewPath, append(body, []byte("\nconcurrent edit\n")...), 0o644)
	}})
	if err == nil || !strings.Contains(err.Error(), "changed while loading") {
		t.Fatalf("cross-file mutation err=%v", err)
	}
}

func TestLegacyReadOnlyLoadRejectsMutationBetweenStateAndSnapshot(t *testing.T) {
	root := t.TempDir()
	writeLegacyOverview(t, root)
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	overviewPath := filepath.Join(root, "docs", "session-review", "project-overview.md")
	_, err = loadAcceptedWithHooks(root, rootInfo, true, loadHooks{afterLegacyLoad: func() error {
		body, readErr := os.ReadFile(overviewPath)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(overviewPath, append(body, []byte("\nconcurrent legacy edit\n")...), 0o644)
	}})
	if err == nil || !strings.Contains(err.Error(), "changed while loading") {
		t.Fatalf("legacy cross-snapshot mutation err=%v", err)
	}
}

func writeV2Fixture(t *testing.T) (string, State) {
	t.Helper()
	state, err := ProjectLegacy(legacyFixtureState(t))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	plan, err := Render(root, state)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range plan.Files {
		path := filepath.Join(root, filepath.FromSlash(file.RelativePath))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.Data, file.Perm); err != nil {
			t.Fatal(err)
		}
	}
	return root, state
}

func writeLegacyOverview(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, "docs", "session-review", "project-overview.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("---\nproject_id: project-0123456789abcdef\ncreated_at: 2026-08-25T00:00:00Z\n---\n# Legacy\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}
