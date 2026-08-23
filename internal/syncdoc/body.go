package syncdoc

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

const maxBodySections = 10_000

// Body retains exact Markdown source slices. Section values contain only the
// bytes after their heading and before the next heading; heading source stays
// attached to the section so ATX and Setext presentation survive edits.
type Body struct {
	preamble []byte
	sections []bodySection
}

type bodySection struct {
	key     UnitKey
	heading []byte
	value   []byte
	level   int
}

type sourceHeading struct {
	start, headingEnd, bodyStart int
	level                        int
	name                         string
}

func parseBody(source []byte) (Body, error) {
	headings, err := topLevelHeadings(source)
	if err != nil {
		return Body{}, err
	}
	if len(headings) > maxBodySections {
		return Body{}, invalidDocument("too many Markdown sections")
	}

	ancestry := make([]string, 6)
	occurrences := make(map[string]int, len(headings))
	sections := make([]bodySection, 0, len(headings))
	for i, heading := range headings {
		ancestry[heading.level-1] = heading.name
		for level := heading.level; level < len(ancestry); level++ {
			ancestry[level] = ""
		}
		parts := make([]string, 0, heading.level)
		for level := 0; level < heading.level; level++ {
			if ancestry[level] != "" {
				parts = append(parts, ancestry[level])
			}
		}
		path := strings.Join(parts, " / ")
		occurrences[path]++
		valueEnd := len(source)
		if i+1 < len(headings) {
			valueEnd = headings[i+1].start
		}
		sections = append(sections, bodySection{
			key:     UnitKey{Kind: UnitSection, Name: fmt.Sprintf("%s#%d", path, occurrences[path])},
			heading: bytes.Clone(source[heading.start:heading.bodyStart]),
			value:   bytes.Clone(source[heading.bodyStart:valueEnd]),
			level:   heading.level,
		})
	}
	preambleEnd := len(source)
	if len(headings) != 0 {
		preambleEnd = headings[0].start
	}
	return Body{preamble: bytes.Clone(source[:preambleEnd]), sections: sections}, nil
}

func topLevelHeadings(source []byte) ([]sourceHeading, error) {
	root := goldmark.DefaultParser().Parse(text.NewReader(source))
	headings := make([]sourceHeading, 0)
	for node := root.FirstChild(); node != nil; node = node.NextSibling() {
		heading, ok := node.(*ast.Heading)
		if !ok || heading.Level < 1 || heading.Level > 6 || heading.Lines().Len() == 0 {
			continue
		}
		name, err := normalizedHeadingName(string(heading.Text(source)))
		if err != nil {
			continue
		}
		first := heading.Lines().At(0)
		last := heading.Lines().At(heading.Lines().Len() - 1)
		start := physicalLineStart(source, first.Start)
		contentEnd, afterContent := physicalLineEnd(source, last.Stop)
		headingEnd, bodyStart := contentEnd, afterContent
		if !isSourceATXHeading(source[start:contentEnd], heading.Level) {
			underlineStart := afterContent
			if last.Stop > 0 && last.Stop <= len(source) && source[last.Stop-1] == '\n' {
				underlineStart = last.Stop
			}
			headingEnd, bodyStart = physicalLineEnd(source, underlineStart)
		}
		if headingEnd < start || bodyStart < headingEnd || bodyStart > len(source) {
			return nil, invalidDocument("invalid Markdown heading source position")
		}
		headings = append(headings, sourceHeading{start: start, headingEnd: headingEnd, bodyStart: bodyStart, level: heading.Level, name: name})
	}
	return headings, nil
}

func (b Body) units() UnitSet {
	units := make(UnitSet, len(b.sections)+1)
	units[UnitKey{Kind: UnitPreamble}] = Unit{Present: true, Value: bytes.Clone(b.preamble)}
	for _, section := range b.sections {
		units[section.key] = Unit{Present: true, Value: bytes.Clone(section.value)}
	}
	return units
}

func (b Body) withUnits(units UnitSet) (Body, bool, error) {
	result := Body{}
	preambleKey := UnitKey{Kind: UnitPreamble}
	preamble, ok := units[preambleKey]
	if ok && preamble.Present {
		result.preamble = bytes.Clone(preamble.Value)
	}
	changed := !ok || !preamble.Present || !bytes.Equal(result.preamble, b.preamble)
	seen := make(map[UnitKey]struct{}, len(b.sections))
	for _, section := range b.sections {
		seen[section.key] = struct{}{}
		unit, exists := units[section.key]
		if !exists || !unit.Present {
			changed = true
			continue
		}
		copy := section
		copy.heading = bytes.Clone(section.heading)
		copy.value = bytes.Clone(unit.Value)
		if !bytes.Equal(copy.value, section.value) {
			changed = true
		}
		result.sections = append(result.sections, copy)
	}
	var additions []UnitKey
	for key, unit := range units {
		if key.Kind != UnitSection || !unit.Present {
			continue
		}
		if _, exists := seen[key]; !exists {
			additions = append(additions, key)
		}
	}
	sort.Slice(additions, func(i, j int) bool { return additions[i].Name < additions[j].Name })
	for _, key := range additions {
		name, level, err := headingFromUnitName(key.Name)
		if err != nil {
			return Body{}, false, err
		}
		heading := []byte(strings.Repeat("#", level) + " " + name + "\n")
		result.sections = append(result.sections, bodySection{key: key, heading: heading, value: bytes.Clone(units[key].Value), level: level})
		changed = true
	}
	if len(result.sections) > maxBodySections {
		return Body{}, false, invalidDocument("too many Markdown sections")
	}
	return result, changed, nil
}

func (b Body) render() []byte {
	var out bytes.Buffer
	out.Write(normalizeLF(b.preamble))
	for _, section := range b.sections {
		if out.Len() != 0 && out.Bytes()[out.Len()-1] != '\n' {
			out.WriteByte('\n')
		}
		out.Write(normalizeLF(section.heading))
		if len(section.value) != 0 && out.Len() != 0 && out.Bytes()[out.Len()-1] != '\n' {
			out.WriteByte('\n')
		}
		out.Write(normalizeLF(section.value))
	}
	return out.Bytes()
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
		if end > 0 && source[end-1] == '\r' {
			return end - 1, end + 1
		}
		return end, end + 1
	}
	return len(source), len(source)
}

func isSourceATXHeading(line []byte, level int) bool {
	line = bytes.TrimSuffix(line, []byte("\r"))
	index := 0
	for index < len(line) && index < 3 && line[index] == ' ' {
		index++
	}
	if index+level > len(line) {
		return false
	}
	for offset := 0; offset < level; offset++ {
		if line[index+offset] != '#' {
			return false
		}
	}
	index += level
	return index == len(line) || line[index] == ' ' || line[index] == '\t'
}

func normalizedHeadingName(name string) (string, error) {
	for _, value := range name {
		if unicode.IsControl(value) {
			return "", invalidDocument("invalid Markdown heading")
		}
	}
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "", invalidDocument("empty Markdown heading")
	}
	return name, nil
}

func headingFromUnitName(unitName string) (string, int, error) {
	hash := strings.LastIndexByte(unitName, '#')
	if hash <= 0 || hash == len(unitName)-1 {
		return "", 0, invalidDocument("invalid section unit name")
	}
	for _, digit := range unitName[hash+1:] {
		if digit < '0' || digit > '9' {
			return "", 0, invalidDocument("invalid section occurrence")
		}
	}
	parts := strings.Split(unitName[:hash], " / ")
	if len(parts) == 0 || len(parts) > 6 {
		return "", 0, invalidDocument("invalid section ancestry")
	}
	name, err := normalizedHeadingName(parts[len(parts)-1])
	return name, 2, err
}

func normalizeLF(value []byte) []byte {
	value = bytes.ReplaceAll(value, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(value, []byte("\r"), []byte("\n"))
}
