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
