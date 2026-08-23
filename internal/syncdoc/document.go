package syncdoc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/ledger"
	"gopkg.in/yaml.v3"
)

const (
	MaxDocumentBytes    = 4 << 20
	maxFrontmatterBytes = 1 << 20
	maxYAMLNodes        = 10_000
	maxYAMLDepth        = 100
)

var (
	ErrInvalidDocument     = errors.New("invalid sync document")
	ErrInvalidPath         = errors.New("invalid repository-relative path")
	ErrReservedField       = errors.New("machine-reserved field changed")
	ErrProtectedProvenance = errors.New("proposal provenance changed")
	ErrUnauthorizedUnit    = errors.New("document unit is not human-editable")
)

// MachineReservedFields returns a caller-owned snapshot of the reserved field
// catalog. Authorization uses immutable package-local predicates, so callers
// cannot weaken validation by mutating the returned map.
func MachineReservedFields() map[string]struct{} {
	return map[string]struct{}{"id": {}, "entity_type": {}, "project_id": {}, "sync_status": {}, "sync_hash": {}, "base_hash": {}, "project_hash": {}, "vault_hash": {}}
}

// ProposalOwnedFields returns a caller-owned snapshot of the provenance field
// catalog. Mutating it never changes merge validation.
func ProposalOwnedFields() map[string]struct{} {
	return map[string]struct{}{"revision": {}, "source_sessions": {}, "evidence": {}, "supersedes": {}}
}

func isMachineReservedField(name string) bool {
	switch name {
	case "id", "entity_type", "project_id", "sync_status", "sync_hash", "base_hash", "project_hash", "vault_hash":
		return true
	default:
		return false
	}
}

func isProposalOwnedField(name string) bool {
	switch name {
	case "revision", "source_sessions", "evidence", "supersedes":
		return true
	default:
		return false
	}
}

type Identity struct {
	ID, EntityType, ProjectID string
}

type UnitKind string

const (
	UnitFrontmatter UnitKind = "frontmatter"
	UnitPreamble    UnitKind = "preamble"
	UnitSection     UnitKind = "section"
)

type UnitKey struct {
	Kind UnitKind
	Name string
}

type Unit struct {
	Present         bool
	Value           []byte
	KeyPresentation []byte
}

type UnitSet map[UnitKey]Unit

type Document struct {
	relativePath string
	raw          []byte
	frontmatter  *yaml.Node
	body         Body
	dirty        bool
}

func Parse(relativePath string, content []byte) (Document, error) {
	if err := validateRelativePath(relativePath); err != nil {
		return Document{}, err
	}
	if len(content) > MaxDocumentBytes {
		return Document{}, invalidDocument("document exceeds size limit")
	}
	if !utf8.Valid(content) {
		return Document{}, invalidDocument("document is not valid UTF-8")
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return Document{}, invalidDocument("document contains NUL")
	}
	if hasBareCR(content) {
		return Document{}, invalidDocument("document contains a bare carriage return")
	}
	frontmatterSource, bodySource, err := splitFrontmatter(content)
	if err != nil {
		return Document{}, err
	}
	if len(frontmatterSource) > maxFrontmatterBytes {
		return Document{}, invalidDocument("frontmatter exceeds size limit")
	}
	mapping, err := decodeFrontmatter(frontmatterSource)
	if err != nil {
		return Document{}, err
	}
	legacyOverview := isLegacyOverviewPath(relativePath)
	if err := validateFrontmatter(mapping, legacyOverview); err != nil {
		return Document{}, err
	}
	body, err := parseBody(bodySource)
	if err != nil {
		return Document{}, err
	}
	return Document{relativePath: relativePath, raw: bytes.Clone(content), frontmatter: mapping, body: body}, nil
}

func (d Document) Render() ([]byte, error) {
	if d.frontmatter == nil {
		return nil, invalidDocument("nil document")
	}
	if !d.dirty {
		return bytes.Clone(d.raw), nil
	}
	if err := validateFrontmatter(d.frontmatter, isLegacyOverviewPath(d.relativePath)); err != nil {
		return nil, err
	}
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err := encoder.Encode(d.frontmatter); err != nil {
		return nil, invalidDocument("cannot encode frontmatter")
	}
	if err := encoder.Close(); err != nil {
		return nil, invalidDocument("cannot finish frontmatter")
	}
	frontmatter := bytes.TrimPrefix(encoded.Bytes(), []byte("---\n"))
	frontmatter = bytes.TrimSuffix(frontmatter, []byte("...\n"))
	var out bytes.Buffer
	out.WriteString("---\n")
	out.Write(normalizeLF(frontmatter))
	if out.Len() == 0 || out.Bytes()[out.Len()-1] != '\n' {
		out.WriteByte('\n')
	}
	out.WriteString("---\n")
	out.Write(d.body.render())
	result := bytes.TrimRight(out.Bytes(), "\r\n")
	result = append(bytes.Clone(result), '\n')
	if len(result) > MaxDocumentBytes {
		return nil, invalidDocument("rendered document exceeds size limit")
	}
	if _, err := Parse(d.relativePath, result); err != nil {
		return nil, invalidDocument("rendered document cannot be reparsed")
	}
	return result, nil
}

func (d Document) Identity() (Identity, error) {
	if d.frontmatter == nil {
		return Identity{}, invalidDocument("nil document")
	}
	id, err := requiredString(d.frontmatter, "id")
	if err != nil {
		return Identity{}, err
	}
	entityType, err := requiredString(d.frontmatter, "entity_type")
	if err != nil {
		return Identity{}, err
	}
	projectID, err := requiredString(d.frontmatter, "project_id")
	if err != nil {
		return Identity{}, err
	}
	return Identity{ID: id, EntityType: entityType, ProjectID: projectID}, nil
}

func (d Document) Units() UnitSet {
	units := d.body.units()
	if d.frontmatter == nil {
		return units
	}
	for i := 0; i+1 < len(d.frontmatter.Content); i += 2 {
		key := d.frontmatter.Content[i].Value
		units[UnitKey{Kind: UnitFrontmatter, Name: key}] = Unit{
			Present:         true,
			Value:           encodeNode(d.frontmatter.Content[i+1]),
			KeyPresentation: encodeNode(d.frontmatter.Content[i]),
		}
	}
	return cloneUnitSet(units)
}

func (d Document) WithUnits(units UnitSet) (Document, error) {
	if d.frontmatter == nil {
		return Document{}, invalidDocument("nil document")
	}
	input := cloneUnitSet(units)
	for key := range input {
		switch key.Kind {
		case UnitFrontmatter:
			if key.Name == "" {
				return Document{}, invalidDocument("empty frontmatter unit name")
			}
		case UnitPreamble:
			if key.Name != "" {
				return Document{}, invalidDocument("invalid preamble unit name")
			}
		case UnitSection:
			if key.Name == "" {
				return Document{}, invalidDocument("empty section unit name")
			}
		default:
			return Document{}, invalidDocument("unknown unit kind")
		}
	}
	result := Document{relativePath: d.relativePath, raw: bytes.Clone(d.raw), frontmatter: cloneNode(d.frontmatter), dirty: d.dirty}
	changed := false
	existing := make(map[string]struct{}, len(result.frontmatter.Content)/2)
	content := make([]*yaml.Node, 0, len(result.frontmatter.Content))
	for i := 0; i+1 < len(result.frontmatter.Content); i += 2 {
		keyNode, valueNode := result.frontmatter.Content[i], result.frontmatter.Content[i+1]
		key := UnitKey{Kind: UnitFrontmatter, Name: keyNode.Value}
		existing[key.Name] = struct{}{}
		unit, ok := input[key]
		if !ok || !unit.Present {
			changed = true
			continue
		}
		nextKey := cloneNode(keyNode)
		var err error
		if len(unit.KeyPresentation) != 0 {
			nextKey, err = decodeUnitKey(unit.KeyPresentation, key.Name)
			if err != nil {
				return Document{}, err
			}
			if !bytes.Equal(encodeNode(keyNode), encodeNode(nextKey)) {
				changed = true
			}
		}
		next, err := decodeUnitValue(unit.Value)
		if err != nil {
			return Document{}, fmt.Errorf("%w: invalid frontmatter unit", ErrInvalidDocument)
		}
		if !bytes.Equal(encodeNode(valueNode), encodeNode(next)) {
			preservePresentation(valueNode, next)
			changed = true
		}
		content = append(content, nextKey, next)
	}
	var additions []string
	for key, unit := range input {
		if key.Kind != UnitFrontmatter || !unit.Present {
			continue
		}
		if _, ok := existing[key.Name]; !ok {
			additions = append(additions, key.Name)
		}
	}
	sort.Strings(additions)
	for _, name := range additions {
		unit := input[UnitKey{Kind: UnitFrontmatter, Name: name}]
		node, err := decodeUnitValue(unit.Value)
		if err != nil {
			return Document{}, fmt.Errorf("%w: invalid frontmatter unit", ErrInvalidDocument)
		}
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
		if len(unit.KeyPresentation) != 0 {
			keyNode, err = decodeUnitKey(unit.KeyPresentation, name)
			if err != nil {
				return Document{}, err
			}
		}
		content = append(content, keyNode, node)
		changed = true
	}
	result.frontmatter.Content = content
	body, bodyChanged, err := d.body.withUnits(input)
	if err != nil {
		return Document{}, err
	}
	result.body = body
	result.dirty = result.dirty || changed || bodyChanged
	if err := validateFrontmatter(result.frontmatter, isLegacyOverviewPath(result.relativePath)); err != nil {
		return Document{}, err
	}
	return result, nil
}

func (d Document) ValidateHumanChanges(base Document) error {
	if err := validateComparableDocuments(d, base); err != nil {
		return err
	}
	editedUnits, baseUnits := d.Units(), base.Units()
	keys := unionUnitKeys(editedUnits, baseUnits)
	for _, key := range keys {
		if unitsEqual(editedUnits[key], baseUnits[key]) {
			continue
		}
		if key.Kind == UnitFrontmatter {
			if isMachineReservedField(key.Name) {
				return fmt.Errorf("%w: %s", ErrReservedField, key.Name)
			}
			if isProposalOwnedField(key.Name) {
				return fmt.Errorf("%w: %s", ErrProtectedProvenance, key.Name)
			}
			continue
		}
		if key.Kind == UnitSection {
			continue
		}
		return fmt.Errorf("%w: %s", ErrUnauthorizedUnit, key.Kind)
	}
	return nil
}

func (d Document) FinalizeHumanMerge(base Document, changed bool) (Document, error) {
	if err := d.ValidateHumanChanges(base); err != nil {
		return Document{}, err
	}
	if !changed || documentsLogicallyEqual(d, base) {
		return cloneDocument(base), nil
	}
	baseUnits := base.Units()
	units := d.Units()
	for _, field := range [...]string{"revision", "source_sessions", "evidence", "supersedes"} {
		key := UnitKey{Kind: UnitFrontmatter, Name: field}
		if value, ok := baseUnits[key]; ok {
			units[key] = value
		} else {
			delete(units, key)
		}
	}
	baseRevision, err := revisionOf(base)
	if err != nil || baseRevision == int(^uint(0)>>1) {
		return Document{}, invalidDocument("invalid base revision")
	}
	preIncrement, err := d.WithUnits(units)
	if err != nil {
		return Document{}, err
	}
	identity, err := preIncrement.Identity()
	if err != nil {
		return Document{}, err
	}
	if identity.EntityType == "project_overview" {
		if identity.ID != "project-overview" || baseRevision < 1 {
			return Document{}, invalidDocument("invalid project overview")
		}
	} else {
		rendered, err := preIncrement.Render()
		if err != nil {
			return Document{}, err
		}
		ledgerDocument, err := ledger.ParseDocument(rendered)
		if err != nil {
			return Document{}, fmt.Errorf("%w: ledger validation failed", ErrInvalidDocument)
		}
		fields := map[string]any{"id": identity.ID, "entity_type": identity.EntityType, "project_id": identity.ProjectID, "revision": baseRevision + 1}
		if sessions, ok, err := stringSequenceUnit(baseUnits, "source_sessions"); err != nil {
			return Document{}, err
		} else if ok {
			fields["source_sessions"] = sessions
		}
		if err := ledgerDocument.SetReserved(fields); err != nil {
			return Document{}, fmt.Errorf("%w: ledger mutation validation failed", ErrInvalidDocument)
		}
	}
	units[UnitKey{Kind: UnitFrontmatter, Name: "revision"}] = Unit{Present: true, Value: []byte(strconv.Itoa(baseRevision+1) + "\n")}
	candidate, err := d.WithUnits(units)
	if err != nil {
		return Document{}, err
	}
	rendered, err := candidate.Render()
	if err != nil {
		return Document{}, err
	}
	return Parse(candidate.relativePath, rendered)
}

func (d Document) WithSyncStatus(status string) (Document, error) {
	if status != "synced" && status != "conflicted" {
		return Document{}, invalidDocument("invalid sync status")
	}
	units := d.Units()
	units[UnitKey{Kind: UnitFrontmatter, Name: "sync_status"}] = Unit{Present: true, Value: []byte(status + "\n")}
	return d.WithUnits(units)
}

func ContentHash(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func validateRelativePath(relativePath string) error {
	if relativePath == "" || relativePath == "." || strings.Contains(relativePath, "\\") || strings.ContainsRune(relativePath, 0) || strings.HasPrefix(relativePath, "/") || path.Clean(relativePath) != relativePath {
		return ErrInvalidPath
	}
	for _, part := range strings.Split(relativePath, "/") {
		if part == "" || part == "." || part == ".." {
			return ErrInvalidPath
		}
	}
	return nil
}

func splitFrontmatter(content []byte) ([]byte, []byte, error) {
	lineEnd := bytes.IndexByte(content, '\n')
	if lineEnd < 0 || !bytes.Equal(bytes.TrimSuffix(content[:lineEnd], []byte("\r")), []byte("---")) {
		return nil, nil, invalidDocument("missing opening frontmatter fence")
	}
	frontmatterStart := lineEnd + 1
	for start := frontmatterStart; start <= len(content); {
		endIndex := bytes.IndexByte(content[start:], '\n')
		end := len(content)
		next := len(content)
		if endIndex >= 0 {
			end = start + endIndex
			next = end + 1
		}
		line := bytes.TrimSuffix(content[start:end], []byte("\r"))
		if bytes.Equal(line, []byte("---")) {
			return bytes.Clone(content[frontmatterStart:start]), bytes.Clone(content[next:]), nil
		}
		if endIndex < 0 {
			break
		}
		start = next
	}
	return nil, nil, invalidDocument("missing closing frontmatter fence")
}

func decodeFrontmatter(source []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(normalizeLF(source)))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, invalidDocument("malformed YAML frontmatter")
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, invalidDocument("multiple YAML documents are not allowed")
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, invalidDocument("frontmatter must be one mapping")
	}
	return document.Content[0], nil
}

func validateFrontmatter(mapping *yaml.Node, legacyOverview bool) error {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return invalidDocument("frontmatter must be a mapping")
	}
	stats := yamlStats{}
	if err := validateYAMLNode(mapping, 1, &stats); err != nil {
		return err
	}
	if legacyOverview {
		if _, err := requiredString(mapping, "project_id"); err != nil {
			return err
		}
		for _, key := range []string{"id", "entity_type"} {
			if node, ok := mappingValue(mapping, key); ok && (node.Kind != yaml.ScalarNode || node.Tag != "!!str" || node.Value == "") {
				return invalidDocument("invalid overview identity")
			}
		}
		if node, ok := mappingValue(mapping, "revision"); ok {
			if _, err := positiveInt(node); err != nil {
				return err
			}
		}
		return nil
	}
	for _, key := range []string{"id", "entity_type", "project_id"} {
		if _, err := requiredString(mapping, key); err != nil {
			return err
		}
	}
	return nil
}

type yamlStats struct{ nodes int }

func validateYAMLNode(node *yaml.Node, depth int, stats *yamlStats) error {
	if node == nil {
		return invalidDocument("nil YAML node")
	}
	stats.nodes++
	if stats.nodes > maxYAMLNodes || depth > maxYAMLDepth {
		return invalidDocument("YAML structure exceeds safety limits")
	}
	if node.Kind == yaml.AliasNode || node.Alias != nil {
		return invalidDocument("YAML aliases are not allowed")
	}
	if !coreYAMLTag(node.Tag) {
		return invalidDocument("non-core YAML tag is not allowed")
	}
	if node.Kind == yaml.MappingNode {
		if len(node.Content)%2 != 0 {
			return invalidDocument("malformed YAML mapping")
		}
		seen := make(map[string]struct{}, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "" || key.Value == "<<" {
				return invalidDocument("YAML mapping keys must be unique scalar strings")
			}
			if _, ok := seen[key.Value]; ok {
				return invalidDocument("duplicate YAML mapping key")
			}
			seen[key.Value] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLNode(child, depth+1, stats); err != nil {
			return err
		}
	}
	return nil
}

func coreYAMLTag(tag string) bool {
	switch tag {
	case "", "!!map", "!!seq", "!!str", "!!int", "!!float", "!!bool", "!!null", "!!timestamp", "tag:yaml.org,2002:map", "tag:yaml.org,2002:seq", "tag:yaml.org,2002:str", "tag:yaml.org,2002:int", "tag:yaml.org,2002:float", "tag:yaml.org,2002:bool", "tag:yaml.org,2002:null", "tag:yaml.org,2002:timestamp":
		return true
	default:
		return false
	}
}

func decodeUnitValue(source []byte) (*yaml.Node, error) {
	if len(source) == 0 || len(source) > maxFrontmatterBytes || !utf8.Valid(source) || bytes.IndexByte(source, 0) >= 0 {
		return nil, invalidDocument("invalid frontmatter unit")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(normalizeLF(source)))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil || document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, invalidDocument("invalid frontmatter unit")
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, invalidDocument("frontmatter unit contains multiple documents")
	}
	node := document.Content[0]
	stats := yamlStats{}
	if err := validateYAMLNode(node, 1, &stats); err != nil {
		return nil, err
	}
	return node, nil
}

func decodeUnitKey(source []byte, name string) (*yaml.Node, error) {
	node, err := decodeUnitValue(source)
	if err != nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" || node.Value != name {
		return nil, invalidDocument("frontmatter key presentation does not match unit name")
	}
	return node, nil
}

func requiredString(mapping *yaml.Node, key string) (string, error) {
	node, ok := mappingValue(mapping, key)
	if !ok || node.Kind != yaml.ScalarNode || node.Tag != "!!str" || node.Value == "" {
		return "", invalidDocument("missing or invalid identity field")
	}
	return node.Value, nil
}

func revisionOf(document Document) (int, error) {
	node, ok := mappingValue(document.frontmatter, "revision")
	if !ok {
		return 0, invalidDocument("missing revision")
	}
	return positiveInt(node)
}

func positiveInt(node *yaml.Node) (int, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return 0, invalidDocument("invalid revision")
	}
	value, err := strconv.Atoi(node.Value)
	if err != nil || value < 1 {
		return 0, invalidDocument("invalid revision")
	}
	return value, nil
}

func mappingValue(mapping *yaml.Node, key string) (*yaml.Node, bool) {
	if mapping == nil {
		return nil, false
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1], true
		}
	}
	return nil, false
}

func encodeNode(node *yaml.Node) []byte {
	if node == nil {
		return nil
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(node); err != nil {
		return nil
	}
	_ = encoder.Close()
	return bytes.Clone(out.Bytes())
}

func preservePresentation(old, next *yaml.Node) {
	next.HeadComment = old.HeadComment
	next.LineComment = old.LineComment
	next.FootComment = old.FootComment
	if old.Kind == next.Kind {
		switch old.Kind {
		case yaml.ScalarNode:
			next.Style = old.Style
		case yaml.SequenceNode, yaml.MappingNode:
			next.Style = old.Style & yaml.FlowStyle
		}
	}
}

func cloneNode(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	copy := *node
	copy.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		copy.Content[i] = cloneNode(child)
	}
	copy.Alias = nil
	return &copy
}

func cloneUnitSet(units UnitSet) UnitSet {
	copy := make(UnitSet, len(units))
	for key, unit := range units {
		copy[key] = Unit{Present: unit.Present, Value: bytes.Clone(unit.Value), KeyPresentation: bytes.Clone(unit.KeyPresentation)}
	}
	return copy
}

func unitsEqual(first, second Unit) bool {
	return first.Present == second.Present && bytes.Equal(first.Value, second.Value) && bytes.Equal(first.KeyPresentation, second.KeyPresentation)
}

func unionUnitKeys(first, second UnitSet) []UnitKey {
	seen := make(map[UnitKey]struct{}, len(first)+len(second))
	for key := range first {
		seen[key] = struct{}{}
	}
	for key := range second {
		seen[key] = struct{}{}
	}
	keys := make([]UnitKey, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Kind == keys[j].Kind {
			return keys[i].Name < keys[j].Name
		}
		return keys[i].Kind < keys[j].Kind
	})
	return keys
}

func validateComparableDocuments(edited, base Document) error {
	if edited.frontmatter == nil || base.frontmatter == nil {
		return invalidDocument("nil comparison document")
	}
	if err := validateRelativePath(edited.relativePath); err != nil {
		return err
	}
	if err := validateRelativePath(base.relativePath); err != nil {
		return err
	}
	return nil
}

func documentsLogicallyEqual(first, second Document) bool {
	if first.relativePath != second.relativePath {
		return false
	}
	firstUnits, secondUnits := first.Units(), second.Units()
	keys := unionUnitKeys(firstUnits, secondUnits)
	for _, key := range keys {
		if !unitsEqual(firstUnits[key], secondUnits[key]) {
			return false
		}
	}
	return true
}

func cloneDocument(document Document) Document {
	return Document{relativePath: document.relativePath, raw: bytes.Clone(document.raw), frontmatter: cloneNode(document.frontmatter), body: cloneBody(document.body), dirty: document.dirty}
}

func cloneBody(body Body) Body {
	result := Body{preamble: bytes.Clone(body.preamble), sections: make([]bodySection, len(body.sections))}
	for i, section := range body.sections {
		result.sections[i] = bodySection{key: section.key, heading: bytes.Clone(section.heading), value: bytes.Clone(section.value), level: section.level}
	}
	return result
}

func stringSequenceUnit(units UnitSet, name string) ([]string, bool, error) {
	unit, ok := units[UnitKey{Kind: UnitFrontmatter, Name: name}]
	if !ok || !unit.Present {
		return nil, false, nil
	}
	node, err := decodeUnitValue(unit.Value)
	if err != nil || node.Kind != yaml.SequenceNode {
		return nil, false, invalidDocument("invalid string sequence")
	}
	result := make([]string, len(node.Content))
	for i, item := range node.Content {
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" || item.Value == "" {
			return nil, false, invalidDocument("invalid string sequence")
		}
		result[i] = item.Value
	}
	return result, true, nil
}

func isLegacyOverviewPath(relativePath string) bool {
	return relativePath == "docs/session-review/project-overview.md" || path.Base(relativePath) == "project-overview.md"
}

func hasBareCR(content []byte) bool {
	for i, value := range content {
		if value == '\r' && (i+1 == len(content) || content[i+1] != '\n') {
			return true
		}
	}
	return false
}

func invalidDocument(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidDocument, reason)
}
