package reviewprompt_test

import (
	"reflect"
	"testing"

	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/reviewprompt"
)

func TestBuildRejectsShortNamedSecretInEveryRawAcceptedContextString(t *testing.T) {
	const secret = `password:"x"`
	tests := map[string]func(*reviewprompt.Input){
		"context project id": func(input *reviewprompt.Input) { input.Accepted.Review.ProjectID = secret },
		"shared accepted and packet project id": func(input *reviewprompt.Input) {
			input.Accepted.Review.ProjectID = secret
			input.Accepted.Machine.ProjectID = secret
			input.Packet.ProjectID = secret
		},
		"current goal":         func(input *reviewprompt.Input) { input.Accepted.Review.Goal = secret },
		"current verification": func(input *reviewprompt.Input) { input.Accepted.Review.LastVerification = secret },
		"current branch":       func(input *reviewprompt.Input) { input.Accepted.Review.Stage = secret },
		"current status":       func(input *reviewprompt.Input) { input.Accepted.Review.Status = secret },
		"current next action":  func(input *reviewprompt.Input) { input.Accepted.Review.NextAction = secret },
		"current first inspection": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.CurrentState.FirstInspection = secret
		},
		"current last updated": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.CurrentState.LastUpdated = secret
		},
		"current source session": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.CurrentState.SourceSessions[0] = secret
		},
		"risk id":     func(input *reviewprompt.Input) { input.Accepted.Review.Risks[0].ID = secret },
		"risk title":  func(input *reviewprompt.Input) { input.Accepted.Review.Risks[0].Title = secret },
		"risk status": func(input *reviewprompt.Input) { input.Accepted.Review.Risks[0].Status = secret },
		"risk detail": func(input *reviewprompt.Input) { input.Accepted.Review.Risks[0].Detail = secret },
		"decision id": func(input *reviewprompt.Input) {
			input.Accepted.Review.Decisions[0].ID = secret
			input.Accepted.Machine.LegacyCompatibility.Decisions[0].ID = secret
		},
		"decision occurred at": func(input *reviewprompt.Input) { input.Accepted.Review.Decisions[0].OccurredAt = secret },
		"decision title":       func(input *reviewprompt.Input) { input.Accepted.Review.Decisions[0].Title = secret },
		"decision status":      func(input *reviewprompt.Input) { input.Accepted.Review.Decisions[0].Status = secret },
		"decision rationale":   func(input *reviewprompt.Input) { input.Accepted.Review.Decisions[0].Rationale = secret },
		"decision impact":      func(input *reviewprompt.Input) { input.Accepted.Review.Decisions[0].Impact = secret },
		"decision project id": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.Decisions[0].ProjectID = secret
		},
		"decision tag": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.Decisions[0].Tags[0] = secret
		},
		"decision supersedes": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.Decisions[0].Supersedes = []string{secret}
		},
		"decision source session": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.Decisions[0].SourceSessions[0] = secret
		},
		"decision context": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.Decisions[0].Context = secret
		},
		"decision reevaluate when": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.Decisions[0].ReevaluateWhen = secret
		},
		"decision alternative": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.Decisions[0].Alternatives = []string{secret}
		},
		"decision rejected path": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.Decisions[0].RejectedPaths[0] = secret
		},
		"open loop id": func(input *reviewprompt.Input) { input.Accepted.Machine.LegacyCompatibility.OpenLoops[0].ID = secret },
		"open loop project id": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.OpenLoops[0].ProjectID = secret
		},
		"open loop title": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.OpenLoops[0].ID = "unmatched-loop"
			input.Accepted.Machine.LegacyCompatibility.OpenLoops[0].Title = secret
		},
		"open loop status": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.OpenLoops[0].ID = "unmatched-loop"
			input.Accepted.Machine.LegacyCompatibility.OpenLoops[0].Status = secret
		},
		"open loop tag": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.OpenLoops[0].Tags[0] = secret
		},
		"open loop source session": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.OpenLoops[0].SourceSessions[0] = secret
		},
		"open loop question": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.OpenLoops[0].Question = secret
		},
		"open loop attempt": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.OpenLoops[0].Attempts = []string{secret}
		},
		"open loop blocker": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.OpenLoops[0].Blocker = secret
		},
		"open loop next experiment": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.OpenLoops[0].NextExperiment = secret
		},
		"open loop completion criterion": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.OpenLoops[0].CompletionCriterion = secret
		},
		"timeline id":          func(input *reviewprompt.Input) { input.Accepted.Events[0].ID = secret },
		"timeline occurred at": func(input *reviewprompt.Input) { input.Accepted.Events[0].OccurredAt = secret },
		"timeline class":       func(input *reviewprompt.Input) { input.Accepted.Events[0].Kind = secret },
		"timeline title":       func(input *reviewprompt.Input) { input.Accepted.Events[0].Title = secret },
		"timeline meaning":     func(input *reviewprompt.Input) { input.Accepted.Events[0].Meaning = secret },
		"timeline summary":     func(input *reviewprompt.Input) { input.Accepted.Events[0].Summary = secret },
		"timeline why":         func(input *reviewprompt.Input) { input.Accepted.Events[0].Why = secret },
		"timeline change":      func(input *reviewprompt.Input) { input.Accepted.Events[0].Changes = []string{secret} },
		"timeline result":      func(input *reviewprompt.Input) { input.Accepted.Events[0].Results = []string{secret} },
		"timeline next":        func(input *reviewprompt.Input) { input.Accepted.Events[0].Next = secret },
		"timeline decision id": func(input *reviewprompt.Input) { input.Accepted.Events[0].DecisionIDs[0] = secret },
		"timeline open loop id": func(input *reviewprompt.Input) {
			input.Accepted.Machine.LegacyCompatibility.Timeline[0].OpenLoopIDs[0] = secret
		},
		"session report id":    func(input *reviewprompt.Input) { input.Accepted.Machine.Sessions[0].ID = secret },
		"session project id":   func(input *reviewprompt.Input) { input.Accepted.Machine.Sessions[0].ProjectID = secret },
		"session id":           func(input *reviewprompt.Input) { input.Accepted.Machine.Sessions[0].SessionID = secret },
		"session initial goal": func(input *reviewprompt.Input) { input.Accepted.Machine.Sessions[0].InitialGoal = secret },
		"session goal change":  func(input *reviewprompt.Input) { input.Accepted.Machine.Sessions[0].GoalChanges[0] = secret },
		"session phase title": func(input *reviewprompt.Input) {
			input.Accepted.Machine.Sessions[0].Phases = []ledger.SessionPhase{{Title: secret, Summary: "safe"}}
		},
		"session phase summary": func(input *reviewprompt.Input) {
			input.Accepted.Machine.Sessions[0].Phases = []ledger.SessionPhase{{Title: "safe", Summary: secret}}
		},
		"session commit":         func(input *reviewprompt.Input) { input.Accepted.Machine.Sessions[0].Commits[0] = secret },
		"session verification":   func(input *reviewprompt.Input) { input.Accepted.Machine.Sessions[0].Verification[0] = secret },
		"session decision added": func(input *reviewprompt.Input) { input.Accepted.Machine.Sessions[0].DecisionsAdded[0] = secret },
		"session decision revised": func(input *reviewprompt.Input) {
			input.Accepted.Machine.Sessions[0].DecisionsRevised = []string{secret}
		},
		"session open loop created": func(input *reviewprompt.Input) { input.Accepted.Machine.Sessions[0].OpenLoopsCreated[0] = secret },
		"session open loop closed":  func(input *reviewprompt.Input) { input.Accepted.Machine.Sessions[0].OpenLoopsClosed = []string{secret} },
		"session previous id":       func(input *reviewprompt.Input) { input.Accepted.Machine.Sessions[0].PreviousSessionID = secret },
		"session next id":           func(input *reviewprompt.Input) { input.Accepted.Machine.Sessions[0].NextSessionID = secret },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := fixtureInput()
			mutate(&input)
			bundle, err := reviewprompt.Build(input)
			if err != reviewprompt.ErrUnsafeInput || !reflect.DeepEqual(bundle, reviewprompt.Bundle{}) {
				t.Fatalf("bundle=%+v err=%v want zero bundle and exact ErrUnsafeInput", bundle, err)
			}
		})
	}
}

func TestBuildRejectsDuplicateCanonicalWarningsWithZeroBundle(t *testing.T) {
	tests := map[string]string{
		"redaction":  "redacted:openai_key:1",
		"structural": "malformed_jsonl_lines:2",
	}
	for name, warning := range tests {
		t.Run(name, func(t *testing.T) {
			input := fixtureInput()
			input.Packet.Warnings = []string{warning, warning}
			bundle, err := reviewprompt.Build(input)
			if err != reviewprompt.ErrInvalidInput || !reflect.DeepEqual(bundle, reviewprompt.Bundle{}) {
				t.Fatalf("bundle=%+v err=%v want zero bundle and exact ErrInvalidInput", bundle, err)
			}
		})
	}
}

func TestBuildStillAcceptsDistinctCanonicalWarnings(t *testing.T) {
	input := fixtureInput()
	input.Packet.Warnings = []string{"malformed_jsonl_lines:2", "redacted:openai_key:1"}
	if _, err := reviewprompt.Build(input); err != nil {
		t.Fatalf("distinct canonical warnings rejected: %v", err)
	}
}
