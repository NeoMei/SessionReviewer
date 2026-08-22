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
	"github.com/neomei/SessionReviewer/internal/platform"
)

const initTransactionLockTimeout = 10 * time.Second

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

func Initialize(opts InitOptions) (result InitResult, retErr error) {
	root, err := filepath.Abs(opts.ProjectRoot)
	if err != nil {
		return InitResult{}, err
	}
	vault, err := filepath.Abs(opts.VaultRoot)
	if err != nil {
		return InitResult{}, err
	}
	physicalRoot, err := validateRoot(opts.GOOS, root)
	if err != nil {
		return InitResult{}, err
	}
	physicalVault, err := validateRoot(opts.GOOS, vault)
	if err != nil {
		return InitResult{}, err
	}
	if inside(opts.GOOS, root, vault) || inside(opts.GOOS, vault, root) {
		return InitResult{}, fmt.Errorf("project and vault must not contain one another")
	}
	if inside(runtime.GOOS, physicalRoot, physicalVault) || inside(runtime.GOOS, physicalVault, physicalRoot) {
		return InitResult{}, fmt.Errorf("project and vault must not contain one another")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}

	configPath := filepath.Join(opts.DataDir, "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return InitResult{}, err
	}
	lock, err := acquireInitLock(configPath+".lock", initTransactionLockTimeout)
	if err != nil {
		return InitResult{}, err
	}
	defer func() {
		retErr = errors.Join(retErr, lock.release())
	}()

	cfg, err := config.Load(configPath)
	if err != nil {
		return InitResult{}, err
	}
	ledger := filepath.Join(root, "docs", "session-review")
	overviewPath := filepath.Join(ledger, "project-overview.md")
	overviewID, overviewExists, err := readOverviewID(opts.GOOS, root, overviewPath)
	if err != nil {
		return InitResult{}, err
	}
	existing, mapped, err := findProject(cfg, opts.GOOS, root, physicalRoot)
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
			if err := writeOverview(opts.GOOS, root, ledger, overviewBody(existing.ID, opts.Now(), root)); err != nil {
				return InitResult{}, err
			}
		}
		return InitResult{ProjectID: existing.ID, LedgerRoot: ledger, ConfigPath: configPath}, nil
	}
	if overviewExists {
		cfg.Projects = append(cfg.Projects, config.ProjectMapping{ID: overviewID, Root: root, VaultRoot: vault})
		if err := config.Save(configPath, cfg); err != nil {
			return InitResult{}, err
		}
		return InitResult{ProjectID: overviewID, LedgerRoot: ledger, ConfigPath: configPath}, nil
	}

	raw := make([]byte, 8)
	if _, err := io.ReadFull(opts.Random, raw); err != nil {
		return InitResult{}, err
	}
	id := "project-" + hex.EncodeToString(raw)
	if err := writeOverview(opts.GOOS, root, ledger, overviewBody(id, opts.Now(), root)); err != nil {
		return InitResult{}, err
	}
	cfg.Projects = append(cfg.Projects, config.ProjectMapping{ID: id, Root: root, VaultRoot: vault})
	if err := config.Save(configPath, cfg); err != nil {
		return InitResult{}, err
	}
	return InitResult{ProjectID: id, LedgerRoot: ledger, ConfigPath: configPath}, nil
}

func findProject(cfg config.Config, goos, root, physicalRoot string) (config.ProjectMapping, bool, error) {
	if existing, ok := cfg.FindProject(goos, root); ok {
		return existing, true, nil
	}
	physicalRoot = platform.NormalizePath(runtime.GOOS, physicalRoot)
	for _, project := range cfg.Projects {
		physicalProject, err := filepath.EvalSymlinks(project.Root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return config.ProjectMapping{}, false, err
		}
		if platform.NormalizePath(runtime.GOOS, physicalProject) == physicalRoot {
			return project, true, nil
		}
	}
	return config.ProjectMapping{}, false, nil
}

func overviewBody(projectID string, createdAt time.Time, root string) string {
	return fmt.Sprintf(
		"---\nproject_id: %s\ncreated_at: %s\n---\n\n# %s\n",
		projectID,
		createdAt.UTC().Format(time.RFC3339),
		filepath.Base(root),
	)
}

func readOverviewID(goos, root, overview string) (string, bool, error) {
	if err := validateTargetPath(goos, root, overview); err != nil {
		return "", false, err
	}
	body, err := os.ReadFile(overview)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
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
		return "", false, fmt.Errorf("project overview contains invalid project ID %q", projectID)
	}
	return projectID, true, nil
}

func validProjectID(projectID string) bool {
	const prefix = "project-"
	if !strings.HasPrefix(projectID, prefix) || len(projectID) != len(prefix)+16 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(projectID, prefix))
	return err == nil
}

func validateRoot(goos, root string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("root %q is a symlink or reparse point", root)
	}
	evaluated, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	evaluatedParent, err := filepath.EvalSymlinks(filepath.Dir(root))
	if err != nil {
		return "", err
	}
	expected := filepath.Join(evaluatedParent, filepath.Base(root))
	if platform.NormalizePath(goos, evaluated) != platform.NormalizePath(goos, expected) {
		return "", fmt.Errorf("root %q is a symlink or reparse point", root)
	}
	return evaluated, nil
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
	physicalFirst, err := filepath.EvalSymlinks(first)
	if err != nil {
		return false, err
	}
	physicalSecond, err := filepath.EvalSymlinks(second)
	if err != nil {
		return false, err
	}
	return platform.NormalizePath(runtime.GOOS, physicalFirst) == platform.NormalizePath(runtime.GOOS, physicalSecond), nil
}

func writeOverview(goos, root, ledger, body string) error {
	overview := filepath.Join(ledger, "project-overview.md")
	if err := validateTargetPath(goos, root, overview); err != nil {
		return err
	}
	if err := os.MkdirAll(ledger, 0o755); err != nil {
		return err
	}
	if err := validateTargetPath(goos, root, overview); err != nil {
		return err
	}
	return atomicfile.Write(overview, []byte(body), 0o644)
}

func validateTargetPath(goos, root, target string) error {
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("target %q is outside project root", target)
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("target component %q is redirected", current)
		}
		physicalCurrent, err := filepath.EvalSymlinks(current)
		if err != nil {
			return err
		}
		if !inside(runtime.GOOS, physicalRoot, physicalCurrent) {
			return fmt.Errorf("target component %q escapes project root", current)
		}
		physicalParent, err := filepath.EvalSymlinks(filepath.Dir(current))
		if err != nil {
			return err
		}
		expected := filepath.Join(physicalParent, filepath.Base(current))
		if platform.NormalizePath(goos, physicalCurrent) != platform.NormalizePath(goos, expected) {
			return fmt.Errorf("target component %q is redirected", current)
		}
	}
	return nil
}
