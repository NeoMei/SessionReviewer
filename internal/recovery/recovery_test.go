package recovery

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/ledger"
)

const recoveryProjectID = "project-0123456789abcdef"

func TestResumeIgnoresPendingEvidenceAndUsesLatestAcceptedSession(t *testing.T) {
	root := recoveryFixture(t)
	writeRecoveryCanaries(t, root)
	t.Setenv("SESSION_REVIEWER_RECOVERY_CANARY", "ENV-UNREVIEWED-CANARY")
	before := recoveryTree(t, root)

	card, err := ResumeLedgerOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	want := ResumeCard{
		ProjectID:       recoveryProjectID,
		Goal:            "Ship ledger",
		StopPoint:       "Latest accepted stop",
		LastVerified:    "focused tests pass",
		Drift:           []string{"work-z", "work-a", "risk-z", "risk-a"},
		Blockers:        []string{"block-z", "block-a"},
		OpenQuestions:   []string{"Blocked question", "Open question"},
		NextAction:      "run full tests",
		FirstInspection: "internal/recovery",
		SourceSessions:  []string{"a-latest", "z-earlier"},
	}
	if !reflect.DeepEqual(card, want) {
		t.Fatalf("card=%+v want=%+v", card, want)
	}
	markdown := card.Markdown()
	for _, canary := range []string{"UNREVIEWED-CANARY", "EVIDENCE-CANARY", "RECEIPT-CANARY", "DIAGRAM-CANARY", "GIT-CANARY", "ENV-UNREVIEWED-CANARY"} {
		if strings.Contains(markdown, canary) {
			t.Fatalf("resume leaked %q:\n%s", canary, markdown)
		}
	}
	if again := card.Markdown(); again != markdown {
		t.Fatal("resume Markdown is not deterministic")
	}
	if after := recoveryTree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("ledger-only resume mutated project files")
	}
}

func TestHistoryFollowsSupersedesAndGroupsOnlyUnresolvedEditableTags(t *testing.T) {
	root := recoveryFixture(t)
	writeRecoveryCanaries(t, root)
	before := recoveryTree(t, root)

	view, err := HistoryLedgerOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := timelineIDs(view.Timeline); !reflect.DeepEqual(got, []string{"event-a", "event-z"}) {
		t.Fatalf("timeline order=%v", got)
	}
	if got := decisionIDs(view.Decisions); !reflect.DeepEqual(got, []string{"decision-new", "decision-old"}) {
		t.Fatalf("decision order=%v", got)
	}
	if got := openLoopIDs(view.OpenLoops); !reflect.DeepEqual(got, []string{"loop-abandoned", "loop-blocked", "loop-open", "loop-resolved"}) {
		t.Fatalf("open-loop order=%v", got)
	}
	wantThemes := []Theme{
		{Name: " Durability ", DecisionIDs: []string{"decision-old"}, OpenLoopIDs: []string{}},
		{Name: " alpha ", DecisionIDs: []string{"decision-new"}, OpenLoopIDs: []string{}},
		{Name: " durability ", DecisionIDs: []string{}, OpenLoopIDs: []string{"loop-open"}},
		{Name: "ALPHA", DecisionIDs: []string{"decision-old"}, OpenLoopIDs: []string{}},
		{Name: "Alpha", DecisionIDs: []string{}, OpenLoopIDs: []string{"loop-blocked"}},
		{Name: "DURABILITY", DecisionIDs: []string{"decision-new"}, OpenLoopIDs: []string{}},
		{Name: "durability", DecisionIDs: []string{"decision-new"}, OpenLoopIDs: []string{}},
	}
	if !reflect.DeepEqual(view.Themes, wantThemes) {
		t.Fatalf("themes=%+v want=%+v", view.Themes, wantThemes)
	}
	markdown := view.Markdown()
	for _, required := range []string{"# Project history", "decision-new", "decision-old", "Supersedes", "loop-resolved", "ALPHA", "durability"} {
		if !strings.Contains(markdown, required) {
			t.Fatalf("history omitted %q:\n%s", required, markdown)
		}
	}
	for _, canary := range []string{"UNREVIEWED-CANARY", "EVIDENCE-CANARY", "RECEIPT-CANARY", "DIAGRAM-CANARY", "GIT-CANARY"} {
		if strings.Contains(markdown, canary) {
			t.Fatalf("history leaked %q:\n%s", canary, markdown)
		}
	}
	if again := view.Markdown(); again != markdown {
		t.Fatal("history Markdown is not deterministic")
	}
	if after := recoveryTree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("ledger-only history mutated project files")
	}
}

func TestHistoryRejectsMissingAndCyclicSupersedes(t *testing.T) {
	t.Run("missing predecessor", func(t *testing.T) {
		root := recoveryFixture(t)
		replaceRecoveryFile(t, root, "decisions/decision-new.md", "supersedes:\n  - decision-old", "supersedes:\n  - decision-missing")
		if _, err := HistoryLedgerOnly(root); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("cycle", func(t *testing.T) {
		root := recoveryFixture(t)
		replaceRecoveryFile(t, root, "decisions/decision-old.md", "supersedes: []", "supersedes:\n  - decision-new")
		if _, err := HistoryLedgerOnly(root); err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestRecoveryFailsClosedOnMalformedAcceptedState(t *testing.T) {
	root := recoveryFixture(t)
	replaceRecoveryFile(t, root, "decisions/decision-old.md", "tags:\n  - ' Durability '", "tags:\n  - '   '")
	if _, err := ResumeLedgerOnly(root); err == nil {
		t.Fatal("resume accepted malformed ledger")
	}
	if _, err := HistoryLedgerOnly(root); err == nil {
		t.Fatal("history accepted malformed ledger")
	}
}

func TestRecoveryHandlesEmptyAcceptedState(t *testing.T) {
	root := emptyRecoveryFixture(t)
	card, err := ResumeLedgerOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	if card.ProjectID != recoveryProjectID || card.Goal != "" || card.StopPoint != "" || len(card.Drift) != 0 || len(card.OpenQuestions) != 0 {
		t.Fatalf("card=%+v", card)
	}
	view, err := HistoryLedgerOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	if view.ProjectID != recoveryProjectID || len(view.Timeline) != 0 || len(view.Decisions) != 0 || len(view.OpenLoops) != 0 || len(view.Themes) != 0 {
		t.Fatalf("view=%+v", view)
	}
	if !strings.Contains(card.Markdown(), recoveryProjectID) || !strings.Contains(view.Markdown(), recoveryProjectID) {
		t.Fatal("empty views omitted project identity")
	}
}

func TestResumeRejectsMissingCyclicAndDisconnectedSessionChains(t *testing.T) {
	t.Run("missing next session", func(t *testing.T) {
		root := recoveryFixture(t)
		replaceRecoveryFile(t, root, "sessions/report-z.md", "next_session_id: a-latest", "next_session_id: missing-session")
		if _, err := ResumeLedgerOnly(root); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("cycle", func(t *testing.T) {
		root := recoveryFixture(t)
		replaceRecoveryFile(t, root, "sessions/report-z.md", "previous_session_id: \"\"", "previous_session_id: a-latest")
		replaceRecoveryFile(t, root, "sessions/report-a.md", "next_session_id: \"\"", "next_session_id: z-earlier")
		if _, err := ResumeLedgerOnly(root); err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("disconnected", func(t *testing.T) {
		root := recoveryFixture(t)
		replaceRecoveryFile(t, root, "sessions/report-z.md", "next_session_id: a-latest", "next_session_id: \"\"")
		replaceRecoveryFile(t, root, "sessions/report-a.md", "previous_session_id: z-earlier", "previous_session_id: \"\"")
		if _, err := ResumeLedgerOnly(root); err == nil || !strings.Contains(err.Error(), "disconnected") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestBuildThemesPreservesExactEditableTagBytesAndIgnoresBlank(t *testing.T) {
	decisions := []ledger.Decision{{ID: "decision-1", Tags: []string{" Durability ", "durability", "durability", "", "   "}}}
	loops := []ledger.OpenLoop{{ID: "loop-1", Status: "open", Tags: []string{"durability", "   "}}, {ID: "loop-2", Status: "resolved", Tags: []string{" Durability "}}}
	want := []Theme{
		{Name: " Durability ", DecisionIDs: []string{"decision-1"}, OpenLoopIDs: []string{}},
		{Name: "durability", DecisionIDs: []string{"decision-1"}, OpenLoopIDs: []string{"loop-1"}},
	}
	if got := buildThemes(decisions, loops); !reflect.DeepEqual(got, want) {
		t.Fatalf("themes=%+v want=%+v", got, want)
	}
}

func TestRecoveryMarkdownEscapesControlsAndIsBoundedWithoutMutation(t *testing.T) {
	card := ResumeCard{
		ProjectID:       "project-0123456789abcdef",
		Goal:            "goal\n# injected [link](bad)\x00",
		StopPoint:       "stop",
		Drift:           []string{"- fake list", "safe"},
		Blockers:        []string{},
		OpenQuestions:   []string{},
		NextAction:      "next",
		FirstInspection: "inspect",
		SourceSessions:  []string{"s1"},
	}
	original := cloneResumeCard(card)
	markdown := card.Markdown()
	if strings.Contains(markdown, "\x00") || strings.Contains(markdown, "\n# injected") || strings.Contains(markdown, "[link](bad)") || strings.Contains(markdown, "\n- - fake list") {
		t.Fatalf("unsafe resume Markdown:\n%s", markdown)
	}
	if !reflect.DeepEqual(card, original) {
		t.Fatal("ResumeCard.Markdown mutated its receiver")
	}

	view := HistoryView{ProjectID: recoveryProjectID, Timeline: []ledger.TimelineEvent{{ID: "event", OccurredAt: "2026-08-23T00:00:00Z", Class: ledger.Verified, Title: "title\n## injected", Summary: "summary\x01"}}}
	copyView := cloneHistoryView(view)
	history := view.Markdown()
	if strings.Contains(history, "\x01") || strings.Contains(history, "\n## injected") {
		t.Fatalf("unsafe history Markdown:\n%s", history)
	}
	if !reflect.DeepEqual(view, copyView) {
		t.Fatal("HistoryView.Markdown mutated its receiver")
	}

	huge := strings.Repeat("x", maxRecoveryMarkdownBytes+1024)
	card.Goal = huge
	if got := card.Markdown(); len(got) > maxRecoveryMarkdownBytes || !strings.Contains(got, "output omitted") {
		t.Fatalf("unbounded resume output: len=%d", len(got))
	}
	view.Decisions = []ledger.Decision{{ID: "decision", Title: huge}}
	if got := view.Markdown(); len(got) > maxRecoveryMarkdownBytes || !strings.Contains(got, "output omitted") {
		t.Fatalf("unbounded history output: len=%d", len(got))
	}
}

func recoveryFixture(t *testing.T) string {
	t.Helper()
	root := emptyRecoveryFixture(t)
	state, err := ledger.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	current := &ledger.CurrentState{
		ProjectID: recoveryProjectID, Revision: 1, Goal: "Ship ledger", LastVerified: "focused tests pass", Branch: "main",
		UncommittedChanges: []string{"work-z", "work-a"}, Blockers: []string{"block-z", "block-a"}, OpenRisks: []string{"risk-z", "risk-a"},
		NextAction: "run full tests", FirstInspection: "internal/recovery", LastUpdated: "2026-08-23T00:00:00Z", SourceSessions: []string{"a-latest", "z-earlier"}, Evidence: []ledger.EvidenceRef{},
	}
	decisions := []ledger.Decision{
		{ID: "decision-old", ProjectID: recoveryProjectID, Title: "Old choice", Status: "superseded", Revision: 1, Tags: []string{" Durability ", "ALPHA"}, Supersedes: []string{}, SourceSessions: []string{"z-earlier"}, Evidence: []ledger.EvidenceRef{}, Alternatives: []string{}, RejectedPaths: []string{}},
		{ID: "decision-new", ProjectID: recoveryProjectID, Title: "New choice", Status: "accepted", Revision: 1, Tags: []string{"durability", " alpha ", "DURABILITY"}, Supersedes: []string{"decision-old"}, SourceSessions: []string{"a-latest"}, Evidence: []ledger.EvidenceRef{}, Alternatives: []string{}, RejectedPaths: []string{}},
	}
	loops := []ledger.OpenLoop{
		{ID: "loop-open", ProjectID: recoveryProjectID, Title: "Open question", Status: "open", Revision: 1, Tags: []string{" durability "}, SourceSessions: []string{"a-latest"}, Evidence: []ledger.EvidenceRef{}, Attempts: []string{}},
		{ID: "loop-blocked", ProjectID: recoveryProjectID, Title: "Blocked question", Status: "blocked", Revision: 1, Tags: []string{"Alpha"}, SourceSessions: []string{"a-latest"}, Evidence: []ledger.EvidenceRef{}, Attempts: []string{}},
		{ID: "loop-resolved", ProjectID: recoveryProjectID, Title: "Resolved question", Status: "resolved", Revision: 1, Tags: []string{"durability"}, SourceSessions: []string{"z-earlier"}, Evidence: []ledger.EvidenceRef{}, Attempts: []string{}},
		{ID: "loop-abandoned", ProjectID: recoveryProjectID, Title: "Abandoned question", Status: "abandoned", Revision: 1, Tags: []string{"alpha"}, SourceSessions: []string{"z-earlier"}, Evidence: []ledger.EvidenceRef{}, Attempts: []string{}},
	}
	timeline := []ledger.TimelineEvent{
		{ID: "event-z", OccurredAt: "2026-08-23T02:00:00Z", Revision: 1, Class: ledger.Verified, Title: "Later", Summary: "Later accepted event", Evidence: []ledger.EvidenceRef{}, DecisionIDs: []string{"decision-new"}, OpenLoopIDs: []string{"loop-open"}},
		{ID: "event-a", OccurredAt: "2026-08-23T01:00:00Z", Revision: 1, Class: ledger.DecisionFact, Title: "Earlier", Summary: "Earlier accepted event", Evidence: []ledger.EvidenceRef{}, DecisionIDs: []string{"decision-old"}, OpenLoopIDs: []string{"loop-blocked"}},
	}
	earlier := recoverySession("report-z", "z-earlier", []ledger.SessionPhase{{Title: "First", Summary: "Earlier stop", Evidence: []ledger.EvidenceRef{}}})
	earlier.NextSessionID = "a-latest"
	latest := recoverySession("report-a", "a-latest", []ledger.SessionPhase{{Title: "Start", Summary: "Started", Evidence: []ledger.EvidenceRef{}}, {Title: "Final", Summary: "Latest accepted stop", Evidence: []ledger.EvidenceRef{}}})
	latest.PreviousSessionID = "z-earlier"
	sessions := []ledger.SessionReport{earlier, latest}
	plan, err := ledger.Render(state, ledger.ChangeSet{Current: current, Timeline: timeline, Decisions: decisions, OpenLoops: loops, Sessions: sessions})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(plan); err != nil {
		t.Fatal(err)
	}
	return root
}

func emptyRecoveryFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "docs", "session-review", "project-overview.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("---\nproject_id: " + recoveryProjectID + "\n---\n\n# Recovery fixture\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func recoverySession(id, sessionID string, phases []ledger.SessionPhase) ledger.SessionReport {
	return ledger.SessionReport{
		ID: id, ProjectID: recoveryProjectID, SessionID: sessionID, Revision: 1, InitialGoal: "Ship ledger",
		GoalChanges: []string{}, Phases: phases, Files: []string{}, Commits: []string{}, Verification: []string{},
		DecisionsAdded: []string{}, DecisionsRevised: []string{}, OpenLoopsCreated: []string{}, OpenLoopsClosed: []string{}, Evidence: []ledger.EvidenceRef{},
	}
}

func writeRecoveryCanaries(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"pending/session.jsonl":                                        "UNREVIEWED-CANARY",
		"evidence/evidence-v2.json":                                    "EVIDENCE-CANARY",
		".session-reviewer/applied-proposals/receipt.json":             "RECEIPT-CANARY",
		"docs/session-review/diagrams/project-evolution.md":            "DIAGRAM-CANARY",
		".git/session-reviewer-ledger-only-must-not-inspect-this-file": "GIT-CANARY",
	}
	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func recoveryTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = body
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func replaceRecoveryFile(t *testing.T, root, relative, old, next string) {
	t.Helper()
	path := filepath.Join(root, "docs", "session-review", filepath.FromSlash(relative))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(old)) {
		t.Fatalf("%s does not contain %q:\n%s", relative, old, body)
	}
	body = bytes.Replace(body, []byte(old), []byte(next), 1)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func timelineIDs(items []ledger.TimelineEvent) []string {
	ids := make([]string, len(items))
	for index, item := range items {
		ids[index] = item.ID
	}
	return ids
}

func decisionIDs(items []ledger.Decision) []string {
	ids := make([]string, len(items))
	for index, item := range items {
		ids[index] = item.ID
	}
	return ids
}

func openLoopIDs(items []ledger.OpenLoop) []string {
	ids := make([]string, len(items))
	for index, item := range items {
		ids[index] = item.ID
	}
	return ids
}

func cloneResumeCard(card ResumeCard) ResumeCard {
	card.Drift = cloneTestStrings(card.Drift)
	card.Blockers = cloneTestStrings(card.Blockers)
	card.OpenQuestions = cloneTestStrings(card.OpenQuestions)
	card.SourceSessions = cloneTestStrings(card.SourceSessions)
	return card
}

func cloneTestStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

func cloneHistoryView(view HistoryView) HistoryView {
	view.Timeline = append([]ledger.TimelineEvent(nil), view.Timeline...)
	view.Decisions = append([]ledger.Decision(nil), view.Decisions...)
	view.OpenLoops = append([]ledger.OpenLoop(nil), view.OpenLoops...)
	view.Themes = append([]Theme(nil), view.Themes...)
	return view
}

func TestHistoryRepeatedCallsAreByteStable(t *testing.T) {
	root := recoveryFixture(t)
	var outputs []string
	for range 20 {
		view, err := HistoryLedgerOnly(root)
		if err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, view.Markdown())
	}
	if !sort.StringsAreSorted(outputs) || outputs[0] != outputs[len(outputs)-1] {
		t.Fatal("history output varied across calls")
	}
}
