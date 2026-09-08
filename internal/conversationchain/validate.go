package conversationchain

import (
	"errors"
	"fmt"
	"regexp"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/memory"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validID(value string) bool {
	return utf8.ValidString(value) && len(value) <= 256 && idPattern.MatchString(value)
}
func validText(value string, limit int) bool {
	return utf8.ValidString(value) && len([]byte(value)) <= limit
}

func Validate(document Document) error {
	if document.SchemaVersion != 1 || document.MinimumReaderVersion != "0.4.0" || !validID(document.ProjectID) || !validID(document.Provider) || !validID(document.SessionID) || !digestPattern.MatchString(document.Digest) || !digestPattern.MatchString(document.SessionViewDigest) || !digestPattern.MatchString(document.DependencyDigest) || !validID(document.SegmentationRuleVersion) {
		return errors.New("invalid conversation chain metadata")
	}
	if len(document.TurnUnits) > 65536 {
		return errors.New("conversation chain exceeds turn limit")
	}
	if document.DependencyProofV1 != nil {
		if err := validateDependencyProof(document, *document.DependencyProofV1); err != nil {
			return err
		}
	}
	if document.Coverage.SourceMessages > MaxWireInteger || document.Coverage.CapturedMessages > MaxWireInteger || document.Coverage.TurnUnits > MaxWireInteger || document.Coverage.UnansweredUnits > MaxWireInteger || document.Coverage.TruncatedMessages > MaxWireInteger {
		return errors.New("conversation chain coverage exceeds the wire integer maximum")
	}
	var captured, unanswered, truncated uint64
	turnIDs := make(map[string]bool, len(document.TurnUnits))
	for index, turn := range document.TurnUnits {
		if !validID(turn.TurnUnitID) || turnIDs[turn.TurnUnitID] || turn.Ordinal != uint64(index+1) || !validTimestamp(turn.StartedAt) || (turn.EndedAt != nil && !validTimestamp(*turn.EndedAt)) {
			return fmt.Errorf("invalid turn unit %d", index)
		}
		turnIDs[turn.TurnUnitID] = true
		if len(turn.AssistantMessages) > 65536 || len(turn.Actions) > 65536 || len(turn.Results) > 65536 {
			return fmt.Errorf("turn unit %q exceeds item limit", turn.TurnUnitID)
		}
		if err := validateMessage(document, turn.UserMessage, RoleUser); err != nil {
			return fmt.Errorf("turn unit %q user message: %w", turn.TurnUnitID, err)
		}
		if !incrementWireCount(&captured) {
			return errors.New("captured message count exceeds the wire integer maximum")
		}
		if turn.UserMessage.Truncated {
			if !incrementWireCount(&truncated) {
				return errors.New("truncated message count exceeds the wire integer maximum")
			}
		}
		for _, message := range turn.AssistantMessages {
			if err := validateMessage(document, message, RoleAssistant); err != nil {
				return fmt.Errorf("turn unit %q assistant message: %w", turn.TurnUnitID, err)
			}
			if !incrementWireCount(&captured) {
				return errors.New("captured message count exceeds the wire integer maximum")
			}
			if message.Truncated {
				if !incrementWireCount(&truncated) {
					return errors.New("truncated message count exceeds the wire integer maximum")
				}
			}
		}
		for _, action := range turn.Actions {
			if !validID(action.RevisionID) || !validID(action.Kind) || !validText(action.Excerpt, 4096) || (action.ToolName != nil && !validID(*action.ToolName)) {
				return fmt.Errorf("turn unit %q has invalid action", turn.TurnUnitID)
			}
			if err := validateSourceRef(document, action.SourceRef); err != nil {
				return err
			}
		}
		for _, result := range turn.Results {
			if !validID(result.RevisionID) || !validID(result.Kind) || !validText(result.Excerpt, 4096) {
				return fmt.Errorf("turn unit %q has invalid result", turn.TurnUnitID)
			}
			switch result.VerificationState {
			case "unknown", "passed", "failed", "partial":
			default:
				return fmt.Errorf("turn unit %q has invalid verification state", turn.TurnUnitID)
			}
			if err := validateSourceRef(document, result.SourceRef); err != nil {
				return err
			}
		}
		switch turn.AnswerState {
		case AnswerNone:
			if len(turn.AssistantMessages) != 0 {
				return fmt.Errorf("turn unit %q claims no answer but has assistant messages", turn.TurnUnitID)
			}
			if !incrementWireCount(&unanswered) {
				return errors.New("unanswered unit count exceeds the wire integer maximum")
			}
		case AnswerAnswered, AnswerPartial:
			if len(turn.AssistantMessages) == 0 {
				return fmt.Errorf("turn unit %q claims an answer without assistant messages", turn.TurnUnitID)
			}
		default:
			return fmt.Errorf("turn unit %q has invalid answer state", turn.TurnUnitID)
		}
	}
	if document.Coverage.TurnUnits != uint64(len(document.TurnUnits)) || document.Coverage.CapturedMessages != captured || document.Coverage.SourceMessages < captured || document.Coverage.UnansweredUnits != unanswered || document.Coverage.TruncatedMessages != truncated {
		return errors.New("conversation chain coverage does not reconcile")
	}
	return nil
}

func validateDependencyProof(document Document, proof DependencyProofV1) error {
	if !digestPattern.MatchString(proof.SessionViewDigest) || !digestPattern.MatchString(proof.SourceRecordDigest) || !validID(proof.RuleVersion) || !validID(proof.RedactionVersion) {
		return errors.New("invalid conversation dependency proof metadata")
	}
	if len(proof.VisibleRecords) > 100000 {
		return errors.New("conversation dependency proof exceeds visible-record limit")
	}
	var previous uint64
	for index, record := range proof.VisibleRecords {
		if record.RecordOrdinal == 0 || record.RecordOrdinal > MaxWireInteger || !shaPattern.MatchString(record.SourceHash) || (index > 0 && record.RecordOrdinal <= previous) {
			return errors.New("conversation dependency proof has invalid visible records")
		}
		previous = record.RecordOrdinal
	}
	if len(proof.ActiveRevisionIDs) > 65536 {
		return errors.New("conversation dependency proof exceeds active-revision limit")
	}
	for index, revisionID := range proof.ActiveRevisionIDs {
		if !digestPattern.MatchString(revisionID) || (index > 0 && proof.ActiveRevisionIDs[index-1] >= revisionID) {
			return errors.New("conversation dependency proof has invalid active revisions")
		}
	}
	want, err := memory.Digest(proof)
	if err != nil || document.DependencyDigest != want {
		return errors.Join(errors.New("conversation dependency digest does not match proof"), err)
	}
	if proof.SessionViewDigest != document.SessionViewDigest {
		return errors.New("conversation dependency proof view binding mismatch")
	}
	if proof.RuleVersion != document.SegmentationRuleVersion {
		return errors.New("conversation dependency proof rule binding mismatch")
	}
	records := make(map[uint64]string, len(proof.VisibleRecords))
	for _, record := range proof.VisibleRecords {
		records[record.RecordOrdinal] = record.SourceHash
	}
	if err := forEachRetainedMessage(document, func(message Message) error {
		if records[message.SourceRef.RecordOrdinal] != message.SourceRef.SourceHash {
			return errors.New("retained message is absent from conversation dependency proof")
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

func forEachRetainedMessage(document Document, visit func(Message) error) error {
	for _, turn := range document.TurnUnits {
		if err := visit(turn.UserMessage); err != nil {
			return err
		}
		for _, message := range turn.AssistantMessages {
			if err := visit(message); err != nil {
				return err
			}
		}
	}
	return nil
}

func dependencyProofDigest(proof DependencyProofV1) string {
	digest, err := memory.Digest(proof)
	if err != nil {
		return ""
	}
	return digest
}

func validateMessage(document Document, message Message, expected Role) error {
	if message.Role != expected || !validID(message.RevisionID) || !validTimestamp(message.OccurredAt) || !validText(message.VisibleExcerpt, 4096) {
		return errors.New("invalid visible message")
	}
	return validateSourceRef(document, message.SourceRef)
}

func validateSourceRef(document Document, ref SourceRef) error {
	if ref.Provider != document.Provider || ref.SessionID != document.SessionID || !validID(ref.Provider) || !validID(ref.SessionID) || !validID(ref.SourceIdentity) || ref.RecordOrdinal > MaxWireInteger || !shaPattern.MatchString(ref.SourceHash) {
		return errors.New("source reference is not authenticated to the conversation identity")
	}
	return nil
}

func incrementWireCount(value *uint64) bool {
	if *value == MaxWireInteger {
		return false
	}
	*value++
	return true
}

func validTimestamp(value string) bool { return value != "" && validText(value, 128) }
