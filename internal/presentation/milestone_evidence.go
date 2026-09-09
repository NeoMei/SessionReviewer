package presentation

import (
	"strings"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

const (
	milestoneAnswerBytes  = 4096
	milestoneSegmentBytes = 16384
)

func milestoneClosure(session MilestoneSessionInput, revisionsByID map[string]memory.ObservationRevision, turn conversationchain.TurnUnit, ref reviewv4.SourceTurnRef) reviewv4.ClosedLoop {
	incomplete := milestoneTurnIncomplete(session.Chain, turn)
	coverage := reviewv4.ClosedLoopCoverage{SourceTurns: 1, CapturedTurns: 1}
	if session.View.SourceAvailability == "source_unavailable" {
		coverage.SourceUnavailableTurns = 1
	} else if incomplete {
		coverage.TruncatedTurns = 1
	}
	return reviewv4.ClosedLoop{
		TriggerQuestion: presentMilestoneSegment(turn.UserMessage.VisibleExcerpt, ref, incomplete), Conclusion: milestoneConclusion(turn, ref),
		Execution: milestoneExecution(turn, ref, incomplete), Verification: milestoneVerification(revisionsByID, turn, ref, incomplete), ImpactAndFollowUp: missingMilestoneSegment("not_captured"),
		SourceTurnRefs: []reviewv4.SourceTurnRef{ref}, Coverage: coverage,
	}
}

func milestoneConclusion(turn conversationchain.TurnUnit, ref reviewv4.SourceTurnRef) reviewv4.ClosedLoopConclusion {
	for index := len(turn.AssistantMessages) - 1; index >= 0; index-- {
		text := boundedMilestoneText(turn.AssistantMessages[index].VisibleExcerpt, milestoneAnswerBytes)
		if strings.TrimSpace(text) != "" {
			return reviewv4.ClosedLoopConclusion{Kind: reviewv4.ConclusionVisibleAnswerExcerpt, Text: text, SourceTurnRefs: []reviewv4.SourceTurnRef{ref}}
		}
	}
	reason := "no_visible_answer"
	return reviewv4.ClosedLoopConclusion{Kind: reviewv4.ConclusionMissing, Text: "", MissingReason: &reason, SourceTurnRefs: []reviewv4.SourceTurnRef{}}
}

func milestoneExecution(turn conversationchain.TurnUnit, ref reviewv4.SourceTurnRef, incomplete bool) reviewv4.ClosedLoopSegment {
	parts := make([]string, 0, len(turn.Actions)+len(turn.Results))
	for _, action := range turn.Actions {
		parts = append(parts, renderMilestoneEvidence(action.Kind, "", action.Excerpt))
	}
	for _, result := range turn.Results {
		if result.Kind != "verification" {
			parts = append(parts, renderMilestoneEvidence(result.Kind, result.VerificationState, result.Excerpt))
		}
	}
	if len(parts) == 0 {
		return missingMilestoneSegment("no_execution_evidence")
	}
	return presentMilestoneSegment(strings.Join(parts, "\n"), ref, incomplete)
}

func milestoneVerification(revisionsByID map[string]memory.ObservationRevision, turn conversationchain.TurnUnit, ref reviewv4.SourceTurnRef, incomplete bool) reviewv4.ClosedLoopSegment {
	parts := make([]string, 0)
	for _, result := range turn.Results {
		if result.Kind == "verification" {
			parts = append(parts, renderMilestoneEvidence(result.Kind, milestoneVerificationDisplayState(revisionsByID[result.RevisionID]), result.Excerpt))
		}
	}
	if len(parts) == 0 {
		return missingMilestoneSegment("not_verified")
	}
	return presentMilestoneSegment(strings.Join(parts, "\n"), ref, incomplete)
}

func milestoneVerificationDisplayState(revision memory.ObservationRevision) string {
	if milestoneVerificationPassed(revision) {
		return "passed"
	}
	outcome := strings.ToLower(strings.TrimSpace(revision.Outcome))
	if outcome == "passed" || outcome == "success" {
		return "conflict"
	}
	if outcome == "failed" || outcome == "failure" || outcome == "error" {
		if value, present := revision.Fields["exit_code"]; present && value == "0" {
			return "conflict"
		}
		if value, present := revision.Fields["failed"]; present && (value == "0" || value == "false") {
			return "conflict"
		}
		return "failed"
	}
	return "unknown"
}

func presentMilestoneSegment(text string, ref reviewv4.SourceTurnRef, partial bool) reviewv4.ClosedLoopSegment {
	state := "present"
	if partial {
		state = "partial"
	}
	return reviewv4.ClosedLoopSegment{State: state, Text: boundedMilestoneText(text, milestoneSegmentBytes), SourceTurnRefs: []reviewv4.SourceTurnRef{ref}}
}

func missingMilestoneSegment(reasonText string) reviewv4.ClosedLoopSegment {
	reason := reasonText
	return reviewv4.ClosedLoopSegment{State: "missing", Text: "", MissingReason: &reason, SourceTurnRefs: []reviewv4.SourceTurnRef{}}
}

func milestoneTurnIncomplete(chain conversationchain.Document, turn conversationchain.TurnUnit) bool {
	if chain.MaterializationCoverageV1 != nil && chain.MaterializationCoverageV1.SourceIncomplete {
		return true
	}
	if turn.AnswerState == conversationchain.AnswerPartial || turn.UserMessage.Truncated {
		return true
	}
	for _, message := range turn.AssistantMessages {
		if message.Truncated {
			return true
		}
	}
	return false
}

func renderMilestoneFact(fact qualifiedMilestoneFact) string {
	state := ""
	if fact.category == "verification" {
		state = "passed"
	}
	return renderMilestoneEvidence(fact.revision.Operation, state, retainedMilestoneFields(fact))
}

func retainedMilestoneFields(fact qualifiedMilestoneFact) string {
	allowed := map[string][]string{
		"verification": {"component", "status", "exit_code", "passed", "failed", "tool_id"},
		"commit":       {"git_head"}, "release": {"release_id", "tag", "version", "status", "target"},
		"deployment": {"release_id", "version", "status", "target", "component"}, "version": {"version", "component"},
	}
	parts := make([]string, 0)
	for _, key := range allowed[fact.category] {
		if value, exists := fact.revision.Fields[key]; exists {
			parts = append(parts, key+"="+value)
		}
	}
	return strings.Join(parts, "; ")
}

func renderMilestoneEvidence(kind, state, excerpt string) string {
	label := kind
	if state != "" {
		label += " (" + state + ")"
	}
	if excerpt != "" {
		label += ": " + excerpt
	}
	return boundedMilestoneText(label, milestoneSegmentBytes)
}

func boundedMilestoneText(value string, limit int) string {
	value = redact.AbsolutePaths(redact.Default().Text(value).Text)
	if len(value) <= limit {
		return value
	}
	const suffix = "…"
	end := limit - len(suffix)
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + suffix
}
