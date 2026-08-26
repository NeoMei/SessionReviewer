package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	applyengine "github.com/neomei/SessionReviewer/internal/apply"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/cursor"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/prepare"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/session"
)

func TestRunRequiresCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run(nil, &out, &errOut)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "Usage: session-reviewer") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"version"}, &out, &errOut)
	if code != 0 || strings.TrimSpace(out.String()) != "dev" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunVersionJSONIncludesBuildIdentity(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"version", "--json"}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, field := range []string{`"version":"dev"`, `"commit":"unknown"`, `"built_at":"unknown"`, `"go_version":"go1.26`} {
		if !strings.Contains(out.String(), field) {
			t.Fatalf("json=%q missing %q", out.String(), field)
		}
	}
}

func TestRunRejectsArgumentsAfterRootHelpOrVersion(t *testing.T) {
	for _, args := range [][]string{{"version", "unexpected"}, {"help", "unexpected"}, {"--help", "unexpected"}} {
		var out, errOut bytes.Buffer
		if code := Run(args, &out, &errOut); code != 2 || out.Len() != 0 || errOut.Len() == 0 {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, out.String(), errOut.String())
		}
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"unknown"}, &out, &errOut)
	if code != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), `unknown command "unknown"`) || !strings.Contains(errOut.String(), "Usage: session-reviewer") {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}

func TestRunHelpListsEveryFoundationCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"help"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut.String())
	}
	for _, text := range []string{"init", "prepare review", "prepare checkpoint", "version", "--sessions-root", "--current-session-id"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("help=%q missing %q", out.String(), text)
		}
	}
}

func TestRunHelpListsLedgerCommandsAndBoundaries(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"help"}, &out, &errOut); code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, text := range []string{
		"apply", "resume", "history", "--proposal", "--evidence", "--ledger-only",
		"validates a Skill proposal", "do not process pending sessions",
	} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("help=%q missing %q", out.String(), text)
		}
	}
}

func TestRunRootHelpAliasesAreCompleteAndUseStdout(t *testing.T) {
	for _, alias := range []string{"help", "-h", "--help"} {
		t.Run(alias, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := Run([]string{alias}, &out, &errOut); code != 0 || errOut.Len() != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			for _, text := range []string{
				"Usage:", "Commands:", "Options:", "Examples:",
				"--project", "--vault", "--data-dir", "--write",
				"--sessions-root", "--cwd", "--session", "--current-session-id", "--output", "--from-start",
			} {
				if !strings.Contains(out.String(), text) {
					t.Fatalf("help=%q missing %q", out.String(), text)
				}
			}
		})
	}
}

func TestRunSubcommandHelpIsCompleteAndUsesStdout(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "init",
			args: []string{"init", "--help"},
			want: []string{"Usage:", "Options:", "Examples:", "init", "--project", "--vault", "--data-dir", "--write"},
		},
		{
			name: "prepare overview",
			args: []string{"prepare", "--help"},
			want: []string{"Usage:", "Modes:", "Examples:", "prepare review", "prepare checkpoint"},
		},
		{
			name: "prepare review",
			args: []string{"prepare", "review", "--help"},
			want: []string{"Usage:", "Options:", "Examples:", "prepare review", "--sessions-root", "--cwd", "--session", "--current-session-id", "--data-dir", "--output", "--from-start"},
		},
		{
			name: "prepare checkpoint",
			args: []string{"prepare", "checkpoint", "-h"},
			want: []string{"Usage:", "Options:", "Examples:", "prepare checkpoint", "--sessions-root", "--cwd", "--session", "--current-session-id", "--data-dir", "--output"},
		},
		{
			name: "apply",
			args: []string{"apply", "--help"},
			want: []string{"Usage:", "Options:", "Examples:", "apply", "--proposal", "--evidence", "--project", "--data-dir", "validates a Skill proposal"},
		},
		{
			name: "resume",
			args: []string{"resume", "help"},
			want: []string{"Usage:", "Options:", "Examples:", "resume", "--ledger-only", "--project", "does not process pending sessions"},
		},
		{
			name: "history",
			args: []string{"history", "-h"},
			want: []string{"Usage:", "Options:", "Examples:", "history", "--ledger-only", "--project", "does not process pending sessions"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := Run(test.args, &out, &errOut); code != 0 || errOut.Len() != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			for _, text := range test.want {
				if !strings.Contains(out.String(), text) {
					t.Fatalf("help=%q missing %q", out.String(), text)
				}
			}
		})
	}
}

func TestRecoveryRequiresLedgerOnly(t *testing.T) {
	for _, command := range []string{"resume", "history"} {
		t.Run(command, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := Run([]string{command, "--project", t.TempDir()}, &out, &errOut)
			if code != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), "--ledger-only") || strings.Contains(errOut.String(), "E_") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
		})
	}
}

func TestLedgerCommandsRejectUnknownFlagsPositionalsAndPromptsAsSyntax(t *testing.T) {
	tests := [][]string{
		{"apply", "--proposal", "proposal.json", "--evidence", "evidence.json", "extra"},
		{"apply", "--unknown"},
		{"resume", "--ledger-only", "continue this project"},
		{"resume", "--ledger-only", "--prompt", "continue"},
		{"history", "--ledger-only", "extra"},
		{"history", "--ledger-only=false"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := Run(args, &out, &errOut)
			if code != 2 || out.Len() != 0 || errOut.Len() == 0 || strings.Contains(errOut.String(), "E_") || strings.Contains(errOut.String(), "recovery:") {
				t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, out.String(), errOut.String())
			}
		})
	}
}

func TestApplyRequiresProposalAndEvidenceBeforeResolvingDefaults(t *testing.T) {
	for _, args := range [][]string{{"apply"}, {"apply", "--proposal", "proposal.json"}, {"apply", "--evidence", "evidence.json"}} {
		var out, errOut bytes.Buffer
		code := Run(args, &out, &errOut)
		if code != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), "requires --proposal and --evidence") || strings.Contains(errOut.String(), "E_") {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, out.String(), errOut.String())
		}
	}
}

func TestApplyReportsDeterministicResult(t *testing.T) {
	fixture := newCLIApplyFixture(t, "")
	var out, errOut bytes.Buffer
	code := Run([]string{
		"apply", "--proposal", fixture.proposal, "--evidence", fixture.evidence,
		"--project", fixture.project, "--data-dir", fixture.data,
	}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	wantPrefix := "project_id: project-1111111111111111\nsession_id: session-1\ncursor_range: 1-2\nchanged_files:\n"
	if !strings.HasPrefix(out.String(), wantPrefix) || !strings.HasSuffix(out.String(), "cursor_advanced: true\nalready_applied: false\n") {
		t.Fatalf("stdout=%q", out.String())
	}
	assertApplyOutputContract(t, out.String())

	first := out.String()
	out.Reset()
	errOut.Reset()
	code = Run([]string{
		"apply", "--proposal", fixture.proposal, "--evidence", fixture.evidence,
		"--project", fixture.project, "--data-dir", fixture.data,
	}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "changed_files: []\n") || !strings.HasSuffix(out.String(), "cursor_advanced: false\nalready_applied: true\n") {
		t.Fatalf("repeat code=%d stdout=%q stderr=%q first=%q", code, out.String(), errOut.String(), first)
	}
	assertApplyOutputContract(t, out.String())
}

func TestLedgerCommandsResolveOnlyImplicitLogicalSymlinkWorkingDirectory(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("logical PWD subprocess coverage is a macOS acceptance test")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	defaultData := filepath.Join(home, ".local", "share", "session-reviewer")
	fixture := newCLIApplyFixture(t, defaultData)
	link := filepath.Join(root, "project-link")
	if err := os.Symlink(fixture.project, link); err != nil {
		t.Fatal(err)
	}
	binary := buildCLIForSubprocess(t)
	environment := append(os.Environ(), "HOME="+home)

	stdout, stderr, code := runCLIThroughLogicalSymlink(t, link, binary, environment,
		"apply", "--proposal", fixture.proposal, "--evidence", fixture.evidence)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "cursor_advanced: true") {
		t.Fatalf("apply code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, command := range []string{"resume", "history"} {
		stdout, stderr, code = runCLIThroughLogicalSymlink(t, link, binary, environment, command, "--ledger-only")
		if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "# ") {
			t.Fatalf("%s code=%d stdout=%q stderr=%q", command, code, stdout, stderr)
		}
	}

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "explicit project symlink", args: []string{"resume", "--ledger-only", "--project", link}},
		{name: "explicit apply project symlink", args: []string{"apply", "--proposal", fixture.proposal, "--evidence", fixture.evidence, "--project", link, "--data-dir", fixture.data}},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(binary, test.args...)
			command.Env = environment
			var out, errOut bytes.Buffer
			command.Stdout, command.Stderr = &out, &errOut
			err := command.Run()
			if exitCode(err) != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "E_") {
				t.Fatalf("args=%v code=%d stdout=%q stderr=%q", test.args, exitCode(err), out.String(), errOut.String())
			}
		})
	}

	dataLink := filepath.Join(root, "data-link")
	if err := os.Symlink(fixture.data, dataLink); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "apply", "--proposal", fixture.proposal, "--evidence", fixture.evidence, "--project", fixture.project, "--data-dir", dataLink)
	command.Env = environment
	var out, errOut bytes.Buffer
	command.Stdout, command.Stderr = &out, &errOut
	err := command.Run()
	if exitCode(err) != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "E_APPLY_FAILED") {
		t.Fatalf("explicit data symlink code=%d stdout=%q stderr=%q", exitCode(err), out.String(), errOut.String())
	}
}

func TestLedgerCommandsRejectProjectReplacementAfterResolution(t *testing.T) {
	for _, test := range []struct {
		command string
		args    func(cliApplyFixture) []string
	}{
		{command: "apply", args: func(fixture cliApplyFixture) []string {
			return []string{"apply", "--proposal", fixture.proposal, "--evidence", fixture.evidence}
		}},
		{command: "resume", args: func(cliApplyFixture) []string { return []string{"resume", "--ledger-only"} }},
		{command: "history", args: func(cliApplyFixture) []string { return []string{"history", "--ledger-only"} }},
	} {
		for _, explicit := range []bool{false, true} {
			mode := "implicit"
			if explicit {
				mode = "explicit"
			}
			t.Run(test.command+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				home := filepath.Join(root, "home")
				fixture := newCLIApplyFixture(t, filepath.Join(home, ".local", "share", "session-reviewer"))
				originalProject := snapshotCLITree(t, fixture.project)
				originalData := snapshotCLITree(t, fixture.data)
				oldWorkingDirectory, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chdir(fixture.project); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chdir(oldWorkingDirectory) })
				setCurrentEnv(t, platform.Env{GOOS: "darwin", Home: home})

				var livePath, moved string
				replaced := false
				t.Cleanup(func() {
					if replaced {
						_ = os.RemoveAll(livePath)
						_ = os.Rename(moved, livePath)
					}
				})
				var replacementBefore string
				projectRootResolvedHook = func(command, path string) error {
					if command != test.command {
						return fmt.Errorf("resolved command/path = %q/%q", command, path)
					}
					if runtime.GOOS == "windows" {
						// Windows does not rename the process working directory.
						// Resolution is already complete, so leave it before replacing
						// the pathname and exercise the same identity check.
						if err := os.Chdir(oldWorkingDirectory); err != nil {
							return err
						}
					}
					livePath = path
					moved = path + "-verified-original"
					if err := os.Rename(livePath, moved); err != nil {
						return err
					}
					replaced = true
					if err := copyCLITreeForTest(moved, livePath); err != nil {
						return err
					}
					replacementBefore = snapshotCLITree(t, livePath)
					return nil
				}
				t.Cleanup(func() { projectRootResolvedHook = nil })

				var out, errOut bytes.Buffer
				args := test.args(fixture)
				if explicit {
					args = append(args, "--project", fixture.project)
				}
				code := Run(args, &out, &errOut)
				if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "E_") {
					t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
				}
				if got := snapshotCLITree(t, livePath); got != replacementBefore {
					t.Fatalf("substituted project was written\nbefore:\n%s\nafter:\n%s\nstderr=%s", replacementBefore, got, errOut.String())
				}
				if got := snapshotCLITree(t, moved); got != originalProject {
					t.Fatal("verified project was written")
				}
				if got := snapshotCLITree(t, fixture.data); got != originalData {
					t.Fatal("data directory was written")
				}
			})
		}
	}
}

func TestRecoveryCommandsPrintAcceptedMarkdownOnlyAndNeverMutateGit(t *testing.T) {
	fixture := newCLIApplyFixture(t, "")
	var out, errOut bytes.Buffer
	if code := Run([]string{"apply", "--proposal", fixture.proposal, "--evidence", fixture.evidence, "--project", fixture.project, "--data-dir", fixture.data}, &out, &errOut); code != 0 {
		t.Fatalf("apply code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	const canary = "CLI-PENDING-RAW-CONTENT-CANARY"
	if err := os.WriteFile(filepath.Join(fixture.project, "pending-evidence.json"), []byte(canary), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, fixture.project, "init")
	gitRun(t, fixture.project, "config", "user.email", "session-reviewer@example.invalid")
	gitRun(t, fixture.project, "config", "user.name", "SessionReviewer Test")
	gitRun(t, fixture.project, "add", ".")
	gitRun(t, fixture.project, "commit", "-m", "fixture")
	beforeHead := gitRun(t, fixture.project, "rev-parse", "HEAD")
	beforeTree := gitRun(t, fixture.project, "write-tree")
	beforeStatus := gitRun(t, fixture.project, "status", "--porcelain=v1", "--untracked-files=all")

	for _, command := range []string{"resume", "history"} {
		out.Reset()
		errOut.Reset()
		code := Run([]string{command, "--ledger-only", "--project", fixture.project}, &out, &errOut)
		if code != 0 || errOut.Len() != 0 || !strings.HasPrefix(out.String(), "# ") || strings.Contains(out.String(), canary) {
			t.Fatalf("%s code=%d stdout=%q stderr=%q", command, code, out.String(), errOut.String())
		}
	}
	if got := gitRun(t, fixture.project, "rev-parse", "HEAD"); got != beforeHead {
		t.Fatalf("HEAD changed: before=%q after=%q", beforeHead, got)
	}
	if got := gitRun(t, fixture.project, "write-tree"); got != beforeTree {
		t.Fatalf("index tree changed: before=%q after=%q", beforeTree, got)
	}
	if got := gitRun(t, fixture.project, "status", "--porcelain=v1", "--untracked-files=all"); got != beforeStatus {
		t.Fatalf("status changed: before=%q after=%q", beforeStatus, got)
	}
}

func TestLedgerOperationalDiagnosticsAreStableAndPrivate(t *testing.T) {
	const canary = "RAW-ERROR-AND-PATH-CANARY"
	tests := []struct {
		action string
		err    error
		code   string
		hint   string
	}{
		{action: "apply", err: fmt.Errorf("%s: %w", canary, cursor.ErrStale), code: "E_APPLY_CURSOR_STALE", hint: "prepare a fresh"},
		{action: "apply", err: fmt.Errorf("%s: %w", canary, applyengine.ErrPendingReceiptConflict), code: "E_APPLY_RECEIPT_CONFLICT", hint: "same --proposal and --evidence"},
		{action: "apply", err: fmt.Errorf("wrapped: %w", &reviewv2.ErrMigrationRequired{ProjectRoot: canary}), code: "E_APPLY_MIGRATION_REQUIRED", hint: "session-reviewer sync --dry-run, then run session-reviewer sync"},
		{action: "apply", err: errors.New(canary), code: "E_APPLY_FAILED", hint: "original --proposal and --evidence"},
		{action: "resume", err: errors.New(canary), code: "E_RECOVERY_FAILED", hint: "accepted Markdown ledger"},
		{action: "history", err: errors.New(canary), code: "E_RECOVERY_FAILED", hint: "accepted Markdown ledger"},
	}
	for _, test := range tests {
		t.Run(test.action+test.code, func(t *testing.T) {
			var out bytes.Buffer
			if code := writeDiagnostic(&out, test.action, test.err); code != 1 {
				t.Fatalf("exit=%d", code)
			}
			if got := out.String(); !strings.Contains(got, test.code) || !strings.Contains(got, test.hint) || strings.Contains(got, canary) {
				t.Fatalf("diagnostic=%q", got)
			}
		})
	}
}

func TestLedgerOperationalFailuresDoNotLeakInputsOrPaths(t *testing.T) {
	root := t.TempDir()
	proposalPath := filepath.Join(root, "PRIVATE-PROPOSAL-PATH.json")
	evidencePath := filepath.Join(root, "PRIVATE-EVIDENCE-PATH.json")
	projectPath := filepath.Join(root, "PRIVATE-PROJECT-PATH")
	dataPath := filepath.Join(root, "PRIVATE-DATA-PATH")
	for _, path := range []string{projectPath, dataPath} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, body := range map[string]string{proposalPath: `{"canary":"PRIVATE-PROPOSAL-CONTENT"}`, evidencePath: `{"canary":"PRIVATE-EVIDENCE-CONTENT"}`} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"apply", "--proposal", proposalPath, "--evidence", evidencePath, "--project", projectPath, "--data-dir", dataPath},
		{"resume", "--ledger-only", "--project", projectPath},
		{"history", "--ledger-only", "--project", projectPath},
	} {
		var out, errOut bytes.Buffer
		code := Run(args, &out, &errOut)
		if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "E_") {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, out.String(), errOut.String())
		}
		for _, canary := range []string{proposalPath, evidencePath, projectPath, dataPath, "PRIVATE-PROPOSAL-CONTENT", "PRIVATE-EVIDENCE-CONTENT"} {
			if strings.Contains(errOut.String(), canary) {
				t.Fatalf("stderr leaked %q: %q", canary, errOut.String())
			}
		}
	}
}

func TestApplyResultOutputIsBoundedBeforeWriting(t *testing.T) {
	result := applyengine.Result{
		ProjectID: "project-1111111111111111",
		SessionID: "session-1",
		ChangedFiles: []string{
			"docs/session-review/decisions/" + strings.Repeat("x", 2<<20) + ".md",
		},
	}
	body, err := formatApplyResult(result)
	if err == nil || body != "" {
		t.Fatalf("body bytes=%d err=%v", len(body), err)
	}
}

func TestRunHelpWordsUsedAsFlagValuesAreNotHelpRequests(t *testing.T) {
	t.Run("init project path", func(t *testing.T) {
		root := t.TempDir()
		projectRoot := filepath.Join(root, "help")
		vaultRoot := filepath.Join(root, "vault")
		dataRoot := filepath.Join(root, "data")
		for _, path := range []string{projectRoot, vaultRoot} {
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		oldWorkingDirectory, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(root); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(oldWorkingDirectory) })

		var out, errOut bytes.Buffer
		code := Run([]string{"init", "--project", "help", "--vault", vaultRoot, "--data-dir", dataRoot}, &out, &errOut)
		if code != 0 || !strings.Contains(out.String(), "action:") || errOut.Len() != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
		}
	})

	t.Run("prepare session id", func(t *testing.T) {
		root := t.TempDir()
		projectRoot := filepath.Join(root, "project")
		dataRoot := filepath.Join(root, "data")
		sessionsRoot := filepath.Join(root, "sessions")
		for _, path := range []string{projectRoot, dataRoot, sessionsRoot} {
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		writeCLISession(t, sessionsRoot, "help", projectRoot)
		writeCLIConfig(t, dataRoot, projectRoot)
		output := filepath.Join(root, "packet.json")
		if got := runPrepareAndReadSessionID(t, []string{
			"--session", "help", "--sessions-root", sessionsRoot, "--cwd", projectRoot,
			"--data-dir", dataRoot, "--output", output,
		}, output); got != "help" {
			t.Fatalf("session_id=%q", got)
		}
	})
}

func TestRunInitRequiresProjectAndVault(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"init"}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "init requires --project and --vault") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunInitRejectsPositionalArgumentsBeforePreviewOrWrite(t *testing.T) {
	for _, test := range []struct {
		name  string
		write bool
	}{
		{name: "preview"},
		{name: "write", write: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectRoot := t.TempDir()
			vaultRoot := t.TempDir()
			dataRoot := filepath.Join(t.TempDir(), "data")
			args := []string{"init", "--project", projectRoot, "--vault", vaultRoot, "--data-dir", dataRoot}
			if test.write {
				args = append(args, "--write")
			}
			args = append(args, "unexpected")

			var out, errOut bytes.Buffer
			code := Run(args, &out, &errOut)
			if code != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), "init does not accept positional arguments") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			for _, root := range []string{projectRoot, vaultRoot} {
				entries, err := os.ReadDir(root)
				if err != nil || len(entries) != 0 {
					t.Fatalf("root=%q entries=%v err=%v", root, entries, err)
				}
			}
			if _, err := os.Stat(dataRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("data root created: %v", err)
			}
		})
	}
}

func TestRunInitPreviewsWithoutWritingUntilWriteFlag(t *testing.T) {
	projectRoot := t.TempDir()
	vaultRoot := t.TempDir()
	dataRoot := filepath.Join(t.TempDir(), "data")
	args := []string{"init", "--project", projectRoot, "--vault", vaultRoot, "--data-dir", dataRoot}
	var out, errOut bytes.Buffer
	if code := Run(args, &out, &errOut); code != 0 || !strings.Contains(out.String(), "action: create") {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "project_id: \n") || !strings.Contains(out.String(), "project_id: (generated on write)") {
		t.Fatalf("create preview has an ambiguous project ID: %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "docs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview wrote docs: %v", err)
	}
	out.Reset()
	errOut.Reset()
	if code := Run(append(args, "--write"), &out, &errOut); code != 0 || !strings.Contains(out.String(), "written: true") {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
}

func TestRunInitFailureDoesNotLeakUnpreviewedPaths(t *testing.T) {
	projectRoot := t.TempDir()
	vaultRoot := filepath.Join(t.TempDir(), "customer-secret-vault")
	ownerRoot := filepath.Join(t.TempDir(), "customer-secret-owner")
	dataRoot := t.TempDir()
	for _, path := range []string{vaultRoot, ownerRoot, filepath.Join(projectRoot, "docs", "session-review")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const projectID = "project-1111111111111111"
	if err := os.WriteFile(filepath.Join(projectRoot, "docs", "session-review", "project-overview.md"), []byte("---\nproject_id: "+projectID+"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: projectID, Root: ownerRoot, VaultRoot: vaultRoot,
	}}}); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"init", "--project", projectRoot, "--vault", vaultRoot, "--data-dir", dataRoot, "--write"}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "E_INIT_IDENTITY_CONFLICT") || !strings.Contains(errOut.String(), "recovery:") || strings.Contains(errOut.String(), ownerRoot) || strings.Contains(errOut.String(), vaultRoot) {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
}

func TestWriteDiagnosticClassifiesInitFailuresWithoutDisclosure(t *testing.T) {
	const canary = "CUSTOMER-PRIVATE-PATH"
	tests := []struct {
		name     string
		err      error
		code     string
		hintPart string
	}{
		{name: "invalid root", err: fmt.Errorf("%s: %w", canary, project.ErrInvalidInitializationRoot), code: "E_INIT_ROOT_INVALID", hintPart: "check --project, --vault, and --data-dir"},
		{name: "nested roots", err: fmt.Errorf("%s: %w", canary, project.ErrNestedInitializationRoots), code: "E_INIT_ROOTS_NESTED", hintPart: "choose separate roots"},
		{name: "corrupt config", err: fmt.Errorf("%s: %w", canary, project.ErrCorruptInitializationConfig), code: "E_INIT_CONFIG_CORRUPT", hintPart: "restore config.toml"},
		{name: "identity conflict", err: fmt.Errorf("%s: %w", canary, project.ErrConflictingInitializationIdentity), code: "E_INIT_IDENTITY_CONFLICT", hintPart: "use the mapped --vault"},
		{name: "state changed", err: fmt.Errorf("%s: %w", canary, project.ErrInitializationStateChanged), code: "E_INIT_STATE_CHANGED", hintPart: "rerun init preview"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := writeDiagnostic(&out, "init", test.err); code != 1 {
				t.Fatalf("code=%d", code)
			}
			got := out.String()
			if !strings.Contains(got, test.code) || !strings.Contains(got, test.hintPart) || strings.Contains(got, canary) {
				t.Fatalf("diagnostic=%q", got)
			}
		})
	}
}

func TestWriteDiagnosticClosedPrepareMappingsDoNotDiscloseCauses(t *testing.T) {
	const canary = "PRIVATE-PATH-SESSION-SOURCE-CANARY"
	tests := []struct {
		name     string
		err      error
		code     string
		hintPart string
	}{
		{name: "session not found", err: fmt.Errorf("%s: %w", canary, prepare.ErrSessionNotFound), code: "E_SESSION_NOT_FOUND", hintPart: "check --session"},
		{name: "session ambiguous", err: fmt.Errorf("%s: %w", canary, prepare.ErrSessionAmbiguous), code: "E_SESSION_AMBIGUOUS", hintPart: "--current-session-id"},
		{name: "project not initialized", err: fmt.Errorf("%s: %w", canary, prepare.ErrProjectNotInitialized), code: "E_PROJECT_NOT_INITIALIZED", hintPart: "session-reviewer init"},
		{name: "unsafe output", err: fmt.Errorf("%s: %w", canary, prepare.ErrUnsafeOutput), code: "E_OUTPUT_UNSAFE", hintPart: "outside session/data roots"},
		{name: "cursor drift", err: fmt.Errorf("%s: %w", canary, prepare.ErrCursorSourceDrift), code: "E_CURSOR_DRIFT", hintPart: "prepare review --from-start"},
		{name: "session segment conflict", err: fmt.Errorf("%s: %w", canary, prepare.ErrSessionSegmentConflict), code: "E_SESSION_SEGMENT_CONFLICT", hintPart: "one project's session segments"},
		{name: "unsupported session format", err: fmt.Errorf("%s: %w", canary, prepare.ErrSessionFormatUnsupported), code: "E_SESSION_FORMAT_UNSUPPORTED", hintPart: "upgrade SessionReviewer"},
		{name: "session discovery limit", err: fmt.Errorf("%s: %w", canary, prepare.ErrSessionDiscoveryLimit), code: "E_SESSION_DISCOVERY_LIMIT", hintPart: "narrow --sessions-root"},
		{name: "unknown", err: errors.New(canary), code: "E_PREPARE_FAILED", hintPart: "session-reviewer help"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := writeDiagnostic(&out, "prepare", test.err); code != 1 {
				t.Fatalf("code=%d", code)
			}
			got := out.String()
			if !strings.Contains(got, test.code) || !strings.Contains(got, test.hintPart) || strings.Contains(got, canary) {
				t.Fatalf("diagnostic=%q", got)
			}
		})
	}
}

func TestRunPrepareRequiresMode(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"prepare"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "requires review or checkpoint") {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}

func TestRunPrepareNotFoundGivesSafeSelectionHint(t *testing.T) {
	base := t.TempDir()
	sessions := filepath.Join(base, "customer-secret-sessions")
	projectRoot := filepath.Join(base, "project")
	dataRoot := filepath.Join(base, "data")
	for _, path := range []string{sessions, projectRoot, dataRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "project-1111111111111111", Root: projectRoot}}}); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run([]string{"prepare", "review", "--session", "missing", "--sessions-root", sessions, "--cwd", projectRoot, "--data-dir", dataRoot, "--output", filepath.Join(t.TempDir(), "packet.json")}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "E_SESSION_NOT_FOUND") || !strings.Contains(errOut.String(), "check --session") || strings.Contains(errOut.String(), sessions) {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}

func TestRunPrepareAmbiguousGivesSafeExplicitSelectionHint(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "customer-secret-sessions")
	projectRoot := filepath.Join(root, "customer-secret-project")
	dataRoot := filepath.Join(root, "data")
	for _, path := range []string{sessions, projectRoot, dataRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"source-canary-one", "source-canary-two"} {
		writeCLISession(t, sessions, id, projectRoot)
		path := filepath.Join(sessions, id+".jsonl")
		if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	writeCLIConfig(t, dataRoot, projectRoot)
	setCurrentEnv(t, platform.Env{})

	var out, errOut bytes.Buffer
	code := Run([]string{"prepare", "review", "--sessions-root", sessions, "--cwd", projectRoot, "--data-dir", dataRoot, "--output", filepath.Join(root, "packet.json")}, &out, &errOut)
	if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "E_SESSION_AMBIGUOUS") || !strings.Contains(errOut.String(), "--session") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, canary := range []string{sessions, projectRoot, "source-canary-one", "source-canary-two"} {
		if strings.Contains(errOut.String(), canary) {
			t.Fatalf("stderr disclosed %q: %q", canary, errOut.String())
		}
	}
}

func TestRunPrepareUninitializedGivesSafeInitHint(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	projectRoot := filepath.Join(root, "customer-secret-project")
	dataRoot := filepath.Join(root, "data")
	for _, path := range []string{sessions, projectRoot, dataRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeCLISession(t, sessions, "source-canary", projectRoot)
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1}); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"prepare", "review", "--session", "source-canary", "--sessions-root", sessions, "--cwd", projectRoot, "--data-dir", dataRoot, "--output", filepath.Join(root, "packet.json")}, &out, &errOut)
	if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "E_PROJECT_NOT_INITIALIZED") || !strings.Contains(errOut.String(), "session-reviewer init") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, canary := range []string{projectRoot, "source-canary"} {
		if strings.Contains(errOut.String(), canary) {
			t.Fatalf("stderr disclosed %q: %q", canary, errOut.String())
		}
	}
}

func TestRunPrepareUnsafeOutputGivesSafePathHint(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "customer-secret-sessions")
	projectRoot := filepath.Join(root, "project")
	dataRoot := filepath.Join(root, "customer-secret-data")
	for _, path := range []string{sessions, projectRoot, dataRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"prepare", "review", "--session", "source-canary", "--sessions-root", sessions, "--cwd", projectRoot, "--data-dir", dataRoot, "--output", filepath.Join(sessions, "packet.json")}, &out, &errOut)
	if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "E_OUTPUT_UNSAFE") || !strings.Contains(errOut.String(), "outside session/data roots") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, canary := range []string{sessions, dataRoot, "source-canary"} {
		if strings.Contains(errOut.String(), canary) {
			t.Fatalf("stderr disclosed %q: %q", canary, errOut.String())
		}
	}
}

func TestRunPrepareUnknownFailureUsesStaticFallback(t *testing.T) {
	root := t.TempDir()
	missingSessions := filepath.Join(root, "customer-secret-missing-sessions")
	projectRoot := filepath.Join(root, "project")
	dataRoot := filepath.Join(root, "data")
	for _, path := range []string{projectRoot, dataRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"prepare", "review", "--session", "source-canary", "--sessions-root", missingSessions, "--cwd", projectRoot, "--data-dir", dataRoot, "--output", filepath.Join(root, "packet.json")}, &out, &errOut)
	if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "E_PREPARE_FAILED") || !strings.Contains(errOut.String(), "session-reviewer help") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, canary := range []string{missingSessions, "source-canary"} {
		if strings.Contains(errOut.String(), canary) {
			t.Fatalf("stderr disclosed %q: %q", canary, errOut.String())
		}
	}
}

func TestRunPrepareRequiresOutput(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"prepare", "review"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "requires --output") {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}

func TestRunCheckpointRejectsFromStart(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"prepare", "checkpoint", "--from-start", "--output", "evidence.json"}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "valid only for review") {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}

func TestRunPrepareSuccessIsSilentAndWritesEvidence(t *testing.T) {
	setCurrentEnv(t, platform.Env{})
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	data := filepath.Join(root, "data")
	project := filepath.Join(root, "project")
	for _, dir := range []string{sessions, data, project} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sessionPath := filepath.Join(sessions, "rollout.jsonl")
	const canary = "CLI-EVENT-CANARY"
	body := `{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"` + filepath.ToSlash(project) + `"}}` + "\n" +
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"` + canary + `"}]}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(sessionPath, now, now); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "p1", Root: project}}}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "out", "packet.json")
	var out, errOut bytes.Buffer
	code := Run([]string{"prepare", "review", "--sessions-root", sessions, "--cwd", project, "--data-dir", data, "--output", output}, &out, &errOut)
	if code != 0 || out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatal(err)
	}
}

func TestRunPrepareSessionRootFlagOverridesEnvironment(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	dataRoot := filepath.Join(root, "data")
	flagSessions := filepath.Join(root, "flag-sessions")
	envSessions := filepath.Join(root, "env-sessions")
	for _, dir := range []string{projectRoot, dataRoot, flagSessions, envSessions} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeCLISession(t, flagSessions, "flag-session", projectRoot)
	writeCLISession(t, envSessions, "env-session", projectRoot)
	writeCLIConfig(t, dataRoot, projectRoot)
	setCurrentEnv(t, platform.Env{GOOS: "darwin", Home: filepath.Join(root, "home"), SessionReviewerSessionsRoot: envSessions})

	output := filepath.Join(root, "packet.json")
	got := runPrepareAndReadSessionID(t, []string{
		"--session", "flag-session",
		"--sessions-root", flagSessions,
		"--cwd", projectRoot,
		"--data-dir", dataRoot,
		"--output", output,
	}, output)
	if got != "flag-session" {
		t.Fatalf("session_id=%q", got)
	}
}

func TestRunPrepareCurrentSessionIDPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		ids       []string
		args      []string
		threadID  string
		sessionID string
		want      string
	}{
		{
			name:     "session flag overrides every current-session input",
			ids:      []string{"explicit", "current", "thread", "environment"},
			args:     []string{"--session", "explicit", "--current-session-id", "current"},
			threadID: "thread", sessionID: "environment", want: "explicit",
		},
		{
			name:     "current-session flag overrides environment",
			ids:      []string{"current", "thread", "environment"},
			args:     []string{"--current-session-id", "current"},
			threadID: "thread", sessionID: "environment", want: "current",
		},
		{
			name:     "thread environment overrides session environment",
			ids:      []string{"thread", "environment"},
			threadID: "thread", sessionID: "environment", want: "thread",
		},
		{
			name:      "session environment is used after thread environment",
			ids:       []string{"environment"},
			sessionID: "environment", want: "environment",
		},
		{
			name: "cwd and time fallback remains available",
			ids:  []string{"fallback"},
			want: "fallback",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			projectRoot := filepath.Join(root, "project")
			dataRoot := filepath.Join(root, "data")
			sessionsRoot := filepath.Join(root, "sessions")
			for _, dir := range []string{projectRoot, dataRoot, sessionsRoot} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range test.ids {
				writeCLISession(t, sessionsRoot, id, projectRoot)
			}
			writeCLIConfig(t, dataRoot, projectRoot)
			setCurrentEnv(t, platform.Env{
				GOOS:                        "darwin",
				Home:                        filepath.Join(root, "home"),
				SessionReviewerSessionsRoot: sessionsRoot,
				CodexThreadID:               test.threadID,
				CodexSessionID:              test.sessionID,
			})

			output := filepath.Join(root, "packet.json")
			args := append([]string{}, test.args...)
			args = append(args,
				"--cwd", projectRoot,
				"--data-dir", dataRoot,
				"--output", output,
			)
			if got := runPrepareAndReadSessionID(t, args, output); got != test.want {
				t.Fatalf("session_id=%q want=%q", got, test.want)
			}
		})
	}
}

func setCurrentEnv(t *testing.T, env platform.Env) {
	t.Helper()
	original := currentEnv
	currentEnv = func() platform.Env { return env }
	t.Cleanup(func() { currentEnv = original })
}

func writeCLISession(t *testing.T, sessionsRoot, id, projectRoot string) {
	t.Helper()
	body := `{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"` + id + `","cwd":"` + filepath.ToSlash(projectRoot) + `"}}` + "\n" +
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"message-` + id + `","role":"user","content":[{"type":"input_text","text":"safe"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionsRoot, id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCLIConfig(t *testing.T, dataRoot, projectRoot string) {
	t.Helper()
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "p1", Root: projectRoot}}}); err != nil {
		t.Fatal(err)
	}
}

func runPrepareAndReadSessionID(t *testing.T, args []string, output string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	args = append([]string{"prepare", "review"}, args...)
	if code := Run(args, &out, &errOut); code != 0 || out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var packet evidence.Packet
	if err := json.Unmarshal(contents, &packet); err != nil {
		t.Fatal(err)
	}
	return packet.SessionID
}

func TestRunPrepareFailureDoesNotPrintSessionContent(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	data := filepath.Join(root, "data")
	project := filepath.Join(root, "project")
	for _, dir := range []string{sessions, data, project} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const canary = "CLI-FAILURE-CANARY"
	body := `{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"` + filepath.ToSlash(project) + `"}}` + "\n" +
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","role":"user","content":"` + canary + `"}}` + "\n"
	path := filepath.Join(sessions, "rollout.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "p1", Root: project}}}); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run([]string{"prepare", "review", "--session", "s1", "--sessions-root", sessions, "--cwd", project, "--data-dir", data, "--output", filepath.Join(root, "packet.json")}, &out, &errOut)
	if code != 1 || out.Len() != 0 || strings.Contains(errOut.String(), canary) || strings.Contains(errOut.String(), project) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunPrepareCursorDriftPrintsOnlySafeRecoveryGuidance(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	data := filepath.Join(root, "data")
	project := filepath.Join(root, "secret-project-path")
	for _, dir := range []string{sessions, data, project} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(sessions, "rollout.jsonl")
	body := `{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"` + filepath.ToSlash(project) + `"}}` + "\n" +
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"SECRET-CONTENT"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var hash string
	if _, err := session.Stream(path, session.DecodeOptions{FromLine: 2}, func(record session.Record) error {
		if record.Line == 2 {
			hash = record.SourceHash
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "p1", Root: project}}}); err != nil {
		t.Fatal(err)
	}
	projectData := filepath.Join(data, "projects", "p1")
	if err := os.MkdirAll(projectData, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (cursor.Store{Root: projectData}).Commit("s1", cursor.Cursor{}, cursor.Cursor{SessionID: "s1", LastLine: 2, LastHash: hash, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	body = strings.Replace(body, "SECRET-CONTENT", "CHANGED-SECRET-CONTENT", 1)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run([]string{"prepare", "checkpoint", "--session", "s1", "--sessions-root", sessions, "--cwd", project, "--data-dir", data, "--output", filepath.Join(root, "packet.json")}, &out, &errOut)
	if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "prepare review --from-start") || strings.Contains(errOut.String(), "SECRET") || strings.Contains(errOut.String(), project) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunPrepareRejectsInvalidModeAndExtraArguments(t *testing.T) {
	for _, args := range [][]string{{"prepare", "unknown"}, {"prepare", "review", "--output", "packet.json", "extra"}} {
		var out, errOut bytes.Buffer
		if code := Run(args, &out, &errOut); code != 2 || out.Len() != 0 {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, out.String(), errOut.String())
		}
	}
}

type cliApplyFixture struct {
	project  string
	data     string
	proposal string
	evidence string
}

func newCLIApplyFixture(t *testing.T, dataDir string) cliApplyFixture {
	t.Helper()
	projectRoot := t.TempDir()
	vaultRoot := t.TempDir()
	if dataDir == "" {
		dataDir = t.TempDir()
	} else if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := project.Initialize(project.InitOptions{
		ProjectRoot: projectRoot,
		VaultRoot:   vaultRoot,
		DataDir:     dataDir,
		Now:         func() time.Time { return time.Date(2026, 8, 23, 2, 0, 0, 0, time.UTC) },
		Random:      bytes.NewReader(bytes.Repeat([]byte{0x11}, 16)),
	}); err != nil {
		t.Fatal(err)
	}

	proposalBody, err := os.ReadFile(filepath.Join("..", "..", "testdata", "proposals", "valid-first.json"))
	if err != nil {
		t.Fatal(err)
	}
	packet := evidence.Packet{
		SchemaVersion: 2,
		ProjectID:     "project-1111111111111111",
		SessionID:     "session-1",
		CWD:           "/repo",
		FromCursor:    1,
		ToCursor:      2,
		ExpectedCursor: evidence.CursorBoundary{
			Line: 0,
		},
		NextCursor: evidence.CursorBoundary{
			Line:       2,
			SourceHash: strings.Repeat("b", 64),
		},
		Events: []evidence.Item{
			{ID: "ev-message", Timestamp: "2026-08-23T01:02:03Z", JSONLLine: 1, SourceHash: strings.Repeat("a", 64), Kind: "message", Role: "user", Summary: "Choose durable ledger"},
			{ID: "ev-verify", Timestamp: "2026-08-23T01:03:03Z", JSONLLine: 2, SourceHash: strings.Repeat("b", 64), Kind: "tool_result", ToolName: "exec_command", Summary: "go test passed"},
		},
	}
	packetDigest, err := evidence.Digest(packet)
	if err != nil {
		t.Fatal(err)
	}
	proposalBody = bytes.Replace(
		proposalBody,
		[]byte("sha256:8bdbc9254ac37b3ea000f15910bd142068a0e991cd6ecafee482cbfd9ba9a4a4"),
		[]byte(packetDigest),
		1,
	)
	convertCLIApplyFixtureToV2(t, projectRoot)
	proposalBody = bytes.Replace(proposalBody, []byte(`"expected_revision": 0`), []byte(`"expected_revision": 1`), 1)
	proposalBody = bytes.Replace(proposalBody, []byte(`"blockers": []`), []byte(`"blockers": ["Fixture risk"]`), 1)
	proposalPath := filepath.Join(t.TempDir(), "proposal.json")
	if err := os.WriteFile(proposalPath, proposalBody, 0o600); err != nil {
		t.Fatal(err)
	}
	evidenceBody, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	evidencePath := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(evidencePath, append(evidenceBody, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return cliApplyFixture{project: projectRoot, data: dataDir, proposal: proposalPath, evidence: evidencePath}
}

func convertCLIApplyFixtureToV2(t *testing.T, projectRoot string) {
	t.Helper()
	legacy, err := ledger.Load(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	legacy.CurrentState = ledger.CurrentState{
		ProjectID: legacy.ProjectID, Revision: 1, Goal: "Fixture seed goal",
		LastVerified: "Fixture seed verification", Branch: "fixture-seed",
		Blockers: []string{"Fixture risk"}, NextAction: "Apply the first proposal",
	}
	state, err := reviewv2.ProjectLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reviewv2.Render(projectRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range plan.Files {
		full := filepath.Join(projectRoot, filepath.FromSlash(file.RelativePath))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, file.Data, file.Perm); err != nil {
			t.Fatal(err)
		}
	}
	for _, relative := range []string{
		"docs/session-review/project-overview.md", "docs/session-review/current-state.md",
		"docs/session-review/evolution-timeline.md", "docs/session-review/decisions",
		"docs/session-review/open-loops", "docs/session-review/sessions",
	} {
		if err := os.RemoveAll(filepath.Join(projectRoot, filepath.FromSlash(relative))); err != nil {
			t.Fatal(err)
		}
	}
}

func assertApplyOutputContract(t *testing.T, output string) {
	t.Helper()
	allowed := map[string]bool{
		"project_id": true, "session_id": true, "cursor_range": true, "changed_files": true,
		"cursor_advanced": true, "already_applied": true,
	}
	var changed []string
	inChanged := false
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if strings.HasPrefix(line, "  - ") {
			if !inChanged {
				t.Fatalf("list item outside changed_files: %q", line)
			}
			path := strings.TrimPrefix(line, "  - ")
			if filepath.IsAbs(path) || strings.Contains(path, "..") {
				t.Fatalf("unsafe changed path in output: %q", path)
			}
			changed = append(changed, path)
			continue
		}
		name, _, found := strings.Cut(line, ":")
		if !found || !allowed[name] {
			t.Fatalf("unexpected apply output line %q", line)
		}
		inChanged = name == "changed_files" && line == "changed_files:"
	}
	for index := 1; index < len(changed); index++ {
		if changed[index-1] > changed[index] {
			t.Fatalf("changed paths are not sorted: %v", changed)
		}
	}
	for _, forbidden := range []string{"summarized", "rationale", "evidence_id", "source_hash", "proposal", "PRIVATE"} {
		if strings.Contains(strings.ToLower(output), strings.ToLower(forbidden)) {
			t.Fatalf("apply output disclosed %q: %q", forbidden, output)
		}
	}
}

func gitRun(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	body, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, body)
	}
	return strings.TrimSpace(string(body))
}

func buildCLIForSubprocess(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "session-reviewer")
	command := exec.Command("go", "build", "-o", binary, "./cmd/session-reviewer")
	command.Dir = filepath.Clean(filepath.Join("..", ".."))
	if body, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build CLI subprocess fixture: %v\n%s", err, body)
	}
	return binary
}

func runCLIThroughLogicalSymlink(t *testing.T, link, binary string, environment []string, args ...string) (string, string, int) {
	t.Helper()
	shellArgs := []string{"-c", `cd "$1" || exit 99; export PWD="$1"; shift; exec "$@"`, "session-reviewer-subprocess", link, binary}
	shellArgs = append(shellArgs, args...)
	command := exec.Command("/bin/sh", shellArgs...)
	command.Env = environment
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return stdout.String(), stderr.String(), exitCode(err)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func copyCLITreeForTest(source, target string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if info.IsDir() {
			return os.MkdirAll(destination, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected fixture entry %q", relative)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, body, info.Mode().Perm())
	})
}

func snapshotCLITree(t *testing.T, root string) string {
	t.Helper()
	var snapshot strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&snapshot, "%s %s", filepath.ToSlash(relative), info.Mode())
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(body)
			fmt.Fprintf(&snapshot, " %x", digest)
		}
		snapshot.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.String()
}
