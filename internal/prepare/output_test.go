package prepare

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOutputTargetRejectsParentReplacementRace(t *testing.T) {
	base := t.TempDir()
	live := filepath.Join(base, "live")
	moved := filepath.Join(base, "moved")
	outside := filepath.Join(base, "outside")
	sessions := filepath.Join(base, "sessions")
	data := filepath.Join(base, "data")
	for _, dir := range []string{live, outside, sessions, data} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(live, "packet.json"), filepath.Join(outside, "packet.json")} {
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target, err := prepareOutputTarget(filepath.Join(live, "packet.json"), sessions, data)
	if err != nil {
		t.Fatal(err)
	}
	defer target.close()
	if err := os.Rename(live, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, live); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := target.write([]byte("new")); err == nil {
		t.Fatal("expected parent replacement error")
	}
	for path, want := range map[string]string{
		filepath.Join(moved, "packet.json"):   "old",
		filepath.Join(outside, "packet.json"): "old",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s=%q err=%v", path, got, err)
		}
	}
}

func TestOutputTargetRejectsNewSymlinkParentRace(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	sessions := filepath.Join(base, "sessions")
	data := filepath.Join(base, "data")
	for _, dir := range []string{outside, sessions, data} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	target, err := prepareOutputTarget(filepath.Join(base, "new-parent", "packet.json"), sessions, data)
	if err != nil {
		t.Fatal(err)
	}
	defer target.close()
	if err := os.Symlink(outside, filepath.Join(base, "new-parent")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := target.write([]byte("new")); err == nil {
		t.Fatal("expected new parent redirection error")
	}
	if _, err := os.Stat(filepath.Join(outside, "packet.json")); !os.IsNotExist(err) {
		t.Fatalf("outside output created: %v", err)
	}
}
