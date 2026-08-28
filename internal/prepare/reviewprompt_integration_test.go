package prepare

import (
	"os"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/proposal"
	"github.com/neomei/SessionReviewer/internal/reviewprompt"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

func TestMalformedJSONLWarningSurvivesPreparePromptAndFinalValidation(t *testing.T) {
	fixture := newFoundationFixture(t)
	fixture.writeSession(t, messageRecord("u1", "user", "retain the valid evidence"))
	opts := fixture.options("review")
	opts.afterOpenSession = func() error {
		file, err := os.OpenFile(fixture.sessionPath, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return err
		}
		if _, err := file.WriteString("{malformed-after-discovery\n"); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	}
	packet, err := Write(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(packet.Warnings, "malformed_jsonl_lines:1") {
		t.Fatalf("prepare warning=%v", packet.Warnings)
	}

	state := ledger.State{
		ProjectID: packet.ProjectID,
		CurrentState: ledger.CurrentState{
			ProjectID: packet.ProjectID, Revision: 1, UncommittedChanges: []string{}, Blockers: []string{}, OpenRisks: []string{},
			SourceSessions: []string{}, Evidence: []ledger.EvidenceRef{},
		},
		Timeline: []ledger.TimelineEvent{}, Decisions: map[string]ledger.Decision{},
		OpenLoops: map[string]ledger.OpenLoop{}, Sessions: map[string]ledger.SessionReport{},
	}
	accepted, err := reviewv2.ProjectLegacy(state)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("../../schemas/proposal-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := reviewprompt.Build(reviewprompt.Input{Packet: packet, Accepted: accepted, OutputSchema: schema})
	if err != nil {
		t.Fatalf("prompt rejected prepare warning: %v", err)
	}
	if !strings.Contains(string(bundle.Prompt), `"malformed_jsonl_lines:1"`) {
		t.Fatal("prompt silently dropped the structural evidence warning")
	}

	if len(packet.Events) != 1 {
		t.Fatalf("events=%+v", packet.Events)
	}
	item := packet.Events[0]
	ref := ledger.EvidenceRef{
		EvidenceID: item.ID, SessionID: packet.SessionID, JSONLLine: item.JSONLLine,
		SourceHash: item.SourceHash, Summary: item.Summary,
	}
	goal := "retain the valid evidence"
	sourceSessions := []string{packet.SessionID}
	refs := []ledger.EvidenceRef{ref}
	report := ledger.SessionReport{
		ID: "session-report-1", ProjectID: packet.ProjectID, SessionID: packet.SessionID, Revision: 1,
		InitialGoal: goal, GoalChanges: []string{}, Phases: []ledger.SessionPhase{}, Files: []string{},
		Commits: []string{}, Verification: []string{}, DecisionsAdded: []string{}, DecisionsRevised: []string{},
		OpenLoopsCreated: []string{}, OpenLoopsClosed: []string{}, Evidence: refs,
	}
	if packet.SessionUsage != nil {
		report.Accounting = &accounting.SessionAccounting{
			StartedAt: packet.SessionUsage.StartedAt, EndedAt: packet.SessionUsage.EndedAt,
			DurationMS: packet.SessionUsage.DurationMS, Models: []accounting.ModelAccounting{},
			TotalTokens: packet.SessionUsage.TotalTokens,
		}
	}
	digest, err := evidence.Digest(packet)
	if err != nil {
		t.Fatal(err)
	}
	p := proposal.Proposal{
		SchemaVersion: 1, ProjectID: packet.ProjectID, SessionID: packet.SessionID,
		FromCursor: packet.FromCursor, ToCursor: packet.ToCursor, EvidencePacketSHA256: digest,
		NewDecisions: []ledger.Decision{}, UpdatedDecisions: []proposal.DecisionPatch{},
		OpenLoops: []proposal.OpenLoopChange{}, TimelineEvents: []ledger.TimelineEvent{},
		CurrentStatePatch: proposal.CurrentStatePatch{
			ExpectedRevision: 1, Goal: &goal, SourceSessions: &sourceSessions, Evidence: &refs,
		},
		SessionReport: report,
		EvidenceLinks: []proposal.EvidenceLink{
			{EntityID: "current-state", EvidenceID: item.ID, Relation: "supports"},
			{EntityID: report.ID, EvidenceID: item.ID, Relation: "supports"},
		},
	}
	if _, err := proposal.Validate(p, packet, state); err != nil {
		t.Fatalf("final validator rejected prepare warning: %v", err)
	}
}
