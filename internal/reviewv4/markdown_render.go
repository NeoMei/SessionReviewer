package reviewv4

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

const (
	markdownReviewRelative  = "项目回顾.md"
	markdownHistoryRelative = "项目历史.md"
)

func RenderMarkdown(p Presentation, ledger MachineLedger, previous *MarkdownPair) (MarkdownPair, error) {
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
	mergedReview, err := mergeMarkdownDocument(markdownReviewRelative, previous.Review, oldCanonical.Review, desired.Review, ledger.ReviewSHA256, base, p)
	if err != nil {
		return MarkdownPair{}, err
	}
	mergedHistory, err := mergeMarkdownDocument(markdownHistoryRelative, previous.History, oldCanonical.History, desired.History, ledger.HistorySHA256, base, p)
	if err != nil {
		return MarkdownPair{}, err
	}
	return MarkdownPair{Review: mergedReview, History: mergedHistory}, nil
}

func renderFreshMarkdown(p Presentation) (MarkdownPair, error) {
	var review, history bytes.Buffer
	writeMarkdownFrontmatter(&review, "review-"+p.ProjectID, "project-review", p)
	review.WriteString("# 项目回顾\n\n## 当前状态\n\n")
	writeMarkdownField(&review, FieldKey{Entity: "project-overview", Name: "goal"}, p.CurrentState.Goal)
	writeMarkdownField(&review, FieldKey{Entity: "project-overview", Name: "stage"}, p.CurrentState.Stage)
	writeMarkdownField(&review, FieldKey{Entity: "project-overview", Name: "status"}, p.CurrentState.Status)
	writeMarkdownField(&review, FieldKey{Entity: "project-overview", Name: "next_action"}, p.CurrentState.NextAction)
	writeMarkdownField(&review, FieldKey{Entity: "project-overview", Name: "last_verification"}, p.CurrentState.LastVerification)
	review.WriteString("\n## 问题概览\n")
	writeMarkdownGenerated(&review, FieldKey{Entity: "project-overview", Name: "problem-tree"}, renderProblemTree(p))
	review.WriteString("\n## 置顶决策\n")
	writeMarkdownGenerated(&review, FieldKey{Entity: "project-overview", Name: "pinned-decisions"}, renderPinnedDecisions(p))
	review.WriteString("\n## 近期里程碑\n")
	writeMarkdownGenerated(&review, FieldKey{Entity: "project-overview", Name: "recent-milestones"}, renderRecentMilestones(p))
	renderReviewEntities(&review, p)

	writeMarkdownFrontmatter(&history, "history-"+p.ProjectID, "project-history", p)
	history.WriteString("# 项目历史\n\n## 里程碑\n")
	if len(p.Timeline) == 0 {
		history.WriteString("\n暂无里程碑。\n")
	}
	for _, item := range p.Timeline {
		fmt.Fprintf(&history, "\n<a id=\"milestone-%s\"></a>\n\n", markdownAnchorID(item.ID))
		writeMarkdownField(&history, FieldKey{Entity: "milestone:" + item.ID, Name: "title"}, item.Title)
		history.WriteString("\n### 摘要\n")
		writeMarkdownField(&history, FieldKey{Entity: "milestone:" + item.ID, Name: "summary"}, item.Summary)
		history.WriteString("\n### 结论\n")
		writeMarkdownField(&history, FieldKey{Entity: "milestone:" + item.ID, Name: "conclusion"}, item.ClosedLoop.Conclusion.Text)
		history.WriteString("\n### 影响与后续\n")
		writeMarkdownField(&history, FieldKey{Entity: "milestone:" + item.ID, Name: "impact_and_follow_up"}, item.ClosedLoop.ImpactAndFollowUp.Text)
		history.WriteString("\n### 触发、执行、验证与 Coverage\n")
		writeMarkdownGenerated(&history, FieldKey{Entity: "milestone:" + item.ID, Name: "evidence"}, renderMilestoneEvidence(item))
	}
	if review.Len() > maxMarkdownDocumentBytes || history.Len() > maxMarkdownDocumentBytes {
		return MarkdownPair{}, &MarkdownError{Code: MarkdownFormatInvalid}
	}
	return MarkdownPair{Review: bytes.Clone(review.Bytes()), History: bytes.Clone(history.Bytes())}, nil
}

func renderReviewEntities(review *bytes.Buffer, p Presentation) {
	review.WriteString("\n## 决策\n")
	if len(p.Decisions) == 0 {
		review.WriteString("\n暂无决策。\n")
	}
	for _, item := range p.Decisions {
		fmt.Fprintf(review, "\n<a id=\"decision-%s\"></a>\n\n", markdownAnchorID(item.ID))
		writeMarkdownField(review, FieldKey{Entity: "decision:" + item.ID, Name: "title"}, item.Title)
		writeMarkdownField(review, FieldKey{Entity: "decision:" + item.ID, Name: "rationale"}, item.Rationale)
		writeMarkdownField(review, FieldKey{Entity: "decision:" + item.ID, Name: "impact"}, item.Impact)
		writeMarkdownField(review, FieldKey{Entity: "decision:" + item.ID, Name: "reevaluate_when"}, item.ReevaluateWhen)
	}
	review.WriteString("\n## 风险\n")
	if len(p.Risks) == 0 {
		review.WriteString("\n暂无风险。\n")
	}
	for _, item := range p.Risks {
		fmt.Fprintf(review, "\n<a id=\"risk-%s\"></a>\n\n", markdownAnchorID(item.ID))
		writeMarkdownField(review, FieldKey{Entity: "risk:" + item.ID, Name: "title"}, item.Title)
		writeMarkdownField(review, FieldKey{Entity: "risk:" + item.ID, Name: "detail"}, item.Detail)
		writeMarkdownField(review, FieldKey{Entity: "risk:" + item.ID, Name: "status"}, item.Status)
	}
	review.WriteString("\n## 未决问题\n")
	if len(p.OpenLoops) == 0 {
		review.WriteString("\n暂无未决问题。\n")
	}
	for _, item := range p.OpenLoops {
		fmt.Fprintf(review, "\n<a id=\"open-loop-%s\"></a>\n\n", markdownAnchorID(item.ID))
		writeMarkdownField(review, FieldKey{Entity: "open-loop:" + item.ID, Name: "title"}, item.Title)
		writeMarkdownField(review, FieldKey{Entity: "open-loop:" + item.ID, Name: "question"}, item.Question)
		writeMarkdownField(review, FieldKey{Entity: "open-loop:" + item.ID, Name: "next_experiment"}, item.NextExperiment)
		writeMarkdownField(review, FieldKey{Entity: "open-loop:" + item.ID, Name: "completion_criterion"}, item.CompletionCriterion)
		writeMarkdownField(review, FieldKey{Entity: "open-loop:" + item.ID, Name: "status"}, item.Status)
	}
	review.WriteString("\n## 正式问题\n")
	if len(p.ProblemNodes) == 0 {
		review.WriteString("\n暂无正式问题。\n")
	}
	for _, item := range p.ProblemNodes {
		fmt.Fprintf(review, "\n<a id=\"problem-%s\"></a>\n\n", markdownAnchorID(item.ID))
		writeMarkdownField(review, FieldKey{Entity: "problem:" + item.ID, Name: "question"}, item.Question)
		writeMarkdownField(review, FieldKey{Entity: "problem:" + item.ID, Name: "completion_criterion"}, item.CompletionCriterion)
		writeMarkdownField(review, FieldKey{Entity: "problem:" + item.ID, Name: "current_conclusion"}, item.CurrentConclusion)
	}
}

func writeMarkdownFrontmatter(out *bytes.Buffer, id, entityType string, p Presentation) {
	out.WriteString("---\n")
	fmt.Fprintf(out, "id: %s\nentity_type: %s\nproject_id: %s\nschema_version: 4\ndocument_format: review-markdown-v1\nrevision: %d\ngeneration_id: %s\nminimum_reader_version: 0.4.1\nminimum_writer_version: 0.4.1\n---\n", id, entityType, p.ProjectID, p.Revision, p.GenerationID)
}

func writeMarkdownField(out *bytes.Buffer, key FieldKey, value string) {
	writeMarkdownBlock(out, "field", key, value)
}
func writeMarkdownGenerated(out *bytes.Buffer, key FieldKey, value string) {
	writeMarkdownBlock(out, "generated", key, value)
}
func writeMarkdownBlock(out *bytes.Buffer, kind string, key FieldKey, value string) {
	fmt.Fprintf(out, "<!-- session-reviewer:v4-%s entity=\"%s\" name=\"%s\" -->\n", kind, key.Entity, key.Name)
	out.WriteString(value)
	out.WriteByte('\n')
	fmt.Fprintf(out, "<!-- /session-reviewer:v4-%s entity=\"%s\" name=\"%s\" -->\n", kind, key.Entity, key.Name)
}

func renderProblemTree(p Presentation) string {
	if len(p.ProblemNodes) == 0 {
		return "- 暂无正式问题"
	}
	byParent := make(map[string][]ProblemNode, len(p.ProblemNodes))
	for _, item := range p.ProblemNodes {
		parent := ""
		if item.PrimaryParentID != nil {
			parent = *item.PrimaryParentID
		}
		byParent[parent] = append(byParent[parent], item)
	}
	var out strings.Builder
	var visit func(string, int)
	visit = func(parent string, depth int) {
		for _, item := range byParent[parent] {
			out.WriteString(strings.Repeat("  ", depth))
			fmt.Fprintf(&out, "- [正式问题](#problem-%s)\n", markdownAnchorID(item.ID))
			visit(item.ID, depth+1)
		}
	}
	visit("", 0)
	return strings.TrimSuffix(out.String(), "\n")
}

func renderPinnedDecisions(p Presentation) string {
	var out strings.Builder
	for _, item := range p.Decisions {
		if item.Pinned {
			fmt.Fprintf(&out, "- [决策](#decision-%s)\n", markdownAnchorID(item.ID))
		}
	}
	if out.Len() == 0 {
		return "- 暂无置顶决策"
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func renderRecentMilestones(p Presentation) string {
	count := len(p.Timeline)
	displayed := count
	if displayed > 5 {
		displayed = 5
	}
	var out strings.Builder
	fmt.Fprintf(&out, "共 %d 条，显示 %d 条；[查看完整历史](项目历史.md)。", count, displayed)
	for index := count - displayed; index < count; index++ {
		if index >= 0 {
			fmt.Fprintf(&out, "\n- [里程碑](项目历史.md#milestone-%s)", markdownAnchorID(p.Timeline[index].ID))
		}
	}
	return out.String()
}

func renderMilestoneEvidence(item Timeline) string {
	var out strings.Builder
	fmt.Fprintf(&out, "- 发生时间：%s\n- 类型：%s\n", item.OccurredAt, item.Kind)
	writeSegmentEvidence(&out, "触发", item.ClosedLoop.TriggerQuestion)
	fmt.Fprintf(&out, "- 结论来源：%s\n", item.ClosedLoop.Conclusion.Kind)
	writeSourceRefs(&out, "结论引用", item.ClosedLoop.Conclusion.SourceTurnRefs)
	writeSegmentEvidence(&out, "执行", item.ClosedLoop.Execution)
	writeSegmentEvidence(&out, "验证", item.ClosedLoop.Verification)
	fmt.Fprintf(&out, "- 影响/后续状态：%s\n", item.ClosedLoop.ImpactAndFollowUp.State)
	writeSourceRefs(&out, "影响/后续引用", item.ClosedLoop.ImpactAndFollowUp.SourceTurnRefs)
	coverage := item.ClosedLoop.Coverage
	fmt.Fprintf(&out, "- Coverage：source=%d, captured=%d, truncated=%d, unavailable=%d\n", coverage.SourceTurns, coverage.CapturedTurns, coverage.TruncatedTurns, coverage.SourceUnavailableTurns)
	writeSourceRefs(&out, "全部引用", item.ClosedLoop.SourceTurnRefs)
	if len(item.DecisionIDs) == 0 {
		out.WriteString("- 关联决策：无\n")
	} else {
		out.WriteString("- 关联决策：" + strings.Join(item.DecisionIDs, ", ") + "\n")
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func writeSegmentEvidence(out *strings.Builder, label string, segment ClosedLoopSegment) {
	fmt.Fprintf(out, "- %s状态：%s\n", label, segment.State)
	if segment.Text != "" {
		fmt.Fprintf(out, "  - 文本：%s\n", strings.ReplaceAll(segment.Text, "\n", "\n    "))
	}
	if segment.MissingReason != nil {
		fmt.Fprintf(out, "  - 缺失原因：%s\n", *segment.MissingReason)
	}
	writeSourceRefs(out, label+"引用", segment.SourceTurnRefs)
}

func writeSourceRefs(out *strings.Builder, label string, refs []SourceTurnRef) {
	if len(refs) == 0 {
		fmt.Fprintf(out, "- %s：无\n", label)
		return
	}
	for _, ref := range refs {
		fmt.Fprintf(out, "- %s：%s/%s#%s\n", label, ref.Provider, ref.SessionID, ref.TurnUnitID)
	}
}

func markdownAnchorID(id string) string {
	var out strings.Builder
	for _, character := range id {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			out.WriteRune(character)
		}
	}
	return out.String()
}

func mergeMarkdownDocument(relative string, previous, oldCanonical, desired []byte, acceptedHash string, base, next Presentation) ([]byte, error) {
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
		oldBlock, _ := markdownBlockByKey(oldExpected, block.Key)
		if !markdownSemanticEqual(markdownBlockValue(oldDocument, block), markdownBlockValue(oldExpected, oldBlock)) {
			return nil, &MarkdownError{Code: MarkdownGeneratedRegionModified, Relative: relative, Entity: block.Key.Entity, Field: block.Key.Name}
		}
	}
	if sha256Hex(previous) != acceptedHash {
		return nil, &MarkdownError{Code: MarkdownBaselineMissing, Relative: relative}
	}
	replacements := make(map[FieldKey]string, len(newExpected.blocks))
	for _, block := range newExpected.blocks {
		replacements[block.Key] = markdownBlockValue(newExpected, block)
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
	additions := make([]byte, 0)
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
		additionBytes := len(heading) + end - start
		if additionBytes > maxMarkdownDocumentBytes || len(merged)+len(additions) > maxMarkdownDocumentBytes-additionBytes {
			return nil, &MarkdownError{Code: MarkdownFormatInvalid, Relative: relative, Entity: entity}
		}
		additions = append(additions, heading...)
		additions = append(additions, desired[start:end]...)
	}
	if len(additions) == 0 {
		return merged, nil
	}
	separatorBytes := 0
	if len(merged) > 0 && merged[len(merged)-1] != '\n' {
		separatorBytes = 1
	}
	if len(merged) > maxMarkdownDocumentBytes-len(additions)-separatorBytes {
		return nil, &MarkdownError{Code: MarkdownFormatInvalid, Relative: relative}
	}
	result := make([]byte, 0, len(merged)+len(additions)+separatorBytes)
	result = append(result, merged...)
	if separatorBytes != 0 {
		result = append(result, '\n')
	}
	result = append(result, additions...)
	return result, nil
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
		previousStart := bytes.LastIndexByte(document.raw[:previousEnd], '\n') + 1
		line := document.raw[previousStart:previousEnd]
		if bytes.HasPrefix(line, []byte(`<a id="`)) && bytes.HasSuffix(line, []byte(`"></a>`)) {
			return previousStart
		}
		if len(bytes.TrimSpace(line)) != 0 {
			return -1
		}
		lineStart = previousStart
	}
	return -1
}

func replaceMarkdownBlocks(document MarkdownDocument, replacements map[FieldKey]string) ([]byte, error) {
	var out bytes.Buffer
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
	if out.Len() > maxMarkdownDocumentBytes {
		return nil, &MarkdownError{Code: MarkdownFormatInvalid}
	}
	return out.Bytes(), nil
}

func validateMarkdownAnchors(relative string, actual, expected []byte) error {
	for start := 0; start < len(expected); {
		end, next := markdownPhysicalLine(expected, start)
		line := expected[start:end]
		if bytes.HasPrefix(line, []byte(`<a id="`)) && bytes.HasSuffix(line, []byte(`"></a>`)) && bytes.Count(actual, line) != 1 {
			return &MarkdownError{Code: MarkdownStructureEditRequiresCommand, Relative: relative}
		}
		start = next
	}
	return nil
}

func replaceMarkdownFrontmatterBindings(raw []byte, next Presentation) ([]byte, error) {
	replacements := map[string]string{
		"revision":      strconv.Itoa(next.Revision),
		"generation_id": next.GenerationID,
	}
	found := map[string]bool{}
	var out bytes.Buffer
	cursor := 0
	for start := 0; start < len(raw); {
		end, nextLine := markdownPhysicalLine(raw, start)
		line := raw[start:end]
		if start > 0 && bytes.Equal(line, []byte("---")) {
			out.Write(raw[cursor:nextLine])
			cursor = nextLine
			break
		}
		for key, value := range replacements {
			prefix := []byte(key + ":")
			if bytes.HasPrefix(line, prefix) {
				if found[key] {
					return nil, &MarkdownError{Code: MarkdownFormatInvalid}
				}
				found[key] = true
				out.Write(raw[cursor:start])
				out.WriteString(key + ": " + value)
				out.Write(raw[end:nextLine])
				cursor = nextLine
			}
		}
		start = nextLine
	}
	if !found["revision"] || !found["generation_id"] {
		return nil, &MarkdownError{Code: MarkdownFormatInvalid}
	}
	out.Write(raw[cursor:])
	return out.Bytes(), nil
}
