package syncdoc

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"strings"

	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"gopkg.in/yaml.v3"
)

const v2UnitPrefix = "session-reviewer/"

type v2DocumentState struct {
	entityType string
	spans      []reviewv2.MarkerSpan
	shell      Document
	semantic   UnitSet
	physical   map[UnitKey][]byte
	logical    map[UnitKey][]byte
}

func v2UnitKey(kind, id string) UnitKey {
	return UnitKey{Kind: UnitSection, Name: v2UnitPrefix + kind + "/" + id}
}

func (d Document) v2EntityType() (string, bool) {
	if d.frontmatter == nil {
		return "", false
	}
	schema, ok := mappingValue(d.frontmatter, "schema_version")
	if !ok {
		return "", false
	}
	version, err := positiveInt(schema)
	if err != nil || (version != reviewv2.LegacySchemaVersion && version != reviewv2.SchemaVersion) {
		return "", false
	}
	entityType, err := requiredString(d.frontmatter, "entity_type")
	if err != nil || (entityType != "project_review" && entityType != "project_history") {
		return "", false
	}
	return entityType, true
}

func validatedV2Source(mappingNode *yaml.Node, source []byte) ([]byte, []reviewv2.MarkerSpan, string, error) {
	document := Document{frontmatter: mappingNode}
	entityType, v2 := document.v2EntityType()
	if !v2 {
		if schema, ok := mappingValue(mappingNode, "schema_version"); ok {
			version, err := positiveInt(schema)
			if err == nil && (version == reviewv2.LegacySchemaVersion || version == reviewv2.SchemaVersion) {
				return nil, nil, "", invalidDocument("schema v2 requires a compact review entity type")
			}
		}
		return source, nil, "", nil
	}
	normalized, spans, err := reviewv2.ValidatedMarkerDocument(source, entityType)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: invalid compact review document: %v", ErrInvalidDocument, err)
	}
	return normalized, spans, entityType, nil
}

func buildV2DocumentState(document Document, source []byte, spans []reviewv2.MarkerSpan, entityType string) (*v2DocumentState, error) {
	state := &v2DocumentState{
		entityType: entityType,
		spans:      append([]reviewv2.MarkerSpan(nil), spans...),
		physical:   make(map[UnitKey][]byte, len(spans)),
		logical:    make(map[UnitKey][]byte, len(spans)),
	}
	shellSource, err := v2ShellSource(source, spans, state.physical, state.logical)
	if err != nil {
		return nil, err
	}
	state.shell, err = parseDocument(document.relativePath, shellSource, false)
	if err != nil {
		return nil, invalidDocument("compact review shell cannot be parsed")
	}
	// Compact v2 owns no legacy generated-section headings. Every parser-
	// accepted non-marker section is human content, regardless of its title.
	state.semantic = state.shell.Units()
	for _, span := range spans {
		key := v2UnitKey(span.Kind, span.ID)
		count := replaceUnitBytes(state.semantic, state.physical[key], state.logical[key])
		if count != 1 {
			return nil, invalidDocument("compact review marker position cannot be represented")
		}
		state.semantic[key] = Unit{Present: true, Value: bytes.Clone(source[span.Start:span.End])}
	}
	return state, nil
}

// SensitiveScanSource returns the rendered document with only authoritative
// compact-v2 marker identities replaced by a low-entropy placeholder. The
// complete document is parsed before any replacement, so marker-looking text
// in ordinary Markdown or fenced code remains visible to the sensitive-content
// scanner.
func (d Document) SensitiveScanSource() ([]byte, error) {
	rendered, err := d.Render()
	if err != nil {
		return nil, err
	}
	normalized, spans, entityType, err := validatedV2Source(d.frontmatter, rendered)
	if err != nil {
		return nil, err
	}
	if entityType == "" || len(spans) == 0 {
		return normalized, nil
	}

	var out bytes.Buffer
	previous := 0
	for _, span := range spans {
		if span.Start < previous || span.Start < 0 || span.End <= span.Start || span.End > len(normalized) {
			return nil, invalidDocument("invalid compact review marker scan span")
		}
		lineEndRelative := bytes.IndexByte(normalized[span.Start:span.End], '\n')
		if lineEndRelative < 0 {
			return nil, invalidDocument("compact review marker opening line is incomplete")
		}
		lineEnd := span.Start + lineEndRelative
		expected := []byte(fmt.Sprintf("<!-- session-reviewer:%s id=\"%s\" -->", span.Kind, span.ID))
		if !bytes.Equal(normalized[span.Start:lineEnd], expected) {
			return nil, invalidDocument("compact review marker opening line changed after validation")
		}
		out.Write(normalized[previous:span.Start])
		fmt.Fprintf(&out, "<!-- session-reviewer:%s id=\"validated-marker-id\" -->", span.Kind)
		out.Write(normalized[lineEnd:span.End])
		previous = span.End
	}
	out.Write(normalized[previous:])
	return out.Bytes(), nil
}

func (d Document) withV2SemanticUnits(units UnitSet) (Document, error) {
	entityType, ok := d.v2EntityType()
	if !ok || d.v2 == nil || d.v2.entityType != entityType {
		return Document{}, invalidDocument("compact review semantic state is unavailable")
	}
	generic := make(UnitSet, len(units))
	markers := make(map[UnitKey]Unit, len(d.v2.spans))
	for key, unit := range units {
		if isV2MarkerUnitKey(key) {
			markers[key] = cloneUnit(unit)
			continue
		}
		generic[key] = cloneUnit(unit)
	}
	if len(markers) != len(d.v2.spans) {
		return Document{}, invalidDocument("compact review marker units cannot be added or deleted")
	}
	for _, span := range d.v2.spans {
		key := v2UnitKey(span.Kind, span.ID)
		unit, found := markers[key]
		if !found || !unit.Present || len(unit.KeyPresentation) != 0 || len(unit.HeadingPresentation) != 0 {
			return Document{}, invalidDocument("invalid compact review marker unit")
		}
		if count := replaceUnitBytes(generic, d.v2.logical[key], d.v2.physical[key]); count != 1 {
			return Document{}, invalidDocument("compact review marker position was changed")
		}
	}
	rebuiltShell, err := d.v2.shell.WithUnits(generic)
	if err != nil {
		return Document{}, err
	}
	rebuilt, err := rebuiltShell.Render()
	if err != nil {
		return Document{}, err
	}
	for _, span := range d.v2.spans {
		key := v2UnitKey(span.Kind, span.ID)
		physical := d.v2.physical[key]
		if bytes.Count(rebuilt, physical) != 1 {
			return Document{}, invalidDocument("compact review marker position was changed")
		}
		rebuilt = bytes.Replace(rebuilt, physical, markers[key].Value, 1)
	}
	result, err := Parse(d.relativePath, rebuilt)
	if err != nil {
		return Document{}, invalidDocument("rebuilt compact review document cannot be reparsed")
	}
	return result, nil
}

func v2ShellSource(source []byte, spans []reviewv2.MarkerSpan, physical, logical map[UnitKey][]byte) ([]byte, error) {
	var out bytes.Buffer
	previous := 0
	for _, span := range spans {
		if span.Start < previous || span.Start < 0 || span.End <= span.Start || span.End > len(source) {
			return nil, invalidDocument("invalid compact review marker span")
		}
		key := v2UnitKey(span.Kind, span.ID)
		var anchor []byte
		for {
			nonce := make([]byte, 32)
			if _, err := rand.Read(nonce); err != nil {
				return nil, invalidDocument("compact review shell anchor cannot be generated")
			}
			anchor = []byte(fmt.Sprintf("<!-- sr-v2-anchor:%x -->\n", nonce))
			if !bytes.Contains(source, anchor) && !bytes.Contains(out.Bytes(), anchor) {
				break
			}
		}
		physical[key] = anchor
		logical[key] = []byte("\x00sr-v2-unit:" + span.Kind + ":" + span.ID + "\x00\n")
		out.Write(source[previous:span.Start])
		out.Write(anchor)
		previous = span.End
	}
	out.Write(source[previous:])
	return out.Bytes(), nil
}

func replaceUnitBytes(units UnitSet, old, replacement []byte) int {
	count := 0
	for key, unit := range units {
		hits := bytes.Count(unit.Value, old)
		if hits == 0 {
			continue
		}
		count += hits
		unit.Value = bytes.ReplaceAll(unit.Value, old, replacement)
		units[key] = unit
	}
	return count
}

func cloneV2DocumentState(state *v2DocumentState) *v2DocumentState {
	if state == nil {
		return nil
	}
	result := &v2DocumentState{
		entityType: state.entityType,
		spans:      append([]reviewv2.MarkerSpan(nil), state.spans...),
		shell:      cloneDocument(state.shell),
		semantic:   cloneUnitSet(state.semantic),
		physical:   make(map[UnitKey][]byte, len(state.physical)),
		logical:    make(map[UnitKey][]byte, len(state.logical)),
	}
	for key, value := range state.physical {
		result.physical[key] = bytes.Clone(value)
	}
	for key, value := range state.logical {
		result.logical[key] = bytes.Clone(value)
	}
	return result
}

func isV2MarkerUnitKey(key UnitKey) bool {
	if key.Kind != UnitSection || !strings.HasPrefix(key.Name, v2UnitPrefix) {
		return false
	}
	parts := strings.Split(key.Name, "/")
	return len(parts) == 3 && parts[0] == "session-reviewer" &&
		(parts[1] == "risk" || parts[1] == "decision" || parts[1] == "event") && parts[2] != ""
}
