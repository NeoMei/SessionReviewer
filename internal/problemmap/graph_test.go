package problemmap

import (
	"reflect"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func TestApplyRootChildMoveReorderMergeAndResolveLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 9, 1, 2, 3, 0, time.UTC)
	ref := func(provider, session, turn string) reviewv4.SourceTurnRef {
		return reviewv4.SourceTurnRef{Provider: provider, SessionID: session, TurnUnitID: turn}
	}
	candidate := func(id, question string, refs ...reviewv4.SourceTurnRef) Candidate {
		return Candidate{CandidateID: id, ProjectID: "project-a", Question: question, SourceTurnRefs: refs, Status: CandidatePending, Revision: 1}
	}
	graph := Graph{ProjectID: "project-a", Revision: 0, Nodes: []reviewv4.ProblemNode{}}
	var err error
	graph, err = ApplyCandidate(graph, candidate("candidate-root", "Root?", ref("codex", "s1", "t1")), ApplyRoot, "", now)
	if err != nil || len(graph.Nodes) != 1 || graph.Nodes[0].PrimaryParentID != nil || graph.Revision != 1 {
		t.Fatalf("root graph=%+v err=%v", graph, err)
	}
	rootID := graph.Nodes[0].ID
	graph, err = ApplyCandidate(graph, candidate("candidate-child", "Child?", ref("claude", "same", "t2")), ApplyChild, rootID, now)
	if err != nil || graph.Nodes[1].PrimaryParentID == nil || *graph.Nodes[1].PrimaryParentID != rootID {
		t.Fatalf("child graph=%+v err=%v", graph, err)
	}
	childID := graph.Nodes[1].ID
	graph, err = ApplyCandidate(graph, candidate("candidate-root-2", "Second root?", ref("opencode", "same", "t3")), ApplyRoot, "", now)
	if err != nil {
		t.Fatal(err)
	}
	secondRoot := graph.Nodes[2].ID
	preview, err := PreviewMove(graph.Nodes, childID, "root")
	if err != nil || !reflect.DeepEqual(preview.OldPath, []string{rootID, childID}) || !reflect.DeepEqual(preview.NewPath, []string{childID}) {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	graph, err = Move(graph, childID, "root")
	if err != nil {
		t.Fatal(err)
	}
	graph, err = Reorder(graph, "root", []string{secondRoot, childID, rootID})
	if err != nil {
		t.Fatal(err)
	}
	graph, err = ApplyCandidate(graph, candidate("candidate-merge", "Duplicate?", ref("codex", "s4", "t4")), ApplyMerge, rootID, now)
	if err != nil {
		t.Fatal(err)
	}
	root := graph.Node(rootID)
	if root == nil || len(root.SourceTurnRefs) != 2 {
		t.Fatalf("merge refs=%+v", root)
	}
	graph, err = SetWorkflowState(graph, rootID, "resolved")
	if err != nil || graph.Node(rootID).AnswerState != "answered_unverified" {
		t.Fatalf("resolve=%+v err=%v", graph.Node(rootID), err)
	}
	graph, err = SetWorkflowState(graph, rootID, "in_progress")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateGraph(graph.Nodes); err != nil {
		t.Fatal(err)
	}
}

func TestGraphRejectsCycleForeignAndIncompleteReorderBeforeMutation(t *testing.T) {
	parent := "a"
	graph := Graph{ProjectID: "project-a", Revision: 2, Nodes: []reviewv4.ProblemNode{
		graphProblemNode("a", nil, 0), graphProblemNode("b", &parent, 0), graphProblemNode("c", nil, 1),
	}}
	before := cloneGraph(graph)
	if _, err := Move(graph, "a", "b"); err == nil {
		t.Fatal("cycle accepted")
	}
	if _, err := Reorder(graph, "root", []string{"a", "foreign"}); err == nil {
		t.Fatal("foreign reorder accepted")
	}
	if !reflect.DeepEqual(graph, before) {
		t.Fatal("failed operation mutated graph")
	}
}

func graphProblemNode(id string, parent *string, order int) reviewv4.ProblemNode {
	now := "2026-09-09T01:02:03Z"
	return reviewv4.ProblemNode{ID: id, Question: id + "?", PrimaryParentID: parent, RelatedNodeIDs: []string{}, WorkflowState: "not_started", AnswerState: "no_answer", CompletionCriterion: "", CurrentConclusion: "", SourceTurnRefs: []reviewv4.SourceTurnRef{}, Provenance: "human_created", FirstProposedAt: now, SiblingOrder: order, ConfirmedAt: &now, Revision: 1}
}
