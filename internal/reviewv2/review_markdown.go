package reviewv2

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

type EditUnit struct {
	Document       string
	UnitID         string
	Field          string
	Value          string
	ExpectedSHA256 string
}

type ReviewDocument struct {
	Model  Review
	source []byte
	fields map[semanticField]sourceSpan
	blocks map[string]markerBlock
}

func ParseReview(source []byte) (ReviewDocument, error) {
	if err := validateDocumentSize("review", source); err != nil {
		return ReviewDocument{}, err
	}
	source = normalizeMarkdownSource(bytes.Clone(source))
	identity, err := parseFrontmatter(source, "project-overview", "project_review")
	if err != nil {
		return ReviewDocument{}, fmt.Errorf("parse review: %w", err)
	}
	blocks, err := scanMarkerBlocks(source, identity.bodyStart)
	if err != nil {
		return ReviewDocument{}, fmt.Errorf("parse review markers: %w", err)
	}
	firstControlled, err := firstReviewControlledStart(source, identity.bodyStart, blocks)
	if err != nil {
		return ReviewDocument{}, err
	}
	name, err := strictDocumentRootTitle(source, identity.bodyStart, firstControlled, "")
	if err != nil {
		return ReviewDocument{}, fmt.Errorf("parse review: %w", err)
	}
	document := ReviewDocument{
		Model: Review{
			ProjectID: identity.projectID, Revision: identity.revision, Name: name,
			GenerationID: identity.generationID, MinimumWriterVersion: identity.minimumWriterVersion,
		},
		source: source,
		fields: make(map[semanticField]sourceSpan),
		blocks: make(map[string]markerBlock),
	}
	if err := document.parseTopLevelFields(identity.bodyStart); err != nil {
		return ReviewDocument{}, err
	}
	containers, err := reviewMarkerContainers(source, identity.bodyStart)
	if err != nil {
		return ReviewDocument{}, err
	}
	for _, block := range blocks {
		document.blocks[block.id] = block
		switch block.kind {
		case "risk":
			if !spanContains(containers["risk"], block.whole) {
				return ReviewDocument{}, fmt.Errorf("risk marker %q is outside the 风险与待办 section", block.id)
			}
			risk, spans, err := parseRiskBlock(source, block)
			if err != nil {
				return ReviewDocument{}, err
			}
			document.Model.Risks = append(document.Model.Risks, risk)
			for field, span := range spans {
				document.fields[semanticField{unitID: risk.ID, field: field}] = span
			}
		case "decision":
			if !spanContains(containers["decision"], block.whole) {
				return ReviewDocument{}, fmt.Errorf("decision marker %q is outside the 关键决策 section", block.id)
			}
			decision, spans, err := parseDecisionBlock(source, block)
			if err != nil {
				return ReviewDocument{}, err
			}
			document.Model.Decisions = append(document.Model.Decisions, decision)
			for field, span := range spans {
				document.fields[semanticField{unitID: decision.ID, field: field}] = span
			}
		case "event":
			return ReviewDocument{}, errors.New("review document cannot contain event marker blocks")
		}
	}
	return document, nil
}

func firstReviewControlledStart(source []byte, bodyStart int, blocks []markerBlock) (int, error) {
	first := firstMarkerStart(blocks, len(source))
	headings, err := markdownHeadings(source[bodyStart:], bodyStart)
	if err != nil {
		return 0, fmt.Errorf("parse review headings: %w", err)
	}
	controlled := map[string]struct{}{
		"项目目标": {}, "当前阶段": {}, "当前状态": {}, "下一步": {},
		"风险与待办": {}, "关键决策": {}, "最近验证": {},
	}
	for _, heading := range headings {
		if heading.level != 2 {
			continue
		}
		if _, ok := controlled[heading.name]; ok && heading.start < first {
			first = heading.start
		}
	}
	return first, nil
}

func reviewMarkerContainers(source []byte, bodyStart int) (map[string]sourceSpan, error) {
	headings, err := markdownHeadings(source[bodyStart:], bodyStart)
	if err != nil {
		return nil, fmt.Errorf("parse review marker containers: %w", err)
	}
	containers := make(map[string]sourceSpan, 2)
	for index, heading := range headings {
		if heading.level != 2 {
			continue
		}
		kind := ""
		switch heading.name {
		case "风险与待办":
			kind = "risk"
		case "关键决策":
			kind = "decision"
		}
		if kind == "" {
			continue
		}
		if _, duplicate := containers[kind]; duplicate {
			return nil, fmt.Errorf("parse review: duplicate %s marker container", kind)
		}
		containers[kind] = headingBodySpan(headings, index, len(source))
	}
	return containers, nil
}

func spanContains(container, child sourceSpan) bool {
	return container.end > container.start && child.start >= container.start && child.end <= container.end
}

func (document ReviewDocument) Render() ([]byte, error) {
	if _, err := ParseReview(document.source); err != nil {
		return nil, fmt.Errorf("render review: %w", err)
	}
	return bytes.Clone(document.source), nil
}

func RenderReview(value Review) ([]byte, error) {
	if !validStableID(value.ProjectID) || !strings.HasPrefix(value.ProjectID, "project-") {
		return nil, errors.New("render review: invalid project ID")
	}
	if value.Revision < 1 {
		return nil, errors.New("render review: revision must be positive")
	}
	value.Name = strings.TrimSpace(value.Name)
	if err := validateHeadingValue("project name", value.Name); err != nil {
		return nil, err
	}
	if len(value.Risks) == 0 {
		value.Risks = nil
	}
	if len(value.Decisions) == 0 {
		value.Decisions = nil
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, "---\nid: project-overview\nentity_type: project_review\nproject_id: %s\nschema_version: %d\nrevision: %d\n---\n", value.ProjectID, LegacySchemaVersion, value.Revision)
	fmt.Fprintf(&out, "# %s\n\n", strings.TrimSpace(value.Name))
	writeReviewSection(&out, "项目目标", value.Goal)
	writeReviewSection(&out, "当前阶段", value.Stage)
	writeReviewSection(&out, "当前状态", value.Status)
	writeReviewSection(&out, "下一步", value.NextAction)
	out.WriteString("## 风险与待办\n")
	for _, risk := range value.Risks {
		if !validStableID(risk.ID) {
			return nil, fmt.Errorf("render review: invalid risk ID %q", risk.ID)
		}
		if err := validateHeadingValue("risk title", risk.Title); err != nil {
			return nil, err
		}
		fmt.Fprintf(&out, "<!-- session-reviewer:risk id=\"%s\" -->\n### %s\n", risk.ID, strings.TrimSpace(risk.Title))
		writeReviewSubsection(&out, "状态", risk.Status)
		writeReviewSubsection(&out, "详情", risk.Detail)
		out.WriteString("<!-- /session-reviewer:risk -->\n\n")
	}
	if len(value.Risks) == 0 {
		out.WriteByte('\n')
	}
	out.WriteString("## 关键决策\n")
	for _, decision := range value.Decisions {
		if !validStableID(decision.ID) {
			return nil, fmt.Errorf("render review: invalid decision ID %q", decision.ID)
		}
		if err := validateHeadingValue("decision title", decision.Title); err != nil {
			return nil, err
		}
		fmt.Fprintf(&out, "<!-- session-reviewer:decision id=\"%s\" -->\n### %s\n", decision.ID, strings.TrimSpace(decision.Title))
		if decision.OccurredAt != "" {
			writeReviewSubsection(&out, "日期", decision.OccurredAt)
		}
		writeReviewSubsection(&out, "原因", decision.Rationale)
		writeReviewSubsection(&out, "影响", decision.Impact)
		if decision.Status != "" {
			writeReviewSubsection(&out, "状态", decision.Status)
		}
		out.WriteString("<!-- /session-reviewer:decision -->\n\n")
	}
	if len(value.Decisions) == 0 {
		out.WriteByte('\n')
	}
	writeReviewSection(&out, "最近验证", value.LastVerification)
	out.WriteString("## 项目历史\n[打开完整项目历史](./项目历史.md)\n")

	if out.Len() > MaxDocumentBytes {
		return nil, fmt.Errorf("render review: document exceeds %d bytes", MaxDocumentBytes)
	}
	rendered := out.Bytes()
	document, err := ParseReview(rendered)
	if err != nil {
		return nil, fmt.Errorf("render review: generated Markdown is invalid: %w", err)
	}
	expected := value
	expected.GenerationID = document.Model.GenerationID
	expected.MinimumWriterVersion = document.Model.MinimumWriterVersion
	if !reflect.DeepEqual(document.Model, expected) {
		return nil, errors.New("render review: generated Markdown changed semantic fields")
	}
	return bytes.Clone(rendered), nil
}

func PatchReviewUnit(source []byte, edit EditUnit) ([]byte, error) {
	allowed := map[string]bool{
		"goal": true, "stage": true, "status": true, "next_action": true,
		"risk.title": true, "risk.status": true, "risk.detail": true,
		"decision.title": true, "decision.rationale": true, "decision.impact": true,
	}
	if !allowed[edit.Field] {
		return nil, fmt.Errorf("review field %q is not editable", edit.Field)
	}
	if !isReviewDocumentName(edit.Document) {
		return nil, fmt.Errorf("edit document %q is not the review document", edit.Document)
	}
	if err := verifyEditHash(source, edit.ExpectedSHA256); err != nil {
		return nil, err
	}
	document, err := ParseReview(source)
	if err != nil {
		return nil, err
	}
	span, exists := document.fields[semanticField{unitID: edit.UnitID, field: edit.Field}]
	if !exists {
		return nil, fmt.Errorf("review unit %q has no editable field %q", edit.UnitID, edit.Field)
	}
	var replacement []byte
	if edit.Field == "risk.title" {
		if err := validateHeadingValue("risk title", edit.Value); err != nil {
			return nil, err
		}
		replacement = []byte("### " + strings.TrimSpace(edit.Value) + "\n")
	} else if edit.Field == "decision.title" {
		if err := validateHeadingValue("decision title", edit.Value); err != nil {
			return nil, err
		}
		replacement = []byte("### " + strings.TrimSpace(edit.Value) + "\n")
	} else {
		replacement = replaceFieldBody(document.source[span.start:span.end], edit.Value, false)
	}
	patched := spliceSource(document.source, span, replacement)
	reparsed, err := ParseReview(patched)
	if err != nil {
		return nil, fmt.Errorf("review patch cannot be reparsed: %w", err)
	}
	if !reviewFieldMatches(reparsed.Model, edit) {
		return nil, errors.New("review patch did not preserve the requested field value")
	}
	return patched, nil
}

func writeReviewSection(out *bytes.Buffer, heading, value string) {
	fmt.Fprintf(out, "## %s\n%s\n\n", heading, strings.TrimSpace(value))
}

func writeReviewSubsection(out *bytes.Buffer, heading, value string) {
	fmt.Fprintf(out, "#### %s\n%s\n", heading, strings.TrimSpace(value))
}

func isReviewDocumentName(value string) bool {
	return value == "review" || value == "project_review" || value == ReviewRelativePath
}

func verifyEditHash(source []byte, expected string) error {
	if !lowercaseSHA256.MatchString(expected) {
		return errors.New("expected SHA-256 must be 64 lower-case hexadecimal characters")
	}
	digest := sha256.Sum256(source)
	if fmt.Sprintf("%x", digest) != expected {
		return errors.New("stale Markdown edit: expected SHA-256 does not match current bytes")
	}
	return nil
}

func validateHeadingValue(label, value string) error {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s must be one non-empty line", label)
	}
	for _, char := range value {
		if char < 0x20 && char != '\t' {
			return fmt.Errorf("%s contains a control character", label)
		}
	}
	return nil
}

func replaceFieldBody(original []byte, value string, list bool) []byte {
	leading := 0
	for leading < len(original) && original[leading] == '\n' {
		leading++
	}
	trailing := len(original)
	for trailing > leading && original[trailing-1] == '\n' {
		trailing--
	}
	var body string
	if list {
		items := parseMarkdownList(value)
		lines := make([]string, len(items))
		for index, item := range items {
			lines[index] = "- " + item
		}
		body = strings.Join(lines, "\n")
	} else {
		body = strings.TrimSpace(normalizeMarkdownText(value))
	}
	result := make([]byte, 0, leading+len(body)+len(original)-trailing)
	result = append(result, original[:leading]...)
	result = append(result, body...)
	result = append(result, original[trailing:]...)
	return result
}

func normalizeMarkdownText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func spliceSource(source []byte, span sourceSpan, replacement []byte) []byte {
	result := make([]byte, 0, len(source)-(span.end-span.start)+len(replacement))
	result = append(result, source[:span.start]...)
	result = append(result, replacement...)
	result = append(result, source[span.end:]...)
	return result
}

func reviewFieldMatches(value Review, edit EditUnit) bool {
	want := strings.TrimSpace(normalizeMarkdownText(edit.Value))
	switch edit.Field {
	case "goal":
		return edit.UnitID == "project-overview" && value.Goal == want
	case "stage":
		return edit.UnitID == "project-overview" && value.Stage == want
	case "status":
		return edit.UnitID == "project-overview" && value.Status == want
	case "next_action":
		return edit.UnitID == "project-overview" && value.NextAction == want
	}
	for _, risk := range value.Risks {
		if risk.ID != edit.UnitID {
			continue
		}
		switch edit.Field {
		case "risk.title":
			return risk.Title == want
		case "risk.status":
			return risk.Status == want
		case "risk.detail":
			return risk.Detail == want
		}
	}
	for _, decision := range value.Decisions {
		if decision.ID != edit.UnitID {
			continue
		}
		switch edit.Field {
		case "decision.title":
			return decision.Title == want
		case "decision.rationale":
			return decision.Rationale == want
		case "decision.impact":
			return decision.Impact == want
		}
	}
	return false
}

func (document *ReviewDocument) parseTopLevelFields(bodyStart int) error {
	headings, err := markdownHeadings(document.source[bodyStart:], bodyStart)
	if err != nil {
		return fmt.Errorf("parse review headings: %w", err)
	}
	type target struct {
		field string
		set   func(string)
	}
	targets := map[string]target{
		"项目目标": {"goal", func(value string) { document.Model.Goal = value }},
		"当前阶段": {"stage", func(value string) { document.Model.Stage = value }},
		"当前状态": {"status", func(value string) { document.Model.Status = value }},
		"下一步":  {"next_action", func(value string) { document.Model.NextAction = value }},
		"最近验证": {"last_verification", func(value string) { document.Model.LastVerification = value }},
	}
	seen := make(map[string]struct{}, len(targets))
	for index, heading := range headings {
		if heading.level != 2 {
			continue
		}
		entry, controlled := targets[heading.name]
		if !controlled {
			continue
		}
		if _, duplicate := seen[entry.field]; duplicate {
			return fmt.Errorf("parse review: duplicate controlled section %q", heading.name)
		}
		seen[entry.field] = struct{}{}
		span := headingBodySpan(headings, index, len(document.source))
		entry.set(cleanMarkdownText(document.source, span))
		document.fields[semanticField{unitID: "project-overview", field: entry.field}] = span
	}
	for _, required := range []string{"goal", "stage", "status", "next_action", "last_verification"} {
		if _, ok := seen[required]; !ok {
			return fmt.Errorf("parse review: missing controlled field %q", required)
		}
	}
	return nil
}

func parseRiskBlock(source []byte, block markerBlock) (Risk, map[string]sourceSpan, error) {
	headings, err := markdownHeadings(source[block.body.start:block.body.end], block.body.start)
	if err != nil || len(headings) == 0 || headings[0].level != 3 {
		return Risk{}, nil, fmt.Errorf("risk %q must begin with one level-three title", block.id)
	}
	risk := Risk{ID: block.id, Title: headings[0].name}
	spans := map[string]sourceSpan{"risk.title": {start: headings[0].start, end: headings[0].lineEnd}}
	seen := make(map[string]struct{})
	for index := 1; index < len(headings); index++ {
		heading := headings[index]
		if heading.level != 4 {
			continue
		}
		span := headingBodySpan(headings, index, block.body.end)
		switch heading.name {
		case "状态":
			if _, duplicate := seen["status"]; duplicate {
				return Risk{}, nil, fmt.Errorf("risk %q has duplicate status subsection", block.id)
			}
			seen["status"] = struct{}{}
			risk.Status = cleanMarkdownText(source, span)
			spans["risk.status"] = span
		case "详情":
			if _, duplicate := seen["detail"]; duplicate {
				return Risk{}, nil, fmt.Errorf("risk %q has duplicate detail subsection", block.id)
			}
			seen["detail"] = struct{}{}
			risk.Detail = cleanMarkdownText(source, span)
			spans["risk.detail"] = span
		}
	}
	if risk.Status == "" || risk.Detail == "" {
		return Risk{}, nil, fmt.Errorf("risk %q is missing status or detail", block.id)
	}
	return risk, spans, nil
}

func parseDecisionBlock(source []byte, block markerBlock) (Decision, map[string]sourceSpan, error) {
	headings, err := markdownHeadings(source[block.body.start:block.body.end], block.body.start)
	if err != nil || len(headings) == 0 || headings[0].level != 3 {
		return Decision{}, nil, fmt.Errorf("decision %q must begin with one level-three title", block.id)
	}
	decision := Decision{ID: block.id, Title: headings[0].name}
	spans := map[string]sourceSpan{"decision.title": {start: headings[0].start, end: headings[0].lineEnd}}
	seen := make(map[string]struct{})
	for index := 1; index < len(headings); index++ {
		heading := headings[index]
		if heading.level != 4 {
			continue
		}
		span := headingBodySpan(headings, index, block.body.end)
		field := ""
		switch heading.name {
		case "日期":
			field, decision.OccurredAt = "occurred_at", cleanMarkdownText(source, span)
			spans["decision.occurred_at"] = span
		case "原因":
			field, decision.Rationale = "rationale", cleanMarkdownText(source, span)
			spans["decision.rationale"] = span
		case "影响":
			field, decision.Impact = "impact", cleanMarkdownText(source, span)
			spans["decision.impact"] = span
		case "状态":
			field, decision.Status = "status", cleanMarkdownText(source, span)
			spans["decision.status"] = span
		}
		if field != "" {
			if _, duplicate := seen[field]; duplicate {
				return Decision{}, nil, fmt.Errorf("decision %q has duplicate %s subsection", block.id, field)
			}
			seen[field] = struct{}{}
		}
	}
	if decision.Rationale == "" || decision.Impact == "" {
		return Decision{}, nil, fmt.Errorf("decision %q is missing rationale or impact", block.id)
	}
	return decision, spans, nil
}
