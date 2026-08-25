package reviewv2

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/ledger"
)

var lowercaseSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func Validate(state State) error {
	if err := validateDocumentModelSize("review", state.Review); err != nil {
		return err
	}
	if err := validateDocumentModelSize("history", state.Events); err != nil {
		return err
	}
	if strings.TrimSpace(state.Review.ProjectID) == "" || state.Review.ProjectID != state.Machine.ProjectID {
		return errors.New("review and machine ledger project IDs must match")
	}
	if state.Review.Revision < 0 || state.Review.Revision != state.Machine.AcceptedRevision {
		return errors.New("review and machine ledger revisions must match")
	}
	if err := validateMachineLedger(state.Machine); err != nil {
		return err
	}

	entities := make(map[string]string, len(state.Review.Risks)+len(state.Review.Decisions)+len(state.Events)+len(state.Machine.Sessions))
	risks := make(map[string]struct{}, len(state.Review.Risks))
	decisions := make(map[string]struct{}, len(state.Review.Decisions))
	for _, risk := range state.Review.Risks {
		if err := reserveIdentity(entities, risk.ID, "risk"); err != nil {
			return err
		}
		risks[risk.ID] = struct{}{}
	}
	for _, decision := range state.Review.Decisions {
		if err := reserveIdentity(entities, decision.ID, "decision"); err != nil {
			return err
		}
		id := decision.ID
		decisions[id] = struct{}{}
	}
	for _, event := range state.Events {
		if err := reserveIdentity(entities, event.ID, "event"); err != nil {
			return err
		}
	}
	for _, session := range state.Machine.Sessions {
		if err := reserveIdentity(entities, session.ID, "session"); err != nil {
			return err
		}
	}

	for _, event := range state.Events {
		if err := validateReferences(event.DecisionIDs, decisions, fmt.Sprintf("event %q decision", event.ID)); err != nil {
			return err
		}
	}
	for _, session := range state.Machine.Sessions {
		decisionIDs := append(append([]string(nil), session.DecisionsAdded...), session.DecisionsRevised...)
		if err := validateReferences(decisionIDs, decisions, fmt.Sprintf("session %q decision", session.ID)); err != nil {
			return err
		}
		riskIDs := append(append([]string(nil), session.OpenLoopsCreated...), session.OpenLoopsClosed...)
		if err := validateReferences(riskIDs, risks, fmt.Sprintf("session %q risk", session.ID)); err != nil {
			return err
		}
	}
	for ownerID := range state.Machine.Evidence {
		if _, exists := entities[ownerID]; !exists {
			return fmt.Errorf("evidence owner %q does not identify a risk, decision, event, or session", ownerID)
		}
	}
	return validateSessionChain(state.Machine.Sessions)
}

func validateMachineLedger(value MachineLedger) error {
	if value.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported review ledger schema version %d", value.SchemaVersion)
	}
	if strings.TrimSpace(value.ProjectID) == "" {
		return errors.New("machine ledger project ID is required")
	}
	if value.AcceptedRevision < 0 {
		return errors.New("accepted revision must be nonnegative")
	}
	if !lowercaseSHA256.MatchString(value.ReviewSHA256) || !lowercaseSHA256.MatchString(value.HistorySHA256) {
		return errors.New("document hashes must be lower-case SHA-256 values")
	}
	if err := validateProjectSummaryScalars(value.Accounting); err != nil {
		return fmt.Errorf("invalid project accounting: %w", err)
	}

	reportIDs := make(map[string]struct{}, len(value.Sessions))
	sessionIDs := make(map[string]struct{}, len(value.Sessions))
	for _, session := range value.Sessions {
		if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.SessionID) == "" {
			return errors.New("session report and source session IDs are required")
		}
		if _, duplicate := reportIDs[session.ID]; duplicate {
			return fmt.Errorf("duplicate session report identity %q", session.ID)
		}
		reportIDs[session.ID] = struct{}{}
		if _, duplicate := sessionIDs[session.SessionID]; duplicate {
			return fmt.Errorf("duplicate session identity %q", session.SessionID)
		}
		sessionIDs[session.SessionID] = struct{}{}
		if session.ProjectID != value.ProjectID {
			return fmt.Errorf("session %q has a different project ID", session.ID)
		}
		if session.Revision < 0 {
			return fmt.Errorf("session %q has a negative revision", session.ID)
		}
		if err := accounting.ValidateStoredSessionAccounting(session.Accounting); err != nil {
			return fmt.Errorf("session %q accounting: %w", session.ID, err)
		}
		if err := validateEvidenceRefs(session.Evidence); err != nil {
			return fmt.Errorf("session %q evidence: %w", session.ID, err)
		}
		for index, phase := range session.Phases {
			if err := validateEvidenceRefs(phase.Evidence); err != nil {
				return fmt.Errorf("session %q phase %d evidence: %w", session.ID, index, err)
			}
		}
	}
	for _, session := range value.Sessions {
		if err := validateEvidenceSessions(session.Evidence, sessionIDs); err != nil {
			return fmt.Errorf("session %q evidence: %w", session.ID, err)
		}
		for index, phase := range session.Phases {
			if err := validateEvidenceSessions(phase.Evidence, sessionIDs); err != nil {
				return fmt.Errorf("session %q phase %d evidence: %w", session.ID, index, err)
			}
		}
	}

	for ownerID, refs := range value.Evidence {
		if strings.TrimSpace(ownerID) == "" {
			return errors.New("evidence owner identity is required")
		}
		if err := validateEvidenceRefs(refs); err != nil {
			return fmt.Errorf("evidence for %q: %w", ownerID, err)
		}
		if err := validateEvidenceSessions(refs, sessionIDs); err != nil {
			return fmt.Errorf("evidence for %q: %w", ownerID, err)
		}
	}
	return nil
}

func validateProjectSummaryScalars(value accounting.ProjectSummary) error {
	if value.TotalDurationMS < 0 || value.TotalTokens < 0 {
		return errors.New("duration and token totals must be nonnegative")
	}
	if !finiteNonnegative(value.TotalCostUSD) {
		return errors.New("total cost must be finite and nonnegative")
	}
	models := make(map[string]struct{}, len(value.Models))
	for _, model := range value.Models {
		if strings.TrimSpace(model.Model) == "" {
			return errors.New("project accounting model is required")
		}
		if _, duplicate := models[model.Model]; duplicate {
			return fmt.Errorf("duplicate project accounting model %q", model.Model)
		}
		models[model.Model] = struct{}{}
		if model.TotalTokens < 0 {
			return fmt.Errorf("model %q tokens must be nonnegative", model.Model)
		}
		if !finiteNonnegative(model.TotalCostUSD) || !finiteNonnegative(model.TokenSharePct) || !finiteNonnegative(model.CostSharePct) {
			return fmt.Errorf("model %q costs and shares must be finite and nonnegative", model.Model)
		}
	}
	return nil
}

func validateEvidenceRef(ref ledger.EvidenceRef) error {
	if strings.TrimSpace(ref.EvidenceID) == "" || strings.TrimSpace(ref.SessionID) == "" {
		return errors.New("evidence and session IDs are required")
	}
	if ref.JSONLLine < 1 {
		return errors.New("evidence JSONL line must be positive")
	}
	if !lowercaseSHA256.MatchString(ref.SourceHash) {
		return errors.New("evidence source hash must be lower-case SHA-256")
	}
	return nil
}

func validateEvidenceRefs(refs []ledger.EvidenceRef) error {
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if err := validateEvidenceRef(ref); err != nil {
			return err
		}
		if _, duplicate := seen[ref.EvidenceID]; duplicate {
			return fmt.Errorf("duplicate evidence identity %q", ref.EvidenceID)
		}
		seen[ref.EvidenceID] = struct{}{}
	}
	return nil
}

func validateEvidenceSessions(refs []ledger.EvidenceRef, sessions map[string]struct{}) error {
	for _, ref := range refs {
		if _, exists := sessions[ref.SessionID]; !exists {
			return fmt.Errorf("evidence %q references missing session %q", ref.EvidenceID, ref.SessionID)
		}
	}
	return nil
}

func reserveIdentity(entities map[string]string, id, kind string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%s identity is required", kind)
	}
	if previous, duplicate := entities[id]; duplicate {
		return fmt.Errorf("identity %q is shared by %s and %s", id, previous, kind)
	}
	entities[id] = kind
	return nil
}

func validateReferences(ids []string, targets map[string]struct{}, label string) error {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("duplicate %s reference %q", label, id)
		}
		seen[id] = struct{}{}
		if _, exists := targets[id]; !exists {
			return fmt.Errorf("%s references missing identity %q", label, id)
		}
	}
	return nil
}

func validateSessionChain(sessions []ledger.SessionReport) error {
	if len(sessions) == 0 {
		return nil
	}
	bySessionID := make(map[string]ledger.SessionReport, len(sessions))
	for _, session := range sessions {
		bySessionID[session.SessionID] = session
	}
	head := ""
	heads := 0
	terminals := 0
	for _, session := range sessions {
		if session.PreviousSessionID == "" {
			head = session.SessionID
			heads++
		} else {
			previous, exists := bySessionID[session.PreviousSessionID]
			if !exists || previous.NextSessionID != session.SessionID {
				return fmt.Errorf("accepted session chain link into %q is missing or nonreciprocal", session.SessionID)
			}
		}
		if session.NextSessionID == "" {
			terminals++
		} else {
			next, exists := bySessionID[session.NextSessionID]
			if !exists || next.PreviousSessionID != session.SessionID {
				return fmt.Errorf("accepted session chain link out of %q is missing or nonreciprocal", session.SessionID)
			}
		}
	}
	if heads != 1 || terminals != 1 {
		return errors.New("accepted session reports do not form one unambiguous chain")
	}
	visited := make(map[string]struct{}, len(sessions))
	for sessionID := head; sessionID != ""; sessionID = bySessionID[sessionID].NextSessionID {
		if _, duplicate := visited[sessionID]; duplicate {
			return fmt.Errorf("accepted session chain contains a cycle at %q", sessionID)
		}
		visited[sessionID] = struct{}{}
	}
	if len(visited) != len(sessions) {
		return errors.New("accepted session reports form a disconnected chain")
	}
	return nil
}

func finiteNonnegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func validateDocumentSize(name string, body []byte) error {
	if len(body) > MaxDocumentBytes {
		return fmt.Errorf("%s document exceeds %d bytes", name, MaxDocumentBytes)
	}
	return nil
}

func validateDocumentModelSize(name string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s document model: %w", name, err)
	}
	return validateDocumentSize(name, body)
}
