package syncproject

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/project"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

// Removing configured-mapping authentication, passing the global data root to
// the engine, or reconciling with a different trigger makes this test fail.
func TestSyncProjectServiceAuthenticatesMappingAndReconciles(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	vaultRoot := filepath.Join(root, "vault")
	dataRoot := filepath.Join(root, "data")
	for _, path := range []string{projectRoot, vaultRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	initialized, err := project.Initialize(project.InitOptions{
		ProjectRoot: projectRoot,
		VaultRoot:   vaultRoot,
		DataDir:     dataRoot,
		GOOS:        runtime.GOOS,
		Now:         func() time.Time { return now },
		Random:      bytes.NewReader(bytes.Repeat([]byte{0x11}, 8)),
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := Run(t.Context(), Options{
		ProjectID: initialized.ProjectID,
		CWD:       projectRoot,
		DataDir:   dataRoot,
		GOOS:      runtime.GOOS,
		Now:       func() time.Time { return now },
		Trigger:   syncengine.TriggerCLI,
		DryRun:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectID != initialized.ProjectID || !report.DryRun || len(report.Operations) == 0 {
		t.Fatalf("Run() report = %#v", report)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "Projects")); !os.IsNotExist(err) {
		t.Fatalf("dry-run mutated the Vault: %v", err)
	}

	if _, err := Run(t.Context(), Options{
		ProjectID: initialized.ProjectID,
		CWD:       filepath.Join(root, "other"),
		DataDir:   dataRoot,
		GOOS:      runtime.GOOS,
		Now:       func() time.Time { return now },
		Trigger:   syncengine.TriggerCLI,
		DryRun:    true,
	}); err == nil {
		t.Fatal("Run() accepted a CWD that does not authenticate the configured project")
	}
}

// Changing the service contract to silently resolve a relative machine-data
// root would let worker and CLI callers disagree about the protected root.
func TestSyncProjectServiceRequiresExplicitAbsoluteDataDir(t *testing.T) {
	if _, err := Run(t.Context(), Options{
		ProjectID: "project-1111111111111111",
		DataDir:   "relative-data",
		GOOS:      runtime.GOOS,
		Now:       time.Now,
		Trigger:   syncengine.TriggerCLI,
	}); err == nil {
		t.Fatal("Run() accepted a relative data directory")
	}
}

// Extraction must preserve the old CLI lookup error when the data directory
// has not been initialized; opening the data root eagerly changes that public
// diagnostic into a lower-level filesystem error.
func TestSyncProjectServicePreservesMissingConfigLookupError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-data")
	_, err := Run(t.Context(), Options{
		ProjectID: "project-1111111111111111",
		DataDir:   missing,
		GOOS:      runtime.GOOS,
		Now:       time.Now,
		Trigger:   syncengine.TriggerCLI,
	})
	if err == nil || err.Error() != "configured project ID was not found" || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Run() error = %v, want configured mapping lookup diagnostic", err)
	}
}

func TestPinnedSyncRejectsConfigOrVaultReplacementWithoutDecoyWrites(t *testing.T) {
	for _, target := range []string{"config", "vault"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			projectRoot := filepath.Join(root, "project")
			vaultRoot := filepath.Join(root, "vault")
			dataRoot := filepath.Join(root, "data")
			for _, path := range []string{projectRoot, vaultRoot} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
			initialized, err := project.Initialize(project.InitOptions{
				ProjectRoot: projectRoot, VaultRoot: vaultRoot, DataDir: dataRoot,
				GOOS: runtime.GOOS, Now: func() time.Time { return now },
				Random: bytes.NewReader(bytes.Repeat([]byte{0x22}, 8)),
			})
			if err != nil {
				t.Fatal(err)
			}
			options := Options{
				ProjectID: initialized.ProjectID, CWD: projectRoot, DataDir: dataRoot,
				GOOS: runtime.GOOS, Now: func() time.Time { return now }, Trigger: syncengine.TriggerCLI,
			}
			pin, err := PinMapping(options)
			if err != nil {
				t.Fatal(err)
			}
			defer pin.Close()
			if target == "config" {
				fragments, err := os.ReadDir(filepath.Join(dataRoot, "projects.d"))
				if err != nil || len(fragments) != 1 {
					t.Fatalf("project fragments=%v err=%v", fragments, err)
				}
				configPath := filepath.Join(dataRoot, "projects.d", fragments[0].Name())
				body, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(configPath, configPath+".pinned"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(configPath, body, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(vaultRoot, vaultRoot+".pinned"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(vaultRoot, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			options.Pin = pin
			if _, err := Run(t.Context(), options); err == nil {
				t.Fatalf("Run() accepted %s replacement after mapping pin", target)
			}
			entries, err := os.ReadDir(vaultRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("replacement Vault received writes: entries=%v err=%v", entries, err)
			}
		})
	}
}
