package syncdoc

import (
	"bytes"
	"sort"

	"gopkg.in/yaml.v3"
)

func (d Document) rewriteV4Shell(nextShell Document, units UnitSet) ([]byte, error) {
	original := d.v4.shellAll
	frontmatterChanged := false
	for _, key := range unionUnitKeys(original, units) {
		if !unitsEqual(original[key], units[key]) && key.Kind == UnitFrontmatter {
			frontmatterChanged = true
		}
	}
	body, err := rewriteV4Body(d.v4.shell.body, nextShell.body, units, v4MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	var prefix []byte
	if frontmatterChanged {
		prefix, err = d.rewriteV4Frontmatter(units, v4MaxDocumentBytes-len(body))
		if err != nil {
			return nil, err
		}
	} else {
		_, oldBody, splitErr := splitFrontmatter(d.v4.shell.raw)
		if splitErr != nil {
			return nil, splitErr
		}
		prefix = bytes.Clone(d.v4.shell.raw[:len(d.v4.shell.raw)-len(oldBody)])
	}
	if len(prefix) > v4MaxDocumentBytes-len(body) {
		return nil, invalidDocument("rendered v4 Markdown shell exceeds size limit")
	}
	result := make([]byte, 0, len(prefix)+len(body))
	result = append(result, prefix...)
	result = append(result, body...)
	return result, nil
}

func (d Document) rewriteV4Frontmatter(units UnitSet, limit int) ([]byte, error) {
	if d.v4.frontmatterFlow {
		return d.rewriteV4FlowFrontmatter(units, limit)
	}
	raw := d.v4.shell.raw
	newline := v4FrontmatterNewline(raw[:d.v4.bodyStart])
	parts := make([][]byte, 0, len(d.v4.frontmatter)+2)
	parts = append(parts, raw[:d.v4.frontStart])
	seen := make(map[UnitKey]bool, len(d.v4.frontmatter))
	for index := 0; index+1 < len(d.v4.shell.frontmatter.Content); index += 2 {
		key := UnitKey{Kind: UnitFrontmatter, Name: d.v4.shell.frontmatter.Content[index].Value}
		seen[key] = true
		unit, present := units[key]
		if !present || !unit.Present {
			continue
		}
		if original, found := d.v4.shellAll[key]; found && unitsEqual(original, unit) {
			span := d.v4.frontmatter[key]
			parts = append(parts, raw[span.start:span.end])
			continue
		}
		encoded, err := encodeV4FrontmatterUnit(key, unit)
		if err != nil {
			return nil, err
		}
		parts = append(parts, applyV4NewlineStyle(encoded, newline))
	}
	for _, name := range sortedFrontmatterNames(units, seen) {
		key := UnitKey{Kind: UnitFrontmatter, Name: name}
		encoded, err := encodeV4FrontmatterUnit(key, units[key])
		if err != nil {
			return nil, err
		}
		parts = append(parts, applyV4NewlineStyle(encoded, newline))
	}
	parts = append(parts, raw[d.v4.frontEnd:d.v4.bodyStart])
	return joinV4Bounded(parts, limit)
}

type v4SourceEdit struct {
	start, end int
	value      []byte
}

func (d Document) rewriteV4FlowFrontmatter(units UnitSet, limit int) ([]byte, error) {
	raw := d.v4.shell.raw
	seen := make(map[UnitKey]bool, len(d.v4.frontmatter))
	edits := make([]v4SourceEdit, 0, len(d.v4.frontmatter)+1)
	for index := 0; index < len(d.v4.frontmatterOrder); {
		key := d.v4.frontmatterOrder[index]
		seen[key] = true
		span := d.v4.frontmatter[key]
		unit, present := units[key]
		if !present || !unit.Present {
			first := index
			for index++; index < len(d.v4.frontmatterOrder); index++ {
				groupKey := d.v4.frontmatterOrder[index]
				seen[groupKey] = true
				groupUnit, groupPresent := units[groupKey]
				if groupPresent && groupUnit.Present {
					break
				}
			}
			last := index - 1
			if index < len(d.v4.frontmatterOrder) {
				next := d.v4.frontmatter[d.v4.frontmatterOrder[index]]
				edits = append(edits, v4SourceEdit{start: span.start, end: next.start})
			} else if first > 0 {
				previous := d.v4.frontmatter[d.v4.frontmatterOrder[first-1]]
				lastSpan := d.v4.frontmatter[d.v4.frontmatterOrder[last]]
				edits = append(edits, v4SourceEdit{start: previous.delimiter, end: lastSpan.end})
			} else {
				return nil, invalidDocument("v4 Markdown flow frontmatter cannot be empty")
			}
			continue
		}
		if original, found := d.v4.shellAll[key]; found && unitsEqual(original, unit) {
			index++
			continue
		}
		encoded, err := encodeV4FlowFrontmatterUnit(key, unit)
		if err != nil {
			return nil, err
		}
		edits = append(edits, v4SourceEdit{start: span.start, end: span.end, value: encoded})
		index++
	}
	additions := sortedFrontmatterNames(units, seen)
	if len(additions) != 0 {
		var addition bytes.Buffer
		last := d.v4.frontmatter[d.v4.frontmatterOrder[len(d.v4.frontmatterOrder)-1]]
		if last.delimiter == d.v4.frontmatterClose {
			addition.WriteString(", ")
		} else if last.delimiter+1 == d.v4.frontmatterClose {
			addition.WriteByte(' ')
		}
		for index, name := range additions {
			key := UnitKey{Kind: UnitFrontmatter, Name: name}
			encoded, err := encodeV4FlowFrontmatterUnit(key, units[key])
			if err != nil {
				return nil, err
			}
			if index > 0 {
				addition.WriteString(", ")
			}
			addition.Write(encoded)
		}
		edits = append(edits, v4SourceEdit{start: d.v4.frontmatterClose, end: d.v4.frontmatterClose, value: addition.Bytes()})
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	parts := make([][]byte, 0, len(edits)*2+1)
	cursor := 0
	for _, edit := range edits {
		if edit.start < cursor || edit.end < edit.start || edit.end > len(raw) {
			return nil, invalidDocument("overlapping v4 Markdown flow frontmatter edits")
		}
		parts = append(parts, raw[cursor:edit.start], edit.value)
		cursor = edit.end
	}
	parts = append(parts, raw[cursor:d.v4.bodyStart])
	return joinV4Bounded(parts, limit)
}

func encodeV4FlowFrontmatterUnit(key UnitKey, unit Unit) ([]byte, error) {
	encoded, err := encodeV4FrontmatterUnit(key, unit)
	if err != nil {
		return nil, err
	}
	mapping, err := decodeFrontmatter(encoded)
	if err != nil || len(mapping.Content) != 2 {
		return nil, invalidDocument("cannot encode v4 Markdown flow frontmatter unit")
	}
	mapping.Style |= yaml.FlowStyle
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(mapping); err != nil {
		return nil, invalidDocument("cannot encode v4 Markdown flow frontmatter unit")
	}
	if err := encoder.Close(); err != nil {
		return nil, invalidDocument("cannot finish v4 Markdown flow frontmatter unit")
	}
	source := out.Bytes()
	flowMapping, err := decodeFrontmatter(source)
	if err != nil || len(flowMapping.Content) != 2 {
		return nil, invalidDocument("cannot decode encoded v4 Markdown flow frontmatter unit")
	}
	lineStarts := []int{0}
	for index, value := range source {
		if value == '\n' && index+1 < len(source) {
			lineStarts = append(lineStarts, index+1)
		}
	}
	start, ok := v4YAMLNodeOffset(source, lineStarts, flowMapping.Content[0])
	if !ok {
		return nil, invalidDocument("cannot locate encoded v4 Markdown flow frontmatter unit")
	}
	close, ok := v4FlowMappingClose(source, 0)
	if !ok {
		return nil, invalidDocument("cannot bound encoded v4 Markdown flow frontmatter unit")
	}
	delimiter, ok := v4FlowEntryDelimiter(source, start, close, true)
	if !ok {
		return nil, invalidDocument("cannot bound encoded v4 Markdown flow frontmatter unit")
	}
	return bytes.Clone(bytes.TrimRight(source[start:delimiter], " \t\r\n")), nil
}

func v4FrontmatterNewline(source []byte) []byte {
	end := bytes.IndexByte(source, '\n')
	if end > 0 && source[end-1] == '\r' {
		return []byte("\r\n")
	}
	return []byte("\n")
}

func applyV4NewlineStyle(source, newline []byte) []byte {
	if bytes.Equal(newline, []byte("\n")) {
		return source
	}
	return bytes.ReplaceAll(source, []byte("\n"), newline)
}

func encodeV4FrontmatterUnit(key UnitKey, unit Unit) ([]byte, error) {
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key.Name}
	var err error
	if len(unit.KeyPresentation) != 0 {
		keyNode, err = decodeUnitKey(unit.KeyPresentation, key.Name)
		if err != nil {
			return nil, err
		}
	}
	valueNode, err := decodeUnitValue(unit.Value)
	if err != nil {
		return nil, err
	}
	mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{keyNode, valueNode}}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(mapping); err != nil {
		return nil, invalidDocument("cannot encode v4 Markdown frontmatter unit")
	}
	if err := encoder.Close(); err != nil {
		return nil, invalidDocument("cannot finish v4 Markdown frontmatter unit")
	}
	return bytes.Clone(out.Bytes()), nil
}

func rewriteV4Body(original, ordered Body, units UnitSet, limit int) ([]byte, error) {
	parts := make([][]byte, 0, 1+2*len(ordered.sections))
	preamble := original.preamble
	if unit, ok := units[UnitKey{Kind: UnitPreamble}]; ok {
		if !unit.Present {
			return nil, invalidDocument("v4 Markdown preamble cannot be deleted")
		}
		preamble = unit.Value
	}
	parts = append(parts, preamble)
	originalByKey := make(map[UnitKey]bodySection, len(original.sections))
	for _, section := range original.sections {
		originalByKey[section.key] = section
	}
	for _, section := range ordered.sections {
		unit := units[section.key]
		heading, value := unit.HeadingPresentation, unit.Value
		if old, found := originalByKey[section.key]; found && unitsEqual(unit, Unit{Present: true, Value: old.value, HeadingPresentation: old.heading}) {
			heading, value = old.heading, old.value
		} else if len(heading) == 0 {
			heading = section.heading
		}
		parts = append(parts, heading, value)
	}
	return joinV4Bounded(parts, limit)
}

func replaceV4Placeholders(shell []byte, blocks []v4BlockState, fields UnitSet) ([]byte, error) {
	return replaceV4PlaceholdersBounded(shell, blocks, fields, v4MaxDocumentBytes)
}

func replaceV4PlaceholdersBounded(shell []byte, blocks []v4BlockState, fields UnitSet, limit int) ([]byte, error) {
	byPlaceholder := make(map[string]v4BlockState, len(blocks))
	total := len(shell)
	for _, block := range blocks {
		byPlaceholder[string(block.semanticPlaceholder)] = block
		replacementLength := v4BlockReplacementLength(block, fields)
		removed := len(block.semanticPlaceholder)
		if total < removed || limit < 0 || replacementLength > limit-(total-removed) {
			return nil, invalidDocument("rendered v4 Markdown document exceeds size limit")
		}
		total = total - removed + replacementLength
	}
	parts := make([][]byte, 0, 2*len(blocks)+1)
	cursor := 0
	seen := make(map[string]bool, len(blocks))
	for cursor < len(shell) {
		offset := bytes.Index(shell[cursor:], []byte("<!-- sr-v4-block:"))
		if offset < 0 {
			break
		}
		start := cursor + offset
		endOffset := bytes.IndexByte(shell[start:], '\n')
		if endOffset < 0 {
			return nil, invalidDocument("v4 Markdown placeholder is incomplete")
		}
		end := start + endOffset + 1
		placeholder := string(shell[start:end])
		block, ok := byPlaceholder[placeholder]
		if !ok || seen[placeholder] {
			return nil, invalidDocument("v4 Markdown marker position was changed")
		}
		seen[placeholder] = true
		parts = append(parts, shell[cursor:start], v4BlockReplacement(block, fields))
		cursor = end
	}
	parts = append(parts, shell[cursor:])
	if len(seen) != len(blocks) {
		return nil, invalidDocument("v4 Markdown marker position was changed")
	}
	return joinV4Bounded(parts, limit)
}

func v4BlockReplacementLength(block v4BlockState, fields UnitSet) int {
	if block.block.Generated {
		return len(block.original)
	}
	unit := fields[block.unitKey]
	return block.block.ValueStart - block.block.Start + len(unit.Value) + len(block.original) - (block.block.ValueEnd - block.block.Start)
}

func sortedFrontmatterNames(units UnitSet, seen map[UnitKey]bool) []string {
	names := make([]string, 0)
	for key, unit := range units {
		if key.Kind == UnitFrontmatter && unit.Present && !seen[key] {
			names = append(names, key.Name)
		}
	}
	sort.Strings(names)
	return names
}

func v4BlockReplacement(block v4BlockState, fields UnitSet) []byte {
	if block.block.Generated {
		return block.original
	}
	unit := fields[block.unitKey]
	prefixLength := block.block.ValueStart - block.block.Start
	suffixStart := block.block.ValueEnd - block.block.Start
	replacement := make([]byte, 0, prefixLength+len(unit.Value)+len(block.original)-suffixStart)
	replacement = append(replacement, block.original[:prefixLength]...)
	replacement = append(replacement, unit.Value...)
	replacement = append(replacement, block.original[suffixStart:]...)
	return replacement
}

func joinV4Bounded(parts [][]byte, limit int) ([]byte, error) {
	total := 0
	for _, part := range parts {
		if limit < 0 || len(part) > limit-total {
			return nil, invalidDocument("rendered v4 Markdown document exceeds size limit")
		}
		total += len(part)
	}
	var out bytes.Buffer
	out.Grow(total)
	for _, part := range parts {
		out.Write(part)
	}
	return out.Bytes(), nil
}
