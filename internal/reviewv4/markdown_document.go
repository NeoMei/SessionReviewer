package reviewv4

import (
	"bytes"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	maxMarkdownFrontmatterBytes = 1 << 20
	maxMarkdownYAMLNodes        = 10_000
	maxMarkdownYAMLDepth        = 100
)

type MarkdownDocument struct {
	raw    []byte
	blocks []MarkdownBlock
}

func ParseMarkdownDocument(relative string, raw []byte) (MarkdownDocument, error) {
	if !validMarkdownRelative(relative) || len(raw) > maxMarkdownDocumentBytes || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 || markdownHasBareCR(raw) {
		return MarkdownDocument{}, markdownError(MarkdownFormatInvalid, relative, FieldKey{})
	}
	frontmatter, err := markdownFrontmatter(raw)
	if err != nil {
		return MarkdownDocument{}, markdownError(MarkdownFormatInvalid, relative, FieldKey{})
	}
	documentKind, err := validateMarkdownFrontmatter(frontmatter)
	if err != nil {
		return MarkdownDocument{}, markdownError(MarkdownFormatInvalid, relative, FieldKey{})
	}
	blocks, err := ScanMarkdownBlocks(raw)
	if err != nil {
		if markdown, ok := err.(*MarkdownError); ok {
			markdown.Relative = relative
		}
		return MarkdownDocument{}, err
	}
	for _, block := range blocks {
		if markdownBlockDocument(block) != documentKind {
			return MarkdownDocument{}, markdownError(MarkdownFormatInvalid, relative, block.Key)
		}
	}
	return MarkdownDocument{raw: bytes.Clone(raw), blocks: append([]MarkdownBlock(nil), blocks...)}, nil
}

func (d MarkdownDocument) Bytes() []byte { return bytes.Clone(d.raw) }

func (d MarkdownDocument) Fields() map[FieldKey]string {
	fields := make(map[FieldKey]string)
	for _, block := range d.blocks {
		if !block.Generated {
			fields[block.Key] = string(d.raw[block.ValueStart:block.ValueEnd])
		}
	}
	return fields
}

func (d MarkdownDocument) ReplaceFields(replacements map[FieldKey]string) ([]byte, error) {
	return d.replaceFields(replacements, maxMarkdownDocumentBytes)
}

func (d MarkdownDocument) replaceFields(replacements map[FieldKey]string, maximumBytes int) ([]byte, error) {
	byKey := make(map[FieldKey]MarkdownBlock, len(d.blocks))
	for _, block := range d.blocks {
		byKey[block.Key] = block
	}
	for key, value := range replacements {
		block, ok := byKey[key]
		if !ok {
			return nil, markdownError(MarkdownFormatInvalid, "", key)
		}
		if block.Generated {
			return nil, markdownError(MarkdownGeneratedRegionModified, "", key)
		}
		if len(value) > markdownFieldLimit(key, false) || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || markdownHasBareCR([]byte(value)) {
			return nil, markdownError(MarkdownFormatInvalid, "", key)
		}
	}
	removedBytes, replacementBytes := 0, 0
	for key, value := range replacements {
		block := byKey[key]
		oldLength := block.ValueEnd - block.ValueStart
		removedBytes += oldLength
		if replacementBytes > maximumBytes-len(value) {
			return nil, markdownError(MarkdownFormatInvalid, "", key)
		}
		replacementBytes += len(value)
	}
	prospectiveBytes := len(d.raw) - removedBytes
	if prospectiveBytes > maximumBytes-replacementBytes {
		return nil, markdownError(MarkdownFormatInvalid, "", FieldKey{})
	}
	prospectiveBytes += replacementBytes
	var out bytes.Buffer
	out.Grow(prospectiveBytes)
	cursor := 0
	for _, block := range d.blocks {
		value, changed := replacements[block.Key]
		if !changed || block.Generated {
			continue
		}
		out.Write(d.raw[cursor:block.ValueStart])
		out.WriteString(value)
		cursor = block.ValueEnd
	}
	out.Write(d.raw[cursor:])
	candidate := out.Bytes()
	if len(candidate) != prospectiveBytes {
		return nil, markdownError(MarkdownFormatInvalid, "", FieldKey{})
	}
	reparsed, err := ScanMarkdownBlocks(candidate)
	if err != nil || !sameMarkdownStructure(d.blocks, reparsed) {
		return nil, markdownError(MarkdownFormatInvalid, "", FieldKey{})
	}
	return bytes.Clone(candidate), nil
}

func sameMarkdownStructure(left, right []MarkdownBlock) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Key != right[index].Key || left[index].Generated != right[index].Generated {
			return false
		}
	}
	return true
}

func validMarkdownRelative(relative string) bool {
	if relative == "" || path.IsAbs(relative) || strings.Contains(relative, "\\") || path.Clean(relative) != relative {
		return false
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func markdownFrontmatter(raw []byte) (*yaml.Node, error) {
	firstEnd := bytes.IndexByte(raw, '\n')
	if firstEnd < 0 || !bytes.Equal(bytes.TrimSuffix(raw[:firstEnd], []byte("\r")), []byte("---")) {
		return nil, io.ErrUnexpectedEOF
	}
	start := firstEnd + 1
	for cursor := start; cursor < len(raw); {
		end, next := markdownPhysicalLine(raw, cursor)
		if bytes.Equal(raw[cursor:end], []byte("---")) {
			if cursor-start > maxMarkdownFrontmatterBytes {
				return nil, io.ErrShortBuffer
			}
			return decodeMarkdownYAML(raw[start:cursor])
		}
		cursor = next
	}
	return nil, io.ErrUnexpectedEOF
}

func decodeMarkdownYAML(source []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n"))))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, io.ErrUnexpectedEOF
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, io.ErrUnexpectedEOF
	}
	stats := markdownYAMLStats{}
	if err := validateMarkdownYAMLNode(document.Content[0], 1, &stats); err != nil {
		return nil, err
	}
	return document.Content[0], nil
}

type markdownYAMLStats struct{ nodes int }

func validateMarkdownYAMLNode(node *yaml.Node, depth int, stats *markdownYAMLStats) error {
	if node == nil || depth > maxMarkdownYAMLDepth {
		return io.ErrShortBuffer
	}
	stats.nodes++
	if stats.nodes > maxMarkdownYAMLNodes || node.Kind == yaml.AliasNode || node.Alias != nil || !markdownCoreYAMLTag(node.Tag) {
		return io.ErrShortBuffer
	}
	if node.Kind == yaml.MappingNode {
		if len(node.Content)%2 != 0 {
			return io.ErrUnexpectedEOF
		}
		seen := make(map[string]bool, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "" || key.Value == "<<" || seen[key.Value] {
				return io.ErrUnexpectedEOF
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := validateMarkdownYAMLNode(child, depth+1, stats); err != nil {
			return err
		}
	}
	return nil
}

func markdownCoreYAMLTag(tag string) bool {
	switch tag {
	case "", "!!map", "!!seq", "!!str", "!!int", "!!float", "!!bool", "!!null", "!!timestamp", "tag:yaml.org,2002:map", "tag:yaml.org,2002:seq", "tag:yaml.org,2002:str", "tag:yaml.org,2002:int", "tag:yaml.org,2002:float", "tag:yaml.org,2002:bool", "tag:yaml.org,2002:null", "tag:yaml.org,2002:timestamp":
		return true
	default:
		return false
	}
}

func validateMarkdownFrontmatter(mapping *yaml.Node) (string, error) {
	values := make(map[string]*yaml.Node, len(mapping.Content)/2)
	for index := 0; index < len(mapping.Content); index += 2 {
		key := mapping.Content[index].Value
		if strings.HasPrefix(key, "session_reviewer_") {
			return "", io.ErrUnexpectedEOF
		}
		values[key] = mapping.Content[index+1]
	}
	stringsRequired := map[string]string{
		"document_format":        "review-markdown-v1",
		"minimum_reader_version": "0.4.1",
		"minimum_writer_version": "0.4.1",
	}
	for key, want := range stringsRequired {
		if got, ok := markdownString(values[key]); !ok || got != want {
			return "", io.ErrUnexpectedEOF
		}
	}
	for _, key := range []string{"id", "project_id", "generation_id"} {
		got, ok := markdownString(values[key])
		if !ok || !validID(got) {
			return "", io.ErrUnexpectedEOF
		}
	}
	entityType, ok := markdownString(values["entity_type"])
	if !ok || (entityType != "project-review" && entityType != "project-history") {
		return "", io.ErrUnexpectedEOF
	}
	if got, ok := markdownInteger(values["schema_version"]); !ok || got != 4 {
		return "", io.ErrUnexpectedEOF
	}
	if got, ok := markdownInteger(values["revision"]); !ok || got < 1 {
		return "", io.ErrUnexpectedEOF
	}
	if entityType == "project-review" {
		return "review", nil
	}
	return "history", nil
}

func markdownString(node *yaml.Node) (string, bool) {
	return markdownScalar(node, "!!str")
}

func markdownScalar(node *yaml.Node, tag string) (string, bool) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != tag || node.Value == "" {
		return "", false
	}
	return node.Value, true
}

func markdownInteger(node *yaml.Node) (int, bool) {
	value, ok := markdownScalar(node, "!!int")
	if !ok {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	return parsed, err == nil
}

func markdownBlockDocument(block MarkdownBlock) string {
	kind := block.Key.Entity
	if prefix, _, ok := strings.Cut(kind, ":"); ok {
		kind = prefix
	}
	if kind == "milestone" {
		return "history"
	}
	return "review"
}
