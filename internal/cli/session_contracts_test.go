package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSessionOpenContractAcceptsOnlyExactPublicCommands(t *testing.T) {
	dataRoot := t.TempDir()
	digest := "sha256:" + strings.Repeat("a", 64)
	open, err := ParseSessionOpenContract([]string{"open", "--project-id", "project-p", "--provider", "codex", "--session-id", "session-1", "--expected-generation-id", "generation-1", "--expected-session-view-digest", digest, "--data-dir", dataRoot, "--json"})
	if err != nil || open != (SessionOpenRequest{Command: "open", ProjectID: "project-p", Provider: "codex", SessionID: "session-1", ExpectedGenerationID: "generation-1", ExpectedSessionViewDigest: digest, DataDir: dataRoot}) {
		t.Fatalf("open=%+v err=%v", open, err)
	}
	executable := filepath.Join(t.TempDir(), "codex")
	verify, err := ParseSessionOpenContract([]string{"launcher", "verify", "--provider", "claude", "--executable", executable, "--data-dir", dataRoot, "--json"})
	if err != nil || verify != (SessionOpenRequest{Command: "launcher", Subcommand: "verify", Provider: "claude", Executable: executable, DataDir: dataRoot}) {
		t.Fatalf("verify=%+v err=%v", verify, err)
	}
	for _, args := range [][]string{
		{"open", "--project-id", "project-p", "--provider", "unknown", "--session-id", "session-1", "--expected-generation-id", "generation-1", "--expected-session-view-digest", digest, "--json"},
		{"open", "--project-id", "project-p", "--provider", "codex", "--session-id", "session-1", "--expected-generation-id", "generation-1", "--expected-session-view-digest", digest, "--cwd", "/tmp", "--json"},
		{"launcher", "verify", "--provider", "codex", "--executable", "relative", "--json"},
		{"worker", "--launch-token", strings.Repeat("a", 64)},
	} {
		if _, err := ParseSessionOpenContract(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}
