package ledger

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"
)

func TestRenderDiagramRejectsGeneratedNodeIDCollisions(t *testing.T) {
	tests := []struct {
		name      string
		kind      string
		first     string
		second    string
		makeState func() State
	}{
		{name: "events", kind: "event", first: "event-a", second: "event-b", makeState: func() State {
			state := baseDiagramState()
			state.Timeline = []TimelineEvent{{ID: "event-a"}, {ID: "event-b"}}
			return state
		}},
		{name: "decisions", kind: "decision", first: "decision-a", second: "decision-b", makeState: func() State {
			state := baseDiagramState()
			state.Decisions["decision-a"] = Decision{ID: "decision-a", ProjectID: state.ProjectID}
			state.Decisions["decision-b"] = Decision{ID: "decision-b", ProjectID: state.ProjectID}
			return state
		}},
		{name: "loops", kind: "loop", first: "loop-a", second: "loop-b", makeState: func() State {
			state := baseDiagramState()
			state.OpenLoops["loop-a"] = OpenLoop{ID: "loop-a", ProjectID: state.ProjectID}
			state.OpenLoops["loop-b"] = OpenLoop{ID: "loop-b", ProjectID: state.ProjectID}
			return state
		}},
		{name: "sessions", kind: "session", first: "session-a", second: "session-b", makeState: func() State {
			state := baseDiagramState()
			state.Sessions["session-a"] = SessionReport{ID: "session-a", ProjectID: state.ProjectID, SessionID: "source-a"}
			state.Sessions["session-b"] = SessionReport{ID: "session-b", ProjectID: state.ProjectID, SessionID: "source-b"}
			return state
		}},
		{name: "blockers", kind: "blocker", first: "first blocker", second: "second blocker", makeState: func() State {
			state := baseDiagramState()
			state.CurrentState.Blockers = []string{"first blocker", "second blocker"}
			return state
		}},
		{name: "experiments", kind: "experiment", first: "first experiment", second: "second experiment", makeState: func() State {
			state := baseDiagramState()
			state.OpenLoops["loop-a"] = OpenLoop{ID: "loop-a", ProjectID: state.ProjectID, NextExperiment: "first experiment"}
			state.OpenLoops["loop-b"] = OpenLoop{ID: "loop-b", ProjectID: state.ProjectID, NextExperiment: "second experiment"}
			return state
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := renderDiagramWithOptions(test.makeState(), diagramRenderOptions{nodeDigest: collidingDiagramDigest(test.kind)})
			if err == nil || !strings.Contains(err.Error(), test.first) || !strings.Contains(err.Error(), test.second) {
				t.Fatalf("collision error=%v", err)
			}
		})
	}
}

func TestRenderDiagramRejectsNodeAndEdgeBudgetsBeforeBuilding(t *testing.T) {
	t.Run("nodes", func(t *testing.T) {
		state := State{Timeline: []TimelineEvent{{ID: "event-a"}, {ID: "event-b"}}}
		if got, err := renderDiagramWithOptions(state, diagramRenderOptions{maxNodes: 1}); err == nil || len(got) != 0 || !strings.Contains(err.Error(), "node limit") {
			t.Fatalf("bytes=%d err=%v", len(got), err)
		}
	})
	t.Run("edges", func(t *testing.T) {
		state := State{Timeline: []TimelineEvent{{ID: "event-a"}, {ID: "event-b"}, {ID: "event-c"}}}
		if got, err := renderDiagramWithOptions(state, diagramRenderOptions{maxEdges: 1}); err == nil || len(got) != 0 || !strings.Contains(err.Error(), "edge limit") {
			t.Fatalf("bytes=%d err=%v", len(got), err)
		}
	})
}

func TestRenderDiagramUsesCappedOutputBuilder(t *testing.T) {
	state := State{Timeline: []TimelineEvent{{ID: "event-a", Title: strings.Repeat("&", 100)}}}
	got, err := renderDiagramWithOptions(state, diagramRenderOptions{maxOutputBytes: 400})
	if err == nil || len(got) != 0 || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("bytes=%d err=%v", len(got), err)
	}
}

func TestRenderDiagramRejectsOversizedIdentityBeforeHashing(t *testing.T) {
	state := State{Timeline: []TimelineEvent{{ID: strings.Repeat("x", 129)}}}
	digestCalled := false
	_, err := renderDiagramWithOptions(state, diagramRenderOptions{nodeDigest: func([]byte) [32]byte {
		digestCalled = true
		return [32]byte{}
	}})
	if err == nil || digestCalled || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("digestCalled=%v err=%v", digestCalled, err)
	}
}

func baseDiagramState() State {
	const projectID = "project-1111111111111111"
	return State{
		ProjectID:    projectID,
		CurrentState: CurrentState{ProjectID: projectID},
		Decisions:    make(map[string]Decision),
		OpenLoops:    make(map[string]OpenLoop),
		Sessions:     make(map[string]SessionReport),
	}
}

func collidingDiagramDigest(kind string) func([]byte) [32]byte {
	prefix := []byte(kind + "\x00")
	return func(value []byte) [32]byte {
		if bytes.HasPrefix(value, prefix) {
			return [32]byte{0x42, 0x42, 0x42, 0x42, 0x42, 0x42}
		}
		return sha256.Sum256(value)
	}
}
