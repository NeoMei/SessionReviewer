package sessionreviewer_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestSchemaCopyMatches(t *testing.T) {
	want, err := os.ReadFile("../../../schemas/proposal-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../references/proposal-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		t.Fatal("packaged proposal schema differs from the authoritative schema")
	}
}

func TestSkillIsConciseDiscoverableAndRoutesToSchema(t *testing.T) {
	body, err := os.ReadFile("../SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 8*1024 {
		t.Fatalf("SKILL.md is not concise: %d bytes", len(body))
	}
	text := string(body)
	if !strings.HasPrefix(text, "---\nname: session-reviewer\ndescription: ") {
		t.Fatal("skill must have lowercase hyphenated name and a discriminating description")
	}
	if strings.Contains(text, "allow_implicit_invocation: false") {
		t.Fatal("automatic discovery must remain enabled")
	}
	if _, err := os.Stat("../agents/openai.yaml"); !os.IsNotExist(err) {
		t.Fatalf("unexpected UI metadata: %v", err)
	}
	for _, required := range []string{
		"review", "checkpoint", "resume", "resume --ledger-only", "private temporary directory",
		"one bounded packet", "accepted ledger entities", "references/proposal-v1.schema.json",
		"references/apply-invariants.md", "canonical project root", "--cwd", "--project",
		"evidence_packet_sha256", "expected_cursor", "next_cursor", "has_more",
		"Never edit ledger files directly", "Never read raw JSONL", "Never interpret hidden reasoning",
		"Never run Git mutation commands", "Never call an API client", "apply-proposal",
		"compare-and-swap", "Stop on any failure", "Do not claim acceptance", "entities", "cursor",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("SKILL.md missing contract %q", required)
		}
	}
	for _, forbidden := range []string{"TODO", "PLACEHOLDER", "README.md"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("SKILL.md contains unfinished or unnecessary content %q", forbidden)
		}
	}
}

func TestReviewFromStartForwardFlowPinsOneProjectRoot(t *testing.T) {
	body, err := os.ReadFile("../SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(body))
	for _, required := range []string{
		"Only the first prepared packet may include `--from-start`",
		"Every later packet must omit `--from-start`",
		"same canonical project root",
		"For every successor packet",
		"`expected_cursor` must equal the prior packet's `next_cursor`",
		"same packet repeats, stop",
		"resume with pending evidence uses review mode",
		"Always read the accepted current state",
		"copy its `summary` byte-for-byte",
	} {
		if !strings.Contains(text, strings.ToLower(required)) {
			t.Errorf("SKILL.md missing forward-flow rule %q", required)
		}
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX forward-flow runtime test")
	}

	stubDir := installStub(t)
	path := stubDir + string(os.PathListSeparator) + os.Getenv("PATH")
	root := "/project root/[literal]*;$HOME"
	steps := []struct {
		script string
		args   []string
		want   []string
	}{
		{
			script: "prepare-workflow.sh",
			args:   []string{"review", "packet one.json", "--cwd", root, "--from-start"},
			want:   []string{"prepare", "review", "--output", "packet one.json", "--cwd", root, "--from-start"},
		},
		{
			script: "apply-proposal.sh",
			args:   []string{"proposal one.json", "packet one.json", "--project", root},
			want:   []string{"apply", "--proposal", "proposal one.json", "--evidence", "packet one.json", "--project", root},
		},
		{
			script: "prepare-workflow.sh",
			args:   []string{"review", "packet two.json", "--cwd", root},
			want:   []string{"prepare", "review", "--output", "packet two.json", "--cwd", root},
		},
		{
			script: "apply-proposal.sh",
			args:   []string{"proposal two.json", "packet two.json", "--project", root},
			want:   []string{"apply", "--proposal", "proposal two.json", "--evidence", "packet two.json", "--project", root},
		},
	}
	for index, step := range steps {
		capture := filepath.Join(t.TempDir(), "args.json")
		cmd := exec.Command("sh", append([]string{filepath.Join("../scripts", step.script)}, step.args...)...)
		cmd.Env = append(os.Environ(), "PATH="+path, "SESSION_REVIEWER_TEST_CAPTURE="+capture, "SESSION_REVIEWER_TEST_EXIT=0")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("step %d failed: %v\n%s", index+1, err, output)
		}
		assertCapturedArgs(t, capture, step.want)
	}
}

func TestApplyInvariantReferenceIsProgressiveAndComplete(t *testing.T) {
	skill, err := os.ReadFile("../SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(skill, []byte("[references/apply-invariants.md](references/apply-invariants.md)")) {
		t.Fatal("SKILL.md does not route proposal synthesis to the semantic invariant reference")
	}
	body, err := os.ReadFile("../references/apply-invariants.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 12*1024 {
		t.Fatalf("semantic invariant reference is not concise: %d bytes", len(body))
	}
	text := strings.ToLower(string(body))
	for _, required := range []string{
		"exact packet tuple", "summary must equal", "every changed entity", "current-packet evidence",
		"exactly one `evidence_link`", "initial revision `1`", "expected_revision",
		"proposed -> accepted|archived", "accepted -> superseded|archived",
		"open <-> blocked", "open|blocked -> resolved|abandoned", "resolved|abandoned -> archived",
		"`current-state`", "source_sessions", "current session", "no-op",
		"decisions_added", "decisions_revised", "open_loops_created", "open_loops_closed",
		"sorted exact packet effects", "report and phase evidence", "inference", "verifies",
		"supersedes", "cycle", "redaction",
	} {
		if !strings.Contains(text, strings.ToLower(required)) {
			t.Errorf("apply invariant reference missing %q", required)
		}
	}
}

func TestTask9ReportDescribesActualSkillPackage(t *testing.T) {
	body, err := os.ReadFile("../../../.superpowers/sdd/task-9-report.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, required := range []string{
		"Task 9 report: packaged semantic review workflows", "27d1ac1..HEAD", "099db5b",
		"RED", "GREEN", "go test -count=1 ./...", "go test -race -count=1 ./...", "go vet ./...",
		"quick_validate.py", "PowerShell", "Windows amd64 cross", "macOS",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("Task 9 report missing %q", required)
		}
	}
	if strings.Contains(text, "Implemented `prepare.Run`") {
		t.Fatal("Task 9 report still describes the old foundation prepare task")
	}
}

func TestWrappersContainNoDataAccessNetworkOrGitCommands(t *testing.T) {
	for _, name := range []string{
		"prepare-workflow.sh", "apply-proposal.sh",
		"prepare-workflow.ps1", "apply-proposal.ps1",
	} {
		body, err := os.ReadFile(filepath.Join("../scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(body))
		for _, forbidden := range []string{
			"curl ", "wget ", "git ", "cat ", "jq ", "get-content", "invoke-webrequest",
			"invoke-restmethod", "http://", "https://", "openai", "jsonl",
		} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("%s contains forbidden capability %q", name, forbidden)
			}
		}
	}
}

func TestPowerShellWrappersPinApplicationAndImmediateExitStatus(t *testing.T) {
	for _, name := range []string{"prepare-workflow.ps1", "apply-proposal.ps1"} {
		body, err := os.ReadFile(filepath.Join("../scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, required := range []string{
			`Get-Command -Name "session-reviewer" -CommandType Application -ErrorAction Stop`,
			`& $Application.Path`,
			"$ExitCode = $LASTEXITCODE",
			"application executable not found",
			"application executable failed to start",
			"exit 127",
			"exit 126",
		} {
			if !strings.Contains(text, required) {
				t.Errorf("%s missing hardened execution contract %q", name, required)
			}
		}
		invoke := strings.Index(text, "& $Application.Path")
		capture := strings.Index(text, "$ExitCode = $LASTEXITCODE")
		if invoke < 0 || capture < invoke || strings.Contains(text[invoke:capture], "Write") {
			t.Errorf("%s does not capture native status immediately after invocation", name)
		}
		if strings.Contains(text, "& session-reviewer ") {
			t.Errorf("%s invokes an unpinned command name", name)
		}
	}
}

func TestPOSIXWrappersPreserveArgumentsAndExitCodes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell test")
	}
	stubDir := installStub(t)
	path := stubDir + string(os.PathListSeparator) + os.Getenv("PATH")
	cases := []struct {
		name   string
		script string
		args   []string
		want   []string
	}{
		{
			name:   "prepare",
			script: "prepare-workflow.sh",
			args:   []string{"review", "packet path;$(literal).json", "--session", "id with spaces", "--cwd", "[project]*;$HOME"},
			want:   []string{"prepare", "review", "--output", "packet path;$(literal).json", "--session", "id with spaces", "--cwd", "[project]*;$HOME"},
		},
		{
			name:   "apply",
			script: "apply-proposal.sh",
			args:   []string{"proposal path;$(literal).json", "evidence [one]*.json", "--project", "project path & value", "--data-dir", "$DATA;literal"},
			want:   []string{"apply", "--proposal", "proposal path;$(literal).json", "--evidence", "evidence [one]*.json", "--project", "project path & value", "--data-dir", "$DATA;literal"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "args.json")
			cmd := exec.Command("sh", append([]string{filepath.Join("../scripts", tc.script)}, tc.args...)...)
			cmd.Env = append(os.Environ(), "PATH="+path, "SESSION_REVIEWER_TEST_CAPTURE="+capture, "SESSION_REVIEWER_TEST_EXIT=37")
			err := cmd.Run()
			var exitErr *exec.ExitError
			if err == nil || !errorAs(err, &exitErr) || exitErr.ExitCode() != 37 {
				t.Fatalf("exit code was not preserved: %v", err)
			}
			assertCapturedArgs(t, capture, tc.want)
		})
	}
}

func TestPOSIXPrepareWrapperRejectsBadModeAndArity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell test")
	}
	for _, args := range [][]string{{}, {"review"}, {"resume", "packet.json"}} {
		cmd := exec.Command("sh", append([]string{"../scripts/prepare-workflow.sh"}, args...)...)
		if err := cmd.Run(); exitCode(err) != 2 {
			t.Fatalf("args=%q exit=%d err=%v", args, exitCode(err), err)
		}
	}
	for _, args := range [][]string{{}, {"proposal.json"}} {
		cmd := exec.Command("sh", append([]string{"../scripts/apply-proposal.sh"}, args...)...)
		if err := cmd.Run(); exitCode(err) != 2 {
			t.Fatalf("args=%q exit=%d err=%v", args, exitCode(err), err)
		}
	}
}

func TestPowerShellWrappersPreserveArgumentsAndExitCodesWhenAvailable(t *testing.T) {
	pwsh := findPowerShell()
	if pwsh == "" {
		t.Skip("PowerShell is not installed")
	}
	stubDir := installStub(t)
	path := stubDir + string(os.PathListSeparator) + os.Getenv("PATH")
	cases := []struct {
		script string
		args   []string
		want   []string
	}{
		{
			script: "prepare-workflow.ps1",
			args:   []string{"checkpoint", "packet path;$(literal).json", "--session", "id with spaces", "--cwd", "[project]*;$HOME"},
			want:   []string{"prepare", "checkpoint", "--output", "packet path;$(literal).json", "--session", "id with spaces", "--cwd", "[project]*;$HOME"},
		},
		{
			script: "apply-proposal.ps1",
			args:   []string{"proposal path;$(literal).json", "evidence [one]*.json", "--project", "project path & value"},
			want:   []string{"apply", "--proposal", "proposal path;$(literal).json", "--evidence", "evidence [one]*.json", "--project", "project path & value"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.script, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "args.json")
			args := append([]string{"-NoLogo", "-NoProfile", "-NonInteractive", "-File", filepath.Join("../scripts", tc.script)}, tc.args...)
			cmd := exec.Command(pwsh, args...)
			cmd.Env = append(os.Environ(), "PATH="+path, "SESSION_REVIEWER_TEST_CAPTURE="+capture, "SESSION_REVIEWER_TEST_EXIT=41")
			err := cmd.Run()
			if exitCode(err) != 41 {
				t.Fatalf("exit code was not preserved: %v", err)
			}
			assertCapturedArgs(t, capture, tc.want)
		})
	}
}

func TestPowerShellWrappersParseWhenAvailable(t *testing.T) {
	pwsh := findPowerShell()
	if pwsh == "" {
		t.Skip("PowerShell is not installed")
	}
	for _, name := range []string{"prepare-workflow.ps1", "apply-proposal.ps1"} {
		absolute, err := filepath.Abs(filepath.Join("../scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		command := fmt.Sprintf("$e=$null; [System.Management.Automation.Language.Parser]::ParseFile(%s,[ref]$null,[ref]$e) > $null; if ($e.Count) { $e | Out-String | Write-Error; exit 1 }", powerShellLiteral(absolute))
		if output, err := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command).CombinedOutput(); err != nil {
			t.Fatalf("%s does not parse: %v\n%s", name, err, output)
		}
	}
}

func TestPowerShellWrappersRejectBadModeAndArityWhenAvailable(t *testing.T) {
	pwsh := findPowerShell()
	if pwsh == "" {
		t.Skip("PowerShell is not installed")
	}
	tests := []struct {
		script string
		args   []string
	}{
		{script: "prepare-workflow.ps1"},
		{script: "prepare-workflow.ps1", args: []string{"review"}},
		{script: "prepare-workflow.ps1", args: []string{"resume", "packet.json"}},
		{script: "apply-proposal.ps1"},
		{script: "apply-proposal.ps1", args: []string{"proposal.json"}},
	}
	for _, test := range tests {
		args := append([]string{"-NoLogo", "-NoProfile", "-NonInteractive", "-File", filepath.Join("../scripts", test.script)}, test.args...)
		if err := exec.Command(pwsh, args...).Run(); err == nil {
			t.Fatalf("%s accepted invalid arguments %q", test.script, test.args)
		}
	}
}

func TestPowerShellWrappersRejectMissingAndNonApplicationShadowWhenAvailable(t *testing.T) {
	pwsh := findPowerShell()
	if pwsh == "" {
		t.Skip("PowerShell is not installed")
	}
	tests := []struct {
		script string
		args   []string
	}{
		{script: "prepare-workflow.ps1", args: []string{"review", "packet.json"}},
		{script: "apply-proposal.ps1", args: []string{"proposal.json", "packet.json"}},
	}
	for _, test := range tests {
		t.Run(test.script+"/missing_stale_status", func(t *testing.T) {
			emptyPath := t.TempDir()
			command := "$global:LASTEXITCODE = 73; & " + powerShellLiteral(filepath.Join("../scripts", test.script))
			for _, arg := range test.args {
				command += " " + powerShellLiteral(arg)
			}
			cmd := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
			cmd.Env = append(os.Environ(), "PATH="+emptyPath)
			output, err := cmd.CombinedOutput()
			if exitCode(err) != 127 || !bytes.Contains(output, []byte("session-reviewer application executable not found")) {
				t.Fatalf("exit=%d err=%v output=%s", exitCode(err), err, output)
			}
		})

		t.Run(test.script+"/external_script_shadow", func(t *testing.T) {
			shadowDir := t.TempDir()
			canary := filepath.Join(shadowDir, "shadow-ran")
			shadow := "[IO.File]::WriteAllText(" + powerShellLiteral(canary) + ", 'shadow')\nexit 99\n"
			if err := os.WriteFile(filepath.Join(shadowDir, "session-reviewer.ps1"), []byte(shadow), 0o600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"-NoLogo", "-NoProfile", "-NonInteractive", "-File", filepath.Join("../scripts", test.script)}, test.args...)
			cmd := exec.Command(pwsh, args...)
			cmd.Env = append(os.Environ(), "PATH="+shadowDir)
			output, err := cmd.CombinedOutput()
			if exitCode(err) != 127 || !bytes.Contains(output, []byte("session-reviewer application executable not found")) {
				t.Fatalf("exit=%d err=%v output=%s", exitCode(err), err, output)
			}
			if _, err := os.Stat(canary); !os.IsNotExist(err) {
				t.Fatalf("non-Application shadow executed: %v", err)
			}
		})
	}
}

func TestPowerShellWrappersBypassFunctionShadowAndCaptureImmediateExitWhenAvailable(t *testing.T) {
	pwsh := findPowerShell()
	if pwsh == "" {
		t.Skip("PowerShell is not installed")
	}
	stubDir := installStub(t)
	path := stubDir + string(os.PathListSeparator) + os.Getenv("PATH")
	tests := []struct {
		script string
		args   []string
		want   []string
	}{
		{
			script: "prepare-workflow.ps1",
			args:   []string{"checkpoint", "packet.json", "--cwd", "project root"},
			want:   []string{"prepare", "checkpoint", "--output", "packet.json", "--cwd", "project root"},
		},
		{
			script: "apply-proposal.ps1",
			args:   []string{"proposal.json", "packet.json", "--project", "project root"},
			want:   []string{"apply", "--proposal", "proposal.json", "--evidence", "packet.json", "--project", "project root"},
		},
	}
	for _, test := range tests {
		t.Run(test.script, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "args.json")
			canary := filepath.Join(t.TempDir(), "function-shadow-ran")
			command := "function global:session-reviewer { [IO.File]::WriteAllText(" + powerShellLiteral(canary) + ", 'shadow'); exit 99 }; & " + powerShellLiteral(filepath.Join("../scripts", test.script))
			for _, arg := range test.args {
				command += " " + powerShellLiteral(arg)
			}
			cmd := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
			cmd.Env = append(os.Environ(), "PATH="+path, "SESSION_REVIEWER_TEST_CAPTURE="+capture, "SESSION_REVIEWER_TEST_EXIT=41")
			output, err := cmd.CombinedOutput()
			if exitCode(err) != 41 {
				t.Fatalf("exit code was not captured immediately: %v\n%s", err, output)
			}
			assertCapturedArgs(t, capture, test.want)
			if _, err := os.Stat(canary); !os.IsNotExist(err) {
				t.Fatalf("function shadow executed: %v", err)
			}
		})
	}
}

func TestPowerShellWrappersHandleApplicationStartFailureWhenAvailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("invalid executable fixture is POSIX-specific")
	}
	pwsh := findPowerShell()
	if pwsh == "" {
		t.Skip("PowerShell is not installed")
	}
	badDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(badDir, "session-reviewer"), []byte("not a native executable\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		script string
		args   []string
	}{
		{script: "prepare-workflow.ps1", args: []string{"review", "packet.json"}},
		{script: "apply-proposal.ps1", args: []string{"proposal.json", "packet.json"}},
	} {
		args := append([]string{"-NoLogo", "-NoProfile", "-NonInteractive", "-File", filepath.Join("../scripts", test.script)}, test.args...)
		cmd := exec.Command(pwsh, args...)
		cmd.Env = append(os.Environ(), "PATH="+badDir)
		output, err := cmd.CombinedOutput()
		if exitCode(err) != 126 || !bytes.Contains(output, []byte("session-reviewer application executable failed to start")) {
			t.Fatalf("%s exit=%d err=%v output=%s", test.script, exitCode(err), err, output)
		}
	}
}

func TestSessionReviewerHelperProcess(t *testing.T) {
	if os.Getenv("SESSION_REVIEWER_TEST_HELPER") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		os.Exit(98)
	}
	body, err := json.Marshal(os.Args[separator+1:])
	if err != nil {
		os.Exit(97)
	}
	if err := os.WriteFile(os.Getenv("SESSION_REVIEWER_TEST_CAPTURE"), body, 0o600); err != nil {
		os.Exit(96)
	}
	code, err := strconv.Atoi(os.Getenv("SESSION_REVIEWER_TEST_EXIT"))
	if err != nil {
		os.Exit(95)
	}
	os.Exit(code)
}

func installStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		source := filepath.Join(dir, "main.go")
		body := `package main

import (
	"encoding/json"
	"os"
	"strconv"
)

func main() {
	body, err := json.Marshal(os.Args[1:])
	if err != nil { os.Exit(97) }
	if err := os.WriteFile(os.Getenv("SESSION_REVIEWER_TEST_CAPTURE"), body, 0600); err != nil { os.Exit(96) }
	code, err := strconv.Atoi(os.Getenv("SESSION_REVIEWER_TEST_EXIT"))
	if err != nil { os.Exit(95) }
	os.Exit(code)
}
`
		if err := os.WriteFile(source, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "session-reviewer.exe")
		if output, err := exec.Command("go", "build", "-o", path, source).CombinedOutput(); err != nil {
			t.Fatalf("build Windows test stub: %v\n%s", err, output)
		}
		return dir
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nSESSION_REVIEWER_TEST_HELPER=1; export SESSION_REVIEWER_TEST_HELPER\nexec \"$GO_TEST_HELPER\" -test.run=TestSessionReviewerHelperProcess -- \"$@\"\n"
	path := filepath.Join(dir, "session-reviewer")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GO_TEST_HELPER", executable)
	return dir
}

func assertCapturedArgs(t *testing.T, path string, want []string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("args mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}

func errorAs(err error, target **exec.ExitError) bool {
	exitErr, ok := err.(*exec.ExitError)
	if ok {
		*target = exitErr
	}
	return ok
}

func findPowerShell() string {
	for _, name := range []string{"pwsh", "powershell"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

func powerShellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
