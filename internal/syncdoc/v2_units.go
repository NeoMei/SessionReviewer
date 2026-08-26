package syncdoc

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"gopkg.in/yaml.v3"
)

const v2UnitPrefix = "session-reviewer/"

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
	if err != nil || version != reviewv2.SchemaVersion {
		return "", false
	}
	entityType, err := requiredString(d.frontmatter, "entity_type")
	if err != nil || (entityType != "project_review" && entityType != "project_history") {
		return "", false
	}
	return entityType, true
}

func validatedV2Source(mappingNode *yaml.Node, source []byte) error {
	document := Document{frontmatter: mappingNode}
	entityType, v2 := document.v2EntityType()
	if !v2 {
		if schema, ok := mappingValue(mappingNode, "schema_version"); ok {
			version, err := positiveInt(schema)
			if err == nil && version == reviewv2.SchemaVersion {
				return invalidDocument("schema v2 requires a compact review entity type")
			}
		}
		return nil
	}
	if _, err := reviewv2.ValidatedMarkerSpans(source, entityType); err != nil {
		return invalidDocument("invalid compact review document")
	}
	return nil
}

func (d Document) v2SemanticUnits() (UnitSet, error) {
	entityType, ok := d.v2EntityType()
	if !ok {
		return nil, invalidDocument("not a compact review document")
	}
	source, err := d.Render()
	if err != nil {
		return nil, err
	}
	spans, err := reviewv2.ValidatedMarkerSpans(source, entityType)
	if err != nil {
		return nil, invalidDocument("invalid compact review marker blocks")
	}
	shell, err := v2ShellSource(source, spans)
	if err != nil {
		return nil, err
	}
	shellDocument, err := Parse(d.relativePath, shell)
	if err != nil {
		return nil, invalidDocument("compact review shell cannot be parsed")
	}
	units := shellDocument.genericSemanticUnits()
	for _, span := range spans {
		units[v2UnitKey(span.Kind, span.ID)] = Unit{Present: true, Value: bytes.Clone(source[span.Start:span.End])}
	}
	return units, nil
}

func (d Document) withV2SemanticUnits(units UnitSet) (Document, error) {
	entityType, ok := d.v2EntityType()
	if !ok {
		return Document{}, invalidDocument("not a compact review document")
	}
	source, err := d.Render()
	if err != nil {
		return Document{}, err
	}
	spans, err := reviewv2.ValidatedMarkerSpans(source, entityType)
	if err != nil {
		return Document{}, invalidDocument("invalid compact review marker blocks")
	}
	shellSource, err := v2ShellSource(source, spans)
	if err != nil {
		return Document{}, err
	}
	shellDocument, err := Parse(d.relativePath, shellSource)
	if err != nil {
		return Document{}, invalidDocument("compact review shell cannot be parsed")
	}
	generic := make(UnitSet, len(units))
	markers := make(map[UnitKey]Unit, len(spans))
	for key, unit := range units {
		if isV2MarkerUnitKey(key) {
			markers[key] = cloneUnit(unit)
			continue
		}
		generic[key] = cloneUnit(unit)
	}
	if len(markers) != len(spans) {
		return Document{}, invalidDocument("compact review marker units cannot be added or deleted")
	}
	for _, span := range spans {
		key := v2UnitKey(span.Kind, span.ID)
		unit, found := markers[key]
		if !found || !unit.Present || len(unit.KeyPresentation) != 0 || len(unit.HeadingPresentation) != 0 {
			return Document{}, invalidDocument("invalid compact review marker unit")
		}
	}
	rebuiltShell, err := shellDocument.WithUnits(generic)
	if err != nil {
		return Document{}, err
	}
	rebuilt, err := rebuiltShell.Render()
	if err != nil {
		return Document{}, err
	}
	for _, span := range spans {
		placeholder := v2Placeholder(span)
		if bytes.Count(rebuilt, placeholder) != 1 {
			return Document{}, invalidDocument("compact review marker position was changed")
		}
		rebuilt = bytes.Replace(rebuilt, placeholder, markers[v2UnitKey(span.Kind, span.ID)].Value, 1)
	}
	result, err := Parse(d.relativePath, rebuilt)
	if err != nil {
		return Document{}, invalidDocument("rebuilt compact review document cannot be reparsed")
	}
	if _, err := reviewv2.ValidatedMarkerSpans(rebuilt, entityType); err != nil {
		return Document{}, invalidDocument("rebuilt compact review markers are invalid")
	}
	return result, nil
}

func v2ShellSource(source []byte, spans []reviewv2.MarkerSpan) ([]byte, error) {
	var out bytes.Buffer
	previous := 0
	for _, span := range spans {
		if span.Start < previous || span.Start < 0 || span.End <= span.Start || span.End > len(source) {
			return nil, invalidDocument("invalid compact review marker span")
		}
		placeholder := v2Placeholder(span)
		if bytes.Contains(source, placeholder) {
			return nil, invalidDocument("compact review contains a reserved merge placeholder")
		}
		out.Write(source[previous:span.Start])
		out.Write(placeholder)
		previous = span.End
	}
	out.Write(source[previous:])
	return out.Bytes(), nil
}

func v2Placeholder(span reviewv2.MarkerSpan) []byte {
	return []byte(fmt.Sprintf("<!-- sr-v2-unit:%s:%s -->\n", span.Kind, span.ID))
}

func isV2MarkerUnitKey(key UnitKey) bool {
	if key.Kind != UnitSection || !strings.HasPrefix(key.Name, v2UnitPrefix) {
		return false
	}
	parts := strings.Split(key.Name, "/")
	return len(parts) == 3 && parts[0] == "session-reviewer" &&
		(parts[1] == "risk" || parts[1] == "decision" || parts[1] == "event") && parts[2] != ""
}
