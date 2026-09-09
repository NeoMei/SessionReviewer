package reviewv4

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"reflect"
)

const (
	markdownReviewRelative  = "项目回顾.md"
	markdownHistoryRelative = "项目历史.md"
)

func RenderMarkdown(p Presentation, ledger MachineLedger, previous *MarkdownPair) (MarkdownPair, error) {
	return renderMarkdown(p, ledger, previous, false)
}

// RenderMarkdownDraft renders a pending human draft only after proving the
// supplied presentation is exactly the result of parsing that draft against
// the accepted ledger. It intentionally does not weaken RenderMarkdown's
// accepted-byte hash requirement.
func RenderMarkdownDraft(p Presentation, ledger MachineLedger, draft MarkdownPair) (MarkdownPair, error) {
	parsed, err := ParseMarkdownDraft(draft, ledger)
	if err != nil {
		return MarkdownPair{}, err
	}
	if !reflect.DeepEqual(parsed.Presentation, p) {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownBaselineMissing}
	}
	return renderMarkdown(p, ledger, &draft, true)
}

// RenderMarkdownUpdate proves pending human Markdown against the old accepted
// ledger before allowing a caller-mapped scan identity to update generated
// regions. The caller remains solely responsible for mapping and revision.
func RenderMarkdownUpdate(next Presentation, oldLedger MachineLedger, pending MarkdownPair) (MarkdownPair, error) {
	draft, err := ParseMarkdownDraft(pending, oldLedger)
	if err != nil {
		return MarkdownPair{}, err
	}
	wantRevision := draft.Presentation.Revision
	base := oldLedger.DocumentProjection.PresentationBase
	if draft.Presentation.Revision == base.Revision && (next.GenerationID != base.GenerationID || next.ProjectViewDigest != base.ProjectViewDigest) {
		wantRevision++
	}
	if next.Revision != wantRevision || !samePresentationAcrossScanIdentity(next, draft.Presentation) {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownBaselineMissing}
	}
	return renderMarkdown(next, oldLedger, &pending, true)
}

func samePresentationAcrossScanIdentity(next, prior Presentation) bool {
	if next.ProjectID != prior.ProjectID {
		return false
	}
	expected := clonePresentation(prior)
	if CarryMarkdownGeneratedBaselines(&expected, next.GenerationID) != nil {
		return false
	}
	expected.GenerationID, expected.ProjectViewDigest = next.GenerationID, next.ProjectViewDigest
	expected.Revision = next.Revision
	for i := range expected.Timeline {
		expected.Timeline[i].GenerationID = next.GenerationID
	}
	return reflect.DeepEqual(next, expected)
}

func renderMarkdown(p Presentation, ledger MachineLedger, previous *MarkdownPair, validatedDraft bool) (MarkdownPair, error) {
	if err := ValidatePresentation(p); err != nil {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownFormatInvalid, Cause: err}
	}
	if p.Revision < 1 {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownFormatInvalid}
	}
	if err := validateMarkdownLedger(ledger); err != nil {
		return MarkdownPair{}, err
	}
	base := ledger.DocumentProjection.PresentationBase
	if p.ProjectID != base.ProjectID || p.Revision < base.Revision {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownBaselineMissing}
	}
	desired, err := renderFreshMarkdown(p)
	if err != nil {
		return MarkdownPair{}, err
	}
	if previous == nil {
		return desired, nil
	}
	oldCanonical, err := renderFreshMarkdown(base)
	if err != nil {
		return MarkdownPair{}, err
	}
	mergedReview, err := mergeMarkdownDocument(markdownReviewRelative, previous.Review, oldCanonical.Review, desired.Review, ledger.ReviewSHA256, base, p, validatedDraft)
	if err != nil {
		return MarkdownPair{}, err
	}
	mergedHistory, err := mergeMarkdownDocument(markdownHistoryRelative, previous.History, oldCanonical.History, desired.History, ledger.HistorySHA256, base, p, validatedDraft)
	if err != nil {
		return MarkdownPair{}, err
	}
	return MarkdownPair{Review: mergedReview, History: mergedHistory}, nil
}

func renderFreshMarkdown(p Presentation) (MarkdownPair, error) {
	return renderFreshMarkdownBounded(p, maxMarkdownDocumentBytes)
}

func renderFreshMarkdownBounded(p Presentation, limit int) (MarkdownPair, error) {
	review := newMarkdownBoundedWriter(limit)
	history := newMarkdownBoundedWriter(limit)
	writeMarkdownFrontmatter(review, "review-"+p.ProjectID, "project-review", p)
	review.WriteString("# 项目回顾\n\n## 当前状态\n\n")
	writeMarkdownLabeledField(review, "目标", FieldKey{Entity: "project-overview", Name: "goal"}, p.CurrentState.Goal)
	writeMarkdownLabeledField(review, "阶段", FieldKey{Entity: "project-overview", Name: "stage"}, p.CurrentState.Stage)
	writeMarkdownLabeledField(review, "状态", FieldKey{Entity: "project-overview", Name: "status"}, p.CurrentState.Status)
	writeMarkdownLabeledField(review, "下一步", FieldKey{Entity: "project-overview", Name: "next_action"}, p.CurrentState.NextAction)
	writeMarkdownLabeledField(review, "上次验证", FieldKey{Entity: "project-overview", Name: "last_verification"}, p.CurrentState.LastVerification)
	review.WriteString("\n## 问题概览\n")
	writeMarkdownGeneratedStart(review, FieldKey{Entity: "project-overview", Name: "problem-tree"})
	renderProblemTree(review, p)
	writeMarkdownGeneratedEnd(review, FieldKey{Entity: "project-overview", Name: "problem-tree"})
	review.WriteString("\n## 置顶决策\n")
	writeMarkdownGeneratedStart(review, FieldKey{Entity: "project-overview", Name: "pinned-decisions"})
	renderPinnedDecisions(review, p)
	writeMarkdownGeneratedEnd(review, FieldKey{Entity: "project-overview", Name: "pinned-decisions"})
	review.WriteString("\n## 近期里程碑\n")
	writeMarkdownGeneratedStart(review, FieldKey{Entity: "project-overview", Name: "recent-milestones"})
	renderRecentMilestones(review, p)
	writeMarkdownGeneratedEnd(review, FieldKey{Entity: "project-overview", Name: "recent-milestones"})
	renderReviewEntities(review, p)
	if review.Err() != nil {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownFormatInvalid}
	}

	writeMarkdownFrontmatter(history, "history-"+p.ProjectID, "project-history", p)
	history.WriteString("# 项目历史\n\n## 里程碑\n")
	if len(p.Timeline) == 0 {
		history.WriteString("\n已接受的里程碑（如有）列于下方。\n")
	}
	for _, item := range p.Timeline {
		if history.Err() != nil {
			break
		}
		fmt.Fprintf(history, "\n<a id=\"milestone-%s\"></a>\n\n", markdownAnchorID(item.ID))
		writeMarkdownLabeledField(history, "标题", FieldKey{Entity: "milestone:" + item.ID, Name: "title"}, item.Title)
		writeMarkdownLabeledField(history, "摘要", FieldKey{Entity: "milestone:" + item.ID, Name: "summary"}, item.Summary)
		writeMarkdownLabeledField(history, "结论", FieldKey{Entity: "milestone:" + item.ID, Name: "conclusion"}, item.ClosedLoop.Conclusion.Text)
		writeMarkdownLabeledField(history, "影响与后续", FieldKey{Entity: "milestone:" + item.ID, Name: "impact_and_follow_up"}, item.ClosedLoop.ImpactAndFollowUp.Text)
		history.WriteString("\n### 触发、执行、验证与 Coverage\n")
		key := FieldKey{Entity: "milestone:" + item.ID, Name: "evidence"}
		writeMarkdownGeneratedStart(history, key)
		renderMilestoneEvidence(history, item)
		writeMarkdownGeneratedEnd(history, key)
	}
	reviewBody, reviewErr := review.take()
	historyBody, historyErr := history.take()
	if reviewErr != nil || historyErr != nil {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownFormatInvalid}
	}
	return MarkdownPair{Review: reviewBody, History: historyBody}, nil
}

func renderReviewEntities(review *markdownBoundedWriter, p Presentation) {
	review.WriteString("\n## 决策\n")
	if len(p.Decisions) == 0 {
		review.WriteString("\n已接受的决策（如有）列于下方。\n")
	}
	for _, item := range p.Decisions {
		if review.Err() != nil {
			return
		}
		fmt.Fprintf(review, "\n<a id=\"decision-%s\"></a>\n\n", markdownAnchorID(item.ID))
		writeMarkdownLabeledField(review, "标题", FieldKey{Entity: "decision:" + item.ID, Name: "title"}, item.Title)
		writeMarkdownLabeledField(review, "理由", FieldKey{Entity: "decision:" + item.ID, Name: "rationale"}, item.Rationale)
		writeMarkdownLabeledField(review, "影响", FieldKey{Entity: "decision:" + item.ID, Name: "impact"}, item.Impact)
		writeMarkdownLabeledField(review, "重新评估条件", FieldKey{Entity: "decision:" + item.ID, Name: "reevaluate_when"}, item.ReevaluateWhen)
	}
	review.WriteString("\n## 风险\n")
	if len(p.Risks) == 0 {
		review.WriteString("\n已识别的风险（如有）列于下方。\n")
	}
	for _, item := range p.Risks {
		if review.Err() != nil {
			return
		}
		fmt.Fprintf(review, "\n<a id=\"risk-%s\"></a>\n\n", markdownAnchorID(item.ID))
		writeMarkdownLabeledField(review, "标题", FieldKey{Entity: "risk:" + item.ID, Name: "title"}, item.Title)
		writeMarkdownLabeledField(review, "详情", FieldKey{Entity: "risk:" + item.ID, Name: "detail"}, item.Detail)
		writeMarkdownLabeledField(review, "状态", FieldKey{Entity: "risk:" + item.ID, Name: "status"}, item.Status)
	}
	review.WriteString("\n## 未决问题\n")
	if len(p.OpenLoops) == 0 {
		review.WriteString("\n未决问题（如有）列于下方。\n")
	}
	for _, item := range p.OpenLoops {
		if review.Err() != nil {
			return
		}
		fmt.Fprintf(review, "\n<a id=\"open-loop-%s\"></a>\n\n", markdownAnchorID(item.ID))
		writeMarkdownLabeledField(review, "标题", FieldKey{Entity: "open-loop:" + item.ID, Name: "title"}, item.Title)
		writeMarkdownLabeledField(review, "问题", FieldKey{Entity: "open-loop:" + item.ID, Name: "question"}, item.Question)
		writeMarkdownLabeledField(review, "下一个实验", FieldKey{Entity: "open-loop:" + item.ID, Name: "next_experiment"}, item.NextExperiment)
		writeMarkdownLabeledField(review, "完成标准", FieldKey{Entity: "open-loop:" + item.ID, Name: "completion_criterion"}, item.CompletionCriterion)
		writeMarkdownLabeledField(review, "状态", FieldKey{Entity: "open-loop:" + item.ID, Name: "status"}, item.Status)
	}
	review.WriteString("\n## 正式问题\n")
	if len(p.ProblemNodes) == 0 {
		review.WriteString("\n正式问题（如有）列于下方。\n")
	}
	for _, item := range p.ProblemNodes {
		if review.Err() != nil {
			return
		}
		fmt.Fprintf(review, "\n<a id=\"problem-%s\"></a>\n\n", markdownAnchorID(item.ID))
		writeMarkdownLabeledField(review, "问题", FieldKey{Entity: "problem:" + item.ID, Name: "question"}, item.Question)
		writeMarkdownLabeledField(review, "完成标准", FieldKey{Entity: "problem:" + item.ID, Name: "completion_criterion"}, item.CompletionCriterion)
		writeMarkdownLabeledField(review, "当前结论", FieldKey{Entity: "problem:" + item.ID, Name: "current_conclusion"}, item.CurrentConclusion)
	}
}

func writeMarkdownFrontmatter(out *markdownBoundedWriter, id, entityType string, p Presentation) {
	out.WriteString("---\n")
	capability := markdownCapability(p)
	fmt.Fprintf(out, "id: %s\nentity_type: %s\nproject_id: %s\nschema_version: 4\ndocument_format: review-markdown-v1\nrevision: %d\ngeneration_id: %s\nminimum_reader_version: %s\nminimum_writer_version: %s\n---\n", id, entityType, p.ProjectID, p.Revision, p.GenerationID, capability, capability)
}

func writeMarkdownLabeledField(out *markdownBoundedWriter, label string, key FieldKey, value string) {
	out.WriteString("### " + label + "\n")
	writeMarkdownField(out, key, value)
}
func writeMarkdownField(out *markdownBoundedWriter, key FieldKey, value string) {
	writeMarkdownBlock(out, "field", key, value)
}
func writeMarkdownGeneratedStart(out *markdownBoundedWriter, key FieldKey) {
	fmt.Fprintf(out, "<!-- session-reviewer:v4-generated entity=\"%s\" name=\"%s\" -->\n", key.Entity, key.Name)
}
func writeMarkdownGeneratedEnd(out *markdownBoundedWriter, key FieldKey) {
	out.WriteByte('\n')
	fmt.Fprintf(out, "<!-- /session-reviewer:v4-generated entity=\"%s\" name=\"%s\" -->\n", key.Entity, key.Name)
}
func writeMarkdownBlock(out *markdownBoundedWriter, kind string, key FieldKey, value string) {
	fmt.Fprintf(out, "<!-- session-reviewer:v4-%s entity=\"%s\" name=\"%s\" -->\n", kind, key.Entity, key.Name)
	out.WriteString(value)
	out.WriteByte('\n')
	fmt.Fprintf(out, "<!-- /session-reviewer:v4-%s entity=\"%s\" name=\"%s\" -->\n", kind, key.Entity, key.Name)
}

func renderProblemTree(out *markdownBoundedWriter, p Presentation) {
	if len(p.ProblemNodes) == 0 {
		out.WriteString("- 暂无正式问题")
		return
	}
	byParent := make(map[string][]int, len(p.ProblemNodes))
	for index, item := range p.ProblemNodes {
		parent := ""
		if item.PrimaryParentID != nil {
			parent = *item.PrimaryParentID
		}
		byParent[parent] = append(byParent[parent], index)
	}
	type pendingNode struct{ index, depth int }
	stack := make([]pendingNode, 0, len(p.ProblemNodes))
	for index := len(byParent[""]) - 1; index >= 0; index-- {
		stack = append(stack, pendingNode{index: byParent[""][index]})
	}
	first := true
	for len(stack) > 0 && out.Err() == nil {
		pending := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		item := p.ProblemNodes[pending.index]
		if !first {
			out.WriteByte('\n')
		}
		first = false
		if err := out.WriteRepeat("  ", pending.depth); err != nil {
			return
		}
		fmt.Fprintf(out, "- [正式问题](#problem-%s)", markdownAnchorID(item.ID))
		children := byParent[item.ID]
		for index := len(children) - 1; index >= 0; index-- {
			stack = append(stack, pendingNode{index: children[index], depth: pending.depth + 1})
		}
	}
}

func renderPinnedDecisions(out *markdownBoundedWriter, p Presentation) {
	first := true
	for _, item := range p.Decisions {
		if out.Err() != nil {
			return
		}
		if item.Pinned {
			if !first {
				out.WriteByte('\n')
			}
			first = false
			fmt.Fprintf(out, "- [决策](#decision-%s)", markdownAnchorID(item.ID))
		}
	}
	if first {
		out.WriteString("- 暂无置顶决策")
	}
}

func renderRecentMilestones(out *markdownBoundedWriter, p Presentation) {
	count := len(p.Timeline)
	displayed := count
	if displayed > 5 {
		displayed = 5
	}
	fmt.Fprintf(out, "共 %d 条，显示 %d 条；[查看完整历史](项目历史.md)。", count, displayed)
	for index := count - displayed; index < count; index++ {
		if index >= 0 {
			fmt.Fprintf(out, "\n- [里程碑](项目历史.md#milestone-%s)", markdownAnchorID(p.Timeline[index].ID))
		}
	}
}

func renderMilestoneEvidence(out *markdownBoundedWriter, item Timeline) {
	first := true
	writeEvidenceLine(out, &first, "- 发生时间：", item.OccurredAt)
	writeEvidenceLine(out, &first, "- 类型：", item.Kind)
	writeSegmentEvidence(out, &first, "触发", item.ClosedLoop.TriggerQuestion)
	writeEvidenceLine(out, &first, "- 结论来源：", string(item.ClosedLoop.Conclusion.Kind))
	writeSourceRefs(out, &first, "结论引用", item.ClosedLoop.Conclusion.SourceTurnRefs)
	writeSegmentEvidence(out, &first, "执行", item.ClosedLoop.Execution)
	writeSegmentEvidence(out, &first, "验证", item.ClosedLoop.Verification)
	writeEvidenceLine(out, &first, "- 影响/后续状态：", item.ClosedLoop.ImpactAndFollowUp.State)
	writeSourceRefs(out, &first, "影响/后续引用", item.ClosedLoop.ImpactAndFollowUp.SourceTurnRefs)
	coverage := item.ClosedLoop.Coverage
	writeEvidencePrefix(out, &first)
	fmt.Fprintf(out, "- Coverage：source=%d, captured=%d, truncated=%d, unavailable=%d", coverage.SourceTurns, coverage.CapturedTurns, coverage.TruncatedTurns, coverage.SourceUnavailableTurns)
	writeSourceRefs(out, &first, "全部引用", item.ClosedLoop.SourceTurnRefs)
	writeEvidencePrefix(out, &first)
	if len(item.DecisionIDs) == 0 {
		out.WriteString("- 关联决策：无")
	} else {
		out.WriteString("- 关联决策：")
		for index, id := range item.DecisionIDs {
			if index > 0 {
				out.WriteString(", ")
			}
			out.WriteString(id)
		}
	}
}

func writeSegmentEvidence(out *markdownBoundedWriter, first *bool, label string, segment ClosedLoopSegment) {
	writeEvidenceLine(out, first, "- "+label+"状态：", segment.State)
	if segment.Text != "" {
		writeEvidencePrefix(out, first)
		out.WriteString("  - 文本：")
		writeMarkdownIndented(out, segment.Text)
	}
	if segment.MissingReason != nil {
		writeEvidenceLine(out, first, "  - 缺失原因：", *segment.MissingReason)
	}
	writeSourceRefs(out, first, label+"引用", segment.SourceTurnRefs)
}

func writeSourceRefs(out *markdownBoundedWriter, first *bool, label string, refs []SourceTurnRef) {
	if len(refs) == 0 {
		writeEvidenceLine(out, first, "- "+label+"：", "无")
		return
	}
	for _, ref := range refs {
		writeEvidencePrefix(out, first)
		fmt.Fprintf(out, "- %s：%s/%s", label, ref.Provider, ref.SessionID)
		if ref.SessionViewDigest != "" {
			fmt.Fprintf(out, "@%s", ref.SessionViewDigest)
		}
		fmt.Fprintf(out, "#%s", ref.TurnUnitID)
	}
}

func markdownCapability(p Presentation) string {
	return documentProjectionCapability(p)
}

func writeEvidenceLine(out *markdownBoundedWriter, first *bool, prefix, value string) {
	writeEvidencePrefix(out, first)
	out.WriteString(prefix)
	out.WriteString(value)
}

func writeEvidencePrefix(out *markdownBoundedWriter, first *bool) {
	if !*first {
		out.WriteByte('\n')
	}
	*first = false
}

func writeMarkdownIndented(out *markdownBoundedWriter, value string) {
	start := 0
	for index := 0; index < len(value); index++ {
		if value[index] != '\n' {
			continue
		}
		out.WriteString(value[start:index])
		out.WriteString("\n    ")
		start = index + 1
	}
	out.WriteString(value[start:])
}

func markdownAnchorID(id string) string {
	return "x" + hex.EncodeToString([]byte(id))
}

func mergeMarkdownDocument(relative string, previous, oldCanonical, desired []byte, acceptedHash string, base, next Presentation, validatedDraft bool) ([]byte, error) {
	oldDocument, err := ParseMarkdownDocument(relative, previous)
	if err != nil {
		return nil, err
	}
	if err := validateMarkdownFrontmatterIdentity(relative, previous, base); err != nil {
		return nil, err
	}
	oldExpected, err := ParseMarkdownDocument(relative, oldCanonical)
	if err != nil {
		return nil, err
	}
	newExpected, err := ParseMarkdownDocument(relative, desired)
	if err != nil {
		return nil, err
	}
	if err := validateMarkdownStructure(oldDocument, oldExpected); err != nil {
		return nil, err
	}
	if err := validateMarkdownAnchors(relative, oldDocument.raw, oldExpected.raw); err != nil {
		return nil, err
	}
	oldIndex, err := newMarkdownDocumentIndex(relative, oldDocument)
	if err != nil {
		return nil, err
	}
	oldExpectedIndex, err := newMarkdownDocumentIndex(relative, oldExpected)
	if err != nil {
		return nil, err
	}
	newKeys := make(map[FieldKey]bool, len(newExpected.blocks))
	for _, block := range newExpected.blocks {
		newKeys[block.Key] = true
	}
	for _, block := range oldDocument.blocks {
		if !newKeys[block.Key] {
			return nil, &MarkdownError{Code: MarkdownStructureEditRequiresCommand, Relative: relative, Entity: block.Key.Entity, Field: block.Key.Name}
		}
		if !block.Generated {
			continue
		}
		oldBlock := oldExpectedIndex.blocks[block.Key]
		if !markdownSemanticEqual(markdownBlockValue(oldDocument, block), markdownBlockValue(oldExpected, oldBlock)) {
			return nil, &MarkdownError{Code: MarkdownGeneratedRegionModified, Relative: relative, Entity: block.Key.Entity, Field: block.Key.Name}
		}
	}
	if !validatedDraft && sha256Hex(previous) != acceptedHash {
		return nil, &MarkdownError{Code: MarkdownBaselineMissing, Relative: relative}
	}
	replacements := make(map[FieldKey]string, len(newExpected.blocks))
	for _, block := range newExpected.blocks {
		desiredValue := markdownBlockValue(newExpected, block)
		oldBlock, oldExists := oldIndex.blocks[block.Key]
		oldExpectedBlock, expectedExists := oldExpectedIndex.blocks[block.Key]
		if oldExists && expectedExists && markdownSemanticEqual(desiredValue, markdownBlockValue(oldExpected, oldExpectedBlock)) {
			replacements[block.Key] = markdownBlockValue(oldDocument, oldBlock)
			continue
		}
		replacements[block.Key] = desiredValue
	}
	merged, err := replaceMarkdownBlocks(oldDocument, replacements)
	if err != nil {
		return nil, err
	}
	if len(newExpected.blocks) > len(oldDocument.blocks) {
		merged, err = appendNewMarkdownEntities(relative, merged, desired, oldDocument, newExpected)
		if err != nil {
			return nil, err
		}
	}
	return replaceMarkdownFrontmatterBindings(merged, next)
}

func appendNewMarkdownEntities(relative string, merged, desired []byte, oldDocument, newExpected MarkdownDocument) ([]byte, error) {
	oldEntities := make(map[string]bool, len(oldDocument.blocks))
	for _, block := range oldDocument.blocks {
		if block.Key.Entity != "project-overview" {
			oldEntities[block.Key.Entity] = true
		}
	}
	seen := make(map[string]bool)
	seenKind := make(map[string]bool)
	separatorBytes := 0
	if len(merged) > 0 && merged[len(merged)-1] != '\n' {
		separatorBytes = 1
	}
	if len(merged) > maxMarkdownDocumentBytes-separatorBytes {
		return nil, &MarkdownError{Code: MarkdownFormatInvalid, Relative: relative}
	}
	additions := newMarkdownBoundedWriter(maxMarkdownDocumentBytes - len(merged) - separatorBytes)
	for index, block := range newExpected.blocks {
		entity := block.Key.Entity
		if entity == "project-overview" || oldEntities[entity] || seen[entity] {
			continue
		}
		seen[entity] = true
		start := newEntityAnchorStart(newExpected, block)
		last := index
		for next := index + 1; next < len(newExpected.blocks) && newExpected.blocks[next].Key.Entity == entity; next++ {
			last = next
		}
		end := newExpected.blocks[last].End
		if start < 0 || start >= end || end > len(desired) {
			return nil, &MarkdownError{Code: MarkdownFormatInvalid, Relative: relative, Entity: entity}
		}
		kind, _, _ := splitMarkdownEntity(entity)
		heading := ""
		if !seenKind[kind] {
			heading = markdownNewEntityHeading(kind)
			seenKind[kind] = true
		}
		additions.WriteString(heading)
		additions.Write(desired[start:end])
		if additions.Err() != nil {
			return nil, &MarkdownError{Code: MarkdownFormatInvalid, Relative: relative, Entity: entity}
		}
	}
	additionBytes, err := additions.take()
	if err != nil {
		return nil, err
	}
	if len(additionBytes) == 0 {
		return merged, nil
	}
	result := newMarkdownBoundedWriter(maxMarkdownDocumentBytes)
	result.Write(merged)
	if separatorBytes != 0 {
		result.WriteByte('\n')
	}
	result.Write(additionBytes)
	body, err := result.take()
	if err != nil {
		return nil, &MarkdownError{Code: MarkdownFormatInvalid, Relative: relative}
	}
	return body, nil
}

func markdownNewEntityHeading(kind string) string {
	switch kind {
	case "decision":
		return "\n## 新增决策\n\n"
	case "risk":
		return "\n## 新增风险\n\n"
	case "open-loop":
		return "\n## 新增未决问题\n\n"
	case "problem":
		return "\n## 新增正式问题\n\n"
	case "milestone":
		return "\n## 新增里程碑\n\n"
	default:
		return ""
	}
}

func newEntityAnchorStart(document MarkdownDocument, block MarkdownBlock) int {
	lineStart := block.Start
	for lineStart > 0 {
		previousEnd := lineStart - 1
		if previousEnd > 0 && document.raw[previousEnd-1] == '\r' {
			previousEnd--
		}
		previousStart := previousEnd
		for previousStart > 0 && document.raw[previousStart-1] != '\n' {
			previousStart--
		}
		line := document.raw[previousStart:previousEnd]
		if bytes.HasPrefix(line, []byte(`<a id="`)) && bytes.HasSuffix(line, []byte(`"></a>`)) {
			return previousStart
		}
		if bytes.HasPrefix(line, []byte("### ")) {
			lineStart = previousStart
			continue
		}
		if len(bytes.TrimSpace(line)) != 0 {
			return -1
		}
		lineStart = previousStart
	}
	return -1
}

func replaceMarkdownBlocks(document MarkdownDocument, replacements map[FieldKey]string) ([]byte, error) {
	return replaceMarkdownBlocksBounded(document, replacements, maxMarkdownDocumentBytes)
}

func replaceMarkdownBlocksBounded(document MarkdownDocument, replacements map[FieldKey]string, limit int) ([]byte, error) {
	removedBytes, replacementBytes := 0, 0
	for _, block := range document.blocks {
		value, exists := replacements[block.Key]
		if !exists {
			return nil, &MarkdownError{Code: MarkdownFieldMissing, Entity: block.Key.Entity, Field: block.Key.Name}
		}
		removedBytes += block.ValueEnd - block.ValueStart
		if len(value) > limit-replacementBytes {
			return nil, &MarkdownError{Code: MarkdownFormatInvalid}
		}
		replacementBytes += len(value)
	}
	prospective := len(document.raw) - removedBytes
	if prospective > limit-replacementBytes {
		return nil, &MarkdownError{Code: MarkdownFormatInvalid}
	}
	prospective += replacementBytes
	out := newMarkdownBoundedWriter(limit)
	cursor := 0
	for _, block := range document.blocks {
		value, exists := replacements[block.Key]
		if !exists {
			return nil, &MarkdownError{Code: MarkdownFieldMissing, Entity: block.Key.Entity, Field: block.Key.Name}
		}
		out.Write(document.raw[cursor:block.ValueStart])
		out.WriteString(value)
		cursor = block.ValueEnd
	}
	out.Write(document.raw[cursor:])
	return out.take()
}

func validateMarkdownAnchors(relative string, actual, expected []byte) error {
	actualDocument := MarkdownDocument{raw: actual}
	expectedDocument := MarkdownDocument{raw: expected}
	actualIndex, err := newMarkdownDocumentIndex(relative, actualDocument)
	if err != nil {
		return err
	}
	expectedIndex, err := newMarkdownDocumentIndex(relative, expectedDocument)
	if err != nil {
		return err
	}
	for anchor, count := range expectedIndex.anchors {
		if count != 1 || actualIndex.anchors[anchor] != 1 {
			return &MarkdownError{Code: MarkdownStructureEditRequiresCommand, Relative: relative}
		}
	}
	return nil
}

func replaceMarkdownFrontmatterBindings(raw []byte, next Presentation) ([]byte, error) {
	mapping, err := markdownFrontmatter(raw)
	if err != nil {
		return nil, &MarkdownError{Code: MarkdownFormatInvalid}
	}
	if _, err := validateMarkdownFrontmatter(mapping); err != nil {
		return nil, &MarkdownError{Code: MarkdownFormatInvalid}
	}
	type bindingReplacement struct {
		start, end int
		value      string
	}
	replacements := make([]bindingReplacement, 0, 4)
	for index := 0; index < len(mapping.Content); index += 2 {
		key, node := mapping.Content[index].Value, mapping.Content[index+1]
		value, changed := "", false
		switch key {
		case "revision":
			old, ok := markdownInteger(node)
			if !ok {
				return nil, &MarkdownError{Code: MarkdownFormatInvalid}
			}
			value, changed = fmt.Sprint(next.Revision), old != next.Revision
		case "generation_id":
			old, ok := markdownString(node)
			if !ok {
				return nil, &MarkdownError{Code: MarkdownFormatInvalid}
			}
			value, changed = next.GenerationID, old != next.GenerationID
		case "minimum_reader_version", "minimum_writer_version":
			old, ok := markdownString(node)
			if !ok {
				return nil, &MarkdownError{Code: MarkdownFormatInvalid}
			}
			value, changed = markdownCapability(next), old != markdownCapability(next)
		default:
			continue
		}
		if !changed {
			continue
		}
		start, end, spanErr := markdownScalarValueSpan(raw, node)
		if spanErr != nil || start < 0 || end < start || end > len(raw) {
			return nil, &MarkdownError{Code: MarkdownFormatInvalid}
		}
		replacements = append(replacements, bindingReplacement{start: start, end: end, value: value})
	}
	if len(replacements) == 0 {
		return bytes.Clone(raw), nil
	}
	if len(replacements) > 4 {
		return nil, &MarkdownError{Code: MarkdownFormatInvalid}
	}
	for index := 1; index < len(replacements); index++ {
		if replacements[index-1].start >= replacements[index].start {
			return nil, &MarkdownError{Code: MarkdownFormatInvalid}
		}
	}
	removed, added := 0, 0
	for _, replacement := range replacements {
		removed += replacement.end - replacement.start
		if len(replacement.value) > maxMarkdownDocumentBytes-added {
			return nil, &MarkdownError{Code: MarkdownFormatInvalid}
		}
		added += len(replacement.value)
	}
	prospective := len(raw) - removed
	if prospective > maxMarkdownDocumentBytes-added {
		return nil, &MarkdownError{Code: MarkdownFormatInvalid}
	}
	out := newMarkdownBoundedWriter(prospective + added)
	cursor := 0
	for _, replacement := range replacements {
		out.Write(raw[cursor:replacement.start])
		out.WriteString(replacement.value)
		cursor = replacement.end
	}
	out.Write(raw[cursor:])
	body, err := out.take()
	if err != nil {
		return nil, &MarkdownError{Code: MarkdownFormatInvalid}
	}
	updated, err := markdownFrontmatter(body)
	if err != nil {
		return nil, &MarkdownError{Code: MarkdownFormatInvalid}
	}
	if _, err := validateMarkdownFrontmatter(updated); err != nil {
		return nil, &MarkdownError{Code: MarkdownFormatInvalid}
	}
	revisionMatches, generationMatches := false, false
	for index := 0; index < len(updated.Content); index += 2 {
		switch updated.Content[index].Value {
		case "revision":
			revision, ok := markdownInteger(updated.Content[index+1])
			revisionMatches = ok && revision == next.Revision
		case "generation_id":
			generation, ok := markdownString(updated.Content[index+1])
			generationMatches = ok && generation == next.GenerationID
		}
	}
	if !revisionMatches || !generationMatches {
		return nil, &MarkdownError{Code: MarkdownFormatInvalid}
	}
	return body, nil
}
