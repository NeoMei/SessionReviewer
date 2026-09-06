package syncdoc

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"gopkg.in/yaml.v3"
)

const (
	v4MaxDocumentBytes = 64 << 20
	v4UnitPrefix       = "session-reviewer/v4/"
)

type v4BlockState struct {
	block               reviewv4.MarkdownBlock
	unitKey             UnitKey
	physicalPlaceholder []byte
	semanticPlaceholder []byte
	original            []byte
}

type v4FrontmatterSpan struct {
	start, end, delimiter int
}

type v4DocumentState struct {
	ledger             reviewv4.MachineLedger
	markdown           reviewv4.MarkdownDocument
	blocks             []v4BlockState
	shell              Document
	shellAll           UnitSet
	shellMachine       UnitSet
	semantic           UnitSet
	frontmatter        map[UnitKey]v4FrontmatterSpan
	frontmatterOrder   []UnitKey
	frontmatterFlow    bool
	frontmatterClose   int
	physicalToSemantic map[string][]byte
	physicalPrefix     []byte
	frontStart         int
	frontEnd           int
	bodyStart          int
}

func ParseV4(relative string, content []byte, ledger reviewv4.MachineLedger) (Document, error) {
	if err := validateRelativePath(relative); err != nil {
		return Document{}, err
	}
	markdown, err := reviewv4.ParseMarkdownDocumentAgainstLedger(relative, content, ledger)
	if err != nil {
		return Document{}, fmt.Errorf("%w: invalid v4 markdown document: %v", ErrInvalidDocument, err)
	}
	state := &v4DocumentState{ledger: ledger, markdown: markdown}
	shellSource, err := state.buildShell(content, markdown.Blocks())
	if err != nil {
		return Document{}, err
	}
	shell, err := parseDocumentBounded(relative, shellSource, false, v4MaxDocumentBytes)
	if err != nil {
		return Document{}, fmt.Errorf("%w: v4 Markdown shell cannot be parsed", ErrInvalidDocument)
	}
	state.shell = shell
	state.shellAll = make(UnitSet)
	for key, unit := range shell.Units() {
		state.shellAll[key] = state.semanticShellUnit(unit)
	}
	if err := state.indexFrontmatter(shellSource); err != nil {
		return Document{}, err
	}
	state.shellMachine = make(UnitSet)
	state.semantic = make(UnitSet)
	for key, unit := range state.shellAll {
		if key.Kind == UnitFrontmatter && v4MachineFrontmatter(key.Name) {
			state.shellMachine[key] = unit
			continue
		}
		state.semantic[key] = unit
	}
	for _, block := range state.blocks {
		if block.block.Generated {
			continue
		}
		state.semantic[block.unitKey] = Unit{Present: true, Value: bytes.Clone(content[block.block.ValueStart:block.block.ValueEnd])}
	}
	document := shell
	document.raw = bytes.Clone(content)
	document.dirty = false
	document.v4 = state
	return document, nil
}

func (state *v4DocumentState) buildShell(source []byte, blocks []reviewv4.MarkdownBlock) ([]byte, error) {
	var out bytes.Buffer
	previous := 0
	state.blocks = make([]v4BlockState, 0, len(blocks))
	nonce := make([]byte, 16)
	for {
		if _, err := rand.Read(nonce); err != nil {
			return nil, invalidDocument("v4 Markdown shell placeholder cannot be generated")
		}
		if !bytes.Contains(source, []byte(fmt.Sprintf("<!-- sr-v4-%x-block:", nonce))) {
			break
		}
	}
	state.physicalPrefix = []byte(fmt.Sprintf("<!-- sr-v4-%x-block:", nonce))
	state.physicalToSemantic = make(map[string][]byte, len(blocks))
	for _, block := range blocks {
		if block.Start < previous || block.Start < 0 || block.End <= block.Start || block.End > len(source) {
			return nil, invalidDocument("invalid v4 Markdown block span")
		}
		identity := v4PlaceholderIdentity(block)
		physical := []byte(fmt.Sprintf("<!-- sr-v4-%x-block:%s -->\n", nonce, identity))
		semantic := []byte(fmt.Sprintf("<!-- sr-v4-block:%s -->\n", identity))
		unitKey := UnitKey{Kind: UnitSection, Name: v4UnitPrefix + block.Key.Entity + "/" + block.Key.Name}
		state.blocks = append(state.blocks, v4BlockState{
			block: block, unitKey: unitKey, physicalPlaceholder: physical, semanticPlaceholder: semantic, original: bytes.Clone(source[block.Start:block.End]),
		})
		state.physicalToSemantic[string(physical)] = semantic
		out.Write(source[previous:block.Start])
		out.Write(physical)
		previous = block.End
	}
	out.Write(source[previous:])
	return out.Bytes(), nil
}

func v4PlaceholderIdentity(block reviewv4.MarkdownBlock) string {
	kind := "field"
	if block.Generated {
		kind = "generated"
	}
	return kind + ":" + hex.EncodeToString([]byte(block.Key.Entity)) + ":" + hex.EncodeToString([]byte(block.Key.Name))
}

func (state *v4DocumentState) semanticShellUnit(unit Unit) Unit {
	unit.Value = replaceV4PlaceholderTokens(unit.Value, state.physicalPrefix, state.physicalToSemantic)
	unit.HeadingPresentation = replaceV4PlaceholderTokens(unit.HeadingPresentation, state.physicalPrefix, state.physicalToSemantic)
	return unit
}

func replaceV4PlaceholderTokens(source, prefix []byte, byToken map[string][]byte) []byte {
	var out bytes.Buffer
	cursor := 0
	for cursor < len(source) {
		offset := bytes.Index(source[cursor:], prefix)
		if offset < 0 {
			break
		}
		start := cursor + offset
		endOffset := bytes.IndexByte(source[start:], '\n')
		if endOffset < 0 {
			break
		}
		end := start + endOffset + 1
		replacement, ok := byToken[string(source[start:end])]
		if !ok {
			out.Write(source[cursor:end])
			cursor = end
			continue
		}
		out.Write(source[cursor:start])
		out.Write(replacement)
		cursor = end
	}
	out.Write(source[cursor:])
	return out.Bytes()
}

func (state *v4DocumentState) indexFrontmatter(source []byte) error {
	frontmatter, body, err := splitFrontmatter(source)
	if err != nil {
		return err
	}
	state.frontStart = bytes.Index(source, frontmatter)
	state.frontEnd = state.frontStart + len(frontmatter)
	state.bodyStart = len(source) - len(body)
	if state.frontStart < 0 || state.frontEnd > state.bodyStart {
		return invalidDocument("invalid v4 Markdown frontmatter position")
	}
	lineStarts := []int{0}
	for index, value := range frontmatter {
		if value == '\n' && index+1 < len(frontmatter) {
			lineStarts = append(lineStarts, index+1)
		}
	}
	keys, starts := make([]UnitKey, 0, len(state.shell.frontmatter.Content)/2), make([]int, 0, len(state.shell.frontmatter.Content)/2)
	for index := 0; index+1 < len(state.shell.frontmatter.Content); index += 2 {
		keyNode := state.shell.frontmatter.Content[index]
		start, ok := v4YAMLNodeOffset(frontmatter, lineStarts, keyNode)
		if !ok {
			return invalidDocument("invalid v4 Markdown frontmatter key position")
		}
		keys = append(keys, UnitKey{Kind: UnitFrontmatter, Name: keyNode.Value})
		starts = append(starts, state.frontStart+start)
	}
	state.frontmatterOrder = append([]UnitKey(nil), keys...)
	state.frontmatter = make(map[UnitKey]v4FrontmatterSpan, len(keys))
	if state.shell.frontmatter.Style&yaml.FlowStyle != 0 {
		return state.indexFlowFrontmatter(source, frontmatter, lineStarts, keys, starts)
	}
	for index, key := range keys {
		end := state.frontEnd
		if index+1 < len(starts) {
			end = starts[index+1]
		}
		state.frontmatter[key] = v4FrontmatterSpan{start: starts[index], end: end, delimiter: end}
	}
	return nil
}

func (state *v4DocumentState) indexFlowFrontmatter(source, frontmatter []byte, lineStarts []int, keys []UnitKey, starts []int) error {
	open, ok := v4YAMLNodeOffset(frontmatter, lineStarts, state.shell.frontmatter)
	if !ok || open >= len(frontmatter) || frontmatter[open] != '{' {
		return invalidDocument("invalid v4 Markdown flow frontmatter position")
	}
	state.frontmatterFlow = true
	frontmatterOpen := state.frontStart + open
	for index, key := range keys {
		valueNode := state.shell.frontmatter.Content[index*2+1]
		valueStart, ok := v4YAMLNodeOffset(frontmatter, lineStarts, valueNode)
		if !ok {
			return invalidDocument("invalid v4 Markdown flow frontmatter value position")
		}
		delimiter, ok := v4FlowEntryDelimiter(frontmatter, valueStart)
		if !ok || index+1 < len(keys) && frontmatter[delimiter] != ',' || index+1 == len(keys) && frontmatter[delimiter] != '}' {
			return invalidDocument("invalid v4 Markdown flow frontmatter entry boundary")
		}
		end := delimiter
		for end > valueStart && (frontmatter[end-1] == ' ' || frontmatter[end-1] == '\t' || frontmatter[end-1] == '\r' || frontmatter[end-1] == '\n') {
			end--
		}
		absoluteDelimiter := state.frontStart + delimiter
		state.frontmatter[key] = v4FrontmatterSpan{
			start:     starts[index],
			end:       state.frontStart + end,
			delimiter: absoluteDelimiter,
		}
		if index+1 == len(keys) {
			state.frontmatterClose = absoluteDelimiter
		}
	}
	if state.frontmatterClose < frontmatterOpen || state.frontmatterClose >= state.frontEnd || source[state.frontmatterClose] != '}' {
		return invalidDocument("invalid v4 Markdown flow frontmatter close")
	}
	return nil
}

func v4YAMLNodeOffset(source []byte, lineStarts []int, node *yaml.Node) (int, bool) {
	if node == nil || node.Line < 1 || node.Line > len(lineStarts) || node.Column < 1 {
		return 0, false
	}
	offset := lineStarts[node.Line-1]
	for column := 1; column < node.Column; column++ {
		if offset >= len(source) || source[offset] == '\r' || source[offset] == '\n' {
			return 0, false
		}
		_, size := utf8.DecodeRune(source[offset:])
		if size == 0 {
			return 0, false
		}
		offset += size
	}
	return offset, offset < len(source)
}

func v4FlowEntryDelimiter(source []byte, start int) (int, bool) {
	braceDepth, bracketDepth := 0, 0
	inSingle, inDouble, inComment := false, false, false
	for index := start; index < len(source); index++ {
		value := source[index]
		if inComment {
			if value == '\n' {
				inComment = false
			}
			continue
		}
		if inSingle {
			if value == '\'' {
				if index+1 < len(source) && source[index+1] == '\'' {
					index++
				} else {
					inSingle = false
				}
			}
			continue
		}
		if inDouble {
			if value == '\\' {
				index++
			} else if value == '"' {
				inDouble = false
			}
			continue
		}
		switch value {
		case '#':
			if index == start || source[index-1] == ' ' || source[index-1] == '\t' || source[index-1] == '\r' || source[index-1] == '\n' {
				inComment = true
			}
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '{':
			braceDepth++
		case '[':
			bracketDepth++
		case ']':
			if bracketDepth == 0 {
				return 0, false
			}
			bracketDepth--
		case '}':
			if braceDepth == 0 && bracketDepth == 0 {
				return index, true
			}
			if braceDepth == 0 {
				return 0, false
			}
			braceDepth--
		case ',':
			if braceDepth == 0 && bracketDepth == 0 {
				return index, true
			}
		}
	}
	return 0, false
}

func v4MachineFrontmatter(name string) bool {
	switch name {
	case "id", "entity_type", "project_id", "schema_version", "document_format", "revision", "generation_id", "minimum_reader_version", "minimum_writer_version":
		return true
	default:
		return strings.HasPrefix(name, "session_reviewer_")
	}
}

func preflightV4UnitSet(units UnitSet, limit int) error {
	remaining := limit
	consume := func(length int) bool {
		if length < 0 || remaining < length {
			return false
		}
		remaining -= length
		return true
	}
	if limit < 0 {
		return invalidDocument("rendered v4 Markdown document exceeds size limit")
	}
	for key, unit := range units {
		if !unit.Present {
			continue
		}
		if !consume(len(unit.Value)) || !consume(len(unit.KeyPresentation)) || !consume(len(unit.HeadingPresentation)) {
			return invalidDocument("rendered v4 Markdown document exceeds size limit")
		}
		switch key.Kind {
		case UnitFrontmatter:
			if !consume(len(key.Name) + 4) {
				return invalidDocument("rendered v4 Markdown document exceeds size limit")
			}
		case UnitSection:
			if len(unit.HeadingPresentation) == 0 && !consume(len(key.Name)+8) {
				return invalidDocument("rendered v4 Markdown document exceeds size limit")
			}
		}
	}
	return nil
}

func (d Document) withV4SemanticUnits(units UnitSet) (Document, error) {
	if d.v4 == nil {
		return Document{}, invalidDocument("v4 Markdown semantic state is unavailable")
	}
	if err := preflightV4UnitSet(units, v4MaxDocumentBytes); err != nil {
		return Document{}, err
	}
	input := cloneUnitSet(units)
	fields := make(map[UnitKey]Unit)
	shell := make(UnitSet)
	expectedFields := make(map[UnitKey]v4BlockState)
	for _, block := range d.v4.blocks {
		if !block.block.Generated {
			expectedFields[block.unitKey] = block
		}
	}
	for key, unit := range input {
		if strings.HasPrefix(key.Name, v4UnitPrefix) {
			_, ok := expectedFields[key]
			if !ok || key.Kind != UnitSection || !unit.Present || len(unit.KeyPresentation) != 0 || len(unit.HeadingPresentation) != 0 {
				return Document{}, invalidDocument("v4 Markdown field units cannot be added or changed structurally")
			}
			fields[key] = unit
			continue
		}
		if key.Kind == UnitFrontmatter && v4MachineFrontmatter(key.Name) {
			return Document{}, ErrReservedField
		}
		shell[key] = unit
	}
	if len(fields) != len(expectedFields) {
		return Document{}, invalidDocument("v4 Markdown field units cannot be added or deleted")
	}
	fieldReplacements := make(map[reviewv4.FieldKey]string, len(fields))
	for key, unit := range fields {
		fieldReplacements[expectedFields[key].block.Key] = string(unit.Value)
	}
	renderedFields, err := d.v4.markdown.ReplaceFields(fieldReplacements)
	if err != nil {
		return Document{}, fmt.Errorf("%w: invalid v4 Markdown field", ErrInvalidDocument)
	}

	shellUnchanged := true
	for _, key := range unionUnitKeys(shell, d.v4.semantic) {
		if strings.HasPrefix(key.Name, v4UnitPrefix) {
			continue
		}
		if !unitsEqual(shell[key], d.v4.semantic[key]) {
			shellUnchanged = false
			break
		}
	}
	if shellUnchanged {
		return ParseV4(d.relativePath, renderedFields, d.v4.ledger)
	}

	combined := make(UnitSet, len(shell)+len(d.v4.shellMachine))
	for key, unit := range d.v4.shellMachine {
		combined[key] = unit
	}
	for key, unit := range shell {
		combined[key] = unit
	}
	nextShell, err := d.v4.shell.WithUnits(combined)
	if err != nil {
		return Document{}, err
	}
	renderedShell, err := d.rewriteV4Shell(nextShell, combined)
	if err != nil {
		return Document{}, err
	}
	renderedShell, err = replaceV4Placeholders(renderedShell, d.v4.blocks, fields)
	if err != nil {
		return Document{}, err
	}
	return ParseV4(d.relativePath, renderedShell, d.v4.ledger)
}

func (d Document) v4SensitiveScanSource() ([]byte, error) {
	if d.v4 == nil {
		return nil, invalidDocument("v4 Markdown semantic state is unavailable")
	}
	source, err := d.v4.markdown.SensitiveScanSource()
	if err != nil {
		return nil, invalidDocument("v4 Markdown sensitive scan source is unavailable")
	}
	return source, nil
}

func cloneV4DocumentState(state *v4DocumentState) *v4DocumentState {
	if state == nil {
		return nil
	}
	copy := &v4DocumentState{
		ledger: state.ledger, markdown: state.markdown, shell: cloneDocument(state.shell),
		shellAll: cloneUnitSet(state.shellAll), shellMachine: cloneUnitSet(state.shellMachine), semantic: cloneUnitSet(state.semantic),
		blocks: make([]v4BlockState, len(state.blocks)), physicalPrefix: bytes.Clone(state.physicalPrefix),
		physicalToSemantic: make(map[string][]byte, len(state.physicalToSemantic)),
	}
	for index, block := range state.blocks {
		copy.blocks[index] = v4BlockState{block: block.block, unitKey: block.unitKey, physicalPlaceholder: bytes.Clone(block.physicalPlaceholder), semanticPlaceholder: bytes.Clone(block.semanticPlaceholder), original: bytes.Clone(block.original)}
	}
	copy.frontmatter = make(map[UnitKey]v4FrontmatterSpan, len(state.frontmatter))
	for key, span := range state.frontmatter {
		copy.frontmatter[key] = span
	}
	copy.frontmatterOrder = append([]UnitKey(nil), state.frontmatterOrder...)
	copy.frontmatterFlow = state.frontmatterFlow
	copy.frontmatterClose = state.frontmatterClose
	for token, replacement := range state.physicalToSemantic {
		copy.physicalToSemantic[token] = bytes.Clone(replacement)
	}
	copy.frontStart, copy.frontEnd, copy.bodyStart = state.frontStart, state.frontEnd, state.bodyStart
	return copy
}
