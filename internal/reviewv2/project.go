package reviewv2

import (
	"bytes"
	"crypto/sha256"
	"fmt"
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
			SchemaVersion:    SchemaVersion,
			ProjectID:        legacy.ProjectID,
			AcceptedRevision: legacy.CurrentState.Revision,
			Evidence:         make(map[string][]ledger.EvidenceRef),
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
		state.Review.Risks = append(state.Review.Risks, Risk{
			ID:     value.ID,
			Title:  value.Title,
			Status: value.Status,
			Detail: openLoopDetail(value),
		})
		appendMachineEvidence(state.Machine.Evidence, id, value.Evidence)
	}
	for _, blocker := range legacy.CurrentState.Blockers {
		state.Review.Risks = append(state.Review.Risks, currentStateRisk("blocker", "blocked", blocker, usedRiskIDs))
	}
	for _, risk := range legacy.CurrentState.OpenRisks {
		state.Review.Risks = append(state.Review.Risks, currentStateRisk("risk", "open", risk, usedRiskIDs))
	}
	sort.Slice(state.Review.Risks, func(left, right int) bool {
		if state.Review.Risks[left].Status != state.Review.Risks[right].Status {
			return state.Review.Risks[left].Status < state.Review.Risks[right].Status
		}
		return state.Review.Risks[left].ID < state.Review.Risks[right].ID
	})

	reportsBySource := make(map[string]ledger.SessionReport, len(legacy.Sessions))
	for _, id := range sortedMapKeys(legacy.Sessions) {
		report := cloneLegacySession(legacy.Sessions[id])
		state.Machine.Sessions = append(state.Machine.Sessions, report)
		reportsBySource[report.SessionID] = report
		appendMachineEvidence(state.Machine.Evidence, report.ID, report.Evidence)
	}
	sort.Slice(state.Machine.Sessions, func(left, right int) bool {
		if state.Machine.Sessions[left].SessionID != state.Machine.Sessions[right].SessionID {
			return state.Machine.Sessions[left].SessionID < state.Machine.Sessions[right].SessionID
		}
		return state.Machine.Sessions[left].ID < state.Machine.Sessions[right].ID
	})

	for _, source := range legacy.Timeline {
		event := projectLegacyEvent(source, legacy.Decisions, legacy.OpenLoops, reportsBySource, legacy.CurrentState.NextAction)
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
	legacy := ledger.State{
		ProjectID: state.Review.ProjectID,
		CurrentState: ledger.CurrentState{
			ProjectID:          state.Review.ProjectID,
			Revision:           state.Review.Revision,
			Goal:               state.Review.Goal,
			LastVerified:       state.Review.LastVerification,
			Branch:             state.Review.Stage,
			UncommittedChanges: []string{},
			Blockers:           []string{},
			OpenRisks:          []string{},
			NextAction:         state.Review.NextAction,
			FirstInspection:    "",
			LastUpdated:        latestEventTime(state.Events),
			SourceSessions:     []string{},
			Evidence:           sortedAllEvidence(state.Machine.Evidence),
		},
		Decisions: make(map[string]ledger.Decision, len(state.Review.Decisions)),
		OpenLoops: make(map[string]ledger.OpenLoop, len(state.Review.Risks)),
		Sessions:  make(map[string]ledger.SessionReport, len(state.Machine.Sessions)),
	}
	for _, decision := range state.Review.Decisions {
		evidence := cloneEvidenceRefs(state.Machine.Evidence[decision.ID])
		legacy.Decisions[decision.ID] = ledger.Decision{
			ID:             decision.ID,
			ProjectID:      state.Review.ProjectID,
			Title:          decision.Title,
			Status:         decision.Status,
			Revision:       state.Review.Revision,
			Tags:           []string{},
			Supersedes:     []string{},
			SourceSessions: sortedEvidenceSessionIDs(evidence),
			Evidence:       evidence,
			Rationale:      decision.Rationale,
			Consequences:   decision.Impact,
			Alternatives:   []string{},
			RejectedPaths:  []string{},
		}
	}
	for _, risk := range state.Review.Risks {
		if kind, generated := generatedCurrentRisk(risk.ID); generated {
			if kind == "blocker" {
				legacy.CurrentState.Blockers = append(legacy.CurrentState.Blockers, risk.Title)
			} else {
				legacy.CurrentState.OpenRisks = append(legacy.CurrentState.OpenRisks, risk.Title)
			}
			continue
		}
		evidence := cloneEvidenceRefs(state.Machine.Evidence[risk.ID])
		legacy.OpenLoops[risk.ID] = ledger.OpenLoop{
			ID:             risk.ID,
			ProjectID:      state.Review.ProjectID,
			Title:          risk.Title,
			Status:         risk.Status,
			Revision:       state.Review.Revision,
			Tags:           []string{},
			SourceSessions: sortedEvidenceSessionIDs(evidence),
			Evidence:       evidence,
			Question:       risk.Detail,
			Attempts:       []string{},
			NextExperiment: state.Review.NextAction,
		}
	}
	for _, event := range state.Events {
		legacy.Timeline = append(legacy.Timeline, ledger.TimelineEvent{
			ID:          event.ID,
			OccurredAt:  event.OccurredAt,
			Revision:    state.Review.Revision,
			Class:       ledger.FactClass(event.Kind),
			Title:       event.Title,
			Summary:     event.Summary,
			Evidence:    cloneEvidenceRefs(state.Machine.Evidence[event.ID]),
			DecisionIDs: append([]string(nil), event.DecisionIDs...),
			OpenLoopIDs: []string{},
		})
	}
	sortLegacyTimeline(legacy.Timeline)
	for _, report := range state.Machine.Sessions {
		copy := cloneLegacySession(report)
		legacy.Sessions[copy.ID] = copy
		legacy.CurrentState.SourceSessions = append(legacy.CurrentState.SourceSessions, copy.SessionID)
	}
	legacy.CurrentState.SourceSessions = uniqueNonemptySorted(legacy.CurrentState.SourceSessions)
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
	preserveUnchangedReviewUnits(&next, current.State, changes)
	if err := setDocumentHashes(&next); err != nil {
		return ledger.WritePlan{}, err
	}
	plan, err := Render(current.projectRoot, next)
	if err != nil {
		return ledger.WritePlan{}, err
	}
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

func preserveUnchangedReviewUnits(next *State, current State, changes ledger.ChangeSet) {
	changedDecisions := make(map[string]struct{}, len(changes.Decisions))
	for _, value := range changes.Decisions {
		changedDecisions[value.ID] = struct{}{}
	}
	oldDecisions := make(map[string]Decision, len(current.Review.Decisions))
	for _, value := range current.Review.Decisions {
		oldDecisions[value.ID] = value
	}
	for index := range next.Review.Decisions {
		if _, changed := changedDecisions[next.Review.Decisions[index].ID]; changed {
			continue
		}
		if previous, exists := oldDecisions[next.Review.Decisions[index].ID]; exists {
			next.Review.Decisions[index] = previous
		}
	}

	changedRisks := make(map[string]struct{}, len(changes.OpenLoops))
	for _, value := range changes.OpenLoops {
		changedRisks[value.ID] = struct{}{}
	}
	oldRisks := make(map[string]Risk, len(current.Review.Risks))
	for _, value := range current.Review.Risks {
		oldRisks[value.ID] = value
	}
	for index := range next.Review.Risks {
		if _, changed := changedRisks[next.Review.Risks[index].ID]; changed {
			continue
		}
		if _, generated := generatedCurrentRisk(next.Review.Risks[index].ID); generated && changes.Current != nil {
			continue
		}
		if previous, exists := oldRisks[next.Review.Risks[index].ID]; exists {
			next.Review.Risks[index] = previous
		}
	}

	changedEvents := make(map[string]struct{}, len(changes.Timeline))
	for _, value := range changes.Timeline {
		changedEvents[value.ID] = struct{}{}
	}
	oldEvents := make(map[string]Event, len(current.Events))
	for _, value := range current.Events {
		oldEvents[value.ID] = value
	}
	for index := range next.Events {
		if _, changed := changedEvents[next.Events[index].ID]; changed {
			continue
		}
		if previous, exists := oldEvents[next.Events[index].ID]; exists {
			next.Events[index] = previous
		}
	}
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
		{RelativePath: MachineLedgerRelativePath, Data: machineBody, Perm: 0o644},
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

func projectLegacyEvent(source ledger.TimelineEvent, decisions map[string]ledger.Decision, loops map[string]ledger.OpenLoop, reports map[string]ledger.SessionReport, fallbackNext string) Event {
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
	for _, ref := range source.Evidence {
		if strings.TrimSpace(ref.Summary) != "" {
			event.Results = append(event.Results, ref.Summary)
		}
		if report, exists := reports[ref.SessionID]; exists {
			event.Results = append(event.Results, report.Verification...)
		}
	}
	if event.Why == "" {
		event.Why = source.Summary
	}
	if event.Next == "" {
		event.Next = fallbackNext
	}
	event.Changes = uniqueNonemptySorted(event.Changes)
	event.Results = uniqueNonemptySorted(event.Results)
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
		copy.Models = append([]accounting.ModelAccounting(nil), copy.Models...)
		value.Accounting = &copy
	}
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
