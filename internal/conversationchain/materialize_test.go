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
		{"<subagent_notification>\n{\"agent_path\":\"worker-1\",\"status\":{\"completed\":\"done\"}}\n</subagent_notification>", ""},
		{"<subagent_notification>{\"agent_path\":\"worker-1\",\"status\":\"shutdown\"}</subagent_notification>", ""},
		{"<subagent_notification>\n{\"agent_path\":\"worker-1\",\"status\":{\"completed\":\"done\"}}\n</subagent_notification>\nWhat should I do next?", "What should I do next?"},
		{"<subagent_notification>{\"agent_path\":\"example\",\"status\":true}</subagent_notification>", "<subagent_notification>{\"agent_path\":\"example\",\"status\":true}</subagent_notification>"},
		{"<subagent_notification>{\"agent_path\":\"example\",\"status\":{},\"note\":\"quoted example\"}</subagent_notification>", "<subagent_notification>{\"agent_path\":\"example\",\"status\":{},\"note\":\"quoted example\"}</subagent_notification>"},
		{"> <subagent_notification>{\"agent_path\":\"example\"}</subagent_notification>", "> <subagent_notification>{\"agent_path\":\"example\"}</subagent_notification>"},
		{"```xml\n<subagent_notification>{\"agent_path\":\"example\"}</subagent_notification>\n```", "```xml\n<subagent_notification>{\"agent_path\":\"example\"}</subagent_notification>\n```"},
	} {
		if got := VisibleUserText(test.input); got != test.want {
			t.Fatalf("input=%q got=%q want=%q", test.input, got, test.want)
		}
	}
}

func TestMaterializeVisibleIgnoresOrchestrationNotificationWithoutSplittingQuestionAnswer(t *testing.T) {
	question := SourceMessage{Role: RoleUser, Text: "How do we finish F1?", OccurredAt: testTime1, RecordOrdinal: 7, RecordHash: strings.Repeat("a", 64)}
	working := SourceMessage{Role: RoleAssistant, Phase: "commentary", Text: "Checking.", OccurredAt: testTime2, RecordOrdinal: 8, RecordHash: strings.Repeat("b", 64)}
	notification := SourceMessage{Role: RoleUser, Text: "<subagent_notification>\n{\"agent_path\":\"worker-1\",\"status\":{\"completed\":\"done\"}}\n</subagent_notification>", OccurredAt: testTime2, RecordOrdinal: 9, RecordHash: strings.Repeat("c", 64)}
	answer := SourceMessage{Role: RoleAssistant, Phase: "final_answer", Text: "F1 is complete.", OccurredAt: testTime3, RecordOrdinal: 10, RecordHash: strings.Repeat("d", 64)}

	turns, coverage := MaterializeVisible("codex", "session-1", "source-1", []SourceMessage{question, working, notification, answer})
	control, _ := MaterializeVisible("codex", "session-1", "source-1", []SourceMessage{question, working, answer})
	if len(turns) != 1 || len(control) != 1 || turns[0].AnswerState != AnswerAnswered || turns[0].AssistantMessageCount != 2 {
		t.Fatalf("notification split real question/answer: turns=%+v", turns)
	}
	if coverage.VisibleMessages != 4 || coverage.CapturedMessages != 3 || coverage.ContextMessages != 1 {
		t.Fatalf("notification coverage=%+v", coverage)
	}
	if turns[0].TurnUnitID != control[0].TurnUnitID || turns[0].UserMessage.RevisionID != control[0].UserMessage.RevisionID || turns[0].UserMessage.SourceRef != control[0].UserMessage.SourceRef {
		t.Fatalf("notification changed authenticated question identity: got=%+v control=%+v", turns[0].UserMessage, control[0].UserMessage)
	}
	for index := range turns[0].Messages[1:] {
		if turns[0].Messages[index+1].RevisionID != control[0].Messages[index+1].RevisionID || turns[0].Messages[index+1].SourceRef != control[0].Messages[index+1].SourceRef {
			t.Fatalf("notification changed assistant identity at %d", index)
		}
	}
}

func TestMaterializeVisibleKeepsRealSuffixOnNotificationRecord(t *testing.T) {
	wrapped := SourceMessage{Role: RoleUser, Text: "<subagent_notification>{\"agent_path\":\"worker-1\",\"status\":{\"completed\":\"done\"}}</subagent_notification>\nWhat changed?", OccurredAt: testTime1, RecordOrdinal: 11, RecordHash: strings.Repeat("e", 64)}
	plain := wrapped
	plain.Text = "What changed?"

	turns, _ := MaterializeVisible("codex", "session-1", "source-1", []SourceMessage{wrapped})
	control, _ := MaterializeVisible("codex", "session-1", "source-1", []SourceMessage{plain})
	if len(turns) != 1 || turns[0].UserMessage.VisibleExcerpt != "What changed?" || turns[0].UserMessage.SourceRef.RecordOrdinal != 11 || turns[0].UserMessage.SourceRef.SourceHash != strings.Repeat("e", 64) {
		t.Fatalf("mixed notification/request=%+v", turns)
	}
	if turns[0].TurnUnitID != control[0].TurnUnitID || turns[0].UserMessage.RevisionID != control[0].UserMessage.RevisionID {
		t.Fatal("stripping notification prefix changed source-derived IDs")
	}
}

func TestMaterializeVisibleVersionPreservesHistoricalV1NotificationTurn(t *testing.T) {
	messages := []SourceMessage{
		{Role: RoleUser, Text: "How do we finish F1?", OccurredAt: testTime1, RecordOrdinal: 7, RecordHash: strings.Repeat("a", 64)},
		{Role: RoleUser, Text: "<subagent_notification>{\"agent_path\":\"worker-1\",\"status\":{\"completed\":\"done\"}}</subagent_notification>", OccurredAt: testTime2, RecordOrdinal: 9, RecordHash: strings.Repeat("c", 64)},
		{Role: RoleAssistant, Phase: "final_answer", Text: "F1 is complete.", OccurredAt: testTime3, RecordOrdinal: 10, RecordHash: strings.Repeat("d", 64)},
	}
	legacy, _, err := MaterializeVisibleVersion("codex", "session-1", "source-1", LegacySegmentationRuleVersion, messages)
	if err != nil || len(legacy) != 2 || legacy[1].UserMessage.SourceRef.RecordOrdinal != 9 || legacy[1].AnswerState != AnswerAnswered {
		t.Fatalf("historical v1 interpretation changed: turns=%+v err=%v", legacy, err)
	}
	current, _, err := MaterializeVisibleVersion("codex", "session-1", "source-1", CurrentSegmentationRuleVersion, messages)
	if err != nil || len(current) != 1 || current[0].UserMessage.SourceRef.RecordOrdinal != 7 || current[0].AnswerState != AnswerAnswered {
		t.Fatalf("current v2 interpretation=%+v err=%v", current, err)
	}
	if _, _, err := MaterializeVisibleVersion("codex", "session-1", "source-1", "visible-turn-v3", messages); err == nil {
		t.Fatal("unsupported historical segmentation rule was reinterpreted")
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
