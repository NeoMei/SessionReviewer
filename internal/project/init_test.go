package project

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
)

func TestInitializeCreatesStableProjectAndMapping(t *testing.T) {
	root := filepath.Join(t.TempDir(), "项目")
	vault := filepath.Join(t.TempDir(), "知识库")
	data := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data,
		Now:    func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) },
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 16)),
	}
	first, err := Initialize(opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Initialize(opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProjectID != second.ProjectID {
		t.Fatalf("ids differ: %q %q", first.ProjectID, second.ProjectID)
	}
	b, err := os.ReadFile(filepath.Join(root, "docs", "session-review", "project-overview.md"))
	if err != nil || !strings.Contains(string(b), "project-2a2a2a2a2a2a2a2a") {
		t.Fatalf("overview=%q err=%v", b, err)
	}
	cfg, err := config.Load(first.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("projects=%+v, want one mapping", cfg.Projects)
	}
}

func TestInitializeRejectsNestedVaultAndProject(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "must not contain") {
		t.Fatalf("err=%v", err)
	}
}

func TestInitializeRejectsSymlinkedRoot(t *testing.T) {
	realRoot := t.TempDir()
	linkedRoot := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := Initialize(InitOptions{ProjectRoot: linkedRoot, VaultRoot: t.TempDir(), DataDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "symlink or reparse point") {
		t.Fatalf("err=%v", err)
	}
}

func TestInitializeRejectsRedirectedDocsWithoutWritingOutside(t *testing.T) {
	root := t.TempDir()
	vault := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "docs")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "redirected") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "session-review", "project-overview.md")); !os.IsNotExist(statErr) {
		t.Fatalf("outside overview exists or could not be inspected: %v", statErr)
	}
}

func TestInitializeRejectsEqualProjectAndVaultRoots(t *testing.T) {
	root := t.TempDir()
	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: root, DataDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "must not contain") {
		t.Fatalf("err=%v", err)
	}
}

func TestInitializeRejectsPhysicalContainmentThroughAliasedAncestor(t *testing.T) {
	realBase := t.TempDir()
	aliasBase := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realBase, aliasBase); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	root := filepath.Join(aliasBase, "project")
	vault := filepath.Join(realBase, "project", "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "must not contain") {
		t.Fatalf("err=%v", err)
	}
}

func TestInitializeCanonicalRootAliasReusesMapping(t *testing.T) {
	realBase := t.TempDir()
	aliasBase := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realBase, aliasBase); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	realRoot := filepath.Join(realBase, "project")
	aliasRoot := filepath.Join(aliasBase, "project")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	vault := t.TempDir()
	data := t.TempDir()
	first, err := Initialize(InitOptions{ProjectRoot: realRoot, VaultRoot: vault, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Initialize(InitOptions{ProjectRoot: aliasRoot, VaultRoot: vault, DataDir: data, Random: errorReader{}})
	if err != nil {
		t.Fatal(err)
	}
	if first.ProjectID != second.ProjectID {
		t.Fatalf("ids differ: %q %q", first.ProjectID, second.ProjectID)
	}
	cfg, err := config.Load(filepath.Join(data, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("projects=%+v, want one canonical mapping", cfg.Projects)
	}
}

func TestInitializeRejectsDifferentVaultForExistingProject(t *testing.T) {
	root := t.TempDir()
	vault := t.TempDir()
	data := t.TempDir()
	if _, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data}); err != nil {
		t.Fatal(err)
	}

	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: t.TempDir(), DataDir: data})
	if err == nil || !strings.Contains(err.Error(), "different vault") {
		t.Fatalf("err=%v", err)
	}
}

func TestInitializeConcurrentSameProjectUsesOneIdentity(t *testing.T) {
	root := t.TempDir()
	vault := t.TempDir()
	data := t.TempDir()
	const workers = 8
	start := make(chan struct{})
	type outcome struct {
		result InitResult
		err    error
	}
	outcomes := make(chan outcome, workers)
	for worker := 0; worker < workers; worker++ {
		worker := worker
		go func() {
			<-start
			result, err := Initialize(InitOptions{
				ProjectRoot: root,
				VaultRoot:   vault,
				DataDir:     data,
				Random:      slowByteReader{value: byte(worker + 1)},
			})
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	var projectID string
	for range workers {
		outcome := <-outcomes
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if projectID == "" {
			projectID = outcome.result.ProjectID
		}
		if outcome.result.ProjectID != projectID {
			t.Fatalf("split project IDs: first=%q got=%q", projectID, outcome.result.ProjectID)
		}
	}
	cfg, err := config.Load(filepath.Join(data, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].ID != projectID {
		t.Fatalf("cfg=%+v projectID=%q", cfg, projectID)
	}
}

func TestInitializeConcurrentDifferentProjectsKeepsEveryMapping(t *testing.T) {
	data := t.TempDir()
	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		root := t.TempDir()
		vault := t.TempDir()
		worker := worker
		go func() {
			<-start
			_, err := Initialize(InitOptions{
				ProjectRoot: root,
				VaultRoot:   vault,
				DataDir:     data,
				Random:      slowByteReader{value: byte(worker + 1)},
			})
			errs <- err
		}()
	}
	close(start)
	for range workers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(filepath.Join(data, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != workers {
		t.Fatalf("projects=%+v, want %d mappings", cfg.Projects, workers)
	}
}

func TestInitializeRecoversOverviewWithoutMapping(t *testing.T) {
	root := t.TempDir()
	vault := t.TempDir()
	data := t.TempDir()
	wantID := "project-1111111111111111"
	writeTestOverview(t, root, wantID)

	result, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, Random: errorReader{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProjectID != wantID {
		t.Fatalf("projectID=%q want=%q", result.ProjectID, wantID)
	}
	cfg, err := config.Load(result.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].ID != wantID {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestInitializeRecoversMappingWithoutOverview(t *testing.T) {
	root := t.TempDir()
	vault := t.TempDir()
	data := t.TempDir()
	wantID := "project-2222222222222222"
	configPath := filepath.Join(data, "config.toml")
	if err := config.Save(configPath, config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: wantID, Root: root, VaultRoot: vault,
	}}}); err != nil {
		t.Fatal(err)
	}

	result, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, Random: errorReader{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProjectID != wantID {
		t.Fatalf("projectID=%q want=%q", result.ProjectID, wantID)
	}
	b, err := os.ReadFile(filepath.Join(root, "docs", "session-review", "project-overview.md"))
	if err != nil || !strings.Contains(string(b), "project_id: "+wantID) {
		t.Fatalf("overview=%q err=%v", b, err)
	}
}

func TestAcquireInitLockDoesNotDeleteUnknownLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml.lock")
	if err := os.WriteFile(path, []byte("unknown-owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := acquireInitLock(path, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "refusing to remove") {
		t.Fatalf("err=%v", err)
	}
	b, readErr := os.ReadFile(path)
	if readErr != nil || string(b) != "unknown-owner" {
		t.Fatalf("lock=%q err=%v", b, readErr)
	}
}

func TestInsideUsesTargetWindowsPathSemantics(t *testing.T) {
	if !inside("windows", `C:\Projects\Repo`, `c:/projects/repo/docs`) {
		t.Fatal("expected Windows child path to be inside parent")
	}
	if inside("windows", `C:\Projects\Repo`, `c:/projects/repository`) {
		t.Fatal("path prefix without a component boundary must not count as containment")
	}
	if inside("windows", `C:\Projects\Repo`, `D:\Projects\Repo\docs`) {
		t.Fatal("different Windows drives must not count as containment")
	}
}

type slowByteReader struct {
	value byte
}

func (r slowByteReader) Read(buffer []byte) (int, error) {
	time.Sleep(40 * time.Millisecond)
	for index := range buffer {
		buffer[index] = r.value
	}
	return len(buffer), nil
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("random reader must not be used during recovery")
}

func writeTestOverview(t *testing.T, root, projectID string) {
	t.Helper()
	ledger := filepath.Join(root, "docs", "session-review")
	if err := os.MkdirAll(ledger, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nproject_id: " + projectID + "\ncreated_at: 2026-08-22T12:00:00Z\n---\n\n# project\n"
	if err := os.WriteFile(filepath.Join(ledger, "project-overview.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
