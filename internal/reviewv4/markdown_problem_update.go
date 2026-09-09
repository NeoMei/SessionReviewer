package reviewv4

import (
	"errors"
	"reflect"
)

// RenderMarkdownProblemOperation is the structural counterpart to
// RenderMarkdownUpdate. It authenticates the pending Project/Vault draft, then
// permits exactly one validated formal-problem graph revision while preserving
// every non-problem field and all human/custom Markdown bytes.
func RenderMarkdownProblemOperation(next Presentation, oldLedger MachineLedger, pending MarkdownPair) (MarkdownPair, error) {
	draft, err := ParseMarkdownDraft(pending, oldLedger)
	if err != nil {
		return MarkdownPair{}, err
	}
	prior := draft.Presentation
	if next.Revision != prior.Revision+1 || next.ProblemMapRevision != prior.ProblemMapRevision+1 {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownBaselineMissing}
	}
	expected := clonePresentation(prior)
	expected.Revision = next.Revision
	expected.ProblemMapRevision = next.ProblemMapRevision
	expected.ProblemRootIDs = cloneSliceValue(next.ProblemRootIDs)
	expected.ProblemNodes = cloneProblemNodes(next.ProblemNodes)
	if !reflect.DeepEqual(expected, next) {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownStructureEditRequiresCommand, Cause: errors.New("problem operation changed non-problem fields")}
	}
	if err := ValidateProblemGraph(next.ProblemNodes); err != nil {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownStructureEditRequiresCommand, Cause: err}
	}
	return renderMarkdown(next, oldLedger, &pending, true)
}

func cloneSliceValue[T any](values []T) []T {
	if values == nil {
		return nil
	}
	result := make([]T, len(values))
	copy(result, values)
	return result
}
func cloneProblemNodes(values []ProblemNode) []ProblemNode {
	result := cloneSliceValue(values)
	for i := range result {
		result[i].PrimaryParentID = cloneStringValue(result[i].PrimaryParentID)
		result[i].RelatedNodeIDs = cloneSliceValue(result[i].RelatedNodeIDs)
		result[i].SourceTurnRefs = cloneSliceValue(result[i].SourceTurnRefs)
		result[i].ConfirmedAt = cloneStringValue(result[i].ConfirmedAt)
	}
	return result
}
func cloneStringValue(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
