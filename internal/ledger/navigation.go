package ledger

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/accounting"
)

const (
	GeneratedMarkerPrefix      = "<!-- session-reviewer:generated=v1;owner=navigation;section="
	recoveryMermaidRuneBudget  = 4_096
	navigationSummaryRuneLimit = 160
	navigationRecentItemLimit  = 3
)

type DerivedArtifact struct {
	RelativePath string
	EntityID     string
	Data         []byte
	Perm         fs.FileMode
}

var standaloneDerivedPaths = map[string]struct{}{
	ledgerRootRelative + "/diagrams/project-evolution.md": {},
	ledgerRootRelative + "/decisions/00-目录说明.md":          {},
	ledgerRootRelative + "/open-loops/00-目录说明.md":         {},
	ledgerRootRelative + "/sessions/00-目录说明.md":           {},
}

func IsStandaloneDerivedPath(relative string) bool {
	_, ok := standaloneDerivedPaths[relative]
	return ok
}

func IsGeneratedSectionName(name string) bool {
	return name == "项目导航" || name == "快速理解" || name == "Project accounting"
}

func RenderRecoveryMermaid(state State) (string, error) {
	timeline := append([]TimelineEvent(nil), state.Timeline...)
	sort.Slice(timeline, func(i, j int) bool {
		if timeline[i].OccurredAt != timeline[j].OccurredAt {
			return timeline[i].OccurredAt < timeline[j].OccurredAt
		}
		return timeline[i].ID < timeline[j].ID
	})

	referenced := make(map[string]int)
	for index := len(timeline) - 1; index >= 0; index-- {
		for _, id := range timeline[index].DecisionIDs {
			if _, exists := referenced[id]; !exists {
				referenced[id] = index
			}
		}
	}
	decisions := make([]Decision, 0, len(state.Decisions))
	for _, decision := range state.Decisions {
		decisions = append(decisions, decision)
	}
	sort.Slice(decisions, func(i, j int) bool {
		left, leftReferenced := referenced[decisions[i].ID]
		right, rightReferenced := referenced[decisions[j].ID]
		if leftReferenced != rightReferenced {
			return leftReferenced
		}
		if leftReferenced && left != right {
			return left > right
		}
		if decisions[i].Title != decisions[j].Title {
			return decisions[i].Title < decisions[j].Title
		}
		return decisions[i].ID < decisions[j].ID
	})
	decisionLines := make([]string, 0, navigationRecentItemLimit+1)
	for _, decision := range decisions[:min(len(decisions), navigationRecentItemLimit)] {
		decisionLines = append(decisionLines, summarizeNavigation(decision.Title, 48))
	}
	if remaining := len(decisions) - len(decisionLines); remaining > 0 {
		decisionLines = append(decisionLines, fmt.Sprintf("另有 %d 项", remaining))
	}
	if len(decisionLines) == 0 {
		decisionLines = append(decisionLines, "暂无已记录决策")
	}

	verified := make([]TimelineEvent, 0, len(timeline))
	for _, event := range timeline {
		if event.Class == Verified {
			verified = append(verified, event)
		}
	}
	if len(verified) > navigationRecentItemLimit {
		verified = verified[len(verified)-navigationRecentItemLimit:]
	}
	milestoneLines := make([]string, 0, len(verified))
	for _, event := range verified {
		milestoneLines = append(milestoneLines, summarizeNavigation(event.Title, 48))
	}
	if len(milestoneLines) == 0 {
		milestoneLines = append(milestoneLines, "暂无已验证里程碑")
	}

	goal := summarizeNavigation(state.CurrentState.Goal, 96)
	if goal == "" {
		goal = "尚未记录项目目标"
	}
	current := summarizeNavigation(state.CurrentState.LastVerified, 96)
	if current == "" {
		current = "尚未记录验证状态"
	}
	next := summarizeNavigation(state.CurrentState.NextAction, 96)
	if next == "" {
		next = "尚未记录下一步"
	}
	next += fmt.Sprintf("\n开放待办：%d", openLoopCount(state.OpenLoops))

	var out strings.Builder
	out.WriteString("flowchart LR\n")
	fmt.Fprintf(&out, "  goal[\"项目目标<br/>%s\"]\n", mermaidNavigationText(goal))
	fmt.Fprintf(&out, "  decisions[\"关键决策汇总<br/>%s\"]\n", mermaidNavigationText(strings.Join(decisionLines, "\n")))
	fmt.Fprintf(&out, "  milestones[\"最近已验证里程碑<br/>%s\"]\n", mermaidNavigationText(strings.Join(milestoneLines, "\n")))
	fmt.Fprintf(&out, "  current[\"当前状态<br/>%s\"]\n", mermaidNavigationText(current))
	fmt.Fprintf(&out, "  next[\"下一步<br/>%s\"]\n", mermaidNavigationText(next))
	out.WriteString("  goal --> decisions --> milestones --> current --> next\n")
	result := out.String()
	if !utf8.ValidString(result) || len([]rune(result)) > recoveryMermaidRuneBudget {
		return "", errors.New("recovery Mermaid exceeds display budget")
	}
	return result, nil
}

func RenderDerivedArtifacts(state State) ([]DerivedArtifact, error) {
	if state.documents.overview == nil {
		return nil, errors.New("project overview is required for navigation")
	}
	mermaid, err := RenderRecoveryMermaid(state)
	if err != nil {
		return nil, err
	}
	summary, pricingComplete, err := aggregateNavigationAccounting(state)
	if err != nil {
		return nil, err
	}

	artifacts := make([]DerivedArtifact, 0, 5+len(state.Decisions)+len(state.OpenLoops)+len(state.Sessions))
	overview, err := parseNavigationOverview(state.documents.overview.Original, state.ProjectID)
	if err != nil {
		return nil, err
	}
	prependGeneratedSection(&overview, "项目导航", renderOverviewNavigation(state, mermaid, summary))
	if err := overview.UpsertSection("Project accounting", GeneratedMarkerPrefix+"Project accounting -->\n\n"+projectAccountingMarkdown(summary, pricingComplete)); err != nil {
		return nil, err
	}
	if err := appendNavigationDocument(&artifacts, state.documents.overview.RelativePath, "project-overview", overview, state.documents.overview.Perm); err != nil {
		return nil, err
	}

	for _, decision := range sortedNavigationDecisions(state.Decisions) {
		loaded, ok := state.documents.decisions[decision.ID]
		if !ok {
			return nil, fmt.Errorf("decision %s has no loaded document", decision.ID)
		}
		doc, err := loaded.Document.Clone()
		if err != nil {
			return nil, err
		}
		prependGeneratedSection(&doc, "快速理解", renderDecisionQuick(decision))
		if err := appendNavigationDocument(&artifacts, loaded.RelativePath, decision.ID, doc, loaded.Perm); err != nil {
			return nil, err
		}
	}
	for _, loop := range sortedNavigationLoops(state.OpenLoops) {
		loaded, ok := state.documents.openLoops[loop.ID]
		if !ok {
			return nil, fmt.Errorf("open loop %s has no loaded document", loop.ID)
		}
		doc, err := loaded.Document.Clone()
		if err != nil {
			return nil, err
		}
		prependGeneratedSection(&doc, "快速理解", renderOpenLoopQuick(loop))
		if err := appendNavigationDocument(&artifacts, loaded.RelativePath, loop.ID, doc, loaded.Perm); err != nil {
			return nil, err
		}
	}
	for _, session := range sortedNavigationSessions(state.Sessions) {
		loaded, ok := state.documents.sessions[session.ID]
		if !ok {
			return nil, fmt.Errorf("session %s has no loaded document", session.ID)
		}
		doc, err := loaded.Document.Clone()
		if err != nil {
			return nil, err
		}
		prependGeneratedSection(&doc, "快速理解", renderSessionQuick(session))
		if err := appendNavigationDocument(&artifacts, loaded.RelativePath, session.ID, doc, loaded.Perm); err != nil {
			return nil, err
		}
	}

	standalone := []DerivedArtifact{
		{RelativePath: ledgerRootRelative + "/decisions/00-目录说明.md", Data: renderDecisionIndex(state), Perm: 0o644},
		{RelativePath: ledgerRootRelative + "/open-loops/00-目录说明.md", Data: renderOpenLoopIndex(state), Perm: 0o644},
		{RelativePath: ledgerRootRelative + "/sessions/00-目录说明.md", Data: renderSessionIndex(state), Perm: 0o644},
	}
	diagram, err := renderDiagram(state)
	if err != nil {
		return nil, err
	}
	standalone = append(standalone, DerivedArtifact{RelativePath: ledgerRootRelative + "/diagrams/project-evolution.md", Data: diagram, Perm: 0o644})
	for _, artifact := range standalone {
		if len(artifact.Data) > MaxDocumentBytes {
			return nil, errors.New("derived navigation exceeds document size limit")
		}
		artifacts = append(artifacts, artifact)
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].RelativePath < artifacts[j].RelativePath })
	for i := 1; i < len(artifacts); i++ {
		if artifacts[i-1].RelativePath == artifacts[i].RelativePath {
			return nil, errors.New("derived navigation target collision")
		}
	}
	return artifacts, nil
}

func parseNavigationOverview(src []byte, projectID string) (Document, error) {
	if document, err := ParseDocument(src); err == nil {
		return document, nil
	}
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	frontmatter, body, err := splitFrontmatter(text)
	if err != nil {
		return Document{}, err
	}
	mapping, err := decodeFrontmatter(frontmatter)
	if err != nil {
		return Document{}, err
	}
	existingProjectID, err := requiredString(mapping, "project_id")
	if err != nil || existingProjectID != projectID {
		return Document{}, errors.New("project overview identity is invalid")
	}
	defaults := []struct {
		key   string
		value any
	}{{"id", "project-overview"}, {"entity_type", "project_overview"}, {"revision", 1}}
	for _, field := range defaults {
		key, value := field.key, field.value
		if _, exists := mappingValue(mapping, key); exists {
			return Document{}, errors.New("project overview identity is invalid")
		}
		node, err := encodeValue(value)
		if err != nil {
			return Document{}, err
		}
		setMappingValue(mapping, key, node)
	}
	preamble, sections, err := parseSections(body)
	if err != nil {
		return Document{}, err
	}
	document := Document{Frontmatter: *mapping, Preamble: preamble, Sections: sections}
	if _, err := document.Render(); err != nil {
		return Document{}, err
	}
	return document, nil
}

func appendNavigationDocument(target *[]DerivedArtifact, relative, entityID string, doc Document, perm fs.FileMode) error {
	body, err := doc.Render()
	if err != nil {
		return err
	}
	if len(body) > MaxDocumentBytes {
		return errors.New("derived navigation document exceeds size limit")
	}
	*target = append(*target, DerivedArtifact{RelativePath: relative, EntityID: entityID, Data: body, Perm: perm})
	return nil
}

func prependGeneratedSection(doc *Document, name, body string) {
	sections := make([]Section, 0, len(doc.Sections)+1)
	sections = append(sections, Section{Name: name, Heading: "## " + name, Body: "\n" + GeneratedMarkerPrefix + name + " -->\n\n" + strings.TrimSpace(body) + "\n\n"})
	for _, section := range doc.Sections {
		if section.Name != name {
			sections = append(sections, section)
		}
	}
	doc.Sections = sections
}

func renderOverviewNavigation(state State, mermaid string, summary accounting.ProjectSummary) string {
	var out strings.Builder
	out.WriteString("### 项目总览\n\n")
	fmt.Fprintf(&out, "- **项目目标：** %s\n", markdownNavigationText(valueOr(state.CurrentState.Goal, "尚未记录")))
	fmt.Fprintf(&out, "- **最后验证：** %s\n", markdownNavigationText(valueOr(state.CurrentState.LastVerified, "尚未记录")))
	fmt.Fprintf(&out, "- **下一步：** %s\n\n", markdownNavigationText(valueOr(state.CurrentState.NextAction, "尚未记录")))
	out.WriteString("### 项目演进主线\n\n```mermaid\n")
	out.WriteString(mermaid)
	out.WriteString("```\n\n### 当前风险、阻塞与开放待办\n\n")
	writeNavigationList(&out, append(append([]string(nil), state.CurrentState.Blockers...), state.CurrentState.OpenRisks...), "暂无当前风险或阻塞")
	fmt.Fprintf(&out, "\n- 开放待办：%d 项\n", openLoopCount(state.OpenLoops))
	out.WriteString("\n### 最近三项变化\n\n")
	writeRecentTimeline(&out, state.Timeline)
	out.WriteString("\n### 项目用量与成本\n\n")
	fmt.Fprintf(&out, "- 总耗时：%s (%d ms)\n- Token 总量：%s\n- 总成本：$%.9f USD\n", accounting.FormatDurationMS(summary.TotalDurationMS), summary.TotalDurationMS, formatNavigationInt(summary.TotalTokens), summary.TotalCostUSD)
	for _, model := range summary.Models {
		fmt.Fprintf(&out, "- %s：%s Token (%.4f%%)；$%.9f USD (%.4f%% 成本)\n", markdownNavigationText(model.Model), formatNavigationInt(model.TotalTokens), model.TokenSharePct, model.TotalCostUSD, model.CostSharePct)
	}
	out.WriteString("\n### 快速入口\n\n")
	out.WriteString("- [当前状态](<current-state.md>)\n- [完整时间线](<evolution-timeline.md>)\n- [完整项目演进图](<diagrams/project-evolution.md>)\n- [决策索引](<decisions/00-目录说明.md>)\n- [待办索引](<open-loops/00-目录说明.md>)\n- [Session 索引](<sessions/00-目录说明.md>)")
	return out.String()
}

func renderDecisionIndex(state State) []byte {
	var out strings.Builder
	out.WriteString("# 决策目录\n\n> 此文件由 SessionReviewer 生成；手工修改会被覆盖。请编辑链接中的决策正文。\n\n")
	items := sortedNavigationDecisions(state.Decisions)
	if len(items) == 0 {
		out.WriteString("当前还没有记录决策。\n")
	}
	for _, item := range items {
		reason := valueOr(item.Rationale, item.Context)
		fmt.Fprintf(&out, "- [%s](<%s>) — %s", markdownNavigationText(item.Title), navigationDocumentLink(state.documents.decisions[item.ID], item.ID), decisionStatusLabel(item.Status))
		if len(item.Tags) != 0 {
			fmt.Fprintf(&out, "；标签：%s", markdownNavigationText(strings.Join(item.Tags, "、")))
		}
		if reason != "" {
			fmt.Fprintf(&out, "；%s", markdownNavigationText(summarizeNavigation(reason, navigationSummaryRuneLimit)))
		}
		out.WriteByte('\n')
	}
	return []byte(out.String())
}

func renderOpenLoopIndex(state State) []byte {
	var out strings.Builder
	out.WriteString("# 待办目录\n\n> 此文件由 SessionReviewer 生成；手工修改会被覆盖。请编辑链接中的待办正文。\n\n")
	items := sortedNavigationLoops(state.OpenLoops)
	if len(items) == 0 {
		out.WriteString("当前没有开放待办。\n")
	}
	for _, item := range items {
		fmt.Fprintf(&out, "- [%s](<%s>) — %s；阻塞：%s；下一步：%s\n", markdownNavigationText(item.Title), navigationDocumentLink(state.documents.openLoops[item.ID], item.ID), loopStatusLabel(item.Status), markdownNavigationText(valueOr(item.Blocker, "无")), markdownNavigationText(valueOr(item.NextExperiment, "尚未记录")))
	}
	return []byte(out.String())
}

func renderSessionIndex(state State) []byte {
	var out strings.Builder
	out.WriteString("# Session 目录\n\n> 此文件由 SessionReviewer 生成；手工修改会被覆盖。请编辑链接中的 Session 正文。\n\n")
	items := sortedNavigationSessions(state.Sessions)
	if len(items) == 0 {
		out.WriteString("当前还没有 Session 记录。\n")
	}
	for _, item := range items {
		date, phase := "日期未知", "尚无阶段摘要"
		if item.Accounting != nil {
			date = valueOr(item.Accounting.EndedAt, item.Accounting.StartedAt)
		}
		if len(item.Phases) != 0 {
			last := item.Phases[len(item.Phases)-1]
			phase = strings.TrimSpace(last.Title + "：" + last.Summary)
		}
		fmt.Fprintf(&out, "- [%s · %s](<%s>) — 目标：%s；最近阶段：%s", markdownNavigationText(date), markdownNavigationText(item.InitialGoal), navigationDocumentLink(state.documents.sessions[item.ID], item.ID), markdownNavigationText(summarizeNavigation(item.InitialGoal, 80)), markdownNavigationText(summarizeNavigation(phase, navigationSummaryRuneLimit)))
		if item.Accounting != nil {
			fmt.Fprintf(&out, "；耗时：%s；Token：%s", accounting.FormatDurationMS(item.Accounting.DurationMS), formatNavigationInt(item.Accounting.TotalTokens))
			if accounting.SessionPricingComplete(item.Accounting) {
				fmt.Fprintf(&out, "；成本：$%.9f USD", item.Accounting.TotalCostUSD)
			} else {
				out.WriteString("；成本：费用暂不可用")
			}
		}
		out.WriteByte('\n')
	}
	return []byte(out.String())
}

func renderDecisionQuick(item Decision) string {
	reason := valueOr(item.Rationale, item.Context)
	return fmt.Sprintf("- **结论：** %s\n- **状态：** %s\n- **为什么重要：** %s", markdownNavigationText(item.Title), decisionStatusLabel(item.Status), markdownNavigationText(valueOr(summarizeNavigation(reason, navigationSummaryRuneLimit), "尚未记录")))
}

func renderOpenLoopQuick(item OpenLoop) string {
	return fmt.Sprintf("- **问题：** %s\n- **状态：** %s\n- **当前阻塞：** %s\n- **下一实验：** %s", markdownNavigationText(valueOr(item.Question, item.Title)), loopStatusLabel(item.Status), markdownNavigationText(valueOr(item.Blocker, "无")), markdownNavigationText(valueOr(item.NextExperiment, "尚未记录")))
}

func renderSessionQuick(item SessionReport) string {
	phase, verification := "尚未记录", "尚未记录"
	if len(item.Phases) != 0 {
		last := item.Phases[len(item.Phases)-1]
		phase = summarizeNavigation(strings.TrimSpace(last.Title+"："+last.Summary), navigationSummaryRuneLimit)
	}
	if len(item.Verification) != 0 {
		verification = summarizeNavigation(item.Verification[len(item.Verification)-1], navigationSummaryRuneLimit)
	}
	usage := "- **耗时 / Token / 成本：** 尚未记录"
	if item.Accounting != nil {
		cost := fmt.Sprintf("$%.9f USD", item.Accounting.TotalCostUSD)
		if !accounting.SessionPricingComplete(item.Accounting) {
			cost = "费用暂不可用"
		}
		usage = fmt.Sprintf("- **耗时 / Token / 成本：** %s / %s / %s", accounting.FormatDurationMS(item.Accounting.DurationMS), formatNavigationInt(item.Accounting.TotalTokens), cost)
	}
	return fmt.Sprintf("- **初始目标：** %s\n- **最近阶段：** %s\n- **验证摘要：** %s\n%s", markdownNavigationText(item.InitialGoal), markdownNavigationText(phase), markdownNavigationText(verification), usage)
}

func aggregateNavigationAccounting(state State) (accounting.ProjectSummary, bool, error) {
	values := make([]*accounting.SessionAccounting, 0, len(state.Sessions))
	for _, session := range state.Sessions {
		values = append(values, session.Accounting)
	}
	summary, err := accounting.Aggregate(values)
	return summary, accounting.SessionsPricingComplete(values), err
}

func writeRecentTimeline(out *strings.Builder, timeline []TimelineEvent) {
	items := append([]TimelineEvent(nil), timeline...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].OccurredAt != items[j].OccurredAt {
			return items[i].OccurredAt < items[j].OccurredAt
		}
		return items[i].ID < items[j].ID
	})
	if len(items) > navigationRecentItemLimit {
		items = items[len(items)-navigationRecentItemLimit:]
	}
	if len(items) == 0 {
		out.WriteString("- 暂无变化记录\n")
		return
	}
	for index := len(items) - 1; index >= 0; index-- {
		fmt.Fprintf(out, "- %s：%s\n", markdownNavigationText(items[index].Title), markdownNavigationText(summarizeNavigation(items[index].Summary, navigationSummaryRuneLimit)))
	}
}

func writeNavigationList(out *strings.Builder, values []string, empty string) {
	written := 0
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			fmt.Fprintf(out, "- %s\n", markdownNavigationText(value))
			written++
		}
	}
	if written == 0 {
		fmt.Fprintf(out, "- %s\n", empty)
	}
}

func sortedNavigationDecisions(values map[string]Decision) []Decision {
	result := make([]Decision, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Title != result[j].Title {
			return result[i].Title < result[j].Title
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func sortedNavigationLoops(values map[string]OpenLoop) []OpenLoop {
	result := make([]OpenLoop, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Title != result[j].Title {
			return result[i].Title < result[j].Title
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func sortedNavigationSessions(values map[string]SessionReport) []SessionReport {
	result := make([]SessionReport, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := "", ""
		if result[i].Accounting != nil {
			left = valueOr(result[i].Accounting.EndedAt, result[i].Accounting.StartedAt)
		}
		if result[j].Accounting != nil {
			right = valueOr(result[j].Accounting.EndedAt, result[j].Accounting.StartedAt)
		}
		if left != right {
			return left > right
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func summarizeNavigation(value string, limit int) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	paragraphs := strings.Split(value, "\n\n")
	value = strings.Join(strings.Fields(paragraphs[0]), " ")
	runes := []rune(value)
	if limit < 2 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

func mermaidNavigationText(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "\n", "<br/>", "[", "&#91;", "]", "&#93;").Replace(value)
}

func markdownNavigationText(value string) string {
	value = strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
	value = html.EscapeString(value)
	return strings.NewReplacer(
		"\\", "\\\\",
		"`", "\\`",
		"*", "\\*",
		"_", "\\_",
		"[", "\\[",
		"]", "\\]",
		"~", "\\~",
	).Replace(value)
}

func markdownNavigationTarget(value string) string {
	parsed := &url.URL{Path: value}
	return parsed.EscapedPath()
}

func navigationDocumentLink(document loadedDocument, fallbackID string) string {
	filename := path.Base(document.RelativePath)
	if filename == "." || filename == "/" || filename == "" {
		filename = fallbackID + ".md"
	}
	return markdownNavigationTarget(filename)
}

func formatNavigationInt(value int64) string {
	negative := value < 0
	digits := strconv.FormatInt(value, 10)
	if negative {
		digits = strings.TrimPrefix(digits, "-")
	}
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	if negative {
		return "-" + digits
	}
	return digits
}

func decisionStatusLabel(status string) string {
	return map[string]string{"proposed": "提议中", "accepted": "已接受", "superseded": "已被替代", "archived": "已归档"}[status]
}

func loopStatusLabel(status string) string {
	return map[string]string{"open": "开放", "blocked": "受阻", "resolved": "已解决", "abandoned": "已放弃", "archived": "已归档"}[status]
}

func openLoopCount(values map[string]OpenLoop) int {
	count := 0
	for _, value := range values {
		if value.Status == "open" || value.Status == "blocked" {
			count++
		}
	}
	return count
}

func valueOr(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func derivedArtifactBytes(artifacts []DerivedArtifact, relative string) ([]byte, bool) {
	for _, artifact := range artifacts {
		if artifact.RelativePath == relative {
			return bytes.Clone(artifact.Data), true
		}
	}
	return nil, false
}
