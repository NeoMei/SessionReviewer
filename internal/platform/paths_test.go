package platform

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDataDirMacOS(t *testing.T) {
	got, err := DataDir(Env{GOOS: "darwin", Home: "/Users/mei"})
	if err != nil || got != filepath.Join("/Users/mei", ".local", "share", "session-reviewer") {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestDataDirWindows(t *testing.T) {
	got, err := DataDir(Env{GOOS: "windows", LocalAppData: `C:\Users\Mei\AppData\Local`})
	if err != nil || got != filepath.Join(`C:\Users\Mei\AppData\Local`, "SessionReviewer") {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestDataDirRejectsMissingBase(t *testing.T) {
	if _, err := DataDir(Env{GOOS: "windows"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestNormalizePathWindowsAcrossSlashAndCase(t *testing.T) {
	a := NormalizePath("windows", `C:\项目\Repo`)
	b := NormalizePath("windows", `c:/项目/repo`)
	if a != b {
		t.Fatalf("a=%q b=%q", a, b)
	}
}

func TestNormalizePathWindowsPreservesDriveRootIdentity(t *testing.T) {
	root := NormalizePath("windows", `C:\`)
	relative := NormalizePath("windows", `C:`)
	if root == relative {
		t.Fatalf("drive root collided with drive-relative path: %q", root)
	}
}

func TestNormalizePathWindowsCleansEquivalentDrivePaths(t *testing.T) {
	a := NormalizePath("windows", `C:\Projects\.\SessionReviewer\..\SessionReviewer`)
	b := NormalizePath("windows", `c:/projects/sessionreviewer`)
	if a != b {
		t.Fatalf("a=%q b=%q", a, b)
	}
}

func TestNormalizePathWindowsPreservesUNCShare(t *testing.T) {
	a := NormalizePath("windows", `\\Server\Share\Folder\.\Child`)
	b := NormalizePath("windows", `//server/share/folder/child`)
	if a != b {
		t.Fatalf("a=%q b=%q", a, b)
	}
	localRooted := NormalizePath("windows", `/server/share/folder/child`)
	if a == localRooted {
		t.Fatalf("UNC path collided with rooted path: %q", a)
	}
}

func TestNormalizePathWindowsPreservesDevicePrefixIdentity(t *testing.T) {
	device := NormalizePath("windows", `\\?\C:\Projects\SessionReviewer`)
	regular := NormalizePath("windows", `C:\Projects\SessionReviewer`)
	dotDevice := NormalizePath("windows", `\\.\C:\Projects\SessionReviewer`)
	if device == regular {
		t.Fatalf("device path collided with regular path: %q", device)
	}
	if device == dotDevice {
		t.Fatalf("device prefixes collided: %q", device)
	}
	if !strings.HasPrefix(device, `//?/`) || !strings.HasPrefix(dotDevice, `//./`) {
		t.Fatalf("device prefixes were not preserved: device=%q dotDevice=%q", device, dotDevice)
	}
}
