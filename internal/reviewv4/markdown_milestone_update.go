package reviewv4

import (
	"bytes"
	"errors"
	"slices"
	"sort"

	"github.com/neomei/SessionReviewer/internal/baselinehash"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

// ScanMilestoneUpdate is the complete public milestone projection produced by
// one scan. It deliberately contains no human-owned presentation fields.
type ScanMilestoneUpdate struct {
	ProjectID         string
	GenerationID      string
	ProjectViewDigest string
	Timeline          []Timeline
	ChainDependencies []ChainDependency
}

var markdownMilestoneScalarFields = [...]string{"title", "summary", "conclusion", "impact_and_follow_up"}

// RebaseMarkdownMilestones authenticates pending Markdown against oldLedger,
// then rebases only a proven machine-owned milestone projection over it.
func RebaseMarkdownMilestones(oldLedger MachineLedger, pending MarkdownPair, update ScanMilestoneUpdate) (Presentation, error) {
	draft, err := ParseMarkdownDraft(pending, oldLedger)
	if err != nil {
		return Presentation{}, err
	}
	base := oldLedger.DocumentProjection.PresentationBase
	if update.ProjectID != base.ProjectID || update.ProjectID != draft.Presentation.ProjectID {
		return Presentation{}, &MarkdownError{Code: MarkdownBaselineMissing}
	}
	incoming, err := validateScanMilestoneUpdate(update)
	if err != nil {
		return Presentation{}, &MarkdownError{Code: MarkdownFormatInvalid, Cause: err}
	}

	validated := clonePresentation(draft.Presentation)
	if err := CarryMarkdownGeneratedBaselines(&validated, validated.GenerationID); err != nil {
		return Presentation{}, &MarkdownError{Code: MarkdownBaselineMissing, Cause: err}
	}
	oldFields := newMarkdownPresentationIndex(&draft.Presentation)
	oldMetadata, err := newMarkdownEditMetadataIndex(&draft.Presentation, oldFields)
	if err != nil {
		return Presentation{}, &MarkdownError{Code: MarkdownBaselineMissing, Cause: err}
	}

	next := clonePresentation(draft.Presentation)
	if err := CarryMarkdownGeneratedBaselines(&next, update.GenerationID); err != nil {
		return Presentation{}, &MarkdownError{Code: MarkdownBaselineMissing, Cause: err}
	}
	next.GenerationID = update.GenerationID
	next.ProjectViewDigest = update.ProjectViewDigest
	combinedDependencies, err := mergeMilestoneDependencies(next.ChainDependencies, incoming.ChainDependencies)
	if err != nil {
		return Presentation{}, &MarkdownError{Code: MarkdownFormatInvalid, Cause: err}
	}
	authenticatedBindings, err := newSourceTurnBindingIndex(draft.Presentation.ChainDependencies)
	if err != nil {
		return Presentation{}, &MarkdownError{Code: MarkdownBaselineMissing, Cause: err}
	}
	incomingBindings, err := newSourceTurnBindingIndex(incoming.ChainDependencies)
	if err != nil {
		return Presentation{}, &MarkdownError{Code: MarkdownFormatInvalid, Cause: err}
	}
	combinedBindings, err := newSourceTurnBindingIndex(combinedDependencies)
	if err != nil {
		return Presentation{}, &MarkdownError{Code: MarkdownFormatInvalid, Cause: err}
	}
	if err := qualifyPresentationLegacyRefs(&next, authenticatedBindings, combinedBindings); err != nil {
		return Presentation{}, &MarkdownError{Code: MarkdownBaselineMissing, Cause: err}
	}
	if err := qualifyTimelineLegacyRefs(incoming.Timeline, incomingBindings, combinedBindings); err != nil {
		return Presentation{}, &MarkdownError{Code: MarkdownFormatInvalid, Cause: err}
	}
	next.ChainDependencies = combinedDependencies

	byID := make(map[string]int, len(next.Timeline))
	for index, item := range next.Timeline {
		byID[item.ID] = index
	}
	newCount := 0
	for _, item := range incoming.Timeline {
		if _, exists := byID[item.ID]; !exists {
			newCount++
		}
	}
	if newCount > 65536-len(next.Timeline) || newCount > (65536-len(next.GeneratedBaselines))/len(markdownMilestoneScalarFields) {
		return Presentation{}, &MarkdownError{Code: MarkdownFormatInvalid, Cause: errors.New("milestone update exceeds array limit")}
	}

	for _, generated := range incoming.Timeline {
		position, exists := byID[generated.ID]
		if !exists {
			next.Timeline = append(next.Timeline, cloneTimeline(generated))
			AddScanMilestoneBaselines(&next, generated)
			byID[generated.ID] = len(next.Timeline) - 1
			continue
		}
		humanFields, ownershipErr := authenticatedGeneratedMilestoneFields(draft.Presentation, oldFields, oldMetadata, position)
		if ownershipErr != nil {
			return Presentation{}, &MarkdownError{Code: MarkdownBaselineMissing, Entity: "milestone:" + generated.ID, Cause: ownershipErr}
		}
		preserved := next.Timeline[position]
		replacement := cloneTimeline(generated)
		if humanFields["title"] {
			replacement.Title = preserved.Title
		}
		if humanFields["summary"] {
			replacement.Summary = preserved.Summary
		}
		if humanFields["conclusion"] {
			replacement.ClosedLoop.Conclusion = cloneConclusion(preserved.ClosedLoop.Conclusion)
			if err := retainMilestoneAggregateRefs(&replacement.ClosedLoop, preserved.ClosedLoop.Conclusion.SourceTurnRefs, combinedBindings); err != nil {
				return Presentation{}, &MarkdownError{Code: MarkdownBaselineMissing, Entity: "milestone:" + generated.ID, Field: "conclusion", Cause: err}
			}
		}
		if humanFields["impact_and_follow_up"] {
			replacement.ClosedLoop.ImpactAndFollowUp = cloneClosedLoopSegment(preserved.ClosedLoop.ImpactAndFollowUp)
			if err := retainMilestoneAggregateRefs(&replacement.ClosedLoop, preserved.ClosedLoop.ImpactAndFollowUp.SourceTurnRefs, combinedBindings); err != nil {
				return Presentation{}, &MarkdownError{Code: MarkdownBaselineMissing, Entity: "milestone:" + generated.ID, Field: "impact_and_follow_up", Cause: err}
			}
		}
		next.Timeline[position] = replacement
		for _, field := range markdownMilestoneScalarFields {
			if err := replaceGeneratedMilestoneBaseline(&next, oldMetadata, generated, field); err != nil {
				return Presentation{}, &MarkdownError{Code: MarkdownBaselineMissing, Entity: "milestone:" + generated.ID, Field: field, Cause: err}
			}
		}
	}

	for index := range next.Timeline {
		next.Timeline[index].GenerationID = update.GenerationID
	}
	sort.Slice(next.GeneratedBaselines, func(i, j int) bool {
		if next.GeneratedBaselines[i].EntityID != next.GeneratedBaselines[j].EntityID {
			return next.GeneratedBaselines[i].EntityID < next.GeneratedBaselines[j].EntityID
		}
		return next.GeneratedBaselines[i].Field < next.GeneratedBaselines[j].Field
	})
	capability := presentationCapability(next)
	next.MinimumReaderVersion, next.MinimumWriterVersion = capability, capability
	equalPrior, err := markdownPresentationsEqual(next, draft.Presentation)
	if err != nil {
		return Presentation{}, &MarkdownError{Code: MarkdownFormatInvalid, Cause: err}
	}
	scanChanged := !equalPrior
	if draft.Presentation.Revision == base.Revision && scanChanged {
		if int64(next.Revision) >= maxWireInteger {
			return Presentation{}, &MarkdownError{Code: MarkdownFormatInvalid, Cause: errors.New("presentation revision overflow")}
		}
		next.Revision++
	}
	if err := CarryMarkdownGeneratedBaselines(&next, next.GenerationID); err != nil {
		return Presentation{}, &MarkdownError{Code: MarkdownBaselineMissing, Cause: err}
	}
	if err := ValidatePresentation(next); err != nil {
		return Presentation{}, &MarkdownError{Code: MarkdownFormatInvalid, Cause: err}
	}
	return next, nil
}

// RenderMarkdownMilestoneUpdate renders only the exact rebase derived from
// oldLedger, pending and update. A caller-provided Presentation cannot widen
// the generated update's authority.
func RenderMarkdownMilestoneUpdate(next Presentation, oldLedger MachineLedger, pending MarkdownPair, update ScanMilestoneUpdate) (MarkdownPair, error) {
	expected, err := RebaseMarkdownMilestones(oldLedger, pending, update)
	if err != nil {
		return MarkdownPair{}, err
	}
	equalExpected, err := markdownPresentationsEqual(next, expected)
	if err != nil {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownFormatInvalid, Cause: err}
	}
	if !equalExpected {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownBaselineMissing}
	}
	return renderMarkdown(next, oldLedger, &pending, true)
}

func validateScanMilestoneUpdate(update ScanMilestoneUpdate) (Presentation, error) {
	if !validID(update.ProjectID) || !validID(update.GenerationID) || !digestRE.MatchString(update.ProjectViewDigest) || len(update.Timeline) > 65536 || len(update.ChainDependencies) > 65536 {
		return Presentation{}, errors.New("invalid scan milestone update metadata")
	}
	if len(update.Timeline) == 0 && len(update.ChainDependencies) != 0 {
		return Presentation{}, errors.New("milestone-free update carries chain dependencies")
	}
	p := Presentation{
		SchemaVersion: 4, ProjectID: update.ProjectID, GenerationID: update.GenerationID, ProjectViewDigest: update.ProjectViewDigest,
		Timeline: cloneTimelines(update.Timeline), Decisions: []Decision{}, Risks: []Risk{}, OpenLoops: []OpenLoop{},
		ProblemRootIDs: []string{}, ProblemNodes: []ProblemNode{}, ChainDependencies: cloneChainDependencies(update.ChainDependencies),
		HumanPatches: []Patch{}, OrphanPatches: []Patch{}, GeneratedBaselines: []Baseline{},
	}
	capability := presentationCapability(p)
	p.MinimumReaderVersion, p.MinimumWriterVersion = capability, capability
	for _, item := range p.Timeline {
		if !isGeneratedMilestoneKind(item.Kind) || len(item.DecisionIDs) != 0 || item.ClosedLoop.Conclusion.Kind != ConclusionVisibleAnswerExcerpt && item.ClosedLoop.Conclusion.Kind != ConclusionMissing {
			return Presentation{}, errors.New("scan milestone is not a supported machine event")
		}
	}
	if err := ValidatePresentation(p); err != nil {
		return Presentation{}, err
	}
	return p, nil
}

func isGeneratedMilestoneKind(kind string) bool {
	switch kind {
	case "machine_verification", "machine_commit", "machine_release", "machine_deployment", "machine_version":
		return true
	default:
		return false
	}
}

func authenticatedGeneratedMilestoneFields(p Presentation, fields *markdownPresentationIndex, metadata *markdownEditMetadataIndex, position int) (map[string]bool, error) {
	item := p.Timeline[position]
	if !isGeneratedMilestoneKind(item.Kind) || item.GenerationID != p.GenerationID {
		return nil, errors.New("existing milestone lacks machine event identity")
	}
	human := make(map[string]bool, len(markdownMilestoneScalarFields))
	for _, field := range markdownMilestoneScalarFields {
		key := FieldKey{Entity: "milestone:" + item.ID, Name: field}
		semanticKey, err := fields.patchSemanticKey(key)
		if err != nil {
			return nil, err
		}
		positions := metadata.baselines[semanticKey]
		if len(positions) != 1 {
			return nil, errors.New("generated milestone lacks a unique scalar baseline")
		}
		baseline := p.GeneratedBaselines[positions[0]]
		if baseline.GenerationID != p.GenerationID || baseline.Kind != "scalar" || baseline.Value == nil || baseline.Values != nil || !validMarkdownBaselineShape(baseline) {
			return nil, errors.New("generated milestone has an invalid scalar baseline")
		}
		current, ok := fields.field(key)
		if !ok {
			return nil, errors.New("generated milestone field is absent")
		}
		patchPositions := metadata.patches[semanticKey]
		if len(patchPositions) == 0 {
			if current != *baseline.Value {
				return nil, errors.New("unpatched generated field differs from its baseline")
			}
			continue
		}
		if len(patchPositions) != 1 {
			return nil, errors.New("generated milestone has duplicate human patches")
		}
		patch := p.HumanPatches[patchPositions[0]]
		if !validMarkdownPatchBinding(patch, baseline) || !markdownPatchMatchesLiveValue(patch, baseline, current) {
			return nil, errors.New("generated milestone patch does not match its live field")
		}
		if field == "conclusion" && patch.Operation != "restore_default" && item.ClosedLoop.Conclusion.Kind != ConclusionHumanConfirmed && item.ClosedLoop.Conclusion.Kind != ConclusionAICandidateConfirmed {
			return nil, errors.New("edited generated conclusion is not explicitly confirmed")
		}
		human[field] = patch.Operation != "restore_default"
	}
	return human, nil
}

func markdownPatchMatchesLiveValue(patch Patch, baseline Baseline, current string) bool {
	switch patch.Operation {
	case "set":
		return patch.Value != nil && current == *patch.Value
	case "suppress":
		return current == ""
	case "restore_default":
		return baseline.Value != nil && current == *baseline.Value
	default:
		return false
	}
}

func replaceGeneratedMilestoneBaseline(p *Presentation, oldMetadata *markdownEditMetadataIndex, generated Timeline, field string) error {
	semanticKey := markdownSemanticKey("milestone:"+generated.ID, field)
	positions := oldMetadata.baselines[semanticKey]
	if len(positions) != 1 {
		return errors.New("generated milestone baseline is absent or ambiguous")
	}
	position := positions[0]
	value := generatedMilestoneField(generated, field)
	baseline := &p.GeneratedBaselines[position]
	baseline.GenerationID = p.GenerationID
	baseline.Kind, baseline.Value, baseline.Values = "scalar", &value, nil
	baseline.GeneratedHash = baselinehash.SHA256(baseline.EntityID, baseline.Field, baseline.Kind, value, nil)
	for _, patchPosition := range oldMetadata.patches[semanticKey] {
		p.HumanPatches[patchPosition].BaseGeneratedHash = baseline.GeneratedHash
	}
	return nil
}

// AddScanMilestoneBaselines seeds the exact editable scalar baselines for one
// generated milestone before its first Markdown render.
func AddScanMilestoneBaselines(p *Presentation, item Timeline) {
	entity := "milestone:" + item.ID
	for _, field := range markdownMilestoneScalarFields {
		value := generatedMilestoneField(item, field)
		valueCopy := value
		p.GeneratedBaselines = append(p.GeneratedBaselines, Baseline{
			GenerationID: p.GenerationID, EntityID: entity, Field: field, Kind: "scalar", Value: &valueCopy,
			GeneratedHash: baselinehash.SHA256(entity, field, "scalar", value, nil),
		})
	}
}

func generatedMilestoneField(item Timeline, field string) string {
	switch field {
	case "title":
		return item.Title
	case "summary":
		return item.Summary
	case "conclusion":
		return item.ClosedLoop.Conclusion.Text
	case "impact_and_follow_up":
		return item.ClosedLoop.ImpactAndFollowUp.Text
	default:
		return ""
	}
}

func mergeMilestoneDependencies(old, generated []ChainDependency) ([]ChainDependency, error) {
	if len(old) > 65536 || len(generated) > 65536 {
		return nil, errors.New("chain dependencies exceed array limit")
	}
	result := cloneChainDependencies(old)
	bySnapshot := make(map[string]ChainDependency, len(old))
	for _, dependency := range old {
		bySnapshot[milestoneDependencyKey(dependency)] = dependency
	}
	for _, dependency := range generated {
		key := milestoneDependencyKey(dependency)
		if prior, exists := bySnapshot[key]; exists {
			if !markdownDependenciesEqual(prior, dependency) {
				return nil, errors.New("same chain snapshot has conflicting dependency content")
			}
			continue
		}
		if len(result) == 65536 {
			return nil, errors.New("chain dependencies exceed array limit")
		}
		result = append(result, cloneChainDependency(dependency))
		bySnapshot[key] = dependency
	}
	return result, nil
}

func milestoneDependencyKey(value ChainDependency) string {
	return value.Provider + "\x00" + value.SessionID + "\x00" + value.SessionViewDigest
}

func markdownDependenciesEqual(left, right ChainDependency) bool {
	return left.Provider == right.Provider && left.SessionID == right.SessionID && left.SessionViewDigest == right.SessionViewDigest && left.DependencyDigest == right.DependencyDigest && slices.Equal(left.TurnUnitIDs, right.TurnUnitIDs)
}

func markdownPresentationsEqual(left, right Presentation) (bool, error) {
	leftBody, leftErr := strictjson.Encode(left)
	if leftErr != nil {
		return false, leftErr
	}
	rightBody, rightErr := strictjson.Encode(right)
	if rightErr != nil {
		return false, rightErr
	}
	return bytes.Equal(leftBody, rightBody), nil
}

func qualifyPresentationLegacyRefs(p *Presentation, authenticated, combined sourceTurnBindingIndex) error {
	for index := range p.ProblemNodes {
		if err := qualifyLegacyRefs(p.ProblemNodes[index].SourceTurnRefs, authenticated, combined); err != nil {
			return err
		}
	}
	return qualifyTimelineLegacyRefs(p.Timeline, authenticated, combined)
}

func qualifyTimelineLegacyRefs(timeline []Timeline, authenticated, combined sourceTurnBindingIndex) error {
	for index := range timeline {
		loop := &timeline[index].ClosedLoop
		groups := [][]SourceTurnRef{
			loop.SourceTurnRefs, loop.TriggerQuestion.SourceTurnRefs, loop.Conclusion.SourceTurnRefs,
			loop.Execution.SourceTurnRefs, loop.Verification.SourceTurnRefs, loop.ImpactAndFollowUp.SourceTurnRefs,
		}
		for _, refs := range groups {
			if err := qualifyLegacyRefs(refs, authenticated, combined); err != nil {
				return err
			}
		}
	}
	return nil
}

func qualifyLegacyRefs(refs []SourceTurnRef, authenticated, combined sourceTurnBindingIndex) error {
	for index := range refs {
		if refs[index].SessionViewDigest != "" {
			continue
		}
		if err := validateSourceTurnRefShape(refs[index]); err != nil {
			return err
		}
		if _, err := combined.resolve(refs[index]); err == nil {
			continue
		}
		dependency, err := authenticated.resolve(refs[index])
		if err != nil {
			return err
		}
		refs[index].SessionViewDigest = dependency.SessionViewDigest
	}
	return nil
}

func cloneTimeline(value Timeline) Timeline {
	p := Presentation{Timeline: []Timeline{value}}
	return clonePresentation(p).Timeline[0]
}

func cloneTimelines(values []Timeline) []Timeline {
	p := Presentation{Timeline: slices.Clone(values)}
	return clonePresentation(p).Timeline
}

func cloneChainDependency(value ChainDependency) ChainDependency {
	value.TurnUnitIDs = slices.Clone(value.TurnUnitIDs)
	return value
}

func cloneChainDependencies(values []ChainDependency) []ChainDependency {
	result := slices.Clone(values)
	for index := range result {
		result[index] = cloneChainDependency(result[index])
	}
	return result
}

func cloneConclusion(value ClosedLoopConclusion) ClosedLoopConclusion {
	result := value
	result.SourceTurnRefs = slices.Clone(value.SourceTurnRefs)
	if value.MissingReason != nil {
		copy := *value.MissingReason
		result.MissingReason = &copy
	}
	return result
}

func retainMilestoneAggregateRefs(loop *ClosedLoop, retained []SourceTurnRef, bindings sourceTurnBindingIndex) error {
	seen := make(map[string]bool, len(loop.SourceTurnRefs)+len(retained))
	for _, ref := range loop.SourceTurnRefs {
		if err := validateSourceTurnRefShape(ref); err != nil {
			return err
		}
		dependency, err := bindings.resolve(ref)
		if err != nil {
			return err
		}
		seen[canonicalSourceTurnKey(ref, dependency)] = true
	}
	added := uint64(0)
	for _, ref := range retained {
		if err := validateSourceTurnRefShape(ref); err != nil {
			return err
		}
		dependency, err := bindings.resolve(ref)
		if err != nil {
			return err
		}
		key := canonicalSourceTurnKey(ref, dependency)
		if seen[key] {
			continue
		}
		loop.SourceTurnRefs = append(loop.SourceTurnRefs, ref)
		seen[key] = true
		added++
	}
	loop.Coverage.CapturedTurns += added
	loop.Coverage.SourceTurns += added
	return nil
}
