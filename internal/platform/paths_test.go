package platform

import (
	"path/filepath"
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
