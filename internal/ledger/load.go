package ledger

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/pathguard"
	"gopkg.in/yaml.v3"
)

const ledgerRootRelative = "docs/session-review"

var (
	stableLedgerID  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	stableProjectID = regexp.MustCompile(`^project-[0-9a-f]{16}$`)
)

// Load reads the accepted Markdown ledger without following any redirect. It
// returns no partial state when any durable document is unsafe or malformed.
func Load(projectRoot string) (State, error) {
	directory, err := pathguard.Open(projectRoot)
	if err != nil {
		return State{}, fmt.Errorf("open project root: %w", err)
	}
	defer directory.Close()

	overviewPath := ledgerRootRelative + "/project-overview.md"
	overview, _, err := readLedgerRegular(directory, overviewPath, true)
	if err != nil {
		return State{}, fmt.Errorf("load project overview: %w", err)
	}
	projectID, err := parseOverviewProjectID(overview)
	if err != nil {
		return State{}, err
	}

	state := State{
		ProjectID:   projectID,
		projectRoot: directory.Path,
		Decisions:   make(map[string]Decision),
		OpenLoops:   make(map[string]OpenLoop),
		Sessions:    make(map[string]SessionReport),
		documents: stateDocuments{
			decisions: make(map[string]loadedDocument),
			openLoops: make(map[string]loadedDocument),
			sessions:  make(map[string]loadedDocument),
		},
	}
	ids := map[string]string{"current-state": "reserved current state", "evolution-timeline": "reserved timeline"}
	sessionIDs := make(map[string]string)

	if loaded, found, err := loadDocument(directory, ledgerRootRelative+"/current-state.md"); err != nil {
		return State{}, err
	} else if found {
		current, err := decodeCurrentState(loaded.Document, projectID)
		if err != nil {
			return State{}, fmt.Errorf("load current state: %w", err)
		}
		state.CurrentState = current
		state.documents.current = &loaded
	}

	if loaded, found, err := loadDocument(directory, ledgerRootRelative+"/evolution-timeline.md"); err != nil {
		return State{}, err
	} else if found {
		timeline, err := decodeTimeline(loaded.Document, projectID)
		if err != nil {
			return State{}, fmt.Errorf("load evolution timeline: %w", err)
		}
		for _, event := range timeline {
			if previous, duplicate := ids[event.ID]; duplicate {
				return State{}, fmt.Errorf("duplicate ledger ID %q in timeline and %s", event.ID, previous)
			}
			ids[event.ID] = "timeline"
		}
		state.Timeline = timeline
		state.documents.timeline = &loaded
	}

	classes := []struct {
		dir        string
		entityType string
		consume    func(loadedDocument) error
	}{
		{dir: "decisions", entityType: "decision", consume: func(loaded loadedDocument) error {
			item, err := decodeDecision(loaded.Document, projectID)
			if err != nil {
				return err
			}
			state.Decisions[item.ID] = item
			state.documents.decisions[item.ID] = loaded
			return nil
		}},
		{dir: "open-loops", entityType: "open_loop", consume: func(loaded loadedDocument) error {
			item, err := decodeOpenLoop(loaded.Document, projectID)
			if err != nil {
				return err
			}
			state.OpenLoops[item.ID] = item
			state.documents.openLoops[item.ID] = loaded
			return nil
		}},
		{dir: "sessions", entityType: "session", consume: func(loaded loadedDocument) error {
			item, err := decodeSession(loaded.Document, projectID)
			if err != nil {
				return err
			}
			if previous, duplicate := sessionIDs[item.SessionID]; duplicate {
				return fmt.Errorf("duplicate session ID %q in %q and %q", item.SessionID, previous, item.ID)
			}
			sessionIDs[item.SessionID] = item.ID
			state.Sessions[item.ID] = item
			state.documents.sessions[item.ID] = loaded
			return nil
		}},
	}
	for _, class := range classes {
		relativeDir := ledgerRootRelative + "/" + class.dir
		entries, err := readLedgerDirectory(directory, relativeDir)
		if err != nil {
			return State{}, fmt.Errorf("enumerate %s: %w", class.dir, err)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			relative := relativeDir + "/" + entry.Name()
			loaded, found, err := loadDocument(directory, relative)
			if err != nil || !found {
				if err == nil {
					err = errors.New("ledger entry disappeared")
				}
				return State{}, fmt.Errorf("load %s: %w", relative, err)
			}
			entityType, err := requiredString(&loaded.Document.Frontmatter, "entity_type")
			if err != nil || entityType != class.entityType {
				return State{}, fmt.Errorf("%s has mismatched entity class", relative)
			}
			id, err := requiredString(&loaded.Document.Frontmatter, "id")
			if err != nil || !stableLedgerID.MatchString(id) || entry.Name() != id+".md" {
				return State{}, fmt.Errorf("%s has mismatched or invalid ID", relative)
			}
			if previous, duplicate := ids[id]; duplicate {
				return State{}, fmt.Errorf("duplicate ledger ID %q in %s and %s", id, previous, relative)
			}
			ids[id] = relative
			if err := class.consume(loaded); err != nil {
				return State{}, fmt.Errorf("decode %s: %w", relative, err)
			}
		}
	}
	return state, nil
}

func parseOverviewProjectID(src []byte) (string, error) {
	if len(src) > MaxDocumentBytes {
		return "", invalidDocument("project overview exceeds size limit")
	}
	if !utf8.Valid(src) {
		return "", invalidDocument("project overview is not valid UTF-8")
	}
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	frontmatter, _, err := splitFrontmatter(text)
	if err != nil {
		return "", fmt.Errorf("invalid project overview: %w", err)
	}
	mapping, err := decodeFrontmatter(frontmatter)
	if err != nil {
		return "", fmt.Errorf("invalid project overview: %w", err)
	}
	if err := validateYAMLNode(mapping, 1, &yamlStats{}); err != nil {
		return "", fmt.Errorf("invalid project overview: %w", err)
	}
	projectID, err := requiredString(mapping, "project_id")
	if err != nil || !stableProjectID.MatchString(projectID) {
		return "", invalidDocument("invalid project overview project_id")
	}
	return projectID, nil
}

func readLedgerRegular(directory *pathguard.Directory, relative string, required bool) ([]byte, fs.FileMode, error) {
	file, info, err := directory.OpenRegular(relative)
	if err != nil {
		if !required && errors.Is(err, os.ErrNotExist) {
			return nil, 0, os.ErrNotExist
		}
		return nil, 0, err
	}
	defer file.Close()
	if info.Size() > MaxDocumentBytes {
		return nil, 0, invalidDocument("document exceeds size limit")
	}
	body, err := io.ReadAll(io.LimitReader(file, MaxDocumentBytes+1))
	if err != nil {
		return nil, 0, err
	}
	if len(body) > MaxDocumentBytes {
		return nil, 0, invalidDocument("document exceeds size limit")
	}
	after, err := directory.Root.Lstat(filepath.FromSlash(relative))
	if err != nil || !os.SameFile(info, after) {
		return nil, 0, errors.New("ledger file changed while reading")
	}
	return body, info.Mode().Perm(), nil
}

func loadDocument(directory *pathguard.Directory, relative string) (loadedDocument, bool, error) {
	body, perm, err := readLedgerRegular(directory, relative, false)
	if errors.Is(err, os.ErrNotExist) {
		return loadedDocument{}, false, nil
	}
	if err != nil {
		return loadedDocument{}, false, err
	}
	doc, err := ParseDocument(body)
	if err != nil {
		return loadedDocument{}, false, err
	}
	return loadedDocument{Document: doc, RelativePath: relative, Original: append([]byte(nil), body...), Perm: perm}, true, nil
}

func readLedgerDirectory(directory *pathguard.Directory, relative string) ([]os.DirEntry, error) {
	subdirectory, err := pathguard.Open(filepath.Join(directory.Path, filepath.FromSlash(relative)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("ledger directory is redirected or not a directory")
	}
	defer subdirectory.Close()
	file, err := subdirectory.Root.Open(".")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(subdirectory.Info(), opened) {
		return nil, errors.New("ledger directory changed while opening")
	}
	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	after, err := subdirectory.Root.Stat(".")
	if err != nil || !os.SameFile(opened, after) {
		return nil, errors.New("ledger directory changed while reading")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func decodeCurrentState(doc Document, projectID string) (CurrentState, error) {
	if err := validateExistingReservedFields(&doc.Frontmatter); err != nil {
		return CurrentState{}, err
	}
	if err := requireIdentity(doc, "current-state", "current_state", projectID); err != nil {
		return CurrentState{}, err
	}
	revision, _ := requiredRevision(&doc.Frontmatter)
	var item CurrentState
	item.ProjectID, item.Revision = projectID, revision
	if err := decodeOptionalField(&doc.Frontmatter, "source_sessions", &item.SourceSessions); err != nil {
		return CurrentState{}, err
	}
	if err := decodeOptionalField(&doc.Frontmatter, "evidence", &item.Evidence); err != nil {
		return CurrentState{}, err
	}
	item.Goal = sectionText(doc, "Current goal")
	item.LastVerified = sectionText(doc, "Last verified state")
	item.Branch = sectionText(doc, "Repository")
	item.UncommittedChanges = sectionList(doc, "Uncommitted changes")
	item.Blockers = sectionList(doc, "Blockers")
	item.OpenRisks = sectionList(doc, "Open risks")
	item.NextAction = sectionText(doc, "Next action")
	item.FirstInspection = sectionText(doc, "First inspection")
	item.LastUpdated = sectionText(doc, "Last updated")
	if item.SourceSessions == nil || item.Evidence == nil {
		return CurrentState{}, errors.New("current state omits required arrays")
	}
	if err := validateUniqueNonempty(item.SourceSessions, "current state source sessions"); err != nil {
		return CurrentState{}, err
	}
	if err := validateLedgerEvidence(item.Evidence); err != nil {
		return CurrentState{}, err
	}
	return item, nil
}

func decodeTimeline(doc Document, projectID string) ([]TimelineEvent, error) {
	if err := validateExistingReservedFields(&doc.Frontmatter); err != nil {
		return nil, err
	}
	if err := requireIdentity(doc, "evolution-timeline", "timeline", projectID); err != nil {
		return nil, err
	}
	var timeline []TimelineEvent
	if err := decodeOptionalField(&doc.Frontmatter, "events", &timeline); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(timeline))
	for _, event := range timeline {
		if !stableLedgerID.MatchString(event.ID) || !positiveSafeRevision(event.Revision) || !validLedgerFactClass(event.Class) || !validLedgerTime(event.OccurredAt) || strings.TrimSpace(event.Title) == "" || event.Evidence == nil || event.DecisionIDs == nil || event.OpenLoopIDs == nil {
			return nil, errors.New("invalid timeline event identity")
		}
		if _, duplicate := seen[event.ID]; duplicate {
			return nil, fmt.Errorf("duplicate timeline event %q", event.ID)
		}
		seen[event.ID] = struct{}{}
		if err := validateLedgerEvidence(event.Evidence); err != nil {
			return nil, err
		}
		if err := validateUniqueStable(event.DecisionIDs, "timeline decision IDs"); err != nil {
			return nil, err
		}
		if err := validateUniqueStable(event.OpenLoopIDs, "timeline open-loop IDs"); err != nil {
			return nil, err
		}
	}
	sort.Slice(timeline, func(i, j int) bool {
		if timeline[i].OccurredAt != timeline[j].OccurredAt {
			return timeline[i].OccurredAt < timeline[j].OccurredAt
		}
		return timeline[i].ID < timeline[j].ID
	})
	return timeline, nil
}

func decodeDecision(doc Document, projectID string) (Decision, error) {
	if err := validateExistingReservedFields(&doc.Frontmatter); err != nil {
		return Decision{}, err
	}
	id, revision, err := entityIdentity(doc, "decision", projectID)
	if err != nil {
		return Decision{}, err
	}
	item := Decision{ID: id, ProjectID: projectID, Revision: revision}
	for _, field := range []struct {
		name   string
		target any
	}{
		{"title", &item.Title}, {"status", &item.Status}, {"tags", &item.Tags}, {"supersedes", &item.Supersedes}, {"source_sessions", &item.SourceSessions}, {"evidence", &item.Evidence},
	} {
		if err := decodeOptionalField(&doc.Frontmatter, field.name, field.target); err != nil {
			return Decision{}, err
		}
	}
	item.Context = sectionText(doc, "Context")
	item.Alternatives = sectionList(doc, "Alternatives")
	item.Rationale = sectionText(doc, "Rationale")
	item.RejectedPaths = sectionList(doc, "Rejected paths")
	item.Consequences = sectionText(doc, "Consequences")
	item.ReevaluateWhen = sectionText(doc, "Conditions for reevaluation")
	if strings.TrimSpace(item.Title) == "" || !validDecisionStatus(item.Status) || item.Tags == nil || item.Supersedes == nil || item.SourceSessions == nil || item.Evidence == nil || item.Alternatives == nil || item.RejectedPaths == nil {
		return Decision{}, errors.New("decision is incomplete or invalid")
	}
	if err := validateUniqueNonempty(item.Tags, "decision tags"); err != nil {
		return Decision{}, err
	}
	if err := validateUniqueNonempty(item.SourceSessions, "decision source sessions"); err != nil {
		return Decision{}, err
	}
	if err := validateUniqueStable(item.Supersedes, "decision supersedes"); err != nil {
		return Decision{}, err
	}
	if err := validateLedgerEvidence(item.Evidence); err != nil {
		return Decision{}, err
	}
	return item, nil
}

func decodeOpenLoop(doc Document, projectID string) (OpenLoop, error) {
	if err := validateExistingReservedFields(&doc.Frontmatter); err != nil {
		return OpenLoop{}, err
	}
	id, revision, err := entityIdentity(doc, "open_loop", projectID)
	if err != nil {
		return OpenLoop{}, err
	}
	item := OpenLoop{ID: id, ProjectID: projectID, Revision: revision}
	for _, field := range []struct {
		name   string
		target any
	}{
		{"title", &item.Title}, {"status", &item.Status}, {"tags", &item.Tags}, {"source_sessions", &item.SourceSessions}, {"evidence", &item.Evidence},
	} {
		if err := decodeOptionalField(&doc.Frontmatter, field.name, field.target); err != nil {
			return OpenLoop{}, err
		}
	}
	item.Question = sectionText(doc, "Question")
	item.Attempts = sectionList(doc, "Attempted paths")
	item.Blocker = sectionText(doc, "Blocking condition")
	item.NextExperiment = sectionText(doc, "Recommended next experiment")
	item.CompletionCriterion = sectionText(doc, "Completion criterion")
	if strings.TrimSpace(item.Title) == "" || !validLoopStatus(item.Status) || item.Tags == nil || item.SourceSessions == nil || item.Evidence == nil || item.Attempts == nil {
		return OpenLoop{}, errors.New("open loop is incomplete or invalid")
	}
	if err := validateUniqueNonempty(item.Tags, "open-loop tags"); err != nil {
		return OpenLoop{}, err
	}
	if err := validateUniqueNonempty(item.SourceSessions, "open-loop source sessions"); err != nil {
		return OpenLoop{}, err
	}
	if err := validateLedgerEvidence(item.Evidence); err != nil {
		return OpenLoop{}, err
	}
	return item, nil
}

func decodeSession(doc Document, projectID string) (SessionReport, error) {
	if err := validateExistingReservedFields(&doc.Frontmatter); err != nil {
		return SessionReport{}, err
	}
	id, revision, err := entityIdentity(doc, "session", projectID)
	if err != nil {
		return SessionReport{}, err
	}
	item := SessionReport{ID: id, ProjectID: projectID, Revision: revision}
	for _, field := range []struct {
		name   string
		target any
	}{
		{"session_id", &item.SessionID}, {"initial_goal", &item.InitialGoal}, {"goal_changes", &item.GoalChanges}, {"phases", &item.Phases}, {"files", &item.Files}, {"commits", &item.Commits}, {"verification", &item.Verification}, {"decisions_added", &item.DecisionsAdded}, {"decisions_revised", &item.DecisionsRevised}, {"open_loops_created", &item.OpenLoopsCreated}, {"open_loops_closed", &item.OpenLoopsClosed}, {"previous_session_id", &item.PreviousSessionID}, {"next_session_id", &item.NextSessionID}, {"evidence", &item.Evidence},
	} {
		if err := decodeOptionalField(&doc.Frontmatter, field.name, field.target); err != nil {
			return SessionReport{}, err
		}
	}
	if strings.TrimSpace(item.SessionID) == "" || item.GoalChanges == nil || item.Phases == nil || item.Files == nil || item.Commits == nil || item.Verification == nil || item.DecisionsAdded == nil || item.DecisionsRevised == nil || item.OpenLoopsCreated == nil || item.OpenLoopsClosed == nil || item.Evidence == nil {
		return SessionReport{}, errors.New("session report is incomplete or invalid")
	}
	for name, values := range map[string][]string{"goal changes": item.GoalChanges, "files": item.Files, "commits": item.Commits, "verification": item.Verification} {
		if err := validateUnique(values, "session "+name, false); err != nil {
			return SessionReport{}, err
		}
	}
	for name, values := range map[string][]string{"decisions added": item.DecisionsAdded, "decisions revised": item.DecisionsRevised, "open loops created": item.OpenLoopsCreated, "open loops closed": item.OpenLoopsClosed} {
		if err := validateUniqueStable(values, "session "+name); err != nil {
			return SessionReport{}, err
		}
	}
	if err := validateLedgerEvidence(item.Evidence); err != nil {
		return SessionReport{}, err
	}
	for _, phase := range item.Phases {
		if strings.TrimSpace(phase.Title) == "" || phase.Evidence == nil {
			return SessionReport{}, errors.New("invalid session phase")
		}
		if err := validateLedgerEvidence(phase.Evidence); err != nil {
			return SessionReport{}, err
		}
	}
	return item, nil
}

func requireIdentity(doc Document, id, entityType, projectID string) error {
	gotID, err := requiredString(&doc.Frontmatter, "id")
	if err != nil || gotID != id {
		return errors.New("document ID mismatch")
	}
	gotType, err := requiredString(&doc.Frontmatter, "entity_type")
	if err != nil || gotType != entityType {
		return errors.New("document entity type mismatch")
	}
	gotProject, err := requiredString(&doc.Frontmatter, "project_id")
	if err != nil || gotProject != projectID {
		return errors.New("document project mismatch")
	}
	return nil
}

func entityIdentity(doc Document, entityType, projectID string) (string, int, error) {
	id, err := requiredString(&doc.Frontmatter, "id")
	if err != nil || !stableLedgerID.MatchString(id) {
		return "", 0, errors.New("invalid entity ID")
	}
	if err := requireIdentity(doc, id, entityType, projectID); err != nil {
		return "", 0, err
	}
	revision, err := requiredRevision(&doc.Frontmatter)
	return id, revision, err
}

func decodeOptionalField(mapping *yaml.Node, name string, target any) error {
	node, exists := mappingValue(mapping, name)
	if !exists {
		return nil
	}
	if node.Kind == yaml.AliasNode || node.Tag == "!!null" {
		return fmt.Errorf("invalid %s", name)
	}
	if err := node.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", name, err)
	}
	return nil
}

func sectionText(doc Document, name string) string {
	for _, section := range doc.Sections {
		if section.Name == name {
			return strings.TrimSpace(section.Body)
		}
	}
	return ""
}

func sectionList(doc Document, name string) []string {
	var text string
	found := false
	for _, section := range doc.Sections {
		if section.Name == name {
			text = strings.TrimSpace(section.Body)
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	if text == "" {
		return []string{}
	}
	var values []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") {
			values = append(values, strings.TrimSpace(strings.TrimPrefix(line, "- ")))
		}
	}
	return values
}

func positiveSafeRevision(value int) bool { return value >= 1 && value <= 1<<53-1 }

func validDecisionStatus(status string) bool {
	switch status {
	case "proposed", "accepted", "superseded", "archived":
		return true
	default:
		return false
	}
}

func validLoopStatus(status string) bool {
	switch status {
	case "open", "blocked", "resolved", "abandoned", "archived":
		return true
	default:
		return false
	}
}

func validLedgerFactClass(class FactClass) bool {
	switch class {
	case Verified, DecisionFact, Inference, Superseded, PendingConfirmation:
		return true
	default:
		return false
	}
}

func validLedgerTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Format(time.RFC3339Nano) == value
}

func validateUnique(values []string, label string, nonempty bool) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if nonempty && strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s contains empty value", label)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%s contains duplicate %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateUniqueNonempty(values []string, label string) error {
	return validateUnique(values, label, true)
}

func validateUniqueStable(values []string, label string) error {
	if err := validateUniqueNonempty(values, label); err != nil {
		return err
	}
	for _, value := range values {
		if !stableLedgerID.MatchString(value) {
			return fmt.Errorf("%s contains invalid ID %q", label, value)
		}
	}
	return nil
}

func validateLedgerEvidence(values []EvidenceRef) error {
	seen := make(map[string]struct{}, len(values))
	for _, ref := range values {
		if !stableLedgerID.MatchString(ref.EvidenceID) || strings.TrimSpace(ref.SessionID) == "" || !positiveSafeRevision(ref.JSONLLine) || !lowercaseSHA256.MatchString(ref.SourceHash) {
			return fmt.Errorf("invalid evidence reference %q", ref.EvidenceID)
		}
		if _, duplicate := seen[ref.EvidenceID]; duplicate {
			return fmt.Errorf("duplicate evidence reference %q", ref.EvidenceID)
		}
		seen[ref.EvidenceID] = struct{}{}
	}
	return nil
}
