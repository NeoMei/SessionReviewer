package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/platform"
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
	if err != nil || len(got.Projects) != 2 || !reflect.DeepEqual(got.Projects[1], want.Projects[1]) {
		t.Fatalf("got=%+v err=%v", got, err)
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
	if len(got.Projects) != 1 || !reflect.DeepEqual(got.Projects[0], want.Projects[0]) {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if info.Mode().Perm()&0o200 == 0 {
			t.Fatalf("mode=%o, want writable", info.Mode().Perm())
		}
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o, want 600", info.Mode().Perm())
	}
}

func TestLoadRejectsNonPrivateConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX owner-only modes")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "version = 1\n\n[[projects]]\nid = 'project-1111111111111111'\nroot = '/work'\nvault_root = '/vault'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("non-private configuration accepted")
	}
}

func TestSaveFailsClosedAndPreservesUntrustedBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	backup := path + ".session-reviewer-backup"
	if err := os.WriteFile(backup, []byte("untrusted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(backup, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(path, Config{Version: 1, Projects: []ProjectMapping{{ID: "project-1111111111111111", Root: "/work", VaultRoot: "/vault"}}}); err == nil || !strings.Contains(err.Error(), "recovery backup") {
		t.Fatalf("err=%v", err)
	}
	if got, readErr := os.ReadFile(backup); readErr != nil || string(got) != "untrusted" {
		t.Fatalf("untrusted backup was changed: body=%q err=%v", got, readErr)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("primary was unexpectedly written: %v", statErr)
	}
}

func TestSaveCleansOnlyByteIdenticalStaleBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	old := Config{Version: 1, Projects: []ProjectMapping{{ID: "project-1111111111111111", Root: "/old", VaultRoot: "/vault"}}}
	if err := Save(path, old); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backup := path + ".session-reviewer-backup"
	if err := os.WriteFile(backup, body, 0o600); err != nil {
		t.Fatal(err)
	}
	next := Config{Version: 1, Projects: []ProjectMapping{{ID: "project-1111111111111111", Root: "/new", VaultRoot: "/vault"}}}
	if err := Save(path, next); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got.Projects[0].Root != "/new" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("converged backup remains: %v", err)
	}
}

func TestConfigUnionRoundTripPreservesEveryField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	want := Config{
		Version: 1,
		Projects: []ProjectMapping{{
			ID:               "project-2a2a2a2a2a2a2a2a",
			Root:             "/work/项目",
			VaultRoot:        "/notes/知识库",
			VaultReviewPath:  "Projects/项目--2a2a2a2a/Session Review",
			VaultCaseMode:    platform.CaseInsensitive,
			RemoteIdentities: []string{"origin:https://example.invalid/repo.git"},
			CommonDirs:       []string{"/work/common"},
			Aliases:          []string{"reviewer", "会话审查"},
		}},
		SessionAssociations: []SessionAssociation{{SessionID: "session-1", ProjectID: "project-2a2a2a2a2a2a2a2a"}},
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}

func TestConfigAllowsLegacyMappingOnlyWhenBothVaultFieldsAreEmpty(t *testing.T) {
	legacy := Config{Version: 1, Projects: []ProjectMapping{{ID: "project-1111111111111111", Root: "/work", VaultRoot: "/vault"}}}
	if err := Save(filepath.Join(t.TempDir(), "config.toml"), legacy); err != nil {
		t.Fatal(err)
	}
	tests := []ProjectMapping{
		{ID: "project-1111111111111111", Root: "/work", VaultRoot: "/vault", VaultReviewPath: "Projects/work--11111111/Session Review"},
		{ID: "project-1111111111111111", Root: "/work", VaultRoot: "/vault", VaultCaseMode: platform.CaseSensitive},
	}
	for _, mapping := range tests {
		if err := Save(filepath.Join(t.TempDir(), "config.toml"), Config{Version: 1, Projects: []ProjectMapping{mapping}}); err == nil {
			t.Fatalf("accepted partial mapping: %+v", mapping)
		}
	}
}

func TestConfigRejectsUnsafeVaultReviewMapping(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		caseMode platform.CaseMode
	}{
		{name: "unknown case mode", path: "Projects/work--11111111/Session Review", caseMode: "unknown"},
		{name: "not below projects", path: "Elsewhere/work--11111111/Session Review", caseMode: platform.CaseSensitive},
		{name: "projects root only", path: "Projects/Session Review", caseMode: platform.CaseSensitive},
		{name: "wrong leaf", path: "Projects/work--11111111/Review", caseMode: platform.CaseSensitive},
		{name: "backslash", path: `Projects\work--11111111\Session Review`, caseMode: platform.CaseSensitive},
		{name: "dot", path: "Projects/./work--11111111/Session Review", caseMode: platform.CaseSensitive},
		{name: "dotdot", path: "Projects/work--11111111/../Session Review", caseMode: platform.CaseSensitive},
		{name: "absolute", path: "/Projects/work--11111111/Session Review", caseMode: platform.CaseSensitive},
		{name: "drive", path: "C:/Projects/work--11111111/Session Review", caseMode: platform.CaseSensitive},
		{name: "UNC", path: "//server/share/Projects/work--11111111/Session Review", caseMode: platform.CaseSensitive},
		{name: "device", path: `\\?\C:\Projects\work--11111111\Session Review`, caseMode: platform.CaseSensitive},
		{name: "reserved", path: "Projects/CON/Session Review", caseMode: platform.CaseSensitive},
		{name: "trailing dot", path: "Projects/work./Session Review", caseMode: platform.CaseSensitive},
		{name: "trailing space", path: "Projects/work /Session Review", caseMode: platform.CaseSensitive},
		{name: "NUL", path: "Projects/work\x00name/Session Review", caseMode: platform.CaseSensitive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapping := ProjectMapping{
				ID: "project-1111111111111111", Root: "/work", VaultRoot: "/vault",
				VaultReviewPath: test.path, VaultCaseMode: test.caseMode,
			}
			if err := Save(filepath.Join(t.TempDir(), "config.toml"), Config{Version: 1, Projects: []ProjectMapping{mapping}}); err == nil {
				t.Fatalf("accepted unsafe mapping path %q", test.path)
			}
		})
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
