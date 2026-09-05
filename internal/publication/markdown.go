package publication

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

// PublishMarkdownEditLocked adapts the three-file human edit transaction to
// the existing publication journal while retaining its mapping, preimage, and
// owner validation. PublishLocked performs Owner.Use exactly once.
func PublishMarkdownEditLocked(ctx context.Context, opts Options, edit syncproject.MarkdownSyncPlan, owner *publicationlock.Owner) (Result, error) {
	if edit.ExpectedGenerationID == "" || edit.ExpectedIndexDigest == "" || len(edit.Index) == 0 {
		return Result{}, errors.New("Markdown edit requires an index guard")
	}
	opts.PreparedGeneration = edit.ExpectedGenerationID
	opts.Plan = edit.Plan
	opts.markdownIndex = bytes.Clone(edit.Index)
	opts.markdownIndexDigest = edit.ExpectedIndexDigest
	opts.markdownVaultExpected = edit.VaultExpected
	opts.markdownReceiptRevision = edit.ExpectedReceiptRevision
	opts.markdownBaseDigest = edit.ExpectedBaseDigest
	return PublishLocked(ctx, opts, owner)
}

// PublishMarkdownScanLocked publishes a four-file scan result while retaining
// the authenticated old Project/Vault, receipt, and Base preimages.
func PublishMarkdownScanLocked(ctx context.Context, opts Options, scan syncproject.MarkdownSyncPlan, owner *publicationlock.Owner) (Result, error) {
	if len(scan.Plan.Files) != 4 {
		return Result{}, errors.New("Markdown scan publication requires four files")
	}
	if (scan.ExpectedReceiptRevision == "") != (scan.ExpectedBaseDigest == "") {
		return Result{}, errors.New("Markdown scan receipt and Base preimages must be supplied together")
	}
	if scan.ExpectedReceiptRevision == "" {
		for _, file := range scan.Plan.Files {
			if file.ExpectedExists {
				return Result{}, errors.New("existing Markdown scan requires authenticated receipt and Base preimages")
			}
			expected, ok := scan.VaultExpected[file.Relative]
			if !ok || expected != nil {
				return Result{}, errors.New("initial Markdown scan requires explicit absent Vault preimages")
			}
		}
		if err := owner.Use(opts.DataRoot, opts.ProjectID, func() error {
			journal, err := OpenJournal(opts.DataRoot, opts.ProjectID)
			if err != nil {
				return err
			}
			defer journal.Close()
			if _, err := journal.LoadAcceptedMarkdown(); !errors.Is(err, os.ErrNotExist) {
				if err == nil {
					return errors.New("initial Markdown scan found accepted publication history")
				}
				return err
			}
			store, closeStore, err := markdownBaseStore(opts)
			if err != nil {
				return err
			}
			defer closeStore()
			_, found, err := store.Load(syncengine.MarkdownBaseEntityID)
			if err != nil || found {
				return errors.Join(errors.New("initial Markdown scan found merge Base history"), err)
			}
			return nil
		}); err != nil {
			return Result{}, err
		}
	}
	return PublishMarkdownEditLocked(ctx, opts, scan, owner)
}

// PublishMarkdownScan acquires publication ownership for an initial four-file
// Markdown scan. Nil VaultExpected entries mean the corresponding file must
// not exist on the Vault side.
func PublishMarkdownScan(ctx context.Context, opts Options, scan syncproject.MarkdownSyncPlan) (_ Result, retErr error) {
	owner, err := publicationlock.Acquire(opts.DataRoot, opts.ProjectID, 10*time.Second)
	if err != nil {
		return Result{}, err
	}
	defer func() { retErr = errors.Join(retErr, owner.Release()) }()
	return PublishMarkdownScanLocked(ctx, opts, scan, owner)
}

// RecoverMarkdownLocked resolves an unfinished human-edit intent before
// syncproject authenticates and diffs a new draft. It never treats an
// unchanged generation pointer as proof of a same-generation human revision.
func RecoverMarkdownLocked(ctx context.Context, opts Options, owner *publicationlock.Owner) error {
	return owner.Use(opts.DataRoot, opts.ProjectID, func() error {
		journal, err := OpenJournal(opts.DataRoot, opts.ProjectID)
		if err != nil {
			return err
		}
		defer journal.Close()
		intent, err := journal.Load()
		if errors.Is(err, ErrNoActiveIntent) {
			return nil
		}
		if err != nil || intent.Stage == StageCommitted {
			return err
		}
		if intent.Version != 2 || intent.Kind != KindMarkdown {
			return errors.New("non-Markdown publication recovery must be completed first")
		}
		projectDir, err := pathguard.Open(opts.Mapping.Root)
		if err != nil {
			return err
		}
		defer projectDir.Close()
		vaultDir, err := pathguard.Open(opts.Mapping.VaultRoot)
		if err != nil {
			return err
		}
		defer vaultDir.Close()
		opts.PreparedGeneration = intent.GenerationID
		opts.markdownIndexDigest = intent.IndexGuard.Digest
		rollback := func() error {
			return rollbackIntent(ctx, intent, journal, projectDir, vaultDir, func() error { return repairMarkdownBaseAfterRollback(intent, journal, opts) })
		}
		complete := false
		if intent.Stage == StageBaseCommitted {
			accepted, err := markdownReceiptCommitted(journal, intent)
			if err != nil {
				return err
			}
			if accepted {
				complete = true
			} else {
				return rollback()
			}
		}
		if !complete && intent.RequiresPointer && intent.Stage == StageVerified {
			store, err := memorystore.Open(opts.DataRoot, opts.ProjectID)
			if err != nil {
				return err
			}
			defer store.Close()
			published, _, err := store.LoadPublished()
			if err == nil && markdownPointerCrossed(intent, published) {
				complete = true
			}
		}
		if !complete {
			return rollback()
		}
		index, found, err := projectDir.ReadRegular(intent.IndexGuard.Relative, 64<<20)
		if err != nil || !found {
			return errors.Join(errors.New("recover Markdown index guard"), err)
		}
		opts.markdownIndex = index
		files, err := markdownPlanFromIntent(intent, projectDir)
		if err != nil {
			return err
		}
		opts.Plan = presentation.RenderPlan{ProjectID: intent.ProjectID, GenerationID: intent.GenerationID, ProjectViewDigest: intent.ProjectViewDigest, Files: files}
		return completeMarkdownAcceptance(intent, journal, opts, projectDir, vaultDir)
	})
}

func markdownPointerCrossed(intent Intent, published string) bool {
	return intent.RequiresPointer && intent.PointerPreimage != nil && *intent.PointerPreimage != intent.GenerationID && published == intent.GenerationID
}

func markdownPlanFromIntent(intent Intent, projectDir *pathguard.Directory) ([]presentation.FilePlan, error) {
	files := make([]presentation.FilePlan, 0, 4)
	for _, destination := range intent.Destinations {
		if destination.Side != "project" || destination.Relative == intent.IndexGuard.Relative {
			continue
		}
		body, found, err := projectDir.ReadRegular(destination.Relative, 64<<20)
		if err != nil || !found || sha256Hex(body) != destination.DesiredSHA256 {
			return nil, errors.Join(errors.New("recover Markdown desired Project bytes"), err)
		}
		files = append(files, presentation.FilePlan{Relative: destination.Relative, Desired: body, Mode: 0o600})
	}
	if len(files) != 3 && len(files) != 4 {
		return nil, errors.New("Markdown recovery destination set is incomplete")
	}
	return files, nil
}

func markdownDesiredPair(plan presentation.RenderPlan) ([]byte, []byte, error) {
	var review, history []byte
	for _, file := range plan.Files {
		switch file.Relative {
		case reviewv2.ReviewRelativePath:
			review = bytes.Clone(file.Desired)
		case reviewv2.HistoryRelativePath:
			history = bytes.Clone(file.Desired)
		}
	}
	if len(review) == 0 || len(history) == 0 {
		return nil, nil, errors.New("Markdown publication plan has no document pair")
	}
	return review, history, nil
}

func markdownProjectionIndex(plan presentation.RenderPlan) ([]byte, bool) {
	var ledger, index []byte
	for _, file := range plan.Files {
		switch file.Relative {
		case reviewv2.MachineLedgerRelativePath:
			ledger = file.Desired
		case sessionIndexRelativePath:
			index = file.Desired
		}
	}
	if len(ledger) == 0 || len(index) == 0 {
		return nil, false
	}
	parsed, err := reviewv4.DecodeLedger(ledger)
	return bytes.Clone(index), err == nil && parsed.DocumentProjection != nil
}

func markdownIndexGuard(opts Options, projectDir, vaultDir *pathguard.Directory, desired bool) (*IndexGuard, error) {
	vaultRelative := vaultRelativePath(opts.Mapping.VaultReviewPath, sessionIndexRelativePath)
	parsed, err := sessionindex.Parse(opts.markdownIndex)
	if err != nil || parsed.ProjectID != opts.ProjectID || parsed.GenerationID != opts.PreparedGeneration || parsed.Digest != opts.markdownIndexDigest {
		return nil, errors.Join(errors.New("session index guard binding mismatch"), err)
	}
	if desired {
		hash := sha256Hex(opts.markdownIndex)
		return &IndexGuard{Relative: sessionIndexRelativePath, VaultRelative: vaultRelative, ProjectSHA256: hash, VaultSHA256: hash, Digest: opts.markdownIndexDigest, GenerationID: opts.PreparedGeneration}, nil
	}
	projectBody, found, err := projectDir.ReadRegular(sessionIndexRelativePath, 64<<20)
	if err != nil || !found || !bytes.Equal(projectBody, opts.markdownIndex) {
		return nil, errors.Join(errors.New("Project session index guard changed"), err)
	}
	vaultBody, found, err := vaultDir.ReadRegular(vaultRelative, 64<<20)
	if err != nil || !found || !bytes.Equal(vaultBody, opts.markdownIndex) {
		return nil, errors.Join(errors.New("Vault session index guard changed"), err)
	}
	return &IndexGuard{Relative: sessionIndexRelativePath, VaultRelative: vaultRelative, ProjectSHA256: sha256Hex(projectBody), VaultSHA256: sha256Hex(vaultBody), Digest: opts.markdownIndexDigest, GenerationID: opts.PreparedGeneration}, nil
}

func markdownBaseStore(opts Options) (syncengine.BaseStore, func() error, error) {
	root, err := os.OpenRoot(filepath.Join(opts.DataRoot, "projects", opts.ProjectID))
	if err != nil {
		return syncengine.BaseStore{}, nil, err
	}
	return syncengine.BaseStore{Root: root}, root.Close, nil
}

func completeMarkdownAcceptance(intent Intent, journal *Journal, opts Options, projectDir, vaultDir *pathguard.Directory) error {
	currentIntent, err := journal.Load()
	if err != nil || currentIntent.RevisionID != intent.RevisionID {
		return errors.Join(errors.New("Markdown acceptance journal changed"), err)
	}
	intent = currentIntent
	if err := verifyIntentDesired(context.Background(), intent, projectDir, vaultDir); err != nil {
		return err
	}
	if err := runPublishCheckpoint(opts, checkpointBeforeIndexGuard, "final", ""); err != nil {
		return err
	}
	guard, err := markdownIndexGuard(opts, projectDir, vaultDir, false)
	if err != nil || intent.IndexGuard == nil || *guard != *intent.IndexGuard {
		return errors.Join(errors.New("session index guard changed before acceptance"), err)
	}
	if err := runPublishCheckpoint(opts, checkpointAfterIndexGuard, "final", ""); err != nil {
		return err
	}
	review, history, err := markdownDesiredPair(opts.Plan)
	if err != nil {
		return err
	}
	next, err := syncengine.NewMarkdownBaseRecord(review, history, opts.Now().UTC())
	if err != nil || next.ContentHash != intent.BaseDesiredDigest {
		return errors.Join(errors.New("Markdown Base digest mismatch"), err)
	}
	store, closeStore, err := markdownBaseStore(opts)
	if err != nil {
		return err
	}
	defer closeStore()
	current, found, err := store.Load(syncengine.MarkdownBaseEntityID)
	if err != nil {
		return err
	}
	if intent.Stage == StageVerified {
		expected := ""
		if found {
			expected = current.ContentHash
		}
		if expected == intent.BaseDesiredDigest {
			if err := journal.Advance(StageVerified, StageBaseCommitted); err != nil {
				return err
			}
			intent.Stage = StageBaseCommitted
		} else if expected != intent.BasePreimageDigest {
			return errors.New("Markdown Base preimage changed")
		} else {
			if err := store.Commit(expected, next); err != nil {
				return fmt.Errorf("commit Markdown Base pair: %w", err)
			}
			if err := journal.Advance(StageVerified, StageBaseCommitted); err != nil {
				return err
			}
			intent.Stage = StageBaseCommitted
		}
	} else if intent.Stage == StageBaseCommitted {
		if !found || current.ContentHash != intent.BaseDesiredDigest {
			return errors.New("committed Markdown Base pair is missing")
		}
	} else {
		return errors.New("Markdown acceptance has invalid journal stage")
	}
	if err := runPublishCheckpoint(opts, checkpointBeforeReceiptCommit, "", ""); err != nil {
		return err
	}
	return journal.commitMarkdownAccepted(func() error {
		return runPublishCheckpoint(opts, checkpointAfterReceiptCommit, "", "")
	})
}

func markdownReceiptCommitted(journal *Journal, intent Intent) (bool, error) {
	receipt, err := journal.LoadAcceptedMarkdown()
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return receipt.RevisionID == intent.RevisionID, nil
}

func repairMarkdownBaseAfterRollback(intent Intent, journal *Journal, opts Options) error {
	store, closeStore, err := markdownBaseStore(opts)
	if err != nil {
		return err
	}
	defer closeStore()
	current, found, err := store.Load(syncengine.MarkdownBaseEntityID)
	if err != nil {
		return err
	}
	if !found {
		if intent.BasePreimageDigest == "" {
			return nil
		}
		return errors.New("Markdown Base disappeared during rollback")
	}
	if current.ContentHash == intent.BasePreimageDigest {
		return nil
	}
	if current.ContentHash != intent.BaseDesiredDigest {
		return errors.New("Markdown Base changed during rollback")
	}
	if intent.BasePreimageDigest == "" {
		return store.Remove(intent.BaseDesiredDigest, syncengine.MarkdownBaseEntityID)
	}
	review, err := journal.LoadPreimage(intent.BaseReviewPreimage)
	if err != nil {
		return errors.Join(errors.New("Markdown Base review rollback preimage is unavailable"), err)
	}
	history, err := journal.LoadPreimage(intent.BaseHistoryPreimage)
	if err != nil {
		return errors.Join(errors.New("Markdown Base history rollback preimage is unavailable"), err)
	}
	previous, err := syncengine.NewMarkdownBaseRecord(review, history, opts.Now().UTC())
	if err != nil || previous.ContentHash != intent.BasePreimageDigest {
		return errors.Join(errors.New("Markdown Base rollback digest mismatch"), err)
	}
	return store.Commit(intent.BaseDesiredDigest, previous)
}
