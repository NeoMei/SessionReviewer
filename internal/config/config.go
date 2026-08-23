package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
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
// when valid; otherwise a valid atomic-replacement backup is used. Any state
// files with no valid snapshot fail closed.
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
	if err := atomicfile.WriteRoot(root, name, b, 0o600); err != nil {
		return err
	}
	if _, err := root.Lstat(atomicfile.BackupPath(name)); err == nil {
		if err := root.Remove(atomicfile.BackupPath(name)); err != nil {
			return fmt.Errorf("remove stale configuration backup: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func readConfig(root *os.Root, name string) (Config, bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, true, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 4<<20 {
		return Config{}, true, fmt.Errorf("invalid configuration state")
	}
	file, err := root.Open(name)
	if err != nil {
		return Config{}, true, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return Config{}, true, fmt.Errorf("configuration changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil {
		return Config{}, true, err
	}
	var cfg Config
	decoder := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, true, fmt.Errorf("invalid configuration state")
	}
	if err := validate(cfg); err != nil {
		return Config{}, true, err
	}
	after, err := root.Lstat(name)
	if err != nil || !os.SameFile(opened, after) {
		return Config{}, true, fmt.Errorf("configuration changed while reading")
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
