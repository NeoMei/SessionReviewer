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
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/pathguard"
)

const (
	maxGitOutputBytes = 8 << 20
	gitCommandTimeout = 10 * time.Second
)

var (
	gitHeadPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	scpRemote      = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._~/-]+$`)
	windowsDrive   = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
	windowsUNCHost = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,253}$`)
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

type authenticatedGitExecutable struct {
	path     string
	identity pathguard.IdentityToken
	file     *os.File
}

func authenticateGitExecutable(executable string, projectRoot *pathguard.Directory) (*authenticatedGitExecutable, error) {
	if executable == "" || len(executable) > 4096 || strings.IndexByte(executable, 0) >= 0 || !filepath.IsAbs(executable) || filepath.Clean(executable) != executable || projectRoot == nil || projectRoot.Info() == nil {
		return nil, errors.New("Git executable must be a clean absolute path")
	}
	parent, err := pathguard.Open(filepath.Dir(executable))
	if err != nil {
		return nil, errors.New("Git executable parent is unavailable or redirected")
	}
	defer parent.Close()
	if parent.ContainsIdentity(projectRoot.Info()) {
		return nil, errors.New("Git executable must be outside the authenticated project root")
	}
	file, info, err := parent.OpenRegular(filepath.Base(executable))
	if err != nil || !isExecutableFile(executable, info) {
		if file != nil {
			_ = file.Close()
		}
		return nil, errors.New("Git executable is not an authenticated regular executable")
	}
	identity, err := pathguard.PhysicalFileIdentity(file)
	if err != nil {
		_ = file.Close()
		return nil, errors.New("Git executable identity is unavailable")
	}
	return &authenticatedGitExecutable{path: executable, identity: identity, file: file}, nil
}

func (executable *authenticatedGitExecutable) Close() error {
	if executable == nil || executable.file == nil {
		return nil
	}
	return executable.file.Close()
}

func (executable *authenticatedGitExecutable) reauthenticate() error {
	if executable == nil || executable.file == nil {
		return errors.New("Git executable authentication is missing")
	}
	parent, err := pathguard.Open(filepath.Dir(executable.path))
	if err != nil {
		return errors.New("Git executable parent changed")
	}
	defer parent.Close()
	file, info, err := parent.OpenRegular(filepath.Base(executable.path))
	if err != nil || !isExecutableFile(executable.path, info) {
		if file != nil {
			_ = file.Close()
		}
		return errors.New("Git executable changed")
	}
	defer file.Close()
	identity, err := pathguard.PhysicalFileIdentity(file)
	if err != nil || identity != executable.identity {
		return errors.New("Git executable identity changed")
	}
	return nil
}

func isExecutableFile(executable string, info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Ext(executable), ".exe")
	}
	return info.Mode().Perm()&0o111 != 0
}

func defaultGitRunner(root string, authenticated *authenticatedGitExecutable) func(context.Context, string, ...string) ([]byte, error) {
	return func(ctx context.Context, executable string, args ...string) ([]byte, error) {
		if executable != "git" {
			return nil, errors.New("only git may be executed")
		}
		if err := authenticated.reauthenticate(); err != nil {
			return nil, err
		}
		commandContext, cancel := boundedGitContext(ctx)
		defer cancel()
		command := exec.CommandContext(commandContext, authenticated.path, args...)
		command.Dir = root
		command.Env = safeGitEnvironment(os.Environ())
		stdout := &boundedBuffer{maximum: maxGitOutputBytes}
		stderr := &boundedBuffer{maximum: 64 << 10}
		command.Stdout = stdout
		command.Stderr = stderr
		runErr := command.Run()
		identityErr := authenticated.reauthenticate()
		if err := errors.Join(runErr, identityErr); err != nil {
			return nil, err
		}
		return bytes.Clone(stdout.Bytes()), nil
	}
}

func boundedGitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, gitCommandTimeout)
}

func safeGitEnvironment(environment []string) []string {
	allowed := map[string]struct{}{
		"SYSTEMROOT": {}, "WINDIR": {}, "TEMP": {}, "TMP": {}, "TMPDIR": {},
	}
	values := make(map[string]string, len(allowed))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		name = strings.ToUpper(name)
		if _, keep := allowed[name]; found && keep && !strings.ContainsAny(value, "\x00\r\n") {
			values[name] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys)+20)
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	hooksPath := "/dev/null"
	if runtime.GOOS == "windows" {
		hooksPath = "NUL"
	}
	return append(result,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GCM_INTERACTIVE=Never",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_PAGER=",
		"GIT_CONFIG_COUNT=6",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=core.fsmonitor",
		"GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=diff.ignoreSubmodules",
		"GIT_CONFIG_VALUE_2=all",
		"GIT_CONFIG_KEY_3=core.hooksPath",
		"GIT_CONFIG_VALUE_3="+hooksPath,
		"GIT_CONFIG_KEY_4=core.pager",
		"GIT_CONFIG_VALUE_4=",
		"GIT_CONFIG_KEY_5=credential.interactive",
		"GIT_CONFIG_VALUE_5=false",
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

func parseRemoteIdentities(output []byte) ([]string, bool, bool) {
	if len(output) == 0 {
		return []string{}, false, false
	}
	if len(output) > maxGitOutputBytes || bytes.IndexByte(output, 0) >= 0 {
		return []string{}, true, false
	}
	hashes := make([]string, 0, 256)
	malformed := false
	excess := false
	for start := 0; start < len(output); {
		end := bytes.IndexByte(output[start:], '\n')
		if end < 0 {
			end = len(output)
		} else {
			end += start
		}
		line := output[start:end]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) == 0 || len(line) > 4096 || !utf8.Valid(line) || !validRemote(string(line)) {
			malformed = true
		} else {
			sum := sha256.Sum256(line)
			hash := fmt.Sprintf("sha256:%x", sum)
			index := sort.SearchStrings(hashes, hash)
			if index == len(hashes) || hashes[index] != hash {
				if len(hashes) < 256 {
					hashes = append(hashes, "")
					copy(hashes[index+1:], hashes[index:])
					hashes[index] = hash
				} else {
					excess = true
					if index < 256 {
						copy(hashes[index+1:], hashes[index:255])
						hashes[index] = hash
					}
				}
			}
		}
		if end == len(output) {
			break
		}
		start = end + 1
		if start == len(output) {
			break
		}
	}
	return hashes, malformed, excess
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
	if windowsDrive.MatchString(value) {
		return validWindowsDriveRemote(value)
	}
	if strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, "//") {
		return validWindowsUNCRemote(value)
	}
	if strings.HasPrefix(value, "/") {
		return path.Clean(value) == value && !strings.HasPrefix(value, "//")
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
		return parsed.User == nil && parsed.Opaque == "" && (parsed.Host != "" || (parsed.Scheme == "file" && strings.HasPrefix(parsed.Path, "/")))
	}
	return false
}

func validWindowsDriveRemote(value string) bool {
	normalized := strings.ReplaceAll(value, `\`, "/")
	if len(normalized) < 4 || normalized[1] != ':' || normalized[2] != '/' || path.Clean(normalized[2:]) != normalized[2:] {
		return false
	}
	return validWindowsLocalComponents(strings.Split(normalized[3:], "/"))
}

func validWindowsUNCRemote(value string) bool {
	normalized := strings.ReplaceAll(value, `\`, "/")
	if !strings.HasPrefix(normalized, "//") || strings.HasPrefix(normalized, "///") {
		return false
	}
	components := strings.Split(strings.TrimPrefix(normalized, "//"), "/")
	return len(components) >= 2 && windowsUNCHost.MatchString(components[0]) && validWindowsLocalComponents(components)
}

func validWindowsLocalComponents(components []string) bool {
	for _, component := range components {
		if component == "" || component == "." || component == ".." || strings.ContainsAny(component, `<>:"|?*`) {
			return false
		}
	}
	return len(components) > 0
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
