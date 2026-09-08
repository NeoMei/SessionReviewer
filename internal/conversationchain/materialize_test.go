package conversationchain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestVisibleUserTextPreservesRequestAndArbitraryMarkup(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"<environment_context>runtime</environment_context>", ""},
		{"<recommended_plugins>catalog</recommended_plugins>\n<environment_context>runtime</environment_context>\nimplement this", "implement this"},
		{"# Context from my IDE setup:\nopen files\n## My request:\nfix this", "fix this"},
		{"<in-app-browser-context source=\"ambient-ui-state\">browser</in-app-browser-context>\n## My request:\nlook at this", "look at this"},
		{"<recommended_plugins>catalog</recommended_plugins>\n# AGENTS.md instructions\n\n<INSTRUCTIONS>host policy</INSTRUCTIONS><environment_context>runtime</environment_context>", ""},
		{"# AGENTS.md instructions\n\n<INSTRUCTIONS>host policy</INSTRUCTIONS><environment_context>runtime</environment_context>\nimplement this", "implement this"},
		{"Discuss # AGENTS.md instructions <INSTRUCTIONS>example</INSTRUCTIONS>", "Discuss # AGENTS.md instructions <INSTRUCTIONS>example</INSTRUCTIONS>"},
		{"Explain <root>/my/path</root>", "Explain <root>/my/path</root>"},
		{"```xml\n<environment_context>example</environment_context>\n```", "```xml\n<environment_context>example</environment_context>\n```"},
	} {
		if got := VisibleUserText(test.input); got != test.want {
			t.Fatalf("input=%q got=%q want=%q", test.input, got, test.want)
		}
	}
}

func TestMaterializeVisibleBoundsExpandedRedactedBody(t *testing.T) {
	messages := []SourceMessage{{Role: RoleUser, Text: "question", RecordOrdinal: 1, RecordHash: strings.Repeat("a", 64)}, {Role: RoleAssistant, Phase: "final_answer", Text: strings.Repeat("界", 30000), RecordOrdinal: 2, RecordHash: strings.Repeat("b", 64)}}
	turns, _ := MaterializeVisible("codex", "s", "source-s", messages)
	body := turns[0].Messages[1].Text
	if body == nil || len(*body) > 64<<10 || !utf8.ValidString(*body) {
		t.Fatalf("body bytes=%d", len(*body))
	}
}

func TestMaterializeVisibleIDsRemainStableAfterAppendAndBindRevisionHash(t *testing.T) {
	user := SourceMessage{Role: RoleUser, Text: "q", RecordOrdinal: 7, RecordHash: strings.Repeat("a", 64)}
	first, _ := MaterializeVisible("codex", "s", "identity", []SourceMessage{user})
	next, _ := MaterializeVisible("codex", "s", "identity", []SourceMessage{user, {Role: RoleAssistant, Text: "a", RecordOrdinal: 9, RecordHash: strings.Repeat("b", 64)}})
	if first[0].TurnUnitID != next[0].TurnUnitID || first[0].UserMessage.RevisionID != next[0].UserMessage.RevisionID {
		t.Fatal("append changed stable IDs")
	}
	user.RecordHash = strings.Repeat("c", 64)
	changed, _ := MaterializeVisible("codex", "s", "identity", []SourceMessage{user})
	if first[0].UserMessage.RevisionID == changed[0].UserMessage.RevisionID {
		t.Fatal("source revision hash ignored")
	}
}

func TestMaterializeVisibleClassifiesAnswersConservatively(t *testing.T) {
	tests := []struct {
		name     string
		messages []SourceMessage
		want     AnswerState
	}{
		{"normal final", []SourceMessage{{Role: RoleUser, Text: "q", OccurredAt: testTime1, RecordOrdinal: 1, RecordHash: strings.Repeat("a", 64)}, {Role: RoleAssistant, Phase: "final_answer", Text: "done", OccurredAt: testTime2, RecordOrdinal: 2, RecordHash: strings.Repeat("b", 64)}}, AnswerAnswered},
		{"commentary only", []SourceMessage{{Role: RoleUser, Text: "q", OccurredAt: testTime1, RecordOrdinal: 1, RecordHash: strings.Repeat("a", 64)}, {Role: RoleAssistant, Phase: "commentary", Text: "working", OccurredAt: testTime2, RecordOrdinal: 2, RecordHash: strings.Repeat("b", 64)}}, AnswerPartial},
		{"empty final", []SourceMessage{{Role: RoleUser, Text: "q", OccurredAt: testTime1, RecordOrdinal: 1, RecordHash: strings.Repeat("a", 64)}, {Role: RoleAssistant, Phase: "final_answer", Text: "", OccurredAt: testTime2, RecordOrdinal: 2, RecordHash: strings.Repeat("b", 64)}}, AnswerPartial},
		{"final then interrupted commentary", []SourceMessage{{Role: RoleUser, Text: "q", OccurredAt: testTime1, RecordOrdinal: 1, RecordHash: strings.Repeat("a", 64)}, {Role: RoleAssistant, Phase: "final_answer", Text: "done", OccurredAt: testTime2, RecordOrdinal: 2, RecordHash: strings.Repeat("b", 64)}, {Role: RoleAssistant, Phase: "commentary", Text: "more", OccurredAt: testTime3, RecordOrdinal: 3, RecordHash: strings.Repeat("c", 64)}}, AnswerPartial},
		{"commentary then unphased", []SourceMessage{{Role: RoleUser, Text: "q", OccurredAt: testTime1, RecordOrdinal: 1, RecordHash: strings.Repeat("a", 64)}, {Role: RoleAssistant, Phase: "commentary", Text: "working", OccurredAt: testTime2, RecordOrdinal: 2, RecordHash: strings.Repeat("b", 64)}, {Role: RoleAssistant, Text: "interrupted", OccurredAt: testTime3, RecordOrdinal: 3, RecordHash: strings.Repeat("c", 64)}}, AnswerPartial},
		{"final commentary then unphased", []SourceMessage{{Role: RoleUser, Text: "q", OccurredAt: testTime1, RecordOrdinal: 1, RecordHash: strings.Repeat("a", 64)}, {Role: RoleAssistant, Phase: "final_answer", Text: "done", OccurredAt: testTime2, RecordOrdinal: 2, RecordHash: strings.Repeat("b", 64)}, {Role: RoleAssistant, Phase: "commentary", Text: "more", OccurredAt: testTime3, RecordOrdinal: 3, RecordHash: strings.Repeat("c", 64)}, {Role: RoleAssistant, Text: "interrupted", OccurredAt: testTime3, RecordOrdinal: 4, RecordHash: strings.Repeat("d", 64)}}, AnswerPartial},
		{"unanswered", []SourceMessage{{Role: RoleUser, Text: "q", OccurredAt: testTime1, RecordOrdinal: 1, RecordHash: strings.Repeat("a", 64)}}, AnswerNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			turns, _ := MaterializeVisible("codex", "session-1", "source-1", test.messages)
			if len(turns) != 1 || turns[0].AnswerState != test.want {
				t.Fatalf("answer state=%+v want %s", turns, test.want)
			}
		})
	}
}

func TestApplyVisibleCoverageDowngradesAnsweredTurnWhenSourceHasGaps(t *testing.T) {
	messages := []SourceMessage{
		{Role: RoleUser, Text: "q", OccurredAt: testTime1, RecordOrdinal: 1, RecordHash: strings.Repeat("a", 64)},
		{Role: RoleAssistant, Phase: "final_answer", Text: "done", OccurredAt: testTime2, RecordOrdinal: 2, RecordHash: strings.Repeat("b", 64)},
	}
	turns, _ := MaterializeVisible("codex", "session-1", "source-1", messages)
	if turns[0].AnswerState != AnswerAnswered {
		t.Fatalf("complete source control=%+v", turns)
	}
	ApplyVisibleCoverage(turns, VisibleCoverage{SourceRecords: 3, VisibleMessages: 2, CapturedMessages: 2, MalformedRecords: 1, Complete: false})
	if turns[0].AnswerState != AnswerPartial {
		t.Fatalf("source gap erased by later coverage assignment: %+v", turns)
	}
}
