package contextupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/projectprobe"
	"github.com/neomei/SessionReviewer/internal/projectview"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/scan"
	"github.com/neomei/SessionReviewer/internal/sessionview"
	"github.com/neomei/SessionReviewer/internal/source/codex"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

// Options configures foreground or worker context updates.
type Options struct {
	ProjectID     string
	SessionsRoot  string
	DataRoot      string
	Now           func() time.Time
	PhaseObserver func(phase string)
}

// Result contains the outcome of a context update scan and publication.
type Result struct {
	SchemaVersion   int                `json:"schema_version"`
	ProjectID       string             `json:"project_id"`
	State           scan.State         `json:"state"`
	GenerationID    string             `json:"generation_id,omitempty"`
	SourceSessions  int                `json:"source_sessions"`
	IndexedSessions int                `json:"indexed_sessions"`
	IssueSessions   int                `json:"issue_sessions"`
	Publication     publication.Result `json:"publication,omitempty"`
	ReviewRunTokens int                `json:"review_run_tokens"`
}

// Run executes the full zero-token context update lifecycle.
func Run(ctx context.Context, opts Options) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("context is required")
	}
	if opts.ProjectID == "" {
		return Result{}, errors.New("project ID is required")
	}
	if !filepath.IsAbs(opts.DataRoot) || filepath.Clean(opts.DataRoot) != opts.DataRoot {
		return Result{}, errors.New("data root must be an absolute clean path")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	notifyPhase := func(phase string) {
		if opts.PhaseObserver != nil {
			opts.PhaseObserver(phase)
		}
	}

	cfg, err := config.Load(filepath.Join(opts.DataRoot, "config.toml"))
	if err != nil {
		return Result{}, fmt.Errorf("load config: %w", err)
	}
	var mapping config.ProjectMapping
	found := false
	for _, p := range cfg.Projects {
		if p.ID == opts.ProjectID {
			mapping = p
			found = true
			break
		}
	}
	if !found {
		return Result{}, fmt.Errorf("project %q is not configured in config.toml", opts.ProjectID)
	}

	binding, err := projectidentity.Resolve(mapping, mapping.Root, runtime.GOOS)
	if err != nil {
		return Result{}, fmt.Errorf("resolve project identity: %w", err)
	}

	projectDir, err := pathguard.Open(mapping.Root)
	if err != nil {
		return Result{}, fmt.Errorf("open project root: %w", err)
	}
	defer projectDir.Close()

	store, err := memorystore.Open(opts.DataRoot, opts.ProjectID)
	if err != nil {
		return Result{}, fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	catalog, err := sourcecatalog.Open(opts.DataRoot)
	if err != nil {
		return Result{}, fmt.Errorf("open source catalog: %w", err)
	}
	defer catalog.Close()

	sessionsRoot := opts.SessionsRoot
	if sessionsRoot == "" {
		if home := os.Getenv("CODEX_HOME"); home != "" {
			sessionsRoot = filepath.Join(home, "sessions")
		} else if userHome, err := os.UserHomeDir(); err == nil {
			sessionsRoot = filepath.Join(userHome, ".codex", "sessions")
		}
	}
	if sessionsRoot == "" || !filepath.IsAbs(sessionsRoot) {
		return Result{}, errors.New("sessions root must be an absolute path")
	}
	adapter, err := codex.New(codex.AdapterOptions{
		SessionsRoot:   sessionsRoot,
		Bindings:       []projectidentity.Binding{binding},
		Catalog:        catalog,
		AdapterVersion: "codex-jsonl-v1",
	})
	if err != nil {
		return Result{}, fmt.Errorf("open source adapter: %w", err)
	}

	notifyPhase("discovering")
	scanOpts := scan.Options{
		ProjectID:    opts.ProjectID,
		Binding:      binding,
		SessionsRoot: sessionsRoot,
		DataRoot:     opts.DataRoot,
		Adapter:      adapter,
		Catalog:      catalog,
		Store:        store,
		Now:          now,
		Materialize:  sessionview.Materialize,
		Probe:        projectprobe.Run,
		ProbeOptions: projectprobe.Options{Binding: binding, Now: now},
		Reduce:       projectview.Reduce,
	}

	scanResult, err := scan.Run(ctx, scanOpts)
	if err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, err
	}

	notifyPhase("reducing")
	prepared, manifest, err := store.LoadPrepared()
	if err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("load prepared generation: %w", err)
	}
	pvBytes, err := store.LoadObject(memorystore.ObjectProjectView, manifest.ProjectViewDigest)
	if err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("load project view object: %w", err)
	}
	var pv memory.ProjectView
	if err := json.Unmarshal(pvBytes, &pv); err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("decode project view: %w", err)
	}

	notifyPhase("rendering")
	reviewBody, reviewFound, _ := projectDir.ReadRegularOptional(reviewv2.ReviewRelativePath, 64<<20)
	historyBody, historyFound, _ := projectDir.ReadRegularOptional(reviewv2.HistoryRelativePath, 64<<20)
	ledgerBody, ledgerFound, _ := projectDir.ReadRegularOptional(reviewv2.MachineLedgerRelativePath, 64<<20)

	var currentPatches []presentation.Patch
	var currentOrphans []presentation.Patch
	var currentUnknown map[string][]byte
	var legacyPresentation reviewv2.LegacyPresentation
	revision := 1
	expectedFiles := make(map[string][]byte)

	if reviewFound {
		expectedFiles[reviewv2.ReviewRelativePath] = reviewBody
	}
	if historyFound {
		expectedFiles[reviewv2.HistoryRelativePath] = historyBody
	}
	if ledgerFound {
		expectedFiles[reviewv2.MachineLedgerRelativePath] = ledgerBody
	}

	if reviewFound && historyFound && ledgerFound {
		if acceptedV3, err := reviewv2.LoadV3Bytes(reviewBody, historyBody, ledgerBody); err == nil {
			revision = acceptedV3.State.Review.Revision + 1
			legacyPresentation = reviewv2.LegacyPresentation{Compatibility: acceptedV3.State.Machine.LegacyCompatibility}
		}
	}

	pInput := presentation.ProjectInput{
		ProjectView:   pv,
		GenerationID:  prepared.GenerationID,
		Revision:      revision,
		Legacy:        legacyPresentation,
		ActivePatches: currentPatches,
		OrphanPatches: currentOrphans,
		UnknownBlocks: currentUnknown,
		ExpectedFiles: expectedFiles,
	}

	pOutput, err := presentation.Project(pInput)
	if err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("project presentation: %w", err)
	}
	plan, err := presentation.Render(pInput, pOutput)
	if err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("render presentation: %w", err)
	}

	notifyPhase("syncing")
	pubOpts := publication.Options{
		ProjectID:          opts.ProjectID,
		PreparedGeneration: prepared.GenerationID,
		Plan:               plan,
		Mapping:            mapping,
		DataRoot:           opts.DataRoot,
		Now:                now,
	}
	pubResult, err := publication.Publish(ctx, pubOpts)
	if err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("publish presentation: %w", err)
	}

	return Result{
		SchemaVersion:   1,
		ProjectID:       opts.ProjectID,
		State:           scanResult.State,
		GenerationID:    pubResult.GenerationID,
		SourceSessions:  scanResult.SourceSessions,
		IndexedSessions: scanResult.IndexedSessions,
		IssueSessions:   scanResult.IssueSessions,
		Publication:     pubResult,
		ReviewRunTokens: 0,
	}, nil
}
