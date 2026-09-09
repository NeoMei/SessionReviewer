package problemmap

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

var ErrProblemRevisionConflict = errors.New("problem revision conflict")

type ApplyAction string

const (
	ApplyRoot    ApplyAction = "apply_root"
	ApplyChild   ApplyAction = "apply_child"
	ApplySibling ApplyAction = "apply_sibling"
	ApplyMerge   ApplyAction = "merge"
)

type Graph struct {
	ProjectID string
	Revision  int
	Nodes     []reviewv4.ProblemNode
}

func (g Graph) Node(id string) *reviewv4.ProblemNode {
	for i := range g.Nodes {
		if g.Nodes[i].ID == id {
			return &g.Nodes[i]
		}
	}
	return nil
}

func ApplyCandidate(graph Graph, candidate Candidate, action ApplyAction, targetID string, now time.Time) (Graph, error) {
	next := cloneGraph(graph)
	if candidate.ProjectID != graph.ProjectID || candidate.Status != CandidatePending || candidate.Revision < 1 || now.IsZero() {
		return Graph{}, errors.New("candidate is not applicable")
	}
	if action == ApplyMerge {
		target := next.Node(targetID)
		if target == nil {
			return Graph{}, errors.New("merge target does not exist")
		}
		target.SourceTurnRefs = mergeSourceTurns(target.SourceTurnRefs, candidate.SourceTurnRefs)
		target.Revision++
		next.Revision++
		if err := ValidateGraph(next.Nodes); err != nil {
			return Graph{}, err
		}
		return next, nil
	}
	var parent *string
	switch action {
	case ApplyRoot:
		if targetID != "" {
			return Graph{}, errors.New("root application cannot have a target")
		}
	case ApplyChild:
		if next.Node(targetID) == nil {
			return Graph{}, errors.New("child target does not exist")
		}
		value := targetID
		parent = &value
	case ApplySibling:
		target := next.Node(targetID)
		if target == nil {
			return Graph{}, errors.New("sibling target does not exist")
		}
		if target.PrimaryParentID != nil {
			value := *target.PrimaryParentID
			parent = &value
		}
	default:
		return Graph{}, errors.New("unsupported candidate action")
	}
	id := problemID(candidate.ProjectID, candidate.CandidateID)
	if next.Node(id) != nil {
		return Graph{}, errors.New("candidate problem already exists")
	}
	confirmed := now.UTC().Round(0).Format(time.RFC3339Nano)
	node := reviewv4.ProblemNode{ID: id, Question: candidate.Question, PrimaryParentID: parent, RelatedNodeIDs: cloneNonNilSlice(candidate.RelatedNodeIDs), WorkflowState: "not_started", AnswerState: answerState(candidate.SourceTurnRefs), CompletionCriterion: "", CurrentConclusion: "", SourceTurnRefs: cloneNonNilSlice(candidate.SourceTurnRefs), Provenance: "candidate_confirmed", FirstProposedAt: candidate.CreatedAt, SiblingOrder: nextSiblingOrder(next.Nodes, parent), ConfirmedAt: &confirmed, Revision: 1}
	if node.FirstProposedAt == "" {
		node.FirstProposedAt = confirmed
	}
	next.Nodes = append(next.Nodes, node)
	next.Revision++
	if err := ValidateGraph(next.Nodes); err != nil {
		return Graph{}, err
	}
	return next, nil
}

func Move(graph Graph, problemID, newParentID string, expectedRevision ...int) (Graph, error) {
	if _, err := PreviewMove(graph.Nodes, problemID, newParentID); err != nil {
		return Graph{}, err
	}
	next := cloneGraph(graph)
	node := next.Node(problemID)
	if len(expectedRevision) > 0 && node.Revision != expectedRevision[0] {
		return Graph{}, ErrProblemRevisionConflict
	}
	oldParent := cloneString(node.PrimaryParentID)
	if newParentID == "root" {
		node.PrimaryParentID = nil
	} else {
		value := newParentID
		node.PrimaryParentID = &value
	}
	node.SiblingOrder = nextSiblingOrderExcluding(next.Nodes, node.PrimaryParentID, node.ID)
	node.Revision++
	normalizeSiblingOrders(next.Nodes, oldParent)
	normalizeSiblingOrders(next.Nodes, node.PrimaryParentID)
	next.Revision++
	if err := ValidateGraph(next.Nodes); err != nil {
		return Graph{}, err
	}
	return next, nil
}

func Reorder(graph Graph, parentID string, ordered []string) (Graph, error) {
	next := cloneGraph(graph)
	var parent *string
	if parentID != "root" {
		if next.Node(parentID) == nil {
			return Graph{}, errors.New("reorder parent does not exist")
		}
		value := parentID
		parent = &value
	}
	current := directChildren(next.Nodes, parent)
	if len(current) != len(ordered) {
		return Graph{}, errors.New("reorder must include every direct child")
	}
	want := map[string]bool{}
	for _, id := range current {
		want[id] = true
	}
	seen := map[string]bool{}
	for index, id := range ordered {
		if !want[id] || seen[id] {
			return Graph{}, errors.New("reorder contains a foreign or duplicate child")
		}
		seen[id] = true
		node := next.Node(id)
		if node.SiblingOrder != index {
			node.SiblingOrder, node.Revision = index, node.Revision+1
		}
	}
	next.Revision++
	if err := ValidateGraph(next.Nodes); err != nil {
		return Graph{}, err
	}
	return next, nil
}

type HumanFields struct {
	Question, CurrentConclusion, CompletionCriterion string
}

func EditHumanFields(graph Graph, problemID string, expectedRevision int, fields HumanFields) (Graph, error) {
	next := cloneGraph(graph)
	node := next.Node(problemID)
	if node == nil {
		return Graph{}, errors.New("problem does not exist")
	}
	if node.Revision != expectedRevision {
		return Graph{}, ErrProblemRevisionConflict
	}
	if fields.Question == "" {
		return Graph{}, errors.New("problem question is required")
	}
	node.Question = fields.Question
	node.CurrentConclusion = fields.CurrentConclusion
	node.CompletionCriterion = fields.CompletionCriterion
	node.Revision++
	next.Revision++
	if err := ValidateGraph(next.Nodes); err != nil {
		return Graph{}, err
	}
	return next, nil
}

func SetWorkflowState(graph Graph, problemID string, expectedRevision int, state string) (Graph, error) {
	if state != "resolved" && state != "in_progress" {
		return Graph{}, errors.New("workflow operation must resolve or reopen")
	}
	next := cloneGraph(graph)
	node := next.Node(problemID)
	if node == nil {
		return Graph{}, errors.New("problem does not exist")
	}
	if node.Revision != expectedRevision {
		return Graph{}, ErrProblemRevisionConflict
	}
	if node.WorkflowState == state {
		return Graph{}, errors.New("problem is already in requested state")
	}
	if state == "in_progress" && node.WorkflowState != "resolved" {
		return Graph{}, errors.New("only resolved problems can be reopened")
	}
	node.WorkflowState, node.Revision = state, node.Revision+1
	next.Revision++
	if err := ValidateGraph(next.Nodes); err != nil {
		return Graph{}, err
	}
	return next, nil
}

func problemID(projectID, candidateID string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + candidateID))
	return "problem-" + hex.EncodeToString(sum[:12])
}

func answerState(refs []reviewv4.SourceTurnRef) string {
	if len(refs) == 0 {
		return "no_answer"
	}
	return "answered_unverified"
}
func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneGraph(graph Graph) Graph {
	next := Graph{ProjectID: graph.ProjectID, Revision: graph.Revision, Nodes: cloneSlice(graph.Nodes)}
	for i := range next.Nodes {
		next.Nodes[i].PrimaryParentID = cloneString(next.Nodes[i].PrimaryParentID)
		next.Nodes[i].RelatedNodeIDs = cloneSlice(next.Nodes[i].RelatedNodeIDs)
		next.Nodes[i].SourceTurnRefs = cloneSlice(next.Nodes[i].SourceTurnRefs)
		next.Nodes[i].ConfirmedAt = cloneString(next.Nodes[i].ConfirmedAt)
	}
	return next
}

func cloneSlice[T any](values []T) []T {
	if values == nil {
		return nil
	}
	result := make([]T, len(values))
	copy(result, values)
	return result
}

func cloneNonNilSlice[T any](values []T) []T {
	result := cloneSlice(values)
	if result == nil {
		return []T{}
	}
	return result
}

func sameParentID(left, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}
func nextSiblingOrder(nodes []reviewv4.ProblemNode, parent *string) int {
	return nextSiblingOrderExcluding(nodes, parent, "")
}
func nextSiblingOrderExcluding(nodes []reviewv4.ProblemNode, parent *string, excluded string) int {
	n := 0
	for _, node := range nodes {
		if node.ID != excluded && sameParentID(node.PrimaryParentID, parent) && node.SiblingOrder >= n {
			n = node.SiblingOrder + 1
		}
	}
	return n
}
func directChildren(nodes []reviewv4.ProblemNode, parent *string) []string {
	type item struct {
		id    string
		order int
	}
	values := []item{}
	for _, node := range nodes {
		if sameParentID(node.PrimaryParentID, parent) {
			values = append(values, item{node.ID, node.SiblingOrder})
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].order != values[j].order {
			return values[i].order < values[j].order
		}
		return values[i].id < values[j].id
	})
	result := make([]string, len(values))
	for i := range values {
		result[i] = values[i].id
	}
	return result
}
func normalizeSiblingOrders(nodes []reviewv4.ProblemNode, parent *string) {
	ids := directChildren(nodes, parent)
	by := map[string]int{}
	for i, id := range ids {
		by[id] = i
	}
	for i := range nodes {
		if order, ok := by[nodes[i].ID]; ok && nodes[i].SiblingOrder != order {
			nodes[i].SiblingOrder = order
			nodes[i].Revision++
		}
	}
}
func mergeSourceTurns(left, right []reviewv4.SourceTurnRef) []reviewv4.SourceTurnRef {
	result := append([]reviewv4.SourceTurnRef(nil), left...)
	seen := map[string]bool{}
	for _, ref := range result {
		seen[ref.Provider+"\x00"+ref.SessionID+"\x00"+ref.TurnUnitID+"\x00"+ref.SessionViewDigest] = true
	}
	for _, ref := range right {
		key := ref.Provider + "\x00" + ref.SessionID + "\x00" + ref.TurnUnitID + "\x00" + ref.SessionViewDigest
		if !seen[key] {
			seen[key] = true
			result = append(result, ref)
		}
	}
	return result
}
