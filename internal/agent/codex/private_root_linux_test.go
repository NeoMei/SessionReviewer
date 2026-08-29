//go:build linux

package codex

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLinuxCommandUsesPinnedDirectoryFDForPreExecChdir(t *testing.T) {
	if os.Getenv("SESSIONREVIEWER_LINUX_CWD_HELPER") == "1" {
		data, err := os.ReadFile(outputSchemaName)
		if err != nil {
			os.Exit(41)
		}
		info, err := os.Stat(".")
		if err != nil {
			os.Exit(42)
		}
		_, _ = os.Stdout.Write([]byte(info.Name()))
		_, _ = os.Stdout.Write([]byte{0})
		_, _ = os.Stdout.Write(data)
		os.Exit(0)
	}

	outer := t.TempDir()
	rootPath := filepath.Join(outer, "root")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := openPrivateRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.close()
	directory, err := root.createDirectory("run-")
	if err != nil {
		t.Fatal(err)
	}
	defer directory.cleanup()
	if err := directory.writePrivateFile(outputSchemaName, []byte("schema-readable")); err != nil {
		t.Fatal(err)
	}

	movedOuter := outer + "-moved"
	if err := os.Rename(outer, movedOuter); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(movedOuter) })
	if err := os.MkdirAll(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestLinuxCommandUsesPinnedDirectoryFDForPreExecChdir$")
	command.Env = append(os.Environ(), "SESSIONREVIEWER_LINUX_CWD_HELPER=1", sessionReviewerTestChildHelperEnv+"=1")
	if err := directory.configureCommandDirectory(command); err != nil {
		t.Fatal(err)
	}
	output, err := command.Output()
	if err != nil {
		t.Fatalf("child did not enter pinned directory: %v", err)
	}
	parts := bytes.SplitN(output, []byte{0}, 2)
	if len(parts) != 2 || string(parts[1]) != "schema-readable" {
		t.Fatalf("child output=%q", output)
	}
	wantInfo, err := directory.anchor.Stat()
	if err != nil {
		t.Fatal(err)
	}
	physicalPath := filepath.Join(movedOuter, "root", directory.name)
	gotInfo, err := os.Stat(physicalPath)
	if err != nil || !os.SameFile(wantInfo, gotInfo) {
		t.Fatalf("child cwd identity was redirected: err=%v", err)
	}
}
