package contextupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/pricing"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
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
		if err := carryV4GeneratedBaselines(&presentation, in.Index.GenerationID); err != nil {
			return reviewv4.Presentation{}, reviewv4.MachineLedger{}, err
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
	ledger.Sessions = make([]reviewv4.LedgerSession, 0, len(in.Index.Sessions))
	for _, entry := range in.Index.Sessions {
		ledger.Sessions = append(ledger.Sessions, reviewv4.LedgerSession{Provider: entry.Provider, SessionID: entry.SessionID, ProcessingState: reviewv4.ProcessingState(entry.ProcessingState), SourceAvailability: entry.SourceAvailability, SessionViewDigest: cloneV4String(entry.SessionViewDigest), UsageRecordDigest: cloneV4String(entry.UsageRecordDigest)})
	}
	ledger.Accounting = mapV4Accounting(in.Accounting, in.Accepted.Ledger.Accounting, in.Accepted.Ledger.Sessions, ledger.Sessions)
	ledger.HumanPatches = presentation.HumanPatches
	ledger.OrphanPatches = presentation.OrphanPatches
	ledger.GeneratedBaselines = presentation.GeneratedBaselines
	ledger.DocumentProjection = &reviewv4.DocumentProjection{SchemaVersion: 1, Format: "review-markdown-v1", PresentationBase: presentation}
	ledger.SyncHashes.SessionIndexDigest = in.Index.Digest
	return presentation, ledger, nil
}

func carryV4GeneratedBaselines(presentation *reviewv4.Presentation, generationID string) error {
	if err := reviewv4.CarryMarkdownGeneratedBaselines(presentation, generationID); err != nil {
		return errors.Join(errors.New("v4 scan cannot carry generated baselines"), err)
	}
	return nil
}

func mapV4Accounting(value accounting.ProjectSummary, accepted reviewv4.Accounting, acceptedSessions, nextSessions []reviewv4.LedgerSession) reviewv4.Accounting {
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
	if sameV4UsageIdentity(acceptedSessions, nextSessions) && sameV4AccountingTokens(accepted, result) {
		result.TotalCostUSD = cloneV4Cost(accepted.TotalCostUSD)
		acceptedCosts := make(map[string]*float64, len(accepted.Models))
		for _, model := range accepted.Models {
			acceptedCosts[model.Model] = model.TotalCostUSD
		}
		for index := range result.Models {
			result.Models[index].TotalCostUSD = cloneV4Cost(acceptedCosts[result.Models[index].Model])
		}
	}
	return result
}

func sameV4UsageIdentity(left, right []reviewv4.LedgerSession) bool {
	if len(left) != len(right) {
		return false
	}
	identity := func(sessions []reviewv4.LedgerSession) []string {
		values := make([]string, 0, len(sessions))
		for _, session := range sessions {
			digest := "<null>"
			if session.UsageRecordDigest != nil {
				digest = *session.UsageRecordDigest
			}
			values = append(values, session.Provider+"\x00"+session.SessionID+"\x00"+digest)
		}
		sort.Strings(values)
		return values
	}
	return slices.Equal(identity(left), identity(right))
}

func sameV4AccountingTokens(left, right reviewv4.Accounting) bool {
	if left.TotalTokens != right.TotalTokens || len(left.Models) != len(right.Models) {
		return false
	}
	leftTokens := make(map[string]uint64, len(left.Models))
	for _, model := range left.Models {
		leftTokens[model.Model] = model.TotalTokens
	}
	for _, model := range right.Models {
		if tokens, found := leftTokens[model.Model]; !found || tokens != model.TotalTokens {
			return false
		}
	}
	return true
}

func cloneV4Cost(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneV4String(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func recoverActiveMarkdownBeforeScan(ctx context.Context, dataRoot, projectID string, mapping config.ProjectMapping, now func() time.Time) (retErr error) {
	info, err := os.Stat(filepath.Join(dataRoot, "publication-journal", projectID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return errors.Join(errors.New("inspect Markdown publication journal"), err)
	}
	reader, err := publicationstate.OpenReadOnly(dataRoot, projectID)
	if err != nil {
		return err
	}
	intent, intentErr := reader.Intent()
	closeErr := reader.Close()
	if errors.Is(intentErr, os.ErrNotExist) {
		return closeErr
	}
	if intentErr != nil || closeErr != nil {
		return errors.Join(intentErr, closeErr)
	}
	if intent.Stage == publicationstate.StageCommitted || intent.Version != 2 || intent.Kind != publicationstate.KindMarkdown {
		return nil
	}
	owner, err := publicationlock.Acquire(dataRoot, projectID, 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, owner.Release()) }()
	return publication.RecoverMarkdownLocked(ctx, publication.Options{ProjectID: projectID, Mapping: mapping, DataRoot: dataRoot, Now: now}, owner)
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
	AfterDestination   func(side, relative string) error
}

func publishV4Scan(ctx context.Context, in v4PublishInput) (_ publication.Result, retErr error) {
	if ctx == nil {
		return publication.Result{}, errors.New("v4 publication context is required")
	}
	if cause := context.Cause(ctx); cause != nil {
		return publication.Result{}, cause
	}
	pubOpts := publication.Options{ProjectID: in.ProjectID, PreparedGeneration: in.PreparedGeneration, Mapping: in.Mapping, DataRoot: in.DataRoot, Now: in.Now, AfterDestination: in.AfterDestination}
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
	if cause := context.Cause(ctx); cause != nil {
		return publication.Result{}, cause
	}
	read, err := syncproject.ReadMarkdownForScan(ctx, syncproject.Options{ProjectID: in.ProjectID, CWD: in.Mapping.Root, DataDir: in.DataRoot, GOOS: runtime.GOOS, Now: in.Now, Trigger: syncengine.TriggerPeriodic}, owner)
	if err != nil {
		return publication.Result{}, err
	}
	if cause := context.Cause(ctx); cause != nil {
		return publication.Result{}, cause
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
