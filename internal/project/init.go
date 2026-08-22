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

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
)

const initTransactionLockTimeout = 10 * time.Second

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
}

type InitResult struct {
	ProjectID  string
	LedgerRoot string
	ConfigPath string
}

func Initialize(opts InitOptions) (result InitResult, retErr error) {
	root, err := filepath.Abs(opts.ProjectRoot)
	if err != nil {
		return InitResult{}, err
	}
	vault, err := filepath.Abs(opts.VaultRoot)
	if err != nil {
		return InitResult{}, err
	}
	projectDir, err := pathguard.Open(root)
	if err != nil {
		return InitResult{}, fmt.Errorf("project root is a symlink or reparse point, or invalid: %w", err)
	}
	defer projectDir.Close()
	vaultDir, err := pathguard.Open(vault)
	if err != nil {
		return InitResult{}, fmt.Errorf("vault root is a symlink or reparse point, or invalid: %w", err)
	}
	defer vaultDir.Close()
	if inside(opts.GOOS, root, vault) || inside(opts.GOOS, vault, root) {
		return InitResult{}, fmt.Errorf("project and vault must not contain one another")
	}
	if projectDir.ContainsIdentity(vaultDir.Info()) || vaultDir.ContainsIdentity(projectDir.Info()) {
		return InitResult{}, fmt.Errorf("project and vault must not contain one another")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}

	dataPath, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return InitResult{}, err
	}
	dataDir, err := openOrCreateDirectory(dataPath, 0o700)
	if err != nil {
		return InitResult{}, fmt.Errorf("data root is a symlink or reparse point, or invalid: %w", err)
	}
	defer dataDir.Close()
	configPath := filepath.Join(dataPath, "config.toml")
	lock, err := acquireInitLock(dataDir.Root, "config.toml.lock", initTransactionLockTimeout)
	if err != nil {
		return InitResult{}, err
	}
	defer func() {
		retErr = errors.Join(retErr, lock.release())
	}()
	if opts.afterLock != nil {
		if err := opts.afterLock(); err != nil {
			return InitResult{}, err
		}
	}

	cfg, err := config.LoadRoot(dataDir.Root, "config.toml")
	if err != nil {
		return InitResult{}, err
	}
	ledger := filepath.Join(root, "docs", "session-review")
	overviewID, overviewExists, err := readOverviewID(projectDir.Root, filepath.ToSlash(filepath.Join("docs", "session-review", "project-overview.md")))
	if err != nil {
		return InitResult{}, err
	}
	existing, mapped, err := findProject(cfg, opts.GOOS, root, projectDir.Info())
	if err != nil {
		return InitResult{}, err
	}
	if mapped {
		sameVault, err := samePhysicalPath(existing.VaultRoot, vault)
		if err != nil {
			return InitResult{}, err
		}
		if !sameVault {
			return InitResult{}, fmt.Errorf("project is already mapped to a different vault")
		}
		if !validProjectID(existing.ID) {
			return InitResult{}, fmt.Errorf("mapped project ID %q is invalid", existing.ID)
		}
		if overviewExists && overviewID != existing.ID {
			return InitResult{}, fmt.Errorf("project overview ID %q does not match mapped ID %q", overviewID, existing.ID)
		}
		if !overviewExists {
			if opts.beforeOverviewWrite != nil {
				if err := opts.beforeOverviewWrite(); err != nil {
					return InitResult{}, err
				}
			}
			if err := writeOverview(projectDir.Root, overviewBody(existing.ID, opts.Now(), root)); err != nil {
				return InitResult{}, err
			}
		}
		return InitResult{ProjectID: existing.ID, LedgerRoot: ledger, ConfigPath: configPath}, nil
	}
	if overviewExists {
		if owner, claimed := cfg.ProjectByID(overviewID); claimed {
			return InitResult{}, fmt.Errorf("project ID %q already belongs to another project root %q", overviewID, owner.Root)
		}
		cfg.Projects = append(cfg.Projects, config.ProjectMapping{ID: overviewID, Root: root, VaultRoot: vault})
		if opts.beforeConfigWrite != nil {
			if err := opts.beforeConfigWrite(); err != nil {
				return InitResult{}, err
			}
		}
		if err := config.SaveRoot(dataDir.Root, "config.toml", cfg); err != nil {
			return InitResult{}, err
		}
		return InitResult{ProjectID: overviewID, LedgerRoot: ledger, ConfigPath: configPath}, nil
	}

	raw := make([]byte, 8)
	if _, err := io.ReadFull(opts.Random, raw); err != nil {
		return InitResult{}, err
	}
	id := "project-" + hex.EncodeToString(raw)
	if opts.beforeOverviewWrite != nil {
		if err := opts.beforeOverviewWrite(); err != nil {
			return InitResult{}, err
		}
	}
	if err := writeOverview(projectDir.Root, overviewBody(id, opts.Now(), root)); err != nil {
		return InitResult{}, err
	}
	cfg.Projects = append(cfg.Projects, config.ProjectMapping{ID: id, Root: root, VaultRoot: vault})
	if opts.beforeConfigWrite != nil {
		if err := opts.beforeConfigWrite(); err != nil {
			return InitResult{}, err
		}
	}
	if err := config.SaveRoot(dataDir.Root, "config.toml", cfg); err != nil {
		return InitResult{}, err
	}
	return InitResult{ProjectID: id, LedgerRoot: ledger, ConfigPath: configPath}, nil
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

func overviewBody(projectID string, createdAt time.Time, root string) string {
	return fmt.Sprintf(
		"---\nproject_id: %s\ncreated_at: %s\n---\n\n# %s\n",
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
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if err := root.Mkdir(name, perm); err != nil {
			return err
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("target component is redirected or not a directory")
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
	directory, remaining, err := pathguard.OpenDeepest(path)
	if err != nil {
		return nil, err
	}
	if len(remaining) == 0 {
		return directory, nil
	}
	for _, component := range remaining {
		if err := directory.Root.Mkdir(component, perm); err != nil && !errors.Is(err, os.ErrExist) {
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
