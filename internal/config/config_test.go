package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
