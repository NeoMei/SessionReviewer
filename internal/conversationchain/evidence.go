package conversationchain

import (
	"errors"
	"fmt"
	"sort"

	"github.com/neomei/SessionReviewer/internal/memory"
)

// ValidateRetainedEvidence authenticates the source coordinates and exact
// deterministic retained projection of every typed fact in document against
// one immutable SessionView and its complete active revision set.
func ValidateRetainedEvidence(document Document, view memory.SessionView, revisions []memory.ObservationRevision) error {
	if err := ValidateDependencyProof(document, view); err != nil {
		return fmt.Errorf("conversation chain dependency proof: %w", err)
	}

	active := make(map[string]memory.ObservationRevision, len(revisions))
	summaries := make(map[string]memory.ObservationSummary, len(view.ObservationSummaries))
	for _, summary := range view.ObservationSummaries {
		summaries[summary.RevisionID] = summary
	}
	if len(revisions) != len(view.ActiveRevisionIDs) {
		return errors.New("supplied revisions do not cover active SessionView")
	}
	for index, revision := range revisions {
		if err := memory.ValidateObservationRevision(revision); err != nil {
			return fmt.Errorf("invalid active revision %d: %w", index, err)
		}
		if revision.Key.ProjectID != view.ProjectID || revision.Key.Provider != view.Provider || revision.Key.SessionID != view.SessionID || revision.Key.SourceIdentity != view.SourceIdentity {
			return errors.New("active revision identity does not match SessionView")
		}
		if _, duplicate := active[revision.RevisionID]; duplicate {
			return errors.New("duplicate supplied active revision")
		}
		summary, exists := summaries[revision.RevisionID]
		if !exists || !observationSummaryEqual(summary, observationSummary(revision)) {
			return errors.New("active revision diverges from SessionView summary")
		}
		active[revision.RevisionID] = revision
	}
	for _, revisionID := range view.ActiveRevisionIDs {
		if _, exists := active[revisionID]; !exists {
			return errors.New("SessionView active revision is absent from supplied revisions")
		}
	}

	seenEvidence := make(map[string]struct{})
	var previousUser uint64
	for turnIndex, turn := range document.TurnUnits {
		start := turn.UserMessage.SourceRef.RecordOrdinal
		if start <= previousUser {
			return errors.New("conversation user turns contradict source order")
		}
		previousUser = start
		end := uint64(MaxWireInteger) + 1
		if turnIndex+1 < len(document.TurnUnits) {
			end = document.TurnUnits[turnIndex+1].UserMessage.SourceRef.RecordOrdinal
		}
		if err := validateRetainedMessageSource(turn.UserMessage, view, start, end); err != nil {
			return fmt.Errorf("turn %d user message: %w", turnIndex, err)
		}
		var previousMessage = start
		for _, message := range turn.AssistantMessages {
			if err := validateRetainedMessageSource(message, view, start, end); err != nil {
				return fmt.Errorf("turn %d assistant message: %w", turnIndex, err)
			}
			if message.SourceRef.RecordOrdinal <= previousMessage {
				return errors.New("visible messages contradict source order")
			}
			previousMessage = message.SourceRef.RecordOrdinal
		}
		var previousAction uint64
		for _, action := range turn.Actions {
			if action.SourceRef.RecordOrdinal < previousAction {
				return errors.New("retained actions contradict source order")
			}
			previousAction = action.SourceRef.RecordOrdinal
			if err := validateRetainedAction(action, view, active, start, end, seenEvidence); err != nil {
				return fmt.Errorf("turn %d action: %w", turnIndex, err)
			}
		}
		var previousResult uint64
		for _, result := range turn.Results {
			if result.SourceRef.RecordOrdinal < previousResult {
				return errors.New("retained results contradict source order")
			}
			previousResult = result.SourceRef.RecordOrdinal
			if err := validateRetainedResult(result, view, active, start, end, seenEvidence); err != nil {
				return fmt.Errorf("turn %d result: %w", turnIndex, err)
			}
		}
	}
	return nil
}

// ValidateDependencyProof binds the document's content-free dependency
// preimage to one authenticated SessionView and every retained message.
func ValidateDependencyProof(document Document, view memory.SessionView) error {
	if err := Validate(document); err != nil {
		return fmt.Errorf("invalid conversation chain: %w", err)
	}
	if err := memory.ValidateSessionView(view); err != nil {
		return fmt.Errorf("invalid SessionView: %w", err)
	}
	if document.ProjectID != view.ProjectID || document.Provider != view.Provider || document.SessionID != view.SessionID || document.SessionViewDigest != view.Digest {
		return errors.New("conversation chain identity does not match SessionView")
	}
	proof := document.DependencyProofV1
	if proof == nil {
		return errors.New("dependency proof is required")
	}
	if proof.SessionViewDigest != view.Digest {
		return errors.New("SessionView digest does not match authenticated view")
	}
	if proof.SourceRecordDigest != view.SourceRecordDigest {
		return errors.New("source-record digest does not match authenticated view")
	}
	active := append([]string(nil), view.ActiveRevisionIDs...)
	sort.Strings(active)
	if len(active) != len(proof.ActiveRevisionIDs) {
		return errors.New("active revisions do not match authenticated view")
	}
	for index := range active {
		if active[index] != proof.ActiveRevisionIDs[index] {
			return errors.New("active revisions do not match authenticated view")
		}
	}
	if err := forEachRetainedMessage(document, func(message Message) error {
		if message.SourceRef.SourceIdentity != view.SourceIdentity {
			return errors.New("retained message source identity does not match authenticated view")
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

func validateRetainedMessageSource(message Message, view memory.SessionView, start, end uint64) error {
	if err := validateEvidenceSourceRef(message.SourceRef, view, start, end); err != nil {
		return err
	}
	return nil
}

func validateRetainedAction(action Action, view memory.SessionView, active map[string]memory.ObservationRevision, start, end uint64, seen map[string]struct{}) error {
	revision, exists := active[action.RevisionID]
	if !exists {
		return errors.New("action revision is not active in SessionView")
	}
	if _, duplicate := seen[action.RevisionID]; duplicate {
		return errors.New("duplicate retained evidence revision")
	}
	policy, supported := retainedFactPolicyFor(revision)
	toolName := retainedToolName(revision, policy)
	if !supported || !policy.action || action.Kind != policy.kind || (action.ToolName == nil) != (toolName == nil) || action.ToolName != nil && *action.ToolName != *toolName || action.Excerpt != retainedFactExcerpt(revision, policy) {
		return errors.New("action does not match deterministic retained projection")
	}
	if err := validateEvidenceRevisionSource(action.SourceRef, revision, view, start, end); err != nil {
		return err
	}
	seen[action.RevisionID] = struct{}{}
	return nil
}

func validateRetainedResult(result Result, view memory.SessionView, active map[string]memory.ObservationRevision, start, end uint64, seen map[string]struct{}) error {
	revision, exists := active[result.RevisionID]
	if !exists {
		return errors.New("result revision is not active in SessionView")
	}
	if _, duplicate := seen[result.RevisionID]; duplicate {
		return errors.New("duplicate retained evidence revision")
	}
	policy, supported := retainedFactPolicyFor(revision)
	if !supported || policy.action || result.Kind != policy.kind || result.VerificationState != policy.state || result.Excerpt != retainedFactExcerpt(revision, policy) {
		return errors.New("result does not match deterministic retained projection")
	}
	if err := validateEvidenceRevisionSource(result.SourceRef, revision, view, start, end); err != nil {
		return err
	}
	seen[result.RevisionID] = struct{}{}
	return nil
}

func validateEvidenceRevisionSource(ref SourceRef, revision memory.ObservationRevision, view memory.SessionView, start, end uint64) error {
	if revision.Ref.Location.RecordOrdinal() <= 0 {
		return errors.New("retained evidence revision lacks an exact source ordinal")
	}
	if err := validateEvidenceSourceRef(ref, view, start, end); err != nil {
		return err
	}
	if ref.Provider != revision.Ref.Provider || ref.SessionID != revision.Ref.SessionID || ref.SourceIdentity != revision.Ref.SourceIdentity || ref.RecordOrdinal != uint64(revision.Ref.Location.RecordOrdinal()) || ref.SourceHash != revision.Ref.SourceHash {
		return errors.New("retained evidence source coordinate does not match active revision")
	}
	return nil
}

func validateEvidenceSourceRef(ref SourceRef, view memory.SessionView, start, end uint64) error {
	if ref.Provider != view.Provider || ref.SessionID != view.SessionID || ref.SourceIdentity != view.SourceIdentity {
		return errors.New("source reference identity does not match SessionView")
	}
	if ref.RecordOrdinal == 0 || ref.RecordOrdinal < start || ref.RecordOrdinal >= end {
		return errors.New("source reference is outside its user-turn interval")
	}
	return nil
}
