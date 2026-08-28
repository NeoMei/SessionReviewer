// Package syncproject exposes deterministic Project-to-Vault reconciliation
// without coupling the engine to CLI formatting or platform-default paths.
package syncproject

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

// Options contains only explicit service inputs. In particular, DataDir is an
// already resolved absolute machine-data root; platform defaults remain a CLI
// responsibility.
type Options struct {
	ProjectID string
	CWD       string
	DataDir   string
	GOOS      string
	Now       func() time.Time
	Trigger   syncengine.Trigger
	DryRun    bool
	Pin       *MappingPin

	pinCheckpoint func(pinCheckpointStage) error
}

// Run authenticates one configured Project mapping, constructs the existing
// sync engine, and returns its unformatted report.
func Run(ctx context.Context, options Options) (syncengine.Report, error) {
	if ctx == nil {
		return syncengine.Report{}, errors.New("sync context is required")
	}
	if !filepath.IsAbs(options.DataDir) {
		return syncengine.Report{}, errors.New("sync data directory must be absolute")
	}
	if strings.TrimSpace(options.GOOS) == "" || options.Now == nil || options.Trigger == "" {
		return syncengine.Report{}, errors.New("sync service requires GOOS, time source, and trigger")
	}
	pin := options.Pin
	ownedPin := false
	if pin == nil {
		var err error
		pin, err = PinMapping(options)
		if err != nil {
			return syncengine.Report{}, err
		}
		ownedPin = true
	}
	if ownedPin {
		defer pin.Close()
	}
	if err := pin.verify(options); err != nil {
		return syncengine.Report{}, err
	}

	engine, err := syncengine.NewEngine(syncengine.Options{
		ProjectRoot:         pin.project.Path,
		ProjectRootExpected: pin.project.Info(),
		VaultRoot:           pin.vault.Path,
		VaultRootExpected:   pin.vault.Info(),
		VaultReviewPath:     pin.mapping.VaultReviewPath,
		DataRoot:            pin.syncData.Path,
		DataRootExpected:    pin.syncData.Info(),
		ProjectID:           pin.mapping.ID,
		GOOS:                options.GOOS,
		VaultCaseMode:       pin.mapping.VaultCaseMode,
		Retry:               syncengine.DefaultRetryPolicy(),
		Now:                 options.Now,
	})
	if err != nil {
		return syncengine.Report{}, err
	}
	defer engine.Close()
	report, err := engine.Reconcile(ctx, syncengine.ReconcileRequest{DryRun: options.DryRun, Trigger: options.Trigger})
	if err != nil {
		return report, err
	}
	if err := pin.verify(options); err != nil {
		return syncengine.Report{}, err
	}
	return report, nil
}

func resolveMapping(cfg config.Config, projectID, cwd string) (config.ProjectMapping, *pathguard.Directory, error) {
	if projectID != "" {
		mapping, found := cfg.ProjectByID(projectID)
		if !found {
			return config.ProjectMapping{}, nil, errors.New("configured project ID was not found")
		}
		project, err := pathguard.Open(mapping.Root)
		if err != nil {
			return config.ProjectMapping{}, nil, fmt.Errorf("configured project root is unavailable or unsafe: %w", err)
		}
		if cwd != "" {
			requested, err := pathguard.Open(cwd)
			if err != nil {
				_ = project.Close()
				return config.ProjectMapping{}, nil, fmt.Errorf("requested project root is unavailable or unsafe: %w", err)
			}
			same := os.SameFile(project.Info(), requested.Info())
			closeErr := requested.Close()
			if closeErr != nil || !same {
				_ = project.Close()
				return config.ProjectMapping{}, nil, errors.New("configured project root does not match requested working directory")
			}
		}
		return mapping, project, nil
	}

	requested, err := openRequestedProject(cwd)
	if err != nil {
		return config.ProjectMapping{}, nil, err
	}
	var mapping config.ProjectMapping
	matches := 0
	for _, candidate := range cfg.Projects {
		configured, statErr := os.Stat(candidate.Root)
		if statErr == nil && os.SameFile(requested.Info(), configured) {
			mapping = candidate
			matches++
		}
	}
	if matches != 1 {
		_ = requested.Close()
		return config.ProjectMapping{}, nil, errors.New("project has no complete Obsidian sync mapping")
	}
	return mapping, requested, nil
}

func openRequestedProject(cwd string) (*pathguard.Directory, error) {
	if cwd != "" {
		return pathguard.Open(cwd)
	}
	workingInfo, err := os.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("inspect current directory: %w", err)
	}
	logical, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("read current directory: %w", err)
	}
	absolute, err := filepath.Abs(logical)
	if err != nil {
		return nil, fmt.Errorf("make current directory absolute: %w", err)
	}
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve current directory: %w", err)
	}
	requested, err := pathguard.Open(physical)
	if err != nil {
		return nil, fmt.Errorf("open resolved current directory: %w", err)
	}
	if !os.SameFile(workingInfo, requested.Info()) {
		_ = requested.Close()
		return nil, errors.New("resolved current directory identity changed")
	}
	return requested, nil
}
