package publication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

// Options configures a full publication run across Project, Vault, and Store.
type Options struct {
	ProjectID          string
	PreparedGeneration string
	Plan               presentation.RenderPlan
	Mapping            config.ProjectMapping
	DataRoot           string
	Now                func() time.Time

	checkpoint             func(publishCheckpoint, string, string) error
	publicationLockTimeout time.Duration
}

type publishCheckpoint string

const (
	checkpointAfterDestination    publishCheckpoint = "after_destination"
	checkpointBeforePointerCommit publishCheckpoint = "before_pointer_commit"
	checkpointAfterPointerCommit  publishCheckpoint = "after_pointer_commit"
)

// VerifiedFile captures one verified file on disk after publication.
type VerifiedFile struct {
	Side     string `json:"side"`
	Relative string `json:"relative"`
	SHA256   string `json:"sha256"`
}

// Result contains the committed generation and verified file hashes.
type Result struct {
	GenerationID string         `json:"generation_id"`
	ProjectFiles []VerifiedFile `json:"project_files"`
	VaultFiles   []VerifiedFile `json:"vault_files"`
	Recovered    bool           `json:"recovered"`
}

// PublicationConflictError describes a preimage or post-publication hash mismatch.
type PublicationConflictError struct {
	Side     string `json:"side"`
	Relative string `json:"relative"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

func (e *PublicationConflictError) Error() string {
	return fmt.Sprintf("publication conflict on %s file %q: expected SHA256 %s, actual is %s", e.Side, e.Relative, e.Expected, e.Actual)
}

var (
	ErrPublicationConflict = errors.New("publication conflict")
)

const sessionIndexRelativePath = "docs/session-review/.session-reviewer/session-index.json"

// Publish acquires the per-project public-projection lock and executes the
// complete durable cross-root publication workflow.
func Publish(ctx context.Context, opts Options) (_ Result, retErr error) {
	if opts.ProjectID == "" || !journalIDPattern.MatchString(opts.ProjectID) {
		return Result{}, errors.New("valid project ID is required")
	}
	if !filepath.IsAbs(opts.DataRoot) || filepath.Clean(opts.DataRoot) != opts.DataRoot {
		return Result{}, errors.New("SessionReviewer data root must be an absolute clean path")
	}
	timeout := opts.publicationLockTimeout
	if timeout == 0 {
		// Zero is reserved for the nonblocking test seam. Production callers
		// that do not set it receive the normal bounded wait.
		timeout = 10 * time.Second
	}
	if opts.publicationLockTimeout < 0 {
		timeout = 0
	}
	owner, err := publicationlock.Acquire(opts.DataRoot, opts.ProjectID, timeout)
	if err != nil {
		return Result{}, err
	}
	defer func() { retErr = errors.Join(retErr, owner.Release()) }()
	return PublishLocked(ctx, opts, owner)
}

// PublishLocked publishes with an already-held ownership token. It exists so
// migration can keep the same OS lock from preview recomputation through the
// durable pointer commit without recursively acquiring it.
func PublishLocked(ctx context.Context, opts Options, owner *publicationlock.Owner) (Result, error) {
	var result Result
	err := owner.Use(opts.DataRoot, opts.ProjectID, func() error {
		var err error
		result, err = publishWithOwnership(ctx, opts)
		return err
	})
	return result, err
}

func publishWithOwnership(ctx context.Context, opts Options) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("publication context is required")
	}
	if opts.ProjectID == "" {
		return Result{}, errors.New("project ID is required")
	}
	if opts.PreparedGeneration == "" {
		return Result{}, errors.New("prepared generation ID is required")
	}
	if !filepath.IsAbs(opts.DataRoot) || filepath.Clean(opts.DataRoot) != opts.DataRoot {
		return Result{}, errors.New("SessionReviewer data root must be an absolute clean path")
	}
	if opts.Plan.ProjectID != opts.ProjectID || opts.Plan.GenerationID != opts.PreparedGeneration {
		return Result{}, errors.New("presentation plan does not match project and prepared generation")
	}
	if opts.Mapping.ID != opts.ProjectID {
		return Result{}, errors.New("project mapping ID does not match project ID")
	}
	if !filepath.IsAbs(opts.Mapping.Root) || !filepath.IsAbs(opts.Mapping.VaultRoot) {
		return Result{}, errors.New("project root and vault root must be absolute paths")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	j, err := OpenJournal(opts.DataRoot, opts.ProjectID)
	if err != nil {
		return Result{}, fmt.Errorf("open journal: %w", err)
	}
	defer j.Close()

	store, err := memorystore.Open(opts.DataRoot, opts.ProjectID)
	if err != nil {
		return Result{}, fmt.Errorf("open memorystore: %w", err)
	}
	defer store.Close()

	projectDir, err := pathguard.Open(opts.Mapping.Root)
	if err != nil {
		return Result{}, fmt.Errorf("open project root: %w", err)
	}
	defer projectDir.Close()

	vaultDir, err := pathguard.Open(opts.Mapping.VaultRoot)
	if err != nil {
		return Result{}, fmt.Errorf("open vault root: %w", err)
	}
	defer vaultDir.Close()

	prepared, manifest, err := store.LoadPrepared()
	if err != nil {
		return Result{}, fmt.Errorf("load prepared generation: %w", err)
	}
	if prepared.GenerationID != opts.PreparedGeneration {
		return Result{}, fmt.Errorf("prepared generation ID %q does not match requested %q", prepared.GenerationID, opts.PreparedGeneration)
	}
	projectionVersion, err := authenticatePublicationPlan(opts, prepared, manifest)
	if err != nil {
		return Result{}, fmt.Errorf("authenticate publication projection: %w", err)
	}
	if err := repairRetainedRollbackEvidence(j, opts, projectDir, vaultDir, now); err != nil {
		return Result{}, fmt.Errorf("repair legacy rolled-back merge-base state: %w", err)
	}

	rollback := func(ctx context.Context, intent Intent) error {
		return rollbackIntent(ctx, intent, j, projectDir, vaultDir, func() error {
			return repairRolledBackBases(intent, j, opts, projectDir, vaultDir, now)
		})
	}
	rollbackFailure := func(ctx context.Context, intent Intent, primary error) error {
		if rollbackErr := rollback(ctx, intent); rollbackErr != nil {
			return errors.Join(primary, fmt.Errorf("rollback publication: %w", rollbackErr))
		}
		return primary
	}

	recovered := false
	recoveryHandler := RecoveryHandlerFunc(func(ctx context.Context, intent Intent, j *Journal) error {
		recovered = true
		publishedID, _, publishedErr := store.LoadPublished()
		if publishedErr == nil {
			if publishedID != intent.GenerationID {
				return fmt.Errorf("published generation %q does not match active publication intent %q", publishedID, intent.GenerationID)
			}
			if intent.Stage != StageVerified {
				return errors.New("published generation has a publication journal that was not verified")
			}
			if err := verifyIntentDesired(ctx, intent, projectDir, vaultDir); err != nil {
				return fmt.Errorf("published generation destinations do not match verified intent: %w", err)
			}
			return j.Advance(StageVerified, StageCommitted)
		}
		if publishedErr != nil && !errors.Is(publishedErr, memorystore.ErrNoPublishedGeneration) {
			return fmt.Errorf("inspect published generation during recovery: %w", publishedErr)
		}
		return rollback(ctx, intent)
	})
	if err := j.Recover(ctx, recoveryHandler); err != nil {
		return Result{}, fmt.Errorf("recover active publication journal: %w", err)
	}

	// Check if already published
	pubID, _, err := store.LoadPublished()
	if err == nil && pubID == opts.PreparedGeneration {
		projFiles, vaultFiles, err := verifyPublishedFiles(opts.Plan, opts.Mapping, projectDir, vaultDir)
		if err == nil {
			return Result{GenerationID: pubID, ProjectFiles: projFiles, VaultFiles: vaultFiles, Recovered: recovered}, nil
		}
	}

	// Pre-flight check Project files against expected plan preimages
	for _, file := range opts.Plan.Files {
		body, found, err := projectDir.ReadRegularOptional(file.Relative, 64<<20)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Result{}, fmt.Errorf("inspect project file %q: %w", file.Relative, err)
		}
		if file.ExpectedExists {
			if !found {
				return Result{}, fmt.Errorf("%w: %w", ErrPublicationConflict, &PublicationConflictError{Side: "project", Relative: file.Relative, Expected: sha256Hex(file.Expected), Actual: "missing"})
			}
			actualSHA := sha256Hex(body)
			expectedSHA := sha256Hex(file.Expected)
			if actualSHA != expectedSHA {
				return Result{}, fmt.Errorf("%w: %w", ErrPublicationConflict, &PublicationConflictError{Side: "project", Relative: file.Relative, Expected: expectedSHA, Actual: actualSHA})
			}
		} else if found {
			return Result{}, fmt.Errorf("%w: %w", ErrPublicationConflict, &PublicationConflictError{Side: "project", Relative: file.Relative, Expected: "missing", Actual: sha256Hex(body)})
		}
	}

	// Construct Intent destinations
	destinations := make([]Destination, 0, len(opts.Plan.Files)*2)
	for _, file := range opts.Plan.Files {
		pSHA := ""
		if file.ExpectedExists {
			pSHA = sha256Hex(file.Expected)
			if err := j.PutPreimage(pSHA, file.Expected); err != nil {
				return Result{}, err
			}
		}
		destinations = append(destinations, Destination{
			Side:           "project",
			Relative:       file.Relative,
			PreimageSHA256: pSHA,
			DesiredSHA256:  sha256Hex(file.Desired),
			PreimageExists: file.ExpectedExists,
		})

		vaultRel := vaultRelativePath(opts.Mapping.VaultReviewPath, file.Relative)
		vaultBody, vaultFound, err := vaultDir.ReadRegularOptional(vaultRel, 64<<20)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Result{}, fmt.Errorf("inspect vault file %q: %w", vaultRel, err)
		}
		vSHA := ""
		if vaultFound {
			vSHA = sha256Hex(vaultBody)
			if err := j.PutPreimage(vSHA, vaultBody); err != nil {
				return Result{}, err
			}
		}
		destinations = append(destinations, Destination{
			Side:           "vault",
			Relative:       vaultRel,
			PreimageSHA256: vSHA,
			DesiredSHA256:  sha256Hex(file.Desired),
			PreimageExists: vaultFound,
		})
	}

	sort.Slice(destinations, func(i, k int) bool {
		if destinations[i].Side != destinations[k].Side {
			return destinations[i].Side < destinations[k].Side
		}
		return destinations[i].Relative < destinations[k].Relative
	})

	intent := Intent{
		Version:           1,
		ProjectID:         opts.ProjectID,
		GenerationID:      opts.PreparedGeneration,
		ManifestDigest:    prepared.ManifestDigest,
		ProjectViewDigest: prepared.ProjectViewDigest,
		Stage:             StagePrepared,
		CreatedAt:         now().UTC(),
		Destinations:      destinations,
	}
	if err := j.Create(intent); err != nil {
		return Result{}, fmt.Errorf("create journal intent: %w", err)
	}

	// Write Project files
	for _, file := range opts.Plan.Files {
		if err := verifyDestinationPreimage(intent.Destinations, projectDir, "project", file.Relative); err != nil {
			return Result{}, rollbackFailure(ctx, intent, err)
		}
		parentDir := filepath.ToSlash(filepath.Dir(file.Relative))
		if parentDir != "." && parentDir != "" {
			if err := projectDir.EnsureDirectory(parentDir, 0o755); err != nil {
				return Result{}, rollbackFailure(ctx, intent, fmt.Errorf("ensure project directory %q: %w", parentDir, err))
			}
		}
		if err := atomicfile.WriteRoot(projectDir.Root, file.Relative, file.Desired, file.Mode); err != nil {
			return Result{}, rollbackFailure(ctx, intent, fmt.Errorf("write project file %q: %w", file.Relative, err))
		}
		if err := runPublishCheckpoint(opts, checkpointAfterDestination, "project", file.Relative); err != nil {
			return Result{}, rollbackFailure(ctx, intent, err)
		}
	}
	if err := j.Advance(StagePrepared, StageProjectWritten); err != nil {
		return Result{}, rollbackFailure(ctx, intent, err)
	}

	if projectionVersion == 3 {
		if err := publishLegacyV3(ctx, opts, intent, rollbackFailure, now); err != nil {
			return Result{}, err
		}
	} else {
		for _, file := range opts.Plan.Files {
			vaultRelative := vaultRelativePath(opts.Mapping.VaultReviewPath, file.Relative)
			if err := verifyDestinationPreimage(intent.Destinations, vaultDir, "vault", vaultRelative); err != nil {
				return Result{}, rollbackFailure(ctx, intent, err)
			}
			parentDir := filepath.ToSlash(filepath.Dir(vaultRelative))
			if parentDir != "." && parentDir != "" {
				if err := vaultDir.EnsureDirectory(parentDir, 0o755); err != nil {
					return Result{}, rollbackFailure(ctx, intent, err)
				}
			}
			if err := atomicfile.WriteRoot(vaultDir.Root, vaultRelative, file.Desired, file.Mode); err != nil {
				return Result{}, rollbackFailure(ctx, intent, fmt.Errorf("write Vault file %q: %w", vaultRelative, err))
			}
			if err := runPublishCheckpoint(opts, checkpointAfterDestination, "vault", vaultRelative); err != nil {
				return Result{}, rollbackFailure(ctx, intent, err)
			}
		}
	}
	if err := j.Advance(StageProjectWritten, StageVaultSynced); err != nil {
		return Result{}, rollbackFailure(ctx, intent, err)
	}

	// Verify every Project and Vault target before the pointer is committed.
	projFiles, vaultFiles, err := verifyPublishedFiles(opts.Plan, opts.Mapping, projectDir, vaultDir)
	if err != nil {
		return Result{}, rollbackFailure(ctx, intent, fmt.Errorf("verify published files: %w", err))
	}

	if err := j.Advance(StageVaultSynced, StageVerified); err != nil {
		return Result{}, rollbackFailure(ctx, intent, err)
	}

	// Extract hashes for proof
	var reviewSHA, historySHA, ledgerSHA, sessionIndexSHA string
	for _, f := range projFiles {
		switch f.Relative {
		case reviewv2.ReviewRelativePath:
			reviewSHA = f.SHA256
		case reviewv2.HistoryRelativePath:
			historySHA = f.SHA256
		case reviewv2.MachineLedgerRelativePath:
			ledgerSHA = f.SHA256
		case sessionIndexRelativePath:
			sessionIndexSHA = f.SHA256
		}
	}

	proof := PublicationProof{
		ProjectID:         opts.ProjectID,
		GenerationID:      opts.PreparedGeneration,
		ManifestDigest:    prepared.ManifestDigest,
		ProjectViewDigest: manifest.ProjectViewDigest,
		ReviewSHA256:      reviewSHA,
		HistorySHA256:     historySHA,
		LedgerSHA256:      ledgerSHA,
		JournalVerified:   true,
	}
	if projectionVersion == 4 {
		proof.Version = 4
		proof.SessionIndexSHA256 = sessionIndexSHA
	}
	if err := runPublishCheckpoint(opts, checkpointBeforePointerCommit, "", ""); err != nil {
		return Result{}, rollbackFailure(ctx, intent, err)
	}
	if err := store.CommitPublished(opts.PreparedGeneration, proof); err != nil {
		return Result{}, rollbackFailure(ctx, intent, fmt.Errorf("commit published generation: %w", err))
	}
	if err := runPublishCheckpoint(opts, checkpointAfterPointerCommit, "", ""); err != nil {
		return Result{}, err
	}
	if err := j.Advance(StageVerified, StageCommitted); err != nil {
		return Result{}, err
	}

	return Result{GenerationID: opts.PreparedGeneration, ProjectFiles: projFiles, VaultFiles: vaultFiles, Recovered: recovered}, nil
}

func publishLegacyV3(ctx context.Context, opts Options, intent Intent, rollbackFailure func(context.Context, Intent, error) error, now func() time.Time) error {
	// Ensure sync scaffold directories and lock.
	syncDataDir := filepath.Join(opts.DataRoot, "projects", opts.ProjectID)
	syncDataRoot, err := pathguard.Open(syncDataDir)
	if err != nil {
		return rollbackFailure(ctx, intent, fmt.Errorf("open project sync data root: %w", err))
	}
	for _, name := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := syncDataRoot.EnsureDirectory(name, 0o700); err != nil {
			closeErr := syncDataRoot.Close()
			return rollbackFailure(ctx, intent, errors.Join(fmt.Errorf("ensure sync directory %q: %w", name, err), closeErr))
		}
	}
	if err := syncDataRoot.Close(); err != nil {
		return rollbackFailure(ctx, intent, fmt.Errorf("close project sync data root: %w", err))
	}

	trustTransition := func(relative string, preimageExists bool, preimageHash, targetHash string) (bool, error) {
		for _, dest := range intent.Destinations {
			if dest.Side == "project" && dest.Relative == relative && strings.EqualFold(dest.DesiredSHA256, targetHash) {
				if (!dest.PreimageExists && !preimageExists) || (dest.PreimageExists && preimageExists && strings.EqualFold(dest.PreimageSHA256, preimageHash)) {
					return true, nil
				}
				// When the project Markdown was edited by a human after the
				// previous sync, the merge base still matches the Vault preimage,
				// not the captured Project preimage. The publication plan was
				// rendered from that exact human Project preimage and is protected
				// by the journal/CAS checks above, so authorize only this complete
				// three-point transition: Vault/base -> human Project -> desired.
				vaultRelative := vaultRelativePath(opts.Mapping.VaultReviewPath, relative)
				for _, vaultDest := range intent.Destinations {
					if vaultDest.Side == "vault" && vaultDest.Relative == vaultRelative && vaultDest.PreimageExists == preimageExists &&
						(!preimageExists || strings.EqualFold(vaultDest.PreimageSHA256, preimageHash)) {
						return true, nil
					}
				}
			}
		}
		return false, nil
	}

	// Sync to Vault
	syncOpts := syncproject.Options{
		ProjectID:              opts.ProjectID,
		CWD:                    opts.Mapping.Root,
		DataDir:                opts.DataRoot,
		GOOS:                   runtime.GOOS,
		Now:                    now,
		Trigger:                "cli",
		RepairMachineLedger:    true,
		AllowV3Publication:     true,
		TrustAppliedTransition: trustTransition,
	}
	preflightOpts := syncOpts
	preflightOpts.DryRun = true
	// RepairMachineLedger makes dry-run tolerate a stale Vault mirror in memory
	// so it can inspect the complete human-document plan. syncproject guarantees
	// that the actual repair remains exclusive to the real pass below.
	preflight, err := syncproject.Run(ctx, preflightOpts)
	if err != nil {
		return rollbackFailure(ctx, intent, fmt.Errorf("sync to vault preflight: %w", err))
	}
	if !syncReportReadyToApply(preflight) {
		return rollbackFailure(ctx, intent, fmt.Errorf(
			"sync to vault preflight did not converge: conflicts=%d issues=%d errors=%d error_codes=%s queue_depth=%d derived=%s migration_required=%t machine=%s",
			len(preflight.Conflicts), len(preflight.Issues), len(preflight.Errors), syncErrorSummary(preflight.Errors), preflight.QueueDepth,
			preflight.Derived.State, preflight.Migration.Required, preflight.Machine.State,
		))
	}
	rep, err := syncproject.Run(ctx, syncOpts)
	if err != nil {
		return rollbackFailure(ctx, intent, fmt.Errorf("sync to vault: %w", err))
	}
	if !syncReportConverged(rep) {
		return rollbackFailure(ctx, intent, fmt.Errorf(
			"sync to vault did not converge: conflicts=%d issues=%d errors=%d error_codes=%s queue_depth=%d derived=%s migration_required=%t machine=%s",
			len(rep.Conflicts), len(rep.Issues), len(rep.Errors), syncErrorSummary(rep.Errors), rep.QueueDepth,
			rep.Derived.State, rep.Migration.Required, rep.Machine.State,
		))
	}
	return nil
}

func syncErrorSummary(entityErrors []syncengine.EntityError) string {
	if len(entityErrors) == 0 {
		return "none"
	}
	values := make([]string, 0, len(entityErrors))
	for _, entityError := range entityErrors {
		values = append(values, entityError.EntityID+":"+entityError.Code)
	}
	return strings.Join(values, ",")
}

func runPublishCheckpoint(opts Options, stage publishCheckpoint, side, relative string) error {
	if opts.checkpoint == nil {
		return nil
	}
	return opts.checkpoint(stage, side, relative)
}

func planProjectionVersion(plan presentation.RenderPlan) (int, error) {
	required := map[string]bool{
		reviewv2.ReviewRelativePath:        false,
		reviewv2.HistoryRelativePath:       false,
		reviewv2.MachineLedgerRelativePath: false,
	}
	hasIndex := false
	seen := make(map[string]bool, len(plan.Files))
	for _, file := range plan.Files {
		if file.Relative == "" || seen[file.Relative] {
			return 0, errors.New("publication plan contains an empty or duplicate destination")
		}
		seen[file.Relative] = true
		if _, ok := required[file.Relative]; ok {
			required[file.Relative] = true
		}
		if file.Relative == sessionIndexRelativePath {
			hasIndex = true
		}
	}
	for relative, found := range required {
		if !found {
			return 0, fmt.Errorf("publication plan is missing required file %q", relative)
		}
	}
	if hasIndex {
		if len(plan.Files) != 4 {
			return 0, errors.New("v4 publication plan must contain exactly four files")
		}
		return 4, nil
	}
	if len(plan.Files) != 3 {
		return 0, errors.New("legacy v3 publication plan must contain exactly three files")
	}
	return 3, nil
}

func authenticatePublicationPlan(opts Options, prepared memorystore.Prepared, manifest memory.GenerationManifest) (int, error) {
	version, err := planProjectionVersion(opts.Plan)
	if err != nil {
		return 0, err
	}
	if manifest.ProjectID != opts.ProjectID || manifest.GenerationID != opts.PreparedGeneration ||
		prepared.GenerationID != manifest.GenerationID || prepared.ProjectViewDigest != manifest.ProjectViewDigest ||
		opts.Plan.ProjectID != manifest.ProjectID || opts.Plan.GenerationID != manifest.GenerationID ||
		opts.Plan.ProjectViewDigest != manifest.ProjectViewDigest {
		return 0, errors.New("publication plan, prepared pointer, and manifest identity do not match")
	}
	files := make(map[string][]byte, len(opts.Plan.Files))
	for _, file := range opts.Plan.Files {
		files[file.Relative] = file.Desired
	}
	var projectID, generationID, projectViewDigest string
	if version == 3 {
		accepted, err := reviewv2.LoadV3Bytes(files[reviewv2.ReviewRelativePath], files[reviewv2.HistoryRelativePath], files[reviewv2.MachineLedgerRelativePath])
		if err != nil {
			return 0, fmt.Errorf("load v3 projection: %w", err)
		}
		projectID = accepted.State.Machine.ProjectID
		generationID = accepted.State.Machine.GenerationID
		projectViewDigest = "sha256:" + accepted.State.Machine.ProjectViewDigest
	} else {
		accepted, err := reviewv4.LoadProjection(files[reviewv2.ReviewRelativePath], files[reviewv2.HistoryRelativePath], files[reviewv2.MachineLedgerRelativePath], files[sessionIndexRelativePath])
		if err != nil {
			return 0, fmt.Errorf("load v4 projection: %w", err)
		}
		projectID = accepted.Review.ProjectID
		generationID = accepted.Review.GenerationID
		projectViewDigest = accepted.Review.ProjectViewDigest
	}
	if projectID != manifest.ProjectID || generationID != manifest.GenerationID || projectViewDigest != manifest.ProjectViewDigest {
		return 0, errors.New("projection identity does not match prepared manifest")
	}
	return version, nil
}

func syncReportReadyToApply(report syncengine.Report) bool {
	return len(report.Conflicts) == 0 &&
		len(report.Issues) == 0 &&
		len(report.Errors) == 0 &&
		report.QueueDepth == 0 &&
		report.Derived.State != syncengine.DerivedFailed &&
		!report.Migration.Required &&
		report.Machine.State != syncengine.MachineBlocked
}

// repairRetainedRollbackEvidence handles both a retained backup intent and the
// current committed intent. Some affected releases completed journal rollback
// after restoring the human files but before restoring a partially advanced
// merge-base, so the committed intent itself can be the only durable proof.
func repairRetainedRollbackEvidence(j *Journal, opts Options, projectDir, vaultDir *pathguard.Directory, now func() time.Time) error {
	previous, err := j.LoadPrevious()
	if err == nil {
		if err := repairRolledBackBases(previous, j, opts, projectDir, vaultDir, now); err != nil {
			return err
		}
	} else if !errors.Is(err, ErrNoActiveIntent) {
		return fmt.Errorf("inspect backup publication intent: %w", err)
	}

	current, err := j.Load()
	if errors.Is(err, ErrNoActiveIntent) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect current publication intent: %w", err)
	}
	if current.Stage != StageCommitted {
		return nil
	}
	return repairRolledBackBases(current, j, opts, projectDir, vaultDir, now)
}

func syncReportConverged(report syncengine.Report) bool {
	return len(report.Conflicts) == 0 &&
		len(report.Issues) == 0 &&
		len(report.Errors) == 0 &&
		report.QueueDepth == 0 &&
		report.Derived.State == syncengine.DerivedCurrent &&
		!report.Migration.Required &&
		report.Machine.State == syncengine.MachineCurrent
}

func verifyPublishedFiles(plan presentation.RenderPlan, mapping config.ProjectMapping, projectDir, vaultDir *pathguard.Directory) ([]VerifiedFile, []VerifiedFile, error) {
	var projFiles []VerifiedFile
	var vaultFiles []VerifiedFile

	for _, file := range plan.Files {
		pBody, pFound, err := projectDir.ReadRegularOptional(file.Relative, 64<<20)
		if err != nil || !pFound {
			return nil, nil, fmt.Errorf("read project file %q: %w", file.Relative, err)
		}
		pSHA := sha256Hex(pBody)
		desiredSHA := sha256Hex(file.Desired)
		if pSHA != desiredSHA {
			return nil, nil, fmt.Errorf("project file %q SHA256 %s does not match desired %s", file.Relative, pSHA, desiredSHA)
		}
		projFiles = append(projFiles, VerifiedFile{Side: "project", Relative: file.Relative, SHA256: pSHA})

		vaultRel := vaultRelativePath(mapping.VaultReviewPath, file.Relative)
		vBody, vFound, err := vaultDir.ReadRegularOptional(vaultRel, 64<<20)
		if err != nil || !vFound {
			return nil, nil, fmt.Errorf("read vault file %q: %w", vaultRel, err)
		}
		vSHA := sha256Hex(vBody)
		if vSHA != desiredSHA {
			return nil, nil, fmt.Errorf("vault file %q SHA256 %s does not match desired %s", vaultRel, vSHA, desiredSHA)
		}
		vaultFiles = append(vaultFiles, VerifiedFile{Side: "vault", Relative: vaultRel, SHA256: vSHA})
	}
	return projFiles, vaultFiles, nil
}

func rollbackIntent(ctx context.Context, intent Intent, j *Journal, projectDir, vaultDir *pathguard.Directory, afterRestore func() error) error {
	for _, dest := range intent.Destinations {
		var dir *pathguard.Directory
		if dest.Side == "project" {
			dir = projectDir
		} else if dest.Side == "vault" {
			dir = vaultDir
		} else {
			continue
		}
		if dir == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		body, found, err := dir.ReadRegularOptional(dest.Relative, 64<<20)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if !found {
			if !dest.PreimageExists {
				continue
			}
			preimage, err := j.LoadPreimage(dest.PreimageSHA256)
			if err != nil {
				return err
			}
			pDir := filepath.ToSlash(filepath.Dir(dest.Relative))
			if pDir != "." && pDir != "" {
				if err := dir.EnsureDirectory(pDir, 0o755); err != nil {
					return fmt.Errorf("restore %s directory %q: %w", dest.Side, pDir, err)
				}
			}
			if err := atomicfile.WriteRoot(dir.Root, dest.Relative, preimage, 0o600); err != nil {
				return fmt.Errorf("restore missing %s file %q: %w", dest.Side, dest.Relative, err)
			}
			continue
		}
		curSHA := sha256Hex(body)
		if curSHA == dest.DesiredSHA256 {
			if dest.PreimageExists {
				preimage, err := j.LoadPreimage(dest.PreimageSHA256)
				if err != nil {
					return err
				}
				if err := atomicfile.WriteRoot(dir.Root, dest.Relative, preimage, 0o600); err != nil {
					return fmt.Errorf("restore %s file %q: %w", dest.Side, dest.Relative, err)
				}
			} else {
				if err := atomicfile.RemoveRoot(dir.Root, dest.Relative); err != nil {
					return fmt.Errorf("remove new %s file %q: %w", dest.Side, dest.Relative, err)
				}
			}
		} else if curSHA == dest.PreimageSHA256 {
			// already preimage
		} else {
			return &PublicationConflictError{Side: dest.Side, Relative: dest.Relative, Expected: dest.DesiredSHA256, Actual: curSHA}
		}
	}
	if afterRestore != nil {
		if err := afterRestore(); err != nil {
			return err
		}
	}
	current, err := j.Load()
	if err != nil {
		return err
	}
	if current.GenerationID != intent.GenerationID {
		return errors.New("publication rollback journal generation changed")
	}
	if current.Stage == StageCommitted {
		return nil
	}
	if current.Stage != StageRollbackRequired {
		if err := j.Advance(current.Stage, StageRollbackRequired); err != nil {
			return err
		}
	}
	return j.Advance(StageRollbackRequired, StageCommitted)
}

// repairRolledBackBases compensates for releases that could commit one entity's
// merge-base before a later entity blocked the same publication. It deliberately
// has no general "trust identical files" escape hatch: the current Project and
// Vault bytes must both equal the immutable journal preimage, while the current
// Base must equal that same journal intent's desired bytes.
func repairRolledBackBases(intent Intent, j *Journal, opts Options, projectDir, vaultDir *pathguard.Directory, now func() time.Time) error {
	// Four-file v4 publication never uses the legacy sync merge-base store.
	// Its generic Intent preimages are the complete recovery authority.
	for _, destination := range intent.Destinations {
		if destination.Side == "project" && destination.Relative == sessionIndexRelativePath {
			return nil
		}
	}
	if now == nil {
		now = time.Now
	}
	destinationByKey := make(map[string]Destination, len(intent.Destinations))
	for _, destination := range intent.Destinations {
		destinationByKey[destination.Side+"\x00"+destination.Relative] = destination
	}

	type compactDocument struct {
		entityID string
		relative string
	}
	documents := []compactDocument{
		{entityID: "project-overview", relative: reviewv2.ReviewRelativePath},
		{entityID: "project-history", relative: reviewv2.HistoryRelativePath},
	}

	var baseStore *syncengine.BaseStore
	var syncData *pathguard.Directory
	defer func() {
		if syncData != nil {
			_ = syncData.Close()
		}
	}()
	for _, document := range documents {
		projectDestination, projectFound := destinationByKey["project\x00"+document.relative]
		vaultRelative := vaultRelativePath(opts.Mapping.VaultReviewPath, document.relative)
		vaultDestination, vaultFound := destinationByKey["vault\x00"+vaultRelative]
		if !projectFound || !vaultFound || !projectDestination.PreimageExists || !vaultDestination.PreimageExists ||
			!strings.EqualFold(projectDestination.PreimageSHA256, vaultDestination.PreimageSHA256) ||
			!strings.EqualFold(projectDestination.DesiredSHA256, vaultDestination.DesiredSHA256) {
			continue
		}

		projectBody, projectExists, err := projectDir.ReadRegularOptional(document.relative, 64<<20)
		if err != nil {
			return fmt.Errorf("inspect rolled-back project file %q: %w", document.relative, err)
		}
		vaultBody, vaultExists, err := vaultDir.ReadRegularOptional(vaultRelative, 64<<20)
		if err != nil {
			return fmt.Errorf("inspect rolled-back vault file %q: %w", vaultRelative, err)
		}
		if !projectExists || !vaultExists ||
			!strings.EqualFold(sha256Hex(projectBody), projectDestination.PreimageSHA256) ||
			!strings.EqualFold(sha256Hex(vaultBody), projectDestination.PreimageSHA256) {
			continue
		}
		preimage, err := j.LoadPreimage(projectDestination.PreimageSHA256)
		if err != nil {
			return fmt.Errorf("load merge-base rollback preimage for %q: %w", document.entityID, err)
		}
		if !bytes.Equal(projectBody, preimage) || !bytes.Equal(vaultBody, preimage) {
			return fmt.Errorf("merge-base rollback preimage for %q changed", document.entityID)
		}
		parsed, err := syncdoc.Parse(path.Base(document.relative), preimage)
		if err != nil {
			return fmt.Errorf("parse merge-base rollback preimage for %q: %w", document.entityID, err)
		}
		identity, err := parsed.Identity()
		if err != nil || identity.ID != document.entityID || identity.ProjectID != intent.ProjectID {
			return fmt.Errorf("merge-base rollback preimage identity for %q is invalid", document.entityID)
		}

		if baseStore == nil {
			syncDataPath := filepath.Join(opts.DataRoot, "projects", opts.ProjectID)
			if _, statErr := os.Stat(syncDataPath); errors.Is(statErr, os.ErrNotExist) {
				return nil
			} else if statErr != nil {
				return fmt.Errorf("inspect sync state for merge-base rollback: %w", statErr)
			}
			syncData, err = pathguard.Open(syncDataPath)
			if err != nil {
				return fmt.Errorf("open sync state for merge-base rollback: %w", err)
			}
			store := syncengine.BaseStore{Root: syncData.Root}
			baseStore = &store
		}
		base, found, err := baseStore.Load(document.entityID)
		if err != nil {
			return fmt.Errorf("load merge-base for rollback %q: %w", document.entityID, err)
		}
		if !found || strings.EqualFold(base.ContentHash, projectDestination.PreimageSHA256) {
			continue
		}
		if !strings.EqualFold(base.ContentHash, projectDestination.DesiredSHA256) {
			continue
		}
		preimageHash := strings.ToLower(projectDestination.PreimageSHA256)
		next := syncengine.BaseRecord{
			Version: 1, EntityID: document.entityID, RelativePath: path.Base(document.relative),
			ContentHash: preimageHash, ProjectHash: preimageHash, VaultHash: preimageHash,
			Content: bytes.Clone(preimage), SyncedAt: now().UTC(),
		}
		if err := baseStore.Commit(base.ContentHash, next); err != nil {
			return fmt.Errorf("restore merge-base for %q: %w", document.entityID, err)
		}
	}
	return nil
}

func vaultRelativePath(vaultReviewPath, projectRelative string) string {
	if relative, ok := strings.CutPrefix(projectRelative, "docs/session-review/"); ok {
		return path.Join(vaultReviewPath, relative)
	}
	return path.Join(vaultReviewPath, projectRelative)
}

func verifyDestinationPreimage(destinations []Destination, directory *pathguard.Directory, side, relative string) error {
	for _, destination := range destinations {
		if destination.Side != side || destination.Relative != relative {
			continue
		}
		body, found, err := directory.ReadRegularOptional(relative, 64<<20)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		actual := "missing"
		if found {
			actual = sha256Hex(body)
		}
		if found != destination.PreimageExists || (found && !strings.EqualFold(actual, destination.PreimageSHA256)) {
			return fmt.Errorf("%w: %w", ErrPublicationConflict, &PublicationConflictError{Side: side, Relative: relative, Expected: destination.PreimageSHA256, Actual: actual})
		}
		return nil
	}
	return errors.New("publication destination is missing from intent")
}

func verifyIntentDesired(ctx context.Context, intent Intent, projectDir, vaultDir *pathguard.Directory) error {
	for _, destination := range intent.Destinations {
		if err := ctx.Err(); err != nil {
			return err
		}
		var directory *pathguard.Directory
		switch destination.Side {
		case "project":
			directory = projectDir
		case "vault":
			directory = vaultDir
		default:
			return errors.New("publication intent contains an unknown destination side")
		}
		body, found, err := directory.ReadRegularOptional(destination.Relative, 64<<20)
		if err != nil {
			return err
		}
		actual := "missing"
		if found {
			actual = sha256Hex(body)
		}
		if !found || !strings.EqualFold(actual, destination.DesiredSHA256) {
			return fmt.Errorf("%w: %w", ErrPublicationConflict, &PublicationConflictError{Side: destination.Side, Relative: destination.Relative, Expected: destination.DesiredSHA256, Actual: actual})
		}
	}
	return nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
