package reviewv4

import (
	"errors"
	"reflect"
)

// RenderMarkdownDecisionOperation authenticates the pending draft and permits
// exactly one validated decision-set revision. All other presentation fields
// and custom Markdown remain outside this command's authority.
func RenderMarkdownDecisionOperation(next Presentation, oldLedger MachineLedger, pending MarkdownPair) (MarkdownPair, error) {
	draft, err := ParseMarkdownDraft(pending, oldLedger)
	if err != nil {
		return MarkdownPair{}, err
	}
	prior := draft.Presentation
	if next.Revision != prior.Revision+1 {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownBaselineMissing}
	}
	expected := clonePresentation(prior)
	expected.Revision = next.Revision
	expected.Decisions = cloneDecisions(next.Decisions)
	if !reflect.DeepEqual(expected, next) {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownStructureEditRequiresCommand, Cause: errors.New("decision operation changed non-decision fields")}
	}
	if err := ValidatePresentation(next); err != nil {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownStructureEditRequiresCommand, Cause: err}
	}
	return renderMarkdown(next, oldLedger, &pending, true)
}

func cloneDecisions(values []Decision) []Decision {
	result := cloneSliceValue(values)
	for index := range result {
		result[index].LegacyStatusText = cloneStringValue(result[index].LegacyStatusText)
		result[index].Supersedes = cloneSliceValue(result[index].Supersedes)
		result[index].MilestoneIDs = cloneSliceValue(result[index].MilestoneIDs)
		result[index].SessionRefs = cloneSliceValue(result[index].SessionRefs)
	}
	return result
}
