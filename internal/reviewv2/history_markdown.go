package reviewv2

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

type HistoryDocument struct {
	ProjectID string
	Revision  int
	Events    []Event
	source    []byte
	fields    map[semanticField]sourceSpan
	blocks    map[string]markerBlock
}

func ParseHistory(source []byte) (HistoryDocument, error) {
	if err := validateDocumentSize("history", source); err != nil {
		return HistoryDocument{}, err
	}
	source = normalizeMarkdownSource(bytes.Clone(source))
	identity, err := parseFrontmatter(source, "project-history", "project_history")
	if err != nil {
		return HistoryDocument{}, fmt.Errorf("parse history: %w", err)
	}
	blocks, err := scanMarkerBlocks(source, identity.bodyStart)
	if err != nil {
		return HistoryDocument{}, fmt.Errorf("parse history markers: %w", err)
	}
	if _, err := strictDocumentRootTitle(source, identity.bodyStart, firstMarkerStart(blocks, len(source)), "项目历史"); err != nil {
		return HistoryDocument{}, fmt.Errorf("parse history: %w", err)
	}
	document := HistoryDocument{
		ProjectID: identity.projectID,
		Revision:  identity.revision,
		source:    source,
		fields:    make(map[semanticField]sourceSpan),
		blocks:    make(map[string]markerBlock),
	}
	for _, block := range blocks {
		if block.kind != "event" {
			return HistoryDocument{}, errors.New("history document can contain only event marker blocks")
		}
		event, spans, err := parseEventBlock(source, block)
		if err != nil {
			return HistoryDocument{}, err
		}
		document.Events = append(document.Events, event)
		document.blocks[event.ID] = block
		for field, span := range spans {
			document.fields[semanticField{unitID: event.ID, field: field}] = span
		}
	}
	if err := validateHistoryOrder(document.Events); err != nil {
		return HistoryDocument{}, err
	}
	return document, nil
}

func (document HistoryDocument) Render() ([]byte, error) {
	if _, err := ParseHistory(document.source); err != nil {
		return nil, fmt.Errorf("render history: %w", err)
	}
	return bytes.Clone(document.source), nil
}

func RenderHistory(projectID string, revision int, events []Event) ([]byte, error) {
	if !validStableID(projectID) || !strings.HasPrefix(projectID, "project-") {
		return nil, errors.New("render history: invalid project ID")
	}
	if revision < 1 {
		return nil, errors.New("render history: revision must be positive")
	}
	ordered, err := canonicalHistoryEvents(events)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "---\nid: project-history\nentity_type: project_history\nproject_id: %s\nschema_version: %d\nrevision: %d\n---\n# 项目历史\n\n> 按时间逆序排列。\n\n", projectID, SchemaVersion, revision)
	for _, event := range ordered {
		if !validStableID(event.ID) {
			return nil, fmt.Errorf("render history: invalid event ID %q", event.ID)
		}
		if err := validateHeadingValue("event title", event.Title); err != nil {
			return nil, err
		}
		fmt.Fprintf(&out, "<!-- session-reviewer:event id=\"%s\" -->\n## %s · %s\n", event.ID, strings.TrimSpace(event.OccurredAt), strings.TrimSpace(event.Title))
		writeHistorySubsection(&out, "事件类别", event.Kind)
		writeHistorySubsection(&out, "节点意义", event.Meaning)
		writeHistorySubsection(&out, "摘要", event.Summary)
		writeHistorySubsection(&out, "为什么会走到这里", event.Why)
		writeHistoryList(&out, "发生了什么", event.Changes)
		writeHistoryList(&out, "结果与验证", event.Results)
		if len(event.DecisionIDs) != 0 {
			writeHistoryList(&out, "关联决策", event.DecisionIDs)
		}
		writeHistorySubsection(&out, "留下的问题或下一步", event.Next)
		out.WriteString("<!-- /session-reviewer:event -->\n\n")
	}
	if out.Len() > MaxDocumentBytes {
		return nil, fmt.Errorf("render history: document exceeds %d bytes", MaxDocumentBytes)
	}
	rendered := bytes.TrimSuffix(out.Bytes(), []byte("\n"))
	rendered = append(rendered, '\n')
	document, err := ParseHistory(rendered)
	if err != nil {
		return nil, fmt.Errorf("render history: generated Markdown is invalid: %w", err)
	}
	if !reflect.DeepEqual(normalizedEvents(document.Events), normalizedEvents(ordered)) || document.ProjectID != projectID || document.Revision != revision {
		return nil, errors.New("render history: generated Markdown changed semantic fields")
	}
	return bytes.Clone(rendered), nil
}

type timedHistoryEvent struct {
	event Event
	time  time.Time
}

func canonicalHistoryEvents(events []Event) ([]Event, error) {
	if len(events) == 0 {
		return nil, nil
	}
	timed := make([]timedHistoryEvent, len(events))
	for index, event := range events {
		occurred, err := parseEventOccurredAt(event.OccurredAt)
		if err != nil {
			return nil, fmt.Errorf("render history: event %q occurred_at: %w", event.ID, err)
		}
		timed[index] = timedHistoryEvent{event: event, time: occurred}
	}
	sort.Slice(timed, func(left, right int) bool {
		if !timed[left].time.Equal(timed[right].time) {
			return timed[left].time.After(timed[right].time)
		}
		return timed[left].event.ID < timed[right].event.ID
	})
	ordered := make([]Event, len(timed))
	for index := range timed {
		ordered[index] = timed[index].event
	}
	return ordered, nil
}

func validateHistoryOrder(events []Event) error {
	if len(events) < 2 {
		return nil
	}
	previousTime, err := parseEventOccurredAt(events[0].OccurredAt)
	if err != nil {
		return fmt.Errorf("event %q occurred_at: %w", events[0].ID, err)
	}
	for index := 1; index < len(events); index++ {
		currentTime, err := parseEventOccurredAt(events[index].OccurredAt)
		if err != nil {
			return fmt.Errorf("event %q occurred_at: %w", events[index].ID, err)
		}
		if previousTime.Before(currentTime) {
			return fmt.Errorf("history events are not in reverse chronological order at %q", events[index].ID)
		}
		if previousTime.Equal(currentTime) && events[index-1].ID > events[index].ID {
			return fmt.Errorf("history events at the same time are not ordered by stable ID at %q", events[index].ID)
		}
		previousTime = currentTime
	}
	return nil
}

func parseEventOccurredAt(value string) (time.Time, error) {
	if value != strings.TrimSpace(value) || value == "" || strings.ContainsAny(value, "\r\n") {
		return time.Time{}, errors.New("must be YYYY-MM-DD or RFC3339Nano")
	}
	if len(value) == len("2006-01-02") {
		parsed, err := time.Parse("2006-01-02", value)
		if err == nil {
			return parsed, nil
		}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errors.New("must be YYYY-MM-DD or RFC3339Nano")
	}
	return parsed, nil
}

func normalizedEvents(events []Event) []Event {
	if len(events) == 0 {
		return nil
	}
	result := append([]Event(nil), events...)
	for index := range result {
		if len(result[index].Changes) == 0 {
			result[index].Changes = nil
		}
		if len(result[index].Results) == 0 {
			result[index].Results = nil
		}
		if len(result[index].DecisionIDs) == 0 {
			result[index].DecisionIDs = nil
		}
	}
	return result
}

func PatchHistoryUnit(source []byte, edit EditUnit) ([]byte, error) {
	allowed := map[string]bool{
		"event.title": true, "event.meaning": true, "event.summary": true, "event.why": true,
		"event.changes": true, "event.results": true, "event.next": true,
	}
	if !allowed[edit.Field] {
		return nil, fmt.Errorf("history field %q is not editable", edit.Field)
	}
	if !isHistoryDocumentName(edit.Document) {
		return nil, fmt.Errorf("edit document %q is not the history document", edit.Document)
	}
	if err := verifyEditHash(source, edit.ExpectedSHA256); err != nil {
		return nil, err
	}
	document, err := ParseHistory(source)
	if err != nil {
		return nil, err
	}
	span, exists := document.fields[semanticField{unitID: edit.UnitID, field: edit.Field}]
	if !exists {
		if edit.Field != "event.summary" {
			return nil, fmt.Errorf("history unit %q has no editable field %q", edit.UnitID, edit.Field)
		}
		block, ok := document.blocks[edit.UnitID]
		if !ok {
			return nil, fmt.Errorf("history event %q does not exist", edit.UnitID)
		}
		insertAt, err := eventSummaryInsertionPoint(document.source, block)
		if err != nil {
			return nil, err
		}
		span = sourceSpan{start: insertAt, end: insertAt}
	}
	var replacement []byte
	if edit.Field == "event.title" {
		if err := validateHeadingValue("event title", edit.Value); err != nil {
			return nil, err
		}
		event, ok := historyEvent(document.Events, edit.UnitID)
		if !ok {
			return nil, fmt.Errorf("history event %q does not exist", edit.UnitID)
		}
		replacement = []byte("## " + event.OccurredAt + " · " + strings.TrimSpace(edit.Value) + "\n")
	} else if edit.Field == "event.summary" && span.start == span.end {
		replacement = []byte("### 摘要\n" + strings.TrimSpace(normalizeMarkdownText(edit.Value)) + "\n")
	} else {
		list := edit.Field == "event.changes" || edit.Field == "event.results"
		replacement = replaceFieldBody(document.source[span.start:span.end], edit.Value, list)
	}
	patched := spliceSource(document.source, span, replacement)
	reparsed, err := ParseHistory(patched)
	if err != nil {
		return nil, fmt.Errorf("history patch cannot be reparsed: %w", err)
	}
	if !historyFieldMatches(reparsed.Events, edit) {
		return nil, errors.New("history patch did not preserve the requested field value")
	}
	return patched, nil
}

func eventSummaryInsertionPoint(source []byte, block markerBlock) (int, error) {
	headings, err := markdownHeadings(source[block.body.start:block.body.end], block.body.start)
	if err != nil {
		return 0, err
	}
	for _, heading := range headings {
		if heading.level == 3 && heading.name == "为什么会走到这里" {
			return heading.start, nil
		}
	}
	return 0, fmt.Errorf("event %q has no stable summary insertion point", block.id)
}

func writeHistorySubsection(out *bytes.Buffer, heading, value string) {
	fmt.Fprintf(out, "### %s\n%s\n", heading, strings.TrimSpace(value))
}

func writeHistoryList(out *bytes.Buffer, heading string, values []string) {
	fmt.Fprintf(out, "### %s\n", heading)
	for _, value := range values {
		fmt.Fprintf(out, "- %s\n", strings.TrimSpace(value))
	}
}

func isHistoryDocumentName(value string) bool {
	return value == "history" || value == "project_history" || value == HistoryRelativePath
}

func historyEvent(events []Event, id string) (Event, bool) {
	for _, event := range events {
		if event.ID == id {
			return event, true
		}
	}
	return Event{}, false
}

func historyFieldMatches(events []Event, edit EditUnit) bool {
	event, ok := historyEvent(events, edit.UnitID)
	if !ok {
		return false
	}
	want := strings.TrimSpace(normalizeMarkdownText(edit.Value))
	switch edit.Field {
	case "event.title":
		return event.Title == want
	case "event.meaning":
		return event.Meaning == want
	case "event.summary":
		return event.Summary == want
	case "event.why":
		return event.Why == want
	case "event.next":
		return event.Next == want
	case "event.changes":
		return reflect.DeepEqual(event.Changes, parseMarkdownList(want))
	case "event.results":
		return reflect.DeepEqual(event.Results, parseMarkdownList(want))
	default:
		return false
	}
}

func parseEventBlock(source []byte, block markerBlock) (Event, map[string]sourceSpan, error) {
	headings, err := markdownHeadings(source[block.body.start:block.body.end], block.body.start)
	if err != nil || len(headings) == 0 || headings[0].level != 2 {
		return Event{}, nil, fmt.Errorf("event %q must begin with one level-two title", block.id)
	}
	occurredAt, title, ok := strings.Cut(headings[0].name, " · ")
	if !ok || strings.TrimSpace(occurredAt) == "" || strings.TrimSpace(title) == "" {
		return Event{}, nil, fmt.Errorf("event %q title must use date middle-dot title form", block.id)
	}
	event := Event{ID: block.id, OccurredAt: strings.TrimSpace(occurredAt), Title: strings.TrimSpace(title)}
	if _, err := parseEventOccurredAt(event.OccurredAt); err != nil {
		return Event{}, nil, fmt.Errorf("event %q occurred_at: %w", block.id, err)
	}
	spans := map[string]sourceSpan{"event.title": {start: headings[0].start, end: headings[0].lineEnd}}
	seen := make(map[string]struct{})
	for index := 1; index < len(headings); index++ {
		heading := headings[index]
		if heading.level != 3 {
			continue
		}
		span := headingBodySpan(headings, index, block.body.end)
		value := cleanMarkdownText(source, span)
		field := ""
		switch heading.name {
		case "事件类别":
			field, event.Kind = "kind", value
			spans["event.kind"] = span
		case "节点意义":
			field, event.Meaning = "meaning", value
			spans["event.meaning"] = span
		case "摘要":
			field, event.Summary = "summary", value
			spans["event.summary"] = span
		case "为什么会走到这里":
			field, event.Why = "why", value
			spans["event.why"] = span
		case "发生了什么":
			field, event.Changes = "changes", parseMarkdownList(value)
			spans["event.changes"] = span
		case "结果与验证":
			field, event.Results = "results", parseMarkdownList(value)
			spans["event.results"] = span
		case "关联决策":
			field, event.DecisionIDs = "decision_ids", parseMarkdownList(value)
			spans["event.decision_ids"] = span
		case "留下的问题或下一步":
			field, event.Next = "next", value
			spans["event.next"] = span
		}
		if field != "" {
			if _, duplicate := seen[field]; duplicate {
				return Event{}, nil, fmt.Errorf("event %q has duplicate %s subsection", block.id, field)
			}
			seen[field] = struct{}{}
		}
	}
	if event.Meaning == "" || event.Why == "" || len(event.Changes) == 0 || len(event.Results) == 0 || event.Next == "" {
		return Event{}, nil, fmt.Errorf("event %q is missing a required visible field", block.id)
	}
	return event, spans, nil
}
