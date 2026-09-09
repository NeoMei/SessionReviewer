//go:build !windows

package sessionlaunch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/pathguard"
)

func TestSanitizedEnvironmentRemovesSessionReviewerSourceContext(t *testing.T) {
	got := sanitizedEnvironment([]string{"PATH=/bin", "HOME=/tmp/home", "CODEX_THREAD_ID=secret", "CODEX_SESSION_ID=secret-2", "SESSION_REVIEWER_SESSIONS_ROOT=/private/source"})
	if strings.Join(got, "\n") != "PATH=/bin\nHOME=/tmp/home" {
		t.Fatalf("environment=%q", got)
	}
}

func TestExecuteRejectsNonInteractiveWorker(t *testing.T) {
	if isInteractiveTerminal() {
		t.Skip("test runner already has a TTY")
	}
	identity := pathguard.IdentityToken{Kind: "posix-dev-inode", Volume: "1", File: "1"}
	plan := ExecPlan{Executable: "/bin/sh", ExecutableIdentity: identity, ExecutableSHA256: strings.Repeat("a", 64), Arguments: []string{"--version"}, WorkingDirectory: "/tmp", ProjectIdentity: identity}
	if err := Execute(plan); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("err=%v", err)
	}
}

func TestExecuteAuthenticatedPlanUsesExactDirectoryArgumentsAndSanitizedEnvironment(t *testing.T) {
	project, executable, output := filepath.Join(t.TempDir(), "project"), filepath.Join(t.TempDir(), "provider"), filepath.Join(t.TempDir(), "output")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nif [ -t 0 ]; then tty_state=tty; else tty_state=no-tty; fi\nprintf '%s\\n%s\\n%s\\n%s\\n' \"$PWD\" \"$*\" \"${CODEX_THREAD_ID-unset}\" \"$tty_state\" > \"$SESSION_LAUNCH_TEST_OUTPUT\"\n"
	if err := os.WriteFile(executable, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/usr/bin/script", "-q", "/dev/null", os.Args[0], "-test.run=TestExecuteAuthenticatedPlanHelper")
	command.Env = append(os.Environ(), "SESSION_LAUNCH_HELPER=1", "SESSION_LAUNCH_TEST_PROJECT="+project, "SESSION_LAUNCH_TEST_EXECUTABLE="+executable, "SESSION_LAUNCH_TEST_OUTPUT="+output, "CODEX_THREAD_ID=must-not-leak")
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v %s", err, combined)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	physicalProject, _ := filepath.EvalSymlinks(project)
	if string(got) != physicalProject+"\nresume session-fixture\nunset\ntty\n" {
		t.Fatalf("output=%q", got)
	}
}

func TestExecuteAuthenticatedPlanHelper(t *testing.T) {
	if os.Getenv("SESSION_LAUNCH_HELPER") != "1" {
		return
	}
	project, _ := filepath.EvalSymlinks(os.Getenv("SESSION_LAUNCH_TEST_PROJECT"))
	executable, _ := filepath.EvalSymlinks(os.Getenv("SESSION_LAUNCH_TEST_EXECUTABLE"))
	configuration, err := MeasureConfiguration("codex", "fixture", executable)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(project)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := pathguard.PhysicalFileIdentity(directory)
	_ = directory.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(ExecPlan{Executable: configuration.Executable, ExecutableIdentity: configuration.Identity, ExecutableSHA256: configuration.SHA256, Arguments: []string{"resume", "session-fixture"}, WorkingDirectory: project, ProjectIdentity: identity}); err != nil {
		t.Fatal(err)
	}
}
