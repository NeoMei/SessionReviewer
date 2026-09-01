package projectidentity

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

func TestResolveKeepsProjectIDAcrossVerifiedWorktreeAndMove(t *testing.T) {
	parent := t.TempDir()
	mainRoot := filepath.Join(parent, "main")
	commonDir := filepath.Join(mainRoot, ".git")
	if err := os.MkdirAll(filepath.Join(commonDir, "worktrees", "linked"), 0o700); err != nil {
		t.Fatal(err)
	}
	mapping := config.ProjectMapping{ID: "project-a", Root: mainRoot}
	mainBinding, err := Resolve(mapping, mainRoot, runtime.GOOS)
	if err != nil {
		t.Fatalf("bootstrap main root: %v", err)
	}
	mapping.AuthenticatedAliases = []config.AuthenticatedProjectAlias{mainBinding.AuthenticatedAlias}

	worktree := filepath.Join(parent, "linked")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(commonDir, "worktrees", "linked")
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktreeBinding, err := Resolve(mapping, worktree, runtime.GOOS)
	if err != nil || worktreeBinding.ProjectID != "project-a" || worktreeBinding.CommonDirIdentity != mainBinding.CommonDirIdentity || !worktreeBinding.NewAuthentication {
		t.Fatalf("worktree binding=%#v err=%v", worktreeBinding, err)
	}

	movedRoot := filepath.Join(parent, "moved")
	if err := os.Rename(mainRoot, movedRoot); err != nil {
		t.Fatal(err)
	}
	movedBinding, err := Resolve(mapping, movedRoot, runtime.GOOS)
	if err != nil || movedBinding.ProjectID != "project-a" || movedBinding.RootIdentity != mainBinding.RootIdentity || !movedBinding.NewAuthentication {
		t.Fatalf("moved binding=%#v err=%v", movedBinding, err)
	}
}

func TestResolveBootstrapReturnsPersistableAuthenticatedAlias(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	mapping := config.ProjectMapping{ID: "project-a", Root: root, VaultRoot: filepath.Join(parent, "vault")}
	wantMapping := cloneMapping(mapping)
	binding, err := Resolve(mapping, root, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	if !binding.Bootstrap || !binding.NewAuthentication {
		t.Fatal("first exact-root resolution was not marked as bootstrap")
	}
	alias := binding.AuthenticatedAlias
	if alias.SchemaVersion != 1 || alias.Path != root || alias.RootIdentity != binding.RootIdentity || alias.CommonDirIdentity != binding.CommonDirIdentity {
		t.Fatalf("bootstrap alias does not capture binding: alias=%#v binding=%#v", alias, binding)
	}
	if !reflect.DeepEqual(mapping, wantMapping) {
		t.Fatalf("bootstrap mutated caller mapping: got=%+v want=%+v", mapping, wantMapping)
	}

	mapping.AuthenticatedAliases = []config.AuthenticatedProjectAlias{alias}
	configPath := filepath.Join(parent, "config", "config.toml")
	if err := config.Save(configPath, config.Config{Version: 1, Projects: []config.ProjectMapping{mapping}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(configPath)
	if err != nil || len(loaded.Projects) != 1 || len(loaded.Projects[0].AuthenticatedAliases) != 1 {
		t.Fatalf("load persisted bootstrap metadata: projects=%+v err=%v", loaded.Projects, err)
	}
	alias = loaded.Projects[0].AuthenticatedAliases[0]
	authenticated, err := Resolve(loaded.Projects[0], root, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	if authenticated.Bootstrap || authenticated.NewAuthentication || authenticated.ProjectID != mapping.ID || authenticated.RootIdentity != binding.RootIdentity || authenticated.AuthenticatedAlias != alias {
		t.Fatalf("persisted metadata did not round trip immediately: %#v", authenticated)
	}
}

func TestReauthenticateDetectsGitCommonDirectoryReplacement(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	binding, err := Resolve(config.ProjectMapping{ID: "project-a", Root: root}, root, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	if err := Reauthenticate(binding); err != nil {
		t.Fatalf("unchanged binding failed reauthentication: %v", err)
	}
	if err := os.Rename(filepath.Join(root, ".git"), filepath.Join(root, ".git-original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Reauthenticate(binding); err == nil {
		t.Fatal("replaced Git common directory retained authenticated authority")
	}
}

func TestReauthenticateDetectsWorktreeGitdirRedirection(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "worktree")
	firstGitDir := filepath.Join(parent, "common-one.git", "worktrees", "one")
	secondGitDir := filepath.Join(parent, "common-two.git", "worktrees", "two")
	for _, directory := range []string{root, firstGitDir, secondGitDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+firstGitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binding, err := Resolve(config.ProjectMapping{ID: "project-a", Root: root}, root, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+secondGitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Reauthenticate(binding); err == nil {
		t.Fatal("rewritten worktree Git pointer retained authenticated authority")
	}
}

func TestResolveRejectsReplacedExactRootAfterBootstrapWithoutMutation(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	mapping := config.ProjectMapping{ID: "project-a", Root: root}
	binding, err := Resolve(mapping, root, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	mapping.AuthenticatedAliases = []config.AuthenticatedProjectAlias{binding.AuthenticatedAlias}
	wantMapping := cloneMapping(mapping)
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(mapping, root, runtime.GOOS); !errors.Is(err, ErrAssociationRequired) {
		t.Fatalf("replacement error=%v, want association required", err)
	}
	if !reflect.DeepEqual(mapping, wantMapping) {
		t.Fatalf("replacement resolution mutated mapping: got=%+v want=%+v", mapping, wantMapping)
	}
}

func TestResolveNeverUsesLegacyCommonDirsAsIdentityAuthority(t *testing.T) {
	parent := t.TempDir()
	configuredRoot := filepath.Join(parent, "configured")
	if err := os.MkdirAll(configuredRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	requestedRoot := filepath.Join(parent, "worktree")
	commonDir := filepath.Join(parent, "common.git")
	gitDir := filepath.Join(commonDir, "worktrees", "linked")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(requestedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(requestedRoot, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mapping := config.ProjectMapping{ID: "project-a", Root: configuredRoot, CommonDirs: []string{commonDir}}
	if _, err := Resolve(mapping, requestedRoot, runtime.GOOS); !errors.Is(err, ErrAssociationRequired) {
		t.Fatalf("mutable CommonDirs path authenticated worktree: %v", err)
	}
}

func TestResolveNeverUsesReplacedLegacyCommonDirAsIdentityAuthority(t *testing.T) {
	parent := t.TempDir()
	configuredRoot := filepath.Join(parent, "configured")
	requestedRoot := filepath.Join(parent, "requested")
	commonPath := filepath.Join(requestedRoot, ".git")
	if err := os.MkdirAll(configuredRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(commonPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(commonPath, commonPath+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(commonPath, 0o700); err != nil {
		t.Fatal(err)
	}
	mapping := config.ProjectMapping{ID: "project-a", Root: configuredRoot, CommonDirs: []string{commonPath}}
	if _, err := Resolve(mapping, requestedRoot, runtime.GOOS); !errors.Is(err, ErrAssociationRequired) {
		t.Fatalf("replaced CommonDirs path authenticated project: %v", err)
	}
}

func TestResolveRejectsMutablePathOrRemoteAloneAndDoesNotMutateMapping(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	mapping := config.ProjectMapping{
		ID:               "project-a",
		Root:             root,
		RemoteIdentities: []string{"github.com/example/repo"},
		Aliases:          []string{other},
	}
	want := cloneMapping(mapping)
	if _, err := Resolve(mapping, other, runtime.GOOS); !errors.Is(err, ErrAssociationRequired) {
		t.Fatalf("error=%v, want association required", err)
	}
	if !reflect.DeepEqual(mapping, want) {
		t.Fatalf("Resolve mutated mapping: got=%+v want=%+v", mapping, want)
	}
}

func TestResolveConflictingAuthenticatedEvidenceRequiresAssociation(t *testing.T) {
	root := t.TempDir()
	wrong := t.TempDir()
	rootDirectory, err := pathguard.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootDirectory.Close()
	wrongDirectory, err := pathguard.Open(wrong)
	if err != nil {
		t.Fatal(err)
	}
	defer wrongDirectory.Close()
	wrongIdentity, err := wrongDirectory.PhysicalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	mapping := config.ProjectMapping{
		ID:   "project-a",
		Root: root,
		AuthenticatedAliases: []config.AuthenticatedProjectAlias{{
			SchemaVersion: 1,
			Path:          root,
			RootIdentity:  wrongIdentity,
		}},
	}
	if _, err := Resolve(mapping, root, runtime.GOOS); !errors.Is(err, ErrAssociationRequired) {
		t.Fatalf("error=%v, want association required", err)
	}
}

func TestResolvePreservesTrailingSpacePathsOnDarwin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve trailing-space path components")
	}
	parent := t.TempDir()
	plain := filepath.Join(parent, "repo")
	spaced := filepath.Join(parent, "repo ")
	if err := os.MkdirAll(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(spaced, 0o700); err != nil {
		t.Fatal(err)
	}
	plainBinding, err := Resolve(config.ProjectMapping{ID: "project-a", Root: plain}, plain, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	mapping := config.ProjectMapping{ID: "project-a", Root: plain, AuthenticatedAliases: []config.AuthenticatedProjectAlias{{
		SchemaVersion: 1, Path: plain, RootIdentity: plainBinding.RootIdentity,
	}}}
	if _, err := Resolve(mapping, spaced, "darwin"); !errors.Is(err, ErrAssociationRequired) {
		t.Fatalf("trailing-space alias collapsed: %v", err)
	}
	spacedBinding, err := Resolve(config.ProjectMapping{ID: "project-b", Root: spaced}, spaced, "darwin")
	if err != nil || !strings.HasSuffix(spacedBinding.CanonicalRoot, "repo ") {
		t.Fatalf("binding=%#v err=%v", spacedBinding, err)
	}
}

func TestWindowsAliasKeysUsePortableCaseFoldAndRejectTrailingSpaces(t *testing.T) {
	first, err := pathAliasKey("windows", `C:\Work\Project`)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pathAliasKey("windows", `c:/work/project`)
	if err != nil || first != second {
		t.Fatalf("keys differ: %q %q err=%v", first, second, err)
	}
	if _, err := pathAliasKey("windows", `C:\Work\Project `); err == nil {
		t.Fatal("Windows trailing-space alias was accepted")
	}
}

func cloneMapping(value config.ProjectMapping) config.ProjectMapping {
	value.RemoteIdentities = append([]string(nil), value.RemoteIdentities...)
	value.CommonDirs = append([]string(nil), value.CommonDirs...)
	value.Aliases = append([]string(nil), value.Aliases...)
	value.AuthenticatedAliases = append([]config.AuthenticatedProjectAlias(nil), value.AuthenticatedAliases...)
	return value
}
