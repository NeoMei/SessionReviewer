package contextupdate

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/pricing"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

type v4MapInput struct {
	Accepted   reviewv4.Accepted
	Index      sessionindex.Document
	Accounting accounting.ProjectSummary
}

func mapV4Scan(in v4MapInput) (reviewv4.Presentation, reviewv4.MachineLedger, error) {
	if in.Index.ProjectID == "" || in.Index.GenerationID == "" || in.Index.ProjectViewDigest == "" {
		return reviewv4.Presentation{}, reviewv4.MachineLedger{}, errors.New("v4 scan requires canonical index identity")
	}
	var presentation reviewv4.Presentation
	var ledger reviewv4.MachineLedger
	if in.Accepted.Review.ProjectID == "" {
		presentation = reviewv4.Presentation{
			SchemaVersion: 4, MinimumReaderVersion: "0.4.0", MinimumWriterVersion: "0.4.0",
			ProjectID: in.Index.ProjectID, GenerationID: in.Index.GenerationID, ProjectViewDigest: in.Index.ProjectViewDigest, Revision: 1,
			CurrentState: reviewv4.CurrentState{}, Timeline: []reviewv4.Timeline{}, Decisions: []reviewv4.Decision{}, Risks: []reviewv4.Risk{}, OpenLoops: []reviewv4.OpenLoop{},
			ProblemRootIDs: []string{}, ProblemNodes: []reviewv4.ProblemNode{}, ChainDependencies: []reviewv4.ChainDependency{},
			HumanPatches: []reviewv4.Patch{}, OrphanPatches: []reviewv4.Patch{}, GeneratedBaselines: []reviewv4.Baseline{},
		}
		ledger = reviewv4.MachineLedger{
			SchemaVersion: 4, MinimumReaderVersion: "0.4.1", MinimumWriterVersion: "0.4.1",
			HumanPatches: []reviewv4.Patch{}, OrphanPatches: []reviewv4.Patch{}, GeneratedBaselines: []reviewv4.Baseline{},
			PricingSnapshots: []pricing.Snapshot{}, CurrentPricingSnapshotIDs: []string{},
			SyncHashes: reviewv4.SyncHashes{ReviewSHA256: strings.Repeat("0", 64), HistorySHA256: strings.Repeat("0", 64), LedgerSHA256: strings.Repeat("0", 64), SessionIndexDigest: in.Index.Digest},
		}
	} else {
		body, err := json.Marshal(in.Accepted.Review)
		if err != nil {
			return reviewv4.Presentation{}, reviewv4.MachineLedger{}, err
		}
		presentation, err = reviewv4.DecodePresentation(body)
		if err != nil {
			return reviewv4.Presentation{}, reviewv4.MachineLedger{}, err
		}
		body, err = json.Marshal(in.Accepted.Ledger)
		if err != nil {
			return reviewv4.Presentation{}, reviewv4.MachineLedger{}, err
		}
		ledger, err = reviewv4.DecodeLedger(body)
		if err != nil {
			return reviewv4.Presentation{}, reviewv4.MachineLedger{}, err
		}
		if presentation.ProjectID != in.Index.ProjectID {
			return reviewv4.Presentation{}, reviewv4.MachineLedger{}, errors.New("v4 scan project identity mismatch")
		}
		if presentation.GenerationID != in.Index.GenerationID || presentation.ProjectViewDigest != in.Index.ProjectViewDigest {
			if presentation.Revision == in.Accepted.Ledger.DocumentProjection.PresentationBase.Revision {
				presentation.Revision++
			}
		}
		presentation.GenerationID, presentation.ProjectViewDigest = in.Index.GenerationID, in.Index.ProjectViewDigest
		for i := range presentation.Timeline {
			presentation.Timeline[i].GenerationID = in.Index.GenerationID
		}
	}
	ledger.ProjectID, ledger.GenerationID, ledger.ProjectViewDigest = in.Index.ProjectID, in.Index.GenerationID, in.Index.ProjectViewDigest
	ledger.AcceptedRevision = presentation.Revision
	ledger.Accounting = mapV4Accounting(in.Accounting)
	ledger.Sessions = make([]reviewv4.LedgerSession, 0, len(in.Index.Sessions))
	for _, entry := range in.Index.Sessions {
		ledger.Sessions = append(ledger.Sessions, reviewv4.LedgerSession{Provider: entry.Provider, SessionID: entry.SessionID, ProcessingState: reviewv4.ProcessingState(entry.ProcessingState), SourceAvailability: entry.SourceAvailability, SessionViewDigest: cloneV4String(entry.SessionViewDigest), UsageRecordDigest: cloneV4String(entry.UsageRecordDigest)})
	}
	ledger.HumanPatches = presentation.HumanPatches
	ledger.OrphanPatches = presentation.OrphanPatches
	ledger.GeneratedBaselines = presentation.GeneratedBaselines
	ledger.DocumentProjection = &reviewv4.DocumentProjection{SchemaVersion: 1, Format: "review-markdown-v1", PresentationBase: presentation}
	ledger.SyncHashes.SessionIndexDigest = in.Index.Digest
	return presentation, ledger, nil
}

func mapV4Accounting(value accounting.ProjectSummary) reviewv4.Accounting {
	result := reviewv4.Accounting{Models: make([]reviewv4.Model, 0, len(value.Models))}
	if value.TotalDurationMS > 0 {
		result.TotalDurationMS = uint64(value.TotalDurationMS)
	}
	if value.TotalTokens > 0 {
		result.TotalTokens = uint64(value.TotalTokens)
	}
	for _, model := range value.Models {
		tokens := uint64(0)
		if model.TotalTokens > 0 {
			tokens = uint64(model.TotalTokens)
		}
		result.Models = append(result.Models, reviewv4.Model{Model: model.Model, TotalTokens: tokens})
	}
	return result
}

func cloneV4String(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

type v4PublishInput struct {
	ProjectID          string
	DataRoot           string
	Mapping            config.ProjectMapping
	PreparedGeneration string
	Index              sessionindex.Document
	Accounting         accounting.ProjectSummary
	Now                func() time.Time
	Existing           bool
}

func publishV4Scan(ctx context.Context, in v4PublishInput) (_ publication.Result, retErr error) {
	pubOpts := publication.Options{ProjectID: in.ProjectID, PreparedGeneration: in.PreparedGeneration, Mapping: in.Mapping, DataRoot: in.DataRoot, Now: in.Now}
	if !in.Existing {
		p, ledger, err := mapV4Scan(v4MapInput{Index: in.Index, Accounting: in.Accounting})
		if err != nil {
			return publication.Result{}, err
		}
		plan, err := presentation.RenderV4(presentation.V4RenderInput{Presentation: p, Ledger: ledger, Index: in.Index, ExpectedFiles: map[string][]byte{}})
		if err != nil {
			return publication.Result{}, err
		}
		vaultExpected := make(map[string][]byte, len(plan.Files))
		for _, file := range plan.Files {
			vaultExpected[file.Relative] = nil
		}
		indexBody, err := sessionindex.Render(in.Index)
		if err != nil {
			return publication.Result{}, err
		}
		return publication.PublishMarkdownScan(ctx, pubOpts, syncproject.MarkdownSyncPlan{Plan: plan, Index: indexBody, ExpectedGenerationID: in.PreparedGeneration, ExpectedIndexDigest: in.Index.Digest, VaultExpected: vaultExpected})
	}

	owner, err := publicationlock.Acquire(in.DataRoot, in.ProjectID, 10*time.Second)
	if err != nil {
		return publication.Result{}, err
	}
	defer func() { retErr = errors.Join(retErr, owner.Release()) }()
	if err := publication.RecoverMarkdownLocked(ctx, pubOpts, owner); err != nil {
		return publication.Result{}, err
	}
	read, err := syncproject.ReadMarkdownForScan(ctx, syncproject.Options{ProjectID: in.ProjectID, CWD: in.Mapping.Root, DataDir: in.DataRoot, GOOS: runtime.GOOS, Now: in.Now, Trigger: syncengine.TriggerPeriodic}, owner)
	if err != nil {
		return publication.Result{}, err
	}
	pendingAccepted := read.OldAccepted
	pendingAccepted.Review = read.Pending.Presentation
	next, ledger, err := mapV4Scan(v4MapInput{Accepted: pendingAccepted, Index: in.Index, Accounting: in.Accounting})
	if err != nil {
		return publication.Result{}, err
	}
	plan, err := presentation.RenderV4(presentation.V4RenderInput{
		Presentation: next, Ledger: ledger, Index: in.Index,
		Previous: &read.AcceptedPair, Pending: &read.Pending.Documents,
		PreviousLedger: &read.OldAccepted.Ledger, ExpectedFiles: read.ProjectExpected,
	})
	if err != nil {
		return publication.Result{}, err
	}
	indexBody, err := sessionindex.Render(in.Index)
	if err != nil {
		return publication.Result{}, err
	}
	scanPlan := syncproject.MarkdownSyncPlan{
		Plan: plan, Index: indexBody, ExpectedGenerationID: in.PreparedGeneration,
		ExpectedIndexDigest: in.Index.Digest, VaultExpected: read.VaultExpected,
		ExpectedReceiptRevision: read.ExpectedReceiptRevision, ExpectedBaseDigest: read.ExpectedBaseDigest,
	}
	return publication.PublishMarkdownScanLocked(ctx, pubOpts, scanPlan, owner)
}
