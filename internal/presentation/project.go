package presentation

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

type ProjectInput struct {
	ProjectView   memory.ProjectView
	GenerationID  string
	Revision      int
	Legacy        reviewv2.LegacyPresentation
	ActivePatches []Patch
	OrphanPatches []Patch
	UnknownBlocks map[string][]byte
	ExpectedFiles map[string][]byte
}

type ProjectOutput struct {
	Review         reviewv2.Review
	Events         []reviewv2.Event
	Applied        map[string]AppliedField
	Baselines      []Baseline
	ActivePatches  []Patch
	OrphanPatches  []Patch
	RecentProgress string
	Usage          string
	UnknownBlocks  map[string][]byte
}

func Project(input ProjectInput) (ProjectOutput, error) {
	if err := memory.ValidateProjectView(input.ProjectView); err != nil {
		return ProjectOutput{}, fmt.Errorf("validate deterministic ProjectView: %w", err)
	}
	if input.GenerationID == "" || input.Revision < 1 {
		return ProjectOutput{}, errors.New("project projection generation identity is required")
	}
	baselines := projectBaselines(input)
	applied, err := Apply(input.ActivePatches, baselines)
	if err != nil {
		return ProjectOutput{}, err
	}
	if err := validateOrphanPatches(input.OrphanPatches); err != nil {
		return ProjectOutput{}, err
	}
	review := input.Legacy.Review
	review.ProjectID = input.ProjectView.ProjectID
	review.GenerationID = input.GenerationID
	review.MinimumWriterVersion = reviewv2.MinimumWriterVersion
	review.Revision = input.Revision
	review.Goal = appliedValue(applied, "project-overview", "goal", review.Goal)
	review.Status = appliedValue(applied, "project-overview", "status", deterministicStatus(input.ProjectView))
	review.NextAction = appliedValue(applied, "project-overview", "next_action", review.NextAction)
	if review.Name == "" {
		review.Name = input.ProjectView.ProjectID
	}
	review.Risks = appliedRisks(applied, review.Risks)
	review.Decisions = appliedDecisions(applied, review.Decisions)
	events := appliedEvents(applied, input.Legacy.Events)
	return ProjectOutput{
		Review: review, Events: events,
		Applied: applied, Baselines: baselines,
		ActivePatches: clonePatchSet(input.ActivePatches), OrphanPatches: clonePatchSet(input.OrphanPatches),
		RecentProgress: recentProgress(input.ProjectView), Usage: usageSummary(input.ProjectView),
		UnknownBlocks: cloneUnknownBlocks(input.UnknownBlocks),
	}, nil
}

func projectBaselines(input ProjectInput) []Baseline {
	result := []Baseline{
		NewScalarBaseline("project-overview", "goal", input.Legacy.Review.Goal),
		NewScalarBaseline("project-overview", "status", deterministicStatus(input.ProjectView)),
		NewScalarBaseline("project-overview", "next_action", input.Legacy.Review.NextAction),
	}
	for _, risk := range input.Legacy.Review.Risks {
		result = append(result,
			NewScalarBaseline(risk.ID, "visibility", "visible"),
			NewScalarBaseline(risk.ID, "title", risk.Title),
			NewScalarBaseline(risk.ID, "status", risk.Status),
			NewScalarBaseline(risk.ID, "detail", risk.Detail),
		)
	}
	for _, decision := range input.Legacy.Review.Decisions {
		result = append(result,
			NewScalarBaseline(decision.ID, "visibility", "visible"),
			NewScalarBaseline(decision.ID, "title", decision.Title),
			NewScalarBaseline(decision.ID, "rationale", decision.Rationale),
			NewScalarBaseline(decision.ID, "impact", decision.Impact),
			NewScalarBaseline(decision.ID, "status", decision.Status),
		)
	}
	for _, event := range input.Legacy.Events {
		result = append(result,
			NewScalarBaseline(event.ID, "visibility", "visible"),
			NewScalarBaseline(event.ID, "title", event.Title),
			NewScalarBaseline(event.ID, "meaning", event.Meaning),
			NewScalarBaseline(event.ID, "summary", event.Summary),
			NewScalarBaseline(event.ID, "why", event.Why),
			NewListBaseline(event.ID, "changes", event.Changes),
			NewListBaseline(event.ID, "results", event.Results),
			NewScalarBaseline(event.ID, "next", event.Next),
		)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].EntityID != result[j].EntityID {
			return result[i].EntityID < result[j].EntityID
		}
		return result[i].Field < result[j].Field
	})
	return result
}

func deterministicStatus(view memory.ProjectView) string {
	if view.LiveState.Branch == "" {
		return ""
	}
	return view.LiveState.Branch + " · 未提交路径 " + strconv.Itoa(view.LiveState.DirtyPathCount)
}

func appliedValue(fields map[string]AppliedField, entityID, field, fallback string) string {
	value, exists := fields[entityID+"\x00"+field]
	if !exists || !value.Present {
		return ""
	}
	return value.Value
}

func appliedPresent(fields map[string]AppliedField, entityID, field string) bool {
	return fields[entityID+"\x00"+field].Present
}

func appliedRisks(fields map[string]AppliedField, values []reviewv2.Risk) []reviewv2.Risk {
	result := make([]reviewv2.Risk, 0, len(values))
	for _, risk := range values {
		if !appliedPresent(fields, risk.ID, "visibility") {
			continue
		}
		risk.Title = appliedValue(fields, risk.ID, "title", risk.Title)
		risk.Status = appliedValue(fields, risk.ID, "status", risk.Status)
		risk.Detail = appliedValue(fields, risk.ID, "detail", risk.Detail)
		result = append(result, risk)
	}
	return result
}

func appliedDecisions(fields map[string]AppliedField, values []reviewv2.Decision) []reviewv2.Decision {
	result := make([]reviewv2.Decision, 0, len(values))
	for _, decision := range values {
		if !appliedPresent(fields, decision.ID, "visibility") {
			continue
		}
		decision.Title = appliedValue(fields, decision.ID, "title", decision.Title)
		decision.Rationale = appliedValue(fields, decision.ID, "rationale", decision.Rationale)
		decision.Impact = appliedValue(fields, decision.ID, "impact", decision.Impact)
		decision.Status = appliedValue(fields, decision.ID, "status", decision.Status)
		result = append(result, decision)
	}
	return result
}

func appliedEvents(fields map[string]AppliedField, values []reviewv2.Event) []reviewv2.Event {
	result := make([]reviewv2.Event, 0, len(values))
	for _, event := range values {
		if !appliedPresent(fields, event.ID, "visibility") {
			continue
		}
		event.Title = appliedValue(fields, event.ID, "title", event.Title)
		event.Meaning = appliedValue(fields, event.ID, "meaning", event.Meaning)
		event.Summary = appliedValue(fields, event.ID, "summary", event.Summary)
		event.Why = appliedValue(fields, event.ID, "why", event.Why)
		event.Changes = appliedValues(fields, event.ID, "changes", event.Changes)
		event.Results = appliedValues(fields, event.ID, "results", event.Results)
		event.Next = appliedValue(fields, event.ID, "next", event.Next)
		result = append(result, event)
	}
	return result
}

func appliedValues(fields map[string]AppliedField, entityID, field string, fallback []string) []string {
	value, exists := fields[entityID+"\x00"+field]
	if !exists || !value.Present {
		return nil
	}
	return append([]string(nil), value.Values...)
}

func recentProgress(view memory.ProjectView) string {
	records := append([]memory.DerivedRecord(nil), view.DerivedRecords...)
	sort.Slice(records, func(i, j int) bool {
		if records[i].OccurredAt != records[j].OccurredAt {
			return records[i].OccurredAt > records[j].OccurredAt
		}
		return records[i].ID < records[j].ID
	})
	var output strings.Builder
	for _, record := range records {
		if record.Kind != "event_ref" {
			continue
		}
		if output.Len() > 0 {
			output.WriteByte('\n')
		}
		output.WriteString("- " + record.OccurredAt + " · " + record.ID + " · " + record.Subject)
	}
	return output.String()
}

func usageSummary(view memory.ProjectView) string {
	usage := append([]memory.AssociatedUsage(nil), view.AssociatedUsage...)
	sort.Slice(usage, func(i, j int) bool {
		if usage[i].Provider != usage[j].Provider {
			return usage[i].Provider < usage[j].Provider
		}
		return usage[i].SessionID < usage[j].SessionID
	})
	var output strings.Builder
	for _, item := range usage {
		kind := "关联使用"
		if item.Shared {
			kind = "共享使用"
		}
		if output.Len() > 0 {
			output.WriteByte('\n')
		}
		output.WriteString("- " + kind + " · " + item.Provider + "/" + item.SessionID)
	}
	return output.String()
}

func validateOrphanPatches(values []Patch) error {
	return validatePatchSet(values)
}

func clonePatchSet(values []Patch) []Patch {
	result := make([]Patch, len(values))
	for index, value := range values {
		result[index] = value.Clone()
	}
	return result
}
