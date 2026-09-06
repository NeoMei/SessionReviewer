package syncproject

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
)

const (
	markdownReviewRelative  = "项目回顾.md"
	markdownHistoryRelative = "项目历史.md"
	markdownLedgerRelative  = ".session-reviewer/ledger.json"
	markdownIndexRelative   = ".session-reviewer/session-index.json"
)

var errMarkdownFieldsConflict = errors.New("Markdown fields conflict")

type MarkdownSyncPlan struct {
	Plan                    presentation.RenderPlan
	Pending                 reviewv4.MarkdownPair
	Index                   []byte
	ExpectedGenerationID    string
	ExpectedIndexDigest     string
	ProjectExpected         map[string][]byte
	VaultExpected           map[string][]byte
	ExpectedReceiptRevision string
	ExpectedBaseDigest      string
}

type MarkdownPublisher func(context.Context, MarkdownSyncPlan, *publicationlock.Owner) error
type MarkdownRecoverer func(context.Context, *publicationlock.Owner) error

// VerifyMarkdownBinding verifies the public projection identities against an
// already authenticated private generation manifest.
func VerifyMarkdownBinding(accepted reviewv4.Accepted, manifest memory.GenerationManifest) error {
	if manifest.SessionIndexDigest == "" ||
		manifest.ProjectID != accepted.Review.ProjectID ||
		manifest.GenerationID != accepted.Review.GenerationID ||
		manifest.ProjectViewDigest != accepted.Review.ProjectViewDigest ||
		manifest.SessionIndexDigest != accepted.SessionIndex.Digest {
		return errors.New("markdown projection has no matching private index binding")
	}
	return nil
}

func RunMarkdown(ctx context.Context, options Options) (_ syncengine.Report, retErr error) {
	report := syncengine.Report{ProjectID: options.ProjectID, DryRun: options.DryRun, Operations: []syncengine.Operation{}, Conflicts: []string{}, Errors: []syncengine.EntityError{}}
	if ctx == nil || !filepath.IsAbs(options.DataDir) || options.Now == nil || strings.TrimSpace(options.GOOS) == "" || options.Trigger == "" {
		return report, errors.New("Markdown sync requires context, absolute data directory, time source, GOOS, and trigger")
	}
	if cause := context.Cause(ctx); cause != nil {
		return report, cause
	}
	pin, err := PinMapping(options)
	if err != nil {
		return report, err
	}
	defer func() { retErr = errors.Join(retErr, pin.Close()) }()

	build := func() (MarkdownSyncPlan, syncengine.BaseRecord, error) {
		return buildMarkdownPlan(pin, options, &report)
	}
	if options.DryRun {
		plan, _, err := build()
		if err == nil {
			report.Operations = markdownOperations(plan)
		}
		return report, err
	}
	if options.PublishMarkdown == nil {
		return report, errors.New("Markdown publisher is required")
	}
	acquire := options.acquireMarkdownLock
	if acquire == nil {
		acquire = publicationlock.Acquire
	}
	owner, err := acquire(options.DataDir, pin.mapping.ID, 10*time.Second)
	if err != nil {
		return report, err
	}
	defer func() { retErr = errors.Join(retErr, owner.Release()) }()
	if err := pin.syncData.EnsureDirectory("locks", 0o700); err != nil {
		return report, fmt.Errorf("initialize Markdown sync lock directory: %w", err)
	}
	lock, err := project.AcquireProjectLock(pin.syncData.Root, "locks/sync.lock", 10*time.Second)
	if err != nil {
		return report, err
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	if options.RecoverMarkdown == nil {
		return report, errors.New("Markdown recovery callback is required")
	}
	if err := options.RecoverMarkdown(ctx, owner); err != nil {
		return report, err
	}
	if cause := context.Cause(ctx); cause != nil {
		return report, cause
	}
	plan, _, err := build()
	if err != nil {
		return report, err
	}
	if len(plan.Plan.Files) == 0 {
		return report, pin.verify(options)
	}
	if cause := context.Cause(ctx); cause != nil {
		return report, cause
	}
	if err := options.PublishMarkdown(ctx, plan, owner); err != nil {
		return report, err
	}
	return report, pin.verify(options)
}

func markdownOperations(plan MarkdownSyncPlan) []syncengine.Operation {
	operations := make([]syncengine.Operation, 0, len(plan.Plan.Files)*2)
	for _, file := range plan.Plan.Files {
		relative := strings.TrimPrefix(file.Relative, "docs/session-review/")
		entityID := "machine-ledger"
		switch file.Relative {
		case reviewv2.ReviewRelativePath:
			entityID = "project-overview"
		case reviewv2.HistoryRelativePath:
			entityID = "project-history"
		}
		after := bareHash(file.Desired)
		operations = append(operations, syncengine.Operation{
			EntityID: entityID, Kind: syncengine.OperationUpdateProject, Target: syncengine.SideProject,
			RelativePath: relative, BeforeHash: bareHash(file.Expected), AfterHash: after,
		})
		operations = append(operations, syncengine.Operation{
			EntityID: entityID, Kind: syncengine.OperationUpdateVault, Target: syncengine.SideVault,
			RelativePath: relative, BeforeHash: bareHash(plan.VaultExpected[file.Relative]), AfterHash: after,
		})
	}
	return operations
}

func buildMarkdownPlan(pin *MappingPin, options Options, report *syncengine.Report) (MarkdownSyncPlan, syncengine.BaseRecord, error) {
	return buildMarkdownPlanWithReadSet(pin, options, report, nil)
}

func buildMarkdownPlanWithReadSet(pin *MappingPin, options Options, report *syncengine.Report, readSet *markdownStatusReadSet) (MarkdownSyncPlan, syncengine.BaseRecord, error) {
	store, err := memorystore.OpenReadOnly(pin.data.Path, pin.mapping.ID)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, fmt.Errorf("open private store read-only: %w", err)
	}
	defer store.Close()
	publishedID, manifest, err := store.LoadPublished()
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	state, err := publicationstate.OpenReadOnly(pin.data.Path, pin.mapping.ID)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	defer state.Close()
	var intent publicationstate.Intent
	if readSet != nil {
		intent, err = state.Intent()
		if err != nil {
			return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
		}
	}
	receipt, err := state.Accepted()
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, fmt.Errorf("authenticate accepted Markdown receipt: %w", err)
	}
	manifestDigest, digestErr := memory.Digest(manifest)
	if digestErr != nil || receipt.GenerationID != publishedID || receipt.ManifestDigest != manifestDigest || receipt.ProjectViewDigest != manifest.ProjectViewDigest {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, errors.New("accepted Markdown receipt does not match published generation")
	}

	readProject := func(relative string) ([]byte, error) {
		body, found, err := pin.project.ReadRegularOptional(filepath.ToSlash(filepath.Join("docs/session-review", relative)), 64<<20)
		if err != nil || !found {
			return nil, errors.Join(fmt.Errorf("required Project file %q is missing", relative), err)
		}
		return body, nil
	}
	readVault := func(relative string) ([]byte, error) {
		body, found, err := pin.vault.ReadRegularOptional(filepath.ToSlash(filepath.Join(pin.mapping.VaultReviewPath, relative)), 64<<20)
		if err != nil || !found {
			return nil, errors.Join(fmt.Errorf("required Vault file %q is missing", relative), err)
		}
		return body, nil
	}
	projectReview, err := readProject(markdownReviewRelative)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	projectHistory, err := readProject(markdownHistoryRelative)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	ledgerBody, err := readProject(markdownLedgerRelative)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	indexBody, err := readProject(markdownIndexRelative)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	vaultReview, err := readVault(markdownReviewRelative)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	vaultHistory, err := readVault(markdownHistoryRelative)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	vaultLedger, err := readVault(markdownLedgerRelative)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	vaultIndex, err := readVault(markdownIndexRelative)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	if !bytes.Equal(ledgerBody, vaultLedger) || !bytes.Equal(indexBody, vaultIndex) {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, errors.New("machine ledger or session index differs across Project and Vault")
	}
	ledger, err := reviewv4.DecodeLedger(ledgerBody)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}

	baseRecord, found, err := (syncengine.BaseStore{Root: pin.syncData.Root}).Load(syncengine.MarkdownBaseEntityID)
	if err != nil || !found || baseRecord.ContentHash != receipt.BaseDigest {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, errors.Join(errors.New("authenticated Markdown merge base is missing"), err)
	}
	baseReview, baseHistory, err := syncengine.MarkdownBaseDocuments(baseRecord)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	if err := verifyReceiptBaseline(receipt, baseReview, baseHistory, ledgerBody, indexBody, vaultIndex); err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	accepted, err := reviewv4.LoadProjection(baseReview, baseHistory, ledgerBody, indexBody)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, fmt.Errorf("load accepted Markdown baseline: %w", err)
	}
	if err := VerifyMarkdownBinding(accepted, manifest); err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	storedView, err := store.LoadObject(memorystore.ObjectProjectView, manifest.ProjectViewDigest)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, fmt.Errorf("verify private ProjectView: %w", err)
	}
	storedIndex, err := store.LoadObject(memorystore.ObjectSessionIndex, manifest.SessionIndexDigest)
	if err != nil || !bytes.Equal(storedIndex, indexBody) {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, errors.Join(errors.New("public session index is not the private canonical object"), err)
	}
	if readSet != nil {
		*readSet = markdownStatusReadSet{
			privateRoots: readSet.privateRoots,
			intent:       intent, receipt: receipt, generation: publishedID, manifest: manifest, base: baseRecord,
			viewHash: bareHash(storedView), indexHash: bareHash(storedIndex),
			project: map[string]string{markdownReviewRelative: bareHash(projectReview), markdownHistoryRelative: bareHash(projectHistory), markdownLedgerRelative: bareHash(ledgerBody), markdownIndexRelative: bareHash(indexBody)},
			vault:   map[string]string{markdownReviewRelative: bareHash(vaultReview), markdownHistoryRelative: bareHash(vaultHistory), markdownLedgerRelative: bareHash(vaultLedger), markdownIndexRelative: bareHash(vaultIndex)},
		}
	}

	mergeOne := func(relative string, base, projectBytes, vaultBytes []byte) ([]byte, error) {
		baseDoc, err := syncdoc.ParseV4(relative, base, ledger)
		if err != nil {
			return nil, err
		}
		projectDoc, err := syncdoc.ParseV4(relative, projectBytes, ledger)
		if err != nil {
			return nil, err
		}
		vaultDoc, err := syncdoc.ParseV4(relative, vaultBytes, ledger)
		if err != nil {
			return nil, err
		}
		for _, document := range []syncdoc.Document{projectDoc, vaultDoc} {
			source, err := document.SensitiveScanSource()
			if err != nil {
				return nil, err
			}
			if len(redact.Default().Text(string(source)).Findings) != 0 {
				return nil, errors.New("sensitive content blocks Markdown publication")
			}
		}
		merged := syncengine.MergeV4Units(syncengine.V4MergeInput{Base: baseDoc.SemanticUnits(), Project: projectDoc.SemanticUnits(), Vault: vaultDoc.SemanticUnits(), HasBase: true})
		if len(merged.Conflicts) != 0 {
			for _, conflict := range merged.Conflicts {
				report.Conflicts = append(report.Conflicts, string(conflict.Key.Kind)+":"+conflict.Key.Name)
			}
			return nil, errMarkdownFieldsConflict
		}
		document, err := projectDoc.WithSemanticUnits(merged.Units)
		if err != nil {
			return nil, err
		}
		return document.Render()
	}
	mergedReview, reviewErr := mergeOne(markdownReviewRelative, baseReview, projectReview, vaultReview)
	mergedHistory, historyErr := mergeOne(markdownHistoryRelative, baseHistory, projectHistory, vaultHistory)
	// An invalid document must not be hidden by a conflict in the other one.
	for _, err := range []error{reviewErr, historyErr} {
		if err != nil && err != errMarkdownFieldsConflict {
			return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
		}
	}
	if reviewErr != nil || historyErr != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, errMarkdownFieldsConflict
	}
	draftPair := reviewv4.MarkdownPair{Review: mergedReview, History: mergedHistory}
	draft, err := reviewv4.ParseMarkdownDraft(draftPair, ledger)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	desiredPair, err := reviewv4.RenderMarkdownDraft(draft.Presentation, ledger, draftPair)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	ledger.AcceptedRevision = draft.Presentation.Revision
	ledger.HumanPatches = append([]reviewv4.Patch(nil), draft.Presentation.HumanPatches...)
	ledger.OrphanPatches = append([]reviewv4.Patch(nil), draft.Presentation.OrphanPatches...)
	ledger.GeneratedBaselines = append([]reviewv4.Baseline(nil), draft.Presentation.GeneratedBaselines...)
	ledger.DocumentProjection.PresentationBase = draft.Presentation
	ledger.ReviewSHA256, ledger.HistorySHA256 = bareHash(desiredPair.Review), bareHash(desiredPair.History)
	ledger.SyncHashes.ReviewSHA256, ledger.SyncHashes.HistorySHA256 = ledger.ReviewSHA256, ledger.HistorySHA256
	desiredLedger, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, err
	}
	if _, err := reviewv4.LoadProjection(desiredPair.Review, desiredPair.History, desiredLedger, indexBody); err != nil {
		return MarkdownSyncPlan{}, syncengine.BaseRecord{}, fmt.Errorf("validate desired Markdown projection: %w", err)
	}
	plan := presentation.RenderPlan{ProjectID: accepted.Review.ProjectID, GenerationID: publishedID, ProjectViewDigest: accepted.Review.ProjectViewDigest, Files: []presentation.FilePlan{
		{Relative: filepath.ToSlash(filepath.Join("docs/session-review", markdownReviewRelative)), Expected: projectReview, ExpectedExists: true, Desired: desiredPair.Review, Mode: 0o644},
		{Relative: filepath.ToSlash(filepath.Join("docs/session-review", markdownHistoryRelative)), Expected: projectHistory, ExpectedExists: true, Desired: desiredPair.History, Mode: 0o644},
		{Relative: filepath.ToSlash(filepath.Join("docs/session-review", markdownLedgerRelative)), Expected: ledgerBody, ExpectedExists: true, Desired: desiredLedger, Mode: 0o600},
	}}
	if bytes.Equal(projectReview, desiredPair.Review) && bytes.Equal(vaultReview, desiredPair.Review) && bytes.Equal(projectHistory, desiredPair.History) && bytes.Equal(vaultHistory, desiredPair.History) && bytes.Equal(ledgerBody, desiredLedger) {
		plan.Files = nil
	}
	indexRelative := filepath.ToSlash(filepath.Join("docs/session-review", markdownIndexRelative))
	return MarkdownSyncPlan{Plan: plan, Pending: reviewv4.MarkdownPair{Review: bytes.Clone(draftPair.Review), History: bytes.Clone(draftPair.History)}, Index: bytes.Clone(indexBody), ExpectedGenerationID: publishedID, ExpectedIndexDigest: manifest.SessionIndexDigest, ExpectedReceiptRevision: receipt.RevisionID, ExpectedBaseDigest: baseRecord.ContentHash, ProjectExpected: map[string][]byte{
		reviewv2.ReviewRelativePath: bytes.Clone(projectReview), reviewv2.HistoryRelativePath: bytes.Clone(projectHistory), reviewv2.MachineLedgerRelativePath: bytes.Clone(ledgerBody), indexRelative: bytes.Clone(indexBody),
	}, VaultExpected: map[string][]byte{
		reviewv2.ReviewRelativePath: bytes.Clone(vaultReview), reviewv2.HistoryRelativePath: bytes.Clone(vaultHistory), reviewv2.MachineLedgerRelativePath: bytes.Clone(vaultLedger), indexRelative: bytes.Clone(vaultIndex),
	}}, baseRecord, nil
}

func verifyReceiptBaseline(receipt publicationstate.AcceptedReceipt, review, history, ledger, projectIndex, vaultIndex []byte) error {
	if receipt.IndexGuard == nil || receipt.IndexGuard.ProjectSHA256 != bareHash(projectIndex) || receipt.IndexGuard.VaultSHA256 != bareHash(vaultIndex) {
		return errors.New("session index does not match accepted private receipt")
	}
	const indexSuffix = "/.session-reviewer/session-index.json"
	vaultRoot := strings.TrimSuffix(receipt.IndexGuard.VaultRelative, indexSuffix)
	if vaultRoot == receipt.IndexGuard.VaultRelative {
		return errors.New("accepted Markdown receipt has an invalid Vault index guard")
	}
	want := map[string]string{}
	for relative, hash := range map[string]string{reviewv2.ReviewRelativePath: bareHash(review), reviewv2.HistoryRelativePath: bareHash(history), reviewv2.MachineLedgerRelativePath: bareHash(ledger)} {
		want["project\x00"+relative] = hash
		want["vault\x00"+filepath.ToSlash(filepath.Join(vaultRoot, strings.TrimPrefix(relative, "docs/session-review/")))] = hash
	}
	if receipt.RequiresPointer {
		want["project\x00"+receipt.IndexGuard.Relative] = bareHash(projectIndex)
		want["vault\x00"+receipt.IndexGuard.VaultRelative] = bareHash(vaultIndex)
	}
	if len(receipt.Destinations) != len(want) {
		return errors.New("accepted Markdown receipt has an unexpected destination set")
	}
	for _, destination := range receipt.Destinations {
		key := destination.Side + "\x00" + destination.Relative
		hash, found := want[key]
		if !found || destination.DesiredSHA256 != hash {
			return errors.New("accepted Markdown document does not match private receipt")
		}
		delete(want, key)
	}
	if len(want) != 0 {
		return errors.New("accepted Markdown receipt has an incomplete destination set")
	}
	return nil
}

func bareHash(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
