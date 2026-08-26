package project

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

const (
	initTransactionLockTimeout = 10 * time.Second
	initialReviewV2JournalPath = "docs/session-review/.session-reviewer/init-v2.json"
)

var (
	ErrInvalidInitializationRoot            = errors.New("initialization root is invalid or missing")
	ErrNestedInitializationRoots            = errors.New("project and vault must not contain one another")
	ErrCorruptInitializationConfig          = errors.New("initialization configuration is invalid")
	ErrConflictingInitializationIdentity    = errors.New("initialization identity conflicts with existing state")
	ErrInitializationStateChanged           = errors.New("initialization state changed")
	ErrInitializationConfigRollbackConflict = errors.New("initialization config rollback requires manual recovery")
)

type InitOptions struct {
	ProjectRoot           string
	VaultRoot             string
	DataDir               string
	GOOS                  string
	Now                   func() time.Time
	Random                io.Reader
	beforeOverviewWrite   func() error
	afterOverviewWrite    func() error
	afterReviewV2File     func(string) error
	afterStateComponent   func(string) error
	beforeConfigWrite     func() error
	afterConfigWrite      func() error
	beforeResultRootCheck func() error
	afterLock             func() error
	caseDetector          func(*os.Root) (platform.CaseMode, error)
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

type initializationConfigFileSnapshot struct {
	body  []byte
	hash  string
	info  os.FileInfo
	found bool
}

type initializationConfigPublication struct {
	root     *os.Root
	rootInfo os.FileInfo
	before   initializationConfigFileSnapshot
	next     initializationConfigFileSnapshot
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
	lock, err := AcquireProjectLock(dataDir.Root, "config.toml.lock", initTransactionLockTimeout)
	if err != nil {
		return InitResult{}, initializationError(ErrInitializationStateChanged, err)
	}
	defer func() {
		retErr = errors.Join(retErr, lock.Release())
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
	originalConfigFile, err := captureInitializationConfigFile(dataDir.Root, "config.toml")
	if err != nil {
		return InitResult{}, initializationError(ErrCorruptInitializationConfig, err)
	}
	if err := recoverInitialReviewV2(roots.project.Root, paths.projectRoot, opts.afterReviewV2File); err != nil {
		return InitResult{}, initializationError(ErrConflictingInitializationIdentity, err)
	}
	overviewPath := filepath.ToSlash(filepath.Join("docs", "session-review", "project-overview.md"))
	overview, err := readOverview(roots.project.Root, overviewPath)
	if err != nil {
		return InitResult{}, initializationError(ErrConflictingInitializationIdentity, err)
	}
	overviewID, overviewExists := overview.projectID, overview.exists
	v2ID, v2Exists, err := readInitializedV2Identity(paths.projectRoot, roots.project.Info(), roots.project.Root)
	if err != nil {
		return InitResult{}, initializationError(ErrConflictingInitializationIdentity, err)
	}
	if overviewExists && v2Exists {
		return InitResult{}, initializationError(ErrConflictingInitializationIdentity, errors.New("project contains both legacy and v2 review state"))
	}
	publishOverviewUpgrade := func() error {
		if len(overview.publish) == 0 {
			return nil
		}
		if err := atomicfile.WriteRoot(roots.project.Root, filepath.FromSlash(overviewPath), overview.publish, 0o644); err != nil {
			return initializationError(ErrConflictingInitializationIdentity, err)
		}
		overview.publish = nil
		return nil
	}
	existing, mapped, err := findProject(cfg, opts.GOOS, paths.projectRoot, roots.project.Info())
	if err != nil {
		return InitResult{}, initializationError(ErrConflictingInitializationIdentity, err)
	}
	if mapped {
		var publication *initializationConfigPublication
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
		if v2Exists && v2ID != existing.ID {
			return InitResult{}, initializationError(ErrConflictingInitializationIdentity, fmt.Errorf("review v2 ID %q does not match mapped ID %q", v2ID, existing.ID))
		}
		updated, changed, err := completeVaultMapping(opts, roots.vault.Root, existing, filepath.Base(paths.projectRoot))
		if err != nil {
			return InitResult{}, err
		}
		if err := publishOverviewUpgrade(); err != nil {
			return InitResult{}, err
		}
		if err := ensureProjectSyncState(dataDir.Root, updated.ID); err != nil {
			return InitResult{}, err
		}
		if !overviewExists && !v2Exists {
			if opts.beforeOverviewWrite != nil {
				if err := opts.beforeOverviewWrite(); err != nil {
					return InitResult{}, err
				}
			}
			if err := writeInitialReviewV2(roots.project.Root, paths.projectRoot, updated.ID, opts.Now(), opts.afterReviewV2File); err != nil {
				return InitResult{}, err
			}
		}
		if changed {
			replaceProjectMapping(&cfg, updated)
			published, err := publishInitializationConfig(opts, dataDir.Root, paths.projectRoot, roots.project.Info(), originalConfigFile, cfg)
			if err != nil {
				return InitResult{}, err
			}
			publication = &published
		}
		return verifiedInitializationResult(opts, paths, roots.project.Info(), updated.ID, publication)
	}
	if v2Exists {
		if owner, claimed := cfg.ProjectByID(v2ID); claimed {
			return InitResult{}, initializationError(ErrConflictingInitializationIdentity, fmt.Errorf("project ID %q already belongs to another project root %q", v2ID, owner.Root))
		}
		mapping, _, err := completeVaultMapping(opts, roots.vault.Root, config.ProjectMapping{ID: v2ID, Root: paths.projectRoot, VaultRoot: paths.vaultRoot}, filepath.Base(paths.projectRoot))
		if err != nil {
			return InitResult{}, err
		}
		if err := ensureExactInitializationScaffold(dataDir.Root, mapping.ID, true, opts.afterStateComponent); err != nil {
			return InitResult{}, err
		}
		cfg.Projects = append(cfg.Projects, mapping)
		publication, err := publishInitializationConfig(opts, dataDir.Root, paths.projectRoot, roots.project.Info(), originalConfigFile, cfg)
		if err != nil {
			return InitResult{}, err
		}
		return verifiedInitializationResult(opts, paths, roots.project.Info(), v2ID, &publication)
	}
	if overviewExists {
		if owner, claimed := cfg.ProjectByID(overviewID); claimed {
			return InitResult{}, initializationError(ErrConflictingInitializationIdentity, fmt.Errorf("project ID %q already belongs to another project root %q", overviewID, owner.Root))
		}
		mapping, _, err := completeVaultMapping(opts, roots.vault.Root, config.ProjectMapping{ID: overviewID, Root: paths.projectRoot, VaultRoot: paths.vaultRoot}, filepath.Base(paths.projectRoot))
		if err != nil {
			return InitResult{}, err
		}
		if err := publishOverviewUpgrade(); err != nil {
			return InitResult{}, err
		}
		if err := ensureExactInitializationScaffold(dataDir.Root, mapping.ID, true, opts.afterStateComponent); err != nil {
			return InitResult{}, err
		}
		cfg.Projects = append(cfg.Projects, mapping)
		publication, err := publishInitializationConfig(opts, dataDir.Root, paths.projectRoot, roots.project.Info(), originalConfigFile, cfg)
		if err != nil {
			return InitResult{}, err
		}
		return verifiedInitializationResult(opts, paths, roots.project.Info(), overviewID, &publication)
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
	if err := writeInitialReviewV2(roots.project.Root, paths.projectRoot, id, opts.Now(), opts.afterReviewV2File); err != nil {
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
	publication, err := publishInitializationConfig(opts, dataDir.Root, paths.projectRoot, roots.project.Info(), originalConfigFile, cfg)
	if err != nil {
		return InitResult{}, err
	}
	return verifiedInitializationResult(opts, paths, roots.project.Info(), id, &publication)
}

func publishInitializationConfig(opts InitOptions, dataRoot *os.Root, logicalProjectPath string, pinned os.FileInfo, before initializationConfigFileSnapshot, next config.Config) (initializationConfigPublication, error) {
	rootInfo, err := dataRoot.Stat(".")
	if err != nil || !rootInfo.IsDir() {
		return initializationConfigPublication{}, errors.New("initialization config root is unavailable")
	}
	if opts.beforeConfigWrite != nil {
		if err := opts.beforeConfigWrite(); err != nil {
			return initializationConfigPublication{}, err
		}
	}
	if err := verifyLiveInitializationProject(logicalProjectPath, pinned); err != nil {
		return initializationConfigPublication{}, err
	}
	if err := config.SaveRoot(dataRoot, "config.toml", next); err != nil {
		return initializationConfigPublication{}, err
	}
	nextFile, err := captureInitializationConfigFile(dataRoot, "config.toml")
	if err != nil || !nextFile.found {
		return initializationConfigPublication{}, errors.New("published initialization config cannot be authenticated")
	}
	publication := initializationConfigPublication{root: dataRoot, rootInfo: rootInfo, before: before, next: nextFile}
	if opts.afterConfigWrite != nil {
		if err := opts.afterConfigWrite(); err != nil {
			return publication, err
		}
	}
	if err := verifyLiveInitializationProject(logicalProjectPath, pinned); err != nil {
		return publication, errors.Join(err, publication.rollback())
	}
	return publication, nil
}

func captureInitializationConfigFile(root *os.Root, name string) (initializationConfigFileSnapshot, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return initializationConfigFileSnapshot{}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return initializationConfigFileSnapshot{}, errors.New("initialization config file is unavailable or unsafe")
	}
	body, err := pathguard.ReadStableRegularRootFile(root, name, info, 4<<20)
	if err != nil {
		return initializationConfigFileSnapshot{}, errors.New("initialization config changed while reading")
	}
	digest := sha256.Sum256(body)
	return initializationConfigFileSnapshot{body: body, hash: hex.EncodeToString(digest[:]), info: info, found: true}, nil
}

func sameInitializationConfigFile(current, expected initializationConfigFileSnapshot) bool {
	if current.found != expected.found {
		return false
	}
	if !current.found {
		return true
	}
	return current.info != nil && expected.info != nil && os.SameFile(current.info, expected.info) &&
		current.hash == expected.hash && current.info.Size() == expected.info.Size() &&
		current.info.Mode() == expected.info.Mode() && current.info.ModTime().Equal(expected.info.ModTime()) &&
		bytes.Equal(current.body, expected.body)
}

func (publication initializationConfigPublication) rollback() error {
	if publication.root == nil || publication.rootInfo == nil {
		return ErrInitializationConfigRollbackConflict
	}
	currentRoot, err := publication.root.Stat(".")
	if err != nil || !os.SameFile(publication.rootInfo, currentRoot) {
		return ErrInitializationConfigRollbackConflict
	}
	current, err := captureInitializationConfigFile(publication.root, "config.toml")
	if err != nil || !sameInitializationConfigFile(current, publication.next) {
		return ErrInitializationConfigRollbackConflict
	}
	if !publication.before.found {
		if err := atomicfile.RemoveRootFileIfHashMatches(publication.root, "config.toml", publication.next.hash); err != nil {
			return errors.Join(ErrInitializationConfigRollbackConflict, err)
		}
		return nil
	}
	checks := 0
	checkpoint := func() error {
		checks++
		if checks > 2 {
			return nil
		}
		current, err := captureInitializationConfigFile(publication.root, "config.toml")
		if err != nil || !sameInitializationConfigFile(current, publication.next) {
			return ErrInitializationConfigRollbackConflict
		}
		return nil
	}
	if err := atomicfile.WriteRootFileChecked(publication.root, "config.toml", publication.before.body, publication.before.info.Mode().Perm(), checkpoint); err != nil {
		return errors.Join(ErrInitializationConfigRollbackConflict, err)
	}
	restored, err := captureInitializationConfigFile(publication.root, "config.toml")
	if err != nil || !restored.found || restored.hash != publication.before.hash || restored.info.Mode() != publication.before.info.Mode() || !bytes.Equal(restored.body, publication.before.body) {
		return ErrInitializationConfigRollbackConflict
	}
	return nil
}

func verifyLiveInitializationProject(logicalPath string, pinned os.FileInfo) error {
	live, err := pathguard.Open(logicalPath)
	if err != nil {
		return initializationError(ErrInitializationStateChanged, errors.New("project root logical path is unavailable or unsafe"))
	}
	defer live.Close()
	if !os.SameFile(pinned, live.Info()) {
		return initializationError(ErrInitializationStateChanged, errors.New("project root identity changed during initialization"))
	}
	return nil
}

func verifiedInitializationResult(opts InitOptions, paths initializationPaths, pinned os.FileInfo, projectID string, publication *initializationConfigPublication) (InitResult, error) {
	var checkpointErr error
	if opts.beforeResultRootCheck != nil {
		checkpointErr = opts.beforeResultRootCheck()
	}
	if checkpointErr == nil {
		checkpointErr = verifyLiveInitializationProject(paths.projectRoot, pinned)
	}
	if checkpointErr != nil {
		if publication != nil {
			checkpointErr = errors.Join(checkpointErr, publication.rollback())
		}
		return InitResult{}, checkpointErr
	}
	return InitResult{ProjectID: projectID, LedgerRoot: paths.ledgerRoot, ConfigPath: paths.configPath}, nil
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
	projectInfo, err := projectsRoot.Lstat(projectID)
	if err != nil {
		return err
	}
	projectRoot, err := openStableRoot(projectsRoot, projectID)
	if err != nil {
		return err
	}
	defer projectRoot.Close()
	if err := requireAllowedRootEntries(projectRoot, initializationStateComponents); err != nil {
		return fmt.Errorf("initialization scaffold has unexpected content: %w", err)
	}

	componentInfos := make(map[string]os.FileInfo, len(initializationStateComponents))
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
		info, err = projectRoot.Lstat(relative)
		if err != nil {
			return err
		}
		componentInfos[relative] = info
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
	return revalidateExactInitializationScaffold(projectsRoot, projectID, projectInfo, componentInfos, lockInfo)
}

func revalidateExactInitializationScaffold(projectsRoot *os.Root, projectID string, projectInfo os.FileInfo, componentInfos map[string]os.FileInfo, lockInfo os.FileInfo) error {
	currentProject, err := projectsRoot.Lstat(projectID)
	if err != nil || !sameStableEntry(currentProject, projectInfo) {
		return errors.New("initialization scaffold root changed before final validation")
	}
	if err := validateStrictPrivateDirectory(projectsRoot, projectID); err != nil {
		return fmt.Errorf("invalid initialization scaffold root during final validation: %w", err)
	}
	projectRoot, err := openStableRoot(projectsRoot, projectID)
	if err != nil {
		return err
	}
	defer projectRoot.Close()
	if err := requireExactRootEntriesBounded(projectRoot, initializationStateComponents); err != nil {
		return fmt.Errorf("initialization scaffold has unexpected content during final validation: %w", err)
	}

	for _, relative := range initializationStateComponents {
		current, err := projectRoot.Lstat(relative)
		if err != nil || !sameStableEntry(current, componentInfos[relative]) {
			return fmt.Errorf("initialization scaffold component %q identity or mode changed before final validation", relative)
		}
		if err := validateStrictPrivateDirectory(projectRoot, relative); err != nil {
			return fmt.Errorf("invalid initialization scaffold component %q during final validation: %w", relative, err)
		}
		componentRoot, err := openStableRoot(projectRoot, relative)
		if err != nil {
			return err
		}
		want := []string(nil)
		if relative == "locks" {
			want = []string{"sync.lock"}
		}
		if err := requireExactRootEntriesBounded(componentRoot, want); err != nil {
			_ = componentRoot.Close()
			return fmt.Errorf("initialization scaffold component %q has unexpected content during final validation: %w", relative, err)
		}
		if relative == "locks" {
			currentLock, err := componentRoot.Lstat("sync.lock")
			if err != nil || !sameStableEntry(currentLock, lockInfo) {
				_ = componentRoot.Close()
				return errors.New("initialization sync lock changed before final validation")
			}
			if err := validateExactEmptyLock(componentRoot, currentLock); err != nil {
				_ = componentRoot.Close()
				return err
			}
		}
		if err := componentRoot.Close(); err != nil {
			return err
		}
		after, err := projectRoot.Lstat(relative)
		if err != nil || !sameStableEntry(after, componentInfos[relative]) {
			return fmt.Errorf("initialization scaffold component %q identity or mode changed during final validation", relative)
		}
	}
	afterProject, err := projectsRoot.Lstat(projectID)
	if err != nil || !sameStableEntry(afterProject, projectInfo) {
		return errors.New("initialization scaffold root changed during final validation")
	}
	return nil
}

func sameStableEntry(current, expected os.FileInfo) bool {
	return current != nil && expected != nil && os.SameFile(current, expected) && current.Mode() == expected.Mode()
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
	entries, readErr := directory.ReadDir(limit + 1)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	visible := make([]os.DirEntry, 0, len(entries))
	lockSeen := false
	for _, entry := range entries {
		if atomicfile.IsRootDirectoryLockName(entry.Name()) {
			if lockSeen || atomicfile.ValidateRootDirectoryLock(root, entry.Name()) != nil {
				return nil, errors.New("initialization directory lock artifact is unsafe")
			}
			lockSeen = true
			continue
		}
		if atomicfile.IsRootDirectoryLockLikeName(entry.Name()) {
			return nil, errors.New("initialization directory lock artifact is unsafe")
		}
		visible = append(visible, entry)
	}
	return visible, nil
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

func readInitializedV2Identity(projectRoot string, expectedRoot os.FileInfo, root *os.Root) (string, bool, error) {
	paths := []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath}
	found := 0
	for _, relative := range paths {
		if _, err := root.Lstat(filepath.FromSlash(relative)); err == nil {
			found++
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", false, err
		}
	}
	if found == 0 {
		return "", false, nil
	}
	if found != len(paths) {
		return "", false, errors.New("review v2 initialization is incomplete")
	}
	accepted, err := reviewv2.LoadExpected(projectRoot, expectedRoot)
	if err != nil {
		return "", false, err
	}
	return accepted.State.Review.ProjectID, true, nil
}

type initialReviewV2Journal struct {
	Version     int    `json:"version"`
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	CreatedAt   string `json:"created_at"`
}

type initialReviewV2File struct {
	path string
	body []byte
	mode os.FileMode
}

func writeInitialReviewV2(root *os.Root, projectRoot, projectID string, now time.Time, afterFile func(string) error) error {
	journal := initialReviewV2Journal{
		Version: 1, ProjectID: projectID, ProjectName: filepath.Base(projectRoot),
		CreatedAt: now.UTC().Format(time.RFC3339Nano),
	}
	if err := validateInitialReviewV2Journal(journal, projectRoot); err != nil {
		return err
	}
	body, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := ensureRootDirectory(root, "docs", 0o755); err != nil {
		return err
	}
	if err := ensureRootDirectory(root, filepath.Join("docs", "session-review"), 0o755); err != nil {
		return err
	}
	if err := ensureRootDirectory(root, filepath.Join("docs", "session-review", ".session-reviewer"), 0o700); err != nil {
		return err
	}
	if err := writeInitialReviewV2FileIfAbsent(root, initialReviewV2JournalPath, body, 0o600); err != nil {
		return err
	}
	return completeInitialReviewV2(root, journal, afterFile)
}

func recoverInitialReviewV2(root *os.Root, projectRoot string, afterFile func(string) error) error {
	info, err := root.Lstat(filepath.FromSlash(initialReviewV2JournalPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("review v2 initialization journal path is redirected or invalid: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return errors.New("review v2 initialization journal is invalid")
	}
	body, err := pathguard.ReadStableRegularRootFile(root, filepath.FromSlash(initialReviewV2JournalPath), info, 4096)
	if err != nil {
		return errors.New("review v2 initialization journal changed while reading")
	}
	var journal initialReviewV2Journal
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return errors.New("review v2 initialization journal is invalid")
	}
	canonical, err := json.Marshal(journal)
	if err != nil || !bytes.Equal(body, append(canonical, '\n')) || validateInitialReviewV2Journal(journal, projectRoot) != nil {
		return errors.New("review v2 initialization journal is invalid")
	}
	return completeInitialReviewV2(root, journal, afterFile)
}

func validateInitialReviewV2Journal(journal initialReviewV2Journal, projectRoot string) error {
	if journal.Version != 1 || !validProjectID(journal.ProjectID) || journal.ProjectName != filepath.Base(projectRoot) {
		return errors.New("review v2 initialization journal identity is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, journal.CreatedAt); err != nil {
		return errors.New("review v2 initialization journal timestamp is invalid")
	}
	return nil
}

func completeInitialReviewV2(root *os.Root, journal initialReviewV2Journal, afterFile func(string) error) error {
	createdAt, err := time.Parse(time.RFC3339Nano, journal.CreatedAt)
	if err != nil {
		return err
	}
	files, err := renderInitialReviewV2(journal.ProjectName, journal.ProjectID, createdAt)
	if err != nil {
		return err
	}
	for _, file := range files {
		relative := filepath.FromSlash(file.path)
		info, statErr := root.Lstat(relative)
		if statErr == nil {
			if !info.Mode().IsRegular() || info.Size() > int64(len(file.body))+1 {
				return fmt.Errorf("partial review v2 file %s is invalid", file.path)
			}
			existing, readErr := pathguard.ReadStableRegularRootFile(root, relative, info, int64(len(file.body))+1)
			if readErr != nil || !bytes.Equal(existing, file.body) || !initialReviewV2ModeMatches(runtime.GOOS, info.Mode(), file.mode) {
				return fmt.Errorf("partial review v2 file %s does not match its initialization journal", file.path)
			}
		} else if errors.Is(statErr, os.ErrNotExist) {
			if err := writeInitialReviewV2FileIfAbsent(root, file.path, file.body, file.mode); err != nil {
				return err
			}
			if afterFile != nil {
				if err := afterFile(file.path); err != nil {
					return err
				}
			}
		} else {
			return statErr
		}
	}
	if err := atomicfile.RemoveRoot(root, filepath.FromSlash(initialReviewV2JournalPath)); err != nil {
		return err
	}
	return nil
}

func initialReviewV2ModeMatches(goos string, got, want os.FileMode) bool {
	if goos == "windows" {
		// Windows exposes writable regular files as 0666 regardless of the
		// Unix-style creation mode. The write bit still distinguishes a
		// read-only replacement, so retain that safety boundary.
		return got.Perm()&0o222 != 0
	}
	return got.Perm() == want.Perm()
}

func writeInitialReviewV2FileIfAbsent(root *os.Root, relative string, body []byte, mode os.FileMode) error {
	clean := filepath.FromSlash(relative)
	parent, err := root.OpenRoot(filepath.Dir(clean))
	if err != nil {
		return err
	}
	defer parent.Close()
	return atomicfile.WriteRootFileCreateIfAbsent(parent, filepath.Base(clean), body, mode, nil)
}

func renderInitialReviewV2(projectName, projectID string, now time.Time) ([]initialReviewV2File, error) {
	legacy := ledger.State{
		ProjectID: projectID,
		CurrentState: ledger.CurrentState{
			ProjectID: projectID, Revision: 1,
			Goal: "在这里记录项目目标。", Branch: "初始化", NextAction: "准备第一次 session review。",
			LastVerified: now.UTC().Format(time.RFC3339), LastUpdated: now.UTC().Format(time.RFC3339),
		},
		Decisions: map[string]ledger.Decision{}, OpenLoops: map[string]ledger.OpenLoop{}, Sessions: map[string]ledger.SessionReport{},
	}
	state, err := reviewv2.ProjectLegacy(legacy)
	if err != nil {
		return nil, err
	}
	state.Review.Name = projectName
	reviewBody, err := reviewv2.RenderReview(state.Review)
	if err != nil {
		return nil, err
	}
	historyBody, err := reviewv2.RenderHistory(projectID, state.Review.Revision, state.Events)
	if err != nil {
		return nil, err
	}
	reviewHash := sha256.Sum256(reviewBody)
	historyHash := sha256.Sum256(historyBody)
	state.Machine.ReviewSHA256 = hex.EncodeToString(reviewHash[:])
	state.Machine.HistorySHA256 = hex.EncodeToString(historyHash[:])
	machineBody, err := reviewv2.RenderMachineLedger(state.Machine)
	if err != nil {
		return nil, err
	}
	return []initialReviewV2File{
		{reviewv2.HistoryRelativePath, historyBody, 0o644},
		{reviewv2.MachineLedgerRelativePath, machineBody, 0o600},
		{reviewv2.ReviewRelativePath, reviewBody, 0o644},
	}, nil
}

type overviewRead struct {
	projectID string
	exists    bool
	publish   []byte
}

func readOverview(root *os.Root, overview string) (overviewRead, error) {
	primaryBody, primaryFound, primaryReadErr := readOverviewFile(root, overview)
	primaryID, primaryMigrated, primaryParseErr := parseOverview(overview, primaryBody, primaryFound)
	backupBody, backupFound, backupReadErr := readOverviewFile(root, atomicfile.BackupPath(overview))
	backupID, backupMigrated, backupParseErr := parseOverview(overview, backupBody, backupFound)
	if primaryFound && primaryReadErr == nil && primaryParseErr == nil {
		return overviewRead{projectID: primaryID, exists: true, publish: primaryMigrated}, nil
	}
	if backupFound && backupReadErr == nil && backupParseErr == nil {
		publish := backupMigrated
		if len(publish) == 0 {
			publish = bytes.Clone(backupBody)
		}
		return overviewRead{projectID: backupID, exists: true, publish: publish}, nil
	}
	if !primaryFound && !backupFound {
		return overviewRead{}, nil
	}
	return overviewRead{}, fmt.Errorf("project overview or its parent is redirected or invalid")
}

func parseOverview(relativePath string, body []byte, found bool) (string, []byte, error) {
	if !found {
		return "", nil, nil
	}
	doc, err := syncdoc.Parse(relativePath, body)
	if err != nil {
		return "", nil, err
	}
	units := doc.Units()
	reserved := []struct {
		name string
		want any
	}{
		{"id", "project-overview"},
		{"entity_type", "project_overview"},
		{"revision", 1},
		{"sync_status", "synced"},
	}
	changed := false
	for _, field := range reserved {
		key := syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: field.name}
		unit, ok := units[key]
		if ok && unit.Present {
			var value any
			if err := yaml.Unmarshal(unit.Value, &value); err != nil || !reflectScalarEqual(value, field.want) {
				return "", nil, fmt.Errorf("project overview contains conflicting reserved identity")
			}
			continue
		}
		value, err := yaml.Marshal(field.want)
		if err != nil {
			return "", nil, err
		}
		units[key] = syncdoc.Unit{Present: true, Value: value}
		changed = true
	}
	updated, err := doc.WithUnits(units)
	if err != nil {
		return "", nil, err
	}
	identity, err := updated.Identity()
	if err != nil || !validProjectID(identity.ProjectID) {
		return "", nil, fmt.Errorf("project overview contains invalid project ID")
	}
	if !changed {
		return identity.ProjectID, nil, nil
	}
	migrated, err := updated.Render()
	if err != nil {
		return "", nil, err
	}
	return identity.ProjectID, migrated, nil
}

func reflectScalarEqual(got, want any) bool {
	switch expected := want.(type) {
	case string:
		actual, ok := got.(string)
		return ok && actual == expected
	case int:
		actual, ok := got.(int)
		return ok && actual == expected
	default:
		return false
	}
}

func readOverviewFile(root *os.Root, name string) ([]byte, bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, true, fmt.Errorf("invalid project overview")
	}
	body, err := pathguard.ReadStableRegularRootFile(root, name, info, 1<<20)
	if err != nil {
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
