package reviewv4

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

const (
	maxMarkdownDocumentBytes = 64 << 20
	maxMarkdownFieldBytes    = 16_384
	maxMarkdownBlocks        = 65_536
)

type MarkdownBlock struct {
	Key        FieldKey
	Generated  bool
	Start      int
	ValueStart int
	ValueEnd   int
	End        int
}

type markdownMarker struct {
	key       FieldKey
	generated bool
	closing   bool
}

func ScanMarkdownBlocks(raw []byte) ([]MarkdownBlock, error) {
	if len(raw) > maxMarkdownDocumentBytes || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 || markdownHasBareCR(raw) {
		return nil, markdownError(MarkdownFormatInvalid, "", FieldKey{})
	}
	blocks := make([]MarkdownBlock, 0)
	seen := make(map[FieldKey]bool)
	var active *MarkdownBlock
	var fence byte
	fenceLength := 0
	indentedCode := false

	for start := 0; start < len(raw); {
		end, next := markdownPhysicalLine(raw, start)
		line := raw[start:end]
		if indentedCode {
			if len(line) == 0 || markdownIndented(line) {
				start = next
				continue
			}
			indentedCode = false
		}
		if fence != 0 {
			if markdownFenceClose(line, fence, fenceLength) {
				fence, fenceLength = 0, 0
			}
			start = next
			continue
		}
		if character, length, ok := markdownFenceOpen(line); ok {
			fence, fenceLength = character, length
			start = next
			continue
		}
		if markdownIndented(line) {
			indentedCode = true
			start = next
			continue
		}

		marker, recognized, err := parseMarkdownMarker(line)
		if err != nil {
			return nil, err
		}
		if !recognized {
			start = next
			continue
		}
		if marker.closing {
			if active == nil || active.Key != marker.key || active.Generated != marker.generated {
				return nil, markdownError(MarkdownFormatInvalid, "", marker.key)
			}
			active.ValueEnd = start
			if active.ValueEnd > active.ValueStart && raw[active.ValueEnd-1] == '\n' {
				active.ValueEnd--
				if active.ValueEnd > active.ValueStart && raw[active.ValueEnd-1] == '\r' {
					active.ValueEnd--
				}
			}
			if active.ValueEnd-active.ValueStart > markdownFieldLimit(active.Key, active.Generated) {
				return nil, markdownError(MarkdownFormatInvalid, "", active.Key)
			}
			active.End = next
			blocks = append(blocks, *active)
			active = nil
			start = next
			continue
		}
		if active != nil {
			return nil, markdownError(MarkdownFormatInvalid, "", marker.key)
		}
		if seen[marker.key] {
			return nil, markdownError(MarkdownFieldDuplicate, "", marker.key)
		}
		if len(blocks) >= maxMarkdownBlocks {
			return nil, markdownError(MarkdownFormatInvalid, "", marker.key)
		}
		seen[marker.key] = true
		active = &MarkdownBlock{Key: marker.key, Generated: marker.generated, Start: start, ValueStart: next}
		start = next
	}
	if active != nil {
		return nil, markdownError(MarkdownFormatInvalid, "", active.Key)
	}
	return blocks, nil
}

func markdownIndented(line []byte) bool {
	return bytes.HasPrefix(line, []byte("    ")) || bytes.HasPrefix(line, []byte("\t"))
}

func parseMarkdownMarker(line []byte) (markdownMarker, bool, error) {
	const openingPrefix = "<!-- session-reviewer:v4-"
	const closingPrefix = "<!-- /session-reviewer:v4-"
	text := string(line)
	closing := false
	rest := ""
	switch {
	case strings.HasPrefix(text, openingPrefix):
		rest = strings.TrimPrefix(text, openingPrefix)
	case strings.HasPrefix(text, closingPrefix):
		closing = true
		rest = strings.TrimPrefix(text, closingPrefix)
	default:
		return markdownMarker{}, false, nil
	}
	kind, attributes, ok := strings.Cut(rest, " entity=\"")
	if !ok || (kind != "field" && kind != "generated") || !strings.HasSuffix(attributes, " -->") {
		return markdownMarker{}, true, markdownError(MarkdownFormatInvalid, "", FieldKey{})
	}
	entity, tail, ok := strings.Cut(strings.TrimSuffix(attributes, " -->"), "\" name=\"")
	if !ok || entity == "" || !strings.HasSuffix(tail, "\"") {
		return markdownMarker{}, true, markdownError(MarkdownFormatInvalid, "", FieldKey{})
	}
	name := strings.TrimSuffix(tail, "\"")
	key := FieldKey{Entity: entity, Name: name}
	generated := kind == "generated"
	if !validMarkdownKey(key, generated) {
		return markdownMarker{}, true, markdownError(MarkdownFormatInvalid, "", key)
	}
	return markdownMarker{key: key, generated: generated, closing: closing}, true, nil
}

func validMarkdownKey(key FieldKey, generated bool) bool {
	kind, id, hasID := strings.Cut(key.Entity, ":")
	if !hasID {
		kind = key.Entity
	}
	if kind == "project-overview" {
		if hasID {
			return false
		}
	} else if kind != "decision" && kind != "risk" && kind != "open-loop" && kind != "problem" && kind != "milestone" {
		return false
	} else if !hasID || !validID(id) {
		return false
	}
	if generated {
		return (kind == "project-overview" && (key.Name == "problem-tree" || key.Name == "pinned-decisions" || key.Name == "recent-milestones")) ||
			(kind == "milestone" && key.Name == "evidence")
	}
	for _, spec := range markdownFieldSpecs {
		if spec.EntityKind == kind && spec.Name == key.Name {
			return true
		}
	}
	return false
}

func markdownFieldLimit(key FieldKey, generated bool) int {
	if generated {
		return maxMarkdownDocumentBytes
	}
	if strings.HasPrefix(key.Entity, "problem:") && key.Name == "question" {
		return 4_096
	}
	return maxMarkdownFieldBytes
}

func markdownPhysicalLine(raw []byte, start int) (end, next int) {
	if offset := bytes.IndexByte(raw[start:], '\n'); offset >= 0 {
		end = start + offset
		if end > start && raw[end-1] == '\r' {
			end--
		}
		return end, start + offset + 1
	}
	return len(raw), len(raw)
}

func markdownFenceOpen(line []byte) (byte, int, bool) {
	index := 0
	for index < len(line) && index < 3 && line[index] == ' ' {
		index++
	}
	if index >= len(line) || (line[index] != '`' && line[index] != '~') {
		return 0, 0, false
	}
	character := line[index]
	start := index
	for index < len(line) && line[index] == character {
		index++
	}
	if index-start < 3 || (character == '`' && bytes.IndexByte(line[index:], '`') >= 0) {
		return 0, 0, false
	}
	return character, index - start, true
}

func markdownFenceClose(line []byte, character byte, openingLength int) bool {
	index := 0
	for index < len(line) && index < 3 && line[index] == ' ' {
		index++
	}
	start := index
	for index < len(line) && line[index] == character {
		index++
	}
	if index-start < openingLength {
		return false
	}
	for ; index < len(line); index++ {
		if line[index] != ' ' && line[index] != '\t' {
			return false
		}
	}
	return true
}

func markdownHasBareCR(raw []byte) bool {
	for index, value := range raw {
		if value == '\r' && (index+1 == len(raw) || raw[index+1] != '\n') {
			return true
		}
	}
	return false
}

func markdownError(code, relative string, key FieldKey) error {
	return &MarkdownError{Code: code, Relative: relative, Entity: key.Entity, Field: key.Name}
}
