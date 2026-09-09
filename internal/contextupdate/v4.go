package contextupdate

import (
	"bytes"
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
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/pricing"
	"github.com/neomei/SessionReviewer/internal/problemmap"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

type v4MapInput struct {
	Accepted   reviewv4.Accepted
	Index      sessionindex.Document
	Accounting accounting.ProjectSummary
	Milestones presentation.MilestoneProjection
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
		presentation.Timeline = cloneV4Timeline(in.Milestones.Timeline)
		presentation.ChainDependencies = cloneV4Dependencies(in.Milestones.ChainDependencies)
		if len(presentation.Timeline) != 0 {
			presentation.MinimumReaderVersion, presentation.MinimumWriterVersion = "0.4.3", "0.4.3"
			seedV4MilestoneBaselines(&presentation)
		}
		ledger = reviewv4.MachineLedger{
			SchemaVersion: 4, MinimumReaderVersion: "0.4.1", MinimumWriterVersion: "0.4.1",
			HumanPatches: []reviewv4.Patch{}, OrphanPatches: []reviewv4.Patch{}, GeneratedBaselines: []reviewv4.Baseline{},
			PricingSnapshots: []pricing.Snapshot{}, CurrentPricingSnapshotIDs: []string{},
			SyncHashes: reviewv4.SyncHashes{ReviewSHA256: strings.Repeat("0", 64), HistorySHA256: strings.Repeat("0", 64), LedgerSHA256: strings.Repeat("0", 64), SessionIndexDigest: in.Index.Digest},
		}
		if len(presentation.Timeline) != 0 {
			ledger.MinimumReaderVersion, ledger.MinimumWriterVersion = "0.4.3", "0.4.3"
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

func scanMilestoneUpdate(index sessionindex.Document, projected presentation.MilestoneProjection) reviewv4.ScanMilestoneUpdate {
	return reviewv4.ScanMilestoneUpdate{ProjectID: index.ProjectID, GenerationID: index.GenerationID, ProjectViewDigest: index.ProjectViewDigest, Timeline: cloneV4Timeline(projected.Timeline), ChainDependencies: cloneV4Dependencies(projected.ChainDependencies)}
}

func cloneV4Timeline(values []reviewv4.Timeline) []reviewv4.Timeline {
	result := slices.Clone(values)
	if result == nil {
		result = []reviewv4.Timeline{}
	}
	for index := range result {
		result[index].DecisionIDs = slices.Clone(result[index].DecisionIDs)
		loop := &result[index].ClosedLoop
		loop.SourceTurnRefs = slices.Clone(loop.SourceTurnRefs)
		cloneV4Segment := func(segment *reviewv4.ClosedLoopSegment) {
			segment.SourceTurnRefs = slices.Clone(segment.SourceTurnRefs)
			if segment.MissingReason != nil {
				copy := *segment.MissingReason
				segment.MissingReason = &copy
			}
		}
		cloneV4Segment(&loop.TriggerQuestion)
		cloneV4Segment(&loop.Execution)
		cloneV4Segment(&loop.Verification)
		cloneV4Segment(&loop.ImpactAndFollowUp)
		loop.Conclusion.SourceTurnRefs = slices.Clone(loop.Conclusion.SourceTurnRefs)
		if loop.Conclusion.MissingReason != nil {
			copy := *loop.Conclusion.MissingReason
			loop.Conclusion.MissingReason = &copy
		}
	}
	return result
}

func cloneV4Dependencies(values []reviewv4.ChainDependency) []reviewv4.ChainDependency {
	result := slices.Clone(values)
	if result == nil {
		result = []reviewv4.ChainDependency{}
	}
	for index := range result {
		result[index].TurnUnitIDs = slices.Clone(result[index].TurnUnitIDs)
	}
	return result
}

func seedV4MilestoneBaselines(value *reviewv4.Presentation) {
	for _, milestone := range value.Timeline {
		reviewv4.AddScanMilestoneBaselines(value, milestone)
	}
	sort.Slice(value.GeneratedBaselines, func(i, j int) bool {
		if value.GeneratedBaselines[i].EntityID != value.GeneratedBaselines[j].EntityID {
			return value.GeneratedBaselines[i].EntityID < value.GeneratedBaselines[j].EntityID
		}
		return value.GeneratedBaselines[i].Field < value.GeneratedBaselines[j].Field
	})
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
	PricingRequests    []pricing.ResolutionRequest
	ProjectID          string
	DataRoot           string
	Mapping            config.ProjectMapping
	PreparedGeneration string
	Index              sessionindex.Document
	Accounting         accounting.ProjectSummary
	Milestones         presentation.MilestoneProjection
	Store              *memorystore.Store
	Manifest           memory.GenerationManifest
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
		p, ledger, err := mapV4Scan(v4MapInput{Index: in.Index, Accounting: in.Accounting, Milestones: in.Milestones})
		if err != nil {
			return publication.Result{}, err
		}
		if in.PricingRequests != nil {
			if err := applyScanPricing(ctx, &ledger, in.PricingRequests, in.Now()); err != nil {
				return publication.Result{}, err
			}
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
		result, err := publication.PublishMarkdownScan(ctx, pubOpts, syncproject.MarkdownSyncPlan{Plan: plan, Index: indexBody, ExpectedGenerationID: in.PreparedGeneration, ExpectedIndexDigest: in.Index.Digest, VaultExpected: vaultExpected})
		if err == nil {
			err = problemmap.ReconcileManifestCandidates(ctx, in.DataRoot, in.ProjectID, in.Store, in.Manifest, p.ProblemNodes, in.Now())
		}
		return result, err
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
	_, ledger, err := mapV4Scan(v4MapInput{Accepted: pendingAccepted, Index: in.Index, Accounting: in.Accounting})
	if err != nil {
		return publication.Result{}, err
	}
	if in.PricingRequests != nil {
		if err := applyScanPricing(ctx, &ledger, in.PricingRequests, in.Now()); err != nil {
			return publication.Result{}, err
		}
	}
	update := scanMilestoneUpdate(in.Index, in.Milestones)
	if result, unchanged, err := unchangedV4ScanPublication(ctx, in, read, update, ledger, owner); err != nil {
		return publication.Result{}, err
	} else if unchanged {
		if err := problemmap.ReconcileManifestCandidates(ctx, in.DataRoot, in.ProjectID, in.Store, in.Manifest, read.OldAccepted.Review.ProblemNodes, in.Now()); err != nil {
			return publication.Result{}, err
		}
		return result, nil
	}
	next, err := reviewv4.RebaseMarkdownMilestones(read.OldAccepted.Ledger, read.Pending.Documents, update)
	if err != nil {
		return publication.Result{}, err
	}
	plan, err := presentation.RenderV4(presentation.V4RenderInput{
		Presentation: next, MilestoneUpdate: &update, Ledger: ledger, Index: in.Index,
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
	result, err := publication.PublishMarkdownScanLocked(ctx, pubOpts, scanPlan, owner)
	if err == nil {
		err = problemmap.ReconcileManifestCandidates(ctx, in.DataRoot, in.ProjectID, in.Store, in.Manifest, next.ProblemNodes, in.Now())
	}
	return result, err
}

func unchangedV4ScanPublication(ctx context.Context, in v4PublishInput, read syncproject.MarkdownScanRead, update reviewv4.ScanMilestoneUpdate, nextLedger reviewv4.MachineLedger, owner *publicationlock.Owner) (publication.Result, bool, error) {
	old := read.OldAccepted
	if old.Review.ProjectViewDigest != update.ProjectViewDigest || !bytes.Equal(read.Pending.Documents.Review, read.AcceptedPair.Review) || !bytes.Equal(read.Pending.Documents.History, read.AcceptedPair.History) {
		return publication.Result{}, false, nil
	}
	_, publishedManifest, err := in.Store.LoadPublishedContext(ctx)
	if err != nil {
		return publication.Result{}, false, err
	}
	if !sameV4ConversationGraph(in.Manifest, publishedManifest) {
		return publication.Result{}, false, nil
	}
	normalized := update
	normalized.GenerationID, normalized.ProjectViewDigest = old.Review.GenerationID, old.Review.ProjectViewDigest
	normalized.Timeline = cloneV4Timeline(update.Timeline)
	for index := range normalized.Timeline {
		normalized.Timeline[index].GenerationID = normalized.GenerationID
	}
	rebased, err := reviewv4.RebaseMarkdownMilestones(old.Ledger, read.Pending.Documents, normalized)
	if err != nil {
		return publication.Result{}, false, err
	}
	rebasedBody, err := json.Marshal(rebased)
	if err != nil {
		return publication.Result{}, false, err
	}
	oldBody, err := json.Marshal(old.Review)
	if err != nil || !bytes.Equal(rebasedBody, oldBody) {
		return publication.Result{}, false, err
	}
	nextLedger.ProjectID, nextLedger.GenerationID, nextLedger.ProjectViewDigest = old.Ledger.ProjectID, old.Ledger.GenerationID, old.Ledger.ProjectViewDigest
	nextLedger.AcceptedRevision = old.Ledger.AcceptedRevision
	nextLedger.HumanPatches = old.Ledger.HumanPatches
	nextLedger.OrphanPatches = old.Ledger.OrphanPatches
	nextLedger.GeneratedBaselines = old.Ledger.GeneratedBaselines
	nextLedger.DocumentProjection = old.Ledger.DocumentProjection
	nextLedger.SyncHashes = old.Ledger.SyncHashes
	nextLedgerBody, marshalErr := json.Marshal(nextLedger)
	if marshalErr != nil {
		return publication.Result{}, false, marshalErr
	}
	oldLedgerSemantic, marshalErr := json.Marshal(old.Ledger)
	if marshalErr != nil || !bytes.Equal(nextLedgerBody, oldLedgerSemantic) {
		return publication.Result{}, false, marshalErr
	}
	normalizedIndex := in.Index
	normalizedIndex.Sessions = slices.Clone(in.Index.Sessions)
	for index := range normalizedIndex.Sessions {
		normalizedIndex.Sessions[index].StateReasonCodes = slices.Clone(in.Index.Sessions[index].StateReasonCodes)
		normalizedIndex.Sessions[index].SessionViewDigest = cloneV4String(in.Index.Sessions[index].SessionViewDigest)
		normalizedIndex.Sessions[index].UsageRecordDigest = cloneV4String(in.Index.Sessions[index].UsageRecordDigest)
		normalizedIndex.Sessions[index].SummaryDigest = cloneV4String(in.Index.Sessions[index].SummaryDigest)
		normalizedIndex.Sessions[index].LastSeenGenerationID = cloneV4String(in.Index.Sessions[index].LastSeenGenerationID)
		normalizedIndex.Sessions[index].LastSuccessfulGenerationID = cloneV4String(in.Index.Sessions[index].LastSuccessfulGenerationID)
	}
	normalizedIndex.Digest, normalizedIndex.GenerationID, normalizedIndex.ProjectViewDigest, normalizedIndex.GeneratedAt = old.SessionIndex.Digest, old.SessionIndex.GenerationID, old.SessionIndex.ProjectViewDigest, old.SessionIndex.GeneratedAt
	oldEntries := make(map[string]sessionindex.Entry, len(old.SessionIndex.Sessions))
	for _, entry := range old.SessionIndex.Sessions {
		oldEntries[entry.Provider+"\x00"+entry.SessionID] = entry
	}
	for index := range normalizedIndex.Sessions {
		if prior, exists := oldEntries[normalizedIndex.Sessions[index].Provider+"\x00"+normalizedIndex.Sessions[index].SessionID]; exists {
			normalizedIndex.Sessions[index].LastSeenGenerationID = prior.LastSeenGenerationID
			normalizedIndex.Sessions[index].LastSuccessfulGenerationID = prior.LastSuccessfulGenerationID
		}
	}
	normalizedIndexBody, marshalErr := json.Marshal(normalizedIndex)
	if marshalErr != nil {
		return publication.Result{}, false, marshalErr
	}
	oldIndexSemantic, marshalErr := json.Marshal(old.SessionIndex)
	if marshalErr != nil || !bytes.Equal(normalizedIndexBody, oldIndexSemantic) {
		return publication.Result{}, false, marshalErr
	}
	paths := []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath, presentation.SessionIndexRelativePath}
	for _, relative := range paths {
		if !bytes.Equal(read.ProjectExpected[relative], read.VaultExpected[relative]) {
			return publication.Result{}, false, nil
		}
	}
	if !bytes.Equal(read.ProjectExpected[reviewv2.ReviewRelativePath], read.AcceptedPair.Review) || !bytes.Equal(read.ProjectExpected[reviewv2.HistoryRelativePath], read.AcceptedPair.History) {
		return publication.Result{}, false, nil
	}
	ledger, err := reviewv4.DecodeLedger(read.ProjectExpected[reviewv2.MachineLedgerRelativePath])
	if err != nil {
		return publication.Result{}, false, err
	}
	index, err := sessionindex.Parse(read.ProjectExpected[presentation.SessionIndexRelativePath])
	if err != nil {
		return publication.Result{}, false, err
	}
	ledgerBody, err := json.Marshal(ledger)
	if err != nil {
		return publication.Result{}, false, err
	}
	oldLedgerBody, err := json.Marshal(old.Ledger)
	if err != nil {
		return publication.Result{}, false, err
	}
	indexBody, err := json.Marshal(index)
	if err != nil {
		return publication.Result{}, false, err
	}
	oldIndexBody, err := json.Marshal(old.SessionIndex)
	if err != nil {
		return publication.Result{}, false, err
	}
	if !bytes.Equal(ledgerBody, oldLedgerBody) || !bytes.Equal(indexBody, oldIndexBody) {
		return publication.Result{}, false, nil
	}
	if err := publication.VerifyPrivateChainBindings(ctx, in.Store, in.Manifest, old); err != nil {
		return publication.Result{}, false, err
	}
	files := make([]presentation.FilePlan, 0, len(paths))
	for _, relative := range paths {
		files = append(files, presentation.FilePlan{Relative: relative, Expected: bytes.Clone(read.ProjectExpected[relative]), ExpectedExists: true, Desired: bytes.Clone(read.ProjectExpected[relative])})
	}
	noOpIndexBody := bytes.Clone(read.ProjectExpected[presentation.SessionIndexRelativePath])
	plan := syncproject.MarkdownSyncPlan{
		Plan:  presentation.RenderPlan{ProjectID: in.ProjectID, GenerationID: old.Review.GenerationID, ProjectViewDigest: old.Review.ProjectViewDigest, Files: files},
		Index: noOpIndexBody, ExpectedGenerationID: old.Review.GenerationID, ExpectedIndexDigest: old.SessionIndex.Digest,
		VaultExpected: read.VaultExpected, ExpectedReceiptRevision: read.ExpectedReceiptRevision, ExpectedBaseDigest: read.ExpectedBaseDigest,
	}
	result, err := publication.VerifyMarkdownScanNoOpLocked(ctx, publication.Options{ProjectID: in.ProjectID, Mapping: in.Mapping, DataRoot: in.DataRoot, Now: in.Now}, plan, owner)
	return result, true, err
}

func sameV4ConversationGraph(left, right memory.GenerationManifest) bool {
	return sameV4ConversationDependencies(left.ConversationChains, right.ConversationChains) &&
		sameV4ConversationDependencies(left.RetainedConversationChains, right.RetainedConversationChains)
}

func sameV4ConversationDependencies(left, right []memory.ConversationChainDependency) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[memory.ConversationChainDependency]int, len(left))
	for _, dependency := range left {
		counts[dependency]++
	}
	for _, dependency := range right {
		if counts[dependency] == 0 {
			return false
		}
		counts[dependency]--
	}
	return true
}
