package syncdoc

import (
	"bytes"
	"sort"
	"strconv"
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

	ancestry := make([]sectionComponent, 6)
	active := make([]bool, 6)
	occurrences := make(map[sectionOccurrenceIdentity]int, len(headings))
	sections := make([]bodySection, 0, len(headings))
	for i, heading := range headings {
		for level := heading.level; level < len(ancestry); level++ {
			active[level] = false
		}
		components := make([]sectionComponent, 0, heading.level)
		for level := 0; level < heading.level-1; level++ {
			if active[level] {
				components = append(components, ancestry[level])
			}
		}
		occurrenceIdentity := sectionOccurrenceIdentity{parent: encodeSectionComponents(components), name: heading.name, level: heading.level}
		occurrences[occurrenceIdentity]++
		component := sectionComponent{name: heading.name, level: heading.level, occurrence: occurrences[occurrenceIdentity]}
		ancestry[heading.level-1] = component
		active[heading.level-1] = true
		components = append(components, component)
		valueEnd := len(source)
		if i+1 < len(headings) {
			valueEnd = headings[i+1].start
		}
		sections = append(sections, bodySection{
			key:     UnitKey{Kind: UnitSection, Name: encodeSectionUnitName(components)},
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
		units[section.key] = Unit{Present: true, Value: bytes.Clone(section.value), HeadingPresentation: bytes.Clone(section.heading)}
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
	entries := make(map[UnitKey]sectionEntry, len(b.sections))
	existingKeys := make(map[UnitKey]struct{}, len(b.sections))
	for order, section := range b.sections {
		existingKeys[section.key] = struct{}{}
		unit, exists := units[section.key]
		if !exists || !unit.Present {
			changed = true
			continue
		}
		identity, err := decodeSectionUnitName(section.key.Name)
		if err != nil {
			return Body{}, false, err
		}
		copy := section
		copy.heading = bytes.Clone(section.heading)
		copy.value = bytes.Clone(unit.Value)
		if len(unit.HeadingPresentation) != 0 {
			copy.heading, err = validatedHeadingPresentation(unit.HeadingPresentation, identity.leaf())
			if err != nil {
				return Body{}, false, err
			}
		}
		if !bytes.Equal(copy.heading, section.heading) || !bytes.Equal(copy.value, section.value) {
			changed = true
		}
		entries[section.key] = sectionEntry{section: copy, identity: identity, existing: true, order: order}
	}
	additionKeys := make(map[UnitKey]struct{})
	for key, unit := range units {
		if key.Kind != UnitSection || !unit.Present {
			continue
		}
		if _, exists := existingKeys[key]; exists {
			continue
		}
		identity, err := decodeSectionUnitName(key.Name)
		if err != nil {
			return Body{}, false, err
		}
		leaf := identity.leaf()
		heading := []byte(strings.Repeat("#", leaf.level) + " " + leaf.name + "\n")
		if len(unit.HeadingPresentation) != 0 {
			heading, err = validatedHeadingPresentation(unit.HeadingPresentation, leaf)
			if err != nil {
				return Body{}, false, err
			}
		}
		entries[key] = sectionEntry{
			section:  bodySection{key: key, heading: heading, value: bytes.Clone(unit.Value), level: leaf.level},
			identity: identity,
		}
		additionKeys[key] = struct{}{}
		changed = true
	}
	if len(entries) > maxBodySections {
		return Body{}, false, invalidDocument("too many Markdown sections")
	}
	ordered, err := orderSectionEntries(entries)
	if err != nil {
		return Body{}, false, err
	}
	result.sections = make([]bodySection, len(ordered))
	for index, entry := range ordered {
		result.sections[index] = entry.section
	}
	if changed {
		reparsed, err := parseBody(result.render())
		if err != nil {
			return Body{}, false, err
		}
		if len(reparsed.sections) != len(result.sections) {
			return Body{}, false, invalidDocument("new section render introduced unexpected structure")
		}
		for index, section := range reparsed.sections {
			if _, added := additionKeys[result.sections[index].key]; added && section.key != result.sections[index].key {
				return Body{}, false, invalidDocument("new section ancestry is not representable at the requested position")
			}
		}
		result = reparsed
	}
	return result, changed, nil
}

type sectionEntry struct {
	section  bodySection
	identity sectionUnitIdentity
	existing bool
	order    int
}

func orderSectionEntries(entries map[UnitKey]sectionEntry) ([]sectionEntry, error) {
	children := make(map[UnitKey][]sectionEntry)
	roots := make([]sectionEntry, 0)
	for _, entry := range entries {
		parent, hasParent := entry.identity.parentKey()
		if !hasParent {
			roots = append(roots, entry)
			continue
		}
		if _, ok := entries[parent]; !ok {
			return nil, invalidDocument("section parent is missing")
		}
		children[parent] = append(children[parent], entry)
	}
	sortSectionSiblings(roots)
	for parent := range children {
		sortSectionSiblings(children[parent])
	}
	ordered := make([]sectionEntry, 0, len(entries))
	var appendTree func(sectionEntry)
	appendTree = func(entry sectionEntry) {
		ordered = append(ordered, entry)
		for _, child := range children[entry.section.key] {
			appendTree(child)
		}
	}
	for _, root := range roots {
		appendTree(root)
	}
	if len(ordered) != len(entries) {
		return nil, invalidDocument("section ancestry is cyclic or unreachable")
	}
	return ordered, nil
}

func sortSectionSiblings(entries []sectionEntry) {
	sort.Slice(entries, func(i, j int) bool {
		first, second := entries[i], entries[j]
		firstLeaf, secondLeaf := first.identity.leaf(), second.identity.leaf()
		if firstLeaf.name == secondLeaf.name && firstLeaf.level == secondLeaf.level && firstLeaf.occurrence != secondLeaf.occurrence {
			return firstLeaf.occurrence < secondLeaf.occurrence
		}
		if first.existing != second.existing {
			return first.existing
		}
		if first.existing {
			return first.order < second.order
		}
		if firstLeaf.name != secondLeaf.name {
			return firstLeaf.name < secondLeaf.name
		}
		if firstLeaf.level != secondLeaf.level {
			return firstLeaf.level < secondLeaf.level
		}
		if firstLeaf.occurrence != secondLeaf.occurrence {
			return firstLeaf.occurrence < secondLeaf.occurrence
		}
		return first.section.key.Name < second.section.key.Name
	})
}

func validatedHeadingPresentation(source []byte, expected sectionComponent) ([]byte, error) {
	parsed, err := parseBody(source)
	if err != nil || len(parsed.preamble) != 0 || len(parsed.sections) != 1 || len(parsed.sections[0].value) != 0 {
		return nil, invalidDocument("invalid section heading presentation")
	}
	identity, err := decodeSectionUnitName(parsed.sections[0].key.Name)
	if err != nil {
		return nil, err
	}
	actual := identity.leaf()
	if actual.name != expected.name || actual.level != expected.level {
		return nil, invalidDocument("section heading presentation does not match unit identity")
	}
	return bytes.Clone(source), nil
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

type sectionComponent struct {
	name       string
	level      int
	occurrence int
}

type sectionOccurrenceIdentity struct {
	parent string
	name   string
	level  int
}

type sectionUnitIdentity struct {
	components []sectionComponent
}

func (identity sectionUnitIdentity) leaf() sectionComponent {
	return identity.components[len(identity.components)-1]
}

func (identity sectionUnitIdentity) parentKey() (UnitKey, bool) {
	if len(identity.components) < 2 {
		return UnitKey{}, false
	}
	return UnitKey{Kind: UnitSection, Name: encodeSectionUnitName(identity.components[:len(identity.components)-1])}, true
}

func decodeSectionUnitName(unitName string) (sectionUnitIdentity, error) {
	encodedComponents, err := splitSectionPath(unitName)
	if err != nil || len(encodedComponents) == 0 || len(encodedComponents) > 6 {
		return sectionUnitIdentity{}, invalidDocument("invalid section ancestry")
	}
	components := make([]sectionComponent, len(encodedComponents))
	previousLevel := 0
	for index, encoded := range encodedComponents {
		component, err := decodeSectionComponent(encoded)
		if err != nil {
			return sectionUnitIdentity{}, err
		}
		if component.level <= previousLevel {
			return sectionUnitIdentity{}, invalidDocument("section ancestry levels must increase")
		}
		components[index] = component
		previousLevel = component.level
	}
	identity := sectionUnitIdentity{components: components}
	if encodeSectionUnitName(components) != unitName {
		return sectionUnitIdentity{}, invalidDocument("non-canonical section unit name")
	}
	return identity, nil
}

func decodeSectionComponent(encoded string) (sectionComponent, error) {
	hash := lastUnescapedByte(encoded, '#')
	if hash <= 0 || hash == len(encoded)-1 {
		return sectionComponent{}, invalidDocument("invalid section unit name")
	}
	for _, digit := range encoded[hash+1:] {
		if digit < '0' || digit > '9' {
			return sectionComponent{}, invalidDocument("invalid section occurrence")
		}
	}
	occurrence, err := strconv.Atoi(encoded[hash+1:])
	if err != nil || occurrence < 1 {
		return sectionComponent{}, invalidDocument("invalid section occurrence")
	}
	identity := encoded[:hash]
	level := 2
	if marker := lastUnescapedByte(identity, '@'); marker >= 0 {
		parsed, err := strconv.Atoi(identity[marker+1:])
		if err != nil || parsed < 1 || parsed > 6 {
			return sectionComponent{}, invalidDocument("invalid section heading level")
		}
		level = parsed
		identity = identity[:marker]
	}
	name, err := unescapeSectionComponent(identity)
	if err != nil {
		return sectionComponent{}, err
	}
	name, err = normalizedHeadingName(name)
	if err != nil {
		return sectionComponent{}, err
	}
	component := sectionComponent{name: name, level: level, occurrence: occurrence}
	if encodeSectionComponent(component) != encoded {
		return sectionComponent{}, invalidDocument("non-canonical section component")
	}
	return component, nil
}

func encodeSectionUnitName(components []sectionComponent) string {
	return encodeSectionComponents(components)
}

func encodeSectionComponents(components []sectionComponent) string {
	encoded := make([]string, len(components))
	for index, component := range components {
		encoded[index] = encodeSectionComponent(component)
	}
	return strings.Join(encoded, " / ")
}

func encodeSectionComponent(component sectionComponent) string {
	identity := escapeSectionComponent(component.name)
	if component.level != 2 {
		identity += "@" + strconv.Itoa(component.level)
	}
	return identity + "#" + strconv.Itoa(component.occurrence)
}

func escapeSectionComponent(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	for _, character := range value {
		switch character {
		case '\\', '/', '#', '@':
			out.WriteByte('\\')
		}
		out.WriteRune(character)
	}
	return out.String()
}

func unescapeSectionComponent(value string) (string, error) {
	var out strings.Builder
	out.Grow(len(value))
	escaped := false
	for _, character := range value {
		if escaped {
			if character != '\\' && character != '/' && character != '#' && character != '@' {
				return "", invalidDocument("invalid section unit escape")
			}
			out.WriteRune(character)
			escaped = false
			continue
		}
		if character == '\\' {
			escaped = true
			continue
		}
		out.WriteRune(character)
	}
	if escaped {
		return "", invalidDocument("trailing section unit escape")
	}
	return out.String(), nil
}

func splitSectionPath(value string) ([]string, error) {
	parts := make([]string, 0, 6)
	start := 0
	escaped := false
	for index := 0; index < len(value); index++ {
		if escaped {
			escaped = false
			continue
		}
		if value[index] == '\\' {
			escaped = true
			continue
		}
		if strings.HasPrefix(value[index:], " / ") {
			if index == start {
				return nil, invalidDocument("empty section ancestry component")
			}
			parts = append(parts, value[start:index])
			index += len(" / ") - 1
			start = index + 1
		}
	}
	if escaped || start >= len(value) {
		return nil, invalidDocument("invalid section ancestry")
	}
	return append(parts, value[start:]), nil
}

func lastUnescapedByte(value string, target byte) int {
	escaped := false
	last := -1
	for index := 0; index < len(value); index++ {
		if escaped {
			escaped = false
			continue
		}
		if value[index] == '\\' {
			escaped = true
			continue
		}
		if value[index] == target {
			last = index
		}
	}
	return last
}

func normalizeLF(value []byte) []byte {
	value = bytes.ReplaceAll(value, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(value, []byte("\r"), []byte("\n"))
}
