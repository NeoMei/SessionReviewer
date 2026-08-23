package ledger

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

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
	before := cloneStateForTest(state)
	if _, err := Render(state, completeChanges()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, before) {
		t.Fatal("Render mutated input state")
	}
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

func cloneStateForTest(state State) State {
	clone := state
	clone.Timeline = append([]TimelineEvent(nil), state.Timeline...)
	clone.Decisions = make(map[string]Decision, len(state.Decisions))
	for id, item := range state.Decisions {
		clone.Decisions[id] = item
	}
	clone.OpenLoops = make(map[string]OpenLoop, len(state.OpenLoops))
	for id, item := range state.OpenLoops {
		clone.OpenLoops[id] = item
	}
	clone.Sessions = make(map[string]SessionReport, len(state.Sessions))
	for id, item := range state.Sessions {
		clone.Sessions[id] = item
	}
	return clone
}
