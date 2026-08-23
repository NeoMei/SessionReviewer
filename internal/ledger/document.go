package ledger

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"gopkg.in/yaml.v3"
)

const (
	MaxDocumentBytes    = 4 << 20
	maxFrontmatterBytes = 1 << 20
	maxYAMLNodes        = 10_000
	maxYAMLDepth        = 100
	maxYAMLAliases      = 100
	maxSections         = 1_000
)

var (
	ErrInvalidDocument      = errors.New("invalid ledger document")
	ErrReservedFieldChanged = errors.New("reserved ledger field changed")
	ErrEditableField        = errors.New("field is not editable")
	ErrInvalidSectionName   = errors.New("invalid section name")
	lowercaseSHA256         = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Section struct {
	Name    string
	Heading string
	Body    string
}

type Document struct {
	Frontmatter yaml.Node
	Preamble    string
	Sections    []Section
}

// Clone returns a structurally independent document. Rendering and reparsing
// deliberately duplicates every YAML node and section string while preserving
// the stable serialized representation.
func (d Document) Clone() (Document, error) {
	body, err := d.Render()
	if err != nil {
		return Document{}, err
	}
	return ParseDocument(body)
}

func ParseDocument(src []byte) (Document, error) {
	if len(src) > MaxDocumentBytes {
		return Document{}, invalidDocument("document exceeds size limit")
	}
	if !utf8.Valid(src) {
		return Document{}, invalidDocument("document is not valid UTF-8")
	}

	normalized := strings.ReplaceAll(string(src), "\r\n", "\n")
	if strings.ContainsRune(normalized, '\r') {
		return Document{}, invalidDocument("document contains a bare carriage return")
	}
	frontmatter, body, err := splitFrontmatter(normalized)
	if err != nil {
		return Document{}, err
	}
	if len(frontmatter) > maxFrontmatterBytes {
		return Document{}, invalidDocument("frontmatter exceeds size limit")
	}

	mapping, err := decodeFrontmatter(frontmatter)
	if err != nil {
		return Document{}, err
	}
	if err := validateFrontmatter(mapping); err != nil {
		return Document{}, err
	}

	preamble, sections, err := parseSections(body)
	if err != nil {
		return Document{}, err
	}
	return Document{Frontmatter: *mapping, Preamble: preamble, Sections: sections}, nil
}

func (d *Document) SetReserved(fields map[string]any) error {
	if d == nil {
		return fmt.Errorf("%w: nil document", ErrInvalidDocument)
	}
	if err := validateFrontmatter(&d.Frontmatter); err != nil {
		return err
	}
	if err := validateExistingReservedFields(&d.Frontmatter); err != nil {
		return err
	}

	for _, key := range []string{"id", "entity_type", "project_id", "revision"} {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("%w: missing %s", ErrReservedFieldChanged, key)
		}
	}
	for key := range fields {
		if !isReservedField(key) {
			return fmt.Errorf("%w: %s is not reserved", ErrReservedFieldChanged, key)
		}
	}

	for _, key := range []string{"id", "entity_type", "project_id"} {
		current, err := requiredString(&d.Frontmatter, key)
		if err != nil {
			return err
		}
		incoming, ok := fields[key].(string)
		if !ok || incoming != current {
			return fmt.Errorf("%w: %s", ErrReservedFieldChanged, key)
		}
	}

	currentRevision, err := requiredRevision(&d.Frontmatter)
	if err != nil {
		return err
	}
	incomingRevision, ok := fields["revision"].(int)
	if !ok || currentRevision == math.MaxInt || incomingRevision != currentRevision+1 {
		return fmt.Errorf("%w: revision must increment exactly once", ErrReservedFieldChanged)
	}

	nodes := make(map[string]yaml.Node, len(fields))
	for key, value := range fields {
		if key == "id" || key == "entity_type" || key == "project_id" {
			continue
		}
		if err := validateReservedValue(key, value); err != nil {
			return err
		}
		node, err := encodeValue(value)
		if err != nil {
			return fmt.Errorf("%w: invalid %s", ErrReservedFieldChanged, key)
		}
		if key == "revision" && (node.Kind != yaml.ScalarNode || node.Tag != "!!int") {
			return fmt.Errorf("%w: invalid revision", ErrReservedFieldChanged)
		}
		if key == "source_sessions" && !stringSequence(&node) {
			return fmt.Errorf("%w: invalid source_sessions", ErrReservedFieldChanged)
		}
		if key != "revision" && key != "source_sessions" && node.Kind != yaml.ScalarNode {
			return fmt.Errorf("%w: invalid %s", ErrReservedFieldChanged, key)
		}
		nodes[key] = node
	}

	for _, key := range reservedUpdateOrder {
		if node, ok := nodes[key]; ok {
			setMappingValue(&d.Frontmatter, key, node)
		}
	}
	return nil
}

func validateReservedValue(key string, value any) error {
	switch key {
	case "revision":
		if _, ok := value.(int); !ok {
			return fmt.Errorf("%w: invalid revision", ErrReservedFieldChanged)
		}
	case "source_sessions":
		values, ok := value.([]string)
		if !ok {
			return fmt.Errorf("%w: invalid source_sessions", ErrReservedFieldChanged)
		}
		seen := make(map[string]struct{}, len(values))
		for _, item := range values {
			if item == "" {
				return fmt.Errorf("%w: invalid source_sessions", ErrReservedFieldChanged)
			}
			if _, exists := seen[item]; exists {
				return fmt.Errorf("%w: duplicate source_sessions", ErrReservedFieldChanged)
			}
			seen[item] = struct{}{}
		}
	case "sync_status":
		status, ok := value.(string)
		if !ok || (status != "synced" && status != "conflicted") {
			return fmt.Errorf("%w: invalid sync_status", ErrReservedFieldChanged)
		}
	case "sync_hash", "base_hash", "project_hash", "vault_hash", "source_hash":
		hash, ok := value.(string)
		if !ok || !lowercaseSHA256.MatchString(hash) {
			return fmt.Errorf("%w: invalid %s", ErrReservedFieldChanged, key)
		}
	default:
		return fmt.Errorf("%w: invalid %s", ErrReservedFieldChanged, key)
	}
	return nil
}

func validateExistingReservedFields(mapping *yaml.Node) error {
	entityType, err := requiredString(mapping, "entity_type")
	if err != nil {
		return err
	}
	if entityType != "decision" && entityType != "open_loop" && entityType != "session" && entityType != "current_state" && entityType != "timeline" {
		return fmt.Errorf("%w: invalid entity_type", ErrReservedFieldChanged)
	}

	if node, ok := mappingValue(mapping, "source_sessions"); ok {
		if node.Kind != yaml.SequenceNode || node.Tag != "!!seq" {
			return fmt.Errorf("%w: invalid source_sessions", ErrReservedFieldChanged)
		}
		seen := make(map[string]struct{}, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode || item.Tag != "!!str" || item.Value == "" {
				return fmt.Errorf("%w: invalid source_sessions", ErrReservedFieldChanged)
			}
			if _, exists := seen[item.Value]; exists {
				return fmt.Errorf("%w: duplicate source_sessions", ErrReservedFieldChanged)
			}
			seen[item.Value] = struct{}{}
		}
	}

	if node, ok := mappingValue(mapping, "sync_status"); ok {
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" || (node.Value != "synced" && node.Value != "conflicted") {
			return fmt.Errorf("%w: invalid sync_status", ErrReservedFieldChanged)
		}
	}
	for _, key := range []string{"sync_hash", "base_hash", "project_hash", "vault_hash", "source_hash"} {
		if node, ok := mappingValue(mapping, key); ok {
			if node.Kind != yaml.ScalarNode || node.Tag != "!!str" || !lowercaseSHA256.MatchString(node.Value) {
				return fmt.Errorf("%w: invalid %s", ErrReservedFieldChanged, key)
			}
		}
	}
	return nil
}

func (d *Document) SetEditable(fields map[string]any) error {
	if d == nil {
		return fmt.Errorf("%w: nil document", ErrInvalidDocument)
	}
	if err := validateFrontmatter(&d.Frontmatter); err != nil {
		return err
	}

	for key := range fields {
		if isReservedField(key) {
			return fmt.Errorf("%w: %s", ErrReservedFieldChanged, key)
		}
		if key != "title" && key != "status" && key != "tags" {
			return fmt.Errorf("%w: %s", ErrEditableField, key)
		}
	}

	nodes := make(map[string]yaml.Node, len(fields))
	for _, key := range []string{"title", "status", "tags"} {
		value, ok := fields[key]
		if !ok {
			continue
		}
		if key == "title" || key == "status" {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%w: %s must be a string", ErrEditableField, key)
			}
		}
		if key == "tags" {
			var valid bool
			value, valid = normalizeStrings(value)
			if !valid {
				return fmt.Errorf("%w: tags must be strings", ErrEditableField)
			}
		}
		node, err := encodeValue(value)
		if err != nil {
			return fmt.Errorf("%w: invalid %s", ErrEditableField, key)
		}
		nodes[key] = node
	}

	for _, key := range []string{"title", "status", "tags"} {
		if node, ok := nodes[key]; ok {
			setMappingValue(&d.Frontmatter, key, node)
		}
	}
	return nil
}

func (d *Document) UpsertSection(name, body string) error {
	if d == nil {
		return fmt.Errorf("%w: nil document", ErrInvalidDocument)
	}
	var err error
	name, err = validatedSectionName(name)
	if err != nil {
		return err
	}
	body = normalizeSectionBody(body)
	for i := range d.Sections {
		if d.Sections[i].Name == name {
			d.Sections[i].Body = body
			return nil
		}
	}
	d.Sections = append(d.Sections, Section{Name: name, Heading: "## " + name, Body: body})
	return nil
}

func (d Document) Render() ([]byte, error) {
	if err := validateFrontmatter(&d.Frontmatter); err != nil {
		return nil, err
	}
	if len(d.Sections) > maxSections {
		return nil, invalidDocument("too many sections")
	}
	seen := make(map[string]struct{}, len(d.Sections))
	canonicalNames := make([]string, 0, len(d.Sections))
	for _, section := range d.Sections {
		if section.Heading == "" {
			return nil, invalidDocument("section has no name or heading")
		}
		name, err := validatedSectionName(section.Name)
		if err != nil {
			return nil, invalidDocument("invalid section heading name")
		}
		if _, ok := seen[name]; ok {
			return nil, invalidDocument("duplicate section heading")
		}
		seen[name] = struct{}{}
		canonicalNames = append(canonicalNames, name)
	}

	var yamlBody bytes.Buffer
	encoder := yaml.NewEncoder(&yamlBody)
	encoder.SetIndent(2)
	if err := encoder.Encode(&d.Frontmatter); err != nil {
		return nil, invalidDocument("cannot encode frontmatter")
	}
	if err := encoder.Close(); err != nil {
		return nil, invalidDocument("cannot finish frontmatter")
	}

	var out strings.Builder
	out.Grow(yamlBody.Len() + len(d.Preamble) + 64)
	out.WriteString("---\n")
	out.Write(yamlBody.Bytes())
	out.WriteString("---\n")
	preamble := normalizeLineEndings(d.Preamble)
	out.WriteString(preamble)
	endsWithNewline := preamble == "" || strings.HasSuffix(preamble, "\n")
	for _, section := range d.Sections {
		if !endsWithNewline {
			out.WriteByte('\n')
		}
		out.WriteString(normalizeLineEndings(section.Heading))
		out.WriteByte('\n')
		body := normalizeLineEndings(section.Body)
		out.WriteString(body)
		endsWithNewline = body == "" || strings.HasSuffix(body, "\n")
	}

	rendered := strings.TrimRight(out.String(), "\n") + "\n"
	if len(rendered) > MaxDocumentBytes {
		return nil, invalidDocument("rendered document exceeds size limit")
	}
	reparsed, err := ParseDocument([]byte(rendered))
	if err != nil || len(reparsed.Sections) != len(canonicalNames) {
		return nil, invalidDocument("rendered document cannot be reparsed as the same sections")
	}
	for i, name := range canonicalNames {
		if reparsed.Sections[i].Name != name {
			return nil, invalidDocument("rendered document cannot be reparsed as the same sections")
		}
	}
	return []byte(rendered), nil
}

func splitFrontmatter(src string) (string, string, error) {
	if !strings.HasPrefix(src, "---\n") {
		return "", "", invalidDocument("missing opening frontmatter fence")
	}
	for lineStart := len("---\n"); lineStart <= len(src); {
		lineEnd := strings.IndexByte(src[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(src)
		} else {
			lineEnd += lineStart
		}
		if src[lineStart:lineEnd] == "---" {
			bodyStart := lineEnd
			if bodyStart < len(src) {
				bodyStart++
			}
			return src[len("---\n"):lineStart], src[bodyStart:], nil
		}
		if lineEnd == len(src) {
			break
		}
		lineStart = lineEnd + 1
	}
	return "", "", invalidDocument("missing closing frontmatter fence")
}

func decodeFrontmatter(src string) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(src))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, invalidDocument("malformed YAML frontmatter")
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, invalidDocument("multiple YAML documents are not allowed")
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, invalidDocument("frontmatter must be a mapping")
	}
	return document.Content[0], nil
}

func validateFrontmatter(mapping *yaml.Node) error {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return invalidDocument("frontmatter must be a mapping")
	}
	stats := yamlStats{}
	if err := validateYAMLNode(mapping, 1, &stats); err != nil {
		return err
	}
	for _, key := range []string{"id", "entity_type", "project_id"} {
		if _, err := requiredString(mapping, key); err != nil {
			return err
		}
	}
	_, err := requiredRevision(mapping)
	return err
}

type yamlStats struct {
	nodes   int
	aliases int
}

func validateYAMLNode(node *yaml.Node, depth int, stats *yamlStats) error {
	if node == nil {
		return invalidDocument("nil YAML node")
	}
	stats.nodes++
	if stats.nodes > maxYAMLNodes || depth > maxYAMLDepth {
		return invalidDocument("YAML structure exceeds safety limits")
	}
	if node.Kind == yaml.AliasNode {
		stats.aliases++
		if stats.aliases > maxYAMLAliases || node.Alias == nil {
			return invalidDocument("YAML aliases exceed safety limits")
		}
		return nil
	}
	if node.Kind == yaml.MappingNode {
		if len(node.Content)%2 != 0 {
			return invalidDocument("malformed YAML mapping")
		}
		seen := make(map[string]struct{}, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "" {
				return invalidDocument("YAML mapping keys must be non-empty strings")
			}
			if _, ok := seen[key.Value]; ok {
				return invalidDocument("duplicate YAML mapping key")
			}
			seen[key.Value] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLNode(child, depth+1, stats); err != nil {
			return err
		}
	}
	return nil
}

func requiredString(mapping *yaml.Node, key string) (string, error) {
	node, ok := mappingValue(mapping, key)
	if !ok || node.Kind != yaml.ScalarNode || node.Tag != "!!str" || node.Value == "" {
		return "", invalidDocument("missing or invalid reserved string field " + key)
	}
	return node.Value, nil
}

func requiredRevision(mapping *yaml.Node) (int, error) {
	node, ok := mappingValue(mapping, "revision")
	if !ok || node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return 0, invalidDocument("missing or invalid revision")
	}
	var revision int
	if err := node.Decode(&revision); err != nil || revision < 1 {
		return 0, invalidDocument("missing or invalid revision")
	}
	return revision, nil
}

func parseSections(body string) (string, []Section, error) {
	source := []byte(body)
	headings, err := topLevelH2Headings(source)
	if err != nil {
		return "", nil, err
	}
	if len(headings) > maxSections {
		return "", nil, invalidDocument("too many sections")
	}

	seen := make(map[string]struct{}, len(headings))
	sections := make([]Section, 0, len(headings))
	for i, item := range headings {
		if _, ok := seen[item.name]; ok {
			return "", nil, invalidDocument("duplicate section heading")
		}
		seen[item.name] = struct{}{}
		bodyEnd := len(body)
		if i+1 < len(headings) {
			bodyEnd = headings[i+1].start
		}
		sections = append(sections, Section{
			Name:    item.name,
			Heading: body[item.start:item.headingEnd],
			Body:    body[item.bodyStart:bodyEnd],
		})
	}
	if len(headings) == 0 {
		return body, sections, nil
	}
	return body[:headings[0].start], sections, nil
}

type sourceHeading struct {
	start      int
	headingEnd int
	bodyStart  int
	name       string
}

func topLevelH2Headings(source []byte) ([]sourceHeading, error) {
	root := goldmark.DefaultParser().Parse(text.NewReader(source))
	headings := make([]sourceHeading, 0)
	for node := root.FirstChild(); node != nil; node = node.NextSibling() {
		heading, ok := node.(*ast.Heading)
		if !ok || heading.Level != 2 || heading.Lines().Len() == 0 {
			continue
		}

		name := strings.ReplaceAll(string(heading.Text(source)), "\n", " ")
		if strings.TrimSpace(name) == "" {
			continue
		}
		name, err := validatedSectionName(name)
		if err != nil {
			return nil, invalidDocument("invalid section heading name")
		}
		first := heading.Lines().At(0)
		last := heading.Lines().At(heading.Lines().Len() - 1)
		start := physicalLineStart(source, first.Start)
		contentEnd, afterContent := physicalLineEnd(source, last.Stop)
		headingEnd, bodyStart := contentEnd, afterContent
		if !isSourceATXH2(source[start:contentEnd]) {
			underlineStart := afterContent
			if last.Stop > 0 && last.Stop <= len(source) && source[last.Stop-1] == '\n' {
				underlineStart = last.Stop
			}
			headingEnd, bodyStart = physicalLineEnd(source, underlineStart)
		}
		if headingEnd < start || bodyStart < headingEnd || bodyStart > len(source) {
			return nil, invalidDocument("invalid Markdown heading source position")
		}
		headings = append(headings, sourceHeading{
			start:      start,
			headingEnd: headingEnd,
			bodyStart:  bodyStart,
			name:       name,
		})
	}
	return headings, nil
}

func physicalLineStart(source []byte, position int) int {
	if position < 0 {
		return 0
	}
	if position > len(source) {
		position = len(source)
	}
	if index := bytes.LastIndexByte(source[:position], '\n'); index >= 0 {
		return index + 1
	}
	return 0
}

func physicalLineEnd(source []byte, position int) (int, int) {
	if position < 0 {
		position = 0
	}
	if position > len(source) {
		position = len(source)
	}
	if index := bytes.IndexByte(source[position:], '\n'); index >= 0 {
		end := position + index
		return end, end + 1
	}
	return len(source), len(source)
}

func isSourceATXH2(line []byte) bool {
	index := 0
	for index < len(line) && index < 3 && line[index] == ' ' {
		index++
	}
	if index+2 > len(line) || line[index] != '#' || line[index+1] != '#' {
		return false
	}
	index += 2
	return index == len(line) || line[index] == ' ' || line[index] == '\t'
}

func sectionName(line string) (string, bool) {
	if !strings.HasPrefix(line, "## ") && !strings.HasPrefix(line, "##\t") {
		return "", false
	}
	name := strings.TrimSpace(line[2:])
	if lastNonHash := strings.TrimRight(name, "#"); lastNonHash != name && strings.HasSuffix(lastNonHash, " ") {
		name = strings.TrimSpace(lastNonHash)
	}
	if name == "" {
		return "", false
	}
	return name, true
}

func canonicalSectionName(name string) string {
	if parsed, ok := sectionName("## " + strings.TrimSpace(name)); ok {
		return parsed
	}
	return ""
}

func validatedSectionName(name string) (string, error) {
	for _, value := range name {
		if unicode.IsControl(value) {
			return "", ErrInvalidSectionName
		}
	}
	name = strings.Join(strings.Fields(name), " ")
	name = canonicalSectionName(name)
	if name == "" {
		return "", ErrInvalidSectionName
	}
	return name, nil
}

func normalizeSectionBody(body string) string {
	body = normalizeLineEndings(body)
	body = strings.Trim(body, "\n")
	if body == "" {
		return "\n\n"
	}
	return "\n" + body + "\n\n"
}

func normalizeLineEndings(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func mappingValue(mapping *yaml.Node, key string) (*yaml.Node, bool) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1], true
		}
	}
	return nil, false
}

func setMappingValue(mapping *yaml.Node, key string, value yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			preserveNodePresentation(mapping.Content[i+1], &value)
			*mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&value,
	)
}

func preserveNodePresentation(old, next *yaml.Node) {
	next.HeadComment = old.HeadComment
	next.LineComment = old.LineComment
	next.FootComment = old.FootComment
	next.Anchor = old.Anchor
	if old.Kind == next.Kind {
		switch old.Kind {
		case yaml.ScalarNode:
			next.Style = old.Style
		case yaml.SequenceNode, yaml.MappingNode:
			next.Style = old.Style & yaml.FlowStyle
		}
	}
}

func encodeValue(value any) (yaml.Node, error) {
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return yaml.Node{}, err
	}
	return node, nil
}

func normalizeStrings(value any) ([]string, bool) {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...), true
	case []any:
		result := make([]string, len(values))
		for i, item := range values {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			result[i] = text
		}
		return result, true
	default:
		return nil, false
	}
}

func stringSequence(node *yaml.Node) bool {
	if node.Kind != yaml.SequenceNode {
		return false
	}
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
			return false
		}
	}
	return true
}

var reservedUpdateOrder = []string{
	"revision",
	"source_sessions",
	"sync_status",
	"sync_hash",
	"base_hash",
	"project_hash",
	"vault_hash",
	"source_hash",
}

func isReservedField(key string) bool {
	switch key {
	case "id", "entity_type", "project_id", "revision", "source_sessions", "sync_status", "sync_hash", "base_hash", "project_hash", "vault_hash", "source_hash":
		return true
	default:
		return false
	}
}

func invalidDocument(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidDocument, reason)
}
