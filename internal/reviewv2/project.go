package reviewv2

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/ledger"
)

// ProjectLegacy deterministically projects the public legacy model into the
// two human documents and the machine-owned ledger. It performs no I/O.
func ProjectLegacy(legacy ledger.State) (State, error) {
	if err := validateLegacyProjectionInput(legacy); err != nil {
		return State{}, fmt.Errorf("project legacy state: %w", err)
	}
	state := State{
		Review: Review{
			ProjectID:        legacy.ProjectID,
			Revision:         legacy.CurrentState.Revision,
			Name:             legacy.ProjectID,
			Goal:             legacy.CurrentState.Goal,
			Stage:            legacy.CurrentState.Branch,
			Status:           legacyProjectStatus(legacy.CurrentState),
			NextAction:       legacy.CurrentState.NextAction,
			LastVerification: legacy.CurrentState.LastVerified,
		},
		Machine: MachineLedger{
			SchemaVersion:       LegacySchemaVersion,
			ProjectID:           legacy.ProjectID,
			AcceptedRevision:    legacy.CurrentState.Revision,
			Evidence:            make(map[string][]ledger.EvidenceRef),
			LegacyCompatibility: legacyCompatibilityFromState(legacy),
		},
	}
	if state.Review.Revision < 1 {
		state.Review.Revision = maximumLegacyRevision(legacy)
		state.Machine.AcceptedRevision = state.Review.Revision
	}

	decisionTimes := make(map[string]string, len(legacy.Decisions))
	for _, event := range legacy.Timeline {
		for _, decisionID := range event.DecisionIDs {
			if laterLegacyTime(event.OccurredAt, decisionTimes[decisionID]) {
				decisionTimes[decisionID] = event.OccurredAt
			}
		}
	}
	decisionIDs := sortedMapKeys(legacy.Decisions)
	for _, id := range decisionIDs {
		value := legacy.Decisions[id]
		state.Review.Decisions = append(state.Review.Decisions, Decision{
			ID:         value.ID,
			OccurredAt: decisionTimes[id],
			Title:      value.Title,
			Rationale:  value.Rationale,
			Impact:     value.Consequences,
			Status:     value.Status,
		})
		appendMachineEvidence(state.Machine.Evidence, id, value.Evidence)
	}
	sort.Slice(state.Review.Decisions, func(left, right int) bool {
		if state.Review.Decisions[left].OccurredAt != state.Review.Decisions[right].OccurredAt {
			return laterLegacyTime(state.Review.Decisions[left].OccurredAt, state.Review.Decisions[right].OccurredAt)
		}
		return state.Review.Decisions[left].ID < state.Review.Decisions[right].ID
	})

	usedRiskIDs := make(map[string]struct{}, len(legacy.OpenLoops)+len(legacy.CurrentState.Blockers)+len(legacy.CurrentState.OpenRisks))
	loopIDs := sortedMapKeys(legacy.OpenLoops)
	for _, id := range loopIDs {
		value := legacy.OpenLoops[id]
		usedRiskIDs[id] = struct{}{}
		if !legacyLoopVisible(value.Status) {
			continue
		}
		state.Review.Risks = append(state.Review.Risks, Risk{
			ID:     value.ID,
			Title:  value.Title,
			Status: value.Status,
			Detail: openLoopDetail(value),
		})
		appendMachineEvidence(state.Machine.Evidence, id, value.Evidence)
	}
	for index, blocker := range legacy.CurrentState.Blockers {
		risk := currentStateRisk("blocker", "blocked", blocker, usedRiskIDs)
		state.Review.Risks = append(state.Review.Risks, risk)
		state.Machine.LegacyCompatibility.CurrentRisks = append(state.Machine.LegacyCompatibility.CurrentRisks, CurrentRiskProvenance{RiskID: risk.ID, Kind: "blocker", SourceKey: currentRiskSourceKey(risk.ID, "blocker", index, blocker)})
	}
	for index, risk := range legacy.CurrentState.OpenRisks {
		projected := currentStateRisk("risk", "open", risk, usedRiskIDs)
		state.Review.Risks = append(state.Review.Risks, projected)
		state.Machine.LegacyCompatibility.CurrentRisks = append(state.Machine.LegacyCompatibility.CurrentRisks, CurrentRiskProvenance{RiskID: projected.ID, Kind: "open_risk", SourceKey: currentRiskSourceKey(projected.ID, "open_risk", index, risk)})
	}
	sort.Slice(state.Review.Risks, func(left, right int) bool {
		if state.Review.Risks[left].Status != state.Review.Risks[right].Status {
			return state.Review.Risks[left].Status < state.Review.Risks[right].Status
		}
		return state.Review.Risks[left].ID < state.Review.Risks[right].ID
	})

	for _, id := range sortedMapKeys(legacy.Sessions) {
		report := cloneLegacySession(legacy.Sessions[id])
		state.Machine.Sessions = append(state.Machine.Sessions, report)
		appendMachineEvidence(state.Machine.Evidence, report.ID, report.Evidence)
	}
	sort.Slice(state.Machine.Sessions, func(left, right int) bool {
		if state.Machine.Sessions[left].SessionID != state.Machine.Sessions[right].SessionID {
			return state.Machine.Sessions[left].SessionID < state.Machine.Sessions[right].SessionID
		}
		return state.Machine.Sessions[left].ID < state.Machine.Sessions[right].ID
	})

	for _, source := range legacy.Timeline {
		event := projectLegacyEvent(source, legacy.Decisions, legacy.OpenLoops, legacy.CurrentState.NextAction)
		state.Events = append(state.Events, event)
		appendMachineEvidence(state.Machine.Evidence, event.ID, source.Evidence)
	}
	orderedEvents, err := canonicalHistoryEvents(state.Events)
	if err != nil {
		return State{}, err
	}
	state.Events = orderedEvents

	accountingInputs := make([]*accounting.SessionAccounting, 0, len(state.Machine.Sessions))
	for index := range state.Machine.Sessions {
		accountingInputs = append(accountingInputs, state.Machine.Sessions[index].Accounting)
	}
	state.Machine.Accounting, err = accounting.Aggregate(accountingInputs)
	if err != nil {
		return State{}, fmt.Errorf("aggregate project accounting: %w", err)
	}
	if err := accounting.ValidateProjectSummary(state.Machine.Accounting, accountingInputs); err != nil {
		return State{}, fmt.Errorf("validate project accounting: %w", err)
	}
	if err := setDocumentHashes(&state); err != nil {
		return State{}, err
	}
	if err := Validate(state); err != nil {
		return State{}, fmt.Errorf("project legacy state: %w", err)
	}
	return state, nil
}

type LegacyPresentation struct {
	Review              Review
	Events              []Event
	Compatibility       LegacyCompatibility
	HasMachineInternals bool
}

func SafeLegacyPresentation(state State) (LegacyPresentation, error) {
	if err := Validate(state); err != nil {
		return LegacyPresentation{}, err
	}
	events := append([]Event(nil), state.Events...)
	for index := range events {
		events[index].Changes = append([]string(nil), events[index].Changes...)
		events[index].Results = append([]string(nil), events[index].Results...)
		events[index].DecisionIDs = append([]string(nil), events[index].DecisionIDs...)
	}
	review := state.Review
	review.Risks = append([]Risk(nil), review.Risks...)
	review.Decisions = append([]Decision(nil), review.Decisions...)
	return LegacyPresentation{
		Review: review, Events: events,
		Compatibility: cloneLegacyCompatibility(state.Machine.LegacyCompatibility),
	}, nil
}

// LegacyState reconstructs the public compatibility model used by proposal,
// resume, and history. Machine-only session and evidence values remain exact;
// aggregate human fields use the accepted v2 revision.
func LegacyState(state State) (ledger.State, error) {
	if err := validateStateProjectAccounting(state); err != nil {
		return ledger.State{}, err
	}
	if err := Validate(state); err != nil {
		return ledger.State{}, err
	}
	compatibility := cloneLegacyCompatibility(state.Machine.LegacyCompatibility)
	legacy := ledger.State{
		ProjectID:    state.Review.ProjectID,
		CurrentState: compatibility.CurrentState,
		Decisions:    make(map[string]ledger.Decision, len(state.Review.Decisions)),
		OpenLoops:    make(map[string]ledger.OpenLoop, len(state.Review.Risks)),
		Sessions:     make(map[string]ledger.SessionReport, len(state.Machine.Sessions)),
	}
	legacy.CurrentState.ProjectID = state.Review.ProjectID
	legacy.CurrentState.Revision = state.Review.Revision
	legacy.CurrentState.Goal = state.Review.Goal
	legacy.CurrentState.LastVerified = state.Review.LastVerification
	legacy.CurrentState.Branch = state.Review.Stage
	legacy.CurrentState.NextAction = state.Review.NextAction
	legacy.CurrentState.Blockers = nil
	legacy.CurrentState.OpenRisks = nil

	compatibleDecisions := make(map[string]ledger.Decision, len(compatibility.Decisions))
	for _, value := range compatibility.Decisions {
		compatibleDecisions[value.ID] = value
	}
	for _, decision := range state.Review.Decisions {
		value, exists := compatibleDecisions[decision.ID]
		if !exists {
			return ledger.State{}, fmt.Errorf("decision %q has no legacy compatibility record", decision.ID)
		}
		value.Title = decision.Title
		value.Status = decision.Status
		value.Rationale = decision.Rationale
		value.Consequences = decision.Impact
		legacy.Decisions[decision.ID] = value
	}
	currentRisks := make(map[string]CurrentRiskProvenance, len(compatibility.CurrentRisks))
	for _, provenance := range compatibility.CurrentRisks {
		currentRisks[provenance.RiskID] = provenance
	}
	compatibleLoops := make(map[string]ledger.OpenLoop, len(compatibility.OpenLoops))
	for _, value := range compatibility.OpenLoops {
		compatibleLoops[value.ID] = value
		legacy.OpenLoops[value.ID] = value
	}
	for _, risk := range state.Review.Risks {
		if _, generated := currentRisks[risk.ID]; generated {
			continue
		}
		value, exists := compatibleLoops[risk.ID]
		if !exists {
			return ledger.State{}, fmt.Errorf("risk %q has no legacy compatibility record or current-risk provenance", risk.ID)
		}
		value.Title = risk.Title
		value.Status = risk.Status
		legacy.OpenLoops[risk.ID] = value
	}
	reviewRisks := riskByID(state.Review.Risks)
	for _, provenance := range compatibility.CurrentRisks {
		risk, exists := reviewRisks[provenance.RiskID]
		if !exists {
			return ledger.State{}, fmt.Errorf("current-risk provenance %q has no visible risk", provenance.RiskID)
		}
		if provenance.Kind == "blocker" {
			legacy.CurrentState.Blockers = append(legacy.CurrentState.Blockers, risk.Title)
		} else {
			legacy.CurrentState.OpenRisks = append(legacy.CurrentState.OpenRisks, risk.Title)
		}
	}
	compatibleEvents := make(map[string]ledger.TimelineEvent, len(compatibility.Timeline))
	for _, value := range compatibility.Timeline {
		compatibleEvents[value.ID] = value
	}
	for _, event := range state.Events {
		value, exists := compatibleEvents[event.ID]
		if !exists {
			return ledger.State{}, fmt.Errorf("event %q has no legacy compatibility record", event.ID)
		}
		value.OccurredAt = event.OccurredAt
		value.Class = ledger.FactClass(event.Kind)
		value.Title = event.Title
		value.Summary = event.Summary
		value.DecisionIDs = append([]string(nil), event.DecisionIDs...)
		legacy.Timeline = append(legacy.Timeline, value)
	}
	sortLegacyTimeline(legacy.Timeline)
	for _, report := range state.Machine.Sessions {
		copy := cloneLegacySession(report)
		legacy.Sessions[copy.ID] = copy
	}
	return legacy, nil
}

// ApplyChangeSet applies the legacy proposal model, reprojects the complete
// accepted state, and returns all three v2 targets with exact loaded
// preimages. No write is performed.
func ApplyChangeSet(current Accepted, changes ledger.ChangeSet) (ledger.WritePlan, error) {
	if !current.v2 || current.projectRoot == "" || current.projectInfo == nil || len(current.files) != 3 {
		return ledger.WritePlan{}, fmt.Errorf("ApplyChangeSet requires a loaded v2 accepted state")
	}
	if err := validateAcceptedPreimages(current); err != nil {
		return ledger.WritePlan{}, err
	}
	if emptyChangeSet(changes) {
		return ledger.WritePlan{ProjectRoot: current.projectRoot}, nil
	}
	nextLegacy, err := ledger.ApplyChangeSetModel(current.Legacy, changes)
	if err != nil {
		return ledger.WritePlan{}, err
	}
	next, err := ProjectLegacy(nextLegacy)
	if err != nil {
		return ledger.WritePlan{}, err
	}
	next.Review.Revision = current.State.Review.Revision + 1
	next.Machine.AcceptedRevision = next.Review.Revision
	next.Review.Name = current.State.Review.Name
	next.Machine.LastSuccessfulSync = current.State.Machine.LastSuccessfulSync
	baseline, err := ProjectLegacy(current.Legacy)
	if err != nil {
		return ledger.WritePlan{}, err
	}
	if err := preserveCurrentRiskIdentities(&baseline, current.State); err != nil {
		return ledger.WritePlan{}, err
	}
	if err := preserveCurrentRiskIdentities(&next, current.State); err != nil {
		return ledger.WritePlan{}, err
	}
	overlayUnchangedHumanFields(&next, current.State, baseline, changes)
	canonicalizeReviewOrder(&next.Review)
	reviewBody, historyBody, err := patchAcceptedDocuments(current, next)
	if err != nil {
		return ledger.WritePlan{}, err
	}
	reviewDocument, err := ParseReview(reviewBody)
	if err != nil {
		return ledger.WritePlan{}, err
	}
	historyDocument, err := ParseHistory(historyBody)
	if err != nil {
		return ledger.WritePlan{}, err
	}
	next.Review = reviewDocument.Model
	next.Events = historyDocument.Events
	next.Machine.ReviewSHA256 = sha256Hex(reviewBody)
	next.Machine.HistorySHA256 = sha256Hex(historyBody)
	if err := validateStateProjectAccounting(next); err != nil {
		return ledger.WritePlan{}, err
	}
	if err := Validate(next); err != nil {
		return ledger.WritePlan{}, err
	}
	machineBody, err := RenderMachineLedger(next.Machine)
	if err != nil {
		return ledger.WritePlan{}, err
	}
	plan := ledger.WritePlan{ProjectRoot: current.projectRoot, Files: []ledger.PlannedFile{
		{RelativePath: HistoryRelativePath, Data: historyBody, Perm: 0o644},
		{RelativePath: MachineLedgerRelativePath, Data: machineBody, Perm: 0o600},
		{RelativePath: ReviewRelativePath, Data: reviewBody, Perm: 0o644},
	}}
	for index := range plan.Files {
		previous, exists := current.files[plan.Files[index].RelativePath]
		if !exists {
			return ledger.WritePlan{}, fmt.Errorf("accepted preimage for %s is missing", plan.Files[index].RelativePath)
		}
		plan.Files[index].Perm = previous.perm.Perm()
		plan.Files[index].ExpectedExists = true
		plan.Files[index].ExpectedData = append([]byte(nil), previous.body...)
		plan.Files[index].ExpectedPerm = previous.perm.Perm()
	}
	return plan, nil
}

func validateAcceptedPreimages(current Accepted) error {
	directory, err := openReviewRoot(current.projectRoot, current.projectInfo)
	if err != nil {
		return err
	}
	defer directory.Close()
	for _, relative := range []string{HistoryRelativePath, MachineLedgerRelativePath, ReviewRelativePath} {
		previous, exists := current.files[relative]
		if !exists {
			return fmt.Errorf("accepted preimage for %s is missing", relative)
		}
		maximum := int64(MaxDocumentBytes)
		if relative == MachineLedgerRelativePath {
			maximum = MaxMachineLedgerBytes
		}
		body, perm, err := readStableReviewFile(directory, relative, maximum)
		if err != nil {
			return err
		}
		if !bytes.Equal(body, previous.body) || perm.Perm() != previous.perm.Perm() {
			return fmt.Errorf("accepted file %s changed after load", relative)
		}
	}
	return nil
}

func emptyChangeSet(changes ledger.ChangeSet) bool {
	return changes.Current == nil && len(changes.Timeline) == 0 && len(changes.Decisions) == 0 && len(changes.OpenLoops) == 0 && len(changes.Sessions) == 0
}

func overlayUnchangedHumanFields(next *State, current, baseline State, changes ledger.ChangeSet) {
	if next == nil {
		return
	}
	if next.Review.Goal == baseline.Review.Goal {
		next.Review.Goal = current.Review.Goal
	}
	if next.Review.Stage == baseline.Review.Stage {
		next.Review.Stage = current.Review.Stage
	}
	if next.Review.Status == baseline.Review.Status {
		next.Review.Status = current.Review.Status
	}
	if next.Review.NextAction == baseline.Review.NextAction {
		next.Review.NextAction = current.Review.NextAction
	}
	if next.Review.LastVerification == baseline.Review.LastVerification {
		next.Review.LastVerification = current.Review.LastVerification
	}

	currentRisks, baselineRisks := riskByID(current.Review.Risks), riskByID(baseline.Review.Risks)
	for index := range next.Review.Risks {
		value := &next.Review.Risks[index]
		previous, exists := currentRisks[value.ID]
		before, hadBaseline := baselineRisks[value.ID]
		if !exists || !hadBaseline {
			continue
		}
		if currentRiskProvenanceByID(next.Machine.LegacyCompatibility, value.ID) != nil {
			if value.Title == before.Title {
				value.Title = previous.Title
			}
			value.Status = previous.Status
			value.Detail = previous.Detail
			continue
		}
		if value.Title == before.Title {
			value.Title = previous.Title
		}
		if value.Status == before.Status {
			value.Status = previous.Status
		}
		if value.Detail == before.Detail {
			value.Detail = previous.Detail
		}
	}
	currentDecisions, baselineDecisions := decisionByID(current.Review.Decisions), decisionByID(baseline.Review.Decisions)
	for index := range next.Review.Decisions {
		value := &next.Review.Decisions[index]
		previous, exists := currentDecisions[value.ID]
		before, hadBaseline := baselineDecisions[value.ID]
		if !exists || !hadBaseline {
			continue
		}
		if value.Title == before.Title {
			value.Title = previous.Title
		}
		if value.Rationale == before.Rationale {
			value.Rationale = previous.Rationale
		}
		if value.Impact == before.Impact {
			value.Impact = previous.Impact
		}
	}
	currentEvents, baselineEvents := eventByID(current.Events), eventByID(baseline.Events)
	changedEvents := make(map[string]struct{}, len(changes.Timeline))
	for _, value := range changes.Timeline {
		changedEvents[value.ID] = struct{}{}
	}
	for index := range next.Events {
		value := &next.Events[index]
		previous, exists := currentEvents[value.ID]
		before, hadBaseline := baselineEvents[value.ID]
		if !exists || !hadBaseline {
			continue
		}
		if _, changed := changedEvents[value.ID]; !changed {
			value.Title = previous.Title
			value.Meaning = previous.Meaning
			value.Summary = previous.Summary
			value.Why = previous.Why
			value.Changes = append([]string(nil), previous.Changes...)
			value.Results = append([]string(nil), previous.Results...)
			value.Next = previous.Next
			continue
		}
		if value.Title == before.Title {
			value.Title = previous.Title
		}
		if value.Meaning == before.Meaning {
			value.Meaning = previous.Meaning
		}
		if value.Summary == before.Summary {
			value.Summary = previous.Summary
		}
		if value.Why == before.Why {
			value.Why = previous.Why
		}
		if reflect.DeepEqual(value.Changes, before.Changes) {
			value.Changes = append([]string(nil), previous.Changes...)
		}
		if reflect.DeepEqual(value.Results, before.Results) {
			value.Results = append([]string(nil), previous.Results...)
		}
		if value.Next == before.Next {
			value.Next = previous.Next
		}
	}
}

func canonicalizeReviewOrder(review *Review) {
	if review == nil {
		return
	}
	sort.Slice(review.Risks, func(left, right int) bool {
		if review.Risks[left].Status != review.Risks[right].Status {
			return review.Risks[left].Status < review.Risks[right].Status
		}
		return review.Risks[left].ID < review.Risks[right].ID
	})
	sort.Slice(review.Decisions, func(left, right int) bool {
		if review.Decisions[left].OccurredAt != review.Decisions[right].OccurredAt {
			return laterLegacyTime(review.Decisions[left].OccurredAt, review.Decisions[right].OccurredAt)
		}
		return review.Decisions[left].ID < review.Decisions[right].ID
	})
}

type currentRiskIdentityEntry struct {
	kind         string
	value        string
	visibleValue string
	riskID       string
	sourceKey    string
	sourceIndex  int
	riskIndex    int
	provenance   int
}

func preserveCurrentRiskIdentities(next *State, current State) error {
	if next == nil {
		return nil
	}
	oldEntries, err := currentRiskIdentityEntries(current)
	if err != nil {
		return err
	}
	newEntries, err := currentRiskIdentityEntries(*next)
	if err != nil {
		return err
	}
	oldMatched := make([]bool, len(oldEntries))
	newMatched := make([]bool, len(newEntries))
	type match struct{ old, next int }
	matches := make([]match, 0, min(len(oldEntries), len(newEntries)))
	// A source-key match is exact across visible identity, kind, normalized
	// hidden value, and source position. It is eligible only while the next
	// value also equals the accepted visible title: an accepted title edit is
	// authoritative for deciding whether a later ChangeSet changed semantics.
	for nextIndex := range newEntries {
		for oldIndex := range oldEntries {
			if oldMatched[oldIndex] || oldEntries[oldIndex].sourceKey != newEntries[nextIndex].sourceKey || oldEntries[oldIndex].visibleValue != newEntries[nextIndex].value {
				continue
			}
			oldMatched[oldIndex], newMatched[nextIndex] = true, true
			matches = append(matches, match{old: oldIndex, next: nextIndex})
			break
		}
	}
	// If the complete ordered current-risk lists are unchanged from the
	// accepted visible titles, source position is explicit enough to preserve
	// even duplicate-title identities.
	if currentRiskListsEqualAcceptedVisible(oldEntries, newEntries) {
		for index := range newEntries {
			if oldMatched[index] || newMatched[index] {
				continue
			}
			oldMatched[index], newMatched[index] = true, true
			matches = append(matches, match{old: index, next: index})
		}
	}
	// Otherwise retain an exact accepted-visible-title identity only when that
	// unmatched content is unique on both sides. Duplicate content cannot
	// safely carry distinct human status/detail across a positional edit.
	for nextIndex := range newEntries {
		if newMatched[nextIndex] {
			continue
		}
		oldCandidate, oldCount, newCount := -1, 0, 0
		for oldIndex := range oldEntries {
			if !oldMatched[oldIndex] && oldEntries[oldIndex].kind == newEntries[nextIndex].kind && oldEntries[oldIndex].visibleValue == newEntries[nextIndex].value {
				oldCandidate, oldCount = oldIndex, oldCount+1
			}
		}
		for candidate := range newEntries {
			if !newMatched[candidate] && newEntries[candidate].kind == newEntries[nextIndex].kind && newEntries[candidate].value == newEntries[nextIndex].value {
				newCount++
			}
		}
		if oldCount == 1 && newCount == 1 {
			oldMatched[oldCandidate], newMatched[nextIndex] = true, true
			matches = append(matches, match{old: oldCandidate, next: nextIndex})
		}
	}
	nonCurrentIDs := make(map[string]struct{}, len(next.Review.Risks))
	currentIDs := make(map[string]struct{}, len(newEntries))
	for _, entry := range newEntries {
		currentIDs[entry.riskID] = struct{}{}
	}
	for _, risk := range next.Review.Risks {
		if _, currentRisk := currentIDs[risk.ID]; !currentRisk {
			nonCurrentIDs[risk.ID] = struct{}{}
		}
	}
	assignments := make(map[int]string, len(matches))
	used := make(map[string]struct{}, len(nonCurrentIDs)+len(newEntries))
	for id := range nonCurrentIDs {
		used[id] = struct{}{}
	}
	for _, pair := range matches {
		oldID := oldEntries[pair.old].riskID
		if _, collision := used[oldID]; collision {
			continue
		}
		assignments[pair.next] = oldID
		used[oldID] = struct{}{}
	}
	// Unmatched current-state values have no explicit rename relation in the
	// ChangeSet. Reserve every old identity so delete+insert cannot transfer an
	// old marker ID (and therefore its accepted human fields) to a new value.
	for _, entry := range oldEntries {
		used[entry.riskID] = struct{}{}
	}
	for index, target := range newEntries {
		id, assigned := assignments[index]
		if !assigned {
			id = target.riskID
			if _, collision := used[id]; collision {
				projectionKind, status := "blocker", "blocked"
				if target.kind == "open_risk" {
					projectionKind, status = "risk", "open"
				}
				id = currentStateRisk(projectionKind, status, target.value, used).ID
			} else {
				used[id] = struct{}{}
			}
		}
		next.Review.Risks[target.riskIndex].ID = id
		next.Machine.LegacyCompatibility.CurrentRisks[target.provenance].RiskID = id
		next.Machine.LegacyCompatibility.CurrentRisks[target.provenance].SourceKey = currentRiskSourceKey(id, target.kind, target.sourceIndex, target.value)
	}
	sort.Slice(next.Review.Risks, func(left, right int) bool {
		if next.Review.Risks[left].Status != next.Review.Risks[right].Status {
			return next.Review.Risks[left].Status < next.Review.Risks[right].Status
		}
		return next.Review.Risks[left].ID < next.Review.Risks[right].ID
	})
	return nil
}

func currentRiskListsEqualAcceptedVisible(oldEntries, newEntries []currentRiskIdentityEntry) bool {
	if len(oldEntries) != len(newEntries) {
		return false
	}
	for index := range oldEntries {
		if oldEntries[index].kind != newEntries[index].kind || oldEntries[index].visibleValue != newEntries[index].value {
			return false
		}
	}
	return true
}

func currentRiskIdentityEntries(state State) ([]currentRiskIdentityEntry, error) {
	compatibility := state.Machine.LegacyCompatibility
	riskIndexes := make(map[string]int, len(state.Review.Risks))
	for index, risk := range state.Review.Risks {
		riskIndexes[risk.ID] = index
	}
	entries := make([]currentRiskIdentityEntry, 0, len(compatibility.CurrentRisks))
	blockerIndex, openRiskIndex := 0, 0
	for provenanceIndex, provenance := range compatibility.CurrentRisks {
		var value string
		var sourceIndex int
		switch provenance.Kind {
		case "blocker":
			if blockerIndex >= len(compatibility.CurrentState.Blockers) {
				return nil, errors.New("current-risk blocker provenance exceeds hidden sources")
			}
			value = compatibility.CurrentState.Blockers[blockerIndex]
			sourceIndex = blockerIndex
			blockerIndex++
		case "open_risk":
			if openRiskIndex >= len(compatibility.CurrentState.OpenRisks) {
				return nil, errors.New("current-risk open-risk provenance exceeds hidden sources")
			}
			value = compatibility.CurrentState.OpenRisks[openRiskIndex]
			sourceIndex = openRiskIndex
			openRiskIndex++
		default:
			return nil, fmt.Errorf("unknown current-risk provenance kind %q", provenance.Kind)
		}
		riskIndex, exists := riskIndexes[provenance.RiskID]
		if !exists {
			return nil, fmt.Errorf("current-risk provenance %q has no visible risk", provenance.RiskID)
		}
		entries = append(entries, currentRiskIdentityEntry{
			kind:         provenance.Kind,
			value:        strings.TrimSpace(normalizeMarkdownText(value)),
			visibleValue: strings.TrimSpace(normalizeMarkdownText(state.Review.Risks[riskIndex].Title)),
			riskID:       provenance.RiskID,
			sourceKey:    provenance.SourceKey,
			sourceIndex:  sourceIndex,
			riskIndex:    riskIndex,
			provenance:   provenanceIndex,
		})
	}
	return entries, nil
}

func currentRiskProvenanceByID(value LegacyCompatibility, id string) *CurrentRiskProvenance {
	for index := range value.CurrentRisks {
		if value.CurrentRisks[index].RiskID == id {
			return &value.CurrentRisks[index]
		}
	}
	return nil
}

// Render returns the complete three-file accepted-state write plan. Fresh
// projections have no preimages; loaded-state updates add them in
// ApplyChangeSet.
func Render(projectRoot string, state State) (ledger.WritePlan, error) {
	if err := validateStateProjectAccounting(state); err != nil {
		return ledger.WritePlan{}, err
	}
	reviewBody, err := RenderReview(state.Review)
	if err != nil {
		return ledger.WritePlan{}, err
	}
	historyBody, err := RenderHistory(state.Review.ProjectID, state.Review.Revision, state.Events)
	if err != nil {
		return ledger.WritePlan{}, err
	}
	if state.Machine.ReviewSHA256 != sha256Hex(reviewBody) || state.Machine.HistorySHA256 != sha256Hex(historyBody) {
		return ledger.WritePlan{}, fmt.Errorf("machine ledger document hashes do not match rendered Markdown")
	}
	if err := Validate(state); err != nil {
		return ledger.WritePlan{}, err
	}
	machineBody, err := RenderMachineLedger(state.Machine)
	if err != nil {
		return ledger.WritePlan{}, err
	}
	return ledger.WritePlan{ProjectRoot: projectRoot, Files: []ledger.PlannedFile{
		{RelativePath: HistoryRelativePath, Data: historyBody, Perm: 0o644},
		{RelativePath: MachineLedgerRelativePath, Data: machineBody, Perm: 0o600},
		{RelativePath: ReviewRelativePath, Data: reviewBody, Perm: 0o644},
	}}, nil
}

func validateStateProjectAccounting(state State) error {
	sessions := make([]*accounting.SessionAccounting, 0, len(state.Machine.Sessions))
	for index := range state.Machine.Sessions {
		sessions = append(sessions, state.Machine.Sessions[index].Accounting)
	}
	if err := accounting.ValidateProjectSummary(state.Machine.Accounting, sessions); err != nil {
		return fmt.Errorf("invalid project accounting: %w", err)
	}
	return nil
}

func setDocumentHashes(state *State) error {
	if state == nil {
		return fmt.Errorf("review state is required")
	}
	reviewBody, err := RenderReview(state.Review)
	if err != nil {
		return err
	}
	historyBody, err := RenderHistory(state.Review.ProjectID, state.Review.Revision, state.Events)
	if err != nil {
		return err
	}
	state.Machine.ReviewSHA256 = sha256Hex(reviewBody)
	state.Machine.HistorySHA256 = sha256Hex(historyBody)
	return nil
}

func sha256Hex(body []byte) string {
	digest := sha256.Sum256(body)
	return fmt.Sprintf("%x", digest)
}

func legacyProjectStatus(current ledger.CurrentState) string {
	if len(current.Blockers) != 0 {
		return "blocked"
	}
	if len(current.OpenRisks) != 0 {
		return "at_risk"
	}
	return "active"
}

func legacyLoopVisible(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "resolved", "closed", "done", "completed":
		return false
	default:
		return true
	}
}

func latestEventTime(events []Event) string {
	latest := ""
	for _, event := range events {
		if laterLegacyTime(event.OccurredAt, latest) {
			latest = event.OccurredAt
		}
	}
	return latest
}

func maximumLegacyRevision(state ledger.State) int {
	maximum := state.CurrentState.Revision
	for _, event := range state.Timeline {
		maximum = max(maximum, event.Revision)
	}
	for _, decision := range state.Decisions {
		maximum = max(maximum, decision.Revision)
	}
	for _, loop := range state.OpenLoops {
		maximum = max(maximum, loop.Revision)
	}
	for _, session := range state.Sessions {
		maximum = max(maximum, session.Revision)
	}
	return maximum
}

func laterLegacyTime(left, right string) bool {
	if right == "" {
		return left != ""
	}
	leftTime, leftOK := parseLegacyTime(left)
	rightTime, rightOK := parseLegacyTime(right)
	if leftOK && rightOK {
		return leftTime.After(rightTime)
	}
	return left > right
}

func parseLegacyTime(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func currentStateRisk(kind, status, title string, used map[string]struct{}) Risk {
	for attempt := 0; ; attempt++ {
		seed := kind + "\x00" + title
		if attempt != 0 {
			seed += "\x00" + strconv.Itoa(attempt)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(seed)))
		id := "risk-current-" + kind + "-" + digest[:16]
		if _, collision := used[id]; !collision {
			used[id] = struct{}{}
			return Risk{ID: id, Title: title, Status: status, Detail: title}
		}
	}
}

func currentRiskSourceKey(riskID, kind string, index int, value string) string {
	normalized := strings.TrimSpace(normalizeMarkdownText(value))
	digest := sha256.Sum256([]byte(riskID + "\x00" + kind + "\x00" + strconv.Itoa(index) + "\x00" + normalized))
	return fmt.Sprintf("%x", digest)
}

func openLoopDetail(value ledger.OpenLoop) string {
	parts := make([]string, 0, 5)
	appendDetail := func(label, body string) {
		if strings.TrimSpace(body) != "" {
			parts = append(parts, label+"\uff1a"+strings.TrimSpace(body))
		}
	}
	appendDetail("问题", value.Question)
	appendDetail("阻塞", value.Blocker)
	appendDetail("下一实验", value.NextExperiment)
	appendDetail("完成标准", value.CompletionCriterion)
	if len(value.Attempts) != 0 {
		appendDetail("已尝试", strings.Join(value.Attempts, "；"))
	}
	return strings.Join(parts, "\n")
}

func projectLegacyEvent(source ledger.TimelineEvent, decisions map[string]ledger.Decision, loops map[string]ledger.OpenLoop, fallbackNext string) Event {
	event := Event{
		ID:          source.ID,
		OccurredAt:  source.OccurredAt,
		Kind:        string(source.Class),
		Title:       source.Title,
		Meaning:     legacyEventMeaning(source.Class),
		Summary:     source.Summary,
		DecisionIDs: append([]string(nil), source.DecisionIDs...),
	}
	for _, id := range source.DecisionIDs {
		if decision, exists := decisions[id]; exists {
			event.Changes = append(event.Changes, "决策："+decision.Title)
			if strings.TrimSpace(decision.Rationale) != "" {
				event.Why = appendParagraph(event.Why, decision.Rationale)
			}
		}
	}
	for _, id := range source.OpenLoopIDs {
		if loop, exists := loops[id]; exists {
			event.Changes = append(event.Changes, "风险与待办："+loop.Title)
			if strings.TrimSpace(loop.NextExperiment) != "" {
				event.Next = appendParagraph(event.Next, loop.NextExperiment)
			} else if strings.TrimSpace(loop.Blocker) != "" {
				event.Next = appendParagraph(event.Next, loop.Blocker)
			}
		}
	}
	if strings.TrimSpace(event.Summary) == "" {
		event.Summary = source.Title
	}
	if strings.TrimSpace(event.Why) == "" {
		event.Why = source.Summary
		if strings.TrimSpace(event.Why) == "" {
			event.Why = source.Title
		}
	}
	if strings.TrimSpace(event.Next) == "" {
		event.Next = fallbackNext
		if strings.TrimSpace(event.Next) == "" {
			event.Next = "旧记录未包含下一步。"
		}
	}
	event.Changes = uniqueNonemptySorted(event.Changes)
	event.Results = uniqueNonemptySorted(event.Results)
	if len(event.Changes) == 0 {
		event.Changes = []string{event.Summary}
	}
	if len(event.Results) == 0 {
		if strings.TrimSpace(source.Summary) == "" {
			event.Results = []string{"旧记录未包含独立验证结果。"}
		} else {
			event.Results = []string{source.Summary}
		}
	}
	event.DecisionIDs = uniqueNonemptySorted(event.DecisionIDs)
	return event
}

func legacyEventMeaning(class ledger.FactClass) string {
	switch class {
	case ledger.Verified:
		return "已验证的项目状态节点"
	case ledger.DecisionFact:
		return "影响后续实施的关键决策"
	case ledger.Inference:
		return "需要后续验证的推断"
	case ledger.Superseded:
		return "已被后续证据替代的历史状态"
	case ledger.PendingConfirmation:
		return "等待确认的项目状态节点"
	default:
		return "项目演进节点"
	}
}

func appendParagraph(existing, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return value
	}
	return existing + "\n" + value
}

func appendMachineEvidence(target map[string][]ledger.EvidenceRef, owner string, values []ledger.EvidenceRef) {
	if len(values) == 0 {
		return
	}
	target[owner] = append(target[owner], cloneEvidenceRefs(values)...)
}

func cloneEvidenceRefs(values []ledger.EvidenceRef) []ledger.EvidenceRef {
	if len(values) == 0 {
		return nil
	}
	return append([]ledger.EvidenceRef(nil), values...)
}

func cloneLegacySession(value ledger.SessionReport) ledger.SessionReport {
	value.GoalChanges = append([]string(nil), value.GoalChanges...)
	value.Files = append([]string(nil), value.Files...)
	value.Commits = append([]string(nil), value.Commits...)
	value.Verification = append([]string(nil), value.Verification...)
	value.DecisionsAdded = append([]string(nil), value.DecisionsAdded...)
	value.DecisionsRevised = append([]string(nil), value.DecisionsRevised...)
	value.OpenLoopsCreated = append([]string(nil), value.OpenLoopsCreated...)
	value.OpenLoopsClosed = append([]string(nil), value.OpenLoopsClosed...)
	value.Evidence = cloneEvidenceRefs(value.Evidence)
	value.Phases = append([]ledger.SessionPhase(nil), value.Phases...)
	for index := range value.Phases {
		value.Phases[index].Evidence = cloneEvidenceRefs(value.Phases[index].Evidence)
	}
	if value.Accounting != nil {
		copy := *value.Accounting
		copy.Models = append([]accounting.ModelAccounting{}, copy.Models...)
		value.Accounting = &copy
	}
	return value
}

func legacyCompatibilityFromState(state ledger.State) LegacyCompatibility {
	result := LegacyCompatibility{
		CurrentState: cloneLegacyCurrentState(state.CurrentState),
		Timeline:     make([]ledger.TimelineEvent, 0, len(state.Timeline)),
		Decisions:    make([]ledger.Decision, 0, len(state.Decisions)),
		OpenLoops:    make([]ledger.OpenLoop, 0, len(state.OpenLoops)),
		CurrentRisks: []CurrentRiskProvenance{},
	}
	for _, event := range state.Timeline {
		result.Timeline = append(result.Timeline, cloneLegacyTimelineEvent(event))
	}
	for _, id := range sortedMapKeys(state.Decisions) {
		result.Decisions = append(result.Decisions, cloneLegacyDecision(state.Decisions[id]))
	}
	for _, id := range sortedMapKeys(state.OpenLoops) {
		result.OpenLoops = append(result.OpenLoops, cloneLegacyOpenLoop(state.OpenLoops[id]))
	}
	return result
}

func cloneLegacyCompatibility(value LegacyCompatibility) LegacyCompatibility {
	result := LegacyCompatibility{
		CurrentState: cloneLegacyCurrentState(value.CurrentState),
		Timeline:     make([]ledger.TimelineEvent, 0, len(value.Timeline)),
		Decisions:    make([]ledger.Decision, 0, len(value.Decisions)),
		OpenLoops:    make([]ledger.OpenLoop, 0, len(value.OpenLoops)),
		CurrentRisks: append([]CurrentRiskProvenance{}, value.CurrentRisks...),
	}
	for _, event := range value.Timeline {
		result.Timeline = append(result.Timeline, cloneLegacyTimelineEvent(event))
	}
	for _, decision := range value.Decisions {
		result.Decisions = append(result.Decisions, cloneLegacyDecision(decision))
	}
	for _, loop := range value.OpenLoops {
		result.OpenLoops = append(result.OpenLoops, cloneLegacyOpenLoop(loop))
	}
	return result
}

func cloneLegacyCurrentState(value ledger.CurrentState) ledger.CurrentState {
	value.UncommittedChanges = append([]string{}, value.UncommittedChanges...)
	value.Blockers = append([]string{}, value.Blockers...)
	value.OpenRisks = append([]string{}, value.OpenRisks...)
	value.SourceSessions = append([]string{}, value.SourceSessions...)
	value.Evidence = append([]ledger.EvidenceRef{}, value.Evidence...)
	return value
}

func cloneLegacyTimelineEvent(value ledger.TimelineEvent) ledger.TimelineEvent {
	value.Evidence = append([]ledger.EvidenceRef{}, value.Evidence...)
	value.DecisionIDs = append([]string{}, value.DecisionIDs...)
	value.OpenLoopIDs = append([]string{}, value.OpenLoopIDs...)
	return value
}

func cloneLegacyDecision(value ledger.Decision) ledger.Decision {
	value.Tags = append([]string{}, value.Tags...)
	value.Supersedes = append([]string{}, value.Supersedes...)
	value.SourceSessions = append([]string{}, value.SourceSessions...)
	value.Evidence = append([]ledger.EvidenceRef{}, value.Evidence...)
	value.Alternatives = append([]string{}, value.Alternatives...)
	value.RejectedPaths = append([]string{}, value.RejectedPaths...)
	return value
}

func cloneLegacyOpenLoop(value ledger.OpenLoop) ledger.OpenLoop {
	value.Tags = append([]string{}, value.Tags...)
	value.SourceSessions = append([]string{}, value.SourceSessions...)
	value.Evidence = append([]ledger.EvidenceRef{}, value.Evidence...)
	value.Attempts = append([]string{}, value.Attempts...)
	return value
}

func uniqueNonemptySorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil
	}
	return result
}

func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
