package sync

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	applyledger "github.com/neomei/SessionReviewer/internal/apply"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
)

type Trigger string

const (
	TriggerCLI      Trigger = "cli"
	TriggerWatcher  Trigger = "watcher"
	TriggerPeriodic Trigger = "periodic"
	TriggerQueue    Trigger = "queue"
)

type Options struct {
	ProjectRoot, VaultRoot, VaultReviewPath, DataRoot, ProjectID, GOOS string
	VaultCaseMode                                                      platform.CaseMode
	Retry                                                              RetryPolicy
	Debounce                                                           time.Duration
	Now                                                                func() time.Time
}

type ReconcileRequest struct {
	DryRun    bool
	EntityIDs []string
	Trigger   Trigger
}

type OperationKind string

const (
	OperationAddProject    OperationKind = "add_project"
	OperationAddVault      OperationKind = "add_vault"
	OperationUpdateProject OperationKind = "update_project"
	OperationUpdateVault   OperationKind = "update_vault"
	OperationArchive       OperationKind = "archive"
	OperationRestore       OperationKind = "restore"
	OperationRename        OperationKind = "rename"
	OperationConflict      OperationKind = "conflict"
	OperationQueue         OperationKind = "queue"
)

type Operation struct {
	EntityID     string        `json:"entity_id"`
	Kind         OperationKind `json:"kind"`
	Target       Side          `json:"target,omitempty"`
	RelativePath string        `json:"relative_path,omitempty"`
	BeforeHash   string        `json:"before_hash,omitempty"`
	AfterHash    string        `json:"after_hash,omitempty"`
}

type EntityError struct {
	EntityID string `json:"entity_id"`
	Code     string `json:"code"`
}

type Report struct {
	ProjectID  string              `json:"project_id"`
	DryRun     bool                `json:"dry_run"`
	Operations []Operation         `json:"operations"`
	Conflicts  []string            `json:"conflicts"`
	Issues     []syncdoc.ScanIssue `json:"-"`
	Errors     []EntityError       `json:"errors"`
	QueueDepth int                 `json:"queue_depth"`
	Derived    DerivedReport       `json:"derived"`
}

type Status struct {
	ProjectID     string              `json:"project_id"`
	InSync        int                 `json:"in_sync"`
	Conflicted    int                 `json:"conflicted"`
	Malformed     int                 `json:"malformed"`
	Queued        int                 `json:"queued"`
	Blocked       int                 `json:"blocked"`
	OpenConflicts []string            `json:"open_conflicts"`
	Pending       []Operation         `json:"pending"`
	DerivedState  DerivedPublishState `json:"derived_state"`
	DerivedFiles  int                 `json:"derived_files"`
}

type QueueReport struct{ Attempted, Completed, Rescheduled, Blocked int }

type Engine struct {
	options                Options
	project                *pathguard.Directory
	vault                  *pathguard.Directory
	data                   *pathguard.Directory
	bases                  BaseStore
	transactions           TransactionStore
	writer                 RootedWriter
	trustAppliedTransition func(relative string, preimageExists bool, preimageHash, targetHash string) (bool, error)
}

func NewEngine(options Options) (*Engine, error) {
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.ProjectID == "" || options.VaultReviewPath == "" || options.Now == nil ||
		(options.VaultCaseMode != platform.CaseSensitive && options.VaultCaseMode != platform.CaseInsensitive) {
		return nil, errors.New("invalid sync engine options")
	}
	if options.Retry == (RetryPolicy{}) {
		options.Retry = DefaultRetryPolicy()
	}
	if err := validateRetryPolicy(options.Retry); err != nil {
		return nil, err
	}
	projectRoot, err := pathguard.Open(options.ProjectRoot)
	if err != nil {
		return nil, errors.New("project root is unavailable or unsafe")
	}
	vaultRoot, err := pathguard.Open(options.VaultRoot)
	if err != nil {
		_ = projectRoot.Close()
		return nil, errors.New("vault root is unavailable or unsafe")
	}
	dataRoot, err := pathguard.Open(options.DataRoot)
	if err != nil {
		_ = projectRoot.Close()
		_ = vaultRoot.Close()
		return nil, errors.New("sync data root is unavailable or unsafe")
	}
	if projectRoot.ContainsIdentity(vaultRoot.Info()) || vaultRoot.ContainsIdentity(projectRoot.Info()) {
		_ = projectRoot.Close()
		_ = vaultRoot.Close()
		_ = dataRoot.Close()
		return nil, errors.New("project and vault roots overlap")
	}
	engine := &Engine{options: options, project: projectRoot, vault: vaultRoot, data: dataRoot}
	engine.bases = BaseStore{Root: dataRoot.Root}
	engine.transactions = TransactionStore{Root: dataRoot.Root}
	engine.writer = RootedWriter{Project: projectRoot, Vault: vaultRoot, Retry: options.Retry}
	engine.trustAppliedTransition = func(relative string, preimageExists bool, preimageHash, targetHash string) (bool, error) {
		return applyledger.TrustsAppliedTransition(dataRoot.Root, options.ProjectID, relative, preimageExists, preimageHash, targetHash)
	}
	projectInventory := syncdoc.Scan(projectRoot, "docs/session-review", options.GOOS, platform.CaseSensitive)
	overview, found := projectInventory.ByID["project-overview"]
	identityMatches := found && overview.Identity.ProjectID == options.ProjectID
	if !identityMatches {
		base, baseFound, baseErr := engine.bases.Load("project-overview")
		if baseErr == nil && baseFound {
			document, parseErr := syncdoc.Parse(base.RelativePath, base.Content)
			identity, identityErr := document.Identity()
			identityMatches = parseErr == nil && identityErr == nil && identity.ID == "project-overview" && identity.ProjectID == options.ProjectID
		}
	}
	if !identityMatches {
		_ = engine.Close()
		return nil, errors.New("project identity does not match sync mapping")
	}
	return engine, nil
}

func (engine *Engine) Close() error {
	if engine == nil {
		return nil
	}
	return errors.Join(engine.project.Close(), engine.vault.Close(), engine.data.Close())
}

func (engine *Engine) Reconcile(ctx context.Context, request ReconcileRequest) (report Report, retErr error) {
	report = Report{ProjectID: engine.options.ProjectID, DryRun: request.DryRun, Operations: []Operation{}, Conflicts: []string{}, Errors: []EntityError{}, Derived: DerivedReport{State: DerivedDeferred, Operations: []Operation{}}}
	if ctx == nil {
		return report, errors.New("sync context is required")
	}
	if request.Trigger == "" {
		request.Trigger = TriggerCLI
	}
	if request.Trigger != TriggerCLI && request.Trigger != TriggerWatcher && request.Trigger != TriggerPeriodic && request.Trigger != TriggerQueue {
		return report, errors.New("invalid sync trigger")
	}
	lock, err := project.AcquireProjectLock(engine.data.Root, "locks/sync.lock", 10*time.Second)
	if err != nil {
		return report, errors.New("sync project is locked or unsafe")
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	if err := ctx.Err(); err != nil {
		return report, err
	}
	transactions, err := engine.transactions.List()
	if err != nil {
		return report, errors.New("sync transaction state is invalid")
	}
	if len(transactions) != 0 {
		if request.DryRun {
			return report, errors.New("sync recovery is required before dry-run")
		}
		if err := engine.recoverTransactions(ctx, transactions); err != nil {
			return report, errors.New("sync transaction recovery failed")
		}
	}

	projectInventory := syncdoc.Scan(engine.project, "docs/session-review", engine.options.GOOS, platform.CaseSensitive)
	vaultInventory, vaultReady, err := engine.scanVault()
	if err != nil {
		return report, err
	}
	report.Issues = append(report.Issues, projectInventory.Issues...)
	report.Issues = append(report.Issues, vaultInventory.Issues...)
	bases, err := engine.bases.List()
	if err != nil {
		return report, errors.New("sync merge-base state is invalid")
	}
	baseByID := make(map[string]BaseRecord, len(bases))
	ids := make(map[string]struct{}, len(bases)+len(projectInventory.ByID)+len(vaultInventory.ByID))
	for _, base := range bases {
		baseByID[base.EntityID] = base
		ids[base.EntityID] = struct{}{}
	}
	for id := range projectInventory.ByID {
		ids[id] = struct{}{}
	}
	for id := range vaultInventory.ByID {
		ids[id] = struct{}{}
	}
	if len(request.EntityIDs) != 0 {
		selected := make(map[string]struct{}, len(request.EntityIDs))
		for _, id := range request.EntityIDs {
			if !stableBaseID.MatchString(id) {
				return report, errors.New("invalid selected sync entity")
			}
			selected[id] = struct{}{}
		}
		for id := range ids {
			if _, ok := selected[id]; !ok {
				delete(ids, id)
			}
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	occupied := occupiedEntityPaths(projectInventory, vaultInventory, engine.options)
	projectIssuePaths := scanIssuePaths(projectInventory.Issues)
	vaultIssuePaths := scanIssuePaths(vaultInventory.Issues)
	dryRunDerivedStale := false
	for _, id := range ordered {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		baseRecord, hasBase := baseByID[id]
		var base *syncdoc.Document
		if hasBase {
			document, err := syncdoc.Parse(baseRecord.RelativePath, baseRecord.Content)
			if err != nil {
				return report, errors.New("sync merge-base state is invalid")
			}
			base = &document
		}
		projectCandidate := inventoryCandidate(projectInventory, id, "docs/session-review")
		vaultCandidate := inventoryCandidate(vaultInventory, id, engine.options.VaultReviewPath)
		if scanIssuesBlockEntity(id, baseRecord, hasBase, projectInventory.Issues, vaultInventory.Issues, projectIssuePaths, vaultIssuePaths, engine.options.VaultReviewPath) {
			report.Errors = append(report.Errors, EntityError{EntityID: id, Code: "malformed_source"})
			continue
		}
		if candidateSensitive(projectCandidate) || candidateSensitive(vaultCandidate) {
			report.Errors = append(report.Errors, EntityError{EntityID: id, Code: "sensitive_content"})
			continue
		}
		result := Merge(MergeInput{EntityID: id, Base: base, Project: projectCandidate, Vault: vaultCandidate,
			ProjectID: engine.options.ProjectID, BasePath: baseRecord.RelativePath, GOOS: engine.options.GOOS,
			CaseMode: engine.options.VaultCaseMode, OccupiedPathKeys: occupied})
		if result.Kind == MergeConflict && result.Reason == "protected_provenance" {
			trusted, trustErr := engine.trustedAppliedProjectResult(baseRecord, hasBase, projectCandidate, vaultCandidate)
			if trustErr != nil {
				report.Errors = append(report.Errors, EntityError{EntityID: id, Code: "apply_receipt_invalid"})
				continue
			}
			if trusted != nil {
				result = *trusted
			}
		}
		if result.Kind == MergeConflict {
			conflictID := "conflict-" + id
			report.Conflicts = append(report.Conflicts, conflictID)
			report.Operations = append(report.Operations, Operation{EntityID: id, Kind: OperationConflict, RelativePath: result.Reason})
			continue
		}
		if result.Kind == MergeNoop || result.Accepted == nil {
			continue
		}
		rendered, err := result.Accepted.Render()
		if err != nil {
			report.Errors = append(report.Errors, EntityError{EntityID: id, Code: "render_failed"})
			continue
		}
		target := acceptedRelativePath(*result.Accepted, projectCandidate, vaultCandidate, baseRecord)
		if _, blocked := projectIssuePaths[path.Join("docs/session-review", target)]; blocked {
			report.Errors = append(report.Errors, EntityError{EntityID: id, Code: "malformed_source"})
			continue
		}
		if _, blocked := vaultIssuePaths[path.Join(engine.options.VaultReviewPath, target)]; blocked {
			report.Errors = append(report.Errors, EntityError{EntityID: id, Code: "malformed_source"})
			continue
		}
		operation := operationForMerge(id, target, result.Kind, projectCandidate, vaultCandidate, syncdoc.ContentHash(rendered))
		report.Operations = append(report.Operations, operation...)
		if request.DryRun {
			projectWillChange := !projectCandidate.Present || projectCandidate.RelativePath != target || !result.Accepted.SemanticEqual(projectCandidate.Document)
			baseWillChange := base != nil && (baseRecord.RelativePath != target || !result.Accepted.SemanticEqual(*base))
			if projectWillChange || baseWillChange {
				dryRunDerivedStale = true
			}
			continue
		}
		if !vaultReady {
			if err := engine.vault.EnsureDirectory(engine.options.VaultReviewPath, 0o700); err != nil {
				report.Errors = append(report.Errors, EntityError{EntityID: id, Code: "vault_unavailable"})
				continue
			}
			vaultReady = true
		}
		if err := engine.applyAccepted(ctx, id, target, rendered, result.Kind, baseRecord, hasBase, projectCandidate, vaultCandidate); err != nil {
			report.Errors = append(report.Errors, EntityError{EntityID: id, Code: "write_failed"})
			continue
		}
	}
	if len(report.Conflicts) == 0 && len(report.Issues) == 0 && len(report.Errors) == 0 && !dryRunDerivedStale {
		plan, err := engine.planDerived()
		if err != nil {
			report.Derived.State = DerivedFailed
			return report, errors.New("derived navigation planning failed")
		}
		report.Derived = plan.report()
		if !request.DryRun && plan.changed() {
			if err := engine.publishDerived(ctx, plan); err != nil {
				report.Derived.State = DerivedFailed
				return report, fmt.Errorf("derived navigation publication failed: %w", err)
			}
			report.Derived.State = DerivedCurrent
		}
	}
	sort.Strings(report.Conflicts)
	sort.Slice(report.Operations, func(i, j int) bool {
		if report.Operations[i].EntityID != report.Operations[j].EntityID {
			return report.Operations[i].EntityID < report.Operations[j].EntityID
		}
		return report.Operations[i].Kind < report.Operations[j].Kind
	})
	return report, nil
}

func (engine *Engine) trustedAppliedProjectResult(base BaseRecord, hasBase bool, projectCandidate, vaultCandidate Candidate) (*MergeResult, error) {
	if !hasBase || !projectCandidate.Present || !vaultCandidate.Present ||
		projectCandidate.RelativePath != base.RelativePath || vaultCandidate.RelativePath != base.RelativePath ||
		vaultCandidate.Hash != base.ContentHash || projectCandidate.Hash == base.ContentHash {
		return nil, nil
	}
	if err := validateCandidateClaim(projectCandidate); err != nil || !validDocumentShape(projectCandidate.Document) {
		return nil, nil
	}
	baseDocument, err := syncdoc.Parse(base.RelativePath, base.Content)
	if err != nil {
		return nil, errors.New("invalid sync merge base")
	}
	baseIdentity, baseErr := baseDocument.Identity()
	projectIdentity, projectErr := projectCandidate.Document.Identity()
	if baseErr != nil || projectErr != nil || baseIdentity != projectIdentity {
		return nil, nil
	}
	trusted, err := engine.trustAppliedTransition(path.Join("docs/session-review", projectCandidate.RelativePath), true, base.ContentHash, projectCandidate.Hash)
	if err != nil || !trusted {
		return nil, err
	}
	accepted := projectCandidate.Document
	return &MergeResult{Kind: MergeWriteVault, Accepted: &accepted}, nil
}

func scanIssuePaths(issues []syncdoc.ScanIssue) map[string]struct{} {
	result := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		result[issue.RelativePath] = struct{}{}
	}
	return result
}

func scanIssuesBlockEntity(id string, base BaseRecord, hasBase bool, projectIssues, vaultIssues []syncdoc.ScanIssue, projectPaths, vaultPaths map[string]struct{}, vaultReviewPath string) bool {
	for index, issues := range [][]syncdoc.ScanIssue{projectIssues, vaultIssues} {
		rootRelative := "docs/session-review"
		if index == 1 {
			rootRelative = vaultReviewPath
		}
		for _, issue := range issues {
			if issue.RelativePath == rootRelative {
				return true
			}
			if issue.EntityID == id {
				return true
			}
		}
	}
	if !hasBase {
		return false
	}
	_, projectBlocked := projectPaths[path.Join("docs/session-review", base.RelativePath)]
	_, vaultBlocked := vaultPaths[path.Join(vaultReviewPath, base.RelativePath)]
	return projectBlocked || vaultBlocked
}

func (engine *Engine) Status(ctx context.Context) (Status, error) {
	report, err := engine.Reconcile(ctx, ReconcileRequest{DryRun: true, Trigger: TriggerCLI})
	status := Status{
		ProjectID: engine.options.ProjectID, OpenConflicts: append([]string{}, report.Conflicts...), Pending: append([]Operation{}, report.Operations...),
		DerivedState: report.Derived.State, DerivedFiles: report.Derived.Files,
	}
	status.Conflicted = len(report.Conflicts)
	status.Malformed = len(report.Issues)
	status.Blocked = len(report.Errors)
	if err == nil {
		bases, loadErr := engine.bases.List()
		if loadErr != nil {
			return status, loadErr
		}
		pending := make(map[string]struct{}, len(report.Operations))
		for _, operation := range report.Operations {
			pending[operation.EntityID] = struct{}{}
		}
		for _, entityError := range report.Errors {
			pending[entityError.EntityID] = struct{}{}
		}
		status.InSync = len(bases) - len(pending)
		if status.InSync < 0 {
			status.InSync = 0
		}
	}
	return status, err
}

func (engine *Engine) Resolve(ctx context.Context, resolution Resolution) (report Report, retErr error) {
	report = Report{ProjectID: engine.options.ProjectID, Operations: []Operation{}, Conflicts: []string{}, Errors: []EntityError{}}
	if ctx == nil || !strings.HasPrefix(resolution.ConflictID, "conflict-") || !validResolutionAction(resolution.Action) {
		return report, ErrInvalidResolution
	}
	entityID := strings.TrimPrefix(resolution.ConflictID, "conflict-")
	if !stableBaseID.MatchString(entityID) {
		return report, ErrInvalidResolution
	}
	if resolution.Action == ManualMerge {
		if strings.TrimSpace(resolution.ManualFile) == "" {
			return report, ErrInvalidResolution
		}
	} else if resolution.ManualFile != "" {
		return report, ErrInvalidResolution
	}
	lock, err := project.AcquireProjectLock(engine.data.Root, "locks/sync.lock", 10*time.Second)
	if err != nil {
		return report, errors.New("sync project is locked or unsafe")
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			retErr = errors.Join(retErr, lock.Release())
		}
	}()
	transactions, err := engine.transactions.List()
	if err != nil {
		return report, errors.New("sync transaction state is invalid")
	}
	if len(transactions) != 0 {
		if err := engine.recoverTransactions(ctx, transactions); err != nil {
			return report, errors.New("sync transaction recovery failed")
		}
	}
	projectInventory := syncdoc.Scan(engine.project, "docs/session-review", engine.options.GOOS, platform.CaseSensitive)
	vaultInventory, _, err := engine.scanVault()
	if err != nil || len(projectInventory.Issues) != 0 || len(vaultInventory.Issues) != 0 {
		return report, ErrStaleConflict
	}
	baseRecord, found, err := engine.bases.Load(entityID)
	if err != nil || !found {
		return report, ErrStaleConflict
	}
	base, err := syncdoc.Parse(baseRecord.RelativePath, baseRecord.Content)
	if err != nil {
		return report, ErrStaleConflict
	}
	projectCandidate := inventoryCandidate(projectInventory, entityID, "docs/session-review")
	vaultCandidate := inventoryCandidate(vaultInventory, entityID, engine.options.VaultReviewPath)
	occupied := occupiedEntityPaths(projectInventory, vaultInventory, engine.options)
	merged := Merge(MergeInput{EntityID: entityID, Base: &base, Project: projectCandidate, Vault: vaultCandidate,
		ProjectID: engine.options.ProjectID, BasePath: baseRecord.RelativePath, GOOS: engine.options.GOOS,
		CaseMode: engine.options.VaultCaseMode, OccupiedPathKeys: occupied})
	if merged.Kind != MergeConflict {
		return report, ErrStaleConflict
	}
	var selected syncdoc.Document
	switch resolution.Action {
	case AcceptProject:
		if !projectCandidate.Present {
			return report, ErrInvalidResolution
		}
		selected = projectCandidate.Document
	case AcceptObsidian:
		if !vaultCandidate.Present {
			return report, ErrInvalidResolution
		}
		selected = vaultCandidate.Document
	case ManualMerge:
		selected, err = readManualResolution(resolution.ManualFile, baseRecord.RelativePath)
		if err != nil {
			return report, ErrInvalidResolution
		}
	}
	if err := selected.ValidateHumanChanges(base); err != nil {
		return report, err
	}
	selected, err = selected.FinalizeHumanMerge(base, true)
	if err != nil {
		return report, err
	}
	selected, err = selected.WithSyncStatus("synced")
	if err != nil || candidateSensitive(Candidate{Present: true, RelativePath: baseRecord.RelativePath, Document: selected, Hash: documentHash(selected)}) {
		return report, ErrSensitiveContent
	}
	rendered, err := selected.Render()
	if err != nil {
		return report, err
	}
	if err := engine.applyAccepted(ctx, entityID, baseRecord.RelativePath, rendered, MergeWriteBoth, baseRecord, true, projectCandidate, vaultCandidate); err != nil {
		return report, err
	}
	afterHash := syncdoc.ContentHash(rendered)
	report.Operations = operationForMerge(entityID, baseRecord.RelativePath, MergeWriteBoth, projectCandidate, vaultCandidate, afterHash)
	if err := lock.Release(); err != nil {
		lockHeld = false
		return report, err
	}
	lockHeld = false

	followup, err := engine.Reconcile(ctx, ReconcileRequest{Trigger: TriggerCLI})
	report.Operations = append(report.Operations, followup.Operations...)
	report.Conflicts = append([]string(nil), followup.Conflicts...)
	report.Issues = append([]syncdoc.ScanIssue(nil), followup.Issues...)
	report.Errors = append([]EntityError(nil), followup.Errors...)
	report.QueueDepth = followup.QueueDepth
	report.Derived = followup.Derived
	return report, err
}

func (engine *Engine) DrainQueue(context.Context, int) (QueueReport, error) {
	return QueueReport{}, nil
}

func (engine *Engine) scanVault() (syncdoc.Inventory, bool, error) {
	full := filepath.Join(engine.options.VaultRoot, filepath.FromSlash(engine.options.VaultReviewPath))
	opened, remaining, err := pathguard.OpenDeepest(full)
	if err != nil {
		return syncdoc.Inventory{}, false, errors.New("vault review path is unsafe")
	}
	_ = opened.Close()
	if len(remaining) != 0 {
		return syncdoc.Inventory{ByID: map[string]syncdoc.Entry{}}, false, nil
	}
	return syncdoc.Scan(engine.vault, engine.options.VaultReviewPath, engine.options.GOOS, engine.options.VaultCaseMode), true, nil
}

func inventoryCandidate(inventory syncdoc.Inventory, id, prefix string) Candidate {
	entry, found := inventory.ByID[id]
	if !found {
		return Candidate{}
	}
	relative := strings.TrimPrefix(entry.RelativePath, strings.TrimSuffix(prefix, "/")+"/")
	return Candidate{Present: true, RelativePath: relative, Document: entry.Document, Hash: entry.ContentHash}
}

func occupiedEntityPaths(projectInventory, vaultInventory syncdoc.Inventory, options Options) map[string]string {
	result := make(map[string]string)
	for _, item := range []struct {
		inventory syncdoc.Inventory
		prefix    string
	}{{projectInventory, "docs/session-review"}, {vaultInventory, options.VaultReviewPath}} {
		for id, entry := range item.inventory.ByID {
			relative := strings.TrimPrefix(entry.RelativePath, strings.TrimSuffix(item.prefix, "/")+"/")
			key, err := platform.PathKey(options.GOOS, options.VaultCaseMode, relative)
			if err == nil {
				result[key] = id
			}
		}
	}
	return result
}

func candidateSensitive(candidate Candidate) bool {
	if !candidate.Present {
		return false
	}
	rendered, err := candidate.Document.Render()
	return err != nil || len(redact.Default().Text(string(rendered)).Findings) != 0
}

func acceptedRelativePath(document syncdoc.Document, projectCandidate, vaultCandidate Candidate, base BaseRecord) string {
	if base.RelativePath != "" {
		if projectCandidate.Present && projectCandidate.RelativePath != base.RelativePath {
			return projectCandidate.RelativePath
		}
		if vaultCandidate.Present && vaultCandidate.RelativePath != base.RelativePath {
			return vaultCandidate.RelativePath
		}
		return base.RelativePath
	}
	if projectCandidate.Present {
		return projectCandidate.RelativePath
	}
	return vaultCandidate.RelativePath
}

func operationForMerge(id, relative string, kind MergeKind, projectCandidate, vaultCandidate Candidate, afterHash string) []Operation {
	result := []Operation{}
	if kind == MergeWriteProject || kind == MergeWriteBoth {
		op := Operation{EntityID: id, Kind: OperationUpdateProject, Target: SideProject, RelativePath: relative, AfterHash: afterHash}
		if !projectCandidate.Present {
			op.Kind = OperationAddProject
		} else {
			op.BeforeHash = projectCandidate.Hash
		}
		result = append(result, op)
	}
	if kind == MergeWriteVault || kind == MergeWriteBoth {
		op := Operation{EntityID: id, Kind: OperationUpdateVault, Target: SideVault, RelativePath: relative, AfterHash: afterHash}
		if !vaultCandidate.Present {
			op.Kind = OperationAddVault
		} else {
			op.BeforeHash = vaultCandidate.Hash
		}
		result = append(result, op)
	}
	return result
}

func (engine *Engine) applyAccepted(ctx context.Context, id, relative string, content []byte, kind MergeKind, previous BaseRecord, hasBase bool, projectCandidate, vaultCandidate Candidate) error {
	projectRelative := path.Join("docs/session-review", relative)
	vaultRelative := path.Join(engine.options.VaultReviewPath, relative)
	hash := syncdoc.ContentHash(content)
	expected := ""
	if hasBase {
		expected = previous.ContentHash
	}
	transaction := Transaction{
		Version: 1, Kind: TxnEntitySync, EntityID: id, DesiredHash: hash, ExpectedBaseHash: expected,
		ExpectedProjectHash: projectCandidate.Hash, ExpectedVaultHash: vaultCandidate.Hash,
		Stage: TxnPlanned, UpdatedAt: engine.options.Now().UTC(),
	}
	if err := engine.transactions.Save(transaction); err != nil {
		return err
	}
	for _, target := range []struct {
		side      Side
		relative  string
		write     bool
		candidate Candidate
	}{
		{SideProject, projectRelative, kind == MergeWriteProject || kind == MergeWriteBoth, projectCandidate},
		{SideVault, vaultRelative, kind == MergeWriteVault || kind == MergeWriteBoth, vaultCandidate},
	} {
		directory := engine.project
		if target.side == SideVault {
			directory = engine.vault
		}
		if hasBase && distinctRelativePath(engine.options, previous.RelativePath, relative) {
			oldRelative, oldHash := path.Join("docs/session-review", previous.RelativePath), previous.ProjectHash
			if target.side == SideVault {
				oldRelative, oldHash = path.Join(engine.options.VaultReviewPath, previous.RelativePath), previous.VaultHash
			}
			if err := directory.RemoveRegularIfHashMatches(oldRelative, oldHash); err != nil {
				return err
			}
		}
		if parent := path.Dir(target.relative); parent != "." {
			if err := directory.EnsureDirectory(parent, 0o700); err != nil {
				return err
			}
		}
		if target.write {
			expectedExists := target.candidate.Present && target.candidate.RelativePath == relative
			var expectedContent []byte
			if expectedExists {
				var renderErr error
				expectedContent, renderErr = target.candidate.Document.Render()
				if renderErr != nil {
					return errors.New("sync target preimage cannot be rendered")
				}
			}
			if err := engine.writer.WriteIfUnchanged(ctx, target.side, target.relative, content, 0o644, expectedContent, expectedExists); err != nil {
				return err
			}
		}
		read, found, err := directory.ReadRegular(target.relative, int64(syncdoc.MaxDocumentBytes))
		if err != nil || !found || syncdoc.ContentHash(read) != syncdoc.ContentHash(content) {
			return errors.New("sync target verification failed")
		}
		if target.side == SideProject {
			transaction.Stage = TxnProjectWritten
		} else {
			transaction.Stage = TxnVaultWritten
		}
		transaction.UpdatedAt = engine.options.Now().UTC()
		if err := engine.transactions.Save(transaction); err != nil {
			return err
		}
	}
	if err := engine.bases.Commit(expected, BaseRecord{Version: 1, EntityID: id, RelativePath: relative, ContentHash: hash, ProjectHash: hash, VaultHash: hash, Content: content, SyncedAt: engine.options.Now().UTC()}); err != nil {
		return err
	}
	transaction.Stage = TxnBaseCommitted
	transaction.UpdatedAt = engine.options.Now().UTC()
	if err := engine.transactions.Save(transaction); err != nil {
		return err
	}
	return engine.transactions.Remove(id)
}

func distinctRelativePath(options Options, first, second string) bool {
	firstKey, firstErr := platform.PathKey(options.GOOS, options.VaultCaseMode, first)
	secondKey, secondErr := platform.PathKey(options.GOOS, options.VaultCaseMode, second)
	return firstErr != nil || secondErr != nil || firstKey != secondKey
}

func (engine *Engine) recoverTransactions(ctx context.Context, transactions []Transaction) error {
	projectInventory := syncdoc.Scan(engine.project, "docs/session-review", engine.options.GOOS, platform.CaseSensitive)
	vaultInventory, vaultReady, err := engine.scanVault()
	if err != nil {
		return err
	}
	if len(projectInventory.Issues) != 0 || len(vaultInventory.Issues) != 0 {
		return errors.New("cannot recover while a synchronized tree is malformed")
	}
	bases, err := engine.bases.List()
	if err != nil {
		return err
	}
	baseByID := make(map[string]BaseRecord, len(bases))
	for _, base := range bases {
		baseByID[base.EntityID] = base
	}
	for _, transaction := range transactions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if transaction.Kind == TxnDerivedPublish {
			if err := engine.recoverDerived(ctx, transaction); err != nil {
				return err
			}
			continue
		}
		base, hasBase := baseByID[transaction.EntityID]
		if hasBase && base.ContentHash == transaction.DesiredHash {
			if err := engine.transactions.Remove(transaction.EntityID); err != nil {
				return err
			}
			continue
		}
		expected := transaction.ExpectedBaseHash
		if hasBase && base.ContentHash != expected {
			return ErrStaleBase
		}
		var baseDocument syncdoc.Document
		if hasBase {
			baseDocument, err = syncdoc.Parse(base.RelativePath, base.Content)
			if err != nil {
				return errors.New("interrupted merge base is invalid")
			}
		}
		projectEntry, projectFound := projectInventory.ByID[transaction.EntityID]
		vaultEntry, vaultFound := vaultInventory.ByID[transaction.EntityID]
		var source []byte
		relative := ""
		if projectFound && projectEntry.ContentHash == transaction.DesiredHash {
			source = projectEntry.Content
			relative = strings.TrimPrefix(projectEntry.RelativePath, "docs/session-review/")
		} else if vaultFound && vaultEntry.ContentHash == transaction.DesiredHash {
			source = vaultEntry.Content
			relative = strings.TrimPrefix(vaultEntry.RelativePath, strings.TrimSuffix(engine.options.VaultReviewPath, "/")+"/")
		}
		if source == nil {
			if transaction.Stage == TxnPlanned {
				if err := engine.transactions.Remove(transaction.EntityID); err != nil {
					return err
				}
				continue
			}
			return errors.New("interrupted desired content is unavailable")
		}
		if !vaultReady {
			if err := engine.vault.EnsureDirectory(engine.options.VaultReviewPath, 0o700); err != nil {
				return err
			}
			vaultReady = true
		}
		for _, target := range []struct {
			directory *pathguard.Directory
			side      Side
			relative  string
			entry     syncdoc.Entry
			found     bool
			expected  string
		}{
			{engine.project, SideProject, path.Join("docs/session-review", relative), projectEntry, projectFound, transaction.ExpectedProjectHash},
			{engine.vault, SideVault, path.Join(engine.options.VaultReviewPath, relative), vaultEntry, vaultFound, transaction.ExpectedVaultHash},
		} {
			if target.found && target.entry.ContentHash != transaction.DesiredHash {
				if target.expected != "" && target.entry.ContentHash != target.expected {
					return errors.New("interrupted sync target changed after transaction planning")
				}
				if target.expected == "" && hasBase && !target.entry.Document.SemanticEqual(baseDocument) {
					return errors.New("interrupted sync target changed semantically")
				}
			}
			entryAtTarget := target.found && target.entry.RelativePath == target.relative
			if hasBase && distinctRelativePath(engine.options, base.RelativePath, relative) {
				oldRelative, oldHash := path.Join("docs/session-review", base.RelativePath), base.ProjectHash
				if target.side == SideVault {
					oldRelative, oldHash = path.Join(engine.options.VaultReviewPath, base.RelativePath), base.VaultHash
				}
				if err := target.directory.RemoveRegularIfHashMatches(oldRelative, oldHash); err != nil {
					return err
				}
			}
			if entryAtTarget && target.entry.ContentHash == transaction.DesiredHash {
				continue
			}
			if parent := path.Dir(target.relative); parent != "." {
				if err := target.directory.EnsureDirectory(parent, 0o700); err != nil {
					return err
				}
			}
			var expectedContent []byte
			if entryAtTarget {
				expectedContent = target.entry.Content
			}
			if err := engine.writer.WriteIfUnchanged(ctx, target.side, target.relative, source, 0o644, expectedContent, entryAtTarget); err != nil {
				return err
			}
		}
		if err := engine.bases.Commit(expected, BaseRecord{Version: 1, EntityID: transaction.EntityID, RelativePath: relative, ContentHash: transaction.DesiredHash, ProjectHash: transaction.DesiredHash, VaultHash: transaction.DesiredHash, Content: source, SyncedAt: engine.options.Now().UTC()}); err != nil {
			return err
		}
		if err := engine.transactions.Remove(transaction.EntityID); err != nil {
			return err
		}
	}
	return nil
}

func candidateEntryHash(entry syncdoc.Entry, found bool) string {
	if !found {
		return ""
	}
	return entry.ContentHash
}

func readManualResolution(filename, relative string) (syncdoc.Document, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return syncdoc.Document{}, err
	}
	directory, err := pathguard.Open(filepath.Dir(absolute))
	if err != nil {
		return syncdoc.Document{}, err
	}
	defer directory.Close()
	body, found, err := directory.ReadRegular(filepath.Base(absolute), int64(syncdoc.MaxDocumentBytes))
	if err != nil || !found {
		return syncdoc.Document{}, errors.New("manual resolution file is unavailable or unsafe")
	}
	return syncdoc.Parse(relative, body)
}

func documentHash(document syncdoc.Document) string {
	rendered, err := document.Render()
	if err != nil {
		return ""
	}
	return syncdoc.ContentHash(rendered)
}

func (status Status) String() string {
	return fmt.Sprintf("in_sync=%d conflicted=%d malformed=%d queued=%d blocked=%d derived=%s files=%d", status.InSync, status.Conflicted, status.Malformed, status.Queued, status.Blocked, status.DerivedState, status.DerivedFiles)
}
