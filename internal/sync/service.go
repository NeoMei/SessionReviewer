package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	applyledger "github.com/neomei/SessionReviewer/internal/apply"
	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
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
	ProjectRootExpected, VaultRootExpected, DataRootExpected           os.FileInfo
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
	OperationEstablishBase OperationKind = "establish_base"
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

type MigrationReport struct {
	Required bool     `json:"required"`
	DryRun   bool     `json:"dry_run"`
	Creates  []string `json:"creates"`
	Archives []string `json:"archives"`
}

type MachinePublishState string

const (
	MachineCurrent MachinePublishState = "current"
	MachinePending MachinePublishState = "pending"
	MachineBlocked MachinePublishState = "blocked"
)

type MachineReport struct {
	State      MachinePublishState `json:"state"`
	Operations []Operation         `json:"operations"`
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
	Migration  MigrationReport     `json:"migration"`
	Machine    MachineReport       `json:"machine"`
}

type Status struct {
	ProjectID          string              `json:"project_id"`
	InSync             int                 `json:"in_sync"`
	Conflicted         int                 `json:"conflicted"`
	Malformed          int                 `json:"malformed"`
	Queued             int                 `json:"queued"`
	Blocked            int                 `json:"blocked"`
	OpenConflicts      []string            `json:"open_conflicts"`
	Pending            []Operation         `json:"pending"`
	DerivedState       DerivedPublishState `json:"derived_state"`
	DerivedFiles       int                 `json:"derived_files"`
	Migration          string              `json:"migration"`
	MachineState       MachinePublishState `json:"machine_state"`
	LastSuccessfulSync string              `json:"last_successful_sync"`
	PendingOperations  []Operation         `json:"pending_operations"`
	HiddenConflictIDs  []string            `json:"hidden_conflict_ids"`
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
	vaultTarget            ReviewTargetPin
	trustAppliedTransition func(relative string, preimageExists bool, preimageHash, targetHash string) (bool, error)
}

// ReviewTargetPin authenticates the configured Vault review subtree without
// exposing its physical identities. It is reusable by mapping and worker
// preflight checks so sync cannot be the first component to discover overlap.
type ReviewTargetPin struct {
	full       string
	anchorInfo os.FileInfo
	targetInfo os.FileInfo
	caseMode   platform.CaseMode
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
	if options.ProjectRootExpected != nil && !os.SameFile(options.ProjectRootExpected, projectRoot.Info()) {
		_ = projectRoot.Close()
		return nil, errors.New("project root identity changed after mapping resolution")
	}
	vaultRoot, err := pathguard.Open(options.VaultRoot)
	if err != nil {
		_ = projectRoot.Close()
		return nil, errors.New("vault root is unavailable or unsafe")
	}
	if options.VaultRootExpected != nil && !os.SameFile(options.VaultRootExpected, vaultRoot.Info()) {
		_ = projectRoot.Close()
		_ = vaultRoot.Close()
		return nil, errors.New("vault root identity changed after mapping resolution")
	}
	dataRoot, err := pathguard.Open(options.DataRoot)
	if err != nil {
		_ = projectRoot.Close()
		_ = vaultRoot.Close()
		return nil, errors.New("sync data root is unavailable or unsafe")
	}
	if options.DataRootExpected != nil && !os.SameFile(options.DataRootExpected, dataRoot.Info()) {
		_ = projectRoot.Close()
		_ = vaultRoot.Close()
		_ = dataRoot.Close()
		return nil, errors.New("sync data root identity changed after mapping resolution")
	}
	// A common Obsidian layout keeps the reviewed Project beneath its Vault.
	// That direction is safe because all Vault writes remain restricted to the
	// configured review subtree. The inverse would place arbitrary Vault output
	// beneath the authoritative Project and is rejected.
	if vaultRoot.ContainsIdentity(projectRoot.Info()) {
		_ = projectRoot.Close()
		_ = vaultRoot.Close()
		_ = dataRoot.Close()
		return nil, errors.New("vault root must not be nested in project root")
	}
	if projectRoot.ContainsIdentity(dataRoot.Info()) || dataRoot.ContainsIdentity(projectRoot.Info()) ||
		vaultRoot.ContainsIdentity(dataRoot.Info()) || dataRoot.ContainsIdentity(vaultRoot.Info()) {
		_ = projectRoot.Close()
		_ = vaultRoot.Close()
		_ = dataRoot.Close()
		return nil, errors.New("sync data root must be disjoint from project and vault roots")
	}
	vaultTarget, err := PinReviewTarget(options.VaultReviewPath, options.VaultCaseMode, projectRoot, vaultRoot)
	if err != nil {
		_ = projectRoot.Close()
		_ = vaultRoot.Close()
		_ = dataRoot.Close()
		return nil, err
	}
	engine := &Engine{options: options, project: projectRoot, vault: vaultRoot, data: dataRoot, vaultTarget: vaultTarget}
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

func (engine *Engine) verifyRootBindings() error {
	if engine == nil || engine.project == nil || engine.vault == nil || engine.data == nil {
		return errors.New("sync root bindings are unavailable")
	}
	for _, expected := range []struct {
		name string
		path string
		info os.FileInfo
	}{
		{name: "project", path: engine.project.Path, info: engine.project.Info()},
		{name: "vault", path: engine.vault.Path, info: engine.vault.Info()},
		{name: "data", path: engine.data.Path, info: engine.data.Info()},
	} {
		reopened, err := pathguard.Open(expected.path)
		if err != nil {
			return fmt.Errorf("sync %s root identity changed", expected.name)
		}
		same := os.SameFile(expected.info, reopened.Info())
		closeErr := reopened.Close()
		if closeErr != nil || !same {
			return fmt.Errorf("sync %s root identity changed", expected.name)
		}
	}
	return engine.vaultTarget.Recheck(engine.project, engine.vault)
}

// PinReviewTarget requires Project and the editable Vault review subtree to be
// disjoint in both containment directions. Existing components are also
// authenticated by physical ancestry, catching aliases beyond path spelling.
func PinReviewTarget(reviewPath string, caseMode platform.CaseMode, project, vault *pathguard.Directory) (ReviewTargetPin, error) {
	if project == nil || vault == nil || reviewPath == "" || strings.Contains(reviewPath, `\`) ||
		path.Clean(reviewPath) != reviewPath || path.IsAbs(reviewPath) || reviewPath == "." || reviewPath == ".." || strings.HasPrefix(reviewPath, "../") ||
		(caseMode != platform.CaseSensitive && caseMode != platform.CaseInsensitive) {
		return ReviewTargetPin{}, errors.New("vault review target is invalid")
	}
	full := filepath.Join(vault.Path, filepath.FromSlash(reviewPath))
	withinVault, err := filepath.Rel(vault.Path, full)
	if err != nil || withinVault == "." || withinVault == ".." || strings.HasPrefix(withinVault, ".."+string(filepath.Separator)) {
		return ReviewTargetPin{}, errors.New("vault review target escapes its Vault root")
	}
	opened, remaining, err := pathguard.OpenDeepest(full)
	if err != nil {
		return ReviewTargetPin{}, errors.New("vault review target is unsafe")
	}
	defer opened.Close()
	if !opened.ContainsIdentity(vault.Info()) || vaultTargetOverlapsProject(full, project, opened, len(remaining) == 0, caseMode) {
		return ReviewTargetPin{}, errors.New("vault review target must be disjoint from the authoritative Project")
	}
	binding := ReviewTargetPin{full: full, anchorInfo: opened.Info(), caseMode: caseMode}
	if len(remaining) == 0 {
		binding.targetInfo = opened.Info()
	}
	return binding, nil
}

func vaultTargetOverlapsProject(full string, project, deepest *pathguard.Directory, targetExists bool, caseMode platform.CaseMode) bool {
	if project == nil || deepest == nil {
		return true
	}
	if lexicalPathContains(project.Path, full, caseMode) || lexicalPathContains(full, project.Path, caseMode) {
		return true
	}
	if deepest.ContainsIdentity(project.Info()) {
		return true
	}
	return targetExists && project.ContainsIdentity(deepest.Info())
}

func lexicalPathContains(parent, child string, caseMode platform.CaseMode) bool {
	parent = norm.NFC.String(filepath.ToSlash(filepath.Clean(parent)))
	child = norm.NFC.String(filepath.ToSlash(filepath.Clean(child)))
	if runtime.GOOS == "windows" || caseMode == platform.CaseInsensitive {
		folder := cases.Fold()
		parent = folder.String(parent)
		child = folder.String(child)
	}
	if parent == "" || child == "" {
		return false
	}
	if parent == child {
		return true
	}
	if strings.HasSuffix(parent, "/") {
		return strings.HasPrefix(child, parent)
	}
	return strings.HasPrefix(child, parent+"/")
}

// Recheck proves the current target still has the pinned ancestry and remains
// disjoint from the authoritative Project.
func (binding ReviewTargetPin) Recheck(project, vault *pathguard.Directory) error {
	if binding.full == "" || binding.anchorInfo == nil || project == nil || vault == nil ||
		(binding.caseMode != platform.CaseSensitive && binding.caseMode != platform.CaseInsensitive) {
		return errors.New("vault review target binding is unavailable")
	}
	opened, remaining, err := pathguard.OpenDeepest(binding.full)
	if err != nil {
		return errors.New("vault review target identity changed")
	}
	defer opened.Close()
	if !opened.ContainsIdentity(vault.Info()) ||
		vaultTargetOverlapsProject(binding.full, project, opened, len(remaining) == 0, binding.caseMode) ||
		!opened.ContainsIdentity(binding.anchorInfo) {
		return errors.New("vault review target identity changed")
	}
	if binding.targetInfo != nil && (len(remaining) != 0 || !os.SameFile(binding.targetInfo, opened.Info())) {
		return errors.New("vault review target identity changed")
	}
	return nil
}

func (engine *Engine) Reconcile(ctx context.Context, request ReconcileRequest) (report Report, retErr error) {
	report = Report{
		ProjectID: engine.options.ProjectID, DryRun: request.DryRun,
		Operations: []Operation{}, Conflicts: []string{}, Errors: []EntityError{},
		Derived:   DerivedReport{State: DerivedDeferred, Operations: []Operation{}},
		Migration: MigrationReport{DryRun: request.DryRun, Creates: []string{}, Archives: []string{}},
		Machine:   MachineReport{State: MachinePending, Operations: []Operation{}},
	}
	if ctx == nil {
		return report, errors.New("sync context is required")
	}
	if err := engine.verifyRootBindings(); err != nil {
		return report, err
	}
	defer func() { retErr = errors.Join(retErr, engine.verifyRootBindings()) }()
	if request.Trigger == "" {
		request.Trigger = TriggerCLI
	}
	if request.Trigger != TriggerCLI && request.Trigger != TriggerWatcher && request.Trigger != TriggerPeriodic && request.Trigger != TriggerQueue {
		return report, errors.New("invalid sync trigger")
	}
	selectedScope := len(request.EntityIDs) != 0
	lock, err := project.AcquireProjectLock(engine.data.Root, "locks/sync.lock", 10*time.Second)
	if err != nil {
		return report, errors.New("sync project is locked or unsafe")
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	if err := ctx.Err(); err != nil {
		return report, err
	}
	migrationCommitted := false
	if !request.DryRun && request.Trigger == TriggerCLI {
		if err := reviewv2.RecoverMigration(engine.options.ProjectRoot, engine.project.Info(), engine.options.DataRoot, engine.data.Info()); err != nil {
			return report, errors.New("review migration recovery failed")
		}
	}
	if request.Trigger != TriggerCLI {
		pending, pendingErr := reviewv2.MigrationPending(engine.options.ProjectRoot, engine.project.Info(), engine.options.DataRoot, engine.data.Info())
		if pendingErr != nil {
			return report, errors.New("review migration state is invalid")
		}
		if pending {
			report.Migration = MigrationReport{Required: true, DryRun: request.DryRun, Creates: []string{}, Archives: []string{}}
			report.Machine.State = MachinePending
			return report, nil
		}
	}
	version, err := reviewv2.DetectVersionExpected(engine.options.ProjectRoot, engine.project.Info())
	if err != nil {
		return report, errors.New("review ledger version is invalid")
	}
	if request.Trigger != TriggerCLI && (version == reviewv2.VersionLegacy || version == reviewv2.VersionMixed) {
		report.Migration = MigrationReport{Required: true, DryRun: request.DryRun, Creates: []string{}, Archives: []string{}}
		report.Machine.State = MachinePending
		return report, nil
	}
	if version == reviewv2.VersionMixed {
		return report, errors.New("review migration recovery is required")
	}
	transactions, err := engine.transactions.List()
	if err != nil {
		return report, errors.New("sync transaction state is invalid")
	}
	if len(transactions) != 0 {
		if request.DryRun {
			return report, errors.New("sync recovery is required before dry-run")
		}
		if version == reviewv2.VersionLegacy {
			return report, errors.New("legacy sync transaction blocks migration")
		}
		if err := engine.recoverTransactions(ctx, transactions); err != nil {
			return report, fmt.Errorf("sync transaction recovery failed: %w", err)
		}
	}
	if version == reviewv2.VersionLegacy {
		plan, err := reviewv2.PlanMigration(engine.options.ProjectRoot, engine.project.Info(), engine.options.DataRoot, engine.options.Now().UTC(), engine.data.Info())
		if err != nil {
			return report, errors.New("review migration planning failed")
		}
		vaultRetirement, err := engine.planLegacyVaultRetirement(plan)
		if err != nil {
			return report, errors.New("legacy Vault is not a byte-identical Project mirror")
		}
		report.Migration = migrationReportFromReview(plan.Report(), request.DryRun)
		if request.DryRun {
			report.Machine.State = MachinePending
			return report, nil
		}
		if err := vaultRetirement.apply(engine); err != nil {
			return report, errors.New("legacy Vault retirement failed")
		}
		if err := engine.bases.ResetForMigration(); err != nil {
			return report, errors.New("legacy merge-base retirement failed")
		}
		migration, err := reviewv2.ApplyMigration(plan)
		if err != nil {
			return report, errors.New("review migration failed")
		}
		report.Migration = migrationReportFromReview(migration, false)
		migrationCommitted = true
		version = reviewv2.VersionV2
	}
	var machine machineSnapshot
	if version == reviewv2.VersionV2 {
		machine, report.Machine, err = engine.planMachineLedger()
		if err != nil {
			report.Machine.State = MachineBlocked
			report.Errors = append(report.Errors, EntityError{EntityID: machineLedgerEntityID, Code: "machine_ledger_modified"})
			return report, nil
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
	entityCommitted := false
	dryAccepted := make(map[string][]byte, 2)
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
			relative := baseRecord.RelativePath
			if relative == "" {
				relative = projectCandidate.RelativePath
			}
			if relative == "" {
				relative = vaultCandidate.RelativePath
			}
			kind := ConflictUnits
			if result.Reason == "archive_vs_modify" {
				kind = ConflictArchiveEdit
			}
			artifact, conflictErr := BuildConflict(ConflictRecord{
				Version: 1, EntityID: id, ProjectID: engine.options.ProjectID, Kind: kind,
				RelativePath: relative, BasePath: baseRecord.RelativePath,
				ProjectPath: projectCandidate.RelativePath, VaultPath: vaultCandidate.RelativePath,
				Base: bytes.Clone(baseRecord.Content), Project: candidateConflictBytes(projectCandidate), Vault: candidateConflictBytes(vaultCandidate),
				CreatedAt: engine.options.Now().UTC(),
			})
			if conflictErr != nil || artifact.Record == nil {
				report.Errors = append(report.Errors, EntityError{EntityID: id, Code: "conflict_record_failed"})
				continue
			}
			conflictID := artifact.Record.ID
			report.Conflicts = append(report.Conflicts, conflictID)
			report.Operations = append(report.Operations, Operation{EntityID: id, Kind: OperationConflict, RelativePath: result.Reason})
			if !request.DryRun {
				if err := engine.persistConflictRecord(ctx, artifact); err != nil {
					report.Errors = append(report.Errors, EntityError{EntityID: id, Code: "conflict_record_failed"})
				}
			}
			continue
		}
		if result.Kind == MergeNoop && result.Accepted != nil && !hasBase {
			rendered, renderErr := result.Accepted.Render()
			if renderErr != nil {
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
			report.Operations = append(report.Operations, Operation{
				EntityID: id, Kind: OperationEstablishBase, RelativePath: target,
				AfterHash: syncdoc.ContentHash(rendered),
			})
			if request.DryRun {
				dryAccepted[id] = bytes.Clone(rendered)
				continue
			}
			// Byte-identical Project/Vault copies still need a machine-local Base.
			// applyAccepted with MergeNoop verifies both exact preimages, writes no
			// human document, and commits the authenticated common bytes as Base.
			if err := engine.applyAccepted(ctx, id, target, rendered, MergeNoop, BaseRecord{}, false, projectCandidate, vaultCandidate); err != nil {
				report.Errors = append(report.Errors, EntityError{EntityID: id, Code: "write_failed"})
				continue
			}
			entityCommitted = true
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
			dryAccepted[id] = bytes.Clone(rendered)
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
		entityCommitted = true
	}
	if version == reviewv2.VersionV2 && !selectedScope && !request.DryRun && len(report.Conflicts) == 0 && len(report.Issues) == 0 && len(report.Errors) == 0 {
		operations, aligned, err := engine.alignCompactV2Revisions(ctx)
		if err != nil {
			report.Errors = append(report.Errors, EntityError{EntityID: "project-overview", Code: "revision_alignment_failed"})
		} else {
			report.Operations = append(report.Operations, operations...)
			entityCommitted = entityCommitted || aligned
		}
	}
	if version == reviewv2.VersionV2 && !selectedScope && request.DryRun && len(report.Conflicts) == 0 && len(report.Issues) == 0 && len(report.Errors) == 0 {
		alignment, finalBodies, aligned, err := engine.planCompactV2DryRun(projectInventory, vaultInventory, dryAccepted)
		if err != nil {
			report.Errors = append(report.Errors, EntityError{EntityID: "project-overview", Code: "revision_alignment_failed"})
		} else {
			report.Operations = append(report.Operations, alignment...)
			desired, renderErr := engine.renderMachineLedgerForAccepted(machine, finalBodies["project-overview"], finalBodies["project-history"], len(dryAccepted) != 0 || aligned || machine.humanStale)
			if renderErr != nil {
				report.Errors = append(report.Errors, EntityError{EntityID: machineLedgerEntityID, Code: "machine_ledger_invalid"})
			} else {
				operations := machineLedgerOperations(machine, syncdoc.ContentHash(desired))
				state := MachineCurrent
				if len(operations) != 0 {
					state = MachinePending
				}
				report.Machine = MachineReport{State: state, Operations: operations}
			}
		}
	}
	if len(report.Conflicts) == 0 && len(report.Issues) == 0 && len(report.Errors) == 0 {
		report.Derived = DerivedReport{State: DerivedCurrent, Operations: []Operation{}}
	}
	if version == reviewv2.VersionV2 && len(report.Conflicts) == 0 && len(report.Issues) == 0 && len(report.Errors) == 0 {
		if selectedScope {
			// The compact machine ledger is a whole-review acceptance boundary. A
			// selected reconcile cannot prove that excluded documents converged.
			report.Machine = MachineReport{State: MachinePending, Operations: []Operation{}}
		} else if request.DryRun {
			// The complete in-memory dry-run plan was populated above.
		} else if migrationCommitted || entityCommitted || machine.needsPublish() {
			machineReport, err := engine.publishMachineLedger(ctx, machine, migrationCommitted || entityCommitted || machine.humanStale, false)
			if err != nil {
				report.Machine.State = MachineBlocked
				return report, errors.New("machine ledger publication failed")
			}
			report.Machine = machineReport
		} else {
			report.Machine = machine.report()
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

type legacyVaultRetirement struct {
	files       map[string]string
	directories []string
}

func (engine *Engine) planLegacyVaultRetirement(plan reviewv2.MigrationPlan) (legacyVaultRetirement, error) {
	retirement := legacyVaultRetirement{files: make(map[string]string)}
	_, ready, err := engine.scanVault()
	if err != nil || !ready {
		return retirement, err
	}
	archives := make(map[string]struct{})
	for _, archived := range plan.Report().Archives {
		const prefix = "docs/session-review/"
		if !strings.HasPrefix(archived, prefix) {
			return legacyVaultRetirement{}, errors.New("invalid legacy migration archive path")
		}
		archives[strings.TrimPrefix(archived, prefix)] = struct{}{}
	}
	directories := make(map[string]struct{})
	err = engine.vault.WalkMarkdown(engine.options.VaultReviewPath, func(relative string, vaultBody []byte) error {
		prefix := strings.TrimSuffix(engine.options.VaultReviewPath, "/") + "/"
		if !strings.HasPrefix(relative, prefix) {
			return errors.New("legacy Vault file escaped the configured review root")
		}
		legacyRelative := strings.TrimPrefix(relative, prefix)
		if _, expected := archives[legacyRelative]; !expected {
			return errors.New("legacy Vault contains a Markdown file outside the Project migration inventory")
		}
		projectRelative := path.Join("docs/session-review", legacyRelative)
		projectBody, found, err := engine.project.ReadRegular(projectRelative, int64(syncdoc.MaxDocumentBytes))
		if err != nil || !found {
			return errors.New("legacy Vault Markdown differs from the Project source")
		}
		if !bytes.Equal(projectBody, vaultBody) {
			safe := legacyGeneratedArtifact(legacyRelative, projectBody, vaultBody)
			if !safe {
				safe, err = engine.legacyVaultStillMatchesBase(legacyRelative, projectBody, vaultBody)
			}
			if err != nil || !safe {
				return errors.New("legacy Vault Markdown differs from the Project source")
			}
		}
		retirement.files[relative] = syncdoc.ContentHash(vaultBody)
		for parent := path.Dir(legacyRelative); parent != "."; parent = path.Dir(parent) {
			directories[path.Join(engine.options.VaultReviewPath, parent)] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return legacyVaultRetirement{}, err
	}
	for directory := range directories {
		retirement.directories = append(retirement.directories, directory)
	}
	sort.Slice(retirement.directories, func(left, right int) bool {
		leftDepth := strings.Count(retirement.directories[left], "/")
		rightDepth := strings.Count(retirement.directories[right], "/")
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return retirement.directories[left] > retirement.directories[right]
	})
	return retirement, nil
}

func legacyGeneratedArtifact(relative string, projectBody, vaultBody []byte) bool {
	var marker []byte
	switch relative {
	case "decisions/00-目录说明.md", "open-loops/00-目录说明.md", "sessions/00-目录说明.md":
		marker = []byte("此文件由 SessionReviewer 生成；手工修改会被覆盖。")
	case "diagrams/project-evolution.md":
		marker = []byte("This file is derived from the accepted project ledger. Manual edits are overwritten")
	default:
		return false
	}
	return bytes.Contains(projectBody, marker) && bytes.Contains(vaultBody, marker)
}

func (engine *Engine) legacyVaultStillMatchesBase(relative string, projectBody, vaultBody []byte) (bool, error) {
	projectDocument, err := syncdoc.Parse(relative, projectBody)
	if err != nil {
		return false, nil
	}
	vaultDocument, err := syncdoc.Parse(relative, vaultBody)
	if err != nil {
		return false, nil
	}
	projectIdentity, err := projectDocument.Identity()
	if err != nil {
		return false, nil
	}
	vaultIdentity, err := vaultDocument.Identity()
	if err != nil || projectIdentity != vaultIdentity || projectIdentity.ProjectID != engine.options.ProjectID {
		return false, nil
	}
	base, found, err := engine.bases.Load(projectIdentity.ID)
	if err != nil || !found {
		return false, err
	}
	return base.RelativePath == relative && base.VaultHash == syncdoc.ContentHash(vaultBody), nil
}

func (retirement legacyVaultRetirement) apply(engine *Engine) error {
	files := make([]string, 0, len(retirement.files))
	for relative := range retirement.files {
		files = append(files, relative)
	}
	sort.Strings(files)
	for _, relative := range files {
		if err := engine.vault.RemoveRegularIfHashMatches(relative, retirement.files[relative]); err != nil {
			return err
		}
	}
	for _, directory := range retirement.directories {
		if err := atomicfile.RemoveRoot(engine.vault.Root, filepath.FromSlash(directory)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func candidateConflictBytes(candidate Candidate) []byte {
	if !candidate.Present {
		return nil
	}
	if candidate.Source != nil {
		return bytes.Clone(candidate.Source)
	}
	rendered, err := candidate.Document.Render()
	if err != nil {
		return nil
	}
	return rendered
}

func (engine *Engine) alignCompactV2Revisions(ctx context.Context) ([]Operation, bool, error) {
	projectInventory := syncdoc.Scan(engine.project, "docs/session-review", engine.options.GOOS, platform.CaseSensitive)
	vaultInventory, _, err := engine.scanVault()
	if err != nil {
		return nil, false, err
	}
	if len(projectInventory.Issues) != 0 || len(vaultInventory.Issues) != 0 ||
		!compactV2Inventory(projectInventory, "docs/session-review") || !compactV2Inventory(vaultInventory, engine.options.VaultReviewPath) {
		return nil, false, errors.New("compact v2 revision inventory is invalid")
	}
	desired := 0
	revisions := make(map[string]int, 2)
	for _, id := range []string{"project-overview", "project-history"} {
		entry, found := projectInventory.ByID[id]
		if !found {
			return nil, false, errors.New("compact v2 revision peer is missing")
		}
		revision, err := compactDocumentRevision(entry.Document)
		if err != nil {
			return nil, false, err
		}
		revisions[id] = revision
		if revision > desired {
			desired = revision
		}
	}
	operations := []Operation{}
	changed := false
	for _, id := range []string{"project-overview", "project-history"} {
		if revisions[id] == desired {
			continue
		}
		projectEntry := projectInventory.ByID[id]
		units := projectEntry.Document.SemanticUnits()
		key := syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "revision"}
		unit, found := units[key]
		if !found || !unit.Present {
			return nil, false, errors.New("compact v2 revision unit is missing")
		}
		unit.Value = []byte(strconv.Itoa(desired) + "\n")
		units[key] = unit
		accepted, err := projectEntry.Document.WithSemanticUnits(units)
		if err != nil {
			return nil, false, err
		}
		rendered, err := accepted.Render()
		if err != nil {
			return nil, false, err
		}
		if len(redact.Default().Text(string(rendered)).Findings) != 0 {
			return nil, false, ErrSensitiveContent
		}
		switch id {
		case "project-overview":
			parsed, parseErr := reviewv2.ParseReview(rendered)
			if parseErr != nil || parsed.Model.ProjectID != engine.options.ProjectID || parsed.Model.Revision != desired {
				return nil, false, errors.New("aligned compact review is invalid")
			}
		case "project-history":
			parsed, parseErr := reviewv2.ParseHistory(rendered)
			if parseErr != nil || parsed.ProjectID != engine.options.ProjectID || parsed.Revision != desired {
				return nil, false, errors.New("aligned compact history is invalid")
			}
		}
		base, hasBase, err := engine.bases.Load(id)
		if err != nil || !hasBase {
			return nil, false, errors.New("compact v2 revision base is unavailable")
		}
		projectCandidate := inventoryCandidate(projectInventory, id, "docs/session-review")
		vaultCandidate := inventoryCandidate(vaultInventory, id, engine.options.VaultReviewPath)
		operations = append(operations, operationForMerge(id, base.RelativePath, MergeWriteBoth, projectCandidate, vaultCandidate, syncdoc.ContentHash(rendered))...)
		if err := engine.applyAccepted(ctx, id, base.RelativePath, rendered, MergeWriteBoth, base, true, projectCandidate, vaultCandidate); err != nil {
			return nil, false, err
		}
		changed = true
	}
	return operations, changed, nil
}

func (engine *Engine) planCompactV2DryRun(projectInventory, vaultInventory syncdoc.Inventory, accepted map[string][]byte) ([]Operation, map[string][]byte, bool, error) {
	finalBodies := make(map[string][]byte, 2)
	revisions := make(map[string]int, 2)
	desiredRevision := 0
	for _, id := range []string{"project-overview", "project-history"} {
		entry, found := projectInventory.ByID[id]
		if !found {
			return nil, nil, false, errors.New("compact v2 dry-run peer is missing")
		}
		body := bytes.Clone(entry.Content)
		if planned, ok := accepted[id]; ok {
			body = bytes.Clone(planned)
		}
		document, err := syncdoc.Parse(path.Base(entry.RelativePath), body)
		if err != nil {
			return nil, nil, false, err
		}
		revision, err := compactDocumentRevision(document)
		if err != nil {
			return nil, nil, false, err
		}
		finalBodies[id] = body
		revisions[id] = revision
		if revision > desiredRevision {
			desiredRevision = revision
		}
	}
	operations := []Operation{}
	aligned := false
	for _, id := range []string{"project-overview", "project-history"} {
		if revisions[id] == desiredRevision {
			continue
		}
		entry := projectInventory.ByID[id]
		document, err := syncdoc.Parse(path.Base(entry.RelativePath), finalBodies[id])
		if err != nil {
			return nil, nil, false, err
		}
		units := document.SemanticUnits()
		key := syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "revision"}
		unit, found := units[key]
		if !found || !unit.Present {
			return nil, nil, false, errors.New("compact v2 dry-run revision is missing")
		}
		unit.Value = []byte(strconv.Itoa(desiredRevision) + "\n")
		units[key] = unit
		alignedDocument, err := document.WithSemanticUnits(units)
		if err != nil {
			return nil, nil, false, err
		}
		body, err := alignedDocument.Render()
		if err != nil {
			return nil, nil, false, err
		}
		relative := strings.TrimPrefix(entry.RelativePath, "docs/session-review/")
		projectCandidate := inventoryCandidate(projectInventory, id, "docs/session-review")
		vaultCandidate := inventoryCandidate(vaultInventory, id, engine.options.VaultReviewPath)
		if planned, ok := accepted[id]; ok {
			projectCandidate, err = candidateFromExactBody(relative, planned)
			if err != nil {
				return nil, nil, false, err
			}
			vaultCandidate = projectCandidate
		}
		operations = append(operations, operationForMerge(id, relative, MergeWriteBoth, projectCandidate, vaultCandidate, syncdoc.ContentHash(body))...)
		finalBodies[id] = body
		aligned = true
	}
	return operations, finalBodies, aligned, nil
}

func candidateFromExactBody(relative string, body []byte) (Candidate, error) {
	document, err := syncdoc.Parse(relative, body)
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{
		Present: true, RelativePath: relative, Document: document, Hash: documentHash(document),
		Source: bytes.Clone(body), SourceHash: syncdoc.ContentHash(body),
	}, nil
}

func compactDocumentRevision(document syncdoc.Document) (int, error) {
	unit, found := document.SemanticUnits()[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "revision"}]
	if !found || !unit.Present {
		return 0, errors.New("compact v2 revision is missing")
	}
	revision, err := strconv.Atoi(strings.TrimSpace(string(unit.Value)))
	if err != nil || revision < 0 {
		return 0, errors.New("compact v2 revision is invalid")
	}
	return revision, nil
}

func migrationReportFromReview(report reviewv2.MigrationReport, dryRun bool) MigrationReport {
	return MigrationReport{
		Required: report.Required, DryRun: dryRun,
		Creates: append([]string(nil), report.Creates...), Archives: append([]string(nil), report.Archives...),
	}
}

func compactV2Inventory(inventory syncdoc.Inventory, rootRelative string) bool {
	if len(inventory.Issues) != 0 || len(inventory.ByID) != 2 {
		return false
	}
	for id, expected := range map[string]struct{ entityType, relative string }{
		"project-overview": {entityType: "project_review", relative: path.Join(rootRelative, path.Base(reviewv2.ReviewRelativePath))},
		"project-history":  {entityType: "project_history", relative: path.Join(rootRelative, path.Base(reviewv2.HistoryRelativePath))},
	} {
		entry, found := inventory.ByID[id]
		if !found || entry.Identity.EntityType != expected.entityType || entry.RelativePath != expected.relative {
			return false
		}
	}
	return true
}

func (engine *Engine) trustedAppliedProjectResult(base BaseRecord, hasBase bool, projectCandidate, vaultCandidate Candidate) (*MergeResult, error) {
	projectHash := candidateSourceHash(projectCandidate)
	vaultHash := candidateSourceHash(vaultCandidate)
	if !hasBase || !projectCandidate.Present || !vaultCandidate.Present ||
		projectCandidate.RelativePath != base.RelativePath || vaultCandidate.RelativePath != base.RelativePath ||
		vaultHash != base.ContentHash || projectHash == base.ContentHash {
		return nil, nil
	}
	if err := validateCandidateIntegrity(projectCandidate); err != nil || !validDocumentShape(projectCandidate.Document) {
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
	trusted, err := engine.trustAppliedTransition(path.Join("docs/session-review", projectCandidate.RelativePath), true, base.ContentHash, projectHash)
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
		MachineState:      report.Machine.State,
		PendingOperations: append(append([]Operation{}, report.Operations...), report.Machine.Operations...),
		HiddenConflictIDs: append([]string{}, report.Conflicts...),
	}
	status.Migration = "current"
	if report.Migration.Required {
		status.Migration = "required"
	}
	if body, found, readErr := engine.project.ReadRegular(reviewv2.MachineLedgerRelativePath, int64(reviewv2.MaxMachineLedgerBytes)); readErr == nil && found {
		if machine, parseErr := reviewv2.ParseMachineLedger(body); parseErr == nil && machine.ProjectID == engine.options.ProjectID {
			status.LastSuccessfulSync = machine.LastSuccessfulSync
		}
	}
	status.Conflicted = len(report.Conflicts)
	status.Malformed = len(report.Issues)
	status.Blocked = len(report.Errors)
	status.Queued = report.QueueDepth
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

func (engine *Engine) RepairMachineLedger(ctx context.Context) (report MachineReport, retErr error) {
	report = MachineReport{State: MachineBlocked, Operations: []Operation{}}
	if ctx == nil {
		return report, errors.New("sync context is required")
	}
	if err := engine.verifyRootBindings(); err != nil {
		return report, err
	}
	defer func() { retErr = errors.Join(retErr, engine.verifyRootBindings()) }()
	lock, err := project.AcquireProjectLock(engine.data.Root, "locks/sync.lock", 10*time.Second)
	if err != nil {
		return report, errors.New("sync project is locked or unsafe")
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if err := reviewv2.RecoverMigration(engine.options.ProjectRoot, engine.project.Info(), engine.options.DataRoot, engine.data.Info()); err != nil {
		return report, errors.New("review migration recovery failed")
	}
	transactions, err := engine.transactions.List()
	if err != nil {
		return report, errors.New("sync transaction state is invalid")
	}
	if len(transactions) != 0 {
		if err := engine.recoverTransactions(ctx, transactions); err != nil {
			return report, errors.New("sync transaction recovery failed")
		}
	}
	version, err := reviewv2.DetectVersionExpected(engine.options.ProjectRoot, engine.project.Info())
	if err != nil || version != reviewv2.VersionV2 {
		return report, errors.New("machine ledger repair requires review v2")
	}
	snapshot, _, err := engine.loadMachineLedgerSnapshot(true)
	if err != nil {
		return report, errors.New("project machine ledger is invalid")
	}
	if !snapshot.vaultFound || bytes.Equal(snapshot.projectBody, snapshot.vaultBody) {
		return report, errors.New("machine ledger repair requires a modified vault copy")
	}
	return engine.publishMachineLedger(ctx, snapshot, false, true)
}

func (engine *Engine) Resolve(ctx context.Context, resolution Resolution) (report Report, retErr error) {
	report = Report{
		ProjectID: engine.options.ProjectID, Operations: []Operation{}, Conflicts: []string{}, Errors: []EntityError{},
		Derived:   DerivedReport{State: DerivedDeferred, Operations: []Operation{}},
		Migration: MigrationReport{Creates: []string{}, Archives: []string{}},
		Machine:   MachineReport{State: MachinePending, Operations: []Operation{}},
	}
	if ctx == nil || !strings.HasPrefix(resolution.ConflictID, "conflict-") || !stableBaseID.MatchString(resolution.ConflictID) || !validResolutionAction(resolution.Action) {
		return report, ErrInvalidResolution
	}
	if resolution.Action == ManualMerge {
		if strings.TrimSpace(resolution.ManualFile) == "" {
			return report, ErrInvalidResolution
		}
	} else if resolution.ManualFile != "" {
		return report, ErrInvalidResolution
	}
	if err := engine.verifyRootBindings(); err != nil {
		return report, err
	}
	defer func() { retErr = errors.Join(retErr, engine.verifyRootBindings()) }()
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
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if err := reviewv2.RecoverMigration(engine.options.ProjectRoot, engine.project.Info(), engine.options.DataRoot, engine.data.Info()); err != nil {
		return report, fmt.Errorf("review migration recovery failed: %w", err)
	}
	version, err := reviewv2.DetectVersionExpected(engine.options.ProjectRoot, engine.project.Info())
	if err != nil || version != reviewv2.VersionV2 {
		return report, errors.New("conflict resolution requires review v2")
	}
	transactions, err := engine.transactions.List()
	if err != nil {
		return report, errors.New("sync transaction state is invalid")
	}
	if len(transactions) != 0 {
		if err := engine.recoverTransactions(ctx, transactions); err != nil {
			return report, fmt.Errorf("sync transaction recovery failed: %w", err)
		}
	}
	conflictRecord, err := engine.loadMirroredConflictRecord(resolution.ConflictID)
	if err != nil || conflictRecord.ProjectID != engine.options.ProjectID {
		return report, ErrStaleConflict
	}
	machine, _, err := engine.planMachineLedger()
	if err != nil {
		return report, errors.New("machine ledger blocks conflict resolution")
	}
	entityID := conflictRecord.EntityID
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
	if err != nil || conflictRecord.BaseHash != syncdoc.ContentHash(baseRecord.Content) || conflictPath(conflictRecord.BasePath, conflictRecord.RelativePath) != baseRecord.RelativePath {
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
	var manual *syncdoc.Document
	if resolution.Action == ManualMerge {
		manualDocument, readErr := readManualResolution(resolution.ManualFile, baseRecord.RelativePath)
		if readErr != nil {
			return report, ErrInvalidResolution
		}
		manual = &manualDocument
	}
	selected, err := SelectResolution(conflictRecord, resolution, projectCandidate, vaultCandidate, manual)
	if err != nil {
		return report, err
	}
	otherConflicts, err := engine.liveConflictIDsExcept(projectInventory, vaultInventory, entityID)
	if err != nil {
		return report, ErrStaleConflict
	}
	rendered, err := selected.Render()
	if err != nil {
		return report, err
	}
	afterHash := syncdoc.ContentHash(rendered)
	resolvedConflict, openConflict, resolutionTxn, err := engine.planResolvedConflict(conflictRecord, resolution.Action, afterHash)
	if err != nil {
		return report, errors.New("hidden conflict resolution planning failed")
	}
	if err := engine.applyAccepted(ctx, entityID, baseRecord.RelativePath, rendered, MergeWriteBoth, baseRecord, true, projectCandidate, vaultCandidate); err != nil {
		return report, err
	}
	report.Operations = operationForMerge(entityID, baseRecord.RelativePath, MergeWriteBoth, projectCandidate, vaultCandidate, afterHash)
	if err := engine.resumeConflictResolution(ctx, conflictRecord.ID, resolvedConflict, openConflict, resolutionTxn); err != nil {
		return report, errors.New("hidden conflict resolution publication failed")
	}
	if len(otherConflicts) != 0 {
		report.Conflicts = otherConflicts
		report.Machine = MachineReport{State: MachinePending, Operations: []Operation{}}
		if err := lock.Release(); err != nil {
			lockHeld = false
			return report, err
		}
		lockHeld = false
		return report, nil
	}
	alignment, _, err := engine.alignCompactV2Revisions(ctx)
	if err != nil {
		return report, err
	}
	report.Operations = append(report.Operations, alignment...)
	report.Machine, err = engine.publishMachineLedger(ctx, machine, true, false)
	if err != nil {
		return report, errors.New("machine ledger publication failed")
	}
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

func (engine *Engine) liveConflictIDsExcept(projectInventory, vaultInventory syncdoc.Inventory, excludedEntityID string) ([]string, error) {
	bases, err := engine.bases.List()
	if err != nil {
		return nil, err
	}
	occupied := occupiedEntityPaths(projectInventory, vaultInventory, engine.options)
	result := []string{}
	for _, baseRecord := range bases {
		if baseRecord.EntityID == excludedEntityID {
			continue
		}
		base, err := syncdoc.Parse(baseRecord.RelativePath, baseRecord.Content)
		if err != nil {
			return nil, err
		}
		projectCandidate := inventoryCandidate(projectInventory, baseRecord.EntityID, "docs/session-review")
		vaultCandidate := inventoryCandidate(vaultInventory, baseRecord.EntityID, engine.options.VaultReviewPath)
		merged := Merge(MergeInput{
			EntityID: baseRecord.EntityID, Base: &base, Project: projectCandidate, Vault: vaultCandidate,
			ProjectID: engine.options.ProjectID, BasePath: baseRecord.RelativePath, GOOS: engine.options.GOOS,
			CaseMode: engine.options.VaultCaseMode, OccupiedPathKeys: occupied,
		})
		if merged.Kind != MergeConflict {
			continue
		}
		kind := ConflictUnits
		if merged.Reason == "archive_vs_modify" {
			kind = ConflictArchiveEdit
		}
		artifact, err := BuildConflict(ConflictRecord{
			Version: 1, EntityID: baseRecord.EntityID, ProjectID: engine.options.ProjectID, Kind: kind,
			RelativePath: baseRecord.RelativePath, BasePath: baseRecord.RelativePath,
			ProjectPath: projectCandidate.RelativePath, VaultPath: vaultCandidate.RelativePath,
			Base: bytes.Clone(baseRecord.Content), Project: candidateConflictBytes(projectCandidate), Vault: candidateConflictBytes(vaultCandidate),
			CreatedAt: engine.options.Now().UTC(),
		})
		if err != nil || artifact.Record == nil {
			return nil, ErrInvalidConflict
		}
		result = append(result, artifact.Record.ID)
	}
	sort.Strings(result)
	return result, nil
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
	return Candidate{
		Present: true, RelativePath: relative, Document: entry.Document,
		Hash: documentHash(entry.Document), Source: bytes.Clone(entry.Content), SourceHash: entry.ContentHash,
	}
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
			op.BeforeHash = candidateSourceHash(projectCandidate)
		}
		result = append(result, op)
	}
	if kind == MergeWriteVault || kind == MergeWriteBoth {
		op := Operation{EntityID: id, Kind: OperationUpdateVault, Target: SideVault, RelativePath: relative, AfterHash: afterHash}
		if !vaultCandidate.Present {
			op.Kind = OperationAddVault
		} else {
			op.BeforeHash = candidateSourceHash(vaultCandidate)
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
		ExpectedProjectHash: candidateSourceHash(projectCandidate), ExpectedVaultHash: candidateSourceHash(vaultCandidate),
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
				if target.candidate.Source != nil {
					expectedContent = bytes.Clone(target.candidate.Source)
				} else {
					var renderErr error
					expectedContent, renderErr = target.candidate.Document.Render()
					if renderErr != nil {
						return errors.New("sync target preimage cannot be rendered")
					}
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
	version, err := reviewv2.DetectVersionExpected(engine.options.ProjectRoot, engine.project.Info())
	if err != nil || version == reviewv2.VersionMixed || version == reviewv2.VersionEmpty {
		return errors.New("sync transaction recovery requires a stable review version")
	}
	if version == reviewv2.VersionV2 {
		if err := engine.validateV2RecoveryTransactions(transactions); err != nil {
			return err
		}
	}
	remaining := make([]Transaction, 0, len(transactions))
	resolutions := make([]Transaction, 0, 1)
	for _, transaction := range transactions {
		if transaction.Kind == TxnMachinePublish || transaction.Kind == TxnMachineRepair {
			if err := engine.recoverMachineLedger(ctx, transaction); err != nil {
				return err
			}
			continue
		}
		if transaction.Kind == TxnConflictRecord {
			if err := engine.recoverConflictRecord(ctx, transaction); err != nil {
				return err
			}
			continue
		}
		if transaction.Kind == TxnConflictResolve {
			resolutions = append(resolutions, transaction)
			continue
		}
		remaining = append(remaining, transaction)
	}
	if len(remaining) == 0 {
		for _, transaction := range resolutions {
			if err := engine.recoverConflictResolution(ctx, transaction); err != nil {
				return err
			}
		}
		return nil
	}
	transactions = remaining
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
			return errors.New("legacy derived navigation transaction is unsupported")
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
	for _, transaction := range resolutions {
		if err := engine.recoverConflictResolution(ctx, transaction); err != nil {
			return err
		}
	}
	return nil
}

func (engine *Engine) validateV2RecoveryTransactions(transactions []Transaction) error {
	projectInventory := syncdoc.Scan(engine.project, "docs/session-review", engine.options.GOOS, platform.CaseSensitive)
	vaultInventory, _, err := engine.scanVault()
	if err != nil || len(projectInventory.Issues) != 0 || len(vaultInventory.Issues) != 0 {
		return errors.New("cannot authenticate transaction against compact review v2")
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
		switch transaction.Kind {
		case TxnMachinePublish, TxnMachineRepair, TxnConflictRecord, TxnConflictResolve:
			continue
		case TxnEntitySync:
		default:
			return errors.New("legacy sync transaction cannot be recovered into review v2")
		}
		var body []byte
		relative := ""
		if base, found := baseByID[transaction.EntityID]; found && base.ContentHash == transaction.DesiredHash {
			body, relative = base.Content, base.RelativePath
		} else if entry, found := projectInventory.ByID[transaction.EntityID]; found && entry.ContentHash == transaction.DesiredHash {
			body, relative = entry.Content, strings.TrimPrefix(entry.RelativePath, "docs/session-review/")
		} else if entry, found := vaultInventory.ByID[transaction.EntityID]; found && entry.ContentHash == transaction.DesiredHash {
			body, relative = entry.Content, strings.TrimPrefix(entry.RelativePath, strings.TrimSuffix(engine.options.VaultReviewPath, "/")+"/")
		}
		if body == nil {
			if transaction.Stage == TxnPlanned {
				continue
			}
			return errors.New("interrupted desired content is unavailable")
		}
		if !engine.validCompactV2RecoveryBody(transaction.EntityID, relative, body) {
			return errors.New("legacy sync transaction cannot be recovered into review v2")
		}
	}
	return nil
}

func (engine *Engine) validCompactV2RecoveryBody(entityID, relative string, body []byte) bool {
	switch entityID {
	case "project-overview":
		document, err := reviewv2.ParseReview(body)
		return err == nil && relative == strings.TrimPrefix(reviewv2.ReviewRelativePath, "docs/session-review/") && document.Model.ProjectID == engine.options.ProjectID
	case "project-history":
		document, err := reviewv2.ParseHistory(body)
		return err == nil && relative == strings.TrimPrefix(reviewv2.HistoryRelativePath, "docs/session-review/") && document.ProjectID == engine.options.ProjectID
	default:
		return false
	}
}

func candidateEntryHash(entry syncdoc.Entry, found bool) string {
	if !found {
		return ""
	}
	return entry.ContentHash
}

func candidateSourceHash(candidate Candidate) string {
	if candidate.SourceHash != "" {
		return candidate.SourceHash
	}
	return candidate.Hash
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
	return fmt.Sprintf("in_sync=%d conflicted=%d malformed=%d queued=%d blocked=%d derived=%s files=%d migration=%s machine=%s pending_operations=%d hidden_conflicts=%d",
		status.InSync, status.Conflicted, status.Malformed, status.Queued, status.Blocked,
		status.DerivedState, status.DerivedFiles, status.Migration, status.MachineState,
		len(status.PendingOperations), len(status.HiddenConflictIDs))
}
