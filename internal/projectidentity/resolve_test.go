package projectidentity

import (
	"errors"
	"os"
	"os/exec"
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
	runGitFixture(t, parent, "init", "-q", mainRoot)
	runGitFixture(t, mainRoot, "config", "user.email", "session-reviewer@example.invalid")
	runGitFixture(t, mainRoot, "config", "user.name", "Session Reviewer Test")
	if err := os.WriteFile(filepath.Join(mainRoot, "tracked.txt"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitFixture(t, mainRoot, "add", "tracked.txt")
	runGitFixture(t, mainRoot, "commit", "-q", "-m", "fixture")
	mapping := config.ProjectMapping{ID: "project-a", Root: mainRoot}
	mainBinding, err := Resolve(mapping, mainRoot, runtime.GOOS)
	if err != nil {
		t.Fatalf("bootstrap main root: %v", err)
	}
	mapping.AuthenticatedAliases = []config.AuthenticatedProjectAlias{mainBinding.AuthenticatedAlias}

	worktree := filepath.Join(parent, "linked")
	runGitFixture(t, mainRoot, "worktree", "add", "-q", "-b", "linked-branch", worktree)
	worktreeBinding, err := Resolve(mapping, worktree, runtime.GOOS)
	if err != nil {
		directory, openErr := pathguard.Open(worktree)
		if openErr == nil {
			_, cause := gitCommonDirIdentity(directory)
			_ = directory.Close()
			t.Logf("worktree authentication cause: %v", cause)
		}
	}
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

func TestResolveRejectsForgedWorktreePointerWithoutRegisteredBacklink(t *testing.T) {
	parent := t.TempDir()
	mainRoot := filepath.Join(parent, "main")
	commonDir := filepath.Join(mainRoot, ".git")
	if err := os.MkdirAll(filepath.Join(commonDir, "worktrees", "forged"), 0o700); err != nil {
		t.Fatal(err)
	}
	mapping := config.ProjectMapping{ID: "project-a", Root: mainRoot}
	mainBinding, err := Resolve(mapping, mainRoot, runtime.GOOS)
	if err != nil {
		t.Fatalf("bootstrap main root: %v", err)
	}
	mapping.AuthenticatedAliases = []config.AuthenticatedProjectAlias{mainBinding.AuthenticatedAlias}

	forgedRoot := filepath.Join(parent, "forged")
	if err := os.MkdirAll(forgedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(commonDir, "worktrees", "forged")
	if err := os.WriteFile(filepath.Join(forgedRoot, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(mapping, forgedRoot, runtime.GOOS); !errors.Is(err, ErrAssociationRequired) {
		t.Fatalf("forged worktree pointer error=%v, want association required", err)
	}
}

func runGitFixture(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
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
	mainRoot := filepath.Join(parent, "main")
	runGitFixture(t, parent, "init", "-q", mainRoot)
	runGitFixture(t, mainRoot, "config", "user.email", "session-reviewer@example.invalid")
	runGitFixture(t, mainRoot, "config", "user.name", "Session Reviewer Test")
	if err := os.WriteFile(filepath.Join(mainRoot, "tracked.txt"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitFixture(t, mainRoot, "add", "tracked.txt")
	runGitFixture(t, mainRoot, "commit", "-q", "-m", "fixture")
	root := filepath.Join(parent, "worktree")
	runGitFixture(t, mainRoot, "worktree", "add", "-q", "-b", "redirection-fixture", root)
	secondGitDir := filepath.Join(parent, "common-two.git", "worktrees", "two")
	if err := os.MkdirAll(secondGitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondGitDir, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondGitDir, "gitdir"), []byte(filepath.Join(root, ".git")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binding, err := Resolve(config.ProjectMapping{ID: "project-a", Root: root}, root, runtime.GOOS)
	if err != nil {
		directory, openErr := pathguard.Open(root)
		if openErr == nil {
			_, cause := gitCommonDirIdentity(directory)
			_ = directory.Close()
			t.Fatalf("%v (worktree authentication cause: %v)", err, cause)
		}
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

func TestResolveAcceptsSeparateGitDirWithCoreWorktree(t *testing.T) {
	parent := t.TempDir()
	projectRoot := filepath.Join(parent, "project")
	separateGitDir := filepath.Join(parent, "repo.git")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(separateGitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".git"), []byte("gitdir: "+separateGitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(separateGitDir, "config"), []byte("[core]\n\tworktree = "+projectRoot+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mapping := config.ProjectMapping{ID: "project-a", Root: projectRoot}
	binding, err := Resolve(mapping, projectRoot, runtime.GOOS)
	if err != nil {
		t.Fatalf("separate git dir bootstrap: %v", err)
	}
	if binding.CommonDirIdentity == "" {
		t.Fatal("common dir identity empty")
	}
	mapping.AuthenticatedAliases = []config.AuthenticatedProjectAlias{binding.AuthenticatedAlias}
	if err := Reauthenticate(binding); err != nil {
		t.Fatalf("reauthenticate separate git dir: %v", err)
	}
	binding2, err := Resolve(mapping, projectRoot, runtime.GOOS)
	if err != nil {
		t.Fatalf("resolve with alias: %v", err)
	}
	if binding2.CommonDirIdentity != binding.CommonDirIdentity || binding2.NewAuthentication {
		t.Fatalf("unexpected second binding: %#v", binding2)
	}
}

func TestResolveRejectsSeparateGitDirWithWrongCoreWorktree(t *testing.T) {
	parent := t.TempDir()
	projectRoot := filepath.Join(parent, "project")
	otherRoot := filepath.Join(parent, "other")
	separateGitDir := filepath.Join(parent, "repo.git")
	for _, d := range []string{projectRoot, otherRoot, separateGitDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".git"), []byte("gitdir: "+separateGitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(separateGitDir, "config"), []byte("[core]\n\tworktree = "+otherRoot+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mapping := config.ProjectMapping{ID: "project-a", Root: projectRoot}
	if _, err := Resolve(mapping, projectRoot, runtime.GOOS); !errors.Is(err, ErrAssociationRequired) {
		t.Fatalf("wrong core.worktree error=%v, want association required", err)
	}
}

func TestResolveAcceptsSeparateGitDirWithBacklinkFile(t *testing.T) {
	parent := t.TempDir()
	projectRoot := filepath.Join(parent, "project")
	separateGitDir := filepath.Join(parent, "repo.git")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(separateGitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".git"), []byte("gitdir: "+separateGitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(separateGitDir, "gitdir"), []byte(filepath.Join(projectRoot, ".git")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mapping := config.ProjectMapping{ID: "project-a", Root: projectRoot}
	if _, err := Resolve(mapping, projectRoot, runtime.GOOS); err != nil {
		t.Fatalf("separate git dir backlink bootstrap: %v", err)
	}
}

func TestResolveRejectsReplacedSeparateGitDirPointer(t *testing.T) {
	parent := t.TempDir()
	projectRoot := filepath.Join(parent, "project")
	separateGitDir := filepath.Join(parent, "repo.git")
	otherRoot := filepath.Join(parent, "other")
	otherGitDir := filepath.Join(parent, "other.git")
	for _, d := range []string{projectRoot, separateGitDir, otherRoot, otherGitDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".git"), []byte("gitdir: "+separateGitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(separateGitDir, "config"), []byte("[core]\n\tworktree = "+projectRoot+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mapping := config.ProjectMapping{ID: "project-a", Root: projectRoot}
	binding, err := Resolve(mapping, projectRoot, runtime.GOOS)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	mapping.AuthenticatedAliases = []config.AuthenticatedProjectAlias{binding.AuthenticatedAlias}

	if err := os.WriteFile(filepath.Join(projectRoot, ".git"), []byte("gitdir: "+otherGitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherGitDir, "config"), []byte("[core]\n\tworktree = "+otherRoot+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Reauthenticate(binding); err == nil {
		t.Fatal("replaced separate Git pointer retained authenticated authority")
	}
}
