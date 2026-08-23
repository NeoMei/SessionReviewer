package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

func TestEnsureSafeParentsPropagatesDurableCreatorFailure(t *testing.T) {
	directory, err := pathguard.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	durabilityErr := errors.New("injected ledger parent durability failure")
	err = ensureSafeParentsWith(directory, filepath.Join("docs", "session-review"), 0o755, func(*os.Root, string, fs.FileMode) error {
		return durabilityErr
	})
	if !errors.Is(err, durabilityErr) {
		t.Fatalf("error=%v want=%v", err, durabilityErr)
	}
}

func TestEnsureSafeParentsRetryResyncsExistingParents(t *testing.T) {
	directory, err := pathguard.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	relative := filepath.Join("docs", "session-review")
	firstErr := errors.New("injected ledger parent creation sync failure")
	firstCalls := 0
	err = ensureSafeParentsWith(directory, relative, 0o755, func(root *os.Root, path string, perm fs.FileMode) error {
		firstCalls++
		if err := atomicfile.EnsureRootDir(root, path, perm); err != nil {
			return err
		}
		if path == relative {
			return firstErr
		}
		return nil
	})
	if !errors.Is(err, firstErr) || firstCalls != 2 {
		t.Fatalf("first error=%v calls=%d", err, firstCalls)
	}

	retryErr := errors.New("injected existing ledger parent sync failure")
	var retryPaths []string
	err = ensureSafeParentsWith(directory, relative, 0o755, func(_ *os.Root, path string, _ fs.FileMode) error {
		retryPaths = append(retryPaths, path)
		if path == relative {
			return retryErr
		}
		return nil
	})
	if !errors.Is(err, retryErr) || !reflect.DeepEqual(retryPaths, []string{"docs", relative}) {
		t.Fatalf("retry error=%v paths=%v", err, retryPaths)
	}
}

const testProjectID = "project-1111111111111111"

func TestRenderCompleteLayoutPreservesUserContent(t *testing.T) {
	root := ledgerFixture(t)
	state, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Render(state, completeChanges())
	if err != nil {
		t.Fatal(err)
	}
	files, err := Apply(plan)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"docs/session-review/current-state.md",
		"docs/session-review/decisions/decision-1.md",
		"docs/session-review/evolution-timeline.md",
		"docs/session-review/open-loops/loop-1.md",
		"docs/session-review/sessions/session-s1.md",
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files=%v want=%v", files, want)
	}

	decisionPath := filepath.Join(root, filepath.FromSlash("docs/session-review/decisions/decision-1.md"))
	body, err := os.ReadFile(decisionPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ParseDocument(body)
	if err != nil {
		t.Fatal(err)
	}
	custom, err := encodeValue("gold")
	if err != nil {
		t.Fatal(err)
	}
	setMappingValue(&doc.Frontmatter, "custom_rating", custom)
	doc.Sections = append([]Section{{Name: "My notes", Heading: "## My notes", Body: "\nKeep exactly.\n\n"}}, doc.Sections...)
	body, err = doc.Render()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(decisionPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	next, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	updated := next.Decisions["decision-1"]
	updated.Revision++
	updated.Consequences = "The ledger survives restarts."
	plan, err = Render(next, ChangeSet{Decisions: []Decision{updated}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(decisionPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"custom_rating: gold", "## My notes", "Keep exactly.", "The ledger survives restarts."} {
		if !bytes.Contains(body, []byte(text)) {
			t.Fatalf("decision lost %q:\n%s", text, body)
		}
	}
}

func TestRenderUnchangedReturnsEmptyPlanAndRepeatHasNoDiff(t *testing.T) {
	root := ledgerFixture(t)
	state, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Render(state, completeChanges())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(first); err != nil {
		t.Fatal(err)
	}
	before := ledgerBytes(t, root)
	next, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(next, ChangeSet{})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Files) != 0 {
		t.Fatalf("files=%+v", second.Files)
	}
	written, err := Apply(second)
	if err != nil || len(written) != 0 {
		t.Fatalf("written=%v err=%v", written, err)
	}
	if after := ledgerBytes(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("repeat changed ledger bytes")
	}
}

func TestRenderPreservesArchivedFilesAndUserEditableFields(t *testing.T) {
	root := ledgerFixture(t)
	state, _ := Load(root)
	first, err := Render(state, completeChanges())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(first); err != nil {
		t.Fatal(err)
	}

	decisionPath := filepath.Join(root, "docs/session-review/decisions/decision-1.md")
	body, _ := os.ReadFile(decisionPath)
	doc, err := ParseDocument(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.SetEditable(map[string]any{"title": "User title", "status": "archived", "tags": []string{"user-tag"}}); err != nil {
		t.Fatal(err)
	}
	body, _ = doc.Render()
	if err := os.WriteFile(decisionPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	next, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	loop := next.OpenLoops["loop-1"]
	loop.Revision++
	loop.Blocker = "Waiting for CI"
	plan, err := Render(next, ChangeSet{OpenLoops: []OpenLoop{loop}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	decision, exists := loaded.Decisions["decision-1"]
	if !exists || decision.Title != "User title" || decision.Status != "archived" || !reflect.DeepEqual(decision.Tags, []string{"user-tag"}) {
		t.Fatalf("archived user-edited decision=%+v exists=%t", decision, exists)
	}
}

func TestLoadRoundTripsAllTypedFieldsAndStableOrder(t *testing.T) {
	root := ledgerFixture(t)
	state, _ := Load(root)
	changes := completeChanges()
	changes.Timeline = append(changes.Timeline, TimelineEvent{ID: "event-0", OccurredAt: "2026-08-22T09:00:00Z", Revision: 1, Class: DecisionFact, Title: "Earlier", Summary: "Earlier decision"})
	plan, err := Render(state, changes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ProjectID != testProjectID || !reflect.DeepEqual(loaded.CurrentState, *changes.Current) {
		t.Fatalf("current=%+v", loaded.CurrentState)
	}
	if !reflect.DeepEqual(loaded.Decisions["decision-1"], changes.Decisions[0]) || !reflect.DeepEqual(loaded.OpenLoops["loop-1"], changes.OpenLoops[0]) || !reflect.DeepEqual(loaded.Sessions["session-s1"], changes.Sessions[0]) {
		t.Fatalf("typed entities did not round trip: %+v", loaded)
	}
	if got := []string{loaded.Timeline[0].ID, loaded.Timeline[1].ID}; !reflect.DeepEqual(got, []string{"event-0", "event-1"}) {
		t.Fatalf("timeline order=%v", got)
	}
}

func TestTypedMarkdownListsRoundTripArbitraryPermittedStrings(t *testing.T) {
	root := ledgerFixture(t)
	state, _ := Load(root)
	special := []string{"", " ", "  indented", "- leading marker", "line one\nline two", "trailing ", "\ttabbed"}
	changes := completeChanges()
	changes.Current.UncommittedChanges = append([]string(nil), special...)
	changes.Current.Blockers = append([]string(nil), special...)
	changes.Current.OpenRisks = append([]string(nil), special...)
	changes.Decisions[0].Alternatives = append([]string(nil), special...)
	changes.Decisions[0].RejectedPaths = append([]string(nil), special...)
	changes.OpenLoops[0].Attempts = append([]string(nil), special...)
	changes.Sessions[0].GoalChanges = append([]string(nil), special...)
	changes.Sessions[0].Files = append([]string(nil), special...)
	changes.Sessions[0].Commits = append([]string(nil), special...)
	changes.Sessions[0].Verification = append([]string(nil), special...)
	plan, err := Render(state, changes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][]string{
		"current uncommitted":   loaded.CurrentState.UncommittedChanges,
		"current blockers":      loaded.CurrentState.Blockers,
		"current risks":         loaded.CurrentState.OpenRisks,
		"decision alternatives": loaded.Decisions["decision-1"].Alternatives,
		"decision rejected":     loaded.Decisions["decision-1"].RejectedPaths,
		"loop attempts":         loaded.OpenLoops["loop-1"].Attempts,
		"session goals":         loaded.Sessions["session-s1"].GoalChanges,
		"session files":         loaded.Sessions["session-s1"].Files,
		"session commits":       loaded.Sessions["session-s1"].Commits,
		"session verification":  loaded.Sessions["session-s1"].Verification,
	} {
		if !reflect.DeepEqual(got, special) {
			t.Fatalf("%s=%q want=%q", name, got, special)
		}
	}
	for name, doc := range map[string]Document{
		"session goals": loaded.documents.sessions["session-s1"].Document,
		"session files": loaded.documents.sessions["session-s1"].Document,
	} {
		section := "Goal changes"
		if name == "session files" {
			section = "Files"
		}
		got, err := sectionList(doc, section)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, special) {
			t.Fatalf("%s Markdown=%q want=%q", name, got, special)
		}
	}
}

func TestTypedListCodecRequiresExactSectionMarker(t *testing.T) {
	t.Run("unmarked codec-like bullet remains literal", func(t *testing.T) {
		doc, err := ParseDocument([]byte("---\nid: decision-1\nentity_type: decision\nproject_id: " + testProjectID + "\nrevision: 1\n---\n\n# Decision\n\n## Alternatives\n\n- sr-string: \"ordinary user text\"\n"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := sectionList(doc, "Alternatives")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{`sr-string: "ordinary user text"`}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got=%q want=%q", got, want)
		}
	})
	t.Run("renderer emits exact marker", func(t *testing.T) {
		root := ledgerFixture(t)
		state, _ := Load(root)
		plan, err := Render(state, completeChanges())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, file := range plan.Files {
			if file.RelativePath == "docs/session-review/decisions/decision-1.md" && !bytes.Contains(file.Data, []byte(typedListCodecMarker)) {
				t.Fatalf("decision lacks codec marker:\n%s", file.Data)
			}
			if file.RelativePath == "docs/session-review/decisions/decision-1.md" {
				found = true
			}
		}
		if !found {
			t.Fatal("decision was not rendered")
		}
	})
}

func TestLoadRejectsMalformedOrAmbiguousTypedListCodec(t *testing.T) {
	for name, replacement := range map[string]string{
		"unclosed marker":          "<!-- session-reviewer:list-codec=v1 --",
		"missing marker version":   "<!-- session-reviewer:list-codec -->",
		"unknown version":          "<!-- session-reviewer:list-codec=v2 -->",
		"duplicate marker":         "<!-- session-reviewer:list-codec=v1 -->\n<!-- session-reviewer:list-codec=v1 -->",
		"marker after data":        "- ordinary\n<!-- session-reviewer:list-codec=v1 -->",
		"invalid encoded entry":    "<!-- session-reviewer:list-codec=v1 -->\n- sr-string: \"unterminated",
		"plain entry under marker": "<!-- session-reviewer:list-codec=v1 -->\n- ordinary",
	} {
		t.Run(name, func(t *testing.T) {
			root := ledgerFixture(t)
			body := decisionDocument("decision-1", testProjectID)
			body = bytes.Replace(body, []byte("## Alternatives\n"), []byte("## Alternatives\n\n"+replacement+"\n"), 1)
			writeLedgerFile(t, root, "decisions/decision-1.md", body, 0o644)
			before, _ := os.ReadFile(filepath.Join(root, "docs/session-review/decisions/decision-1.md"))
			if _, err := Load(root); err == nil {
				t.Fatal("malformed codec accepted")
			} else if !strings.Contains(err.Error(), "typed-list codec") {
				t.Fatalf("malformed codec failed for unrelated reason: %v", err)
			}
			after, _ := os.ReadFile(filepath.Join(root, "docs/session-review/decisions/decision-1.md"))
			if !bytes.Equal(before, after) {
				t.Fatal("failed load changed malformed file")
			}
		})
	}
}

func TestRenderUsesCanonicalSectionOrder(t *testing.T) {
	root := ledgerFixture(t)
	state, _ := Load(root)
	plan, err := Render(state, completeChanges())
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string][]byte, len(plan.Files))
	for _, file := range plan.Files {
		byPath[file.RelativePath] = file.Data
	}
	assertSectionOrder(t, byPath["docs/session-review/current-state.md"], []string{"Current goal", "Last verified state", "Repository", "Blockers", "Next action", "Uncommitted changes", "Open risks", "First inspection", "Last updated"})
	assertSectionOrder(t, byPath["docs/session-review/decisions/decision-1.md"], []string{"Context", "Alternatives", "Rationale", "Rejected paths", "Evidence", "Consequences", "Conditions for reevaluation"})
	assertSectionOrder(t, byPath["docs/session-review/open-loops/loop-1.md"], []string{"Question", "Available evidence", "Attempted paths", "Blocking condition", "Recommended next experiment", "Completion criterion"})
}

func TestRenderFreshLayoutIsByteDeterministic(t *testing.T) {
	var baseline map[string][]byte
	for iteration := range 20 {
		root := ledgerFixture(t)
		state, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := Render(state, completeChanges())
		if err != nil {
			t.Fatal(err)
		}
		current := make(map[string][]byte, len(plan.Files))
		for _, file := range plan.Files {
			current[file.RelativePath] = file.Data
		}
		if iteration == 0 {
			baseline = current
		} else if !reflect.DeepEqual(baseline, current) {
			t.Fatalf("fresh render %d differs", iteration)
		}
	}
}

func assertSectionOrder(t *testing.T, body []byte, names []string) {
	t.Helper()
	previous := -1
	for _, name := range names {
		index := bytes.Index(body, []byte("## "+name+"\n"))
		if index <= previous {
			t.Fatalf("section %q is missing or out of order:\n%s", name, body)
		}
		previous = index
	}
}

func TestLoadFailsClosedForUnsafeOrMalformedFiles(t *testing.T) {
	tests := map[string]func(*testing.T, string){
		"invalid UTF-8": func(t *testing.T, root string) {
			writeLedgerFile(t, root, "decisions/bad.md", []byte{0xff}, 0o644)
		},
		"oversized": func(t *testing.T, root string) {
			writeLedgerFile(t, root, "decisions/big.md", bytes.Repeat([]byte("x"), MaxDocumentBytes+1), 0o644)
		},
		"malformed": func(t *testing.T, root string) {
			writeLedgerFile(t, root, "decisions/bad.md", []byte("---\nid: bad\n"), 0o644)
		},
		"non regular": func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "docs/session-review/decisions/directory.md"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"project mismatch": func(t *testing.T, root string) {
			writeLedgerFile(t, root, "decisions/decision-1.md", decisionDocument("decision-1", "project-2222222222222222"), 0o644)
		},
		"filename mismatch": func(t *testing.T, root string) {
			writeLedgerFile(t, root, "decisions/wrong.md", decisionDocument("decision-1", testProjectID), 0o644)
		},
		"duplicate entity ID": func(t *testing.T, root string) {
			writeLedgerFile(t, root, "decisions/same.md", decisionDocument("same", testProjectID), 0o644)
			writeLedgerFile(t, root, "open-loops/same.md", openLoopDocument("same", testProjectID), 0o644)
		},
		"duplicate session ID": func(t *testing.T, root string) {
			writeLedgerFile(t, root, "sessions/report-1.md", sessionDocument("report-1", "session-1", testProjectID), 0o644)
			writeLedgerFile(t, root, "sessions/report-2.md", sessionDocument("report-2", "session-1", testProjectID), 0o644)
		},
		"wrong entity class": func(t *testing.T, root string) {
			writeLedgerFile(t, root, "decisions/loop-1.md", openLoopDocument("loop-1", testProjectID), 0o644)
		},
		"invalid known status": func(t *testing.T, root string) {
			body := bytes.Replace(decisionDocument("decision-1", testProjectID), []byte("status: accepted"), []byte("status: unknown"), 1)
			writeLedgerFile(t, root, "decisions/decision-1.md", body, 0o644)
		},
		"reserved document ID": func(t *testing.T, root string) {
			writeLedgerFile(t, root, "decisions/current-state.md", decisionDocument("current-state", testProjectID), 0o644)
		},
	}
	if runtime.GOOS != "windows" {
		tests["redirected entity"] = func(t *testing.T, root string) {
			outside := filepath.Join(t.TempDir(), "outside.md")
			if err := os.WriteFile(outside, decisionDocument("redirect", testProjectID), 0o644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "docs/session-review/decisions/redirect.md")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, corrupt := range tests {
		t.Run(name, func(t *testing.T) {
			root := ledgerFixture(t)
			corrupt(t, root)
			before := ledgerBytes(t, root)
			if _, err := Load(root); err == nil {
				t.Fatal("unsafe or malformed ledger accepted")
			}
			if after := ledgerBytes(t, root); !reflect.DeepEqual(before, after) {
				t.Fatal("failed load changed files")
			}
		})
	}
}

func TestLoadRejectsInvalidOverviewUTF8AndOversize(t *testing.T) {
	for name, body := range map[string][]byte{
		"invalid UTF-8": append([]byte("---\nproject_id: "+testProjectID+"\n---\n"), 0xff),
		"oversized":     bytes.Repeat([]byte("x"), MaxDocumentBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			root := ledgerFixture(t)
			writeLedgerFile(t, root, "project-overview.md", body, 0o644)
			if _, err := Load(root); err == nil {
				t.Fatal("invalid project overview accepted")
			}
		})
	}
}

func TestRenderUpdatesCurrentAndTimelineIncrementally(t *testing.T) {
	root := ledgerFixture(t)
	state, _ := Load(root)
	first, err := Render(state, completeChanges())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(first); err != nil {
		t.Fatal(err)
	}
	next, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	current := next.CurrentState
	current.Revision++
	current.NextAction = "Commit Task 4"
	event := next.Timeline[0]
	event.Revision++
	event.Summary = "The renderer is implemented."
	plan, err := Render(next, ChangeSet{Current: &current, Timeline: []TimelineEvent{event}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CurrentState.NextAction != current.NextAction || loaded.Timeline[0].Summary != event.Summary {
		t.Fatalf("current=%+v timeline=%+v", loaded.CurrentState, loaded.Timeline)
	}
}

func TestRenderFailureReturnsNoPlanAndWritesNothing(t *testing.T) {
	root := ledgerFixture(t)
	state, _ := Load(root)
	before := ledgerBytes(t, root)
	changes := completeChanges()
	changes.Decisions[0].Rationale = strings.Repeat("x", MaxDocumentBytes)
	plan, err := Render(state, changes)
	if err == nil || len(plan.Files) != 0 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if after := ledgerBytes(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("render failure wrote files")
	}
}

func TestRenderDoesNotMutateInputState(t *testing.T) {
	root := ledgerFixture(t)
	state, _ := Load(root)
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	beforeDocuments := snapshotLoadedDocuments(t, state)
	if _, err := Render(state, completeChanges()); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !reflect.DeepEqual(beforeDocuments, snapshotLoadedDocuments(t, state)) {
		t.Fatal("Render mutated input state")
	}
}

func TestRenderDeepClonesLoadedDocumentsAndIsRepeatable(t *testing.T) {
	root := ledgerFixture(t)
	state, _ := Load(root)
	first, err := Render(state, completeChanges())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(first); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotLoadedDocuments(t, loaded)
	beforeState, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	updated := cloneDecision(loaded.Decisions["decision-1"])
	updated.Revision++
	updated.Rationale = "Repeated renders must be pure."
	changes := ChangeSet{Decisions: []Decision{updated}}
	one, err := Render(loaded, changes)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Render(loaded, changes)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one, two) {
		t.Fatal("repeated Render calls differ")
	}
	after := snapshotLoadedDocuments(t, loaded)
	if !reflect.DeepEqual(before, after) {
		for key, want := range before {
			if !bytes.Equal(want, after[key]) {
				t.Fatalf("Render mutated loaded document %s\nbefore:\n%s\nafter:\n%s", key, want, after[key])
			}
		}
		t.Fatal("Render changed loaded document inventory")
	}
	afterState, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeState, afterState) {
		t.Fatalf("Render mutated typed state: before=%s after=%s", beforeState, afterState)
	}
}

func snapshotLoadedDocuments(t *testing.T, state State) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	add := func(name string, loaded *loadedDocument) {
		if loaded == nil {
			return
		}
		body, err := loaded.Document.Render()
		if err != nil {
			t.Fatal(err)
		}
		result[name] = body
	}
	add("current", state.documents.current)
	add("timeline", state.documents.timeline)
	for id, loaded := range state.documents.decisions {
		item := loaded
		add("decision/"+id, &item)
	}
	for id, loaded := range state.documents.openLoops {
		item := loaded
		add("loop/"+id, &item)
	}
	for id, loaded := range state.documents.sessions {
		item := loaded
		add("session/"+id, &item)
	}
	return result
}

func TestExpectedProjectRootRejectsReplacementInsideLedgerOpen(t *testing.T) {
	t.Run("load", func(t *testing.T) {
		root := ledgerFixture(t)
		moved := root + "-moved"
		expected, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		_, err = loadWithRootOptions(root, rootOpenOptions{
			expectedRoot: expected,
			beforeOpen: func() error {
				if err := os.Rename(root, moved); err != nil {
					return err
				}
				if err := os.MkdirAll(filepath.Join(root, "docs", "session-review"), 0o755); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(root, "docs", "session-review", "project-overview.md"), []byte("---\nproject_id: "+testProjectID+"\n---\n\n# Replacement\n"), 0o644)
			},
		})
		if err == nil || !strings.Contains(err.Error(), "expected project root identity") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("apply", func(t *testing.T) {
		root := ledgerFixture(t)
		moved := root + "-moved"
		expected, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		relative := "docs/session-review/decisions/replacement.md"
		plan := WritePlan{ProjectRoot: root, Files: []PlannedFile{{RelativePath: relative, Data: decisionDocument("replacement", testProjectID), Perm: 0o644}}}
		_, err = applyWithRootOptions(plan, rootOpenOptions{
			expectedRoot: expected,
			beforeOpen: func() error {
				if err := os.Rename(root, moved); err != nil {
					return err
				}
				return os.Mkdir(root, 0o700)
			},
		})
		if err == nil || !strings.Contains(err.Error(), "expected project root identity") {
			t.Fatalf("err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("replacement tree target=%v", err)
		}
	})
}

func TestApplyRejectsTraversalRedirectAndConcurrentEdit(t *testing.T) {
	t.Run("traversal", func(t *testing.T) {
		root := ledgerFixture(t)
		outside := filepath.Join(filepath.Dir(root), "outside.md")
		plan := WritePlan{ProjectRoot: root, Files: []PlannedFile{{RelativePath: "../outside.md", Data: []byte("bad"), Perm: 0o644}}}
		if _, err := Apply(plan); err == nil {
			t.Fatal("traversal accepted")
		}
		if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("outside created: %v", err)
		}
	})
	if runtime.GOOS != "windows" {
		t.Run("redirected parent", func(t *testing.T) {
			root := ledgerFixture(t)
			outside := t.TempDir()
			ledger := filepath.Join(root, "docs/session-review")
			if err := os.Remove(filepath.Join(ledger, "decisions")); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(ledger, "decisions")); err != nil {
				t.Fatal(err)
			}
			plan := WritePlan{ProjectRoot: root, Files: []PlannedFile{{RelativePath: "docs/session-review/decisions/escape.md", Data: []byte("bad"), Perm: 0o644}}}
			if _, err := Apply(plan); err == nil {
				t.Fatal("redirected parent accepted")
			}
			if _, err := os.Stat(filepath.Join(outside, "escape.md")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("outside created: %v", err)
			}
		})
	}
	t.Run("concurrent user edit", func(t *testing.T) {
		root := ledgerFixture(t)
		state, _ := Load(root)
		plan, err := Render(state, completeChanges())
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "docs/session-review/current-state.md")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		const userEdit = "user-created-concurrently\n"
		if err := os.WriteFile(target, []byte(userEdit), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Apply(plan); err == nil {
			t.Fatal("concurrent edit overwritten")
		}
		got, _ := os.ReadFile(target)
		if string(got) != userEdit {
			t.Fatalf("concurrent edit changed: %q", got)
		}
	})
	t.Run("concurrent mode edit", func(t *testing.T) {
		root := ledgerFixture(t)
		state, _ := Load(root)
		first, err := Render(state, completeChanges())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Apply(first); err != nil {
			t.Fatal(err)
		}
		next, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		decision := next.Decisions["decision-1"]
		decision.Revision++
		decision.Rationale = "New rationale"
		plan, err := Render(next, ChangeSet{Decisions: []Decision{decision}})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "docs/session-review/decisions/decision-1.md")
		before, _ := os.ReadFile(path)
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Apply(plan); err == nil {
			t.Fatal("concurrent mode edit overwritten")
		}
		after, _ := os.ReadFile(path)
		info, _ := os.Stat(path)
		if !bytes.Equal(before, after) || info.Mode().Perm() != 0o600 {
			t.Fatalf("content or mode changed")
		}
	})
}

func TestApplySortsPathsSkipsIdenticalAndUsesRequestedMode(t *testing.T) {
	root := ledgerFixture(t)
	state, _ := Load(root)
	plan, err := Render(state, completeChanges())
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(plan.Files)
	written, err := Apply(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.IsSorted(written) {
		t.Fatalf("paths not sorted: %v", written)
	}
	for _, relative := range written {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o644 {
			t.Fatalf("%s mode=%#o", relative, got)
		}
	}
	loaded, _ := Load(root)
	repeat, err := Render(loaded, ChangeSet{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Apply(repeat); err != nil || len(got) != 0 {
		t.Fatalf("repeat=%v err=%v", got, err)
	}
}

func TestRenderPreservesExistingRegularFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows exposes writable/read-only rather than POSIX permission bits")
	}
	root := ledgerFixture(t)
	state, _ := Load(root)
	plan, err := Render(state, completeChanges())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "docs/session-review/decisions/decision-1.md")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	decision := next.Decisions["decision-1"]
	decision.Revision++
	decision.Rationale = "Updated safely."
	plan, err = Render(next, ChangeSet{Decisions: []Decision{decision}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode=%#o", got)
	}
}

func TestApplyCreatesMissingLedgerClassDirectoriesSafely(t *testing.T) {
	root := t.TempDir()
	writeLedgerFile(t, root, "project-overview.md", []byte("---\nproject_id: "+testProjectID+"\n---\n\n# Fixture\n"), 0o644)
	state, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Render(state, completeChanges())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"decisions/decision-1.md", "open-loops/loop-1.md", "sessions/session-s1.md"} {
		if _, err := os.Stat(filepath.Join(root, "docs/session-review", filepath.FromSlash(relative))); err != nil {
			t.Fatalf("%s: %v", relative, err)
		}
	}
}

func TestApplyRejectsInvalidBytesBeforeWritingAnyFile(t *testing.T) {
	for name, invalid := range map[string][]byte{
		"invalid UTF-8": {0xff},
		"oversized":     bytes.Repeat([]byte("x"), MaxDocumentBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			root := ledgerFixture(t)
			validPath := "docs/session-review/decisions/a-valid.md"
			plan := WritePlan{ProjectRoot: root, Files: []PlannedFile{
				{RelativePath: validPath, Data: decisionDocument("a-valid", testProjectID), Perm: 0o644},
				{RelativePath: "docs/session-review/decisions/z-invalid.md", Data: invalid, Perm: 0o644},
			}}
			if _, err := Apply(plan); err == nil {
				t.Fatal("invalid planned bytes accepted")
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(validPath))); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("valid earlier path was written: %v", err)
			}
		})
	}
}

func TestApplyRevalidatesEachTargetImmediatelyBeforeWrite(t *testing.T) {
	root := ledgerFixture(t)
	files := make([]PlannedFile, 31)
	for i := range files {
		id := fmt.Sprintf("decision-%02d", i)
		files[i] = PlannedFile{
			RelativePath: "docs/session-review/decisions/" + id + ".md",
			Data:         decisionDocument(id, testProjectID),
			Perm:         0o644,
		}
	}
	last := filepath.Join(root, filepath.FromSlash(files[30].RelativePath))
	const userEdit = "user edit after earlier writes\n"
	written, err := applyWithHooks(WritePlan{ProjectRoot: root, Files: files}, applyHooks{
		beforeWrite: func(index int, _ PlannedFile) error {
			if index != 30 {
				return nil
			}
			return os.WriteFile(last, []byte(userEdit), 0o600)
		},
	})
	if err == nil {
		t.Fatal("late concurrent edit was overwritten")
	}
	if len(written) != 30 {
		t.Fatalf("written=%d want=30 err=%v", len(written), err)
	}
	body, readErr := os.ReadFile(last)
	if readErr != nil || string(body) != userEdit {
		t.Fatalf("last=%q err=%v", body, readErr)
	}
}

func TestApplyChecksExpectationBeforeIdenticalShortCircuit(t *testing.T) {
	t.Run("unexpected exact creator", func(t *testing.T) {
		root := ledgerFixture(t)
		relative := "docs/session-review/decisions/exact.md"
		data := decisionDocument("exact", testProjectID)
		writeLedgerFile(t, root, "decisions/exact.md", data, 0o644)
		plan := WritePlan{ProjectRoot: root, Files: []PlannedFile{{RelativePath: relative, Data: data, Perm: 0o644}}}
		if _, err := Apply(plan); err == nil {
			t.Fatal("unexpected exact creator was accepted")
		}
	})
	t.Run("wrong expected mode with identical data", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows does not expose POSIX permission bits")
		}
		root := ledgerFixture(t)
		relative := "docs/session-review/decisions/mode.md"
		data := decisionDocument("mode", testProjectID)
		writeLedgerFile(t, root, "decisions/mode.md", data, 0o600)
		plan := WritePlan{ProjectRoot: root, Files: []PlannedFile{{RelativePath: relative, Data: data, Perm: 0o600, ExpectedExists: true, ExpectedData: data, ExpectedPerm: 0o644}}}
		if _, err := Apply(plan); err == nil {
			t.Fatal("wrong expected mode was accepted")
		}
	})
}

func ledgerFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeLedgerFile(t, root, "project-overview.md", []byte("---\nproject_id: "+testProjectID+"\ncreated_at: 2026-08-22T12:00:00Z\n---\n\n# Fixture\n"), 0o644)
	for _, dir := range []string{"decisions", "open-loops", "sessions"} {
		if err := os.MkdirAll(filepath.Join(root, "docs/session-review", dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func writeLedgerFile(t *testing.T, root, relative string, data []byte, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, "docs/session-review", filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func completeChanges() ChangeSet {
	evidence := []EvidenceRef{{EvidenceID: "evidence-1", SessionID: "s1", JSONLLine: 2, SourceHash: strings.Repeat("a", 64), Summary: "Tests passed"}}
	current := &CurrentState{ProjectID: testProjectID, Revision: 1, Goal: "Ship the ledger", LastVerified: "Focused tests pass", Branch: "main", UncommittedChanges: []string{"internal/ledger/render.go"}, Blockers: []string{"none"}, OpenRisks: []string{"Windows needs native CI"}, NextAction: "Run all tests", FirstInspection: "internal/ledger/render.go", LastUpdated: "2026-08-22T12:00:00Z", SourceSessions: []string{"s1"}, Evidence: evidence}
	decision := Decision{ID: "decision-1", ProjectID: testProjectID, Title: "Use durable Markdown", Status: "accepted", Revision: 1, Tags: []string{"durability", "ledger"}, Supersedes: []string{}, SourceSessions: []string{"s1"}, Evidence: evidence, Context: "Session state was ephemeral.", Alternatives: []string{"Raw transcripts"}, Rationale: "Markdown is editable.", RejectedPaths: []string{"Opaque database only"}, Consequences: "History is durable.", ReevaluateWhen: "Markdown cannot scale."}
	loop := OpenLoop{ID: "loop-1", ProjectID: testProjectID, Title: "Verify Windows", Status: "open", Revision: 1, Tags: []string{"windows"}, SourceSessions: []string{"s1"}, Evidence: evidence, Question: "Does native Windows pass?", Attempts: []string{"Cross compile"}, Blocker: "No Windows host", NextExperiment: "Run CI", CompletionCriterion: "Native suite passes"}
	event := TimelineEvent{ID: "event-1", OccurredAt: "2026-08-22T10:00:00Z", Revision: 1, Class: Verified, Title: "Ledger selected", Summary: "The durable ledger design was accepted.", Evidence: evidence, DecisionIDs: []string{"decision-1"}, OpenLoopIDs: []string{"loop-1"}}
	report := SessionReport{ID: "session-s1", ProjectID: testProjectID, SessionID: "s1", Revision: 1, InitialGoal: "Build a durable ledger", GoalChanges: []string{"Add safe rendering"}, Phases: []SessionPhase{{Title: "Design", Summary: "Selected Markdown", Evidence: evidence}}, Files: []string{"internal/ledger/render.go"}, Commits: []string{"deadbeef"}, Verification: []string{"go test ./internal/ledger"}, DecisionsAdded: []string{"decision-1"}, DecisionsRevised: []string{}, OpenLoopsCreated: []string{"loop-1"}, OpenLoopsClosed: []string{}, PreviousSessionID: "s0", NextSessionID: "s2", Evidence: evidence}
	return ChangeSet{Current: current, Timeline: []TimelineEvent{event}, Decisions: []Decision{decision}, OpenLoops: []OpenLoop{loop}, Sessions: []SessionReport{report}}
}

func decisionDocument(id, projectID string) []byte {
	return []byte("---\nid: " + id + "\nentity_type: decision\nproject_id: " + projectID + "\nrevision: 1\ntitle: Decision\nstatus: accepted\ntags: []\nsupersedes: []\nsource_sessions: []\nevidence: []\n---\n\n# Decision\n\n## Alternatives\n\n## Rejected paths\n")
}

func openLoopDocument(id, projectID string) []byte {
	return []byte("---\nid: " + id + "\nentity_type: open_loop\nproject_id: " + projectID + "\nrevision: 1\ntitle: Loop\nstatus: open\ntags: []\nsource_sessions: []\nevidence: []\n---\n\n# Loop\n\n## Attempted paths\n")
}

func sessionDocument(id, sessionID, projectID string) []byte {
	return []byte("---\nid: " + id + "\nentity_type: session\nproject_id: " + projectID + "\nrevision: 1\nsession_id: " + sessionID + "\ninitial_goal: ''\ngoal_changes: []\nphases: []\nfiles: []\ncommits: []\nverification: []\ndecisions_added: []\ndecisions_revised: []\nopen_loops_created: []\nopen_loops_closed: []\nprevious_session_id: ''\nnext_session_id: ''\nevidence: []\n---\n\n# Session\n")
}

func ledgerBytes(t *testing.T, root string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	base := filepath.Join(root, "docs/session-review")
	_ = filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		relative, _ := filepath.Rel(base, path)
		body, readErr := os.ReadFile(path)
		if readErr == nil {
			result[filepath.ToSlash(relative)] = body
		}
		return nil
	})
	return result
}
