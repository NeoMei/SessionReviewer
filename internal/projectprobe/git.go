package projectprobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxGitOutputBytes = 8 << 20
	gitCommandTimeout = 10 * time.Second
)

var (
	gitHeadPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	scpRemote      = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._~/-]+$`)
	approvedGit    = map[string]struct{}{
		gitCallKey("rev-parse", "--show-toplevel"):          {},
		gitCallKey("symbolic-ref", "--short", "-q", "HEAD"): {},
		gitCallKey("rev-parse", "HEAD"):                     {},
		gitCallKey("status", "--porcelain=v1", "-z"):        {},
		gitCallKey("remote", "get-url", "--all", "origin"):  {},
	}
)

func runApprovedGit(ctx context.Context, runner func(context.Context, string, ...string) ([]byte, error), executable string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runner == nil || executable != "git" {
		return nil, errors.New("Git runner or executable is not approved")
	}
	if _, approved := approvedGit[gitCallKey(args...)]; !approved {
		return nil, errors.New("Git argv is not approved")
	}
	output, err := runner(ctx, executable, append([]string(nil), args...)...)
	if err != nil {
		return nil, err
	}
	if len(output) > maxGitOutputBytes {
		return nil, errors.New("Git output exceeds read limit")
	}
	return bytes.Clone(output), nil
}

func defaultGitRunner(root string) func(context.Context, string, ...string) ([]byte, error) {
	return func(ctx context.Context, executable string, args ...string) ([]byte, error) {
		if executable != "git" {
			return nil, errors.New("only git may be executed")
		}
		commandContext, cancel := boundedGitContext(ctx)
		defer cancel()
		command := exec.CommandContext(commandContext, executable, args...)
		command.Dir = root
		command.Env = safeGitEnvironment(os.Environ())
		stdout := &boundedBuffer{maximum: maxGitOutputBytes}
		stderr := &boundedBuffer{maximum: 64 << 10}
		command.Stdout = stdout
		command.Stderr = stderr
		if err := command.Run(); err != nil {
			return nil, err
		}
		return bytes.Clone(stdout.Bytes()), nil
	}
}

func boundedGitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, gitCommandTimeout)
}

func safeGitEnvironment(environment []string) []string {
	blocked := map[string]struct{}{
		"GIT_DIR": {}, "GIT_WORK_TREE": {}, "GIT_COMMON_DIR": {}, "GIT_INDEX_FILE": {},
		"GIT_OBJECT_DIRECTORY": {}, "GIT_ALTERNATE_OBJECT_DIRECTORIES": {},
		"GIT_TERMINAL_PROMPT": {}, "GIT_OPTIONAL_LOCKS": {}, "GCM_INTERACTIVE": {},
		"GIT_ASKPASS": {}, "SSH_ASKPASS": {}, "GIT_CONFIG_COUNT": {},
	}
	result := make([]string, 0, len(environment)+6)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if _, remove := blocked[name]; !remove && !strings.HasPrefix(name, "GIT_CONFIG_KEY_") && !strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
			result = append(result, entry)
		}
	}
	return append(result,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GCM_INTERACTIVE=Never",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=core.fsmonitor",
		"GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=diff.ignoreSubmodules",
		"GIT_CONFIG_VALUE_2=all",
	)
}

type boundedBuffer struct {
	bytes.Buffer
	maximum int
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.maximum - buffer.Len()
	if remaining <= 0 || len(value) > remaining {
		return 0, errors.New("command output exceeds read limit")
	}
	return buffer.Buffer.Write(value)
}

func gitCallKey(args ...string) string {
	return strings.Join(args, "\x00")
}

func parseSingleLine(output []byte, maximum int) (string, error) {
	if len(output) == 0 || len(output) > maximum || !utf8.Valid(output) || bytes.IndexByte(output, 0) >= 0 {
		return "", errors.New("invalid line")
	}
	value := string(output)
	if strings.HasSuffix(value, "\n") {
		value = strings.TrimSuffix(value, "\n")
		value = strings.TrimSuffix(value, "\r")
	}
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("invalid line")
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return "", errors.New("invalid line")
		}
	}
	return value, nil
}

func parseBranch(output []byte) (string, error) {
	branch, err := parseSingleLine(output, 512)
	if err != nil || !validBranch(branch) {
		return "", errors.New("invalid branch")
	}
	return branch, nil
}

func validBranch(branch string) bool {
	if branch == "@" || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") || strings.Contains(branch, "//") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.ContainsAny(branch, " ~^:?*[\\") {
		return false
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func parseHead(output []byte) (string, error) {
	head, err := parseSingleLine(output, 65)
	if err != nil || !gitHeadPattern.MatchString(head) {
		return "", errors.New("invalid HEAD")
	}
	return head, nil
}

func parseRemoteIdentities(output []byte) ([]string, bool) {
	if len(output) == 0 {
		return []string{}, false
	}
	if len(output) > maxGitOutputBytes || !utf8.Valid(output) || bytes.IndexByte(output, 0) >= 0 {
		return []string{}, true
	}
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	hashes := make(map[string]struct{}, len(lines))
	malformed := false
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if !validRemote(line) {
			malformed = true
			continue
		}
		sum := sha256.Sum256([]byte(line))
		hashes[fmt.Sprintf("sha256:%x", sum)] = struct{}{}
	}
	result := make([]string, 0, len(hashes))
	for hash := range hashes {
		result = append(result, hash)
	}
	sort.Strings(result)
	return result, malformed
}

func validRemote(value string) bool {
	if value == "" || len(value) > 4096 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character == 0x7f {
			return false
		}
	}
	if scpRemote.MatchString(value) {
		return true
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if parsed.Scheme != "" {
		switch parsed.Scheme {
		case "https", "http", "ssh", "git", "file":
		default:
			return false
		}
		return parsed.User == nil && (parsed.Host != "" || parsed.Scheme == "file")
	}
	return !strings.Contains(value, "://")
}

func parseStatus(output []byte) (int, bool) {
	if len(output) == 0 {
		return 0, false
	}
	if len(output) > maxGitOutputBytes || output[len(output)-1] != 0 {
		return 0, true
	}
	segments := bytes.Split(output[:len(output)-1], []byte{0})
	count := 0
	malformed := false
	for index := 0; index < len(segments); index++ {
		record := segments[index]
		if len(record) < 4 || record[2] != ' ' || !validStatusCode(record[0], record[1]) || !validStatusPath(record[3:]) {
			malformed = true
			continue
		}
		if record[0] == 'R' || record[1] == 'R' || record[0] == 'C' || record[1] == 'C' {
			index++
			if index >= len(segments) || !validStatusPath(segments[index]) {
				malformed = true
				continue
			}
		}
		count++
	}
	return count, malformed
}

func validStatusCode(first, second byte) bool {
	if (first == '?' && second == '?') || (first == '!' && second == '!') {
		return true
	}
	return strings.ContainsRune(" MADRCU", rune(first)) && strings.ContainsRune(" MADRCU", rune(second)) && (first != ' ' || second != ' ')
}

func validStatusPath(value []byte) bool {
	if len(value) == 0 || len(value) > 4096 || !utf8.Valid(value) || value[0] == '/' || bytes.IndexByte(value, 0) >= 0 {
		return false
	}
	text := string(value)
	if path.Clean(text) != text || text == "." || text == ".." || strings.HasPrefix(text, "../") {
		return false
	}
	for _, character := range text {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

var _ io.Writer = (*boundedBuffer)(nil)
