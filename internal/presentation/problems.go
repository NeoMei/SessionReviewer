package presentation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"

	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

type ProblemOperationInput struct {
	Presentation  reviewv4.Presentation
	Ledger        reviewv4.MachineLedger
	Index         sessionindex.Document
	Pending       reviewv4.MarkdownPair
	ExpectedFiles map[string][]byte
}

// RenderProblemOperation creates the exact three-file publication plan for a
// trusted formal-problem operation. The canonical Session index is an
// authenticated guard and is deliberately absent from the write set.
func RenderProblemOperation(in ProblemOperationInput) (RenderPlan, error) {
	paths := []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath, SessionIndexRelativePath}
	for _, path := range paths {
		if _, ok := in.ExpectedFiles[path]; !ok {
			return RenderPlan{}, errors.New("problem operation requires every public preimage")
		}
	}
	oldLedger, err := reviewv4.DecodeLedger(in.ExpectedFiles[reviewv2.MachineLedgerRelativePath])
	if err != nil {
		return RenderPlan{}, err
	}
	index, err := sessionindex.Parse(in.ExpectedFiles[SessionIndexRelativePath])
	if err != nil {
		return RenderPlan{}, err
	}
	if index.Digest != in.Index.Digest || index.ProjectID != in.Presentation.ProjectID || index.GenerationID != in.Presentation.GenerationID || index.ProjectViewDigest != in.Presentation.ProjectViewDigest {
		return RenderPlan{}, errors.New("problem operation index guard mismatch")
	}
	pair, err := reviewv4.RenderMarkdownProblemOperation(in.Presentation, oldLedger, in.Pending)
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
	ledger.ReviewSHA256, ledger.HistorySHA256 = problemSHA(pair.Review), problemSHA(pair.History)
	ledger.SyncHashes.ReviewSHA256, ledger.SyncHashes.HistorySHA256 = ledger.ReviewSHA256, ledger.HistorySHA256
	ledger.SyncHashes.SessionIndexDigest = index.Digest
	ledgerBody, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		return RenderPlan{}, err
	}
	if _, err := reviewv4.LoadProjection(pair.Review, pair.History, ledgerBody, in.ExpectedFiles[SessionIndexRelativePath]); err != nil {
		return RenderPlan{}, err
	}
	return RenderPlan{ProjectID: in.Presentation.ProjectID, GenerationID: in.Presentation.GenerationID, ProjectViewDigest: in.Presentation.ProjectViewDigest, Files: []FilePlan{
		{Relative: reviewv2.ReviewRelativePath, Expected: cloneProblemBytes(in.ExpectedFiles[reviewv2.ReviewRelativePath]), ExpectedExists: true, Desired: pair.Review, Mode: 0o644},
		{Relative: reviewv2.HistoryRelativePath, Expected: cloneProblemBytes(in.ExpectedFiles[reviewv2.HistoryRelativePath]), ExpectedExists: true, Desired: pair.History, Mode: 0o644},
		{Relative: filepath.ToSlash(reviewv2.MachineLedgerRelativePath), Expected: cloneProblemBytes(in.ExpectedFiles[reviewv2.MachineLedgerRelativePath]), ExpectedExists: true, Desired: ledgerBody, Mode: 0o600},
	}}, nil
}

func problemSHA(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
func cloneProblemBytes(body []byte) []byte {
	result := make([]byte, len(body))
	copy(result, body)
	return result
}
