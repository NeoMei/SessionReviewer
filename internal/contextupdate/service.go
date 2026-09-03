package contextupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/projectprobe"
	"github.com/neomei/SessionReviewer/internal/projectview"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/scan"
	"github.com/neomei/SessionReviewer/internal/sessionview"
	"github.com/neomei/SessionReviewer/internal/source/codex"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

// Options configures foreground or worker context updates.
type Options struct {
	ProjectID          string
	SessionsRoot       string
	DataRoot           string
	Now                func() time.Time
	PhaseObserver      func(phase string) error
	ExtractionObserver func(scan.Progress) error
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

type currentProjectFiles struct {
	reviewBody   []byte
	reviewFound  bool
	historyBody  []byte
	historyFound bool
	ledgerBody   []byte
	ledgerFound  bool
	expected     map[string][]byte
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
	r := redact.Default()
	adapter, err := codex.New(codex.AdapterOptions{
		SessionsRoot:   sessionsRoot,
		Bindings:       []projectidentity.Binding{binding},
		Catalog:        catalog,
		Redactor:       &r,
		AdapterVersion: "codex-jsonl-v1",
	})
	if err != nil {
		return Result{}, fmt.Errorf("open source adapter: %w", err)
	}

	if err := notifyPhase(opts.PhaseObserver, "discovering"); err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, err
	}
	scanOpts := scan.Options{
		ProjectID:    opts.ProjectID,
		Binding:      binding,
		SessionsRoot: sessionsRoot,
		DataRoot:     opts.DataRoot,
		Adapter:      adapter,
		Catalog:      catalog,
		Workers:      4,
		Store:        store,
		Now:          now,
		Materialize:  sessionview.Materialize,
		Probe:        projectprobe.Run,
		ProbeOptions: projectprobe.Options{
			Binding:       binding,
			GitExecutable: resolveGitExecutable(),
			Now:           now,
		},
		Reduce:           projectview.Reduce,
		ProgressObserver: opts.ExtractionObserver,
	}

	scanResult, err := scan.Run(ctx, scanOpts)
	if err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, err
	}

	if err := notifyPhase(opts.PhaseObserver, "reducing"); err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, err
	}
	prepared, manifest, err := store.LoadPrepared()
	if err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("load prepared generation: %w", err)
	}
	publishedID, _, pubErr := store.LoadPublished()
	if pubErr != nil && !errors.Is(pubErr, memorystore.ErrNoPublishedGeneration) {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("load published generation: %w", pubErr)
	}
	pvBytes, err := store.LoadObject(memorystore.ObjectProjectView, manifest.ProjectViewDigest)
	if err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("load project view object: %w", err)
	}
	var pv memory.ProjectView
	if err := json.Unmarshal(pvBytes, &pv); err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("decode project view: %w", err)
	}
	projectAccounting, sessionReports, err := loadProjectionAccounting(catalog, pv)
	if err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("load projection accounting: %w", err)
	}

	if err := notifyPhase(opts.PhaseObserver, "rendering"); err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, err
	}
	currentFiles, err := loadCurrentProjectFiles(projectDir)
	if err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, err
	}
	reviewBody, reviewFound := currentFiles.reviewBody, currentFiles.reviewFound
	historyBody, historyFound := currentFiles.historyBody, currentFiles.historyFound
	ledgerBody, ledgerFound := currentFiles.ledgerBody, currentFiles.ledgerFound

	var capturedPatches []presentation.Patch
	var currentUnknown map[string][]byte
	var legacyPresentation reviewv2.LegacyPresentation
	lastSuccessfulSync := ""
	revision := 1
	expectedFiles := currentFiles.expected

	if reviewFound || historyFound || ledgerFound {
		if !reviewFound || !historyFound || !ledgerFound {
			return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, errors.New("current project projection is incomplete; refusing to overwrite human files")
		}
		acceptedV3, err := reviewv2.LoadV3Bytes(reviewBody, historyBody, ledgerBody)
		if err != nil {
			return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("load current project projection: %w", err)
		}
		if err := validateCurrentProjectionProject(acceptedV3, opts.ProjectID); err != nil {
			return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, err
		}
		journalSettled, err := publicationJournalSettled(opts.DataRoot, opts.ProjectID, publishedID)
		if err != nil {
			return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("inspect publication journal: %w", err)
		}
		if journalSettled {
			unchangedPublication, unchanged, err := unchangedPublishedProjection(mapping, currentFiles, acceptedV3, manifest, publishedID)
			if err != nil {
				return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("verify unchanged public projection: %w", err)
			}
			if unchanged {
				if err := notifyPhase(opts.PhaseObserver, "syncing"); err != nil {
					return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, err
				}
				return Result{
					SchemaVersion: 1, ProjectID: opts.ProjectID, State: scanResult.State,
					GenerationID: publishedID, SourceSessions: scanResult.SourceSessions,
					IndexedSessions: scanResult.IndexedSessions, IssueSessions: scanResult.IssueSessions,
					Publication: unchangedPublication, ReviewRunTokens: 0,
				}, nil
			}
		}
		legacyPresentation, capturedPatches, err = captureCurrentPresentation(acceptedV3)
		if err != nil {
			return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("capture current human presentation: %w", err)
		}
		lastSuccessfulSync = acceptedV3.State.Machine.LastSuccessfulSync
		currentUnknown, err = presentation.CaptureCustomContent(reviewBody)
		if err != nil {
			return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("capture current custom presentation: %w", err)
		}
		revision = nextProjectionRevision(acceptedV3, reviewBody, historyBody, prepared.GenerationID, publishedID)
	}

	pInput := presentation.ProjectInput{
		ProjectView:        pv,
		GenerationID:       prepared.GenerationID,
		Revision:           revision,
		ProjectName:        strings.TrimSpace(filepath.Base(mapping.Root)),
		Accounting:         projectAccounting,
		SessionReports:     sessionReports,
		LastSuccessfulSync: lastSuccessfulSync,
		Legacy:             legacyPresentation,
		PreservedEventIDs:  patchEntityIDs(capturedPatches),
		UnknownBlocks:      currentUnknown,
		ExpectedFiles:      expectedFiles,
	}

	baselineOutput, err := presentation.Project(pInput)
	if err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("project presentation baseline: %w", err)
	}
	rebased, err := presentation.Rebase(capturedPatches, baselineOutput.Baselines)
	if err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("rebase current human presentation: %w", err)
	}
	pInput.ActivePatches = rebased.Active
	pInput.OrphanPatches = rebased.Orphans
	pOutput, err := presentation.Project(pInput)
	if err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("project presentation: %w", err)
	}
	plan, err := presentation.Render(pInput, pOutput)
	if err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("render presentation: %w", err)
	}
	if renderPlanChangesFiles(plan) {
		pInput.LastSuccessfulSync = now().UTC().Format(time.RFC3339Nano)
		plan, err = presentation.Render(pInput, pOutput)
		if err != nil {
			return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, fmt.Errorf("render timestamped presentation: %w", err)
		}
	}
	if err := notifyPhase(opts.PhaseObserver, "syncing"); err != nil {
		return Result{SchemaVersion: 1, ProjectID: opts.ProjectID, State: scan.Failed}, err
	}
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

func validateCurrentProjectionProject(accepted reviewv2.AcceptedV3, projectID string) error {
	if accepted.State.Machine.ProjectID != projectID {
		return fmt.Errorf("current project projection belongs to %q, not %q", accepted.State.Machine.ProjectID, projectID)
	}
	return nil
}

func publicationJournalSettled(dataRoot, projectID, publishedID string) (bool, error) {
	j, err := publication.OpenJournal(dataRoot, projectID)
	if err != nil {
		return false, err
	}
	_, previousErr := j.LoadPrevious()
	if previousErr == nil {
		return false, j.Close()
	}
	if !errors.Is(previousErr, publication.ErrNoActiveIntent) {
		return false, errors.Join(previousErr, j.Close())
	}
	intent, loadErr := j.Load()
	closeErr := j.Close()
	if errors.Is(loadErr, publication.ErrNoActiveIntent) {
		return true, closeErr
	}
	if loadErr != nil {
		return false, errors.Join(loadErr, closeErr)
	}
	return intent.Stage == publication.StageCommitted && intent.GenerationID == publishedID, closeErr
}

// unchangedPublishedProjection recognizes a fresh private scan generation whose
// human-facing ProjectView is byte-identical to the currently published one.
// The new generation remains in the private audit history, while the public
// revision and its Project/Vault mirrors stay untouched. Any human edit or
// mirror drift deliberately falls through to the normal publication path.
func unchangedPublishedProjection(mapping config.ProjectMapping, current currentProjectFiles, accepted reviewv2.AcceptedV3, manifest memory.GenerationManifest, publishedID string) (publication.Result, bool, error) {
	if publishedID == "" || accepted.State.Machine.GenerationID != publishedID ||
		strings.TrimPrefix(manifest.ProjectViewDigest, "sha256:") != accepted.State.Machine.ProjectViewDigest ||
		!current.reviewFound || !current.historyFound || !current.ledgerFound ||
		digestHex(current.reviewBody) != accepted.State.Machine.ReviewSHA256 ||
		digestHex(current.historyBody) != accepted.State.Machine.HistorySHA256 {
		return publication.Result{}, false, nil
	}

	vaultDir, err := pathguard.Open(mapping.VaultRoot)
	if err != nil {
		return publication.Result{}, false, fmt.Errorf("open vault root: %w", err)
	}
	defer vaultDir.Close()

	files := []struct {
		relative string
		body     []byte
	}{
		{relative: reviewv2.ReviewRelativePath, body: current.reviewBody},
		{relative: reviewv2.HistoryRelativePath, body: current.historyBody},
		{relative: reviewv2.MachineLedgerRelativePath, body: current.ledgerBody},
	}
	result := publication.Result{GenerationID: publishedID}
	for _, file := range files {
		vaultRelative := vaultProjectionRelative(mapping.VaultReviewPath, file.relative)
		vaultBody, found, err := vaultDir.ReadRegularOptional(vaultRelative, 64<<20)
		if err != nil {
			return publication.Result{}, false, fmt.Errorf("read vault file %q: %w", vaultRelative, err)
		}
		if !found || !bytes.Equal(vaultBody, file.body) {
			return publication.Result{}, false, nil
		}
		digest := digestHex(file.body)
		result.ProjectFiles = append(result.ProjectFiles, publication.VerifiedFile{Side: "project", Relative: file.relative, SHA256: digest})
		result.VaultFiles = append(result.VaultFiles, publication.VerifiedFile{Side: "vault", Relative: vaultRelative, SHA256: digest})
	}
	return result, true, nil
}

func vaultProjectionRelative(vaultReviewPath, projectRelative string) string {
	switch projectRelative {
	case reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath:
		return path.Join(vaultReviewPath, path.Base(projectRelative))
	case reviewv2.MachineLedgerRelativePath:
		return path.Join(vaultReviewPath, ".session-reviewer/ledger.json")
	default:
		return path.Join(vaultReviewPath, projectRelative)
	}
}

func digestHex(body []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func patchEntityIDs(patches []presentation.Patch) []string {
	seen := make(map[string]struct{}, len(patches))
	result := make([]string, 0, len(patches))
	for _, patch := range patches {
		if _, exists := seen[patch.EntityID]; exists {
			continue
		}
		seen[patch.EntityID] = struct{}{}
		result = append(result, patch.EntityID)
	}
	sort.Strings(result)
	return result
}

func loadProjectionAccounting(catalog *sourcecatalog.Catalog, view memory.ProjectView) (accounting.ProjectSummary, []ledger.SessionReport, error) {
	keys := make([]sourcecatalog.SnapshotKey, 0, len(view.AssociatedUsage))
	for _, item := range view.AssociatedUsage {
		keys = append(keys, sourcecatalog.SnapshotKey{Provider: item.Provider, SessionID: item.SessionID})
	}
	snapshots, err := catalog.SnapshotSources(keys)
	if err != nil {
		return accounting.ProjectSummary{}, nil, err
	}
	return buildProjectionAccounting(view.ProjectID, view.AssociatedUsage, snapshots)
}

func buildProjectionAccounting(projectID string, associated []memory.AssociatedUsage, snapshots map[sourcecatalog.SnapshotKey]sourcecatalog.SourceSnapshot) (accounting.ProjectSummary, []ledger.SessionReport, error) {
	reports := make([]ledger.SessionReport, 0, len(associated))
	seen := make(map[string]struct{}, len(associated))
	for _, item := range associated {
		key := sourcecatalog.SnapshotKey{Provider: item.Provider, SessionID: item.SessionID}
		logicalID := item.Provider + "/" + item.SessionID
		if _, duplicate := seen[logicalID]; duplicate {
			return accounting.ProjectSummary{}, nil, fmt.Errorf("duplicate associated usage %s", logicalID)
		}
		seen[logicalID] = struct{}{}
		snapshot, exists := snapshots[key]
		if !exists || !snapshot.Found || !containsProjectID(snapshot.Record.ProjectIDs, projectID) {
			return accounting.ProjectSummary{}, nil, fmt.Errorf("associated source %s is unavailable", logicalID)
		}
		digest, err := memory.Digest(snapshot.Record.Usage)
		if err != nil || digest != item.UsageRecordDigest {
			return accounting.ProjectSummary{}, nil, errors.Join(fmt.Errorf("associated source %s usage digest changed", logicalID), err)
		}
		if item.Shared != (len(snapshot.Record.ProjectIDs) > 1) {
			return accounting.ProjectSummary{}, nil, fmt.Errorf("associated source %s sharing state changed", logicalID)
		}
		models := make([]accounting.ModelAccounting, len(snapshot.Record.Usage.Models))
		for index, model := range snapshot.Record.Usage.Models {
			models[index] = accounting.ModelAccounting{ModelUsage: model}
		}
		sessionAccounting := &accounting.SessionAccounting{
			StartedAt: snapshot.Record.Usage.StartedAt, EndedAt: snapshot.Record.Usage.EndedAt,
			DurationMS: snapshot.Record.Usage.DurationMS, Models: models,
			TotalTokens: snapshot.Record.Usage.TotalTokens,
		}
		identity := sha256.Sum256([]byte(logicalID))
		reports = append(reports, ledger.SessionReport{
			ID: "session-" + fmt.Sprintf("%x", identity[:16]), ProjectID: projectID,
			SessionID: logicalID, Revision: 1, Accounting: sessionAccounting,
		})
	}
	sort.Slice(reports, func(i, j int) bool {
		left, right := reports[i].Accounting, reports[j].Accounting
		if left.StartedAt != right.StartedAt {
			return left.StartedAt < right.StartedAt
		}
		return reports[i].SessionID < reports[j].SessionID
	})
	accountingInputs := make([]*accounting.SessionAccounting, 0, len(reports))
	for index := range reports {
		if index > 0 {
			reports[index].PreviousSessionID = reports[index-1].SessionID
			reports[index-1].NextSessionID = reports[index].SessionID
		}
		accountingInputs = append(accountingInputs, reports[index].Accounting)
	}
	summary, err := accounting.Aggregate(accountingInputs)
	return summary, reports, err
}

func containsProjectID(values []string, projectID string) bool {
	for _, value := range values {
		if value == projectID {
			return true
		}
	}
	return false
}

func renderPlanChangesFiles(plan presentation.RenderPlan) bool {
	for _, file := range plan.Files {
		if !file.ExpectedExists || !bytes.Equal(file.Expected, file.Desired) {
			return true
		}
	}
	return false
}

func notifyPhase(observer func(string) error, phase string) error {
	if observer == nil {
		return nil
	}
	if err := observer(phase); err != nil {
		return fmt.Errorf("persist scan phase %s: %w", phase, err)
	}
	return nil
}

func nextProjectionRevision(accepted reviewv2.AcceptedV3, reviewBody, historyBody []byte, preparedGeneration, publishedGeneration string) int {
	revision := accepted.State.Review.Revision
	reviewDigest := fmt.Sprintf("%x", sha256.Sum256(reviewBody))
	historyDigest := fmt.Sprintf("%x", sha256.Sum256(historyBody))
	if preparedGeneration != publishedGeneration || reviewDigest != accepted.State.Machine.ReviewSHA256 || historyDigest != accepted.State.Machine.HistorySHA256 {
		return revision + 1
	}
	return revision
}

func captureCurrentPresentation(accepted reviewv2.AcceptedV3) (reviewv2.LegacyPresentation, []presentation.Patch, error) {
	state := accepted.State
	legacy := reviewv2.LegacyPresentation{
		Review:              state.Review,
		Events:              append([]reviewv2.Event(nil), state.Events...),
		Compatibility:       state.Machine.LegacyCompatibility,
		HasMachineInternals: true,
	}

	previousPatches := make([]presentation.Patch, 0, len(state.Machine.HumanPatches)+len(state.Machine.OrphanPatches))
	for _, wire := range append(append([]reviewv2.HumanPatchWire(nil), state.Machine.HumanPatches...), state.Machine.OrphanPatches...) {
		previousPatches = append(previousPatches, presentation.Patch{
			EntityID: wire.EntityID, Field: wire.Field, Operation: presentation.Operation(wire.Operation),
			Value: wire.Value, Values: cloneOptionalStrings(wire.Values), BaseGeneratedHash: wire.BaseGeneratedHash,
		})
	}
	previousBaselines := make([]presentation.Baseline, 0, len(state.Machine.GeneratedBaselines))
	for _, wire := range state.Machine.GeneratedBaselines {
		previousBaselines = append(previousBaselines, presentation.Baseline{
			EntityID: wire.EntityID, Field: wire.Field, Kind: presentation.FieldKind(wire.Kind),
			Value: wire.Value, Values: cloneOptionalStrings(wire.Values), GeneratedHash: wire.GeneratedHash,
		})
	}

	knownFields := make(map[string]struct{}, len(previousBaselines))
	for _, baseline := range previousBaselines {
		knownFields[baseline.EntityID+"\x00"+baseline.Field] = struct{}{}
	}
	allFields := currentFieldObservations(state.Review, state.Events)
	fields := make([]presentation.FieldObservation, 0, len(allFields))
	for _, field := range allFields {
		if _, known := knownFields[field.EntityID+"\x00"+field.Field]; known {
			fields = append(fields, field)
		}
	}
	captured, err := presentation.Capture(presentation.CaptureInput{
		PreviousPatches: previousPatches, PreviousBaselines: previousBaselines, Fields: fields,
	})
	if err != nil {
		return reviewv2.LegacyPresentation{}, nil, err
	}
	return legacy, captured.Patches, nil
}

func cloneOptionalStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

func currentFieldObservations(review reviewv2.Review, events []reviewv2.Event) []presentation.FieldObservation {
	fields := []presentation.FieldObservation{
		{EntityID: "project-overview", Field: "goal", Present: true, Value: review.Goal},
		{EntityID: "project-overview", Field: "status", Present: true, Value: review.Status},
		{EntityID: "project-overview", Field: "next_action", Present: true, Value: review.NextAction},
	}
	for _, risk := range review.Risks {
		fields = append(fields,
			presentation.FieldObservation{EntityID: risk.ID, Field: "visibility", Present: true, Value: "visible"},
			presentation.FieldObservation{EntityID: risk.ID, Field: "title", Present: true, Value: risk.Title},
			presentation.FieldObservation{EntityID: risk.ID, Field: "status", Present: true, Value: risk.Status},
			presentation.FieldObservation{EntityID: risk.ID, Field: "detail", Present: true, Value: risk.Detail},
		)
	}
	for _, decision := range review.Decisions {
		fields = append(fields,
			presentation.FieldObservation{EntityID: decision.ID, Field: "visibility", Present: true, Value: "visible"},
			presentation.FieldObservation{EntityID: decision.ID, Field: "title", Present: true, Value: decision.Title},
			presentation.FieldObservation{EntityID: decision.ID, Field: "rationale", Present: true, Value: decision.Rationale},
			presentation.FieldObservation{EntityID: decision.ID, Field: "impact", Present: true, Value: decision.Impact},
			presentation.FieldObservation{EntityID: decision.ID, Field: "status", Present: true, Value: decision.Status},
		)
	}
	for _, event := range events {
		fields = append(fields,
			presentation.FieldObservation{EntityID: event.ID, Field: "visibility", Present: true, Value: "visible"},
			presentation.FieldObservation{EntityID: event.ID, Field: "title", Present: true, Value: event.Title},
			presentation.FieldObservation{EntityID: event.ID, Field: "meaning", Present: true, Value: event.Meaning},
			presentation.FieldObservation{EntityID: event.ID, Field: "summary", Present: true, Value: event.Summary},
			presentation.FieldObservation{EntityID: event.ID, Field: "why", Present: true, Value: event.Why},
			presentation.FieldObservation{EntityID: event.ID, Field: "changes", Present: true, Values: append([]string{}, event.Changes...)},
			presentation.FieldObservation{EntityID: event.ID, Field: "results", Present: true, Values: append([]string{}, event.Results...)},
			presentation.FieldObservation{EntityID: event.ID, Field: "next", Present: true, Value: event.Next},
		)
	}
	return fields
}

func loadCurrentProjectFiles(projectDir *pathguard.Directory) (currentProjectFiles, error) {
	if projectDir == nil {
		return currentProjectFiles{}, errors.New("project directory is required")
	}
	result := currentProjectFiles{expected: make(map[string][]byte, 3)}
	files := []struct {
		relative string
		maximum  int64
		body     *[]byte
		found    *bool
	}{
		{reviewv2.ReviewRelativePath, reviewv2.MaxDocumentBytes, &result.reviewBody, &result.reviewFound},
		{reviewv2.HistoryRelativePath, reviewv2.MaxDocumentBytes, &result.historyBody, &result.historyFound},
		{reviewv2.MachineLedgerRelativePath, reviewv2.MaxMachineLedgerBytes, &result.ledgerBody, &result.ledgerFound},
	}
	for _, file := range files {
		body, found, err := projectDir.ReadRegularOptional(file.relative, file.maximum)
		if err != nil {
			return currentProjectFiles{}, fmt.Errorf("read current project file %s: %w", file.relative, err)
		}
		*file.body, *file.found = body, found
		if found {
			result.expected[file.relative] = append([]byte(nil), body...)
		}
	}
	return result, nil
}

func resolveGitExecutable() string {
	if p, err := exec.LookPath("git"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			if eval, err := filepath.EvalSymlinks(abs); err == nil {
				return filepath.Clean(eval)
			}
			return filepath.Clean(abs)
		}
	}
	return "/usr/bin/git"
}
