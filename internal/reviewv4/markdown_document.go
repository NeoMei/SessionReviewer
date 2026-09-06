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
	raw                  []byte
	blocks               []MarkdownBlock
	trustedAnchorIDs     map[string]struct{}
	trustedAnchorSpans   []markdownAnchorSpan
	trustedIdentitySpans []markdownIdentitySpan
	authenticated        bool
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

func (d MarkdownDocument) Blocks() []MarkdownBlock {
	return append([]MarkdownBlock(nil), d.blocks...)
}

// SensitiveScanSource masks only structural identities authenticated against
// the ledger baseline. Human field values and marker-looking ordinary content
// remain byte-for-byte visible to the caller's scanner.
func (d MarkdownDocument) SensitiveScanSource() ([]byte, error) {
	if !d.authenticated {
		return nil, markdownError(MarkdownBaselineMissing, "", FieldKey{})
	}
	type sensitiveSpan struct {
		start, end int
		kind       byte
		key        FieldKey
	}
	const (
		markerSpan byte = iota
		generatedSpan
		anchorSpan
		identitySpan
	)
	blockSpans := make([]sensitiveSpan, 0, len(d.blocks)*3)
	for _, span := range d.trustedIdentitySpans {
		blockSpans = append(blockSpans, sensitiveSpan{start: span.start, end: span.end, kind: identitySpan})
	}
	for _, block := range d.blocks {
		blockSpans = append(blockSpans, sensitiveSpan{start: block.Start, end: block.ValueStart, kind: markerSpan, key: block.Key})
		if block.Generated && block.ValueStart < block.ValueEnd {
			blockSpans = append(blockSpans, sensitiveSpan{start: block.ValueStart, end: block.ValueEnd, kind: generatedSpan})
		}
		blockSpans = append(blockSpans, sensitiveSpan{start: block.ValueEnd, end: block.End, kind: markerSpan, key: block.Key})
	}
	var out bytes.Buffer
	cursor := 0
	blockIndex, anchorIndex := 0, 0
	for blockIndex < len(blockSpans) || anchorIndex < len(d.trustedAnchorSpans) {
		span := sensitiveSpan{start: len(d.raw), end: len(d.raw)}
		if blockIndex < len(blockSpans) {
			span = blockSpans[blockIndex]
		}
		if anchorIndex < len(d.trustedAnchorSpans) && d.trustedAnchorSpans[anchorIndex].start < span.start {
			anchor := d.trustedAnchorSpans[anchorIndex]
			span = sensitiveSpan{start: anchor.start, end: anchor.end, kind: anchorSpan}
			anchorIndex++
		} else {
			blockIndex++
		}
		if span.start < cursor || span.end < span.start || span.end > len(d.raw) {
			return nil, markdownError(MarkdownFormatInvalid, "", FieldKey{})
		}
		out.Write(d.raw[cursor:span.start])
		switch span.kind {
		case markerSpan:
			out.Write(maskMarkdownMarkerIdentity(d.raw[span.start:span.end], span.key))
		case generatedSpan:
			out.Write(maskTrustedMarkdownAnchorIDs(d.raw[span.start:span.end], d.trustedAnchorIDs))
		case anchorSpan:
			out.WriteString(`<a id="validated-marker"></a>`)
		case identitySpan:
			out.WriteString("validated-identity")
		}
		cursor = span.end
	}
	out.Write(d.raw[cursor:])
	return bytes.Clone(out.Bytes()), nil
}

func maskTrustedMarkdownAnchorIDs(source []byte, trusted map[string]struct{}) []byte {
	var out bytes.Buffer
	cursor := 0
	for cursor < len(source) {
		offset := bytes.IndexByte(source[cursor:], '#')
		if offset < 0 {
			break
		}
		start := cursor + offset
		end := start + 1
		for end < len(source) && markdownIDByte(source[end]) {
			end++
		}
		if _, ok := trusted[string(source[start+1:end])]; ok {
			out.Write(source[cursor:start])
			out.WriteString("#validated-marker")
			cursor = end
			continue
		}
		out.Write(source[cursor : start+1])
		cursor = start + 1
	}
	out.Write(source[cursor:])
	return out.Bytes()
}

func markdownIDByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '.' || value == '_' || value == ':' || value == '-'
}

func maskMarkdownMarkerIdentity(source []byte, key FieldKey) []byte {
	identity := []byte("entity=\"" + key.Entity + "\" name=\"" + key.Name + "\"")
	return bytes.ReplaceAll(source, identity, []byte(`entity="validated-marker" name="validated-marker"`))
}

func ParseMarkdownDocumentAgainstLedger(relative string, raw []byte, ledger MachineLedger) (MarkdownDocument, error) {
	if err := validateMarkdownLedger(ledger); err != nil {
		return MarkdownDocument{}, err
	}
	base := ledger.DocumentProjection.PresentationBase
	document, err := ParseMarkdownDocument(relative, raw)
	if err != nil {
		return MarkdownDocument{}, err
	}
	if err := validateMarkdownFrontmatterIdentity(relative, raw, base); err != nil {
		return MarkdownDocument{}, err
	}
	document.trustedIdentitySpans, err = markdownIdentitySpans(raw)
	if err != nil {
		return MarkdownDocument{}, markdownError(MarkdownFormatInvalid, relative, FieldKey{})
	}
	expectedPair, err := renderFreshMarkdown(base)
	if err != nil {
		return MarkdownDocument{}, err
	}
	expectedRaw := expectedPair.Review
	if relative == markdownHistoryRelative {
		expectedRaw = expectedPair.History
	}
	expected, err := ParseMarkdownDocument(relative, expectedRaw)
	if err != nil {
		return MarkdownDocument{}, err
	}
	if err := validateMarkdownStructure(document, expected); err != nil {
		return MarkdownDocument{}, err
	}
	if err := validateMarkdownAnchors(relative, document.raw, expected.raw); err != nil {
		return MarkdownDocument{}, err
	}
	expectedAnchorIndex, err := newMarkdownDocumentIndex(relative, expected)
	if err != nil {
		return MarkdownDocument{}, err
	}
	actualAnchorIndex, err := newMarkdownDocumentIndex(relative, document)
	if err != nil {
		return MarkdownDocument{}, err
	}
	document.authenticated = true
	document.trustedAnchorIDs = make(map[string]struct{})
	for _, expectedDocumentInput := range []struct {
		relative string
		raw      []byte
	}{{markdownReviewRelative, expectedPair.Review}, {markdownHistoryRelative, expectedPair.History}} {
		expectedDocument, parseErr := ParseMarkdownDocument(expectedDocumentInput.relative, expectedDocumentInput.raw)
		if parseErr != nil {
			return MarkdownDocument{}, parseErr
		}
		index, indexErr := newMarkdownDocumentIndex(relative, expectedDocument)
		if indexErr != nil {
			return MarkdownDocument{}, indexErr
		}
		for anchor := range index.anchors {
			if id, ok := parseMarkdownAnchorLineID(anchor); ok {
				document.trustedAnchorIDs[id] = struct{}{}
			}
		}
	}
	for _, anchor := range actualAnchorIndex.anchorSpans {
		if expectedAnchorIndex.anchors[anchor.line] == 1 {
			document.trustedAnchorSpans = append(document.trustedAnchorSpans, anchor)
		}
	}
	expectedIndex := expectedAnchorIndex
	for _, block := range document.blocks {
		if !block.Generated {
			continue
		}
		expectedBlock := expectedIndex.blocks[block.Key]
		if !markdownSemanticEqual(markdownBlockValue(document, block), markdownBlockValue(expected, expectedBlock)) {
			return MarkdownDocument{}, &MarkdownError{Code: MarkdownGeneratedRegionModified, Relative: relative, Entity: block.Key.Entity, Field: block.Key.Name}
		}
	}
	return document, nil
}

type markdownIdentitySpan struct{ start, end int }

// Called only after fixed frontmatter identities match the authenticated Base.
// YAML positions select scalar values; spelling never selects a user field or
// comment, even when it contains the very same identity.
func markdownIdentitySpans(raw []byte) ([]markdownIdentitySpan, error) {
	mapping, err := markdownFrontmatter(raw)
	if err != nil {
		return nil, err
	}
	var spans []markdownIdentitySpan
	for i := 0; i < len(mapping.Content); i += 2 {
		key, node := mapping.Content[i].Value, mapping.Content[i+1]
		if key != "id" && key != "project_id" && key != "generation_id" {
			continue
		}
		start, end, err := markdownScalarValueSpan(raw, node)
		if err != nil {
			return nil, err
		}
		spans = append(spans, markdownIdentitySpan{start, end})
	}
	return spans, nil
}

// markdownScalarValueSpan returns only the presented scalar value bytes. YAML
// syntax around the value (tags, anchors, quote delimiters, block indicators,
// indentation, comments, and line endings) is deliberately outside the span.
// Callers must first validate the parsed mapping and the replacement value.
func markdownScalarValueSpan(raw []byte, node *yaml.Node) (int, int, error) {
	if node == nil || node.Kind != yaml.ScalarNode {
		return 0, 0, io.ErrUnexpectedEOF
	}
	starts := []int{bytes.IndexByte(raw, '\n') + 1}
	for cursor := starts[0]; cursor < len(raw); {
		end, next := markdownPhysicalLine(raw, cursor)
		if bytes.Equal(raw[cursor:end], []byte("---")) {
			break
		}
		starts = append(starts, next)
		cursor = next
	}
	if starts[0] == 0 || node.Line < 1 || node.Line > len(starts) {
		return 0, 0, io.ErrUnexpectedEOF
	}
	start := starts[node.Line-1]
	for column := 1; column < node.Column && start < len(raw); column++ {
		_, size := utf8.DecodeRune(raw[start:])
		start += size
	}
	// Explicit tags and anchors are syntax, not scalar value bytes.
	for start < len(raw) && (raw[start] == '!' || raw[start] == '&') {
		for start < len(raw) && raw[start] != ' ' && raw[start] != '\t' && raw[start] != '\n' && raw[start] != '\r' {
			start++
		}
		for start < len(raw) && (raw[start] == ' ' || raw[start] == '\t' || raw[start] == '\n' || raw[start] == '\r') {
			start++
		}
	}
	if start >= len(raw) {
		return 0, 0, io.ErrUnexpectedEOF
	}
	end := start
	switch raw[start] {
	case '\'', '"':
		quote := raw[start]
		end = start + 1
		for end < len(raw) {
			if quote == '"' && raw[end] == '\\' {
				end += 2
				continue
			}
			if raw[end] == quote {
				if quote == '\'' && end+1 < len(raw) && raw[end+1] == quote {
					end += 2
					continue
				}
				break
			}
			end++
		}
		if end >= len(raw) {
			return 0, 0, io.ErrUnexpectedEOF
		}
		start++
	case '|', '>':
		// Bound metadata values contain no whitespace, so an accepted block
		// scalar has one nonblank content line after its presentation header.
		_, start = markdownPhysicalLine(raw, start)
		for start < len(raw) && (raw[start] == ' ' || raw[start] == '\t' || raw[start] == '\n' || raw[start] == '\r') {
			start++
		}
		end = start + len(node.Value)
		if end > len(raw) || string(raw[start:end]) != node.Value {
			return 0, 0, io.ErrUnexpectedEOF
		}
	default:
		end = start
		for end < len(raw) && raw[end] != ' ' && raw[end] != '\t' && raw[end] != '\n' && raw[end] != '\r' && raw[end] != ',' && raw[end] != ']' && raw[end] != '}' {
			end++
		}
		if end == start {
			return 0, 0, io.ErrUnexpectedEOF
		}
	}
	return start, end, nil
}

func parseMarkdownAnchorLineID(anchor string) (string, bool) {
	if !strings.HasPrefix(anchor, `<a id="`) || !strings.HasSuffix(anchor, `"></a>`) {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(anchor, `<a id="`), `"></a>`), true
}

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
