package project

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"golang.org/x/text/unicode/norm"
)

const initTransactionLockTimeout = 10 * time.Second

var (
	ErrInvalidInitializationRoot         = errors.New("initialization root is invalid or missing")
	ErrNestedInitializationRoots         = errors.New("project and vault must not contain one another")
	ErrCorruptInitializationConfig       = errors.New("initialization configuration is invalid")
	ErrConflictingInitializationIdentity = errors.New("initialization identity conflicts with existing state")
	ErrInitializationStateChanged        = errors.New("initialization state changed")
)

type InitOptions struct {
	ProjectRoot         string
	VaultRoot           string
	DataDir             string
	GOOS                string
	Now                 func() time.Time
	Random              io.Reader
	beforeOverviewWrite func() error
	afterOverviewWrite  func() error
	afterStateComponent func(string) error
	beforeConfigWrite   func() error
	afterConfigWrite    func() error
	afterLock           func() error
	caseDetector        func(*os.Root) (platform.CaseMode, error)
}

type InitResult struct {
	ProjectID  string
	LedgerRoot string
	ConfigPath string
}

type InitPreview struct {
	ProjectID   string
	ProjectRoot string
	VaultRoot   string
	LedgerRoot  string
	ConfigPath  string
	Action      string
}

type initializationPaths struct {
	projectRoot string
	vaultRoot   string
	dataRoot    string
	ledgerRoot  string
	configPath  string
}

type initializationRoots struct {
	project *pathguard.Directory
	vault   *pathguard.Directory
}

func PreviewInitialization(opts InitOptions) (InitPreview, error) {
	paths, err := resolveInitializationPaths(opts)
	if err != nil {
		return InitPreview{}, err
	}
	roots, err := openInitializationRoots(opts.GOOS, paths)
	if err != nil {
		return InitPreview{}, err
	}
	defer roots.close()

	preview := InitPreview{
		ProjectRoot: paths.projectRoot,
		VaultRoot:   paths.vaultRoot,
		LedgerRoot:  paths.ledgerRoot,
		ConfigPath:  paths.configPath,
		Action:      "create",
	}
	cfg, err := config.Load(paths.configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return InitPreview{}, initializationError(ErrCorruptInitializationConfig, err)
	}
	if err == nil {
		mapped, found, findErr := findProject(cfg, opts.GOOS, paths.projectRoot, roots.project.Info())
		if findErr != nil {
			return InitPreview{}, initializationError(ErrConflictingInitializationIdentity, findErr)
		}
		if found {
			preview.ProjectID = mapped.ID
			preview.Action = "reuse"
		}
	}
	return preview, nil
}

func Initialize(opts InitOptions) (result InitResult, retErr error) {
	paths, err := resolveInitializationPaths(opts)
	if err != nil {
		return InitResult{}, err
	}
	preflightRoots, err := openInitializationRoots(opts.GOOS, paths)
	if err != nil {
		return InitResult{}, err
	}
	preflightRoots.close()
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	if opts.caseDetector == nil {
		opts.caseDetector = detectCaseMode
	}

	dataDir, err := openOrCreateDirectory(paths.dataRoot, 0o700)
	if err != nil {
		return InitResult{}, initializationError(ErrInvalidInitializationRoot, fmt.Errorf("data root is a symlink or reparse point, or invalid: %w", err))
	}
	defer dataDir.Close()
	lock, err := acquireInitLock(dataDir.Root, "config.toml.lock", initTransactionLockTimeout)
	if err != nil {
		return InitResult{}, initializationError(ErrInitializationStateChanged, err)
	}
	defer func() {
		retErr = errors.Join(retErr, lock.release())
	}()
	if opts.afterLock != nil {
		if err := opts.afterLock(); err != nil {
			return InitResult{}, err
		}
	}
	roots, err := openInitializationRoots(opts.GOOS, paths)
	if err != nil {
		return InitResult{}, initializationError(ErrInitializationStateChanged, err)
	}
	defer roots.close()

	cfg, err := config.LoadRoot(dataDir.Root, "config.toml")
	if err != nil {
		return InitResult{}, initializationError(ErrCorruptInitializationConfig, err)
	}
	overviewID, overviewExists, err := readOverviewID(roots.project.Root, filepath.ToSlash(filepath.Join("docs", "session-review", "project-overview.md")))
	if err != nil {
		return InitResult{}, initializationError(ErrConflictingInitializationIdentity, err)
	}
	existing, mapped, err := findProject(cfg, opts.GOOS, paths.projectRoot, roots.project.Info())
	if err != nil {
		return InitResult{}, initializationError(ErrConflictingInitializationIdentity, err)
	}
	if mapped {
		sameVault, err := samePhysicalPath(existing.VaultRoot, paths.vaultRoot)
		if err != nil {
			return InitResult{}, initializationError(ErrConflictingInitializationIdentity, err)
		}
		if !sameVault {
			return InitResult{}, initializationError(ErrConflictingInitializationIdentity, errors.New("project is already mapped to a different vault"))
		}
		if !validProjectID(existing.ID) {
			return InitResult{}, initializationError(ErrConflictingInitializationIdentity, fmt.Errorf("mapped project ID %q is invalid", existing.ID))
		}
		if overviewExists && overviewID != existing.ID {
			return InitResult{}, initializationError(ErrConflictingInitializationIdentity, fmt.Errorf("project overview ID %q does not match mapped ID %q", overviewID, existing.ID))
		}
		updated, changed, err := completeVaultMapping(opts, roots.vault.Root, existing, filepath.Base(paths.projectRoot))
		if err != nil {
			return InitResult{}, err
		}
		if err := ensureProjectSyncState(dataDir.Root, updated.ID); err != nil {
			return InitResult{}, err
		}
		if !overviewExists {
			if opts.beforeOverviewWrite != nil {
				if err := opts.beforeOverviewWrite(); err != nil {
					return InitResult{}, err
				}
			}
			if err := writeOverview(roots.project.Root, overviewBody(updated.ID, opts.Now(), paths.projectRoot)); err != nil {
				return InitResult{}, err
			}
		}
		if changed {
			replaceProjectMapping(&cfg, updated)
			if opts.beforeConfigWrite != nil {
				if err := opts.beforeConfigWrite(); err != nil {
					return InitResult{}, err
				}
			}
			if err := config.SaveRoot(dataDir.Root, "config.toml", cfg); err != nil {
				return InitResult{}, err
			}
			if opts.afterConfigWrite != nil {
				if err := opts.afterConfigWrite(); err != nil {
					return InitResult{}, err
				}
			}
		}
		return InitResult{ProjectID: updated.ID, LedgerRoot: paths.ledgerRoot, ConfigPath: paths.configPath}, nil
	}
	if overviewExists {
		if owner, claimed := cfg.ProjectByID(overviewID); claimed {
			return InitResult{}, initializationError(ErrConflictingInitializationIdentity, fmt.Errorf("project ID %q already belongs to another project root %q", overviewID, owner.Root))
		}
		mapping, _, err := completeVaultMapping(opts, roots.vault.Root, config.ProjectMapping{ID: overviewID, Root: paths.projectRoot, VaultRoot: paths.vaultRoot}, filepath.Base(paths.projectRoot))
		if err != nil {
			return InitResult{}, err
		}
		if err := ensureExactInitializationScaffold(dataDir.Root, mapping.ID, true, opts.afterStateComponent); err != nil {
			return InitResult{}, err
		}
		cfg.Projects = append(cfg.Projects, mapping)
		if opts.beforeConfigWrite != nil {
			if err := opts.beforeConfigWrite(); err != nil {
				return InitResult{}, err
			}
		}
		if err := config.SaveRoot(dataDir.Root, "config.toml", cfg); err != nil {
			return InitResult{}, err
		}
		if opts.afterConfigWrite != nil {
			if err := opts.afterConfigWrite(); err != nil {
				return InitResult{}, err
			}
		}
		return InitResult{ProjectID: overviewID, LedgerRoot: paths.ledgerRoot, ConfigPath: paths.configPath}, nil
	}

	raw := make([]byte, 8)
	if _, err := io.ReadFull(opts.Random, raw); err != nil {
		return InitResult{}, err
	}
	id := "project-" + hex.EncodeToString(raw)
	mapping, _, err := completeVaultMapping(opts, roots.vault.Root, config.ProjectMapping{ID: id, Root: paths.projectRoot, VaultRoot: paths.vaultRoot}, filepath.Base(paths.projectRoot))
	if err != nil {
		return InitResult{}, err
	}
	stateExists, err := projectSyncStateExists(dataDir.Root, mapping.ID)
	if err != nil {
		return InitResult{}, err
	}
	if stateExists {
		return InitResult{}, fmt.Errorf("generated project state already exists")
	}
	if opts.beforeOverviewWrite != nil {
		if err := opts.beforeOverviewWrite(); err != nil {
			return InitResult{}, err
		}
	}
	if err := writeOverview(roots.project.Root, overviewBody(id, opts.Now(), paths.projectRoot)); err != nil {
		return InitResult{}, err
	}
	if opts.afterOverviewWrite != nil {
		if err := opts.afterOverviewWrite(); err != nil {
			return InitResult{}, err
		}
	}
	if err := ensureExactInitializationScaffold(dataDir.Root, mapping.ID, false, opts.afterStateComponent); err != nil {
		return InitResult{}, err
	}
	cfg.Projects = append(cfg.Projects, mapping)
	if opts.beforeConfigWrite != nil {
		if err := opts.beforeConfigWrite(); err != nil {
			return InitResult{}, err
		}
	}
	if err := config.SaveRoot(dataDir.Root, "config.toml", cfg); err != nil {
		return InitResult{}, err
	}
	if opts.afterConfigWrite != nil {
		if err := opts.afterConfigWrite(); err != nil {
			return InitResult{}, err
		}
	}
	return InitResult{ProjectID: id, LedgerRoot: paths.ledgerRoot, ConfigPath: paths.configPath}, nil
}

func resolveInitializationPaths(opts InitOptions) (initializationPaths, error) {
	root, err := filepath.Abs(opts.ProjectRoot)
	if err != nil {
		return initializationPaths{}, initializationError(ErrInvalidInitializationRoot, err)
	}
	vault, err := filepath.Abs(opts.VaultRoot)
	if err != nil {
		return initializationPaths{}, initializationError(ErrInvalidInitializationRoot, err)
	}
	data, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return initializationPaths{}, initializationError(ErrInvalidInitializationRoot, err)
	}
	return initializationPaths{
		projectRoot: root,
		vaultRoot:   vault,
		dataRoot:    data,
		ledgerRoot:  filepath.Join(root, "docs", "session-review"),
		configPath:  filepath.Join(data, "config.toml"),
	}, nil
}

func openInitializationRoots(goos string, paths initializationPaths) (initializationRoots, error) {
	projectDir, err := pathguard.Open(paths.projectRoot)
	if err != nil {
		return initializationRoots{}, initializationError(ErrInvalidInitializationRoot, fmt.Errorf("project root is a symlink or reparse point, or invalid: %w", err))
	}
	vaultDir, err := pathguard.Open(paths.vaultRoot)
	if err != nil {
		_ = projectDir.Close()
		return initializationRoots{}, initializationError(ErrInvalidInitializationRoot, fmt.Errorf("vault root is a symlink or reparse point, or invalid: %w", err))
	}
	roots := initializationRoots{project: projectDir, vault: vaultDir}
	if inside(goos, paths.projectRoot, paths.vaultRoot) || inside(goos, paths.vaultRoot, paths.projectRoot) ||
		projectDir.ContainsIdentity(vaultDir.Info()) || vaultDir.ContainsIdentity(projectDir.Info()) {
		roots.close()
		return initializationRoots{}, ErrNestedInitializationRoots
	}
	return roots, nil
}

func initializationError(kind, cause error) error {
	return fmt.Errorf("%w: %v", kind, cause)
}

func (roots initializationRoots) close() {
	_ = roots.project.Close()
	_ = roots.vault.Close()
}

func findProject(cfg config.Config, goos, root string, rootInfo os.FileInfo) (config.ProjectMapping, bool, error) {
	if goos != runtime.GOOS {
		existing, ok := cfg.FindProject(goos, root)
		return existing, ok, nil
	}
	var match config.ProjectMapping
	matches := 0
	for _, project := range cfg.Projects {
		mapped, err := pathguard.Open(project.Root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return config.ProjectMapping{}, false, err
		}
		same := os.SameFile(mapped.Info(), rootInfo)
		_ = mapped.Close()
		if same {
			match = project
			matches++
		}
	}
	if matches > 1 {
		return config.ProjectMapping{}, false, fmt.Errorf("configured project mapping is ambiguous")
	}
	return match, matches == 1, nil
}

func DefaultVaultReviewPath(projectName, projectID string) (string, error) {
	if !validProjectID(projectID) {
		return "", fmt.Errorf("invalid project ID %q", projectID)
	}
	name := norm.NFC.String(strings.TrimSpace(projectName))
	var display strings.Builder
	replaced := false
	for _, r := range name {
		invalid := unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r)
		if invalid {
			if !replaced {
				display.WriteByte('-')
				replaced = true
			}
			continue
		}
		display.WriteRune(r)
		replaced = false
	}
	name = strings.TrimRight(display.String(), ". ")
	if name == "" || strings.Trim(name, "-") == "" {
		name = "Project"
	}
	if platform.IsWindowsReservedName(name) {
		name = "_" + name
	}
	if utf8.RuneCountInString(name) > 64 {
		name = string([]rune(name)[:64])
		name = strings.TrimRight(name, ". ")
	}
	suffix := strings.TrimPrefix(projectID, "project-")[:8]
	relative := "Projects/" + name + "--" + suffix + "/Session Review"
	if _, err := platform.PathKey("darwin", platform.CaseSensitive, relative); err != nil {
		return "", fmt.Errorf("construct vault review path: %w", err)
	}
	return relative, nil
}

func completeVaultMapping(opts InitOptions, vaultRoot *os.Root, mapping config.ProjectMapping, projectName string) (config.ProjectMapping, bool, error) {
	if mapping.VaultReviewPath != "" && mapping.VaultCaseMode != "" {
		return mapping, false, nil
	}
	reviewPath, err := DefaultVaultReviewPath(projectName, mapping.ID)
	if err != nil {
		return config.ProjectMapping{}, false, err
	}
	goos := opts.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	caseMode := platform.CaseInsensitive
	if goos != "windows" {
		caseMode, err = opts.caseDetector(vaultRoot)
		if err != nil {
			return config.ProjectMapping{}, false, err
		}
		if caseMode != platform.CaseSensitive && caseMode != platform.CaseInsensitive {
			return config.ProjectMapping{}, false, fmt.Errorf("case probe returned an invalid result")
		}
	}
	mapping.VaultReviewPath = reviewPath
	mapping.VaultCaseMode = caseMode
	return mapping, true, nil
}

func replaceProjectMapping(cfg *config.Config, mapping config.ProjectMapping) {
	for index := range cfg.Projects {
		if cfg.Projects[index].ID == mapping.ID {
			cfg.Projects[index] = mapping
			return
		}
	}
}

func detectCaseMode(root *os.Root) (mode platform.CaseMode, retErr error) {
	if root == nil {
		return "", errors.New("vault root is required for case detection")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate case probe name: %w", err)
	}
	lower := ".session-reviewer-case-" + hex.EncodeToString(random[:])
	upper := strings.ToUpper(lower)
	first, err := root.OpenFile(lower, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create case probe: %w", err)
	}
	defer func() {
		retErr = errors.Join(retErr, first.Close())
		if err := root.Remove(lower); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("remove lowercase case probe: %w", err))
		}
		if err := root.Remove(upper); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("remove uppercase case probe: %w", err))
		}
		retErr = errors.Join(retErr, atomicfile.SyncRootDirectory(root))
	}()
	firstInfo, err := first.Stat()
	if err != nil || !firstInfo.Mode().IsRegular() {
		return "", errors.New("inspect lowercase case probe")
	}
	second, err := root.OpenFile(upper, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		secondInfo, statErr := second.Stat()
		closeErr := second.Close()
		if statErr != nil || closeErr != nil || !secondInfo.Mode().IsRegular() || os.SameFile(firstInfo, secondInfo) {
			return "", errors.New("filesystem case probe was inconclusive")
		}
		return platform.CaseSensitive, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create uppercase case probe: %w", err)
	}
	second, err = root.Open(upper)
	if err != nil {
		return "", errors.New("filesystem case probe was inconclusive")
	}
	secondInfo, statErr := second.Stat()
	closeErr := second.Close()
	if statErr != nil || closeErr != nil || !secondInfo.Mode().IsRegular() || !os.SameFile(firstInfo, secondInfo) {
		return "", errors.New("filesystem case probe was inconclusive")
	}
	return platform.CaseInsensitive, nil
}

var initializationStateComponents = []string{"merge-bases", "queue", "transactions", "locks"}

func projectSyncStateExists(dataRoot *os.Root, projectID string) (bool, error) {
	projectsInfo, err := dataRoot.Lstat("projects")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !projectsInfo.IsDir() || projectsInfo.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("projects state root is redirected or not a directory")
	}
	projectsRoot, err := openStableRoot(dataRoot, "projects")
	if err != nil {
		return false, err
	}
	defer projectsRoot.Close()
	_, err = projectsRoot.Lstat(projectID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func ensureExactInitializationScaffold(dataRoot *os.Root, projectID string, allowExisting bool, afterComponent func(string) error) error {
	if !validProjectID(projectID) {
		return fmt.Errorf("invalid project ID %q", projectID)
	}
	if err := ensurePrivateRootDirectory(dataRoot, "projects"); err != nil {
		return fmt.Errorf("ensure projects state root: %w", err)
	}
	projectsRoot, err := openStableRoot(dataRoot, "projects")
	if err != nil {
		return err
	}
	defer projectsRoot.Close()

	_, err = projectsRoot.Lstat(projectID)
	projectExists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if projectExists && !allowExisting {
		return fmt.Errorf("generated project state already exists")
	}
	if !projectExists {
		if err := ensurePrivateRootDirectory(projectsRoot, projectID); err != nil {
			return fmt.Errorf("ensure project state root: %w", err)
		}
	} else if err := validateStrictPrivateDirectory(projectsRoot, projectID); err != nil {
		return fmt.Errorf("invalid initialization scaffold root: %w", err)
	}
	projectRoot, err := openStableRoot(projectsRoot, projectID)
	if err != nil {
		return err
	}
	defer projectRoot.Close()
	if err := requireAllowedRootEntries(projectRoot, initializationStateComponents); err != nil {
		return fmt.Errorf("initialization scaffold has unexpected content: %w", err)
	}

	for _, relative := range initializationStateComponents {
		info, statErr := projectRoot.Lstat(relative)
		if errors.Is(statErr, os.ErrNotExist) {
			if err := ensurePrivateRootDirectory(projectRoot, relative); err != nil {
				return fmt.Errorf("ensure project state directory %q: %w", relative, err)
			}
		} else if statErr != nil {
			return statErr
		} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("initialization scaffold component %q is redirected or not a directory", relative)
		} else if err := validateStrictPrivateDirectory(projectRoot, relative); err != nil {
			return fmt.Errorf("invalid initialization scaffold component %q: %w", relative, err)
		}
		componentRoot, err := openStableRoot(projectRoot, relative)
		if err != nil {
			return err
		}
		want := []string(nil)
		if relative == "locks" {
			want = []string{"sync.lock"}
		}
		if err := requireAllowedRootEntries(componentRoot, want); err != nil {
			_ = componentRoot.Close()
			return fmt.Errorf("initialization scaffold component %q has unexpected content: %w", relative, err)
		}
		if err := componentRoot.Close(); err != nil {
			return err
		}
		if afterComponent != nil {
			if err := afterComponent(relative); err != nil {
				return err
			}
		}
	}

	lockRoot, err := openStableRoot(projectRoot, "locks")
	if err != nil {
		return err
	}
	defer lockRoot.Close()
	lockInfo, err := lockRoot.Lstat("sync.lock")
	if errors.Is(err, os.ErrNotExist) {
		lockFile, createErr := openStableInitLockFile(lockRoot, "sync.lock")
		if createErr != nil {
			return fmt.Errorf("ensure sync lock: %w", createErr)
		}
		if closeErr := lockFile.Close(); closeErr != nil {
			return closeErr
		}
	} else if err != nil {
		return err
	}
	lockInfo, err = lockRoot.Lstat("sync.lock")
	if err != nil {
		return err
	}
	if err := validateExactEmptyLock(lockRoot, lockInfo); err != nil {
		return err
	}
	if afterComponent != nil {
		if err := afterComponent("sync.lock"); err != nil {
			return err
		}
	}
	if err := requireExactRootEntriesBounded(projectRoot, initializationStateComponents); err != nil {
		return fmt.Errorf("initialization scaffold has unexpected content: %w", err)
	}
	return requireExactRootEntriesBounded(lockRoot, []string{"sync.lock"})
}

func validateStrictPrivateDirectory(parent *os.Root, name string) error {
	before, err := parent.Lstat(name)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return errors.New("state component is redirected or not a directory")
	}
	if runtime.GOOS != "windows" && before.Mode().Perm() != 0o700 {
		return fmt.Errorf("private directory mode is %o, want 700", before.Mode().Perm())
	}
	opened, err := parent.OpenRoot(name)
	if err != nil {
		return err
	}
	defer opened.Close()
	after, err := opened.Stat(".")
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		return errors.New("state component changed while opening")
	}
	return nil
}

func validateExactEmptyLock(root *os.Root, before os.FileInfo) error {
	if before == nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() != 0 {
		return errors.New("initialization sync lock is redirected, nonempty, or not regular")
	}
	if runtime.GOOS != "windows" && before.Mode().Perm() != 0o600 {
		return fmt.Errorf("initialization sync lock mode is %o, want 600", before.Mode().Perm())
	}
	file, err := root.Open("sync.lock")
	if err != nil {
		return err
	}
	opened, statErr := file.Stat()
	closeErr := file.Close()
	after, afterErr := root.Lstat("sync.lock")
	if statErr != nil || closeErr != nil || afterErr != nil || !os.SameFile(before, opened) || !os.SameFile(opened, after) || opened.Mode() != after.Mode() || after.Size() != 0 {
		return errors.New("initialization sync lock changed while validating")
	}
	return nil
}

func requireAllowedRootEntries(root *os.Root, allowed []string) error {
	entries, err := readBoundedRootEntries(root, len(allowed)+1)
	if err != nil {
		return err
	}
	allowedNames := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedNames[name] = struct{}{}
	}
	for _, entry := range entries {
		if _, ok := allowedNames[entry.Name()]; !ok {
			return fmt.Errorf("unexpected entry %q", entry.Name())
		}
	}
	if len(entries) > len(allowed) {
		return errors.New("too many entries")
	}
	return nil
}

func requireExactRootEntriesBounded(root *os.Root, want []string) error {
	entries, err := readBoundedRootEntries(root, len(want)+1)
	if err != nil {
		return err
	}
	if len(entries) != len(want) {
		return fmt.Errorf("entry count=%d want=%d", len(entries), len(want))
	}
	wantNames := make(map[string]struct{}, len(want))
	for _, name := range want {
		wantNames[name] = struct{}{}
	}
	for _, entry := range entries {
		if _, ok := wantNames[entry.Name()]; !ok {
			return fmt.Errorf("unexpected entry %q", entry.Name())
		}
	}
	return nil
}

func readBoundedRootEntries(root *os.Root, limit int) ([]os.DirEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(limit)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	return entries, errors.Join(readErr, directory.Close())
}

func ensureProjectSyncState(dataRoot *os.Root, projectID string) error {
	if !validProjectID(projectID) {
		return fmt.Errorf("invalid project ID %q", projectID)
	}
	if err := ensurePrivateRootDirectory(dataRoot, "projects"); err != nil {
		return fmt.Errorf("ensure projects state root: %w", err)
	}
	projectsRoot, err := openStableRoot(dataRoot, "projects")
	if err != nil {
		return err
	}
	defer projectsRoot.Close()
	if err := ensurePrivateRootDirectory(projectsRoot, projectID); err != nil {
		return fmt.Errorf("ensure project state root: %w", err)
	}
	projectRoot, err := openStableRoot(projectsRoot, projectID)
	if err != nil {
		return err
	}
	defer projectRoot.Close()
	for _, relative := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := ensurePrivateRootDirectory(projectRoot, relative); err != nil {
			return fmt.Errorf("ensure project state directory %q: %w", relative, err)
		}
	}
	lockRoot, err := openStableRoot(projectRoot, "locks")
	if err != nil {
		return err
	}
	defer lockRoot.Close()
	lockFile, err := openStableInitLockFile(lockRoot, "sync.lock")
	if err != nil {
		return fmt.Errorf("ensure sync lock: %w", err)
	}
	return lockFile.Close()
}

func openStableRoot(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("state component is redirected or not a directory")
	}
	opened, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := opened.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = opened.Close()
		return nil, errors.New("state component changed while opening")
	}
	return opened, nil
}

func ensurePrivateRootDirectory(root *os.Root, relative string) error {
	if err := ensureRootDirectory(root, relative, 0o700); err != nil {
		return err
	}
	before, err := root.Lstat(relative)
	if err != nil {
		return err
	}
	opened, err := root.OpenRoot(relative)
	if err != nil {
		return err
	}
	defer opened.Close()
	after, err := opened.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		return errors.New("private directory changed while opening")
	}
	file, err := opened.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o700); err != nil {
		return err
	}
	return nil
}

func overviewBody(projectID string, createdAt time.Time, root string) string {
	return fmt.Sprintf(
		"---\nid: project-overview\nentity_type: project_overview\nproject_id: %s\nrevision: 1\nsync_status: synced\ncreated_at: %s\n---\n\n# %s\n",
		projectID,
		createdAt.UTC().Format(time.RFC3339),
		filepath.Base(root),
	)
}

func readOverviewID(root *os.Root, overview string) (string, bool, error) {
	primary, primaryFound, primaryErr := readOverviewIDFile(root, overview)
	backup, backupFound, backupErr := readOverviewIDFile(root, atomicfile.BackupPath(overview))
	if primaryFound && primaryErr == nil {
		return primary, true, nil
	} else if backupFound && backupErr == nil {
		return backup, true, nil
	} else if !primaryFound && !backupFound {
		return "", false, nil
	} else {
		return "", false, fmt.Errorf("project overview or its parent is redirected or invalid")
	}
}

func readOverviewIDFile(root *os.Root, name string) (string, bool, error) {
	body, found, err := readOverviewFile(root, name)
	if err != nil || !found {
		return "", found, err
	}
	var projectID string
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "project_id:") {
			continue
		}
		if projectID != "" {
			return "", false, fmt.Errorf("project overview contains multiple project IDs")
		}
		projectID = strings.TrimSpace(strings.TrimPrefix(line, "project_id:"))
	}
	if !validProjectID(projectID) {
		return "", true, fmt.Errorf("project overview contains invalid project ID")
	}
	return projectID, true, nil
}

func readOverviewFile(root *os.Root, name string) ([]byte, bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1<<20 {
		return nil, true, fmt.Errorf("invalid project overview")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, true, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, true, fmt.Errorf("project overview changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, true, err
	}
	after, err := root.Lstat(name)
	if err != nil || !os.SameFile(opened, after) {
		return nil, true, fmt.Errorf("project overview changed while reading")
	}
	return body, true, nil
}

func validProjectID(projectID string) bool {
	const prefix = "project-"
	if !strings.HasPrefix(projectID, prefix) || len(projectID) != len(prefix)+16 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(projectID, prefix))
	return err == nil
}

func inside(goos, parent, child string) bool {
	parent = platform.NormalizePath(goos, parent)
	child = platform.NormalizePath(goos, child)
	if parent == child {
		return true
	}
	separator := string(filepath.Separator)
	if goos == "windows" {
		separator = "/"
	}
	prefix := strings.TrimSuffix(parent, separator) + separator
	return strings.HasPrefix(child, prefix)
}

func samePhysicalPath(first, second string) (bool, error) {
	return pathguard.SameDirectory(first, second)
}

func writeOverview(root *os.Root, body string) error {
	if err := ensureRootDirectory(root, "docs", 0o755); err != nil {
		return err
	}
	if err := ensureRootDirectory(root, filepath.Join("docs", "session-review"), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteRoot(root, filepath.Join("docs", "session-review", "project-overview.md"), []byte(body), 0o644)
}

func ensureRootDirectory(root *os.Root, name string, perm os.FileMode) error {
	return ensureRootDirectoryWith(root, name, perm, atomicfile.EnsureRootDir)
}

func ensureRootDirectoryWith(root *os.Root, name string, perm os.FileMode, ensure func(*os.Root, string, os.FileMode) error) error {
	before, err := root.Lstat(name)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	if !missing && (!before.IsDir() || before.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("target component is redirected or not a directory")
	}
	if err := ensure(root, name, perm); err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("target component is redirected or not a directory")
	}
	if before != nil && !os.SameFile(before, info) {
		return fmt.Errorf("target component changed while ensuring durability")
	}
	opened, err := root.OpenRoot(name)
	if err != nil {
		return err
	}
	defer opened.Close()
	after, err := opened.Stat(".")
	if err != nil || !os.SameFile(info, after) {
		return fmt.Errorf("target component changed while opening")
	}
	return nil
}

func openOrCreateDirectory(path string, perm os.FileMode) (*pathguard.Directory, error) {
	return openOrCreateDirectoryWith(path, perm, atomicfile.EnsureRootDir)
}

func openOrCreateDirectoryWith(path string, perm os.FileMode, ensure func(*os.Root, string, os.FileMode) error) (*pathguard.Directory, error) {
	directory, remaining, err := pathguard.OpenDeepest(path)
	if err != nil {
		return nil, err
	}
	if err := ensureOpenedDirectoryPublication(directory, perm, ensure); err != nil {
		_ = directory.Close()
		return nil, err
	}
	if len(remaining) == 0 {
		return directory, nil
	}
	for _, component := range remaining {
		if err := ensure(directory.Root, component, perm); err != nil {
			_ = directory.Close()
			return nil, err
		}
		next, err := directory.Root.OpenRoot(component)
		if err != nil {
			_ = directory.Close()
			return nil, err
		}
		_ = directory.Root.Close()
		directory.Root = next
		directory.Path = filepath.Join(directory.Path, component)
		info, err := next.Stat(".")
		if err != nil {
			_ = directory.Close()
			return nil, err
		}
		directory.Ancestors = append(directory.Ancestors, info)
	}
	return directory, nil
}

func ensureOpenedDirectoryPublication(directory *pathguard.Directory, perm os.FileMode, ensure func(*os.Root, string, os.FileMode) error) error {
	if directory == nil {
		return errors.New("opened directory is required")
	}
	clean := filepath.Clean(directory.Path)
	parentPath := filepath.Dir(clean)
	if clean == parentPath {
		return nil
	}
	parent, err := pathguard.Open(parentPath)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := ensureRootDirectoryWith(parent.Root, filepath.Base(clean), perm, ensure); err != nil {
		return err
	}
	after, err := parent.Root.Lstat(filepath.Base(clean))
	if err != nil || !os.SameFile(directory.Info(), after) {
		return errors.New("opened directory changed while ensuring durability")
	}
	return nil
}
