package conversationchain

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/redact"
)

const retainedEvidenceBytes = 1024

type MaterializeInput struct {
	View             memory.SessionView
	Messages         []SourceMessage
	Revisions        []memory.ObservationRevision
	SourceCoverage   VisibleCoverage
	RuleVersion      string
	RedactionVersion string
}

type MaterializeReport struct {
	UnassignedFacts  uint64
	UnsupportedFacts uint64
	SourceIncomplete bool
}

type retainedRecordIdentity struct {
	RecordOrdinal uint64 `json:"record_ordinal"`
	SourceHash    string `json:"source_hash"`
}

type retainedDependencyIdentity struct {
	SessionViewDigest  string                   `json:"session_view_digest"`
	SourceRecordDigest string                   `json:"source_record_digest"`
	VisibleRecords     []retainedRecordIdentity `json:"visible_records"`
	ActiveRevisionIDs  []string                 `json:"active_revision_ids"`
	RuleVersion        string                   `json:"rule_version"`
	RedactionVersion   string                   `json:"redaction_version"`
}

// Materialize builds the bounded retained conversation chain from an
// authenticated SessionView, visible source messages, and its exact active
// observation revisions. Source ordinals, not wall clocks, define causality.
func Materialize(input MaterializeInput) (Document, MaterializeReport, error) {
	var report MaterializeReport
	if err := validateMaterializeInput(input); err != nil {
		return Document{}, report, err
	}

	visibleTurns, visibleCoverage := MaterializeVisible(input.View.Provider, input.View.SessionID, input.View.SourceIdentity, input.Messages)
	report.SourceIncomplete = sourceCoverageIncomplete(input.SourceCoverage)
	document := Document{
		SchemaVersion: 1, MinimumReaderVersion: "0.4.0", Digest: zeroDigest(),
		ProjectID: input.View.ProjectID, Provider: input.View.Provider, SessionID: input.View.SessionID,
		SessionViewDigest: input.View.Digest, SegmentationRuleVersion: input.RuleVersion,
		TurnUnits: make([]TurnUnit, 0, len(visibleTurns)),
	}
	for _, visible := range visibleTurns {
		turn := TurnUnit{
			TurnUnitID: visible.TurnUnitID, Ordinal: visible.Ordinal, StartedAt: visible.StartedAt, EndedAt: visible.EndedAt,
			UserMessage: retainedWireMessage(visible.UserMessage), AssistantMessages: []Message{}, Actions: []Action{}, Results: []Result{},
			AnswerState: AnswerNone,
		}
		lastPhase := ""
		for _, message := range visible.Messages[1:] {
			turn.AssistantMessages = append(turn.AssistantMessages, retainedWireMessage(message))
			if message.Phase != nil {
				lastPhase = *message.Phase
			}
		}
		lastHasText := len(turn.AssistantMessages) != 0 && strings.TrimSpace(turn.AssistantMessages[len(turn.AssistantMessages)-1].VisibleExcerpt) != ""
		switch {
		case len(turn.AssistantMessages) == 0:
			turn.AnswerState = AnswerNone
		case (lastPhase == "" || lastPhase == "final_answer") && lastHasText && !report.SourceIncomplete:
			turn.AnswerState = AnswerAnswered
		default:
			turn.AnswerState = AnswerPartial
		}
		document.TurnUnits = append(document.TurnUnits, turn)
	}

	revisions := append([]memory.ObservationRevision(nil), input.Revisions...)
	sort.Slice(revisions, func(i, j int) bool {
		left, right := revisions[i], revisions[j]
		leftLine, rightLine := left.Ref.Location.JSONL.Line, right.Ref.Location.JSONL.Line
		if leftLine != rightLine {
			return leftLine < rightLine
		}
		if left.Key.Sequence != right.Key.Sequence {
			return left.Key.Sequence < right.Key.Sequence
		}
		return left.RevisionID < right.RevisionID
	})
	for _, revision := range revisions {
		turnIndex := retainedTurnForOrdinal(document.TurnUnits, uint64(revision.Ref.Location.JSONL.Line))
		kind, state, action, supported := retainedFactSemantics(revision)
		if !supported {
			report.UnsupportedFacts++
			continue
		}
		if turnIndex < 0 {
			report.UnassignedFacts++
			continue
		}
		ref := SourceRef{
			Provider: revision.Ref.Provider, SessionID: revision.Ref.SessionID, SourceIdentity: revision.Ref.SourceIdentity,
			RecordOrdinal: uint64(revision.Ref.Location.JSONL.Line), SourceHash: revision.Ref.SourceHash,
		}
		excerpt := retainedFactExcerpt(revision)
		if action {
			document.TurnUnits[turnIndex].Actions = append(document.TurnUnits[turnIndex].Actions, Action{RevisionID: revision.RevisionID, SourceRef: ref, Kind: kind, ToolName: nil, Excerpt: excerpt})
		} else {
			document.TurnUnits[turnIndex].Results = append(document.TurnUnits[turnIndex].Results, Result{RevisionID: revision.RevisionID, SourceRef: ref, Kind: kind, VerificationState: state, Excerpt: excerpt})
		}
	}

	document.Coverage.SourceMessages = visibleCoverage.VisibleMessages
	document.Coverage.TurnUnits = uint64(len(document.TurnUnits))
	for _, turn := range document.TurnUnits {
		document.Coverage.CapturedMessages += 1 + uint64(len(turn.AssistantMessages))
		if turn.AnswerState == AnswerNone {
			document.Coverage.UnansweredUnits++
		}
		if turn.UserMessage.Truncated {
			document.Coverage.TruncatedMessages++
		}
		for _, message := range turn.AssistantMessages {
			if message.Truncated {
				document.Coverage.TruncatedMessages++
			}
		}
	}
	dependency, err := retainedDependencyDigest(input)
	if err != nil {
		return Document{}, report, fmt.Errorf("digest retained conversation dependencies: %w", err)
	}
	document.DependencyDigest = dependency
	body, err := Render(document)
	if err != nil {
		return Document{}, report, fmt.Errorf("render retained conversation: %w", err)
	}
	canonical, err := Parse(body)
	if err != nil {
		return Document{}, report, fmt.Errorf("parse retained conversation: %w", err)
	}
	return canonical, report, nil
}

func validateMaterializeInput(input MaterializeInput) error {
	if err := memory.ValidateSessionView(input.View); err != nil {
		return fmt.Errorf("invalid SessionView: %w", err)
	}
	if !validID(input.RuleVersion) || !validID(input.RedactionVersion) {
		return errors.New("invalid retained materializer version")
	}
	counts := []uint64{
		input.SourceCoverage.SourceRecords, input.SourceCoverage.VisibleMessages, input.SourceCoverage.CapturedMessages,
		input.SourceCoverage.TruncatedMessages, input.SourceCoverage.TruncatedBodies, input.SourceCoverage.ContextMessages,
		input.SourceCoverage.OrphanMessages, input.SourceCoverage.OversizedRecords, input.SourceCoverage.MalformedRecords,
	}
	for _, count := range counts {
		if count > MaxWireInteger {
			return errors.New("visible source coverage exceeds the wire integer maximum")
		}
	}
	if input.SourceCoverage.SourceRecords < uint64(len(input.Messages)) || input.SourceCoverage.VisibleMessages < input.SourceCoverage.CapturedMessages {
		return errors.New("visible source coverage does not reconcile")
	}
	seenCoordinates := make(map[uint64]struct{}, len(input.Messages))
	var previous uint64
	for index, message := range input.Messages {
		if message.Role != RoleUser && message.Role != RoleAssistant {
			return fmt.Errorf("message %d has forbidden role", index)
		}
		if message.Phase != "" && message.Phase != "commentary" && message.Phase != "final_answer" {
			return fmt.Errorf("message %d has invalid phase", index)
		}
		if message.RecordOrdinal == 0 || message.RecordOrdinal > MaxWireInteger || message.RecordOrdinal > input.SourceCoverage.SourceRecords || !shaPattern.MatchString(message.RecordHash) {
			return fmt.Errorf("message %d has invalid source coordinate", index)
		}
		if _, err := time.Parse(time.RFC3339Nano, message.OccurredAt); err != nil {
			return fmt.Errorf("message %d has invalid timestamp", index)
		}
		if !utf8.ValidString(message.Text) {
			return fmt.Errorf("message %d has invalid UTF-8", index)
		}
		if index > 0 && message.RecordOrdinal <= previous {
			return errors.New("visible messages are not in strict source order")
		}
		if _, duplicate := seenCoordinates[message.RecordOrdinal]; duplicate {
			return errors.New("duplicate visible message source coordinate")
		}
		seenCoordinates[message.RecordOrdinal] = struct{}{}
		previous = message.RecordOrdinal
	}

	summaries := make(map[string]memory.ObservationSummary, len(input.View.ObservationSummaries))
	for _, summary := range input.View.ObservationSummaries {
		summaries[summary.RevisionID] = summary
	}
	if len(input.Revisions) != len(input.View.ActiveRevisionIDs) {
		return errors.New("supplied revisions do not cover the active SessionView")
	}
	seenRevisions := make(map[string]struct{}, len(input.Revisions))
	for index, revision := range input.Revisions {
		if _, duplicate := seenRevisions[revision.RevisionID]; duplicate {
			return errors.New("duplicate supplied observation revision")
		}
		seenRevisions[revision.RevisionID] = struct{}{}
		if err := memory.ValidateObservationRevision(revision); err != nil {
			return fmt.Errorf("invalid supplied observation revision %d: %w", index, err)
		}
		if revision.Key.Provider != input.View.Provider || revision.Key.SessionID != input.View.SessionID || revision.Key.SourceIdentity != input.View.SourceIdentity || revision.Key.ProjectID != input.View.ProjectID {
			return errors.New("observation revision belongs to another SessionView")
		}
		if revision.Ref.Location.Kind != memory.SourceLocationJSONL || revision.Ref.Location.JSONL == nil || revision.Ref.Location.JSONL.Line < 1 || uint64(revision.Ref.Location.JSONL.Line) > MaxWireInteger || uint64(revision.Ref.Location.JSONL.Line) > input.SourceCoverage.SourceRecords {
			return errors.New("observation revision has invalid source ordinal")
		}
		summary, active := summaries[revision.RevisionID]
		if !active || !reflect.DeepEqual(summary, observationSummary(revision)) {
			return errors.New("supplied observation revision diverges from active SessionView summary")
		}
	}
	return nil
}

func observationSummary(revision memory.ObservationRevision) memory.ObservationSummary {
	return memory.ObservationSummary{
		RevisionID: revision.RevisionID, Sequence: revision.Key.Sequence, Kind: revision.Key.Kind, Subject: revision.Key.Subject,
		OccurredAt: revision.Timestamp, Operation: revision.Operation, Object: revision.Object, Outcome: revision.Outcome,
		Fields: revision.Fields, Excerpt: revision.Excerpt,
	}
}

func sourceCoverageIncomplete(coverage VisibleCoverage) bool {
	return !coverage.Complete || coverage.OversizedRecords != 0 || coverage.MalformedRecords != 0 || coverage.TruncatedBodies != 0
}

func retainedWireMessage(message VisibleMessage) Message {
	excerpt, truncated := boundedRetainedText(message.VisibleExcerpt, 4096)
	return Message{
		Role: message.Role, RevisionID: message.RevisionID, SourceRef: message.SourceRef, OccurredAt: message.OccurredAt,
		VisibleExcerpt: excerpt, Truncated: message.Truncated || truncated,
	}
}

func boundedRetainedText(value string, limit int) (string, bool) {
	value = redact.AbsolutePaths(redact.Default().Text(value).Text)
	if len(value) <= limit {
		return value, false
	}
	const suffix = "…"
	end := limit - len(suffix)
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + suffix, true
}

func retainedTurnForOrdinal(turns []TurnUnit, ordinal uint64) int {
	index := -1
	for candidate := range turns {
		if turns[candidate].UserMessage.SourceRef.RecordOrdinal > ordinal {
			break
		}
		index = candidate
	}
	return index
}

func retainedFactSemantics(revision memory.ObservationRevision) (kind, state string, action, supported bool) {
	kind = revision.Operation
	if kind == "" {
		kind = revision.Key.Kind
	}
	if !validID(kind) {
		return "", "", false, false
	}
	switch {
	case revision.Key.Kind == "command" && revision.Operation == "command_started":
		return kind, "", true, true
	case revision.Key.Kind == "command" && revision.Operation == "command_finished":
		return kind, authoritativeState(revision.Outcome), false, true
	case revision.Key.Kind == "verification" && revision.Operation == "verification":
		return kind, authoritativeState(revision.Outcome), false, true
	case revision.Key.Kind == "file" && revision.Operation == "file_change" && revision.Outcome == "success":
		return kind, "", true, true
	case revision.Key.Kind == "file" && revision.Operation == "file_change" && revision.Outcome == "failure":
		return kind, "failed", false, true
	case revision.Key.Kind == "commit" || revision.Key.Kind == "release" || revision.Key.Kind == "deployment" || revision.Key.Kind == "version" || revision.Key.Kind == "branch":
		return kind, "unknown", false, true
	default:
		return "", "", false, false
	}
}

func authoritativeState(outcome string) string {
	switch outcome {
	case "passed", "success":
		return "passed"
	case "failed", "failure":
		return "failed"
	default:
		return "unknown"
	}
}

func retainedFactExcerpt(revision memory.ObservationRevision) string {
	parts := make([]string, 0, len(revision.Fields)+1)
	if revision.Excerpt != "" {
		parts = append(parts, revision.Excerpt)
	}
	keys := make([]string, 0, len(revision.Fields))
	for key := range revision.Fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts = append(parts, key+"="+revision.Fields[key])
	}
	excerpt, _ := boundedRetainedText(strings.Join(parts, "; "), retainedEvidenceBytes)
	return excerpt
}

func retainedDependencyDigest(input MaterializeInput) (string, error) {
	records := make([]retainedRecordIdentity, 0, len(input.Messages))
	for _, message := range input.Messages {
		records = append(records, retainedRecordIdentity{RecordOrdinal: message.RecordOrdinal, SourceHash: message.RecordHash})
	}
	active := append([]string(nil), input.View.ActiveRevisionIDs...)
	sort.Strings(active)
	return memory.Digest(retainedDependencyIdentity{
		SessionViewDigest: input.View.Digest, SourceRecordDigest: input.View.SourceRecordDigest,
		VisibleRecords: records, ActiveRevisionIDs: active, RuleVersion: input.RuleVersion, RedactionVersion: input.RedactionVersion,
	})
}
