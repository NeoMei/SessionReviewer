package reviewv4

import (
	"errors"
	"slices"

	"github.com/neomei/SessionReviewer/internal/baselinehash"
)

type markdownFieldLocation struct {
	kind  string
	index int
	name  string
}

type markdownSemanticResolution struct {
	key       string
	ambiguous bool
}

// markdownPresentationIndex is deliberately operation-scoped. It binds the
// fixed Markdown field registry to one presentation clone so validation,
// application, and semantic metadata resolution share the same live lookup.
type markdownPresentationIndex struct {
	presentation *Presentation
	fields       map[FieldKey]markdownFieldLocation
	stored       map[string]markdownSemanticResolution
}

func newMarkdownPresentationIndex(presentation *Presentation) *markdownPresentationIndex {
	fieldCount := 5 + len(presentation.Decisions)*4 + len(presentation.Risks)*3 + len(presentation.OpenLoops)*5 + len(presentation.ProblemNodes)*3 + len(presentation.Timeline)*4
	index := &markdownPresentationIndex{
		presentation: presentation,
		fields:       make(map[FieldKey]markdownFieldLocation, fieldCount),
		stored:       make(map[string]markdownSemanticResolution, fieldCount*2),
	}
	index.addEntity("project-overview", "", -1)
	for position, item := range presentation.Decisions {
		index.addEntity("decision", item.ID, position)
	}
	for position, item := range presentation.Risks {
		index.addEntity("risk", item.ID, position)
	}
	for position, item := range presentation.OpenLoops {
		index.addEntity("open-loop", item.ID, position)
	}
	for position, item := range presentation.ProblemNodes {
		index.addEntity("problem", item.ID, position)
	}
	for position, item := range presentation.Timeline {
		index.addEntity("milestone", item.ID, position)
	}
	return index
}

func (index *markdownPresentationIndex) addEntity(kind, id string, position int) {
	entity := kind
	if kind != "project-overview" {
		entity += ":" + id
	}
	for _, spec := range markdownFieldSpecs {
		if spec.EntityKind != kind {
			continue
		}
		key := FieldKey{Entity: entity, Name: spec.Name}
		index.fields[key] = markdownFieldLocation{kind: kind, index: position, name: spec.Name}
		semanticKey := markdownSemanticKey(entity, spec.Name)
		index.addStoredCandidate(semanticKey, semanticKey)
		if kind != "project-overview" {
			index.addStoredCandidate(markdownSemanticKey(id, spec.Name), semanticKey)
		}
	}
}

func (index *markdownPresentationIndex) addStoredCandidate(storedKey, semanticKey string) {
	resolution, exists := index.stored[storedKey]
	if !exists {
		index.stored[storedKey] = markdownSemanticResolution{key: semanticKey}
		return
	}
	if resolution.key != semanticKey {
		resolution.ambiguous = true
		index.stored[storedKey] = resolution
	}
}

func (index *markdownPresentationIndex) field(key FieldKey) (string, bool) {
	location, exists := index.fields[key]
	if !exists {
		return "", false
	}
	presentation := index.presentation
	switch location.kind {
	case "project-overview":
		switch location.name {
		case "goal":
			return presentation.CurrentState.Goal, true
		case "stage":
			return presentation.CurrentState.Stage, true
		case "status":
			return presentation.CurrentState.Status, true
		case "next_action":
			return presentation.CurrentState.NextAction, true
		case "last_verification":
			return presentation.CurrentState.LastVerification, true
		}
	case "decision":
		item := presentation.Decisions[location.index]
		switch location.name {
		case "title":
			return item.Title, true
		case "rationale":
			return item.Rationale, true
		case "impact":
			return item.Impact, true
		case "reevaluate_when":
			return item.ReevaluateWhen, true
		}
	case "risk":
		item := presentation.Risks[location.index]
		switch location.name {
		case "title":
			return item.Title, true
		case "detail":
			return item.Detail, true
		case "status":
			return item.Status, true
		}
	case "open-loop":
		item := presentation.OpenLoops[location.index]
		switch location.name {
		case "title":
			return item.Title, true
		case "question":
			return item.Question, true
		case "next_experiment":
			return item.NextExperiment, true
		case "completion_criterion":
			return item.CompletionCriterion, true
		case "status":
			return item.Status, true
		}
	case "problem":
		item := presentation.ProblemNodes[location.index]
		switch location.name {
		case "question":
			return item.Question, true
		case "completion_criterion":
			return item.CompletionCriterion, true
		case "current_conclusion":
			return item.CurrentConclusion, true
		}
	case "milestone":
		item := presentation.Timeline[location.index]
		switch location.name {
		case "title":
			return item.Title, true
		case "summary":
			return item.Summary, true
		case "conclusion":
			return item.ClosedLoop.Conclusion.Text, true
		case "impact_and_follow_up":
			return item.ClosedLoop.ImpactAndFollowUp.Text, true
		}
	}
	return "", false
}

func (index *markdownPresentationIndex) set(key FieldKey, value string) error {
	location, exists := index.fields[key]
	if !exists {
		return markdownEditStructureError(key)
	}
	presentation := index.presentation
	switch location.kind {
	case "project-overview":
		switch location.name {
		case "goal":
			presentation.CurrentState.Goal = value
		case "stage":
			presentation.CurrentState.Stage = value
		case "status":
			presentation.CurrentState.Status = value
		case "next_action":
			presentation.CurrentState.NextAction = value
		case "last_verification":
			presentation.CurrentState.LastVerification = value
		}
	case "decision":
		item := &presentation.Decisions[location.index]
		switch location.name {
		case "title":
			item.Title = value
		case "rationale":
			item.Rationale = value
		case "impact":
			item.Impact = value
		case "reevaluate_when":
			item.ReevaluateWhen = value
		}
	case "risk":
		item := &presentation.Risks[location.index]
		switch location.name {
		case "title":
			item.Title = value
		case "detail":
			item.Detail = value
		case "status":
			item.Status = value
		}
	case "open-loop":
		item := &presentation.OpenLoops[location.index]
		switch location.name {
		case "title":
			item.Title = value
		case "question":
			item.Question = value
		case "next_experiment":
			item.NextExperiment = value
		case "completion_criterion":
			item.CompletionCriterion = value
		case "status":
			item.Status = value
		}
	case "problem":
		item := &presentation.ProblemNodes[location.index]
		switch location.name {
		case "question":
			item.Question = value
		case "completion_criterion":
			item.CompletionCriterion = value
		case "current_conclusion":
			item.CurrentConclusion = value
		}
	case "milestone":
		item := &presentation.Timeline[location.index]
		switch location.name {
		case "title":
			item.Title = value
		case "summary":
			item.Summary = value
		case "conclusion":
			if value == "" && item.ClosedLoop.Conclusion.Kind != ConclusionMissing {
				return markdownEditStructureError(key)
			}
			if value != "" {
				item.ClosedLoop.Conclusion.Text = value
				item.ClosedLoop.Conclusion.Kind = ConclusionHumanConfirmed
				item.ClosedLoop.Conclusion.MissingReason = nil
			}
		case "impact_and_follow_up":
			segment := &item.ClosedLoop.ImpactAndFollowUp
			if value == "" && segment.Text != "" {
				return markdownEditStructureError(key)
			}
			if value != "" {
				segment.Text = value
				if segment.State == "missing" {
					segment.State = "present"
				}
				segment.MissingReason = nil
			}
		}
	}
	return nil
}

func (index *markdownPresentationIndex) storedSemanticKey(entityID, field string) (string, bool, error) {
	rawKey := markdownSemanticKey(entityID, field)
	resolution, exists := index.stored[rawKey]
	if !exists {
		return rawKey, false, nil
	}
	if resolution.ambiguous {
		return "", false, errors.New("ambiguous legacy Markdown entity identity")
	}
	return resolution.key, true, nil
}

func (index *markdownPresentationIndex) patchSemanticKey(key FieldKey) (string, error) {
	if _, exists := index.fields[key]; !exists {
		return "", errors.New("Markdown field does not exist")
	}
	return markdownSemanticKey(key.Entity, key.Name), nil
}

func markdownSemanticKey(entityID, field string) string {
	return entityID + "\x00" + field
}

type markdownEditMetadataIndex struct {
	presentation *Presentation
	fields       *markdownPresentationIndex
	baselines    map[string][]int
	patches      map[string][]int
	orphans      map[string][]int
}

func newMarkdownEditMetadataIndex(presentation *Presentation, fields *markdownPresentationIndex) (*markdownEditMetadataIndex, error) {
	index := &markdownEditMetadataIndex{
		presentation: presentation,
		fields:       fields,
		baselines:    make(map[string][]int, len(presentation.GeneratedBaselines)),
		patches:      make(map[string][]int, len(presentation.HumanPatches)),
		orphans:      make(map[string][]int, len(presentation.OrphanPatches)),
	}
	for position, baseline := range presentation.GeneratedBaselines {
		key, _, err := fields.storedSemanticKey(baseline.EntityID, baseline.Field)
		if err != nil {
			return nil, err
		}
		index.baselines[key] = append(index.baselines[key], position)
	}
	for position, patch := range presentation.HumanPatches {
		key, _, err := fields.storedSemanticKey(patch.EntityID, patch.Field)
		if err != nil {
			return nil, err
		}
		index.patches[key] = append(index.patches[key], position)
	}
	for position, patch := range presentation.OrphanPatches {
		key, _, err := fields.storedSemanticKey(patch.EntityID, patch.Field)
		if err != nil {
			return nil, err
		}
		index.orphans[key] = append(index.orphans[key], position)
	}
	return index, nil
}

func (index *markdownEditMetadataIndex) record(edit FieldEdit) error {
	semanticKey, err := index.fields.patchSemanticKey(edit.Key)
	if err != nil {
		return markdownBaselineError(edit, err)
	}
	baselinePositions := index.baselines[semanticKey]
	if len(baselinePositions) > 1 {
		return markdownBaselineError(edit, nil)
	}
	if len(baselinePositions) == 0 {
		before := edit.Before
		entityID := markdownPatchEntityID(edit.Key)
		index.presentation.GeneratedBaselines = append(index.presentation.GeneratedBaselines, Baseline{
			GenerationID: index.presentation.GenerationID, EntityID: entityID, Field: edit.Key.Name,
			Kind: "scalar", Value: &before,
			GeneratedHash: baselinehash.SHA256(entityID, edit.Key.Name, "scalar", before, nil),
		})
		position := len(index.presentation.GeneratedBaselines) - 1
		index.baselines[semanticKey] = []int{position}
		baselinePositions = []int{position}
	}
	baseline := index.presentation.GeneratedBaselines[baselinePositions[0]]
	if baseline.GenerationID != index.presentation.GenerationID || baseline.Kind != "scalar" || baseline.Value == nil || baseline.Values != nil ||
		baseline.GeneratedHash != baselinehash.SHA256(baseline.EntityID, baseline.Field, baseline.Kind, *baseline.Value, nil) {
		return markdownBaselineError(edit, nil)
	}

	patchPositions := index.patches[semanticKey]
	if len(patchPositions) > 0 {
		patch := index.presentation.HumanPatches[patchPositions[0]]
		if patch.EntityID != baseline.EntityID || patch.Field != baseline.Field {
			return markdownBaselineError(edit, nil)
		}
		if len(patchPositions) > 1 {
			return &MarkdownError{Code: MarkdownFieldDuplicate, Entity: edit.Key.Entity, Field: edit.Key.Name}
		}
	}
	if orphanPositions := index.orphans[semanticKey]; len(orphanPositions) > 0 {
		patch := index.presentation.OrphanPatches[orphanPositions[0]]
		if patch.EntityID != baseline.EntityID || patch.Field != baseline.Field {
			return markdownBaselineError(edit, nil)
		}
		return &MarkdownError{Code: MarkdownFieldDuplicate, Entity: edit.Key.Entity, Field: edit.Key.Name}
	}

	if edit.After == *baseline.Value {
		if len(patchPositions) == 1 {
			index.removePatch(semanticKey, patchPositions[0])
		}
		return nil
	}
	after := edit.After
	patch := Patch{EntityID: baseline.EntityID, Field: baseline.Field, Operation: "set", Value: &after, BaseGeneratedHash: baseline.GeneratedHash}
	if len(patchPositions) == 1 {
		index.presentation.HumanPatches[patchPositions[0]] = patch
	} else {
		index.presentation.HumanPatches = append(index.presentation.HumanPatches, patch)
		index.patches[semanticKey] = []int{len(index.presentation.HumanPatches) - 1}
	}
	return nil
}

func (index *markdownEditMetadataIndex) removePatch(semanticKey string, position int) {
	last := len(index.presentation.HumanPatches) - 1
	if position != last {
		moved := index.presentation.HumanPatches[last]
		movedKey, _, _ := index.fields.storedSemanticKey(moved.EntityID, moved.Field)
		index.presentation.HumanPatches[position] = moved
		positions := index.patches[movedKey]
		for item := range positions {
			if positions[item] == last {
				positions[item] = position
				break
			}
		}
		index.patches[movedKey] = positions
	}
	index.presentation.HumanPatches = slices.Delete(index.presentation.HumanPatches, last, last+1)
	delete(index.patches, semanticKey)
}

func markdownBaselineError(edit FieldEdit, cause error) error {
	return &MarkdownError{Code: MarkdownBaselineMissing, Entity: edit.Key.Entity, Field: edit.Key.Name, Cause: cause}
}
