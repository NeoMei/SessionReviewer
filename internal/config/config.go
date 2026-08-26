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
)

const ProjectFragmentsDir = "projects.d"

var ErrProjectFragmentConflict = errors.New("project mapping fragment conflicts with existing state")

type projectFragment struct {
	Project ProjectMapping `toml:"project"`
}

type ProjectMapping struct {
	ID               string            `toml:"id"`
	Root             string            `toml:"root"`
	VaultRoot        string            `toml:"vault_root"`
	VaultReviewPath  string            `toml:"vault_review_path,omitempty"`
	VaultCaseMode    platform.CaseMode `toml:"vault_case_mode,omitempty"`
	RemoteIdentities []string          `toml:"remote_identities,omitempty"`
	CommonDirs       []string          `toml:"common_dirs,omitempty"`
	Aliases          []string          `toml:"aliases,omitempty"`
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
	primary, primaryFound, primaryErr := readConfig(root, name)
	backup, backupFound, backupErr := readConfig(root, atomicfile.BackupPath(name))
	var base Config
	if primaryFound && primaryErr == nil {
		base = primary
	} else if backupFound && backupErr == nil {
		base = backup
	} else if !primaryFound && !backupFound {
		base = Config{Version: 1}
	} else {
		return Config{}, fmt.Errorf("configuration state and recovery backup are invalid")
	}
	fragments, err := loadProjectFragmentsRoot(root)
	if err != nil {
		return Config{}, err
	}
	return mergeProjectFragments(base, fragments)
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
	if err := cleanupConvergedConfigBackup(root, name); err != nil {
		return err
	}
	if err := atomicfile.WriteRoot(root, name, b, 0o600); err != nil {
		return err
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
	if err := atomicfile.EnsureRootDir(root, ProjectFragmentsDir, 0o700); err != nil {
		return false, fmt.Errorf("prepare project fragments directory: %w", err)
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
	err = atomicfile.WriteRootFileCreateIfAbsent(fragmentsRoot, name, body, 0o600, beforePublish)
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
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
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
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
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
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) || info.Size() > 1<<20 {
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
	var fragment projectFragment
	decoder := toml.NewDecoder(bytes.NewReader(body)).DisallowUnknownFields()
	if err := decoder.Decode(&fragment); err != nil {
		return ProjectMapping{}, errors.New("project mapping fragment is invalid")
	}
	if err := validateProjectFragment(fragment.Project); err != nil {
		return ProjectMapping{}, err
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
		(len(legacy.Aliases) == 0 || reflect.DeepEqual(legacy.Aliases, fragment.Aliases))
}

func sharedConfigForSave(root *os.Root, name string, cfg Config) (Config, error) {
	fragments, err := loadProjectFragmentsRoot(root)
	if err != nil {
		return Config{}, err
	}
	if len(fragments) == 0 {
		return cfg, nil
	}
	existing, found, readErr := readConfig(root, name)
	if readErr != nil {
		return Config{}, readErr
	}
	if !found {
		existing = Config{Version: 1}
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

func cleanupConvergedConfigBackup(root *os.Root, name string) error {
	backupName := atomicfile.BackupPath(name)
	if _, err := root.Lstat(backupName); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return errors.New("cannot inspect configuration recovery backup")
	}
	if _, found, err := readConfig(root, name); err == nil && found {
		hash, err := stableConfigHash(root, name)
		if err != nil {
			return err
		}
		if err := atomicfile.RemoveRootFileIfHashMatches(root, backupName, hash); err != nil {
			return errors.New("configuration recovery backup requires explicit resolution")
		}
		return nil
	}
	if _, found, err := readConfig(root, backupName); err != nil || !found {
		return errors.New("configuration recovery backup does not match an authenticated primary")
	}
	hash, err := stableConfigHash(root, backupName)
	if err != nil {
		return err
	}
	if err := atomicfile.RecoverRootFileRollback(root, name, hash); err != nil {
		return errors.New("configuration recovery backup requires explicit resolution")
	}
	return nil
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
	}
	return nil
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
