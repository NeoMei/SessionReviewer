package syncdoc

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func TestV4UnitsExposeOnlyHumanFieldsAndShell(t *testing.T) {
	document, raw, _ := v4FixtureDocument(t, "项目回顾.md", "review.md")
	units := document.SemanticUnits()
	goal := UnitKey{Kind: UnitSection, Name: "session-reviewer/v4/project-overview/goal"}
	if got := string(units[goal].Value); got != "项目目标夹具" {
		t.Fatalf("goal=%q", got)
	}
	if _, found := units[UnitKey{Kind: UnitFrontmatter, Name: "custom_owner"}]; !found {
		t.Fatal("custom frontmatter was not exposed as a shell unit")
	}
	for _, name := range []string{"id", "entity_type", "project_id", "schema_version", "document_format", "revision", "generation_id", "minimum_reader_version", "minimum_writer_version"} {
		if units[UnitKey{Kind: UnitFrontmatter, Name: name}].Present {
			t.Fatalf("machine frontmatter %q became writable", name)
		}
	}
	rendered, err := document.Render()
	if err != nil || !bytes.Equal(rendered, raw) {
		t.Fatalf("unchanged v4 bytes changed: err=%v", err)
	}
}

func TestV4ShellReplacementChangesOnlySelectedField(t *testing.T) {
	_, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	crlf := bytes.ReplaceAll(raw, []byte("\n"), []byte("\r\n"))
	document, err := ParseV4("项目回顾.md", crlf, ledger)
	if err != nil {
		t.Fatal(err)
	}
	key := UnitKey{Kind: UnitSection, Name: "session-reviewer/v4/project-overview/goal"}
	units := document.SemanticUnits()
	unit := units[key]
	unit.Value = []byte("新的\r\n目标")
	units[key] = unit
	edited, err := document.WithSemanticUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := edited.Render()
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(crlf, []byte("项目目标夹具"), []byte("新的\r\n目标"), 1)
	if !bytes.Equal(rendered, want) {
		t.Fatal("field replacement rewrote the physical shell")
	}
	if _, err := ParseV4("项目回顾.md", rendered, ledger); err != nil {
		t.Fatalf("rendered field draft cannot be reparsed: %v", err)
	}
}

func TestV4ShellParagraphReorderingPreservesMixedPhysicalBytes(t *testing.T) {
	_, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	raw = bytes.Replace(raw, []byte("这段自定义文本和 [链接](https://example.test/custom) 必须原样保留。"), []byte("第一段。\n\n第二段。"), 1)
	mixed := bytes.Replace(raw, []byte("# 项目回顾\n\n这段自定义文本"), []byte("# 项目回顾\r\n\r\n这段自定义文本"), 1)
	document, err := ParseV4("项目回顾.md", mixed, ledger)
	if err != nil {
		t.Fatal(err)
	}
	units := document.SemanticUnits()
	found := false
	for key, unit := range units {
		if key.Kind != UnitSection || !bytes.Contains(unit.Value, []byte("第一段。")) {
			continue
		}
		unit.Value = bytes.Replace(unit.Value, []byte("第一段。\n\n第二段。"), []byte("第二段。\n\n第一段。"), 1)
		units[key] = unit
		found = true
		break
	}
	if !found {
		t.Fatal("custom shell unit not found")
	}
	edited, err := document.WithSemanticUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := edited.Render()
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(mixed, []byte("第一段。\n\n第二段。"), []byte("第二段。\n\n第一段。"), 1)
	if !bytes.Equal(rendered, want) {
		t.Fatal("custom paragraph edit rewrote unrelated mixed line endings")
	}
}

func TestV4ShellCustomFrontmatterCanBeAddedAndRemovedWithoutRewritingMachineBindings(t *testing.T) {
	document, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	units := document.SemanticUnits()
	delete(units, UnitKey{Kind: UnitFrontmatter, Name: "custom_owner"})
	units[UnitKey{Kind: UnitFrontmatter, Name: "custom_team"}] = Unit{Present: true, Value: []byte("codec\n")}

	edited, err := document.WithSemanticUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := edited.Render()
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(raw, []byte("custom_owner: 保留\n"), nil, 1)
	want = bytes.Replace(want, []byte("---\n# 项目回顾"), []byte("custom_team: codec\n---\n# 项目回顾"), 1)
	if !bytes.Equal(rendered, want) {
		t.Fatal("custom frontmatter edit rewrote unrelated document bytes")
	}
	if _, err := ParseV4("项目回顾.md", rendered, ledger); err != nil {
		t.Fatalf("custom frontmatter result cannot be reparsed: %v", err)
	}
}

func TestV4ShellCustomFrontmatterUsesCRLFForChangedAndAddedUnits(t *testing.T) {
	_, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	crlf := bytes.ReplaceAll(raw, []byte("\n"), []byte("\r\n"))
	crlf = bytes.Replace(crlf, []byte("这段自定义文本和 [链接](https://example.test/custom) 必须原样保留。\r\n"), []byte("这段自定义文本和 [链接](https://example.test/custom) 必须原样保留。\n"), 1)
	document, err := ParseV4("项目回顾.md", crlf, ledger)
	if err != nil {
		t.Fatal(err)
	}
	units := document.SemanticUnits()
	owner := UnitKey{Kind: UnitFrontmatter, Name: "custom_owner"}
	unit := units[owner]
	unit.Value = []byte("修改\n")
	units[owner] = unit
	units[UnitKey{Kind: UnitFrontmatter, Name: "custom_team"}] = Unit{Present: true, Value: []byte("codec\n")}

	edited, err := document.WithSemanticUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := edited.Render()
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(crlf, []byte("custom_owner: 保留\r\n"), []byte("custom_owner: 修改\r\n"), 1)
	want = bytes.Replace(want, []byte("---\r\n# 项目回顾"), []byte("custom_team: codec\r\n---\r\n# 项目回顾"), 1)
	if !bytes.Equal(rendered, want) {
		t.Fatalf("CRLF frontmatter edit changed physical bytes\ngot:  %q\nwant: %q", rendered[:bytes.Index(rendered, []byte("# 项目回顾"))], want[:bytes.Index(want, []byte("# 项目回顾"))])
	}
	if !bytes.Contains(rendered, []byte("必须原样保留。\n")) {
		t.Fatal("unrelated mixed-LF body byte was normalized")
	}
}

func TestV4ShellFlowCustomFrontmatterChangesPreserveEntryBoundaries(t *testing.T) {
	_, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	tests := []struct {
		name, before, after string
	}{
		{
			name:   "first quoted entry",
			before: `{"custom_owner": "保留", id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: !!int +1, generation_id: "generation-\x31", minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1}`,
			after:  `{"custom_owner": "修改", id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: !!int +1, generation_id: "generation-\x31", minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1}`,
		},
		{
			name:   "middle nested entry",
			before: `{id: review-project-p, entity_type: project-review, project_id: project-p, custom_owner: {name: "保留", roles: [writer, reviewer], url: https://example.test/a#part}, schema_version: 4, document_format: review-markdown-v1, revision: !!int +1, generation_id: "generation-\x31", minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1}`,
			after:  `{id: review-project-p, entity_type: project-review, project_id: project-p, custom_owner: {name: "修改", roles: [writer, reviewer], url: 'https://example.test/a#part'}, schema_version: 4, document_format: review-markdown-v1, revision: !!int +1, generation_id: "generation-\x31", minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1}`,
		},
		{
			name:   "last entry",
			before: `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: !!int +1, generation_id: "generation-\x31", minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: '保留'}`,
			after:  `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: !!int +1, generation_id: "generation-\x31", minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: '修改'}`,
		},
		{
			name:   "multiline CRLF with comments",
			before: "{\r\n  id: review-project-p, # identity\r\n  entity_type: project-review,\r\n  project_id: project-p,\r\n  schema_version: 4,\r\n  custom_owner: \"保留\", # human owner\r\n  document_format: review-markdown-v1,\r\n  revision: !!int +1,\r\n  generation_id: \"generation-\\x31\",\r\n  minimum_reader_version: 0.4.1,\r\n  minimum_writer_version: 0.4.1\r\n}",
			after:  "{\r\n  id: review-project-p, # identity\r\n  entity_type: project-review,\r\n  project_id: project-p,\r\n  schema_version: 4,\r\n  custom_owner: \"修改\", # human owner\r\n  document_format: review-markdown-v1,\r\n  revision: !!int +1,\r\n  generation_id: \"generation-\\x31\",\r\n  minimum_reader_version: 0.4.1,\r\n  minimum_writer_version: 0.4.1\r\n}",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectRaw := replaceV4TestFrontmatter(t, raw, []byte(test.before))
			vaultRaw := replaceV4TestFrontmatter(t, raw, []byte(test.after))
			project, err := ParseV4("项目回顾.md", projectRaw, ledger)
			if err != nil {
				t.Fatal(err)
			}
			vault, err := ParseV4("项目回顾.md", vaultRaw, ledger)
			if err != nil {
				t.Fatal(err)
			}
			units := project.SemanticUnits()
			key := UnitKey{Kind: UnitFrontmatter, Name: "custom_owner"}
			units[key] = vault.SemanticUnits()[key]
			edited, err := project.WithSemanticUnits(units)
			if err != nil {
				t.Fatal(err)
			}
			got, err := edited.Render()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, vaultRaw) {
				t.Fatalf("flow custom edit changed bytes outside the selected entry\ngot:\n%s\nwant:\n%s", got, vaultRaw)
			}
			if _, err := ParseV4("项目回顾.md", got, ledger); err != nil {
				t.Fatalf("flow custom edit cannot be reparsed: %v", err)
			}
		})
	}
}

func TestV4ShellFlowCustomFrontmatterCanBeAddedAndRemoved(t *testing.T) {
	_, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	flow := `{id: review-project-p, entity_type: project-review, custom_remove: old, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: keep}`
	projectRaw := replaceV4TestFrontmatter(t, raw, []byte(flow))
	project, err := ParseV4("项目回顾.md", projectRaw, ledger)
	if err != nil {
		t.Fatal(err)
	}
	units := project.SemanticUnits()
	delete(units, UnitKey{Kind: UnitFrontmatter, Name: "custom_remove"})
	units[UnitKey{Kind: UnitFrontmatter, Name: "custom_team"}] = Unit{Present: true, Value: []byte("codec\n")}
	edited, err := project.WithSemanticUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	got, err := edited.Render()
	if err != nil {
		t.Fatal(err)
	}
	wantFlow := `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: keep, custom_team: codec}`
	want := replaceV4TestFrontmatter(t, raw, []byte(wantFlow))
	if !bytes.Equal(got, want) {
		t.Fatalf("flow add/remove changed unrelated entry bytes\ngot:\n%s\nwant:\n%s", got, want)
	}
	if _, err := ParseV4("项目回顾.md", got, ledger); err != nil {
		t.Fatalf("flow add/remove cannot be reparsed: %v", err)
	}
}

func TestV4ShellFlowScalarBoundariesUseValidatedYAMLContext(t *testing.T) {
	_, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	tests := []struct {
		name, before, after string
	}{
		{
			name:   "apostrophe in plain scalar",
			before: `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: team's}`,
			after:  `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: team's-updated}`,
		},
		{
			name:   "double quote in nested plain scalar",
			before: `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: {name: team"blue, roles: [writer, reviewer]}}`,
			after:  `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: {name: team"green, roles: [writer, reviewer]}}`,
		},
		{
			name:   "same-line trailing comma",
			before: `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: keep,}`,
			after:  `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: changed,}`,
		},
		{
			name:   "trailing comma before CRLF comment and close",
			before: "{\r\n  id: review-project-p,\r\n  entity_type: project-review,\r\n  project_id: project-p,\r\n  schema_version: 4,\r\n  document_format: review-markdown-v1,\r\n  revision: 1,\r\n  generation_id: generation-1,\r\n  minimum_reader_version: 0.4.1,\r\n  minimum_writer_version: 0.4.1,\r\n  custom_owner: keep, # trailing separator comment\r\n}",
			after:  "{\r\n  id: review-project-p,\r\n  entity_type: project-review,\r\n  project_id: project-p,\r\n  schema_version: 4,\r\n  document_format: review-markdown-v1,\r\n  revision: 1,\r\n  generation_id: generation-1,\r\n  minimum_reader_version: 0.4.1,\r\n  minimum_writer_version: 0.4.1,\r\n  custom_owner: changed, # trailing separator comment\r\n}",
		},
		{
			name:   "quoted nested tagged Unicode and comments",
			before: "{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: {single: 'team''s', double: \"team\\\"blue\", tagged: !!str 中文, nested: [{label: keep}]}, # owner metadata\n custom_edit: before}",
			after:  "{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: {single: 'team''s', double: \"team\\\"blue\", tagged: !!str 中文, nested: [{label: keep}]}, # owner metadata\n custom_edit: after}",
		},
		{
			name:   "double quoted comma hash",
			before: `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: "team,#blue", custom_keep: yes}`,
			after:  `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: "team,#green", custom_keep: yes}`,
		},
		{
			name:   "single quoted comma hash",
			before: `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: 'team,#blue', custom_keep: yes}`,
			after:  `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: 'team,#green', custom_keep: yes}`,
		},
		{
			name:   "nested quoted punctuation and comment",
			before: "{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: {label: \"team,#blue} still scalar\", alternate: 'team,#blue] still scalar'}, # actual comment },#\n custom_keep: yes}",
			after:  "{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: {label: \"team,#green} still scalar\", alternate: 'team,#blue] still scalar'}, # actual comment },#\n custom_keep: yes}",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectRaw := replaceV4TestFrontmatter(t, raw, []byte(test.before))
			vaultRaw := replaceV4TestFrontmatter(t, raw, []byte(test.after))
			project, err := ParseV4("项目回顾.md", projectRaw, ledger)
			if err != nil {
				t.Fatalf("parse accepted Project flow YAML: %v", err)
			}
			noOp, err := project.WithSemanticUnits(project.SemanticUnits())
			if err != nil {
				t.Fatalf("no-op accepted Project flow YAML: %v", err)
			}
			noOpRaw, err := noOp.Render()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(noOpRaw, projectRaw) {
				t.Fatal("no-op changed accepted flow YAML bytes")
			}
			vault, err := ParseV4("项目回顾.md", vaultRaw, ledger)
			if err != nil {
				t.Fatalf("parse accepted Vault flow YAML: %v", err)
			}
			units := project.SemanticUnits()
			changedKey := UnitKey{Kind: UnitFrontmatter, Name: "custom_owner"}
			if test.name == "quoted nested tagged Unicode and comments" {
				changedKey.Name = "custom_edit"
			}
			units[changedKey] = vault.SemanticUnits()[changedKey]
			merged, err := project.WithSemanticUnits(units)
			if err != nil {
				t.Fatalf("merge accepted flow YAML: %v", err)
			}
			got, err := merged.Render()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, vaultRaw) {
				t.Fatalf("flow YAML merge changed bytes outside the selected entry\ngot:\n%s\nwant:\n%s", got, vaultRaw)
			}
			if _, err := ParseV4("项目回顾.md", got, ledger); err != nil {
				t.Fatalf("merged accepted flow YAML cannot be reopened: %v", err)
			}
		})
	}
}

func TestV4ShellFlowGroupedCustomDeletionsProduceDisjointEdits(t *testing.T) {
	_, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	tests := []struct {
		name, before, want string
		remove             []string
		change             map[string]string
		add                map[string]string
	}{
		{
			name: "final two", remove: []string{"custom_a", "custom_b"},
			before: `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_a: one, custom_b: two}`,
			want:   `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1}`,
		},
		{
			name: "final three retaining trailing comma", remove: []string{"custom_a", "custom_b", "custom_c"},
			before: `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_a: one, custom_b: two, custom_c: three,}`,
			want:   `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1,}`,
		},
		{
			name: "adjacent middle", remove: []string{"custom_a", "custom_b"},
			before: `{id: review-project-p, entity_type: project-review, custom_a: one, custom_b: two, # deleted group comment
 project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_keep: yes}`,
			want: `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_keep: yes}`,
		},
		{
			name: "adjacent prefix", remove: []string{"custom_a", "custom_b"},
			before: `{custom_a: one, custom_b: two, id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1}`,
			want:   `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1}`,
		},
		{
			name: "deletion with addition and value change after trailing comma", remove: []string{"custom_a", "custom_b"},
			change: map[string]string{"custom_owner": "changed\n"}, add: map[string]string{"custom_team": "codec\n"},
			before: `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: keep, custom_a: one, custom_b: two,}`,
			want:   `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: changed, custom_team: codec}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectRaw := replaceV4TestFrontmatter(t, raw, []byte(test.before))
			project, err := ParseV4("项目回顾.md", projectRaw, ledger)
			if err != nil {
				t.Fatalf("parse accepted flow YAML: %v", err)
			}
			units := project.SemanticUnits()
			for _, name := range test.remove {
				delete(units, UnitKey{Kind: UnitFrontmatter, Name: name})
			}
			for name, value := range test.change {
				key := UnitKey{Kind: UnitFrontmatter, Name: name}
				unit := units[key]
				unit.Value = []byte(value)
				units[key] = unit
			}
			for name, value := range test.add {
				units[UnitKey{Kind: UnitFrontmatter, Name: name}] = Unit{Present: true, Value: []byte(value)}
			}
			merged, err := project.WithSemanticUnits(units)
			if err != nil {
				t.Fatalf("apply grouped flow YAML deletion: %v", err)
			}
			got, err := merged.Render()
			if err != nil {
				t.Fatal(err)
			}
			want := replaceV4TestFrontmatter(t, raw, []byte(test.want))
			if !bytes.Equal(got, want) {
				t.Fatalf("grouped flow deletion changed surviving bytes\ngot:\n%s\nwant:\n%s", got, want)
			}
			reopened, err := ParseV4("项目回顾.md", got, ledger)
			if err != nil {
				t.Fatalf("grouped flow deletion cannot be reopened: %v", err)
			}
			stable, err := reopened.WithSemanticUnits(reopened.SemanticUnits())
			if err != nil {
				t.Fatalf("grouped flow deletion no-op: %v", err)
			}
			stableRaw, err := stable.Render()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(stableRaw, got) {
				t.Fatal("grouped flow deletion was not stable on no-op")
			}
		})
	}
}

func TestV4ShellFlowQuotedBraceHashKeepsMappingCloseWhenAdding(t *testing.T) {
	_, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	flow := `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: "team}#blue"}`
	projectRaw := replaceV4TestFrontmatter(t, raw, []byte(flow))
	project, err := ParseV4("项目回顾.md", projectRaw, ledger)
	if err != nil {
		t.Fatalf("parse quoted brace/hash flow YAML: %v", err)
	}
	units := project.SemanticUnits()
	units[UnitKey{Kind: UnitFrontmatter, Name: "custom_team"}] = Unit{Present: true, Value: []byte("codec\n")}
	merged, err := project.WithSemanticUnits(units)
	if err != nil {
		t.Fatalf("add after quoted brace/hash flow YAML: %v", err)
	}
	got, err := merged.Render()
	if err != nil {
		t.Fatal(err)
	}
	wantFlow := `{id: review-project-p, entity_type: project-review, project_id: project-p, schema_version: 4, document_format: review-markdown-v1, revision: 1, generation_id: generation-1, minimum_reader_version: 0.4.1, minimum_writer_version: 0.4.1, custom_owner: "team}#blue", custom_team: codec}`
	want := replaceV4TestFrontmatter(t, raw, []byte(wantFlow))
	if !bytes.Equal(got, want) {
		t.Fatalf("quoted brace/hash moved the mapping close\ngot:\n%s\nwant:\n%s", got, want)
	}
	if _, err := ParseV4("项目回顾.md", got, ledger); err != nil {
		t.Fatalf("quoted brace/hash addition cannot be reopened: %v", err)
	}
}

func replaceV4TestFrontmatter(t *testing.T, raw, replacement []byte) []byte {
	t.Helper()
	frontmatter, _, err := splitFrontmatter(raw)
	if err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(raw, frontmatter)
	if start < 0 {
		t.Fatal("frontmatter source not found")
	}
	result := make([]byte, 0, len(raw)-len(frontmatter)+len(replacement)+1)
	result = append(result, raw[:start]...)
	result = append(result, replacement...)
	if len(replacement) == 0 || replacement[len(replacement)-1] != '\n' {
		result = append(result, '\n')
	}
	result = append(result, raw[start+len(frontmatter):]...)
	return result
}

func TestV4StablePlaceholdersPreserveFormalIdentityAcrossPhysicalReordering(t *testing.T) {
	original, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	parsed, err := reviewv4.ParseMarkdownDocument("项目回顾.md", raw)
	if err != nil {
		t.Fatal(err)
	}
	blocks := parsed.Blocks()
	goal := findV4TestBlock(t, blocks, reviewv4.FieldKey{Entity: "project-overview", Name: "goal"})
	stage := findV4TestBlock(t, blocks, reviewv4.FieldKey{Entity: "project-overview", Name: "stage"})
	problemTree := findV4TestBlock(t, blocks, reviewv4.FieldKey{Entity: "project-overview", Name: "problem-tree"})

	for _, test := range []struct {
		name        string
		first, last reviewv4.MarkdownBlock
	}{
		{name: "two human fields", first: goal, last: stage},
		{name: "human and generated", first: goal, last: problemTree},
	} {
		t.Run(test.name, func(t *testing.T) {
			reorderedRaw := swapV4TestBlocks(raw, test.first, test.last)
			reordered, err := ParseV4("项目回顾.md", reorderedRaw, ledger)
			if err != nil {
				t.Fatal(err)
			}
			applied, err := original.WithSemanticUnits(reordered.SemanticUnits())
			if err != nil {
				t.Fatal(err)
			}
			got, err := applied.Render()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, reorderedRaw) {
				t.Fatal("independent physical reordering was hidden by ordinal placeholders")
			}
			validated, err := reviewv4.ParseMarkdownDocumentAgainstLedger("项目回顾.md", got, ledger)
			if err != nil {
				t.Fatalf("reconstructed identities no longer validate: %v", err)
			}
			for _, block := range validated.Blocks() {
				if block.Key == test.last.Key && block.Generated != test.last.Generated {
					t.Fatalf("block %v changed generated identity", block.Key)
				}
			}
		})
	}
}

func TestV4StablePlaceholdersRejectUnknownAndDuplicateInjection(t *testing.T) {
	document, _, _ := v4FixtureDocument(t, "项目回顾.md", "review.md")
	known := "<!-- sr-v4-block:" + v4PlaceholderIdentity(reviewv4.MarkdownBlock{Key: reviewv4.FieldKey{Entity: "project-overview", Name: "goal"}}) + " -->\n"
	unknown := "<!-- sr-v4-block:field:756e6b6e6f776e:756e6b6e6f776e -->\n"
	for _, test := range []struct {
		name, token string
	}{
		{name: "duplicate known identity", token: known},
		{name: "unknown identity", token: unknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			units := document.SemanticUnits()
			key := UnitKey{Kind: UnitPreamble}
			unit := units[key]
			unit.Value = append(unit.Value, []byte(test.token)...)
			units[key] = unit
			if _, err := document.WithSemanticUnits(units); err == nil {
				t.Fatal("injected internal placeholder was accepted")
			}
		})
	}
}

func findV4TestBlock(t *testing.T, blocks []reviewv4.MarkdownBlock, key reviewv4.FieldKey) reviewv4.MarkdownBlock {
	t.Helper()
	for _, block := range blocks {
		if block.Key == key {
			return block
		}
	}
	t.Fatalf("fixture block %v not found", key)
	return reviewv4.MarkdownBlock{}
}

func swapV4TestBlocks(raw []byte, first, last reviewv4.MarkdownBlock) []byte {
	if first.Start > last.Start {
		first, last = last, first
	}
	result := make([]byte, 0, len(raw))
	result = append(result, raw[:first.Start]...)
	result = append(result, raw[last.Start:last.End]...)
	result = append(result, raw[first.End:last.Start]...)
	result = append(result, raw[first.Start:first.End]...)
	result = append(result, raw[last.End:]...)
	return result
}

func TestV4UnitsCannotAddOrDeleteFormalFields(t *testing.T) {
	document, _, _ := v4FixtureDocument(t, "项目回顾.md", "review.md")
	units := document.SemanticUnits()
	delete(units, UnitKey{Kind: UnitSection, Name: "session-reviewer/v4/project-overview/goal"})
	if _, err := document.WithSemanticUnits(units); err == nil {
		t.Fatal("deleted formal field was accepted")
	}
	units = document.SemanticUnits()
	units[UnitKey{Kind: UnitSection, Name: "session-reviewer/v4/project-overview/unknown"}] = Unit{Present: true, Value: []byte("x")}
	if _, err := document.WithSemanticUnits(units); err == nil {
		t.Fatal("added formal field was accepted")
	}
}

func TestV4ShellSupportsFiveMiBWithoutRelaxingLegacyLimit(t *testing.T) {
	_, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	custom := append([]byte("\n"+strings.Repeat("x", 5<<20)+"\n"), raw...)
	openingEnd := bytes.IndexByte(raw, '\n') + 1
	closing := bytes.Index(raw[openingEnd:], []byte("---\n"))
	if closing < 0 {
		t.Fatal("fixture frontmatter has no close")
	}
	insert := openingEnd + closing + len("---\n")
	custom = append(bytes.Clone(raw[:insert]), append(custom[:(5<<20)+2], raw[insert:]...)...)
	document, err := ParseV4("项目回顾.md", custom, ledger)
	if err != nil {
		t.Fatalf("v4 5 MiB document rejected: %v", err)
	}
	if rendered, err := document.Render(); err != nil || !bytes.Equal(rendered, custom) {
		t.Fatalf("large v4 round trip changed bytes: err=%v", err)
	}
	if _, err := Parse("项目回顾.md", custom); err == nil {
		t.Fatal("legacy parser accepted a document over 4 MiB")
	}
}

func TestV4SensitiveScanMasksOnlyAuthenticatedMarkerIdentities(t *testing.T) {
	_, raw, ledger := v4FixtureDocument(t, "项目回顾.md", "review.md")
	fake := []byte("\n```markdown\n<!-- session-reviewer:v4-field entity=\"risk:/Users/private\" name=\"title\" -->\n```\n")
	raw = append(bytes.Clone(raw), fake...)
	document, err := ParseV4("项目回顾.md", raw, ledger)
	if err != nil {
		t.Fatal(err)
	}
	source, err := document.SensitiveScanSource()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(source, []byte("risk:/Users/private")) {
		t.Fatal("fenced marker-looking human content was masked")
	}
	if bytes.Contains(source, []byte(`entity="project-overview"`)) || !bytes.Contains(source, []byte(`entity="validated-marker"`)) {
		t.Fatal("authenticated marker identities were not masked")
	}
}

func TestV4PlaceholderAssemblyPreflightsInjectedLimit(t *testing.T) {
	placeholder := []byte("<!-- sr-v4-block:0 -->\n")
	block := v4BlockState{
		block:               reviewv4.MarkdownBlock{Key: reviewv4.FieldKey{Entity: "project-overview", Name: "goal"}, ValueStart: 2, ValueEnd: 5},
		unitKey:             UnitKey{Kind: UnitSection, Name: "session-reviewer/v4/project-overview/goal"},
		semanticPlaceholder: placeholder,
		original:            []byte("[old]"),
	}
	fields := UnitSet{block.unitKey: {Present: true, Value: []byte(strings.Repeat("x", 32))}}
	if result, err := replaceV4PlaceholdersBounded(placeholder, []v4BlockState{block}, fields, 16); err == nil || result != nil {
		t.Fatalf("over-limit block assembly result=%q err=%v", result, err)
	}
}

func TestV4SemanticUnitPreflightUsesInjectedLimit(t *testing.T) {
	units := UnitSet{
		{Kind: UnitPreamble}:                 {Present: true, Value: []byte("12345678")},
		{Kind: UnitSection, Name: "heading"}: {Present: true, Value: []byte("12345678"), HeadingPresentation: []byte("# heading\n")},
	}
	if err := preflightV4UnitSet(units, 16); err == nil {
		t.Fatal("aggregate-over-limit semantic units were accepted")
	}
}

func v4FixtureDocument(t *testing.T, relative, fixture string) (Document, []byte, reviewv4.MachineLedger) {
	t.Helper()
	ledgerBody, err := os.ReadFile("../../testdata/contracts/v4/markdown/ledger.json")
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := reviewv4.DecodeLedger(ledgerBody)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../testdata/contracts/v4/markdown/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	document, err := ParseV4(relative, raw, ledger)
	if err != nil {
		t.Fatal(err)
	}
	return document, raw, ledger
}
