package syncdoc

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentPreservesUnknownFrontmatterAndBodySections(t *testing.T) {
	input := []byte("---\nid: decision-1\nentity_type: decision\nproject_id: project-1\nrevision: 3\ntitle: 'Keep quotes'\nplugin_key:\n  nested: true\n---\n\nPreamble.\n\n## Context\nHuman edit.\n\n## Plugin Section\n```query\n# not a heading\n```\n")
	doc, err := Parse("decisions/decision-1.md", input)
	if err != nil {
		t.Fatal(err)
	}
	units := doc.Units()
	units[UnitKey{Kind: UnitFrontmatter, Name: "status"}] = Unit{Present: true, Value: []byte("accepted\n")}
	updated, err := doc.WithUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	out, err := updated.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"plugin_key:", "nested: true", "## Plugin Section", "# not a heading", "'Keep quotes'"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
}

func TestDocumentNoopRoundTripIsByteExactAndChangedRenderIsCanonical(t *testing.T) {
	input := []byte("---\r\nid: decision-1\r\nentity_type: decision\r\nproject_id: project-1\r\nrevision: 3\r\ntitle: 'Quoted' # keep\r\n---\r\n\r\n# 标题\r\n\r\nContext\r\n-------\r\nBody without final newline")
	doc := mustParsePath(t, "decisions/decision-1.md", input)
	out, err := doc.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, input) {
		t.Fatalf("no-op changed bytes:\n%q\n%q", input, out)
	}
	units := doc.Units()
	key := requireUnit(t, units, UnitSection, "标题 / Context#1")
	units[key] = Unit{Present: true, Value: []byte("Changed\r\n")}
	changed, err := doc.WithUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	out, err = changed.Render()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("\r")) || !bytes.HasSuffix(out, []byte("\n")) || bytes.HasSuffix(out, []byte("\n\n")) {
		t.Fatalf("changed output is not canonical: %q", out)
	}
	for _, want := range []string{"title: 'Quoted' # keep", "Context\n-------\nChanged\n"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}

func TestBodyTracksDuplicateHeadingOccurrencesAndFences(t *testing.T) {
	doc := mustParse(t, []byte("---\nid: decision-1\nentity_type: decision\nproject_id: project-1\nrevision: 3\n---\n\n## Context\none\n\n```md\n## Context\n```\n\n### Child\nchild\n\n## Context\ntwo\n"))
	units := doc.Units()
	for _, name := range []string{"Context#1", "Context / Child#1", "Context#2"} {
		requireUnit(t, units, UnitSection, name)
	}
	if len(sectionKeys(units)) != 3 {
		t.Fatalf("section keys=%v", sectionKeys(units))
	}
}

func TestBodyUsesCommonMarkHeadingBoundariesAndSetextAncestry(t *testing.T) {
	doc := mustParse(t, []byte("---\nid: decision-1\nentity_type: decision\nproject_id: project-1\nrevision: 3\n---\n\nFirst *line*\n------------\nbody\n\n<div>\n## HTML pseudoheading\n</div>\n\n- item\n  ## Nested list heading\n\n### Child\nchild\n"))
	units := doc.Units()
	requireUnit(t, units, UnitSection, "First line#1")
	requireUnit(t, units, UnitSection, "First line / Child#1")
	if len(sectionKeys(units)) != 2 {
		t.Fatalf("section keys=%v", sectionKeys(units))
	}
}

func TestUnitsAreDefensiveAndMutationsAreDeterministic(t *testing.T) {
	doc := mustParse(t, entity("decision-1", "project-1", "Base"))
	first := doc.Units()
	title := UnitKey{Kind: UnitFrontmatter, Name: "title"}
	u := first[title]
	u.Value[0] = 'X'
	first[title] = u
	delete(first, title)
	if !bytes.Equal(doc.Units()[title].Value, []byte("Base\n")) {
		t.Fatal("Units aliases document storage")
	}

	baseUnits := doc.Units()
	baseUnits[UnitKey{Kind: UnitFrontmatter, Name: "zeta"}] = Unit{Present: true, Value: []byte("z\n")}
	baseUnits[UnitKey{Kind: UnitFrontmatter, Name: "alpha"}] = Unit{Present: true, Value: []byte("a\n")}
	baseUnits[UnitKey{Kind: UnitSection, Name: "Zulu#1"}] = Unit{Present: true, Value: []byte("z\n")}
	baseUnits[UnitKey{Kind: UnitSection, Name: "Alpha#1"}] = Unit{Present: true, Value: []byte("a\n")}
	updated, err := doc.WithUnits(baseUnits)
	if err != nil {
		t.Fatal(err)
	}
	baseUnits[UnitKey{Kind: UnitFrontmatter, Name: "alpha"}] = Unit{Present: true, Value: []byte("mutated\n")}
	out, err := updated.Render()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Index(out, []byte("alpha:")) > bytes.Index(out, []byte("zeta:")) || bytes.Index(out, []byte("## Alpha")) > bytes.Index(out, []byte("## Zulu")) || bytes.Contains(out, []byte("mutated")) {
		t.Fatalf("non-deterministic or aliased output:\n%s", out)
	}
}

func TestBodyDeletionReadditionAndDuplicateOccurrencesAreIndependent(t *testing.T) {
	doc := mustParse(t, []byte("---\nid: decision-1\nentity_type: decision\nproject_id: project-1\nrevision: 3\n---\n\n## Context\none\n\n## Context\ntwo\n"))
	units := doc.Units()
	first := requireUnit(t, units, UnitSection, "Context#1")
	second := requireUnit(t, units, UnitSection, "Context#2")
	units[first] = Unit{Present: false}
	units[second] = Unit{Present: true, Value: []byte("second changed\n")}
	deleted, err := doc.WithUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	out, err := deleted.Render()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("\none\n")) || !bytes.Contains(out, []byte("second changed")) || bytes.Count(out, []byte("## Context")) != 1 {
		t.Fatalf("duplicate deletion was not independent:\n%s", out)
	}

	readdedUnits := deleted.Units()
	readdedUnits[first] = Unit{Present: true, Value: []byte("one\n")}
	readded, err := deleted.WithUnits(readdedUnits)
	if err != nil {
		t.Fatal(err)
	}
	readdedOut, err := readded.Render()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(readdedOut, []byte("## Context")) != 2 || !bytes.Contains(readdedOut, []byte("second changed")) || !bytes.Contains(readdedOut, []byte("one\n")) {
		t.Fatalf("re-added duplicate was not deterministic:\n%s", readdedOut)
	}
}

func TestChangedRenderSeparatesEOFHeadingFromNewBody(t *testing.T) {
	doc := mustParse(t, []byte("---\nid: decision-1\nentity_type: decision\nproject_id: project-1\nrevision: 3\n---\n## Context"))
	units := doc.Units()
	key := requireUnit(t, units, UnitSection, "Context#1")
	units[key] = Unit{Present: true, Value: []byte("body\n")}
	updated, err := doc.WithUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	out, err := updated.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("## Context\nbody\n")) {
		t.Fatalf("heading and body were joined: %q", out)
	}
}

func TestReservedFieldEditIsReportedWithoutEchoingValue(t *testing.T) {
	base := mustParse(t, entity("decision-1", "project-1", "Base"))
	edited := mustParse(t, entity("decision-evil-secret-value", "project-1", "Edit"))
	err := edited.ValidateHumanChanges(base)
	if !errors.Is(err, ErrReservedField) || strings.Contains(err.Error(), "evil") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestProtectedProvenanceCannotBeHumanEdited(t *testing.T) {
	base := mustParse(t, entity("decision-1", "project-1", "Base"))
	for _, tc := range []struct {
		field string
		value string
	}{
		{"revision", "4\n"},
		{"source_sessions", "[session-2]\n"},
		{"evidence", "[]\n"},
		{"supersedes", "[decision-0]\n"},
	} {
		edited := replaceFrontmatterUnit(t, base, tc.field, []byte(tc.value))
		if err := edited.ValidateHumanChanges(base); !errors.Is(err, ErrProtectedProvenance) {
			t.Fatalf("field=%s err=%v", tc.field, err)
		}
	}
	editedHash := replaceNestedEvidenceHash(t, base, strings.Repeat("b", 64))
	if err := editedHash.ValidateHumanChanges(base); !errors.Is(err, ErrProtectedProvenance) {
		t.Fatalf("hash err=%v", err)
	}
}

func TestRevisionIncrementsOnceForMergedHumanChangeAndNotForNoop(t *testing.T) {
	base := mustParse(t, entity("decision-1", "project-1", "Base"))
	edited := replaceSection(t, base, "Context#1", []byte("Human edit.\n"))
	if err := edited.ValidateHumanChanges(base); err != nil {
		t.Fatal(err)
	}
	merged, err := edited.FinalizeHumanMerge(base, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := frontmatterInt(t, merged, "revision"); got != 4 {
		t.Fatalf("revision=%d", got)
	}
	mergedAgain, err := merged.FinalizeHumanMerge(merged, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := frontmatterInt(t, mergedAgain, "revision"); got != 4 {
		t.Fatalf("unchanged merge incremented revision=%d", got)
	}
	noop, err := base.FinalizeHumanMerge(base, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := frontmatterInt(t, noop, "revision"); got != 3 {
		t.Fatalf("noop revision=%d", got)
	}
	got, _ := noop.Render()
	want, _ := base.Render()
	if !bytes.Equal(got, want) {
		t.Fatal("no-op merge changed bytes")
	}
}

func TestProjectOverviewFinalizesLocally(t *testing.T) {
	base := mustParsePath(t, "docs/session-review/project-overview.md", []byte("---\nid: project-overview\nentity_type: project_overview\nproject_id: project-1\nrevision: 1\nsync_status: synced\nplugin: yes\n---\n\n# Project\n"))
	units := base.Units()
	units[UnitKey{Kind: UnitFrontmatter, Name: "plugin"}] = Unit{Present: true, Value: []byte("no\n")}
	edited, err := base.WithUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := edited.FinalizeHumanMerge(base, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := frontmatterInt(t, merged, "revision"); got != 2 {
		t.Fatalf("revision=%d", got)
	}
}

func TestParseRejectsMalformedAndHostileDocuments(t *testing.T) {
	valid := entity("decision-1", "project-1", "Base")
	tests := map[string][]byte{
		"oversize":      bytes.Repeat([]byte("x"), MaxDocumentBytes+1),
		"invalid utf8":  append(append([]byte(nil), valid...), 0xff),
		"nul":           append(append([]byte(nil), valid...), 0),
		"bare cr":       bytes.Replace(valid, []byte("title: Base\n"), []byte("title: Base\r"), 1),
		"duplicate key": bytes.Replace(valid, []byte("entity_type:"), []byte("id: other\nentity_type:"), 1),
		"alias":         bytes.Replace(valid, []byte("title: Base"), []byte("anchor: &x Base\ntitle: *x"), 1),
		"merge":         bytes.Replace(valid, []byte("title: Base"), []byte("defaults: &x {x: y}\n<<: *x\ntitle: Base"), 1),
		"custom tag":    bytes.Replace(valid, []byte("title: Base"), []byte("title: !run Base"), 1),
		"multi doc":     bytes.Replace(valid, []byte("title: Base"), []byte("title: Base\n...\nother: doc"), 1),
		"non mapping":   []byte("---\n- item\n---\n"),
	}
	deep := "---\nid: decision-1\nentity_type: decision\nproject_id: project-1\nrevision: 1\nx: " + strings.Repeat("[", 120) + "0" + strings.Repeat("]", 120) + "\n---\n"
	tests["depth"] = []byte(deep)
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse("decisions/decision-1.md", input); !errors.Is(err, ErrInvalidDocument) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestParseRejectsUnsafeRelativePaths(t *testing.T) {
	for _, name := range []string{"", ".", "../x.md", "/absolute.md", "a\\b.md", "a/../b.md", "a//b.md"} {
		if _, err := Parse(name, entity("decision-1", "project-1", "Base")); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("path=%q err=%v", name, err)
		}
	}
	if _, err := Parse(filepath.ToSlash("decisions/decision-1.md"), entity("decision-1", "project-1", "Base")); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityAndHash(t *testing.T) {
	doc := mustParse(t, entity("decision-1", "project-1", "Base"))
	identity, err := doc.Identity()
	if err != nil || identity != (Identity{ID: "decision-1", EntityType: "decision", ProjectID: "project-1"}) {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
	out, _ := doc.Render()
	if got := ContentHash(out); len(got) != 64 || got != strings.ToLower(got) {
		t.Fatalf("hash=%q", got)
	}
}

func mustParse(t *testing.T, content []byte) Document {
	t.Helper()
	return mustParsePath(t, "decisions/entity.md", content)
}

func mustParsePath(t *testing.T, relativePath string, content []byte) Document {
	t.Helper()
	doc, err := Parse(relativePath, content)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func entity(id, projectID, title string) []byte {
	return []byte(fmt.Sprintf("---\nid: %s\nentity_type: decision\nproject_id: %s\nrevision: 3\ntitle: %s\nstatus: accepted\nsource_sessions: [session-1]\nevidence:\n  - evidence_id: ev-1\n    session_id: session-1\n    jsonl_line: 7\n    source_hash: %s\n    summary: safe\nsupersedes: []\nsync_status: synced\n---\n\n## Context\n%s\n", id, projectID, title, strings.Repeat("a", 64), title))
}

func replaceFrontmatterUnit(t *testing.T, doc Document, name string, value []byte) Document {
	t.Helper()
	units := doc.Units()
	units[UnitKey{Kind: UnitFrontmatter, Name: name}] = Unit{Present: true, Value: value}
	updated, err := doc.WithUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func replaceNestedEvidenceHash(t *testing.T, doc Document, hash string) Document {
	t.Helper()
	units := doc.Units()
	key := UnitKey{Kind: UnitFrontmatter, Name: "evidence"}
	value := string(units[key].Value)
	value = strings.Replace(value, strings.Repeat("a", 64), hash, 1)
	units[key] = Unit{Present: true, Value: []byte(value)}
	updated, err := doc.WithUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func replaceSection(t *testing.T, doc Document, name string, value []byte) Document {
	t.Helper()
	units := doc.Units()
	key := requireUnit(t, units, UnitSection, name)
	units[key] = Unit{Present: true, Value: value}
	updated, err := doc.WithUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func requireUnit(t *testing.T, units UnitSet, kind UnitKind, name string) UnitKey {
	t.Helper()
	key := UnitKey{Kind: kind, Name: name}
	if _, ok := units[key]; !ok {
		t.Fatalf("missing unit %+v; have=%v", key, units)
	}
	return key
}

func sectionKeys(units UnitSet) []UnitKey {
	var keys []UnitKey
	for key := range units {
		if key.Kind == UnitSection {
			keys = append(keys, key)
		}
	}
	return keys
}

func frontmatterInt(t *testing.T, doc Document, name string) int {
	t.Helper()
	unit, ok := doc.Units()[UnitKey{Kind: UnitFrontmatter, Name: name}]
	if !ok {
		t.Fatalf("missing %s", name)
	}
	var got int
	if _, err := fmt.Sscan(string(unit.Value), &got); err != nil {
		t.Fatalf("parse %s=%q: %v", name, unit.Value, err)
	}
	return got
}
