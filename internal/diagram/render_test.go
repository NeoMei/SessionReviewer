package diagram_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/diagram"
	"github.com/neomei/SessionReviewer/internal/ledger"
)

func TestRenderStableAndEscaped(t *testing.T) {
	state := ledger.State{
		ProjectID: "project-1111111111111111",
		Timeline: []ledger.TimelineEvent{{
			ID:         "event-1",
			OccurredAt: "2026-08-23T01:02:03Z",
			Title:      `Choose "A" [safely]`,
		}},
		Decisions: map[string]ledger.Decision{},
		OpenLoops: map[string]ledger.OpenLoop{},
		Sessions:  map[string]ledger.SessionReport{},
	}

	first, err := diagram.Render(state)
	if err != nil {
		t.Fatal(err)
	}
	second, err := diagram.Render(state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) ||
		!bytes.Contains(first, []byte("flowchart LR")) ||
		!bytes.Contains(first, []byte("graph TD")) ||
		!bytes.Contains(first, []byte("&quot;A&quot;")) ||
		!bytes.Contains(first, []byte("&#91;safely&#93;")) {
		t.Fatalf("diagram=%s", first)
	}
}

func TestRenderEscapesArbitraryLabelsWithoutClosingMermaidFence(t *testing.T) {
	state := emptyState()
	state.Timeline = []ledger.TimelineEvent{{
		ID:         "event-hostile",
		OccurredAt: "2026-08-23T01:02:03Z",
		Title:      "line one\n```html\n<script>[x] & \"quoted\"\t\x00 汉字",
	}}

	got, err := diagram.Render(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{[]byte("```html"), []byte("<script>"), []byte{0}, []byte{'\t'}} {
		if bytes.Contains(got, forbidden) {
			t.Fatalf("unsafe bytes %q remain in:\n%s", forbidden, got)
		}
	}
	for _, escaped := range []string{"&#96;&#96;&#96;html", "&lt;script&gt;", "&#91;x&#93;", "&amp;", "&quot;quoted&quot;", "汉字"} {
		if !bytes.Contains(got, []byte(escaped)) {
			t.Fatalf("missing escaped label %q in:\n%s", escaped, got)
		}
	}
}

func TestRenderRejectsDuplicateAcceptedStateIDs(t *testing.T) {
	tests := map[string]func(*ledger.State){
		"cross entity collision": func(state *ledger.State) {
			state.Timeline = []ledger.TimelineEvent{{ID: "shared", OccurredAt: "2026-08-23T01:02:03Z", Title: "Event"}}
			state.Decisions["shared"] = ledger.Decision{ID: "shared", ProjectID: state.ProjectID, Title: "Decision"}
		},
		"map key mismatch": func(state *ledger.State) {
			state.Decisions["decision-key"] = ledger.Decision{ID: "decision-value", ProjectID: state.ProjectID, Title: "Decision"}
		},
		"duplicate session identity": func(state *ledger.State) {
			state.Sessions["session-a"] = ledger.SessionReport{ID: "session-a", ProjectID: state.ProjectID, SessionID: "source-session"}
			state.Sessions["session-b"] = ledger.SessionReport{ID: "session-b", ProjectID: state.ProjectID, SessionID: "source-session"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			state := emptyState()
			mutate(&state)
			if got, err := diagram.Render(state); err == nil {
				t.Fatalf("accepted invalid state:\n%s", got)
			}
		})
	}
}

func TestRenderSortsEntitiesTagsAndEdges(t *testing.T) {
	state := richState()
	got, err := diagram.Render(state)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)

	wantNodes := []string{
		diagramNode("event", "event-a"),
		diagramNode("event", "event-b"),
		diagramNode("decision", "decision-a"),
		diagramNode("decision", "decision-b"),
		diagramNode("loop", "loop-a"),
	}
	for i := 1; i < len(wantNodes); i++ {
		if strings.Index(text, wantNodes[i-1]) >= strings.Index(text, wantNodes[i]) {
			t.Fatalf("nodes are not in stable semantic order:\n%s", got)
		}
	}
	if !strings.Contains(text, "tags: a, z") {
		t.Fatalf("tags are not bytewise sorted:\n%s", got)
	}

	causal := graphBody(t, text, "flowchart LR")
	wantEdges := []string{
		"  " + diagramNode("event", "event-a") + " --> " + diagramNode("event", "event-b"),
		"  " + diagramNode("event", "event-b") + " --> " + diagramNode("decision", "decision-a"),
		"  " + diagramNode("event", "event-b") + " --> " + diagramNode("decision", "decision-b"),
		"  " + diagramNode("event", "event-b") + " --> " + diagramNode("loop", "loop-a"),
	}
	sort.Strings(wantEdges)
	if gotEdges := edgeLines(causal); !reflect.DeepEqual(gotEdges, wantEdges) {
		t.Fatalf("causal edges=%q want=%q\n%s", gotEdges, wantEdges, got)
	}

	relationships := graphBody(t, text, "graph TD")
	for _, relation := range []string{
		diagramNode("project", state.ProjectID) + " --> " + diagramNode("session", "session-a"),
		diagramNode("project", state.ProjectID) + " --> " + diagramNode("decision", "decision-a"),
		diagramNode("project", state.ProjectID) + " --> " + diagramNode("loop", "loop-a"),
		diagramNode("session", "session-a") + " --> " + diagramNode("decision", "decision-a"),
		diagramNode("session", "session-a") + " --> " + diagramNode("loop", "loop-a"),
		diagramNode("loop", "loop-a") + " --> " + diagramNode("blocker", "loop-a\x00Waiting for native CI"),
		diagramNode("loop", "loop-a") + " --> " + diagramNode("experiment", "loop-a\x00Run Windows tests"),
	} {
		if !strings.Contains(relationships, relation) {
			t.Fatalf("missing relationship %q:\n%s", relation, got)
		}
	}
	gotRelationshipEdges := edgeLines(relationships)
	if !sort.StringsAreSorted(gotRelationshipEdges) {
		t.Fatalf("relationship edges are not sorted: %q", gotRelationshipEdges)
	}
}

func TestRenderUsesOnlyExplicitTimelineEntityIDsForCausalEdges(t *testing.T) {
	state := richState()
	state.Timeline[1].Summary = "decision-b loop-a"
	state.Timeline[0].DecisionIDs = []string{"decision-a"}
	state.Timeline[0].OpenLoopIDs = nil

	got, err := diagram.Render(state)
	if err != nil {
		t.Fatal(err)
	}
	causal := graphBody(t, string(got), "flowchart LR")
	for _, forbidden := range []string{
		diagramNode("event", "event-a") + " --> " + diagramNode("decision", "decision-b"),
		diagramNode("event", "event-a") + " --> " + diagramNode("loop", "loop-a"),
		diagramNode("event", "event-b") + " --> " + diagramNode("decision", "decision-b"),
		diagramNode("event", "event-b") + " --> " + diagramNode("loop", "loop-a"),
	} {
		if strings.Contains(causal, forbidden) {
			t.Fatalf("inferred causal edge %q:\n%s", forbidden, got)
		}
	}
}

func TestRenderEmptyState(t *testing.T) {
	got, err := diagram.Render(ledger.State{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("flowchart LR")) || !bytes.Contains(got, []byte("graph TD")) || len(edgeLines(string(got))) != 0 {
		t.Fatalf("empty diagram=%s", got)
	}
}

func TestRenderDoesNotMutateState(t *testing.T) {
	state := richState()
	want := richState()
	if _, err := diagram.Render(state); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, want) {
		t.Fatalf("render mutated state\ngot=%+v\nwant=%+v", state, want)
	}
}

func emptyState() ledger.State {
	return ledger.State{
		ProjectID: "project-1111111111111111",
		Decisions: make(map[string]ledger.Decision),
		OpenLoops: make(map[string]ledger.OpenLoop),
		Sessions:  make(map[string]ledger.SessionReport),
	}
}

func richState() ledger.State {
	state := emptyState()
	state.CurrentState = ledger.CurrentState{ProjectID: state.ProjectID, Goal: "Ship safely", Blockers: []string{"Z blocker", "A blocker"}}
	state.Timeline = []ledger.TimelineEvent{
		{ID: "event-b", OccurredAt: "2026-08-23T01:00:00Z", Title: "Later", DecisionIDs: []string{"decision-b", "decision-a"}, OpenLoopIDs: []string{"loop-a"}},
		{ID: "event-a", OccurredAt: "2026-08-23T01:00:00Z", Title: "Earlier"},
	}
	state.Decisions["decision-b"] = ledger.Decision{ID: "decision-b", ProjectID: state.ProjectID, Title: "Second", Status: "accepted", Tags: []string{"z", "a"}}
	state.Decisions["decision-a"] = ledger.Decision{ID: "decision-a", ProjectID: state.ProjectID, Title: "First", Status: "accepted", Tags: []string{"z", "a"}}
	state.OpenLoops["loop-a"] = ledger.OpenLoop{ID: "loop-a", ProjectID: state.ProjectID, Title: "Windows", Status: "blocked", Tags: []string{"z", "a"}, Blocker: "Waiting for native CI", NextExperiment: "Run Windows tests"}
	state.Sessions["session-b"] = ledger.SessionReport{ID: "session-b", ProjectID: state.ProjectID, SessionID: "source-b"}
	state.Sessions["session-a"] = ledger.SessionReport{ID: "session-a", ProjectID: state.ProjectID, SessionID: "source-a", DecisionsAdded: []string{"decision-a"}, OpenLoopsCreated: []string{"loop-a"}}
	return state
}

func diagramNode(kind, id string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + id))
	return kind + "_" + hex.EncodeToString(sum[:6])
}

func graphBody(t *testing.T, document, declaration string) string {
	t.Helper()
	start := strings.Index(document, declaration)
	if start < 0 {
		t.Fatalf("missing %q in:\n%s", declaration, document)
	}
	body := document[start+len(declaration):]
	if end := strings.Index(body, "```"); end >= 0 {
		body = body[:end]
	}
	return body
}

func edgeLines(body string) []string {
	var result []string
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, " --> ") {
			result = append(result, line)
		}
	}
	return result
}
