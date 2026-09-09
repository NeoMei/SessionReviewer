package projectprobe

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseStatusDirectoryRecords(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		count     int
		malformed bool
	}{
		{"file", "?? docs.txt\x00", 1, false},
		{"directory", "?? docs/\x00", 1, false},
		{"unicode space directory", "?? 资料 空间/\x00", 1, false},
		{"ignored directory", "!! ignored/\x00", 1, false},
		{"tracked terminal slash", " M tracked/\x00", 0, true},
		{"root", "?? /\x00", 0, true},
		{"double slash", "?? docs//\x00", 0, true},
		{"traversal", "?? ../docs/\x00", 0, true},
		{"rename", "R  new.txt\x00old.txt\x00", 1, false},
		{"bad rename source", "R  new.txt\x00old/\x00", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			count, malformed := parseStatus([]byte(tc.raw))
			if count != tc.count || malformed != tc.malformed {
				t.Fatalf("got (%d,%v), want (%d,%v)", count, malformed, tc.count, tc.malformed)
			}
		})
	}
}

func TestParseStatusDirectoryPathBoundaries(t *testing.T) {
	acceptedDirectory := []byte("?? " + strings.Repeat("a", 4095) + "/\x00")
	rejectedDirectory := []byte("?? " + strings.Repeat("a", 4096) + "/\x00")
	cases := []struct {
		name      string
		raw       []byte
		count     int
		malformed bool
	}{
		{"4096 byte directory name", acceptedDirectory, 1, false},
		{"4097 byte directory name", rejectedDirectory, 0, true},
		{"4096 byte file name", []byte("?? " + strings.Repeat("a", 4096) + "\x00"), 1, false},
		{"control", []byte("?? docs/\x1fmore/\x00"), 0, true},
		{"invalid UTF-8", append([]byte("?? docs/"), 0xff, '/', 0), 0, true},
		{"empty", []byte("?? \x00"), 0, true},
		{"missing NUL", []byte("?? docs/"), 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			count, malformed := parseStatus(tc.raw)
			if count != tc.count || malformed != tc.malformed {
				t.Fatalf("got (%d,%v), want (%d,%v)", count, malformed, tc.count, tc.malformed)
			}
		})
	}
}

func TestParseStatusCopyAndMixedRecords(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		count     int
		malformed bool
	}{
		{"copy", "C  copy.txt\x00source.txt\x00", 1, false},
		{"copy source directory marker remains invalid", "C  copy.txt\x00source/\x00", 0, true},
		{"copy destination directory marker remains invalid", "C  copy/\x00source.txt\x00", 0, true},
		{"missing copy source", "C  copy.txt\x00", 0, true},
		{"mixed valid and malformed", "?? docs/\x00 M tracked/\x00?? file.txt\x00", 2, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			count, malformed := parseStatus([]byte(tc.raw))
			if count != tc.count || malformed != tc.malformed {
				t.Fatalf("got (%d,%v), want (%d,%v)", count, malformed, tc.count, tc.malformed)
			}
		})
	}
}

func TestRunCountsUntrackedDirectoryAsOneRecord(t *testing.T) {
	trustedGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("Git unavailable: %v", err)
	}
	trustedGit, err = filepath.Abs(trustedGit)
	if err != nil {
		t.Fatal(err)
	}
	trustedGit, err = filepath.EvalSymlinks(trustedGit)
	if err != nil {
		t.Fatal(err)
	}

	root, binding := newBinding(t)
	command := exec.Command(trustedGit, "init", "--quiet", root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}

	directoryName := "untracked-nested-private"
	fileName := "independent-private.txt"
	writeFile(t, root, directoryName+"/one.txt", "one")
	writeFile(t, root, directoryName+"/deeper/two.txt", "two")

	state, _, err := Run(context.Background(), Options{Binding: binding, GitExecutable: trustedGit, Now: time.Now})
	if err != nil {
		t.Fatalf("probe directory: %v", err)
	}
	if state.DirtyPathCount != 1 || hasDiagnostic(state.Diagnostics, "git_status_malformed") {
		t.Fatalf("untracked directory was not one valid record: count=%d diagnostics=%+v", state.DirtyPathCount, state.Diagnostics)
	}
	if encoded := fmt.Sprintf("%+v", state); strings.Contains(encoded, directoryName) {
		t.Fatalf("raw directory name escaped into public probe state: %+v", state)
	}

	writeFile(t, root, fileName, "independent")
	state, _, err = Run(context.Background(), Options{Binding: binding, GitExecutable: trustedGit, Now: time.Now})
	if err != nil {
		t.Fatalf("probe directory and file: %v", err)
	}
	if state.DirtyPathCount != 2 || hasDiagnostic(state.Diagnostics, "git_status_malformed") {
		t.Fatalf("independent file did not add one record: count=%d diagnostics=%+v", state.DirtyPathCount, state.Diagnostics)
	}
	if encoded := fmt.Sprintf("%+v", state); strings.Contains(encoded, directoryName) || strings.Contains(encoded, fileName) {
		t.Fatalf("raw status names escaped into public probe state: %+v", state)
	}
}
