package decisions

import (
	"errors"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func TestCreateAndEditDecisionPreserveHumanFieldsAndRevision(t *testing.T) {
	presentation := decisionPresentation()
	input := DecisionInput{SchemaVersion: 1, Kind: "agreement", OccurredAt: "2026-09-09", Title: "Use exact preimages", Rationale: "Avoid stale edits", Impact: "All decision writes", Status: reviewv4.DecisionActive, ReevaluateWhen: "Publication changes", Supersedes: []string{}, MilestoneIDs: []string{}, SessionRefs: []reviewv4.SessionRef{{Provider: "codex", SessionID: "session-1"}}, Pinned: true}
	created, err := CreateDecision(presentation, "decision-new", input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != presentation.Revision+1 || len(created.Decisions) != 1 || created.Decisions[0].Revision != 1 || created.Decisions[0].Provenance != "human_created" || created.Decisions[0].Title != input.Title {
		t.Fatalf("created=%+v", created)
	}
	input.Title = "Use exact review and entity preimages"
	edited, err := EditDecision(created, "decision-new", 1, input)
	if err != nil {
		t.Fatal(err)
	}
	if edited.Revision != created.Revision+1 || edited.Decisions[0].Revision != 2 || edited.Decisions[0].Title != input.Title || edited.Decisions[0].Provenance != "human_created" {
		t.Fatalf("edited=%+v", edited.Decisions[0])
	}
	if _, err := EditDecision(edited, "decision-new", 1, input); !errors.Is(err, ErrDecisionRevisionConflict) {
		t.Fatalf("stale edit err=%v", err)
	}
}

func TestCreateDecisionSupersedesExistingEntryWithoutDeletingHistory(t *testing.T) {
	presentation := decisionPresentation()
	presentation.Decisions = []reviewv4.Decision{{ID: "decision-old", Kind: "decision", OccurredAt: "2026-09-01", Title: "Old", Rationale: "Old rationale", Impact: "Old impact", Status: reviewv4.DecisionActive, ReevaluateWhen: "", Supersedes: []string{}, MilestoneIDs: []string{}, SessionRefs: []reviewv4.SessionRef{}, Provenance: "human_created", Revision: 2}}
	input := DecisionInput{SchemaVersion: 1, Kind: "decision", OccurredAt: "2026-09-09", Title: "New", Rationale: "New rationale", Impact: "New impact", Status: reviewv4.DecisionActive, ReevaluateWhen: "", Supersedes: []string{"decision-old"}, MilestoneIDs: []string{}, SessionRefs: []reviewv4.SessionRef{}}
	next, err := CreateDecision(presentation, "decision-new", input)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Decisions) != 2 || next.Decisions[0].Status != reviewv4.DecisionSuperseded || next.Decisions[0].Revision != 3 {
		t.Fatalf("supersession did not retain and revise predecessor: %+v", next.Decisions)
	}
}

func TestParseDecisionInputIsStrictAndBounded(t *testing.T) {
	body := []byte(`{"schema_version":1,"kind":"decision","occurred_at":"2026-09-09","title":"Keep authority narrow","rationale":"reason","impact":"impact","status":"active","reevaluate_when":"later","supersedes":[],"milestone_ids":[],"session_refs":[],"pinned":false}`)
	if _, err := ParseDecisionInput(body); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseDecisionInput(append(body[:len(body)-1], []byte(`,"unknown":true}`)...)); err == nil {
		t.Fatal("unknown input field accepted")
	}
	if _, err := ParseDecisionInput(make([]byte, MaxInputBytes+1)); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("oversized err=%v", err)
	}
}

func decisionPresentation() reviewv4.Presentation {
	return reviewv4.Presentation{
		SchemaVersion: 4, MinimumReaderVersion: "0.4.0", MinimumWriterVersion: "0.4.0", ProjectID: "project-p", GenerationID: "generation-1", ProjectViewDigest: "sha256:" + strings.Repeat("1", 64), Revision: 1,
		CurrentState: reviewv4.CurrentState{}, Timeline: []reviewv4.Timeline{}, Decisions: []reviewv4.Decision{}, Risks: []reviewv4.Risk{}, OpenLoops: []reviewv4.OpenLoop{}, ProblemRootIDs: []string{}, ProblemNodes: []reviewv4.ProblemNode{}, ChainDependencies: []reviewv4.ChainDependency{}, HumanPatches: []reviewv4.Patch{}, OrphanPatches: []reviewv4.Patch{}, GeneratedBaselines: []reviewv4.Baseline{},
	}
}
