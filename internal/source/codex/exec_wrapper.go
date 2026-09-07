package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

const maxExecWrapperBytes = 1 << 20

type execWrapperResultMode uint8

const (
	execWrapperResultNone execWrapperResultMode = iota
	execWrapperResultWhole
	execWrapperResultBody
)

type literalExecWrapper struct {
	name       string
	input      string
	resultMode execWrapperResultMode
}

func decodeLiteralExecWrapper(source string) (literalExecWrapper, bool) {
	if source == "" || len(source) > maxExecWrapperBytes {
		return literalExecWrapper{}, false
	}
	p := execWrapperParser{source: source}
	if !p.consumeDirective() {
		return literalExecWrapper{}, false
	}
	start := p.offset
	if wrapper, ok := p.parseInline(); ok {
		return wrapper, true
	}
	p.offset = start
	return p.parseAssigned()
}

type execWrapperParser struct {
	source string
	offset int
}

func (p *execWrapperParser) consumeDirective() bool {
	p.skipSpace()
	if !strings.HasPrefix(p.source[p.offset:], "// @exec:") {
		return true
	}
	lineEnd := strings.IndexByte(p.source[p.offset:], '\n')
	if lineEnd < 0 {
		return false
	}
	lineEnd += p.offset
	pragma := strings.TrimSpace(strings.TrimSuffix(p.source[p.offset+len("// @exec:"):lineEnd], "\r"))
	if _, err := decodeUniqueJSONObject([]byte(pragma)); err != nil {
		return false
	}
	p.offset = lineEnd + 1
	p.skipSpace()
	return true
}

func (p *execWrapperParser) parseInline() (literalExecWrapper, bool) {
	if !p.keyword("text") || !p.punct('(') || !p.keyword("await") {
		return literalExecWrapper{}, false
	}
	wrapper, ok := p.toolCall()
	if !ok || !p.punct(')') || !p.punct(';') || !p.finished() {
		return literalExecWrapper{}, false
	}
	wrapper.resultMode = execWrapperResultWhole
	return wrapper, true
}

func (p *execWrapperParser) parseAssigned() (literalExecWrapper, bool) {
	if !p.keyword("const") {
		return literalExecWrapper{}, false
	}
	name, ok := p.identifier()
	if !ok || name != "r" || !p.punct('=') || !p.keyword("await") {
		return literalExecWrapper{}, false
	}
	wrapper, ok := p.toolCall()
	if !ok || !p.punct(';') || !p.keyword("text") || !p.punct('(') {
		return literalExecWrapper{}, false
	}
	reference, ok := p.identifier()
	if !ok || reference != name {
		return literalExecWrapper{}, false
	}
	wrapper.resultMode = execWrapperResultWhole
	if p.punct('.') {
		selector, ok := p.identifier()
		if !ok || selector != "output" {
			return literalExecWrapper{}, false
		}
		wrapper.resultMode = execWrapperResultBody
	}
	if !p.punct(')') || !p.punct(';') || !p.finished() {
		return literalExecWrapper{}, false
	}
	return wrapper, true
}

func (p *execWrapperParser) toolCall() (literalExecWrapper, bool) {
	if !p.keyword("tools") || !p.punct('.') {
		return literalExecWrapper{}, false
	}
	name, ok := p.identifier()
	if !ok || (name != "exec_command" && name != "apply_patch") || !p.punct('(') {
		return literalExecWrapper{}, false
	}
	raw, ok := p.jsonLiteral()
	if !ok || !p.punct(')') {
		return literalExecWrapper{}, false
	}
	input, ok := normalizeLiteralToolInput(name, raw)
	if !ok {
		return literalExecWrapper{}, false
	}
	return literalExecWrapper{name: name, input: input}, true
}

func normalizeLiteralToolInput(name string, raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", false
	}
	switch trimmed[0] {
	case '{':
		if _, err := decodeUniqueJSONObject(trimmed); err != nil {
			return "", false
		}
		input := string(trimmed)
		if name == "exec_command" {
			_, _, valid := decodeExecCommandInput(input)
			return input, valid
		}
		_, _, valid := decodePatchInput(input)
		return input, valid
	case '"':
		if name != "apply_patch" {
			return "", false
		}
		var value string
		if json.Unmarshal(trimmed, &value) != nil {
			return "", false
		}
		_, _, valid := decodePatchInput(value)
		return value, valid
	default:
		return "", false
	}
}

func (p *execWrapperParser) jsonLiteral() (json.RawMessage, bool) {
	p.skipSpace()
	if p.offset >= len(p.source) {
		return nil, false
	}
	decoder := json.NewDecoder(strings.NewReader(p.source[p.offset:]))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil || len(raw) == 0 {
		return nil, false
	}
	p.offset += int(decoder.InputOffset())
	return raw, true
}

func (p *execWrapperParser) keyword(value string) bool {
	p.skipSpace()
	if !strings.HasPrefix(p.source[p.offset:], value) {
		return false
	}
	end := p.offset + len(value)
	if end < len(p.source) && identifierByte(p.source[end], false) {
		return false
	}
	p.offset = end
	return true
}

func (p *execWrapperParser) identifier() (string, bool) {
	p.skipSpace()
	start := p.offset
	if start >= len(p.source) || !identifierByte(p.source[start], true) {
		return "", false
	}
	p.offset++
	for p.offset < len(p.source) && identifierByte(p.source[p.offset], false) {
		p.offset++
	}
	return p.source[start:p.offset], true
}

func identifierByte(value byte, first bool) bool {
	if value == '_' || value == '$' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' {
		return true
	}
	return !first && value >= '0' && value <= '9'
}

func (p *execWrapperParser) punct(value byte) bool {
	p.skipSpace()
	if p.offset >= len(p.source) || p.source[p.offset] != value {
		return false
	}
	p.offset++
	return true
}

func (p *execWrapperParser) finished() bool {
	p.skipSpace()
	return p.offset == len(p.source)
}

func (p *execWrapperParser) skipSpace() {
	for p.offset < len(p.source) {
		switch p.source[p.offset] {
		case ' ', '\t', '\r', '\n':
			p.offset++
		default:
			return
		}
	}
}

func decodeExecWrapperOutput(raw json.RawMessage, mode execWrapperResultMode) (terminalOutput, error) {
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) != 2 {
		return terminalOutput{}, errors.New("unsupported exec wrapper output count")
	}
	status, err := decodeExecWrapperTextBlock(blocks[0])
	if err != nil || !validExecWrapperStatus(status) {
		return terminalOutput{}, errors.New("unsupported exec wrapper status")
	}
	result, err := decodeExecWrapperTextBlock(blocks[1])
	if err != nil {
		return terminalOutput{}, errors.New("unsupported exec wrapper result block")
	}
	if mode == execWrapperResultBody {
		return terminalOutput{body: result}, nil
	}
	trimmed := strings.TrimSpace(result)
	if mode != execWrapperResultWhole || len(trimmed) == 0 || trimmed[0] != '{' {
		return terminalOutput{}, errors.New("unsupported exec wrapper result")
	}
	return decodeTerminalOutput(json.RawMessage(trimmed))
}

func decodePendingToolOutput(raw json.RawMessage, mode execWrapperResultMode) (terminalOutput, error) {
	if mode != execWrapperResultNone {
		return decodeExecWrapperOutput(raw, mode)
	}
	return decodeTerminalOutput(raw)
}

func decodeExecWrapperTextBlock(raw json.RawMessage) (string, error) {
	object, err := decodeUniqueJSONObject(raw)
	if err != nil || len(object) != 2 {
		return "", errors.New("unsupported exec wrapper block")
	}
	var blockType, text string
	if json.Unmarshal(object["type"], &blockType) != nil || blockType != "input_text" ||
		json.Unmarshal(object["text"], &text) != nil {
		return "", errors.New("unsupported exec wrapper text block")
	}
	return text, nil
}

func validExecWrapperStatus(value string) bool {
	first, _, _ := strings.Cut(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	return first == "Script completed" || first == "Script failed" || strings.HasPrefix(first, "Script running with cell ID ")
}
