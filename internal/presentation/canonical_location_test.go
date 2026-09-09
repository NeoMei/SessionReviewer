package presentation

import (
	"testing"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
)

func TestCanonicalCoordinatesProduceAuthenticatedMilestones(t *testing.T) {
	messages := milestoneMessages("verify", "passed")
	input := milestoneProjectInput(t, milestoneFixtureSpec{provider: "opencode", messages: messages, facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, fields: map[string]string{"passed": "true", "failed": "false"}}}})
	session := &input.Sessions[0]
	for i := range session.Revisions {
		revision := &session.Revisions[i]
		revision.Ref.Location = memory.SourceLocation{Kind: memory.SourceLocationCanonical, Canonical: &memory.CanonicalSourceLocation{Record: 2}}
		revision.RevisionID = memory.ObservationRevisionID(*revision)
	}
	session.View = milestoneSessionView("opencode", "session-1", "source-1", session.Revisions)
	var err error
	session.Chain, _, err = conversationchain.Materialize(conversationchain.MaterializeInput{View: session.View, Messages: messages, Revisions: session.Revisions, SourceCoverage: conversationchain.VisibleCoverage{SourceRecords: 3, VisibleMessages: 2, CapturedMessages: 2, Complete: true}, RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ProjectMilestones(input)
	if err != nil {
		t.Fatal(err)
	}
	if result.QualifyingFacts != 1 || len(result.Timeline) != 1 || result.Timeline[0].Kind != "machine_verification" {
		t.Fatalf("canonical milestone missing: %+v", result)
	}
}
