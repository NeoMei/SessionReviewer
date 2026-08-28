//go:build !windows

package codex

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateRootKeepsAncestorReplacementOutsideJobWrites(t *testing.T) {
	outer := t.TempDir()
	basePath := filepath.Join(outer, "base")
	if err := os.Mkdir(basePath, 0o700); err != nil {
		t.Fatal(err)
	}
	base, err := openPrivateRoot(basePath)
	if err != nil {
		t.Fatal(err)
	}
	defer base.close()

	movedOuter := outer + "-moved"
	if err := os.Rename(outer, movedOuter); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(movedOuter) })
	if err := os.MkdirAll(basePath, 0o700); err != nil {
		t.Fatal(err)
	}

	job, err := base.createDirectory("run-")
	if err != nil {
		t.Fatal(err)
	}
	if err := job.writePrivateFile(outputSchemaName, []byte("anchored")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(basePath, job.name, outputSchemaName)); !os.IsNotExist(err) {
		t.Fatalf("replacement ancestor received an anchored write: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(movedOuter, "base", job.name, outputSchemaName)); err != nil || string(data) != "anchored" {
		t.Fatalf("pinned root write data=%q err=%v", data, err)
	}
	command := exec.Command("/bin/pwd")
	if err := job.configureCommandDirectory(command); err != nil {
		t.Fatal(err)
	}
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	wantDirectory := filepath.Join(movedOuter, "base", job.name)
	gotDirectory := strings.TrimSpace(string(output))
	gotInfo, gotErr := os.Stat(gotDirectory)
	wantInfo, wantErr := os.Stat(wantDirectory)
	if gotErr != nil || wantErr != nil || !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("pinned command cwd=%q want=%q gotErr=%v wantErr=%v", gotDirectory, wantDirectory, gotErr, wantErr)
	}
	if err := job.cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateDirectoryCleanupRefusesLeafReplacement(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "base")
	if err := os.Mkdir(basePath, 0o700); err != nil {
		t.Fatal(err)
	}
	base, err := openPrivateRoot(basePath)
	if err != nil {
		t.Fatal(err)
	}
	defer base.close()
	job, err := base.createDirectory("run-")
	if err != nil {
		t.Fatal(err)
	}
	if err := job.writePrivateFile(outputSchemaName, []byte("private")); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(basePath, "moved")
	if err := os.Rename(filepath.Join(basePath, job.name), moved); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(basePath, job.name)
	if err := os.Mkdir(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "keep"), []byte("decoy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := job.recheckForStart(); !errors.Is(err, errPrivateRootIdentityChanged) {
		t.Fatalf("replacement leaf start check err=%v", err)
	}
	if err := job.cleanup(); err == nil {
		t.Fatal("leaf replacement was not detected")
	}
	if data, err := os.ReadFile(filepath.Join(decoy, "keep")); err != nil || string(data) != "decoy" {
		t.Fatalf("cleanup touched replacement leaf data=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(moved, outputSchemaName)); !os.IsNotExist(err) {
		t.Fatalf("anchored private contents were not cleaned: %v", err)
	}
}

func TestOpenPrivateRootRejectsSymlinkAndPermissiveDirectory(t *testing.T) {
	realPath := filepath.Join(t.TempDir(), "real")
	if err := os.Mkdir(realPath, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if root, err := openPrivateRoot(symlinkPath); err == nil {
		root.close()
		t.Fatal("symlink root was accepted")
	}

	permissive := filepath.Join(t.TempDir(), "permissive")
	if err := os.Mkdir(permissive, 0o755); err != nil {
		t.Fatal(err)
	}
	if root, err := openPrivateRoot(permissive); err == nil {
		root.close()
		t.Fatal("permissive root was accepted")
	}

	nonempty := filepath.Join(t.TempDir(), "nonempty")
	if err := os.Mkdir(nonempty, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nonempty, "foreign"), []byte("must not be exposed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if root, err := openPrivateRoot(nonempty); err == nil {
		root.close()
		t.Fatal("non-empty root was accepted")
	}
}
