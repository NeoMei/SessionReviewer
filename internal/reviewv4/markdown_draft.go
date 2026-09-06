package reviewv4

import (
	"bytes"
	"fmt"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/neomei/SessionReviewer/internal/baselinehash"
)

type MarkdownPair struct {
	Review  []byte
	History []byte
}

type FieldEdit struct {
	Key           FieldKey
	Before, After string
}

type MarkdownDraft struct {
	Documents    MarkdownPair
	Edits        []FieldEdit
	Presentation Presentation
}

func ParseMarkdownDraft(pair MarkdownPair, ledger MachineLedger) (MarkdownDraft, error) {
	if err := validateMarkdownLedger(ledger); err != nil {
		return MarkdownDraft{}, err
	}
	base := ledger.DocumentProjection.PresentationBase
	review, err := ParseMarkdownDocument(markdownReviewRelative, pair.Review)
	if err != nil {
		return MarkdownDraft{}, err
	}
	history, err := ParseMarkdownDocument(markdownHistoryRelative, pair.History)
	if err != nil {
		return MarkdownDraft{}, err
	}
	if err := validateMarkdownFrontmatterIdentity(markdownReviewRelative, pair.Review, base); err != nil {
		return MarkdownDraft{}, err
	}
	if err := validateMarkdownFrontmatterIdentity(markdownHistoryRelative, pair.History, base); err != nil {
		return MarkdownDraft{}, err
	}
	expectedPair, err := renderFreshMarkdown(base)
	if err != nil {
		return MarkdownDraft{}, err
	}
	expectedReview, err := ParseMarkdownDocument(markdownReviewRelative, expectedPair.Review)
	if err != nil {
		return MarkdownDraft{}, err
	}
	expectedHistory, err := ParseMarkdownDocument(markdownHistoryRelative, expectedPair.History)
	if err != nil {
		return MarkdownDraft{}, err
	}
	if err := validateMarkdownStructure(review, expectedReview); err != nil {
		return MarkdownDraft{}, err
	}
	if err := validateMarkdownStructure(history, expectedHistory); err != nil {
		return MarkdownDraft{}, err
	}
	if err := validateMarkdownAnchors(markdownReviewRelative, review.raw, expectedReview.raw); err != nil {
		return MarkdownDraft{}, err
	}
	if err := validateMarkdownAnchors(markdownHistoryRelative, history.raw, expectedHistory.raw); err != nil {
		return MarkdownDraft{}, err
	}
	edits := make([]FieldEdit, 0)
	for _, documents := range []struct {
		actual, expected MarkdownDocument
		relative         string
	}{{review, expectedReview, markdownReviewRelative}, {history, expectedHistory, markdownHistoryRelative}} {
		actual, expected := documents.actual, documents.expected
		expectedIndex, indexErr := newMarkdownDocumentIndex(documents.relative, expected)
		if indexErr != nil {
			return MarkdownDraft{}, indexErr
		}
		for _, block := range actual.blocks {
			expectedBlock := expectedIndex.blocks[block.Key]
			after := markdownBlockValue(actual, block)
			before := markdownBlockValue(expected, expectedBlock)
			if block.Generated {
				if !markdownSemanticEqual(after, before) {
					return MarkdownDraft{}, &MarkdownError{Code: MarkdownGeneratedRegionModified, Relative: documents.relative, Entity: block.Key.Entity, Field: block.Key.Name}
				}
				continue
			}
			if !markdownSemanticEqual(after, before) {
				edits = append(edits, FieldEdit{Key: block.Key, Before: before, After: after})
			}
		}
	}
	next, err := ApplyMarkdownEdits(base, edits)
	if err != nil {
		return MarkdownDraft{}, err
	}
	if sha256Hex(pair.Review) != ledger.ReviewSHA256 || sha256Hex(pair.History) != ledger.HistorySHA256 {
		if len(edits) == 0 {
			if int64(next.Revision) >= maxWireInteger {
				return MarkdownDraft{}, &MarkdownError{Code: MarkdownFormatInvalid}
			}
			next.Revision++
		}
	}
	if err := ValidatePresentation(next); err != nil {
		return MarkdownDraft{}, &MarkdownError{Code: MarkdownFormatInvalid, Cause: err}
	}
	return MarkdownDraft{
		Documents: MarkdownPair{Review: bytes.Clone(pair.Review), History: bytes.Clone(pair.History)},
		Edits:     append([]FieldEdit(nil), edits...), Presentation: next,
	}, nil
}

func validateMarkdownLedger(ledger MachineLedger) error {
	if err := ValidateLedger(ledger); err != nil {
		return &MarkdownError{Code: MarkdownBaselineMissing, Cause: err}
	}
	if isZeroSHA(ledger.SyncHashes.LedgerSHA256) || CanonicalLedgerSHA256(ledger) != ledger.SyncHashes.LedgerSHA256 {
		return &MarkdownError{Code: MarkdownBaselineMissing}
	}
	if ledger.DocumentProjection == nil || ledger.DocumentProjection.Format != "review-markdown-v1" {
		return &MarkdownError{Code: MarkdownBaselineMissing}
	}
	return nil
}

type markdownDocumentIndex struct {
	blocks      map[FieldKey]MarkdownBlock
	anchors     map[string]int
	anchorSpans []markdownAnchorSpan
}

type markdownAnchorSpan struct {
	start, end int
	line       string
}

func newMarkdownDocumentIndex(relative string, document MarkdownDocument) (markdownDocumentIndex, error) {
	index := markdownDocumentIndex{
		blocks:  make(map[FieldKey]MarkdownBlock, len(document.blocks)),
		anchors: make(map[string]int),
	}
	for _, block := range document.blocks {
		if _, exists := index.blocks[block.Key]; exists {
			return markdownDocumentIndex{}, &MarkdownError{Code: MarkdownFieldDuplicate, Relative: relative, Entity: block.Key.Entity, Field: block.Key.Name}
		}
		index.blocks[block.Key] = block
	}
	var fence byte
	fenceLength := 0
	indentedCode := false
	for start := 0; start < len(document.raw); {
		end, next := markdownPhysicalLine(document.raw, start)
		line := document.raw[start:end]
		if indentedCode {
			if len(line) == 0 || markdownIndented(line) {
				start = next
				continue
			}
			indentedCode = false
		}
		if fence != 0 {
			if markdownFenceClose(line, fence, fenceLength) {
				fence, fenceLength = 0, 0
			}
			start = next
			continue
		}
		if character, length, ok := markdownFenceOpen(line); ok {
			fence, fenceLength = character, length
			start = next
			continue
		}
		if markdownIndented(line) {
			indentedCode = true
			start = next
			continue
		}
		if bytes.HasPrefix(line, []byte(`<a id="`)) && bytes.HasSuffix(line, []byte(`"></a>`)) {
			anchor := string(line)
			index.anchors[anchor]++
			index.anchorSpans = append(index.anchorSpans, markdownAnchorSpan{start: start, end: end, line: anchor})
		}
		start = next
	}
	return index, nil
}

func validateMarkdownStructure(actual, expected MarkdownDocument) error {
	actualIndex, err := newMarkdownDocumentIndex("", actual)
	if err != nil {
		return err
	}
	expectedIndex, err := newMarkdownDocumentIndex("", expected)
	if err != nil {
		return err
	}
	if len(actual.blocks) != len(expected.blocks) {
		for _, block := range expected.blocks {
			if _, exists := actualIndex.blocks[block.Key]; !exists {
				return &MarkdownError{Code: MarkdownFieldMissing, Entity: block.Key.Entity, Field: block.Key.Name}
			}
		}
		return &MarkdownError{Code: MarkdownStructureEditRequiresCommand}
	}
	for _, block := range actual.blocks {
		expectedBlock, exists := expectedIndex.blocks[block.Key]
		if !exists || block.Generated != expectedBlock.Generated {
			return &MarkdownError{Code: MarkdownStructureEditRequiresCommand, Entity: block.Key.Entity, Field: block.Key.Name}
		}
	}
	return nil
}

func markdownBlockValue(document MarkdownDocument, block MarkdownBlock) string {
	return string(document.raw[block.ValueStart:block.ValueEnd])
}

func markdownSemanticEqual(left, right string) bool {
	return strings.ReplaceAll(left, "\r\n", "\n") == strings.ReplaceAll(right, "\r\n", "\n")
}

func validateMarkdownFrontmatterIdentity(relative string, raw []byte, base Presentation) error {
	mapping, err := markdownFrontmatter(raw)
	if err != nil {
		return &MarkdownError{Code: MarkdownFormatInvalid, Relative: relative}
	}
	values := make(map[string]*yaml.Node, len(mapping.Content)/2)
	for index := 0; index < len(mapping.Content); index += 2 {
		values[mapping.Content[index].Value] = mapping.Content[index+1]
	}
	wantEntity := "project-review"
	wantID := "review-" + base.ProjectID
	if relative == markdownHistoryRelative {
		wantEntity = "project-history"
		wantID = "history-" + base.ProjectID
	}
	id, idOK := markdownString(values["id"])
	entityType, entityOK := markdownString(values["entity_type"])
	projectID, projectOK := markdownString(values["project_id"])
	generationID, generationOK := markdownString(values["generation_id"])
	revision, revisionOK := markdownInteger(values["revision"])
	if !idOK || !entityOK || !projectOK || !generationOK || !revisionOK || id != wantID || entityType != wantEntity || projectID != base.ProjectID || generationID != base.GenerationID || revision != base.Revision {
		return &MarkdownError{Code: MarkdownStructureEditRequiresCommand, Relative: relative}
	}
	return nil
}

func ApplyMarkdownEdits(base Presentation, edits []FieldEdit) (Presentation, error) {
	if err := ValidatePresentation(base); err != nil {
		return Presentation{}, err
	}
	next := clonePresentation(base)
	seen := make(map[FieldKey]bool, len(edits))
	changed := make([]FieldEdit, 0, len(edits))
	for _, edit := range edits {
		if seen[edit.Key] {
			return Presentation{}, &MarkdownError{Code: MarkdownFieldDuplicate, Entity: edit.Key.Entity, Field: edit.Key.Name}
		}
		seen[edit.Key] = true
		before, exists := markdownPresentationField(base, edit.Key)
		if !exists {
			return Presentation{}, &MarkdownError{Code: MarkdownStructureEditRequiresCommand, Entity: edit.Key.Entity, Field: edit.Key.Name}
		}
		if edit.Before != before {
			return Presentation{}, &MarkdownError{Code: MarkdownFieldConflict, Entity: edit.Key.Entity, Field: edit.Key.Name}
		}
		if edit.After != edit.Before {
			changed = append(changed, edit)
		}
	}
	if len(changed) == 0 {
		return next, nil
	}
	changedDecisions := map[string]bool{}
	changedProblems := map[string]bool{}
	for _, edit := range changed {
		kind, id, _ := splitMarkdownEntity(edit.Key.Entity)
		if err := setMarkdownPresentationField(&next, edit.Key, edit.After); err != nil {
			return Presentation{}, err
		}
		if kind == "decision" {
			changedDecisions[id] = true
		}
		if kind == "problem" {
			changedProblems[id] = true
		}
		if err := recordMarkdownPatch(&next, edit); err != nil {
			return Presentation{}, err
		}
	}
	next.Revision++
	for index := range next.Decisions {
		if changedDecisions[next.Decisions[index].ID] {
			next.Decisions[index].Revision++
		}
	}
	for index := range next.ProblemNodes {
		if changedProblems[next.ProblemNodes[index].ID] {
			next.ProblemNodes[index].Revision++
		}
	}
	sort.Slice(next.HumanPatches, func(i, j int) bool {
		if next.HumanPatches[i].EntityID != next.HumanPatches[j].EntityID {
			return next.HumanPatches[i].EntityID < next.HumanPatches[j].EntityID
		}
		return next.HumanPatches[i].Field < next.HumanPatches[j].Field
	})
	sort.Slice(next.GeneratedBaselines, func(i, j int) bool {
		if next.GeneratedBaselines[i].EntityID != next.GeneratedBaselines[j].EntityID {
			return next.GeneratedBaselines[i].EntityID < next.GeneratedBaselines[j].EntityID
		}
		return next.GeneratedBaselines[i].Field < next.GeneratedBaselines[j].Field
	})
	if err := ValidatePresentation(next); err != nil {
		return Presentation{}, &MarkdownError{Code: MarkdownFormatInvalid, Cause: fmt.Errorf("apply markdown edits: %w", err)}
	}
	return next, nil
}

func markdownPresentationField(p Presentation, key FieldKey) (string, bool) {
	kind, id, hasID := splitMarkdownEntity(key.Entity)
	switch kind {
	case "project-overview":
		if hasID {
			return "", false
		}
		switch key.Name {
		case "goal":
			return p.CurrentState.Goal, true
		case "stage":
			return p.CurrentState.Stage, true
		case "status":
			return p.CurrentState.Status, true
		case "next_action":
			return p.CurrentState.NextAction, true
		case "last_verification":
			return p.CurrentState.LastVerification, true
		}
	case "decision":
		for _, item := range p.Decisions {
			if item.ID != id {
				continue
			}
			switch key.Name {
			case "title":
				return item.Title, true
			case "rationale":
				return item.Rationale, true
			case "impact":
				return item.Impact, true
			case "reevaluate_when":
				return item.ReevaluateWhen, true
			}
		}
	case "risk":
		for _, item := range p.Risks {
			if item.ID != id {
				continue
			}
			switch key.Name {
			case "title":
				return item.Title, true
			case "detail":
				return item.Detail, true
			case "status":
				return item.Status, true
			}
		}
	case "open-loop":
		for _, item := range p.OpenLoops {
			if item.ID != id {
				continue
			}
			switch key.Name {
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
		}
	case "problem":
		for _, item := range p.ProblemNodes {
			if item.ID != id {
				continue
			}
			switch key.Name {
			case "question":
				return item.Question, true
			case "completion_criterion":
				return item.CompletionCriterion, true
			case "current_conclusion":
				return item.CurrentConclusion, true
			}
		}
	case "milestone":
		for _, item := range p.Timeline {
			if item.ID != id {
				continue
			}
			switch key.Name {
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
	}
	return "", false
}

func setMarkdownPresentationField(p *Presentation, key FieldKey, value string) error {
	kind, id, _ := splitMarkdownEntity(key.Entity)
	switch kind {
	case "project-overview":
		switch key.Name {
		case "goal":
			p.CurrentState.Goal = value
		case "stage":
			p.CurrentState.Stage = value
		case "status":
			p.CurrentState.Status = value
		case "next_action":
			p.CurrentState.NextAction = value
		case "last_verification":
			p.CurrentState.LastVerification = value
		default:
			return markdownEditStructureError(key)
		}
		return nil
	case "decision":
		for index := range p.Decisions {
			item := &p.Decisions[index]
			if item.ID != id {
				continue
			}
			switch key.Name {
			case "title":
				item.Title = value
			case "rationale":
				item.Rationale = value
			case "impact":
				item.Impact = value
			case "reevaluate_when":
				item.ReevaluateWhen = value
			default:
				return markdownEditStructureError(key)
			}
			return nil
		}
	case "risk":
		for index := range p.Risks {
			item := &p.Risks[index]
			if item.ID != id {
				continue
			}
			switch key.Name {
			case "title":
				item.Title = value
			case "detail":
				item.Detail = value
			case "status":
				item.Status = value
			default:
				return markdownEditStructureError(key)
			}
			return nil
		}
	case "open-loop":
		for index := range p.OpenLoops {
			item := &p.OpenLoops[index]
			if item.ID != id {
				continue
			}
			switch key.Name {
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
			default:
				return markdownEditStructureError(key)
			}
			return nil
		}
	case "problem":
		for index := range p.ProblemNodes {
			item := &p.ProblemNodes[index]
			if item.ID != id {
				continue
			}
			switch key.Name {
			case "question":
				item.Question = value
			case "completion_criterion":
				item.CompletionCriterion = value
			case "current_conclusion":
				item.CurrentConclusion = value
			default:
				return markdownEditStructureError(key)
			}
			return nil
		}
	case "milestone":
		for index := range p.Timeline {
			item := &p.Timeline[index]
			if item.ID != id {
				continue
			}
			switch key.Name {
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
			default:
				return markdownEditStructureError(key)
			}
			return nil
		}
	}
	return markdownEditStructureError(key)
}

func markdownEditStructureError(key FieldKey) error {
	return &MarkdownError{Code: MarkdownStructureEditRequiresCommand, Entity: key.Entity, Field: key.Name}
}

func recordMarkdownPatch(p *Presentation, edit FieldEdit) error {
	semanticKey, err := markdownPatchSemanticKey(*p, edit.Key)
	if err != nil {
		return &MarkdownError{Code: MarkdownBaselineMissing, Entity: edit.Key.Entity, Field: edit.Key.Name, Cause: err}
	}
	entityID := markdownPatchEntityID(edit.Key)
	baselineIndex := -1
	for index, baseline := range p.GeneratedBaselines {
		key, _, resolveErr := markdownStoredSemanticKey(*p, baseline.EntityID, baseline.Field)
		if resolveErr != nil {
			return &MarkdownError{Code: MarkdownBaselineMissing, Entity: edit.Key.Entity, Field: edit.Key.Name, Cause: resolveErr}
		}
		if key == semanticKey {
			if baselineIndex >= 0 {
				return &MarkdownError{Code: MarkdownBaselineMissing, Entity: edit.Key.Entity, Field: edit.Key.Name}
			}
			baselineIndex = index
		}
	}
	if baselineIndex < 0 {
		before := edit.Before
		p.GeneratedBaselines = append(p.GeneratedBaselines, Baseline{
			GenerationID: p.GenerationID, EntityID: entityID, Field: edit.Key.Name,
			Kind: "scalar", Value: &before,
			GeneratedHash: baselinehash.SHA256(entityID, edit.Key.Name, "scalar", before, nil),
		})
		baselineIndex = len(p.GeneratedBaselines) - 1
	}
	baseline := p.GeneratedBaselines[baselineIndex]
	if baseline.GenerationID != p.GenerationID || baseline.Kind != "scalar" || baseline.Value == nil || baseline.Values != nil ||
		baseline.GeneratedHash != baselinehash.SHA256(baseline.EntityID, baseline.Field, baseline.Kind, *baseline.Value, nil) {
		return &MarkdownError{Code: MarkdownBaselineMissing, Entity: edit.Key.Entity, Field: edit.Key.Name}
	}
	patchIndex := -1
	for index, patch := range p.HumanPatches {
		key, _, resolveErr := markdownStoredSemanticKey(*p, patch.EntityID, patch.Field)
		if resolveErr != nil {
			return &MarkdownError{Code: MarkdownBaselineMissing, Entity: edit.Key.Entity, Field: edit.Key.Name, Cause: resolveErr}
		}
		if key == semanticKey {
			if patch.EntityID != baseline.EntityID || patch.Field != baseline.Field {
				return &MarkdownError{Code: MarkdownBaselineMissing, Entity: edit.Key.Entity, Field: edit.Key.Name}
			}
			if patchIndex >= 0 {
				return &MarkdownError{Code: MarkdownFieldDuplicate, Entity: edit.Key.Entity, Field: edit.Key.Name}
			}
			patchIndex = index
		}
	}
	for _, patch := range p.OrphanPatches {
		key, _, resolveErr := markdownStoredSemanticKey(*p, patch.EntityID, patch.Field)
		if resolveErr != nil {
			return &MarkdownError{Code: MarkdownBaselineMissing, Entity: edit.Key.Entity, Field: edit.Key.Name, Cause: resolveErr}
		}
		if key == semanticKey {
			if patch.EntityID != baseline.EntityID || patch.Field != baseline.Field {
				return &MarkdownError{Code: MarkdownBaselineMissing, Entity: edit.Key.Entity, Field: edit.Key.Name}
			}
			return &MarkdownError{Code: MarkdownFieldDuplicate, Entity: edit.Key.Entity, Field: edit.Key.Name}
		}
	}
	if edit.After == *baseline.Value {
		if patchIndex >= 0 {
			p.HumanPatches = slices.Delete(p.HumanPatches, patchIndex, patchIndex+1)
		}
		return nil
	}
	after := edit.After
	patch := Patch{EntityID: baseline.EntityID, Field: baseline.Field, Operation: "set", Value: &after, BaseGeneratedHash: baseline.GeneratedHash}
	if patchIndex >= 0 {
		p.HumanPatches[patchIndex] = patch
	} else {
		p.HumanPatches = append(p.HumanPatches, patch)
	}
	return nil
}

func markdownPatchEntityID(key FieldKey) string {
	return key.Entity
}

func splitMarkdownEntity(entity string) (kind, id string, hasID bool) {
	for index := 0; index < len(entity); index++ {
		if entity[index] == ':' {
			return entity[:index], entity[index+1:], true
		}
	}
	return entity, "", false
}

func clonePresentation(base Presentation) Presentation {
	next := base
	next.Timeline = slices.Clone(base.Timeline)
	for index := range next.Timeline {
		next.Timeline[index].DecisionIDs = slices.Clone(base.Timeline[index].DecisionIDs)
		next.Timeline[index].ClosedLoop = cloneClosedLoop(base.Timeline[index].ClosedLoop)
	}
	next.Decisions = slices.Clone(base.Decisions)
	for index := range next.Decisions {
		next.Decisions[index].Supersedes = slices.Clone(base.Decisions[index].Supersedes)
		next.Decisions[index].MilestoneIDs = slices.Clone(base.Decisions[index].MilestoneIDs)
		next.Decisions[index].SessionRefs = slices.Clone(base.Decisions[index].SessionRefs)
		if base.Decisions[index].LegacyStatusText != nil {
			value := *base.Decisions[index].LegacyStatusText
			next.Decisions[index].LegacyStatusText = &value
		}
	}
	next.Risks = slices.Clone(base.Risks)
	next.OpenLoops = slices.Clone(base.OpenLoops)
	next.ProblemRootIDs = slices.Clone(base.ProblemRootIDs)
	next.ProblemNodes = slices.Clone(base.ProblemNodes)
	for index := range next.ProblemNodes {
		next.ProblemNodes[index].RelatedNodeIDs = slices.Clone(base.ProblemNodes[index].RelatedNodeIDs)
		next.ProblemNodes[index].SourceTurnRefs = slices.Clone(base.ProblemNodes[index].SourceTurnRefs)
		if base.ProblemNodes[index].PrimaryParentID != nil {
			value := *base.ProblemNodes[index].PrimaryParentID
			next.ProblemNodes[index].PrimaryParentID = &value
		}
		if base.ProblemNodes[index].ConfirmedAt != nil {
			value := *base.ProblemNodes[index].ConfirmedAt
			next.ProblemNodes[index].ConfirmedAt = &value
		}
	}
	next.ChainDependencies = slices.Clone(base.ChainDependencies)
	for index := range next.ChainDependencies {
		next.ChainDependencies[index].TurnUnitIDs = slices.Clone(base.ChainDependencies[index].TurnUnitIDs)
	}
	next.HumanPatches = cloneMarkdownPatches(base.HumanPatches)
	next.OrphanPatches = cloneMarkdownPatches(base.OrphanPatches)
	next.GeneratedBaselines = cloneMarkdownBaselines(base.GeneratedBaselines)
	return next
}

func cloneClosedLoop(base ClosedLoop) ClosedLoop {
	next := base
	next.TriggerQuestion = cloneClosedLoopSegment(base.TriggerQuestion)
	next.Execution = cloneClosedLoopSegment(base.Execution)
	next.Verification = cloneClosedLoopSegment(base.Verification)
	next.ImpactAndFollowUp = cloneClosedLoopSegment(base.ImpactAndFollowUp)
	next.Conclusion.SourceTurnRefs = slices.Clone(base.Conclusion.SourceTurnRefs)
	if base.Conclusion.MissingReason != nil {
		value := *base.Conclusion.MissingReason
		next.Conclusion.MissingReason = &value
	}
	next.SourceTurnRefs = slices.Clone(base.SourceTurnRefs)
	return next
}

func cloneClosedLoopSegment(base ClosedLoopSegment) ClosedLoopSegment {
	next := base
	next.SourceTurnRefs = slices.Clone(base.SourceTurnRefs)
	if base.MissingReason != nil {
		value := *base.MissingReason
		next.MissingReason = &value
	}
	return next
}

func cloneMarkdownPatches(values []Patch) []Patch {
	result := slices.Clone(values)
	for index := range result {
		if values[index].Value != nil {
			value := *values[index].Value
			result[index].Value = &value
		}
		if values[index].Values != nil {
			copied := slices.Clone(*values[index].Values)
			if copied == nil {
				copied = []string{}
			}
			result[index].Values = &copied
		}
	}
	return result
}

func cloneMarkdownBaselines(values []Baseline) []Baseline {
	result := slices.Clone(values)
	for index := range result {
		if values[index].Value != nil {
			value := *values[index].Value
			result[index].Value = &value
		}
		if values[index].Values != nil {
			copied := slices.Clone(*values[index].Values)
			if copied == nil {
				copied = []string{}
			}
			result[index].Values = &copied
		}
	}
	return result
}
