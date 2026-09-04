package migrationv4

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/neomei/SessionReviewer/internal/pricing"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func BuildPreview(input Input) (Result, error) {
	acceptedV3, err := reviewv2.LoadV3Bytes(input.Review, input.History, input.Ledger)
	if err != nil {
		return Result{}, err
	}
	result, err := migrate(acceptedV3, input.History, input.SessionIndex, input.GenerationID)
	if err != nil {
		return Result{}, err
	}
	preview := basePreview(acceptedV3, input.Review, input.History, input.Ledger)
	preview.GenerationID = result.Accepted.Review.GenerationID
	preview.SessionViewDependencyDigests = sortedUnique(input.SessionViewDependencyDigests)
	preview.TargetHashes = ArtifactHashes{Review: digest(result.Review), History: digest(result.History), Ledger: digest(result.Ledger), SessionIndex: digest(result.SessionIndex)}
	preimages := absentHashes()
	preimages.Review = preimageHash(input.TargetPreimages[ReviewRelativePath])
	preimages.History = preimageHash(input.TargetPreimages[HistoryRelativePath])
	preimages.Ledger = preimageHash(input.TargetPreimages[LedgerRelativePath])
	preimages.SessionIndex = preimageHash(input.TargetPreimages[SessionIndexRelativePath])
	preview.TargetPreimageHashes = preimages
	preview.PreviewDigest = MigrationPreviewDigest(preview)
	if err := validatePreview(preview); err != nil {
		return Result{}, err
	}
	result.Preview = preview
	result.TargetPreimages = clonePreimages(input.TargetPreimages)
	return result, nil
}

func MigrateAcceptedV3(review, history, ledger, sessionIndex []byte) (reviewv4.Accepted, error) {
	result, err := MigrateAcceptedV3Result(review, history, ledger, sessionIndex)
	return result.Accepted, err
}

func MigrateAcceptedV3Result(review, history, ledger, sessionIndex []byte) (Result, error) {
	accepted, err := reviewv2.LoadV3Bytes(review, history, ledger)
	if err != nil {
		return Result{}, err
	}
	return migrate(accepted, history, sessionIndex, "")
}

func migrate(source reviewv2.AcceptedV3, history, indexBytes []byte, selectedGeneration string) (Result, error) {
	index, err := sessionindex.Parse(indexBytes)
	if err != nil {
		return Result{}, fmt.Errorf("session index: %w", err)
	}
	machine := source.State.Machine
	generationID := machine.GenerationID
	if selectedGeneration != "" {
		generationID = selectedGeneration
	}
	projectDigest := "sha256:" + machine.ProjectViewDigest
	if index.ProjectID != machine.ProjectID || index.GenerationID != generationID || index.ProjectViewDigest != projectDigest {
		return Result{}, errors.New("session index project, generation, or ProjectView does not match v3 source")
	}
	indexBytes, err = sessionindex.Render(index)
	if err != nil {
		return Result{}, err
	}
	index, err = sessionindex.Parse(indexBytes)
	if err != nil {
		return Result{}, err
	}

	presentation, err := migratePresentation(source, generationID, projectDigest)
	if err != nil {
		return Result{}, err
	}
	reviewBytes, err := strictjson.Encode(presentation)
	if err != nil {
		return Result{}, err
	}
	if _, err := reviewv4.DecodePresentation(reviewBytes); err != nil {
		return Result{}, fmt.Errorf("render migrated review: %w", err)
	}

	ledger, err := migrateLedger(source, index, generationID, projectDigest, reviewBytes, history)
	if err != nil {
		return Result{}, err
	}
	ledgerBytes, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		return Result{}, fmt.Errorf("render migrated ledger: %w", err)
	}
	accepted, err := reviewv4.LoadProjection(reviewBytes, history, ledgerBytes, indexBytes)
	if err != nil {
		return Result{}, fmt.Errorf("validate migrated projection: %w", err)
	}
	return Result{Review: reviewBytes, History: append([]byte(nil), history...), Ledger: ledgerBytes, SessionIndex: indexBytes, Accepted: accepted}, nil
}

func migratePresentation(source reviewv2.AcceptedV3, generationID, projectDigest string) (reviewv4.Presentation, error) {
	state := source.State
	result := reviewv4.Presentation{
		SchemaVersion: 4, MinimumReaderVersion: "0.4.0", MinimumWriterVersion: "0.4.0",
		ProjectID: state.Review.ProjectID, GenerationID: generationID, ProjectViewDigest: projectDigest, Revision: state.Review.Revision,
		CurrentState: reviewv4.CurrentState{Goal: state.Review.Goal, Stage: state.Review.Stage, Status: state.Review.Status, NextAction: state.Review.NextAction, LastVerification: state.Review.LastVerification},
		Timeline:     []reviewv4.Timeline{}, Decisions: []reviewv4.Decision{}, Risks: []reviewv4.Risk{}, OpenLoops: []reviewv4.OpenLoop{},
		HumanPatches: migratePatches(state.Machine.HumanPatches), OrphanPatches: migratePatches(state.Machine.OrphanPatches), GeneratedBaselines: migrateBaselines(state.Machine.GeneratedBaselines, generationID),
	}
	for _, event := range state.Events {
		result.Timeline = append(result.Timeline, reviewv4.Timeline{ID: event.ID, GenerationID: generationID, OccurredAt: event.OccurredAt, Kind: event.Kind, Title: event.Title, Summary: event.Summary, DecisionIDs: append([]string{}, event.DecisionIDs...)})
	}
	for _, decision := range state.Review.Decisions {
		status, err := migrateDecisionStatus(decision.Status)
		if err != nil {
			return reviewv4.Presentation{}, fmt.Errorf("decision %q: %w", decision.ID, err)
		}
		result.Decisions = append(result.Decisions, reviewv4.Decision{
			ID: decision.ID, Kind: "decision", OccurredAt: decision.OccurredAt, Title: decision.Title, Rationale: decision.Rationale, Impact: decision.Impact, Status: status,
			ReevaluateWhen: "", Supersedes: []string{}, MilestoneIDs: []string{}, SessionRefs: []reviewv4.SessionRef{}, Provenance: "migrated", Pinned: false, Revision: 1,
		})
	}
	for _, risk := range state.Review.Risks {
		result.Risks = append(result.Risks, reviewv4.Risk{ID: risk.ID, Title: risk.Title, Status: risk.Status, Detail: risk.Detail})
	}
	for _, loop := range state.Machine.LegacyCompatibility.OpenLoops {
		result.OpenLoops = append(result.OpenLoops, reviewv4.OpenLoop{ID: loop.ID, Title: loop.Title, Status: loop.Status, Question: loop.Question, NextExperiment: loop.NextExperiment, CompletionCriterion: loop.CompletionCriterion})
	}
	if err := reviewv4.ValidatePresentation(result); err != nil {
		return reviewv4.Presentation{}, fmt.Errorf("migrated presentation: %w", err)
	}
	return result, nil
}

func migrateDecisionStatus(status string) (reviewv4.DecisionStatus, error) {
	switch status {
	case "", "active":
		return reviewv4.DecisionActive, nil
	case "archived":
		return reviewv4.DecisionArchived, nil
	case "superseded":
		return "", errors.New("superseded status cannot be represented without inventing a successor")
	default:
		return "", fmt.Errorf("legacy decision status %q has no exact v4 mapping", status)
	}
}

func migratePatches(values []reviewv2.HumanPatchWire) []reviewv4.Patch {
	result := make([]reviewv4.Patch, 0, len(values))
	for _, value := range values {
		patch := reviewv4.Patch{EntityID: value.EntityID, Field: value.Field, Operation: value.Operation, BaseGeneratedHash: value.BaseGeneratedHash}
		if value.Operation == "set" {
			if value.Values != nil {
				values := append([]string{}, value.Values...)
				patch.Values = &values
			} else {
				scalar := value.Value
				patch.Value = &scalar
			}
		}
		result = append(result, patch)
	}
	return result
}

func migrateBaselines(values []reviewv2.GeneratedBaselineWire, generationID string) []reviewv4.Baseline {
	result := make([]reviewv4.Baseline, 0, len(values))
	for _, value := range values {
		baseline := reviewv4.Baseline{GenerationID: generationID, EntityID: value.EntityID, Field: value.Field, Kind: value.Kind, GeneratedHash: value.GeneratedHash}
		if value.Kind == "list" {
			values := append([]string{}, value.Values...)
			baseline.Values = &values
		} else {
			scalar := value.Value
			baseline.Value = &scalar
		}
		result = append(result, baseline)
	}
	return result
}

func migrateLedger(source reviewv2.AcceptedV3, index sessionindex.Document, generationID, projectDigest string, reviewBytes, history []byte) (reviewv4.MachineLedger, error) {
	machine := source.State.Machine
	duration, tokens, err := nonnegative(machine.Accounting.TotalDurationMS, machine.Accounting.TotalTokens)
	if err != nil {
		return reviewv4.MachineLedger{}, err
	}
	models := make([]reviewv4.Model, 0, len(machine.Accounting.Models))
	for _, model := range machine.Accounting.Models {
		value, _, err := nonnegative(model.TotalTokens, 0)
		if err != nil {
			return reviewv4.MachineLedger{}, err
		}
		models = append(models, reviewv4.Model{Model: model.Model, TotalTokens: value, TotalCostUSD: nil})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Model < models[j].Model })
	sessions := make([]reviewv4.LedgerSession, 0, len(index.Sessions))
	indexBySession := make(map[string]sessionindex.Entry, len(index.Sessions))
	for _, entry := range index.Sessions {
		state := reviewv4.ProcessingState(entry.ProcessingState)
		sessions = append(sessions, reviewv4.LedgerSession{Provider: entry.Provider, SessionID: entry.SessionID, ProcessingState: state, SourceAvailability: entry.SourceAvailability, SessionViewDigest: cloneString(entry.SessionViewDigest), UsageRecordDigest: cloneString(entry.UsageRecordDigest)})
		indexBySession[entry.SessionID] = entry
	}
	pricingSnapshots := make([]pricing.Snapshot, 0)
	for _, session := range machine.Sessions {
		entry, exists := indexBySession[session.SessionID]
		if !exists || entry.UsageRecordDigest == nil || session.Accounting == nil {
			continue
		}
		for modelIndex, model := range session.Accounting.Models {
			uncachedInput := model.InputTokens - model.CachedInputTokens - model.CacheWriteInputTokens
			quantities := pricing.Quantities{
				Input: uint64(uncachedInput), CachedInput: uint64(model.CachedInputTokens), CacheWriteInput: uint64(model.CacheWriteInputTokens),
				Output: uint64(model.OutputTokens), ReasoningOutput: uint64(model.ReasoningOutputTokens),
			}
			missing := make([]string, 0, 5)
			for _, dimension := range []struct {
				name  string
				value uint64
			}{{"input", quantities.Input}, {"cached_input", quantities.CachedInput}, {"cache_write_input", quantities.CacheWriteInput}, {"output", quantities.Output}, {"reasoning_output", quantities.ReasoningOutput}} {
				if dimension.value > 0 {
					missing = append(missing, dimension.name)
				}
			}
			snapshotSeed := fmt.Sprintf("%s\x00%s\x00%s\x00%d", entry.Provider, entry.SessionID, model.Model, modelIndex)
			pricingSnapshots = append(pricingSnapshots, pricing.Snapshot{
				SchemaVersion: 1, MinimumReaderVersion: "0.4.0", SnapshotID: "legacy-" + strings.TrimPrefix(digest([]byte(snapshotSeed)), "sha256:")[:32],
				ProjectID: machine.ProjectID, Provider: entry.Provider, SessionID: entry.SessionID, UsageRecordDigest: *entry.UsageRecordDigest,
				BillingHost: "legacy", BilledModelID: model.Model, BillingMode: "legacy", BillingRuleVersion: "legacy-v3",
				PricedAt: session.Accounting.EndedAt, CreatedAt: session.Accounting.EndedAt, Status: pricing.PriceLegacyUnverified,
				SourceKind: "unresolved", Rates: pricing.Rates{}, BillableQuantities: quantities, LineCostsUSD: pricing.LineCosts{},
				MissingBillingDimensions: missing, KnownSubtotalUSD: 0, TotalCostUSD: nil, PricingComplete: false,
				AuditReason: "Migrated from v3; original price source and date are not independently verifiable.",
			})
		}
	}
	sort.Slice(pricingSnapshots, func(i, j int) bool { return pricingSnapshots[i].SnapshotID < pricingSnapshots[j].SnapshotID })
	ledger := reviewv4.MachineLedger{
		SchemaVersion: 4, MinimumReaderVersion: "0.4.0", MinimumWriterVersion: "0.4.0", ProjectID: machine.ProjectID, GenerationID: generationID, ProjectViewDigest: projectDigest,
		AcceptedRevision: machine.AcceptedRevision, ReviewSHA256: strings.TrimPrefix(digest(reviewBytes), "sha256:"), HistorySHA256: strings.TrimPrefix(digest(history), "sha256:"),
		Accounting: reviewv4.Accounting{TotalDurationMS: duration, TotalTokens: tokens, TotalCostUSD: nil, Models: models}, Sessions: sessions,
		HumanPatches: migratePatches(machine.HumanPatches), OrphanPatches: migratePatches(machine.OrphanPatches), GeneratedBaselines: migrateBaselines(machine.GeneratedBaselines, generationID),
		PricingSnapshots: pricingSnapshots, CurrentPricingSnapshotIDs: []string{},
	}
	ledger.SyncHashes = reviewv4.SyncHashes{ReviewSHA256: ledger.ReviewSHA256, HistorySHA256: ledger.HistorySHA256, LedgerSHA256: strings.Repeat("0", 64), SessionIndexDigest: index.Digest}
	return ledger, nil
}

func nonnegative(first, second int64) (uint64, uint64, error) {
	if first < 0 || second < 0 {
		return 0, 0, errors.New("legacy accounting contains a negative value")
	}
	return uint64(first), uint64(second), nil
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func clonePreimages(values map[string]Preimage) map[string]Preimage {
	result := make(map[string]Preimage, len(values))
	for relative, value := range values {
		value.Bytes = append([]byte(nil), value.Bytes...)
		result[relative] = value
	}
	return result
}
