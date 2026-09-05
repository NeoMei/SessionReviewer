package syncproject

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

// MarkdownScanRead is the authenticated old acceptance and the separately
// merged pending human draft used by an ordinary scan. Expected maps contain
// the exact Project and Vault preimages that the publisher must compare.
type MarkdownScanRead struct {
	OldAccepted             reviewv4.Accepted
	AcceptedPair            reviewv4.MarkdownPair
	Pending                 reviewv4.MarkdownDraft
	ProjectExpected         map[string][]byte
	VaultExpected           map[string][]byte
	ExpectedGenerationID    string
	ExpectedIndexDigest     string
	ExpectedReceiptRevision string
	ExpectedBaseDigest      string
}

// ReadMarkdownForScan reuses the M5 authenticated read/merge path while the
// caller retains the publication lock. It does not accept the pending draft.
func ReadMarkdownForScan(ctx context.Context, options Options, owner *publicationlock.Owner) (result MarkdownScanRead, retErr error) {
	if ctx == nil || owner == nil || !filepath.IsAbs(options.DataDir) || options.Now == nil || strings.TrimSpace(options.GOOS) == "" || options.Trigger == "" {
		return MarkdownScanRead{}, errors.New("Markdown scan read requires context, publication ownership, absolute data directory, time source, GOOS, and trigger")
	}
	pin, err := PinMapping(options)
	if err != nil {
		return MarkdownScanRead{}, err
	}
	defer func() { retErr = errors.Join(retErr, pin.Close()) }()

	err = owner.Use(options.DataDir, pin.mapping.ID, func() error {
		if err := pin.syncData.EnsureDirectory("locks", 0o700); err != nil {
			return fmt.Errorf("initialize Markdown sync lock directory: %w", err)
		}
		lock, err := project.AcquireProjectLock(pin.syncData.Root, "locks/sync.lock", 10*time.Second)
		if err != nil {
			return err
		}
		defer func() { retErr = errors.Join(retErr, lock.Release()) }()

		report := syncengine.Report{ProjectID: options.ProjectID, Conflicts: []string{}, Errors: []syncengine.EntityError{}}
		humanPlan, base, err := buildMarkdownPlan(pin, options, &report)
		if err != nil {
			return err
		}

		readProject := func(relative string) ([]byte, error) {
			body, found, err := pin.project.ReadRegularOptional(relative, 64<<20)
			if err != nil || !found {
				return nil, errors.Join(fmt.Errorf("required Project file %q is missing", relative), err)
			}
			return body, nil
		}
		readVault := func(relative string) ([]byte, error) {
			vaultRelative := filepath.ToSlash(filepath.Join(pin.mapping.VaultReviewPath, strings.TrimPrefix(relative, "docs/session-review/")))
			body, found, err := pin.vault.ReadRegularOptional(vaultRelative, 64<<20)
			if err != nil || !found {
				return nil, errors.Join(fmt.Errorf("required Vault file %q is missing", relative), err)
			}
			return body, nil
		}
		paths := []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath, filepath.ToSlash(filepath.Join("docs/session-review", markdownIndexRelative))}
		projectExpected := make(map[string][]byte, len(paths))
		vaultExpected := make(map[string][]byte, len(paths))
		for _, relative := range paths {
			projectBody, err := readProject(relative)
			if err != nil {
				return err
			}
			vaultBody, err := readVault(relative)
			if err != nil {
				return err
			}
			projectExpected[relative] = bytes.Clone(projectBody)
			vaultExpected[relative] = bytes.Clone(vaultBody)
		}

		ledger, err := reviewv4.DecodeLedger(projectExpected[reviewv2.MachineLedgerRelativePath])
		if err != nil {
			return err
		}
		baseReview, baseHistory, err := syncengine.MarkdownBaseDocuments(base)
		if err != nil {
			return err
		}
		oldAccepted, err := reviewv4.LoadProjection(baseReview, baseHistory, projectExpected[reviewv2.MachineLedgerRelativePath], projectExpected[paths[3]])
		if err != nil {
			return fmt.Errorf("reload authenticated Markdown baseline: %w", err)
		}
		pendingPair := humanPlan.Pending
		pending, err := reviewv4.ParseMarkdownDraft(pendingPair, ledger)
		if err != nil {
			return fmt.Errorf("validate merged Markdown scan draft: %w", err)
		}
		state, err := publicationstate.OpenReadOnly(pin.data.Path, pin.mapping.ID)
		if err != nil {
			return err
		}
		receipt, receiptErr := state.Accepted()
		closeErr := state.Close()
		if receiptErr != nil || closeErr != nil || receipt.BaseDigest != base.ContentHash {
			return errors.Join(errors.New("Markdown receipt or Base changed during scan read"), receiptErr, closeErr)
		}
		result = MarkdownScanRead{
			OldAccepted: oldAccepted, AcceptedPair: reviewv4.MarkdownPair{Review: bytes.Clone(baseReview), History: bytes.Clone(baseHistory)}, Pending: pending,
			ProjectExpected: projectExpected, VaultExpected: vaultExpected,
			ExpectedGenerationID: humanPlan.ExpectedGenerationID, ExpectedIndexDigest: humanPlan.ExpectedIndexDigest,
			ExpectedReceiptRevision: receipt.RevisionID, ExpectedBaseDigest: base.ContentHash,
		}
		return nil
	})
	return result, errors.Join(err, retErr)
}
