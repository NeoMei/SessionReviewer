package codex

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// literalToolArgument accepts only a flat object of scalar literals or a string.
// It never evaluates JavaScript, interpolates variables, or calls user code.
func (p *execWrapperParser) literalToolArgument() (json.RawMessage, bool) {
	p.skipSpace()
	if p.offset >= len(p.source) {
		return nil, false
	}
	if p.source[p.offset] == '\'' || p.source[p.offset] == '"' {
		v, ok := p.literalString()
		if !ok {
			return nil, false
		}
		b, _ := json.Marshal(v)
		return b, true
	}
	if !p.punct('{') {
		return nil, false
	}
	object := map[string]json.RawMessage{}
	for {
		if p.punct('}') {
			b, _ := json.Marshal(object)
			return b, true
		}
		p.skipSpace()
		var key string
		var ok bool
		if p.offset < len(p.source) && (p.source[p.offset] == '\'' || p.source[p.offset] == '"') {
			key, ok = p.literalString()
		} else {
			key, ok = p.identifier()
		}
		if !ok || key == "__proto__" || !p.punct(':') {
			return nil, false
		}
		if _, exists := object[key]; exists {
			return nil, false
		}
		p.skipSpace()
		if p.offset >= len(p.source) {
			return nil, false
		}
		var raw json.RawMessage
		if p.source[p.offset] == '\'' || p.source[p.offset] == '"' {
			v, valid := p.literalString()
			if !valid {
				return nil, false
			}
			raw, _ = json.Marshal(v)
		} else {
			dec := json.NewDecoder(strings.NewReader(p.source[p.offset:]))
			var value any
			dec.UseNumber()
			if dec.Decode(&value) != nil {
				return nil, false
			}
			switch value.(type) {
			case nil, bool, json.Number:
			default:
				return nil, false
			}
			raw = []byte(p.source[p.offset : p.offset+int(dec.InputOffset())])
			p.offset += int(dec.InputOffset())
		}
		object[key] = raw
		if p.punct('}') {
			b, err := json.Marshal(object)
			return b, err == nil
		}
		if !p.punct(',') {
			return nil, false
		}
	}
}

func (p *execWrapperParser) literalString() (string, bool) {
	p.skipSpace()
	if p.offset >= len(p.source) {
		return "", false
	}
	quote := p.source[p.offset]
	if quote != '\'' && quote != '"' {
		return "", false
	}
	p.offset++
	var b strings.Builder
	for p.offset < len(p.source) {
		c := p.source[p.offset]
		if c == quote {
			p.offset++
			return b.String(), true
		}
		if c < 0x20 {
			return "", false
		}
		if c != '\\' {
			r, n := utf8.DecodeRuneInString(p.source[p.offset:])
			if r == utf8.RuneError && n == 1 {
				return "", false
			}
			b.WriteRune(r)
			p.offset += n
			continue
		}
		if p.offset+1 >= len(p.source) {
			return "", false
		}
		next := p.source[p.offset+1]
		if next == '/' {
			b.WriteByte('/')
			p.offset += 2
			continue
		}
		// Only escapes shared by JSON/JavaScript are accepted. No octal, hex,
		// line continuation, or unknown escapes that could change command meaning.
		if !strings.ContainsRune("\\\"'bfnrtu", rune(next)) {
			return "", false
		}
		if next == '\'' || next == '"' {
			b.WriteByte(next)
			p.offset += 2
			continue
		}
		r, _, tail, err := strconv.UnquoteChar(p.source[p.offset:], quote)
		if err != nil {
			return "", false
		}
		p.offset = len(p.source) - len(tail)
		b.WriteRune(r)
	}
	return "", false
}
