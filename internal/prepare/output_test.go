package prepare

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/pathguard"
)

func caseAliasPath(t *testing.T, path string) (string, bool) {
	t.Helper()
	alias := filepath.Join(filepath.Dir(path), strings.ToUpper(filepath.Base(path)))
	if alias == path {
		return "", false
	}
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	aliased, err := os.Stat(alias)
	if err != nil || !os.SameFile(original, aliased) {
		return "", false
	}
	return alias, true
}

func prepareTestOutputTarget(t *testing.T, path, sessions, data string) (*outputTarget, error) {
	t.Helper()
	pinned, err := pathguard.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	return prepareOutputTarget(path, sessions, pinned)
}

func TestOutputTargetRejectsSymlinkInEveryExistingAncestor(t *testing.T) {
	for _, test := range []struct {
		name       string
		linkParent string
		tail       []string
	}{
		{name: "shallow", linkParent: "", tail: []string{"child"}},
		{name: "deep", linkParent: "level", tail: []string{"child", "grandchild"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			realParent := filepath.Join(base, "real")
			sessions := filepath.Join(base, "sessions")
			data := filepath.Join(base, "data")
			for _, dir := range []string{realParent, sessions, data} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			realTail := append([]string{realParent}, test.tail...)
			if err := os.MkdirAll(filepath.Join(realTail...), 0o700); err != nil {
				t.Fatal(err)
			}
			linkBase := base
			if test.linkParent != "" {
				linkBase = filepath.Join(base, test.linkParent)
				if err := os.Mkdir(linkBase, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(linkBase, "link")
			if err := os.Symlink(realParent, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			outputParts := append([]string{link}, test.tail...)
			output := filepath.Join(append(outputParts, "packet.json")...)

			if target, err := prepareTestOutputTarget(t, output, sessions, data); err == nil {
				target.close()
				t.Fatal("expected ancestor symlink rejection")
			}
		})
	}
}

func TestOutputTargetUsesPhysicalIdentityForProtectedRoots(t *testing.T) {
	base := t.TempDir()
	sessions := filepath.Join(base, "sessions")
	data := filepath.Join(base, "data")
	for _, dir := range []string{sessions, data} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, protected := range []string{sessions, data} {
		alias, ok := caseAliasPath(t, protected)
		if !ok {
			t.Skip("test filesystem is case-sensitive")
		}
		if target, err := prepareTestOutputTarget(t, filepath.Join(alias, "packet.json"), sessions, data); err == nil {
			target.close()
			t.Fatalf("accepted casing alias into protected root: %s", alias)
		}
	}
}

func TestOutputTargetUsesPinnedDataIdentityAfterRename(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	moved := filepath.Join(base, "moved")
	sessions := filepath.Join(base, "sessions")
	for _, dir := range []string{data, sessions} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pinned, err := pathguard.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	if err := os.Rename(data, moved); err != nil {
		t.Fatal(err)
	}
	if target, err := prepareOutputTarget(filepath.Join(moved, "packet.json"), sessions, pinned); err == nil {
		target.close()
		t.Fatal("accepted output inside renamed pinned data root")
	}
}

func TestOutputTargetAllowsSimilarlyPrefixedSibling(t *testing.T) {
	base := t.TempDir()
	sessions := filepath.Join(base, "sessions")
	data := filepath.Join(base, "data")
	sibling := filepath.Join(base, "sessions-safe")
	for _, dir := range []string{sessions, data, sibling} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	target, err := prepareTestOutputTarget(t, filepath.Join(sibling, "packet.json"), sessions, data)
	if err != nil {
		t.Fatal(err)
	}
	target.close()
}

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
	target, err := prepareTestOutputTarget(t, filepath.Join(live, "packet.json"), sessions, data)
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
	target, err := prepareTestOutputTarget(t, filepath.Join(base, "new-parent", "packet.json"), sessions, data)
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
