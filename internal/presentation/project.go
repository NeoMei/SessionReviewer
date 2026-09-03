package presentation

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

type ProjectInput struct {
	ProjectView        memory.ProjectView
	GenerationID       string
	Revision           int
	ProjectName        string
	Accounting         accounting.ProjectSummary
	SessionReports     []ledger.SessionReport
	LastSuccessfulSync string
	Legacy             reviewv2.LegacyPresentation
	ActivePatches      []Patch
	OrphanPatches      []Patch
	PreservedEventIDs  []string
	UnknownBlocks      map[string][]byte
	ExpectedFiles      map[string][]byte
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
	input.Legacy.Events = projectHistoryEvents(input.ProjectView, input.Legacy.Events, input.ActivePatches, input.PreservedEventIDs)
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
	review.Goal = appliedValue(applied, "project-overview", "goal")
	review.Status = appliedValue(applied, "project-overview", "status")
	review.NextAction = appliedValue(applied, "project-overview", "next_action")
	review.LastVerification = stripGeneratedPresentationMarkers(review.LastVerification)
	if review.Name == "" || review.Name == input.ProjectView.ProjectID {
		review.Name = strings.TrimSpace(input.ProjectName)
		if review.Name == "" {
			review.Name = input.ProjectView.ProjectID
		}
	}
	review.Risks = appliedRisks(applied, review.Risks)
	review.Decisions = appliedDecisions(applied, review.Decisions)
	events := appliedEvents(applied, input.Legacy.Events)
	return ProjectOutput{
		Review: review, Events: events,
		Applied: applied, Baselines: baselines,
		ActivePatches: clonePatchSet(input.ActivePatches), OrphanPatches: clonePatchSet(input.OrphanPatches),
		RecentProgress: recentProgress(input.ProjectView), Usage: usageSummary(input.ProjectView, input.Accounting),
		UnknownBlocks: cloneUnknownBlocks(input.UnknownBlocks),
	}, nil
}

func projectHistoryEvents(view memory.ProjectView, existing []reviewv2.Event, patches []Patch, preservedIDs []string) []reviewv2.Event {
	records := append([]memory.DerivedRecord(nil), view.DerivedRecords...)
	sort.Slice(records, func(i, j int) bool {
		if records[i].OccurredAt != records[j].OccurredAt {
			return records[i].OccurredAt > records[j].OccurredAt
		}
		return records[i].ID < records[j].ID
	})
	existingByID := make(map[string]reviewv2.Event, len(existing))
	for _, event := range existing {
		existingByID[event.ID] = event
	}
	derivedIDs := make(map[string]struct{})
	selected := make(map[string]struct{})
	result := make([]reviewv2.Event, 0, len(existing)+20)
	for _, record := range records {
		if record.Kind != "event_ref" || record.Fields["operation"] != "user_request" {
			continue
		}
		derivedIDs[record.ID] = struct{}{}
		excerpt := humanRequestExcerpt(record.Fields["excerpt"])
		if excerpt == "" || len(selected) >= 20 {
			continue
		}
		if event, ok := existingByID[record.ID]; ok {
			selected[record.ID] = struct{}{}
			result = append(result, event)
			continue
		}
		title := plainTimelineTitle(excerpt)
		if title == "" {
			continue
		}
		selected[record.ID] = struct{}{}
		result = append(result, reviewv2.Event{
			ID: record.ID, OccurredAt: record.OccurredAt, Kind: "user_request", Title: title,
			Meaning: "项目工作请求", Summary: excerpt, Why: "用户在项目 Session 中提出",
			Changes: []string{"记录该项目请求"}, Results: []string{"已纳入项目脉络索引"},
			Next: "结合后续 Session 验证实际结果",
		})
	}
	patched := make(map[string]struct{})
	for _, patch := range patches {
		patched[patch.EntityID] = struct{}{}
	}
	for _, id := range preservedIDs {
		patched[id] = struct{}{}
	}
	for _, event := range existing {
		if _, already := selected[event.ID]; already {
			continue
		}
		_, generated := derivedIDs[event.ID]
		_, humanEdited := patched[event.ID]
		if !generated || humanEdited {
			result = append(result, event)
		}
	}
	return result
}

func plainTimelineTitle(value string) string {
	value = strings.NewReplacer(
		"`", "", "*", "", "_", "", "~", "", "[", "", "]", "",
		"<", "", ">", "", "#", "", "\\", "",
	).Replace(value)
	return conciseLine(value)
}

func stripGeneratedPresentationMarkers(value string) string {
	lines := strings.Split(value, "\n")
	kept := lines[:0]
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		generated := false
		for _, identity := range []string{GeneratedSectionRecentProgress, GeneratedSectionModelUsage, GeneratedSectionCustomContent} {
			if trimmed == generatedSectionOpen(identity) || trimmed == generatedSectionClose(identity) {
				generated = true
				break
			}
		}
		if !generated {
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
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

func appliedValue(fields map[string]AppliedField, entityID, field string) string {
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
		risk.Title = appliedValue(fields, risk.ID, "title")
		risk.Status = appliedValue(fields, risk.ID, "status")
		risk.Detail = appliedValue(fields, risk.ID, "detail")
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
		decision.Title = appliedValue(fields, decision.ID, "title")
		decision.Rationale = appliedValue(fields, decision.ID, "rationale")
		decision.Impact = appliedValue(fields, decision.ID, "impact")
		decision.Status = appliedValue(fields, decision.ID, "status")
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
		event.Title = appliedValue(fields, event.ID, "title")
		event.Meaning = appliedValue(fields, event.ID, "meaning")
		event.Summary = appliedValue(fields, event.ID, "summary")
		event.Why = appliedValue(fields, event.ID, "why")
		event.Changes = appliedValues(fields, event.ID, "changes")
		event.Results = appliedValues(fields, event.ID, "results")
		event.Next = appliedValue(fields, event.ID, "next")
		result = append(result, event)
	}
	return result
}

func appliedValues(fields map[string]AppliedField, entityID, field string) []string {
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
		if record.Kind != "event_ref" || (record.Fields["operation"] != "" && record.Fields["operation"] != "user_request") {
			continue
		}
		excerpt := humanRequestExcerpt(record.Fields["excerpt"])
		if excerpt == "" && record.Fields["operation"] == "" {
			excerpt = conciseLine(record.Subject)
		}
		if excerpt == "" {
			continue
		}
		if output.Len() > 0 {
			output.WriteByte('\n')
		}
		output.WriteString("- " + record.OccurredAt + " · " + excerpt)
		if strings.Count(output.String(), "\n") >= 19 {
			break
		}
	}
	return output.String()
}

func humanRequestExcerpt(value string) string {
	if marker := strings.LastIndex(value, "## My request:"); marker >= 0 {
		value = value[marker+len("## My request:"):]
	}
	value = strings.TrimSpace(value)
	if platformEnvelope(value) {
		return ""
	}
	return conciseLine(value)
}

func conciseLine(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	const maximumRunes = 160
	runes := []rune(value)
	if len(runes) > maximumRunes {
		value = string(runes[:maximumRunes]) + "…"
	}
	return value
}

func platformEnvelope(value string) bool {
	for _, prefix := range []string{"<recommended_plugins>", "<environment_context>", "<app-context>", "<skills_instructions>", "<permissions instructions>", "<collaboration_mode>", "# AGENTS.md instructions", "<INSTRUCTIONS>"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func usageSummary(view memory.ProjectView, summary accounting.ProjectSummary) string {
	shared := 0
	for _, item := range view.AssociatedUsage {
		if item.Shared {
			shared++
		}
	}
	var output strings.Builder
	fmt.Fprintf(&output, "- %d 个 Session", len(view.AssociatedUsage))
	if shared > 0 {
		fmt.Fprintf(&output, " · %d 个跨项目共享", shared)
	}
	models := append([]accounting.ProjectModelSummary(nil), summary.Models...)
	sort.Slice(models, func(i, j int) bool {
		if models[i].TotalTokens != models[j].TotalTokens {
			return models[i].TotalTokens > models[j].TotalTokens
		}
		return models[i].Model < models[j].Model
	})
	for _, model := range models {
		fmt.Fprintf(&output, "\n- %s · %s tokens · %.2f%%", model.Model, formatInteger(model.TotalTokens), model.TokenSharePct)
	}
	return output.String()
}

func formatInteger(value int64) string {
	raw := strconv.FormatInt(value, 10)
	for index := len(raw) - 3; index > 0; index -= 3 {
		raw = raw[:index] + "," + raw[index:]
	}
	return raw
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
