//go:build windows

package codex

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalPathEqualAcceptsWindowsShortPathAlias(t *testing.T) {
	shortPath := t.TempDir()
	longPath, err := filepath.EvalSymlinks(shortPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(filepath.Clean(shortPath), filepath.Clean(longPath)) {
		t.Skip("temporary directory has no Windows short-path alias")
	}
	if !canonicalPathEqual(shortPath, longPath) {
		t.Fatalf("short path %q and long path %q must identify the same canonical path", shortPath, longPath)
	}
}
