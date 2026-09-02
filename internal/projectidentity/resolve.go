// Package projectidentity resolves mutable project paths to stable project IDs
// using physical directory evidence captured through rooted handles.
package projectidentity

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
)

var ErrAssociationRequired = errors.New("project association requires explicit confirmation")

var errWorktreeRegistration = errors.New("Git worktree registration is invalid")

type Binding struct {
	ProjectID          string
	CanonicalRoot      string
	RootIdentity       pathguard.IdentityToken
	CommonDirIdentity  string
	Bootstrap          bool
	NewAuthentication  bool
	AuthenticatedAlias config.AuthenticatedProjectAlias
}

// Reauthenticate verifies that a previously resolved binding still names the
// same physical project root and rooted Git common directory. It does not
// update or repair binding metadata when either identity has changed.
func Reauthenticate(binding Binding) error {
	if binding.ProjectID == "" || binding.CanonicalRoot == "" || !filepath.IsAbs(binding.CanonicalRoot) || filepath.Clean(binding.CanonicalRoot) != binding.CanonicalRoot || !binding.RootIdentity.Valid() {
		return errors.New("project binding is invalid")
	}
	directory, err := pathguard.Open(binding.CanonicalRoot)
	if err != nil {
		return fmt.Errorf("reopen project root: %w", err)
	}
	defer directory.Close()
	rootIdentity, err := directory.PhysicalIdentity()
	if err != nil || rootIdentity != binding.RootIdentity {
		return errors.New("project root identity changed")
	}
	commonIdentity, err := gitCommonDirIdentity(directory)
	if err != nil {
		return fmt.Errorf("reauthenticate Git common directory: %w", err)
	}
	if commonIdentity != binding.CommonDirIdentity {
		return errors.New("Git common-directory identity changed")
	}
	return nil
}

// Resolve authenticates requestedRoot before resolving it. A legacy mapping
// may bootstrap only at its exact configured root. Once authenticated aliases
// exist, a move requires the recorded root identity and a distinct worktree
// requires the recorded Git common-directory identity.
func Resolve(mapping config.ProjectMapping, requestedRoot, goos string) (Binding, error) {
	if mapping.ID == "" {
		return Binding{}, errors.New("project ID is required")
	}
	requestedKey, err := pathAliasKey(goos, requestedRoot)
	if err != nil {
		return Binding{}, fmt.Errorf("invalid requested project root: %w", err)
	}
	directory, err := pathguard.Open(requestedRoot)
	if err != nil {
		return Binding{}, fmt.Errorf("authenticate requested project root: %w", err)
	}
	defer directory.Close()
	rootIdentity, err := directory.PhysicalIdentity()
	if err != nil {
		return Binding{}, fmt.Errorf("identify requested project root: %w", err)
	}
	commonIdentity, err := gitCommonDirIdentity(directory)
	if err != nil {
		if errors.Is(err, errWorktreeRegistration) {
			return Binding{}, ErrAssociationRequired
		}
		return Binding{}, fmt.Errorf("authenticate Git common directory: %w", err)
	}
	binding := Binding{
		ProjectID: mapping.ID, CanonicalRoot: directory.Path,
		RootIdentity: rootIdentity, CommonDirIdentity: commonIdentity,
		AuthenticatedAlias: config.AuthenticatedProjectAlias{
			SchemaVersion: 1, Path: requestedRoot, RootIdentity: rootIdentity, CommonDirIdentity: commonIdentity,
		},
	}

	if len(mapping.AuthenticatedAliases) == 0 {
		configuredKey, keyErr := pathAliasKey(goos, mapping.Root)
		if keyErr == nil && configuredKey == requestedKey {
			binding.Bootstrap = true
			binding.NewAuthentication = true
			return binding, nil
		}
		return Binding{}, ErrAssociationRequired
	}

	matched := false
	conflict := false
	currentAliasKnown := false
	for _, alias := range mapping.AuthenticatedAliases {
		if alias.SchemaVersion != 1 || !alias.RootIdentity.Valid() {
			return Binding{}, ErrAssociationRequired
		}
		aliasKey, keyErr := pathAliasKey(goos, alias.Path)
		if keyErr != nil {
			return Binding{}, ErrAssociationRequired
		}
		pathMatch := aliasKey == requestedKey
		rootMatch := alias.RootIdentity == rootIdentity
		commonMatch := commonIdentity != "" && alias.CommonDirIdentity != "" && commonIdentity == alias.CommonDirIdentity
		if pathMatch && !rootMatch {
			conflict = true
		}
		if rootMatch && alias.CommonDirIdentity != "" && commonIdentity != alias.CommonDirIdentity {
			conflict = true
		}
		if rootMatch || commonMatch {
			matched = true
		}
		if pathMatch && rootMatch && alias.CommonDirIdentity == commonIdentity {
			currentAliasKnown = true
		}
	}
	if conflict || !matched {
		return Binding{}, ErrAssociationRequired
	}
	binding.NewAuthentication = !currentAliasKnown
	return binding, nil
}

func identityKey(token pathguard.IdentityToken) string {
	if !token.Valid() {
		return ""
	}
	return token.Kind + ":" + token.Volume + ":" + token.File
}

func pathAliasKey(goos, value string) (string, error) {
	if value == "" || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("path is empty or invalid")
	}
	mode := platform.CaseSensitive
	if goos == "windows" {
		mode = platform.CaseInsensitive
		normalized := platform.NormalizePath(goos, value)
		rooted := strings.HasPrefix(normalized, "/") || strings.HasPrefix(normalized, "//") ||
			(len(normalized) >= 3 && normalized[1] == ':' && normalized[2] == '/')
		if !rooted {
			return "", errors.New("path must be absolute")
		}
		encoded := strings.TrimLeft(normalized, "/")
		encoded = strings.ReplaceAll(encoded, ":", "-colon-")
		return platform.PathKey(goos, mode, "absolute/"+encoded)
	}
	if !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", errors.New("path must be clean and absolute")
	}
	relative := "absolute/" + strings.TrimPrefix(filepath.ToSlash(value), "/")
	return platform.PathKey(goos, mode, relative)
}

func gitCommonDirIdentity(projectRoot *pathguard.Directory) (string, error) {
	info, err := projectRoot.Root.Lstat(".git")
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Git metadata is redirected or unavailable")
	}
	var gitDirPath string
	var pointerIdentity pathguard.IdentityToken
	gitPointerFile := false
	switch {
	case info.IsDir():
		gitDirPath = filepath.Join(projectRoot.Path, ".git")
	case info.Mode().IsRegular():
		file, opened, openErr := projectRoot.OpenRegular(".git")
		if openErr != nil {
			return "", openErr
		}
		pointerIdentity, openErr = pathguard.PhysicalFileIdentity(file)
		body, readErr := io.ReadAll(io.LimitReader(file, 4097))
		closeErr := file.Close()
		if openErr != nil || readErr != nil || closeErr != nil || len(body) > 4096 || opened.Size() != int64(len(body)) {
			return "", errors.New("Git directory pointer is invalid")
		}
		line := string(bytes.TrimSuffix(body, []byte("\n")))
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, "gitdir: ") || strings.ContainsAny(line, "\r\n\x00") {
			return "", errors.New("Git directory pointer is invalid")
		}
		gitDirPath = strings.TrimPrefix(line, "gitdir: ")
		if !filepath.IsAbs(gitDirPath) {
			gitDirPath = filepath.Join(projectRoot.Path, gitDirPath)
		}
		gitDirPath = filepath.Clean(gitDirPath)
		gitPointerFile = true
	default:
		return "", errors.New("Git metadata is not a directory or regular pointer")
	}

	gitDir, err := pathguard.Open(gitDirPath)
	if err != nil {
		return "", err
	}
	defer gitDir.Close()
	commonPath := gitDir.Path
	body, found, err := gitDir.ReadRegular("commondir", 4096)
	if err != nil {
		return "", err
	}
	if found {
		line := string(bytes.TrimSuffix(body, []byte("\n")))
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.ContainsAny(line, "\r\n\x00") {
			return "", errors.New("Git common-directory pointer is invalid")
		}
		commonPath = line
		if !filepath.IsAbs(commonPath) {
			commonPath = filepath.Join(gitDir.Path, commonPath)
		}
		commonPath = filepath.Clean(commonPath)
	}
	common, err := pathguard.Open(commonPath)
	if err != nil {
		return "", err
	}
	defer common.Close()
	if gitPointerFile {
		relative, err := filepath.Rel(common.Path, gitDir.Path)
		if err != nil || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
			return "", errors.New("Git directory pointer is outside the common directory")
		}
		switch {
		case relative == ".":
			if err := authenticateSeparateGitDir(projectRoot, gitDir, pointerIdentity); err != nil {
				return "", errors.Join(errWorktreeRegistration, err)
			}
		case isRegisteredWorktree(relative):
			if err := authenticateWorktreeRegistration(projectRoot, gitDir, common, pointerIdentity); err != nil {
				return "", errors.Join(errWorktreeRegistration, err)
			}
		default:
			return "", errors.New("Git directory pointer is not a registered worktree")
		}
	}
	token, err := common.PhysicalIdentity()
	if err != nil {
		return "", err
	}
	return identityKey(token), nil
}


func isRegisteredWorktree(relative string) bool {
	components := strings.Split(relative, string(filepath.Separator))
	if len(components) != 2 || components[0] != "worktrees" || components[1] == "" || components[1] == "." || components[1] == ".." {
		return false
	}
	return true
}

func authenticateSeparateGitDir(projectRoot, gitDir *pathguard.Directory, pointerIdentity pathguard.IdentityToken) error {
	body, found, err := gitDir.ReadRegular("gitdir", 4096)
	if err != nil {
		return err
	}
	if found {
		backlink := string(bytes.TrimSuffix(body, []byte("\n")))
		backlink = strings.TrimSuffix(backlink, "\r")
		if backlink == "" || strings.ContainsAny(backlink, "\r\n\x00") {
			return errors.New("separate Git directory backlink is invalid")
		}
		if !filepath.IsAbs(backlink) {
			backlink = filepath.Join(gitDir.Path, backlink)
		}
		backlink = filepath.Clean(backlink)
		return authenticateSeparateGitDirBacklink(projectRoot, backlink, pointerIdentity)
	}

	worktree, err := readCoreWorktree(gitDir.Path)
	if err != nil {
		return err
	}
	if err := verifyDirectoryEquals(worktree, projectRoot.Path); err != nil {
		return fmt.Errorf("core.worktree does not name project root: %w", err)
	}
	file, info, err := projectRoot.OpenRegular(".git")
	if err != nil {
		return fmt.Errorf("reopen .git pointer after core.worktree check: %w", err)
	}
	identity, identityErr := pathguard.PhysicalFileIdentity(file)
	closeErr := file.Close()
	if info == nil || identityErr != nil || closeErr != nil {
		return errors.Join(errors.New("cannot identify .git pointer after core.worktree check"), identityErr, closeErr)
	}
	if identity != pointerIdentity {
		return errors.New(".git pointer file identity changed after core.worktree check")
	}
	return nil
}

func authenticateSeparateGitDirBacklink(projectRoot *pathguard.Directory, backlink string, pointerIdentity pathguard.IdentityToken) error {
	backlinkParent, err := pathguard.Open(filepath.Dir(backlink))
	if err != nil {
		return fmt.Errorf("open separate Git directory backlink parent: %w", err)
	}
	defer backlinkParent.Close()
	projectIdentity, err := projectRoot.PhysicalIdentity()
	if err != nil {
		return err
	}
	backlinkRootIdentity, err := backlinkParent.PhysicalIdentity()
	if err != nil || backlinkRootIdentity != projectIdentity {
		return errors.Join(errors.New("separate Git directory backlink root identity changed"), err)
	}
	file, _, err := backlinkParent.OpenRegular(filepath.Base(backlink))
	if err != nil {
		return fmt.Errorf("open separate Git directory backlink: %w", err)
	}
	identity, identityErr := pathguard.PhysicalFileIdentity(file)
	closeErr := file.Close()
	if identityErr != nil || closeErr != nil || identity != pointerIdentity {
		return errors.Join(errors.New("separate Git directory backlink file identity changed"), identityErr, closeErr)
	}
	return nil
}

func readCoreWorktree(gitDir string) (string, error) {
	configPath := filepath.Join(gitDir, "config")
	file, err := os.Open(configPath)
	if err != nil {
		return "", fmt.Errorf("open git config: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 4096)
	section := ""
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
			continue
		}
		if section != "core" {
			continue
		}
		eq := strings.IndexByte(trimmed, '=')
		if eq < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(trimmed[:eq]))
		if key != "worktree" {
			continue
		}
		value := strings.TrimSpace(trimmed[eq+1:])
		value = unquoteGitConfigValue(value)
		if value == "" || strings.ContainsAny(value, "\r\n") || strings.ContainsRune(value, 0) {
			return "", errors.New("core.worktree is empty or invalid")
		}
		return filepath.Clean(value), nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read git config: %w", err)
	}
	return "", errors.New("core.worktree is not set")
}

func unquoteGitConfigValue(raw string) string {
	if len(raw) >= 2 && raw[0] == raw[len(raw)-1] && (raw[0] == 34 || raw[0] == 39) {
		return raw[1 : len(raw)-1]
	}
	return raw
}

func verifyDirectoryEquals(first, second string) error {
	firstDir, err := pathguard.Open(first)
	if err != nil {
		return err
	}
	defer firstDir.Close()
	secondDir, err := pathguard.Open(second)
	if err != nil {
		return err
	}
	defer secondDir.Close()
	if firstDir.Path != secondDir.Path {
		return errors.New("paths do not identify the same location")
	}
	return nil
}

func authenticateWorktreeRegistration(projectRoot, gitDir, common *pathguard.Directory, pointerIdentity pathguard.IdentityToken) error {
	relative, err := filepath.Rel(common.Path, gitDir.Path)
	if err != nil || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return errors.New("worktree gitdir is outside the common directory")
	}
	components := strings.Split(relative, string(filepath.Separator))
	if len(components) != 2 || components[0] != "worktrees" || components[1] == "" || components[1] == "." || components[1] == ".." {
		return errors.New("worktree gitdir is not a registered common-directory entry")
	}
	registeredRoot, registeredInfo, err := common.OpenDirectory(filepath.Join(components...))
	if err != nil {
		return fmt.Errorf("open registered worktree entry: %w", err)
	}
	openedInfo, statErr := registeredRoot.Stat(".")
	closeErr := registeredRoot.Close()
	if statErr != nil || closeErr != nil || registeredInfo == nil || openedInfo == nil || !os.SameFile(registeredInfo, openedInfo) || !os.SameFile(openedInfo, gitDir.Info()) {
		return errors.Join(errors.New("registered worktree entry identity changed"), statErr, closeErr)
	}

	body, found, err := gitDir.ReadRegular("gitdir", 4096)
	if err != nil || !found {
		return errors.Join(errors.New("registered worktree backlink is missing"), err)
	}
	backlink := string(bytes.TrimSuffix(body, []byte("\n")))
	backlink = strings.TrimSuffix(backlink, "\r")
	if backlink == "" || strings.ContainsAny(backlink, "\r\n\x00") {
		return errors.New("registered worktree backlink is invalid")
	}
	if !filepath.IsAbs(backlink) {
		backlink = filepath.Join(gitDir.Path, backlink)
	}
	backlink = filepath.Clean(backlink)
	wantBacklink := filepath.Join(projectRoot.Path, ".git")
	backlinkKey, backlinkErr := pathAliasKey(runtime.GOOS, backlink)
	wantBacklinkKey, wantErr := pathAliasKey(runtime.GOOS, wantBacklink)
	if backlinkErr != nil || wantErr != nil || backlinkKey != wantBacklinkKey {
		return errors.New("registered worktree backlink names another root")
	}
	backlinkParent, err := pathguard.Open(filepath.Dir(backlink))
	if err != nil {
		return fmt.Errorf("authenticate registered worktree root: %w", err)
	}
	defer backlinkParent.Close()
	projectIdentity, err := projectRoot.PhysicalIdentity()
	if err != nil {
		return err
	}
	backlinkRootIdentity, err := backlinkParent.PhysicalIdentity()
	if err != nil || backlinkRootIdentity != projectIdentity {
		return errors.Join(errors.New("registered worktree backlink root identity changed"), err)
	}
	backlinkFile, _, err := backlinkParent.OpenRegular(filepath.Base(backlink))
	if err != nil {
		return fmt.Errorf("authenticate registered worktree backlink: %w", err)
	}
	backlinkIdentity, identityErr := pathguard.PhysicalFileIdentity(backlinkFile)
	closeErr = backlinkFile.Close()
	if identityErr != nil || closeErr != nil || backlinkIdentity != pointerIdentity {
		return errors.Join(errors.New("registered worktree backlink file identity changed"), identityErr, closeErr)
	}
	return nil
}
