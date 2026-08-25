package reviewv2

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type documentPatch struct {
	span sourceSpan
	body []byte
}

func patchAcceptedDocuments(current Accepted, desired State) ([]byte, []byte, error) {
	reviewBody, err := patchAcceptedReview(current.reviewDoc, desired.Review)
	if err != nil {
		return nil, nil, err
	}
	historyBody, err := patchAcceptedHistory(current.historyDoc, desired.Review.Revision, desired.Events)
	if err != nil {
		return nil, nil, err
	}
	return reviewBody, historyBody, nil
}

func patchAcceptedReview(document ReviewDocument, desired Review) ([]byte, error) {
	if document.Model.ProjectID != desired.ProjectID || document.Model.Name != desired.Name {
		return nil, errors.New("accepted review identity or project name cannot be rewritten")
	}
	source, err := patchFrontmatterRevision(document.source, desired.Revision)
	if err != nil {
		return nil, err
	}
	document, err = ParseReview(source)
	if err != nil {
		return nil, err
	}

	oldRisks := riskByID(document.Model.Risks)
	oldDecisions := decisionByID(document.Model.Decisions)
	wantedRisks := riskByID(desired.Risks)
	removals := make([]documentPatch, 0)
	for id := range oldRisks {
		if _, exists := wantedRisks[id]; !exists {
			block, blockErr := markerBlockByID(document.source, "risk", id)
			if blockErr != nil {
				return nil, blockErr
			}
			removals = append(removals, documentPatch{span: block.whole})
		}
	}
	for id := range oldDecisions {
		if _, exists := decisionByID(desired.Decisions)[id]; !exists {
			return nil, fmt.Errorf("accepted review decision %q cannot be deleted", id)
		}
	}
	if len(removals) != 0 {
		source = applyDocumentPatches(document.source, removals)
		document, err = ParseReview(source)
		if err != nil {
			return nil, err
		}
		oldRisks = riskByID(document.Model.Risks)
	}

	for _, risk := range desired.Risks {
		if _, exists := oldRisks[risk.ID]; exists {
			continue
		}
		source, err = insertReviewMarkerBlock(source, "risk", risk.ID, reviewRiskBlock(risk))
		if err != nil {
			return nil, err
		}
	}
	for _, decision := range desired.Decisions {
		if _, exists := oldDecisions[decision.ID]; exists {
			continue
		}
		source, err = insertReviewMarkerBlock(source, "decision", decision.ID, reviewDecisionBlock(decision))
		if err != nil {
			return nil, err
		}
	}
	document, err = ParseReview(source)
	if err != nil {
		return nil, err
	}
	patches := make([]documentPatch, 0)
	appendReviewPatch := func(unit, field, value string, list bool) error {
		span, exists := document.fields[semanticField{unitID: unit, field: field}]
		if !exists {
			return fmt.Errorf("review unit %q has no semantic field %q", unit, field)
		}
		original := document.source[span.start:span.end]
		replacement := replaceFieldBody(original, value, list)
		if field == "risk.title" {
			replacement = []byte("### " + strings.TrimSpace(value) + "\n")
		}
		if field == "decision.title" {
			replacement = []byte("### " + strings.TrimSpace(value) + "\n")
		}
		if !bytes.Equal(original, replacement) {
			patches = append(patches, documentPatch{span: span, body: replacement})
		}
		return nil
	}
	for _, field := range []struct{ name, value string }{
		{"goal", desired.Goal}, {"stage", desired.Stage}, {"status", desired.Status},
		{"next_action", desired.NextAction}, {"last_verification", desired.LastVerification},
	} {
		if err := appendReviewPatch("project-overview", field.name, field.value, false); err != nil {
			return nil, err
		}
	}
	for _, risk := range desired.Risks {
		for _, field := range []struct{ name, value string }{
			{"risk.title", risk.Title}, {"risk.status", risk.Status}, {"risk.detail", risk.Detail},
		} {
			if err := appendReviewPatch(risk.ID, field.name, field.value, false); err != nil {
				return nil, err
			}
		}
	}
	for _, decision := range desired.Decisions {
		for _, field := range []struct{ name, value string }{
			{"decision.title", decision.Title}, {"decision.rationale", decision.Rationale}, {"decision.impact", decision.Impact},
		} {
			if err := appendReviewPatch(decision.ID, field.name, field.value, false); err != nil {
				return nil, err
			}
		}
		for _, optional := range []struct{ field, heading, value string }{
			{"decision.occurred_at", "日期", decision.OccurredAt}, {"decision.status", "状态", decision.Status},
		} {
			span, exists := document.fields[semanticField{unitID: decision.ID, field: optional.field}]
			if exists {
				replacement := replaceFieldBody(document.source[span.start:span.end], optional.value, false)
				if !bytes.Equal(document.source[span.start:span.end], replacement) {
					patches = append(patches, documentPatch{span: span, body: replacement})
				}
			} else if optional.value != "" {
				block, err := markerBlockByID(document.source, "decision", decision.ID)
				if err != nil {
					return nil, err
				}
				patches = append(patches, documentPatch{span: sourceSpan{start: block.close.start, end: block.close.start}, body: []byte("#### " + optional.heading + "\n" + strings.TrimSpace(optional.value) + "\n")})
			}
		}
	}
	source = applyDocumentPatches(document.source, patches)
	riskOrder := make([]string, len(desired.Risks))
	for index, risk := range desired.Risks {
		riskOrder[index] = risk.ID
	}
	source, err = reorderReviewMarkerBlocks(source, "risk", riskOrder)
	if err != nil {
		return nil, err
	}
	decisionOrder := make([]string, len(desired.Decisions))
	for index, decision := range desired.Decisions {
		decisionOrder[index] = decision.ID
	}
	source, err = reorderReviewMarkerBlocks(source, "decision", decisionOrder)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseReview(source)
	if err != nil {
		return nil, fmt.Errorf("patch accepted review: %w", err)
	}
	if !equalReviewSemantics(parsed.Model, desired) {
		return nil, errors.New("patch accepted review changed semantic fields")
	}
	return source, nil
}

func patchAcceptedHistory(document HistoryDocument, revision int, desired []Event) ([]byte, error) {
	source, err := patchFrontmatterRevision(document.source, revision)
	if err != nil {
		return nil, err
	}
	document, err = ParseHistory(source)
	if err != nil {
		return nil, err
	}
	old := eventByID(document.Events)
	want := eventByID(desired)
	for id := range old {
		if _, exists := want[id]; !exists {
			return nil, fmt.Errorf("accepted history event %q cannot be deleted", id)
		}
	}
	ordered, err := canonicalHistoryEvents(desired)
	if err != nil {
		return nil, err
	}
	for _, event := range ordered {
		if _, exists := old[event.ID]; exists {
			continue
		}
		source, err = insertHistoryMarkerBlock(source, ordered, event.ID, historyEventBlock(event))
		if err != nil {
			return nil, err
		}
	}
	document, err = ParseHistory(source)
	if err != nil {
		return nil, err
	}
	patches := make([]documentPatch, 0)
	for _, event := range ordered {
		current, exists := eventByID(document.Events)[event.ID]
		if !exists {
			return nil, fmt.Errorf("history event %q is missing after insertion", event.ID)
		}
		if current.Title != event.Title || current.OccurredAt != event.OccurredAt {
			span := document.fields[semanticField{unitID: event.ID, field: "event.title"}]
			patches = append(patches, documentPatch{span: span, body: []byte("## " + event.OccurredAt + " · " + strings.TrimSpace(event.Title) + "\n")})
		}
		for _, field := range []struct {
			name  string
			value string
			list  bool
		}{
			{"event.kind", event.Kind, false}, {"event.meaning", event.Meaning, false},
			{"event.summary", event.Summary, false}, {"event.why", event.Why, false},
			{"event.changes", strings.Join(event.Changes, "\n"), true}, {"event.results", strings.Join(event.Results, "\n"), true},
			{"event.next", event.Next, false},
		} {
			span, exists := document.fields[semanticField{unitID: event.ID, field: field.name}]
			if !exists {
				if field.name == "event.summary" && field.value == "" {
					continue
				}
				return nil, fmt.Errorf("history event %q has no semantic field %q", event.ID, field.name)
			}
			replacement := replaceFieldBody(document.source[span.start:span.end], field.value, field.list)
			if !bytes.Equal(document.source[span.start:span.end], replacement) {
				patches = append(patches, documentPatch{span: span, body: replacement})
			}
		}
		span, exists := document.fields[semanticField{unitID: event.ID, field: "event.decision_ids"}]
		if exists {
			replacement := replaceFieldBody(document.source[span.start:span.end], strings.Join(event.DecisionIDs, "\n"), true)
			if !bytes.Equal(document.source[span.start:span.end], replacement) {
				patches = append(patches, documentPatch{span: span, body: replacement})
			}
		} else if len(event.DecisionIDs) != 0 {
			block := document.blocks[event.ID]
			patches = append(patches, documentPatch{span: sourceSpan{start: block.close.start, end: block.close.start}, body: historyListSection("关联决策", event.DecisionIDs)})
		}
	}
	source = applyDocumentPatches(document.source, patches)
	source, err = reorderHistoryMarkerBlocks(source, ordered)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseHistory(source)
	if err != nil {
		return nil, fmt.Errorf("patch accepted history: %w", err)
	}
	if parsed.ProjectID != document.ProjectID || parsed.Revision != revision || !reflect.DeepEqual(normalizedEvents(parsed.Events), normalizedEvents(ordered)) {
		return nil, errors.New("patch accepted history changed semantic fields")
	}
	return source, nil
}

func patchFrontmatterRevision(source []byte, revision int) ([]byte, error) {
	if revision < 1 {
		return nil, errors.New("accepted revision must be positive")
	}
	end := bytes.Index(source[len("---\n"):], []byte("\n---\n"))
	if end < 0 {
		return nil, errors.New("accepted document has no closed frontmatter")
	}
	end += len("---\n")
	for start := len("---\n"); start < end; {
		lineEnd := bytes.IndexByte(source[start:end], '\n')
		if lineEnd < 0 {
			lineEnd = end - start
		}
		line := source[start : start+lineEnd]
		trimmed := bytes.TrimLeft(line, " \t")
		if bytes.HasPrefix(trimmed, []byte("revision")) {
			colon := bytes.IndexByte(trimmed, ':')
			if colon >= 0 && strings.TrimSpace(string(trimmed[:colon])) == "revision" {
				valueStart := colon + 1
				for valueStart < len(trimmed) && (trimmed[valueStart] == ' ' || trimmed[valueStart] == '\t') {
					valueStart++
				}
				valueEnd := valueStart
				for valueEnd < len(trimmed) && trimmed[valueEnd] >= '0' && trimmed[valueEnd] <= '9' {
					valueEnd++
				}
				if valueEnd == valueStart {
					return nil, errors.New("accepted frontmatter revision is not a decimal integer")
				}
				offset := start + len(line) - len(trimmed)
				return spliceSource(source, sourceSpan{start: offset + valueStart, end: offset + valueEnd}, []byte(strconv.Itoa(revision))), nil
			}
		}
		start += lineEnd + 1
	}
	return nil, errors.New("accepted frontmatter revision is missing")
}

func applyDocumentPatches(source []byte, patches []documentPatch) []byte {
	sort.Slice(patches, func(i, j int) bool { return patches[i].span.start > patches[j].span.start })
	for _, patch := range patches {
		source = spliceSource(source, patch.span, patch.body)
	}
	return source
}

func insertReviewMarkerBlock(source []byte, kind, id string, body []byte) ([]byte, error) {
	identity, err := parseFrontmatter(source, "project-overview", "project_review")
	if err != nil {
		return nil, err
	}
	containers, err := reviewMarkerContainers(source, identity.bodyStart)
	if err != nil {
		return nil, err
	}
	container, exists := containers[kind]
	if !exists {
		return nil, fmt.Errorf("review has no %s marker container", kind)
	}
	return spliceSource(source, sourceSpan{start: container.end, end: container.end}, ensureBlockSpacing(source, container.end, body)), nil
}

func insertHistoryMarkerBlock(source []byte, ordered []Event, id string, body []byte) ([]byte, error) {
	identity, err := parseFrontmatter(source, "project-history", "project_history")
	if err != nil {
		return nil, err
	}
	blocks, err := scanMarkerBlocks(source, identity.bodyStart)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]markerBlock, len(blocks))
	for _, block := range blocks {
		byID[block.id] = block
	}
	insertAt := len(source)
	found := false
	for index, event := range ordered {
		if event.ID != id {
			continue
		}
		for _, later := range ordered[index+1:] {
			if block, exists := byID[later.ID]; exists {
				insertAt, found = block.whole.start, true
				break
			}
		}
		break
	}
	if !found && len(blocks) != 0 {
		insertAt = blocks[len(blocks)-1].whole.end
	}
	return spliceSource(source, sourceSpan{start: insertAt, end: insertAt}, ensureBlockSpacing(source, insertAt, body)), nil
}

func ensureBlockSpacing(source []byte, at int, body []byte) []byte {
	result := append([]byte(nil), body...)
	if at > 0 && source[at-1] != '\n' {
		result = append([]byte("\n"), result...)
	}
	if at < len(source) && len(result) != 0 && result[len(result)-1] != '\n' {
		result = append(result, '\n')
	}
	return result
}

func markerBlockByID(source []byte, kind, id string) (markerBlock, error) {
	identity, err := parseFrontmatter(source, "project-overview", "project_review")
	if err != nil {
		return markerBlock{}, err
	}
	blocks, err := scanMarkerBlocks(source, identity.bodyStart)
	if err != nil {
		return markerBlock{}, err
	}
	for _, block := range blocks {
		if block.kind == kind && block.id == id {
			return block, nil
		}
	}
	return markerBlock{}, fmt.Errorf("%s marker %q does not exist", kind, id)
}

func reorderHistoryMarkerBlocks(source []byte, ordered []Event) ([]byte, error) {
	identity, err := parseFrontmatter(source, "project-history", "project_history")
	if err != nil {
		return nil, err
	}
	blocks, err := scanMarkerBlocks(source, identity.bodyStart)
	if err != nil || len(blocks) < 2 {
		return source, err
	}
	byID := make(map[string]markerBlock, len(blocks))
	for _, block := range blocks {
		byID[block.id] = block
	}
	if len(byID) != len(ordered) {
		return nil, errors.New("history marker count does not match desired events")
	}
	result := append([]byte(nil), source[:blocks[0].whole.start]...)
	for index, event := range ordered {
		block, exists := byID[event.ID]
		if !exists {
			return nil, fmt.Errorf("history marker %q is missing", event.ID)
		}
		result = append(result, source[block.whole.start:block.whole.end]...)
		gapStart := blocks[index].whole.end
		gapEnd := len(source)
		if index+1 < len(blocks) {
			gapEnd = blocks[index+1].whole.start
		}
		result = append(result, source[gapStart:gapEnd]...)
	}
	return result, nil
}

func reorderReviewMarkerBlocks(source []byte, kind string, orderedIDs []string) ([]byte, error) {
	identity, err := parseFrontmatter(source, "project-overview", "project_review")
	if err != nil {
		return nil, err
	}
	containers, err := reviewMarkerContainers(source, identity.bodyStart)
	if err != nil {
		return nil, err
	}
	container, exists := containers[kind]
	if !exists {
		return nil, fmt.Errorf("review has no %s marker container", kind)
	}
	allBlocks, err := scanMarkerBlocks(source, identity.bodyStart)
	if err != nil {
		return nil, err
	}
	blocks := make([]markerBlock, 0, len(orderedIDs))
	byID := make(map[string]markerBlock, len(orderedIDs))
	for _, block := range allBlocks {
		if block.kind == kind && spanContains(container, block.whole) {
			blocks = append(blocks, block)
			byID[block.id] = block
		}
	}
	if len(blocks) != len(orderedIDs) || len(byID) != len(orderedIDs) {
		return nil, fmt.Errorf("review %s marker count does not match desired order", kind)
	}
	if len(blocks) < 2 {
		return source, nil
	}
	var body bytes.Buffer
	body.Write(source[container.start:blocks[0].whole.start])
	for index, id := range orderedIDs {
		block, exists := byID[id]
		if !exists {
			return nil, fmt.Errorf("review %s marker %q is missing", kind, id)
		}
		body.Write(source[block.whole.start:block.whole.end])
		gapStart := blocks[index].whole.end
		gapEnd := container.end
		if index+1 < len(blocks) {
			gapEnd = blocks[index+1].whole.start
		}
		body.Write(source[gapStart:gapEnd])
	}
	return spliceSource(source, container, body.Bytes()), nil
}

func reviewRiskBlock(value Risk) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "<!-- session-reviewer:risk id=\"%s\" -->\n### %s\n", value.ID, strings.TrimSpace(value.Title))
	writeReviewSubsection(&out, "状态", value.Status)
	writeReviewSubsection(&out, "详情", value.Detail)
	out.WriteString("<!-- /session-reviewer:risk -->\n\n")
	return out.Bytes()
}

func reviewDecisionBlock(value Decision) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "<!-- session-reviewer:decision id=\"%s\" -->\n### %s\n", value.ID, strings.TrimSpace(value.Title))
	if value.OccurredAt != "" {
		writeReviewSubsection(&out, "日期", value.OccurredAt)
	}
	writeReviewSubsection(&out, "原因", value.Rationale)
	writeReviewSubsection(&out, "影响", value.Impact)
	if value.Status != "" {
		writeReviewSubsection(&out, "状态", value.Status)
	}
	out.WriteString("<!-- /session-reviewer:decision -->\n\n")
	return out.Bytes()
}

func historyEventBlock(value Event) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "<!-- session-reviewer:event id=\"%s\" -->\n## %s · %s\n", value.ID, value.OccurredAt, strings.TrimSpace(value.Title))
	writeHistorySubsection(&out, "事件类别", value.Kind)
	writeHistorySubsection(&out, "节点意义", value.Meaning)
	writeHistorySubsection(&out, "摘要", value.Summary)
	writeHistorySubsection(&out, "为什么会走到这里", value.Why)
	writeHistoryList(&out, "发生了什么", value.Changes)
	writeHistoryList(&out, "结果与验证", value.Results)
	if len(value.DecisionIDs) != 0 {
		writeHistoryList(&out, "关联决策", value.DecisionIDs)
	}
	writeHistorySubsection(&out, "留下的问题或下一步", value.Next)
	out.WriteString("<!-- /session-reviewer:event -->\n\n")
	return out.Bytes()
}

func historyListSection(heading string, values []string) []byte {
	var out bytes.Buffer
	writeHistoryList(&out, heading, values)
	return out.Bytes()
}

func riskByID(values []Risk) map[string]Risk {
	result := make(map[string]Risk, len(values))
	for _, value := range values {
		result[value.ID] = value
	}
	return result
}
func decisionByID(values []Decision) map[string]Decision {
	result := make(map[string]Decision, len(values))
	for _, value := range values {
		result[value.ID] = value
	}
	return result
}
func eventByID(values []Event) map[string]Event {
	result := make(map[string]Event, len(values))
	for _, value := range values {
		result[value.ID] = value
	}
	return result
}

func equalReviewSemantics(left, right Review) bool {
	return left.ProjectID == right.ProjectID && left.Revision == right.Revision && left.Name == right.Name &&
		left.Goal == right.Goal && left.Stage == right.Stage && left.Status == right.Status &&
		left.NextAction == right.NextAction && left.LastVerification == right.LastVerification &&
		reflect.DeepEqual(riskByID(left.Risks), riskByID(right.Risks)) && reflect.DeepEqual(decisionByID(left.Decisions), decisionByID(right.Decisions))
}
