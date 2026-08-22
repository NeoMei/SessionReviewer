package config

import (
	"errors"
	"os"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/pelletier/go-toml/v2"
)

type ProjectMapping struct {
	ID        string `toml:"id"`
	Root      string `toml:"root"`
	VaultRoot string `toml:"vault_root"`
}

type Config struct {
	Version  int              `toml:"version"`
	Projects []ProjectMapping `toml:"projects"`
}

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{Version: 1}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Version != 1 {
		return Config{}, errors.New("unsupported config version")
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	b, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
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
