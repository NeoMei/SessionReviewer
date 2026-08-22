package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const validFrontmatter = "---\nid: decision-1\nentity_type: decision\nproject_id: project-1111111111111111\nrevision: 2\n"

func TestDocumentPreservesUnknownContentAndIsStable(t *testing.T) {
	src := []byte(validFrontmatter + "custom_rating: gold\n---\n\n# Title\n\n## Rationale\n\nOld.\n\n## My notes\n\nKeep exactly.\n")
	doc, err := ParseDocument(src)
	if err != nil {
		t.Fatal(err)
	}
	doc.UpsertSection("Rationale", "New.\n")
	a, err := doc.Render()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseDocument(a)
	if err != nil {
		t.Fatal(err)
	}
	b, err := parsed.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"custom_rating: gold", "## My notes", "Keep exactly.", "New."} {
		if !bytes.Contains(a, []byte(s)) {
			t.Fatalf("render does not contain %q:\n%s", s, a)
		}
	}
	if bytes.Contains(a, []byte("Old.")) {
		t.Fatalf("old section body survived update:\n%s", a)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("render is not stable:\nfirst:\n%s\nsecond:\n%s", a, b)
	}
}

func TestDocumentPreservesYAMLNodesAndSectionOrder(t *testing.T) {
	src := []byte(validFrontmatter + `title: 'User title'
status: accepted
tags: [one, "two"]
custom_anchor: &details
  tone: "warm"
custom_alias: *details
custom_tagged: !user gold
custom_block: |-
  keep
  this
---

# User heading

Opening prose.

## First unknown

Keep first.

## Rationale

Old rationale.

## Last unknown

Keep last.
`)
	doc, err := ParseDocument(src)
	if err != nil {
		t.Fatal(err)
	}
	doc.UpsertSection("Rationale", "Replacement.\n")
	got, err := doc.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"title: 'User title'", `tags: [one, "two"]`, "custom_anchor: &details", "custom_alias: *details", "custom_tagged: !user gold", "custom_block: |-",
	} {
		if !bytes.Contains(got, []byte(want)) {
			t.Fatalf("render lost YAML node style %q:\n%s", want, got)
		}
	}
	first := bytes.Index(got, []byte("## First unknown"))
	rationale := bytes.Index(got, []byte("## Rationale"))
	last := bytes.Index(got, []byte("## Last unknown"))
	if !(first >= 0 && first < rationale && rationale < last) {
		t.Fatalf("section order changed:\n%s", got)
	}
	if !bytes.Contains(got, []byte("Opening prose.")) {
		t.Fatalf("preamble lost:\n%s", got)
	}
}

func TestDocumentRejectsReservedIdentityChange(t *testing.T) {
	base := map[string]any{
		"id":          "decision-1",
		"entity_type": "decision",
		"project_id":  "project-1111111111111111",
		"revision":    3,
	}
	tests := map[string]map[string]any{
		"id":          {"id": "decision-2"},
		"entity type": {"entity_type": "open_loop"},
		"project id":  {"project_id": "project-2222222222222222"},
	}
	for name, changed := range tests {
		t.Run(name, func(t *testing.T) {
			doc := mustParseDocument(t, []byte(validFrontmatter+"---\n\n# T\n"))
			update := cloneMap(base)
			for key, value := range changed {
				update[key] = value
			}
			if err := doc.SetReserved(update); !errors.Is(err, ErrReservedFieldChanged) {
				t.Fatalf("err=%v", err)
			}
			assertRevision(t, doc, "revision: 2")
		})
	}
}

func TestDocumentRequiresCompleteReservedIdentity(t *testing.T) {
	for _, missing := range []string{"id", "entity_type", "project_id", "revision"} {
		t.Run(missing, func(t *testing.T) {
			doc := mustParseDocument(t, []byte(validFrontmatter+"---\n\n# T\n"))
			update := map[string]any{
				"id":          "decision-1",
				"entity_type": "decision",
				"project_id":  "project-1111111111111111",
				"revision":    3,
			}
			delete(update, missing)
			if err := doc.SetReserved(update); err == nil {
				t.Fatal("incomplete reserved update accepted")
			}
		})
	}
}

func TestDocumentReservedRevisionMustIncrementExactly(t *testing.T) {
	for name, revision := range map[string]any{
		"unchanged": 2,
		"jump":      4,
		"lower":     1,
		"negative":  -1,
		"fraction":  3.5,
		"string":    "3",
	} {
		t.Run(name, func(t *testing.T) {
			doc := mustParseDocument(t, []byte(validFrontmatter+"---\n\n# T\n"))
			err := doc.SetReserved(map[string]any{
				"id":          "decision-1",
				"entity_type": "decision",
				"project_id":  "project-1111111111111111",
				"revision":    revision,
			})
			if !errors.Is(err, ErrReservedFieldChanged) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestDocumentRejectsRevisionOverflow(t *testing.T) {
	src := []byte(strings.Replace(validFrontmatter, "revision: 2", "revision: "+strconv.Itoa(math.MaxInt), 1) + "---\n\n# T\n")
	doc := mustParseDocument(t, src)
	err := doc.SetReserved(map[string]any{
		"id":          "decision-1",
		"entity_type": "decision",
		"project_id":  "project-1111111111111111",
		"revision":    math.MaxInt,
	})
	if !errors.Is(err, ErrReservedFieldChanged) {
		t.Fatalf("overflow update err=%v", err)
	}
}

func TestDocumentSetReservedUpdatesRevisionAndSourcesAtomically(t *testing.T) {
	doc := mustParseDocument(t, []byte(validFrontmatter+"source_sessions: [old]\nsync_hash: old-hash\n---\n\n# T\n"))
	err := doc.SetReserved(map[string]any{
		"id":              "decision-1",
		"entity_type":     "decision",
		"project_id":      "project-1111111111111111",
		"revision":        3,
		"source_sessions": []string{"old", "new"},
		"sync_hash":       "new-hash",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := doc.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"revision: 3", "source_sessions: [old, new]", "sync_hash: new-hash"} {
		if !bytes.Contains(got, []byte(want)) {
			t.Fatalf("render missing %q:\n%s", want, got)
		}
	}
}

func TestDocumentSetEditableOnlyUpdatesEditableFields(t *testing.T) {
	src := []byte(validFrontmatter + "title: Old\nstatus: proposed\ntags: [old]\ncustom: keep\nsource_sessions: [session-1]\n---\n\n# User heading\n\nNarrative.\n")
	doc := mustParseDocument(t, src)
	if err := doc.SetEditable(map[string]any{
		"title":  "New title",
		"status": "accepted",
		"tags":   []string{"alpha", "beta"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := doc.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"title: New title", "status: accepted", "tags: [alpha, beta]", "custom: keep", "source_sessions: [session-1]", "# User heading", "Narrative."} {
		if !bytes.Contains(got, []byte(want)) {
			t.Fatalf("render missing %q:\n%s", want, got)
		}
	}

	for _, field := range []string{"id", "entity_type", "project_id", "revision", "source_sessions", "sync_hash", "narrative", "custom"} {
		t.Run(field, func(t *testing.T) {
			fresh := mustParseDocument(t, src)
			if err := fresh.SetEditable(map[string]any{field: "changed"}); err == nil {
				t.Fatalf("non-editable field %q accepted", field)
			}
		})
	}
}

func TestDocumentSetEditableRejectsInvalidValuesWithoutPartialMutation(t *testing.T) {
	doc := mustParseDocument(t, []byte(validFrontmatter+"title: Old\nstatus: proposed\n---\n\n# T\n"))
	before, err := doc.Render()
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.SetEditable(map[string]any{"title": "New", "tags": []any{"ok", 7}}); err == nil {
		t.Fatal("invalid tags accepted")
	}
	after, err := doc.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("failed edit partially mutated document:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestDocumentUpsertSectionPreservesHeadingAndAppendsNewSection(t *testing.T) {
	doc := mustParseDocument(t, []byte(validFrontmatter+"---\n\n# T\n\n## Rationale ##\n\nOld.\n"))
	doc.UpsertSection("Rationale", "New.\r\n")
	doc.UpsertSection("Consequences", "Added.\r\n")
	got, err := doc.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("## Rationale ##\n\nNew.\n\n## Consequences\n\nAdded.\n")) {
		t.Fatalf("unexpected sections:\n%s", got)
	}
}

func TestDocumentNormalizesCRLFAndFinalNewline(t *testing.T) {
	src := []byte(strings.ReplaceAll(validFrontmatter+"title: T\n---\n\n# T\n\n## Notes\n\nBody.\n\n", "\n", "\r\n"))
	doc := mustParseDocument(t, src)
	got, err := doc.Render()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("\r")) {
		t.Fatalf("CR survived:\n%q", got)
	}
	if !bytes.HasSuffix(got, []byte("\n")) || bytes.HasSuffix(got, []byte("\n\n")) {
		t.Fatalf("render must have exactly one final newline: %q", got)
	}
	parsed := mustParseDocument(t, got)
	again, err := parsed.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, again) {
		t.Fatalf("normalized render unstable:\nfirst=%q\nsecond=%q", got, again)
	}
}

func TestDocumentRejectsMalformedInput(t *testing.T) {
	tests := map[string][]byte{
		"invalid UTF-8":         append([]byte(validFrontmatter), 0xff),
		"missing opening fence": []byte("id: decision-1\n---\n# T\n"),
		"missing closing fence": []byte(validFrontmatter + "# T\n"),
		"empty frontmatter":     []byte("---\n---\n# T\n"),
		"non-mapping YAML":      []byte("---\n- one\n- two\n---\n# T\n"),
		"invalid YAML":          []byte("---\nid: [\n---\n# T\n"),
		"duplicate root key":    []byte(validFrontmatter + "id: decision-2\n---\n# T\n"),
		"duplicate nested key":  []byte(validFrontmatter + "custom:\n  key: one\n  key: two\n---\n# T\n"),
		"reserved alias":        []byte("---\nid: &id decision-1\nentity_type: decision\nproject_id: *id\nrevision: 2\n---\n# T\n"),
		"tagged revision":       []byte(strings.Replace(validFrontmatter, "revision: 2", "revision: !user 2", 1) + "---\n# T\n"),
		"missing id":            []byte(strings.Replace(validFrontmatter, "id: decision-1\n", "", 1) + "---\n# T\n"),
		"missing entity type":   []byte(strings.Replace(validFrontmatter, "entity_type: decision\n", "", 1) + "---\n# T\n"),
		"missing project id":    []byte(strings.Replace(validFrontmatter, "project_id: project-1111111111111111\n", "", 1) + "---\n# T\n"),
		"missing revision":      []byte(strings.Replace(validFrontmatter, "revision: 2\n", "", 1) + "---\n# T\n"),
		"negative revision":     []byte(strings.Replace(validFrontmatter, "revision: 2", "revision: -1", 1) + "---\n# T\n"),
		"duplicate section":     []byte(validFrontmatter + "---\n# T\n\n## Notes\nA\n\n## Notes\nB\n"),
		"bare carriage return":  []byte(strings.Replace(validFrontmatter+"---\n# T\n", "\n", "\r", 1)),
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDocument(src); err == nil {
				t.Fatal("malformed input accepted")
			}
		})
	}
}

func TestDocumentRejectsOversizedInput(t *testing.T) {
	src := append([]byte(validFrontmatter+"---\n\n# T\n"), bytes.Repeat([]byte("x"), MaxDocumentBytes)...)
	if _, err := ParseDocument(src); err == nil {
		t.Fatal("oversized document accepted")
	}
}

func TestEntitySchemaIsPermissiveAndConditional(t *testing.T) {
	body, err := os.ReadFile("../../schemas/entity-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != true {
		t.Fatal("entity schema must preserve unknown user properties")
	}
	required, _ := schema["required"].([]any)
	for _, field := range []string{"id", "entity_type", "project_id", "revision"} {
		if !sliceContains(required, field) {
			t.Fatalf("required field %q missing", field)
		}
	}
	compact := strings.NewReplacer(" ", "", "\n", "", "\t", "").Replace(string(body))
	for _, contract := range []string{
		`"entity_type":{"const":"decision"}`,
		`"entity_type":{"const":"open_loop"}`,
		`"entity_type":{"const":"session"}`,
		`"then":{"required":["title","status"]`,
		`"then":{"required":["session_id"]`,
	} {
		if !strings.Contains(compact, contract) {
			t.Fatalf("schema contract missing: %s", contract)
		}
	}
}

func TestDomainTypesExposeExactFields(t *testing.T) {
	wantClasses := []FactClass{Verified, DecisionFact, Inference, Superseded, PendingConfirmation}
	if got := []FactClass{"verified", "decision", "inference", "superseded", "pending_confirmation"}; !reflect.DeepEqual(wantClasses, got) {
		t.Fatalf("classes=%v", wantClasses)
	}

	state := State{
		ProjectID:    "project-1",
		CurrentState: CurrentState{ProjectID: "project-1"},
		Timeline:     []TimelineEvent{{ID: "event-1"}},
		Decisions:    map[string]Decision{"decision-1": {ID: "decision-1"}},
		OpenLoops:    map[string]OpenLoop{"loop-1": {ID: "loop-1"}},
		Sessions:     map[string]SessionReport{"session-1": {ID: "session-1"}},
	}
	changes := ChangeSet{
		Current:   &state.CurrentState,
		Timeline:  state.Timeline,
		Decisions: []Decision{state.Decisions["decision-1"]},
		OpenLoops: []OpenLoop{state.OpenLoops["loop-1"]},
		Sessions:  []SessionReport{state.Sessions["session-1"]},
	}
	plan := WritePlan{ProjectRoot: "/project", Files: []PlannedFile{{RelativePath: "entity.md", Data: []byte("x"), Perm: 0o600}}}
	if changes.Current.ProjectID != state.ProjectID || plan.Files[0].Perm != 0o600 {
		t.Fatalf("domain types unavailable: state=%+v changes=%+v plan=%+v", state, changes, plan)
	}

	evidence := EvidenceRef{EvidenceID: "e1", SessionID: "s1", JSONLLine: 1, SourceHash: "hash", Summary: "summary"}
	phase := SessionPhase{Title: "phase", Summary: "summary", Evidence: []EvidenceRef{evidence}}
	decision := Decision{ID: "d", ProjectID: "p", Title: "t", Status: "accepted", Revision: 1, Tags: []string{"tag"}, Supersedes: []string{"old"}, SourceSessions: []string{"s"}, Evidence: []EvidenceRef{evidence}, Context: "c", Rationale: "r", Consequences: "c", ReevaluateWhen: "later", Alternatives: []string{"a"}, RejectedPaths: []string{"r"}}
	loop := OpenLoop{ID: "o", ProjectID: "p", Title: "t", Status: "open", Revision: 1, Tags: []string{"tag"}, SourceSessions: []string{"s"}, Evidence: []EvidenceRef{evidence}, Question: "q", Attempts: []string{"a"}, Blocker: "b", NextExperiment: "n", CompletionCriterion: "c"}
	event := TimelineEvent{ID: "e", OccurredAt: "now", Revision: 1, Class: Verified, Title: "t", Summary: "s", Evidence: []EvidenceRef{evidence}, DecisionIDs: []string{"d"}, OpenLoopIDs: []string{"o"}}
	current := CurrentState{ProjectID: "p", Revision: 1, Goal: "g", LastVerified: "v", Branch: "b", UncommittedChanges: []string{"u"}, Blockers: []string{"b"}, OpenRisks: []string{"r"}, NextAction: "n", FirstInspection: "f", LastUpdated: "l", SourceSessions: []string{"s"}, Evidence: []EvidenceRef{evidence}}
	report := SessionReport{ID: "r", ProjectID: "p", SessionID: "s", Revision: 1, InitialGoal: "g", GoalChanges: []string{"c"}, Phases: []SessionPhase{phase}, Files: []string{"f"}, Commits: []string{"c"}, Verification: []string{"v"}, DecisionsAdded: []string{"d"}, DecisionsRevised: []string{"d"}, OpenLoopsCreated: []string{"o"}, OpenLoopsClosed: []string{"o"}, PreviousSessionID: "before", NextSessionID: "after", Evidence: []EvidenceRef{evidence}}
	if _, err := json.Marshal([]any{decision, loop, event, current, report}); err != nil {
		t.Fatal(err)
	}
}

func mustParseDocument(t *testing.T, src []byte) Document {
	t.Helper()
	doc, err := ParseDocument(src)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func cloneMap(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func assertRevision(t *testing.T, doc Document, want string) {
	t.Helper()
	got, err := doc.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(want)) {
		t.Fatalf("render missing %q:\n%s", want, got)
	}
}

func sliceContains(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
