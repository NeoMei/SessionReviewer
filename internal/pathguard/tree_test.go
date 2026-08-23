package pathguard

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestTreeRejectsUnsafeRelativePaths(t *testing.T) {
	directory, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	unsafe := []string{"", "..", "../escape", "a/../escape", "/absolute", `\\server\share`, `\\?\C:\device`, `C:\device`, `NUL`, "dir/COM1.txt", "dir\\file.md"}
	for _, relative := range unsafe {
		t.Run(strings.ReplaceAll(relative, "/", "_"), func(t *testing.T) {
			if err := directory.EnsureDirectory(relative, 0o700); err == nil {
				t.Fatalf("EnsureDirectory(%q) succeeded", relative)
			}
			if _, _, err := directory.ReadRegular(relative, 1024); err == nil {
				t.Fatalf("ReadRegular(%q) succeeded", relative)
			}
			if err := directory.WalkMarkdown(relative, func(string, []byte) error { return nil }); err == nil {
				t.Fatalf("WalkMarkdown(%q) succeeded", relative)
			}
		})
	}
	if err := directory.EnsureDirectory(".", 0o700); err == nil {
		t.Fatal("EnsureDirectory accepted the root itself")
	}
	if _, _, err := directory.ReadRegular(".", 1024); err == nil {
		t.Fatal("ReadRegular accepted the root itself")
	}
}

func TestTreeEnsuresPinnedPrivateDirectories(t *testing.T) {
	rootPath := t.TempDir()
	directory, err := Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	if err := directory.EnsureDirectory("one/two/three", 0o700); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"one", "one/two", "one/two/three"} {
		info, err := os.Stat(filepath.Join(rootPath, filepath.FromSlash(relative)))
		if err != nil || !info.IsDir() {
			t.Fatalf("relative=%q info=%v err=%v", relative, info, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("relative=%q mode=%#o", relative, info.Mode().Perm())
		}
	}
}

func TestTreeRejectsRedirectedParentAndMarkdownFile(t *testing.T) {
	rootPath := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, "redirect")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	directory, err := Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	if err := directory.EnsureDirectory("redirect/child", 0o700); err == nil {
		t.Fatal("redirected parent accepted")
	}
	if _, _, err := directory.ReadRegular("redirect/secret.md", 1024); err == nil {
		t.Fatal("redirected file read")
	}
	visited := false
	err = directory.WalkMarkdown("redirect", func(string, []byte) error {
		visited = true
		return nil
	})
	if err == nil || visited {
		t.Fatalf("err=%v visited=%v", err, visited)
	}

	if err := os.Mkdir(filepath.Join(rootPath, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(rootPath, "docs", "linked.md")); err != nil {
		t.Skipf("file symlink unavailable: %v", err)
	}
	visited = false
	err = directory.WalkMarkdown("docs", func(string, []byte) error {
		visited = true
		return nil
	})
	if err == nil || visited {
		t.Fatalf("Markdown redirect was not rejected: err=%v visited=%v", err, visited)
	}
}

func TestTreeReadRegularRechecksNamespaceIdentityAfterRead(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "state.md"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	_, _, err = directory.readRegularWithHook("state.md", 1024, func() error {
		if err := os.Rename(filepath.Join(rootPath, "state.md"), filepath.Join(rootPath, "moved.md")); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(rootPath, "state.md"), []byte("replacement"), 0o600)
	})
	if err == nil {
		t.Fatal("file namespace replacement after read was accepted")
	}
}

func TestTreeReadRegularBoundsContentAndReturnsMissing(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "small.md"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	if _, found, err := directory.ReadRegular("missing.md", 5); err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if _, found, err := directory.ReadRegular("small.md", 4); err == nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	got, found, err := directory.ReadRegular("small.md", 5)
	if err != nil || !found || string(got) != "12345" {
		t.Fatalf("got=%q found=%v err=%v", got, found, err)
	}
}

func TestTreeReadRegularRejectsOverflowingLimitWithoutReturningEmptySuccess(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "state.md"), []byte("nonempty"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	got, _, err := directory.ReadRegular("state.md", math.MaxInt64)
	if err == nil || len(got) != 0 {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if got, found, err := directory.ReadRegular("state.md", int64(len("nonempty"))); err != nil || !found || string(got) != "nonempty" {
		t.Fatalf("boundary got=%q found=%v err=%v", got, found, err)
	}
}

func TestTreeReadRegularRejectsSameInodeMutationDuringSnapshot(t *testing.T) {
	for _, mutation := range []string{"append", "same-size rewrite"} {
		t.Run(mutation, func(t *testing.T) {
			rootPath := t.TempDir()
			path := filepath.Join(rootPath, "state.md")
			original := []byte("original")
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			directory, err := Open(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			got, found, err := directory.readRegularWithHook("state.md", 1024, func() error {
				switch mutation {
				case "append":
					file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
					if err != nil {
						return err
					}
					_, writeErr := file.WriteString("-appended")
					return errors.Join(writeErr, file.Close())
				case "same-size rewrite":
					if err := os.WriteFile(path, []byte("rewritten"), 0o600); err != nil {
						return err
					}
					return os.Chtimes(path, before.ModTime(), before.ModTime())
				default:
					return errors.New("unknown mutation")
				}
			})
			if err == nil || !found || len(got) != 0 {
				t.Fatalf("got=%q found=%v err=%v", got, found, err)
			}
		})
	}
}

func TestTreeWalkMarkdownRechecksEveryRootRelativeNamespaceComponent(t *testing.T) {
	for _, replaced := range []string{"docs", "docs/nested"} {
		t.Run(strings.ReplaceAll(replaced, "/", "_"), func(t *testing.T) {
			rootPath := t.TempDir()
			walkRoot := filepath.Join(rootPath, "docs", "nested")
			if err := os.MkdirAll(walkRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(walkRoot, "one.md"), []byte("one"), 0o600); err != nil {
				t.Fatal(err)
			}
			directory, err := Open(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			mutated := false
			err = directory.WalkMarkdown("docs/nested", func(string, []byte) error {
				if mutated {
					return nil
				}
				mutated = true
				live := filepath.Join(rootPath, filepath.FromSlash(replaced))
				if err := os.Rename(live, live+"-moved"); err != nil {
					return err
				}
				return os.Mkdir(live, 0o700)
			})
			if err == nil || !mutated {
				t.Fatalf("mutated=%v err=%v", mutated, err)
			}
		})
	}
}

func TestTreeWalkMarkdownUsesSlashSortedPathsAndPropagatesVisitorError(t *testing.T) {
	rootPath := t.TempDir()
	for relative, content := range map[string]string{
		"z.md":        "z",
		"a/second.md": "second",
		"a/first.MD":  "first",
		"a/no.txt":    "ignored",
	} {
		full := filepath.Join(rootPath, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	directory, err := Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	var visited []string
	if err := directory.WalkMarkdown("a", func(relative string, content []byte) error {
		visited = append(visited, relative+":"+string(content))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"a/first.MD:first", "a/second.md:second"}
	if !reflect.DeepEqual(visited, want) {
		t.Fatalf("visited=%v want=%v", visited, want)
	}
	if !sort.StringsAreSorted(visited) {
		t.Fatalf("paths are not sorted: %v", visited)
	}

	wantErr := errors.New("stop")
	err = directory.WalkMarkdown("a", func(string, []byte) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("err=%v", err)
	}
}

func TestTreeRejectsNonDirectoryWalkRoot(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "file.md"), []byte("x"), fs.FileMode(0o600)); err != nil {
		t.Fatal(err)
	}
	directory, err := Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	if err := directory.WalkMarkdown("file.md", func(string, []byte) error { return nil }); err == nil {
		t.Fatal("non-directory walk root accepted")
	}
}
