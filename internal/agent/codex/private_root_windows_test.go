//go:build windows

package codex

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsOpenPrivateRootRejectsPermissiveDACL(t *testing.T) {
	path := t.TempDir()
	if err := prepareOwnedPrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	var pinner runtime.Pinner
	pinner.Pin(world)
	defer pinner.Unpin()
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(world),
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if root, err := openPrivateRoot(path); err == nil {
		root.close()
		t.Fatal("permissive Windows DACL was accepted")
	}
}

func TestWindowsOpenPrivateRootRejectsReparsePoint(t *testing.T) {
	realPath := t.TempDir()
	if err := prepareOwnedPrivateDirectory(realPath); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skipf("creating a Windows reparse point requires Developer Mode or privilege: %v", err)
	}
	if root, err := openPrivateRoot(linkPath); err == nil {
		root.close()
		t.Fatal("Windows reparse point was accepted")
	}
}

func TestWindowsPrivateDirectoryRejectsLeafReplacement(t *testing.T) {
	basePath := t.TempDir()
	if err := prepareOwnedPrivateDirectory(basePath); err != nil {
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
		t.Skipf("filesystem does not permit replacement while a directory is pinned: %v", err)
	}
	decoy := filepath.Join(basePath, job.name)
	if err := os.Mkdir(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := prepareOwnedPrivateDirectory(decoy); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "keep"), []byte("decoy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := job.recheckForStart(); err == nil {
		t.Fatal("replacement leaf passed the suspended-start identity check")
	}
	if err := job.cleanup(); err == nil {
		t.Fatal("replacement leaf passed cleanup identity validation")
	}
	if data, err := os.ReadFile(filepath.Join(decoy, "keep")); err != nil || string(data) != "decoy" {
		t.Fatalf("cleanup touched replacement leaf data=%q err=%v", data, err)
	}
}

func TestWindowsPrivateRootRejectsAncestorReplacementWithoutTouchingDecoy(t *testing.T) {
	outer := t.TempDir()
	if err := prepareOwnedPrivateDirectory(outer); err != nil {
		t.Fatal(err)
	}
	basePath := filepath.Join(outer, "base")
	if err := os.Mkdir(basePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := prepareOwnedPrivateDirectory(basePath); err != nil {
		t.Fatal(err)
	}
	base, err := openPrivateRoot(basePath)
	if err != nil {
		t.Fatal(err)
	}
	defer base.close()
	movedOuter := outer + "-moved"
	if err := os.Rename(outer, movedOuter); err != nil {
		t.Skipf("filesystem does not permit replacing a pinned ancestor: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(movedOuter) })
	if err := os.MkdirAll(basePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := prepareOwnedPrivateDirectory(outer); err != nil {
		t.Fatal(err)
	}
	if err := prepareOwnedPrivateDirectory(basePath); err != nil {
		t.Fatal(err)
	}
	if job, err := base.createDirectory("run-"); err == nil {
		_ = job.cleanup()
		t.Fatal("ancestor replacement was accepted")
	}
	entries, err := os.ReadDir(basePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("pinned-root creation touched replacement ancestor: %v", entries)
	}
}

func TestWindowsImmediateChildIsContainedBeforeProcessResume(t *testing.T) {
	adapter := verifiedAdapter(t)
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "success-with-child")
	t.Setenv("SESSIONREVIEWER_FAKE_CHILD_PID_PATH", pidPath)
	request := validRequest(t, []byte("prompt"))
	request.Deadline = time.Now().Add(5 * time.Second)
	result, err := adapter.GenerateProposal(context.Background(), request)
	if err != nil || len(result.Proposal) == 0 {
		t.Fatalf("result bytes=%d err=%v", len(result.Proposal), err)
	}
	assertRecordedProcessStops(t, pidPath)
}
