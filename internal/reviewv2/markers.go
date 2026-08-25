package reviewv2

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"gopkg.in/yaml.v3"
)

const maxMarkerBlocks = 20_000

type sourceSpan struct {
	start int
	end   int
}

type semanticField struct {
	unitID string
	field  string
}

type markerBlock struct {
	kind  string
	id    string
	open  sourceSpan
	body  sourceSpan
	close sourceSpan
	whole sourceSpan
}

type frontmatterIdentity struct {
	projectID string
	revision  int
	bodyStart int
}

type markdownHeading struct {
	level     int
	name      string
	start     int
	lineEnd   int
	bodyStart int
}

func normalizeMarkdownSource(source []byte) []byte {
	source = bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(source, []byte("\r"), []byte("\n"))
}

func parseFrontmatter(source []byte, expectedID, expectedType string) (frontmatterIdentity, error) {
	if !bytes.HasPrefix(source, []byte("---\n")) {
		return frontmatterIdentity{}, errors.New("missing opening YAML frontmatter fence")
	}
	closeStart := -1
	closeEnd := -1
	for start := len("---\n"); start <= len(source); {
		end, next := physicalLine(source, start)
		if bytes.Equal(source[start:end], []byte("---")) {
			closeStart, closeEnd = start, next
			break
		}
		if next == len(source) {
			break
		}
		start = next
	}
	if closeStart < 0 {
		return frontmatterIdentity{}, errors.New("missing closing YAML frontmatter fence")
	}

	decoder := yaml.NewDecoder(bytes.NewReader(source[len("---\n"):closeStart]))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return frontmatterIdentity{}, fmt.Errorf("malformed YAML frontmatter: %w", err)
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		return frontmatterIdentity{}, errors.New("multiple YAML frontmatter documents are not allowed")
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return frontmatterIdentity{}, errors.New("YAML frontmatter must be one mapping")
	}
	mapping := document.Content[0]
	if err := validateFrontmatterMapping(mapping); err != nil {
		return frontmatterIdentity{}, err
	}
	id, err := requiredFrontmatterString(mapping, "id")
	if err != nil || id != expectedID {
		return frontmatterIdentity{}, fmt.Errorf("frontmatter id must be %q", expectedID)
	}
	entityType, err := requiredFrontmatterString(mapping, "entity_type")
	if err != nil || entityType != expectedType {
		return frontmatterIdentity{}, fmt.Errorf("frontmatter entity_type must be %q", expectedType)
	}
	projectID, err := requiredFrontmatterString(mapping, "project_id")
	if err != nil || !validStableID(projectID) || !strings.HasPrefix(projectID, "project-") {
		return frontmatterIdentity{}, errors.New("frontmatter project_id must be a stable lower-case project ID")
	}
	schema, err := requiredFrontmatterInt(mapping, "schema_version")
	if err != nil || schema != SchemaVersion {
		return frontmatterIdentity{}, fmt.Errorf("frontmatter schema_version must be %d", SchemaVersion)
	}
	revision, err := requiredFrontmatterInt(mapping, "revision")
	if err != nil || revision < 1 {
		return frontmatterIdentity{}, errors.New("frontmatter revision must be a positive integer")
	}
	return frontmatterIdentity{projectID: projectID, revision: revision, bodyStart: closeEnd}, nil
}

func validateFrontmatterMapping(mapping *yaml.Node) error {
	seen := make(map[string]struct{}, len(mapping.Content)/2)
	var visit func(*yaml.Node, int) error
	nodes := 0
	visit = func(node *yaml.Node, depth int) error {
		if node == nil || depth > 64 {
			return errors.New("YAML frontmatter exceeds structural limits")
		}
		nodes++
		if nodes > 20_000 || node.Kind == yaml.AliasNode || node.Alias != nil {
			return errors.New("YAML aliases or excessive structure are not allowed")
		}
		if node.Kind == yaml.MappingNode {
			if len(node.Content)%2 != 0 {
				return errors.New("malformed YAML mapping")
			}
			local := make(map[string]struct{}, len(node.Content)/2)
			for i := 0; i < len(node.Content); i += 2 {
				key := node.Content[i]
				if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "" || key.Value == "<<" {
					return errors.New("YAML mapping keys must be non-empty strings")
				}
				if _, duplicate := local[key.Value]; duplicate {
					return fmt.Errorf("duplicate YAML frontmatter key %q", key.Value)
				}
				local[key.Value] = struct{}{}
			}
		}
		for _, child := range node.Content {
			if err := visit(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(mapping, 1); err != nil {
		return err
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := mapping.Content[i].Value
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate YAML frontmatter key %q", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func frontmatterValue(mapping *yaml.Node, name string) (*yaml.Node, bool) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == name {
			return mapping.Content[i+1], true
		}
	}
	return nil, false
}

func requiredFrontmatterString(mapping *yaml.Node, name string) (string, error) {
	node, ok := frontmatterValue(mapping, name)
	if !ok || node.Kind != yaml.ScalarNode || node.Tag != "!!str" || strings.TrimSpace(node.Value) == "" {
		return "", fmt.Errorf("frontmatter %s must be a non-empty string", name)
	}
	return node.Value, nil
}

func requiredFrontmatterInt(mapping *yaml.Node, name string) (int, error) {
	node, ok := frontmatterValue(mapping, name)
	if !ok || node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return 0, fmt.Errorf("frontmatter %s must be an integer", name)
	}
	value, err := strconv.Atoi(node.Value)
	if err != nil {
		return 0, fmt.Errorf("frontmatter %s must be an integer", name)
	}
	return value, nil
}

func scanMarkerBlocks(source []byte, bodyStart int) ([]markerBlock, error) {
	fenced, err := fencedCodeSpans(source[bodyStart:], bodyStart)
	if err != nil {
		return nil, err
	}
	blocks := make([]markerBlock, 0)
	ids := make(map[string]struct{})
	var active *markerBlock
	fenceIndex := 0

	for start := bodyStart; start <= len(source); {
		end, next := physicalLine(source, start)
		line := source[start:end]
		for fenceIndex < len(fenced) && start >= fenced[fenceIndex].end {
			fenceIndex++
		}
		if fenceIndex < len(fenced) && start >= fenced[fenceIndex].start && start < fenced[fenceIndex].end {
			// Goldmark, rather than marker scanning heuristics, is authoritative
			// about whether this physical line belongs to Markdown fenced code.
		} else if kind, id, recognized, err := parseOpeningMarker(line); recognized {
			if err != nil {
				return nil, fmt.Errorf("invalid marker at byte %d: %w", start, err)
			}
			if active != nil {
				return nil, fmt.Errorf("nested %s marker inside %s %q", kind, active.kind, active.id)
			}
			if _, duplicate := ids[id]; duplicate {
				return nil, fmt.Errorf("duplicate marker identity %q", id)
			}
			active = &markerBlock{kind: kind, id: id, open: sourceSpan{start: start, end: next}, body: sourceSpan{start: next}}
		} else if kind, recognized, err := parseClosingMarker(line); recognized {
			if err != nil {
				return nil, fmt.Errorf("invalid closing marker at byte %d: %w", start, err)
			}
			if active == nil {
				return nil, fmt.Errorf("closing %s marker has no opening marker", kind)
			}
			if active.kind != kind {
				return nil, fmt.Errorf("closing %s marker does not match open %s marker", kind, active.kind)
			}
			active.body.end = start
			active.close = sourceSpan{start: start, end: next}
			active.whole = sourceSpan{start: active.open.start, end: next}
			ids[active.id] = struct{}{}
			blocks = append(blocks, *active)
			if len(blocks) > maxMarkerBlocks {
				return nil, fmt.Errorf("Markdown contains more than %d marker blocks", maxMarkerBlocks)
			}
			active = nil
		} else if reservedMarkerComment(line) {
			return nil, fmt.Errorf("reserved session-reviewer comment at byte %d is not an exact marker line", start)
		}
		if next == len(source) {
			break
		}
		start = next
	}
	if active != nil {
		return nil, fmt.Errorf("marker %s %q is missing its closing marker", active.kind, active.id)
	}
	return blocks, nil
}

func fencedCodeSpans(body []byte, offset int) ([]sourceSpan, error) {
	root := goldmark.DefaultParser().Parse(text.NewReader(body))
	spans := make([]sourceSpan, 0)
	err := ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		block, ok := node.(*ast.FencedCodeBlock)
		if !ok {
			return ast.WalkContinue, nil
		}
		openingStart, ok := fencedOpeningStart(body, block)
		if !ok {
			// An empty closed fence contains no marker-bearing line. Goldmark
			// exposes no source segment for its delimiter-only form.
			return ast.WalkContinue, nil
		}
		openingEnd, afterOpening := physicalLine(body, openingStart)
		marker, width, ok := fenceRun(body[openingStart:openingEnd])
		if !ok {
			return ast.WalkStop, errors.New("cannot locate Goldmark fenced-code opening delimiter")
		}
		closingStart := afterOpening
		if block.Lines().Len() != 0 {
			closingStart = block.Lines().At(block.Lines().Len() - 1).Stop
		}
		if closingStart >= len(body) {
			return ast.WalkStop, fmt.Errorf("unclosed fenced code at byte %d", offset+openingStart)
		}
		closingEnd, afterClosing := physicalLine(body, closingStart)
		if !closesFenceAnywhere(body[closingStart:closingEnd], marker, width) {
			return ast.WalkStop, fmt.Errorf("unclosed fenced code at byte %d", offset+openingStart)
		}
		spans = append(spans, sourceSpan{start: offset + openingStart, end: offset + afterClosing})
		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil, err
	}
	return spans, nil
}

func fencedOpeningStart(source []byte, block *ast.FencedCodeBlock) (int, bool) {
	if block.Info != nil {
		return physicalLineStartAt(source, block.Info.Segment.Start), true
	}
	if block.Lines().Len() == 0 {
		return 0, false
	}
	contentStart := physicalLineStartAt(source, block.Lines().At(0).Start)
	if contentStart == 0 {
		return 0, false
	}
	return previousPhysicalLineStart(source, contentStart), true
}

func physicalLineStartAt(source []byte, position int) int {
	if position < 0 {
		position = 0
	}
	if position > len(source) {
		position = len(source)
	}
	if index := bytes.LastIndexByte(source[:position], '\n'); index >= 0 {
		return index + 1
	}
	return 0
}

func previousPhysicalLineStart(source []byte, lineStart int) int {
	if lineStart <= 0 {
		return 0
	}
	position := lineStart - 1
	if position > 0 && source[position-1] == '\r' {
		position--
	}
	return physicalLineStartAt(source, position)
}

func fenceRun(line []byte) (byte, int, bool) {
	for index := 0; index < len(line); index++ {
		if line[index] != '`' && line[index] != '~' {
			continue
		}
		end := index
		for end < len(line) && line[end] == line[index] {
			end++
		}
		if end-index >= 3 {
			return line[index], end - index, true
		}
		index = end - 1
	}
	return 0, 0, false
}

func closesFenceAnywhere(line []byte, marker byte, minimum int) bool {
	for index := 0; index < len(line); index++ {
		if line[index] != marker {
			continue
		}
		end := index
		for end < len(line) && line[end] == marker {
			end++
		}
		if end-index < minimum {
			index = end - 1
			continue
		}
		for suffix := end; suffix < len(line); suffix++ {
			if line[suffix] != ' ' && line[suffix] != '\t' {
				return false
			}
		}
		return true
	}
	return false
}

func reservedMarkerComment(line []byte) bool {
	line = bytes.TrimSpace(line)
	return bytes.HasPrefix(line, []byte("<!--")) && bytes.Contains(line, []byte("session-reviewer:"))
}

func parseOpeningMarker(line []byte) (kind, id string, recognized bool, err error) {
	const prefix = "<!-- session-reviewer:"
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return "", "", false, nil
	}
	recognized = true
	rest := string(line[len(prefix):])
	kind, rest, ok := strings.Cut(rest, " id=\"")
	if !ok || !validMarkerKind(kind) || !strings.HasSuffix(rest, "\" -->") {
		return "", "", true, errors.New("opening marker must use the exact kind/id form")
	}
	id = strings.TrimSuffix(rest, "\" -->")
	if !validStableID(id) {
		return "", "", true, errors.New("marker ID must be stable lower-case kebab-case")
	}
	return kind, id, true, nil
}

func parseClosingMarker(line []byte) (kind string, recognized bool, err error) {
	const prefix = "<!-- /session-reviewer:"
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return "", false, nil
	}
	recognized = true
	rest := string(line[len(prefix):])
	if !strings.HasSuffix(rest, " -->") {
		return "", true, errors.New("closing marker must use the exact form")
	}
	kind = strings.TrimSuffix(rest, " -->")
	if !validMarkerKind(kind) {
		return "", true, errors.New("unknown marker kind")
	}
	return kind, true, nil
}

func validMarkerKind(kind string) bool {
	return kind == "risk" || kind == "decision" || kind == "event"
}

func validStableID(id string) bool {
	if len(id) < 3 || len(id) > 160 || id[0] < 'a' || id[0] > 'z' || id[len(id)-1] == '-' {
		return false
	}
	previousDash := false
	for _, char := range []byte(id) {
		if char == '-' {
			if previousDash {
				return false
			}
			previousDash = true
			continue
		}
		previousDash = false
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func physicalLine(source []byte, start int) (end, next int) {
	if start >= len(source) {
		return len(source), len(source)
	}
	if offset := bytes.IndexByte(source[start:], '\n'); offset >= 0 {
		return start + offset, start + offset + 1
	}
	return len(source), len(source)
}

func markdownHeadings(source []byte, offset int) ([]markdownHeading, error) {
	root := goldmark.DefaultParser().Parse(text.NewReader(source))
	headings := make([]markdownHeading, 0)
	for node := root.FirstChild(); node != nil; node = node.NextSibling() {
		heading, ok := node.(*ast.Heading)
		if !ok || heading.Lines().Len() == 0 {
			continue
		}
		line := heading.Lines().At(0)
		start := line.Start
		for start > 0 && source[start-1] != '\n' {
			start--
		}
		end, next := physicalLine(source, line.Stop)
		if !isATXHeadingLine(source[start:end], heading.Level) {
			return nil, errors.New("only ATX Markdown headings are allowed in structured fields")
		}
		name := strings.TrimSpace(strings.ReplaceAll(string(heading.Text(source)), "\n", " "))
		if name == "" {
			return nil, errors.New("structured Markdown heading cannot be empty")
		}
		headings = append(headings, markdownHeading{
			level: heading.Level, name: name, start: offset + start, lineEnd: offset + next, bodyStart: offset + next,
		})
	}
	return headings, nil
}

func isATXHeadingLine(line []byte, level int) bool {
	index := 0
	for index < len(line) && index < 3 && line[index] == ' ' {
		index++
	}
	for count := 0; count < level; count++ {
		if index >= len(line) || line[index] != '#' {
			return false
		}
		index++
	}
	if index < len(line) && line[index] == '#' {
		return false
	}
	return index == len(line) || line[index] == ' ' || line[index] == '\t'
}

func headingBodySpan(headings []markdownHeading, index, boundary int) sourceSpan {
	end := boundary
	for next := index + 1; next < len(headings); next++ {
		if headings[next].level <= headings[index].level {
			end = headings[next].start
			break
		}
	}
	return sourceSpan{start: headings[index].bodyStart, end: end}
}

func cleanMarkdownText(source []byte, span sourceSpan) string {
	return strings.TrimSpace(string(source[span.start:span.end]))
}

func parseMarkdownList(value string) []string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	items := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ") {
			line = strings.TrimSpace(line[2:])
		}
		if line != "" {
			items = append(items, line)
		}
	}
	return items
}

func strictDocumentRootTitle(source []byte, bodyStart, firstControlled int, expected string) (string, error) {
	headings, err := markdownHeadings(source[bodyStart:], bodyStart)
	if err != nil {
		return "", err
	}
	rootHeadings := make([]markdownHeading, 0, 1)
	for _, heading := range headings {
		if heading.level == 1 {
			rootHeadings = append(rootHeadings, heading)
		}
	}
	if len(rootHeadings) != 1 {
		return "", fmt.Errorf("document must contain exactly one root H1, found %d", len(rootHeadings))
	}
	root := rootHeadings[0]
	if root.start >= firstControlled {
		return "", errors.New("document root H1 must precede controlled sections and markers")
	}
	if expected != "" && root.name != expected {
		return "", fmt.Errorf("document root H1 must be %q", expected)
	}
	return root.name, nil
}

func firstMarkerStart(blocks []markerBlock, fallback int) int {
	if len(blocks) == 0 {
		return fallback
	}
	first := blocks[0].open.start
	for _, block := range blocks[1:] {
		if block.open.start < first {
			first = block.open.start
		}
	}
	return first
}
