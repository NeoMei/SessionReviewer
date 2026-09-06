package publication

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
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

// PublishMarkdownMigrationLocked adapts an authenticated accepted legacy
// generation into the existing four-file Markdown transaction. Source proof
// is verified before the prepared pointer can advance.
func PublishMarkdownMigrationLocked(ctx context.Context, opts Options, migration syncproject.MigrationPublication) (Result, error) {
	if migration.PublicationLock == nil || migration.Preview.SourceJournalDigest == "" || migration.Preview.SourceGenerationID == "" || migration.Preview.SourceManifestDigest == "" || migration.Preview.TargetManifestDigest == "" {
		return Result{}, errors.New("Markdown migration requires authenticated source proof")
	}
	state, err := publicationstate.OpenReadOnly(migration.DataRoot, migration.ProjectID)
	if err != nil {
		return Result{}, err
	}
	currentIntent, intentErr := state.Intent()
	closeErr := state.Close()
	if intentErr != nil || closeErr != nil {
		return Result{}, errors.Join(intentErr, closeErr)
	}
	proof, err := migrationSourceProof(currentIntent, migration)
	if err != nil {
		return Result{}, err
	}
	store, err := memorystore.Open(migration.DataRoot, migration.ProjectID)
	if err != nil {
		return Result{}, err
	}
	defer store.Close()
	publishedID, sourceManifest, err := store.LoadPublished()
	if err != nil {
		return Result{}, err
	}
	sourceDigest, err := memory.Digest(sourceManifest)
	if err != nil || publishedID != proof.GenerationID || sourceDigest != proof.ManifestDigest || sourceManifest.ProjectViewDigest != proof.ProjectViewDigest || (sourceManifest.SessionIndexDigest != "" && proof.IndexDigest != sourceManifest.SessionIndexDigest) {
		return Result{}, errors.Join(fmt.Errorf("Markdown migration source no longer matches published generation: published=%q proof_generation=%q manifest=%q proof_manifest=%q project=%q proof_project=%q public_index_hash=%q private_index_digest=%q", publishedID, proof.GenerationID, sourceDigest, proof.ManifestDigest, sourceManifest.ProjectViewDigest, proof.ProjectViewDigest, migration.Preview.SourceHashes.SessionIndex, sourceManifest.SessionIndexDigest), err)
	}
	targetManifest := sourceManifest
	targetDigest := sourceDigest
	if migration.SuccessorManifest != nil {
		targetManifest = *migration.SuccessorManifest
		targetDigest, err = memory.Digest(targetManifest)
		if err != nil {
			return Result{}, err
		}
	}
	if targetManifest.GenerationID != migration.PreparedGeneration || targetDigest != migration.Preview.TargetManifestDigest || targetManifest.SessionIndexDigest == "" {
		return Result{}, errors.New("Markdown migration target manifest mismatch")
	}
	var targetIndex []byte
	for _, file := range migration.Plan.Files {
		if file.Relative == sessionIndexRelativePath {
			targetIndex = bytes.Clone(file.Desired)
		}
	}
	parsedTargetIndex, err := sessionindex.Parse(targetIndex)
	if err != nil || parsedTargetIndex.Digest != targetManifest.SessionIndexDigest || parsedTargetIndex.GenerationID != targetManifest.GenerationID {
		return Result{}, errors.Join(errors.New("Markdown migration target index proof mismatch"), err)
	}
	if migration.SuccessorManifest != nil {
		storedIndexDigest, err := store.PutSessionIndex(parsedTargetIndex)
		if err != nil || storedIndexDigest != targetManifest.SessionIndexDigest {
			return Result{}, errors.Join(errors.New("store Markdown migration target index"), err)
		}
	}
	prepared, _, err := store.LoadPrepared()
	if err != nil {
		return Result{}, err
	}
	targetPrepared := memorystore.Prepared{GenerationID: targetManifest.GenerationID, ManifestDigest: targetDigest, ProjectViewDigest: targetManifest.ProjectViewDigest}
	if prepared != targetPrepared {
		if migration.SuccessorManifest == nil {
			return Result{}, errors.New("Markdown migration source generation is not prepared")
		}
		sourcePrepared := memorystore.Prepared{GenerationID: sourceManifest.GenerationID, ManifestDigest: sourceDigest, ProjectViewDigest: sourceManifest.ProjectViewDigest}
		if prepared != sourcePrepared {
			return Result{}, errors.New("Markdown migration found unrelated prepared generation")
		}
		if _, err := store.AdvancePrepared(sourcePrepared, targetManifest); err != nil {
			return Result{}, err
		}
	}
	opts.ProjectID, opts.PreparedGeneration, opts.Plan, opts.Mapping, opts.DataRoot = migration.ProjectID, migration.PreparedGeneration, migration.Plan, migration.Mapping, migration.DataRoot
	opts.markdownIndex, opts.markdownIndexDigest = targetIndex, targetManifest.SessionIndexDigest
	opts.markdownVaultExpected = migration.VaultExpected
	opts.migrationSource = &proof
	return PublishLocked(ctx, opts, migration.PublicationLock)
}

func migrationSourceProof(intent Intent, migration syncproject.MigrationPublication) (publicationstate.MigrationSourceProof, error) {
	if intent.Version == 2 && intent.Stage == StageCommitted && intent.Outcome == OutcomeRolledBack && intent.MigrationSource != nil {
		proof := *intent.MigrationSource
		if proof.JournalDigest != migration.Preview.SourceJournalDigest || proof.GenerationID != migration.Preview.SourceGenerationID || proof.ManifestDigest != migration.Preview.SourceManifestDigest || !migrationProofMatchesPreimages(proof, migration) {
			return publicationstate.MigrationSourceProof{}, errors.New("rolled-back Markdown migration source proof changed")
		}
		return proof, nil
	}
	digest, err := memory.Digest(intent)
	if err != nil || intent.Version != 1 || intent.Stage != StageCommitted || digest != migration.Preview.SourceJournalDigest || intent.GenerationID != migration.Preview.SourceGenerationID || intent.ManifestDigest != migration.Preview.SourceManifestDigest {
		return publicationstate.MigrationSourceProof{}, errors.Join(errors.New("legacy source publication journal changed"), err)
	}
	proof := publicationstate.MigrationSourceProof{GenerationID: intent.GenerationID, ManifestDigest: intent.ManifestDigest, ProjectViewDigest: intent.ProjectViewDigest, JournalDigest: digest, Destinations: append([]Destination(nil), intent.Destinations...)}
	if store, openErr := memorystore.OpenReadOnly(migration.DataRoot, migration.ProjectID); openErr == nil {
		_, sourceManifest, loadErr := store.LoadPublished()
		closeErr := store.Close()
		if loadErr != nil || closeErr != nil {
			return publicationstate.MigrationSourceProof{}, errors.Join(loadErr, closeErr)
		}
		proof.IndexDigest = sourceManifest.SessionIndexDigest
		if proof.IndexDigest == "" {
			for _, file := range migration.Plan.Files {
				if file.Relative != sessionIndexRelativePath || !file.ExpectedExists {
					continue
				}
				index, parseErr := sessionindex.Parse(file.Expected)
				if parseErr != nil {
					return publicationstate.MigrationSourceProof{}, parseErr
				}
				proof.IndexDigest = index.Digest
			}
		}
	} else {
		return publicationstate.MigrationSourceProof{}, openErr
	}
	if !migrationProofMatchesPreimages(proof, migration) {
		return publicationstate.MigrationSourceProof{}, errors.New("legacy source journal does not match exact migration preimages")
	}
	return proof, nil
}

func migrationProofMatchesPreimages(proof publicationstate.MigrationSourceProof, migration syncproject.MigrationPublication) bool {
	want := make(map[string]string, len(migration.Plan.Files)*2)
	for _, file := range migration.Plan.Files {
		if !file.ExpectedExists {
			return false
		}
		want["project\x00"+file.Relative] = sha256Hex(file.Expected)
		vault, ok := migration.VaultExpected[file.Relative]
		if !ok || vault == nil {
			return false
		}
		want["vault\x00"+vaultRelativePath(migration.Mapping.VaultReviewPath, file.Relative)] = sha256Hex(vault)
	}
	if len(want) != len(proof.Destinations) {
		return false
	}
	for _, destination := range proof.Destinations {
		if want[destination.Side+"\x00"+destination.Relative] != destination.DesiredSHA256 {
			return false
		}
	}
	return true
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
		return completeMarkdownAcceptance(ctx, intent, journal, opts, projectDir, vaultDir)
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

func completeMarkdownAcceptance(ctx context.Context, intent Intent, journal *Journal, opts Options, projectDir, vaultDir *pathguard.Directory) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	currentIntent, err := journal.Load()
	if err != nil || currentIntent.RevisionID != intent.RevisionID {
		return errors.Join(errors.New("Markdown acceptance journal changed"), err)
	}
	intent = currentIntent
	if err := verifyIntentDesired(ctx, intent, projectDir, vaultDir); err != nil {
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
	if cause := context.Cause(ctx); cause != nil {
		return cause
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
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if err := journal.commitMarkdownAccepted(func() error {
		return runPublishCheckpoint(opts, checkpointAfterReceiptCommit, "", "")
	}); err != nil {
		return err
	}
	return context.Cause(ctx)
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
