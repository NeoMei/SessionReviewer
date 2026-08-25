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

	if err := validateUniqueIDs("risk", len(state.Review.Risks), func(index int) string {
		return state.Review.Risks[index].ID
	}); err != nil {
		return err
	}
	decisions := make(map[string]struct{}, len(state.Review.Decisions))
	if err := validateUniqueIDs("decision", len(state.Review.Decisions), func(index int) string {
		id := state.Review.Decisions[index].ID
		decisions[id] = struct{}{}
		return id
	}); err != nil {
		return err
	}
	if err := validateUniqueIDs("event", len(state.Events), func(index int) string {
		return state.Events[index].ID
	}); err != nil {
		return err
	}
	for _, event := range state.Events {
		for _, decisionID := range event.DecisionIDs {
			if _, ok := decisions[decisionID]; !ok {
				return fmt.Errorf("event %q references missing decision %q", event.ID, decisionID)
			}
		}
	}
	for _, session := range state.Machine.Sessions {
		for _, decisionID := range append(append([]string(nil), session.DecisionsAdded...), session.DecisionsRevised...) {
			if _, ok := decisions[decisionID]; !ok {
				return fmt.Errorf("session %q references missing decision %q", session.ID, decisionID)
			}
		}
	}
	return validateMachineLedger(state.Machine)
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

	for ownerID, refs := range value.Evidence {
		if strings.TrimSpace(ownerID) == "" {
			return errors.New("evidence owner identity is required")
		}
		if err := validateEvidenceRefs(refs); err != nil {
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

func validateUniqueIDs(kind string, length int, idAt func(int) string) error {
	seen := make(map[string]struct{}, length)
	for index := 0; index < length; index++ {
		id := idAt(index)
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%s identity is required", kind)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("duplicate %s identity %q", kind, id)
		}
		seen[id] = struct{}{}
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
