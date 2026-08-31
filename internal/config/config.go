package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const ProjectFragmentsDir = "projects.d"

var ErrProjectFragmentConflict = errors.New("project mapping fragment conflicts with existing state")

type projectFragment struct {
	Project ProjectMapping `toml:"project"`
}

type ProjectMapping struct {
	ID                   string                      `toml:"id"`
	Root                 string                      `toml:"root"`
	VaultRoot            string                      `toml:"vault_root"`
	VaultReviewPath      string                      `toml:"vault_review_path,omitempty"`
	VaultCaseMode        platform.CaseMode           `toml:"vault_case_mode,omitempty"`
	RemoteIdentities     []string                    `toml:"remote_identities,omitempty"`
	CommonDirs           []string                    `toml:"common_dirs,omitempty"`
	Aliases              []string                    `toml:"aliases,omitempty"`
	AuthenticatedAliases []AuthenticatedProjectAlias `toml:"authenticated_aliases,omitempty"`
}

// AuthenticatedProjectAlias binds an exact historical path spelling to the
// physical project and optional Git common-directory identities observed
// there. SchemaVersion allows the authentication contract to evolve without
// reinterpreting legacy path, remote, or common-directory strings as proof.
type AuthenticatedProjectAlias struct {
	SchemaVersion     int                     `toml:"schema_version"`
	Path              string                  `toml:"path"`
	RootIdentity      pathguard.IdentityToken `toml:"root_identity"`
	CommonDirIdentity string                  `toml:"common_dir_identity,omitempty"`
}

type SessionAssociation struct {
	SessionID string `toml:"session_id"`
	ProjectID string `toml:"project_id"`
}

type Config struct {
	Version             int                  `toml:"version"`
	Projects            []ProjectMapping     `toml:"projects"`
	SessionAssociations []SessionAssociation `toml:"session_associations,omitempty"`
}

// NamespaceSnapshot is an immutable-in-practice copy of the exact files that
// formed one configuration namespace observation. Callers must capture these
// bytes through authenticated handles before parsing them.
type NamespaceSnapshot struct {
	Primary          *FileSnapshot
	Backup           *FileSnapshot
	ProjectFragments []FileSnapshot
}

type FileSnapshot struct {
	Name string
	Body []byte
}

// ValidateNamespaceSnapshotEntry applies the same privacy rules as live
// configuration loading before another package captures an entry's bytes.
func ValidateNamespaceSnapshotEntry(dataPath, name string, info os.FileInfo) error {
	if info == nil || filepath.IsAbs(name) || filepath.Clean(name) != name {
		return errors.New("configuration snapshot entry is invalid")
	}
	full := filepath.Join(dataPath, name)
	switch {
	case name == "config.toml" || name == atomicfile.BackupPath("config.toml"):
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
			return errors.New("configuration snapshot entry is not private")
		}
	case name == ProjectFragmentsDir:
		if info.Mode()&os.ModeSymlink != 0 || !privateProjectFragmentsPath(full, info) {
			return errors.New("project fragment namespace is not private")
		}
	case filepath.Dir(name) == ProjectFragmentsDir:
		if info.Mode()&os.ModeSymlink != 0 || !privateProjectFragmentPath(full, info) {
			return errors.New("project mapping fragment is not private")
		}
	default:
		return errors.New("configuration snapshot entry is outside the namespace")
	}
	return nil
}

func Load(path string) (Config, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return Config{Version: 1}, nil
	}
	if err != nil {
		return Config{}, err
	}
	defer root.Close()
	return LoadRoot(root, filepath.Base(path))
}

func Save(path string, cfg Config) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		root, err = os.OpenRoot(filepath.Dir(path))
	}
	if err != nil {
		return err
	}
	defer root.Close()
	return SaveRoot(root, filepath.Base(path), cfg)
}

// LoadRoot reads a typed transaction snapshot. The destination is authoritative
// when valid; otherwise a valid migration recovery backup is used.
func LoadRoot(root *os.Root, name string) (Config, error) {
	base, err := selectedSharedConfigBase(root, name)
	if err != nil {
		return Config{}, err
	}
	fragments, err := loadProjectFragmentsRoot(root)
	if err != nil {
		return Config{}, err
	}
	return mergeProjectFragments(base, fragments)
}

// LoadNamespaceSnapshot parses only the supplied captured bytes. It performs
// no path lookup, so a namespace rename/restore cannot change the mapping
// between capture and parse.
func LoadNamespaceSnapshot(snapshot NamespaceSnapshot) (Config, error) {
	primary, primaryFound, primaryErr := decodeSharedConfigSnapshot(snapshot.Primary, "config.toml")
	backup, backupFound, backupErr := decodeSharedConfigSnapshot(snapshot.Backup, atomicfile.BackupPath("config.toml"))
	base := Config{Version: 1}
	switch {
	case primaryFound && primaryErr == nil:
		base = primary
	case backupFound && backupErr == nil:
		base = backup
	case !primaryFound && !backupFound:
	default:
		return Config{}, fmt.Errorf("configuration state and recovery backup are invalid")
	}
	fragments := make([]FileSnapshot, len(snapshot.ProjectFragments))
	copy(fragments, snapshot.ProjectFragments)
	sort.Slice(fragments, func(i, j int) bool { return fragments[i].Name < fragments[j].Name })
	mappings := make([]ProjectMapping, 0, len(fragments))
	previous := ""
	for _, file := range fragments {
		if file.Name == "" || file.Name == previous || filepath.Base(file.Name) != file.Name || !strings.HasSuffix(file.Name, ".toml") {
			return Config{}, errors.New("project mapping fragment snapshot is invalid")
		}
		mapping, err := decodeProjectFragmentBytes(file.Name, file.Body)
		if err != nil {
			return Config{}, err
		}
		mappings = append(mappings, mapping)
		previous = file.Name
	}
	return mergeProjectFragments(base, mappings)
}

func decodeSharedConfigSnapshot(file *FileSnapshot, wantName string) (Config, bool, error) {
	if file == nil {
		return Config{}, false, nil
	}
	if file.Name != wantName || file.Body == nil || len(file.Body) > 4<<20 {
		return Config{}, true, errors.New("invalid configuration state")
	}
	var cfg Config
	decoder := toml.NewDecoder(bytes.NewReader(file.Body)).DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, true, errors.New("invalid configuration state")
	}
	if err := validate(cfg); err != nil {
		return Config{}, true, err
	}
	return cfg, true, nil
}

func SaveRoot(root *os.Root, name string, cfg Config) error {
	if err := validate(cfg); err != nil {
		return err
	}
	shared, err := sharedConfigForSave(root, name, cfg)
	if err != nil {
		return err
	}
	b, err := toml.Marshal(shared)
	if err != nil {
		return err
	}
	pendingBackupHash, err := prepareConfigBackupForSave(root, name)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteRoot(root, name, b, 0o600); err != nil {
		return err
	}
	if pendingBackupHash != "" {
		if err := atomicfile.RemoveRootFileIfHashMatches(root, atomicfile.BackupPath(name), pendingBackupHash); err != nil {
			return errors.New("configuration recovery backup requires explicit resolution")
		}
	}
	if _, err := root.Lstat(atomicfile.BackupPath(name)); !errors.Is(err, os.ErrNotExist) {
		return errors.New("configuration recovery backup appeared during save")
	}
	return nil
}

// PublishProjectFragmentRoot durably publishes exactly one project mapping at
// a per-project no-replace path. The callback is the commit-time checkpoint:
// it runs immediately before the kernel no-replace publication operation.
// An existing byte-identical fragment is an idempotent success.
func PublishProjectFragmentRoot(root *os.Root, mapping ProjectMapping, beforePublish func() error) (bool, error) {
	if root == nil {
		return false, errors.New("configuration root is required")
	}
	if err := validateProjectFragment(mapping); err != nil {
		return false, err
	}
	body, err := toml.Marshal(projectFragment{Project: mapping})
	if err != nil {
		return false, err
	}
	if _, err := atomicfile.EnsureRootDirPrepared(root, ProjectFragmentsDir, 0o700, nil, secureProjectFragmentsDirectory); err != nil {
		return false, fmt.Errorf("prepare project fragments directory: %w", err)
	}
	rootInfo, err := root.Stat(".")
	if err != nil || !rootInfo.IsDir() {
		return false, errors.New("configuration root is unavailable")
	}
	fragmentsInfo, err := root.Lstat(ProjectFragmentsDir)
	if err != nil || !privateProjectFragmentsPath(filepath.Join(root.Name(), ProjectFragmentsDir), fragmentsInfo) {
		return false, errors.New("project fragments directory is invalid")
	}
	fragmentsRoot, err := openProjectFragmentsRoot(root)
	if err != nil {
		return false, err
	}
	defer fragmentsRoot.Close()
	name := mapping.ID + ".toml"
	current, err := LoadRoot(root, "config.toml")
	if err != nil {
		return false, err
	}
	if _, err = mergeProjectFragments(current, []ProjectMapping{mapping}); err != nil {
		return false, err
	}
	commitCheck := func() error {
		if beforePublish != nil {
			if err := beforePublish(); err != nil {
				return err
			}
		}
		return verifyLiveProjectFragmentsRoot(root, rootInfo, fragmentsInfo)
	}
	err = atomicfile.WriteRootFileCreateIfAbsentPrepared(fragmentsRoot, name, body, 0o600, secureProjectFragmentFile, commitCheck)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return false, err
	}
	existing, readErr := readProjectFragmentFile(fragmentsRoot, name)
	if readErr != nil || !reflect.DeepEqual(existing, mapping) {
		return false, errors.Join(ErrProjectFragmentConflict, readErr)
	}
	existingBody, readErr := readStableFragmentBytes(fragmentsRoot, name)
	if readErr != nil || !bytes.Equal(existingBody, body) {
		return false, errors.Join(ErrProjectFragmentConflict, readErr)
	}
	return false, nil
}

func loadProjectFragmentsRoot(root *os.Root) ([]ProjectMapping, error) {
	info, err := root.Lstat(ProjectFragmentsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !privateProjectFragmentsPath(filepath.Join(root.Name(), ProjectFragmentsDir), info) {
		return nil, errors.New("project fragments directory is invalid")
	}
	fragmentsRoot, err := root.OpenRoot(ProjectFragmentsDir)
	if err != nil {
		return nil, errors.New("project fragments directory is unavailable")
	}
	defer fragmentsRoot.Close()
	dir, err := fragmentsRoot.Open(".")
	if err != nil {
		return nil, errors.New("project fragments directory cannot be read")
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.New("project fragments directory changed while reading")
	}
	if len(entries) > 4096 {
		return nil, errors.New("too many project mapping fragments")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	fragments := make([]ProjectMapping, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".toml") {
			return nil, fmt.Errorf("invalid project mapping fragment entry %q", name)
		}
		mapping, err := readProjectFragmentFile(fragmentsRoot, name)
		if err != nil {
			return nil, err
		}
		if name != mapping.ID+".toml" {
			return nil, fmt.Errorf("project mapping fragment filename does not match ID")
		}
		fragments = append(fragments, mapping)
	}
	return fragments, nil
}

func openProjectFragmentsRoot(root *os.Root) (*os.Root, error) {
	info, err := root.Lstat(ProjectFragmentsDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !privateProjectFragmentsPath(filepath.Join(root.Name(), ProjectFragmentsDir), info) {
		return nil, errors.New("project fragments directory is invalid")
	}
	opened, err := root.OpenRoot(ProjectFragmentsDir)
	if err != nil {
		return nil, errors.New("project fragments directory is unavailable")
	}
	return opened, nil
}

func readStableFragmentBytes(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil || !privateProjectFragmentPath(filepath.Join(root.Name(), name), info) || info.Size() > 1<<20 {
		return nil, errors.New("project mapping fragment is invalid")
	}
	body, err := pathguard.ReadStableRegularRootFile(root, name, info, 1<<20)
	if err != nil {
		return nil, errors.New("project mapping fragment changed while reading")
	}
	return body, nil
}

func readProjectFragmentFile(root *os.Root, name string) (ProjectMapping, error) {
	body, err := readStableFragmentBytes(root, name)
	if err != nil {
		return ProjectMapping{}, err
	}
	return decodeProjectFragmentBytes(name, body)
}

func decodeProjectFragmentBytes(name string, body []byte) (ProjectMapping, error) {
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".toml") || len(body) > 1<<20 {
		return ProjectMapping{}, errors.New("project mapping fragment is invalid")
	}
	var fragment projectFragment
	decoder := toml.NewDecoder(bytes.NewReader(body)).DisallowUnknownFields()
	if err := decoder.Decode(&fragment); err != nil {
		return ProjectMapping{}, errors.New("project mapping fragment is invalid")
	}
	if err := validateProjectFragment(fragment.Project); err != nil {
		return ProjectMapping{}, err
	}
	if name != fragment.Project.ID+".toml" {
		return ProjectMapping{}, errors.New("project mapping fragment filename does not match ID")
	}
	canonical, err := toml.Marshal(fragment)
	if err != nil || !bytes.Equal(canonical, body) {
		return ProjectMapping{}, errors.New("project mapping fragment is not canonical")
	}
	return fragment.Project, nil
}

func validateProjectFragment(mapping ProjectMapping) error {
	if !isStableProjectID(mapping.ID) {
		return errors.New("project mapping fragment ID is invalid")
	}
	if mapping.Root == "" || mapping.VaultRoot == "" || strings.ContainsRune(mapping.Root, 0) || strings.ContainsRune(mapping.VaultRoot, 0) ||
		!filepath.IsAbs(mapping.Root) || !filepath.IsAbs(mapping.VaultRoot) || filepath.Clean(mapping.Root) != mapping.Root || filepath.Clean(mapping.VaultRoot) != mapping.VaultRoot {
		return errors.New("project mapping fragment roots must be clean absolute paths")
	}
	if err := validateVaultMapping(mapping); err != nil {
		return err
	}
	return nil
}

func isStableProjectID(id string) bool {
	if len(id) != len("project-")+16 || !strings.HasPrefix(id, "project-") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "project-"))
	return err == nil && id == strings.ToLower(id)
}

func mergeProjectFragments(base Config, fragments []ProjectMapping) (Config, error) {
	merged := base
	for _, fragment := range fragments {
		matched := -1
		for i, existing := range merged.Projects {
			if existing.ID == fragment.ID {
				if matched >= 0 || !compatibleLegacyExtension(existing, fragment) {
					return Config{}, ErrProjectFragmentConflict
				}
				matched = i
				continue
			}
			if platform.NormalizePath(runtime.GOOS, existing.Root) == platform.NormalizePath(runtime.GOOS, fragment.Root) {
				return Config{}, ErrProjectFragmentConflict
			}
		}
		if matched >= 0 {
			merged.Projects[matched] = fragment
		} else {
			merged.Projects = append(merged.Projects, fragment)
		}
	}
	if err := validate(merged); err != nil {
		return Config{}, err
	}
	return merged, nil
}

func compatibleLegacyExtension(legacy, fragment ProjectMapping) bool {
	if legacy.ID != fragment.ID || legacy.Root != fragment.Root || legacy.VaultRoot != fragment.VaultRoot {
		return false
	}
	return (legacy.VaultReviewPath == "" || legacy.VaultReviewPath == fragment.VaultReviewPath) &&
		(legacy.VaultCaseMode == "" || legacy.VaultCaseMode == fragment.VaultCaseMode) &&
		(len(legacy.RemoteIdentities) == 0 || reflect.DeepEqual(legacy.RemoteIdentities, fragment.RemoteIdentities)) &&
		(len(legacy.CommonDirs) == 0 || reflect.DeepEqual(legacy.CommonDirs, fragment.CommonDirs)) &&
		(len(legacy.Aliases) == 0 || reflect.DeepEqual(legacy.Aliases, fragment.Aliases)) &&
		(len(legacy.AuthenticatedAliases) == 0 || reflect.DeepEqual(legacy.AuthenticatedAliases, fragment.AuthenticatedAliases))
}

func sharedConfigForSave(root *os.Root, name string, cfg Config) (Config, error) {
	fragments, err := loadProjectFragmentsRoot(root)
	if err != nil {
		return Config{}, err
	}
	if len(fragments) == 0 {
		return cfg, nil
	}
	existing, err := selectedSharedConfigBase(root, name)
	if err != nil {
		return Config{}, err
	}
	fragmentByID := make(map[string]ProjectMapping, len(fragments))
	for _, fragment := range fragments {
		fragmentByID[fragment.ID] = fragment
	}
	next := cfg
	next.Projects = make([]ProjectMapping, 0, len(cfg.Projects))
	seenLegacy := make(map[string]bool)
	for _, mapping := range cfg.Projects {
		fragment, fragmented := fragmentByID[mapping.ID]
		if !fragmented {
			next.Projects = append(next.Projects, mapping)
			continue
		}
		if !reflect.DeepEqual(mapping, fragment) {
			return Config{}, ErrProjectFragmentConflict
		}
		for _, legacy := range existing.Projects {
			if legacy.ID == mapping.ID {
				if !compatibleLegacyExtension(legacy, fragment) {
					return Config{}, ErrProjectFragmentConflict
				}
				next.Projects = append(next.Projects, legacy)
				seenLegacy[legacy.ID] = true
			}
		}
	}
	for _, legacy := range existing.Projects {
		if _, fragmented := fragmentByID[legacy.ID]; fragmented && !seenLegacy[legacy.ID] {
			next.Projects = append(next.Projects, legacy)
		}
	}
	if err := validate(next); err != nil {
		return Config{}, err
	}
	return next, nil
}

// prepareConfigBackupForSave resolves an already-converged backup before the
// write. When the valid backup is the selected source because the primary is
// missing or invalid, it returns the authenticated backup hash so the caller
// can durably replace the primary first and remove only that exact backup.
func prepareConfigBackupForSave(root *os.Root, name string) (string, error) {
	backupName := atomicfile.BackupPath(name)
	if _, err := root.Lstat(backupName); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", errors.New("cannot inspect configuration recovery backup")
	}
	if _, found, err := readConfig(root, name); err == nil && found {
		hash, err := stableConfigHash(root, name)
		if err != nil {
			return "", err
		}
		if err := atomicfile.RemoveRootFileIfHashMatches(root, backupName, hash); err != nil {
			return "", errors.New("configuration recovery backup requires explicit resolution")
		}
		return "", nil
	}
	if _, found, err := readConfig(root, backupName); err != nil || !found {
		return "", errors.New("configuration recovery backup does not match an authenticated primary")
	}
	hash, err := stableConfigHash(root, backupName)
	if err != nil {
		return "", err
	}
	return hash, nil
}

func stableConfigHash(root *os.Root, name string) (string, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return "", errors.New("cannot inspect configuration state for recovery")
	}
	body, err := pathguard.ReadStableRegularRootFile(root, name, info, 4<<20)
	if err != nil {
		return "", errors.New("configuration state changed during recovery")
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func readConfig(root *os.Root, name string) (Config, bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, true, err
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) || info.Size() > 4<<20 {
		return Config{}, true, fmt.Errorf("invalid configuration state")
	}
	b, err := pathguard.ReadStableRegularRootFile(root, name, info, 4<<20)
	if err != nil {
		return Config{}, true, fmt.Errorf("configuration changed while reading")
	}
	var cfg Config
	decoder := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, true, fmt.Errorf("invalid configuration state")
	}
	if err := validate(cfg); err != nil {
		return Config{}, true, err
	}
	return cfg, true, nil
}

func validate(cfg Config) error {
	if cfg.Version != 1 {
		return errors.New("unsupported config version")
	}
	if err := cfg.ValidateProjectIDs(); err != nil {
		return err
	}
	for _, project := range cfg.Projects {
		if err := validateVaultMapping(project); err != nil {
			return fmt.Errorf("project %q: %w", project.ID, err)
		}
		if err := validateAuthenticatedAliases(project.AuthenticatedAliases); err != nil {
			return fmt.Errorf("project %q: %w", project.ID, err)
		}
	}
	if err := validateMappingDestinations(cfg.Projects); err != nil {
		return err
	}
	return nil
}

func validateAuthenticatedAliases(aliases []AuthenticatedProjectAlias) error {
	seenPaths := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		if alias.SchemaVersion != 1 {
			return errors.New("unsupported authenticated alias version")
		}
		if alias.Path == "" || strings.ContainsRune(alias.Path, 0) || !filepath.IsAbs(alias.Path) || filepath.Clean(alias.Path) != alias.Path {
			return errors.New("authenticated alias path must be a clean absolute path")
		}
		if !alias.RootIdentity.Valid() {
			return errors.New("authenticated alias root identity is invalid")
		}
		if alias.CommonDirIdentity != "" && !validIdentityKey(alias.CommonDirIdentity) {
			return errors.New("authenticated alias common-directory identity is invalid")
		}
		key := portableAbsoluteKey(alias.Path, runtime.GOOS == "windows")
		if _, duplicate := seenPaths[key]; duplicate {
			return errors.New("authenticated alias path is duplicated")
		}
		seenPaths[key] = struct{}{}
	}
	return nil
}

func validIdentityKey(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return false
	}
	return (pathguard.IdentityToken{Kind: parts[0], Volume: parts[1], File: parts[2]}).Valid()
}

func selectedSharedConfigBase(root *os.Root, name string) (Config, error) {
	primary, primaryFound, primaryErr := readConfig(root, name)
	backup, backupFound, backupErr := readConfig(root, atomicfile.BackupPath(name))
	if primaryFound && primaryErr == nil {
		return primary, nil
	}
	if backupFound && backupErr == nil {
		return backup, nil
	}
	if !primaryFound && !backupFound {
		return Config{Version: 1}, nil
	}
	return Config{}, fmt.Errorf("configuration state and recovery backup are invalid")
}

func verifyLiveProjectFragmentsRoot(root *os.Root, rootInfo, fragmentsInfo os.FileInfo) error {
	live, err := os.OpenRoot(root.Name())
	if err != nil {
		return errors.New("configuration root changed before fragment publication")
	}
	defer live.Close()
	liveInfo, err := live.Stat(".")
	if err != nil || !os.SameFile(rootInfo, liveInfo) || !liveInfo.IsDir() || liveInfo.Mode() != rootInfo.Mode() {
		return errors.New("configuration root changed before fragment publication")
	}
	liveFragments, err := live.Lstat(ProjectFragmentsDir)
	if err != nil || !os.SameFile(fragmentsInfo, liveFragments) || !privateProjectFragmentsPath(filepath.Join(live.Name(), ProjectFragmentsDir), liveFragments) {
		return errors.New("project fragments directory changed before publication")
	}
	return nil
}

func validateMappingDestinations(projects []ProjectMapping) error {
	for i := range projects {
		for j := 0; j < i; j++ {
			if projects[i].ID == projects[j].ID {
				continue
			}
			if portableAbsoluteKey(projects[i].Root, runtime.GOOS == "windows") == portableAbsoluteKey(projects[j].Root, runtime.GOOS == "windows") {
				return fmt.Errorf("project root is mapped more than once: %q", projects[i].Root)
			}
			if sameVaultDestination(projects[i], projects[j]) {
				return fmt.Errorf("vault destination is mapped more than once: %q", projects[i].VaultReviewPath)
			}
		}
	}
	return nil
}

func sameVaultDestination(left, right ProjectMapping) bool {
	if left.VaultReviewPath == "" || right.VaultReviewPath == "" {
		return false
	}
	insensitive := runtime.GOOS == "windows" || left.VaultCaseMode == platform.CaseInsensitive || right.VaultCaseMode == platform.CaseInsensitive
	if portableAbsoluteKey(left.VaultRoot, insensitive) != portableAbsoluteKey(right.VaultRoot, insensitive) {
		return false
	}
	mode := platform.CaseSensitive
	if insensitive {
		mode = platform.CaseInsensitive
	}
	leftKey, leftErr := platform.PathKey(runtime.GOOS, mode, left.VaultReviewPath)
	rightKey, rightErr := platform.PathKey(runtime.GOOS, mode, right.VaultReviewPath)
	return leftErr == nil && rightErr == nil && leftKey == rightKey
}

func portableAbsoluteKey(value string, insensitive bool) string {
	key := norm.NFC.String(filepath.ToSlash(filepath.Clean(value)))
	if insensitive {
		key = cases.Fold().String(key)
	}
	return key
}

func validateVaultMapping(project ProjectMapping) error {
	pathEmpty := project.VaultReviewPath == ""
	modeEmpty := project.VaultCaseMode == ""
	if pathEmpty != modeEmpty {
		return errors.New("vault review path and case mode must be configured together")
	}
	if pathEmpty {
		return nil
	}
	if project.VaultCaseMode != platform.CaseSensitive && project.VaultCaseMode != platform.CaseInsensitive {
		return fmt.Errorf("invalid vault case mode %q", project.VaultCaseMode)
	}
	if strings.Contains(project.VaultReviewPath, `\`) {
		return errors.New("vault review path must use slash separators")
	}
	if _, err := platform.PathKey("darwin", platform.CaseSensitive, project.VaultReviewPath); err != nil {
		return fmt.Errorf("invalid vault review path: %w", err)
	}
	components := strings.Split(project.VaultReviewPath, "/")
	if len(components) < 3 || components[0] != "Projects" || components[len(components)-1] != "Session Review" {
		return errors.New("vault review path must be below Projects and end in Session Review")
	}
	return nil
}

func (c Config) ValidateProjectIDs() error {
	seen := make(map[string]string, len(c.Projects))
	for _, project := range c.Projects {
		if project.ID == "" {
			return errors.New("configured project ID is empty")
		}
		if firstRoot, found := seen[project.ID]; found {
			return fmt.Errorf("project ID is mapped more than once: %q and %q", firstRoot, project.Root)
		}
		seen[project.ID] = project.Root
	}
	return nil
}

func (c Config) ProjectByID(id string) (ProjectMapping, bool) {
	for _, project := range c.Projects {
		if project.ID == id {
			return project, true
		}
	}
	return ProjectMapping{}, false
}

func (c Config) FindProject(goos, root string) (ProjectMapping, bool) {
	clean := platform.NormalizePath(goos, root)
	for _, project := range c.Projects {
		if platform.NormalizePath(goos, project.Root) == clean {
			return project, true
		}
	}
	return ProjectMapping{}, false
}
