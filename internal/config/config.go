package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/pelletier/go-toml/v2"
)

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

func SaveRoot(root *os.Root, name string, cfg Config) error {
	if err := validate(cfg); err != nil {
		return err
	}
	b, err := toml.Marshal(cfg)
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
