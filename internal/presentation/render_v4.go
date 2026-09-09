package presentation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"

	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

const SessionIndexRelativePath = "docs/session-review/.session-reviewer/session-index.json"

// V4RenderInput contains a fully mapped next presentation and ledger together
// with the authenticated public preimages used to preserve human Markdown.
type V4RenderInput struct {
	Presentation    reviewv4.Presentation
	MilestoneUpdate *reviewv4.ScanMilestoneUpdate
	Ledger          reviewv4.MachineLedger
	Index           sessionindex.Document
	Previous        *reviewv4.MarkdownPair
	Pending         *reviewv4.MarkdownPair
	PreviousLedger  *reviewv4.MachineLedger
	ExpectedFiles   map[string][]byte
}

// RenderV4 renders the four-file human Markdown projection.
func RenderV4(in V4RenderInput) (RenderPlan, error) {
	if in.MilestoneUpdate != nil && (len(in.ExpectedFiles) == 0 || in.Previous == nil || in.Pending == nil || in.PreviousLedger == nil) {
		return RenderPlan{}, errors.New("generated milestone update requires an authenticated existing Markdown baseline")
	}
	paths := []string{
		reviewv2.ReviewRelativePath,
		reviewv2.HistoryRelativePath,
		reviewv2.MachineLedgerRelativePath,
		SessionIndexRelativePath,
	}
	if len(in.ExpectedFiles) != 0 {
		if len(in.ExpectedFiles) != len(paths) || in.Previous == nil || in.Pending == nil || in.PreviousLedger == nil {
			return RenderPlan{}, errors.New("existing Markdown projection requires an authenticated complete baseline")
		}
		for _, relative := range paths {
			if _, exists := in.ExpectedFiles[relative]; !exists {
				return RenderPlan{}, errors.New("existing Markdown projection requires all four preimages")
			}
		}
		oldLedger, err := reviewv4.DecodeLedger(in.ExpectedFiles[reviewv2.MachineLedgerRelativePath])
		if err != nil || !reflect.DeepEqual(oldLedger, *in.PreviousLedger) {
			return RenderPlan{}, errors.Join(errors.New("authenticated ledger does not match expected preimage"), err)
		}
		oldIndex, err := sessionindex.Parse(in.ExpectedFiles[SessionIndexRelativePath])
		if err != nil {
			return RenderPlan{}, errors.Join(errors.New("authenticated index preimage is invalid"), err)
		}
		if _, err := reviewv4.LoadProjection(in.Previous.Review, in.Previous.History, in.ExpectedFiles[reviewv2.MachineLedgerRelativePath], in.ExpectedFiles[SessionIndexRelativePath]); err != nil {
			return RenderPlan{}, errors.Join(errors.New("authenticated Markdown baseline is invalid"), err)
		}
		if in.MilestoneUpdate == nil && bytes.Equal(in.Pending.Review, in.Previous.Review) && bytes.Equal(in.Pending.History, in.Previous.History) &&
			reflect.DeepEqual(oldLedger, in.Ledger) && reflect.DeepEqual(oldLedger.DocumentProjection.PresentationBase, in.Presentation) && reflect.DeepEqual(oldIndex, in.Index) {
			return exactV4Plan(in, paths, [][]byte{in.Previous.Review, in.Previous.History, in.ExpectedFiles[reviewv2.MachineLedgerRelativePath], in.ExpectedFiles[SessionIndexRelativePath]}), nil
		}
	}

	var pair reviewv4.MarkdownPair
	var err error
	if in.Previous == nil {
		seed := in.Ledger
		seed.DocumentProjection = &reviewv4.DocumentProjection{SchemaVersion: 1, Format: "review-markdown-v1", PresentationBase: in.Presentation}
		seed.ReviewSHA256, seed.HistorySHA256 = strings.Repeat("0", 64), strings.Repeat("0", 64)
		seed.SyncHashes.ReviewSHA256, seed.SyncHashes.HistorySHA256, seed.SyncHashes.LedgerSHA256 = seed.ReviewSHA256, seed.HistorySHA256, strings.Repeat("0", 64)
		seedBody, seedErr := reviewv4.RenderLedger(seed)
		if seedErr != nil {
			return RenderPlan{}, seedErr
		}
		seed, seedErr = reviewv4.DecodeLedger(seedBody)
		if seedErr != nil {
			return RenderPlan{}, seedErr
		}
		pair, err = reviewv4.RenderMarkdown(in.Presentation, seed, nil)
	} else {
		if in.MilestoneUpdate == nil {
			pair, err = reviewv4.RenderMarkdownUpdate(in.Presentation, *in.PreviousLedger, *in.Pending)
		} else {
			pair, err = reviewv4.RenderMarkdownMilestoneUpdate(in.Presentation, *in.PreviousLedger, *in.Pending, *in.MilestoneUpdate)
		}
	}
	if err != nil {
		return RenderPlan{}, err
	}
	indexBody, err := sessionindex.Render(in.Index)
	if err != nil {
		return RenderPlan{}, err
	}
	ledger := in.Ledger
	ledger.ProjectID, ledger.GenerationID, ledger.ProjectViewDigest = in.Presentation.ProjectID, in.Presentation.GenerationID, in.Presentation.ProjectViewDigest
	ledger.AcceptedRevision = in.Presentation.Revision
	ledger.HumanPatches = append([]reviewv4.Patch(nil), in.Presentation.HumanPatches...)
	ledger.OrphanPatches = append([]reviewv4.Patch(nil), in.Presentation.OrphanPatches...)
	ledger.GeneratedBaselines = append([]reviewv4.Baseline(nil), in.Presentation.GeneratedBaselines...)
	ledger.DocumentProjection = &reviewv4.DocumentProjection{SchemaVersion: 1, Format: "review-markdown-v1", PresentationBase: in.Presentation}
	ledger.ReviewSHA256, ledger.HistorySHA256 = v4SHA(pair.Review), v4SHA(pair.History)
	ledger.SyncHashes.ReviewSHA256, ledger.SyncHashes.HistorySHA256 = ledger.ReviewSHA256, ledger.HistorySHA256
	ledger.SyncHashes.SessionIndexDigest = in.Index.Digest
	ledgerBody, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		return RenderPlan{}, err
	}
	if expected, exists := in.ExpectedFiles[reviewv2.MachineLedgerRelativePath]; exists {
		old, decodeErr := reviewv4.DecodeLedger(expected)
		if decodeErr == nil && reflect.DeepEqual(old, ledger) {
			ledgerBody = bytes.Clone(expected)
		}
	}
	if expected, exists := in.ExpectedFiles[SessionIndexRelativePath]; exists {
		old, parseErr := sessionindex.Parse(expected)
		if parseErr == nil && reflect.DeepEqual(old, in.Index) {
			indexBody = bytes.Clone(expected)
		}
	}
	if _, err := reviewv4.LoadProjection(pair.Review, pair.History, ledgerBody, indexBody); err != nil {
		return RenderPlan{}, fmt.Errorf("validate rendered v4 projection: %w", err)
	}
	return exactV4Plan(in, paths, [][]byte{pair.Review, pair.History, ledgerBody, indexBody}), nil
}

func exactV4Plan(in V4RenderInput, paths []string, desired [][]byte) RenderPlan {
	modes := []fs.FileMode{0o644, 0o644, 0o600, 0o600}
	plan := RenderPlan{ProjectID: in.Presentation.ProjectID, GenerationID: in.Presentation.GenerationID, ProjectViewDigest: in.Presentation.ProjectViewDigest, Files: make([]FilePlan, 0, len(paths))}
	for i, relative := range paths {
		expected, exists := in.ExpectedFiles[relative]
		plan.Files = append(plan.Files, FilePlan{Relative: relative, Expected: bytes.Clone(expected), ExpectedExists: exists, Desired: desired[i], Mode: modes[i]})
	}
	return plan
}

func v4SHA(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
