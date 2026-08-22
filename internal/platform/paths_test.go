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

func TestResolveSessionsRootPrecedence(t *testing.T) {
	env := Env{
		GOOS:                        "darwin",
		Home:                        "/Users/me",
		SessionReviewerSessionsRoot: "/env/sessions",
		CodexHome:                   "/codex",
	}
	for _, test := range []struct {
		flag       string
		wantPath   string
		wantSource string
	}{
		{flag: "/flag/sessions", wantPath: "/flag/sessions", wantSource: "flag"},
		{wantPath: "/env/sessions", wantSource: "SESSION_REVIEWER_SESSIONS_ROOT"},
	} {
		got, err := ResolveSessionsRoot(test.flag, env)
		if err != nil {
			t.Fatal(err)
		}
		if got.Path != test.wantPath || got.Source != test.wantSource {
			t.Fatalf("got=%+v", got)
		}
	}

	env.SessionReviewerSessionsRoot = ""
	got, err := ResolveSessionsRoot("", env)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != filepath.Join("/codex", "sessions") || got.Source != "CODEX_HOME" {
		t.Fatalf("got=%+v", got)
	}

	env.CodexHome = ""
	got, err = ResolveSessionsRoot("", env)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != filepath.Join("/Users/me", ".codex", "sessions") || got.Source != "conventional" {
		t.Fatalf("got=%+v", got)
	}
}

func TestResolveSessionsRootRejectsMissingCandidates(t *testing.T) {
	if _, err := ResolveSessionsRoot("", Env{}); err == nil || !strings.Contains(err.Error(), "--sessions-root") {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveCurrentSessionIDPrecedenceAndConflict(t *testing.T) {
	env := Env{CodexThreadID: "thread-1", CodexSessionID: "session-1"}
	if id, source, err := ResolveCurrentSessionID("explicit", env); err != nil || id != "explicit" || source != "flag" {
		t.Fatalf("id=%q source=%q err=%v", id, source, err)
	}
	if id, source, err := ResolveCurrentSessionID("", env); err != nil || id != "thread-1" || source != "CODEX_THREAD_ID" {
		t.Fatalf("id=%q source=%q err=%v", id, source, err)
	}
	env.CodexThreadID = ""
	if id, source, err := ResolveCurrentSessionID("", env); err != nil || id != "session-1" || source != "CODEX_SESSION_ID" {
		t.Fatalf("id=%q source=%q err=%v", id, source, err)
	}
	env.CodexSessionID = ""
	if id, source, err := ResolveCurrentSessionID("", env); err != nil || id != "" || source != "cwd-and-time" {
		t.Fatalf("id=%q source=%q err=%v", id, source, err)
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
