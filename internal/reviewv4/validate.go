package reviewv4

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/neomei/SessionReviewer/internal/pricing"
)

var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var shaRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

const maxWireInteger int64 = 1<<53 - 1

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
	if p.SchemaVersion != 4 || p.MinimumReaderVersion != "0.4.0" || p.MinimumWriterVersion != "0.4.0" || !validID(p.ProjectID) || !validID(p.GenerationID) || !digestRE.MatchString(p.ProjectViewDigest) || p.Revision < 0 || int64(p.Revision) > maxWireInteger {
		return errors.New("invalid review presentation metadata")
	}
	for _, value := range []string{p.CurrentState.Goal, p.CurrentState.Stage, p.CurrentState.Status, p.CurrentState.NextAction, p.CurrentState.LastVerification} {
		if !text(value, 16384) {
			return errors.New("current state text exceeds limit")
		}
	}
	if len(p.Timeline) > 65536 || len(p.Decisions) > 65536 || len(p.Risks) > 65536 || len(p.OpenLoops) > 65536 || len(p.ProblemRootIDs) > 65536 || len(p.ProblemNodes) > 65536 || len(p.ChainDependencies) > 65536 || len(p.HumanPatches) > 65536 || len(p.OrphanPatches) > 65536 || len(p.GeneratedBaselines) > 65536 {
		return errors.New("review presentation exceeds array limit")
	}
	chainTurns, err := validateChainDependencies(p.ChainDependencies)
	if err != nil {
		return err
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
		if err := validateClosedLoop(timeline.ClosedLoop, chainTurns); err != nil {
			return fmt.Errorf("timeline %q closed loop: %w", timeline.ID, err)
		}
	}
	decisions := map[string]Decision{}
	for i, decision := range p.Decisions {
		if !validID(decision.ID) || len(decision.OccurredAt) > 128 || !text(decision.Title, 16384) || !text(decision.Rationale, 16384) || !text(decision.Impact, 16384) || !optionalText(decision.LegacyStatusText, 16384) || !text(decision.ReevaluateWhen, 16384) || decision.Revision < 1 || int64(decision.Revision) > maxWireInteger || len(decision.Supersedes) > 256 || len(decision.MilestoneIDs) > 256 || len(decision.SessionRefs) > 256 {
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
			if decision.LegacyStatusText != nil {
				return errors.New("native decision status cannot carry legacy status text")
			}
		case DecisionLegacyUnmapped:
			if decision.LegacyStatusText == nil || decision.Provenance != "migrated" {
				return errors.New("legacy unmapped decision requires migrated provenance and exact status text")
			}
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
	if p.ProblemMapRevision < 0 || int64(p.ProblemMapRevision) > maxWireInteger || (len(p.ProblemNodes) > 0 && p.ProblemMapRevision < 1) {
		return errors.New("invalid problem map revision")
	}
	if err := ValidateProblemGraph(p.ProblemNodes); err != nil {
		return err
	}
	if err := validateProblemRoots(p.ProblemNodes, p.ProblemRootIDs); err != nil {
		return err
	}
	for _, node := range p.ProblemNodes {
		if err := validateSourceTurnRefs(node.SourceTurnRefs, chainTurns); err != nil {
			return fmt.Errorf("problem %q: %w", node.ID, err)
		}
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

func ValidateConclusion(conclusion ClosedLoopConclusion) error {
	if len(conclusion.SourceTurnRefs) > 256 || !text(conclusion.Text, 16384) || !validMissingReason(conclusion.MissingReason) {
		return errors.New("invalid conclusion")
	}
	switch conclusion.Kind {
	case ConclusionMissing:
		if conclusion.Text != "" || conclusion.MissingReason == nil {
			return errors.New("missing conclusion must have empty text and a typed reason")
		}
	case ConclusionVisibleAnswerExcerpt:
		if strings.TrimSpace(conclusion.Text) == "" || len([]byte(conclusion.Text)) > 4096 || conclusion.MissingReason != nil {
			return errors.New("visible answer conclusion must contain a bounded excerpt")
		}
	case ConclusionHumanConfirmed, ConclusionAICandidateConfirmed:
		if strings.TrimSpace(conclusion.Text) == "" || conclusion.MissingReason != nil {
			return errors.New("confirmed conclusion text is required")
		}
	default:
		return errors.New("invalid conclusion kind")
	}
	return nil
}

func validateClosedLoop(loop ClosedLoop, chainTurns map[string]bool) error {
	if err := validateSegment(loop.TriggerQuestion); err != nil {
		return fmt.Errorf("trigger question: %w", err)
	}
	if err := ValidateConclusion(loop.Conclusion); err != nil {
		return err
	}
	for name, segment := range map[string]ClosedLoopSegment{"execution": loop.Execution, "verification": loop.Verification, "impact and follow-up": loop.ImpactAndFollowUp} {
		if err := validateSegment(segment); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if len(loop.SourceTurnRefs) > 256 || loop.Coverage.SourceTurns > uint64(maxWireInteger) || loop.Coverage.CapturedTurns > uint64(maxWireInteger) || loop.Coverage.TruncatedTurns > uint64(maxWireInteger) || loop.Coverage.SourceUnavailableTurns > uint64(maxWireInteger) || loop.Coverage.CapturedTurns != uint64(len(loop.SourceTurnRefs)) || loop.Coverage.SourceTurns < loop.Coverage.CapturedTurns || loop.Coverage.SourceUnavailableTurns > loop.Coverage.SourceTurns || loop.Coverage.TruncatedTurns > loop.Coverage.SourceTurns-loop.Coverage.SourceUnavailableTurns {
		return errors.New("closed-loop coverage does not reconcile")
	}
	if err := validateSourceTurnRefs(loop.SourceTurnRefs, chainTurns); err != nil {
		return err
	}
	top := map[string]bool{}
	for _, ref := range loop.SourceTurnRefs {
		top[sourceTurnKey(ref)] = true
	}
	groups := [][]SourceTurnRef{loop.TriggerQuestion.SourceTurnRefs, loop.Conclusion.SourceTurnRefs, loop.Execution.SourceTurnRefs, loop.Verification.SourceTurnRefs, loop.ImpactAndFollowUp.SourceTurnRefs}
	for _, refs := range groups {
		if err := validateSourceTurnRefs(refs, chainTurns); err != nil {
			return err
		}
		for _, ref := range refs {
			if !top[sourceTurnKey(ref)] {
				return errors.New("closed-loop segment references a turn absent from aggregate references")
			}
		}
	}
	return nil
}

func validateSegment(segment ClosedLoopSegment) error {
	if len(segment.SourceTurnRefs) > 256 || !text(segment.Text, 16384) || !validMissingReason(segment.MissingReason) {
		return errors.New("invalid closed-loop segment")
	}
	switch segment.State {
	case "missing":
		if segment.Text != "" || segment.MissingReason == nil {
			return errors.New("missing segment must have empty text and a typed reason")
		}
	case "present", "partial":
		if strings.TrimSpace(segment.Text) == "" || segment.MissingReason != nil {
			return errors.New("present or partial segment must have text and no missing reason")
		}
	default:
		return errors.New("invalid closed-loop segment state")
	}
	return nil
}

func validMissingReason(reason *string) bool {
	if reason == nil {
		return true
	}
	switch *reason {
	case "not_captured", "no_visible_answer", "no_execution_evidence", "not_verified", "source_unavailable", "partial_coverage":
		return true
	default:
		return false
	}
}

func NeutralClosedLoop() ClosedLoop {
	reason := "not_captured"
	segment := func() ClosedLoopSegment {
		value := reason
		return ClosedLoopSegment{State: "missing", Text: "", MissingReason: &value, SourceTurnRefs: []SourceTurnRef{}}
	}
	conclusionReason := reason
	return ClosedLoop{
		TriggerQuestion: segment(),
		Conclusion:      ClosedLoopConclusion{Kind: ConclusionMissing, Text: "", MissingReason: &conclusionReason, SourceTurnRefs: []SourceTurnRef{}},
		Execution:       segment(), Verification: segment(), ImpactAndFollowUp: segment(),
		SourceTurnRefs: []SourceTurnRef{}, Coverage: ClosedLoopCoverage{},
	}
}

func validateChainDependencies(dependencies []ChainDependency) (map[string]bool, error) {
	turns := map[string]bool{}
	seen := map[string]bool{}
	for _, dependency := range dependencies {
		key := dependency.Provider + "\x00" + dependency.SessionID
		if !validID(dependency.Provider) || !validID(dependency.SessionID) || !digestRE.MatchString(dependency.SessionViewDigest) || !digestRE.MatchString(dependency.DependencyDigest) || len(dependency.TurnUnitIDs) > 65536 || seen[key] {
			return nil, errors.New("invalid or duplicate chain dependency")
		}
		seen[key] = true
		local := map[string]bool{}
		for _, turnID := range dependency.TurnUnitIDs {
			if !validID(turnID) || local[turnID] {
				return nil, errors.New("invalid or duplicate chain turn identity")
			}
			local[turnID] = true
			turns[dependency.Provider+"\x00"+dependency.SessionID+"\x00"+turnID] = true
		}
	}
	return turns, nil
}

func validateSourceTurnRefs(refs []SourceTurnRef, available map[string]bool) error {
	seen := map[string]bool{}
	for _, ref := range refs {
		key := sourceTurnKey(ref)
		if !validID(ref.Provider) || !validID(ref.SessionID) || !validID(ref.TurnUnitID) || seen[key] {
			return errors.New("invalid or duplicate source turn reference")
		}
		seen[key] = true
		if !available[key] {
			return errors.New("source turn reference is absent from retained chain dependencies")
		}
	}
	return nil
}

func sourceTurnKey(ref SourceTurnRef) string {
	return ref.Provider + "\x00" + ref.SessionID + "\x00" + ref.TurnUnitID
}

func ValidateProblemGraph(nodes []ProblemNode) error {
	byID := make(map[string]ProblemNode, len(nodes))
	for _, node := range nodes {
		if !validID(node.ID) || node.Question == "" || !text(node.Question, 4096) || node.SiblingOrder < 0 || int64(node.SiblingOrder) > maxWireInteger || node.Revision < 1 || int64(node.Revision) > maxWireInteger || !text(node.CompletionCriterion, 16384) || !text(node.CurrentConclusion, 16384) || node.FirstProposedAt == "" || !text(node.FirstProposedAt, 128) || !optionalText(node.ConfirmedAt, 128) || len(node.RelatedNodeIDs) > 2 || len(node.SourceTurnRefs) > 256 {
			return fmt.Errorf("invalid problem node %q", node.ID)
		}
		if _, exists := byID[node.ID]; exists {
			return fmt.Errorf("duplicate problem node %q", node.ID)
		}
		switch node.WorkflowState {
		case "not_started", "in_progress", "paused", "resolved":
		default:
			return fmt.Errorf("invalid problem workflow state %q", node.WorkflowState)
		}
		switch node.AnswerState {
		case "no_answer", "answered_unverified", "execution_verified":
		default:
			return fmt.Errorf("invalid problem answer state %q", node.AnswerState)
		}
		switch node.Provenance {
		case "human_created", "migrated", "candidate_confirmed":
		default:
			return fmt.Errorf("invalid problem provenance %q", node.Provenance)
		}
		byID[node.ID] = node
	}
	siblingOrders := map[string]map[int]bool{}
	for _, node := range nodes {
		parentKey := "\x00root"
		if node.PrimaryParentID != nil {
			if *node.PrimaryParentID == node.ID {
				return errors.New("problem cannot parent itself")
			}
			if _, exists := byID[*node.PrimaryParentID]; !exists {
				return fmt.Errorf("problem %q has missing parent %q", node.ID, *node.PrimaryParentID)
			}
			parentKey = *node.PrimaryParentID
		}
		if siblingOrders[parentKey] == nil {
			siblingOrders[parentKey] = map[int]bool{}
		}
		if siblingOrders[parentKey][node.SiblingOrder] {
			return fmt.Errorf("duplicate sibling order %d under %q", node.SiblingOrder, parentKey)
		}
		siblingOrders[parentKey][node.SiblingOrder] = true
		related := map[string]bool{}
		for _, relation := range node.RelatedNodeIDs {
			if relation == node.ID || !validID(relation) || related[relation] {
				return fmt.Errorf("problem %q has invalid related node", node.ID)
			}
			if _, exists := byID[relation]; !exists {
				return fmt.Errorf("problem %q has missing related node %q", node.ID, relation)
			}
			related[relation] = true
		}
	}
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
		if parent := byID[id].PrimaryParentID; parent != nil && visit(*parent) {
			return true
		}
		state[id] = 2
		return false
	}
	for id := range byID {
		if visit(id) {
			return errors.New("problem graph contains cycle")
		}
	}
	return nil
}

func validateProblemRoots(nodes []ProblemNode, declared []string) error {
	if err := uniqueIDs(declared); err != nil {
		return fmt.Errorf("problem roots: %w", err)
	}
	actual := make([]ProblemNode, 0, len(declared))
	for _, node := range nodes {
		if node.PrimaryParentID == nil {
			actual = append(actual, node)
		}
	}
	if len(actual) != len(declared) {
		return errors.New("problem root declarations do not match null parents")
	}
	sort.Slice(actual, func(i, j int) bool {
		if actual[i].SiblingOrder != actual[j].SiblingOrder {
			return actual[i].SiblingOrder < actual[j].SiblingOrder
		}
		return actual[i].ID < actual[j].ID
	})
	for index, id := range declared {
		if actual[index].ID != id {
			return errors.New("problem root declarations do not match null parents")
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
	if l.SchemaVersion != 4 || l.MinimumReaderVersion != "0.4.0" || l.MinimumWriterVersion != "0.4.0" || !validID(l.ProjectID) || !validID(l.GenerationID) || !digestRE.MatchString(l.ProjectViewDigest) || l.AcceptedRevision < 0 || int64(l.AcceptedRevision) > maxWireInteger || !shaRE.MatchString(l.ReviewSHA256) || !shaRE.MatchString(l.HistorySHA256) {
		return errors.New("invalid machine ledger metadata")
	}
	if len(l.Sessions) > 65536 || len(l.HumanPatches) > 65536 || len(l.OrphanPatches) > 65536 || len(l.GeneratedBaselines) > 65536 || len(l.PricingSnapshots) > 65536 || len(l.CurrentPricingSnapshotIDs) > 65536 || len(l.Accounting.Models) > 256 {
		return errors.New("machine ledger exceeds array limit")
	}
	if l.Accounting.TotalDurationMS > uint64(maxWireInteger) || l.Accounting.TotalTokens > uint64(maxWireInteger) || !money(l.Accounting.TotalCostUSD) {
		return errors.New("invalid aggregate accounting")
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
		if !text(model.Model, 16384) || model.TotalTokens > uint64(maxWireInteger) || !money(model.TotalCostUSD) || modelNames[model.Model] {
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
	successorCounts, err := validatePricingSupersessionGraph(pricingByID)
	if err != nil {
		return err
	}
	currentByIdentity := map[string]string{}
	for _, id := range l.CurrentPricingSnapshotIDs {
		snapshot, exists := pricingByID[id]
		if !validID(id) || !exists || seenCurrent[id] || snapshot.Status == pricing.PriceSuperseded || successorCounts[id] != 0 {
			return errors.New("invalid current pricing snapshot reference")
		}
		seenCurrent[id] = true
		identity := pricingIdentity(snapshot)
		if _, exists := currentByIdentity[identity]; exists {
			return errors.New("multiple current pricing snapshots for one usage record")
		}
		currentByIdentity[identity] = id
		currentPricingIncomplete = currentPricingIncomplete || !snapshot.PricingComplete
	}
	for id, snapshot := range pricingByID {
		if selected, exists := currentByIdentity[pricingIdentity(snapshot)]; exists && successorCounts[id] == 0 && selected != id {
			return errors.New("multiple effective pricing leaves for one usage record")
		}
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

func pricingIdentity(snapshot pricing.Snapshot) string {
	return snapshot.Provider + "\x00" + snapshot.SessionID + "\x00" + snapshot.UsageRecordDigest
}

func validatePricingSupersessionGraph(byID map[string]pricing.Snapshot) (map[string]int, error) {
	successors := make(map[string]int, len(byID))
	for id, snapshot := range byID {
		if snapshot.SupersedesSnapshotID == nil {
			continue
		}
		predecessorID := *snapshot.SupersedesSnapshotID
		predecessor, exists := byID[predecessorID]
		if !exists {
			return nil, fmt.Errorf("pricing snapshot %q has missing predecessor %q", id, predecessorID)
		}
		if predecessorID == id {
			return nil, errors.New("pricing snapshot cannot supersede itself")
		}
		if pricingIdentity(predecessor) != pricingIdentity(snapshot) {
			return nil, errors.New("pricing predecessor and successor identity mismatch")
		}
		successors[predecessorID]++
		if successors[predecessorID] > 1 {
			return nil, errors.New("pricing supersession graph branches into multiple leaves")
		}
	}
	state := map[string]uint8{}
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return errors.New("pricing supersession graph contains cycle")
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		if predecessor := byID[id].SupersedesSnapshotID; predecessor != nil {
			if err := visit(*predecessor); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range byID {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	for id, snapshot := range byID {
		if (successors[id] > 0) != (snapshot.Status == pricing.PriceSuperseded) {
			return nil, errors.New("pricing supersession status does not match graph position")
		}
	}
	return successors, nil
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
