package recovery

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/ledger"
)

const (
	maxRecoveryMarkdownBytes = ledger.MaxDocumentBytes
	maxRecoveryEntities      = 20_000
	maxRecoveryValues        = 100_000
	maxEntityLookaheadBytes  = 64
	recoveryOmissionMarkdown = "# Recovery output omitted\n\nRecovery output omitted because it exceeds the safe size limit.\n"
)

var errRecoveryOutputLimit = errors.New("recovery view exceeds safe size limit")

type ResumeCard struct {
	ProjectID       string
	Goal            string
	StopPoint       string
	LastVerified    string
	Drift           []string
	Blockers        []string
	OpenQuestions   []string
	NextAction      string
	FirstInspection string
	SourceSessions  []string
}

// ResumeLedgerOnly derives a recovery card exclusively from accepted ledger
// Markdown. ledger.Load is the sole project-state input boundary.
func ResumeLedgerOnly(projectRoot string) (ResumeCard, error) {
	state, err := ledger.Load(projectRoot)
	return resumeLedgerOnly(state, err)
}

// ResumeLedgerOnlyExpected derives the same accepted-only recovery card while
// requiring the project root opened by ledger.LoadExpected to retain a caller-
// pinned filesystem identity.
func ResumeLedgerOnlyExpected(projectRoot string, expectedRoot os.FileInfo) (ResumeCard, error) {
	state, err := ledger.LoadExpected(projectRoot, expectedRoot)
	return resumeLedgerOnly(state, err)
}

func resumeLedgerOnly(state ledger.State, err error) (ResumeCard, error) {
	if err != nil {
		return ResumeCard{}, err
	}
	if err := validateRecoveryState(state); err != nil {
		return ResumeCard{}, err
	}

	card := ResumeCard{
		ProjectID:       state.ProjectID,
		Goal:            state.CurrentState.Goal,
		LastVerified:    state.CurrentState.LastVerified,
		Drift:           stableUnique(appendCopy(state.CurrentState.UncommittedChanges, state.CurrentState.OpenRisks...)),
		Blockers:        stableUnique(state.CurrentState.Blockers),
		NextAction:      state.CurrentState.NextAction,
		FirstInspection: state.CurrentState.FirstInspection,
		SourceSessions:  sortedUnique(state.CurrentState.SourceSessions),
	}

	latest, found, err := latestAcceptedSession(state.Sessions)
	if err != nil {
		return ResumeCard{}, err
	}
	if found && len(latest.Phases) != 0 {
		phase := latest.Phases[len(latest.Phases)-1]
		card.StopPoint = strings.TrimSpace(phase.Summary)
		if card.StopPoint == "" {
			card.StopPoint = strings.TrimSpace(phase.Title)
		}
	}
	for _, loop := range state.OpenLoops {
		if unresolvedLoop(loop.Status) {
			card.OpenQuestions = append(card.OpenQuestions, loop.Title)
		}
	}
	card.OpenQuestions = sortedUnique(card.OpenQuestions)
	return card, nil
}

// Markdown returns compact, deterministic Markdown. Arbitrary callers can
// construct a card, so the renderer itself also fails closed on output growth.
func (card ResumeCard) Markdown() string {
	out := newRecoveryMarkdownBuilder()
	if !out.raw("# Resume\n\n") {
		return out.finish()
	}
	if !out.field("Project", card.ProjectID) || !out.field("Goal", card.Goal) || !out.field("Stop point", card.StopPoint) || !out.field("Last verified", card.LastVerified) {
		return out.finish()
	}
	if !out.section("Drift", card.Drift) || !out.section("Blockers", card.Blockers) || !out.section("Open questions", card.OpenQuestions) {
		return out.finish()
	}
	if !out.field("Next action", card.NextAction) || !out.field("First inspection", card.FirstInspection) || !out.section("Source sessions", card.SourceSessions) {
		return out.finish()
	}
	return out.finish()
}

func latestAcceptedSession(sessions map[string]ledger.SessionReport) (ledger.SessionReport, bool, error) {
	if len(sessions) == 0 {
		return ledger.SessionReport{}, false, nil
	}
	bySessionID := make(map[string]ledger.SessionReport, len(sessions))
	ids := make([]string, 0, len(sessions))
	for _, report := range sessions {
		bySessionID[report.SessionID] = report
		ids = append(ids, report.SessionID)
	}
	sort.Strings(ids)
	for _, sessionID := range ids {
		report := bySessionID[sessionID]
		if report.PreviousSessionID != "" {
			if _, found := bySessionID[report.PreviousSessionID]; !found {
				return ledger.SessionReport{}, false, fmt.Errorf("session %q references missing previous session %q", sessionID, report.PreviousSessionID)
			}
		}
		if report.NextSessionID != "" {
			if _, found := bySessionID[report.NextSessionID]; !found {
				return ledger.SessionReport{}, false, fmt.Errorf("session %q references missing next session %q", sessionID, report.NextSessionID)
			}
		}
	}
	for _, sessionID := range ids {
		report := bySessionID[sessionID]
		if report.PreviousSessionID != "" {
			previous := bySessionID[report.PreviousSessionID]
			if previous.NextSessionID != sessionID {
				return ledger.SessionReport{}, false, fmt.Errorf("session chain link %q -> %q is not reciprocal", report.PreviousSessionID, sessionID)
			}
		}
		if report.NextSessionID != "" {
			next := bySessionID[report.NextSessionID]
			if next.PreviousSessionID != sessionID {
				return ledger.SessionReport{}, false, fmt.Errorf("session chain link %q -> %q is not reciprocal", sessionID, report.NextSessionID)
			}
		}
	}

	const (
		unseen uint8 = iota
		visiting
		done
	)
	marks := make(map[string]uint8, len(sessions))
	var visit func(string) error
	visit = func(sessionID string) error {
		switch marks[sessionID] {
		case visiting:
			return fmt.Errorf("accepted session chain contains a cycle at %q", sessionID)
		case done:
			return nil
		}
		marks[sessionID] = visiting
		if next := bySessionID[sessionID].NextSessionID; next != "" {
			if err := visit(next); err != nil {
				return err
			}
		}
		marks[sessionID] = done
		return nil
	}
	for _, sessionID := range ids {
		if err := visit(sessionID); err != nil {
			return ledger.SessionReport{}, false, err
		}
	}

	heads := make([]string, 0, 1)
	terminals := make([]string, 0, 1)
	for _, sessionID := range ids {
		report := bySessionID[sessionID]
		if report.PreviousSessionID == "" {
			heads = append(heads, sessionID)
		}
		if report.NextSessionID == "" {
			terminals = append(terminals, sessionID)
		}
	}
	if len(heads) != 1 || len(terminals) != 1 {
		return ledger.SessionReport{}, false, errors.New("accepted session reports form disconnected or ambiguous chains")
	}
	visited := 0
	for sessionID := heads[0]; sessionID != ""; sessionID = bySessionID[sessionID].NextSessionID {
		visited++
	}
	if visited != len(sessions) {
		return ledger.SessionReport{}, false, errors.New("accepted session reports form disconnected chains")
	}
	return bySessionID[terminals[0]], true, nil
}

func unresolvedLoop(status string) bool {
	return status == "open" || status == "blocked"
}

func validateRecoveryState(state ledger.State) error {
	if state.ProjectID == "" {
		return errors.New("accepted ledger has empty project ID")
	}
	var budget recoveryBudget
	if err := budget.countState(state); err != nil {
		return err
	}
	for key, decision := range state.Decisions {
		if key != decision.ID || decision.ProjectID != state.ProjectID {
			return fmt.Errorf("decision %q has inconsistent accepted identity", key)
		}
	}
	for key, loop := range state.OpenLoops {
		if key != loop.ID || loop.ProjectID != state.ProjectID {
			return fmt.Errorf("open loop %q has inconsistent accepted identity", key)
		}
	}
	for key, report := range state.Sessions {
		if key != report.ID || report.ProjectID != state.ProjectID {
			return fmt.Errorf("session %q has inconsistent accepted identity", key)
		}
	}
	return validateSupersedes(state.Decisions)
}

type recoveryBudget struct {
	entities int
	values   int
}

func (budget *recoveryBudget) addEntities(amount int) error {
	if amount < 0 || budget.entities > maxRecoveryEntities || amount > maxRecoveryEntities-budget.entities {
		return errRecoveryOutputLimit
	}
	budget.entities += amount
	return nil
}

func (budget *recoveryBudget) addValues(amount int) error {
	if amount < 0 || budget.values > maxRecoveryValues || amount > maxRecoveryValues-budget.values {
		return errRecoveryOutputLimit
	}
	budget.values += amount
	return nil
}

func (budget *recoveryBudget) countState(state ledger.State) error {
	for _, amount := range []int{len(state.Timeline), len(state.Decisions), len(state.OpenLoops), len(state.Sessions)} {
		if err := budget.addEntities(amount); err != nil {
			return err
		}
	}
	if err := budget.addValues(1); err != nil { // State.ProjectID.
		return err
	}
	if err := budget.countCurrentState(state.CurrentState); err != nil {
		return err
	}
	for _, event := range state.Timeline {
		if err := budget.addValues(6); err != nil { // ID, time, revision, class, title, summary.
			return err
		}
		for _, amount := range []int{len(event.DecisionIDs), len(event.OpenLoopIDs)} {
			if err := budget.addValues(amount); err != nil {
				return err
			}
		}
		if err := budget.countEvidence(event.Evidence); err != nil {
			return err
		}
	}
	for _, decision := range state.Decisions {
		if err := budget.addValues(9); err != nil { // Scalar and revision fields.
			return err
		}
		for _, amount := range []int{len(decision.Tags), len(decision.Supersedes), len(decision.SourceSessions), len(decision.Alternatives), len(decision.RejectedPaths)} {
			if err := budget.addValues(amount); err != nil {
				return err
			}
		}
		if err := budget.countEvidence(decision.Evidence); err != nil {
			return err
		}
	}
	for _, loop := range state.OpenLoops {
		if err := budget.addValues(9); err != nil { // Scalar and revision fields.
			return err
		}
		for _, amount := range []int{len(loop.Tags), len(loop.SourceSessions), len(loop.Attempts)} {
			if err := budget.addValues(amount); err != nil {
				return err
			}
		}
		if err := budget.countEvidence(loop.Evidence); err != nil {
			return err
		}
	}
	for _, report := range state.Sessions {
		if err := budget.addValues(7); err != nil { // IDs, revision, goal, and links.
			return err
		}
		for _, amount := range []int{
			len(report.GoalChanges), len(report.Files), len(report.Commits), len(report.Verification),
			len(report.DecisionsAdded), len(report.DecisionsRevised), len(report.OpenLoopsCreated), len(report.OpenLoopsClosed),
		} {
			if err := budget.addValues(amount); err != nil {
				return err
			}
		}
		if err := budget.addValues(len(report.Phases)); err != nil { // Phase struct values.
			return err
		}
		for _, phase := range report.Phases {
			if err := budget.addValues(2); err != nil { // Title and summary.
				return err
			}
			if err := budget.countEvidence(phase.Evidence); err != nil {
				return err
			}
		}
		if err := budget.countEvidence(report.Evidence); err != nil {
			return err
		}
	}
	return nil
}

func (budget *recoveryBudget) countCurrentState(state ledger.CurrentState) error {
	if err := budget.addValues(8); err != nil { // Project, revision, and six text fields.
		return err
	}
	for _, amount := range []int{len(state.UncommittedChanges), len(state.Blockers), len(state.OpenRisks), len(state.SourceSessions)} {
		if err := budget.addValues(amount); err != nil {
			return err
		}
	}
	return budget.countEvidence(state.Evidence)
}

func (budget *recoveryBudget) countEvidence(refs []ledger.EvidenceRef) error {
	if err := budget.addValues(len(refs)); err != nil { // EvidenceRef struct values.
		return err
	}
	for range refs {
		if err := budget.addValues(5); err != nil { // Every EvidenceRef scalar field.
			return err
		}
	}
	return nil
}

func validateSupersedes(decisions map[string]ledger.Decision) error {
	const (
		unseen uint8 = iota
		visiting
		done
	)
	marks := make(map[string]uint8, len(decisions))
	var visit func(string) error
	visit = func(id string) error {
		switch marks[id] {
		case visiting:
			return fmt.Errorf("supersedes cycle at decision %q", id)
		case done:
			return nil
		}
		marks[id] = visiting
		predecessors := sortedUnique(decisions[id].Supersedes)
		for _, predecessor := range predecessors {
			if predecessor == id {
				return fmt.Errorf("decision %q supersedes itself", id)
			}
			if _, found := decisions[predecessor]; !found {
				return fmt.Errorf("decision %q supersedes missing decision %q", id, predecessor)
			}
			if err := visit(predecessor); err != nil {
				return err
			}
		}
		marks[id] = done
		return nil
	}
	ids := make([]string, 0, len(decisions))
	for id := range decisions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func appendCopy(first []string, rest ...string) []string {
	result := make([]string, 0, len(first)+len(rest))
	result = append(result, first...)
	return append(result, rest...)
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func stableUnique(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

type recoveryMarkdownBuilder struct {
	text     strings.Builder
	overflow bool
}

func newRecoveryMarkdownBuilder() *recoveryMarkdownBuilder {
	var out recoveryMarkdownBuilder
	out.text.Grow(1024)
	return &out
}

func (out *recoveryMarkdownBuilder) raw(value string) bool {
	if out.overflow || len(value) > maxRecoveryMarkdownBytes-out.text.Len() {
		out.overflow = true
		return false
	}
	out.text.WriteString(value)
	return true
}

func (out *recoveryMarkdownBuilder) escaped(value string) bool {
	if out.overflow {
		return false
	}
	separatorPending := false
	for index := 0; index < len(value); {
		start := index
		r, size := utf8.DecodeRuneInString(value[index:])
		index += size
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			separatorPending = out.text.Len() != 0
			continue
		}
		if separatorPending {
			if !out.raw(" ") {
				return false
			}
			separatorPending = false
		}
		switch r {
		case '\\':
			if !out.raw("\\\\") {
				return false
			}
			continue
		case '&':
			if !out.raw("&amp;") {
				return false
			}
			if end, entityLike := entityLikeTerminator(value, start); entityLike {
				if !out.raw(value[index:end]) {
					return false
				}
				index = end
			}
			continue
		case '<':
			if !out.raw("&lt;") {
				return false
			}
			continue
		case '>':
			if !out.raw("&gt;") {
				return false
			}
			continue
		}
		if isASCIIPunctuation(r) {
			if !out.raw("\\") {
				return false
			}
		}
		var encoded [utf8.UTFMax]byte
		encodedSize := utf8.EncodeRune(encoded[:], r)
		if !out.raw(string(encoded[:encodedSize])) {
			return false
		}
	}
	return true
}

func (out *recoveryMarkdownBuilder) escapedList(values []string, separator string) bool {
	for index, value := range values {
		if out.overflow {
			return false
		}
		if index != 0 {
			if !out.raw(separator) {
				return false
			}
		}
		if !out.escaped(value) {
			return false
		}
	}
	return true
}

func isASCIIPunctuation(r rune) bool {
	return r >= '!' && r <= '/' || r >= ':' && r <= '@' || r >= '[' && r <= '`' || r >= '{' && r <= '~'
}

func entityLikeTerminator(value string, ampersand int) (int, bool) {
	index := ampersand + 1
	if index >= len(value) {
		return 0, false
	}
	limit := index + maxEntityLookaheadBytes
	if limit > len(value) {
		limit = len(value)
	}
	if value[index] == '#' {
		index++
		if index < limit && (value[index] == 'x' || value[index] == 'X') {
			index++
			start := index
			for index < limit && (value[index] >= '0' && value[index] <= '9' || value[index] >= 'a' && value[index] <= 'f' || value[index] >= 'A' && value[index] <= 'F') {
				index++
			}
			return index + 1, index > start && index < limit && value[index] == ';'
		}
		start := index
		for index < limit && value[index] >= '0' && value[index] <= '9' {
			index++
		}
		return index + 1, index > start && index < limit && value[index] == ';'
	}
	if !isASCIIAlpha(rune(value[index])) {
		return 0, false
	}
	for index < limit && (isASCIIAlpha(rune(value[index])) || value[index] >= '0' && value[index] <= '9') {
		index++
	}
	return index + 1, index < limit && value[index] == ';'
}

func isASCIIAlpha(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}

func (out *recoveryMarkdownBuilder) field(name, value string) bool {
	if out.overflow {
		return false
	}
	if strings.TrimSpace(value) == "" {
		return true
	}
	return out.raw("**"+name+":** ") && out.escaped(value) && out.raw("\n\n")
}

func (out *recoveryMarkdownBuilder) section(name string, values []string) bool {
	if out.overflow {
		return false
	}
	if len(values) == 0 {
		return true
	}
	if !out.raw("## " + name + "\n\n") {
		return false
	}
	for _, value := range values {
		if !out.raw("- ") || !out.escaped(value) || !out.raw("\n") {
			return false
		}
	}
	return out.raw("\n")
}

func (out *recoveryMarkdownBuilder) finish() string {
	if out.overflow {
		return recoveryOmissionMarkdown
	}
	return strings.TrimRight(out.text.String(), "\n") + "\n"
}
