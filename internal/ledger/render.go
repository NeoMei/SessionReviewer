package ledger

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

const (
	typedListCodecMarker = "<!-- session-reviewer:list-codec=v1 -->"
	typedListCodecPrefix = "<!-- session-reviewer:list-codec"
	typedListEntryPrefix = "- sr-string: "
	v2ReviewPath         = "docs/session-review/项目回顾.md"
	v2HistoryPath        = "docs/session-review/项目历史.md"
	v2MachineLedgerPath  = "docs/session-review/.session-reviewer/ledger.json"
	v2MachineLedgerBytes = 16 << 20
)

// Render validates and applies changes to a private state clone. Every target
// document is rendered successfully before a non-empty plan is returned.
func Render(state State, changes ChangeSet) (WritePlan, error) {
	fail := func(err error) (WritePlan, error) { return WritePlan{}, err }
	if state.ProjectID == "" || !stableProjectID.MatchString(state.ProjectID) {
		return fail(errors.New("ledger state has invalid project ID"))
	}
	next, err := cloneState(state)
	if err != nil {
		return fail(err)
	}
	if err := applyChanges(&next, changes); err != nil {
		return fail(err)
	}

	files := make([]PlannedFile, 0, 6+len(changes.Decisions)+len(changes.OpenLoops)+len(changes.Sessions))
	appendRendered := func(relative string, doc Document, old *loadedDocument) error {
		body, err := doc.Render()
		if err != nil {
			return fmt.Errorf("render %s: %w", relative, err)
		}
		planned := PlannedFile{RelativePath: relative, Data: body, Perm: 0o644}
		if old != nil {
			planned.Perm = old.Perm
			planned.ExpectedExists = true
			planned.ExpectedData = append([]byte(nil), old.Original...)
			planned.ExpectedPerm = old.Perm
			if bytes.Equal(old.Original, body) {
				return nil
			}
		}
		files = append(files, planned)
		return nil
	}

	if changes.Current != nil {
		doc, err := renderCurrentDocument(state.CurrentState, next.CurrentState, next.documents.current)
		if err != nil {
			return fail(err)
		}
		if err := appendRendered(ledgerRootRelative+"/current-state.md", doc, state.documents.current); err != nil {
			return fail(err)
		}
		loaded, err := loadedFromRendered(ledgerRootRelative+"/current-state.md", doc, state.documents.current)
		if err != nil {
			return fail(err)
		}
		next.documents.current = &loaded
	}
	if len(changes.Timeline) != 0 {
		doc, err := renderTimelineDocument(state.Timeline, next.Timeline, next.documents.timeline, state.ProjectID)
		if err != nil {
			return fail(err)
		}
		if err := appendRendered(ledgerRootRelative+"/evolution-timeline.md", doc, state.documents.timeline); err != nil {
			return fail(err)
		}
		loaded, err := loadedFromRendered(ledgerRootRelative+"/evolution-timeline.md", doc, state.documents.timeline)
		if err != nil {
			return fail(err)
		}
		next.documents.timeline = &loaded
	}

	decisions := append([]Decision(nil), changes.Decisions...)
	sort.Slice(decisions, func(i, j int) bool { return decisions[i].ID < decisions[j].ID })
	for _, incoming := range decisions {
		old := state.Decisions[incoming.ID]
		loaded, loadedOK := next.documents.decisions[incoming.ID]
		doc, err := renderDecisionDocument(old, incoming, pointerIf(loaded, loadedOK))
		if err != nil {
			return fail(err)
		}
		relative := ledgerRootRelative + "/decisions/" + incoming.ID + ".md"
		if loadedOK {
			relative = loaded.RelativePath
		}
		if err := appendRendered(relative, doc, pointerIf(loaded, loadedOK)); err != nil {
			return fail(err)
		}
		rendered, err := loadedFromRendered(relative, doc, pointerIf(loaded, loadedOK))
		if err != nil {
			return fail(err)
		}
		next.documents.decisions[incoming.ID] = rendered
	}

	loops := append([]OpenLoop(nil), changes.OpenLoops...)
	sort.Slice(loops, func(i, j int) bool { return loops[i].ID < loops[j].ID })
	for _, incoming := range loops {
		old := state.OpenLoops[incoming.ID]
		loaded, ok := next.documents.openLoops[incoming.ID]
		doc, err := renderOpenLoopDocument(old, incoming, pointerIf(loaded, ok))
		if err != nil {
			return fail(err)
		}
		relative := ledgerRootRelative + "/open-loops/" + incoming.ID + ".md"
		if ok {
			relative = loaded.RelativePath
		}
		if err := appendRendered(relative, doc, pointerIf(loaded, ok)); err != nil {
			return fail(err)
		}
		rendered, err := loadedFromRendered(relative, doc, pointerIf(loaded, ok))
		if err != nil {
			return fail(err)
		}
		next.documents.openLoops[incoming.ID] = rendered
	}

	sessions := append([]SessionReport(nil), changes.Sessions...)
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	for _, incoming := range sessions {
		old := state.Sessions[incoming.ID]
		loaded, ok := next.documents.sessions[incoming.ID]
		doc, err := renderSessionDocument(old, incoming, pointerIf(loaded, ok))
		if err != nil {
			return fail(err)
		}
		relative := ledgerRootRelative + "/sessions/" + incoming.ID + ".md"
		if ok {
			relative = loaded.RelativePath
		}
		if err := appendRendered(relative, doc, pointerIf(loaded, ok)); err != nil {
			return fail(err)
		}
		rendered, err := loadedFromRendered(relative, doc, pointerIf(loaded, ok))
		if err != nil {
			return fail(err)
		}
		next.documents.sessions[incoming.ID] = rendered
	}

	artifacts, err := RenderDerivedArtifacts(next)
	if err != nil {
		return fail(err)
	}
	directory, err := openLedgerProjectRoot(state.projectRoot, rootOpenOptions{expectedRoot: state.projectRootInfo})
	if err != nil {
		return fail(fmt.Errorf("read derived navigation: %w", err))
	}
	planned := make(map[string]PlannedFile, len(files)+len(artifacts))
	for _, file := range files {
		if _, exists := planned[file.RelativePath]; exists {
			_ = directory.Close()
			return fail(errors.New("rendered target collision"))
		}
		planned[file.RelativePath] = file
	}
	for _, artifact := range artifacts {
		existing, mode, readErr := readLedgerRegular(directory, artifact.RelativePath, false)
		safelyMissing := errors.Is(readErr, os.ErrNotExist) || safelyMissingTargetParent(directory, artifact.RelativePath)
		if readErr == nil {
			if bytes.Equal(existing, artifact.Data) {
				delete(planned, artifact.RelativePath)
				continue
			}
			planned[artifact.RelativePath] = PlannedFile{
				RelativePath: artifact.RelativePath, Data: append([]byte(nil), artifact.Data...), Perm: mode,
				ExpectedData: append([]byte(nil), existing...), ExpectedExists: true, ExpectedPerm: mode,
			}
			continue
		}
		if !safelyMissing {
			_ = directory.Close()
			return fail(fmt.Errorf("read derived navigation: %w", readErr))
		}
		planned[artifact.RelativePath] = PlannedFile{RelativePath: artifact.RelativePath, Data: append([]byte(nil), artifact.Data...), Perm: artifact.Perm}
	}
	if err := directory.Close(); err != nil {
		return fail(err)
	}
	files = files[:0]
	for _, file := range planned {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	return WritePlan{ProjectRoot: state.projectRoot, Files: files}, nil
}

func loadedFromRendered(relative string, doc Document, previous *loadedDocument) (loadedDocument, error) {
	body, err := doc.Render()
	if err != nil {
		return loadedDocument{}, err
	}
	perm := fs.FileMode(0o644)
	if previous != nil {
		perm = previous.Perm
	}
	return loadedDocument{Document: doc, RelativePath: relative, Original: body, Perm: perm}, nil
}

func pointerIf[T any](value T, ok bool) *T {
	if !ok {
		return nil
	}
	return &value
}

func cloneState(state State) (State, error) {
	clone := state
	clone.CurrentState = cloneCurrent(state.CurrentState)
	clone.Timeline = make([]TimelineEvent, len(state.Timeline))
	for i, item := range state.Timeline {
		clone.Timeline[i] = cloneTimeline(item)
	}
	clone.Decisions = make(map[string]Decision, len(state.Decisions))
	for id, item := range state.Decisions {
		clone.Decisions[id] = cloneDecision(item)
	}
	clone.OpenLoops = make(map[string]OpenLoop, len(state.OpenLoops))
	for id, item := range state.OpenLoops {
		clone.OpenLoops[id] = cloneOpenLoop(item)
	}
	clone.Sessions = make(map[string]SessionReport, len(state.Sessions))
	for id, item := range state.Sessions {
		clone.Sessions[id] = cloneSession(item)
	}
	var err error
	clone.documents.current, err = cloneLoadedPointer(state.documents.current)
	if err != nil {
		return State{}, err
	}
	clone.documents.timeline, err = cloneLoadedPointer(state.documents.timeline)
	if err != nil {
		return State{}, err
	}
	clone.documents.decisions, err = cloneLoadedMap(state.documents.decisions)
	if err != nil {
		return State{}, err
	}
	clone.documents.openLoops, err = cloneLoadedMap(state.documents.openLoops)
	if err != nil {
		return State{}, err
	}
	clone.documents.sessions, err = cloneLoadedMap(state.documents.sessions)
	if err != nil {
		return State{}, err
	}
	return clone, nil
}

// ApplyChangeSetModel applies a ChangeSet to a deep clone of the public ledger
// model. It deliberately drops loaded document and project-root state so model
// projection remains pure and independent of rendering or filesystem access.
func ApplyChangeSetModel(state State, changes ChangeSet) (State, error) {
	clone := State{
		ProjectID:    state.ProjectID,
		CurrentState: cloneCurrent(state.CurrentState),
		Timeline:     make([]TimelineEvent, len(state.Timeline)),
		Decisions:    make(map[string]Decision, len(state.Decisions)),
		OpenLoops:    make(map[string]OpenLoop, len(state.OpenLoops)),
		Sessions:     make(map[string]SessionReport, len(state.Sessions)),
	}
	for index, item := range state.Timeline {
		clone.Timeline[index] = cloneTimeline(item)
	}
	for id, item := range state.Decisions {
		clone.Decisions[id] = cloneDecision(item)
	}
	for id, item := range state.OpenLoops {
		clone.OpenLoops[id] = cloneOpenLoop(item)
	}
	for id, item := range state.Sessions {
		clone.Sessions[id] = cloneSession(item)
	}
	if err := applyChanges(&clone, changes); err != nil {
		return State{}, err
	}
	return clone, nil
}

func cloneLoadedPointer(source *loadedDocument) (*loadedDocument, error) {
	if source == nil {
		return nil, nil
	}
	clone, err := cloneLoaded(*source)
	return &clone, err
}

func cloneLoadedMap(source map[string]loadedDocument) (map[string]loadedDocument, error) {
	result := make(map[string]loadedDocument, len(source))
	for id, item := range source {
		clone, err := cloneLoaded(item)
		if err != nil {
			return nil, err
		}
		result[id] = clone
	}
	return result, nil
}

func cloneLoaded(source loadedDocument) (loadedDocument, error) {
	doc, err := source.Document.Clone()
	if err != nil {
		return loadedDocument{}, err
	}
	source.Document = doc
	source.Original = append([]byte(nil), source.Original...)
	return source, nil
}

func applyChanges(state *State, changes ChangeSet) error {
	if state == nil {
		return errors.New("nil ledger state")
	}
	globalIDs := map[string]string{"current-state": "reserved current state", "evolution-timeline": "reserved timeline"}
	for id := range state.Decisions {
		globalIDs[id] = "decision"
	}
	for id := range state.OpenLoops {
		if _, ok := globalIDs[id]; ok {
			return fmt.Errorf("duplicate ledger ID %q", id)
		}
		globalIDs[id] = "open loop"
	}
	for id := range state.Sessions {
		if _, ok := globalIDs[id]; ok {
			return fmt.Errorf("duplicate ledger ID %q", id)
		}
		globalIDs[id] = "session"
	}
	for _, event := range state.Timeline {
		if _, ok := globalIDs[event.ID]; ok {
			return fmt.Errorf("duplicate ledger ID %q", event.ID)
		}
		globalIDs[event.ID] = "timeline"
	}

	if changes.Current != nil {
		incoming := cloneCurrent(*changes.Current)
		if incoming.ProjectID != state.ProjectID {
			return errors.New("current state project mismatch")
		}
		if err := requireNextRevision(state.CurrentState.Revision, incoming.Revision, "current state"); err != nil {
			return err
		}
		state.CurrentState = incoming
	}
	seen := make(map[string]struct{})
	for _, incoming := range changes.Decisions {
		if err := validateChangeID(incoming.ID, incoming.ProjectID, state.ProjectID, seen); err != nil {
			return err
		}
		old, exists := state.Decisions[incoming.ID]
		if owner, collision := globalIDs[incoming.ID]; collision && !exists {
			return fmt.Errorf("ledger ID %q already belongs to %s", incoming.ID, owner)
		}
		if err := requireNextRevision(revisionIf(exists, old.Revision), incoming.Revision, "decision "+incoming.ID); err != nil {
			return err
		}
		incoming = cloneDecision(incoming)
		normalizeDecision(&incoming)
		state.Decisions[incoming.ID] = incoming
		globalIDs[incoming.ID] = "decision"
	}
	for _, incoming := range changes.OpenLoops {
		if err := validateChangeID(incoming.ID, incoming.ProjectID, state.ProjectID, seen); err != nil {
			return err
		}
		old, exists := state.OpenLoops[incoming.ID]
		if owner, collision := globalIDs[incoming.ID]; collision && !exists {
			return fmt.Errorf("ledger ID %q already belongs to %s", incoming.ID, owner)
		}
		if err := requireNextRevision(revisionIf(exists, old.Revision), incoming.Revision, "open loop "+incoming.ID); err != nil {
			return err
		}
		incoming = cloneOpenLoop(incoming)
		normalizeOpenLoop(&incoming)
		state.OpenLoops[incoming.ID] = incoming
		globalIDs[incoming.ID] = "open loop"
	}
	sessionsBySession := make(map[string]string)
	for id, report := range state.Sessions {
		sessionsBySession[report.SessionID] = id
	}
	for _, incoming := range changes.Sessions {
		if err := validateChangeID(incoming.ID, incoming.ProjectID, state.ProjectID, seen); err != nil {
			return err
		}
		if incoming.SessionID == "" {
			return fmt.Errorf("session report %q has empty session ID", incoming.ID)
		}
		old, exists := state.Sessions[incoming.ID]
		if owner, collision := globalIDs[incoming.ID]; collision && !exists {
			return fmt.Errorf("ledger ID %q already belongs to %s", incoming.ID, owner)
		}
		if represented, duplicate := sessionsBySession[incoming.SessionID]; duplicate && represented != incoming.ID {
			return fmt.Errorf("duplicate session ID %q", incoming.SessionID)
		}
		if exists && old.SessionID != incoming.SessionID {
			return fmt.Errorf("session report %q changed session ID", incoming.ID)
		}
		if err := requireNextRevision(revisionIf(exists, old.Revision), incoming.Revision, "session "+incoming.ID); err != nil {
			return err
		}
		incoming = cloneSession(incoming)
		normalizeSession(&incoming)
		state.Sessions[incoming.ID] = incoming
		globalIDs[incoming.ID] = "session"
		sessionsBySession[incoming.SessionID] = incoming.ID
	}
	timelineByID := make(map[string]int, len(state.Timeline))
	for i, event := range state.Timeline {
		timelineByID[event.ID] = i
	}
	for _, incoming := range changes.Timeline {
		if !stableLedgerID.MatchString(incoming.ID) {
			return fmt.Errorf("invalid timeline ID %q", incoming.ID)
		}
		if _, duplicate := seen[incoming.ID]; duplicate {
			return fmt.Errorf("duplicate change ID %q", incoming.ID)
		}
		seen[incoming.ID] = struct{}{}
		index, exists := timelineByID[incoming.ID]
		if owner, collision := globalIDs[incoming.ID]; collision && !exists {
			return fmt.Errorf("ledger ID %q already belongs to %s", incoming.ID, owner)
		}
		oldRevision := 0
		if exists {
			oldRevision = state.Timeline[index].Revision
		}
		if err := requireNextRevision(oldRevision, incoming.Revision, "timeline "+incoming.ID); err != nil {
			return err
		}
		incoming = cloneTimeline(incoming)
		normalizeTimeline(&incoming)
		if exists {
			state.Timeline[index] = incoming
		} else {
			timelineByID[incoming.ID] = len(state.Timeline)
			state.Timeline = append(state.Timeline, incoming)
		}
		globalIDs[incoming.ID] = "timeline"
	}
	sort.Slice(state.Timeline, func(i, j int) bool {
		if state.Timeline[i].OccurredAt != state.Timeline[j].OccurredAt {
			return state.Timeline[i].OccurredAt < state.Timeline[j].OccurredAt
		}
		return state.Timeline[i].ID < state.Timeline[j].ID
	})
	return nil
}

func validateChangeID(id, projectID, wantProject string, seen map[string]struct{}) error {
	if !stableLedgerID.MatchString(id) {
		return fmt.Errorf("invalid ledger ID %q", id)
	}
	if projectID != wantProject {
		return fmt.Errorf("ledger entity %q project mismatch", id)
	}
	if _, duplicate := seen[id]; duplicate {
		return fmt.Errorf("duplicate change ID %q", id)
	}
	seen[id] = struct{}{}
	return nil
}

func revisionIf(exists bool, revision int) int {
	if exists {
		return revision
	}
	return 0
}
func requireNextRevision(old, incoming int, label string) error {
	if incoming < 1 || incoming != old+1 {
		return fmt.Errorf("%s revision must increment exactly once", label)
	}
	return nil
}

func renderCurrentDocument(old, incoming CurrentState, loaded *loadedDocument) (Document, error) {
	doc, fresh, err := documentFor(loaded, "current-state", "current_state", incoming.ProjectID, incoming.Revision, "Current state")
	if err != nil {
		return Document{}, err
	}
	if !fresh {
		if err := doc.SetReserved(map[string]any{"id": "current-state", "entity_type": "current_state", "project_id": incoming.ProjectID, "revision": incoming.Revision, "source_sessions": sortedStrings(incoming.SourceSessions)}); err != nil {
			return Document{}, err
		}
	}
	if err := setKnownFrontmatter(&doc, map[string]any{"source_sessions": sortedStrings(incoming.SourceSessions), "evidence": sortedEvidence(incoming.Evidence)}); err != nil {
		return Document{}, err
	}
	sections := []struct {
		name      string
		old, next any
		body      string
	}{
		{"Current goal", old.Goal, incoming.Goal, incoming.Goal},
		{"Last verified state", old.LastVerified, incoming.LastVerified, incoming.LastVerified},
		{"Repository", old.Branch, incoming.Branch, incoming.Branch},
		{"Blockers", old.Blockers, incoming.Blockers, bulletList(incoming.Blockers)},
		{"Next action", old.NextAction, incoming.NextAction, incoming.NextAction},
		{"Uncommitted changes", old.UncommittedChanges, incoming.UncommittedChanges, bulletList(incoming.UncommittedChanges)},
		{"Open risks", old.OpenRisks, incoming.OpenRisks, bulletList(incoming.OpenRisks)},
		{"First inspection", old.FirstInspection, incoming.FirstInspection, incoming.FirstInspection},
		{"Last updated", old.LastUpdated, incoming.LastUpdated, incoming.LastUpdated},
	}
	for _, section := range sections {
		if fresh || !reflect.DeepEqual(section.old, section.next) {
			if err := doc.UpsertSection(section.name, section.body); err != nil {
				return Document{}, err
			}
		}
	}
	return doc, nil
}

func renderTimelineDocument(old, incoming []TimelineEvent, loaded *loadedDocument, projectID string) (Document, error) {
	revision := 1
	if loaded != nil {
		current, err := requiredRevision(&loaded.Document.Frontmatter)
		if err != nil {
			return Document{}, err
		}
		revision = current + 1
	}
	doc, fresh, err := documentFor(loaded, "evolution-timeline", "timeline", projectID, revision, "Evolution timeline")
	if err != nil {
		return Document{}, err
	}
	if !fresh {
		if err := doc.SetReserved(map[string]any{"id": "evolution-timeline", "entity_type": "timeline", "project_id": projectID, "revision": revision}); err != nil {
			return Document{}, err
		}
	}
	if err := setKnownFrontmatter(&doc, map[string]any{"events": incoming}); err != nil {
		return Document{}, err
	}
	if fresh || !reflect.DeepEqual(old, incoming) {
		if err := doc.UpsertSection("Events", timelineMarkdown(incoming)); err != nil {
			return Document{}, err
		}
	}
	return doc, nil
}

func renderDecisionDocument(old, incoming Decision, loaded *loadedDocument) (Document, error) {
	doc, fresh, err := documentFor(loaded, incoming.ID, "decision", incoming.ProjectID, incoming.Revision, incoming.Title)
	if err != nil {
		return Document{}, err
	}
	if !fresh {
		if err := doc.SetReserved(map[string]any{"id": incoming.ID, "entity_type": "decision", "project_id": incoming.ProjectID, "revision": incoming.Revision, "source_sessions": sortedStrings(incoming.SourceSessions)}); err != nil {
			return Document{}, err
		}
	}
	if fresh || old.Title != incoming.Title || old.Status != incoming.Status || !reflect.DeepEqual(old.Tags, incoming.Tags) {
		if err := doc.SetEditable(map[string]any{"title": incoming.Title, "status": incoming.Status, "tags": sortedStrings(incoming.Tags)}); err != nil {
			return Document{}, err
		}
	}
	if err := setKnownFrontmatter(&doc, map[string]any{"supersedes": sortedStrings(incoming.Supersedes), "source_sessions": sortedStrings(incoming.SourceSessions), "evidence": sortedEvidence(incoming.Evidence)}); err != nil {
		return Document{}, err
	}
	sections := []struct {
		name      string
		old, next any
		body      string
	}{
		{"Context", old.Context, incoming.Context, incoming.Context},
		{"Alternatives", old.Alternatives, incoming.Alternatives, bulletList(incoming.Alternatives)},
		{"Rationale", old.Rationale, incoming.Rationale, incoming.Rationale},
		{"Rejected paths", old.RejectedPaths, incoming.RejectedPaths, bulletList(incoming.RejectedPaths)},
		{"Evidence", old.Evidence, incoming.Evidence, evidenceMarkdown(incoming.Evidence)},
		{"Consequences", old.Consequences, incoming.Consequences, incoming.Consequences},
		{"Conditions for reevaluation", old.ReevaluateWhen, incoming.ReevaluateWhen, incoming.ReevaluateWhen},
	}
	for _, section := range sections {
		if fresh || !reflect.DeepEqual(section.old, section.next) {
			if err := doc.UpsertSection(section.name, section.body); err != nil {
				return Document{}, err
			}
		}
	}
	return doc, nil
}

func renderOpenLoopDocument(old, incoming OpenLoop, loaded *loadedDocument) (Document, error) {
	doc, fresh, err := documentFor(loaded, incoming.ID, "open_loop", incoming.ProjectID, incoming.Revision, incoming.Title)
	if err != nil {
		return Document{}, err
	}
	if !fresh {
		if err := doc.SetReserved(map[string]any{"id": incoming.ID, "entity_type": "open_loop", "project_id": incoming.ProjectID, "revision": incoming.Revision, "source_sessions": sortedStrings(incoming.SourceSessions)}); err != nil {
			return Document{}, err
		}
	}
	if fresh || old.Title != incoming.Title || old.Status != incoming.Status || !reflect.DeepEqual(old.Tags, incoming.Tags) {
		if err := doc.SetEditable(map[string]any{"title": incoming.Title, "status": incoming.Status, "tags": sortedStrings(incoming.Tags)}); err != nil {
			return Document{}, err
		}
	}
	if err := setKnownFrontmatter(&doc, map[string]any{"source_sessions": sortedStrings(incoming.SourceSessions), "evidence": sortedEvidence(incoming.Evidence)}); err != nil {
		return Document{}, err
	}
	sections := []struct {
		name      string
		old, next any
		body      string
	}{
		{"Question", old.Question, incoming.Question, incoming.Question},
		{"Available evidence", old.Evidence, incoming.Evidence, evidenceMarkdown(incoming.Evidence)},
		{"Attempted paths", old.Attempts, incoming.Attempts, bulletList(incoming.Attempts)},
		{"Blocking condition", old.Blocker, incoming.Blocker, incoming.Blocker},
		{"Recommended next experiment", old.NextExperiment, incoming.NextExperiment, incoming.NextExperiment},
		{"Completion criterion", old.CompletionCriterion, incoming.CompletionCriterion, incoming.CompletionCriterion},
	}
	for _, section := range sections {
		if fresh || !reflect.DeepEqual(section.old, section.next) {
			if err := doc.UpsertSection(section.name, section.body); err != nil {
				return Document{}, err
			}
		}
	}
	return doc, nil
}

func renderSessionDocument(old, incoming SessionReport, loaded *loadedDocument) (Document, error) {
	doc, fresh, err := documentFor(loaded, incoming.ID, "session", incoming.ProjectID, incoming.Revision, "Session "+incoming.SessionID)
	if err != nil {
		return Document{}, err
	}
	if !fresh {
		if err := doc.SetReserved(map[string]any{"id": incoming.ID, "entity_type": "session", "project_id": incoming.ProjectID, "revision": incoming.Revision, "source_sessions": []string{incoming.SessionID}}); err != nil {
			return Document{}, err
		}
	}
	fields := map[string]any{"session_id": incoming.SessionID, "source_sessions": []string{incoming.SessionID}, "initial_goal": incoming.InitialGoal, "goal_changes": incoming.GoalChanges, "phases": incoming.Phases, "files": incoming.Files, "commits": incoming.Commits, "verification": incoming.Verification, "decisions_added": sortedStrings(incoming.DecisionsAdded), "decisions_revised": sortedStrings(incoming.DecisionsRevised), "open_loops_created": sortedStrings(incoming.OpenLoopsCreated), "open_loops_closed": sortedStrings(incoming.OpenLoopsClosed), "previous_session_id": incoming.PreviousSessionID, "next_session_id": incoming.NextSessionID, "evidence": sortedEvidence(incoming.Evidence)}
	if incoming.Accounting != nil {
		fields["accounting"] = incoming.Accounting
	}
	if err := setKnownFrontmatter(&doc, fields); err != nil {
		return Document{}, err
	}
	sections := []struct {
		name      string
		old, next any
		body      string
	}{{"Initial goal", old.InitialGoal, incoming.InitialGoal, incoming.InitialGoal}, {"Goal changes", old.GoalChanges, incoming.GoalChanges, bulletList(incoming.GoalChanges)}, {"Interaction phases", old.Phases, incoming.Phases, phasesMarkdown(incoming.Phases)}, {"Files", old.Files, incoming.Files, bulletList(incoming.Files)}, {"Commits", old.Commits, incoming.Commits, bulletList(incoming.Commits)}, {"Verification", old.Verification, incoming.Verification, bulletList(incoming.Verification)}, {"Decisions added", old.DecisionsAdded, incoming.DecisionsAdded, bulletList(incoming.DecisionsAdded)}, {"Decisions revised", old.DecisionsRevised, incoming.DecisionsRevised, bulletList(incoming.DecisionsRevised)}, {"Open loops created", old.OpenLoopsCreated, incoming.OpenLoopsCreated, bulletList(incoming.OpenLoopsCreated)}, {"Open loops closed", old.OpenLoopsClosed, incoming.OpenLoopsClosed, bulletList(incoming.OpenLoopsClosed)}, {"Previous session", old.PreviousSessionID, incoming.PreviousSessionID, incoming.PreviousSessionID}, {"Next session", old.NextSessionID, incoming.NextSessionID, incoming.NextSessionID}, {"Usage accounting", old.Accounting, incoming.Accounting, sessionAccountingMarkdown(incoming.Accounting)}, {"Evidence", old.Evidence, incoming.Evidence, evidenceMarkdown(incoming.Evidence)}}
	for _, section := range sections {
		if fresh || !reflect.DeepEqual(section.old, section.next) {
			if err := doc.UpsertSection(section.name, section.body); err != nil {
				return Document{}, err
			}
		}
	}
	return doc, nil
}

func renderProjectAccountingDocument(state State) (Document, bool, error) {
	if state.documents.overview == nil {
		return Document{}, false, errors.New("project overview is required")
	}
	doc, err := ParseDocument(state.documents.overview.Original)
	if err != nil {
		return Document{}, false, nil
	}
	sessions := make([]*accounting.SessionAccounting, 0, len(state.Sessions))
	for _, report := range state.Sessions {
		sessions = append(sessions, report.Accounting)
	}
	summary, err := accounting.Aggregate(sessions)
	if err != nil {
		return Document{}, false, err
	}
	if err := doc.UpsertSection("Project accounting", projectAccountingMarkdown(summary, accounting.SessionsPricingComplete(sessions))); err != nil {
		return Document{}, false, err
	}
	return doc, true, nil
}

func sessionAccountingMarkdown(value *accounting.SessionAccounting) string {
	if value == nil {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "- Duration: %s (%d ms)\n- Total tokens: %d\n", accounting.FormatDurationMS(value.DurationMS), value.DurationMS, value.TotalTokens)
	if accounting.SessionPricingComplete(value) {
		fmt.Fprintf(&out, "- Total cost: $%.9f USD\n", value.TotalCostUSD)
	} else {
		out.WriteString("- Total cost: unavailable (pricing unavailable)\n")
	}
	for _, model := range value.Models {
		if model.Pricing == (accounting.Pricing{}) {
			fmt.Fprintf(&out, "- `%s`: %d tokens; cost unavailable (pricing unavailable)\n", model.Model, model.TotalTokens)
		} else {
			fmt.Fprintf(&out, "- `%s`: %d tokens; $%.9f USD; rates per 1M input/cached/cache-write/output = %.6f/%.6f/%.6f/%.6f USD; as of %s; source %s\n", model.Model, model.TotalTokens, model.CostUSD, model.Pricing.InputPerMillion, model.Pricing.CachedInputPerMillion, model.Pricing.CacheWriteInputPerMillion, model.Pricing.OutputPerMillion, model.Pricing.AsOf, model.Pricing.Source)
		}
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func projectAccountingMarkdown(value accounting.ProjectSummary, pricingComplete bool) string {
	var out strings.Builder
	fmt.Fprintf(&out, "- Total session duration: %s (%d ms)\n- Total tokens: %d\n", accounting.FormatDurationMS(value.TotalDurationMS), value.TotalDurationMS, value.TotalTokens)
	if pricingComplete {
		fmt.Fprintf(&out, "- Total cost: $%.9f USD\n", value.TotalCostUSD)
	} else {
		out.WriteString("- Total cost: unavailable (one or more model prices are unavailable)\n")
	}
	for _, model := range value.Models {
		if pricingComplete {
			fmt.Fprintf(&out, "- `%s`: %d tokens (%.4f%%); $%.9f USD (%.4f%% of cost)\n", model.Model, model.TotalTokens, model.TokenSharePct, model.TotalCostUSD, model.CostSharePct)
		} else {
			fmt.Fprintf(&out, "- `%s`: %d tokens (%.4f%%); project cost unavailable\n", model.Model, model.TotalTokens, model.TokenSharePct)
		}
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func documentFor(loaded *loadedDocument, id, entityType, projectID string, revision int, title string) (Document, bool, error) {
	if loaded != nil {
		return loaded.Document, false, nil
	}
	if !stableLedgerID.MatchString(id) || revision != 1 {
		return Document{}, false, errors.New("new document has invalid identity or revision")
	}
	src := fmt.Sprintf("---\nid: %s\nentity_type: %s\nproject_id: %s\nrevision: 1\n---\n\n# %s\n", id, entityType, projectID, safeHeading(title))
	doc, err := ParseDocument([]byte(src))
	return doc, true, err
}

func safeHeading(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "Untitled"
	}
	return strings.TrimLeft(value, "# ")
}

func setKnownFrontmatter(doc *Document, fields map[string]any) error {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := fields[name]
		node, err := encodeValue(value)
		if err != nil {
			return fmt.Errorf("encode %s: %w", name, err)
		}
		setMappingValue(&doc.Frontmatter, name, node)
	}
	return nil
}

func bulletList(values []string) string {
	var out strings.Builder
	out.WriteString(typedListCodecMarker)
	for _, value := range values {
		out.WriteByte('\n')
		out.WriteString(typedListEntryPrefix)
		out.WriteString(strconv.Quote(value))
	}
	return out.String()
}
func evidenceMarkdown(values []EvidenceRef) string {
	if len(values) == 0 {
		return ""
	}
	var out strings.Builder
	for _, ref := range sortedEvidence(values) {
		summary := strings.TrimRight(ref.Summary, " \t\r\n")
		summary = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(summary)
		fmt.Fprintf(&out, "- `%s` (%s:%d): %s\n", ref.EvidenceID, ref.SessionID, ref.JSONLLine, summary)
	}
	return strings.TrimSuffix(out.String(), "\n")
}
func timelineMarkdown(values []TimelineEvent) string {
	if len(values) == 0 {
		return ""
	}
	var out strings.Builder
	for _, event := range values {
		fmt.Fprintf(&out, "- **%s** `%s` `%s` — %s: %s\n", event.OccurredAt, event.Class, event.ID, event.Title, event.Summary)
	}
	return strings.TrimSuffix(out.String(), "\n")
}
func phasesMarkdown(values []SessionPhase) string {
	if len(values) == 0 {
		return ""
	}
	var out strings.Builder
	for _, phase := range values {
		fmt.Fprintf(&out, "### %s\n\n%s\n\n", safeHeading(phase.Title), phase.Summary)
	}
	return strings.TrimSpace(out.String())
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
func sortedEvidence(values []EvidenceRef) []EvidenceRef {
	result := append([]EvidenceRef(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].EvidenceID != result[j].EvidenceID {
			return result[i].EvidenceID < result[j].EvidenceID
		}
		if result[i].SessionID != result[j].SessionID {
			return result[i].SessionID < result[j].SessionID
		}
		return result[i].JSONLLine < result[j].JSONLLine
	})
	return result
}

func normalizeDecision(item *Decision) {
	item.Tags = sortedStrings(item.Tags)
	item.Supersedes = sortedStrings(item.Supersedes)
	item.SourceSessions = sortedStrings(item.SourceSessions)
	item.Evidence = sortedEvidence(item.Evidence)
}
func normalizeOpenLoop(item *OpenLoop) {
	item.Tags = sortedStrings(item.Tags)
	item.SourceSessions = sortedStrings(item.SourceSessions)
	item.Evidence = sortedEvidence(item.Evidence)
}
func normalizeTimeline(item *TimelineEvent) {
	item.Evidence = sortedEvidence(item.Evidence)
	item.DecisionIDs = sortedStrings(item.DecisionIDs)
	item.OpenLoopIDs = sortedStrings(item.OpenLoopIDs)
}
func normalizeSession(item *SessionReport) {
	item.DecisionsAdded = sortedStrings(item.DecisionsAdded)
	item.DecisionsRevised = sortedStrings(item.DecisionsRevised)
	item.OpenLoopsCreated = sortedStrings(item.OpenLoopsCreated)
	item.OpenLoopsClosed = sortedStrings(item.OpenLoopsClosed)
	item.Evidence = sortedEvidence(item.Evidence)
	for i := range item.Phases {
		item.Phases[i].Evidence = sortedEvidence(item.Phases[i].Evidence)
	}
}

func cloneEvidence(values []EvidenceRef) []EvidenceRef { return append([]EvidenceRef(nil), values...) }
func cloneCurrent(item CurrentState) CurrentState {
	item.UncommittedChanges = append([]string(nil), item.UncommittedChanges...)
	item.Blockers = append([]string(nil), item.Blockers...)
	item.OpenRisks = append([]string(nil), item.OpenRisks...)
	item.SourceSessions = append([]string(nil), item.SourceSessions...)
	item.Evidence = cloneEvidence(item.Evidence)
	return item
}
func cloneTimeline(item TimelineEvent) TimelineEvent {
	item.Evidence = cloneEvidence(item.Evidence)
	item.DecisionIDs = append([]string(nil), item.DecisionIDs...)
	item.OpenLoopIDs = append([]string(nil), item.OpenLoopIDs...)
	return item
}
func cloneDecision(item Decision) Decision {
	item.Tags = append([]string(nil), item.Tags...)
	item.Supersedes = append([]string(nil), item.Supersedes...)
	item.SourceSessions = append([]string(nil), item.SourceSessions...)
	item.Evidence = cloneEvidence(item.Evidence)
	item.Alternatives = append([]string(nil), item.Alternatives...)
	item.RejectedPaths = append([]string(nil), item.RejectedPaths...)
	return item
}
func cloneOpenLoop(item OpenLoop) OpenLoop {
	item.Tags = append([]string(nil), item.Tags...)
	item.SourceSessions = append([]string(nil), item.SourceSessions...)
	item.Evidence = cloneEvidence(item.Evidence)
	item.Attempts = append([]string(nil), item.Attempts...)
	return item
}
func cloneSession(item SessionReport) SessionReport {
	item.GoalChanges = append([]string(nil), item.GoalChanges...)
	item.Phases = append([]SessionPhase(nil), item.Phases...)
	for i := range item.Phases {
		item.Phases[i].Evidence = cloneEvidence(item.Phases[i].Evidence)
	}
	item.Files = append([]string(nil), item.Files...)
	item.Commits = append([]string(nil), item.Commits...)
	item.Verification = append([]string(nil), item.Verification...)
	item.DecisionsAdded = append([]string(nil), item.DecisionsAdded...)
	item.DecisionsRevised = append([]string(nil), item.DecisionsRevised...)
	item.OpenLoopsCreated = append([]string(nil), item.OpenLoopsCreated...)
	item.OpenLoopsClosed = append([]string(nil), item.OpenLoopsClosed...)
	item.Evidence = cloneEvidence(item.Evidence)
	if item.Accounting != nil {
		copyAccounting := *item.Accounting
		copyAccounting.Models = append([]accounting.ModelAccounting(nil), item.Accounting.Models...)
		item.Accounting = &copyAccounting
	}
	return item
}

// Apply preflights the complete plan, revalidates each target immediately
// before its write, writes in bytewise path order, and skips byte-identical
// files. It does not provide filesystem-wide linearizability: an uncooperative
// mutation can still race the final check and atomic replacement.
func Apply(plan WritePlan) ([]string, error) {
	return applyWithRootOptions(plan, rootOpenOptions{})
}

// ApplyExpected writes through a project root only when the handle opened by
// this operation still has the caller's pinned identity.
func ApplyExpected(plan WritePlan, expectedRoot os.FileInfo) ([]string, error) {
	return applyWithRootOptions(plan, rootOpenOptions{expectedRoot: expectedRoot})
}

type applyHooks struct {
	beforeWrite func(index int, file PlannedFile) error
}

func applyWithHooks(plan WritePlan, hooks applyHooks) ([]string, error) {
	return applyWithRootOptionsAndHooks(plan, rootOpenOptions{}, hooks)
}

func applyWithRootOptions(plan WritePlan, options rootOpenOptions) ([]string, error) {
	return applyWithRootOptionsAndHooks(plan, options, applyHooks{})
}

func applyWithRootOptionsAndHooks(plan WritePlan, options rootOpenOptions, hooks applyHooks) ([]string, error) {
	if plan.ProjectRoot == "" {
		return nil, errors.New("write plan has no project root")
	}
	directory, err := openLedgerProjectRoot(plan.ProjectRoot, options)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	files := append([]PlannedFile(nil), plan.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	type prepared struct {
		file PlannedFile
		skip bool
	}
	ready := make([]prepared, 0, len(files))
	seen := map[string]struct{}{}
	for _, file := range files {
		if err := validateLedgerRelativePath(file.RelativePath); err != nil {
			return nil, err
		}
		if _, duplicate := seen[file.RelativePath]; duplicate {
			return nil, fmt.Errorf("duplicate planned path %q", file.RelativePath)
		}
		seen[file.RelativePath] = struct{}{}
		if file.Perm.Perm() != file.Perm || file.Perm == 0 {
			return nil, fmt.Errorf("invalid mode for %s", file.RelativePath)
		}
		if int64(len(file.Data)) > maximumPlannedFileBytes(file.RelativePath) || !utf8.Valid(file.Data) {
			return nil, fmt.Errorf("invalid document bytes for %s", file.RelativePath)
		}
		skip, err := validatePlannedTarget(directory, file)
		if err != nil {
			return nil, err
		}
		ready = append(ready, prepared{file: file, skip: skip})
	}
	// Directory creation begins only after every target and expected byte string
	// has passed preflight.
	for _, item := range ready {
		if item.skip {
			continue
		}
		if err := ensureSafeParents(directory, filepath.Dir(filepath.FromSlash(item.file.RelativePath)), 0o755); err != nil {
			return nil, err
		}
	}
	var written []string
	for index, item := range ready {
		if item.skip {
			continue
		}
		if hooks.beforeWrite != nil {
			if err := hooks.beforeWrite(index, item.file); err != nil {
				return written, err
			}
		}
		// This closes the observable preflight-to-write window for later files.
		// The apply engine calls this while holding the project lock and surrounds
		// the write plan with its prepared/applied receipt recovery protocol.
		skip, err := validatePlannedTarget(directory, item.file)
		if err != nil {
			return written, err
		}
		if skip {
			continue
		}
		if err := atomicfile.WriteRoot(directory.Root, filepath.FromSlash(item.file.RelativePath), item.file.Data, item.file.Perm); err != nil {
			return written, err
		}
		written = append(written, item.file.RelativePath)
	}
	return written, nil
}

func validatePlannedTarget(directory *pathguard.Directory, file PlannedFile) (bool, error) {
	current, currentPerm, readErr := readPlannedRegular(directory, file.RelativePath)
	if readErr == nil {
		// Expectations describe the exact snapshot Render consumed. Validate
		// them before treating desired bytes as an idempotent no-op.
		if !file.ExpectedExists || !bytes.Equal(current, file.ExpectedData) || currentPerm != file.ExpectedPerm {
			return false, fmt.Errorf("ledger file %s changed after render", file.RelativePath)
		}
		return bytes.Equal(current, file.Data), nil
	}
	if errors.Is(readErr, os.ErrNotExist) || safelyMissingTargetParent(directory, file.RelativePath) {
		if file.ExpectedExists {
			return false, fmt.Errorf("ledger file %s disappeared after render", file.RelativePath)
		}
		return false, nil
	}
	return false, readErr
}

func readPlannedRegular(directory *pathguard.Directory, relative string) ([]byte, fs.FileMode, error) {
	maximum := maximumPlannedFileBytes(relative)
	if maximum == MaxDocumentBytes {
		return readLedgerRegular(directory, relative, false)
	}
	file, info, err := directory.OpenRegular(relative)
	if err != nil {
		return nil, 0, err
	}
	if err := file.Close(); err != nil {
		return nil, 0, err
	}
	if info.Size() < 0 || info.Size() > maximum {
		return nil, 0, invalidDocument("document exceeds size limit")
	}
	body, err := pathguard.ReadStableRegularRootFile(directory.Root, relative, info, maximum)
	if err != nil {
		return nil, 0, errors.New("ledger file changed while reading")
	}
	return body, info.Mode().Perm(), nil
}

func safelyMissingTargetParent(directory *pathguard.Directory, relative string) bool {
	if directory == nil {
		return false
	}
	parent := filepath.Dir(filepath.FromSlash(relative))
	guarded, _, err := directory.OpenDirectory(parent)
	if guarded != nil {
		_ = guarded.Close()
	}
	return errors.Is(err, os.ErrNotExist)
}

func validateLedgerRelativePath(relative string) error {
	if relative == "" || strings.Contains(relative, "\\") || filepath.IsAbs(relative) || filepath.Clean(filepath.FromSlash(relative)) != filepath.FromSlash(relative) || strings.HasPrefix(relative, "../") || relative == ".." {
		return errors.New("invalid ledger relative path")
	}
	if relative == ledgerRootRelative+"/current-state.md" || relative == ledgerRootRelative+"/evolution-timeline.md" {
		return nil
	}
	if relative == ledgerRootRelative+"/project-overview.md" {
		return nil
	}
	if relative == v2ReviewPath || relative == v2HistoryPath || relative == v2MachineLedgerPath {
		return nil
	}
	if IsStandaloneDerivedPath(relative) {
		return nil
	}
	for _, dir := range []string{"decisions", "open-loops", "sessions"} {
		prefix := ledgerRootRelative + "/" + dir + "/"
		if strings.HasPrefix(relative, prefix) {
			name := strings.TrimSuffix(strings.TrimPrefix(relative, prefix), ".md")
			if strings.HasSuffix(relative, ".md") && stableLedgerID.MatchString(name) && !strings.Contains(name, "/") {
				return nil
			}
		}
	}
	return errors.New("path is outside durable ledger documents")
}

func maximumPlannedFileBytes(relative string) int64 {
	if relative == v2MachineLedgerPath {
		return v2MachineLedgerBytes
	}
	return MaxDocumentBytes
}

func ensureSafeParents(directory *pathguard.Directory, relative string, perm fs.FileMode) error {
	return ensureSafeParentsWith(directory, relative, perm, atomicfile.EnsureRootDir)
}

func ensureSafeParentsWith(directory *pathguard.Directory, relative string, perm fs.FileMode, ensure func(*os.Root, string, fs.FileMode) error) error {
	if relative == "." {
		return nil
	}
	root := directory.Root
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	current := ""
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errors.New("invalid ledger parent")
		}
		if current == "" {
			current = part
		} else {
			current = filepath.Join(current, part)
		}
		before, err := root.Lstat(current)
		missing := errors.Is(err, os.ErrNotExist)
		if err != nil && !missing {
			return errors.New("ledger parent is redirected or not a directory")
		}
		if !missing && !before.IsDir() {
			return errors.New("ledger parent is redirected or not a directory")
		}
		if err := ensure(root, current, perm); err != nil {
			return err
		}
		info, err := root.Lstat(current)
		if err != nil || !info.IsDir() {
			return errors.New("ledger parent is redirected or not a directory")
		}
		if before != nil && !os.SameFile(before, info) {
			return errors.New("ledger parent changed while ensuring durability")
		}
		guarded, guardInfo, guardErr := directory.OpenDirectory(current)
		if guardErr != nil {
			return errors.New("ledger parent is redirected or not a directory")
		}
		_ = guarded.Close()
		if !os.SameFile(info, guardInfo) {
			return errors.New("ledger parent changed while validating")
		}
	}
	return nil
}
