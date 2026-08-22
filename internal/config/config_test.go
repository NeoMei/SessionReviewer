package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadUsesValidBackupWhenPrimaryMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	want := Config{Version: 1, Projects: []ProjectMapping{{ID: "project-1111111111111111", Root: "/one", VaultRoot: "/vault-one"}, {ID: "project-2222222222222222", Root: "/two", VaultRoot: "/vault-two"}}}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".session-reviewer-backup"); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Projects) != 2 || got.Projects[1] != want.Projects[1] {
		t.Fatalf("got=%+v", got)
	}
}

func TestLoadPrefersValidPrimaryOverBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	primary := "version = 1\n\n[[projects]]\nid = 'project-1111111111111111'\nroot = '/primary'\nvault_root = '/vault'\n"
	backup := "version = 1\n\n[[projects]]\nid = 'project-2222222222222222'\nroot = '/backup'\nvault_root = '/vault'\n"
	if err := os.WriteFile(path, []byte(primary), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".session-reviewer-backup", []byte(backup), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || len(got.Projects) != 1 || got.Projects[0].Root != "/primary" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestLoadFailsClosedForInvalidPrimaryAndBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("version = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".session-reviewer-backup", []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || strings.Contains(err.Error(), "version = 2") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadUsesValidBackupWhenPrimaryInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("version = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup := "version = 1\n\n[[projects]]\nid = 'project-1111111111111111'\nroot = '/kept'\nvault_root = '/vault'\n"
	if err := os.WriteFile(path+".session-reviewer-backup", []byte(backup), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || len(got.Projects) != 1 || got.Projects[0].Root != "/kept" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestLoadMissingConfigReturnsVersionOne(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 || len(cfg.Projects) != 0 {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestSaveAndLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	want := Config{Version: 1, Projects: []ProjectMapping{{
		ID: "project-2a2a2a2a2a2a2a2a", Root: "/work/项目", VaultRoot: "/notes/知识库",
	}}}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Projects) != 1 || got.Projects[0] != want.Projects[0] {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o, want 600", info.Mode().Perm())
	}
}

func TestLoadRejectsUnsupportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("version = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected unsupported version error")
	}
}

func TestFindProjectUsesTargetWindowsPathSemantics(t *testing.T) {
	cfg := Config{Version: 1, Projects: []ProjectMapping{{
		ID: "project-1", Root: `C:\Projects\SessionReviewer`, VaultRoot: `D:\Vault`,
	}}}
	got, ok := cfg.FindProject("windows", `c:/projects/./sessionreviewer`)
	if !ok || got.ID != "project-1" {
		t.Fatalf("got=%+v ok=%v", got, ok)
	}
}

func TestConfigRejectsDuplicateProjectIDAcrossRoots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	err := Save(path, Config{Version: 1, Projects: []ProjectMapping{
		{ID: "project-1111111111111111", Root: "/work/one", VaultRoot: "/vault/one"},
		{ID: "project-1111111111111111", Root: "/work/two", VaultRoot: "/vault/two"},
	}})
	if err == nil || !strings.Contains(err.Error(), "project ID is mapped more than once") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadRejectsDuplicateProjectIDAcrossRoots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "version = 1\n\n[[projects]]\nid = 'project-1111111111111111'\nroot = '/one'\nvault_root = '/v1'\n\n[[projects]]\nid = 'project-1111111111111111'\nroot = '/two'\nvault_root = '/v2'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "configuration state and recovery backup are invalid") {
		t.Fatalf("err=%v", err)
	}
}
