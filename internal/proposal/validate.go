package proposal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/redact"
)

const (
	proposalSchemaVersion = 1
	maxProposalBytes      = 4 * 1024 * 1024
	maxSafeInteger        = 1<<53 - 1
	currentStateEntityID  = "current-state"
	stableIDPattern       = `^[a-z0-9][a-z0-9._-]{0,127}$`
)

var (
	lowercaseSHA256  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	prefixedSHA256   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	stableID         = regexp.MustCompile(stableIDPattern)
	redactionWarning = regexp.MustCompile(`^redacted:[a-z0-9_]+:[1-9][0-9]*$`)
)

// Decode reads one closed proposal JSON value. It rejects oversized input,
// duplicate keys, explicit nulls, unknown fields, missing protocol fields, and
// trailing JSON.
func Decode(r io.Reader) (Proposal, error) {
	if r == nil {
		return Proposal{}, errors.New("proposal reader is required")
	}
	b, err := io.ReadAll(io.LimitReader(r, maxProposalBytes+1))
	if err != nil {
		return Proposal{}, fmt.Errorf("read proposal: %w", err)
	}
	if len(b) > maxProposalBytes {
		return Proposal{}, fmt.Errorf("proposal exceeds %d bytes", maxProposalBytes)
	}
	if err := inspectJSON(b); err != nil {
		return Proposal{}, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return Proposal{}, fmt.Errorf("decode proposal envelope: %w", err)
	}
	for _, name := range proposalTopLevelFields {
		if _, ok := raw[name]; !ok {
			return Proposal{}, fmt.Errorf("missing required proposal field %q", name)
		}
	}
	if err := validateRequiredMembers(raw); err != nil {
		return Proposal{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var p Proposal
	if err := dec.Decode(&p); err != nil {
		return Proposal{}, fmt.Errorf("decode proposal: %w", err)
	}
	if err := expectEOF(dec); err != nil {
		return Proposal{}, err
	}
	if err := validateProtocolShape(p); err != nil {
		return Proposal{}, err
	}
	return p, nil
}

var proposalTopLevelFields = []string{
	"schema_version", "project_id", "session_id", "from_cursor", "to_cursor",
	"evidence_packet_sha256", "new_decisions", "updated_decisions", "open_loops",
	"timeline_events", "current_state_patch", "session_report", "evidence_links",
}

var (
	decisionFields      = []string{"id", "project_id", "title", "status", "revision", "tags", "supersedes", "source_sessions", "evidence", "context", "rationale", "consequences", "reevaluate_when", "alternatives", "rejected_paths"}
	openLoopFields      = []string{"id", "project_id", "title", "status", "revision", "tags", "source_sessions", "evidence", "question", "attempts", "blocker", "next_experiment", "completion_criterion"}
	timelineFields      = []string{"id", "occurred_at", "revision", "class", "title", "summary", "evidence", "decision_ids", "open_loop_ids"}
	sessionReportFields = []string{"id", "project_id", "session_id", "revision", "initial_goal", "goal_changes", "phases", "files", "commits", "verification", "decisions_added", "decisions_revised", "open_loops_created", "open_loops_closed", "previous_session_id", "next_session_id", "evidence"}
	evidenceRefFields   = []string{"evidence_id", "session_id", "jsonl_line", "source_hash", "summary"}
)

func validateRequiredMembers(top map[string]json.RawMessage) error {
	decisions, err := rawArray(top["new_decisions"], "new_decisions")
	if err != nil {
		return err
	}
	for _, raw := range decisions {
		object, err := requiredObject(raw, "decision", decisionFields...)
		if err != nil {
			return err
		}
		if err := validateRawEvidenceArray(object["evidence"]); err != nil {
			return err
		}
	}
	patches, err := rawArray(top["updated_decisions"], "updated_decisions")
	if err != nil {
		return err
	}
	for _, raw := range patches {
		object, err := requiredObject(raw, "decision patch", "id", "expected_revision")
		if err != nil {
			return err
		}
		if evidenceRaw, exists := object["evidence"]; exists {
			if err := validateRawEvidenceArray(evidenceRaw); err != nil {
				return err
			}
		}
	}
	loopChanges, err := rawArray(top["open_loops"], "open_loops")
	if err != nil {
		return err
	}
	for _, raw := range loopChanges {
		change, err := requiredObject(raw, "open-loop change", "operation")
		if err != nil {
			return err
		}
		var operation string
		if err := json.Unmarshal(change["operation"], &operation); err != nil {
			return fmt.Errorf("decode open-loop operation: %w", err)
		}
		switch operation {
		case "create":
			entity, exists := change["entity"]
			if !exists {
				return errors.New("open-loop create omits entity")
			}
			object, err := requiredObject(entity, "open loop", openLoopFields...)
			if err != nil {
				return err
			}
			if err := validateRawEvidenceArray(object["evidence"]); err != nil {
				return err
			}
		case "update":
			patchRaw, exists := change["patch"]
			if !exists {
				return errors.New("open-loop update omits patch")
			}
			object, err := requiredObject(patchRaw, "open-loop patch", "id", "expected_revision")
			if err != nil {
				return err
			}
			if evidenceRaw, exists := object["evidence"]; exists {
				if err := validateRawEvidenceArray(evidenceRaw); err != nil {
					return err
				}
			}
		}
	}
	timeline, err := rawArray(top["timeline_events"], "timeline_events")
	if err != nil {
		return err
	}
	for _, raw := range timeline {
		object, err := requiredObject(raw, "timeline event", timelineFields...)
		if err != nil {
			return err
		}
		if err := validateRawEvidenceArray(object["evidence"]); err != nil {
			return err
		}
	}
	current, err := requiredObject(top["current_state_patch"], "current-state patch", "expected_revision")
	if err != nil {
		return err
	}
	if evidenceRaw, exists := current["evidence"]; exists {
		if err := validateRawEvidenceArray(evidenceRaw); err != nil {
			return err
		}
	}
	report, err := requiredObject(top["session_report"], "session report", sessionReportFields...)
	if err != nil {
		return err
	}
	if err := validateRawEvidenceArray(report["evidence"]); err != nil {
		return err
	}
	phases, err := rawArray(report["phases"], "session phases")
	if err != nil {
		return err
	}
	for _, raw := range phases {
		phase, err := requiredObject(raw, "session phase", "title", "summary", "evidence")
		if err != nil {
			return err
		}
		if err := validateRawEvidenceArray(phase["evidence"]); err != nil {
			return err
		}
	}
	links, err := rawArray(top["evidence_links"], "evidence_links")
	if err != nil {
		return err
	}
	for _, raw := range links {
		if _, err := requiredObject(raw, "evidence link", "entity_id", "evidence_id", "relation"); err != nil {
			return err
		}
	}
	return nil
}

func validateRawEvidenceArray(raw json.RawMessage) error {
	refs, err := rawArray(raw, "evidence")
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if _, err := requiredObject(ref, "evidence reference", evidenceRefFields...); err != nil {
			return err
		}
	}
	return nil
}

func requiredObject(raw json.RawMessage, name string, fields ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	for _, field := range fields {
		if _, exists := object[field]; !exists {
			return nil, fmt.Errorf("%s omits required field %q", name, field)
		}
	}
	return object, nil
}

func rawArray(raw json.RawMessage, name string) ([]json.RawMessage, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, fmt.Errorf("%s must be an array", name)
	}
	return values, nil
}

func inspectJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := inspectJSONValue(dec); err != nil {
		return fmt.Errorf("invalid proposal JSON: %w", err)
	}
	return expectEOF(dec)
}

func inspectJSONValue(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("explicit null is not permitted")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			nameToken, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("object member name is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("duplicate object member %q", name)
			}
			seen[name] = struct{}{}
			if err := inspectJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("unterminated object")
		}
	case '[':
		for dec.More() {
			if err := inspectJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("unterminated array")
		}
	default:
		return fmt.Errorf("unexpected delimiter %q", delim)
	}
	return nil
}

func expectEOF(dec *json.Decoder) error {
	var trailing any
	err := dec.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("trailing JSON value")
	}
	return fmt.Errorf("trailing proposal data: %w", err)
}

// Validate binds a proposal to one exact evidence packet and existing ledger
// state. On every error it returns the zero ChangeSet.
func Validate(p Proposal, packet evidence.Packet, state ledger.State) (ledger.ChangeSet, error) {
	fail := func(err error) (ledger.ChangeSet, error) { return ledger.ChangeSet{}, err }
	if err := validateProtocolShape(p); err != nil {
		return fail(err)
	}
	packetEvidence, err := validatePacket(p, packet)
	if err != nil {
		return fail(err)
	}
	if err := validateSafeText(p, packet); err != nil {
		return fail(err)
	}
	ids, err := validateState(state, p.ProjectID)
	if err != nil {
		return fail(err)
	}
	sessionReportBySession, err := indexSessionReports(state.Sessions)
	if err != nil {
		return fail(err)
	}

	result := ledger.ChangeSet{}
	changeEvidence := make(map[string]map[string]struct{})
	decisionByID := cloneDecisionMap(state.Decisions)
	loopByID := cloneLoopMap(state.OpenLoops)
	timelineByID := make(map[string]ledger.TimelineEvent, len(state.Timeline))
	for _, item := range state.Timeline {
		timelineByID[item.ID] = item
	}

	for _, item := range p.NewDecisions {
		if err := reserveNewID(ids, item.ID, "decision"); err != nil {
			return fail(err)
		}
		if item.ProjectID != p.ProjectID || item.Revision != 1 || (item.Status != "proposed" && item.Status != "accepted") {
			return fail(fmt.Errorf("invalid new decision %q identity, revision, or initial status", item.ID))
		}
		if err := requireCurrentSession(item.SourceSessions, p.SessionID, "decision "+item.ID); err != nil {
			return fail(err)
		}
		if err := bindEvidence(changeEvidence, item.ID, item.Evidence, packetEvidence, p.SessionID); err != nil {
			return fail(err)
		}
		decisionByID[item.ID] = item
		result.Decisions = append(result.Decisions, item)
	}

	seenDecisionChanges := make(map[string]struct{}, len(p.UpdatedDecisions))
	for _, patch := range p.UpdatedDecisions {
		if _, duplicate := seenDecisionChanges[patch.ID]; duplicate {
			return fail(fmt.Errorf("duplicate decision change %q", patch.ID))
		}
		seenDecisionChanges[patch.ID] = struct{}{}
		old, ok := state.Decisions[patch.ID]
		if !ok {
			return fail(fmt.Errorf("decision %q does not exist", patch.ID))
		}
		if old.Revision >= maxSafeInteger || patch.ExpectedRevision != old.Revision {
			return fail(fmt.Errorf("decision %q revision mismatch or overflow", patch.ID))
		}
		if patch.Evidence == nil {
			return fail(fmt.Errorf("decision patch %q lacks current-packet evidence", patch.ID))
		}
		updated := applyDecisionPatch(old, patch)
		if err := validateDecisionTransition(old.Status, updated.Status); err != nil {
			return fail(fmt.Errorf("decision %q: %w", patch.ID, err))
		}
		if reflect.DeepEqual(old, updated) {
			return fail(fmt.Errorf("decision patch %q is a no-op", patch.ID))
		}
		updated.Revision = old.Revision + 1
		if err := requireCurrentSession(updated.SourceSessions, p.SessionID, "decision "+patch.ID); err != nil {
			return fail(err)
		}
		if err := bindEvidence(changeEvidence, patch.ID, *patch.Evidence, packetEvidence, p.SessionID); err != nil {
			return fail(err)
		}
		decisionByID[patch.ID] = updated
		result.Decisions = append(result.Decisions, updated)
	}

	seenLoopChanges := make(map[string]struct{}, len(p.OpenLoops))
	for _, change := range p.OpenLoops {
		var updated ledger.OpenLoop
		switch change.Operation {
		case "create":
			if change.Entity == nil || change.Patch != nil {
				return fail(errors.New("open-loop create requires exactly one entity"))
			}
			updated = *change.Entity
			if _, duplicate := seenLoopChanges[updated.ID]; duplicate {
				return fail(fmt.Errorf("duplicate open-loop change %q", updated.ID))
			}
			seenLoopChanges[updated.ID] = struct{}{}
			if err := reserveNewID(ids, updated.ID, "open_loop"); err != nil {
				return fail(err)
			}
			if updated.ProjectID != p.ProjectID || updated.Revision != 1 || (updated.Status != "open" && updated.Status != "blocked") {
				return fail(fmt.Errorf("invalid new open loop %q", updated.ID))
			}
		case "update":
			if change.Entity != nil || change.Patch == nil {
				return fail(errors.New("open-loop update requires exactly one patch"))
			}
			patch := *change.Patch
			if _, duplicate := seenLoopChanges[patch.ID]; duplicate {
				return fail(fmt.Errorf("duplicate open-loop change %q", patch.ID))
			}
			seenLoopChanges[patch.ID] = struct{}{}
			old, ok := state.OpenLoops[patch.ID]
			if !ok {
				return fail(fmt.Errorf("open loop %q does not exist", patch.ID))
			}
			if old.Revision >= maxSafeInteger || patch.ExpectedRevision != old.Revision {
				return fail(fmt.Errorf("open loop %q revision mismatch or overflow", patch.ID))
			}
			if patch.Evidence == nil {
				return fail(fmt.Errorf("open-loop patch %q lacks current-packet evidence", patch.ID))
			}
			updated = applyOpenLoopPatch(old, patch)
			if err := validateLoopTransition(old.Status, updated.Status); err != nil {
				return fail(fmt.Errorf("open loop %q: %w", patch.ID, err))
			}
			if reflect.DeepEqual(old, updated) {
				return fail(fmt.Errorf("open-loop patch %q is a no-op", patch.ID))
			}
			updated.Revision = old.Revision + 1
		default:
			return fail(fmt.Errorf("unknown open-loop operation %q", change.Operation))
		}
		if err := requireCurrentSession(updated.SourceSessions, p.SessionID, "open loop "+updated.ID); err != nil {
			return fail(err)
		}
		if err := bindEvidence(changeEvidence, updated.ID, updated.Evidence, packetEvidence, p.SessionID); err != nil {
			return fail(err)
		}
		loopByID[updated.ID] = updated
		result.OpenLoops = append(result.OpenLoops, updated)
	}

	seenTimeline := make(map[string]struct{}, len(p.TimelineEvents))
	for _, item := range p.TimelineEvents {
		if _, duplicate := seenTimeline[item.ID]; duplicate {
			return fail(fmt.Errorf("duplicate timeline change %q", item.ID))
		}
		seenTimeline[item.ID] = struct{}{}
		old, exists := timelineByID[item.ID]
		if exists {
			if old.Revision >= maxSafeInteger || item.Revision != old.Revision+1 {
				return fail(fmt.Errorf("timeline %q revision mismatch or overflow", item.ID))
			}
		} else {
			if err := reserveNewID(ids, item.ID, "timeline"); err != nil {
				return fail(err)
			}
			if item.Revision != 1 {
				return fail(fmt.Errorf("new timeline %q revision must be 1", item.ID))
			}
		}
		if err := bindEvidence(changeEvidence, item.ID, item.Evidence, packetEvidence, p.SessionID); err != nil {
			return fail(err)
		}
		if exists && (old.Class == ledger.Inference || old.Class == ledger.PendingConfirmation) && item.Class == ledger.Verified {
			// Checked after evidence links are validated below.
		}
		timelineByID[item.ID] = item
		result.Timeline = append(result.Timeline, item)
	}

	current, err := applyCurrentPatch(state.CurrentState, p.CurrentStatePatch, p.ProjectID, p.SessionID, packetEvidence, changeEvidence)
	if err != nil {
		return fail(err)
	}
	result.Current = &current

	report := p.SessionReport
	if report.ProjectID != p.ProjectID || report.SessionID != p.SessionID {
		return fail(errors.New("session report identity does not match proposal"))
	}
	if packet.SessionUsage != nil {
		if err := accounting.ValidateSessionAccounting(report.Accounting, packet.SessionUsage); err != nil {
			return fail(err)
		}
	} else if report.Accounting != nil {
		return fail(errors.New("session accounting is present without packet usage"))
	}
	if existingID, represented := sessionReportBySession[report.SessionID]; represented && existingID != report.ID {
		return fail(fmt.Errorf("session %q is already represented by report %q", report.SessionID, existingID))
	}
	if old, exists := state.Sessions[report.ID]; exists {
		if old.ProjectID != report.ProjectID || old.SessionID != report.SessionID {
			return fail(fmt.Errorf("session report %q identity cannot change", report.ID))
		}
		if old.Revision >= maxSafeInteger || report.Revision != old.Revision+1 {
			return fail(fmt.Errorf("session report %q revision mismatch or overflow", report.ID))
		}
	} else {
		if err := reserveNewID(ids, report.ID, "session"); err != nil {
			return fail(err)
		}
		if report.Revision != 1 {
			return fail(fmt.Errorf("new session report %q revision must be 1", report.ID))
		}
	}
	reportRefs := append([]ledger.EvidenceRef(nil), report.Evidence...)
	for _, phase := range report.Phases {
		reportRefs = append(reportRefs, phase.Evidence...)
	}
	if err := bindEvidence(changeEvidence, report.ID, reportRefs, packetEvidence, p.SessionID); err != nil {
		return fail(err)
	}
	result.Sessions = append(result.Sessions, report)

	if err := validateSupersedes(decisionByID, p.ProjectID); err != nil {
		return fail(err)
	}
	if err := validateReferences(p, decisionByID, loopByID); err != nil {
		return fail(err)
	}
	links, err := validateEvidenceLinks(p.EvidenceLinks, changeEvidence)
	if err != nil {
		return fail(err)
	}
	for _, item := range p.TimelineEvents {
		old, exists := timelineByIDFromSlice(state.Timeline, item.ID)
		if exists && (old.Class == ledger.Inference || old.Class == ledger.PendingConfirmation) && item.Class == ledger.Verified && !links[item.ID+"\x00verifies"] {
			return fail(fmt.Errorf("timeline %q inference upgrade lacks verification evidence", item.ID))
		}
	}
	if err := validateSessionReportEffects(report, state, result); err != nil {
		return fail(err)
	}

	sort.Slice(result.Decisions, func(i, j int) bool { return result.Decisions[i].ID < result.Decisions[j].ID })
	sort.Slice(result.OpenLoops, func(i, j int) bool { return result.OpenLoops[i].ID < result.OpenLoops[j].ID })
	sort.Slice(result.Timeline, func(i, j int) bool { return result.Timeline[i].ID < result.Timeline[j].ID })
	sort.Slice(result.Sessions, func(i, j int) bool { return result.Sessions[i].ID < result.Sessions[j].ID })
	return result, nil
}

func validateSessionReportEffects(report ledger.SessionReport, state ledger.State, changes ledger.ChangeSet) error {
	added := make([]string, 0, len(changes.Decisions))
	revised := make([]string, 0, len(changes.Decisions))
	for _, item := range changes.Decisions {
		if _, existed := state.Decisions[item.ID]; existed {
			revised = append(revised, item.ID)
		} else {
			added = append(added, item.ID)
		}
	}
	created := make([]string, 0, len(changes.OpenLoops))
	closed := make([]string, 0, len(changes.OpenLoops))
	for _, item := range changes.OpenLoops {
		old, existed := state.OpenLoops[item.ID]
		if !existed {
			created = append(created, item.ID)
		} else if isActiveLoopStatus(old.Status) && isClosedLoopStatus(item.Status) {
			closed = append(closed, item.ID)
		}
	}
	sort.Strings(added)
	sort.Strings(revised)
	sort.Strings(created)
	sort.Strings(closed)
	checks := []struct {
		name     string
		declared []string
		actual   []string
	}{
		{name: "decisions_added", declared: report.DecisionsAdded, actual: added},
		{name: "decisions_revised", declared: report.DecisionsRevised, actual: revised},
		{name: "open_loops_created", declared: report.OpenLoopsCreated, actual: created},
		{name: "open_loops_closed", declared: report.OpenLoopsClosed, actual: closed},
	}
	for _, check := range checks {
		if !equalStrings(check.declared, check.actual) {
			return fmt.Errorf("session report %s does not exactly match packet effects: declared=%v actual=%v", check.name, check.declared, check.actual)
		}
	}
	return nil
}

func isActiveLoopStatus(status string) bool {
	return status == "open" || status == "blocked"
}

func isClosedLoopStatus(status string) bool {
	return status == "resolved" || status == "abandoned"
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func validateProtocolShape(p Proposal) error {
	if p.SchemaVersion != proposalSchemaVersion {
		return fmt.Errorf("unsupported proposal schema version %d", p.SchemaVersion)
	}
	if strings.TrimSpace(p.ProjectID) == "" || strings.TrimSpace(p.SessionID) == "" || !positiveSafeInteger(p.FromCursor) || !nonnegativeSafeInteger(p.ToCursor) || p.ToCursor < p.FromCursor-1 {
		return errors.New("invalid proposal identity or cursor range")
	}
	if !prefixedSHA256.MatchString(p.EvidencePacketSHA256) {
		return errors.New("invalid evidence packet digest")
	}
	if p.NewDecisions == nil || p.UpdatedDecisions == nil || p.OpenLoops == nil || p.TimelineEvents == nil || p.EvidenceLinks == nil {
		return errors.New("required proposal arrays must be present")
	}
	for _, item := range p.NewDecisions {
		if err := validateDecision(item); err != nil {
			return err
		}
	}
	for _, patch := range p.UpdatedDecisions {
		if !validStableID(patch.ID) {
			return fmt.Errorf("invalid stable id for decision patch %q", patch.ID)
		}
		if !positiveSafeInteger(patch.ExpectedRevision) || !decisionPatchHasChange(patch) {
			return fmt.Errorf("invalid decision patch %q", patch.ID)
		}
		if patch.Status != nil && !validDecisionStatus(*patch.Status) {
			return fmt.Errorf("invalid decision status %q", *patch.Status)
		}
		if err := validateOptionalNonemptyStringSlice(patch.Tags, "decision tags"); err != nil {
			return err
		}
		if err := validateOptionalStableIDSlice(patch.Supersedes, "decision supersedes"); err != nil {
			return err
		}
		if err := validateOptionalNonemptyStringSlice(patch.SourceSessions, "decision source sessions"); err != nil {
			return err
		}
		if err := validateOptionalStringSlice(patch.Alternatives, "decision alternatives"); err != nil {
			return err
		}
		if err := validateOptionalStringSlice(patch.RejectedPaths, "decision rejected paths"); err != nil {
			return err
		}
		if err := validateOptionalEvidence(patch.Evidence); err != nil {
			return err
		}
	}
	for _, change := range p.OpenLoops {
		if change.Operation != "create" && change.Operation != "update" {
			return fmt.Errorf("invalid open-loop operation %q", change.Operation)
		}
		if change.Operation == "create" {
			if change.Entity == nil || change.Patch != nil {
				return errors.New("open-loop create requires entity and forbids patch")
			}
			if err := validateOpenLoop(*change.Entity); err != nil {
				return err
			}
		} else {
			if change.Entity != nil || change.Patch == nil {
				return errors.New("open-loop update requires patch and forbids entity")
			}
			patch := *change.Patch
			if !validStableID(patch.ID) {
				return fmt.Errorf("invalid stable id for open-loop patch %q", patch.ID)
			}
			if !positiveSafeInteger(patch.ExpectedRevision) || !openLoopPatchHasChange(patch) {
				return fmt.Errorf("invalid open-loop patch %q", patch.ID)
			}
			if patch.Status != nil && !validLoopStatus(*patch.Status) {
				return fmt.Errorf("invalid open-loop status %q", *patch.Status)
			}
			if err := validateOptionalNonemptyStringSlice(patch.Tags, "open-loop tags"); err != nil {
				return err
			}
			if err := validateOptionalNonemptyStringSlice(patch.SourceSessions, "open-loop source sessions"); err != nil {
				return err
			}
			if err := validateOptionalStringSlice(patch.Attempts, "open-loop attempts"); err != nil {
				return err
			}
			if err := validateOptionalEvidence(patch.Evidence); err != nil {
				return err
			}
		}
	}
	for _, item := range p.TimelineEvents {
		if err := validateTimeline(item); err != nil {
			return err
		}
	}
	if !nonnegativeSafeInteger(p.CurrentStatePatch.ExpectedRevision) || !currentPatchHasChange(p.CurrentStatePatch) {
		return errors.New("invalid or empty current-state patch")
	}
	if err := validateOptionalStringSlice(p.CurrentStatePatch.UncommittedChanges, "uncommitted changes"); err != nil {
		return err
	}
	if err := validateOptionalStringSlice(p.CurrentStatePatch.Blockers, "blockers"); err != nil {
		return err
	}
	if err := validateOptionalStringSlice(p.CurrentStatePatch.OpenRisks, "open risks"); err != nil {
		return err
	}
	if err := validateOptionalNonemptyStringSlice(p.CurrentStatePatch.SourceSessions, "current-state source sessions"); err != nil {
		return err
	}
	if err := validateOptionalEvidence(p.CurrentStatePatch.Evidence); err != nil {
		return err
	}
	if value := p.CurrentStatePatch.LastUpdated; value != nil && !validTime(*value) {
		return fmt.Errorf("invalid current-state time %q", *value)
	}
	if err := validateSessionReport(p.SessionReport); err != nil {
		return err
	}
	for _, link := range p.EvidenceLinks {
		if !validStableID(link.EntityID) || !validStableID(link.EvidenceID) {
			return fmt.Errorf("invalid stable id in evidence link %+v", link)
		}
		if !validRelation(link.Relation) {
			return fmt.Errorf("invalid evidence link %+v", link)
		}
	}
	return nil
}

func validateDecision(item ledger.Decision) error {
	if !validStableID(item.ID) {
		return fmt.Errorf("invalid stable id for decision %q", item.ID)
	}
	if strings.TrimSpace(item.ProjectID) == "" || strings.TrimSpace(item.Title) == "" || !positiveSafeInteger(item.Revision) || !validDecisionStatus(item.Status) {
		return fmt.Errorf("invalid decision %q", item.ID)
	}
	if item.Tags == nil || item.Supersedes == nil || item.SourceSessions == nil || item.Evidence == nil || item.Alternatives == nil || item.RejectedPaths == nil {
		return fmt.Errorf("decision %q omits a required array", item.ID)
	}
	if err := validateUniqueStableIDs(item.Supersedes, "decision supersedes"); err != nil {
		return err
	}
	for name, values := range map[string][]string{"tags": item.Tags, "source_sessions": item.SourceSessions} {
		if err := validateUniqueStrings(values, "decision "+name, true); err != nil {
			return err
		}
	}
	return validateEvidenceRefs(item.Evidence)
}

func validateOpenLoop(item ledger.OpenLoop) error {
	if !validStableID(item.ID) {
		return fmt.Errorf("invalid stable id for open loop %q", item.ID)
	}
	if strings.TrimSpace(item.ProjectID) == "" || strings.TrimSpace(item.Title) == "" || !positiveSafeInteger(item.Revision) || !validLoopStatus(item.Status) {
		return fmt.Errorf("invalid open loop %q", item.ID)
	}
	if item.Tags == nil || item.SourceSessions == nil || item.Evidence == nil || item.Attempts == nil {
		return fmt.Errorf("open loop %q omits a required array", item.ID)
	}
	if err := validateUniqueStrings(item.Tags, "open-loop tags", true); err != nil {
		return err
	}
	if err := validateUniqueStrings(item.SourceSessions, "open-loop source sessions", true); err != nil {
		return err
	}
	return validateEvidenceRefs(item.Evidence)
}

func validateTimeline(item ledger.TimelineEvent) error {
	if !validStableID(item.ID) {
		return fmt.Errorf("invalid stable id for timeline event %q", item.ID)
	}
	if strings.TrimSpace(item.Title) == "" || !positiveSafeInteger(item.Revision) || !validFactClass(item.Class) || !validTime(item.OccurredAt) {
		return fmt.Errorf("invalid timeline event %q", item.ID)
	}
	if item.Evidence == nil || item.DecisionIDs == nil || item.OpenLoopIDs == nil {
		return fmt.Errorf("timeline event %q omits a required array", item.ID)
	}
	if err := validateEvidenceRefs(item.Evidence); err != nil {
		return err
	}
	if err := validateUniqueStableIDs(item.DecisionIDs, "timeline decision ids"); err != nil {
		return err
	}
	return validateUniqueStableIDs(item.OpenLoopIDs, "timeline open-loop ids")
}

func validateSessionReport(report ledger.SessionReport) error {
	if !validStableID(report.ID) {
		return fmt.Errorf("invalid stable id for session report %q", report.ID)
	}
	if strings.TrimSpace(report.ProjectID) == "" || strings.TrimSpace(report.SessionID) == "" || !positiveSafeInteger(report.Revision) {
		return fmt.Errorf("invalid session report %q", report.ID)
	}
	arrays := map[string][]string{
		"goal changes": report.GoalChanges, "files": report.Files, "commits": report.Commits,
		"verification": report.Verification, "decisions added": report.DecisionsAdded,
		"decisions revised": report.DecisionsRevised, "open loops created": report.OpenLoopsCreated,
		"open loops closed": report.OpenLoopsClosed,
	}
	if report.Phases == nil || report.Evidence == nil {
		return fmt.Errorf("session report %q omits a required array", report.ID)
	}
	for name, values := range arrays {
		if values == nil {
			return fmt.Errorf("session report %q omits %s", report.ID, name)
		}
		if err := validateUniqueStrings(values, "session report "+name, false); err != nil {
			return err
		}
	}
	for name, values := range map[string][]string{
		"decisions added": report.DecisionsAdded, "decisions revised": report.DecisionsRevised,
		"open loops created": report.OpenLoopsCreated, "open loops closed": report.OpenLoopsClosed,
	} {
		if err := validateUniqueStableIDs(values, "session report "+name); err != nil {
			return err
		}
	}
	if err := validateEvidenceRefs(report.Evidence); err != nil {
		return err
	}
	for _, phase := range report.Phases {
		if strings.TrimSpace(phase.Title) == "" || phase.Evidence == nil {
			return errors.New("invalid session phase")
		}
		if err := validateEvidenceRefs(phase.Evidence); err != nil {
			return err
		}
	}
	return nil
}

func validateEvidenceRefs(refs []ledger.EvidenceRef) error {
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if !validStableID(ref.EvidenceID) {
			return fmt.Errorf("invalid stable id for evidence reference %q", ref.EvidenceID)
		}
		if strings.TrimSpace(ref.SessionID) == "" || !positiveSafeInteger(ref.JSONLLine) || !lowercaseSHA256.MatchString(ref.SourceHash) {
			return fmt.Errorf("invalid evidence reference %q", ref.EvidenceID)
		}
		if _, duplicate := seen[ref.EvidenceID]; duplicate {
			return fmt.Errorf("duplicate evidence reference %q", ref.EvidenceID)
		}
		seen[ref.EvidenceID] = struct{}{}
	}
	return nil
}

func validateOptionalEvidence(refs *[]ledger.EvidenceRef) error {
	if refs == nil {
		return nil
	}
	if *refs == nil {
		return errors.New("evidence array cannot be null")
	}
	return validateEvidenceRefs(*refs)
}

func validateOptionalStringSlice(values *[]string, name string) error {
	if values == nil {
		return nil
	}
	if *values == nil {
		return fmt.Errorf("%s cannot be null", name)
	}
	return validateUniqueStrings(*values, name, false)
}

func validateOptionalNonemptyStringSlice(values *[]string, name string) error {
	if values == nil {
		return nil
	}
	if *values == nil {
		return fmt.Errorf("%s cannot be null", name)
	}
	return validateUniqueStrings(*values, name, true)
}

func validateOptionalStableIDSlice(values *[]string, name string) error {
	if values == nil {
		return nil
	}
	if *values == nil {
		return fmt.Errorf("%s cannot be null", name)
	}
	return validateUniqueStableIDs(*values, name)
}

func validateUniqueStableIDs(values []string, name string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validStableID(value) {
			return fmt.Errorf("%s contains invalid stable id %q", name, value)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%s contains duplicate %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validStableID(value string) bool {
	return stableID.MatchString(value)
}

func validateUniqueStrings(values []string, name string, nonempty bool) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if nonempty && strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s contains an empty value", name)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%s contains duplicate %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

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

func validFactClass(class ledger.FactClass) bool {
	switch class {
	case ledger.Verified, ledger.DecisionFact, ledger.Inference, ledger.Superseded, ledger.PendingConfirmation:
		return true
	default:
		return false
	}
}

func validRelation(relation string) bool {
	switch relation {
	case "supports", "verifies", "contradicts":
		return true
	default:
		return false
	}
}

func validTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Format(time.RFC3339Nano) == value
}

func nonnegativeSafeInteger(value int) bool {
	return value >= 0 && value <= maxSafeInteger
}

func positiveSafeInteger(value int) bool {
	return value >= 1 && value <= maxSafeInteger
}

func decisionPatchHasChange(p DecisionPatch) bool {
	return p.Title != nil || p.Status != nil || p.Tags != nil || p.Supersedes != nil || p.SourceSessions != nil || p.Evidence != nil ||
		p.Context != nil || p.Rationale != nil || p.Consequences != nil || p.ReevaluateWhen != nil || p.Alternatives != nil || p.RejectedPaths != nil
}

func openLoopPatchHasChange(p OpenLoopPatch) bool {
	return p.Title != nil || p.Status != nil || p.Tags != nil || p.SourceSessions != nil || p.Evidence != nil || p.Question != nil ||
		p.Blocker != nil || p.NextExperiment != nil || p.CompletionCriterion != nil || p.Attempts != nil
}

func currentPatchHasChange(p CurrentStatePatch) bool {
	return p.Goal != nil || p.LastVerified != nil || p.Branch != nil || p.NextAction != nil || p.FirstInspection != nil || p.LastUpdated != nil ||
		p.UncommittedChanges != nil || p.Blockers != nil || p.OpenRisks != nil || p.SourceSessions != nil || p.Evidence != nil
}

func validatePacket(p Proposal, packet evidence.Packet) (map[string]evidence.Item, error) {
	if packet.SchemaVersion != 2 || packet.ProjectID != p.ProjectID || packet.SessionID != p.SessionID || packet.FromCursor != p.FromCursor || packet.ToCursor != p.ToCursor {
		return nil, errors.New("proposal does not identify the exact evidence packet")
	}
	if strings.TrimSpace(packet.CWD) == "" || !positiveSafeInteger(packet.FromCursor) || !nonnegativeSafeInteger(packet.ToCursor) || packet.ToCursor < packet.FromCursor-1 {
		return nil, errors.New("invalid evidence packet envelope")
	}
	if packet.ExpectedCursor.Line != packet.FromCursor-1 || packet.NextCursor.Line != packet.ToCursor {
		return nil, errors.New("evidence packet cursor boundaries are inconsistent")
	}
	if err := validateBoundary(packet.ExpectedCursor); err != nil {
		return nil, err
	}
	if err := validateBoundary(packet.NextCursor); err != nil {
		return nil, err
	}
	if packet.ToCursor == packet.FromCursor-1 && packet.ExpectedCursor != packet.NextCursor {
		return nil, errors.New("empty packet cursor boundaries must be equal")
	}
	digest, err := evidence.Digest(packet)
	if err != nil {
		return nil, fmt.Errorf("digest evidence packet: %w", err)
	}
	if digest != p.EvidencePacketSHA256 {
		return nil, errors.New("evidence packet digest mismatch")
	}
	items := make(map[string]evidence.Item, len(packet.Events))
	lastLine := 0
	for _, item := range packet.Events {
		if strings.TrimSpace(item.ID) == "" || !positiveSafeInteger(item.JSONLLine) || item.JSONLLine < packet.FromCursor || item.JSONLLine > packet.ToCursor || item.JSONLLine <= lastLine || !lowercaseSHA256.MatchString(item.SourceHash) || !validTime(item.Timestamp) {
			return nil, fmt.Errorf("invalid evidence event %q", item.ID)
		}
		lastLine = item.JSONLLine
		if _, duplicate := items[item.ID]; duplicate {
			return nil, fmt.Errorf("duplicate evidence event id %q", item.ID)
		}
		if item.JSONLLine == packet.NextCursor.Line && item.SourceHash != packet.NextCursor.SourceHash {
			return nil, fmt.Errorf("tail evidence event %q source hash does not match next cursor", item.ID)
		}
		switch item.Kind {
		case "message":
			if item.Role != "user" && item.Role != "assistant" {
				return nil, fmt.Errorf("invalid message role in %q", item.ID)
			}
		case "tool_call", "tool_result", "cwd_change":
		default:
			return nil, fmt.Errorf("invalid evidence kind in %q", item.ID)
		}
		items[item.ID] = item
	}
	for _, warning := range packet.Warnings {
		if !redactionWarning.MatchString(warning) {
			return nil, fmt.Errorf("invalid redaction finding %q", warning)
		}
	}
	return items, nil
}

func validateBoundary(boundary evidence.CursorBoundary) error {
	if !nonnegativeSafeInteger(boundary.Line) {
		return errors.New("negative cursor boundary")
	}
	if boundary.Line == 0 {
		if boundary.SourceHash != "" {
			return errors.New("line-zero cursor has a source hash")
		}
		return nil
	}
	if !lowercaseSHA256.MatchString(boundary.SourceHash) {
		return errors.New("cursor boundary lacks a lowercase source hash")
	}
	return nil
}

func validateSafeText(p Proposal, packet evidence.Packet) error {
	// Scan only text that can be persisted or rendered. Protocol-owned values
	// (IDs, hashes, timestamps, and enums) are validated by their dedicated
	// shape and semantic checks; treating them as prose creates false positives
	// for valid high-entropy hashes.
	values := []string{packet.CWD}
	for _, item := range packet.Events {
		values = append(values, item.ToolName, item.Summary)
	}
	for _, decision := range p.NewDecisions {
		values = appendDecisionText(values, decision)
	}
	for _, patch := range p.UpdatedDecisions {
		values = appendDecisionPatchText(values, patch)
	}
	for _, change := range p.OpenLoops {
		if change.Entity != nil {
			values = appendOpenLoopText(values, *change.Entity)
		}
		if change.Patch != nil {
			values = appendOpenLoopPatchText(values, *change.Patch)
		}
	}
	for _, event := range p.TimelineEvents {
		values = append(values, event.Title, event.Summary)
		values = appendEvidenceText(values, event.Evidence)
	}
	values = appendCurrentStatePatchText(values, p.CurrentStatePatch)
	values = appendSessionReportText(values, p.SessionReport)

	redactor := redact.Default()
	for _, value := range values {
		if result := redactor.Text(value); len(result.Findings) != 0 {
			return fmt.Errorf("proposal or packet text contains redaction findings")
		}
	}
	return nil
}

func appendDecisionText(values []string, decision ledger.Decision) []string {
	values = append(values, decision.Title, decision.Context, decision.Rationale, decision.Consequences, decision.ReevaluateWhen)
	values = append(values, decision.Tags...)
	values = append(values, decision.SourceSessions...)
	values = append(values, decision.Alternatives...)
	values = append(values, decision.RejectedPaths...)
	return appendEvidenceText(values, decision.Evidence)
}

func appendDecisionPatchText(values []string, patch DecisionPatch) []string {
	values = appendOptionalText(values, patch.Title)
	values = appendOptionalTexts(values, patch.Tags)
	values = appendOptionalTexts(values, patch.SourceSessions)
	values = appendOptionalEvidenceText(values, patch.Evidence)
	values = appendOptionalText(values, patch.Context)
	values = appendOptionalText(values, patch.Rationale)
	values = appendOptionalText(values, patch.Consequences)
	values = appendOptionalText(values, patch.ReevaluateWhen)
	values = appendOptionalTexts(values, patch.Alternatives)
	return appendOptionalTexts(values, patch.RejectedPaths)
}

func appendOpenLoopText(values []string, loop ledger.OpenLoop) []string {
	values = append(values, loop.Title, loop.Question, loop.Blocker, loop.NextExperiment, loop.CompletionCriterion)
	values = append(values, loop.Tags...)
	values = append(values, loop.SourceSessions...)
	values = append(values, loop.Attempts...)
	return appendEvidenceText(values, loop.Evidence)
}

func appendOpenLoopPatchText(values []string, patch OpenLoopPatch) []string {
	values = appendOptionalText(values, patch.Title)
	values = appendOptionalTexts(values, patch.Tags)
	values = appendOptionalTexts(values, patch.SourceSessions)
	values = appendOptionalEvidenceText(values, patch.Evidence)
	values = appendOptionalText(values, patch.Question)
	values = appendOptionalText(values, patch.Blocker)
	values = appendOptionalText(values, patch.NextExperiment)
	values = appendOptionalText(values, patch.CompletionCriterion)
	return appendOptionalTexts(values, patch.Attempts)
}

func appendCurrentStatePatchText(values []string, patch CurrentStatePatch) []string {
	values = appendOptionalText(values, patch.Goal)
	values = appendOptionalText(values, patch.Branch)
	values = appendOptionalText(values, patch.NextAction)
	values = appendOptionalText(values, patch.FirstInspection)
	values = appendOptionalTexts(values, patch.UncommittedChanges)
	values = appendOptionalTexts(values, patch.Blockers)
	values = appendOptionalTexts(values, patch.OpenRisks)
	values = appendOptionalTexts(values, patch.SourceSessions)
	return appendOptionalEvidenceText(values, patch.Evidence)
}

func appendSessionReportText(values []string, report ledger.SessionReport) []string {
	values = append(values, report.InitialGoal, report.PreviousSessionID, report.NextSessionID)
	values = append(values, report.GoalChanges...)
	values = append(values, report.Files...)
	values = append(values, report.Commits...)
	values = append(values, report.Verification...)
	for _, phase := range report.Phases {
		values = append(values, phase.Title, phase.Summary)
		values = appendEvidenceText(values, phase.Evidence)
	}
	if report.Accounting != nil {
		for _, model := range report.Accounting.Models {
			values = append(values, model.Model, model.Pricing.Currency, model.Pricing.Source, model.Pricing.AsOf)
		}
	}
	return appendEvidenceText(values, report.Evidence)
}

func appendEvidenceText(values []string, refs []ledger.EvidenceRef) []string {
	for _, ref := range refs {
		values = append(values, ref.Summary)
	}
	return values
}

func appendOptionalText(values []string, value *string) []string {
	if value != nil {
		values = append(values, *value)
	}
	return values
}

func appendOptionalTexts(values []string, value *[]string) []string {
	if value != nil {
		values = append(values, (*value)...)
	}
	return values
}

func appendOptionalEvidenceText(values []string, value *[]ledger.EvidenceRef) []string {
	if value != nil {
		values = appendEvidenceText(values, *value)
	}
	return values
}

func validateState(state ledger.State, projectID string) (map[string]string, error) {
	if state.ProjectID != projectID {
		return nil, errors.New("ledger state project does not match proposal")
	}
	if state.CurrentState.ProjectID != "" && state.CurrentState.ProjectID != projectID {
		return nil, errors.New("current state belongs to another project")
	}
	if !nonnegativeSafeInteger(state.CurrentState.Revision) {
		return nil, errors.New("negative current-state revision")
	}
	if err := validateStableEvidenceIDs(state.CurrentState.Evidence, "existing current-state evidence"); err != nil {
		return nil, err
	}
	ids := make(map[string]string)
	ids[currentStateEntityID] = "current_state"
	reserve := func(id, kind string) error {
		if !validStableID(id) {
			return fmt.Errorf("invalid stable id for existing %s %q", kind, id)
		}
		if previous, exists := ids[id]; exists {
			return fmt.Errorf("id %q is shared by %s and %s", id, previous, kind)
		}
		ids[id] = kind
		return nil
	}
	for key, item := range state.Decisions {
		if key != item.ID || item.ProjectID != projectID || !positiveSafeInteger(item.Revision) || !validDecisionStatus(item.Status) {
			return nil, fmt.Errorf("invalid existing decision %q", key)
		}
		if err := reserve(item.ID, "decision"); err != nil {
			return nil, err
		}
		if err := validateUniqueStableIDs(item.Supersedes, "existing decision supersedes"); err != nil {
			return nil, err
		}
		if err := validateStableEvidenceIDs(item.Evidence, "existing decision evidence"); err != nil {
			return nil, err
		}
	}
	for key, item := range state.OpenLoops {
		if key != item.ID || item.ProjectID != projectID || !positiveSafeInteger(item.Revision) || !validLoopStatus(item.Status) {
			return nil, fmt.Errorf("invalid existing open loop %q", key)
		}
		if err := reserve(item.ID, "open_loop"); err != nil {
			return nil, err
		}
		if err := validateStableEvidenceIDs(item.Evidence, "existing open-loop evidence"); err != nil {
			return nil, err
		}
	}
	for _, item := range state.Timeline {
		if !positiveSafeInteger(item.Revision) || !validFactClass(item.Class) {
			return nil, fmt.Errorf("invalid existing timeline %q", item.ID)
		}
		if err := reserve(item.ID, "timeline"); err != nil {
			return nil, err
		}
		if err := validateUniqueStableIDs(item.DecisionIDs, "existing timeline decision ids"); err != nil {
			return nil, err
		}
		if err := validateUniqueStableIDs(item.OpenLoopIDs, "existing timeline open-loop ids"); err != nil {
			return nil, err
		}
		if err := validateStableEvidenceIDs(item.Evidence, "existing timeline evidence"); err != nil {
			return nil, err
		}
	}
	for key, item := range state.Sessions {
		if key != item.ID || item.ProjectID != projectID || !positiveSafeInteger(item.Revision) {
			return nil, fmt.Errorf("invalid existing session report %q", key)
		}
		if err := reserve(item.ID, "session"); err != nil {
			return nil, err
		}
		for name, values := range map[string][]string{
			"decisions added": item.DecisionsAdded, "decisions revised": item.DecisionsRevised,
			"open loops created": item.OpenLoopsCreated, "open loops closed": item.OpenLoopsClosed,
		} {
			if err := validateUniqueStableIDs(values, "existing session report "+name); err != nil {
				return nil, err
			}
		}
		if err := validateStableEvidenceIDs(item.Evidence, "existing session-report evidence"); err != nil {
			return nil, err
		}
		for _, phase := range item.Phases {
			if err := validateStableEvidenceIDs(phase.Evidence, "existing session-phase evidence"); err != nil {
				return nil, err
			}
		}
	}
	return ids, nil
}

func indexSessionReports(reports map[string]ledger.SessionReport) (map[string]string, error) {
	bySession := make(map[string]string, len(reports))
	for id, report := range reports {
		if strings.TrimSpace(report.SessionID) == "" {
			return nil, fmt.Errorf("existing session report %q has no session id", id)
		}
		if previousID, duplicate := bySession[report.SessionID]; duplicate {
			return nil, fmt.Errorf("session id %q is represented by both %q and %q", report.SessionID, previousID, id)
		}
		bySession[report.SessionID] = id
	}
	return bySession, nil
}

func reserveNewID(ids map[string]string, id, kind string) error {
	if !validStableID(id) {
		return fmt.Errorf("invalid stable id for new %s %q", kind, id)
	}
	if previous, exists := ids[id]; exists {
		return fmt.Errorf("new %s id %q already belongs to %s", kind, id, previous)
	}
	ids[id] = kind
	return nil
}

func validateStableEvidenceIDs(refs []ledger.EvidenceRef, name string) error {
	values := make([]string, 0, len(refs))
	for _, ref := range refs {
		values = append(values, ref.EvidenceID)
	}
	return validateUniqueStableIDs(values, name)
}

func requireCurrentSession(values []string, sessionID, entity string) error {
	if err := validateUniqueStrings(values, entity+" source sessions", true); err != nil {
		return err
	}
	for _, value := range values {
		if value == sessionID {
			return nil
		}
	}
	return fmt.Errorf("%s does not cite current session", entity)
}

func bindEvidence(target map[string]map[string]struct{}, entityID string, refs []ledger.EvidenceRef, packet map[string]evidence.Item, sessionID string) error {
	if len(refs) == 0 {
		return fmt.Errorf("entity %q has no current-packet evidence", entityID)
	}
	if target[entityID] == nil {
		target[entityID] = make(map[string]struct{})
	}
	for _, ref := range refs {
		item, exists := packet[ref.EvidenceID]
		if !exists || ref.SessionID != sessionID || ref.JSONLLine != item.JSONLLine || ref.SourceHash != item.SourceHash || ref.Summary != item.Summary {
			return fmt.Errorf("evidence tuple %q does not exactly match packet", ref.EvidenceID)
		}
		target[entityID][ref.EvidenceID] = struct{}{}
	}
	return nil
}

func cloneDecisionMap(source map[string]ledger.Decision) map[string]ledger.Decision {
	result := make(map[string]ledger.Decision, len(source))
	for id, item := range source {
		result[id] = item
	}
	return result
}

func cloneLoopMap(source map[string]ledger.OpenLoop) map[string]ledger.OpenLoop {
	result := make(map[string]ledger.OpenLoop, len(source))
	for id, item := range source {
		result[id] = item
	}
	return result
}

func applyDecisionPatch(item ledger.Decision, patch DecisionPatch) ledger.Decision {
	if patch.Title != nil {
		item.Title = *patch.Title
	}
	if patch.Status != nil {
		item.Status = *patch.Status
	}
	if patch.Tags != nil {
		item.Tags = append([]string(nil), (*patch.Tags)...)
	}
	if patch.Supersedes != nil {
		item.Supersedes = append([]string(nil), (*patch.Supersedes)...)
	}
	if patch.SourceSessions != nil {
		item.SourceSessions = append([]string(nil), (*patch.SourceSessions)...)
	}
	if patch.Evidence != nil {
		item.Evidence = append([]ledger.EvidenceRef(nil), (*patch.Evidence)...)
	}
	if patch.Context != nil {
		item.Context = *patch.Context
	}
	if patch.Rationale != nil {
		item.Rationale = *patch.Rationale
	}
	if patch.Consequences != nil {
		item.Consequences = *patch.Consequences
	}
	if patch.ReevaluateWhen != nil {
		item.ReevaluateWhen = *patch.ReevaluateWhen
	}
	if patch.Alternatives != nil {
		item.Alternatives = append([]string(nil), (*patch.Alternatives)...)
	}
	if patch.RejectedPaths != nil {
		item.RejectedPaths = append([]string(nil), (*patch.RejectedPaths)...)
	}
	return item
}

func applyOpenLoopPatch(item ledger.OpenLoop, patch OpenLoopPatch) ledger.OpenLoop {
	if patch.Title != nil {
		item.Title = *patch.Title
	}
	if patch.Status != nil {
		item.Status = *patch.Status
	}
	if patch.Tags != nil {
		item.Tags = append([]string(nil), (*patch.Tags)...)
	}
	if patch.SourceSessions != nil {
		item.SourceSessions = append([]string(nil), (*patch.SourceSessions)...)
	}
	if patch.Evidence != nil {
		item.Evidence = append([]ledger.EvidenceRef(nil), (*patch.Evidence)...)
	}
	if patch.Question != nil {
		item.Question = *patch.Question
	}
	if patch.Blocker != nil {
		item.Blocker = *patch.Blocker
	}
	if patch.NextExperiment != nil {
		item.NextExperiment = *patch.NextExperiment
	}
	if patch.CompletionCriterion != nil {
		item.CompletionCriterion = *patch.CompletionCriterion
	}
	if patch.Attempts != nil {
		item.Attempts = append([]string(nil), (*patch.Attempts)...)
	}
	return item
}

func validateDecisionTransition(old, next string) error {
	if old == next {
		return nil
	}
	valid := false
	switch old {
	case "proposed":
		valid = next == "accepted" || next == "archived"
	case "accepted":
		valid = next == "superseded" || next == "archived"
	case "superseded":
		valid = next == "archived"
	case "archived":
		valid = false
	}
	if !valid {
		return fmt.Errorf("invalid status transition %s -> %s", old, next)
	}
	return nil
}

func validateLoopTransition(old, next string) error {
	if old == next {
		return nil
	}
	valid := false
	switch old {
	case "open":
		valid = next == "blocked" || next == "resolved" || next == "abandoned"
	case "blocked":
		valid = next == "open" || next == "resolved" || next == "abandoned"
	case "resolved", "abandoned":
		valid = next == "archived"
	case "archived":
		valid = false
	}
	if !valid {
		return fmt.Errorf("invalid status transition %s -> %s", old, next)
	}
	return nil
}

func applyCurrentPatch(old ledger.CurrentState, patch CurrentStatePatch, projectID, sessionID string, packet map[string]evidence.Item, changes map[string]map[string]struct{}) (ledger.CurrentState, error) {
	if old.Revision >= maxSafeInteger || patch.ExpectedRevision != old.Revision {
		return ledger.CurrentState{}, errors.New("current-state revision mismatch or overflow")
	}
	if patch.Evidence == nil || patch.SourceSessions == nil {
		return ledger.CurrentState{}, errors.New("current-state patch lacks evidence or source sessions")
	}
	if err := requireCurrentSession(*patch.SourceSessions, sessionID, "current state"); err != nil {
		return ledger.CurrentState{}, err
	}
	if err := bindEvidence(changes, currentStateEntityID, *patch.Evidence, packet, sessionID); err != nil {
		return ledger.CurrentState{}, err
	}
	result := old
	result.ProjectID = projectID
	result.Revision = old.Revision + 1
	if patch.Goal != nil {
		result.Goal = *patch.Goal
	}
	if patch.LastVerified != nil {
		result.LastVerified = *patch.LastVerified
	}
	if patch.Branch != nil {
		result.Branch = *patch.Branch
	}
	if patch.NextAction != nil {
		result.NextAction = *patch.NextAction
	}
	if patch.FirstInspection != nil {
		result.FirstInspection = *patch.FirstInspection
	}
	if patch.LastUpdated != nil {
		result.LastUpdated = *patch.LastUpdated
	}
	if patch.UncommittedChanges != nil {
		result.UncommittedChanges = append([]string(nil), (*patch.UncommittedChanges)...)
	}
	if patch.Blockers != nil {
		result.Blockers = append([]string(nil), (*patch.Blockers)...)
	}
	if patch.OpenRisks != nil {
		result.OpenRisks = append([]string(nil), (*patch.OpenRisks)...)
	}
	result.SourceSessions = append([]string(nil), (*patch.SourceSessions)...)
	result.Evidence = append([]ledger.EvidenceRef(nil), (*patch.Evidence)...)
	if reflect.DeepEqual(old, result) {
		return ledger.CurrentState{}, errors.New("current-state patch is a no-op")
	}
	return result, nil
}

func validateSupersedes(decisions map[string]ledger.Decision, projectID string) error {
	for id, item := range decisions {
		if item.ProjectID != projectID {
			return fmt.Errorf("decision %q belongs to another project", id)
		}
		seen := make(map[string]struct{}, len(item.Supersedes))
		for _, predecessor := range item.Supersedes {
			if predecessor == id {
				return fmt.Errorf("decision %q supersedes itself", id)
			}
			if _, duplicate := seen[predecessor]; duplicate {
				return fmt.Errorf("decision %q repeats predecessor %q", id, predecessor)
			}
			seen[predecessor] = struct{}{}
			if _, exists := decisions[predecessor]; !exists {
				return fmt.Errorf("decision %q supersedes missing target %q", id, predecessor)
			}
		}
	}
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("supersedes cycle at %q", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, predecessor := range decisions[id].Supersedes {
			if err := visit(predecessor); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for id := range decisions {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func validateReferences(p Proposal, decisions map[string]ledger.Decision, loops map[string]ledger.OpenLoop) error {
	for _, item := range p.TimelineEvents {
		for _, id := range item.DecisionIDs {
			if _, exists := decisions[id]; !exists {
				return fmt.Errorf("timeline %q references missing decision %q", item.ID, id)
			}
		}
		for _, id := range item.OpenLoopIDs {
			if _, exists := loops[id]; !exists {
				return fmt.Errorf("timeline %q references missing open loop %q", item.ID, id)
			}
		}
	}
	report := p.SessionReport
	for _, list := range [][]string{report.DecisionsAdded, report.DecisionsRevised} {
		for _, id := range list {
			if _, exists := decisions[id]; !exists {
				return fmt.Errorf("session report references missing decision %q", id)
			}
		}
	}
	for _, list := range [][]string{report.OpenLoopsCreated, report.OpenLoopsClosed} {
		for _, id := range list {
			if _, exists := loops[id]; !exists {
				return fmt.Errorf("session report references missing open loop %q", id)
			}
		}
	}
	return nil
}

func validateEvidenceLinks(links []EvidenceLink, changes map[string]map[string]struct{}) (map[string]bool, error) {
	seen := make(map[string]struct{}, len(links))
	relations := make(map[string]bool)
	linked := make(map[string]map[string]struct{})
	for _, link := range links {
		key := link.EntityID + "\x00" + link.EvidenceID
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate evidence link for %q and %q", link.EntityID, link.EvidenceID)
		}
		seen[key] = struct{}{}
		refs, entityExists := changes[link.EntityID]
		if !entityExists {
			return nil, fmt.Errorf("evidence link references unchanged entity %q", link.EntityID)
		}
		if _, evidenceExists := refs[link.EvidenceID]; !evidenceExists {
			return nil, fmt.Errorf("evidence link references unbound evidence %q", link.EvidenceID)
		}
		if !validRelation(link.Relation) {
			return nil, fmt.Errorf("unknown evidence relation %q", link.Relation)
		}
		if linked[link.EntityID] == nil {
			linked[link.EntityID] = make(map[string]struct{})
		}
		linked[link.EntityID][link.EvidenceID] = struct{}{}
		relations[link.EntityID+"\x00"+link.Relation] = true
	}
	for entityID, refs := range changes {
		for evidenceID := range refs {
			if _, exists := linked[entityID][evidenceID]; !exists {
				return nil, fmt.Errorf("entity %q evidence %q lacks an evidence link", entityID, evidenceID)
			}
		}
	}
	return relations, nil
}

func timelineByIDFromSlice(items []ledger.TimelineEvent, id string) (ledger.TimelineEvent, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return ledger.TimelineEvent{}, false
}
