package reviewv4

import (
	"errors"
	"fmt"
	"math"
	"regexp"

	"github.com/neomei/SessionReviewer/internal/pricing"
)

var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var shaRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validID(value string) bool                    { return len(value) <= 256 && idRE.MatchString(value) }
func text(value string, maximum int) bool          { return len(value) <= maximum }
func optionalText(value *string, maximum int) bool { return value == nil || text(*value, maximum) }
func optionalDigest(value *string) bool            { return value == nil || digestRE.MatchString(*value) }
func optionalTexts(values *[]string, maximumItems, maximumText int) bool {
	if values == nil {
		return true
	}
	if *values == nil || len(*values) > maximumItems {
		return false
	}
	for _, value := range *values {
		if !text(value, maximumText) {
			return false
		}
	}
	return true
}

func ValidatePresentation(p Presentation) error {
	if p.SchemaVersion != 4 || p.MinimumReaderVersion != "0.4.0" || p.MinimumWriterVersion != "0.4.0" || !validID(p.ProjectID) || !validID(p.GenerationID) || !digestRE.MatchString(p.ProjectViewDigest) || p.Revision < 0 {
		return errors.New("invalid review presentation metadata")
	}
	for _, value := range []string{p.CurrentState.Goal, p.CurrentState.Stage, p.CurrentState.Status, p.CurrentState.NextAction, p.CurrentState.LastVerification} {
		if !text(value, 16384) {
			return errors.New("current state text exceeds limit")
		}
	}
	if len(p.Timeline) > 65536 || len(p.Decisions) > 65536 || len(p.Risks) > 65536 || len(p.OpenLoops) > 65536 || len(p.HumanPatches) > 65536 || len(p.OrphanPatches) > 65536 || len(p.GeneratedBaselines) > 65536 {
		return errors.New("review presentation exceeds array limit")
	}
	timelineIDs := map[string]bool{}
	for i, timeline := range p.Timeline {
		if !validID(timeline.ID) || timeline.GenerationID != p.GenerationID || len(timeline.OccurredAt) > 128 || !validID(timeline.Kind) || !text(timeline.Title, 16384) || !text(timeline.Summary, 16384) || len(timeline.DecisionIDs) > 256 || timelineIDs[timeline.ID] {
			return fmt.Errorf("invalid or duplicate timeline %d", i)
		}
		timelineIDs[timeline.ID] = true
		if err := uniqueIDs(timeline.DecisionIDs); err != nil {
			return err
		}
	}
	decisions := map[string]Decision{}
	for i, decision := range p.Decisions {
		if !validID(decision.ID) || len(decision.OccurredAt) > 128 || !text(decision.Title, 16384) || !text(decision.Rationale, 16384) || !text(decision.Impact, 16384) || !text(decision.ReevaluateWhen, 16384) || decision.Revision < 1 || len(decision.Supersedes) > 256 || len(decision.MilestoneIDs) > 256 || len(decision.SessionRefs) > 256 {
			return fmt.Errorf("invalid decision %d", i)
		}
		if _, exists := decisions[decision.ID]; exists {
			return fmt.Errorf("duplicate decision %q", decision.ID)
		}
		switch decision.Kind {
		case "decision", "agreement":
		default:
			return errors.New("invalid decision kind")
		}
		switch decision.Status {
		case DecisionActive, DecisionSuperseded, DecisionArchived:
		default:
			return errors.New("invalid decision status")
		}
		switch decision.Provenance {
		case "human_created", "migrated", "ai_candidate_confirmed":
		default:
			return errors.New("invalid decision provenance")
		}
		if err := uniqueIDs(decision.Supersedes); err != nil {
			return err
		}
		if err := uniqueIDs(decision.MilestoneIDs); err != nil {
			return err
		}
		refs := map[SessionKey]bool{}
		for _, ref := range decision.SessionRefs {
			key := SessionKey{ref.Provider, ref.SessionID}
			if !validID(ref.Provider) || !validID(ref.SessionID) || refs[key] {
				return errors.New("invalid or duplicate decision session reference")
			}
			refs[key] = true
		}
		decisions[decision.ID] = decision
	}
	successors := map[string]int{}
	for _, decision := range p.Decisions {
		for _, target := range decision.Supersedes {
			if target == decision.ID {
				return errors.New("decision cannot supersede itself")
			}
			if _, exists := decisions[target]; !exists {
				return fmt.Errorf("decision %q supersedes missing %q", decision.ID, target)
			}
			successors[target]++
		}
	}
	if decisionCycle(decisions) {
		return errors.New("decision supersession graph contains cycle")
	}
	for id, decision := range decisions {
		if decision.Status == DecisionSuperseded && successors[id] == 0 {
			return fmt.Errorf("superseded decision %q has no successor", id)
		}
	}
	for _, timeline := range p.Timeline {
		for _, id := range timeline.DecisionIDs {
			if _, exists := decisions[id]; !exists {
				return fmt.Errorf("timeline references missing decision %q", id)
			}
		}
	}
	for _, decision := range p.Decisions {
		for _, id := range decision.MilestoneIDs {
			if !timelineIDs[id] {
				return fmt.Errorf("decision references missing milestone %q", id)
			}
		}
	}
	riskIDs := map[string]bool{}
	for _, risk := range p.Risks {
		if !validID(risk.ID) || !text(risk.Title, 16384) || !text(risk.Status, 16384) || !text(risk.Detail, 16384) {
			return errors.New("invalid risk")
		}
		if riskIDs[risk.ID] {
			return errors.New("duplicate risk")
		}
		riskIDs[risk.ID] = true
	}
	loopIDs := map[string]bool{}
	for _, loop := range p.OpenLoops {
		if !validID(loop.ID) || !text(loop.Title, 16384) || !text(loop.Status, 16384) || !text(loop.Question, 16384) || !text(loop.NextExperiment, 16384) || !text(loop.CompletionCriterion, 16384) {
			return errors.New("invalid open loop")
		}
		if loopIDs[loop.ID] {
			return errors.New("duplicate open loop")
		}
		loopIDs[loop.ID] = true
	}
	for _, patch := range append(append([]Patch{}, p.HumanPatches...), p.OrphanPatches...) {
		if err := validatePatch(patch); err != nil {
			return err
		}
	}
	for _, baseline := range p.GeneratedBaselines {
		if !validID(baseline.GenerationID) || !validID(baseline.EntityID) || !validID(baseline.Field) || !validID(baseline.Kind) || !shaRE.MatchString(baseline.GeneratedHash) || !optionalText(baseline.Value, 16384) || !optionalTexts(baseline.Values, 256, 16384) {
			return errors.New("invalid generated baseline")
		}
	}
	return nil
}

func uniqueIDs(values []string) error {
	seen := map[string]bool{}
	for _, value := range values {
		if !validID(value) || seen[value] {
			return errors.New("invalid or duplicate ID")
		}
		seen[value] = true
	}
	return nil
}

func validatePatch(patch Patch) error {
	if !validID(patch.EntityID) || !validID(patch.Field) || !shaRE.MatchString(patch.BaseGeneratedHash) || !optionalText(patch.Value, 16384) || !optionalTexts(patch.Values, 256, 16384) {
		return errors.New("invalid presentation patch")
	}
	switch patch.Operation {
	case "set", "suppress", "restore_default":
	default:
		return errors.New("invalid patch operation")
	}
	return nil
}

func decisionCycle(decisions map[string]Decision) bool {
	state := map[string]uint8{}
	var visit func(string) bool
	visit = func(id string) bool {
		if state[id] == 1 {
			return true
		}
		if state[id] == 2 {
			return false
		}
		state[id] = 1
		for _, next := range decisions[id].Supersedes {
			if visit(next) {
				return true
			}
		}
		state[id] = 2
		return false
	}
	for id := range decisions {
		if visit(id) {
			return true
		}
	}
	return false
}

func ValidateLedger(l MachineLedger) error {
	if l.SchemaVersion != 4 || l.MinimumReaderVersion != "0.4.0" || l.MinimumWriterVersion != "0.4.0" || !validID(l.ProjectID) || !validID(l.GenerationID) || !digestRE.MatchString(l.ProjectViewDigest) || l.AcceptedRevision < 0 || !shaRE.MatchString(l.ReviewSHA256) || !shaRE.MatchString(l.HistorySHA256) {
		return errors.New("invalid machine ledger metadata")
	}
	if len(l.Sessions) > 65536 || len(l.HumanPatches) > 65536 || len(l.OrphanPatches) > 65536 || len(l.GeneratedBaselines) > 65536 || len(l.PricingSnapshots) > 65536 || len(l.CurrentPricingSnapshotIDs) > 65536 || len(l.Accounting.Models) > 256 {
		return errors.New("machine ledger exceeds array limit")
	}
	if !money(l.Accounting.TotalCostUSD) {
		return errors.New("invalid aggregate cost")
	}
	pricingByID := map[string]pricing.Snapshot{}
	for _, snapshot := range l.PricingSnapshots {
		if err := pricing.ValidateSnapshot(snapshot); err != nil {
			return err
		}
		if snapshot.ProjectID != l.ProjectID {
			return errors.New("pricing snapshot project mismatch")
		}
		if _, exists := pricingByID[snapshot.SnapshotID]; exists {
			return errors.New("duplicate pricing snapshot")
		}
		pricingByID[snapshot.SnapshotID] = snapshot
	}
	modelNames := map[string]bool{}
	var modelTokens uint64
	modelCostsComplete := true
	modelCost := 0.0
	for _, model := range l.Accounting.Models {
		if !text(model.Model, 16384) || !money(model.TotalCostUSD) || modelNames[model.Model] {
			return errors.New("invalid or duplicate accounting model")
		}
		modelNames[model.Model] = true
		if ^uint64(0)-modelTokens < model.TotalTokens {
			return errors.New("accounting model tokens overflow")
		}
		modelTokens += model.TotalTokens
		if model.TotalCostUSD == nil {
			modelCostsComplete = false
		} else {
			modelCost += *model.TotalCostUSD
		}
	}
	if len(l.Accounting.Models) > 0 && modelTokens != l.Accounting.TotalTokens {
		return errors.New("accounting token total does not reconcile")
	}
	if len(l.Accounting.Models) > 0 && !modelCostsComplete && l.Accounting.TotalCostUSD != nil {
		return errors.New("aggregate price must be null when an included model cost is unknown")
	}
	if len(l.Accounting.Models) > 0 && modelCostsComplete && l.Accounting.TotalCostUSD != nil && !nearlyEqual(*l.Accounting.TotalCostUSD, modelCost) {
		return errors.New("accounting cost total does not reconcile")
	}
	keys := map[SessionKey]bool{}
	for _, session := range l.Sessions {
		key := SessionKey{session.Provider, session.SessionID}
		if !validID(session.Provider) || !validID(session.SessionID) || keys[key] || !optionalDigest(session.SessionViewDigest) || !optionalDigest(session.UsageRecordDigest) {
			return errors.New("invalid or duplicate ledger session")
		}
		keys[key] = true
		switch session.ProcessingState {
		case ProcessingComplete, ProcessingPartial, ProcessingError, ProcessingUnprocessed:
		default:
			return errors.New("invalid ledger processing state")
		}
		switch session.SourceAvailability {
		case "available", "unavailable":
		default:
			return errors.New("invalid ledger source availability")
		}
	}
	for _, patch := range append(append([]Patch{}, l.HumanPatches...), l.OrphanPatches...) {
		if err := validatePatch(patch); err != nil {
			return err
		}
	}
	for _, baseline := range l.GeneratedBaselines {
		if !validID(baseline.GenerationID) || !validID(baseline.EntityID) || !validID(baseline.Field) || !validID(baseline.Kind) || !shaRE.MatchString(baseline.GeneratedHash) || !optionalText(baseline.Value, 16384) || !optionalTexts(baseline.Values, 256, 16384) {
			return errors.New("invalid ledger baseline")
		}
	}
	seenCurrent := map[string]bool{}
	currentPricingIncomplete := false
	for _, id := range l.CurrentPricingSnapshotIDs {
		snapshot, exists := pricingByID[id]
		if !validID(id) || !exists || seenCurrent[id] || snapshot.Status == pricing.PriceSuperseded {
			return errors.New("invalid current pricing snapshot reference")
		}
		seenCurrent[id] = true
		currentPricingIncomplete = currentPricingIncomplete || !snapshot.PricingComplete
	}
	if currentPricingIncomplete && l.Accounting.TotalCostUSD != nil {
		return errors.New("aggregate price must be null when a current snapshot is incomplete")
	}
	if !shaRE.MatchString(l.SyncHashes.ReviewSHA256) || !shaRE.MatchString(l.SyncHashes.HistorySHA256) || !shaRE.MatchString(l.SyncHashes.LedgerSHA256) || !digestRE.MatchString(l.SyncHashes.SessionIndexDigest) {
		return errors.New("invalid synchronization hashes")
	}
	if l.SyncHashes.ReviewSHA256 != l.ReviewSHA256 || l.SyncHashes.HistorySHA256 != l.HistorySHA256 {
		return errors.New("top-level and synchronization hashes disagree")
	}
	return nil
}

func money(value *float64) bool {
	return value == nil || (!math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0)
}

func nearlyEqual(left, right float64) bool {
	delta := math.Abs(left - right)
	return delta <= 1e-12*math.Max(1, math.Max(math.Abs(left), math.Abs(right)))
}

func ValidateAccepted(a Accepted) error {
	if err := ValidatePresentation(a.Review); err != nil {
		return err
	}
	if err := ValidateLedger(a.Ledger); err != nil {
		return err
	}
	if a.Review.ProjectID != a.Ledger.ProjectID || a.Review.GenerationID != a.Ledger.GenerationID || a.Review.ProjectViewDigest != a.Ledger.ProjectViewDigest || a.Review.Revision != a.Ledger.AcceptedRevision {
		return errors.New("review and ledger identity, generation, digest, or revision mismatch")
	}
	return nil
}
