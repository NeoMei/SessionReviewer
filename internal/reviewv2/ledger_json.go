package reviewv2

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/ledger"
)

type evidenceWire struct {
	ID   string               `json:"id"`
	Refs []ledger.EvidenceRef `json:"refs"`
}

type machineLedgerWire struct {
	SchemaVersion      int                       `json:"schema_version"`
	ProjectID          string                    `json:"project_id"`
	AcceptedRevision   int                       `json:"accepted_revision"`
	ReviewSHA256       string                    `json:"review_sha256"`
	HistorySHA256      string                    `json:"history_sha256"`
	LastSuccessfulSync string                    `json:"last_successful_sync,omitempty"`
	Accounting         accounting.ProjectSummary `json:"accounting"`
	Sessions           []ledger.SessionReport    `json:"sessions"`
	Evidence           []evidenceWire            `json:"evidence"`
}

func ParseMachineLedger(body []byte) (MachineLedger, error) {
	if len(body) > MaxMachineLedgerBytes {
		return MachineLedger{}, fmt.Errorf("machine ledger exceeds %d bytes", MaxMachineLedgerBytes)
	}
	var wire machineLedgerWire
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return MachineLedger{}, fmt.Errorf("decode machine ledger: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return MachineLedger{}, err
	}
	if err := requireMachineLedgerFields(body); err != nil {
		return MachineLedger{}, err
	}
	if err := validateMachineLedgerWireShape(wire); err != nil {
		return MachineLedger{}, err
	}

	value := MachineLedger{
		SchemaVersion:      wire.SchemaVersion,
		ProjectID:          wire.ProjectID,
		AcceptedRevision:   wire.AcceptedRevision,
		ReviewSHA256:       wire.ReviewSHA256,
		HistorySHA256:      wire.HistorySHA256,
		LastSuccessfulSync: wire.LastSuccessfulSync,
		Accounting:         wire.Accounting,
		Sessions:           wire.Sessions,
		Evidence:           make(map[string][]ledger.EvidenceRef, len(wire.Evidence)),
	}
	for _, entry := range wire.Evidence {
		if _, duplicate := value.Evidence[entry.ID]; duplicate {
			return MachineLedger{}, fmt.Errorf("duplicate evidence owner identity %q", entry.ID)
		}
		value.Evidence[entry.ID] = entry.Refs
	}
	if err := validateMachineLedger(value); err != nil {
		return MachineLedger{}, err
	}
	return value, nil
}

func RenderMachineLedger(value MachineLedger) ([]byte, error) {
	value = normalizedMachineLedger(value)
	if err := validateMachineLedger(value); err != nil {
		return nil, err
	}
	sessions := append([]ledger.SessionReport{}, value.Sessions...)
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].SessionID != sessions[j].SessionID {
			return sessions[i].SessionID < sessions[j].SessionID
		}
		return sessions[i].ID < sessions[j].ID
	})
	evidenceIDs := make([]string, 0, len(value.Evidence))
	for id := range value.Evidence {
		evidenceIDs = append(evidenceIDs, id)
	}
	sort.Strings(evidenceIDs)
	evidence := make([]evidenceWire, 0, len(evidenceIDs))
	for _, id := range evidenceIDs {
		evidence = append(evidence, evidenceWire{ID: id, Refs: value.Evidence[id]})
	}
	wire := machineLedgerWire{
		SchemaVersion:      value.SchemaVersion,
		ProjectID:          value.ProjectID,
		AcceptedRevision:   value.AcceptedRevision,
		ReviewSHA256:       value.ReviewSHA256,
		HistorySHA256:      value.HistorySHA256,
		LastSuccessfulSync: value.LastSuccessfulSync,
		Accounting:         value.Accounting,
		Sessions:           sessions,
		Evidence:           evidence,
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(wire); err != nil {
		return nil, fmt.Errorf("encode machine ledger: %w", err)
	}
	if output.Len() > MaxMachineLedgerBytes {
		return nil, fmt.Errorf("machine ledger exceeds %d bytes", MaxMachineLedgerBytes)
	}
	return output.Bytes(), nil
}

func normalizedMachineLedger(value MachineLedger) MachineLedger {
	value.Accounting.Models = append([]accounting.ProjectModelSummary{}, value.Accounting.Models...)
	value.Sessions = append([]ledger.SessionReport{}, value.Sessions...)
	for index := range value.Sessions {
		session := &value.Sessions[index]
		session.GoalChanges = append([]string{}, session.GoalChanges...)
		session.Phases = append([]ledger.SessionPhase{}, session.Phases...)
		for phaseIndex := range session.Phases {
			session.Phases[phaseIndex].Evidence = append([]ledger.EvidenceRef{}, session.Phases[phaseIndex].Evidence...)
		}
		session.Files = append([]string{}, session.Files...)
		session.Commits = append([]string{}, session.Commits...)
		session.Verification = append([]string{}, session.Verification...)
		session.DecisionsAdded = append([]string{}, session.DecisionsAdded...)
		session.DecisionsRevised = append([]string{}, session.DecisionsRevised...)
		session.OpenLoopsCreated = append([]string{}, session.OpenLoopsCreated...)
		session.OpenLoopsClosed = append([]string{}, session.OpenLoopsClosed...)
		session.Evidence = append([]ledger.EvidenceRef{}, session.Evidence...)
		if session.Accounting != nil {
			accountingCopy := *session.Accounting
			accountingCopy.Models = append([]accounting.ModelAccounting{}, session.Accounting.Models...)
			session.Accounting = &accountingCopy
		}
	}
	if value.Evidence == nil {
		value.Evidence = make(map[string][]ledger.EvidenceRef)
	}
	return value
}

func validateMachineLedgerWireShape(wire machineLedgerWire) error {
	if wire.Sessions == nil || wire.Evidence == nil || wire.Accounting.Models == nil {
		return errors.New("machine ledger array fields must not be null or omitted")
	}
	for _, session := range wire.Sessions {
		if session.GoalChanges == nil || session.Phases == nil || session.Files == nil ||
			session.Commits == nil || session.Verification == nil || session.DecisionsAdded == nil ||
			session.DecisionsRevised == nil || session.OpenLoopsCreated == nil ||
			session.OpenLoopsClosed == nil || session.Evidence == nil {
			return fmt.Errorf("session %q array fields must not be null or omitted", session.ID)
		}
		for index, phase := range session.Phases {
			if phase.Evidence == nil {
				return fmt.Errorf("session %q phase %d evidence must not be null or omitted", session.ID, index)
			}
		}
		if session.Accounting != nil && session.Accounting.Models == nil {
			return fmt.Errorf("session %q accounting models must not be null or omitted", session.ID)
		}
	}
	for _, entry := range wire.Evidence {
		if entry.Refs == nil {
			return fmt.Errorf("evidence owner %q refs must not be null or omitted", entry.ID)
		}
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("machine ledger contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing machine ledger data: %w", err)
	}
	return nil
}

func requireMachineLedgerFields(body []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return fmt.Errorf("decode machine ledger fields: %w", err)
	}
	for _, name := range []string{
		"schema_version", "project_id", "accepted_revision", "review_sha256",
		"history_sha256", "accounting", "sessions", "evidence",
	} {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("machine ledger field %q is required", name)
		}
	}
	return nil
}
