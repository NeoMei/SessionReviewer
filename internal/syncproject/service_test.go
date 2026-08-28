package syncproject

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
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

func TestSyncProjectServiceReconcilesProjectNestedInsideVault(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	projectRoot := filepath.Join(vaultRoot, "project")
	dataRoot := filepath.Join(root, "data")
	projectID := "project-1111111111111111"
	for _, path := range []string{projectRoot, dataRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	legacy := ledger.State{
		ProjectID: projectID,
		CurrentState: ledger.CurrentState{
			ProjectID: projectID, Revision: 1, Goal: "Nested project sync", Branch: "main",
			NextAction: "Sync", LastVerified: "2026-08-29T08:00:00Z", LastUpdated: "2026-08-29T08:00:00Z",
		},
		Decisions: map[string]ledger.Decision{}, OpenLoops: map[string]ledger.OpenLoop{}, Sessions: map[string]ledger.SessionReport{},
	}
	state, err := reviewv2.ProjectLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reviewv2.Render(projectRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(plan); err != nil {
		t.Fatal(err)
	}
	syncData := filepath.Join(dataRoot, "projects", projectID)
	for _, name := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := os.MkdirAll(filepath.Join(syncData, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(syncData, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{
		Version: 1,
		Projects: []config.ProjectMapping{{
			ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
			VaultReviewPath: "Projects/Nested--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	report, err := Run(t.Context(), Options{
		ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot,
		GOOS: runtime.GOOS, Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) },
		Trigger: syncengine.TriggerCLI,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectID != projectID || report.DryRun || len(report.Conflicts) != 0 || len(report.Errors) != 0 {
		t.Fatalf("Run() report = %#v", report)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "Projects", "Nested--11111111", "Session Review", "项目回顾.md")); err != nil {
		t.Fatalf("real nested sync did not publish into Vault: %v", err)
	}
}

func TestSyncProjectRejectsReviewTargetInsideNestedProjectWithoutWrites(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	projectRoot := filepath.Join(vaultRoot, "Projects", "Nested--11111111")
	dataRoot := filepath.Join(root, "data")
	projectID := "project-1111111111111111"
	for _, directory := range []string{projectRoot, dataRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	legacy := ledger.State{
		ProjectID: projectID,
		CurrentState: ledger.CurrentState{
			ProjectID: projectID, Revision: 1, Goal: "Reject nested target", Branch: "main", NextAction: "Do not write",
			LastVerified: "2026-08-29T08:00:00Z", LastUpdated: "2026-08-29T08:00:00Z",
		},
		Decisions: map[string]ledger.Decision{}, OpenLoops: map[string]ledger.OpenLoop{}, Sessions: map[string]ledger.SessionReport{},
	}
	state, err := reviewv2.ProjectLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reviewv2.Render(projectRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(plan); err != nil {
		t.Fatal(err)
	}
	syncData := filepath.Join(dataRoot, "projects", projectID)
	for _, name := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := os.MkdirAll(filepath.Join(syncData, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(syncData, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{
		Version: 1,
		Projects: []config.ProjectMapping{{
			ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
			VaultReviewPath: "Projects/Nested--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(projectRoot, "Session Review")
	if _, err := Run(t.Context(), Options{
		ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS,
		Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) }, Trigger: syncengine.TriggerCLI,
	}); err == nil {
		t.Fatal("Run() accepted a Vault review target inside the authoritative Project")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("unsafe target received writes: %v", err)
	}
	redirectName := "Redirect--11111111"
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(projectRoot, filepath.Join(vaultRoot, "Projects", redirectName)); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{
		Version: 1,
		Projects: []config.ProjectMapping{{
			ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
			VaultReviewPath: "Projects/" + redirectName + "/Session Review", VaultCaseMode: platform.CaseSensitive,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(t.Context(), Options{
		ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS,
		Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) }, Trigger: syncengine.TriggerCLI,
	}); err == nil {
		t.Fatal("Run() followed a redirect in the configured Vault review target")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("redirected target received writes: %v", err)
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

func TestPinMappingParsesOneCapturedConfigSnapshotAcrossABA(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	vaultRoot := filepath.Join(root, "vault-original")
	decoyVault := filepath.Join(root, "vault-decoy")
	dataRoot := filepath.Join(root, "data")
	projectID := "project-1111111111111111"
	for _, directory := range []string{projectRoot, vaultRoot, decoyVault, dataRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	legacy := ledger.State{
		ProjectID: projectID,
		CurrentState: ledger.CurrentState{
			ProjectID: projectID, Revision: 1, Goal: "Captured config", Branch: "main", NextAction: "Sync",
			LastVerified: "2026-08-29T08:00:00Z", LastUpdated: "2026-08-29T08:00:00Z",
		},
		Decisions: map[string]ledger.Decision{}, OpenLoops: map[string]ledger.OpenLoop{}, Sessions: map[string]ledger.SessionReport{},
	}
	state, err := reviewv2.ProjectLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reviewv2.Render(projectRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(plan); err != nil {
		t.Fatal(err)
	}
	syncData := filepath.Join(dataRoot, "projects", projectID)
	for _, name := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := os.MkdirAll(filepath.Join(syncData, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(syncData, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	original := config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
		VaultReviewPath: "Projects/Original--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}}
	decoy := original
	decoy.Projects = []config.ProjectMapping{{
		ID: projectID, Root: projectRoot, VaultRoot: decoyVault,
		VaultReviewPath: "Projects/Decoy--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}
	configPath := filepath.Join(dataRoot, "config.toml")
	if err := config.Save(configPath, original); err != nil {
		t.Fatal(err)
	}
	originalBody, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	decoyPath := filepath.Join(root, "decoy.toml")
	if err := config.Save(decoyPath, decoy); err != nil {
		t.Fatal(err)
	}
	decoyBody, err := os.ReadFile(decoyPath)
	if err != nil {
		t.Fatal(err)
	}
	options := Options{
		ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS,
		Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) }, Trigger: syncengine.TriggerCLI,
	}
	seenCapture, seenParse := false, false
	options.pinCheckpoint = func(stage pinCheckpointStage) error {
		switch stage {
		case pinAfterCapture:
			seenCapture = true
			return os.WriteFile(configPath, decoyBody, 0o600)
		case pinAfterParse:
			seenParse = true
			return os.WriteFile(configPath, originalBody, 0o600)
		default:
			return nil
		}
	}
	pin, err := PinMapping(options)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	if !seenCapture || !seenParse || pin.mapping.VaultRoot != vaultRoot || pin.mapping.VaultReviewPath != "Projects/Original--11111111/Session Review" {
		t.Fatalf("pin mapping=%#v capture=%v parse=%v", pin.mapping, seenCapture, seenParse)
	}
	options.Pin = pin
	options.pinCheckpoint = nil
	if _, err := Run(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "Projects", "Original--11111111", "Session Review", "项目回顾.md")); err != nil {
		t.Fatalf("captured mapping target was not written: %v", err)
	}
	entries, err := os.ReadDir(decoyVault)
	if err != nil || len(entries) != 0 {
		t.Fatalf("decoy Vault received writes: entries=%v err=%v", entries, err)
	}
}

func TestPinMappingNeverUsesDecoyConfigAcrossEverySnapshotCheckpoint(t *testing.T) {
	stages := []pinCheckpointStage{
		pinAfterCapture, pinAfterParse, pinAfterMapping, pinBeforeVaultOpen, pinAfterVaultOpen, pinBeforeFinalVerify,
	}
	for index, mutateAt := range stages {
		t.Run(string(mutateAt), func(t *testing.T) {
			root := t.TempDir()
			projectRoot := filepath.Join(root, "project")
			vaultRoot := filepath.Join(root, "vault-original")
			decoyVault := filepath.Join(root, "vault-decoy")
			dataRoot := filepath.Join(root, "data")
			projectID := "project-1111111111111111"
			for _, directory := range []string{projectRoot, vaultRoot, decoyVault, filepath.Join(dataRoot, "projects", projectID)} {
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			original := config.Config{Version: 1, Projects: []config.ProjectMapping{{
				ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
				VaultReviewPath: "Projects/Original--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
			}}}
			decoy := config.Config{Version: 1, Projects: []config.ProjectMapping{{
				ID: projectID, Root: projectRoot, VaultRoot: decoyVault,
				VaultReviewPath: "Projects/Decoy--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
			}}}
			configPath := filepath.Join(dataRoot, "config.toml")
			if err := config.Save(configPath, original); err != nil {
				t.Fatal(err)
			}
			originalBody, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			decoyPath := filepath.Join(root, "decoy.toml")
			if err := config.Save(decoyPath, decoy); err != nil {
				t.Fatal(err)
			}
			decoyBody, err := os.ReadFile(decoyPath)
			if err != nil {
				t.Fatal(err)
			}
			options := Options{ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI}
			restored := false
			options.pinCheckpoint = func(stage pinCheckpointStage) error {
				if stage == mutateAt {
					return os.WriteFile(configPath, decoyBody, 0o600)
				}
				if index+1 < len(stages) && stage == stages[index+1] {
					restored = true
					return os.WriteFile(configPath, originalBody, 0o600)
				}
				return nil
			}
			pin, err := PinMapping(options)
			if index == len(stages)-1 {
				if err == nil {
					_ = pin.Close()
					t.Fatal("PinMapping accepted an unrestored namespace mutation")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer pin.Close()
				if !restored || pin.mapping.VaultRoot != vaultRoot || pin.mapping.VaultReviewPath != "Projects/Original--11111111/Session Review" {
					t.Fatalf("pin mapping=%#v restored=%v", pin.mapping, restored)
				}
			}
			entries, err := os.ReadDir(decoyVault)
			if err != nil || len(entries) != 0 {
				t.Fatalf("decoy Vault received writes: entries=%v err=%v", entries, err)
			}
		})
	}
}
