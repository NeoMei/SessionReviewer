package syncdoc

import (
	"bytes"
	"strings"
)

// Block entries own their AST key/value comments. The document prefix and
// separator blank lines remain shell bytes, including when an entry is deleted.
func (state *v4DocumentState) indexBlockFrontmatter(source []byte, lineStarts []int, keys []UnitKey, starts []int) error {
	for index, key := range keys {
		node := state.shell.frontmatter.Content[2*index]
		keyLine := state.frontStart + lineStarts[node.Line-1]
		lower := state.frontStart
		if index > 0 {
			lower = starts[index-1]
		}
		head, ok := v4BlockHeadStart(source, lower, keyLine, node.HeadComment)
		if !ok {
			return invalidDocument("invalid v4 Markdown block head comment position")
		}
		indent := starts[index] - keyLine
		if len(bytes.Trim(source[keyLine:starts[index]], " \t")) != 0 {
			// Explicit YAML key syntax is part of this changed entry, not indent.
			indent = 0
		}
		state.frontmatter[key] = v4FrontmatterSpan{start: head, blockKeyStart: keyLine, blockIndent: indent}
	}
	for index, key := range keys {
		span := state.frontmatter[key]
		end := state.frontEnd
		if index+1 < len(keys) {
			end = state.frontmatter[keys[index+1]].start
		}
		if span.start < state.frontStart || span.blockKeyStart < span.start || end < span.blockKeyStart || end > state.frontEnd {
			return invalidDocument("overlapping v4 Markdown block frontmatter spans")
		}
		trimmed := v4BlockTrimBlankLines(source, span.blockKeyStart, end)
		if trimmed < end {
			// A terminal blank in a |+ or >+ scalar is value content. One
			// bounded parse of this entry distinguishes it from separator trivia;
			// all such entry slices together fit the existing frontmatter budget.
			mapping, err := decodeFrontmatter(source[span.blockKeyStart:trimmed])
			stats := yamlStats{}
			if err != nil || len(mapping.Content) != 2 || validateYAMLNode(mapping, 1, &stats) != nil {
				return invalidDocument("cannot bound v4 Markdown block entry trivia")
			}
			value := state.shell.frontmatter.Content[index*2+1]
			if bytes.Equal(encodeNode(mapping.Content[1]), encodeNode(value)) {
				span.valueEnd = trimmed
			} else {
				span.valueEnd = end
			}
		} else {
			span.valueEnd = end
		}
		span.removalEnd, span.delimiter = span.valueEnd, end
		span.blockFootStart = span.valueEnd
		if foot := state.shell.frontmatter.Content[index*2].FootComment; foot != "" {
			var ok bool
			span.blockFootStart, ok = v4BlockHeadStart(source, span.blockKeyStart, span.valueEnd, foot)
			if !ok {
				return invalidDocument("invalid v4 Markdown block foot comment position")
			}
		}
		state.frontmatter[key] = span
	}
	return nil
}

// Associate only whole comment lines immediately before the AST key line.
// An identical # string inside a scalar cannot satisfy this source boundary.
func v4BlockHeadStart(source []byte, lower, keyLine int, comment string) (int, bool) {
	if comment == "" {
		return keyLine, true
	}
	expected := strings.Split(comment, "\n")
	cursor := keyLine
	for index := len(expected) - 1; index >= 0; index-- {
		if cursor <= lower || source[cursor-1] != '\n' {
			return 0, false
		}
		start := bytes.LastIndexByte(source[lower:cursor-1], '\n') + lower + 1
		line := bytes.TrimSpace(source[start:cursor])
		want := strings.TrimSpace(expected[index])
		if string(line) != want || (want != "" && !bytes.HasPrefix(line, []byte("#"))) {
			return 0, false
		}
		cursor = start
	}
	return cursor, true
}

func v4BlockTrimBlankLines(source []byte, lower, end int) int {
	for end > lower && source[end-1] == '\n' {
		start := bytes.LastIndexByte(source[lower:end-1], '\n') + lower + 1
		if len(bytes.TrimSpace(source[start:end])) != 0 {
			break
		}
		end = start
	}
	return end
}

func (d Document) encodeV4BlockEntry(key UnitKey, unit Unit, span v4FrontmatterSpan) ([]byte, error) {
	var head, foot []byte
	if len(unit.KeyPresentation) != 0 && span.valueEnd > span.start {
		selected, err := decodeUnitKey(unit.KeyPresentation, key.Name)
		if err != nil {
			return nil, err
		}
		original, err := decodeUnitKey(d.v4.shellAll[key].KeyPresentation, key.Name)
		if err != nil {
			return nil, err
		}
		if span.blockKeyStart > span.start && selected.HeadComment == original.HeadComment {
			head = d.v4.shell.raw[span.start:span.blockKeyStart]
			selected.HeadComment = ""
		}
		if span.blockFootStart < span.valueEnd && selected.FootComment == original.FootComment {
			foot = d.v4.shell.raw[span.blockFootStart:span.valueEnd]
			selected.FootComment = ""
		}
		unit.KeyPresentation = encodeNode(selected)
	}
	encoded, err := encodeV4FrontmatterUnit(key, unit)
	if err != nil {
		return nil, err
	}
	if span.blockIndent > 0 {
		indent := bytes.Repeat([]byte(" "), span.blockIndent)
		lines := bytes.SplitAfter(encoded, []byte("\n"))
		var out bytes.Buffer
		for _, line := range lines {
			if len(line) > 0 {
				if !bytes.Equal(line, []byte("\n")) {
					out.Write(indent)
				}
				out.Write(line)
			}
		}
		encoded = out.Bytes()
	}
	encoded = applyV4NewlineStyle(encoded, v4FrontmatterNewline(d.v4.shell.raw[:d.v4.bodyStart]))
	return joinV4Bounded([][]byte{head, encoded, foot}, v4MaxDocumentBytes)
}
