package project

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/platform"
)

type InitOptions struct {
	ProjectRoot string
	VaultRoot   string
	DataDir     string
	GOOS        string
	Now         func() time.Time
	Random      io.Reader
}

type InitResult struct {
	ProjectID  string
	LedgerRoot string
	ConfigPath string
}

func Initialize(opts InitOptions) (InitResult, error) {
	root, err := filepath.Abs(opts.ProjectRoot)
	if err != nil {
		return InitResult{}, err
	}
	vault, err := filepath.Abs(opts.VaultRoot)
	if err != nil {
		return InitResult{}, err
	}
	if err := rejectRedirectedRoot(opts.GOOS, root); err != nil {
		return InitResult{}, err
	}
	if err := rejectRedirectedRoot(opts.GOOS, vault); err != nil {
		return InitResult{}, err
	}
	if inside(opts.GOOS, root, vault) || inside(opts.GOOS, vault, root) {
		return InitResult{}, fmt.Errorf("project and vault must not contain one another")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}

	configPath := filepath.Join(opts.DataDir, "config.toml")
	cfg, err := config.Load(configPath)
	if err != nil {
		return InitResult{}, err
	}
	ledger := filepath.Join(root, "docs", "session-review")
	if existing, ok := cfg.FindProject(opts.GOOS, root); ok {
		return InitResult{ProjectID: existing.ID, LedgerRoot: ledger, ConfigPath: configPath}, nil
	}

	raw := make([]byte, 8)
	if _, err := io.ReadFull(opts.Random, raw); err != nil {
		return InitResult{}, err
	}
	id := "project-" + hex.EncodeToString(raw)
	if err := os.MkdirAll(ledger, 0o755); err != nil {
		return InitResult{}, err
	}
	body := fmt.Sprintf(
		"---\nproject_id: %s\ncreated_at: %s\n---\n\n# %s\n",
		id,
		opts.Now().UTC().Format(time.RFC3339),
		filepath.Base(root),
	)
	if err := atomicfile.Write(filepath.Join(ledger, "project-overview.md"), []byte(body), 0o644); err != nil {
		return InitResult{}, err
	}
	cfg.Projects = append(cfg.Projects, config.ProjectMapping{ID: id, Root: root, VaultRoot: vault})
	if err := config.Save(configPath, cfg); err != nil {
		return InitResult{}, err
	}
	return InitResult{ProjectID: id, LedgerRoot: ledger, ConfigPath: configPath}, nil
}

func rejectRedirectedRoot(goos, root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("root %q is a symlink or reparse point", root)
	}
	evaluated, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	evaluatedParent, err := filepath.EvalSymlinks(filepath.Dir(root))
	if err != nil {
		return err
	}
	expected := filepath.Join(evaluatedParent, filepath.Base(root))
	if platform.NormalizePath(goos, evaluated) != platform.NormalizePath(goos, expected) {
		return fmt.Errorf("root %q is a symlink or reparse point", root)
	}
	return nil
}

func inside(goos, parent, child string) bool {
	parent = platform.NormalizePath(goos, parent)
	child = platform.NormalizePath(goos, child)
	if parent == child {
		return false
	}
	separator := string(filepath.Separator)
	if goos == "windows" {
		separator = "/"
	}
	prefix := strings.TrimSuffix(parent, separator) + separator
	return strings.HasPrefix(child, prefix)
}
