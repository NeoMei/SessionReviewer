package project

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	beforeConfigWrite   func() error
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
		if err := ensureProjectSyncState(dataDir.Root, mapping.ID); err != nil {
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
	createdState, err := createNewProjectSyncState(dataDir.Root, mapping.ID)
	if err != nil {
		return InitResult{}, err
	}
	var createdOverview newOverviewCreation
	rollback := func(cause error) error {
		var cleanupErr error
		cleanupErr = errors.Join(cleanupErr, rollbackNewOverview(roots.project.Root, createdOverview))
		cleanupErr = errors.Join(cleanupErr, rollbackNewProjectSyncState(dataDir.Root, createdState))
		return errors.Join(cause, cleanupErr)
	}
	if opts.beforeOverviewWrite != nil {
		if err := opts.beforeOverviewWrite(); err != nil {
			return InitResult{}, rollback(err)
		}
	}
	createdOverview, err = createNewOverview(roots.project.Root, overviewBody(id, opts.Now(), paths.projectRoot))
	if err != nil {
		return InitResult{}, rollback(err)
	}
	cfg.Projects = append(cfg.Projects, mapping)
	if opts.beforeConfigWrite != nil {
		if err := opts.beforeConfigWrite(); err != nil {
			return InitResult{}, rollback(err)
		}
	}
	if err := config.SaveRoot(dataDir.Root, "config.toml", cfg); err != nil {
		if !configContainsProject(dataDir.Root, mapping) {
			return InitResult{}, rollback(err)
		}
		return InitResult{}, err
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

type newProjectStateCreation struct {
	projectID   string
	projectInfo os.FileInfo
	directories map[string]os.FileInfo
	lockInfo    os.FileInfo
}

type newOverviewCreation struct {
	relative string
	info     os.FileInfo
	body     []byte
}

func createNewProjectSyncState(dataRoot *os.Root, projectID string) (newProjectStateCreation, error) {
	if !validProjectID(projectID) {
		return newProjectStateCreation{}, fmt.Errorf("invalid project ID %q", projectID)
	}
	if err := ensurePrivateRootDirectory(dataRoot, "projects"); err != nil {
		return newProjectStateCreation{}, fmt.Errorf("ensure projects state root: %w", err)
	}
	projectsRoot, err := openStableRoot(dataRoot, "projects")
	if err != nil {
		return newProjectStateCreation{}, err
	}
	defer projectsRoot.Close()
	if _, err := projectsRoot.Lstat(projectID); err == nil {
		return newProjectStateCreation{}, fmt.Errorf("generated project state already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return newProjectStateCreation{}, err
	}
	if err := projectsRoot.Mkdir(projectID, 0o700); err != nil {
		return newProjectStateCreation{}, fmt.Errorf("create generated project state: %w", err)
	}
	creation := newProjectStateCreation{projectID: projectID, directories: make(map[string]os.FileInfo, 4)}
	fail := func(cause error) (newProjectStateCreation, error) {
		return newProjectStateCreation{}, errors.Join(cause, rollbackNewProjectSyncState(dataRoot, creation))
	}
	creation.projectInfo, err = projectsRoot.Lstat(projectID)
	if err != nil {
		return fail(err)
	}
	if err := atomicfile.SyncRootDirectory(projectsRoot); err != nil {
		return fail(err)
	}
	projectRoot, err := openStableRoot(projectsRoot, projectID)
	if err != nil {
		return fail(err)
	}
	failProject := func(cause error) (newProjectStateCreation, error) {
		return fail(errors.Join(cause, projectRoot.Close()))
	}
	projectFile, err := projectRoot.Open(".")
	if err != nil {
		return failProject(err)
	}
	if err := projectFile.Chmod(0o700); err != nil {
		_ = projectFile.Close()
		return failProject(err)
	}
	if err := projectFile.Close(); err != nil {
		return failProject(err)
	}
	for _, relative := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := ensurePrivateRootDirectory(projectRoot, relative); err != nil {
			return failProject(fmt.Errorf("ensure project state directory %q: %w", relative, err))
		}
		creation.directories[relative], err = projectRoot.Lstat(relative)
		if err != nil {
			return failProject(err)
		}
	}
	lockRoot, err := openStableRoot(projectRoot, "locks")
	if err != nil {
		return failProject(err)
	}
	lockFile, err := openStableInitLockFile(lockRoot, "sync.lock")
	if err != nil {
		_ = lockRoot.Close()
		return failProject(fmt.Errorf("ensure sync lock: %w", err))
	}
	creation.lockInfo, err = lockFile.Stat()
	err = errors.Join(err, lockFile.Close(), lockRoot.Close())
	if err != nil {
		return failProject(err)
	}
	if err := projectRoot.Close(); err != nil {
		return fail(err)
	}
	return creation, nil
}

func rollbackNewProjectSyncState(dataRoot *os.Root, creation newProjectStateCreation) error {
	if creation.projectID == "" || creation.projectInfo == nil {
		return nil
	}
	projectsRoot, err := openStableRoot(dataRoot, "projects")
	if err != nil {
		return fmt.Errorf("rollback project state: %w", err)
	}
	defer projectsRoot.Close()
	current, err := projectsRoot.Lstat(creation.projectID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !os.SameFile(current, creation.projectInfo) || !sameEntryMode(current, creation.projectInfo) {
		return errors.New("rollback project state changed; refusing removal")
	}
	projectRoot, err := openStableRoot(projectsRoot, creation.projectID)
	if err != nil {
		return fmt.Errorf("rollback project state: %w", err)
	}
	expected := []string{"locks", "merge-bases", "queue", "transactions"}
	if err := requireExactRootEntries(projectRoot, expected); err != nil {
		_ = projectRoot.Close()
		return fmt.Errorf("rollback project state has unexpected content: %w", err)
	}
	for _, name := range []string{"merge-bases", "queue", "transactions"} {
		if err := validateOwnedEmptyDirectory(projectRoot, name, creation.directories[name]); err != nil {
			_ = projectRoot.Close()
			return fmt.Errorf("rollback project state %s changed or has unexpected content: %w", name, err)
		}
	}
	if err := validateOwnedLockDirectory(projectRoot, creation); err != nil {
		_ = projectRoot.Close()
		return err
	}
	lockRoot, err := openStableRoot(projectRoot, "locks")
	if err != nil {
		_ = projectRoot.Close()
		return err
	}
	if err := validateOwnedLockFile(lockRoot, creation.lockInfo); err != nil {
		_ = lockRoot.Close()
		_ = projectRoot.Close()
		return err
	}
	if err := lockRoot.Remove("sync.lock"); err != nil {
		_ = lockRoot.Close()
		_ = projectRoot.Close()
		return fmt.Errorf("remove owned sync lock: %w", err)
	}
	cleanupErr := errors.Join(atomicfile.SyncRootDirectory(lockRoot), lockRoot.Close())
	for _, name := range []string{"locks", "transactions", "queue", "merge-bases"} {
		info, statErr := projectRoot.Lstat(name)
		if statErr != nil || !os.SameFile(info, creation.directories[name]) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("owned state directory %s changed before removal", name))
			break
		}
		if removeErr := projectRoot.Remove(name); removeErr != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove owned state directory %s: %w", name, removeErr))
			break
		}
	}
	cleanupErr = errors.Join(cleanupErr, atomicfile.SyncRootDirectory(projectRoot), projectRoot.Close())
	if cleanupErr != nil {
		return cleanupErr
	}
	current, err = projectsRoot.Lstat(creation.projectID)
	if err != nil || !os.SameFile(current, creation.projectInfo) {
		return errors.New("owned project state changed before removal")
	}
	if err := projectsRoot.Remove(creation.projectID); err != nil {
		return fmt.Errorf("remove owned project state: %w", err)
	}
	return atomicfile.SyncRootDirectory(projectsRoot)
}

func validateOwnedEmptyDirectory(parent *os.Root, name string, want os.FileInfo) error {
	current, err := parent.Lstat(name)
	if err != nil || want == nil || !os.SameFile(current, want) || !sameEntryMode(current, want) {
		return errors.New("directory identity or mode changed")
	}
	root, err := openStableRoot(parent, name)
	if err != nil {
		return err
	}
	defer root.Close()
	return requireExactRootEntries(root, nil)
}

func validateOwnedLockDirectory(projectRoot *os.Root, creation newProjectStateCreation) error {
	if err := validateOwnedDirectoryEntries(projectRoot, "locks", creation.directories["locks"], []string{"sync.lock"}); err != nil {
		return fmt.Errorf("rollback lock directory changed: %w", err)
	}
	lockRoot, err := openStableRoot(projectRoot, "locks")
	if err != nil {
		return err
	}
	defer lockRoot.Close()
	return validateOwnedLockFile(lockRoot, creation.lockInfo)
}

func validateOwnedLockFile(lockRoot *os.Root, want os.FileInfo) error {
	current, err := lockRoot.Lstat("sync.lock")
	if err != nil || want == nil || !os.SameFile(current, want) || !sameEntryMode(current, want) || !current.Mode().IsRegular() || current.Size() != 0 {
		return errors.New("rollback sync lock changed; refusing removal")
	}
	file, err := lockRoot.Open("sync.lock")
	if err != nil {
		return err
	}
	body, readErr := io.ReadAll(io.LimitReader(file, 1))
	opened, statErr := file.Stat()
	closeErr := file.Close()
	after, afterErr := lockRoot.Lstat("sync.lock")
	if readErr != nil || statErr != nil || closeErr != nil || afterErr != nil || len(body) != 0 || !os.SameFile(opened, after) || !os.SameFile(opened, want) {
		return errors.New("rollback sync lock changed while reading")
	}
	return nil
}

func validateOwnedDirectoryEntries(parent *os.Root, name string, want os.FileInfo, entries []string) error {
	current, err := parent.Lstat(name)
	if err != nil || want == nil || !os.SameFile(current, want) || !sameEntryMode(current, want) {
		return errors.New("directory identity or mode changed")
	}
	root, err := openStableRoot(parent, name)
	if err != nil {
		return err
	}
	defer root.Close()
	return requireExactRootEntries(root, entries)
}

func requireExactRootEntries(root *os.Root, want []string) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	slices.Sort(got)
	want = append([]string(nil), want...)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		return fmt.Errorf("entries=%q want=%q", got, want)
	}
	return nil
}

func sameEntryMode(first, second os.FileInfo) bool {
	return first != nil && second != nil && first.Mode() == second.Mode()
}

func createNewOverview(root *os.Root, body string) (newOverviewCreation, error) {
	if err := ensureRootDirectory(root, "docs", 0o755); err != nil {
		return newOverviewCreation{}, err
	}
	parentRelative := filepath.Join("docs", "session-review")
	if err := ensureRootDirectory(root, parentRelative, 0o755); err != nil {
		return newOverviewCreation{}, err
	}
	parent, err := root.OpenRoot(parentRelative)
	if err != nil {
		return newOverviewCreation{}, err
	}
	defer parent.Close()
	const name = "project-overview.md"
	file, err := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return newOverviewCreation{}, fmt.Errorf("create new project overview: %w", err)
	}
	creation := newOverviewCreation{relative: filepath.Join(parentRelative, name), body: []byte(body)}
	creation.info, err = file.Stat()
	if err == nil {
		err = file.Chmod(0o644)
	}
	if err == nil {
		creation.info, err = file.Stat()
	}
	if err == nil {
		var written int
		written, err = file.Write(creation.body)
		if err == nil && written != len(creation.body) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err == nil {
		err = atomicfile.SyncRootPublication(parent, name)
	}
	return creation, err
}

func rollbackNewOverview(root *os.Root, creation newOverviewCreation) error {
	if creation.relative == "" || creation.info == nil {
		return nil
	}
	current, err := root.Lstat(creation.relative)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !os.SameFile(current, creation.info) || !sameEntryMode(current, creation.info) {
		return errors.New("overview changed after initialization write; refusing rollback")
	}
	body, found, err := readOverviewFile(root, creation.relative)
	if err != nil || !found || !bytes.Equal(body, creation.body) {
		return errors.New("overview changed after initialization write; refusing rollback")
	}
	after, err := root.Lstat(creation.relative)
	if err != nil || !os.SameFile(after, creation.info) {
		return errors.New("overview changed before rollback; refusing removal")
	}
	parent, err := root.OpenRoot(filepath.Dir(creation.relative))
	if err != nil {
		return err
	}
	defer parent.Close()
	current, err = parent.Lstat(filepath.Base(creation.relative))
	if err != nil || !os.SameFile(current, creation.info) || !sameEntryMode(current, creation.info) || current.Size() != int64(len(creation.body)) {
		return errors.New("overview changed before rollback removal; refusing removal")
	}
	if err := parent.Remove(filepath.Base(creation.relative)); err != nil {
		return fmt.Errorf("remove owned project overview: %w", err)
	}
	return atomicfile.SyncRootDirectory(parent)
}

func configContainsProject(root *os.Root, mapping config.ProjectMapping) bool {
	cfg, err := config.LoadRoot(root, "config.toml")
	if err != nil {
		return false
	}
	got, found := cfg.ProjectByID(mapping.ID)
	return found && got.Root == mapping.Root && got.VaultRoot == mapping.VaultRoot && got.VaultReviewPath == mapping.VaultReviewPath && got.VaultCaseMode == mapping.VaultCaseMode
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
