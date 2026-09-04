package problemmap

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validID(value string) bool {
	return utf8.ValidString(value) && len(value) <= 256 && idPattern.MatchString(value)
}
func validText(value string, limit int) bool {
	return utf8.ValidString(value) && len([]byte(value)) <= limit
}

func ValidateCandidates(store CandidateStore) error {
	if store.SchemaVersion != 1 || store.MinimumReaderVersion != "0.4.0" || !digestPattern.MatchString(store.Digest) || !validID(store.ProjectID) {
		return errors.New("invalid problem candidate store metadata")
	}
	if len(store.Candidates) > 65536 {
		return errors.New("problem candidate store exceeds item limit")
	}
	seen := make(map[string]bool, len(store.Candidates))
	for index, candidate := range store.Candidates {
		if !validID(candidate.CandidateID) || seen[candidate.CandidateID] || candidate.ProjectID != store.ProjectID || candidate.Question == "" || !validText(candidate.Question, 4096) || len(candidate.SourceTurnRefs) == 0 || len(candidate.SourceTurnRefs) > 256 || len(candidate.AlternateTargetIDs) > 2 || len(candidate.RelatedNodeIDs) > 2 || len(candidate.Grounds) > 256 || len(candidate.DependencyDigests) == 0 || len(candidate.DependencyDigests) > 256 || candidate.Revision < 1 || int64(candidate.Revision) > MaxWireInteger || candidate.CreatedAt == "" || !validText(candidate.CreatedAt, 128) || candidate.UpdatedAt == "" || !validText(candidate.UpdatedAt, 128) {
			return fmt.Errorf("invalid or duplicate problem candidate %d", index)
		}
		seen[candidate.CandidateID] = true
		if err := validateSourceTurns(candidate.SourceTurnRefs); err != nil {
			return err
		}
		if err := validateTargetIDs(candidate.AlternateTargetIDs, candidate.RecommendedTargetID); err != nil {
			return fmt.Errorf("candidate %q alternate targets: %w", candidate.CandidateID, err)
		}
		if err := validateTargetIDs(candidate.RelatedNodeIDs, nil); err != nil {
			return fmt.Errorf("candidate %q related nodes: %w", candidate.CandidateID, err)
		}
		switch candidate.RecommendedRelation {
		case RelationChild, RelationSibling, RelationMerge:
			if candidate.RecommendedTargetID == nil || !validID(*candidate.RecommendedTargetID) {
				return fmt.Errorf("candidate %q requires a target", candidate.CandidateID)
			}
		case RelationKeepPending:
			if candidate.RecommendedTargetID != nil {
				return fmt.Errorf("candidate %q keep-pending relation cannot have a target", candidate.CandidateID)
			}
		default:
			return fmt.Errorf("candidate %q has invalid relation", candidate.CandidateID)
		}
		switch candidate.Confidence {
		case ConfidenceHigh, ConfidenceMedium, ConfidenceLow:
		default:
			return fmt.Errorf("candidate %q has invalid confidence", candidate.CandidateID)
		}
		switch candidate.Status {
		case CandidatePending, CandidateApplied, CandidateMerged, CandidateKeptPending, CandidateStale, CandidateDismissed:
		default:
			return fmt.Errorf("candidate %q has invalid status", candidate.CandidateID)
		}
		switch candidate.AnalysisMode {
		case AnalysisDeterministic:
			if candidate.AgentRunID != nil {
				return fmt.Errorf("deterministic candidate %q cannot reference an Agent run", candidate.CandidateID)
			}
		case AnalysisAgentRequested:
			if candidate.AgentRunID == nil || !validID(*candidate.AgentRunID) {
				return fmt.Errorf("Agent-requested candidate %q requires an Agent run", candidate.CandidateID)
			}
		default:
			return fmt.Errorf("candidate %q has invalid analysis mode", candidate.CandidateID)
		}
		for _, ground := range candidate.Grounds {
			if !validID(ground.RuleID) || !validID(ground.RuleVersion) || len(ground.MatchedFactRefs) > 256 || !validText(ground.Explanation, 4096) {
				return fmt.Errorf("candidate %q has invalid grounds", candidate.CandidateID)
			}
			if err := uniqueIDs(ground.MatchedFactRefs); err != nil {
				return err
			}
		}
		if err := validateSortedDigests(candidate.DependencyDigests); err != nil {
			return fmt.Errorf("candidate %q dependencies: %w", candidate.CandidateID, err)
		}
	}
	return nil
}

func validateSourceTurns(refs []reviewv4.SourceTurnRef) error {
	seen := map[string]bool{}
	for _, ref := range refs {
		key := ref.Provider + "\x00" + ref.SessionID + "\x00" + ref.TurnUnitID
		if !validID(ref.Provider) || !validID(ref.SessionID) || !validID(ref.TurnUnitID) || seen[key] {
			return errors.New("invalid or duplicate source turn reference")
		}
		seen[key] = true
	}
	return nil
}

func validateTargetIDs(ids []string, excluded *string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if !validID(id) || seen[id] || (excluded != nil && id == *excluded) {
			return errors.New("invalid, duplicate, or primary target ID")
		}
		seen[id] = true
	}
	return nil
}

func uniqueIDs(ids []string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if !validID(id) || seen[id] {
			return errors.New("invalid or duplicate ID")
		}
		seen[id] = true
	}
	return nil
}

func validateSortedDigests(digests []string) error {
	for index, digest := range digests {
		if !digestPattern.MatchString(digest) || (index > 0 && digests[index-1] >= digest) {
			return errors.New("dependency digests must be unique and canonically sorted")
		}
	}
	return nil
}

func ValidateGraph(nodes []reviewv4.ProblemNode) error {
	return reviewv4.ValidateProblemGraph(nodes)
}

func PreviewMove(nodes []reviewv4.ProblemNode, problemID, newParentID string) (MovePreview, error) {
	if err := ValidateGraph(nodes); err != nil {
		return MovePreview{}, err
	}
	if !validID(problemID) || (newParentID != "root" && !validID(newParentID)) {
		return MovePreview{}, errors.New("invalid move identity")
	}
	byID := make(map[string]reviewv4.ProblemNode, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	node, exists := byID[problemID]
	if !exists {
		return MovePreview{}, errors.New("problem does not exist")
	}
	if newParentID != "root" {
		if _, exists := byID[newParentID]; !exists {
			return MovePreview{}, errors.New("new parent does not exist")
		}
		if newParentID == problemID {
			return MovePreview{}, errors.New("problem cannot parent itself")
		}
	}
	oldPath := problemPath(byID, problemID)
	updated := append([]reviewv4.ProblemNode(nil), nodes...)
	var nextOrder int
	for _, candidate := range nodes {
		if candidate.ID == problemID {
			continue
		}
		if sameParent(candidate.PrimaryParentID, newParentID) && candidate.SiblingOrder >= nextOrder {
			nextOrder = candidate.SiblingOrder + 1
		}
	}
	for index := range updated {
		if updated[index].ID != problemID {
			continue
		}
		if newParentID == "root" {
			updated[index].PrimaryParentID = nil
		} else {
			parent := newParentID
			updated[index].PrimaryParentID = &parent
		}
		updated[index].SiblingOrder = nextOrder
		node = updated[index]
	}
	if err := ValidateGraph(updated); err != nil {
		return MovePreview{}, err
	}
	updatedByID := make(map[string]reviewv4.ProblemNode, len(updated))
	for _, candidate := range updated {
		updatedByID[candidate.ID] = candidate
	}
	return MovePreview{ProblemID: node.ID, OldPath: oldPath, NewPath: problemPath(updatedByID, problemID), AffectedNodeIDs: subtreeIDs(nodes, problemID)}, nil
}

func sameParent(parent *string, target string) bool {
	if target == "root" {
		return parent == nil
	}
	return parent != nil && *parent == target
}

func problemPath(nodes map[string]reviewv4.ProblemNode, id string) []string {
	path := []string{}
	for {
		path = append(path, id)
		parent := nodes[id].PrimaryParentID
		if parent == nil {
			break
		}
		id = *parent
	}
	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	return path
}

func subtreeIDs(nodes []reviewv4.ProblemNode, root string) []string {
	children := map[string][]reviewv4.ProblemNode{}
	for _, node := range nodes {
		if node.PrimaryParentID != nil {
			children[*node.PrimaryParentID] = append(children[*node.PrimaryParentID], node)
		}
	}
	for parent := range children {
		sort.Slice(children[parent], func(i, j int) bool {
			if children[parent][i].SiblingOrder != children[parent][j].SiblingOrder {
				return children[parent][i].SiblingOrder < children[parent][j].SiblingOrder
			}
			return children[parent][i].ID < children[parent][j].ID
		})
	}
	result := []string{}
	var visit func(string)
	visit = func(id string) {
		result = append(result, id)
		for _, child := range children[id] {
			visit(child.ID)
		}
	}
	visit(root)
	return result
}
