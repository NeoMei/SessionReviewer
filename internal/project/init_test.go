package project

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
)

func TestEnsureRootDirectoryPropagatesDurableCreatorFailure(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	durabilityErr := errors.New("injected durable directory failure")
	err = ensureRootDirectoryWith(root, "docs", 0o755, func(*os.Root, string, fs.FileMode) error {
		return durabilityErr
	})
	if !errors.Is(err, durabilityErr) {
		t.Fatalf("error=%v want=%v", err, durabilityErr)
	}
}

func TestOpenOrCreateDirectoryPropagatesDurableCreatorFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machine", "projects")
	durabilityErr := errors.New("injected durable machine directory failure")
	directory, err := openOrCreateDirectoryWith(path, 0o700, func(*os.Root, string, fs.FileMode) error {
		return durabilityErr
	})
	if directory != nil {
		_ = directory.Close()
		t.Fatal("returned directory after durability failure")
	}
	if !errors.Is(err, durabilityErr) {
		t.Fatalf("error=%v want=%v", err, durabilityErr)
	}
}

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

func TestPreviewInitializationDoesNotCreateDataOrLedger(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	vault := filepath.Join(base, "vault")
	data := filepath.Join(base, "machine")
	for _, path := range []string{root, vault} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	preview, err := PreviewInitialization(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Action != "create" || preview.LedgerRoot != filepath.Join(root, "docs", "session-review") {
		t.Fatalf("preview=%+v", preview)
	}
	for _, path := range []string{data, filepath.Join(root, "docs")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists: %v", path, err)
		}
	}
}

func TestPreviewInitializationClassifiesActionableFailures(t *testing.T) {
	t.Run("invalid root", func(t *testing.T) {
		_, err := PreviewInitialization(InitOptions{
			ProjectRoot: filepath.Join(t.TempDir(), "missing"),
			VaultRoot:   t.TempDir(),
			DataDir:     t.TempDir(),
		})
		if !errors.Is(err, ErrInvalidInitializationRoot) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("nested roots", func(t *testing.T) {
		root := t.TempDir()
		vault := filepath.Join(root, "vault")
		if err := os.Mkdir(vault, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := PreviewInitialization(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: t.TempDir()})
		if !errors.Is(err, ErrNestedInitializationRoots) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("corrupt config", func(t *testing.T) {
		root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(data, "config.toml"), []byte("not = [valid"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := PreviewInitialization(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data})
		if !errors.Is(err, ErrCorruptInitializationConfig) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestInitializeRevalidatesRootsAfterAcquiringTransactionLock(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	moved := filepath.Join(base, "moved")
	outside := filepath.Join(base, "outside")
	for _, path := range []string{root, outside} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := PreviewInitialization(InitOptions{ProjectRoot: root, VaultRoot: outside, DataDir: filepath.Join(base, "data")}); err != nil {
		t.Fatal(err)
	}

	_, err := Initialize(InitOptions{
		ProjectRoot: root,
		VaultRoot:   outside,
		DataDir:     filepath.Join(base, "data"),
		afterLock: func() error {
			if err := os.Rename(root, moved); err != nil {
				return err
			}
			return os.Symlink(outside, root)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "symlink or reparse point") {
		t.Fatalf("err=%v", err)
	}
	if !errors.Is(err, ErrInitializationStateChanged) {
		t.Fatalf("err=%v does not classify the preview/write race", err)
	}
	for _, path := range []string{filepath.Join(moved, "docs"), filepath.Join(outside, "docs")} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("write escaped revalidation at %s: %v", path, statErr)
		}
	}
}

func TestInitializeClassifiesConflictingIdentity(t *testing.T) {
	firstRoot, root, firstVault, vault, data := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	const projectID = "project-1111111111111111"
	writeTestOverview(t, root, projectID)
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: projectID, Root: firstRoot, VaultRoot: firstVault,
	}}}); err != nil {
		t.Fatal(err)
	}
	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data})
	if !errors.Is(err, ErrConflictingInitializationIdentity) {
		t.Fatalf("err=%v", err)
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
	if err == nil || (!strings.Contains(err.Error(), "must not contain") && !strings.Contains(err.Error(), "symlink or reparse point")) {
		t.Fatalf("err=%v", err)
	}
}

func TestInitializeRejectsCanonicalRootAlias(t *testing.T) {
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
	_, err := Initialize(InitOptions{ProjectRoot: realRoot, VaultRoot: vault, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Initialize(InitOptions{ProjectRoot: aliasRoot, VaultRoot: vault, DataDir: data, Random: errorReader{}})
	if err == nil || !strings.Contains(err.Error(), "symlink or reparse point") {
		t.Fatalf("err=%v", err)
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

func TestInitializeRejectsOverviewIDClaimedByAnotherRoot(t *testing.T) {
	first, second, firstVault, secondVault, data := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	wantID := "project-1111111111111111"
	writeTestOverview(t, second, wantID)
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: wantID, Root: first, VaultRoot: firstVault,
	}}}); err != nil {
		t.Fatal(err)
	}
	_, err := Initialize(InitOptions{ProjectRoot: second, VaultRoot: secondVault, DataDir: data})
	if err == nil || !strings.Contains(err.Error(), "already belongs to another project root") {
		t.Fatalf("err=%v", err)
	}
	if got, _ := config.Load(filepath.Join(data, "config.toml")); len(got.Projects) != 1 {
		t.Fatalf("projects=%+v", got.Projects)
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

func TestInitializeRecoversBackupOnlyConfigWithoutLosingMappings(t *testing.T) {
	firstRoot, secondRoot, vault, data := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	configPath := filepath.Join(data, "config.toml")
	want := config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "project-1111111111111111", Root: firstRoot, VaultRoot: vault}}}
	if err := config.Save(configPath, want); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(configPath, configPath+".session-reviewer-backup"); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(InitOptions{ProjectRoot: secondRoot, VaultRoot: t.TempDir(), DataDir: data, Random: bytes.NewReader(bytes.Repeat([]byte{0x22}, 16))}); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Projects) != 2 || got.Projects[0] != want.Projects[0] {
		t.Fatalf("mappings=%+v", got.Projects)
	}
}

func TestInitializeUsesBackupOnlyOverviewIdentity(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	wantID := "project-3333333333333333"
	writeTestOverview(t, root, wantID)
	path := filepath.Join(root, "docs", "session-review", "project-overview.md")
	if err := os.Rename(path, path+".session-reviewer-backup"); err != nil {
		t.Fatal(err)
	}
	result, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, Random: errorReader{}})
	if err != nil || result.ProjectID != wantID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestInitializeUsesValidOverviewBackupWhenPrimaryIsInvalid(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	wantID := "project-4444444444444444"
	writeTestOverview(t, root, wantID)
	path := filepath.Join(root, "docs", "session-review", "project-overview.md")
	if err := os.Rename(path, path+".session-reviewer-backup"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nproject_id: invalid\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, Random: errorReader{}})
	if err != nil || result.ProjectID != wantID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestInitializeRejectsRedirectedAncestor(t *testing.T) {
	base, realBase := t.TempDir(), t.TempDir()
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(realBase, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	root := filepath.Join(alias, "project")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: t.TempDir(), DataDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "symlink or reparse point") {
		t.Fatalf("err=%v", err)
	}
}

func TestInitializeProjectRootReplacementCannotRedirectOverviewWrite(t *testing.T) {
	base, root, moved, outside := t.TempDir(), "", "", ""
	root = filepath.Join(base, "project")
	moved = filepath.Join(base, "moved")
	outside = filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: t.TempDir(), DataDir: t.TempDir(), beforeOverviewWrite: func() error {
		if err := os.Rename(root, moved); err != nil {
			return err
		}
		return os.Symlink(outside, root)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, "docs", "session-review", "project-overview.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "docs")); !os.IsNotExist(err) {
		t.Fatalf("outside write: %v", err)
	}
}

func TestInitializeDataRootReplacementCannotRedirectConfigWrite(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	moved := filepath.Join(base, "moved")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{data, outside} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Initialize(InitOptions{ProjectRoot: t.TempDir(), VaultRoot: t.TempDir(), DataDir: data, beforeConfigWrite: func() error {
		if err := os.Rename(data, moved); err != nil {
			return err
		}
		return os.Symlink(outside, data)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, "config.toml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("outside write: %v", err)
	}
}

func TestInitializeLockReleasedAfterSubprocessCrash(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestInitSubprocessHelper$")
	cmd.Env = append(os.Environ(), "SESSION_REVIEWER_INIT_HELPER=crash", "SESSION_REVIEWER_INIT_PROJECT="+root, "SESSION_REVIEWER_INIT_VAULT="+vault, "SESSION_REVIEWER_INIT_DATA="+data)
	if err := cmd.Run(); err == nil {
		t.Fatal("helper did not crash")
	}
	if _, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data}); err != nil {
		t.Fatalf("lock survived crashed owner: %v", err)
	}
}

func TestInitializeTwoProcessesSerializeOneMapping(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	commands := make([]*exec.Cmd, 2)
	for i := range commands {
		commands[i] = exec.Command(os.Args[0], "-test.run=^TestInitSubprocessHelper$")
		commands[i].Env = append(os.Environ(), "SESSION_REVIEWER_INIT_HELPER=normal", "SESSION_REVIEWER_INIT_PROJECT="+root, "SESSION_REVIEWER_INIT_VAULT="+vault, "SESSION_REVIEWER_INIT_DATA="+data)
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(filepath.Join(data, "config.toml"))
	if err != nil || len(cfg.Projects) != 1 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestInitSubprocessHelper(t *testing.T) {
	mode := os.Getenv("SESSION_REVIEWER_INIT_HELPER")
	if mode == "" {
		return
	}
	opts := InitOptions{ProjectRoot: os.Getenv("SESSION_REVIEWER_INIT_PROJECT"), VaultRoot: os.Getenv("SESSION_REVIEWER_INIT_VAULT"), DataDir: os.Getenv("SESSION_REVIEWER_INIT_DATA")}
	if mode == "crash" {
		opts.afterLock = func() error { os.Exit(23); return nil }
	}
	if _, err := Initialize(opts); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireInitLockUsesPersistentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml.lock")
	if err := os.WriteFile(path, []byte("unknown-owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lock, err := acquireInitLock(root, "config.toml.lock", 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.release(); err != nil {
		t.Fatal(err)
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
