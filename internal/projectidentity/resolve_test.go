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
	mapping.AuthenticatedAliases = []config.AuthenticatedProjectAlias{{
		SchemaVersion:     1,
		Path:              mainRoot,
		RootIdentity:      mainBinding.RootIdentity,
		CommonDirIdentity: mainBinding.CommonDirIdentity,
	}}

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
	if err != nil || worktreeBinding.ProjectID != "project-a" || worktreeBinding.CommonDirIdentity != mainBinding.CommonDirIdentity {
		t.Fatalf("worktree binding=%#v err=%v", worktreeBinding, err)
	}

	movedRoot := filepath.Join(parent, "moved")
	if err := os.Rename(mainRoot, movedRoot); err != nil {
		t.Fatal(err)
	}
	movedBinding, err := Resolve(mapping, movedRoot, runtime.GOOS)
	if err != nil || movedBinding.ProjectID != "project-a" || movedBinding.RootIdentity != mainBinding.RootIdentity {
		t.Fatalf("moved binding=%#v err=%v", movedBinding, err)
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
