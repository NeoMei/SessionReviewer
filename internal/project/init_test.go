package project

import (
	"bytes"
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
