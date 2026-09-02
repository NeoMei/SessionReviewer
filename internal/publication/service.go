package publication

import (
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
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
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
}

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

// Publish executes the complete durable cross-root publication workflow.
func Publish(ctx context.Context, opts Options) (Result, error) {
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

	recovered := false
	recoveryHandler := RecoveryHandlerFunc(func(ctx context.Context, intent Intent, j *Journal) error {
		recovered = true
		return rollbackIntent(ctx, intent, j, projectDir, vaultDir)
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

	prepared, manifest, err := store.LoadPrepared()
	if err != nil {
		return Result{}, fmt.Errorf("load prepared generation: %w", err)
	}
	if prepared.GenerationID != opts.PreparedGeneration {
		return Result{}, fmt.Errorf("prepared generation ID %q does not match requested %q", prepared.GenerationID, opts.PreparedGeneration)
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
		parentDir := filepath.ToSlash(filepath.Dir(file.Relative))
		if parentDir != "." && parentDir != "" {
			if err := projectDir.EnsureDirectory(parentDir, 0o755); err != nil {
				_ = rollbackIntent(ctx, intent, j, projectDir, vaultDir)
				return Result{}, fmt.Errorf("ensure project directory %q: %w", parentDir, err)
			}
		}
		if err := atomicfile.WriteRoot(projectDir.Root, file.Relative, file.Desired, file.Mode); err != nil {
			_ = rollbackIntent(ctx, intent, j, projectDir, vaultDir)
			return Result{}, fmt.Errorf("write project file %q: %w", file.Relative, err)
		}
	}
	if err := j.Advance(StagePrepared, StageProjectWritten); err != nil {
		_ = rollbackIntent(ctx, intent, j, projectDir, vaultDir)
		return Result{}, err
	}

	// Ensure sync scaffold directories and lock
	syncDataDir := filepath.Join(opts.DataRoot, "projects", opts.ProjectID)
	for _, name := range []string{"merge-bases", "queue", "transactions", "locks"} {
		_ = os.MkdirAll(filepath.Join(syncDataDir, name), 0o700)
	}
	syncLockPath := filepath.Join(syncDataDir, "locks", "sync.lock")
	if _, err := os.Stat(syncLockPath); errors.Is(err, os.ErrNotExist) {
		_ = os.WriteFile(syncLockPath, nil, 0o600)
	}

	trustTransition := func(relative string, preimageExists bool, preimageHash, targetHash string) (bool, error) {
		for _, dest := range intent.Destinations {
			if dest.Side == "project" && dest.Relative == relative && strings.EqualFold(dest.DesiredSHA256, targetHash) {
				if (!dest.PreimageExists && !preimageExists) || (dest.PreimageExists && preimageExists && strings.EqualFold(dest.PreimageSHA256, preimageHash)) {
					return true, nil
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
		TrustAppliedTransition: trustTransition,
	}
	rep, err := syncproject.Run(ctx, syncOpts)
	if err != nil {
		_ = rollbackIntent(ctx, intent, j, projectDir, vaultDir)
		return Result{}, fmt.Errorf("sync to vault: %w", err)
	}
	for _, op := range rep.Operations {
		fmt.Printf("DEBUG OP: entity=%s kind=%s rel=%s\n", op.EntityID, op.Kind, op.RelativePath)
	}
	if err := j.Advance(StageProjectWritten, StageVaultSynced); err != nil {
		_ = rollbackIntent(ctx, intent, j, projectDir, vaultDir)
		return Result{}, err
	}

	// Verify all 3 Project files and 3 Vault files match schema 3 and desired hashes
	projFiles, vaultFiles, err := verifyPublishedFiles(opts.Plan, opts.Mapping, projectDir, vaultDir)
	if err != nil {
		_ = rollbackIntent(ctx, intent, j, projectDir, vaultDir)
		return Result{}, fmt.Errorf("verify published files: %w", err)
	}

	if err := j.Advance(StageVaultSynced, StageVerified); err != nil {
		_ = rollbackIntent(ctx, intent, j, projectDir, vaultDir)
		return Result{}, err
	}

	// Extract hashes for proof
	var reviewSHA, historySHA, ledgerSHA string
	for _, f := range projFiles {
		switch f.Relative {
		case reviewv2.ReviewRelativePath:
			reviewSHA = f.SHA256
		case reviewv2.HistoryRelativePath:
			historySHA = f.SHA256
		case reviewv2.MachineLedgerRelativePath:
			ledgerSHA = f.SHA256
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
	if err := store.CommitPublished(opts.PreparedGeneration, proof); err != nil {
		_ = rollbackIntent(ctx, intent, j, projectDir, vaultDir)
		return Result{}, fmt.Errorf("commit published generation: %w", err)
	}
	if err := j.Advance(StageVerified, StageCommitted); err != nil {
		return Result{}, err
	}

	return Result{GenerationID: opts.PreparedGeneration, ProjectFiles: projFiles, VaultFiles: vaultFiles, Recovered: recovered}, nil
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

func rollbackIntent(ctx context.Context, intent Intent, j *Journal, projectDir, vaultDir *pathguard.Directory) error {
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
				_ = dir.EnsureDirectory(pDir, 0o755)
			}
			_ = atomicfile.WriteRoot(dir.Root, dest.Relative, preimage, 0o600)
			continue
		}
		curSHA := sha256Hex(body)
		if curSHA == dest.DesiredSHA256 {
			if dest.PreimageExists {
				preimage, err := j.LoadPreimage(dest.PreimageSHA256)
				if err != nil {
					return err
				}
				_ = atomicfile.WriteRoot(dir.Root, dest.Relative, preimage, 0o600)
			} else {
				_ = atomicfile.RemoveRoot(dir.Root, dest.Relative)
			}
		} else if curSHA == dest.PreimageSHA256 {
			// already preimage
		} else {
			return &PublicationConflictError{Side: dest.Side, Relative: dest.Relative, Expected: dest.DesiredSHA256, Actual: curSHA}
		}
	}
	if err := j.Advance(intent.Stage, StageRollbackRequired); err != nil && !errors.Is(err, ErrStageMismatch) {
		return err
	}
	return j.Advance(StageRollbackRequired, StageCommitted)
}

func vaultRelativePath(vaultReviewPath, projectRelative string) string {
	switch projectRelative {
	case reviewv2.ReviewRelativePath:
		return path.Join(vaultReviewPath, path.Base(reviewv2.ReviewRelativePath))
	case reviewv2.HistoryRelativePath:
		return path.Join(vaultReviewPath, path.Base(reviewv2.HistoryRelativePath))
	case reviewv2.MachineLedgerRelativePath:
		return path.Join(vaultReviewPath, ".session-reviewer/ledger.json")
	default:
		return path.Join(vaultReviewPath, projectRelative)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
